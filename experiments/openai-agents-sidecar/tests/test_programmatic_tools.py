from __future__ import annotations

import asyncio
import json
from types import SimpleNamespace

import pytest
from agents import Agent, FunctionTool, ProgrammaticToolCallingTool, Runner, RunConfig, RunState, function_tool
from agents.items import ModelResponse
from agents.exceptions import ModelBehaviorError
from agents.usage import Usage
from openai.types.responses import ResponseFunctionToolCall
from openai.types.responses.response_output_item import Program, ProgramOutput
from agents.models.openai_responses import OpenAIResponsesModel
from agents.tool import validate_responses_programmatic_tool_calling_configuration
from agents.tool_context import ToolContext

from content_agent_sidecar.agent_tools import AgentToolCatalog, AgentToolConfigurationError, AgentToolProvider
from test_agent_tools import AuditBackend, _context, _runtime_catalog, echo_value
from test_run_state_approval import _text_response
from test_stateful_execution import StreamingSequence, final_item
from content_agent_sidecar.runtime import AgentContext, _serialize_paused_run, normalize_execution_stream_event


def catalog():
    value = _runtime_catalog()
    value["programmatic_tool_ids"] = ["runtime:echo_value"]
    return value


def provider(backend):
    return AgentToolProvider(backend, [echo_value], execution_model=lambda: OpenAIResponsesModel(
        model="test-model", openai_client=SimpleNamespace()))


@pytest.mark.parametrize("value", [None, True, "runtime:echo_value", [None], [1], [""],
                                   ["runtime:echo_value", "runtime:echo_value"], ["runtime:missing"]])
def test_invalid_programmatic_allowlist_is_rejected(value):
    raw = catalog()
    raw["programmatic_tool_ids"] = value
    with pytest.raises(AgentToolConfigurationError):
        AgentToolCatalog.parse(raw)


@pytest.mark.parametrize("field,value", [("access", "write"), ("access", "sensitive"),
                                        ("approval", "always"), ("kind", "hosted")])
def test_programmatic_callers_cannot_gain_write_or_approval_permissions(field, value):
    raw = catalog()
    raw["tools"][0][field] = value
    with pytest.raises(AgentToolConfigurationError):
        AgentToolCatalog.parse(raw)


def test_prepared_programmatic_tool_keeps_audit_and_shared_tool_unmodified():
    backend = AuditBackend(catalog())
    context = _context()

    async def run():
        prepared = await provider(backend).prepare(context, [], set())
        validate_responses_programmatic_tool_calling_configuration(prepared.tools)
        assert sum(isinstance(tool, ProgrammaticToolCallingTool) for tool in prepared.tools) == 1
        tool = next(tool for tool in prepared.tools if isinstance(tool, FunctionTool))
        assert tool.allowed_callers == ["direct", "programmatic"]
        wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="program-read",
                              tool_arguments='{"value":"one"}')
        result = await tool.on_invoke_tool(wrapper, wrapper.tool_arguments)
        assert result == {"value": "one"}
        assert len(backend.begun) == len(backend.started) == len(backend.completed) == 1
        assert echo_value.allowed_callers is None

    asyncio.run(run())


@pytest.mark.parametrize("disabled", [False, True])
def test_no_or_disabled_eligible_tool_does_not_add_programmatic_host(disabled):
    raw = catalog()
    if disabled:
        raw["tools"][0]["enabled"] = False
    else:
        raw.pop("programmatic_tool_ids")
    prepared = asyncio.run(provider(AuditBackend(raw)).prepare(_context(), [], set()))
    assert not any(isinstance(tool, ProgrammaticToolCallingTool) for tool in prepared.tools)


def test_programmatic_configuration_requires_responses_and_audit():
    backend = AuditBackend(catalog())
    with pytest.raises(AgentToolConfigurationError, match="Responses"):
        asyncio.run(AgentToolProvider(backend, [echo_value]).prepare(_context(), [], set()))
    backend.begin_agent_tool_call = None
    with pytest.raises(AgentToolConfigurationError, match="audit"):
        asyncio.run(provider(backend).prepare(_context(), [], set()))


def test_hosted_responses_model_does_not_authorize_non_responses_execution():
    backend = AuditBackend(catalog())
    hosted = OpenAIResponsesModel(model="hosted", openai_client=SimpleNamespace())
    instance = AgentToolProvider(backend, [echo_value], hosted_model=hosted,
                                 execution_model=lambda: SimpleNamespace())
    with pytest.raises(AgentToolConfigurationError, match="execution model"):
        asyncio.run(instance.prepare(_context(), [], set()))
    assert not backend.begun


def test_programmatic_model_is_rechecked_on_each_preparation():
    backend = AuditBackend(catalog())
    selected = [OpenAIResponsesModel(model="execution", openai_client=SimpleNamespace())]
    instance = AgentToolProvider(backend, [echo_value], execution_model=lambda: selected[0])
    assert any(isinstance(tool, ProgrammaticToolCallingTool) for tool in
               asyncio.run(instance.prepare(_context(), [], set())).tools)
    selected[0] = SimpleNamespace()
    with pytest.raises(AgentToolConfigurationError, match="execution model"):
        asyncio.run(instance.prepare(_context(), [], set()))
    assert echo_value.allowed_callers is None


@pytest.mark.parametrize("phase,receipt", [
    ("begin", {}), ("start", None), ("start", {"agent_tool_call_id": "wrong", "status": "running"}),
    ("start", {"agent_tool_call_id": "tcall-1", "status": "pending_approval"}),
    ("complete", None), ("complete", {"agent_tool_call_id": "wrong", "status": "completed"}),
    ("complete", {"agent_tool_call_id": "tcall-1", "status": "running"}),
])
def test_programmatic_tools_require_confirmed_audit_receipts(phase, receipt):
    invoked = []

    @function_tool(name_override="echo_value")
    async def read(value: str) -> dict[str, str]:
        """Read an isolated fixture value."""
        invoked.append(value)
        return {"value": value}

    async def run():
        backend = AuditBackend(catalog())

        async def invalid_receipt(*args, **kwargs):
            return receipt

        setattr(backend, f"{phase}_agent_tool_call", invalid_receipt)
        instance = AgentToolProvider(backend, [read], execution_model=lambda: OpenAIResponsesModel(
            model="fixture", openai_client=SimpleNamespace()))
        context = _context()
        prepared = await instance.prepare(context, [], set())
        tool = next(tool for tool in prepared.tools if isinstance(tool, FunctionTool))
        wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="audit-fixture",
                              tool_arguments='{"value":"one"}')
        with pytest.raises(AgentToolConfigurationError, match="receipt"):
            await tool.on_invoke_tool(wrapper, wrapper.tool_arguments)
        assert invoked == (["one"] if phase == "complete" else [])
        assert not backend.completed

    asyncio.run(run())


def test_programmatic_completion_persistence_error_is_not_returned_as_success():
    async def run():
        backend = AuditBackend(catalog())

        async def unavailable(*args, **kwargs):
            raise RuntimeError("isolated audit outage")

        backend.complete_agent_tool_call = unavailable
        context = _context()
        prepared = await provider(backend).prepare(context, [], set())
        tool = next(tool for tool in prepared.tools if isinstance(tool, FunctionTool))
        wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="audit-outage",
                              tool_arguments='{"value":"one"}')
        with pytest.raises(RuntimeError, match="audit outage"):
            await tool.on_invoke_tool(wrapper, wrapper.tool_arguments)
        assert len(backend.started) == 1
        assert not backend.completed

    asyncio.run(run())


class ProgramResponseModel(OpenAIResponsesModel):
    def __init__(self, *, include_parent=True):
        super().__init__(model="isolated-program-response", openai_client=SimpleNamespace())
        self.inputs = []
        self.include_parent = include_parent

    async def get_response(self, *args, **kwargs):
        self.inputs.append(kwargs.get("input"))
        if len(self.inputs) == 1:
            parent = [Program(type="program", id="program-item-1", call_id="program-1", code="fixture", fingerprint="fixture-fingerprint")] if self.include_parent else []
            return ModelResponse(output=parent + [ResponseFunctionToolCall(
                name="echo_value", call_id="program-function-1", arguments='{"value":"one"}',
                type="function_call", caller={"type": "program", "caller_id": "program-1"})],
                usage=Usage(requests=1), response_id="program-response-1")
        return _text_response()


@pytest.mark.parametrize("mode", ["conversation", "background_task", "stateful_workflow"])
def test_sdk_runner_preserves_program_caller_and_audits_result(mode):
    callers = []

    @function_tool(name_override="echo_value")
    async def read(ctx: ToolContext, value: str) -> dict[str, str]:
        """Read the fixture value."""
        callers.append(ctx.tool_call.caller.model_dump())
        return {"value": value}

    async def run():
        backend = AuditBackend(catalog())
        context = _context()
        if mode != "conversation":
            context.agent_turn_id = ""
            context.attempt_token = "isolated-attempt-token"
            setattr(context, "agent_task_attempt_id" if mode == "background_task" else "execution_attempt_id", "isolated-attempt")
        model = ProgramResponseModel()
        tools = await AgentToolProvider(backend, [read], execution_model=lambda: model).prepare(context, [], set())
        result = await Runner.run(Agent(name="PTC fixture", model=model, tools=tools.tools),
                                  "Read one value", context=context, run_config=RunConfig(tracing_disabled=True))
        assert result.final_output
        assert callers == [{"type": "program", "caller_id": "program-1"}]
        assert len(backend.begun) == len(backend.started) == len(backend.completed) == 1
        assert backend.begun[0]["sdk_tool_call_id"] == "program-function-1"
        assert backend.begun[0]["program_call_id"] == "program-1"
        if mode != "conversation":
            assert backend.begun[0]["agent_turn_id"] == ""
            assert backend.begun[0]["agent_task_attempt_id" if mode == "background_task" else "execution_attempt_id"] == "isolated-attempt"
        assert any(item.get("type") == "function_call_output" for item in model.inputs[-1])

    asyncio.run(run())


def test_program_caller_cannot_change_after_local_registration():
    async def run():
        backend = AuditBackend(catalog())
        instance = provider(backend)
        context = _context()
        wrapper = SimpleNamespace(context=context, tool_call=SimpleNamespace(
            caller={"type": "program", "caller_id": "program-1"}))
        await instance.ensure_call(wrapper, "runtime:echo_value", {"value": "one"}, "same-call")
        for caller in [None, {"type": "program", "caller_id": "program-2"}]:
            wrapper.tool_call.caller = caller
            with pytest.raises(AgentToolConfigurationError, match="changed"):
                await instance.ensure_call(wrapper, "runtime:echo_value", {"value": "one"}, "same-call")
        assert len(backend.begun) == 1

    asyncio.run(run())


@pytest.mark.parametrize("caller", [{"type": "program", "caller_id": value}
                                   for value in [None, "", " leading", "line\n", "x" * 257]] + [{"type": "unknown"}])
def test_invalid_program_caller_is_rejected_before_registration(caller):
    backend = AuditBackend(catalog())
    wrapper = SimpleNamespace(context=_context(), tool_call=SimpleNamespace(caller=caller))
    with pytest.raises(AgentToolConfigurationError):
        asyncio.run(provider(backend).ensure_call(wrapper, "runtime:echo_value", {}, "call"))
    assert not backend.begun


@pytest.mark.parametrize("case", ["missing-parent", "ungranted-tool"])
def test_sdk_runner_rejects_invalid_program_call_before_audit(case):
    async def run():
        backend = AuditBackend(catalog())
        model = ProgramResponseModel(include_parent=case != "missing-parent")
        prepared = await AgentToolProvider(backend, [echo_value], execution_model=lambda: model).prepare(_context(), [], set())
        if case == "ungranted-tool":
            next(tool for tool in prepared.tools if isinstance(tool, FunctionTool)).allowed_callers = ["direct"]
        with pytest.raises(ModelBehaviorError):
            await Runner.run(Agent(name="Invalid PTC fixture", model=model, tools=prepared.tools),
                             "Read", context=_context(), run_config=RunConfig(tracing_disabled=True))
        assert not backend.begun and not backend.started and not backend.completed

    asyncio.run(run())


def test_program_pause_checkpoint_preserves_fingerprint_and_does_not_repeat_read():
    class StreamingProgramModel(OpenAIResponsesModel):
        def __init__(self, responses):
            super().__init__(model="program-fixture", openai_client=SimpleNamespace())
            self.sequence = StreamingSequence(responses)

        async def stream_response(self, *args, **kwargs):
            async for event in self.sequence.stream_response(*args, **kwargs):
                yield event

    async def run():
        backend = AuditBackend(catalog())
        model = StreamingProgramModel([[
            Program(type="program", id="program-item", call_id="program-1", code="fixture", fingerprint="opaque-replay-token"),
            ResponseFunctionToolCall(name="echo_value", call_id="read-once", arguments='{"value":"one"}',
                                     type="function_call", caller={"type": "program", "caller_id": "program-1"}),
        ]])
        context = AgentContext("prj-tools", "conv-tools", backend, agent_turn_id="turn-tools", idempotency_key="turn-key")
        prepared = await AgentToolProvider(backend, [echo_value], execution_model=lambda: model).prepare(context, [], set())
        config = RunConfig(tracing_disabled=True)
        result = Runner.run_streamed(Agent(name="Program recovery", model=model, tools=prepared.tools), "Read", context=context, run_config=config)
        async for event in result.stream_events():
            if event.type == "raw_response_event":
                result.cancel(mode="after_turn")
        assert result.final_output is None
        assert len(backend.completed) == 1
        serialized, _, pending = _serialize_paused_run(result, allow_no_approvals=True)
        assert not pending
        serialized = json.loads(json.dumps(serialized))
        assert "opaque-replay-token" in json.dumps(serialized)

        resumed_model = StreamingProgramModel([[
            ProgramOutput(type="program_output", id="program-output", call_id="program-1", result="one", status="completed"),
            final_item({"summary": "done"}),
        ]])
        restored_context = AgentContext("prj-tools", "conv-tools", backend, agent_turn_id="turn-tools", idempotency_key="turn-key")
        restored_tools = await AgentToolProvider(backend, [echo_value], execution_model=lambda: resumed_model).prepare(restored_context, [], set())
        agent = Agent(name="Program recovery", model=resumed_model, tools=restored_tools.tools)
        state = await RunState.from_json(agent, serialized, context_override=restored_context)
        resumed = Runner.run_streamed(agent, state, run_config=config)
        async for _ in resumed.stream_events():
            pass
        assert json.loads(resumed.final_output) == {"summary": "done"}
        assert len(backend.begun) == len(backend.completed) == 1
        inputs = resumed_model.sequence.inputs[0]
        assert len([item for item in inputs if item.get("type") == "function_call_output" and item.get("call_id") == "read-once"]) == 1
        assert next(item for item in inputs if item.get("type") == "program")["fingerprint"] == "opaque-replay-token"

    asyncio.run(run())


def test_program_tool_cancellation_is_audited_without_success():
    async def run():
        entered = asyncio.Event()
        exited = asyncio.Event()

        @function_tool(name_override="echo_value")
        async def slow_read(value: str) -> dict[str, str]:
            """Wait for a fixture resource."""
            entered.set()
            try:
                await asyncio.Event().wait()
                return {"value": value}
            finally:
                exited.set()

        backend = AuditBackend(catalog())
        model = ProgramResponseModel()
        prepared = await AgentToolProvider(backend, [slow_read], execution_model=lambda: model).prepare(_context(), [], set())
        task = asyncio.create_task(Runner.run(Agent(name="Cancel program", model=model, tools=prepared.tools),
                                              "Read", context=_context(), run_config=RunConfig(tracing_disabled=True)))
        try:
            await asyncio.wait_for(entered.wait(), timeout=3)
            task.cancel()
            with pytest.raises(asyncio.CancelledError):
                await task
            assert exited.is_set()
            assert len(backend.begun) == len(backend.started) == len(backend.cancelled) == 1
            assert not backend.completed
        finally:
            if not task.done():
                task.cancel()
                await asyncio.gather(task, return_exceptions=True)

    asyncio.run(run())


@pytest.mark.parametrize("as_dict", [False, True])
@pytest.mark.parametrize("status", ["completed", "incomplete", "unknown"])
def test_program_status_projection_excludes_code_fingerprint_and_result(as_dict, status):
    raw = {"type": "program_output", "status": status, "result": "private output", "fingerprint": "private fingerprint"}
    event = SimpleNamespace(type="run_item_stream_event", name="tool_output",
                            item=SimpleNamespace(raw_item=raw if as_dict else SimpleNamespace(**raw)))
    expected = None if status == "unknown" else {"event": "agent.tool.completed", "data": {"tool_name": "programmatic_tool_calling", "status": status}}
    assert normalize_execution_stream_event(event) == expected
    raw = {"type": "program", "code": "private code", "fingerprint": "private fingerprint"}
    event.item.raw_item = raw if as_dict else SimpleNamespace(**raw)
    assert normalize_execution_stream_event(event) == {"event": "agent.tool.started", "data": {"tool_name": "programmatic_tool_calling"}}
