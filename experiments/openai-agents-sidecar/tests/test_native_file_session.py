import asyncio
import base64
import hashlib
import io
import json
from pathlib import Path

import pytest
from agents import Agent, Runner, RunState
from agents.sandbox.errors import InvalidManifestPathError
from agents.tool_context import ToolContext

from content_agent_sidecar.agent_tools import AgentToolConfigurationError
from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.native_execution import current_native_patch_call, native_patch_call_scope
from content_agent_sidecar.native_execution import native_shell_call_scope
from content_agent_sidecar.native_execution import NativePatchCall
from content_agent_sidecar.native_file_session import RuntimeSandboxFileSession
from content_agent_sidecar.native_files import MAX_FILE_BYTES, NativeFileOperation, NativeFileResult
from content_agent_sidecar.native_session import RuntimeSandboxClient, RuntimeSandboxState
from content_agent_sidecar.native_snapshot import SnapshotReference
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport
from test_native_patch_tool import CONFIG, PATCH, PatchBackend, prepared_patch, response
from test_native_session import WorkspaceFixture
from test_native_workspace_transport import KEY, SESSION, archive_reply, response as http_response, wire
from content_agent_sidecar.backend import backend_activity
from test_run_state_approval import SequenceModel, _text_response
from test_agent_tools import _context
from test_workspace_transfer_helper import helper


class DiskWorkspaceFixture(WorkspaceFixture):
    """Real temporary file IO with the trusted helper, not Go/OCI authorization."""

    def __init__(self, root):
        super().__init__()
        self.base = root
        self.root = root / "generation-0"
        self.root.mkdir()
        self.file_calls = []
        self.snapshot_entries = {}
        self.file_failure = None

    async def file_operation(self, request_id, operation, call=None):
        await self.validate()
        assert self.status == "ready"
        assert call is not None or not operation.mutates
        self.file_calls.append((request_id, operation, call))
        try:
            result = helper.file_operation(self.root, operation.payload(), MAX_FILE_BYTES)
        except helper.TransferError as error:
            raise BackendError("File preflight rejected", code=error.code, status_code=409) from None
        if operation.operation == "write" and self.file_failure:
            if self.file_failure == "cancel":
                raise asyncio.CancelledError()
            raise BackendError("File receipt was lost", code="NATIVE_WORKSPACE_TRANSPORT_UNCONFIRMED")
        return NativeFileResult.from_wire(operation, result)

    async def export_workspace(self):
        await self.validate()
        listing = helper.entries(self.root, MAX_FILE_BYTES)
        archive = io.BytesIO()
        helper.export_archive(self.root, listing, archive)
        body = archive.getvalue()
        self.snapshot_entries[hashlib.sha256(body).hexdigest()] = listing
        return body

    async def restore_environment(self, request_id, reference):
        await self.validate()
        body = self.versions[(SESSION, reference.version)]
        assert hashlib.sha256(body).hexdigest() == reference.sha256
        if self.status == "closed":
            self.root = self.base / f"generation-{self.closes}"
            self.root.mkdir()
            self.pty_processes = {}
        helper.hydrate_archive(self.root, body, self.snapshot_entries[reference.sha256], MAX_FILE_BYTES)
        self.status = "ready"
        self.restores.append(request_id)


async def create_disk_session(tmp_path):
    transport = DiskWorkspaceFixture(tmp_path)
    client = RuntimeSandboxClient(transport, RuntimeSandboxFileSession)
    session = await client.create()
    await session.start()
    return transport, client, session


def test_concrete_sdk_file_session_binary_operations_snapshot_and_rebuild(tmp_path):
    async def scenario():
        transport, client, session = await create_disk_session(tmp_path)
        body = b"\x00\xff\x80binary"
        with native_patch_call_scope("approved-patch", "sdk-call", "a" * 64):
            await session.mkdir(Path("nested/deep"), parents=True)
            await session.write(Path("nested/deep/file.bin"), io.BytesIO(body))
            await session.write(Path("empty"), io.BytesIO())
        with await session.read(Path("nested/deep/file.bin")) as stream:
            assert stream.read() == body
        entries = await session.ls(Path("nested"))
        assert len(entries) == 1 and entries[0].is_dir() and entries[0].path == "/workspace/nested/deep"
        await session.aclose()
        reference = client.require_confirmed_cleanup()
        state = client.serialize_session_state(session.state)
        assert state["pending_file"] is None and state["snapshot"]["reference"]["version"] == reference.version
        transport.lease = None
        second = RuntimeSandboxClient(transport, RuntimeSandboxFileSession)
        restored = await second.resume(second.deserialize_session_state(state))
        await restored.start()
        assert (await restored.read(Path("nested/deep/file.bin"))).read() == body
        with native_patch_call_scope("remove-patch", "sdk-remove", "b" * 64):
            await restored.rm(Path("nested"), recursive=True)
            await restored.rm(Path("empty"))
        assert await restored.ls(Path(".")) == []
        await restored.aclose()
        assert second.require_confirmed_cleanup().version == reference.version + 1
        writes = [(operation, call) for _, operation, call in transport.file_calls if operation.mutates]
        assert len(writes) == 5 and all(call is not None for _, call in writes)
        assert not transport.commands and not transport.helpers
    asyncio.run(scenario())


@pytest.mark.parametrize("decision", ["approve", "reject"])
def test_actual_sdk_runner_approval_edits_real_file_through_concrete_session(tmp_path, decision):
    async def scenario():
        transport, client, session = await create_disk_session(tmp_path)
        backend, context = PatchBackend(), _context()
        tool = await prepared_patch(backend, session, context)
        pending = await Runner.run(Agent(name="Native files", model=SequenceModel([response()]), tools=[tool]),
                                   "Write the file", context=context, run_config=CONFIG)
        assert len(pending.interruptions) == 1 and not (transport.root / "note.txt").exists()
        run_state = pending.to_state().to_json(context_serializer=lambda value: {"project_id": value.project_id})
        await session.aclose()
        client.require_confirmed_cleanup()
        saved_session = client.serialize_session_state(session.state)
        transport.lease = None
        client = RuntimeSandboxClient(transport, RuntimeSandboxFileSession)
        session = await client.resume(client.deserialize_session_state(saved_session))
        await session.start()
        context = _context()
        tool = await prepared_patch(backend, session, context)
        agent = Agent(name="Native files", model=SequenceModel([_text_response()]), tools=[tool])
        state = await RunState.from_json(agent, run_state, context_override=context)
        backend.calls["patch-call"]["status"] = "approved" if decision == "approve" else "rejected"
        item = state.get_interruptions()[0]
        state.approve(item) if decision == "approve" else state.reject(item)
        result = await Runner.run(agent, state, context=context, run_config=CONFIG)
        assert result.final_output and not result.interruptions
        assert (transport.root / "note.txt").exists() == (decision == "approve")
        if decision == "approve":
            assert (transport.root / "note.txt").read_bytes() == b"native patch"
            assert len(backend.completed) == 1
            write = next(call for _, operation, call in transport.file_calls if operation.operation == "write")
            assert write.sdk_tool_call_id == "patch-call" and write.arguments_hash == backend.calls["patch-call"]["arguments_hash"]
        assert current_native_patch_call() is None
        await session.aclose()
        client.require_confirmed_cleanup()
    asyncio.run(scenario())


def test_sdk_multifile_editor_create_move_delete_and_update_uses_real_files(tmp_path):
    async def scenario():
        transport, client, session = await create_disk_session(tmp_path)
        backend, context = PatchBackend(), _context()
        tool = await prepared_patch(backend, session, context)
        patches = [
            "*** Begin Patch\n*** Add File: skills/draft/SKILL.md\n+---\n+name: draft\n+description: fixture\n+---\n+Instructions\n*** Add File: old.txt\n+before\n*** End Patch\n",
            "*** Begin Patch\n*** Update File: old.txt\n*** Move to: moved.txt\n@@\n-before\n+after\n*** Add File: discard.txt\n+temporary\n*** End Patch\n",
            "*** Begin Patch\n*** Delete File: discard.txt\n*** End Patch\n",
        ]
        for index, patch in enumerate(patches):
            sdk_id = f"patch-{index}"
            tc = ToolContext(context, tool_name="apply_patch", tool_call_id=sdk_id, tool_arguments=patch)
            await tool.needs_approval(tc, patch, sdk_id)
            context.active_tool_calls[sdk_id]["status"] = "approved"
            backend.calls[sdk_id]["status"] = "approved"
            await tool.on_invoke_tool(tc, patch)
        assert (transport.root / "skills/draft/SKILL.md").read_bytes().startswith(b"---\nname: draft")
        assert (transport.root / "moved.txt").read_bytes() == b"after"
        assert not (transport.root / "old.txt").exists() and not (transport.root / "discard.txt").exists()
        assert len(backend.completed) == 3 and not backend.failed
        await session.aclose()
        client.require_confirmed_cleanup()
    asyncio.run(scenario())


@pytest.mark.parametrize("failure", ["lost", "cancel"])
def test_unknown_file_outcome_blocks_further_io_snapshot_cleanup_and_rebuild(tmp_path, failure):
    async def scenario():
        transport, client, session = await create_disk_session(tmp_path)
        transport.file_failure = failure
        backend, context = PatchBackend(), _context()
        tool = await prepared_patch(backend, session, context)
        tc = ToolContext(context, tool_name="apply_patch", tool_call_id="patch-call", tool_arguments=PATCH)
        await tool.needs_approval(tc, PATCH, "patch-call")
        context.active_tool_calls["patch-call"]["status"] = "approved"
        backend.calls["patch-call"]["status"] = "approved"
        with pytest.raises(asyncio.CancelledError if failure == "cancel" else AgentToolConfigurationError):
            await tool.on_invoke_tool(tc, PATCH)
        assert (transport.root / "note.txt").read_bytes() == b"native patch"
        assert session.state.pending_file is not None and not backend.completed and len(backend.failed) == 1
        assert current_native_patch_call() is None
        calls = len(transport.file_calls)
        with pytest.raises(BackendError):
            await session.read(Path("note.txt"))
        with pytest.raises(BackendError):
            await session.aclose()
        with pytest.raises(BackendError):
            await client.delete(session)
        with pytest.raises(BackendError):
            client.require_confirmed_cleanup()
        encoded = client.serialize_session_state(session.state)
        assert "native patch" not in json.dumps(encoded) and "approved" not in json.dumps(encoded)
        assert RuntimeSandboxState.model_validate(encoded).pending_file == session.state.pending_file
        transport.lease = None
        second = RuntimeSandboxClient(transport, RuntimeSandboxFileSession)
        restored = await second.resume(second.deserialize_session_state(encoded))
        with pytest.raises(BackendError):
            await restored.start()
        assert len(transport.file_calls) == calls and transport.closes == 0
    asyncio.run(scenario())


def test_file_session_missing_file_is_known_but_unapproved_writes_never_reach_provider(tmp_path):
    async def scenario():
        transport, client, session = await create_disk_session(tmp_path)
        with pytest.raises(FileNotFoundError):
            await session.read(Path("missing"))
        assert session.state.pending_file is None
        before = len(transport.file_calls)
        for path in (Path("file"), Path("../escape"), Path("/outside")):
            with pytest.raises((BackendError, InvalidManifestPathError)):
                await session.write(path, io.BytesIO(b"no"))
        with pytest.raises(BackendError):
            await session.read(Path("file"), user="root")
        with pytest.raises(BackendError):
            await session.exec("python", "arbitrary.py", shell=False)
        with pytest.raises(BackendError):
            await session.exec("chmod", "0600", "/workspace/file", shell=False)
        assert len(transport.file_calls) == before and not transport.commands
        await session.aclose()
        client.require_confirmed_cleanup()
    asyncio.run(scenario())


@pytest.mark.parametrize("saved", [False, True])
def test_persistence_freezes_file_and_shell_access_until_resumed(tmp_path, saved):
    async def scenario():
        transport, client, session = await create_disk_session(tmp_path)
        with native_patch_call_scope("patch", "sdk", "a" * 64):
            await session.write(Path("file"), io.BytesIO(b"saved"))
        if saved:
            await session.stop()
        else:
            await client._runtime_session.require_cleanup_snapshot()
        before = len(transport.file_calls)
        with native_patch_call_scope("patch", "sdk", "a" * 64):
            with pytest.raises(BackendError, match="persistence"):
                await session.write(Path("file"), io.BytesIO(b"late write"))
        with native_shell_call_scope("shell", "sdk-shell", '{"cmd":"touch late"}'):
            with pytest.raises(BackendError, match="persistence"):
                await session.exec("touch late")
        assert (transport.root / "file").read_bytes() == b"saved"
        assert len(transport.file_calls) == before and not transport.commands
        await session.aclose()
        client.require_confirmed_cleanup()
    asyncio.run(scenario())


def test_concrete_sdk_file_session_and_snapshots_cross_real_http_then_rebuild(tmp_path):
    disk = DiskWorkspaceFixture(tmp_path)
    expected_holder = [KEY]

    def handle(record):
        method, path, headers, body = record
        assert headers["X-Agent-Project-ID"] == "project-1"
        assert headers["X-Agent-Turn-ID"] == "turn-1"
        assert headers["X-Workspace-Lease-Key"] == expected_holder[0]

        async def reply():
            if path.endswith("/current"):
                current = await disk.current()
                return http_response({"lease": current.model_dump(mode="json") if current else None})
            if path.endswith("/reservations"):
                data = json.loads(body)
                result = await disk.reserve(data["expected_generation"], expected_session_id=SESSION if disk.current_lease else None)
                return http_response(result.model_dump(mode="json"))
            if path.endswith("/lease"):
                return http_response((await disk.validate()).model_dump(mode="json"))
            if path.endswith("/probe"):
                return http_response({"session_id": SESSION, "state": await disk.probe()})
            if path.endswith("/ensure"):
                await disk.ensure()
                return http_response({"session_id": SESSION, "state": "ready"})
            if path.endswith("/shutdown"):
                await disk.shutdown()
                return http_response({"session_id": SESSION, "state": "closed"})
            if path.endswith("/files"):
                data = json.loads(body)
                values = data["file"]
                operation = NativeFileOperation(**{**values, "data": base64.b64decode(values.get("data", ""))})
                call = NativePatchCall(data["agent_tool_call_id"], data["sdk_tool_call_id"], data["arguments_hash"]) if data["agent_tool_call_id"] else None
                result = await disk.file_operation(data["request_id"], operation, call)
                from dataclasses import asdict
                value = asdict(result)
                value["entries"] = list(value["entries"])
                value["data"] = base64.b64encode(value["data"]).decode()
                return http_response({"session_id": SESSION, "request_id": data["request_id"], "request_hash": operation.request_hash, "file": value})
            if path.endswith("/export"):
                return archive_reply(await disk.export_workspace(), version=0)
            if "/snapshots/" in path and method == "PUT":
                reference = await disk.save_snapshot(SESSION, path.rsplit("/", 1)[1], int(headers["X-Workspace-Snapshot-Parent"]), body)
                return http_response({"session_id": SESSION, **reference.model_dump()})
            if "/snapshots/" in path and method == "GET":
                from urllib.parse import parse_qs, urlsplit
                url = urlsplit(path)
                reference = SnapshotReference(version=int(url.path.rsplit("/", 1)[1]), sha256=parse_qs(url.query)["sha256"][0])
                return archive_reply(await disk.read_snapshot(SESSION, reference), version=reference.version)
            if path.endswith("/restore"):
                data = json.loads(body)
                await disk.restore_environment(data["request_id"], SnapshotReference(version=data["version"], sha256=data["sha256"]))
                return http_response({"session_id": SESSION, "state": "ready"})
            raise AssertionError(f"Unexpected native HTTP method/path: {method} {path}")

        return asyncio.run(reply())

    # The server is an isolated contract fixture. This proves SDK + transport +
    # actual file bytes, not Go authorization, database behavior or real OCI.
    with wire(*([handle] * 64)) as (backend, requests):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                client = RuntimeSandboxClient(transport, RuntimeSandboxFileSession)
                session = await client.create()
                await session.start()
                with native_patch_call_scope("approved", "sdk-call", "a" * 64):
                    await session.mkdir("nested", parents=True)
                    await session.write(Path("nested/file.bin"), io.BytesIO(b"\x00\xff\x80"))
                await session.aclose()
                first = client.require_confirmed_cleanup()
                saved = client.serialize_session_state(session.state)
                expected_holder[0] = "b" * 64
                replacement = NativeWorkspaceHTTPTransport(backend, expected_holder[0], 1)
                second = RuntimeSandboxClient(replacement, RuntimeSandboxFileSession)
                restored = await second.resume(second.deserialize_session_state(saved))
                await restored.start()
                with await restored.read(Path("nested/file.bin")) as stream:
                    assert stream.read() == b"\x00\xff\x80"
                await restored.aclose()
                assert second.require_confirmed_cleanup().version == first.version + 1
        asyncio.run(scenario())
        file_requests = [record for record in requests if record[1].endswith("/files")]
        assert len(file_requests) == 4
        assert len([operation for _, operation, _ in disk.file_calls if operation.operation == "write"]) == 1
