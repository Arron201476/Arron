import asyncio
import hashlib
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest
from agents import Agent
from agents.sandbox.capabilities import Memory

from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.managed_memory import resolve_memory_source, validate_memory_manifest
from content_agent_sidecar.native_files import _go_json_hash
from content_agent_sidecar.native_manifest import (
    MEMORY_DIRECTORY, NativeManifestFile, NativeManifestInventory, NativeMemorySource, NativeProjectSource,
)
from content_agent_sidecar.native_publication import PublicationSelection
from content_agent_sidecar.native_runner import bind_native_agent
from test_native_runner import RunnerWorkspace, invoke, setup, tools_for
from test_stateful_execution import StreamingSequence, final_item


BODY = "Prefer precise scene descriptions. Preserve supplied dialogue."
FILES = {"memory_summary.md": BODY}
SOURCE = NativeMemorySource(version=1, content_hash=_go_json_hash(FILES))


def snapshot(project="project", activity="turn:turn"):
    return {"activity_key": activity, "project_id": project, "workspace_id": "test-workspace",
            "user_id": "test-user", "version": 1, "current_version": 1, "content_hash": SOURCE.content_hash,
            "read_enabled": True, "files": dict(FILES)}


class MemoryWorkspace(RunnerWorkspace):
    def __init__(self, root):
        super().__init__(root)
        file = NativeManifestFile(path=f"{MEMORY_DIRECTORY}/memory_summary.md", sha256=hashlib.sha256(BODY.encode()).hexdigest(),
                                  size_bytes=len(BODY.encode()), memory=SOURCE, resource_path="memory_summary.md")
        files = sorted([*self.inventory.files, file], key=lambda item: item.path)
        self.inventory = NativeManifestInventory(session_id=self.inventory.session_id, files=files,
                                                manifest_hash=_go_json_hash([item.model_dump(exclude_none=True) for item in files]))
        self.required.update({("write", file.path), ("chmod", file.path), ("mkdir", MEMORY_DIRECTORY), ("chmod", MEMORY_DIRECTORY)})
        self.bodies[file.path] = BODY.encode()

    async def prepare_manifest(self, selected):
        assert selected.memory == SOURCE
        return await super().prepare_manifest(selected.model_copy(update={"memory": None}))

    async def read_manifest_file(self, manifest_hash, file):
        if file.memory:
            assert file.memory == SOURCE and file in self.inventory.files and manifest_hash == self.inventory.manifest_hash
            return BODY.encode()
        return await super().read_manifest_file(manifest_hash, file)


class MemoryModel(StreamingSequence):
    async def stream_response(self, *args, **kwargs):
        assert BODY in args[0] and MEMORY_DIRECTORY in args[0]
        async for event in super().stream_response(*args, **kwargs):
            yield event


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_production_streams_read_native_memory_from_versioned_files(tmp_path, monkeypatch, mode):
    async def run():
        monkeypatch.setattr("test_native_runner.RunnerWorkspace", MemoryWorkspace)
        disk, backend, context, _, config = setup(tmp_path, monkeypatch, mode, [])
        async def read(project, key):
            return snapshot(project, key)
        backend.resolve_agent_memory_snapshot = read
        model = MemoryModel([[final_item({"title": "first"})], [final_item({"title": "next"})]])
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Memory worker", model=model))
            capability = next(item for item in agent.capabilities if type(item) is Memory)
            assert capability.generate is None and capability.read.live_update is False
            assert (await invoke(mode, agent, "First", context, config)).final_output
            assert (await invoke(mode, agent, "Next", context, config)).final_output
        assert disk.prepares == 1 and len(disk.restores) == 1
        assert (disk.root / MEMORY_DIRECTORY / "memory_summary.md").read_text() == BODY
        assert len(model.inputs) == 2
    asyncio.run(run())


@pytest.mark.parametrize("change", ["project", "activity", "user", "hash", "disabled", "version", "path", "extra", "missing"])
def test_memory_source_rejects_wrong_identity_or_content(change):
    raw = snapshot()
    if change in {"project", "activity", "user"}:
        raw[{"project": "project_id", "activity": "activity_key", "user": "user_id"}[change]] = "foreign"
    elif change == "hash": raw["content_hash"] = "0" * 64
    elif change == "disabled": raw["read_enabled"] = False
    elif change == "version": raw["version"] = True
    elif change == "path": raw["files"]["../escape"] = "private"
    elif change == "extra": raw["host_path"] = "C:/private"
    else: raw["files"] = {}
    backend = SimpleNamespace(resolve_agent_memory_snapshot=AsyncMock(return_value=raw))
    context = SimpleNamespace(backend=backend, project_id="project", agent_turn_id="turn", agent_task_attempt_id="", execution_attempt_id="",
                              managed_instructions={"workspace_id": "test-workspace", "user_id": "test-user"})
    with pytest.raises(BackendError, match="snapshot"):
        asyncio.run(resolve_memory_source(context))


@pytest.mark.parametrize("path", [".agent-memory/memory_summary.md", ".AGENT-MEMORY/notes/a.md"])
def test_memory_namespace_is_not_a_project_source_or_publication(path):
    with pytest.raises(ValueError):
        NativeProjectSource(path=path, version=1, content_hash="a"*64)
    for source, destination in [(path, "export.md"), ("notes.md", path)]:
        with pytest.raises(ValueError):
            PublicationSelection(source_path=source, path=destination)


def test_memory_manifest_cannot_restore_another_or_disabled_version(tmp_path):
    manifest = MemoryWorkspace(tmp_path).inventory.manifest()
    validate_memory_manifest(manifest, SOURCE)
    for source in (None, SOURCE.model_copy(update={"version": 2})):
        with pytest.raises(BackendError, match="binding"):
            validate_memory_manifest(manifest, source)
