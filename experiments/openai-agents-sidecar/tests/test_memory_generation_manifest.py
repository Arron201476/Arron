from types import SimpleNamespace

import pytest
from agents.sandbox.entries import Dir
from agents.sandbox.manifest import Manifest

from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.native_manifest import NativeGenerationSource, NativeManifestFile, NativeManifestInventory, RuntimeResourceFile
from content_agent_sidecar.native_files import _go_json_hash
from content_agent_sidecar.native_publication import PublicationSelection
from content_agent_sidecar.native_runner import NativeWorkspaceExecution


def source():
    return NativeGenerationSource(generation_id="generation", source_hash="a" * 64, extraction_hash="b" * 64)


@pytest.mark.parametrize("fault", [None, "missing", "foreign", "alias"])
def test_generation_inventory_requires_both_exact_private_sources(fault):
    owner = object.__new__(NativeWorkspaceExecution)
    owner.context = SimpleNamespace(native_generation_source=source())
    children = {}
    for name in ("rollout.jsonl", "extraction.json"):
        reference = source().model_copy(update={"generation_id": "foreign"}) if fault == "foreign" else source()
        children[name] = RuntimeResourceFile(resource=NativeManifestFile(path=".agent-memory-input/" + name,
            sha256="c" * 64, size_bytes=1, generation=reference, resource_path=name), manifest_hash="d" * 64)
    if fault == "missing":
        children.pop("extraction.json")
    if fault == "alias":
        children["alias"] = children.pop("extraction.json")
    owner.manifest = Manifest(root="/workspace", entries={".agent-memory-input": Dir(children=children)})
    if fault:
        with pytest.raises(BackendError):
            owner._validate_memory_generation_manifest()
    else:
        owner._validate_memory_generation_manifest()


@pytest.mark.parametrize("path", [".agent-memory-input/rollout.jsonl", ".AGENT-MEMORY-INPUT/extraction.json"])
def test_generation_inputs_cannot_use_shared_publication(path):
    for values in ({"source_path": path, "path": "shared.txt"}, {"source_path": "note.txt", "path": path}):
        with pytest.raises(ValueError, match="Private memory"):
            PublicationSelection(**values)


@pytest.mark.parametrize("path,resource", [("note.txt", "rollout.jsonl"), (".agent-memory-input/other", "other")])
def test_generation_inputs_have_fixed_destinations(path, resource):
    with pytest.raises(ValueError):
        NativeManifestFile(path=path, sha256="c" * 64, size_bytes=1, generation=source(), resource_path=resource)


@pytest.mark.parametrize("fault", [None, "missing", "hash", "receipt"])
def test_planned_manifest_requires_exact_file_set_and_hashes(fault):
    reference = source().model_copy(update={"plan_hash": "e" * 64})
    paths = [".agent-memory/raw_memories.md", ".agent-memory/raw_memories/rollout.md",
             ".agent-memory/rollout_summaries/rollout_fixture.md", ".agent-memory-input/rollout.jsonl"]
    owner = object.__new__(NativeWorkspaceExecution)
    owner.context = SimpleNamespace(native_generation_source=reference, native_generation_files={path: "c" * 64 for path in paths})
    files = [NativeManifestFile(path=path, resource_path=path, generation=reference, size_bytes=1, sha256="c" * 64) for path in paths]
    if fault == "missing": files.pop()
    if fault == "hash": files[0] = files[0].model_copy(update={"sha256": "b" * 64})
    if fault == "receipt": files[0] = files[0].model_copy(update={"generation": reference.model_copy(update={"plan_hash": "a" * 64})})
    inventory = NativeManifestInventory(session_id="11111111-1111-1111-1111-111111111111", files=files,
        manifest_hash=_go_json_hash([file.model_dump(exclude_none=True) for file in files]))
    owner.manifest = inventory.manifest()
    if fault:
        with pytest.raises(BackendError): owner._validate_memory_generation_manifest()
    else:
        owner._validate_memory_generation_manifest()
