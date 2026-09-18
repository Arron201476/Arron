import asyncio
import json

import pytest
from agents import Agent, RunConfig, Runner, UserError
from agents.items import ModelResponse
from agents.usage import Usage
from agents.sandbox.capabilities.tools import ExecCommandTool
from agents.sandbox.types import ExecResult
from agents.testing.sandbox import scripted_sandbox_session
from agents.tool_context import ToolContext

from content_agent_sidecar.agent_tools import AgentToolApprovalRequired, AgentToolConfigurationError, AgentToolProvider
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from test_agent_tools import AuditBackend, _context, _runtime_catalog
from test_run_state_approval import SequenceModel, _text_response
from test_stateful_execution import tool_item


@pytest.mark.parametrize("approval", ["never", "always"])
def test_native_shell_preserves_binding_and_obeys_platform_approval(approval):
    async def scenario():
        session = scripted_sandbox_session([
            {"method": "exec", "result": ExecResult(stdout=b"NATIVE_EXEC", stderr=b"", exit_code=0)}
        ])
        native = ExecCommandTool(session=session)
        original_invoke = native.on_invoke_tool
        catalog = _runtime_catalog(approval=approval)
        catalog["tools"][0].update(id="runtime:exec_command", name="exec_command", access="write")
        backend = AuditBackend(catalog)
        provider = AgentToolProvider(backend, [native], guardrail_policy=SDKGuardrailPolicy())
        context = _context()
        if approval == "never":
            with pytest.raises(AgentToolConfigurationError, match="explicit durable approval"):
                await provider.prepare(context, [], set())
            assert not session.calls and not backend.started
            return
        prepared = await provider.prepare(context, [], set())
        assert len(prepared.tools) == 1
        tool = SDKGuardrailPolicy().protect(Agent(name="Native tool fixture", tools=prepared.tools)).tools[0]
        assert tool is not native and tool.params_json_schema == native.params_json_schema
        assert native.on_invoke_tool is original_invoke and native.needs_approval is False
        arguments = {"cmd": "printf NATIVE_EXEC", "yield_time_ms": 1000, "login": False, "tty": False}
        raw = json.dumps(arguments)
        call_context = ToolContext(context, tool_name="exec_command", tool_call_id="native-shell", tool_arguments=raw)
        if approval == "always":
            assert await tool.needs_approval(call_context, arguments, "native-shell")
            assert not session.calls and not backend.started
            with pytest.raises(AgentToolApprovalRequired):
                await tool.on_invoke_tool(call_context, raw)
            assert not session.calls and not backend.started
            context.active_tool_calls["native-shell"]["status"] = "approved"
            assert not await tool.needs_approval(call_context, arguments, "native-shell")
            output = await tool.on_invoke_tool(call_context, raw)
            assert "NATIVE_EXEC" in output
            assert len(backend.started) == len(backend.completed) == len(session.calls) == 1
            assert backend.begun[0]["arguments"] == arguments
            assert session.calls[0].method == "exec"
            session.assert_complete()
    asyncio.run(scenario())


@pytest.mark.parametrize("cmd", ["echo PRIVATE_TEST_CREDENTIAL", "echo " + "x" * 200], ids=["credential", "size"])
def test_native_shell_guardrail_blocks_before_transport(cmd):
    async def scenario():
        session = scripted_sandbox_session([
            {"method": "exec", "result": ExecResult(stdout=b"must-not-execute", stderr=b"", exit_code=0)}
        ])
        native = ExecCommandTool(session=session)
        catalog = _runtime_catalog(approval="always")
        catalog["tools"][0].update(id="runtime:exec_command", name="exec_command", access="write")
        backend = AuditBackend(catalog)
        context = _context()
        policy = SDKGuardrailPolicy(max_output_chars=150, protected_values=("PRIVATE_TEST_CREDENTIAL",))
        prepared = await AgentToolProvider(backend, [native], guardrail_policy=policy).prepare(context, [], set())
        response = ModelResponse(output=[tool_item("exec_command", {"cmd": cmd}, "blocked-shell")], usage=Usage(requests=1), response_id="request-shell")
        model = SequenceModel([response, _text_response()])
        agent = policy.protect(Agent(name="Native protected fixture", model=model, tools=prepared.tools))
        with pytest.raises((AgentToolConfigurationError, UserError)):
            await Runner.run(agent, "Run the safe operation", context=context, max_turns=2, run_config=RunConfig(tracing_disabled=True))
        raw = json.dumps({"cmd": cmd})
        direct = ToolContext(context, tool_name="exec_command", tool_call_id="direct-blocked", tool_arguments=raw)
        with pytest.raises(AgentToolConfigurationError, match="platform data policy"):
            await prepared.tools[0].on_invoke_tool(direct, raw)
        assert not session.calls and not backend.begun and not backend.started and not backend.completed
        assert native.tool_input_guardrails is None and prepared.tools[0].tool_input_guardrails is None
    asyncio.run(scenario())


def test_native_shell_requires_platform_guardrail_policy():
    async def scenario():
        session = scripted_sandbox_session([])
        catalog = _runtime_catalog(approval="always")
        catalog["tools"][0].update(id="runtime:exec_command", name="exec_command", access="write")
        backend = AuditBackend(catalog)
        with pytest.raises(AgentToolConfigurationError, match="platform guardrails"):
            await AgentToolProvider(backend, [ExecCommandTool(session=session)]).prepare(_context(), [], set())
        assert not backend.begun and not session.calls
    asyncio.run(scenario())
