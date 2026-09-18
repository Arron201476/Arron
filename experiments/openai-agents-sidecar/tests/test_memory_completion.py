import asyncio
from hashlib import sha256
import json

import pytest

from content_agent_sidecar.backend import BackendError, backend_memory_activity
from content_agent_sidecar.memory_generation import MemoryGenerationJob
from test_memory_generation_claim import decode, raw_claim
from test_native_workspace_transport import response, wire


@pytest.mark.parametrize("fault", [None, "phase", "delegated", "generation_id", "source_hash", "base_hash",
                                  "attempt", "status", "started", "lease_until", "checkpoint_hash", "revision", "error_code"])
def test_completion_requires_exact_independent_receipt(raw_claim, fault):
    claim = decode(raw_claim)
    claim = claim.model_copy(update={"job": claim.job.model_copy(update={"phase": "consolidation"})})
    completed = {**claim.job.model_dump(), "status": "completed", "started": True, "lease_until": "",
                 "checkpoint_hash": sha256(b"").hexdigest(), "error_code": "", "revision": claim.job.revision + 2}
    if fault == "phase":
        claim = claim.model_copy(update={"job": claim.job.model_copy(update={"phase": "extraction"})})
    elif fault not in {None, "delegated"}:
        completed[fault] = {"started": False, "revision": claim.job.revision, "attempt": claim.job.attempt + 1,
                            "status": "running", "source_hash": "0" * 64, "base_hash": "0" * 64,
                            "checkpoint_hash": "0" * 64}.get(fault, "wrong")

    def reply(request):
        method, path, headers, body = request
        assert method == "POST" and path.endswith("/generations/complete")
        assert not headers["X-Agent-Memory-Generation-ID"]
        assert json.loads(body) == {"generation_id": claim.job.generation_id, "worker_id": "worker",
                                    "attempt_token": claim.attempt_token, "attempt": claim.job.attempt}
        return response({"job": completed})

    async def run():
        with wire(reply, reply) as (backend, requests):
            if fault == "delegated":
                with backend_memory_activity(claim.job.project_id, claim.job.generation_id, claim.job.attempt, claim.attempt_token):
                    with pytest.raises(BackendError):
                        await backend.complete_memory_generation(claim, "worker")
            elif fault:
                with pytest.raises(BackendError):
                    await backend.complete_memory_generation(claim, "worker")
            else:
                receipt = await backend.complete_memory_generation(claim, "worker")
                assert receipt.status == "completed"
                assert await backend.complete_memory_generation(claim, "worker") == receipt
                with pytest.raises(ValueError):
                    MemoryGenerationJob.model_validate(receipt.model_dump())
            assert len(requests) == (0 if fault in {"phase", "delegated"} else 2 if fault is None else 1)
    asyncio.run(run())
