import asyncio
import json

import pytest
from agents import Agent, RunConfig, Runner, RunState, function_tool
from agents.sandbox.memory.interface import RolloutExtractionArtifacts
from agents.sandbox.memory.storage import PhaseTwoInputSelection

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.contracts import AgentToolApprovalDecision
from content_agent_sidecar.native_memory import (
    extraction_has_memory, memory_consolidation_stage, memory_extraction_stage,
)
from content_agent_sidecar.runtime import AgentContext, _apply_approval_decisions, _serialize_paused_run
from test_run_state_approval import DurableApprovalBackend, SequenceModel, _text_response, _tool_call_response


def stage_for(template, stage):
    if stage == "extraction":
        return memory_extraction_stage(template, json.dumps({"terminal_metadata": {}, "input": "fixture"}))
    return memory_consolidation_stage(template, "memories", PhaseTwoInputSelection([], set(), []))


@pytest.mark.parametrize("stage", ["extraction", "consolidation"])
@pytest.mark.parametrize("decision", ["approve", "reject"])
def test_memory_stage_preserves_runtime_approval_after_sdk_reconstruction(stage, decision):
    async def run():
        writes = []

        @function_tool
        async def write_value(value: str) -> str:
            """Write a confirmed fixture memory value."""
            writes.append(value)
            return "written"

        backend = DurableApprovalBackend()
        provider = AgentToolProvider(backend, [write_value])
        context = AgentContext("project-1", "conversation-1", backend, execution_attempt_id="memory-execution", attempt_token="private-token")
        final = _text_response()
        if stage == "extraction":
            final.output[0].content[0].text = json.dumps({"rollout_slug": "fixture", "rollout_summary": "Confirmed summary", "raw_memory": "Confirmed memory"})
        model = SequenceModel([_tool_call_response(), final])
        prepared = await provider.prepare(context, [], set())
        async with prepared:
            template = Agent(name="Platform memory", model=model, tools=prepared.tools, instructions="Do not expose credentials.")
            memory_stage = stage_for(template, stage)
            assert "Do not expose credentials." in memory_stage.agent.instructions
            assert memory_stage.agent.tools == template.tools
            pending = await Runner.run(memory_stage.agent, memory_stage.input, context=context, run_config=RunConfig(tracing_disabled=True))
            saved, _, pending_ids = _serialize_paused_run(pending)
        assert not writes and pending_ids == ["sdk-write-1"]
        assert "private-token" not in json.dumps(saved)
        backend.calls["sdk-write-1"]["status"] = "approved" if decision == "approve" else "rejected"
        restored_context = AgentContext("project-1", "conversation-1", backend, execution_attempt_id="memory-execution", attempt_token="rotated-token")
        restored_tools = await provider.prepare(restored_context, [], set())
        async with restored_tools:
            restored_stage = stage_for(template.clone(tools=restored_tools.tools), stage)
            restored = await RunState.from_json(restored_stage.agent, saved, context_override=restored_context)
            _apply_approval_decisions(restored, [AgentToolApprovalDecision(sdk_tool_call_id="sdk-write-1", action=decision)])
            result = await Runner.run(restored_stage.agent, restored, context=restored_context, run_config=RunConfig(tracing_disabled=True))
        assert not result.interruptions
        assert writes == (["approved payload"] if decision == "approve" else [])
        assert backend.started == backend.completed == (1 if decision == "approve" else 0)
        if stage == "extraction":
            assert extraction_has_memory(result.final_output)
        assert not model.responses

    asyncio.run(run())


@pytest.mark.parametrize("root", ["", ".", "../memories", "/memories", "C:/memories", "a\\memories"])
def test_memory_rejects_unsafe_layout(root):
    with pytest.raises(ValueError, match="relative"):
        memory_consolidation_stage(Agent(name="fixture", model="configured"), root, PhaseTwoInputSelection([], set(), []))


def test_memory_extraction_keeps_sdk_empty_and_partial_contract():
    assert not extraction_has_memory(RolloutExtractionArtifacts(rollout_slug="", rollout_summary="", raw_memory=""))
    with pytest.raises(ValueError, match="partially-empty"):
        extraction_has_memory(RolloutExtractionArtifacts(rollout_slug="fixture", rollout_summary="", raw_memory="content"))
    with pytest.raises(ValueError, match="SDK output contract"):
        extraction_has_memory({"raw_memory": "unvalidated"})


def test_memory_does_not_fall_back_to_sdk_default_model():
    with pytest.raises(ValueError, match="explicitly configured model"):
        stage_for(Agent(name="unconfigured"), "extraction")


@pytest.mark.parametrize("slug", ["../private", "UPPER", "invalid space", "a" * 81, "bad.md.md"])
def test_memory_rejects_slug_before_it_can_poison_consolidation(slug):
    with pytest.raises(ValueError, match="SDK storage") as caught:
        extraction_has_memory(RolloutExtractionArtifacts(rollout_slug=slug, rollout_summary="s", raw_memory="m"))
    assert slug not in str(caught.value) and caught.value.__cause__ is None


@pytest.mark.parametrize("slug", ["valid", " valid.md ", "\x1cvalid\x1f"])
def test_memory_accepts_native_sdk_slug_normalization(slug):
    assert extraction_has_memory(RolloutExtractionArtifacts(rollout_slug=slug, rollout_summary="s", raw_memory="m"))


@pytest.mark.parametrize("stage", ["extraction", "consolidation"])
def test_memory_model_failure_is_not_a_successful_stage(stage):
    async def run():
        class FailedModel(SequenceModel):
            async def get_response(self, *args, **kwargs):
                raise RuntimeError("memory provider failed")

        prepared = stage_for(Agent(name="fixture", model=FailedModel([])), stage)
        with pytest.raises(RuntimeError, match="memory provider failed"):
            await Runner.run(prepared.agent, prepared.input, run_config=RunConfig(tracing_disabled=True))

    asyncio.run(run())


def test_memory_does_not_inherit_business_handoffs_or_dynamic_policy():
    with pytest.raises(ValueError, match="business handoffs"):
        stage_for(Agent(name="fixture", model="configured", handoffs=[Agent(name="business")]), "extraction")
    with pytest.raises(ValueError, match="frozen"):
        stage_for(Agent(name="fixture", model="configured", instructions=lambda *_args: "mutable"), "extraction")
