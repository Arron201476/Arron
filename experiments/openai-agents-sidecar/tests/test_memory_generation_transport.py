import asyncio
import json
from hashlib import sha256

import pytest

from content_agent_sidecar.backend import BackendClient, BackendError, backend_activity
from content_agent_sidecar.native_memory import persist_memory_extraction
from agents.sandbox.memory.interface import RolloutExtractionArtifacts
from test_native_workspace_transport import Reply, response, wire


@pytest.mark.parametrize("has_memory", [False, True])
@pytest.mark.parametrize("wrong_receipt", [False, True])
def test_extraction_submission_requires_exact_original_receipt(has_memory, wrong_receipt):
    async def run():
        value = "confirmed memory" if has_memory else ""
        output = RolloutExtractionArtifacts(rollout_slug="fixture" if has_memory else "", rollout_summary=value, raw_memory=value)
        raw = json.dumps(output.model_dump(), ensure_ascii=False, separators=(",", ":")).encode()
        receipt = {"generation_id": "job-1", "content_hash": sha256(raw).hexdigest(), "has_memory": has_memory}
        sent = {**receipt, "content_hash": "f" * 64} if wrong_receipt else receipt
        with wire(response({"receipt": sent})) as (backend, requests):
            call = persist_memory_extraction(backend, generation_id="job-1", worker_id="worker",
                                             attempt_token="private-attempt", attempt=1, output=output)
            if wrong_receipt:
                with pytest.raises(BackendError, match="original output"):
                    await call
            else:
                assert await call == receipt
            assert len(requests) == 1
            payload = json.loads(requests[0][3])
            assert payload["output"] == output.model_dump()
            assert raw in requests[0][3]

    asyncio.run(run())


@pytest.mark.parametrize("operation", ["claim", "start", "renew", "checkpoint", "extraction"])
def test_memory_worker_fixed_routes_and_no_delegated_identity(operation):
    async def run():
        result = {"claim": None} if operation == "claim" else {"job": {"generation_id": "job-1"}}
        if operation == "extraction":
            result = {"receipt": {"generation_id": "job-1", "content_hash": "a" * 64, "has_memory": True}}
        payload = {"worker_id": "worker-1"}
        with wire(response(result)) as (backend, requests):
            assert await backend.memory_generation_request(operation, payload) == result
            assert len(requests) == 1
            method, path, headers, body = requests[0]
            assert method == "POST" and path == "/internal/v1/agent-memory/generations/" + operation
            assert json.loads(body) == payload
            assert headers["Authorization"] == "Bearer isolated-service-credential"
            assert not any(key.lower().startswith("x-agent-") for key in headers)

    asyncio.run(run())


@pytest.mark.parametrize("identity", [{"agent_turn_id": "turn"},
    {"task_attempt_id": "task", "attempt_token": "secret"},
    {"execution_attempt_id": "execution", "attempt_token": "secret"}])
def test_memory_worker_rejects_parent_context_before_sending(identity):
    async def run():
        backend = BackendClient("http://127.0.0.1:1", internal_token="secret")
        with backend_activity("project", **identity), pytest.raises(BackendError, match="independent worker"):
            await backend.memory_generation_request("claim", {})

    asyncio.run(run())


@pytest.mark.parametrize("result", [{"claim": []}, {"job": None}, {"claim": {}}, {"claim": None, "extra": 1}])
def test_memory_worker_rejects_wrong_claim_envelope(result):
    async def run():
        with wire(response(result)) as (backend, requests):
            with pytest.raises(BackendError):
                await backend.memory_generation_request("claim", {})
            assert len(requests) == 1

    asyncio.run(run())


@pytest.mark.parametrize("reply", [
    Reply(b'{"data":{"claim":null}}', status=307, headers=[("Location", "/other")]),
    Reply(b'{"data":{"claim":null}}', content_type="text/html"),
    Reply(b'{"error":{"code":"WORKER_UNAVAILABLE","message":"PRIVATE_INPUT"}}', status=503),
])
def test_memory_worker_transport_failure_is_private_and_not_retried(reply):
    async def run():
        with wire(reply) as (backend, requests):
            with pytest.raises(BackendError) as caught:
                await backend.memory_generation_request("claim", {})
            assert len(requests) == 1
            assert "PRIVATE_INPUT" not in str(caught.value) and caught.value.__cause__ is None

    asyncio.run(run())
