from __future__ import annotations

from copy import deepcopy
from dataclasses import dataclass, field
import hashlib
import json
from typing import Any

from agents import Agent, RunState, UserError
from agents.items import ItemHelpers

from .backend import BackendClient, BackendError
from .contracts import AgentToolApprovalDecision
from .native_pause import unstarted_checkpoint, validate_unstarted_input
from .native_live_state import restore_native_checkpoint
from .stateful_inputs import StatefulInputs
from .input_attachments import prepare_input_attachments
from .managed_instructions import bind_managed_instructions
from .runtime import AgentContext, RuntimeCompatibilityError, _apply_approval_decisions, _checkpoint_string_list, _load_skill_instructions, _serialize_paused_run


class ExecutionStateInvalid(ValueError):
    """The durable execution cannot be restored without changing its identity."""


class ExecutionCheckpointFailed(RuntimeError):
    """The native SDK execution could not be persisted for human approval."""


class ExecutionTaskPaused(Exception):
    def __init__(self, result: Any, worker_state: dict[str, Any], *, user_pause: bool = False, recovery_reason: str = "") -> None:
        super().__init__("Stateful task paused" if user_pause else "Stateful task awaits tool approval")
        self.user_pause = user_pause
        self.recovery_reason = recovery_reason
        try:
            self.run_state, self.schema_version, self.pending_ids = _serialize_paused_run(result, allow_no_approvals=user_pause)
        except (RuntimeCompatibilityError, ValueError, TypeError, UserError) as exc:
            raise ExecutionCheckpointFailed("Native SDK approval checkpoint could not be serialized") from exc
        self.worker_state = deepcopy(worker_state)
        if len(json.dumps(self.worker_state, ensure_ascii=False).encode("utf-8")) > 4 << 20:
            raise ExecutionCheckpointFailed("Stateful Worker checkpoint exceeds the durable size limit")


@dataclass
class StatefulExecution:
    context: AgentContext
    identity: dict[str, str]
    resume: dict[str, Any] | None = None
    completed_batches: list[dict[str, Any]] = field(default_factory=list)
    context_reads: set[str] = field(default_factory=set)
    output_mode: str = "structured"
    restored: bool = False
    unstarted_input: Any = None
    phase: str = "generate"
    repair: dict[str, Any] | None = None
    repair_origin: dict[str, Any] | None = None
    inputs: StatefulInputs = field(default_factory=StatefulInputs)

    @classmethod
    async def create(cls, claim: dict[str, Any], backend: BackendClient) -> StatefulExecution:
        pack, attempt, task = claim.get("context_pack"), claim.get("attempt"), claim.get("task")
        if not all(isinstance(item, dict) for item in (pack, attempt, task)):
            raise ValueError("Stateful claim is missing its frozen task or context")
        identity = {
            **{key: attempt.get(key) for key in ("attempt_id", "run_id", "step_run_id", "task_item_id", "input_snapshot_hash")},
            **{key: claim.get(key) for key in ("capability_id", "capability_version", "executor_id")},
            **{key: pack.get(key) for key in ("project_id", "conversation_id", "context_hash")},
        }
        if any(not isinstance(value, str) or not value.strip() or len(value) > 256 for value in identity.values()):
            raise ValueError("Stateful claim identity is incomplete")
        if identity["input_snapshot_hash"] != identity["context_hash"]:
            raise ValueError("Stateful claim input hash differs from its frozen context")
        if not isinstance(claim.get("attempt_token"), str) or not claim["attempt_token"] or len(claim["attempt_token"]) > 512:
            raise ValueError("Stateful claim token is missing")
        for data, keys in ((task, ("task_item_id", "run_id", "step_run_id")),
                           (pack.get("run"), ("run_id",)), (pack.get("step"), ("step_run_id", "task_item_id")),
                           (pack.get("capability"), ("capability_id", "capability_version"))):
            if not isinstance(data, dict) or any(data.get(key) != identity[key] for key in keys):
                raise ValueError("Stateful claim does not match its frozen context")
        if pack.get("_sdk_source_analysis_chunk"):
            raise ValueError("A claimed task cannot start from an unverified source chunk")
        if attempt.get("executor_id") != identity["executor_id"]:
            raise ValueError("Stateful claim executor identity changed")
        context = AgentContext(
            identity["project_id"], identity["conversation_id"], backend,
            execution_attempt_id=identity["attempt_id"], attempt_token=claim["attempt_token"],
            raw_request={"execution": identity, "input": pack.get("run_input_snapshot"), "config": pack.get("config_snapshot")},
            routed_capabilities={identity["capability_id"]},
            capability_versions={identity["capability_id"]: identity["capability_version"]},
        )
        await _load_skill_instructions(context, identity["capability_id"])
        primary = context.loaded_capabilities[identity["capability_id"]]
        skill = primary.get("skill")
        frozen = pack.get("skill_instructions")
        if isinstance(frozen, dict) and (not isinstance(skill, dict) or skill.get("instructions") != frozen.get("content")):
            raise ValueError("Primary Skill instructions differ from the frozen workflow")
        try:
            additional = await prepare_input_attachments(identity["project_id"], claim.get("additional_inputs", []), backend)
        except (ValueError, BackendError) as exc:
            raise ExecutionStateInvalid("Stateful additional material is invalid") from exc
        execution = cls(context, identity, inputs=StatefulInputs.from_claim(identity["attempt_id"], additional))
        if execution.inputs.ids and identity["executor_id"] == "workflow.video_script_extract":
            raise ValueError("Video-provider tasks cannot accept text SDK additional input")
        repair = claim.get("repair")
        if repair is not None:
            if (not isinstance(repair, dict) or repair.get("schema_version") != "execution_result_repair.v1"
                or type(repair.get("rejection_no")) is not int or repair["rejection_no"] != 1
                or repair.get("input_snapshot_hash") != identity["input_snapshot_hash"]
                or any(not isinstance(repair.get(key), str) for key in ("candidate", "response_hash", "error_code", "error_detail", "trace_ref"))
                or len(repair["candidate"].encode("utf-8")) > 4 << 20
                or hashlib.sha256(repair["candidate"].encode("utf-8")).hexdigest() != repair["response_hash"]
                or repair["error_code"] not in {"OUTPUT_SCHEMA_VALIDATION_FAILED", "BATCH_COVERAGE_INVALID", "OUTPUT_LENGTH_INSUFFICIENT"}
                or not isinstance(repair.get("usage"), dict)
                or any(type(repair["usage"].get(key, 0)) is not int or repair["usage"].get(key, 0) < 0
                       for key in ("requests", "input_tokens", "output_tokens", "total_tokens"))):
                raise ValueError("Durable output repair receipt is invalid")
            json.loads(repair["candidate"])
            execution.repair_origin = {key: repair[key] for key in ("schema_version", "rejection_no", "input_snapshot_hash", "response_hash", "error_code")}
        if claim.get("resume") is not None:
            await execution.restore(claim["resume"])
            if repair is not None and (execution.phase != "repair_backend" or execution.repair["candidate"] != repair["candidate"]
                or execution.repair["error"] != repair["error_detail"] or execution.completed_batches
                or any(execution.repair["usage"].get(key, 0) < repair["usage"].get(key, 0)
                       for key in ("requests", "input_tokens", "output_tokens", "total_tokens"))):
                raise ValueError("SDK repair checkpoint differs from its durable rejected result")
        elif repair is not None:
            execution.inputs.restore(None, result_repair=True)
            execution.context_reads = set(_checkpoint_string_list(repair["usage"], "context_artifact_version_ids"))
            execution.begin_repair("repair_backend", pack, repair["candidate"], ValueError(repair["error_detail"]), repair["usage"], repair["trace_ref"])
        else:
            execution.inputs.restore(None)
        await bind_managed_instructions(context, execution.resume["run_state"] if execution.resume is not None else None)
        return execution

    async def restore(self, resume: dict[str, Any]) -> None:
        if not isinstance(resume, dict) or type(resume.get("checkpoint_version")) is not int or resume["checkpoint_version"] < 1:
            raise ValueError("Stateful checkpoint version is invalid")
        worker = resume.get("worker_state")
        state = resume.get("run_state")
        if not isinstance(worker, dict) or worker.get("schema_version") not in {"stateful_worker.v1", "stateful_worker.v2", "stateful_worker.v3"} or worker.get("identity") != self.identity:
            raise ValueError("Stateful Worker checkpoint identity or phase is invalid")
        phase = worker.get("phase")
        if worker.get("repair_origin") != self.repair_origin:
            raise ValueError("Stateful repair origin differs from its durable result receipt")
        if not isinstance(phase, str) or phase not in {"generate", "repair_parse", "repair_validate", "repair_backend"}:
            raise ValueError("Stateful Worker checkpoint phase is invalid")
        repair = worker.get("repair")
        if phase == "generate":
            if repair is not None:
                raise ValueError("Generation checkpoint contains a repair candidate")
        else:
            if (worker["schema_version"] not in {"stateful_worker.v2", "stateful_worker.v3"} or not isinstance(repair, dict)
                or set(repair) != {"candidate", "error", "pack_hash", "usage", "trace_ref"}
                or any(not isinstance(repair.get(key), str) for key in ("candidate", "error", "pack_hash", "trace_ref"))
                or not isinstance(repair.get("usage"), dict)
                or any(type(repair["usage"].get(key, 0)) is not int or repair["usage"].get(key, 0) < 0
                       for key in ("requests", "input_tokens", "output_tokens", "total_tokens"))):
                raise ValueError("Stateful repair checkpoint is invalid")
        if not isinstance(state, dict) or state.get("$schemaVersion") != resume.get("schema_version"):
            raise ValueError("Stateful SDK checkpoint schema is invalid")
        wrapper = state.get("context")
        payload = wrapper.get("context") if isinstance(wrapper, dict) else None
        if not isinstance(payload, dict) or payload.get("schema_version") != "content_agent_context.v1":
            raise ValueError("Stateful SDK checkpoint context is incompatible")
        for key in ("project_id", "conversation_id", "execution_attempt_id", "raw_request", "agent_turn_id", "agent_task_id", "agent_task_attempt_id", "skill_invocation_id", "idempotency_key"):
            if payload.get(key) != getattr(self.context, key):
                raise ValueError("Stateful SDK checkpoint identity or input changed")
        versions, loaded = payload.get("capability_versions"), payload.get("loaded_capabilities")
        if not isinstance(versions, dict) or len(versions) > 128 or not all(isinstance(k, str) and k and isinstance(v, str) and v for k, v in versions.items()):
            raise ValueError("Stateful checkpoint Skill versions are invalid")
        if not isinstance(loaded, dict) or not all(isinstance(k, str) and k in versions and isinstance(v, dict) for k, v in loaded.items()):
            raise ValueError("Stateful checkpoint Skill metadata is invalid")
        primary = self.identity["capability_id"]
        if versions.get(primary) != self.identity["capability_version"] or loaded.get(primary) != self.context.loaded_capabilities[primary]:
            raise ValueError("Stateful primary Skill metadata changed since approval")
        routed = set(_checkpoint_string_list(payload, "routed_capabilities"))
        if not routed.issubset(versions) or primary not in routed:
            raise ValueError("Stateful checkpoint routed Skill versions are missing")
        self.context.routed_capabilities.update(routed | set(loaded))
        self.context.capability_versions.update(versions)
        for key in sorted(routed | set(loaded)):
            if key != primary:
                await _load_skill_instructions(self.context, key)
                if key in loaded and loaded[key] != self.context.loaded_capabilities[key]:
                    raise ValueError("Stateful Skill metadata changed since approval")
        self.context.consulted_capabilities.update(_checkpoint_string_list(payload, "consulted_capabilities"))
        restore_native_checkpoint(self.context, payload)
        mode = worker.get("output_mode")
        if not isinstance(mode, str) or mode not in {"structured", "json"}:
            raise ValueError("Stateful checkpoint output mode is invalid")
        completed = worker.get("completed_batches")
        if not isinstance(completed, list) or any(not isinstance(item, dict) or not isinstance(item.get("payload"), dict) or not isinstance(item.get("usage"), dict) or not isinstance(item.get("trace_ref"), str) for item in completed):
            raise ValueError("Stateful checkpoint completed batches are invalid")
        if any(type(item["usage"].get(key, 0)) is not int or item["usage"].get(key, 0) < 0
               for item in completed for key in ("requests", "input_tokens", "output_tokens", "total_tokens")):
            raise ValueError("Stateful checkpoint batch usage is invalid")
        self.context_reads = set(_checkpoint_string_list(worker, "context_artifact_version_ids"))
        self.completed_batches = deepcopy(completed)
        self.output_mode = mode
        self.phase, self.repair = phase, deepcopy(repair)
        self.unstarted_input = deepcopy(worker.get("unstarted_input"))
        if "unstarted_input_hash" in worker:
            original = state.get("original_input")
            if ("unstarted_input" in worker or not unstarted_checkpoint(state)
                or not isinstance(worker["unstarted_input_hash"], str)
                or worker["unstarted_input_hash"] != self.pack_hash(original)):
                raise ValueError("Stateful original input does not match its compact checkpoint reference")
            self.unstarted_input = deepcopy(original)
        input_ledger = worker.get("input_ledger")
        if (worker["schema_version"] == "stateful_worker.v3") != (input_ledger is not None):
            raise ValueError("Stateful input checkpoint version is invalid")
        self.inputs.restore(input_ledger)
        if unstarted_checkpoint(state):
            validate_unstarted_input(state, self.unstarted_input)
        elif self.unstarted_input is not None:
            raise ValueError("Executed stateful checkpoint contains an unstarted input")
        self.resume, self.restored = resume, True

    def checkpoint(self) -> dict[str, Any]:
        initial = {}
        if self.unstarted_input is not None:
            # The native RunState already stores the exact initial multimodal
            # input. Bind large payloads by hash instead of duplicating bytes.
            if len(json.dumps(self.unstarted_input, ensure_ascii=False).encode("utf-8")) > 256 << 10:
                initial["unstarted_input_hash"] = self.pack_hash(self.unstarted_input)
            else:
                initial["unstarted_input"] = self.unstarted_input
        return {"schema_version": "stateful_worker.v3" if self.inputs.ids else "stateful_worker.v2", "phase": self.phase, "identity": self.identity,
                "output_mode": self.output_mode, "completed_batches": self.completed_batches,
                "context_artifact_version_ids": sorted(self.context_reads),
                **({"repair": self.repair} if self.repair is not None else {}),
                **({"repair_origin": self.repair_origin} if self.repair_origin is not None else {}),
                **({"input_ledger": self.inputs.snapshot()} if self.inputs.ids else {}),
                **initial}

    @staticmethod
    def pack_hash(pack: dict[str, Any]) -> str:
        return hashlib.sha256(json.dumps(pack, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False).encode("utf-8")).hexdigest()

    def begin_repair(self, phase: str, pack: dict[str, Any], candidate: str, error: ValueError,
                     usage: dict[str, Any], trace_ref: str) -> None:
        if self.resume is not None or self.phase != "generate":
            raise ExecutionStateInvalid("Cannot replace an unfinished stateful phase")
        self.phase = phase
        self.repair = deepcopy({"candidate": candidate, "error": str(error), "pack_hash": self.pack_hash(pack),
                                "usage": usage, "trace_ref": trace_ref})
        # Repair uses the same output contract, including its transport fallback.
        self.unstarted_input = None

    def repair_candidate(self, pack: dict[str, Any]) -> dict[str, Any]:
        if self.repair is None or self.repair["pack_hash"] != self.pack_hash(pack):
            raise ExecutionStateInvalid("Stateful repair candidate belongs to a different frozen input or batch")
        return deepcopy(self.repair)

    def finish_repair(self) -> None:
        self.phase, self.repair = "generate", None
        self.output_mode = "structured"
        self.unstarted_input = None

    async def runner_input(self, agent: Agent, input_items: Any) -> Any:
        if self.resume is None:
            return self.inputs.stage(input_items)
        try:
            saved_input = self.inputs.saved_original_input(input_items)
            if self.phase != "generate" and ItemHelpers.input_to_new_input_list(self.resume["run_state"]["original_input"]) != ItemHelpers.input_to_new_input_list(saved_input):
                raise ValueError("Stateful repair input differs from its saved candidate")
            state = await RunState.from_json(agent, self.resume["run_state"], context_override=self.context)
            decisions = self.resume.get("approval_decisions")
            if not isinstance(decisions, list):
                raise ValueError("Stateful checkpoint approval decisions are missing")
            if state.get_interruptions() or decisions:
                _apply_approval_decisions(state, [AgentToolApprovalDecision.model_validate(item) for item in decisions])
            if unstarted_checkpoint(self.resume["run_state"]):
                if self.unstarted_input != saved_input:
                    raise ValueError("Stateful unstarted input differs from the frozen rendered input")
                return self.inputs.stage(deepcopy(saved_input), resumed=True)
            return self.inputs.stage(state, resumed=True)
        except (ValueError, UserError, KeyError, TypeError, RuntimeCompatibilityError) as exc:
            raise ExecutionStateInvalid("Stateful SDK checkpoint or approval decisions cannot be restored") from exc

    def finish_generation(self, result: Any, *, user_pause: bool = False) -> None:
        if getattr(result, "interruptions", None) or (user_pause and result.final_output is None):
            raise ExecutionTaskPaused(result, self.checkpoint(), user_pause=user_pause)
        self.resume = None

    def complete_batch(self, payload: dict[str, Any], usage: dict[str, Any], trace_ref: str) -> None:
        self.completed_batches.append(deepcopy({"payload": payload, "usage": usage, "trace_ref": trace_ref}))
        self.context_reads.clear()
        self.output_mode = "structured"
