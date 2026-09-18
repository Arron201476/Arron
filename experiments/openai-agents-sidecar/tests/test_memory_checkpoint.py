import asyncio
from dataclasses import replace
import json

import pytest
from agents import Agent, ModelSettings, RunConfig, Runner, function_tool

from content_agent_sidecar.memory_checkpoint import (
    MemoryGenerationBinding, MemoryStageCheckpoint, checkpoint_memory_stage, restore_memory_stage,
)
from content_agent_sidecar.native_memory import extraction_has_memory
from test_native_memory import stage_for
from test_run_state_approval import SequenceModel, _text_response, _tool_call_response
from test_stateful_execution import StreamingSequence


def binding(phase):
    return MemoryGenerationBinding(generation_id="job-1", phase=phase, project_id="project-1",
                                   user_id="owner-1", source_hash="a" * 64, base_version=1,
                                   base_hash="b" * 64, model_id="configured", policy_hash="c" * 64)


@pytest.mark.parametrize("phase", ["extraction", "consolidation"])
def test_user_pause_restores_sdk_turn_boundary_without_replaying_completed_tool(phase):
    async def run():
        writes = []
        @function_tool
        async def write_value(value: str) -> str:
            """Write one test value."""
            writes.append(value)
            result.cancel(mode="after_turn")
            return "written"
        final = _text_response()
        if phase == "extraction":
            final.output[0].content[0].text = json.dumps({"rollout_slug": "fixture", "rollout_summary": "summary", "raw_memory": "memory"})
        model = StreamingSequence([_tool_call_response().output, final.output])
        stage = stage_for(Agent(name="template", model=model, tools=[write_value]), phase)
        result = Runner.run_streamed(stage.agent, stage.input, context={"attempt_token": "OLD_PRIVATE_TOKEN"},
                                     run_config=RunConfig(tracing_disabled=True))
        async for _ in result.stream_events():
            pass
        if result.run_loop_task is not None:
            await result.run_loop_task
        assert result.final_output is None and not result.interruptions
        assert writes == ["approved payload"]
        with pytest.raises(ValueError, match="paused"):
            checkpoint_memory_stage(result, stage, binding(phase))
        checkpoint = checkpoint_memory_stage(result, stage, binding(phase), user_requested=True)
        assert checkpoint.pause_kind == "user" and "OLD_PRIVATE_TOKEN" not in checkpoint.model_dump_json()
        loaded = MemoryStageCheckpoint.model_validate_json(checkpoint.model_dump_json())
        context = {"attempt_token": "NEW_PRIVATE_TOKEN"}
        restored = await restore_memory_stage(loaded, stage, binding(phase), context=context)
        completed = Runner.run_streamed(stage.agent, restored, run_config=RunConfig(tracing_disabled=True))
        async for _ in completed.stream_events():
            pass
        if completed.run_loop_task is not None:
            await completed.run_loop_task
        assert completed.final_output is not None and writes == ["approved payload"]
        assert completed.context_wrapper.context is context and not model.responses
    asyncio.run(run())


async def paused(phase):
    writes = []

    @function_tool(needs_approval=True)
    async def write_value(value: str) -> str:
        """Write a confirmed memory fixture."""
        writes.append(value)
        return "written"

    final = _text_response()
    if phase == "extraction":
        final.output[0].content[0].text = json.dumps({"rollout_slug": "fixture", "rollout_summary": "summary", "raw_memory": "memory"})
    model = SequenceModel([_tool_call_response(), final])
    stage = stage_for(Agent(name="template", model=model, tools=[write_value]), phase)
    result = await Runner.run(stage.agent, stage.input, context={"attempt_token": "OLD_PRIVATE_TOKEN"},
                              run_config=RunConfig(tracing_disabled=True))
    return stage, result, model, writes


@pytest.mark.parametrize("phase", ["extraction", "consolidation"])
@pytest.mark.parametrize("approve", [True, False])
def test_checkpoint_native_sdk_restores_pending_approval_without_old_credentials(phase, approve):
    async def run():
        stage, result, model, writes = await paused(phase)
        checkpoint = checkpoint_memory_stage(result, stage, binding(phase))
        raw = checkpoint.model_dump_json()
        assert "OLD_PRIVATE_TOKEN" not in raw and not writes
        reloaded = MemoryStageCheckpoint.model_validate_json(raw)
        context = {"attempt_token": "NEW_PRIVATE_TOKEN"}
        restored = await restore_memory_stage(reloaded, stage, binding(phase), context=context)
        pending = restored.get_interruptions()
        assert len(pending) == 1 and pending[0].call_id == "sdk-write-1"
        if approve:
            restored.approve(pending[0])
        else:
            restored.reject(pending[0])
        complete = await Runner.run(stage.agent, restored, run_config=RunConfig(tracing_disabled=True))
        assert writes == (["approved payload"] if approve else [])
        assert not complete.interruptions and not model.responses
        assert complete.context_wrapper.context is context
        if phase == "extraction":
            assert extraction_has_memory(complete.final_output)
        with pytest.raises(ValueError, match="paused"):
            checkpoint_memory_stage(complete, stage, binding(phase))

    asyncio.run(run())


@pytest.mark.parametrize("field,value", [
    ("generation_id", "different"), ("phase", "extraction"), ("project_id", "other-project"),
    ("user_id", "other-owner"), ("source_hash", "d" * 64), ("base_version", 2),
    ("base_hash", "d" * 64), ("model_id", "other-model"), ("policy_hash", "d" * 64),
])
def test_checkpoint_rejects_changed_generation_before_sdk_resume(field, value):
    async def run():
        stage, result, model, writes = await paused("consolidation")
        original = binding("consolidation")
        checkpoint = checkpoint_memory_stage(result, stage, original)
        changed = original.model_copy(update={field: value})
        with pytest.raises(ValueError, match="admitted generation"):
            await restore_memory_stage(checkpoint, stage, changed, context={})
        assert not writes and len(model.responses) == 1

    asyncio.run(run())


def test_checkpoint_rejects_input_policy_corruption_and_missing_runtime_context():
    async def run():
        stage, result, model, writes = await paused("consolidation")
        original = binding("consolidation")
        checkpoint = checkpoint_memory_stage(result, stage, original)
        for changed in (replace(stage, input="different input"),
                        replace(stage, agent=stage.agent.clone(instructions="different policy")),
                        replace(stage, agent=stage.agent.clone(tools=[])),
                        replace(stage, agent=stage.agent.clone(output_type=int)),
                        replace(stage, agent=stage.agent.clone(model_settings=ModelSettings(temperature=0.5)))):
            with pytest.raises(ValueError, match="admitted generation"):
                await restore_memory_stage(checkpoint, changed, original, context={})
        with pytest.raises(ValueError, match="integrity"):
            await restore_memory_stage(checkpoint.model_copy(update={"state_json": "{}"}), stage, original, context={})
        with pytest.raises(ValueError, match="admitted generation"):
            await restore_memory_stage(checkpoint, stage, original, context=None)
        assert not writes and len(model.responses) == 1

    asyncio.run(run())
