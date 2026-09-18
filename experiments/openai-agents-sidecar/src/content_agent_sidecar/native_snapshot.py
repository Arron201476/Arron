from __future__ import annotations

import asyncio
import hashlib
import io
from typing import Literal, Protocol
from uuid import UUID, uuid4

from agents.sandbox.session.dependencies import Dependencies
from agents.sandbox.snapshot import SnapshotBase, SnapshotSpec
from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator


SNAPSHOT_DEPENDENCY = "content_agent.runtime_workspace_snapshot"
MAX_ARCHIVE_BYTES = 18 * 1024 * 1024


class NativeSnapshotError(RuntimeError):
    pass


class SnapshotReference(BaseModel):
    model_config = ConfigDict(frozen=True, extra="forbid", hide_input_in_errors=True)
    version: int = Field(ge=1, strict=True)
    sha256: str = Field(pattern=r"^[a-f0-9]{64}$")


class PendingSnapshotSave(BaseModel):
    model_config = ConfigDict(frozen=True, extra="forbid", hide_input_in_errors=True)
    request_id: str = Field(pattern=r"^[a-f0-9]{32}$")
    parent_version: int = Field(ge=0, strict=True)
    sha256: str = Field(pattern=r"^[a-f0-9]{64}$")


class SnapshotTransport(Protocol):
    async def save_snapshot(self, session_id: str, request_id: str, parent_version: int, data: bytes) -> SnapshotReference: ...
    async def read_snapshot(self, session_id: str, reference: SnapshotReference) -> bytes: ...
    async def read_snapshot_receipt(self, session_id: str, pending: PendingSnapshotSave) -> SnapshotReference | None: ...


class RuntimeSnapshotBinding:
    """Trusted session dependency; transport credentials never enter SDK state."""

    def __init__(self, session_id: str, transport: SnapshotTransport):
        if str(UUID(session_id)) != session_id:
            raise ValueError("Snapshot binding requires a canonical session UUID")
        self.session_id = session_id
        self.transport = transport
        self.lock = asyncio.Lock()


class RuntimeWorkspaceSnapshot(SnapshotBase):
    # SnapshotBase's local/remote implementations overwrite a fixed object ID.
    # This provider advances one serialized reference after a durable receipt.
    model_config = ConfigDict(frozen=False, extra="forbid", hide_input_in_errors=True)
    type: Literal["content_agent_workspace"] = Field(default="content_agent_workspace", frozen=True)
    id: str = Field(min_length=36, max_length=36, frozen=True)
    reference: SnapshotReference | None = None
    pending: PendingSnapshotSave | None = None

    @field_validator("id")
    @classmethod
    def canonical_id(cls, value: str) -> str:
        if str(UUID(value)) != value:
            raise ValueError("Snapshot ID must be a canonical session UUID")
        return value

    @model_validator(mode="after")
    def consistent_pending_parent(self) -> RuntimeWorkspaceSnapshot:
        if self.pending and self.pending.parent_version != (self.reference.version if self.reference else 0):
            raise ValueError("Pending snapshot parent must match the serialized verified reference")
        return self

    async def _binding(self, dependencies: Dependencies | None) -> RuntimeSnapshotBinding:
        if dependencies is None:
            raise NativeSnapshotError("Snapshot restore requires current trusted session dependencies")
        failed = False
        try:
            value = await dependencies.get(SNAPSHOT_DEPENDENCY)
        except Exception:
            failed = True
        if failed or not isinstance(value, RuntimeSnapshotBinding) or value.session_id != self.id:
            raise NativeSnapshotError("Snapshot does not belong to the currently bound execution")
        return value

    def _accept(self, pending: PendingSnapshotSave, receipt: object) -> None:
        if not isinstance(receipt, SnapshotReference) or receipt.version != pending.parent_version + 1 or receipt.sha256 != pending.sha256:
            raise NativeSnapshotError("Snapshot receipt differs from the requested parent or bytes")
        if self.pending != pending or (self.reference.version if self.reference else 0) != pending.parent_version:
            raise NativeSnapshotError("Snapshot changed while its persistence request was in flight")
        self.reference = receipt
        self.pending = None

    async def persist(self, data: io.IOBase, *, dependencies: Dependencies | None = None) -> None:
        binding = await self._binding(dependencies)
        async with binding.lock:
            if binding.session_id != self.id:
                raise NativeSnapshotError("Snapshot execution binding changed")
            if not isinstance(data, io.IOBase):
                raise NativeSnapshotError("Snapshot persistence requires a binary stream")
            body = _read_binary_stream(data)
            parent = self.reference.version if self.reference else 0
            digest = hashlib.sha256(body).hexdigest()
            pending = self.pending or PendingSnapshotSave(request_id=uuid4().hex, parent_version=parent, sha256=digest)
            if pending.parent_version != parent or pending.sha256 != digest:
                raise NativeSnapshotError("Resolve the previous snapshot save before persisting different bytes")
            self.pending = pending
            failed = False
            try:
                receipt = await binding.transport.save_snapshot(self.id, pending.request_id, parent, body)
            except Exception:
                failed = True
            if failed:
                # Do not serialize an arbitrary HTTP body or credentials through
                # exception chaining. The pending request remains recoverable.
                raise NativeSnapshotError("Snapshot save receipt is unconfirmed; automatic replacement is forbidden")
            self._accept(pending, receipt)

    async def _resolve_pending(self, binding: RuntimeSnapshotBinding) -> None:
        pending = self.pending
        if pending is None:
            return
        failed = False
        try:
            receipt = await binding.transport.read_snapshot_receipt(self.id, pending)
        except Exception:
            failed = True
        if failed or receipt is None:
            raise NativeSnapshotError("The exact pending snapshot receipt is unavailable; latest-version fallback is forbidden")
        self._accept(pending, receipt)

    async def _read_exact(self, binding: RuntimeSnapshotBinding) -> bytes:
        if binding.session_id != self.id:
            raise NativeSnapshotError("Snapshot execution binding changed")
        await self._resolve_pending(binding)
        reference = self.reference
        if reference is None:
            raise NativeSnapshotError("Snapshot has no verified version")
        failed = False
        try:
            body = await binding.transport.read_snapshot(self.id, reference)
        except Exception:
            failed = True
        if failed:
            raise NativeSnapshotError("The selected snapshot could not be read; no replacement was selected")
        if not isinstance(body, bytes) or not body or len(body) > MAX_ARCHIVE_BYTES or hashlib.sha256(body).hexdigest() != reference.sha256:
            raise NativeSnapshotError("Selected snapshot bytes failed integrity verification")
        return body

    async def restore(self, *, dependencies: Dependencies | None = None) -> io.IOBase:
        binding = await self._binding(dependencies)
        async with binding.lock:
            return io.BytesIO(await self._read_exact(binding))

    async def restorable(self, *, dependencies: Dependencies | None = None) -> bool:
        binding = await self._binding(dependencies)
        async with binding.lock:
            if self.reference is None and self.pending is None:
                return False
            await self._read_exact(binding)
            return True


class RuntimeWorkspaceSnapshotSpec(SnapshotSpec):
    model_config = ConfigDict(frozen=True, extra="forbid", hide_input_in_errors=True)
    type: Literal["content_agent_workspace"] = "content_agent_workspace"

    def build(self, snapshot_id: str) -> RuntimeWorkspaceSnapshot:
        return RuntimeWorkspaceSnapshot(id=snapshot_id)


def _read_binary_stream(data: io.IOBase) -> bytes:
    chunks: list[bytes] = []
    size = 0
    failed = False
    try:
        while size <= MAX_ARCHIVE_BYTES:
            chunk = data.read(min(64 * 1024, MAX_ARCHIVE_BYTES + 1 - size))
            if not isinstance(chunk, bytes):
                failed = True
                break
            if not chunk:
                break
            chunks.append(chunk)
            size += len(chunk)
    except Exception:
        failed = True
    if failed or size == 0 or size > MAX_ARCHIVE_BYTES:
        raise NativeSnapshotError("Snapshot stream is unreadable, empty, non-binary or exceeds the archive limit")
    return b"".join(chunks)
