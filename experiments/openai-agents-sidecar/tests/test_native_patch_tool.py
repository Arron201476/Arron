import asyncio
from copy import deepcopy
import hashlib

import pytest
from agents import Agent, RunConfig, Runner, RunState
from agents.exceptions import UserError
from agents.items import ModelResponse
from agents.sandbox.capabilities.tools.apply_patch_tool import SandboxApplyPatchTool
from agents.testing.sandbox import scripted_sandbox_session
from agents.tool import CustomTool
from agents.tool_context import ToolContext
from agents.usage import Usage
from openai.types.responses import ResponseCustomToolCall

from content_agent_sidecar.agent_tools import AgentToolConfigurationError, AgentToolProvider
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from test_agent_tools import AuditBackend, _context, _runtime_catalog
from test_run_state_approval import SequenceModel, _text_response


PATCH = "*** Begin Patch\n*** Add File: note.txt\n+native patch\n*** End Patch\n"
CONFIG = RunConfig(tracing_disabled=True)


class PatchBackend(AuditBackend):
    def __init__(self):
        catalog = _runtime_catalog(approval="always")
        catalog["tools"][0].update(id="runtime:apply_patch", name="apply_patch", access="write")
        super().__init__(catalog)
        self.calls = {}

    async def begin_agent_tool_call(self, **payload):
        sdk_id = payload["sdk_tool_call_id"]
        if sdk_id not in self.calls:
            self.calls[sdk_id] = await super().begin_agent_tool_call(**payload)
        return deepcopy(self.calls[sdk_id])

    async def start_agent_tool_call(self, call_id, sdk_id, **kwargs):
        assert self.calls[sdk_id]["status"] == "approved"
        self.calls[sdk_id]["status"] = "running"
        return await super().start_agent_tool_call(call_id, sdk_id, **kwargs)


def response(raw=PATCH):
    return ModelResponse(output=[ResponseCustomToolCall(type="custom_tool_call", call_id="patch-call", name="apply_patch", input=raw, id="patch-item")], usage=Usage(requests=1), response_id="patch-model")


async def prepared_patch(backend, session, context, *, policy=None, on_approval=None):
    native = SandboxApplyPatchTool(session=session, on_approval=on_approval)
    prepared = await AgentToolProvider(backend, [native], guardrail_policy=policy or SDKGuardrailPolicy()).prepare(context, [], set())
    assert len(prepared.tools) == 1
    wrapped = prepared.tools[0]
    assert type(wrapped) is CustomTool and wrapped.format == native.format and wrapped.on_approval is None
    assert "parameters" not in wrapped.tool_config and wrapped.tool_config["type"] == "custom"
    return wrapped


@pytest.mark.parametrize("decision", ["approve", "reject"])
def test_native_patch_sdk_approval_rebuild_preserves_raw_input_and_editor(decision):
    async def scenario():
        backend = PatchBackend()
        before = scripted_sandbox_session([])
        context = _context()
        async def must_not_auto_approve(*args):
            raise AssertionError("native callback must not bypass durable platform decision")
        tool = await prepared_patch(backend, before, context, on_approval=must_not_auto_approve)
        first = Agent(name="Native patch", model=SequenceModel([response()]), tools=[tool])
        pending = await Runner.run(first, "Write the file", context=context, run_config=CONFIG)
        assert len(pending.interruptions) == 1 and not before.calls and not backend.started
        state_json = pending.to_state().to_json(context_serializer=lambda value: {"project_id": value.project_id})
        assert backend.begun[0]["arguments"] == {"input": PATCH}
        backend.calls["patch-call"]["status"] = "approved" if decision == "approve" else "rejected"
        after = scripted_sandbox_session([
            {"method": "mkdir", "result": None}, {"method": "write", "result": None},
        ] if decision == "approve" else [])
        restored_context = _context()
        restored_tool = await prepared_patch(backend, after, restored_context)
        model = SequenceModel([_text_response()])
        second = Agent(name="Native patch", model=model, tools=[restored_tool])
        state = await RunState.from_json(second, state_json, context_override=restored_context)
        item = state.get_interruptions()[0]
        state.approve(item) if decision == "approve" else state.reject(item)
        result = await Runner.run(second, state, context=restored_context, run_config=CONFIG)
        assert result.final_output and not result.interruptions and not model.responses
        assert len(backend.started) == len(backend.completed) == (1 if decision == "approve" else 0)
        assert not backend.failed and not before.calls
        if decision == "approve":
            assert after.calls[1].args[1].getvalue() == b"native patch"
            assert "note.txt" in backend.completed[0]["result"]
        after.assert_complete()
    asyncio.run(scenario())


@pytest.mark.parametrize("raw", [PATCH.replace("native patch", "PRIVATE_PATCH_CREDENTIAL"), "x" * 200])
def test_native_patch_data_policy_runs_before_approval_registration(raw):
    async def scenario():
        backend = PatchBackend()
        session = scripted_sandbox_session([])
        context = _context()
        tool = await prepared_patch(backend, session, context, policy=SDKGuardrailPolicy(max_output_chars=150, protected_values=("PRIVATE_PATCH_CREDENTIAL",)))
        model = SequenceModel([response(raw), _text_response()])
        with pytest.raises((AgentToolConfigurationError, UserError)):
            await Runner.run(Agent(name="Native patch policy", model=model, tools=[tool]), "Write", context=context, run_config=CONFIG)
        assert not backend.begun and not backend.started and not session.calls
        assert len(model.responses) == 1
    asyncio.run(scenario())


@pytest.mark.parametrize("fault", ["changed_input", "changed_tool", "missing_hash", "replay"])
def test_native_patch_rejects_unbound_or_replayed_call_before_start(fault):
    async def scenario():
        backend = PatchBackend()
        session = scripted_sandbox_session([])
        context = _context()
        tool = await prepared_patch(backend, session, context)
        tc = ToolContext(context, tool_name="apply_patch", tool_call_id="patch-call", tool_arguments=PATCH)
        assert await tool.needs_approval(tc, PATCH, "patch-call")
        cached = context.active_tool_calls["patch-call"]
        cached["status"] = "approved"
        raw = PATCH
        if fault == "changed_input":
            raw = PATCH.replace("note.txt", "other.txt")
        elif fault == "changed_tool":
            cached["tool_id"] = "runtime:exec_command"
        elif fault == "missing_hash":
            cached.pop("arguments_hash")
        else:
            cached["status"] = "completed"
        with pytest.raises(AgentToolConfigurationError):
            await tool.on_invoke_tool(tc, raw)
        assert not session.calls and not backend.started
    asyncio.run(scenario())


@pytest.mark.parametrize("fault", ["write_error", "completion_lost", "failure_receipt_lost", "cancelled"])
def test_native_patch_uncertain_write_never_returns_success_or_replays(fault):
    async def scenario():
        class FaultBackend(PatchBackend):
            async def complete_agent_tool_call(self, *args, **kwargs):
                raise RuntimeError("completion unavailable")

            async def fail_agent_tool_call(self, *args, **kwargs):
                if fault == "failure_receipt_lost":
                    raise RuntimeError("audit unavailable")
                return await super().fail_agent_tool_call(*args, **kwargs)

        backend = FaultBackend()
        async def cancel_write(_call):
            raise asyncio.CancelledError()
        write = {"method": "write", "error": OSError("write failed")} if fault == "write_error" else {"method": "write", "result": None}
        if fault == "cancelled":
            write = {"method": "write", "responder": cancel_write}
        session = scripted_sandbox_session([{"method": "mkdir", "result": None}, write])
        context = _context()
        tool = await prepared_patch(backend, session, context)
        tc = ToolContext(context, tool_name="apply_patch", tool_call_id="patch-call", tool_arguments=PATCH)
        assert await tool.needs_approval(tc, PATCH, "patch-call")
        context.active_tool_calls["patch-call"]["status"] = "approved"
        backend.calls["patch-call"]["status"] = "approved"
        with pytest.raises(asyncio.CancelledError if fault == "cancelled" else AgentToolConfigurationError):
            await tool.on_invoke_tool(tc, PATCH)
        with pytest.raises(AgentToolConfigurationError):
            await tool.on_invoke_tool(tc, PATCH)
        assert len(backend.started) == 1 and not backend.completed and len(session.calls) == 2
        if fault != "failure_receipt_lost":
            assert backend.failed[0]["error_code"] == "CUSTOM_TOOL_OUTCOME_UNKNOWN"
        session.assert_complete()
    asyncio.run(scenario())


def test_native_patch_hash_matches_go_string_canonicalization():
    from content_agent_sidecar.native_patch_tool import _arguments_hash

    raw = '"<&>\u2028\u2029\\\n'
    canonical = b'{"input":"\\"\\u003c\\u0026\\u003e\\u2028\\u2029\\\\\\n"}'
    assert _arguments_hash(raw) == hashlib.sha256(canonical).hexdigest()


@pytest.mark.parametrize("fault", ["missing_policy", "no_approval", "retry_write", "read_descriptor"])
def test_native_patch_refuses_incomplete_platform_configuration(fault):
    async def scenario():
        backend = PatchBackend()
        descriptor = backend.catalog["tools"][0]
        if fault == "no_approval":
            descriptor["approval"] = "never"
        elif fault == "retry_write":
            descriptor["max_retries"] = 1
        elif fault == "read_descriptor":
            descriptor["access"] = "read"
        policy = None if fault == "missing_policy" else SDKGuardrailPolicy()
        tool = SandboxApplyPatchTool(session=scripted_sandbox_session([]))
        with pytest.raises(AgentToolConfigurationError):
            await AgentToolProvider(backend, [tool], guardrail_policy=policy).prepare(_context(), [], set())
    asyncio.run(scenario())


@pytest.mark.parametrize("path", ["../escape.txt", "/absolute.txt"])
def test_native_patch_retains_sdk_path_validation_before_file_operations(path):
    async def scenario():
        backend = PatchBackend()
        session = scripted_sandbox_session([])
        context = _context()
        raw = PATCH.replace("note.txt", path)
        tool = await prepared_patch(backend, session, context)
        tc = ToolContext(context, tool_name="apply_patch", tool_call_id="patch-call", tool_arguments=raw)
        await tool.needs_approval(tc, raw, "patch-call")
        context.active_tool_calls["patch-call"]["status"] = "approved"
        backend.calls["patch-call"]["status"] = "approved"
        with pytest.raises(AgentToolConfigurationError):
            await tool.on_invoke_tool(tc, raw)
        assert not session.calls and not backend.completed and len(backend.failed) == 1
    asyncio.run(scenario())


def test_native_patch_concurrent_duplicate_cannot_enter_native_editor_twice():
    async def scenario():
        entered, release = asyncio.Event(), asyncio.Event()

        class SlowStart(PatchBackend):
            async def start_agent_tool_call(self, *args, **kwargs):
                entered.set()
                await release.wait()
                return await super().start_agent_tool_call(*args, **kwargs)

        backend = SlowStart()
        session = scripted_sandbox_session([{"method": "mkdir", "result": None}, {"method": "write", "result": None}])
        context = _context()
        tool = await prepared_patch(backend, session, context)
        tc = ToolContext(context, tool_name="apply_patch", tool_call_id="patch-call", tool_arguments=PATCH)
        await tool.needs_approval(tc, PATCH, "patch-call")
        context.active_tool_calls["patch-call"]["status"] = "approved"
        backend.calls["patch-call"]["status"] = "approved"
        first = asyncio.create_task(tool.on_invoke_tool(tc, PATCH))
        try:
            await asyncio.wait_for(entered.wait(), timeout=2)
            with pytest.raises(AgentToolConfigurationError):
                await tool.on_invoke_tool(tc, PATCH)
        finally:
            release.set()
            await first
        assert len(backend.started) == len(backend.completed) == 1
        session.assert_complete()
    asyncio.run(scenario())
