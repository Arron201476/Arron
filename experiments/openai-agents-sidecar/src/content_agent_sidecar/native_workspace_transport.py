from __future__ import annotations

import hashlib
import base64
import binascii
import json
import posixpath
import re
import secrets
from typing import Any
from .native_files import NativeFileOperation, NativeFileResult
from .native_execution import NativePatchCall
from urllib.parse import quote, urlencode
from uuid import UUID

from agents.sandbox.types import ExecResult

from pydantic import AwareDatetime, BaseModel, ConfigDict, Field, ValidationError, field_validator

from .backend import BackendClient, BackendError
from .native_snapshot import MAX_ARCHIVE_BYTES, PendingSnapshotSave, SnapshotReference


class NativeWorkspaceLease(BaseModel):
    model_config = ConfigDict(frozen=True, extra="forbid", hide_input_in_errors=True)
    session_id: str
    generation: int = Field(ge=1, strict=True)
    lease_until: AwareDatetime
    snapshot_version: int = Field(ge=0, le=128, strict=True)

    @field_validator("session_id")
    @classmethod
    def validate_id(cls, value):
        if str(UUID(value)) != value:
            raise ValueError("Workspace session ID must be canonical")
        return value


class NativeWorkspaceHTTPTransport:
    """Execution-scoped transport. This object must never enter SDK state."""

    def __init__(self, backend: BackendClient, holder_key: str, dispatch_generation: int):
        if not re.fullmatch(r"[a-f0-9]{64}", holder_key) or type(dispatch_generation) is not int or dispatch_generation < 0:
            raise BackendError("Invalid workspace transport identity")
        self._backend = backend
        self._holder_key = holder_key
        self._dispatch = dispatch_generation
        self._activity = backend.native_workspace_activity()
        if bool(self._activity.get("X-Agent-Turn-ID")) != (dispatch_generation > 0):
            raise BackendError("Workspace dispatch does not match its execution mode")
        self.lease: NativeWorkspaceLease | None = None

    @classmethod
    def new(cls, backend: BackendClient, dispatch_generation: int = 0):
        return cls(backend, secrets.token_hex(32), dispatch_generation)

    def _headers(self, *, require_lease=True):
        if self._backend.native_workspace_activity() != self._activity:
            raise BackendError("Native workspace transport cannot move to a different execution")
        headers = {"X-Agent-Dispatch-Generation": str(self._dispatch), "X-Workspace-Lease-Key": self._holder_key}
        if require_lease:
            if self.lease is None:
                raise BackendError("Native workspace lease has not been acquired")
            headers["X-Workspace-Lease-Generation"] = str(self.lease.generation)
        return headers

    def _path(self, suffix: str, session_id: str | None = None):
        if self.lease is None or session_id is not None and session_id != self.lease.session_id:
            raise BackendError("Snapshot does not belong to this workspace lease")
        return f"/internal/v1/native-workspaces/{self.lease.session_id}/{suffix}"

    @staticmethod
    def _data(result: Any) -> dict[str, Any]:
        if not isinstance(result, dict) or not isinstance(result.get("data"), dict):
            raise BackendError("Workspace response data must be an object")
        return result["data"]

    @staticmethod
    def _lease(data: Any) -> NativeWorkspaceLease:
        try:
            return NativeWorkspaceLease.model_validate(data)
        except ValidationError:
            pass
        raise BackendError("Workspace lease receipt is invalid")

    async def current(self) -> NativeWorkspaceLease | None:
        result, _ = await self._backend.native_workspace_request("GET", "/internal/v1/native-workspaces/current", self._headers(require_lease=False))
        data = self._data(result)
        if set(data) != {"lease"}:
            raise BackendError("Current workspace receipt is invalid")
        return None if data["lease"] is None else self._lease(data["lease"])

    async def reserve(self, expected_generation: int = 0, *, expected_session_id: str | None = None) -> NativeWorkspaceLease:
        if type(expected_generation) is not int or expected_generation < 0:
            raise BackendError("Expected workspace generation is invalid")
        if expected_generation > 0 and self.lease is None and expected_session_id is None:
            raise BackendError("Workspace takeover requires the previously discovered session ID")
        result, _ = await self._backend.native_workspace_request("POST", "/internal/v1/native-workspaces/reservations",
                                                               self._headers(require_lease=False), payload={"expected_generation": expected_generation})
        lease = self._lease(self._data(result))
        if expected_session_id is not None and lease.session_id != expected_session_id:
            raise BackendError("Workspace reservation belongs to a different session")
        if self.lease is not None and (lease.session_id != self.lease.session_id or lease.generation != self.lease.generation):
            raise BackendError("Renewal unexpectedly replaced the workspace binding")
        if expected_generation == 0 and self.lease is None and lease.generation != 1:
            raise BackendError("Initial workspace reservation has an unexpected generation")
        if expected_generation > 0 and lease.generation not in {expected_generation, expected_generation + 1}:
            raise BackendError("Workspace takeover returned an unexpected generation")
        self.lease = lease
        return lease

    async def validate(self) -> NativeWorkspaceLease:
        result, _ = await self._backend.native_workspace_request("GET", self._path("lease"), self._headers())
        lease = self._lease(self._data(result))
        if self.lease is None or (lease.session_id, lease.generation) != (self.lease.session_id, self.lease.generation):
            raise BackendError("Validated workspace no longer matches its lease")
        self.lease = lease
        return lease

    async def ensure(self):
        result, _ = await self._backend.native_workspace_request("POST", self._path("ensure"), self._headers(), payload={})
        return self._environment(result)

    async def publish_files(self, agent_tool_call_id: str, sdk_tool_call_id: str, arguments: dict):
        from .native_publication import PublicationArguments

        validated = PublicationArguments.model_validate(arguments)
        if not isinstance(agent_tool_call_id, str) or not 1 <= len(agent_tool_call_id) <= 128 or not isinstance(sdk_tool_call_id, str) or not 1 <= len(sdk_tool_call_id) <= 512:
            raise BackendError("Publication requires bounded audited call identities")
        result, _ = await self._backend.native_workspace_request("POST", self._path("publications"), self._headers(),
            payload={"agent_tool_call_id": agent_tool_call_id, "sdk_tool_call_id": sdk_tool_call_id, "arguments": validated.model_dump()})
        return self._data(result)

    async def publish_memory(self, agent_tool_call_id: str, sdk_tool_call_id: str, arguments: dict):
        from .memory_publication import MemoryPublicationArguments

        validated = MemoryPublicationArguments.model_validate(arguments)
        if not isinstance(agent_tool_call_id, str) or not 1 <= len(agent_tool_call_id) <= 128 or not isinstance(sdk_tool_call_id, str) or not 1 <= len(sdk_tool_call_id) <= 512:
            raise BackendError("Memory publication requires bounded audited call identities")
        result, _ = await self._backend.native_workspace_request("POST", self._path("memory-publications"), self._headers(),
            payload={"agent_tool_call_id": agent_tool_call_id, "sdk_tool_call_id": sdk_tool_call_id, "arguments": validated.model_dump()})
        return self._data(result)

    async def recovery(self):
        from .native_manifest import NativeManifestInventory

        result, _ = await self._backend.native_workspace_request("GET", self._path("recovery"), self._headers())
        data = self._data(result)
        if set(data) != {"manifest", "snapshot"}:
            raise BackendError("Workspace recovery receipt is invalid")
        try:
            manifest = NativeManifestInventory.model_validate(data["manifest"])
            snapshot = SnapshotReference.model_validate(data["snapshot"])
            if (self.lease is not None and manifest.session_id == self.lease.session_id
                    and snapshot.version == self.lease.snapshot_version):
                return manifest, snapshot
        except (ValueError, TypeError):
            pass
        raise BackendError("Workspace recovery differs from the current authoritative version")

    async def renew(self) -> NativeWorkspaceLease:
        result, _ = await self._backend.native_workspace_request("POST", self._path("lease/renew"), self._headers(), payload={})
        lease = self._lease(self._data(result))
        if self.lease is None or (lease.session_id, lease.generation) != (self.lease.session_id, self.lease.generation):
            raise BackendError("Workspace renewal returned a different lease")
        # A concurrent snapshot save may have already advanced this local
        # version; an older renewal receipt cannot roll it back.
        self.lease = lease.model_copy(update={"snapshot_version": max(lease.snapshot_version, self.lease.snapshot_version)})
        return self.lease

    async def probe(self) -> str:
        result, _ = await self._backend.native_workspace_request("POST", self._path("probe"), self._headers(), payload={})
        data = self._data(result)
        if (self.lease is None or set(data) != {"session_id", "state"}
                or data["session_id"] != self.lease.session_id or not isinstance(data["state"], str)
                or data["state"] not in {"ready", "absent", "closed"}):
            raise BackendError("Workspace liveness is unconfirmed")
        return data["state"]

    async def shutdown(self) -> None:
        result, _ = await self._backend.native_workspace_request("POST", self._path("shutdown"), self._headers(), payload={})
        data = self._data(result)
        if self.lease is None or data != {"session_id": self.lease.session_id, "state": "closed"}:
            raise BackendError("Workspace shutdown is unconfirmed")

    def _environment(self, result):
        data = self._data(result)
        if self.lease is None or data != {"session_id": self.lease.session_id, "state": "ready"}:
            raise BackendError("Workspace environment readiness is unconfirmed")
        return data

    async def restore_environment(self, request_id: str, reference: SnapshotReference):
        if not re.fullmatch(r"[a-f0-9]{32}", request_id):
            raise BackendError("Recovery request identity is invalid")
        result, _ = await self._backend.native_workspace_request("POST", self._path("restore"), self._headers(),
                                                               payload={"request_id":request_id, **reference.model_dump()})
        return self._environment(result)

    def _archive(self, result, metadata, version, expected_hash=None):
        if self.lease is None or not isinstance(result, bytes) or not result or len(result) > MAX_ARCHIVE_BYTES:
            raise BackendError("Workspace response is not an archive")
        digest = hashlib.sha256(result).hexdigest()
        if metadata != {"X-Workspace-Session-ID":self.lease.session_id, "X-Workspace-Snapshot-Version":str(version), "X-Workspace-Snapshot-Sha256":digest} or expected_hash is not None and digest != expected_hash:
            raise BackendError("Workspace archive binding or content integrity changed")
        return result

    async def export_workspace(self) -> bytes:
        result, metadata = await self._backend.native_workspace_request("POST", self._path("export"), self._headers(), payload={}, binary=True)
        return self._archive(result,metadata,0)

    def _reference(self, data, session_id):
        if not isinstance(data, dict) or set(data) != {"session_id", "version", "sha256"} or data["session_id"] != session_id:
            raise BackendError("Snapshot receipt belongs to another workspace")
        try:
            return SnapshotReference.model_validate({"version":data["version"],"sha256":data["sha256"]})
        except ValidationError:
            pass
        raise BackendError("Snapshot receipt has an invalid version or hash")

    async def save_snapshot(self, session_id: str, request_id: str, parent_version: int, data: bytes) -> SnapshotReference:
        if not re.fullmatch(r"[a-f0-9]{32}",request_id) or type(parent_version) is not int or not 0 <= parent_version <= 128:
            raise BackendError("Snapshot save identity is invalid")
        if not isinstance(data, bytes) or not data or len(data) > MAX_ARCHIVE_BYTES:
            raise BackendError("Snapshot archive exceeds its transport bound")
        digest = hashlib.sha256(data).hexdigest()
        headers = {**self._headers(),"X-Workspace-Snapshot-Parent":str(parent_version),"X-Workspace-Snapshot-Sha256":digest}
        result, _ = await self._backend.native_workspace_request("PUT",self._path(f"snapshots/{request_id}",session_id),headers,archive=data)
        reference = self._reference(self._data(result),session_id)
        if reference.version != parent_version+1 or reference.sha256 != digest:
            raise BackendError("Snapshot save receipt differs from the submitted archive")
        return reference

    async def read_snapshot(self, session_id: str, reference: SnapshotReference) -> bytes:
        suffix = f"snapshots/{reference.version}?{urlencode({'sha256':reference.sha256})}"
        result, metadata = await self._backend.native_workspace_request("GET",self._path(suffix,session_id),self._headers(),binary=True)
        return self._archive(result,metadata,reference.version,reference.sha256)

    async def read_snapshot_receipt(self, session_id: str, pending: PendingSnapshotSave) -> SnapshotReference | None:
        query = urlencode({"parent_version":pending.parent_version,"sha256":pending.sha256})
        result, _ = await self._backend.native_workspace_request("GET",self._path(f"snapshot-receipts/{quote(pending.request_id,safe='')}?{query}",session_id),self._headers())
        data = self._data(result)
        if set(data) != {"receipt"}: raise BackendError("Snapshot request receipt is invalid")
        reference = None if data["receipt"] is None else self._reference(data["receipt"],session_id)
        if reference is not None and (reference.version != pending.parent_version+1 or reference.sha256 != pending.sha256):
            raise BackendError("Snapshot request receipt does not match the original request")
        return reference

    async def execute_command(
        self, call_id: str, sdk_call_id: str, arguments: dict[str, Any], argv: list[str], *,
        cwd: str = "/workspace", stdin: bytes = b"", timeout_ms: int = 0, configuration_hash: str = "",
    ) -> ExecResult:
        if (not isinstance(call_id, str) or not call_id or len(call_id) > 128
                or not isinstance(sdk_call_id, str) or not sdk_call_id or len(sdk_call_id) > 512
                or not isinstance(arguments, dict) or configuration_hash != ""
                or not isinstance(argv, list) or not 1 <= len(argv) <= 256
                or any(not isinstance(value, str) or "\x00" in value for value in argv)
                or sum(len(value.encode("utf-8")) for value in argv) > 64 << 10
                or not argv[0].strip() or argv[0].startswith("-") or "=" in argv[0]
                or not isinstance(stdin, bytes) or len(stdin) > 1 << 20
                or type(timeout_ms) is not int or not 0 <= timeout_ms <= 30_000
                or not isinstance(cwd, str) or posixpath.normpath(cwd) != cwd
                or cwd != "/workspace" and not cwd.startswith("/workspace/")
                or any(char in cwd for char in "\\\x00\r\n")):
            raise BackendError("Invalid bounded native command request")
        command = {"argv": list(argv), "cwd": cwd}
        if stdin:
            command["stdin"] = base64.b64encode(stdin).decode("ascii")
        if timeout_ms:
            command["timeout_ms"] = timeout_ms
        if len(json.dumps(arguments, ensure_ascii=False).encode("utf-8")) > 64 << 10:
            raise BackendError("Original SDK command arguments exceed their transport bound")
        # Match the fixed Go WorkspaceCommand struct order and encoding/json escaping.
        command_json = json.dumps(command, ensure_ascii=False, separators=(",", ":"))
        for value, escaped in (("<", "\\u003c"), (">", "\\u003e"), ("&", "\\u0026"), ("\u2028", "\\u2028"), ("\u2029", "\\u2029")):
            command_json = command_json.replace(value, escaped)
        command_hash = hashlib.sha256(command_json.encode("utf-8")).hexdigest()
        result, _ = await self._backend.native_workspace_request(
            "POST", self._path("commands"), self._headers(), payload={
                "agent_tool_call_id": call_id, "sdk_tool_call_id": sdk_call_id, "arguments": arguments,
                "configuration_hash": configuration_hash, "command": command,
            },
        )
        data = self._data(result)
        if (set(data) != {"session_id", "agent_tool_call_id", "command_hash", "result_hash", "stdout", "stderr", "exit_code"}
                or self.lease is None or data["session_id"] != self.lease.session_id
                or data["agent_tool_call_id"] != call_id or data["command_hash"] != command_hash
                or type(data["exit_code"]) is not int or not 0 <= data["exit_code"] <= 255):
            raise BackendError("Command receipt differs from its execution or original request")
        streams = []
        for name in ("stdout", "stderr"):
            value = data[name]
            if not isinstance(value, str) or len(value) > ((1 << 20) + 2) // 3 * 4:
                raise BackendError("Command output exceeds its binary bound")
            decoded = None
            try:
                decoded = base64.b64decode(value, validate=True)
            except (ValueError, binascii.Error):
                pass
            if decoded is None or len(decoded) > 1 << 20 or base64.b64encode(decoded).decode("ascii") != value:
                raise BackendError("Command output is not canonical bounded binary")
            streams.append(decoded)
        encoded_result = json.dumps({"Stdout": data["stdout"], "Stderr": data["stderr"], "ExitCode": data["exit_code"]}, separators=(",", ":"))
        if data["result_hash"] != hashlib.sha256(encoded_result.encode()).hexdigest():
            raise BackendError("Command result integrity is unconfirmed")
        return ExecResult(stdout=streams[0], stderr=streams[1], exit_code=data["exit_code"])

    async def pty_start(self, call_id, sdk_call_id, arguments, argv, *, cwd="/workspace", tty=False,
                        yield_ms=1000, timeout_ms=0):
        from .native_pty import pty_start_operation, validate_pty_identity

        validate_pty_identity(call_id, sdk_call_id, arguments)
        operation = pty_start_operation(argv, cwd=cwd, tty=tty, yield_ms=yield_ms, timeout_ms=timeout_ms)
        if arguments.get("tty", False) is not tty:
            raise BackendError("Terminal mode differs from the original SDK approval")
        return await self._pty_exchange(call_id, sdk_call_id, arguments, operation)

    async def pty_input(self, call_id, sdk_call_id, arguments, *, session_id, expected_sequence,
                        chars="", yield_ms=1000, expected_process_id):
        from .native_pty import pty_input_operation, validate_pty_identity

        validate_pty_identity(call_id, sdk_call_id, arguments)
        operation = pty_input_operation(session_id, expected_sequence, chars, yield_ms)
        if (type(arguments.get("session_id")) is not int or arguments["session_id"] != session_id
                or arguments.get("chars", "") != chars or not isinstance(expected_process_id, str)
                or not re.fullmatch(r"[a-f0-9]{32}", expected_process_id)):
            raise BackendError("Terminal input differs from its approved process and characters")
        return await self._pty_exchange(call_id, sdk_call_id, arguments, operation, expected_process_id=expected_process_id)

    async def terminate_pty(self):
        result, _ = await self._backend.native_workspace_request("POST", self._path("pty/terminate"), self._headers(), payload={})
        if self.lease is None or self._data(result) != {"session_id": self.lease.session_id, "state": "stopped"}:
            raise BackendError("Terminal cleanup was not confirmed for this workspace")

    async def pty_state(self):
        from .native_live_state import NativePTYState

        result, _ = await self._backend.native_workspace_request("GET", self._path("pty/state"), self._headers())
        try:
            state = NativePTYState.model_validate(self._data(result))
            if self.lease is None or state.session_id != self.lease.session_id:
                raise ValueError("Workspace identity changed")
        except (ValueError, TypeError):
            raise BackendError("Terminal recovery binding was not confirmed for this workspace") from None
        return state

    async def _pty_exchange(self, call_id, sdk_call_id, arguments, operation, *, expected_process_id=None):
        from .native_pty import parse_pty_receipt

        result, _ = await self._backend.native_workspace_request("POST", self._path("pty"), self._headers(),
            payload={"agent_tool_call_id": call_id, "sdk_tool_call_id": sdk_call_id, "arguments": arguments, **operation})
        return parse_pty_receipt(self._data(result), workspace_id=self.lease.session_id, call_id=call_id,
                                 operation=operation, expected_process_id=expected_process_id)

    async def file_operation(self, request_id: str, operation: NativeFileOperation, call: NativePatchCall | None = None,
                             *, materialization_hash: str = "") -> NativeFileResult:
        if not isinstance(request_id, str) or not re.fullmatch(r"[a-f0-9]{32}", request_id) or type(operation) is not NativeFileOperation:
            raise BackendError("Native file request identity is invalid")
        payload = operation.payload()
        if materialization_hash and (not isinstance(materialization_hash, str) or not re.fullmatch(r"[a-f0-9]{64}", materialization_hash)
                                     or not operation.mutates or call is not None):
            raise BackendError("Initial file mutation requires its exclusive inventory binding")
        if (operation.mutates and call is None and not materialization_hash or call is not None and
                (type(call) is not NativePatchCall or not isinstance(call.agent_tool_call_id, str) or not call.agent_tool_call_id
                 or len(call.agent_tool_call_id) > 128 or not isinstance(call.sdk_tool_call_id, str) or not call.sdk_tool_call_id
                 or len(call.sdk_tool_call_id) > 512 or not isinstance(call.arguments_hash, str)
                 or not re.fullmatch(r"[a-f0-9]{64}", call.arguments_hash))):
            raise BackendError("Native file writes require an approved original patch invocation")
        request = {
            "request_id": request_id, "file": payload,
            "agent_tool_call_id": call.agent_tool_call_id if call else "",
            "sdk_tool_call_id": call.sdk_tool_call_id if call else "",
            "arguments_hash": call.arguments_hash if call else "", "configuration_hash": "",
        }
        if materialization_hash:
            request["materialization_hash"] = materialization_hash
        result, _ = await self._backend.native_workspace_request("POST", self._path("files"), self._headers(), payload=request)
        data = self._data(result)
        if (set(data) != {"session_id", "request_id", "request_hash", "file"} or self.lease is None
                or data["session_id"] != self.lease.session_id or data["request_id"] != request_id
                or data["request_hash"] != operation.request_hash):
            raise BackendError("File receipt belongs to a different operation or execution")
        return NativeFileResult.from_wire(operation, data["file"])

    async def prepare_manifest(self, sources):
        from .native_manifest import NativeManifestInventory, NativeManifestSources

        if type(sources) is not NativeManifestSources:
            raise BackendError("Initial resources require validated version references")
        result, _ = await self._backend.native_workspace_request("POST", self._path("manifest"), self._headers(), payload=sources.model_dump())
        inventory = None
        try:
            inventory = NativeManifestInventory.model_validate(self._data(result))
        except Exception:
            pass
        if inventory is None or self.lease is None or inventory.session_id != self.lease.session_id:
            raise BackendError("Initial resource inventory is not valid for this execution")
        skills = {item.model_dump_json() for item in sources.skills}
        files = {item.model_dump_json() for item in sources.files}
        actual_skills = {item.skill.model_dump_json() for item in inventory.files if item.skill}
        actual_files = {item.project_file.model_dump_json() for item in inventory.files if item.project_file}
        memory = {sources.memory.model_dump_json()} if sources.memory else set()
        actual_memory = {item.memory.model_dump_json() for item in inventory.files if item.memory}
        if skills != actual_skills or files != actual_files or memory != actual_memory:
            raise BackendError("Initial resource inventory differs from the requested version selection")
        return inventory

    async def read_manifest_file(self, manifest_hash, file):
        from .native_manifest import NativeManifestFile

        if type(file) is not NativeManifestFile or not isinstance(manifest_hash, str) or not re.fullmatch(r"[a-f0-9]{64}", manifest_hash):
            raise BackendError("Initial resource read requires an exact inventory file")
        result, _ = await self._backend.native_workspace_request("POST", self._path("manifest/file"), self._headers(),
                                                                payload={"manifest_hash": manifest_hash, "path": file.path})
        data = self._data(result)
        if (set(data) != {"session_id", "manifest_hash", "path", "content"} or self.lease is None
                or data["session_id"] != self.lease.session_id or data["manifest_hash"] != manifest_hash or data["path"] != file.path
                or not isinstance(data["content"], str) or len(data["content"]) > ((file.size_bytes+2)//3)*4):
            raise BackendError("Initial resource response differs from its selected inventory")
        body = None
        try:
            body = base64.b64decode(data["content"], validate=True)
        except (ValueError, binascii.Error):
            pass
        if (body is None or len(body) != file.size_bytes or hashlib.sha256(body).hexdigest() != file.sha256
                or base64.b64encode(body).decode("ascii") != data["content"]):
            raise BackendError("Initial resource bytes failed their exact size or hash check")
        return body

    async def seal_manifest(self, manifest_hash):
        if not isinstance(manifest_hash, str) or not re.fullmatch(r"[a-f0-9]{64}", manifest_hash):
            raise BackendError("Initial resource seal requires an exact inventory binding")
        result, _ = await self._backend.native_workspace_request("POST", self._path("manifest/seal"), self._headers(),
                                                                payload={"manifest_hash": manifest_hash})
        expected = {"session_id": self.lease.session_id, "manifest_hash": manifest_hash, "status": "completed"}
        if self._data(result) != expected:
            raise BackendError("Initial resource materialization has no confirmed completion receipt")
