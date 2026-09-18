import asyncio
from copy import deepcopy
import json

import pytest
from agents.tool_context import ToolContext

from content_agent_sidecar.agent_tools import AgentToolConfigurationError, AgentToolProvider
from test_agent_tools import AuditBackend, _context, _runtime_catalog, echo_value


def context_for(mode):
    context = _context()
    if mode != "main":
        context.agent_turn_id = ""
        context.attempt_token = "fixture-token"
        if mode == "background":
            context.agent_task_attempt_id = "background-attempt"
        else:
            context.execution_attempt_id = "workflow-attempt"
    return context


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_approved_function_cannot_execute_replaced_arguments(mode):
    backend = AuditBackend(_runtime_catalog(approval="always"))
    provider = AgentToolProvider(backend, [echo_value])
    context = context_for(mode)

    async def run():
        tool = (await provider.prepare(context, [], set())).tools[0]
        wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="bound", tool_arguments='{"value":"approved"}')
        assert await tool.needs_approval(wrapper, {"value": "approved"}, "bound")
        context.active_tool_calls["bound"]["status"] = "approved"
        with pytest.raises(AgentToolConfigurationError):
            await tool.on_invoke_tool(wrapper, '{"value":"replacement"}')
        assert not backend.started and not backend.completed

    asyncio.run(run())


@pytest.mark.parametrize("tool_id", ["runtime:echo_value", "mcp:fixture/echo", "hosted:fixture"])
@pytest.mark.parametrize("change", ["tool", "value", "type", "nested"])
def test_cached_call_binds_tool_and_exact_json_arguments(tool_id, change):
    catalog = _runtime_catalog(approval="always")
    catalog["tools"][0]["id"] = tool_id
    backend = AuditBackend(catalog)
    provider = AgentToolProvider(backend, [])
    context = _context()
    wrapper = ToolContext(context, tool_name="echo", tool_call_id="bound", tool_arguments="{}")

    async def run():
        original = {"flag": True, "nested": {"text": "original"}}
        await provider.ensure_call(wrapper, tool_id, original, "bound")
        original["nested"]["text"] = "mutated after registration"
        changed = {"flag": True, "nested": {"text": "original"}}
        replacement_tool = tool_id
        if change == "tool": replacement_tool = "runtime:other"
        if change == "value": changed["flag"] = False
        if change == "type": changed["flag"] = 1
        if change == "nested": changed["nested"]["text"] = original["nested"]["text"]
        with pytest.raises(AgentToolConfigurationError):
            await provider.ensure_call(wrapper, replacement_tool, changed, "bound")
        assert len(backend.begun) == 1

    asyncio.run(run())


def test_cache_keeps_equivalent_key_order_and_revalidates_legacy_records():
    class DurableBackend(AuditBackend):
        async def begin_agent_tool_call(self, **payload):
            canonical = json.dumps([payload["tool_id"], payload["arguments"]], sort_keys=True)
            if hasattr(self, "saved"):
                self.begun.append(deepcopy(payload))
                if canonical != self.canonical:
                    raise AgentToolConfigurationError("backend rejected changed original call")
                return deepcopy(self.saved)
            self.canonical = canonical
            self.saved = deepcopy(await super().begin_agent_tool_call(**payload))
            return deepcopy(self.saved)

    backend = DurableBackend(_runtime_catalog(approval="always"))
    provider = AgentToolProvider(backend, [])
    context = _context()
    wrapper = ToolContext(context, tool_name="echo", tool_call_id="bound", tool_arguments="{}")

    async def run():
        arguments = {"a": 1, "b": "text"}
        first = await provider.ensure_call(wrapper, "runtime:echo_value", arguments, "bound")
        same = await provider.ensure_call(wrapper, "runtime:echo_value", {"b": "text", "a": 1}, "bound")
        assert same is first and len(backend.begun) == 1
        context.active_tool_calls["bound"] = {**deepcopy(backend.saved), "_configuration_hash": ""}
        restored = await provider.ensure_call(wrapper, "runtime:echo_value", arguments, "bound")
        assert restored["agent_tool_call_id"] == first["agent_tool_call_id"]
        assert len(backend.begun) == 2
        context.active_tool_calls["bound"] = {**deepcopy(backend.saved), "_configuration_hash": ""}
        with pytest.raises(AgentToolConfigurationError, match="backend rejected"):
            await provider.ensure_call(wrapper, "runtime:echo_value", {"a": 2}, "bound")

    asyncio.run(run())
