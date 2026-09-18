"""One admitted SDK memory stage under a supervised scheduler lease."""

import asyncio
from dataclasses import dataclass, field, replace
from datetime import datetime, timezone
from hashlib import sha256
import json

from agents import Runner
from agents.sandbox.memory.phase_one import render_phase_one_prompt
from agents.sandbox.memory.storage import PhaseTwoInputSelection

from .backend import BackendError, backend_memory_activity
from .memory_checkpoint import MemoryGenerationBinding, _stage_hash, checkpoint_memory_stage, restore_memory_stage
from .memory_generation import MemoryGenerationJob, MemoryGenerationCompletion
from .memory_approval import restore_memory_approvals
from .native_lease import NativeWorkspaceLeaseGuard
from .native_memory import extraction_has_memory, persist_memory_extraction, memory_consolidation_stage
from .native_runner import native_run_config, settle_native_stream
from .memory_workspace import prepare_memory_stage
from .guardrails import SDKGuardrailPolicy
from .managed_memory import MemorySnapshot, resolve_memory_snapshot
from .memory_input_plan import plan_memory_consolidation, persist_memory_input_plan, recover_memory_input_plan
from .native_manifest import MEMORY_DIRECTORY


@dataclass(frozen=True)
class MemoryExtractionCompletion:
    """Exactly one confirmed pause or extraction receipt, never a raw SDK result."""

    pause: MemoryGenerationJob | None = None
    extraction: dict | None = None


@dataclass(frozen=True)
class MemoryConsolidationRun:
    result: object = field(repr=False)
    stage: object = field(repr=False)
    input_plan: object = field(repr=False)
    input_receipt: dict
    pause: MemoryGenerationJob | None = None
    completion: MemoryGenerationCompletion | None = None


async def run_memory_consolidation(backend, claim, worker_id, template, binding, *, native_policy,
                                    max_raw_memories=256, extra_prompt=None, **options):
    """Run consolidation through a confirmed approval pause or publication completion."""
    if (claim.job.phase != "consolidation" or claim.extraction is None or not isinstance(native_policy, SDKGuardrailPolicy)
            or type(max_raw_memories) is not int or not 1 <= max_raw_memories <= 256):
        raise BackendError("Memory consolidation requires its extraction and private native policy")
    stage = memory_consolidation_stage(template, MEMORY_DIRECTORY, PhaseTwoInputSelection([], set(), []), extra_prompt=extra_prompt)
    return await _run_memory_stage(backend, claim, worker_id, stage, binding, native_policy=native_policy,
        consolidation_template=template, max_raw_memories=max_raw_memories, extra_prompt=extra_prompt, **options)


async def execute_memory_extraction(backend, claim, worker_id, stage, binding, **options):
    """Run one extraction attempt through its durable pause or phase transition."""
    if claim.job.phase != "extraction":
        raise BackendError("Memory extraction worker cannot consume another phase")
    return await _run_memory_stage(backend, claim, worker_id, stage, binding, persist_extraction=True, **options)


async def run_memory_stage(backend, claim, worker_id, stage, binding, **options):
    """Return the actual SDK result; the caller must durably checkpoint or commit it."""
    return await _run_memory_stage(backend, claim, worker_id, stage, binding, persist_extraction=False, **options)


class _MemoryLeaseTransport:
    def __init__(self, backend, claim, worker_id, job, lease_seconds):
        self.backend, self.claim, self.worker_id = backend, claim, worker_id
        self.lease, self.lease_seconds = job, lease_seconds
        self.minimum_window = 0
        self.pause_requested = asyncio.Event()

    def require_window(self):
        until = datetime.fromisoformat(self.lease.lease_until.replace("Z", "+00:00"))
        if (until - datetime.now(timezone.utc)).total_seconds() <= self.minimum_window:
            raise BackendError("Memory lease has insufficient confirmed renewal time")

    async def renew(self):
        self.lease = await self.backend.renew_memory_generation(self.claim, self.worker_id, self.lease, self.lease_seconds)
        self.require_window()
        if self.lease.error_code == "MEMORY_GENERATION_PAUSE_REQUESTED":
            self.pause_requested.set()


async def _confirm_memory_completion(backend, claim, worker_id, timeout_seconds):
    # The server retains the original attempt identity for exact lost-response
    # retries. Never replay the model or publication to confirm this transaction.
    for attempt in range(2):
        try:
            async with asyncio.timeout(timeout_seconds):
                return await backend.complete_memory_generation(claim, worker_id)
        except TimeoutError:
            if attempt == 1:
                raise BackendError("Memory stage persistence outcome is unconfirmed",
                                   code="MEMORY_GENERATION_COMMIT_UNCONFIRMED") from None


async def _run_memory_stage(backend, claim, worker_id, stage, binding: MemoryGenerationBinding, *, context,
                           run_config, lease_seconds=60, interval_seconds=10, request_timeout_seconds=10,
                           max_turns=10, persist_extraction=False, native_policy=None, input_plan=None, input_receipt=None,
                           consolidation_template=None, max_raw_memories=256, extra_prompt=None, cooperative_pause=False):
    expected = MemoryGenerationBinding(generation_id=claim.job.generation_id, phase=claim.job.phase,
        project_id=claim.job.project_id, user_id=claim.job.user_id, source_hash=claim.job.source_hash,
        base_version=claim.job.base_version, base_hash=claim.job.base_hash, model_id=claim.job.model_id,
        policy_hash=binding.policy_hash)
    if binding != expected or type(lease_seconds) is not int or not 30 <= lease_seconds <= 1800 or type(max_turns) is not int or not 1 <= max_turns <= 100:
        raise BackendError("Memory execution configuration does not match its claim")
    if (input_plan is not None or input_receipt is not None) and (native_policy is None or input_plan is None or input_receipt is None):
        raise BackendError("Memory input initialization requires its native policy and complete receipt")
    if native_policy is not None and (not isinstance(native_policy, SDKGuardrailPolicy)
                                     or getattr(context, "native_workspace", None) is not None):
        raise BackendError("Memory stage requires one private workspace preparation owner")
    if native_policy is not None and claim.checkpoint is not None and claim.checkpoint.native_workspace is None:
        raise BackendError("Memory recovery cannot add a workspace absent from its checkpoint")
    if native_policy is not None and getattr(context, "native_workspace_checkpoint", None) is not None:
        expected_workspace = claim.checkpoint.native_workspace if claim.checkpoint is not None else None
        if context.native_workspace_checkpoint != expected_workspace:
            raise BackendError("Memory preparation cannot replace another workspace checkpoint")
    stage_hash = _stage_hash(stage, binding)
    if claim.checkpoint is not None and (native_policy is None and claim.checkpoint.stage_hash != stage_hash or claim.checkpoint.binding != binding):
        raise BackendError("Memory recovery differs from its original SDK stage")
    if native_policy is None and claim.checkpoint is not None and getattr(context, "native_workspace_checkpoint", None) != claim.checkpoint.native_workspace:
        raise BackendError("Memory recovery differs from its original native workspace")
    if (native_policy is None and claim.checkpoint is not None and claim.checkpoint.native_workspace is not None
            and getattr(context, "native_workspace", None) is None):
        raise BackendError("Memory native recovery requires its prepared workspace owner")
    if claim.job.phase == "extraction" and stage.input != render_phase_one_prompt(rollout_contents=claim.source.rollout_jsonl):
        raise BackendError("Memory extraction input differs from its frozen source")
    identity = {"project_id": claim.job.project_id, "conversation_id": claim.conversation_id,
                "memory_generation_id": claim.job.generation_id, "memory_generation_attempt": claim.job.attempt,
                "attempt_token": claim.attempt_token}
    if (getattr(context, "backend", None) is not backend or any(getattr(context, name, None) != value for name, value in identity.items())
            or any(getattr(context, name, "") for name in ("agent_turn_id", "agent_task_attempt_id", "execution_attempt_id", "skill_invocation_id"))):
        raise BackendError("Memory Runner context differs from its admitted attempt")
    # Construct before admission so invalid guard timing cannot consume a start.
    transport = _MemoryLeaseTransport(backend, claim, worker_id, None, lease_seconds)
    guard = NativeWorkspaceLeaseGuard(transport, interval_seconds=interval_seconds, request_timeout_seconds=request_timeout_seconds)
    transport.minimum_window = interval_seconds + request_timeout_seconds + 1
    config = replace(run_config, trace_include_sensitive_data=False)
    transport.lease = await backend.start_memory_generation(claim, worker_id)
    transport.require_window()
    try:
        # The renewal task inherits the scheduler context, not delegated headers.
        async with guard:
            if consolidation_template is not None:
                if claim.checkpoint is not None:
                    input_plan = recover_memory_input_plan(consolidation_template, claim)
                else:
                    with backend_memory_activity(claim.job.project_id, claim.job.generation_id, claim.job.attempt, claim.attempt_token):
                        baseline = await resolve_memory_snapshot(context)
                    if (baseline.version != claim.job.base_version or baseline.current_version != claim.job.base_version or baseline.content_hash != claim.job.base_hash
                            or baseline.user_id != claim.job.user_id or baseline.workspace_id != claim.source.workspace_id):
                        raise BackendError("Memory consolidation baseline changed before preparation")
                    # Only original source identity is needed here; no historical
                    # memory body is fabricated or loaded from a different version.
                    source = MemorySnapshot(activity_key=claim.source.activity_key, workspace_id=claim.source.workspace_id,
                        project_id=claim.source.project_id, user_id=claim.source.user_id, version=claim.source.memory_version,
                        current_version=claim.source.memory_version, content_hash=claim.source.memory_hash,
                        read_enabled=claim.source.read_enabled, files={})
                    encoded = json.dumps(claim.extraction.model_dump(), ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()
                    input_plan = await plan_memory_consolidation(consolidation_template, claim.source, source, baseline,
                        segment_id=claim.job.segment_id, extraction=claim.extraction, extraction_hash=sha256(encoded).hexdigest(),
                        session_id=claim.conversation_id, max_raw_memories=max_raw_memories, extra_prompt=extra_prompt)
                try:
                    async with asyncio.timeout(request_timeout_seconds):
                        input_receipt = await persist_memory_input_plan(backend, claim, worker_id, input_plan)
                except TimeoutError:
                    raise BackendError("Memory consolidation input persistence is unconfirmed",
                                       code="MEMORY_GENERATION_INPUTS_UNCONFIRMED") from None
                stage = input_plan.prepared.stage
                guard.require_confirmed()
            with backend_memory_activity(claim.job.project_id, claim.job.generation_id, claim.job.attempt, claim.attempt_token):
                async with prepare_memory_stage(stage, claim, context, native_policy, input_plan=input_plan, input_receipt=input_receipt) as prepared_stage:
                    stage = prepared_stage
                    _stage_hash(stage, binding)
                    runner_input = (await restore_memory_stage(claim.checkpoint, stage, binding, context=context)
                                    if claim.checkpoint is not None else stage.input)
                    if claim.checkpoint is not None:
                        await restore_memory_approvals(runner_input, backend, context, phase=claim.job.phase)
                    async with native_run_config(context, config, runner_input) as native_config:
                        if cooperative_pause:
                            result = Runner.run_streamed(stage.agent, runner_input, context=context, run_config=native_config, max_turns=max_turns)
                            async def pause_at_boundary():
                                await transport.pause_requested.wait()
                                result.cancel(mode="after_turn")
                            watcher = asyncio.create_task(pause_at_boundary())
                            cancelled = False
                            try:
                                async for _ in result.stream_events():
                                    pass
                            except BaseException:
                                result.cancel()
                                cancelled = True
                                raise
                            finally:
                                watcher.cancel()
                                await asyncio.gather(watcher, return_exceptions=True)
                                if result.run_loop_task is not None:
                                    async with asyncio.timeout(30):
                                        await asyncio.gather(result.run_loop_task, return_exceptions=True)
                                await settle_native_stream(context, result, cancelled=cancelled)
                        else:
                            result = await Runner.run(stage.agent, runner_input, context=context, run_config=native_config, max_turns=max_turns)
                            await settle_native_stream(context, result)
                guard.require_confirmed()
            if persist_extraction or consolidation_template is not None:
                user_pause = cooperative_pause and transport.pause_requested.is_set() and result.final_output is None
                if user_pause:
                    result.cancel(mode="after_turn")
                if persist_extraction and not result.interruptions and not user_pause:
                    extraction_has_memory(result.final_output)
                # The terminal transaction revokes the lease. Stop renewal first
                # so its expected rejection cannot cancel a confirmed commit.
                await guard.pause()
                transport.require_window()
                if consolidation_template is not None and not result.interruptions and not user_pause:
                    completion = await _confirm_memory_completion(backend, claim, worker_id, request_timeout_seconds)
                    return MemoryConsolidationRun(result, stage, input_plan, input_receipt, completion=completion)
                try:
                    async with asyncio.timeout(request_timeout_seconds):
                        if result.interruptions or user_pause:
                            paused = await persist_memory_pause(backend, claim, worker_id, result, stage, binding, user_requested=user_pause)
                            if consolidation_template is not None:
                                return MemoryConsolidationRun(result, stage, input_plan, input_receipt, pause=paused)
                            return MemoryExtractionCompletion(pause=paused)
                        receipt = await persist_memory_extraction(backend, generation_id=claim.job.generation_id,
                            worker_id=worker_id, attempt_token=claim.attempt_token, attempt=claim.job.attempt,
                            output=result.final_output)
                        return MemoryExtractionCompletion(extraction=receipt)
                except TimeoutError:
                    raise BackendError("Memory stage persistence outcome is unconfirmed",
                                       code="MEMORY_GENERATION_COMMIT_UNCONFIRMED") from None
            else:
                return result
    except BackendError as exc:
        if exc.code == "NATIVE_WORKSPACE_LEASE_UNCONFIRMED":
            raise BackendError("Memory generation lease renewal was not confirmed", code="MEMORY_GENERATION_LEASE_UNCONFIRMED") from None
        raise


async def persist_memory_pause(backend, claim, worker_id, result, stage, binding, *, user_requested=False):
    checkpoint = checkpoint_memory_stage(result, stage, binding, user_requested=user_requested)
    if any(getattr(binding, name) != getattr(claim.job, name) for name in type(binding).model_fields if name != "policy_hash"):
        raise BackendError("Memory pause differs from its original claim")
    if claim.job.phase == "extraction" and stage.input != render_phase_one_prompt(rollout_contents=claim.source.rollout_jsonl):
        raise BackendError("Memory pause input differs from its frozen source")
    value = checkpoint.model_dump()
    digest = sha256(json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()).hexdigest()
    receipt = await backend.memory_generation_request("pause", {"generation_id": claim.job.generation_id,
        "worker_id": worker_id, "attempt_token": claim.attempt_token, "attempt": claim.job.attempt,
        "expected_hash": claim.job.checkpoint_hash, "checkpoint": value})
    try:
        job = MemoryGenerationJob.model_validate(receipt.get("job"))
        changed = {"status", "started", "lease_until", "checkpoint_hash", "error_code", "revision"}
        pause_codes = {"MEMORY_GENERATION_USER_PAUSED"} if user_requested else {
            "MEMORY_GENERATION_APPROVAL_PENDING", "MEMORY_GENERATION_USER_PAUSED"}
        if (job.model_dump(exclude=changed) != claim.job.model_dump(exclude=changed) or job.status != "paused"
                or not job.started or job.lease_until or job.checkpoint_hash != digest
                or job.error_code not in pause_codes or job.revision <= claim.job.revision):
            raise ValueError("pause receipt")
        return job
    except (ValueError, TypeError):
        raise BackendError("Memory pause persistence was not confirmed") from None
