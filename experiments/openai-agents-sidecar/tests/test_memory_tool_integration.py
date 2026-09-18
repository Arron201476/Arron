import asyncio
import json

import pytest
from agents.run_context import RunContextWrapper

from content_agent_sidecar.agent_tools import AgentToolConfigurationError, AgentToolProvider
from content_agent_sidecar.backend import backend_memory_activity
from content_agent_sidecar.managed_instructions import AgentInstructionsInvalid, instruction_activity_key
from content_agent_sidecar.runtime import AgentContext, _serialize_agent_context
from test_agent_tools import AuditBackend
from test_native_workspace_transport import response, wire


def memory_context(backend):
    return AgentContext(project_id="project", conversation_id="conversation", backend=backend,
                        memory_generation_id="generation", memory_generation_attempt=2, attempt_token="private-token")


def test_memory_native_tools_use_generation_identity_and_attempt_bound_cache():
    backend = AuditBackend({"tools": [{"id": "runtime:exec_command", "approval": "always"}]})
    provider = AgentToolProvider(backend, [])
    context = memory_context(backend)
    wrapper = RunContextWrapper(context)
    async def run():
        call = await provider.ensure_call(wrapper, "runtime:exec_command", {"command": "inspect"}, "sdk-call")
        assert call["status"] == "pending_approval"
        assert await provider.ensure_call(wrapper, "runtime:exec_command", {"command": "inspect"}, "sdk-call") is call
        assert len(backend.begun) == 1
        sent = backend.begun[0]
        assert sent["memory_generation_id"] == "generation"
        assert sent["memory_generation_attempt"] == 2
        assert sent["agent_turn_id"] == sent["agent_task_attempt_id"] == ""
        assert sent["attempt_token"] == "private-token"
        assert "private-token" not in repr(context.active_tool_calls)
        context.memory_generation_attempt = 3
        with pytest.raises(AgentToolConfigurationError, match="changed"):
            await provider.ensure_call(wrapper, "runtime:exec_command", {"command": "inspect"}, "sdk-call")
    asyncio.run(run())


@pytest.mark.parametrize("field,value", [
    ("agent_turn_id", "turn"), ("agent_task_attempt_id", "task"), ("execution_attempt_id", "execution"),
    ("skill_invocation_id", "skill"), ("memory_generation_attempt", True), ("memory_generation_attempt", 0),
    ("attempt_token", ""),
])
def test_memory_tool_registration_rejects_mixed_identity_before_request(field, value):
    backend = AuditBackend({"tools": []})
    context = memory_context(backend)
    setattr(context, field, value)
    with pytest.raises(AgentToolConfigurationError):
        asyncio.run(AgentToolProvider(backend, []).ensure_call(RunContextWrapper(context), "runtime:exec_command", {}, "sdk"))
    assert not backend.begun


def test_memory_registration_rejects_business_tool_before_request():
    backend = AuditBackend({"tools": []})
    with pytest.raises(AgentToolConfigurationError):
        asyncio.run(AgentToolProvider(backend, []).ensure_call(RunContextWrapper(memory_context(backend)), "runtime:publish_workspace_files", {}, "sdk"))
    assert not backend.begun


def test_memory_tool_registration_real_http_preserves_header_and_body_identity():
    async def run():
        with wire(response({"agent_tool_call_id": "call", "status": "pending_approval"})) as (backend, requests):
            with backend_memory_activity("project", "generation", 2, "private-token"):
                await backend.begin_agent_tool_call(project_id="project", conversation_id="conversation", agent_turn_id="",
                    sdk_tool_call_id="sdk", tool_id="runtime:exec_command", arguments={"command": "inspect"},
                    memory_generation_id="generation", memory_generation_attempt=2, attempt_token="private-token")
            method, path, headers, body = requests[0]
            assert method == "POST" and path == "/internal/v1/agent-tool-calls"
            payload = json.loads(body)
            assert payload["memory_generation_id"] == headers["X-Agent-Memory-Generation-ID"] == "generation"
            assert payload["memory_generation_attempt"] == int(headers["X-Agent-Memory-Generation-Attempt"]) == 2
            assert payload["attempt_token"] == headers["X-Agent-Attempt-Token"] == "private-token"
    asyncio.run(run())


def test_memory_uses_generation_snapshot_key_and_private_checkpoint_only():
    context = memory_context(None)
    assert instruction_activity_key(context) == "memory:generation"
    with pytest.raises(TypeError, match="private SDK checkpoint"):
        _serialize_agent_context(context)
    context.agent_turn_id = "turn"
    with pytest.raises(AgentInstructionsInvalid):
        instruction_activity_key(context)
    context.agent_turn_id = ""
    context.memory_generation_attempt = 0
    with pytest.raises(AgentInstructionsInvalid):
        instruction_activity_key(context)
