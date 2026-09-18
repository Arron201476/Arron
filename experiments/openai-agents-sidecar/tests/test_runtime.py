from __future__ import annotations

import asyncio
from types import SimpleNamespace

import pytest
from agents.run_context import RunContextWrapper
from agents.tool_context import ToolContext

from content_agent_sidecar.runtime import (
    AgentContext,
    AgentExecutionOutcome,
    AgentExecutionPaused,
    AgentExecutionStream,
    RuntimeCompatibilityError,
    execute_skill_script,
    normalize_execution_stream_event,
    normalize_stream_event,
)
from content_agent_sidecar.observability import TurnObservation
from content_agent_sidecar.agent_tools import AgentToolConfigurationError


class ScriptBackend:
    def __init__(self) -> None:
        self.calls: list[tuple[str, str, dict[str, str]]] = []

    async def execute_skill_script(
        self, agent_tool_call_id: str, sdk_tool_call_id: str, arguments: dict[str, str]
    ) -> dict[str, str]:
        self.calls.append((agent_tool_call_id, sdk_tool_call_id, arguments))
        return {"status": "completed"}


def test_skill_script_tool_uses_strict_string_schema_and_turn_allowlist() -> None:
    properties = execute_skill_script.params_json_schema["properties"]
    assert properties["input_json"]["type"] == "string"
    assert execute_skill_script.params_json_schema["additionalProperties"] is False

    backend = ScriptBackend()
    context = AgentContext("project-1", "conversation-1", backend)  # type: ignore[arg-type]
    context.allowed_skill_scripts = {("selected_id", "render")}
    context.skill_script_snapshots = {"selected_id": {"skill_name": "selected-skill", "capability_id": "selected_id", "version": "1.0.0", "content_hash": "hash-1"}}
    context.active_tool_calls["sdk-script-1"] = {"agent_tool_call_id": "call-1"}
    tool_context = ToolContext(
        context,
        tool_name=execute_skill_script.name,
        tool_call_id="sdk-script-1",
        tool_arguments="{}",
    )

    async def invoke(arguments: str) -> str:
        return await execute_skill_script.on_invoke_tool(tool_context, arguments)

    with pytest.raises(AgentToolConfigurationError, match="verified version snapshot"):
        asyncio.run(
            invoke('{"skill_name":"other-skill","script_id":"render","input_json":"{}"}')
        )
    assert backend.calls == []
    result = asyncio.run(
        invoke('{"skill_name":"selected-skill","script_id":"render","input_json":"{\\"value\\":1}"}')
    )
    assert '"status": "completed"' in result
    assert backend.calls == [
        (
            "call-1",
            "sdk-script-1",
            {
                "skill_name": "selected-skill",
                "script_id": "render",
                "input_json": '{"value":1}',
            },
        )
    ]


def test_normalize_tool_events_without_leaking_payloads() -> None:
    tool_call = SimpleNamespace(
        type="run_item_stream_event",
        name="tool_called",
        item=SimpleNamespace(raw_item=SimpleNamespace(name="inspect_project")),
    )
    tool_output = SimpleNamespace(type="run_item_stream_event", name="tool_output")

    assert normalize_stream_event(tool_call) == {
        "event": "agent.tool.started",
        "data": {"name": "inspect_project"},
    }
    assert normalize_stream_event(tool_output) == {
        "event": "agent.tool.completed",
        "data": {},
    }


def test_execution_stream_emits_safe_progress_and_committed_exchange() -> None:
    async def collect():  # type: ignore[no-untyped-def]
        async def operation(emit):  # type: ignore[no-untyped-def]
            await emit(
                {
                    "event": "agent.tool.started",
                    "data": {"tool_name": "inspect_project"},
                }
            )
            observation = TurnObservation("provider", "model", "release-1")
            return AgentExecutionOutcome(
                exchange={"data": {"agent_message": {"message_id": "msg_agent"}}},
                observation=observation.public(),
            )

        return [event async for event in AgentExecutionStream(operation).events()]

    events = asyncio.run(collect())

    assert events[0] == {
        "event": "agent.tool.started",
        "data": {"tool_name": "inspect_project"},
    }
    assert events[-1]["event"] == "agent.turn.committed"
    assert events[-1]["data"]["exchange"]["data"]["agent_message"]["message_id"] == "msg_agent"
    assert events[-1]["data"]["observation"]["release_id"] == "release-1"


def test_execution_stream_emits_one_private_approval_checkpoint() -> None:
    async def collect():  # type: ignore[no-untyped-def]
        async def operation(_emit):  # type: ignore[no-untyped-def]
            return AgentExecutionPaused(
                run_state={"$schemaVersion": "1.16", "private": "checkpoint"},
                run_state_schema="1.16",
                pending_sdk_tool_call_ids=["sdk-write-1"],
                observation={"schema_version": "agent_turn_observation.v1"},
            )

        return [event async for event in AgentExecutionStream(operation).events()]

    events = asyncio.run(collect())
    assert events == [
        {
            "event": "agent.turn.waiting_approval",
            "data": {
                "run_state": {"$schemaVersion": "1.16", "private": "checkpoint"},
                "run_state_schema": "1.16",
                "pending_sdk_tool_call_ids": ["sdk-write-1"],
                "observation": {"schema_version": "agent_turn_observation.v1"},
            },
        }
    ]


def test_execution_stream_cancel_is_idempotent() -> None:
    async def collect():  # type: ignore[no-untyped-def]
        async def operation(_emit):  # type: ignore[no-untyped-def]
            await asyncio.sleep(10)
            return {}

        stream = AgentExecutionStream(operation)
        assert stream.cancel() is True
        assert stream.cancel() is False
        return [event async for event in stream.events()]

    events = asyncio.run(collect())
    assert len(events) == 1
    assert events[0]["event"] == "agent.turn.cancelled"
    assert events[0]["data"]["reason"] == "client_request"
    assert events[0]["data"]["observation"]["cancel_reason"] == "client_request"


def test_execution_stream_normalizes_unexpected_operation_failure() -> None:
    async def collect() -> None:
        async def operation(_emit):  # type: ignore[no-untyped-def]
            raise KeyError("provider-internal")

        with pytest.raises(RuntimeCompatibilityError, match="Agents SDK run failed"):
            _ = [event async for event in AgentExecutionStream(operation).events()]

    asyncio.run(collect())


def test_execution_stream_suppresses_raw_control_output() -> None:
    raw = SimpleNamespace(
        type="raw_response_event",
        data=SimpleNamespace(delta='{"reply":"internal control payload"}'),
    )
    tool = SimpleNamespace(
        type="run_item_stream_event",
        name="tool_called",
        item=SimpleNamespace(raw_item=SimpleNamespace(name="commit_agent_action")),
    )

    assert normalize_execution_stream_event(raw) is None
    assert normalize_execution_stream_event(tool) == {
        "event": "agent.tool.started",
        "data": {"tool_name": "commit_agent_action"},
    }


def test_turn_observation_collects_safe_ids_usage_latency_and_private_trace_config() -> None:
    observation = TurnObservation(
        "openai-compatible-responses/提供者",
        "gpt-test@模型",
        "release-test/发布",
    )
    run_config = observation.run_config(
        workflow_name="content-agent-turn",
        conversation_id="conversation-private-name",
        agent_turn_id="turn-private-name",
        tracing_enabled=True,
    )
    with observation.measure("agent_loop"):
        observation.ingest_result(
            SimpleNamespace(
                raw_responses=[
                    SimpleNamespace(response_id="resp_123", request_id="req_456")
                ],
                context_wrapper=SimpleNamespace(
                    usage=SimpleNamespace(
                        requests=1,
                        input_tokens=12,
                        output_tokens=5,
                        total_tokens=17,
                        input_tokens_details=SimpleNamespace(
                            cached_tokens=3, cache_write_tokens=0
                        ),
                        output_tokens_details=SimpleNamespace(reasoning_tokens=2),
                    )
                ),
            )
        )

    payload = observation.public()
    assert run_config.trace_include_sensitive_data is False
    assert run_config.tracing_disabled is False
    assert run_config.group_id != "conversation-private-name"
    assert run_config.trace_metadata == {
        "release_id": "release-test",
        "turn_hash": run_config.trace_metadata["turn_hash"],
    }
    assert payload["response_ids"] == ["resp_123"]
    assert payload["request_ids"] == ["req_456"]
    assert payload["provider_id"] == "openai-compatible-responses"
    assert payload["model_id"] == "gpt-test"
    assert payload["release_id"] == "release-test"
    assert payload["usage"]["total_tokens"] == 17
    assert payload["usage"]["cached_tokens"] == 3
    assert payload["usage"]["reasoning_tokens"] == 2
    assert payload["latency_ms"]["total"] >= payload["latency_ms"]["agent_loop"]
