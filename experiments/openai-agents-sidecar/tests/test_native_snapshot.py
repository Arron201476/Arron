import asyncio
import hashlib
import io
import json
from types import SimpleNamespace
from uuid import uuid4

import pytest
from agents.sandbox.session.dependencies import Dependencies
from agents.sandbox.session.snapshot_lifecycle import persist_snapshot
from agents.sandbox.snapshot import SnapshotBase
from pydantic import ValidationError

from content_agent_sidecar.native_snapshot import (
    MAX_ARCHIVE_BYTES,
    SNAPSHOT_DEPENDENCY,
    NativeSnapshotError,
    RuntimeSnapshotBinding,
    RuntimeWorkspaceSnapshot,
    SnapshotReference,
)


class MemorySnapshotTransport:
    def __init__(self):
        self.versions = {}
        self.receipts = {}
        self.saves = []
        self.reads = []
        self.fail_save = ""
        self.corrupt_read = False
        self.started = asyncio.Event()
        self.gate = None

    async def save_snapshot(self, session_id, request_id, parent_version, data):
        self.saves.append((session_id, request_id, parent_version, data))
        self.started.set()
        if self.gate:
            await self.gate.wait()
        if self.fail_save == "before":
            raise RuntimeError("private-token-from-http-body")
        receipt = self.receipts.get((session_id, request_id))
        if receipt is None:
            assert (session_id, parent_version + 1) not in self.versions
            receipt = SnapshotReference(version=parent_version + 1, sha256=hashlib.sha256(data).hexdigest())
            self.receipts[(session_id, request_id)] = receipt
            self.versions[(session_id, receipt.version)] = data
        if self.fail_save == "after":
            raise RuntimeError("private-token-from-http-body")
        return receipt

    async def read_snapshot(self, session_id, reference):
        self.reads.append((session_id, reference))
        return b"corrupt" if self.corrupt_read else self.versions[(session_id, reference.version)]

    async def read_snapshot_receipt(self, session_id, pending):
        return self.receipts.get((session_id, pending.request_id))


def fixture():
    snapshot = RuntimeWorkspaceSnapshot(id=str(uuid4()))
    backend = MemorySnapshotTransport()
    dependency = RuntimeSnapshotBinding(snapshot.id, backend)
    return snapshot, backend, Dependencies.with_values({SNAPSHOT_DEPENDENCY: dependency})


def test_native_sdk_snapshot_registry_and_exact_historical_bytes():
    async def scenario():
        snapshot, backend, dependencies = fixture()
        assert not await snapshot.restorable(dependencies=dependencies)
        await snapshot.persist(io.BytesIO(b"binary\x00\xff\x80"), dependencies=dependencies)
        serialized = snapshot.model_dump_json()
        historical = SnapshotBase.parse(json.loads(serialized))
        assert type(historical) is RuntimeWorkspaceSnapshot
        await snapshot.persist(io.BytesIO(b"newer"), dependencies=dependencies)
        assert snapshot.reference.version == 2
        assert (await historical.restore(dependencies=dependencies)).read() == b"binary\x00\xff\x80"
        assert backend.reads[-1][1].version == 1
        assert "transport" not in serialized and "private-token" not in serialized
    asyncio.run(scenario())


def test_native_sdk_snapshot_lifecycle_closes_stream_and_advances_reference():
    async def scenario():
        snapshot, _, dependencies = fixture()
        stream = io.BytesIO(b"native SDK lifecycle bytes")
        async def export():
            return stream
        session = SimpleNamespace(state=SimpleNamespace(snapshot=snapshot), dependencies=dependencies,
                                  _should_compute_snapshot_fingerprint_on_persist=lambda: False,
                                  persist_workspace=export)
        await persist_snapshot(session)
        assert stream.closed and snapshot.reference.version == 1
        assert session.state.snapshot_fingerprint is None
        assert session.state.snapshot_fingerprint_version is None
    asyncio.run(scenario())


@pytest.mark.parametrize("lost", ["before", "after"])
def test_native_snapshot_pending_receipt_serializes_without_latest_fallback(lost):
    async def scenario():
        snapshot, backend, dependencies = fixture()
        backend.fail_save = lost
        with pytest.raises(NativeSnapshotError) as error:
            await snapshot.persist(io.BytesIO(b"private binary body"), dependencies=dependencies)
        assert error.value.__context__ is None
        assert "private-token" not in str(error.value)
        serialized = snapshot.model_dump_json()
        assert "private binary body" not in serialized and "private-token" not in serialized
        rebuilt = SnapshotBase.parse(json.loads(serialized))
        assert rebuilt.pending and rebuilt.reference is None
        if lost == "after":
            restored = await rebuilt.restore(dependencies=dependencies)
            assert restored.read() == b"private binary body"
            assert rebuilt.pending is None and rebuilt.reference.version == 1
        else:
            with pytest.raises(NativeSnapshotError, match="pending snapshot receipt"):
                await rebuilt.restore(dependencies=dependencies)
            assert rebuilt.pending and not backend.reads
        assert len(backend.saves) == 1
    asyncio.run(scenario())


def test_native_snapshot_retry_reuses_only_identical_pending_request():
    async def scenario():
        snapshot, backend, dependencies = fixture()
        backend.fail_save = "after"
        with pytest.raises(NativeSnapshotError):
            await snapshot.persist(io.BytesIO(b"same"), dependencies=dependencies)
        pending = snapshot.pending
        with pytest.raises(NativeSnapshotError, match="different bytes"):
            await snapshot.persist(io.BytesIO(b"different"), dependencies=dependencies)
        backend.fail_save = ""
        await snapshot.persist(io.BytesIO(b"same"), dependencies=dependencies)
        assert len(backend.saves) == 2 and backend.saves[0][1] == backend.saves[1][1] == pending.request_id
        assert snapshot.reference.version == 1 and snapshot.pending is None
    asyncio.run(scenario())


def test_native_snapshot_wrong_execution_or_serialized_transport_cannot_authorize():
    async def scenario():
        snapshot, backend, dependencies = fixture()
        with pytest.raises(NativeSnapshotError):
            await snapshot.restore()
        def failed_factory(_):
            raise RuntimeError("private-binding-credential")
        broken = Dependencies().bind_factory(SNAPSHOT_DEPENDENCY, failed_factory)
        with pytest.raises(NativeSnapshotError) as failure:
            await snapshot.restore(dependencies=broken)
        assert failure.value.__context__ is None and "private-binding" not in str(failure.value)
        foreign = RuntimeWorkspaceSnapshot(id=str(uuid4()))
        with pytest.raises(NativeSnapshotError, match="bound execution"):
            await foreign.persist(io.BytesIO(b"cross scope"), dependencies=dependencies)
        with pytest.raises(ValidationError):
            RuntimeWorkspaceSnapshot.model_validate({**snapshot.model_dump(), "client_dependency_key": "attacker"})
        with pytest.raises(ValidationError):
            snapshot.id = str(uuid4())
        assert not backend.saves
    asyncio.run(scenario())


def test_native_snapshot_corrupt_receipt_and_download_are_rejected():
    async def scenario():
        snapshot, backend, dependencies = fixture()
        backend.fail_save = "after"
        with pytest.raises(NativeSnapshotError):
            await snapshot.persist(io.BytesIO(b"valid"), dependencies=dependencies)
        pending = snapshot.pending
        backend.receipts[(snapshot.id, pending.request_id)] = SnapshotReference(version=2, sha256=pending.sha256)
        with pytest.raises(NativeSnapshotError, match="receipt differs"):
            await snapshot.restore(dependencies=dependencies)
        assert snapshot.reference is None and snapshot.pending == pending and not backend.reads
        backend.receipts[(snapshot.id, pending.request_id)] = SnapshotReference(version=1, sha256=pending.sha256)
        backend.corrupt_read = True
        with pytest.raises(NativeSnapshotError, match="integrity"):
            await snapshot.restore(dependencies=dependencies)
    asyncio.run(scenario())


@pytest.mark.parametrize("body", [b"", "text", b"x" * (MAX_ARCHIVE_BYTES + 1)], ids=["empty", "text", "oversized"])
def test_native_snapshot_input_is_bounded_and_binary(body):
    async def scenario():
        snapshot, backend, dependencies = fixture()
        stream = io.StringIO(body) if isinstance(body, str) else io.BytesIO(body)
        with pytest.raises(NativeSnapshotError):
            await snapshot.persist(stream, dependencies=dependencies)
        assert not backend.saves and snapshot.pending is None
    asyncio.run(scenario())


def test_native_snapshot_partial_reads_and_concurrent_persistence():
    class ShortReads(io.BytesIO):
        def read(self, size=-1):
            return super().read(min(size, 2))
    async def scenario():
        snapshot, backend, dependencies = fixture()
        backend.gate = asyncio.Event()
        first = asyncio.create_task(snapshot.persist(ShortReads(b"complete stream"), dependencies=dependencies))
        await backend.started.wait()
        second = asyncio.create_task(snapshot.persist(io.BytesIO(b"second"), dependencies=dependencies))
        await asyncio.sleep(0)
        assert len(backend.saves) == 1
        backend.gate.set()
        await asyncio.gather(first, second)
        assert backend.saves[0][3] == b"complete stream"
        assert [call[2] for call in backend.saves] == [0, 1]
        assert snapshot.reference.version == 2
    asyncio.run(scenario())


def test_native_snapshot_cancel_keeps_request_and_state_cannot_invent_parent():
    async def scenario():
        snapshot, backend, dependencies = fixture()
        backend.gate = asyncio.Event()
        task = asyncio.create_task(snapshot.persist(io.BytesIO(b"pending"), dependencies=dependencies))
        await backend.started.wait()
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert snapshot.pending is not None and snapshot.reference is None
        data = snapshot.model_dump()
        data["pending"]["parent_version"] = 9
        with pytest.raises(ValidationError):
            SnapshotBase.parse(data)
    asyncio.run(scenario())
