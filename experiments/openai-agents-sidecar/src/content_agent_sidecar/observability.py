from __future__ import annotations

from contextlib import contextmanager
from dataclasses import dataclass, field
from hashlib import sha256
from time import perf_counter
from typing import Any, Iterator
from uuid import uuid4

from agents import RunConfig


_USAGE_FIELDS = (
    "requests",
    "input_tokens",
    "output_tokens",
    "total_tokens",
)

_OBSERVATION_SCHEMA = "agent_turn_observation.v1"
_MAX_OBSERVATION_COUNTERS = 32
_MAX_TRACE_REFS = 32
_MAX_RESPONSE_IDS = 64
_MAX_REQUEST_IDS = 64


def _safe_nonnegative_int(value: Any) -> int:
    try:
        parsed = int(value)
    except (TypeError, ValueError):
        return 0
    return max(0, parsed)


def _safe_identifier(value: Any, limit: int = 160) -> str:
    text = str(value or "").strip()
    if not text:
        return ""
    return "".join(
        character
        for character in text[:limit]
        if character.isascii()
        and (character.isalnum() or character in {"_", "-", ".", ":"})
    )


def privacy_safe_run_config(
    *,
    workflow_name: str,
    tracing_enabled: bool,
    release_id: str = "dev",
    trace_id: str | None = None,
    group_id: str | None = None,
    trace_metadata: dict[str, str] | None = None,
) -> RunConfig:
    metadata = {
        "release_id": _safe_identifier(release_id, 64) or "dev",
    }
    for key, value in (trace_metadata or {}).items():
        safe_key = _safe_identifier(key, 64)
        safe_value = _safe_identifier(value, 160)
        if safe_key and safe_value:
            metadata[safe_key] = safe_value
    return RunConfig(
        tracing_disabled=not tracing_enabled,
        trace_include_sensitive_data=False,
        workflow_name=workflow_name,
        trace_id=trace_id,
        group_id=group_id,
        trace_metadata=metadata,
    )


@dataclass
class TurnObservation:
    provider_id: str
    model_id: str
    release_id: str = "dev"
    started_at: float = field(default_factory=perf_counter, repr=False)
    trace_refs: list[str] = field(default_factory=list)
    response_ids: list[str] = field(default_factory=list)
    request_ids: list[str] = field(default_factory=list)
    usage: dict[str, int] = field(
        default_factory=lambda: {name: 0 for name in _USAGE_FIELDS}
    )
    latency_ms: dict[str, int] = field(default_factory=dict)
    failure_stage: str = ""
    cancel_reason: str = ""
    _active_stage: str = field(default="", init=False, repr=False)
    _result_markers: set[int] = field(default_factory=set, init=False, repr=False)
    _resume_usage_baseline: dict[str, int] = field(
        default_factory=dict, init=False, repr=False
    )

    @contextmanager
    def measure(self, stage: str) -> Iterator[None]:
        normalized_stage = _safe_identifier(stage, 64) or "unknown"
        previous_stage = self._active_stage
        self._active_stage = normalized_stage
        started = perf_counter()
        try:
            yield
        except BaseException as exc:
            if not self.failure_stage:
                self.failure_stage = _safe_identifier(getattr(exc, "failure_stage", ""), 64) or normalized_stage
            raise
        finally:
            elapsed = max(0, round((perf_counter() - started) * 1000))
            self.latency_ms[normalized_stage] = (
                self.latency_ms.get(normalized_stage, 0) + elapsed
            )
            self._active_stage = previous_stage

    def mark_failure(self, stage: str = "") -> None:
        if self.failure_stage:
            return
        self.failure_stage = (
            _safe_identifier(stage, 64)
            or self._active_stage
            or "unknown"
        )

    def mark_cancelled(self, reason: str) -> None:
        self.cancel_reason = _safe_identifier(reason, 64) or "cancelled"

    def run_config(
        self,
        *,
        workflow_name: str,
        conversation_id: str,
        agent_turn_id: str,
        tracing_enabled: bool,
    ) -> RunConfig:
        trace_ref = "trace_" + uuid4().hex
        self.trace_refs.append(trace_ref)
        group_hash = sha256(conversation_id.encode("utf-8")).hexdigest()[:32]
        turn_hash = sha256(agent_turn_id.encode("utf-8")).hexdigest()[:16]
        return privacy_safe_run_config(
            tracing_enabled=tracing_enabled,
            workflow_name=workflow_name,
            release_id=self.release_id,
            trace_id=trace_ref,
            group_id="group_" + group_hash,
            trace_metadata={
                "turn_hash": turn_hash,
            },
        )

    def ingest_result(self, result: Any, *, include_usage: bool = True) -> None:
        marker = id(result)
        if marker in self._result_markers:
            return
        self._result_markers.add(marker)

        for response in getattr(result, "raw_responses", None) or []:
            response_id = _safe_identifier(getattr(response, "response_id", ""))
            request_id = _safe_identifier(getattr(response, "request_id", ""))
            if response_id and response_id not in self.response_ids:
                self.response_ids.append(response_id)
            if request_id and request_id not in self.request_ids:
                self.request_ids.append(request_id)

        if not include_usage:
            return
        result_usage = _result_usage_counters(result)
        for name, value in result_usage.items():
            delta = max(0, value - self._resume_usage_baseline.get(name, 0))
            self.usage[name] = self.usage.get(name, 0) + delta
        self._resume_usage_baseline = {}

    def mark_resume_usage_baseline(self, result: Any) -> None:
        self._resume_usage_baseline = _result_usage_counters(result)

    def checkpoint(self) -> dict[str, Any]:
        result = self.public()
        result["resume_usage_baseline"] = dict(self._resume_usage_baseline)
        return result

    def resume_from_checkpoint(self, checkpoint: Any) -> None:
        if checkpoint is None:
            return
        if not isinstance(checkpoint, dict) or checkpoint.get("schema_version") != _OBSERVATION_SCHEMA:
            raise ValueError("unsupported Agent observation checkpoint")

        self.provider_id = _checkpoint_identifier(
            checkpoint.get("provider_id"), "provider_id", 64
        ) or self.provider_id
        self.model_id = _checkpoint_identifier(
            checkpoint.get("model_id"), "model_id", 128
        ) or self.model_id
        self.release_id = _checkpoint_identifier(
            checkpoint.get("release_id"), "release_id", 64
        ) or self.release_id
        self.trace_refs = _checkpoint_identifiers(
            checkpoint.get("trace_refs"), "trace_refs", _MAX_TRACE_REFS
        )
        self.response_ids = _checkpoint_identifiers(
            checkpoint.get("response_ids"), "response_ids", _MAX_RESPONSE_IDS
        )
        self.request_ids = _checkpoint_identifiers(
            checkpoint.get("request_ids"), "request_ids", _MAX_REQUEST_IDS
        )
        self.usage = _checkpoint_counters(checkpoint.get("usage"), "usage")
        self._resume_usage_baseline = _checkpoint_counters(
            checkpoint.get("resume_usage_baseline", {}),
            "resume_usage_baseline",
        )
        latency = _checkpoint_counters(
            checkpoint.get("latency_ms"), "latency_ms"
        )
        previous_total = latency.pop("total", 0)
        self.latency_ms = latency
        self.failure_stage = _checkpoint_identifier(
            checkpoint.get("failure_stage"), "failure_stage", 64
        )
        self.cancel_reason = _checkpoint_identifier(
            checkpoint.get("cancel_reason"), "cancel_reason", 64
        )
        self.started_at = perf_counter() - (previous_total / 1000)

    def public(self) -> dict[str, Any]:
        latency = {
            key: max(0, int(value))
            for key, value in sorted(self.latency_ms.items())
        }
        latency["total"] = max(0, round((perf_counter() - self.started_at) * 1000))
        result: dict[str, Any] = {
            "schema_version": _OBSERVATION_SCHEMA,
            "provider_id": _safe_identifier(self.provider_id, 64),
            "model_id": _safe_identifier(self.model_id, 128),
            "release_id": _safe_identifier(self.release_id, 64) or "dev",
            "trace_refs": self.trace_refs[:32],
            "response_ids": self.response_ids[:64],
            "request_ids": self.request_ids[:64],
            "usage": {key: max(0, int(value)) for key, value in self.usage.items()},
            "latency_ms": latency,
        }
        if self.failure_stage:
            result["failure_stage"] = self.failure_stage
        if self.cancel_reason:
            result["cancel_reason"] = self.cancel_reason
        return result


def _checkpoint_identifier(value: Any, field_name: str, limit: int) -> str:
    if value is None or value == "":
        return ""
    if not isinstance(value, str):
        raise ValueError(f"Agent observation field {field_name!r} is invalid")
    normalized = _safe_identifier(value, limit)
    if normalized != value:
        raise ValueError(f"Agent observation field {field_name!r} is invalid")
    return normalized


def _checkpoint_identifiers(value: Any, field_name: str, limit: int) -> list[str]:
    if not isinstance(value, list) or len(value) > limit:
        raise ValueError(f"Agent observation field {field_name!r} is invalid")
    result: list[str] = []
    for item in value:
        normalized = _checkpoint_identifier(item, field_name, 160)
        if not normalized:
            raise ValueError(f"Agent observation field {field_name!r} is invalid")
        if normalized not in result:
            result.append(normalized)
    return result


def _checkpoint_counters(value: Any, field_name: str) -> dict[str, int]:
    if not isinstance(value, dict) or len(value) > _MAX_OBSERVATION_COUNTERS:
        raise ValueError(f"Agent observation field {field_name!r} is invalid")
    result: dict[str, int] = {}
    for key, item in value.items():
        normalized_key = _checkpoint_identifier(key, field_name, 64)
        if not normalized_key or isinstance(item, bool) or not isinstance(item, int) or item < 0:
            raise ValueError(f"Agent observation field {field_name!r} is invalid")
        result[normalized_key] = item
    return result


def _result_usage_counters(result: Any) -> dict[str, int]:
    wrapper = getattr(result, "context_wrapper", None)
    usage = getattr(wrapper, "usage", None)
    if usage is None:
        return {}
    counters = {
        name: _safe_nonnegative_int(getattr(usage, name, 0))
        for name in _USAGE_FIELDS
    }
    input_details = getattr(usage, "input_tokens_details", None)
    output_details = getattr(usage, "output_tokens_details", None)
    counters.update({
        "cached_tokens": _safe_nonnegative_int(
            getattr(input_details, "cached_tokens", 0)
        ),
        "cache_write_tokens": _safe_nonnegative_int(
            getattr(input_details, "cache_write_tokens", 0)
        ),
        "reasoning_tokens": _safe_nonnegative_int(
            getattr(output_details, "reasoning_tokens", 0)
        ),
    })
    return counters
