import asyncio
from copy import deepcopy
from datetime import datetime, timezone
from hashlib import sha256
import json

import pytest

from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.memory_checkpoint import MemoryGenerationBinding, MemoryStageCheckpoint
from content_agent_sidecar.memory_generation import decode_memory_generation_claim
from content_agent_sidecar.memory_rollout import capture_memory_rollout
from test_memory_rollout import completed, source
from test_native_workspace_transport import response, wire


POLICY = "c" * 64
NOW = datetime(2030, 1, 1, tzinfo=timezone.utc)


@pytest.fixture
def raw_claim():
    rollout = capture_memory_rollout(asyncio.run(completed()), source(), segment_id="segment-1")
    return {"conversation_id": "conversation", "attempt_token": "PRIVATE_TOKEN", "source": rollout.model_dump(),
        "job": {"generation_id": "generation", "activity_key": rollout.activity_key, "segment_id": rollout.segment_id,
                "project_id": rollout.project_id, "user_id": rollout.user_id, "source_hash": rollout.content_hash,
                "base_version": 1, "base_hash": "a" * 64, "status": "running", "phase": "extraction",
                "revision": 2, "attempt": 1, "model_id": "configured", "lease_until": "2099-01-01T00:00:00Z",
                "started": False, "checkpoint_hash": sha256(b"").hexdigest()}}


def decode(raw):
    return decode_memory_generation_claim(raw, model_id="configured", policy_hash=POLICY, now=NOW)


@pytest.mark.parametrize("phase", ["extraction", "consolidation"])
def test_claim_crosschecks_native_source_and_hides_private_data(raw_claim, phase):
    raw_claim["job"]["phase"] = phase
    if phase == "consolidation":
        raw_claim["extraction"] = {"rollout_slug": "fixture", "rollout_summary": "PRIVATE_SUMMARY", "raw_memory": "PRIVATE_BODY"}
    result = decode(raw_claim)
    assert result.conversation_id == "conversation" and result.job.phase == phase
    assert not any(value in repr(result) for value in ("PRIVATE_TOKEN", "USER_FACT", "PRIVATE_BODY", "PRIVATE_SUMMARY"))
    assert result.source.content_hash == result.job.source_hash


@pytest.mark.parametrize("field,value", [
    ("project_id", "other"), ("user_id", "other"), ("source_hash", "b" * 64), ("activity_key", "turn:other"),
    ("segment_id", "other"), ("started", True), ("started", 0), ("model_id", "other"),
    ("attempt", 0), ("attempt", True), ("status", "queued"), ("phase", "publication"),
    ("lease_until", "2000-01-01T00:00:00Z"), ("lease_until", "2099-01-01T00:00:00"),
    ("checkpoint_hash", "b" * 64),
])
def test_claim_rejects_invalid_or_switched_job(raw_claim, field, value):
    raw_claim["job"][field] = value
    with pytest.raises(BackendError, match="admitted") as error:
        decode(raw_claim)
    assert error.value.__cause__ is None and "PRIVATE_TOKEN" not in str(error.value)


def test_claim_checkpoint_binds_model_policy_phase_and_raw_bytes(raw_claim):
    job = raw_claim["job"]
    binding = MemoryGenerationBinding(generation_id=job["generation_id"], phase=job["phase"], project_id=job["project_id"],
        user_id=job["user_id"], source_hash=job["source_hash"], base_version=job["base_version"], base_hash=job["base_hash"],
        model_id=job["model_id"], policy_hash=POLICY)
    checkpoint = MemoryStageCheckpoint(binding=binding, stage_hash="d" * 64, state_json="{}", state_hash=sha256(b"{}").hexdigest())
    raw_claim["checkpoint"] = checkpoint.model_dump()
    job["checkpoint_hash"] = sha256(json.dumps(raw_claim["checkpoint"], ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()
    assert decode(raw_claim).checkpoint == checkpoint
    with pytest.raises(BackendError):
        decode_memory_generation_claim(raw_claim, model_id="configured", policy_hash="e" * 64, now=NOW)
    changed = deepcopy(raw_claim)
    changed["checkpoint"]["state_json"] = '{"changed":true}'
    changed["job"]["checkpoint_hash"] = sha256(json.dumps(changed["checkpoint"], separators=(",", ":")).encode()).hexdigest()
    with pytest.raises(BackendError):
        decode(changed)


@pytest.mark.parametrize("output", [None, {}, {"rollout_slug": "", "rollout_summary": "", "raw_memory": ""},
    {"rollout_slug": "INVALID SLUG", "rollout_summary": "summary", "raw_memory": "body"},
    {"rollout_slug": "fixture", "rollout_summary": "summary", "raw_memory": "body", "extra": "field"}])
def test_consolidation_cannot_run_without_exact_nonempty_extraction(raw_claim, output):
    raw_claim["job"]["phase"] = "consolidation"
    raw_claim["extraction"] = output
    with pytest.raises(BackendError):
        decode(raw_claim)


def test_typed_claim_transport_handles_empty_and_valid_receipts(raw_claim):
    async def run():
        with wire(response({"claim": None}), response({"claim": raw_claim})) as (backend, requests):
            assert await backend.claim_memory_generation("worker", "configured", 60, policy_hash=POLICY) is None
            claim = await backend.claim_memory_generation("worker", "configured", 60, policy_hash=POLICY)
            assert claim.conversation_id == "conversation"
            assert len(requests) == 2
            assert json.loads(requests[1][3]) == {"worker_id": "worker", "model_id": "configured", "lease_seconds": 60}
            assert requests[1][2]["X-Agent-Memory-Generation-ID"] is None
    asyncio.run(run())
