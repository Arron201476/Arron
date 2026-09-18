import asyncio
from copy import deepcopy
import json

import pytest
from agents import Agent, RunContextWrapper, RunState, Runner
from agents.items import ModelResponse
from agents.tool_context import ToolContext
from agents.usage import Usage
from openai.types.responses import ResponseFunctionToolCall

from content_agent_sidecar.agent_tools import AgentToolConfigurationError, AgentToolProvider
from content_agent_sidecar.runtime import AgentContext, _serialize_paused_run, execute_skill_script
from test_agent_tools import AuditBackend, StaticModel, _runtime_catalog


def skill(capability_id, script_id="render", version="1.0.0"):
    return {
        "capability_id": capability_id, "version": version,
        "status": "available", "execution_mode": "inline",
        "skill": {"name": "same-name", "content_hash": f"hash-{capability_id}-{version}",
                  "scripts": [{"id": script_id, "path": f"scripts/{script_id}.py", "runtime": "python"}]},
    }


class ScriptBackend(AuditBackend):
    def __init__(self):
        catalog = _runtime_catalog(approval="always")
        catalog["tools"][0].update(id="runtime:execute_skill_script", name="execute_skill_script", access="sensitive")
        super().__init__(catalog)
        self.registered = {}
        self.executed = []
        self.approved = False

    async def begin_agent_tool_call(self, **payload):
        sdk_id = payload["sdk_tool_call_id"]
        if sdk_id in self.registered:
            original, call = self.registered[sdk_id]
            assert payload["arguments"] == original["arguments"]
            assert payload["skill_snapshot"] == original["skill_snapshot"]
        else:
            call = await super().begin_agent_tool_call(**payload)
            self.registered[sdk_id] = (deepcopy(payload), deepcopy(call))
        return {**call, "status": "approved" if self.approved else "pending_approval"}

    async def execute_skill_script(self, call_id, sdk_id, arguments):
        original, call = self.registered[sdk_id]
        assert self.approved and call_id == call["agent_tool_call_id"]
        assert arguments == original["arguments"]
        self.executed.append(deepcopy(original["skill_snapshot"]))
        return {"status": "completed"}


@pytest.mark.parametrize("reverse", [False, True])
@pytest.mark.parametrize("selected_id", ["scope_a", "scope_b"])
def test_same_name_scripts_pin_exact_capability_independent_of_catalog_order(reverse, selected_id):
    async def run():
        backend = ScriptBackend()
        context = AgentContext("p", "c", backend)
        provider = AgentToolProvider(backend, [execute_skill_script])
        items = [skill("scope_a"), skill("scope_b")]
        if reverse:
            items.reverse()
        await provider.prepare(context, items, {"scope_a", "scope_b"})
        assert context.allowed_skill_scripts == {("scope_a", "render"), ("scope_b", "render")}
        arguments = {"capability_id": selected_id, "skill_name": "same-name", "script_id": "render", "input_json": "{}"}
        await provider.ensure_call(RunContextWrapper(context), "runtime:execute_skill_script", arguments, "sdk-script")
        assert backend.begun[0]["skill_snapshot"] == {
            "capability_id": selected_id, "version": "1.0.0", "content_hash": f"hash-{selected_id}-1.0.0",
        }
    asyncio.run(run())


@pytest.mark.parametrize("case", ["ambiguous", "unknown_id", "wrong_name", "wrong_script", "unselected_id"])
def test_invalid_script_identity_never_registers_or_executes(case):
    async def run():
        backend = ScriptBackend()
        context = AgentContext("p", "c", backend)
        provider = AgentToolProvider(backend, [execute_skill_script])
        await provider.prepare(context, [skill("scope_a"), skill("scope_b")], {"scope_a"} if case == "unselected_id" else {"scope_a", "scope_b"})
        arguments = {"skill_name": "same-name", "script_id": "render", "input_json": "{}"}
        if case != "ambiguous":
            arguments["capability_id"] = "missing" if case == "unknown_id" else "scope_b"
        if case == "wrong_name": arguments["skill_name"] = "other-name"
        if case == "wrong_script": arguments["script_id"] = "other-script"
        with pytest.raises(AgentToolConfigurationError):
            await provider.ensure_call(RunContextWrapper(context), "runtime:execute_skill_script", arguments, "sdk-script")
        assert not backend.begun and not backend.started and not backend.executed
    asyncio.run(run())


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
@pytest.mark.parametrize("explicit,shared_script", [("omitted", False), ("empty", False), ("exact", False), ("exact", True)])
def test_sdk_script_approval_round_trip_keeps_original_arguments_and_package(explicit, shared_script, mode):
    async def run():
        backend = ScriptBackend()
        provider = AgentToolProvider(backend, [execute_skill_script])
        items = [skill("scope_a", "render"), skill("scope_b", "render" if shared_script else "review")]
        arguments = {"skill_name": "same-name", "script_id": "render", "input_json": '{"value":1}'}
        if explicit != "omitted": arguments["capability_id"] = "scope_a" if explicit == "exact" else ""
        model = StaticModel(ModelResponse(output=[ResponseFunctionToolCall(
            call_id="sdk-script", name="execute_skill_script", arguments=json.dumps(arguments), type="function_call",
        )], usage=Usage(requests=1), response_id=None))
        identity = ({"agent_turn_id": "turn"} if mode == "main" else
                    {"agent_task_attempt_id": "background-attempt", "attempt_token": "fixture-token"} if mode == "background" else
                    {"execution_attempt_id": "stateful-attempt", "attempt_token": "fixture-token"})
        context = AgentContext("p", "c", backend, **identity)
        prepared = await provider.prepare(context, items, {"scope_a", "scope_b"})
        async with prepared:
            agent = Agent(name="Script identity", model=model, tools=prepared.tools, tool_use_behavior="stop_on_first_tool")
            pending = await Runner.run(agent, "render", context=context, max_turns=2)
            assert len(pending.interruptions) == 1 and not backend.executed
            state_json, _, ids = _serialize_paused_run(pending)
            assert ids == ["sdk-script"]
        backend.approved = True
        restored_context = AgentContext("p", "c", backend, **identity)
        restored_prepared = await provider.prepare(restored_context, list(reversed(items)), {"scope_a", "scope_b"})
        async with restored_prepared:
            agent = agent.clone(tools=restored_prepared.tools)
            state = await RunState.from_json(agent, state_json, context_override=restored_context)
            for interruption in state.get_interruptions(): state.approve(interruption)
            result = await Runner.run(agent, state, context=restored_context, max_turns=2)
        assert not result.interruptions and '"status": "completed"' in result.final_output
        assert backend.executed == [{"capability_id": "scope_a", "version": "1.0.0", "content_hash": "hash-scope_a-1.0.0"}]
        assert len(backend.started) == len(backend.completed) == 1
        for key, value in identity.items(): assert backend.begun[0][key] == value
    asyncio.run(run())


def test_live_dynamic_activation_does_not_rebind_registered_script():
    async def run():
        backend = ScriptBackend()
        provider = AgentToolProvider(backend, [execute_skill_script])
        context = AgentContext("p", "c", backend)
        prepared = await provider.prepare(context, [skill("scope_a")], {"scope_a"})
        arguments = {"skill_name": "same-name", "script_id": "render", "input_json": "{}"}
        async with prepared:
            call = await provider.ensure_call(RunContextWrapper(context), "runtime:execute_skill_script", arguments, "sdk-script")
            await prepared.activate_skill(skill("scope_b"))
            await prepared.activate_skill(skill("scope_a", version="2.0.0"))
            backend.approved = True
            call["status"] = "approved"
            tool = prepared.tools[0]
            raw = json.dumps(arguments)
            result = await tool.on_invoke_tool(ToolContext(context, tool_name=tool.name, tool_call_id="sdk-script", tool_arguments=raw), raw)
            assert '"status": "completed"' in result
            assert backend.executed == [{"capability_id": "scope_a", "version": "1.0.0", "content_hash": "hash-scope_a-1.0.0"}]
            with pytest.raises(AgentToolConfigurationError):
                await provider.ensure_call(RunContextWrapper(context), "runtime:execute_skill_script", {**arguments, "capability_id": "scope_b"}, "sdk-script")
            assert len(backend.begun) == 1
    asyncio.run(run())
