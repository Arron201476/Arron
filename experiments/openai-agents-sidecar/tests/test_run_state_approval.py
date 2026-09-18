from __future__ import annotations

import asyncio
import hashlib
import json
from typing import Any

import pytest

from agents import Agent, Runner, RunState, function_tool
from agents.items import ModelResponse
from agents.models.interface import Model
from agents.usage import Usage
from openai.types.responses import (
    ResponseFunctionToolCall,
    ResponseOutputMessage,
    ResponseOutputText,
)

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.contracts import (
    AgentExecutionRequest,
    AgentToolApprovalDecision,
)
from content_agent_sidecar.observability import TurnObservation
from content_agent_sidecar.runtime import (
    AgentContext,
    _apply_approval_decisions,
    _restore_agent_context,
    _serialize_paused_run,
    _serialize_agent_context,
    RuntimeCompatibilityError,
)


def test_request_text_source_policy_survives_checkpoint_and_legacy_defaults():
    backend = DurableApprovalBackend()
    request = AgentExecutionRequest(project_id="p", conversation_id="c", agent_turn_id="t", idempotency_key="i", request={"content": "Use this supplied text"})
    context = AgentContext("p", "c", backend, raw_request=request.request, agent_turn_id="t", idempotency_key="i", source_resolution_attempted=True, source_resolution_requires_binding=False, observation=TurnObservation("provider", "model", "release"))
    payload = _serialize_agent_context(context)
    state = {"context": {"context": payload}}
    restored = _restore_agent_context(state, request, backend, TurnObservation("provider", "model", "release"))
    assert restored.source_resolution_attempted
    assert not restored.source_resolution_requires_binding
    payload.pop("source_resolution_requires_binding")
    legacy = _restore_agent_context(state, request, backend, TurnObservation("provider", "model", "release"))
    assert legacy.source_resolution_requires_binding
    for key in ("main_execution_phase", "main_repair_attempt", "main_repair_candidate", "main_unstarted_input"):
        payload.pop(key)
    legacy = _restore_agent_context(state, request, backend, TurnObservation("provider", "model", "release"))
    assert legacy.main_execution_phase == "execution" and legacy.main_repair_attempt == 0
    assert legacy.main_repair_candidate == "" and legacy.main_unstarted_input is None


@pytest.mark.parametrize("field", ["agent_task_id", "agent_task_attempt_id", "execution_attempt_id"])
def test_main_turn_restore_rejects_mixed_execution_identity(field):
    request = AgentExecutionRequest(project_id="p", conversation_id="c", agent_turn_id="t", idempotency_key="i", request={"content": "fixture"})
    backend = DurableApprovalBackend()
    context = AgentContext("p", "c", backend, raw_request=request.request, agent_turn_id="t", idempotency_key="i")
    payload = _serialize_agent_context(context)
    payload[field] = "another-execution"
    with pytest.raises(RuntimeCompatibilityError, match="identity"):
        _restore_agent_context({"context": {"context": payload}}, request, backend, TurnObservation("provider", "model", "release"))


@pytest.mark.parametrize("decision", ["approve", "reject"])
def test_native_sdk_stateful_approval_preserves_identity_without_serializing_token(decision):
    writes = []
    begins = []

    class StatefulBackend(DurableApprovalBackend):
        async def begin_agent_tool_call(self, **payload):
            begins.append(payload)
            assert payload["execution_attempt_id"] == "execution-1"
            assert not payload["agent_turn_id"] and not payload["agent_task_attempt_id"]
            return await super().begin_agent_tool_call(**payload)

    @function_tool
    async def write_value(value: str) -> str:
        """Write one value."""
        writes.append(value)
        return "written"

    async def run():
        backend = StatefulBackend()
        provider = AgentToolProvider(backend, [write_value])
        context = AgentContext("project-1", "conversation-1", backend, execution_attempt_id="execution-1", attempt_token="old-secret-token")
        model = SequenceModel([_tool_call_response(), _text_response()])
        prepared = await provider.prepare(context, [], set())
        async with prepared:
            agent = Agent(name="Stateful approval", model=model, tools=prepared.tools)
            pending = await Runner.run(agent, "write", context=context, max_turns=3)
            state_json, _, pending_ids = _serialize_paused_run(pending)
        assert pending_ids == ["sdk-write-1"] and writes == []
        assert state_json["context"]["context"]["execution_attempt_id"] == "execution-1"
        assert "old-secret-token" not in json.dumps(state_json)
        backend.calls["sdk-write-1"]["status"] = "approved" if decision == "approve" else "rejected"
        restored_context = AgentContext("project-1", "conversation-1", backend, execution_attempt_id="execution-1", attempt_token="rotated-secret-token")
        restored_prepared = await provider.prepare(restored_context, [], set())
        async with restored_prepared:
            restored_agent = Agent(name="Stateful approval", model=model, tools=restored_prepared.tools)
            restored = await RunState.from_json(restored_agent, state_json, context_override=restored_context)
            _apply_approval_decisions(restored, [AgentToolApprovalDecision(sdk_tool_call_id="sdk-write-1", action=decision)])
            result = await Runner.run(restored_agent, restored, context=restored_context, max_turns=3)
        assert result.final_output == "continued after the decision" and not result.interruptions
        assert writes == (["approved payload"] if decision == "approve" else [])
        assert backend.started == backend.completed == (1 if decision == "approve" else 0)
        assert begins[0]["attempt_token"] == "old-secret-token"
        if decision == "approve":
            assert begins[-1]["attempt_token"] == "rotated-secret-token"
        assert not model.responses

    asyncio.run(run())


class SequenceModel(Model):
    def __init__(self, responses: list[ModelResponse]) -> None:
        self.responses = responses

    async def get_response(self, *_args: Any, **_kwargs: Any) -> ModelResponse:
        if not self.responses:
            raise AssertionError("model received an unexpected extra request")
        return self.responses.pop(0)

    def stream_response(self, *_args: Any, **_kwargs: Any) -> Any:
        raise AssertionError("streaming is not used by this fixture")

    async def close(self) -> None:
        return None


from instruction_fixtures import EmptyInstructionsBackend


class DurableApprovalBackend(EmptyInstructionsBackend):
    def __init__(self) -> None:
        self.calls: dict[str, dict[str, Any]] = {}
        self.started = 0
        self.completed = 0

    async def get_agent_tool_catalog(self) -> dict[str, Any]:
        return {
            "schema_version": "1.0.0",
            "tools": [
                {
                    "id": "runtime:write_value",
                    "kind": "runtime_function",
                    "name": "write_value",
                    "description": "Write one value",
                    "access": "write",
                    "approval": "always",
                    "enabled": True,
                    "timeout_seconds": 5,
                    "max_retries": 0,
                    "max_result_bytes": 4096,
                }
            ],
            "mcp_servers": [],
            "hosted_tools": [],
        }

    async def begin_agent_tool_call(self, **payload: Any) -> dict[str, Any]:
        sdk_id = payload["sdk_tool_call_id"]
        if sdk_id not in self.calls:
            self.calls[sdk_id] = {
                "agent_tool_call_id": "tcall-durable",
                "sdk_tool_call_id": sdk_id,
                "tool_id": payload["tool_id"],
                "arguments_hash": hashlib.sha256(json.dumps(payload["arguments"], sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode()).hexdigest(),
                "status": "pending_approval",
            }
        return dict(self.calls[sdk_id])

    async def start_agent_tool_call(
        self, agent_tool_call_id: str, sdk_tool_call_id: str
    ) -> dict[str, Any]:
        call = self.calls[sdk_tool_call_id]
        if call["status"] != "approved":
            raise AssertionError("tool started without durable approval")
        call["status"] = "running"
        self.started += 1
        return dict(call)

    async def complete_agent_tool_call(
        self, agent_tool_call_id: str, **_payload: Any
    ) -> dict[str, Any]:
        self.completed += 1
        return {"agent_tool_call_id": agent_tool_call_id, "status": "completed"}

    async def fail_agent_tool_call(self, *_args: Any, **_kwargs: Any) -> dict[str, Any]:
        raise AssertionError("tool unexpectedly failed")

    async def cancel_agent_tool_call(self, *_args: Any, **_kwargs: Any) -> dict[str, Any]:
        raise AssertionError("tool unexpectedly cancelled")


def _tool_call_response() -> ModelResponse:
    return ModelResponse(
        output=[
            ResponseFunctionToolCall(
                id=None,
                call_id="sdk-write-1",
                arguments=json.dumps({"value": "approved payload"}),
                name="write_value",
                type="function_call",
                status="completed",
            )
        ],
        usage=Usage(requests=1),
        response_id="resp-tool",
    )


def _text_response() -> ModelResponse:
    return ModelResponse(
        output=[
            ResponseOutputMessage(
                id="msg-final",
                content=[
                    ResponseOutputText(
                        annotations=[],
                        logprobs=[],
                        text="continued after the decision",
                        type="output_text",
                    )
                ],
                role="assistant",
                status="completed",
                type="message",
            )
        ],
        usage=Usage(requests=1),
        response_id="resp-final",
    )


def test_sdk_run_state_round_trip_approves_exact_call_once() -> None:
    backend = DurableApprovalBackend()
    executions: list[str] = []

    @function_tool
    async def write_value(value: str) -> str:
        """Write one value."""
        executions.append(value)
        return "written"

    async def run() -> None:
        provider = AgentToolProvider(backend, [write_value])
        prepared = await provider.prepare(
            AgentContext(
                project_id="project-1",
                conversation_id="conversation-1",
                backend=backend,  # type: ignore[arg-type]
                raw_request={"content": "write it"},
                idempotency_key="idem-1",
                agent_turn_id="turn-1",
            ),
            [],
            set(),
        )
        model = SequenceModel([_tool_call_response(), _text_response()])
        async with prepared:
            agent = Agent(
                name="Durable approval Agent",
                model=model,
                tools=prepared.tools,
            )
            initial_observation = TurnObservation("provider-before", "model-before", "release-before")
            initial_observation.trace_refs.append("trace-before")
            initial_observation.response_ids.append("resp-before")
            initial_observation.request_ids.append("req-before")
            initial_observation.usage["requests"] = 1
            initial_observation.usage["total_tokens"] = 17
            initial_observation.latency_ms["skill_routing"] = 9
            initial_context = AgentContext(
                project_id="project-1",
                conversation_id="conversation-1",
                backend=backend,  # type: ignore[arg-type]
                raw_request={"content": "write it"},
                idempotency_key="idem-1",
                agent_turn_id="turn-1",
                observation=initial_observation,
            )
            pending = await Runner.run(
                agent, "write it", context=initial_context, max_turns=3
            )
            assert len(pending.interruptions) == 1
            assert executions == []
            initial_observation.ingest_result(pending)
            state_json, schema, pending_ids = _serialize_paused_run(pending)
            assert schema and pending_ids == ["sdk-write-1"]

        backend.calls["sdk-write-1"]["status"] = "approved"
        request = AgentExecutionRequest(
            project_id="project-1",
            conversation_id="conversation-1",
            request={"content": "write it"},
            idempotency_key="idem-1",
            agent_turn_id="turn-1",
            run_state=state_json,
            approval_decisions=[
                AgentToolApprovalDecision(
                    sdk_tool_call_id="sdk-write-1", action="approve"
                )
            ],
        )
        resumed_observation = TurnObservation("provider-after", "model-after", "release-after")
        restored_context = _restore_agent_context(
            state_json,
            request,
            backend,  # type: ignore[arg-type]
            resumed_observation,
        )
        restored_observation = restored_context.observation
        assert restored_observation is resumed_observation
        assert restored_observation.provider_id == "provider-before"
        assert restored_observation.model_id == "model-before"
        assert restored_observation.release_id == "release-before"
        assert restored_observation.trace_refs == ["trace-before"]
        assert restored_observation.response_ids == ["resp-before", "resp-tool"]
        assert restored_observation.request_ids == ["req-before"]
        assert restored_observation.usage["total_tokens"] == 17
        assert restored_observation.usage["requests"] == 2
        assert restored_observation.latency_ms["skill_routing"] == 9
        assert restored_observation.public()["latency_ms"]["total"] >= 0
        resumed_tools = await provider.prepare(restored_context, [], set())
        async with resumed_tools:
            resumed_agent = Agent(
                name="Durable approval Agent",
                model=model,
                tools=resumed_tools.tools,
            )
            state = await RunState.from_json(
                resumed_agent,
                state_json,
                context_override=restored_context,
                strict_context=True,
            )
            _apply_approval_decisions(state, request.approval_decisions)
            result = await Runner.run(resumed_agent, state, max_turns=3)
            restored_observation.ingest_result(result)

        assert result.final_output == "continued after the decision"
        assert restored_observation.response_ids == [
            "resp-before",
            "resp-tool",
            "resp-final",
        ]
        assert restored_observation.usage["requests"] == 3
        assert restored_observation.usage["total_tokens"] == 17
        assert executions == ["approved payload"]
        assert backend.started == 1
        assert backend.completed == 1

    asyncio.run(run())


def test_sdk_run_state_round_trip_rejects_without_executing_tool() -> None:
    backend = DurableApprovalBackend()
    executions: list[str] = []

    @function_tool
    async def write_value(value: str) -> str:
        """Write one value."""
        executions.append(value)
        return "written"

    async def run() -> None:
        provider = AgentToolProvider(backend, [write_value])
        initial_context = AgentContext(
            project_id="project-1",
            conversation_id="conversation-1",
            backend=backend,  # type: ignore[arg-type]
            raw_request={"content": "do not write it"},
            idempotency_key="idem-reject",
            agent_turn_id="turn-reject",
        )
        prepared = await provider.prepare(initial_context, [], set())
        model = SequenceModel([_tool_call_response(), _text_response()])
        async with prepared:
            initial_agent = Agent(
                name="Durable rejection Agent", model=model, tools=prepared.tools
            )
            pending = await Runner.run(
                initial_agent, "do not write it", context=initial_context, max_turns=3
            )
            assert len(pending.interruptions) == 1
            state_json, _, pending_ids = _serialize_paused_run(pending)
            assert pending_ids == ["sdk-write-1"]

        backend.calls["sdk-write-1"]["status"] = "rejected"
        request = AgentExecutionRequest(
            project_id="project-1",
            conversation_id="conversation-1",
            request={"content": "do not write it"},
            idempotency_key="idem-reject",
            agent_turn_id="turn-reject",
            run_state=state_json,
            approval_decisions=[
                AgentToolApprovalDecision(
                    sdk_tool_call_id="sdk-write-1", action="reject"
                )
            ],
        )
        restored_context = _restore_agent_context(
            state_json,
            request,
            backend,  # type: ignore[arg-type]
            TurnObservation("test", "test"),
        )
        resumed_tools = await provider.prepare(restored_context, [], set())
        async with resumed_tools:
            resumed_agent = Agent(
                name="Durable rejection Agent", model=model, tools=resumed_tools.tools
            )
            state = await RunState.from_json(
                resumed_agent,
                state_json,
                context_override=restored_context,
                strict_context=True,
            )
            _apply_approval_decisions(state, request.approval_decisions)
            result = await Runner.run(resumed_agent, state, max_turns=3)

        assert result.final_output == "continued after the decision"
        assert executions == []
        assert backend.started == 0
        assert backend.completed == 0

    asyncio.run(run())
