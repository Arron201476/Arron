from __future__ import annotations

import io
from pathlib import Path
import re
import stat
from uuid import uuid4

from agents.sandbox.files import EntryKind, FileEntry
from agents.sandbox.types import ExecResult, Permissions

from .backend import BackendError
from .native_execution import current_native_patch_call
from .native_files import MAX_FILE_BYTES, SAFE_FILE_ERRORS, NativeFileOperation, NativeFileResult
from .native_session import PendingWorkspaceFile, RuntimeSandboxSession


class RuntimeSandboxFileSession(RuntimeSandboxSession):
    """Confined SDK file IO with approved patches or exact initial resources."""

    def __init__(self, state, transport):
        super().__init__(state, transport)
        self._file_lock = self._io_lock

    def _relative_path(self, path: str | Path, *, user=None, for_write=False) -> str:
        self._check_binding()
        if user is not None:
            raise BackendError("File access cannot switch the fixed sandbox user")
        # Preserve the SDK's lexical workspace scope. The OCI helper separately
        # validates the real tree, forbids symlinks, and holds processes still.
        normalized = self.normalize_path(path, for_write=for_write)
        return self._workspace_path_policy().relative_path(normalized).as_posix()

    async def _file(self, operation: NativeFileOperation) -> NativeFileResult:
        operation.payload()
        call = current_native_patch_call()
        materialization_hash = self._materializing if operation.mutates else ""
        if materialization_hash and call is not None:
            raise BackendError("Initialization cannot inherit model patch authority")
        if operation.mutates and call is None and not materialization_hash:
            raise BackendError("File mutation has no active approved SDK patch invocation")
        async with self._file_lock:
            self._check_binding()
            if self._start_failed or self.state.pending_file is not None or self.state.pending_pty:
                raise BackendError("An unconfirmed file operation requires explicit recovery; it cannot be replayed")
            if self._cleanup_pending or self._persisted_reference is not None:
                raise BackendError("Workspace persistence has started; further file access requires a resumed session")
            if not self._started and not self._fresh:
                raise BackendError("File access requires a started or initializing workspace")
            pending = PendingWorkspaceFile(request_id=uuid4().hex, request_hash=operation.request_hash)
            self.state.pending_file = pending
            try:
                if materialization_hash:
                    result = await self.transport.file_operation(pending.request_id, operation, materialization_hash=materialization_hash)
                else:
                    result = await self.transport.file_operation(pending.request_id, operation, call)
            except BackendError as error:
                if error.status_code in {400, 409} and error.code in SAFE_FILE_ERRORS:
                    self.state.pending_file = None
                    if error.code == "WORKSPACE_FILE_NOT_FOUND":
                        raise FileNotFoundError("The requested native workspace file does not exist") from None
                raise
            self.state.pending_file = None
            return result

    async def read(self, path, *, user=None) -> io.IOBase:
        relative = self._relative_path(path, user=user)
        result = await self._file(NativeFileOperation("read", relative))
        return io.BytesIO(result.data)

    async def write(self, path, data: io.IOBase, *, user=None) -> None:
        relative = self._relative_path(path, user=user, for_write=True)
        if not isinstance(data, io.IOBase):
            raise BackendError("Native file writes require a bounded binary stream")
        body = bytearray()
        while True:
            part = data.read(min(65536, MAX_FILE_BYTES + 1 - len(body)))
            if not isinstance(part, bytes):
                raise BackendError("Native file writes require binary stream contents")
            body.extend(part)
            if len(body) > MAX_FILE_BYTES:
                raise BackendError("Native file exceeds its binary write limit")
            if not part:
                break
        await self._file(NativeFileOperation("write", relative, data=bytes(body)))

    async def ls(self, path, *, user=None) -> list[FileEntry]:
        result = await self._file(NativeFileOperation("list", self._relative_path(path, user=user)))
        return [FileEntry(path="/workspace/" + entry.path, owner="65532", group="65532", size=entry.size_bytes,
                          kind=EntryKind.DIRECTORY if entry.directory else EntryKind.FILE,
                          permissions=Permissions.from_mode(entry.mode | (stat.S_IFDIR if entry.directory else stat.S_IFREG)))
                for entry in result.entries]

    async def mkdir(self, path, *, parents=False, user=None) -> None:
        relative = self._relative_path(path, user=user, for_write=True)
        await self._file(NativeFileOperation("mkdir", relative, parents=parents))

    async def rm(self, path, *, recursive=False, user=None) -> None:
        relative = self._relative_path(path, user=user, for_write=True)
        await self._file(NativeFileOperation("remove", relative, recursive=recursive))

    async def _exec_workspace_helper(self, *command, timeout=None) -> ExecResult:
        values = tuple(str(value) for value in command)
        # SDK BaseEntry._apply_metadata emits this exact non-shell operation.
        # It keeps the same write authority as write/mkdir; no generic exec.
        if len(values) == 3 and values[0] == "chmod" and re.fullmatch(r"0[0-7]{3}", values[1]):
            path = self._relative_path(values[2], for_write=True)
            await self._file(NativeFileOperation("chmod", path, mode=int(values[1], 8)))
            return ExecResult(stdout=b"", stderr=b"", exit_code=0)
        raise BackendError("This SDK helper has no confined Runtime operation; arbitrary helper execution is forbidden")
