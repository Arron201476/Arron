import asyncio
import json

import pytest

from content_agent_sidecar.backend import BackendError, backend_activity
from content_agent_sidecar.memory_archive_recovery import recover_memory_archive
from content_agent_sidecar.memory_rollout import capture_memory_rollout
from test_memory_rollout import completed, source
from test_memory_rollout_transport import identity, receipt
from test_native_workspace_transport import Reply, response, wire


@pytest.mark.parametrize("mode", ["turn", "background", "execution"])
def test_recovery_uses_independent_service_and_exact_frozen_payload(mode):
    async def run():
        captured = capture_memory_rollout(await completed(), source(mode), segment_id="segment-1")
        payload = {"rollout": captured.model_dump(), "consent_revision": 3, "generate_enabled": True}
        with wire(response(receipt(captured)), response(receipt(captured))) as (backend, requests):
            for _ in range(2):
                assert await recover_memory_archive(backend, payload) == receipt(captured)
            assert requests[0][3] == requests[1][3]
            for request in requests:
                assert request[0:2] == ("POST", "/internal/v1/agent-memory/archive-recover")
                assert request[2]["Authorization"] == "Bearer isolated-service-credential"
                assert not any(key.lower().startswith("x-agent-") for key in request[2])
                assert json.loads(request[3]) == {"rollout": captured.model_dump(), "consent_revision": 3}
        with wire() as (backend, requests), backend_activity("project", **identity(mode)):
            with pytest.raises(BackendError, match="independent worker"):
                await recover_memory_archive(backend, payload)
            assert requests == []
    asyncio.run(run())


@pytest.mark.parametrize("change", [{"consent_revision": True}, {"consent_revision": 0},
                                    {"generate_enabled": 1}, {"attempt_token": "PRIVATE_TOKEN"}])
def test_recovery_rejects_invalid_queue_contract_before_network(change):
    async def run():
        captured = capture_memory_rollout(await completed(), source(), segment_id="segment-1")
        payload = {"rollout": captured.model_dump(), "consent_revision": 3, "generate_enabled": False, **change}
        with wire() as (backend, requests):
            with pytest.raises(BackendError, match="valid completed private source") as failure:
                await recover_memory_archive(backend, payload)
            assert "PRIVATE_TOKEN" not in str(failure.value)
            assert requests == []
    asyncio.run(run())


@pytest.mark.parametrize("outcome", ["receipt-mismatch", "not-ready", "revoked"])
def test_recovery_preserves_remote_error_classification(outcome):
    async def run():
        captured = capture_memory_rollout(await completed(), source(), segment_id="segment-1")
        payload = {"rollout": captured.model_dump(), "consent_revision": 3, "generate_enabled": False}
        code = "AGENT_MEMORY_ARCHIVE_NOT_READY" if outcome == "not-ready" else "AGENT_MEMORY_ARCHIVE_SOURCE_REVOKED"
        reply = response({**receipt(captured), "content_hash": "b" * 64}) if outcome == "receipt-mismatch" else Reply(
            json.dumps({"error": {"code": code, "message": "PRIVATE_BODY"}}).encode(), status=409)
        with wire(reply) as (backend, requests):
            with pytest.raises(BackendError) as failure:
                await recover_memory_archive(backend, payload)
            if outcome != "receipt-mismatch":
                assert failure.value.code == code and failure.value.status_code == 409
            assert "PRIVATE_BODY" not in str(failure.value)
            assert len(requests) == 1
    asyncio.run(run())
