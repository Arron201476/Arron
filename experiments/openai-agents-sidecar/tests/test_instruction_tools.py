import asyncio
from copy import deepcopy
import hashlib
import json

import pytest
from agents import Agent, Runner, RunState, RunConfig
from agents.items import ModelResponse
from agents.usage import Usage
from agents.tool_context import ToolContext

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.contracts import AgentToolApprovalDecision
from content_agent_sidecar.instruction_tools import get_saved_instructions, update_saved_instructions
from content_agent_sidecar.managed_instructions import bind_managed_instructions, with_managed_instructions
from content_agent_sidecar.runtime import AgentContext, _serialize_paused_run, _apply_approval_decisions
from test_run_state_approval import DurableApprovalBackend, SequenceModel
from test_stateful_execution import tool_item, final_item
from test_managed_instructions import rules_snapshot


class RulesBackend(DurableApprovalBackend):
    def __init__(self):
        super().__init__()
        self.writes = []
        self.docs = deepcopy(rules_snapshot()["documents"])
        for doc in self.docs: doc["can_edit"] = True

    async def get_agent_tool_catalog(self):
        result = await super().get_agent_tool_catalog()
        template = result["tools"][0]
        result["tools"] = [{**template, "id": "runtime:"+name, "name": name, "access": access, "approval": approval, "max_result_bytes": 65536}
                           for name, access, approval in [("get_saved_instructions", "read", "never"), ("update_saved_instructions", "write", "always")]]
        return result

    async def resolve_agent_instruction_snapshot(self, project_id, key): return rules_snapshot(project_id, key)

    async def get_saved_instructions(self, project_id):
        return {"project_id": project_id, "workspace_id": "w", "applies_to": "new_executions", "documents": deepcopy(self.docs)}

    async def begin_agent_tool_call(self, **payload):
        call = await super().begin_agent_tool_call(**payload)
        if payload["tool_id"] == "runtime:get_saved_instructions": self.calls[payload["sdk_tool_call_id"]]["status"] = call["status"] = "approved"
        return call

    async def update_saved_instructions(self, call_id, sdk_id, arguments):
        assert self.calls[sdk_id]["status"] == "running"
        current = next(doc for doc in self.docs if doc["scope"] == arguments["scope"])
        assert current["version"] == arguments["expected_version"]
        current.update(content=arguments["content"], enabled=arguments["enabled"], version=current["version"]+1,
                       content_hash=hashlib.sha256(arguments["content"].encode()).hexdigest())
        self.writes.append(deepcopy(arguments))
        return deepcopy(current)


@pytest.mark.parametrize("mode", ["conversation", "background", "stateful"])
@pytest.mark.parametrize("action", ["approve", "reject"])
def test_native_sdk_instruction_author_approval_resume(mode, action):
    async def run():
        backend = RulesBackend()
        identity = {"agent_turn_id": "t"} if mode == "conversation" else {"agent_task_attempt_id": "b", "attempt_token": "token"} if mode == "background" else {"execution_attempt_id": "e", "attempt_token": "token"}
        context = AgentContext("p", "c", backend, **identity)
        await bind_managed_instructions(context)
        args = {"scope": "user", "expected_version": 1, "content": "Remember this explicit user preference", "enabled": True}
        model = SequenceModel([ModelResponse(output=items, usage=Usage(requests=1), response_id=f"rules-{index}") for index, items in enumerate(
            [[tool_item("get_saved_instructions", {}, "read")], [tool_item("update_saved_instructions", args, "write")], [final_item({"done": True})]])])
        provider = AgentToolProvider(backend, [get_saved_instructions, update_saved_instructions])
        prepared = await provider.prepare(context, [], set())
        async with prepared:
            agent = Agent(name="Author", instructions=with_managed_instructions("Base", context), model=model, tools=prepared.tools)
            result = await Runner.run(agent, "Remember my preference", context=context, run_config=RunConfig(tracing_disabled=True))
            assert len(result.interruptions) == 1 and not backend.writes
            state, _, pending = _serialize_paused_run(result)
            assert pending == ["write"] and state["context"]["context"]["managed_instruction_hash"] == context.managed_instruction_hash
        backend.calls["write"]["status"] = "approved" if action == "approve" else "rejected"
        context = AgentContext("p", "c", backend, **identity)
        await bind_managed_instructions(context, json.loads(json.dumps(state)))
        prepared = await provider.prepare(context, [], set())
        async with prepared:
            agent = Agent(name="Author", instructions=with_managed_instructions("Base", context), model=model, tools=prepared.tools)
            restored = await RunState.from_json(agent, state, context_override=context, strict_context=True)
            _apply_approval_decisions(restored, [AgentToolApprovalDecision(sdk_tool_call_id="write", action=action)])
            result = await Runner.run(agent, restored, context=context, run_config=RunConfig(tracing_disabled=True))
            assert not result.interruptions
        assert len(backend.writes) == (1 if action == "approve" else 0)
        assert "RULE_USER_V1" in with_managed_instructions("Base", context)
        assert not model.responses
    asyncio.run(run())


def test_instruction_tools_reject_unbound_write_and_foreign_read():
    async def run():
        backend = RulesBackend()
        context = AgentContext("p", "c", backend, agent_turn_id="t")
        await bind_managed_instructions(context)
        args = {"scope": "user", "expected_version": 1, "content": "x", "enabled": True}
        tool_context = ToolContext(context=context, tool_name="update_saved_instructions", tool_call_id="unbound", tool_arguments=json.dumps(args))
        result = await update_saved_instructions.on_invoke_tool(tool_context, json.dumps(args))
        assert "error" in result.lower() and not backend.writes
        context.active_tool_calls["unbound"] = {"agent_tool_call_id": "audited-call"}
        context.managed_instructions = None
        result = await update_saved_instructions.on_invoke_tool(tool_context, json.dumps(args))
        assert "error" in result.lower() and not backend.writes
        await bind_managed_instructions(context)
        backend.docs[1]["scope_ref"] = "foreign-user"
        result = await get_saved_instructions.on_invoke_tool(tool_context, "{}")
        assert "error" in result.lower() and "RULE_USER_V1" not in result
    asyncio.run(run())
