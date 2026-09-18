import asyncio
import json
from unittest.mock import AsyncMock

import pytest

from content_agent_sidecar.backend import BackendClient, BackendError, backend_activity
from content_agent_sidecar.memory_rollout import capture_memory_rollout, persist_memory_rollout, restore_memory_rollout
from content_agent_sidecar.memory_rollout import archive_memory_rollout, resolve_memory_archive_policy, confirm_memory_archive, MemoryArchivePolicy
from test_memory_rollout import completed, source
from test_native_workspace_transport import Reply, response, wire


def identity(mode):
    return {"agent_turn_id": "original"} if mode == "turn" else {
        "task_attempt_id" if mode == "background" else "execution_attempt_id": "original", "attempt_token": "private-attempt-token",
    }


def receipt(capture):
    return {key: getattr(capture, key) for key in ("activity_key", "segment_id", "project_id", "user_id", "content_hash")}


@pytest.mark.parametrize("change", [{}, {"user_id": "other"}, {"consent_revision": 2},
                                    {"consent_revision": True}, {"generate_enabled": 1},
                                    {"generation_id": ""}, {"content_hash": "b" * 64},
                                    {"extra": True}])
def test_archive_transaction_receipt_is_exact_and_typed(change):
    async def run():
        binding = source()
        captured = capture_memory_rollout(await completed(), binding, segment_id="segment-1")
        policy = MemoryArchivePolicy(project_id=binding.project_id, user_id=binding.user_id,
                                     archive_enabled=True, generate_enabled=True, revision=1)
        result = {**receipt(captured), "consent_revision": 1, "generate_enabled": True,
                  "generation_id": "amg-confirmed", **change}
        with wire(response(result)) as (backend, requests), backend_activity("project", **identity("turn")):
            if change:
                with pytest.raises(BackendError, match="transaction receipt"):
                    await confirm_memory_archive(backend, captured, binding, policy)
            else:
                assert await confirm_memory_archive(backend, captured, binding, policy) == "amg-confirmed"
            assert len(requests) == 1 and requests[0][1] == "/internal/v1/agent-memory/archive-receipt"
            assert json.loads(requests[0][3]) == {"segment_id": captured.segment_id,
                                                "content_hash": captured.content_hash, "consent_revision": 1}
    asyncio.run(run())


@pytest.mark.parametrize("mode", ["turn", "background", "execution"])
def test_automatic_archive_uses_exact_consent_revision_without_legacy_fallback(mode):
    async def run():
        binding = source(mode)
        captured = capture_memory_rollout(await completed(), binding, segment_id="segment-1")
        consent = {"project_id": binding.project_id, "user_id": binding.user_id,
                   "archive_enabled": True, "generate_enabled": False, "revision": 3}
        denied = Reply(b'{"error":{"code":"AGENT_MEMORY_CONFLICT","message":"revoked"}}', status=409)
        with wire(response(consent), response(receipt(captured)), denied) as (backend, requests), backend_activity("project", **identity(mode)):
            policy = await resolve_memory_archive_policy(backend, binding)
            assert await archive_memory_rollout(backend, captured, binding, policy) == receipt(captured)
            with pytest.raises(BackendError) as failure:
                await archive_memory_rollout(backend, captured, binding, policy)
            assert failure.value.code == "AGENT_MEMORY_CONFLICT"
            assert [item[1] for item in requests] == ["/internal/v1/agent-memory/archive-policy",
                                                     "/internal/v1/agent-memory/archive",
                                                     "/internal/v1/agent-memory/archive"]
            assert json.loads(requests[1][3]) == {"consent_revision": 3, "rollout": captured.model_dump()}
            assert requests[1][3] == requests[2][3]
    asyncio.run(run())


@pytest.mark.parametrize("change", [{"user_id": "foreign"}, {"project_id": "foreign"},
                                    {"archive_enabled": "true"}, {"revision": True},
                                    {"revision": 0}, {"unexpected": "field"}])
def test_automatic_archive_rejects_untrusted_policy(change):
    async def run():
        binding = source()
        consent = {"project_id": binding.project_id, "user_id": binding.user_id,
                   "archive_enabled": True, "generate_enabled": False, "revision": 3, **change}
        with wire(response(consent)) as (backend, requests), backend_activity("project", **identity("turn")):
            with pytest.raises(BackendError, match="execution owner"):
                await resolve_memory_archive_policy(backend, binding)
            assert len(requests) == 1
    asyncio.run(run())


def test_disabled_consent_never_sends_rollout_body():
    async def run():
        binding = source()
        captured = capture_memory_rollout(await completed(), binding, segment_id="segment-1")
        consent = {"project_id": binding.project_id, "user_id": binding.user_id,
                   "archive_enabled": False, "generate_enabled": False, "revision": 0}
        with wire(response(consent)) as (backend, requests), backend_activity("project", **identity("turn")):
            policy = await resolve_memory_archive_policy(backend, binding)
            with pytest.raises(BackendError, match="consent"):
                await archive_memory_rollout(backend, captured, binding, policy)
            assert len(requests) == 1
    asyncio.run(run())


@pytest.mark.parametrize("mode", ["turn", "background", "execution"])
def test_private_source_save_and_read_use_original_activity_and_exact_sdk_bytes(mode):
    async def run():
        binding = source(mode)
        captured = capture_memory_rollout(await completed(), binding, segment_id="segment-1")
        with wire(response(receipt(captured)), response(captured.model_dump())) as (backend, requests), backend_activity("project", **identity(mode)):
            assert await persist_memory_rollout(backend, captured, binding) == receipt(captured)
            restored = await restore_memory_rollout(backend, binding, segment_id="segment-1", expected_hash=captured.content_hash)
            assert restored == captured
            assert [req[0:2] for req in requests] == [("POST", "/internal/v1/agent-memory/rollouts"), ("POST", "/internal/v1/agent-memory/rollouts/read")]
            assert json.loads(requests[0][3]) == captured.model_dump()
            assert json.loads(requests[1][3]) == {"segment_id": "segment-1"}
            for request in requests:
                assert request[2]["X-Agent-Project-ID"] == "project"
                key = {"turn": "X-Agent-Turn-ID", "background": "X-Agent-Task-Attempt-ID", "execution": "X-Agent-Execution-Attempt-ID"}[mode]
                assert request[2][key] == "original"
                assert request[2]["Authorization"] == "Bearer isolated-service-credential"
                if mode != "turn": assert request[2]["X-Agent-Attempt-Token"] == "private-attempt-token"
                assert b"private-attempt-token" not in request[3]
    asyncio.run(run())


@pytest.mark.parametrize("field", ["activity_key", "segment_id", "project_id", "user_id", "content_hash", "extra"])
def test_mismatched_save_receipt_does_not_establish_persistence(field):
    async def run():
        binding = source()
        captured = capture_memory_rollout(await completed(), binding, segment_id="segment-1")
        altered = {**receipt(captured), field: "foreign"}
        with wire(response(altered)) as (backend, requests), backend_activity("project", **identity("turn")):
            with pytest.raises(BackendError, match="persistence receipt"):
                await persist_memory_rollout(backend, captured, binding)
            assert len(requests) == 1
    asyncio.run(run())


@pytest.mark.parametrize("field", ["project_id", "user_id", "workspace_id", "memory_hash", "content_hash", "rollout_jsonl"])
def test_recovery_does_not_replace_original_source_with_wrong_private_record(field):
    async def run():
        binding = source()
        captured = capture_memory_rollout(await completed(), binding, segment_id="segment-1")
        altered = {**captured.model_dump(), field: "foreign"}
        with wire(response(altered)) as (backend, requests), backend_activity("project", **identity("turn")):
            with pytest.raises(BackendError, match="Recovered memory source"):
                await restore_memory_rollout(backend, binding, segment_id="segment-1", expected_hash=captured.content_hash)
            assert len(requests) == 1
    asyncio.run(run())


def test_unknown_save_preserves_exact_original_bytes_for_retry_or_readback():
    async def run():
        binding = source()
        captured = capture_memory_rollout(await completed(), binding, segment_id="segment-1")
        failure = Reply(b'{"error":{"code":"UPSTREAM_UNAVAILABLE","message":"PRIVATE_DIAGNOSTIC"}}', status=503)
        with wire(failure, response(receipt(captured)), response(captured.model_dump())) as (backend, requests), backend_activity("project", **identity("turn")):
            with pytest.raises(BackendError) as failed:
                await persist_memory_rollout(backend, captured, binding)
            assert failed.value.code == "UPSTREAM_UNAVAILABLE" and failed.value.status_code == 503
            assert "PRIVATE_DIAGNOSTIC" not in str(failed.value) and failed.value.__cause__ is None
            assert len(requests) == 1
            await persist_memory_rollout(backend, captured, binding)
            assert requests[0][3] == requests[1][3]
            assert await restore_memory_rollout(backend, binding, segment_id="segment-1", expected_hash=captured.content_hash) == captured
    asyncio.run(run())


def test_retired_legacy_write_reports_consent_requirement_without_fallback():
    async def run():
        binding = source()
        captured = capture_memory_rollout(await completed(), binding, segment_id="segment-1")
        gone = Reply(b'{"error":{"code":"AGENT_MEMORY_ARCHIVE_CONSENT_REQUIRED","message":"Legacy write retired"}}', status=410)
        with wire(gone) as (backend, requests), backend_activity("project", **identity("turn")):
            with pytest.raises(BackendError) as failure:
                await persist_memory_rollout(backend, captured, binding)
            assert failure.value.status_code == 410
            assert failure.value.code == "AGENT_MEMORY_ARCHIVE_CONSENT_REQUIRED"
            assert len(requests) == 1
    asyncio.run(run())


@pytest.mark.parametrize("reply", [
    Reply(b'{}'), Reply(b'{"data":{}}'), Reply(b'{"data":[]}'), Reply(b'bad-json'),
    Reply(b'{"data":{"value":1}}', content_type="text/html"),
    Reply(b'{"data":{"value":1}}', headers=[("Content-Encoding", "gzip")]),
    Reply(b'{"data":{"value":1}}', headers=[("Content-Type", "application/json")]),
    Reply(b'{"data":{"value":1}}', headers=[("Content-Length", "0")]),
    Reply(b'X' * 4097),
    Reply(b'{}', status=307, headers=[("Location", "/must-not-follow")]),
])
def test_private_source_transport_rejects_invalid_or_redirected_responses_without_retry(reply):
    async def run():
        with wire(reply) as (backend, requests), backend_activity("project", **identity("turn")):
            with pytest.raises(BackendError):
                await backend.memory_rollout_request("save", {})
            assert len(requests) == 1
    asyncio.run(run())


def test_memory_source_transport_ignores_environment_proxy(monkeypatch):
    monkeypatch.setenv("http_proxy", "http://127.0.0.1:1")
    monkeypatch.setenv("HTTP_PROXY", "http://127.0.0.1:1")
    monkeypatch.setenv("no_proxy", "")
    monkeypatch.setenv("NO_PROXY", "")
    async def run():
        with wire(response({"ok": True})) as (backend, requests), backend_activity("project", **identity("turn")):
            assert await backend.memory_rollout_request("save", {}) == {"ok": True}
            assert len(requests) == 1
    asyncio.run(run())


def test_source_rejects_changed_transport_scope_before_sending_private_body():
    async def run():
        binding = source()
        captured = capture_memory_rollout(await completed(), binding, segment_id="segment-1")
        backend = BackendClient("http://127.0.0.1:9", internal_token="fixture")
        backend.memory_rollout_request = AsyncMock()
        with backend_activity("foreign", **identity("turn")), pytest.raises(BackendError, match="another execution"):
            await persist_memory_rollout(backend, captured, binding)
        backend.memory_rollout_request.assert_not_called()
    asyncio.run(run())
