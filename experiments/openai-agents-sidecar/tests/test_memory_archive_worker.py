import asyncio
from dataclasses import replace
import importlib
import sqlite3
from unittest.mock import AsyncMock

import pytest

from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.memory_archive_queue import MemoryArchiveQueue
from content_agent_sidecar.memory_archive_worker import MemoryArchiveWorker
from content_agent_sidecar.memory_rollout import capture_memory_rollout, MemoryArchivePolicy
from test_memory_rollout import completed, source
from test_app import settings


@pytest.mark.parametrize("token", ["", "   ", None])
def test_archive_factory_requires_credentials_before_creating_client(monkeypatch, token):
    def forbidden(*args, **kwargs):
        pytest.fail("Created a client for archive recovery without credentials")
    module = importlib.import_module("content_agent_sidecar.memory_archive_worker")
    monkeypatch.setattr(module, "BackendClient", forbidden)
    with pytest.raises(ValueError, match="requires internal service credentials"):
        MemoryArchiveWorker.from_settings(replace(settings(), internal_token=token), object())


async def queue_fixture(count=1, **options):
    queue = MemoryArchiveQueue(sqlite3.connect(":memory:"), **options)
    binding, result = source(), await completed()
    policy = MemoryArchivePolicy(project_id=binding.project_id, user_id=binding.user_id,
                                 archive_enabled=True, generate_enabled=False, revision=1)
    for i in range(count):
        queue.put(capture_memory_rollout(result, binding, segment_id=f"segment-{i}"), policy)
    return queue


def receipt(payload):
    return {key: payload["rollout"][key] for key in ("activity_key", "segment_id", "project_id", "user_id", "content_hash")}


@pytest.mark.parametrize("code,status,removed", [
    ("AGENT_MEMORY_ARCHIVE_NOT_READY", 409, False),
    ("AGENT_MEMORY_ARCHIVE_SOURCE_REVOKED", 409, True),
    ("AGENT_MEMORY_CONFLICT", 409, True),
    ("WORKSPACE_ACCESS_DENIED", 403, True),
    ("AUTHENTICATION_REQUIRED", 401, False),
    ("AGENT_ACTIVITY_FORBIDDEN", 403, False),
    ("UNKNOWN_CONFLICT", 409, False),
    ("AGENT_MEMORY_CONFLICT", 500, False),
])
def test_archive_worker_only_discards_confirmed_source_revocation(code, status, removed, caplog):
    async def run():
        queue = await queue_fixture()
        backend = AsyncMock()
        backend.recover_memory_archive.side_effect = BackendError("PRIVATE_SOURCE_SECRET", code=code, status_code=status)
        worker = MemoryArchiveWorker(backend, queue)
        try:
            assert await worker.run_once() == 0
            assert bool(queue.pending()) != removed
            assert "PRIVATE_SOURCE_SECRET" not in caplog.text
        finally:
            await worker.stop()
            queue.close()
    asyncio.run(run())


def test_archive_worker_unknown_response_retries_exact_bytes_without_model_replay():
    async def run():
        queue = await queue_fixture()
        backend = AsyncMock()
        calls = []
        async def recover(payload):
            calls.append(payload)
            if len(calls) == 1:
                raise TimeoutError("PRIVATE_SOURCE_SECRET")
            return receipt(payload)
        backend.recover_memory_archive.side_effect = recover
        worker = MemoryArchiveWorker(backend, queue)
        try:
            assert await worker.run_once() == 0 and len(queue.pending()) == 1
            assert await worker.run_once() == 1 and queue.pending() == []
            assert calls[0] == calls[1]
        finally:
            await worker.stop()
            queue.close()
    asyncio.run(run())


def test_archive_worker_shutdown_preserves_unconfirmed_entry():
    async def run():
        queue = await queue_fixture()
        entered, cancelled = asyncio.Event(), asyncio.Event()
        backend = AsyncMock()
        async def recover(payload):
            entered.set()
            try:
                await asyncio.Event().wait()
            finally:
                cancelled.set()
        backend.recover_memory_archive.side_effect = recover
        worker = MemoryArchiveWorker(backend, queue)
        worker.start()
        try:
            await asyncio.wait_for(entered.wait(), 2)
            await worker.stop()
            await worker.stop()
            assert cancelled.is_set() and len(queue.pending()) == 1
            with pytest.raises(RuntimeError): worker.start()
            with pytest.raises(RuntimeError): await worker.run_once()
        finally:
            await worker.stop()
            queue.close()
    asyncio.run(run())


def test_archive_worker_rotates_past_unready_sources():
    async def run():
        queue = await queue_fixture(17)
        ready = queue.pending(limit=256)[-1].payload["rollout"]["segment_id"]
        backend = AsyncMock()
        async def recover(payload):
            if payload["rollout"]["segment_id"] != ready:
                raise BackendError("not ready", code="AGENT_MEMORY_ARCHIVE_NOT_READY", status_code=409)
            return receipt(payload)
        backend.recover_memory_archive.side_effect = recover
        worker = MemoryArchiveWorker(backend, queue)
        try:
            assert await worker.run_once() == 0
            assert await worker.run_once() == 1
            assert len(queue.pending(limit=256)) == 16
        finally:
            await worker.stop()
            queue.close()
    asyncio.run(run())


def test_archive_worker_ack_failure_keeps_retryable_entry(monkeypatch):
    async def run():
        queue = await queue_fixture()
        backend = AsyncMock()
        backend.recover_memory_archive.side_effect = lambda payload: receipt(payload)
        worker = MemoryArchiveWorker(backend, queue)
        acknowledge = queue.acknowledge
        def fail(entry): raise sqlite3.OperationalError("PRIVATE_SOURCE_SECRET")
        monkeypatch.setattr(queue, "acknowledge", fail)
        try:
            with pytest.raises(sqlite3.OperationalError): await worker.run_once()
            assert len(queue.pending()) == 1
            monkeypatch.setattr(queue, "acknowledge", acknowledge)
            assert await worker.run_once() == 1 and queue.pending() == []
        finally:
            await worker.stop()
            queue.close()
    asyncio.run(run())


@pytest.mark.parametrize("change", ["expiry", "already-acknowledged"])
def test_archive_worker_rechecks_cached_batch_before_sending(change):
    async def run():
        now = [100]
        queue = await queue_fixture(2, clock=lambda: now[0], retention_seconds=60)
        cached = queue.pending()
        backend = AsyncMock()
        async def recover(payload):
            if change == "expiry":
                now[0] = 160
            else:
                queue.acknowledge(cached[1])
            return receipt(payload)
        backend.recover_memory_archive.side_effect = recover
        worker = MemoryArchiveWorker(backend, queue)
        try:
            assert await worker.run_once() == 1
            assert backend.recover_memory_archive.await_count == 1
            assert queue.pending() == []
        finally:
            await worker.stop()
            queue.close()
    asyncio.run(run())


def test_two_archive_workers_confirm_same_entry_without_changing_frozen_request():
    async def run():
        queue = await queue_fixture()
        original = queue.pending()[0]
        both_entered, release = asyncio.Event(), asyncio.Event()
        requests = []
        async def recover(payload):
            requests.append(payload)
            if len(requests) == 2:
                both_entered.set()
            await release.wait()
            return receipt(payload)
        backends = [AsyncMock(), AsyncMock()]
        for backend in backends:
            backend.recover_memory_archive.side_effect = recover
        workers = [MemoryArchiveWorker(backend, queue) for backend in backends]
        tasks = [asyncio.create_task(worker.run_once()) for worker in workers]
        try:
            await asyncio.wait_for(both_entered.wait(), 2)
            assert queue.pending() == [original]
            release.set()
            assert await asyncio.gather(*tasks) == [1, 1]
            assert queue.pending() == []
            assert requests[0] == requests[1] == {
                "rollout": original.payload["rollout"], "consent_revision": original.payload["consent_revision"]}
        finally:
            release.set()
            await asyncio.gather(*(worker.stop() for worker in workers))
            await asyncio.gather(*tasks, return_exceptions=True)
            queue.close()
    asyncio.run(run())


def test_archive_request_deadline_cancels_wait_and_preserves_retry():
    async def run():
        queue = await queue_fixture()
        cancelled = asyncio.Event()
        backend = AsyncMock()
        async def recover(payload):
            try:
                await asyncio.Event().wait()
            finally:
                cancelled.set()
        backend.recover_memory_archive.side_effect = recover
        worker = MemoryArchiveWorker(backend, queue, request_seconds=1)
        try:
            assert await asyncio.wait_for(worker.run_once(), 3) == 0
            assert cancelled.is_set() and len(queue.pending()) == 1
            backend.recover_memory_archive.side_effect = lambda payload: receipt(payload)
            assert await worker.run_once() == 1 and queue.pending() == []
        finally:
            await worker.stop()
            queue.close()
    asyncio.run(run())


@pytest.mark.parametrize("body", ["PRIVATE_CORRUPT_BODY", b"PRIVATE_BINARY_BODY"])
def test_corrupt_archive_does_not_block_other_valid_sources(body, caplog):
    async def run():
        queue = await queue_fixture(2)
        corrupt, valid = queue.pending()
        with queue._db:
            queue._db.execute("UPDATE pending_memory_archives SET payload=? WHERE entry_id=?", (body, corrupt.entry_id))
        backend = AsyncMock()
        backend.recover_memory_archive.side_effect = lambda payload: receipt(payload)
        worker = MemoryArchiveWorker(backend, queue)
        try:
            assert await worker.run_once() == 1
            assert backend.recover_memory_archive.await_count == 1
            assert backend.recover_memory_archive.call_args.args[0]["rollout"] == valid.payload["rollout"]
            assert queue._db.execute("SELECT entry_id FROM pending_memory_archives").fetchall() == [(corrupt.entry_id,)]
            assert "integrity" in caplog.text
            assert "PRIVATE_CORRUPT_BODY" not in caplog.text and "PRIVATE_BINARY_BODY" not in caplog.text
            with pytest.raises(BackendError, match="integrity"):
                queue.pending()
        finally:
            await worker.stop()
            queue.close()
    asyncio.run(run())
