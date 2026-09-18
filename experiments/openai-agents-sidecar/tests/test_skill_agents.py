from __future__ import annotations

import asyncio
import json
from types import SimpleNamespace
from typing import Any

import pytest
from agents import Agent, RunConfig, Runner, RunState, function_tool
from agents.items import ModelResponse
from agents.usage import Usage
from openai.types.responses import ResponseFunctionToolCall

from content_agent_sidecar.skill_agents import attach_skill_handoffs
from content_agent_sidecar.contracts import AgentToolApprovalDecision
from content_agent_sidecar.runtime import AgentContext, _apply_approval_decisions, _serialize_paused_run
from test_agent_tools import StaticModel
from test_sdk_guardrails import message


def definition(mode: str = "inline") -> dict[str, Any]:
    return {"capability_id": "outline_critic", "version": "1.0.0", "label": "Outline critic", "description": "Diagnose outlines", "status": "available", "execution_mode": mode, "skill": {"name": "outline-critic"}}


def test_sdk_handoff_is_dynamic_lazy_and_preserves_the_same_context() -> None:
    loaded = []
    context = SimpleNamespace(capability_versions={"outline_critic": "1.0.0"}, project_id="same-project")

    async def load(actual: Any, capability_id: str) -> str:
        assert actual is context
        loaded.append(capability_id)
        return "PINNED_DIAGNOSTIC_INSTRUCTIONS"

    class HandoffModel(StaticModel):
        seen_instructions = []

        async def get_response(self, *args: Any, **kwargs: Any) -> ModelResponse:
            self.seen_instructions.append(kwargs.get("system_instructions"))
            if handoffs := kwargs.get("handoffs"):
                return ModelResponse(output=[ResponseFunctionToolCall(id="fc_handoff", type="function_call", call_id="sdk_handoff", name=handoffs[0].tool_name, arguments="{}")], usage=Usage(), response_id="resp_handoff")
            return message("diagnosis completed")

    model = HandoffModel(message("unused"))
    agent = attach_skill_handoffs(Agent(name="Parent", instructions="Platform contract", model=model), context, [definition()], {"outline_critic"}, load)
    assert len(agent.handoffs) == 1 and loaded == []
    run = asyncio.run(Runner.run(agent, "Diagnose", context=context, run_config=RunConfig(tracing_disabled=True)))
    assert run.final_output == "diagnosis completed"
    assert run.context_wrapper.context is context
    assert loaded == ["outline_critic"]
    assert "PINNED_DIAGNOSTIC_INSTRUCTIONS" in model.seen_instructions[-1]
    assert "Platform contract" in model.seen_instructions[-1]
    assert run.last_agent.name != "Parent"


def test_handoff_only_exposes_selected_inline_versions() -> None:
    context = SimpleNamespace(capability_versions={"outline_critic": "1.0.0"})

    async def load(*_args: Any) -> str:
        return ""

    parent = Agent(name="Parent", instructions="Platform", model=StaticModel(message("ok")))
    assert not attach_skill_handoffs(parent, context, [definition()], set(), load).handoffs
    assert not attach_skill_handoffs(parent, context, [definition("stateful_workflow")], {"outline_critic"}, load).handoffs
    context.capability_versions["outline_critic"] = "2.0.0"
    with pytest.raises(ValueError, match="pinned"):
        attach_skill_handoffs(parent, context, [definition()], {"outline_critic"}, load)


@pytest.mark.parametrize("action", ["approve", "reject"])
def test_handoff_child_resumes_native_approval_after_graph_reconstruction(action: str) -> None:
    writes: list[str] = []
    loaded: list[AgentContext] = []

    @function_tool(needs_approval=True)
    async def save_diagnosis(value: str) -> str:
        """Save the completed diagnosis."""
        writes.append(value)
        return "saved"

    class ApprovalModel(StaticModel):
        def __init__(self, *, resumed: bool = False):
            super().__init__(message("unused"))
            self.resumed = resumed

        async def get_response(self, *args: Any, **kwargs: Any) -> ModelResponse:
            if handoffs := kwargs.get("handoffs"):
                assert not self.resumed, "restored run started at root instead of its Skill child"
                return ModelResponse(output=[ResponseFunctionToolCall(type="function_call", call_id="handoff-before-approval", name=handoffs[0].tool_name, arguments="{}")], usage=Usage(), response_id=None)
            assert "pinned diagnosis 1.0.0" in kwargs["system_instructions"]
            items = kwargs.get("input", [])
            if any(isinstance(item, dict) and item.get("type") == "function_call_output" and item.get("call_id") == "save-diagnosis" for item in items):
                return message("diagnosis complete")
            assert not self.resumed, "restored approval replayed the model's write request"
            return ModelResponse(output=[ResponseFunctionToolCall(type="function_call", call_id="save-diagnosis", name="save_diagnosis", arguments='{"value":"diagnosis"}')], usage=Usage(), response_id=None)

    def build(context: AgentContext, model: ApprovalModel) -> Agent:
        async def load(actual: Any, capability_id: str) -> str:
            assert actual is context and capability_id == "outline_critic"
            loaded.append(actual)
            return "pinned diagnosis 1.0.0"
        return attach_skill_handoffs(Agent(name="Root", instructions="Platform contract", model=model, tools=[save_diagnosis]), context, [definition()], {"outline_critic"}, load)

    async def execute() -> None:
        original_context = AgentContext(project_id="project", conversation_id="conversation", backend=SimpleNamespace(), capability_versions={"outline_critic": "1.0.0"})
        original = build(original_context, ApprovalModel())
        pending = await Runner.run(original, "Diagnose and save", context=original_context, run_config=RunConfig(tracing_disabled=True))
        assert len(pending.interruptions) == 1 and writes == []
        assert pending.last_agent.name.startswith("skill_")
        checkpoint, _schema, pending_ids = _serialize_paused_run(pending)
        checkpoint = json.loads(json.dumps(checkpoint))
        assert pending_ids == ["save-diagnosis"]

        fresh_context = AgentContext(project_id="project", conversation_id="conversation", backend=SimpleNamespace(), capability_versions={"outline_critic": "1.0.0"})
        rebuilt = build(fresh_context, ApprovalModel(resumed=True))
        state = await RunState.from_json(rebuilt, checkpoint, context_override=fresh_context, strict_context=True)
        _apply_approval_decisions(state, [AgentToolApprovalDecision(sdk_tool_call_id="save-diagnosis", action=action)])
        result = await Runner.run(rebuilt, state, context=fresh_context, run_config=RunConfig(tracing_disabled=True))
        assert result.final_output == "diagnosis complete"
        assert result.last_agent.name == pending.last_agent.name
        assert result.context_wrapper.context is fresh_context
        assert loaded == [original_context, fresh_context]
        assert writes == (["diagnosis"] if action == "approve" else [])

    asyncio.run(execute())
