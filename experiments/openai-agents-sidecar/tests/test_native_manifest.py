import asyncio
import base64
from dataclasses import asdict
import hashlib
import io
import json
from pathlib import PurePosixPath

import pytest
from agents.sandbox.manifest import Manifest

from content_agent_sidecar.backend import BackendError, backend_activity
from content_agent_sidecar.native_execution import NativePatchCall, native_patch_call_scope
from content_agent_sidecar.native_file_session import RuntimeSandboxFileSession
from content_agent_sidecar.native_files import MAX_FILE_BYTES, NativeFileOperation, NativeFileResult, _go_json_hash
from content_agent_sidecar.native_manifest import (
    NativeManifestFile, NativeManifestInventory, NativeManifestSources, NativeProjectSource,
    NativeSkillSource, RuntimeResourceFile,
)
from content_agent_sidecar.native_session import RuntimeSandboxClient
from content_agent_sidecar.native_snapshot import SnapshotReference
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport
from test_native_file_session import DiskWorkspaceFixture
from test_native_workspace_transport import KEY, OTHER, SESSION, archive_reply, lease, response, wire
from test_workspace_transfer_helper import helper


SKILL = NativeSkillSource(capability_id="skill:fixture", version="1.0.0", content_hash="sha256:" + "b"*64)
BODIES = {
    ".skills/fixture/SKILL.md": b"---\nname: fixture\ndescription: Inspect a supplied outline\n---\nRead references/guide.txt and assets/sample.bin.\n",
    ".skills/fixture/references/guide.txt": b"Keep evidence separate from inference.",
    ".skills/fixture/assets/sample.bin": b"\x00\xff\x80" * 30000,
    "docs/notes.txt": b"selected historical version",
    "empty.txt": b"",
}


def inventory():
    files = []
    for path, body in sorted(BODIES.items()):
        digest = hashlib.sha256(body).hexdigest()
        source = {"skill": SKILL, "resource_path": "/".join(path.split("/")[2:])} if path.startswith(".skills/") else {
            "project_file": NativeProjectSource(path=path, version=2, content_hash=digest),
        }
        files.append(NativeManifestFile(path=path, sha256=digest, size_bytes=len(body), **source))
    return NativeManifestInventory(session_id=SESSION, manifest_hash=_go_json_hash([file.model_dump(exclude_none=True) for file in files]), files=files)


def sources():
    return NativeManifestSources(skills=[SKILL], files=[file.project_file for file in inventory().files if file.project_file])


class ManifestFixture(DiskWorkspaceFixture):
    """SDK and real temp-file fixture; the Go Store is tested separately."""

    def __init__(self, root):
        super().__init__(root)
        self.bodies = dict(BODIES)
        self.inventory = inventory()
        self.prepares = 0
        self.materialized = set()
        self.required = set()
        self.manifest_status = "absent"
        self.seal_failure = False
        for file in self.inventory.files:
            self.required.update({("write", file.path), ("chmod", file.path)})
            for parent in PurePosixPath(file.path).parents:
                if str(parent) != ".":
                    self.required.update({("mkdir", str(parent)), ("chmod", str(parent))})

    async def prepare_manifest(self, selected):
        await self.validate()
        assert self.status == "absent" and selected == sources()
        self.prepares += 1
        self.manifest_status = "prepared"
        return self.inventory

    async def read_manifest_file(self, manifest_hash, file):
        await self.validate()
        assert self.manifest_status == "prepared" and manifest_hash == self.inventory.manifest_hash
        assert file in self.inventory.files
        return self.bodies[file.path]

    async def file_operation(self, request_id, operation, call=None, *, materialization_hash=""):
        if not materialization_hash:
            if operation.mutates:
                assert self.manifest_status == "completed"
            return await super().file_operation(request_id, operation, call)
        await self.validate()
        assert not call and self.manifest_status == "prepared" and materialization_hash == self.inventory.manifest_hash
        key = (operation.operation, operation.path)
        assert key in self.required and key not in self.materialized
        if operation.operation == "write":
            assert operation.data == self.bodies[operation.path]
        elif operation.operation == "chmod":
            assert operation.mode == (0o644 if operation.path in self.bodies else 0o755)
        else:
            assert operation.parents and not operation.recursive
        self.file_calls.append((request_id, operation, None))
        if operation.operation == "chmod":
            # Windows cannot implement POSIX mode bits. Only this metadata
            # receipt is a fixture; all file bytes and archives use real IO.
            value = helper.file_operation(self.root, {"operation": "stat", "path": operation.path}, MAX_FILE_BYTES)
            value["operation"] = "chmod"
            value["entries"][0]["mode"] = operation.mode
        else:
            value = helper.file_operation(self.root, operation.payload(), MAX_FILE_BYTES)
        result = NativeFileResult.from_wire(operation, value)
        self.materialized.add(key)
        if operation.operation == "write" and self.file_failure:
            if self.file_failure == "cancel":
                raise asyncio.CancelledError()
            raise BackendError("Materialization receipt lost", code="NATIVE_WORKSPACE_TRANSPORT_UNCONFIRMED")
        return result

    async def seal_manifest(self, manifest_hash):
        assert manifest_hash == self.inventory.manifest_hash and self.required == self.materialized
        self.manifest_status = "completed"
        if self.seal_failure:
            raise BackendError("Seal receipt was lost")


def test_sdk_materializes_skill_binary_and_versioned_files_then_preserves_edits_on_resume(tmp_path):
    async def scenario():
        disk = ManifestFixture(tmp_path)
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, sources=sources())
        session = await client.create()
        await session.start()
        assert disk.manifest_status == "completed" and disk.prepares == 1
        for path, body in BODIES.items():
            assert (disk.root / path).read_bytes() == body
        with pytest.raises(BackendError, match="approved SDK patch"):
            await session.write("docs/notes.txt", io.BytesIO(b"unapproved"))
        with native_patch_call_scope("approved", "sdk-edit", "a"*64):
            await session.write("docs/notes.txt", io.BytesIO(b"agent revision"))
        await session.aclose()
        first = client.require_confirmed_cleanup()
        saved = client.serialize_session_state(session.state)
        encoded = json.dumps(saved)
        assert "sample.bin" in encoded and base64.b64encode(BODIES[".skills/fixture/assets/sample.bin"]).decode() not in encoded
        assert "Read references/guide.txt" not in encoded
        disk.lease = None
        restored_client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, manifest=Manifest.model_validate(saved["manifest"]))
        restored = await restored_client.resume(restored_client.deserialize_session_state(saved))
        await restored.start()
        assert (await restored.read("docs/notes.txt")).read() == b"agent revision"
        assert (await restored.read(".skills/fixture/assets/sample.bin")).read() == BODIES[".skills/fixture/assets/sample.bin"]
        assert disk.prepares == 1 and len(disk.materialized) == len(disk.required)
        await restored.aclose()
        assert restored_client.require_confirmed_cleanup().version == first.version+1
    asyncio.run(scenario())


@pytest.mark.parametrize("failure", ["lost", "cancel", "seal"])
def test_sdk_incomplete_materialization_never_delivers_destroys_or_replays(tmp_path, failure):
    async def scenario():
        disk = ManifestFixture(tmp_path)
        disk.file_failure = failure if failure != "seal" else None
        disk.seal_failure = failure == "seal"
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, sources=sources())
        session = await client.create()
        with pytest.raises((BackendError, asyncio.CancelledError)):
            await session.start()
        count = len(disk.file_calls)
        assert client._runtime_session._materializing == ""
        with pytest.raises(BackendError):
            await session.start()
        with pytest.raises(BackendError):
            await session.aclose()
        with pytest.raises(BackendError):
            client.require_confirmed_cleanup()
        assert disk.closes == 0 and len(disk.file_calls) == count
    asyncio.run(scenario())


@pytest.mark.parametrize("change", ["hash", "source", "overlap", "case", "escape", "binary_size", "extra"])
def test_inventory_rejects_corrupt_or_ambiguous_sources(change):
    value = inventory().model_dump(exclude_none=True)
    file = value["files"][0]
    if change == "hash":
        value["manifest_hash"] = "c"*64
    elif change == "source":
        file["project_file"] = {"path": "notes.txt", "version": 1, "content_hash": "a"*64}
    elif change == "overlap":
        value["files"].append(file.copy())
    elif change == "case":
        second = file.copy()
        second["path"] = file["path"].replace("fixture", "FIXTURE")
        value["files"].append(second)
    elif change == "escape":
        file["path"] = "../escape"
    elif change == "binary_size":
        file["size_bytes"] = MAX_FILE_BYTES+1
    elif change == "extra":
        file["host_path"] = "untrusted"
    if change != "hash":
        value["manifest_hash"] = _go_json_hash(value["files"])
    with pytest.raises((ValueError, BackendError)):
        NativeManifestInventory.model_validate(value)


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_manifest_real_http_inventory_binary_and_seal(mode):
    selected = inventory()
    binary = next(file for file in selected.files if file.path.endswith("sample.bin"))
    payload = {"session_id": SESSION, "manifest_hash": selected.manifest_hash, "path": binary.path,
               "content": base64.b64encode(BODIES[binary.path]).decode()}
    activity = {"agent_turn_id": "turn"} if mode == "main" else {
        "task_attempt_id" if mode == "background" else "execution_attempt_id": "attempt", "attempt_token": "fixture-token",
    }
    with wire(response(lease()), response(selected.model_dump(exclude_none=True)), response(payload),
              response({"session_id": SESSION, "manifest_hash": selected.manifest_hash, "status": "completed"})) as (backend, requests):
        async def scenario():
            with backend_activity("project", **activity):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1 if mode == "main" else 0)
                await transport.reserve()
                assert await transport.prepare_manifest(sources()) == selected
                assert await transport.read_manifest_file(selected.manifest_hash, binary) == BODIES[binary.path]
                await transport.seal_manifest(selected.manifest_hash)
        asyncio.run(scenario())
        assert len(requests) == 4
        assert requests[2][2]["X-Workspace-Lease-Key"] == KEY


@pytest.mark.parametrize("failure", ["session", "path", "hash", "content", "base64", "size"])
def test_manifest_resource_http_rejects_mismatched_receipt(failure):
    selected = inventory()
    file = selected.files[0]
    payload = {"session_id": SESSION, "manifest_hash": selected.manifest_hash, "path": file.path,
               "content": base64.b64encode(BODIES[file.path]).decode()}
    if failure == "session": payload["session_id"] = OTHER
    if failure == "path": payload["path"] = "other.txt"
    if failure == "hash": payload["manifest_hash"] = "d"*64
    if failure == "content": payload["content"] = base64.b64encode(b"x"*file.size_bytes).decode()
    if failure == "base64": payload["content"] = "*"
    if failure == "size": payload["content"] = ""
    with wire(response(lease()), response(payload)) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                with pytest.raises(BackendError):
                    await transport.read_manifest_file(selected.manifest_hash, file)
        asyncio.run(scenario())
        assert len(requests) == 2


def test_actual_sdk_resource_initialization_and_snapshot_rebuild_over_http(tmp_path):
    disk = ManifestFixture(tmp_path)

    def handle(record):
        method, path, headers, body = record
        assert headers["X-Agent-Turn-ID"] == "turn" and headers["X-Agent-Project-ID"] == "project"

        async def reply():
            if path.endswith("/current"):
                current = await disk.current()
                return response({"lease": None if current is None else current.model_dump(mode="json")})
            if path.endswith("/reservations"):
                disk.lease = None
                return response((await disk.reserve(json.loads(body)["expected_generation"])).model_dump(mode="json"))
            if path.endswith("/lease"):
                return response((await disk.validate()).model_dump(mode="json"))
            if path.endswith("/manifest"):
                selected = NativeManifestSources.model_validate(json.loads(body))
                return response((await disk.prepare_manifest(selected)).model_dump(exclude_none=True))
            if path.endswith("/manifest/file"):
                request = json.loads(body)
                file = next(file for file in disk.inventory.files if file.path == request["path"])
                data = await disk.read_manifest_file(request["manifest_hash"], file)
                return response({"session_id": SESSION, **request, "content": base64.b64encode(data).decode()})
            if path.endswith("/manifest/seal"):
                request = json.loads(body)
                await disk.seal_manifest(request["manifest_hash"])
                return response({"session_id": SESSION, **request, "status": "completed"})
            for operation in ("ensure", "probe", "shutdown"):
                if path.endswith("/"+operation):
                    state = await getattr(disk, operation)()
                    return response({"session_id": SESSION, "state": state or ("closed" if operation == "shutdown" else "ready")})
            if path.endswith("/files"):
                request = json.loads(body)
                values = request["file"]
                operation = NativeFileOperation(**{**values, "data": base64.b64decode(values.get("data", ""))})
                call = NativePatchCall(request["agent_tool_call_id"], request["sdk_tool_call_id"], request["arguments_hash"]) if request["agent_tool_call_id"] else None
                result = await disk.file_operation(request["request_id"], operation, call, materialization_hash=request.get("materialization_hash", ""))
                encoded = asdict(result)
                encoded["data"] = base64.b64encode(result.data).decode()
                return response({"session_id": SESSION, "request_id": request["request_id"], "request_hash": operation.request_hash, "file": encoded})
            if path.endswith("/export"):
                return archive_reply(await disk.export_workspace(), version=0)
            if "/snapshots/" in path and method == "PUT":
                reference = await disk.save_snapshot(SESSION, path.rsplit("/", 1)[1], int(headers["X-Workspace-Snapshot-Parent"]), body)
                return response({"session_id": SESSION, **reference.model_dump()})
            if "/snapshots/" in path and method == "GET":
                from urllib.parse import parse_qs, urlsplit
                url = urlsplit(path)
                reference = SnapshotReference(version=int(url.path.rsplit("/", 1)[1]), sha256=parse_qs(url.query)["sha256"][0])
                return archive_reply(await disk.read_snapshot(SESSION, reference), version=reference.version)
            if path.endswith("/restore"):
                request = json.loads(body)
                await disk.restore_environment(request["request_id"], SnapshotReference(version=request["version"], sha256=request["sha256"]))
                return response({"session_id": SESSION, "state": "ready"})
            raise AssertionError(f"Unexpected HTTP request: {method} {path}")

        return asyncio.run(reply())

    with wire(*([handle] * 128)) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                client = RuntimeSandboxClient(transport, RuntimeSandboxFileSession, sources=sources())
                session = await client.create()
                await session.start()
                assert (await session.read(".skills/fixture/assets/sample.bin")).read() == BODIES[".skills/fixture/assets/sample.bin"]
                with native_patch_call_scope("approved", "sdk-edit", "a"*64):
                    await session.write("docs/notes.txt", io.BytesIO(b"revision over HTTP"))
                await session.aclose()
                first = client.require_confirmed_cleanup()
                saved = client.serialize_session_state(session.state)
                second = RuntimeSandboxClient(NativeWorkspaceHTTPTransport(backend, "d"*64, 1), RuntimeSandboxFileSession,
                                              manifest=Manifest.model_validate(saved["manifest"]))
                restored = await second.resume(second.deserialize_session_state(saved))
                await restored.start()
                assert (await restored.read("docs/notes.txt")).read() == b"revision over HTTP"
                await restored.aclose()
                assert second.require_confirmed_cleanup().version == first.version+1
        asyncio.run(scenario())
        assert disk.prepares == 1
        assert len([record for record in requests if record[1].endswith("/manifest/seal")]) == 1
        assert len([record for record in requests if record[1].endswith("/manifest/file")]) == len(BODIES)
