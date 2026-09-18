import asyncio
from types import SimpleNamespace

import pytest
from agents import Agent
from agents.sandbox import SandboxAgent
from agents.sandbox.entries import Dir, File
from agents.sandbox.manifest import Manifest

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.native_manifest import NativeManifestFile, NativeMemorySource, RuntimeResourceFile
from content_agent_sidecar.native_runner import NativeWorkspaceExecution
from content_agent_sidecar.runtime import AgentContext
from content_agent_sidecar.memory_publication import prepare_agent_memory_publication, publish_agent_memory
from test_native_capabilities import native_backend


class Transport:
    lease = None
    async def current(self):
        return None


def setup(monkeypatch):
    backend = native_backend(disabled=("view_image",))
    async def forbidden_files(*args):
        pytest.fail("memory generation read project files")
    backend.list_workspace_files = forbidden_files
    context = AgentContext("project", "conversation", backend, memory_generation_id="generation", memory_generation_attempt=1,
                           attempt_token="private-token")
    monkeypatch.setattr("content_agent_sidecar.native_runner.NativeWorkspaceHTTPTransport.new", lambda *args: Transport())
    prepared_sources = []
    class Client:
        def __init__(self, *args, sources=None, manifest=None, **kwargs):
            self.manifest = manifest if manifest is not None else Manifest(root="/workspace")
            if sources is not None:
                prepared_sources.append(sources)
        async def prepare_manifest(self):
            return self.manifest
    monkeypatch.setattr("content_agent_sidecar.native_runner.RuntimeSandboxClient", Client)
    return backend, context, prepared_sources


def test_memory_preparation_skips_project_sources_and_binds_private_instructions(monkeypatch):
    backend, context, sources = setup(monkeypatch)
    async def run():
        prepared = await AgentToolProvider(backend, [], guardrail_policy=SDKGuardrailPolicy()).prepare(context, [], set())
        owner = NativeWorkspaceExecution(context)
        agent = await owner.bind_agent(Agent(name="sandbox-memory-phase-two", instructions="STAGE_POLICY", model="configured"), prepared)
        assert isinstance(agent, SandboxAgent)
        assert len(sources) == 1 and sources[0].skills == sources[0].files == []
        assert sources[0].memory is None
        assert "STAGE_POLICY" in agent.instructions and "current private memory generation" in agent.instructions
        assert "prepare_workspace_publication" not in agent.instructions
        assert "publish_agent_memory" not in agent.instructions
        assert "validate_workspace_skill" not in agent.instructions
        await owner.prepare(prepared)
        assert len(sources) == 1
    asyncio.run(run())


def test_consolidation_binds_audited_private_publication_tools(monkeypatch):
    backend, context, _ = setup(monkeypatch)
    context.native_generation_source = SimpleNamespace(generation_id="generation")
    for name, approval, access in [("prepare_agent_memory_publication", "never", "read"),
                                   ("publish_agent_memory", "always", "write")]:
        backend.catalog["tools"].append({**backend.catalog["tools"][0], "id": "runtime:" + name,
                                      "name": name, "approval": approval, "access": access, "enabled": True})

    async def run():
        prepared = await AgentToolProvider(backend, [prepare_agent_memory_publication, publish_agent_memory],
            guardrail_policy=SDKGuardrailPolicy()).prepare(context, [], set())
        owner = NativeWorkspaceExecution(context)
        owner.manifest = Manifest(root="/workspace")
        bound = await owner.bind_agent(Agent(name="sandbox-memory-phase-two", model="configured"), prepared)
        assert isinstance(bound, SandboxAgent)
        assert {tool.name for tool in bound.tools} == {"prepare_agent_memory_publication", "publish_agent_memory"}
        assert "owner must approve" in bound.instructions
        assert not bound.handoffs and not bound.mcp_servers
        assert "prepare_workspace_publication" not in bound.instructions
    asyncio.run(run())


@pytest.mark.parametrize("invalid", ["selected-skill", "external-tool", "image-tool"])
def test_memory_preparation_rejects_business_inventory_before_workspace_access(monkeypatch, invalid):
    backend, context, sources = setup(monkeypatch)
    selected = {"skill"} if invalid == "selected-skill" else set()
    name = "runtime:view_image" if invalid == "image-tool" else "mcp:external/tool"
    prepared = SimpleNamespace(selected_ids=selected, catalog=SimpleNamespace(descriptors={name: SimpleNamespace(enabled=True)}))
    with pytest.raises(BackendError, match="private native tool selection"):
        asyncio.run(NativeWorkspaceExecution(context).prepare(prepared))
    assert not sources


def test_memory_manifest_recovery_rejects_nonprivate_entries(monkeypatch):
    _, context, _ = setup(monkeypatch)
    owner = NativeWorkspaceExecution(context)
    source = NativeMemorySource(version=1, content_hash="a" * 64)
    file = RuntimeResourceFile(resource=NativeManifestFile(path=".agent-memory/memory_summary.md", sha256="b" * 64,
        size_bytes=4, memory=source, resource_path="memory_summary.md"), manifest_hash="c" * 64)
    owner.manifest = Manifest(root="/workspace", entries={".agent-memory": Dir(children={"memory_summary.md": file})})
    owner._validate_memory_generation_manifest()
    for name, entry in [("project.txt", File(content="shared")), (".skills", Dir()), ("alias.txt", file)]:
        owner.manifest = Manifest(root="/workspace", entries={name: entry})
        with pytest.raises(BackendError, match="non-private resources"):
            owner._validate_memory_generation_manifest()


def test_business_preparation_still_selects_project_files_and_publication_instructions(monkeypatch):
    backend, context, sources = setup(monkeypatch)
    context.memory_generation_id, context.memory_generation_attempt = "", 0
    context.agent_turn_id, context.attempt_token = "turn", ""
    reads = []
    async def files(project):
        reads.append(project)
        return [{"path": "notes.txt", "version": 1, "content_hash": "a" * 64}]
    backend.list_workspace_files = files
    async def run():
        prepared = await AgentToolProvider(backend, [], guardrail_policy=SDKGuardrailPolicy()).prepare(context, [], set())
        agent = await NativeWorkspaceExecution(context).bind_agent(Agent(name="business", model="configured"), prepared)
        assert reads == ["project"] and sources[0].files[0].path == "notes.txt"
        assert "prepare_workspace_publication" in agent.instructions
        assert "publish_agent_memory" in agent.instructions
    asyncio.run(run())


def test_memory_binding_rejects_inherited_business_handoff_before_preparation(monkeypatch):
    _, context, sources = setup(monkeypatch)
    agent = Agent(name="memory", model="configured", handoffs=[Agent(name="business", model="configured")])
    with pytest.raises(BackendError, match="inherit business"):
        asyncio.run(NativeWorkspaceExecution(context).bind_agent(agent, None))
    assert not sources
