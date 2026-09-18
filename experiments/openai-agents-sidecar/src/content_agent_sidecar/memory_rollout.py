"""Immutable private input for managed SDK memory extraction stages."""

from hashlib import sha256
from datetime import datetime
import json
import re
from typing import Any, Literal

from agents.result import RunResultBase, RunResultStreaming
from agents.sandbox.memory.rollouts import build_rollout_payload_from_result, dump_rollout_json
from pydantic import BaseModel, ConfigDict, Field, model_validator

from .managed_memory import MemorySnapshot
from .backend import BackendError


MAX_MEMORY_ROLLOUT_BYTES = 4 << 20


class MemoryArchivePolicy(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True, hide_input_in_errors=True)

    project_id: str = Field(min_length=1, max_length=256)
    user_id: str = Field(min_length=1, max_length=256)
    archive_enabled: bool
    generate_enabled: bool
    revision: int = Field(ge=0)
    request_id: str = Field(default="", max_length=128)

    @model_validator(mode="after")
    def validate_consent(self):
        if self.generate_enabled and not self.archive_enabled or self.archive_enabled and self.revision < 1:
            raise ValueError("Invalid memory archival consent")
        return self


async def resolve_memory_archive_policy(backend, source: MemorySnapshot) -> MemoryArchivePolicy:
    _verify_transport_source(backend, source)
    raw = await backend.memory_rollout_request("archive-policy", {})
    try:
        policy = MemoryArchivePolicy.model_validate(raw)
        if policy.project_id != source.project_id or policy.user_id != source.user_id:
            raise ValueError("owner")
    except (ValueError, TypeError):
        raise BackendError("Memory archive policy does not match its execution owner") from None
    return policy


async def archive_memory_rollout(backend, rollout: "MemoryRollout", source: MemorySnapshot,
                                 policy: MemoryArchivePolicy) -> dict[str, str]:
    _verify_transport_source(backend, source)
    rollout.verified_input(source, segment_id=rollout.segment_id)
    checked = MemoryArchivePolicy.model_validate(policy.model_dump())
    if not checked.archive_enabled or checked.project_id != source.project_id or checked.user_id != source.user_id:
        raise BackendError("Memory archival requires its execution owner's consent")
    result = await backend.memory_rollout_request("archive", {
        "consent_revision": checked.revision, "rollout": rollout.model_dump(),
    })
    expected = {key: getattr(rollout, key) for key in ("activity_key", "segment_id", "project_id", "user_id", "content_hash")}
    if result != expected:
        raise BackendError("Memory source persistence receipt does not match the frozen input")
    return expected


async def confirm_memory_archive(backend, rollout: "MemoryRollout", source: MemorySnapshot,
                                 policy: MemoryArchivePolicy) -> str:
    _verify_transport_source(backend, source)
    rollout.verified_input(source, segment_id=rollout.segment_id)
    checked = MemoryArchivePolicy.model_validate(policy.model_dump())
    if not checked.archive_enabled or checked.project_id != source.project_id or checked.user_id != source.user_id:
        raise BackendError("Archive receipt requires original consent")
    result = await backend.memory_rollout_request("archive-receipt", {
        "segment_id": rollout.segment_id, "content_hash": rollout.content_hash,
        "consent_revision": checked.revision,
    })
    expected = {key: getattr(rollout, key) for key in ("activity_key", "segment_id", "project_id", "user_id", "content_hash")}
    generation_id = result.get("generation_id")
    expected.update(consent_revision=checked.revision, generate_enabled=checked.generate_enabled, generation_id=generation_id)
    if (result != expected or type(result.get("consent_revision")) is not int
            or type(result.get("generate_enabled")) is not bool or not isinstance(generation_id, str)
            or len(generation_id) > 256 or bool(generation_id) != checked.generate_enabled):
        raise BackendError("Memory archive transaction receipt does not match the original request")
    return generation_id


class MemoryRollout(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True, hide_input_in_errors=True)

    schema_version: Literal["agent_memory_rollout.v1"] = "agent_memory_rollout.v1"
    activity_key: str = Field(min_length=1, max_length=270)
    workspace_id: str = Field(min_length=1, max_length=256)
    project_id: str = Field(min_length=1, max_length=256)
    user_id: str = Field(min_length=1, max_length=256)
    segment_id: str = Field(pattern=r"^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$")
    memory_version: int = Field(ge=0)
    memory_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    read_enabled: bool
    content_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    rollout_jsonl: str = Field(min_length=1, repr=False)

    @model_validator(mode="after")
    def validate_source(self):
        try:
            encoded = self.rollout_jsonl.encode("utf-8")
            if len(encoded) > MAX_MEMORY_ROLLOUT_BYTES or sha256(encoded).hexdigest() != self.content_hash:
                raise ValueError("bytes")
            lines = self.rollout_jsonl.splitlines()
            if len(lines) != 1:
                raise ValueError("segments")
            payload = json.loads(lines[0])
            required = {"updated_at", "rollout_id", "input", "generated_items", "terminal_metadata"}
            if not isinstance(payload, dict) or not required.issubset(payload) or set(payload) - required - {"interruptions", "final_output"}:
                raise ValueError("shape")
            expected_id = sha256(f"{self.activity_key}:{self.segment_id}".encode()).hexdigest()
            if payload["rollout_id"] != expected_id or not isinstance(payload["updated_at"], str) or not payload["updated_at"]:
                raise ValueError("identity")
            if datetime.fromisoformat(payload["updated_at"]).tzinfo is None:
                raise ValueError("timestamp")
            for field in ("input", "generated_items", "interruptions"):
                if field in payload and (not isinstance(payload[field], list) or any(not isinstance(item, dict) for item in payload[field])):
                    raise ValueError("items")
            from agents.sandbox.memory.rollouts import RolloutTerminalMetadata
            terminal = RolloutTerminalMetadata.model_validate(payload["terminal_metadata"], strict=True)
            if set(payload["terminal_metadata"]) != set(terminal.model_dump()):
                raise ValueError("terminal fields")
            if terminal.exception_message is not None:
                raise ValueError("private diagnostic")
            if terminal.has_final_output != (payload.get("final_output") is not None):
                raise ValueError("terminal output")
            if (terminal.terminal_state == "completed") != terminal.has_final_output:
                raise ValueError("terminal state")
        except (ValueError, TypeError, UnicodeError, KeyError) as exc:
            raise ValueError("Private memory rollout failed its immutable source contract") from exc
        return self

    def verified_input(self, source: MemorySnapshot, *, segment_id: str) -> str:
        # A persisted envelope is data, not authority to switch users or executions.
        checked = type(self).model_validate(self.model_dump())
        expected = _source_identity(source, segment_id)
        if any(getattr(checked, name) != value for name, value in expected.items()):
            raise ValueError("Memory rollout does not match its authorized execution source")
        return checked.rollout_jsonl


def _source_identity(source: MemorySnapshot, segment_id: str) -> dict[str, Any]:
    return {
        "activity_key": source.activity_key, "workspace_id": source.workspace_id,
        "project_id": source.project_id, "user_id": source.user_id, "segment_id": segment_id,
        "memory_version": source.version, "memory_hash": source.content_hash,
        "read_enabled": source.read_enabled,
    }


def capture_memory_rollout(result: RunResultBase, source: MemorySnapshot, *, segment_id: str,
                           exception: BaseException | None = None) -> MemoryRollout:
    if not isinstance(result, RunResultBase):
        raise ValueError("Memory capture requires an actual SDK run result")
    if isinstance(result, RunResultStreaming):
        task = result.run_loop_task
        if not result.is_complete or task is not None and not task.done():
            raise ValueError("Memory capture cannot freeze a running SDK stream")
    # The SDK prioritizes final_output over exception. Do not turn a rejected
    # final result into a successful training source at the platform boundary.
    if exception is not None and result.final_output is not None:
        raise ValueError("Memory capture has conflicting success and failure evidence")
    payload = build_rollout_payload_from_result(result, exception=exception)
    payload["rollout_id"] = sha256(f"{source.activity_key}:{segment_id}".encode()).hexdigest()
    # Provider exception messages can contain authorization data. The native
    # terminal state/type remain evidence, but raw diagnostics are not memory.
    payload["terminal_metadata"]["exception_message"] = None
    frozen = dump_rollout_json(payload)
    return MemoryRollout(**_source_identity(source, segment_id), rollout_jsonl=frozen,
                         content_hash=sha256(frozen.encode("utf-8")).hexdigest())


def _verify_transport_source(backend, source: MemorySnapshot) -> None:
    activity = backend.native_workspace_activity()
    identities = [("turn", "X-Agent-Turn-ID"), ("background", "X-Agent-Task-Attempt-ID"), ("execution", "X-Agent-Execution-Attempt-ID")]
    keys = [f"{mode}:{activity[field]}" for mode, field in identities if activity.get(field)]
    if activity.get("X-Agent-Project-ID") != source.project_id or keys != [source.activity_key]:
        raise BackendError("Memory source transport belongs to another execution")


async def persist_memory_rollout(backend, rollout: MemoryRollout, source: MemorySnapshot) -> dict[str, str]:
    _verify_transport_source(backend, source)
    rollout.verified_input(source, segment_id=rollout.segment_id)
    result = await backend.memory_rollout_request("save", rollout.model_dump())
    expected = {key: getattr(rollout, key) for key in ("activity_key", "segment_id", "project_id", "user_id", "content_hash")}
    if result != expected:
        raise BackendError("Memory source persistence receipt does not match the frozen input")
    return expected


async def restore_memory_rollout(backend, source: MemorySnapshot, *, segment_id: str, expected_hash: str) -> MemoryRollout:
    _verify_transport_source(backend, source)
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}", segment_id) or not re.fullmatch(r"[a-f0-9]{64}", expected_hash):
        raise BackendError("Memory source recovery requires its original segment and content hash")
    result = await backend.memory_rollout_request("read", {"segment_id": segment_id})
    try:
        restored = MemoryRollout.model_validate(result)
        restored.verified_input(source, segment_id=segment_id)
        if restored.content_hash != expected_hash:
            raise ValueError("unconfirmed source")
    except (ValueError, TypeError) as exc:
        raise BackendError("Recovered memory source does not match the original input") from None
    return restored
