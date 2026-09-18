import asyncio
from dataclasses import replace
import sqlite3

import pytest

from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.memory_archive_queue import MemoryArchiveQueue
from content_agent_sidecar.memory_rollout import MemoryArchivePolicy, capture_memory_rollout
from test_memory_rollout import completed, source


def archive():
    binding = source()
    rollout = capture_memory_rollout(asyncio.run(completed()), binding, segment_id="segment-1")
    policy = MemoryArchivePolicy(project_id=binding.project_id, user_id=binding.user_id,
                                  archive_enabled=True, generate_enabled=True, revision=1)
    return rollout, policy


def test_queue_preserves_frozen_payload_across_sqlite_serialization():
    db = sqlite3.connect(":memory:")
    queue = MemoryArchiveQueue(db, clock=lambda: 100)
    rollout, policy = archive()
    entry = queue.put(rollout, policy)
    assert queue.put(rollout, policy) == entry
    assert queue.pending() == [entry]
    assert queue.is_pending(entry)
    assert not queue.is_pending(replace(entry, payload_hash="wrong"))
    assert rollout.rollout_jsonl not in repr(entry)
    saved = db.serialize()
    queue.close()
    restored = sqlite3.connect(":memory:")
    restored.deserialize(saved)
    next_queue = MemoryArchiveQueue(restored, clock=lambda: 101)
    assert next_queue.pending() == [entry]
    assert entry.payload == {"rollout": rollout.model_dump(), "consent_revision": 1, "generate_enabled": True}
    next_queue.acknowledge(replace(entry, payload_hash="wrong"))
    assert next_queue.pending() == [entry]
    next_queue.acknowledge(entry)
    next_queue.acknowledge(entry)
    assert next_queue.pending() == []
    assert not next_queue.is_pending(entry)
    next_queue.close()


@pytest.mark.parametrize("fault", ["owner", "disabled", "revision", "capacity"])
def test_queue_requires_original_consent_and_bounded_storage(fault):
    queue = MemoryArchiveQueue(sqlite3.connect(":memory:"), max_bytes=1 if fault == "capacity" else 64 << 20)
    rollout, policy = archive()
    if fault == "owner": policy = policy.model_copy(update={"user_id": "other"})
    if fault == "disabled": policy = policy.model_copy(update={"archive_enabled": False, "generate_enabled": False})
    if fault == "revision":
        queue.put(rollout, policy)
        policy = policy.model_copy(update={"revision": 2})
    with pytest.raises(BackendError): queue.put(rollout, policy)
    assert len(queue.pending()) == (1 if fault == "revision" else 0)
    queue.close()


def test_queue_expiry_is_not_extended_by_identical_retry():
    now = [100]
    queue = MemoryArchiveQueue(sqlite3.connect(":memory:"), retention_seconds=60, clock=lambda: now[0])
    rollout, policy = archive()
    queue.put(rollout, policy)
    now[0] = 150
    queue.put(rollout, policy)
    now[0] = 160
    assert queue.pending() == []
    queue.close()


@pytest.mark.parametrize("operation", ["acknowledge", "expire"])
def test_queue_clears_deleted_private_payload_from_sqlite_pages(operation):
    db = sqlite3.connect(":memory:")
    db.execute("PRAGMA secure_delete=OFF")
    now = [100]
    queue = MemoryArchiveQueue(db, retention_seconds=60, clock=lambda: now[0])
    entry = queue.put(*archive())
    # JSON escaping changes the stored representation; use its exact payload.
    stored = db.execute("SELECT payload FROM pending_memory_archives").fetchone()[0].encode()
    assert stored in db.serialize()
    if operation == "acknowledge":
        queue.acknowledge(entry)
    else:
        now[0] = 160
    assert queue.pending() == []
    assert stored not in db.serialize()
    assert db.execute("PRAGMA secure_delete").fetchone()[0] == 1
    queue.close()


@pytest.mark.parametrize("column,value", [("payload", "PRIVATE_CORRUPT_BODY"), ("payload_hash", "wrong"), ("entry_id", "wrong"), ("size_bytes", 1)])
def test_queue_rejects_corruption_without_private_error_text(column, value):
    db = sqlite3.connect(":memory:")
    queue = MemoryArchiveQueue(db)
    queue.put(*archive())
    with db:
        db.execute(f"UPDATE pending_memory_archives SET {column}=?", (value,))
    with pytest.raises(BackendError) as error: queue.pending()
    assert "PRIVATE_CORRUPT_BODY" not in str(error.value)
    queue.close()
