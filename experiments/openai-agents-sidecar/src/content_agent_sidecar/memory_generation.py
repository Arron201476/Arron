"""Validated scheduler receipts for the private SDK memory worker."""

from datetime import datetime, timezone
from hashlib import sha256
import json
from typing import Literal

from agents.sandbox.memory.interface import RolloutExtractionArtifacts
from pydantic import BaseModel, ConfigDict, Field

from .backend import BackendError
from .memory_checkpoint import MAX_MEMORY_CHECKPOINT_BYTES, MemoryGenerationBinding, MemoryStageCheckpoint
from .memory_rollout import MemoryRollout
from .native_memory import extraction_has_memory
from .native_files import _go_json_hash, portable_file_path
from .native_manifest import GENERATION_DIRECTORY, MEMORY_DIRECTORY


class MemoryGenerationJob(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True, hide_input_in_errors=True)

    generation_id: str = Field(pattern=r"^[A-Za-z0-9_-]{1,256}$")
    activity_key: str = Field(pattern=r"^(turn|background|execution):[^\s:]{1,256}$")
    segment_id: str = Field(pattern=r"^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$")
    project_id: str = Field(pattern=r"^[A-Za-z0-9_-]{1,256}$")
    user_id: str = Field(min_length=1, max_length=256)
    source_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    base_version: int = Field(ge=0)
    base_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    status: Literal["running", "paused"]
    phase: Literal["extraction", "consolidation"]
    revision: int = Field(ge=1)
    attempt: int = Field(ge=1, le=2**31 - 1)
    model_id: str = Field(min_length=1, max_length=256)
    lease_until: str = Field(default="", max_length=64)
    started: bool
    checkpoint_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    error_code: str = Field(default="", max_length=128)


class MemoryGenerationCompletion(MemoryGenerationJob):
    status: Literal["completed"]
    phase: Literal["consolidation"]


def validate_memory_completion(raw, original: MemoryGenerationJob) -> MemoryGenerationCompletion:
    try:
        receipt = MemoryGenerationCompletion.model_validate(raw)
        changed = {"status", "started", "lease_until", "checkpoint_hash", "error_code", "revision"}
        if (original.phase != "consolidation" or original.status != "running"
                or receipt.model_dump(exclude=changed) != original.model_dump(exclude=changed)
                or not receipt.started or receipt.lease_until or receipt.error_code
                or receipt.checkpoint_hash != sha256(b"").hexdigest() or receipt.revision <= original.revision):
            raise ValueError("completion")
        return receipt
    except (ValueError, TypeError):
        raise BackendError("Memory completion persistence was not confirmed") from None


class FrozenMemoryInputPlan(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True, hide_input_in_errors=True)

    baseline_version: int = Field(ge=0)
    baseline_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    source_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    extraction_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    content_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    files: dict[str, str] = Field(min_length=4, max_length=7, repr=False)


class MemoryGenerationClaim(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True, hide_input_in_errors=True)

    conversation_id: str = Field(min_length=1, max_length=256)
    job: MemoryGenerationJob
    attempt_token: str = Field(pattern=r"^[A-Za-z0-9._~-]{1,512}$", repr=False)
    source: MemoryRollout = Field(repr=False)
    checkpoint: MemoryStageCheckpoint | None = Field(default=None, repr=False)
    extraction: RolloutExtractionArtifacts | None = Field(default=None, repr=False)
    input_plan: FrozenMemoryInputPlan | None = Field(default=None, repr=False)
    input_plan_hash: str | None = Field(default=None, pattern=r"^[a-f0-9]{64}$")


def decode_memory_generation_claim(raw: object, *, model_id: str, policy_hash: str,
                                   now: datetime | None = None) -> MemoryGenerationClaim:
    try:
        if not isinstance(raw, dict):
            raise ValueError("claim")
        claim = MemoryGenerationClaim.model_validate(raw)
        job, source = claim.job, claim.source
        lease = datetime.fromisoformat(job.lease_until.replace("Z", "+00:00"))
        current = now or datetime.now(timezone.utc)
        if lease.tzinfo is None or current.tzinfo is None or lease <= current or job.status != "running" or job.started or job.model_id != model_id:
            raise ValueError("admission")
        if any(getattr(job, key) != getattr(source, key) for key in ("activity_key", "segment_id", "project_id", "user_id")) or job.source_hash != source.content_hash:
            raise ValueError("source")
        expected = MemoryGenerationBinding(generation_id=job.generation_id, phase=job.phase, project_id=job.project_id,
            user_id=job.user_id, source_hash=job.source_hash, base_version=job.base_version, base_hash=job.base_hash,
            model_id=model_id, policy_hash=policy_hash)
        checkpoint_bytes = b""
        if "checkpoint" in raw:
            if claim.checkpoint is None:
                raise ValueError("checkpoint")
            checkpoint_bytes = json.dumps(raw["checkpoint"], ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()
            if len(checkpoint_bytes) > MAX_MEMORY_CHECKPOINT_BYTES or claim.checkpoint.binding != expected:
                raise ValueError("checkpoint binding")
            if sha256(claim.checkpoint.state_json.encode()).hexdigest() != claim.checkpoint.state_hash:
                raise ValueError("state hash")
        if sha256(checkpoint_bytes).hexdigest() != job.checkpoint_hash:
            raise ValueError("checkpoint hash")
        if job.phase == "extraction":
            if "extraction" in raw:
                raise ValueError("unexpected extraction")
        else:
            value = raw.get("extraction")
            if (not isinstance(value, dict) or set(value) != {"rollout_slug", "rollout_summary", "raw_memory"}
                    or any(type(text) is not str for text in value.values()) or not extraction_has_memory(claim.extraction)):
                raise ValueError("extraction receipt")
        if "input_plan" in raw or "input_plan_hash" in raw:
            plan = claim.input_plan
            if job.phase != "consolidation" or plan is None or claim.input_plan_hash is None:
                raise ValueError("input plan receipt")
            extraction = json.dumps(claim.extraction.model_dump(), ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()
            if (plan.baseline_version != job.base_version or plan.baseline_hash != job.base_hash
                    or plan.source_hash != job.source_hash or plan.extraction_hash != sha256(extraction).hexdigest()
                    or plan.content_hash != _go_json_hash(plan.files)
                    or claim.input_plan_hash != _go_json_hash(dict(sorted(plan.model_dump().items())))
                    or len(json.dumps(plan.model_dump(), ensure_ascii=False).encode()) > 16 << 20
                    or any(not portable_file_path(path) or path.split("/")[0] not in {GENERATION_DIRECTORY, MEMORY_DIRECTORY}
                           or "\0" in body for path, body in plan.files.items())):
                raise ValueError("input plan binding")
        return claim
    except (ValueError, TypeError, UnicodeError, OverflowError):
        raise BackendError("Memory generation claim does not match its admitted source, stage or worker policy") from None


def validate_memory_job_transition(raw: object, previous: MemoryGenerationJob, *,
                                   operation: Literal["start", "renew"], now: datetime | None = None) -> MemoryGenerationJob:
    try:
        if operation not in {"start", "renew"}:
            raise ValueError("operation")
        previous = MemoryGenerationJob.model_validate(previous.model_dump())
        job = MemoryGenerationJob.model_validate(raw)
        if previous.status != "running" or job.status != "running":
            raise ValueError("inactive job")
        mutable = {"revision", "lease_until", "started", "error_code"}
        if job.error_code != previous.error_code and not (
                operation == "renew" and previous.started and not previous.error_code
                and job.error_code == "MEMORY_GENERATION_PAUSE_REQUESTED"):
            raise ValueError("pause intent")
        if any(getattr(job, name) != getattr(previous, name) for name in type(job).model_fields if name not in mutable):
            raise ValueError("execution binding")
        old_lease = datetime.fromisoformat(previous.lease_until.replace("Z", "+00:00"))
        lease = datetime.fromisoformat(job.lease_until.replace("Z", "+00:00"))
        current = now or datetime.now(timezone.utc)
        if lease.tzinfo is None or old_lease.tzinfo is None or current.tzinfo is None or lease <= current or lease < old_lease:
            raise ValueError("lease")
        started = True if operation == "start" else previous.started
        if job.started != started or job.revision < previous.revision:
            raise ValueError("state")
        if (job.started != previous.started or lease != old_lease or job.error_code != previous.error_code) and job.revision == previous.revision:
            raise ValueError("revision")
        return job
    except (ValueError, TypeError, UnicodeError, OverflowError):
        raise BackendError("Memory worker transition was not confirmed for its original live attempt") from None
