import asyncio
from types import SimpleNamespace

import pytest
from agents.run_context import RunContextWrapper

from content_agent_sidecar.agent_tools import AgentToolConfigurationError, AgentToolProvider


class Backend:
    def __init__(self):
        self.calls = 0
        self.status = "pending_approval"
        self.identity = "call-1"

    async def begin_agent_tool_call(self, **payload):
        self.calls += 1
        return {"agent_tool_call_id": self.identity, "sdk_tool_call_id": payload["sdk_tool_call_id"],
                "tool_id": payload["tool_id"], "arguments_hash": "stable-hash", "status": self.status}


def test_refresh_reloads_approval_without_changing_registered_binding():
    async def run():
        backend = Backend()
        provider = AgentToolProvider(backend, [])
        context = SimpleNamespace(project_id="p", conversation_id="c", agent_turn_id="t", active_tool_calls={})
        wrapper = RunContextWrapper(context)
        first = await provider.ensure_call(wrapper, "runtime:example", {"value": 1}, "sdk-1", configuration_hash="config")
        backend.status = "approved"
        assert (await provider.ensure_call(wrapper, "runtime:example", {"value": 1}, "sdk-1", configuration_hash="config"))["status"] == "pending_approval"
        refreshed = await provider.ensure_call(wrapper, "runtime:example", {"value": 1}, "sdk-1", configuration_hash="config", refresh=True)
        assert refreshed["status"] == "approved" and refreshed["agent_tool_call_id"] == first["agent_tool_call_id"]
        assert backend.calls == 2
        with pytest.raises(AgentToolConfigurationError):
            await provider.ensure_call(wrapper, "runtime:example", {"value": 2}, "sdk-1", configuration_hash="config", refresh=True)
        assert backend.calls == 2
        backend.identity = "foreign-call"
        with pytest.raises(AgentToolConfigurationError, match="different"):
            await provider.ensure_call(wrapper, "runtime:example", {"value": 1}, "sdk-1", configuration_hash="config", refresh=True)
        assert context.active_tool_calls["sdk-1"] is refreshed
    asyncio.run(run())


def test_refresh_without_registration_never_calls_backend():
    async def run():
        backend = Backend()
        provider = AgentToolProvider(backend, [])
        wrapper = RunContextWrapper(SimpleNamespace(project_id="p", conversation_id="c", active_tool_calls={}))
        with pytest.raises(AgentToolConfigurationError, match="registered"):
            await provider.ensure_call(wrapper, "runtime:example", {}, "sdk-1", refresh=True)
        assert backend.calls == 0
    asyncio.run(run())


def test_refresh_without_durable_backend_is_rejected():
    async def run():
        provider = AgentToolProvider(SimpleNamespace(), [])
        with pytest.raises(AgentToolConfigurationError, match="durable audit"):
            await provider.ensure_call(RunContextWrapper(SimpleNamespace()), "runtime:example", {}, "sdk-1", refresh=True)
    asyncio.run(run())
