import asyncio
import json
from dataclasses import replace
from pathlib import Path

import pytest
from agents import Agent, RunConfig, RunContextWrapper, Runner, RunState, UserError, function_tool
from agents.tool_context import ToolContext
from agents.mcp import MCPServerStdio
from mcp.types import CallToolResult, TextContent, Tool

from content_agent_sidecar.agent_tools import AgentToolConfigurationError, AgentToolProvider
from content_agent_sidecar.background_worker import BackgroundTaskPaused, SDKBackgroundTaskWorker
from content_agent_sidecar.observability import TurnObservation
from content_agent_sidecar.managed_instructions import bind_managed_instructions
from content_agent_sidecar.runtime import AgentContext, _serialize_agent_context
from content_agent_sidecar.stateful_execution import ExecutionTaskPaused, StatefulExecution
from content_agent_sidecar.task_worker import SDKTaskWorker
from content_agent_sidecar.tool_outcomes import EXTERNAL_TOOL_RECOVERY_REASON, ToolOutcomeReviewRequired
from test_agent_tools import AuditBackend, _mcp_catalog, _selected_skill, _runtime_catalog
from test_app import settings
from test_main_pause import make_runtime
from test_stateful_execution import StreamingSequence, final_item, tool_item
from instruction_fixtures import EmptyInstructionsBackend


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
@pytest.mark.parametrize("fault", ["transport", "error_result", "oversize", "lost_completion_ack", "failure_receipt_unavailable", "missing_arguments_binding"])
def test_audited_mcp_production_runner_pauses_without_replaying(tmp_path, monkeypatch, mode, fault):
    async def scenario():
        effects, completions = [], []
        barrier = asyncio.Event()

        async def no_transport(self): pass

        async def tools(self, *_args, **_kwargs):
            return [Tool(name="save_story_fact", description="Already approved fixture write",
                         input_schema={"type": "object", "properties": {"value": {"type": "string"}}, "required": ["value"], "additionalProperties": False})]

        async def call(self, name, arguments, meta=None):
            value = arguments["value"]
            effects.append(value)
            if len(effects) == 2: barrier.set()
            await asyncio.wait_for(barrier.wait(), 2)
            if value == "uncertain":
                if fault in {"transport", "failure_receipt_unavailable"}: raise OSError("Connection lost after sending the operation")
                if fault == "error_result": return CallToolResult(content=[TextContent(text="Partial provider error")], is_error=True)
                if fault == "oversize": return CallToolResult(content=[TextContent(text="x" * 9000)])
            return CallToolResult(content=[TextContent(text="Provider receipt: " + value)])

        monkeypatch.setattr(MCPServerStdio, "connect", no_transport)
        monkeypatch.setattr(MCPServerStdio, "cleanup", no_transport)
        monkeypatch.setattr(MCPServerStdio, "list_tools", tools)
        monkeypatch.setattr(MCPServerStdio, "call_tool", call)
        catalog = _mcp_catalog(Path("not-executed.py"))
        catalog["mcp_servers"][0]["environment"] = {}

        class Backend(AuditBackend, EmptyInstructionsBackend):
            async def begin_agent_tool_call(self, **payload):
                result = await super().begin_agent_tool_call(**payload)
                result["status"] = "approved"
                if fault == "missing_arguments_binding": result.pop("arguments_hash")
                return result

            async def complete_agent_tool_call(self, call_id, **payload):
                completions.append(call_id)
                if fault == "lost_completion_ack" and "uncertain" in json.dumps(payload) and completions.count(call_id) == 1:
                    raise OSError("Durable receipt acknowledgement lost")
                return await super().complete_agent_tool_call(call_id, **payload)

            async def fail_agent_tool_call(self, call_id, **payload):
                if fault == "failure_receipt_unavailable": raise OSError("Audit receipt could not be persisted")
                return await super().fail_agent_tool_call(call_id, **payload)

        backend = Backend(catalog)
        context = AgentContext("prj-tools", "conversation", backend)
        if mode == "main": context.agent_turn_id = "turn"
        elif mode == "background": context.agent_task_attempt_id = "background-attempt"
        else: context.execution_attempt_id = "stateful-attempt"
        await bind_managed_instructions(context)
        provider = AgentToolProvider(backend, [])
        prepared = await provider.prepare(context, _selected_skill(), {"story_fact_check"})
        runtime = None
        try:
            async with prepared:
                agent = Agent(name="Audited fixture", instructions="Preserve original operations and require checked user facts.", mcp_servers=prepared.mcp_servers,
                              mcp_config={"include_server_in_tool_names": True})
                native_tools = await agent.get_all_tools(RunContextWrapper(context))
                name = next(tool.name for tool in native_tools if tool.name.endswith("save_story_fact"))
                responses = [[tool_item(name, {"value": value}, value) for value in ("known", "uncertain")]]
                if fault == "lost_completion_ack": responses.append([final_item({"summary": "Done"})])
                model = StreamingSequence(responses)
                agent = agent.clone(model=model)

                async def run_production():
                    nonlocal runtime
                    if mode == "main":
                        runtime = make_runtime(tmp_path, backend, model)
                        async def emit(_event): pass
                        return await runtime._run_execution_agent(agent, "Original request", context, None, emit,
                                                                  TurnObservation("openai_agents_sdk", "fixture"))
                    if mode == "background":
                        return await SDKBackgroundTaskWorker(settings(), backend)._run_streamed(agent, "Original request", context, None)
                    execution = StatefulExecution(context, {"attempt_id": "stateful-attempt"})
                    return await SDKTaskWorker(settings(), backend)._run_streamed(agent, "Original request", 10, execution=execution)

                if fault in {"failure_receipt_unavailable", "missing_arguments_binding"}:
                    with pytest.raises(Exception) as caught:
                        await run_production()
                    assert not isinstance(caught.value, (ToolOutcomeReviewRequired, BackgroundTaskPaused, ExecutionTaskPaused))
                    assert isinstance(caught.value, UserError)
                    assert isinstance(caught.value.__cause__, AgentToolConfigurationError)
                    assert SDKBackgroundTaskWorker._error_code(caught.value) == SDKTaskWorker._error_code(caught.value) == "AGENT_TOOL_CONFIGURATION_INVALID"
                    assert not context.external_tool_outcome_ids and len(model.inputs) == 1
                    assert sorted(effects) == ([] if fault == "missing_arguments_binding" else ["known", "uncertain"])
                    return

                if fault == "lost_completion_ack":
                    result = await run_production()
                    assert result.final_output and len(model.inputs) == 2 and not backend.failed
                    assert sorted(effects) == ["known", "uncertain"] and len(completions) == 3
                    assert len(backend.completed) == 2
                    return

                expected = {"main": ToolOutcomeReviewRequired, "background": BackgroundTaskPaused, "stateful": ExecutionTaskPaused}[mode]
                with pytest.raises(expected) as caught:
                    await run_production()
                if mode == "main": snapshot = caught.value.result.to_state().to_json(context_serializer=_serialize_agent_context)
                else:
                    assert caught.value.recovery_reason == EXTERNAL_TOOL_RECOVERY_REASON
                    snapshot = caught.value.run_state
                snapshot = json.loads(json.dumps(snapshot))
                assert snapshot["current_step"]["type"] == "next_step_run_again" and len(model.inputs) == 1
                assert sorted(effects) == ["known", "uncertain"] and len(backend.failed) == len(backend.completed) == 1
                assert backend.failed[0]["error_code"] == "MCP_TOOL_OUTCOME_UNKNOWN"
                output = next(item["raw_item"] for item in snapshot["generated_items"] if item["type"] == "tool_call_output_item" and item["raw_item"]["call_id"] == "uncertain")
                marker = json.loads(output["output"][0]["text"])
                assert marker["tool_id"] == "mcp:story-fixture/save_story_fact" and len(marker["arguments_hash"]) == 64
                assert marker["agent_tool_call_id"] == backend.failed[0]["agent_tool_call_id"]
                fresh_context = replace(context, active_tool_calls={}, external_tool_outcome_ids=[])
                final_model = StreamingSequence([[final_item({"summary": "Checked user fact received"})]])
                restored_agent = agent.clone(model=final_model)
                state = await RunState.from_json(restored_agent, snapshot, context_override=fresh_context, strict_context=True)
                state.add_input([{"role": "user", "content": "USER_RECONCILIATION: original uncertain operation was applied; do not repeat it."}])
                result = Runner.run_streamed(restored_agent, state, run_config=RunConfig(tracing_disabled=True))
                async for _ in result.stream_events(): pass
                assert sorted(effects) == ["known", "uncertain"] and result.context_wrapper.usage.requests == 2
                assert result.max_turns == snapshot["max_turns"]
                assert "USER_RECONCILIATION" in json.dumps(final_model.inputs[0])
        finally:
            if runtime is not None: await runtime._model_client.close()
    asyncio.run(scenario())


@pytest.mark.parametrize("status,body,expected", [
    (409, '{"error":{"code":"SDK_TOOL_OUTCOME_UNRESOLVED"}}', "RUNTIME_COMMIT_DEFERRED"),
    (409, '{"error":{"code":"AGENT_TOOL_APPROVAL_REQUIRED"}}', "RUNTIME_COMMIT_DEFERRED"),
    (500, '{"error":{"code":"SDK_TOOL_OUTCOME_UNRESOLVED"}}', "FUNCTION_TOOL_CALL_FAILED"),
    (409, 'SDK_TOOL_OUTCOME_UNRESOLVED', "FUNCTION_TOOL_CALL_FAILED"),
    (409, '{"error":{"code":null}}', "FUNCTION_TOOL_CALL_FAILED"),
])
def test_only_structured_transaction_rejection_is_a_safe_deferred_commit(status, body, expected):
    from content_agent_sidecar.backend import BackendError

    @function_tool
    async def commit_agent_action(value: str) -> str:
        """Fixture terminal transaction rejection."""
        raise BackendError.from_http_response(status, body)

    async def scenario():
        catalog = _runtime_catalog()
        catalog["tools"][0].update(id="runtime:commit_agent_action", name="commit_agent_action", access="write")
        backend = AuditBackend(catalog)
        context = AgentContext("prj-tools", "conversation", backend, agent_turn_id="turn")
        prepared = await AgentToolProvider(backend, [commit_agent_action]).prepare(context, [], set())
        args = '{"value":"original"}'
        output = await prepared.tools[0].on_invoke_tool(ToolContext(context, tool_name="commit_agent_action", tool_call_id="commit", tool_arguments=args), args)
        assert output and len(backend.failed) == 1 and not backend.completed
        assert backend.failed[0]["error_code"] == expected
    asyncio.run(scenario())
