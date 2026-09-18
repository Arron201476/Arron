import asyncio
import json

import pytest

from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.memory_generation import validate_memory_job_transition
from test_memory_generation_claim import NOW, decode, raw_claim
from test_native_workspace_transport import response, wire


def started(claim):
    return {**claim.job.model_dump(), "started": True, "revision": claim.job.revision + 1}


def test_pause_request_survives_renewal_and_cannot_be_cleared_by_worker_receipt(raw_claim):
    claim = decode(raw_claim)
    first = started(claim)
    requested = {**first, "error_code": "MEMORY_GENERATION_PAUSE_REQUESTED", "revision": first["revision"] + 1}
    extended = {**requested, "lease_until": "2099-01-01T01:00:00Z", "revision": requested["revision"] + 1}
    async def run():
        with wire(response({"job": first}), response({"job": requested}), response({"job": extended})) as (backend, requests):
            job = await backend.start_memory_generation(claim, "worker")
            job = await backend.renew_memory_generation(claim, "worker", job, 60)
            job = await backend.renew_memory_generation(claim, "worker", job, 60)
            assert job.error_code == "MEMORY_GENERATION_PAUSE_REQUESTED" and len(requests) == 3
            for code in ("", "MEMORY_GENERATION_USER_PAUSED", "unrelated"):
                with pytest.raises(BackendError):
                    validate_memory_job_transition({**job.model_dump(), "error_code": code, "revision": job.revision + 1}, job, operation="renew", now=NOW)
    asyncio.run(run())


def test_pause_request_requires_started_execution_and_new_revision(raw_claim):
    claim = decode(raw_claim)
    previous = validate_memory_job_transition(started(claim), claim.job, operation="start", now=NOW)
    for before, operation in ((claim.job, "renew"), (claim.job, "start"), (previous, "renew")):
        receipt = {**before.model_dump(), "error_code": "MEMORY_GENERATION_PAUSE_REQUESTED"}
        if operation == "start":
            receipt.update(started=True, revision=before.revision + 1)
        with pytest.raises(BackendError):
            validate_memory_job_transition(receipt, before, operation=operation, now=NOW)


@pytest.mark.parametrize("operation", ["start", "renew"])
def test_transition_accepts_only_monotonic_live_receipts(raw_claim, operation):
    claim = decode(raw_claim)
    previous = claim.job
    value = started(claim) if operation == "start" else previous.model_dump()
    job = validate_memory_job_transition(value, previous, operation=operation, now=NOW)
    assert job.started == (operation == "start")
    renewed = {**job.model_dump(), "lease_until": "2099-01-01T01:00:00Z", "revision": job.revision + 1}
    assert validate_memory_job_transition(renewed, job, operation="renew", now=NOW).lease_until == renewed["lease_until"]


@pytest.mark.parametrize("field,value", [
    ("generation_id", "other"), ("attempt", 2), ("phase", "consolidation"), ("project_id", "other"),
    ("user_id", "other"), ("model_id", "other"), ("source_hash", "f" * 64), ("base_version", 2),
    ("checkpoint_hash", "f" * 64), ("status", "paused"), ("started", False), ("started", 1),
    ("revision", 1), ("revision", 2), ("lease_until", "2031-01-01T00:00:00Z"),
    ("lease_until", "2099-01-01T00:00:00"),
])
def test_start_rejects_changed_binding_or_unconfirmed_admission(raw_claim, field, value):
    claim = decode(raw_claim)
    receipt = started(claim)
    receipt[field] = value
    with pytest.raises(BackendError, match="not confirmed"):
        validate_memory_job_transition(receipt, claim.job, operation="start", now=NOW)


def test_renewal_does_not_accept_start_or_unversioned_extension(raw_claim):
    claim = decode(raw_claim)
    with pytest.raises(BackendError):
        validate_memory_job_transition(started(claim), claim.job, operation="renew", now=NOW)
    receipt = {**claim.job.model_dump(), "lease_until": "2099-01-02T00:00:00Z"}
    with pytest.raises(BackendError):
        validate_memory_job_transition(receipt, claim.job, operation="renew", now=NOW)


def test_start_and_renew_use_exact_scheduler_identity_over_http(raw_claim):
    claim = decode(raw_claim)
    first = started(claim)
    extended = {**first, "revision": first["revision"] + 1, "lease_until": "2099-01-01T01:00:00Z"}
    async def run():
        with wire(response({"job": first}), response({"job": extended})) as (backend, requests):
            job = await backend.start_memory_generation(claim, "worker")
            job = await backend.renew_memory_generation(claim, "worker", job, 60)
            assert job.lease_until == extended["lease_until"]
            for _, _, headers, body in requests:
                payload = json.loads(body)
                assert payload["generation_id"] == claim.job.generation_id
                assert payload["attempt"] == claim.job.attempt
                assert payload["attempt_token"] == claim.attempt_token
                assert headers["X-Agent-Memory-Generation-ID"] is None
    asyncio.run(run())


def test_renewal_accepts_committed_checkpoint_but_rejects_other_attempt_before_http(raw_claim):
    from content_agent_sidecar.memory_generation import MemoryGenerationJob

    claim = decode(raw_claim)
    advanced = MemoryGenerationJob.model_validate({**started(claim), "checkpoint_hash": "f" * 64, "revision": 4})
    async def run():
        with wire(response({"job": advanced.model_dump()})) as (backend, requests):
            assert await backend.renew_memory_generation(claim, "worker", advanced, 60) == advanced
            wrong = advanced.model_copy(update={"attempt": 2})
            with pytest.raises(BackendError, match="different claim"):
                await backend.renew_memory_generation(claim, "worker", wrong, 60)
            assert len(requests) == 1
    asyncio.run(run())
