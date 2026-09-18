import asyncio
from copy import deepcopy
from dataclasses import replace
import hashlib
import io
import json
import tarfile
from types import SimpleNamespace

import pytest
from agents import Agent, RunContextWrapper, RunState
from agents.tool import set_function_tool_failure_error_function
from agents.tool_context import ToolContext
from pydantic import ValidationError

from content_agent_sidecar.agent_tools import AgentToolConfigurationError, AgentToolProvider
from content_agent_sidecar.backend import BackendError, backend_activity
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.managed_instructions import bind_managed_instructions
from content_agent_sidecar.native_publication import (
    PublicationArguments, PublicationFile, prepare_workspace_publication,
    publish_workspace_files, snapshot_files,
)
from content_agent_sidecar.native_runner import bind_native_agent
from content_agent_sidecar.native_snapshot import SnapshotReference
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport
from content_agent_sidecar.runtime import _serialize_agent_context
from content_agent_sidecar.stateful_execution import ExecutionTaskPaused
from test_native_manifest import BODIES, SKILL, sources
from test_native_runner import setup
from test_native_runner import invoke as invoke_runner
from test_native_workspace_transport import KEY, SESSION, lease, response, wire
from test_stateful_execution import StreamingSequence, final_item, tool_item


SELECTION = [
    {"source_path": "docs/notes.txt", "path": "draft/z-notes.txt"},
    {"source_path": ".skills/fixture/SKILL.md", "path": "draft/SKILL.md"},
    {"source_path": ".skills/fixture/assets/sample.bin", "path": "draft/assets/sample.bin"},
    {"source_path": ".skills/fixture/references/guide.txt", "path": "draft/references/guide.txt"},
]


async def prepared_for(backend, context):
    await bind_managed_instructions(context)
    prepared = await AgentToolProvider(backend, [prepare_workspace_publication, publish_workspace_files],
                                       guardrail_policy=SDKGuardrailPolicy()).prepare(context, [], set())
    prepared.capabilities = {SKILL.capability_id: {
        "capability_id": SKILL.capability_id, "version": SKILL.version, "execution_mode": "inline",
        "status": "available", "skill": {"content_hash": SKILL.content_hash},
    }}
    prepared.selected_ids = {SKILL.capability_id}
    return prepared


def publication_setup(tmp_path, monkeypatch, mode):
    disk, backend, context, _, config = setup(tmp_path, monkeypatch, mode, [])
    backend.published = []
    for name, access, approval in [("prepare_workspace_publication", "read", "never"), ("publish_workspace_files", "write", "always")]:
        descriptor = deepcopy(backend.catalog["tools"][0])
        descriptor.update(id="runtime:" + name, name=name, access=access, approval=approval, max_result_bytes=512 * 1024)
        backend.catalog["tools"].append(descriptor)

    async def files(_project):
        return [{"project_id": "project", "deleted": False, **file.model_dump()} for file in sources().files]

    async def publish(call_id, sdk_id, arguments):
        assert backend.calls[sdk_id]["status"] == "running"
        assert not backend.published
        args = PublicationArguments.model_validate(arguments)
        body = await disk.read_snapshot(disk.lease.session_id, args.snapshot)
        entries = snapshot_files(body, args.snapshot, args.files)
        receipt = {"session_id": disk.lease.session_id, "agent_tool_call_id": call_id, "snapshot": arguments["snapshot"], "files": [
            {"project_id": "project", "path": file.path, "version": file.expected_version + 1,
             "deleted": False, "agent_tool_call_id": call_id, **entries[file.source_path]}
            for file in args.files
        ]}
        backend.published.append(deepcopy(receipt))
        return receipt

    backend.list_workspace_files = files
    disk.publish_files = publish
    return disk, backend, context, config


class PublishingModel(StreamingSequence):
    def __init__(self, disk):
        super().__init__([])
        self.disk = disk
        self.prepared = None

    async def stream_response(self, *args, **kwargs):
        if not self.inputs:
            self.responses = [[tool_item("prepare_workspace_publication", {"files": SELECTION}, "prepare")]]
        elif len(self.inputs) == 1:
            output = next(item for item in reversed(args[1]) if isinstance(item, dict) and item.get("type") == "function_call_output")
            self.prepared = json.loads(output["output"])
            assert self.prepared["published"] is False
            # An actual temporary-tree edit after preparation must not replace
            # bytes shown for approval. This is an engine fixture, not OCI proof.
            (self.disk.root / "docs/notes.txt").write_bytes(b"edited after preparation")
            self.responses = [[tool_item("publish_workspace_files", self.prepared["publish_arguments"], "publish")]]
        else:
            self.responses = [[final_item({"title": "publication complete"})]]
        async for event in super().stream_response(*args, **kwargs):
            yield event


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
@pytest.mark.parametrize("approve", [True, False])
def test_sdk_three_loop_publication_freeze_approval_rebuild_and_binary_receipt(tmp_path, monkeypatch, mode, approve):
    async def scenario():
        disk, backend, context, config = publication_setup(tmp_path, monkeypatch, mode)
        model = PublishingModel(disk)
        prepared = await prepared_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Publisher", model=model, tools=prepared.tools))
            try:
                pending = await invoke_runner(mode, agent, "Publish a reusable Skill draft", context, config)
                saved = pending.to_state().to_json(context_serializer=_serialize_agent_context)
            except ExecutionTaskPaused as paused:
                saved = paused.run_state
            assert not backend.published
            assert model.prepared["publish_arguments"]["snapshot"]["version"] == 1
            assert len(disk.saves) == 2 and disk.closes == 0
            assert saved["context"]["context"]["native_workspace_checkpoint"]["state"] == "ready" and "sandbox" not in saved
            assert context.native_workspace.state.snapshot.reference.version == 2
        context = replace(context, native_workspace=None, active_tool_calls={})
        disk.lease = None
        prepared = await prepared_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Publisher", model=model, tools=prepared.tools))
            state = await RunState.from_json(agent, saved, context_override=context)
            item = state.get_interruptions()[0]
            state.approve(item) if approve else state.reject(item)
            backend.calls["publish"]["status"] = "approved" if approve else "rejected"
            result = await invoke_runner(mode, agent, state, context, config)
        assert result.final_output and len(backend.published) == int(approve)
        assert len(disk.saves) == 3 and disk.closes == 1 and disk.creates == 1 and not disk.restores
        assert (disk.root / "docs/notes.txt").read_bytes() == b"edited after preparation"
        if approve:
            files = backend.published[0]["files"]
            assert [file["path"] for file in files] == [file["path"] for file in SELECTION]
            assert files[0]["content_hash"] == hashlib.sha256(BODIES["docs/notes.txt"]).hexdigest()
            assert files[2]["binary"] and files[2]["size_bytes"] > 65536
            outputs = [json.loads(item["output"]) for item in model.inputs[-1] if isinstance(item, dict)
                       and item.get("type") == "function_call_output" and item.get("call_id") == "publish"]
            assert outputs and not outputs[0]["installed_as_skill"]
            assert all("version=1" in file["content_url"] for file in outputs[0]["files"])
    asyncio.run(scenario())


@pytest.mark.parametrize("changes", [{"access": "read"}, {"approval": "never"}, {"max_retries": 1}])
def test_publication_policy_cannot_remove_approval_or_retry(tmp_path, monkeypatch, changes):
    async def scenario():
        _, backend, context, _ = publication_setup(tmp_path, monkeypatch, "main")
        backend.catalog["tools"][-1].update(changes)
        with pytest.raises(AgentToolConfigurationError):
            await prepared_for(backend, context)
    asyncio.run(scenario())


@pytest.mark.parametrize("files", [[], [{"source_path": "../private", "path": "a"}],
    [{"source_path": "a", "path": ".skills/a"}], [{"source_path": "a", "path": "a"}, {"source_path": "b", "path": "A"}],
    [{"source_path": "a", "path": "a"}, {"source_path": "b", "path": "a/b"}]])
def test_publication_rejects_unsafe_or_overlapping_paths(files):
    with pytest.raises((ValueError, ValidationError, BackendError)):
        PublicationArguments(snapshot=SnapshotReference(version=1, sha256="a" * 64), files=[
            PublicationFile(**file, expected_version=0) for file in files
        ])


def test_unknown_preparation_save_is_not_retried_by_sdk_cleanup(tmp_path, monkeypatch):
    async def scenario():
        disk, backend, context, config = publication_setup(tmp_path, monkeypatch, "main")
        disk.fail_save = "after"
        prepared = await prepared_for(backend, context)
        model = StreamingSequence([[tool_item("prepare_workspace_publication", {"files": SELECTION}, "prepare")], [final_item({"title": "failed"})]])
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Publisher", model=model, tools=prepared.tools))
            with pytest.raises(BackendError):
                await invoke_runner("main", agent, "Prepare", context, config)
        assert len(disk.saves) == 1 and not backend.published and disk.closes == 0
    asyncio.run(scenario())


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_publication_transport_uses_real_local_http_and_execution_identity(mode):
    arguments = {"snapshot": {"version": 1, "sha256": "b" * 64}, "files": [{"source_path": "a", "path": "draft/a", "expected_version": 2}]}
    activity = {"agent_turn_id": "turn"} if mode == "main" else {
        "task_attempt_id" if mode == "background" else "execution_attempt_id": "attempt", "attempt_token": "fixture-attempt",
    }
    with wire(response(lease()), response({"session_id": SESSION})) as (backend, requests):
        async def scenario():
            with backend_activity("project", **activity):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1 if mode == "main" else 0)
                await transport.reserve()
                assert await transport.publish_files("call", "sdk", arguments) == {"session_id": SESSION}
        asyncio.run(scenario())
        method, path, headers, body = requests[-1]
        assert (method, path) == ("POST", f"/internal/v1/native-workspaces/{SESSION}/publications")
        assert headers["X-Agent-Project-ID"] == "project" and headers["X-Workspace-Lease-Key"] == KEY
        assert json.loads(body) == {"agent_tool_call_id": "call", "sdk_tool_call_id": "sdk", "arguments": arguments}


def test_publication_transports_256_files_above_small_control_request_and_response_limits():
    files = [{"source_path": f"resources/{index}.bin", "path": f"draft/{'a' * 100}/{index}.bin", "expected_version": 0}
             for index in range(256)]
    arguments = {"snapshot": {"version": 1, "sha256": "b" * 64}, "files": files}
    receipt = {"session_id": SESSION, "agent_tool_call_id": "call", "snapshot": arguments["snapshot"], "files": [
        {"project_id": "project", "path": file["path"], "version": 1, "agent_tool_call_id": "call",
         "deleted": False, "size_bytes": 3, "content_hash": "a" * 64, "binary": True} for file in files
    ]}
    assert len(json.dumps(arguments).encode()) > 4096
    assert len(json.dumps({"data": receipt}).encode()) > 64 << 10
    with wire(response(lease()), response(receipt)) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                assert await transport.publish_files("call", "sdk", arguments) == receipt
        asyncio.run(scenario())
        assert len(requests) == 2
        assert json.loads(requests[-1][3])["arguments"] == arguments


def test_publication_is_hidden_without_a_native_workspace(tmp_path, monkeypatch):
    async def scenario():
        _, backend, context, _ = publication_setup(tmp_path, monkeypatch, "main")
        prepared = await prepared_for(backend, context)
        assert all(not tool.is_enabled(RunContextWrapper(context), None) for tool in prepared.tools)
    asyncio.run(scenario())


@pytest.mark.parametrize("path", ["../private", "/private", "a\\b", "CON", " a", "a/../b", "a\x00b"])
def test_source_manifest_enforces_the_shared_path_predicate(path):
    from content_agent_sidecar.native_manifest import NativeProjectSource, NativeManifestFile

    with pytest.raises(ValidationError):
        NativeProjectSource(path=path, version=1, content_hash="a" * 64)
    with pytest.raises(ValidationError):
        NativeManifestFile(path=path, size_bytes=0, sha256="a" * 64, skill=SKILL, resource_path=path)


@pytest.mark.parametrize("fault", ["session", "call", "snapshot", "count", "project", "path", "version", "deleted", "file-call", "size", "hash", "binary", "no-audit"])
def test_publication_never_reports_success_for_mismatched_receipts(fault):
    async def scenario():
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode="w") as writer:
            entry = tarfile.TarInfo("data.bin")
            entry.size = 3
            writer.addfile(entry, io.BytesIO(b"\x00\xff\x80"))
        body = archive.getvalue()
        snapshot = {"version": 1, "sha256": hashlib.sha256(body).hexdigest()}
        files = [{"source_path": "data.bin", "path": "published/data.bin", "expected_version": 0}]
        receipt = {"session_id": SESSION, "agent_tool_call_id": "call", "snapshot": deepcopy(snapshot), "files": [{
            "project_id": "project", "path": "published/data.bin", "version": 1, "deleted": False,
            "agent_tool_call_id": "call", "size_bytes": 3, "content_hash": hashlib.sha256(b"\x00\xff\x80").hexdigest(), "binary": True,
        }]}
        if fault in {"session", "call"}:
            receipt["session_id" if fault == "session" else "agent_tool_call_id"] = "other"
        elif fault == "snapshot":
            receipt["snapshot"]["sha256"] = "a" * 64
        elif fault == "count":
            receipt["files"] = []
        elif fault != "no-audit":
            field, value = {
                "project": ("project_id", "other"), "path": ("path", "different"), "version": ("version", True),
                "deleted": ("deleted", True), "file-call": ("agent_tool_call_id", "other"), "size": ("size_bytes", 2),
                "hash": ("content_hash", "b" * 64), "binary": ("binary", False),
            }[fault]
            receipt["files"][0][field] = value
        reads, writes = [], []

        async def read(session, ref):
            reads.append((session, ref))
            return body

        async def publish(*args):
            writes.append(args)
            return receipt

        transport = SimpleNamespace(lease=SimpleNamespace(session_id=SESSION), read_snapshot=read, publish_files=publish)
        owner = SimpleNamespace(client=object(), transport=transport, guard=SimpleNamespace(require_confirmed=lambda: None))
        context = SimpleNamespace(project_id="project", native_workspace=owner,
                                  active_tool_calls={} if fault == "no-audit" else {"sdk": {"agent_tool_call_id": "call"}})
        raw = json.dumps({"snapshot": snapshot, "files": files})
        wrapper = ToolContext(context, tool_name="publish_workspace_files", tool_call_id="sdk", tool_arguments=raw)
        unhandled = set_function_tool_failure_error_function(replace(publish_workspace_files), None)
        with pytest.raises(BackendError):
            await unhandled.on_invoke_tool(wrapper, raw)
        assert len(writes) == (0 if fault == "no-audit" else 1)
        if fault == "no-audit":
            assert not reads
    asyncio.run(scenario())
