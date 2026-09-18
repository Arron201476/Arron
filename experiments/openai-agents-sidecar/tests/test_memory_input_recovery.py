import asyncio
from hashlib import sha256
from types import SimpleNamespace

import pytest
from agents import Agent, RunConfig, Runner, function_tool

from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.memory_checkpoint import MemoryGenerationBinding, MemoryStageCheckpoint, checkpoint_memory_stage, restore_memory_stage
from content_agent_sidecar.memory_generation import FrozenMemoryInputPlan
from content_agent_sidecar.memory_input_plan import plan_memory_consolidation, memory_input_plan_payload, recover_memory_input_plan
from content_agent_sidecar.native_files import _go_json_hash
from test_memory_consolidation import fixture
from test_memory_native_lifecycle import workspace
from test_run_state_approval import SequenceModel, _text_response, _tool_call_response
from test_memory_generation_claim import raw_claim


@pytest.mark.parametrize("fault", [None, "plan", "checkpoint", "workspace", "hash", "state", "policy"])
def test_recovery_preserves_original_sdk_input_and_approval_without_replanning(fault):
    async def run():
        writes = []
        @function_tool(needs_approval=True)
        async def write_value(value: str) -> str:
            """Record the approved test write."""
            writes.append(value)
            return "written"
        _, _, rollout, source, options = await fixture()
        baseline = source.model_copy(update={"content_hash": _go_json_hash(source.files)})
        template = Agent(name="template", model=SequenceModel([_tool_call_response(), _text_response()]), tools=[write_value])
        plan = await plan_memory_consolidation(template, rollout, source, baseline, **options, extra_prompt="Original extra input")
        binding = MemoryGenerationBinding(generation_id="generation", phase="consolidation", project_id=source.project_id,
            user_id=source.user_id, source_hash=rollout.content_hash, base_version=baseline.version,
            base_hash=baseline.content_hash, model_id="configured", policy_hash="c" * 64)
        context = SimpleNamespace(native_workspace_checkpoint=workspace(), native_workspace=None)
        result = await Runner.run(plan.prepared.stage.agent, plan.prepared.stage.input, context=context,
                                  run_config=RunConfig(tracing_disabled=True))
        checkpoint = checkpoint_memory_stage(result, plan.prepared.stage, binding)
        claim = SimpleNamespace(job=SimpleNamespace(phase="consolidation", base_version=baseline.version,
            base_hash=baseline.content_hash, source_hash=rollout.content_hash), extraction=options["extraction"], checkpoint=checkpoint)
        payload = memory_input_plan_payload(claim, plan)
        claim.input_plan = FrozenMemoryInputPlan.model_validate(payload)
        claim.input_plan_hash = _go_json_hash(payload)
        if fault == "plan": claim.input_plan = None
        if fault == "checkpoint": claim.checkpoint = None
        if fault == "workspace": claim.checkpoint = checkpoint.model_copy(update={"native_workspace": None})
        if fault == "hash": claim.input_plan_hash = "f" * 64
        if fault == "state": claim.checkpoint = checkpoint.model_copy(update={"state_json": "{}"})
        if fault not in {None, "policy"}:
            with pytest.raises(BackendError): recover_memory_input_plan(template, claim)
            assert not writes
            return
        if fault == "policy": template = template.clone(instructions="Changed policy")
        recovered = recover_memory_input_plan(template, claim)
        assert recovered.files == plan.files
        assert recovered.prepared.stage.input == plan.prepared.stage.input
        assert not hasattr(recovered.prepared, "selection")
        if fault == "policy":
            with pytest.raises(ValueError):
                await restore_memory_stage(checkpoint, recovered.prepared.stage, binding, context=context)
            assert not writes
            return
        state = await restore_memory_stage(checkpoint, recovered.prepared.stage, binding, context=context)
        state.approve(state.get_interruptions()[0])
        completed = await Runner.run(recovered.prepared.stage.agent, state, run_config=RunConfig(tracing_disabled=True))
        assert completed.final_output is not None and writes == ["approved payload"]
    asyncio.run(run())


def test_production_recovery_never_replans_missing_frozen_inputs(raw_claim):
    from content_agent_sidecar.guardrails import SDKGuardrailPolicy
    from content_agent_sidecar.memory_execution import run_memory_consolidation
    from test_memory_execution import setup

    async def run():
        raw_claim["job"]["phase"] = "consolidation"
        raw_claim["extraction"] = {"rollout_slug": "fixture", "rollout_summary": "summary", "raw_memory": "private"}
        model = SequenceModel([])
        backend, claim, _, binding, context = setup(raw_claim, model)
        checkpoint = MemoryStageCheckpoint(binding=binding, stage_hash="d" * 64, state_json="{}",
            state_hash=sha256(b"{}").hexdigest(), native_workspace=workspace())
        claim = claim.model_copy(update={"checkpoint": checkpoint})
        async def snapshot(*args, **kwargs):
            pytest.fail("Recovery must not read a new baseline")
        backend.resolve_agent_memory_snapshot = snapshot
        with pytest.raises(BackendError, match="frozen input plan"):
            await run_memory_consolidation(backend, claim, "worker", Agent(name="template", model=model), binding,
                native_policy=SDKGuardrailPolicy(), context=context, run_config=RunConfig(tracing_disabled=True),
                cooperative_pause=True)
        assert backend.starts == 1
    asyncio.run(run())
