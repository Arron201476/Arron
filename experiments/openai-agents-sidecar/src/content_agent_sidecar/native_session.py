from __future__ import annotations

import abc
import asyncio
import hashlib
import inspect
import io
import math
from pathlib import Path
from typing import Literal
from uuid import UUID, uuid4

from agents.sandbox.entries import Dir, File
from agents.sandbox.manifest import Manifest
from agents.sandbox.session.base_sandbox_session import BaseSandboxSession
from agents.sandbox.session.dependencies import Dependencies
from agents.sandbox.session.sandbox_client import BaseSandboxClient, BaseSandboxClientOptions
from agents.sandbox.session.sandbox_session import SandboxSession
from agents.sandbox.session.sandbox_session_state import SandboxSessionState
from agents.sandbox.types import ExecResult
from pydantic import BaseModel, ConfigDict, Field, model_validator

from .backend import BackendError
from .native_execution import current_native_shell_call
from .native_manifest import NativeManifestSources, RuntimeResourceFile, resource_manifest_hash
from .native_snapshot import (
    SNAPSHOT_DEPENDENCY, RuntimeSnapshotBinding, RuntimeWorkspaceSnapshot, RuntimeWorkspaceSnapshotSpec,
    SnapshotReference, _read_binary_stream,
)
from .native_workspace_transport import NativeWorkspaceHTTPTransport


class RuntimeSandboxOptions(BaseSandboxClientOptions):
    model_config = ConfigDict(frozen=True, extra="forbid", hide_input_in_errors=True)
    type: Literal["content_agent_workspace"] = "content_agent_workspace"


class PendingWorkspaceRestore(BaseModel):
    model_config = ConfigDict(frozen=True, extra="forbid", hide_input_in_errors=True)
    request_id: str = Field(pattern=r"^[a-f0-9]{32}$")
    reference: SnapshotReference


class PendingWorkspaceFile(BaseModel):
    model_config = ConfigDict(frozen=True, extra="forbid", hide_input_in_errors=True)
    request_id: str = Field(pattern=r"^[a-f0-9]{32}$")
    request_hash: str = Field(pattern=r"^[a-f0-9]{64}$")


class RuntimeSandboxState(SandboxSessionState):
    model_config = ConfigDict(arbitrary_types_allowed=True, extra="forbid", hide_input_in_errors=True)
    type: Literal["content_agent_workspace"] = "content_agent_workspace"
    workspace_root_ready: bool = Field(default=False, strict=True)
    pending_restore: PendingWorkspaceRestore | None = None
    pending_file: PendingWorkspaceFile | None = None
    pending_pty: bool = Field(default=False, strict=True)
    active_pty: bool = Field(default=False, strict=True)
    materialization_hash: str = Field(default="", pattern=r"^([a-f0-9]{64})?$")

    @model_validator(mode="after")
    def validate_binding(self):
        if type(self.snapshot) is not RuntimeWorkspaceSnapshot or self.snapshot.id != str(self.session_id):
            raise ValueError("Runtime workspace state requires its own exact snapshot binding")
        if self.exposed_ports or self.path_grants_require_rebind or self.mount_authority_redacted:
            raise ValueError("Runtime workspace state cannot grant ports or host paths")
        _validate_manifest(self.manifest)
        if resource_manifest_hash(self.manifest) not in {"", self.materialization_hash}:
            raise ValueError("Resource entries must retain their exact initialization binding")
        if self.pending_restore and self.pending_restore.reference != self.snapshot.reference:
            raise ValueError("Pending recovery must retain its exact snapshot reference")
        return self


def _validate_manifest(manifest: Manifest) -> None:
    if (type(manifest) is not Manifest or manifest.root != "/workspace" or manifest.environment.value
            or manifest.users or manifest.groups or manifest.extra_path_grants):
        raise BackendError("Runtime workspaces require a fixed root and no host, account, environment or mount grants")
    total = 0
    for index, (_, entry) in enumerate(manifest.iter_entries(), start=1):
        if type(entry) not in {Dir, File, RuntimeResourceFile} or entry.ephemeral or entry.group is not None or index > 1024:
            raise BackendError("Runtime manifests require bounded fixed files and directories")
        if type(entry) is File:
            total += len(entry.content)
            if len(entry.content) > 1 << 20 or total > 16 << 20:
                raise BackendError("Runtime manifest files exceed their bounds")
        if type(entry) is RuntimeResourceFile:
            total += entry.resource.size_bytes
            if total > 16 << 20 or entry.permissions.to_mode() != 0o644 or entry.is_dir:
                raise BackendError("Initial resource permissions or byte budget differ from the Runtime inventory")
    # SDK File state uses JSON UTF-8 bytes. Binary input must use verified
    # workspace files/snapshots, not an unserializable inline manifest entry.
    failed = False
    try:
        manifest.model_dump_json()
    except Exception:
        failed = True
    if failed:
        raise BackendError("Manifest cannot round-trip through native SDK state")


class RuntimeSandboxSession(BaseSandboxSession):
    """Native lifecycle base; a trusted filesystem implementation is required.

    read/write remain abstract. This base is deliberately not a production
    provider until confined filesystem and SDK-helper operations are supplied.
    """

    def __init__(self, state: RuntimeSandboxState, transport: NativeWorkspaceHTTPTransport):
        self.state = state
        self.transport = transport
        self._manifest = state.manifest.model_copy(deep=True)
        self._materialization_hash = state.materialization_hash
        self._materializing = ""
        self._start_lock = asyncio.Lock()
        self._io_lock = asyncio.Lock()
        self._started = False
        self._start_failed = False
        self._closed = False
        self._fresh = False
        self._restored = False
        self._cleanup_pending = False
        self._persisted_reference: SnapshotReference | None = None
        self._persist_lock = asyncio.Lock()
        self._shutdown_lock = asyncio.Lock()
        self._persist_attempted = False
        self._shutdown_attempted = False

    def _check_binding(self) -> None:
        if (self._closed or self.transport.lease is None
                or str(self.state.session_id) != self.transport.lease.session_id
                or type(self.state.snapshot) is not RuntimeWorkspaceSnapshot
                or self.state.snapshot.id != self.transport.lease.session_id
                or self.state.manifest != self._manifest or self.state.exposed_ports):
            raise BackendError("Native session no longer matches its trusted execution binding")
        if self.state.materialization_hash != self._materialization_hash:
            raise BackendError("Native initialization authority no longer matches its trusted binding")

    async def start(self) -> None:
        async with self._start_lock:
            self._check_binding()
            if self._start_failed or self.state.pending_file is not None or self.state.pending_pty:
                raise BackendError("Failed workspace startup requires explicit state recovery")
            if self.state.active_pty and not self._started:
                raise BackendError("A file snapshot cannot restore a live terminal; authoritative process rebinding is required")
            if self._started:
                if not await self.running():
                    raise BackendError("Workspace disappeared; implicit empty recreation is forbidden")
                return
            try:
                await super().start()
            except BaseException:
                self._start_failed = True
                raise
            self._started = True

    async def _ensure_backend_started(self) -> None:
        self._check_binding()
        await self.transport.validate()
        snapshot = self.state.snapshot
        if snapshot.reference is not None or snapshot.pending is not None:
            stream = await snapshot.restore(dependencies=self.dependencies)
            try:
                await self.hydrate_workspace(stream)
            finally:
                stream.close()
            self._restored = True
        else:
            status = await self.transport.probe()
            if status == "absent" and not self.state.workspace_root_ready:
                await self.transport.ensure()
                self._fresh = True
            elif status != "ready" or not self.state.workspace_root_ready:
                raise BackendError("Workspace has no verified recovery state; empty recreation is forbidden")
        self._set_start_state_preserved(not self._fresh, system=True)

    async def _probe_workspace_root_for_preserved_resume(self) -> bool:
        # OCI and Runtime have already checked the fixed workspace root. Do not
        # synthesize an unaudited model command just to repeat that probe.
        if self._restored:
            self._mark_workspace_root_ready_from_probe()
        return self._can_reuse_preserved_workspace_on_resume()

    async def _start_workspace(self) -> None:
        if self._fresh:
            self._materializing = self._materialization_hash
            try:
                await self._apply_manifest(provision_accounts=False)
                if self._materializing:
                    await self.transport.seal_manifest(self._materializing)
            finally:
                self._materializing = ""
        elif not self._restored and not self.state.workspace_root_ready:
            raise BackendError("Unconfirmed manifest application cannot be replayed")

    async def _clear_workspace_root_on_resume(self) -> None:
        raise BackendError("Runtime snapshot recovery never clears a live workspace")

    def should_provision_manifest_accounts_on_resume(self) -> bool:
        return False

    def _should_compute_snapshot_fingerprint_on_persist(self) -> bool:
        # The Runtime's canonical archive SHA and non-overwriting restore are
        # authoritative; the SDK's optional local fingerprint is not required.
        return False

    async def require_cleanup_snapshot(self) -> None:
        if not self._closed and self._persisted_reference is None:
            self._cleanup_pending = True

    async def _persist_snapshot(self) -> None:
        async with self._persist_lock:
            if self._closed or self._persisted_reference is not None:
                return
            if self._persist_attempted:
                raise BackendError("Unconfirmed persistence requires explicit receipt recovery")
            self._persist_attempted = True
            self._cleanup_pending = True
            await super()._persist_snapshot()
            self._persisted_reference = self.state.snapshot.reference
            self._cleanup_pending = False

    def _prepare_exec_command(self, *command, shell=True, user=None):
        if user is not None:
            raise BackendError("Runtime commands cannot switch the fixed sandbox user")
        return super()._prepare_exec_command(*command, shell=shell, user=None)

    async def _exec_internal(self, *command: str | Path, timeout: float | None = None) -> ExecResult:
        self._check_binding()
        if timeout is not None and (isinstance(timeout, bool) or not isinstance(timeout, (int, float))
                                    or not math.isfinite(timeout) or not 0 < timeout <= 30):
            raise BackendError("Native command timeout exceeds the workspace policy")
        call = current_native_shell_call()
        if call is None:
            return await self._exec_workspace_helper(*command, timeout=timeout)
        async with self._io_lock:
            self._check_binding()
            if not self._started:
                raise BackendError("Native command requires a started workspace")
            if self._cleanup_pending or self._persisted_reference is not None or self.state.pending_file is not None or self.state.pending_pty:
                raise BackendError("Workspace persistence or an unconfirmed file operation forbids another command")
            return await self.transport.execute_command(
                call.agent_tool_call_id, call.sdk_tool_call_id, call.arguments(), [str(value) for value in command],
                timeout_ms=0 if timeout is None else max(1, math.ceil(timeout * 1000)),
            )

    @abc.abstractmethod
    async def _exec_workspace_helper(self, *command: str | Path, timeout: float | None = None) -> ExecResult:
        """Execute only the trusted, confined SDK filesystem helper protocol.

        Implementations must enforce the protocol at Runtime, not authorize
        arbitrary shell commands merely because no model tool call is active.
        """
        ...

    async def running(self) -> bool:
        if self._closed:
            return False
        self._check_binding()
        return await self.transport.probe() == "ready"

    async def persist_workspace(self) -> io.IOBase:
        async with self._io_lock:
            self._check_binding()
            if not self._started or self._persist_workspace_skip_relpaths() or self.state.pending_file is not None or self.state.pending_pty:
                raise BackendError("Workspace persistence requires a started session and complete archive scope")
            return io.BytesIO(await self.transport.export_workspace())

    async def hydrate_workspace(self, data: io.IOBase) -> None:
        self._check_binding()
        body = _read_binary_stream(data)
        snapshot = self.state.snapshot
        reference = snapshot.reference
        if snapshot.pending or reference is None or hashlib.sha256(body).hexdigest() != reference.sha256:
            raise BackendError("Workspace hydration requires the selected verified snapshot bytes")
        pending = self.state.pending_restore
        if pending is None:
            pending = PendingWorkspaceRestore(request_id=uuid4().hex, reference=reference)
            self.state.pending_restore = pending
        if pending.reference != reference:
            raise BackendError("Unconfirmed recovery cannot switch to another snapshot")
        await self.transport.restore_environment(pending.request_id, reference)
        self.state.pending_restore = None

    async def _shutdown_backend(self) -> None:
        async with self._shutdown_lock:
            if self._closed:
                return
            self._check_binding()
            if (self._cleanup_pending or self._start_failed or self.state.snapshot.pending is not None
                    or self.state.pending_restore is not None or self.state.pending_file is not None or self.state.pending_pty or self.state.active_pty
                    or self._shutdown_attempted):
                raise BackendError("Unconfirmed workspace persistence or startup forbids automatic destruction")
            self._shutdown_attempted = True
            await self.transport.shutdown()
            self._closed = True
            self._started = False


class RuntimeSandboxClient(BaseSandboxClient[RuntimeSandboxOptions]):
    backend_id = "content_agent_workspace"
    supports_default_options = True

    def __init__(self, transport: NativeWorkspaceHTTPTransport,
                 session_type: type[RuntimeSandboxSession], *, manifest: Manifest | None = None,
                 sources: NativeManifestSources | None = None, share_within_runner: bool = False,
                 expected_snapshot: SnapshotReference | None = None, live_state=None):
        if not issubclass(session_type, RuntimeSandboxSession) or inspect.isabstract(session_type):
            raise BackendError("Native client requires a complete confined filesystem provider")
        self.transport = transport
        if sources is not None and (type(sources) is not NativeManifestSources or manifest is not None):
            raise BackendError("Initial resource sources cannot be mixed with an arbitrary manifest")
        self._sources = sources.model_copy(deep=True) if sources is not None else None
        self._session_type = session_type
        self._manifest = (manifest or Manifest()).model_copy(deep=True)
        _validate_manifest(self._manifest)
        self._lock = asyncio.Lock()
        self._session: SandboxSession | None = None
        self._runtime_session: RuntimeSandboxSession | None = None
        self._preparation_started = False
        self._prepared_lease = None
        self._initial_manifest_hash = resource_manifest_hash(self._manifest)
        self._share_within_runner = share_within_runner
        self._expected_snapshot = expected_snapshot
        self._resume_payload: dict | None = None
        self._live_state = live_state

    def _shared_session(self) -> SandboxSession:
        if (not self._share_within_runner or self._session is None
                or self._runtime_session is None or self._runtime_session._closed):
            raise BackendError("Client already owns a native session")
        self._runtime_session._check_binding()
        return self._session

    async def _prepare_manifest_locked(self) -> None:
        if self._sources is None or self._prepared_lease is not None:
            return
        if self._preparation_started:
            raise BackendError("Unconfirmed resource preparation cannot be replayed")
        self._preparation_started = True
        if await self.transport.current() is not None:
            raise BackendError("This execution already has a workspace; explicit resume is required")
        lease = await self.transport.reserve()
        inventory = await self.transport.prepare_manifest(self._sources)
        self._manifest = inventory.manifest()
        _validate_manifest(self._manifest)
        self._initial_manifest_hash = inventory.manifest_hash
        self._prepared_lease = lease

    async def prepare_manifest(self) -> Manifest:
        """Freeze resources before Runner processes capability manifests.

        This acquires only this client's execution lease, not an OCI environment.
        Runner still owns actual session creation, startup and persistence.
        """
        async with self._lock:
            if self._session is not None:
                raise BackendError("An owned session cannot prepare a different initial manifest")
            await self._prepare_manifest_locked()
            return self._manifest.model_copy(deep=True)

    def _wrap(self, state: RuntimeSandboxState) -> SandboxSession:
        self._dependencies = Dependencies.with_values({
            SNAPSHOT_DEPENDENCY: RuntimeSnapshotBinding(str(state.session_id), self.transport),
        })
        inner = self._session_type(state, self.transport)
        if self._live_state is not None:
            binder = getattr(inner, "bind_live_state", None)
            if not callable(binder):
                raise BackendError("This workspace provider cannot rebind live terminal state")
            binder(self._live_state)
        self._runtime_session = inner
        self._session = self._wrap_session(inner)
        # The SDK manager continues shutdown/delete even when a later pre-stop
        # hook or stop() fails. Set the guard before any such cleanup hook runs.
        self._session.register_pre_stop_hook(inner.require_cleanup_snapshot)
        return self._session

    async def create(self, *, snapshot=None, manifest=None, options=None) -> SandboxSession:
        if options is not None and type(options) is not RuntimeSandboxOptions:
            raise BackendError("Runtime sandbox options cannot select another backend")
        if (snapshot is not None and type(snapshot) is not RuntimeWorkspaceSnapshotSpec
                or manifest is not None and manifest != self._manifest):
            raise BackendError("Create cannot import arbitrary snapshots or manifest authority; use verified resume")
        async with self._lock:
            if self._session is not None:
                if not self._share_within_runner:
                    raise BackendError("This execution already has a workspace; explicit resume is required")
                if self._resume_payload is not None:
                    raise BackendError("An already resumed workspace cannot become a fresh session")
                return self._shared_session()
            if self._sources is not None:
                await self._prepare_manifest_locked()
                lease = await self.transport.validate()
                if (lease.session_id != self._prepared_lease.session_id or lease.generation != self._prepared_lease.generation
                        or lease.snapshot_version != 0):
                    raise BackendError("Prepared resources no longer own a fresh execution lease")
            else:
                if await self.transport.current() is not None:
                    raise BackendError("This execution already has a workspace; explicit resume is required")
                lease = await self.transport.reserve()
            state = RuntimeSandboxState(session_id=UUID(lease.session_id),
                                        snapshot=RuntimeWorkspaceSnapshotSpec().build(lease.session_id),
                                        manifest=self._manifest.model_copy(deep=True), materialization_hash=self._initial_manifest_hash)
            return self._wrap(state)

    async def resume(self, state: SandboxSessionState) -> SandboxSession:
        if type(state) is not RuntimeSandboxState:
            raise BackendError("Workspace resume requires the registered Runtime session state")
        # Revalidate copies: model_copy/update and mutable SDK fields must not
        # turn a serialized UUID or manifest into a fresh authorization grant.
        state = self.deserialize_session_state(self.serialize_session_state(state))
        async with self._lock:
            if self._session is not None:
                if self._resume_payload != self.serialize_session_state(state):
                    raise BackendError("Handoff recovery differs from this Runner's original snapshot")
                return self._shared_session()
            current = await self.transport.current()
            if current is None or current.session_id != str(state.session_id):
                raise BackendError("Saved workspace is not the current execution workspace")
            if self.transport.lease is None:
                await self.transport.reserve(current.generation, expected_session_id=current.session_id)
            else:
                lease = await self.transport.validate()
                if lease.session_id != current.session_id:
                    raise BackendError("Workspace lease cannot move to another saved state")
            self._resume_payload = self.serialize_session_state(state)
            return self._wrap(state)

    async def close_owned_session(self) -> None:
        if self._session is None:
            return
        inner = self._runtime_session
        if inner._closed:
            return
        if inner._start_failed or inner.state.pending_file is not None or inner.state.pending_restore is not None or inner.state.pending_pty:
            raise BackendError("Unknown workspace operations cannot be replayed during Runner cleanup")
        await self._session.aclose()

    async def publication_snapshot(self) -> tuple[SnapshotReference, bytes]:
        """Checkpoint through the SDK snapshot without ending the live session."""
        inner = self._runtime_session
        if inner is None or self._session is None:
            raise BackendError("Publication requires a running SDK workspace")
        async with inner._persist_lock:
            inner._check_binding()
            if (not inner._started or inner._start_failed or inner._cleanup_pending or inner._persist_attempted
                    or inner.state.pending_restore or inner.state.snapshot.pending):
                raise BackendError("Unconfirmed workspace operations forbid publication preparation")
            # persist_workspace waits for active I/O, then checks pending
            # receipts under that same lock before exporting any bytes.
            stream = await self._session.persist_workspace()
            try:
                body = _read_binary_stream(stream)
            finally:
                stream.close()
            try:
                await inner.state.snapshot.persist(io.BytesIO(body), dependencies=inner.dependencies)
            except BaseException:
                # Cleanup must not retry a save whose result is unknown.
                inner._persist_attempted = True
                inner._cleanup_pending = True
                raise
            reference = inner.state.snapshot.reference
            if reference is None or inner.state.snapshot.pending is not None:
                raise BackendError("Publication snapshot has no confirmed receipt")
            return reference.model_copy(deep=True), body

    def confirmed_state(self) -> RuntimeSandboxState:
        self.require_confirmed_cleanup()
        return self._runtime_session.state.model_copy(deep=True)

    async def delete(self, session: SandboxSession) -> SandboxSession:
        if session is not self._session:
            raise BackendError("Client cannot delete an unowned session")
        await session.shutdown()
        return session

    def require_confirmed_cleanup(self) -> SnapshotReference:
        """Gate platform delivery: Runner may log and suppress cleanup failures."""
        inner = self._runtime_session
        if (inner is None or not inner._closed or inner._cleanup_pending or inner._start_failed
                or inner._persisted_reference is None or inner.state.snapshot.pending is not None
                or inner.state.pending_restore is not None or inner.state.pending_file is not None or inner.state.pending_pty or inner.state.active_pty
                or inner.state.snapshot.reference != inner._persisted_reference):
            raise BackendError("SDK output is not proof of a durably saved and closed workspace")
        return inner._persisted_reference

    def deserialize_session_state(self, payload: dict[str, object]) -> RuntimeSandboxState:
        failed = False
        try:
            state = RuntimeSandboxState.model_validate(payload)
            if state.manifest != self._manifest or state.path_grants_require_rebind:
                failed = True
            if self._expected_snapshot is not None and (state.snapshot.reference != self._expected_snapshot
                    or state.snapshot.pending is not None or state.pending_restore is not None or state.pending_file is not None or state.pending_pty or state.active_pty):
                failed = True
        except Exception:
            failed = True
        if failed:
            raise BackendError("Native workspace state differs from the current trusted manifest or identity contract")
        return state
