import asyncio
import hashlib
import io
import json
from pathlib import Path
import tarfile
from uuid import UUID, uuid4

import pytest
from agents import RunConfig, Runner
from agents.run_config import SandboxRunConfig
from agents.sandbox import SandboxAgent
from agents.sandbox.entries import File, LocalFile
from agents.sandbox.manifest import Manifest
from agents.sandbox.runtime_session_manager import _SandboxSessionResources
from agents.sandbox.session.sandbox_client import BaseSandboxClientOptions
from agents.sandbox.session.sandbox_session import SandboxSession
from agents.sandbox.session.sandbox_session_state import SandboxSessionState
from agents.sandbox.types import ExecResult

from content_agent_sidecar.backend import BackendError, backend_activity
from content_agent_sidecar.native_execution import native_shell_call_scope
from content_agent_sidecar.native_session import RuntimeSandboxClient, RuntimeSandboxOptions, RuntimeSandboxSession, RuntimeSandboxState
from content_agent_sidecar.native_snapshot import NativeSnapshotError, RuntimeWorkspaceSnapshotSpec, SnapshotReference
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport, NativeWorkspaceLease
from test_native_snapshot import MemorySnapshotTransport
from test_native_workspace_transport import KEY, OTHER, SESSION, archive_reply, lease, response, wire
from test_run_state_approval import SequenceModel, _text_response


class WorkspaceFixture(MemorySnapshotTransport):
    """In-memory provider IO, not a Go/OCI filesystem implementation."""

    def __init__(self):
        super().__init__()
        self.lease = None
        self.current_lease = None
        self.status = "absent"
        self.files = {}
        self.creates = 0
        self.closes = 0
        self.restores = []
        self.restore_receipts = {}
        self.commands = []
        self.helpers = []
        self.fail_restore = False
        self.fail_close = False
        self.authorized = True
        self.pty_processes = {}

    async def current(self):
        return self.current_lease

    async def reserve(self, expected_generation=0, *, expected_session_id=None):
        assert expected_generation == (self.current_lease.generation if self.current_lease else 0)
        assert expected_session_id is None or expected_session_id == SESSION
        self.lease = NativeWorkspaceLease.model_validate(lease(generation=expected_generation + 1))
        self.current_lease = self.lease
        return self.lease

    async def validate(self):
        if not self.authorized:
            raise BackendError("Execution no longer authorized")
        assert self.lease
        return self.lease

    async def probe(self):
        await self.validate()
        return self.status

    async def ensure(self):
        await self.validate()
        assert self.status == "absent" and not self.files
        self.creates += 1
        self.status = "ready"

    async def shutdown(self):
        await self.validate()
        if self.fail_close:
            raise BackendError("Shutdown outcome unconfirmed")
        self.closes += 1
        self.status = "closed"
        self.files.clear()

    async def export_workspace(self):
        await self.validate()
        assert self.status == "ready"
        stream = io.BytesIO()
        with tarfile.open(fileobj=stream, mode="w", format=tarfile.USTAR_FORMAT) as archive:
            for path, body in sorted(self.files.items()):
                info = tarfile.TarInfo(path)
                info.size = len(body)
                info.mode = 0o600
                archive.addfile(info, io.BytesIO(body))
        return stream.getvalue()

    async def restore_environment(self, request_id, reference):
        await self.validate()
        if request_id not in self.restore_receipts:
            body = self.versions[(SESSION, reference.version)]
            assert hashlib.sha256(body).hexdigest() == reference.sha256
            if self.status == "ready" and self.files and await self.export_workspace() != body:
                raise BackendError("Existing workspace differs; no files were replaced")
            with tarfile.open(fileobj=io.BytesIO(body), mode="r:") as archive:
                self.files = {entry.name: archive.extractfile(entry).read() for entry in archive if entry.isfile()}
            self.status = "ready"
            self.restore_receipts[request_id] = reference
            self.restores.append(request_id)
        assert self.restore_receipts[request_id] == reference
        if self.fail_restore:
            raise BackendError("Restore receipt unconfirmed")

    async def execute_command(self, *args, **kwargs):
        await self.validate()
        self.commands.append((args, kwargs))
        return ExecResult(stdout=b"native\x00\xff", stderr=b"", exit_code=7)

    async def pty_state(self):
        from content_agent_sidecar.native_live_state import NativePTYState

        generation = self.closes - (1 if self.status == "closed" else 0)
        return NativePTYState(session_id=SESSION, environment_id="" if self.status == "absent" else str(UUID(int=100 + generation)),
            state=self.status, processes=[self.pty_processes[key] for key in sorted(self.pty_processes)] if self.status == "ready" else [])

    async def pty_start(self, *args, **kwargs):
        from content_agent_sidecar.native_live_state import NativePTYProcessState
        from content_agent_sidecar.native_pty import NativePTYReceipt

        await self.validate()
        self.commands.append((args, kwargs))
        key = 1000 + len(self.pty_processes)
        process = NativePTYProcessState(pty_session_id=key, process_id=f"{key:032x}", sequence=1,
            tty=kwargs.get("tty", False), status="exited")
        self.pty_processes[key] = process
        return NativePTYReceipt(key, 1, process.process_id, b"native\x00\xff", 7, "exited", 0)

    async def terminate_pty(self):
        await self.validate()
        self.pty_processes = {key: item.model_copy(update={"status": "exited"}) for key, item in self.pty_processes.items()}


class FixtureSession(RuntimeSandboxSession):
    async def _exec_workspace_helper(self, *command, timeout=None):
        await self.transport.validate()
        self.transport.helpers.append(tuple(str(value) for value in command))
        return ExecResult(stdout=b"", stderr=b"", exit_code=0)

    async def read(self, path, *, user=None):
        self._check_binding()
        await self.transport.validate()
        key = self.normalize_path(path).as_posix().removeprefix("/workspace/")
        return io.BytesIO(self.transport.files[key])

    async def write(self, path, data, *, user=None):
        self._check_binding()
        await self.transport.validate()
        key = self.normalize_path(path, for_write=True).as_posix().removeprefix("/workspace/")
        self.transport.files[key] = data.read()


def test_sdk_client_state_lifecycle_binary_snapshot_and_rebuild():
    async def scenario():
        transport = WorkspaceFixture()
        client = RuntimeSandboxClient(transport, FixtureSession)
        session = await client.create(options=RuntimeSandboxOptions())
        assert type(session) is SandboxSession
        assert not await session.running() and transport.creates == 0
        await session.start()
        await session.start()
        assert transport.creates == 1
        await session.write(Path("binary.dat"), io.BytesIO(b"\x00\xff\x80"))
        await session.stop()
        serialized = client.serialize_session_state(session.state)
        assert serialized["snapshot"]["reference"]["version"] == 1
        assert "transport" not in json.dumps(serialized) and KEY not in json.dumps(serialized)
        state = SandboxSessionState.parse(serialized)
        assert type(state) is RuntimeSandboxState
        await client.delete(session)
        await client.delete(session)
        assert transport.closes == 1 and not await session.running()
        transport.lease = None
        second = RuntimeSandboxClient(transport, FixtureSession)
        restored = await second.resume(state)
        assert restored.dependencies is not session.dependencies
        await restored.start()
        assert (await restored.read(Path("binary.dat"))).read() == b"\x00\xff\x80"
        assert transport.creates == 1 and len(transport.restores) == 1
        assert not any("rm" in command for command in transport.helpers)
        await restored.aclose()
        await restored.aclose()
        assert transport.closes == 2 and restored.state.snapshot.reference.version == 2
    asyncio.run(scenario())


def test_native_session_preserves_sdk_command_preparation_and_original_approval():
    async def scenario():
        transport = WorkspaceFixture()
        session = await RuntimeSandboxClient(transport, FixtureSession).create()
        await session.start()
        with native_shell_call_scope("call-1", "sdk-1", '{"cmd":"printf native"}'):
            result = await session.exec("printf native", timeout=0.25)
            assert result.exit_code == 7 and result.stdout == b"native\x00\xff"
            for timeout in (-1, 0, 31, float("nan"), float("inf"), True):
                with pytest.raises(BackendError):
                    await session.exec("no", timeout=timeout)
            with pytest.raises(BackendError, match="sandbox user"):
                await session.exec("no", user="root")
        args, options = transport.commands[0]
        assert args == ("call-1", "sdk-1", {"cmd": "printf native"}, ["sh", "-lc", "printf native"])
        assert options == {"timeout_ms": 250}
        assert len(transport.commands) == 1
        transport.authorized = False
        with pytest.raises(BackendError):
            await session.running()
        assert transport.creates == 1
    asyncio.run(scenario())


@pytest.mark.parametrize("changed", ["unsaved-files", "missing-environment", "unconfirmed-root"])
def test_native_session_resume_never_clears_or_invents_a_workspace(changed):
    async def scenario():
        transport = WorkspaceFixture()
        client = RuntimeSandboxClient(transport, FixtureSession)
        session = await client.create()
        await session.start()
        if changed == "unsaved-files":
            await session.write(Path("draft"), io.BytesIO(b"saved"))
            await session.stop()
            transport.files["draft"] = b"unsaved"
        elif changed == "missing-environment":
            transport.status = "closed"
        else:
            session.state.workspace_root_ready = False
        state = client.deserialize_session_state(client.serialize_session_state(session.state))
        other = RuntimeSandboxClient(transport, FixtureSession)
        restored = await other.resume(state)
        with pytest.raises(BackendError):
            await restored.start()
        with pytest.raises(BackendError, match="explicit state recovery"):
            await restored.start()
        assert transport.creates == 1 and not transport.closes
        if changed == "unsaved-files":
            assert transport.files["draft"] == b"unsaved"
        assert not any("rm" in command for command in transport.helpers)
    asyncio.run(scenario())


def test_native_session_lost_restore_receipt_survives_sdk_state_roundtrip():
    async def scenario():
        transport = WorkspaceFixture()
        client = RuntimeSandboxClient(transport, FixtureSession)
        session = await client.create()
        await session.start()
        await session.write(Path("draft"), io.BytesIO(b"kept"))
        await session.stop()
        state = client.deserialize_session_state(client.serialize_session_state(session.state))
        await client.delete(session)
        transport.fail_restore = True
        second = RuntimeSandboxClient(transport, FixtureSession)
        restarted = await second.resume(state)
        with pytest.raises(BackendError, match="unconfirmed"):
            await restarted.start()
        raw = second.serialize_session_state(restarted.state)
        request_id = raw["pending_restore"]["request_id"]
        transport.fail_restore = False
        third = RuntimeSandboxClient(transport, FixtureSession)
        retried = await third.resume(third.deserialize_session_state(raw))
        await retried.start()
        assert transport.restores == [request_id] and retried.state.pending_restore is None
        assert (await retried.read(Path("draft"))).read() == b"kept"
    asyncio.run(scenario())


def test_native_session_lost_snapshot_save_does_not_shutdown_or_select_latest():
    async def scenario():
        transport = WorkspaceFixture()
        client = RuntimeSandboxClient(transport, FixtureSession)
        session = await client.create()
        await session.start()
        await session.write(Path("draft"), io.BytesIO(b"pending"))
        transport.fail_save = "after"
        with pytest.raises(NativeSnapshotError):
            await session.aclose()
        assert not transport.closes and session.state.snapshot.pending is not None
        raw = client.serialize_session_state(session.state)
        second = RuntimeSandboxClient(transport, FixtureSession)
        restored = await second.resume(second.deserialize_session_state(raw))
        await restored.start()
        assert restored.state.snapshot.pending is None and len(transport.saves) == 1
        assert (await restored.read(Path("draft"))).read() == b"pending"
    asyncio.run(scenario())


@pytest.mark.parametrize("failure", ["save", "export", "pre-stop-hook"])
def test_native_sdk_runtime_manager_does_not_destroy_workspace_after_failed_persistence(failure):
    async def scenario():
        transport = WorkspaceFixture()
        client = RuntimeSandboxClient(transport, FixtureSession)
        session = await client.create()
        resources = _SandboxSessionResources(session=session, client=client, owns_session=True)
        await resources.ensure_started()
        await session.write(Path("draft"), io.BytesIO(b"must remain recoverable"))
        if failure == "save":
            transport.fail_save = "after"
        else:
            async def fail():
                raise BackendError("Fixture cleanup failure")
            if failure == "export":
                transport.export_workspace = fail
            else:
                session.register_pre_stop_hook(fail)
        with pytest.raises((BackendError, NativeSnapshotError)):
            await resources.cleanup()
        assert transport.closes == 0 and transport.files["draft"] == b"must remain recoverable"
        assert await session.running()
        await resources.cleanup()
        assert transport.closes == 0
    asyncio.run(scenario())


def test_native_client_refuses_incomplete_provider_foreign_state_and_second_ownership():
    async def scenario():
        transport = WorkspaceFixture()
        with pytest.raises(BackendError, match="complete confined filesystem"):
            RuntimeSandboxClient(transport, RuntimeSandboxSession)
        client = RuntimeSandboxClient(transport, FixtureSession)
        session = await client.create()
        with pytest.raises(BackendError, match="already has"):
            await client.create()
        with pytest.raises(BackendError, match="unowned"):
            await RuntimeSandboxClient(transport, FixtureSession).delete(session)
        state = client.serialize_session_state(session.state)
        for update in ({"holder_key": KEY}, {"exposed_ports": [8860]}, {"session_id": OTHER}, {"type": "local"}):
            with pytest.raises(BackendError):
                client.deserialize_session_state({**state, **update})
        other_id = str(uuid4())
        foreign = {**state, "session_id": other_id, "snapshot": {**state["snapshot"], "id": other_id}}
        new_client = RuntimeSandboxClient(transport, FixtureSession)
        with pytest.raises(BackendError, match="current execution"):
            await new_client.resume(new_client.deserialize_session_state(foreign))
        assert not transport.creates and not transport.closes
    asyncio.run(scenario())


def test_native_manifest_authority_and_sdk_options_roundtrip():
    options = BaseSandboxClientOptions.parse(json.loads(RuntimeSandboxOptions().model_dump_json()))
    assert type(options) is RuntimeSandboxOptions
    for manifest in (Manifest(root="/tmp"), Manifest(environment={"value": {"HOME": "/tmp"}}),
                     Manifest(entries={"secret": LocalFile(src=Path("private"))}),
                     Manifest(entries={"binary": File(content=b"\xff")})):
        with pytest.raises(BackendError):
            RuntimeSandboxClient(WorkspaceFixture(), FixtureSession, manifest=manifest)


def test_native_manifest_uses_sdk_materialization_and_rejects_changed_saved_authority():
    async def scenario():
        transport = WorkspaceFixture()
        manifest = Manifest(entries={"SKILL.md": File(content=b"trusted fixture skill")})
        client = RuntimeSandboxClient(transport, FixtureSession, manifest=manifest)
        session = await client.create()
        manifest.entries.clear()
        await session.start()
        assert transport.files["SKILL.md"] == b"trusted fixture skill"
        raw = client.serialize_session_state(session.state)
        state = client.deserialize_session_state(raw)
        assert state.manifest.entries["SKILL.md"].content == b"trusted fixture skill"
        raw["manifest"]["entries"]["SKILL.md"]["content"] = "replaced"
        with pytest.raises(BackendError):
            client.deserialize_session_state(raw)
        session.state.manifest.root = "/tmp"
        with pytest.raises(BackendError, match="trusted execution"):
            await session.running()
        assert not transport.closes
    asyncio.run(scenario())


def test_native_shutdown_failure_does_not_mark_session_closed():
    async def scenario():
        transport = WorkspaceFixture()
        client = RuntimeSandboxClient(transport, FixtureSession)
        session = await client.create()
        await session.start()
        transport.fail_close = True
        with pytest.raises(BackendError, match="unconfirmed"):
            await client.delete(session)
        assert await session.running() and not transport.closes
    asyncio.run(scenario())


@pytest.mark.parametrize("status", ["ready", "absent", "closed"])
def test_native_lifecycle_real_http_does_not_use_ensure_for_probe(status):
    with wire(response(lease()), response({"session_id": SESSION, "state": status}),
              response({"session_id": SESSION, "state": "closed"})) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                assert await transport.probe() == status
                await transport.shutdown()
        asyncio.run(scenario())
    assert requests[1][1].endswith("/probe") and requests[2][1].endswith("/shutdown")
    assert all("ensure" not in item[1] for item in requests)


@pytest.mark.parametrize("data", [{"session_id": OTHER, "state": "ready"},
                                 {"session_id": SESSION, "state": []},
                                 {"session_id": SESSION, "state": "creating"}])
def test_native_liveness_malformed_receipt_is_not_false_or_recreation(data):
    with wire(response(lease()), response(data)) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                with pytest.raises(BackendError, match="unconfirmed"):
                    await transport.probe()
        asyncio.run(scenario())
    assert len(requests) == 2


class LifecycleWireSession(RuntimeSandboxSession):
    async def read(self, *args, **kwargs):
        raise AssertionError("Lifecycle HTTP fixture does not provide file IO")

    async def write(self, *args, **kwargs):
        raise AssertionError("Lifecycle HTTP fixture does not provide file IO")

    async def _exec_workspace_helper(self, *command, timeout=None):
        assert all("rm" != str(value) for value in command)
        return ExecResult(stdout=b"", stderr=b"", exit_code=0)


def test_actual_sdk_client_snapshot_lifecycle_through_real_loopback_http():
    body = bytes(1024)
    digest = hashlib.sha256(body).hexdigest()
    reference = {"session_id": SESSION, "version": 1, "sha256": digest}
    ready = {"session_id": SESSION, "state": "ready"}
    with wire(response({"lease": None}), response(lease()), response(lease()),
              response({"session_id": SESSION, "state": "absent"}), response(ready),
              archive_reply(body, version=0), response(reference),
              response({"session_id": SESSION, "state": "closed"}),
              response({"lease": lease(snapshot_version=1)}), response(lease(generation=2, snapshot_version=1)),
              response(lease(generation=2, snapshot_version=1)), archive_reply(body), response(ready)) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                client = RuntimeSandboxClient(NativeWorkspaceHTTPTransport(backend, KEY, 1), LifecycleWireSession)
                session = await client.create()
                await session.start()
                await session.stop()
                raw = client.serialize_session_state(session.state)
                await client.delete(session)
                restored_client = RuntimeSandboxClient(NativeWorkspaceHTTPTransport(backend, "b" * 64, 1), LifecycleWireSession)
                restored = await restored_client.resume(restored_client.deserialize_session_state(raw))
                await restored.start()
                assert restored.state.snapshot.reference == SnapshotReference(version=1, sha256=digest)
                assert restored.state.workspace_root_ready
        asyncio.run(scenario())
    assert len(requests) == 13
    restore = json.loads(requests[-1][3])
    assert restore["version"] == 1 and restore["sha256"] == digest
    assert requests[-1][2]["X-Workspace-Lease-Generation"] == "2"
    assert requests[-1][2]["X-Workspace-Lease-Key"] == "b" * 64


@pytest.mark.parametrize("lose_save", [False, True])
def test_actual_sandbox_agent_runner_owns_runtime_client_and_snapshot_lifecycle(lose_save):
    async def scenario():
        transport = WorkspaceFixture()
        manifest = Manifest(entries={"draft.txt": File(content=b"runner-owned workspace")})
        client = RuntimeSandboxClient(transport, FixtureSession, manifest=manifest)
        model = SequenceModel([_text_response()])
        agent = SandboxAgent(name="Native lifecycle fixture", model=model, capabilities=[], default_manifest=manifest)
        config = RunConfig(tracing_disabled=True, sandbox=SandboxRunConfig(
            client=client, options=RuntimeSandboxOptions(), snapshot=RuntimeWorkspaceSnapshotSpec(), manifest=manifest,
        ))
        if lose_save:
            transport.fail_save = "after"
            result = await Runner.run(agent, "Complete the fixture", run_config=config)
            assert result.final_output
            with pytest.raises(BackendError, match="not proof"):
                client.require_confirmed_cleanup()
            assert not transport.closes and transport.files["draft.txt"] == b"runner-owned workspace"
        else:
            result = await Runner.run(agent, "Complete the fixture", run_config=config)
            assert result.final_output and not model.responses
            assert transport.closes == 1 and len(transport.saves) == 1
            assert client.require_confirmed_cleanup().version == 1
        assert transport.creates == 1
    asyncio.run(scenario())
