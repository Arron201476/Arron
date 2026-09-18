import asyncio
from copy import deepcopy
import json
from pathlib import Path

import pytest
from agents import Agent, RunContextWrapper
from agents.tool_context import ToolContext

from content_agent_sidecar.agent_tools import AgentToolProvider, AuditedMCPServerStdio
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.project_skills import install_workspace_skill
from content_agent_sidecar.runtime import execute_skill_script, load_skill_instructions, _load_skill_instructions, _serialize_agent_context
from test_agent_tools import _mcp_catalog, _selected_skill
from test_project_skills import ARGS, SkillBackend, context_for


class DynamicSkillBackend(SkillBackend):
    def __init__(self):
        super().__init__()
        self.catalog = _mcp_catalog(Path(__file__).parent / "fixtures" / "story_mcp_server.py")
        self.detail["data"]["skill"]["name"] = "my-skill"

    async def get_agent_tool_catalog(self):
        catalog = deepcopy(self.catalog)
        runtime = await super().get_agent_tool_catalog()
        template = runtime["tools"][0]
        catalog["tools"].extend([template,
            {**template, "id": "runtime:execute_skill_script", "name": "execute_skill_script", "access": "sensitive"},
            {**template, "id": "runtime:load_skill_instructions", "name": "load_skill_instructions", "access": "read", "approval": "never", "timeout_seconds": 30}])
        return catalog

    def require(self, *, scripts=False, dependencies=None):
        manifest = self.saved["installation"]["versions"][0]["manifest"]
        manifest["dependencies"] = deepcopy(dependencies or [])
        manifest["scripts"] = [{"id": "review", "path": "scripts/review.py", "runtime": "python"}] if scripts else []
        self.detail["data"]["skill"].update(deepcopy(manifest))


async def install(backend, context):
    wrapper = ToolContext(context, tool_name="install_workspace_skill", tool_call_id="sdk-skill", tool_arguments=json.dumps(ARGS))
    return json.loads(await install_workspace_skill.on_invoke_tool(wrapper, json.dumps(ARGS)))


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
@pytest.mark.parametrize("policy_source", ["entry_policy", "skill"])
def test_discovery_respects_explicit_only_skill_policy(mode, policy_source):
    async def run():
        backend = DynamicSkillBackend()
        backend.require(scripts=True)
        if policy_source == "entry_policy":
            backend.detail["data"]["entry_policy"] = {"auto_route": False}
        else:
            backend.detail["data"]["skill"]["allow_implicit_invocation"] = False
        context = context_for(backend)
        if mode == "background": context.agent_task_id = "background-task"
        if mode == "stateful": context.execution_attempt_id = "stateful-attempt"
        requests = []
        original = backend.get_capability

        async def get_capability(*args):
            requests.append(args)
            return await original(*args)

        backend.get_capability = get_capability
        prepared = await AgentToolProvider(backend, [execute_skill_script, load_skill_instructions]).prepare(
            context, [deepcopy(backend.detail["data"])], set(),
        )
        async with prepared:
            with pytest.raises(ValueError):
                await _load_skill_instructions(context, "my_skill")
            assert not requests
            assert not context.routed_capabilities and not context.loaded_capabilities
            assert not context.allowed_skill_scripts and not prepared.mcp_servers
    asyncio.run(run())


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
@pytest.mark.parametrize("selection", ["menu", "routed", "approved_install"])
def test_explicit_only_skill_remains_loadable_after_selection(mode, selection):
    async def run():
        backend = DynamicSkillBackend()
        backend.detail["data"]["entry_policy"] = {"auto_route": False}
        backend.detail["data"]["skill"]["allow_implicit_invocation"] = False
        context = context_for(backend)
        if mode == "background": context.agent_task_id = "background-task"
        if mode == "stateful": context.execution_attempt_id = "stateful-attempt"
        selected = set()
        if selection == "menu":
            context.raw_request["capability_ref"] = {"capability_id": "my_skill", "version": "1.0.0"}
            selected.add("my_skill")
        elif selection == "routed":
            context.routed_capabilities.add("my_skill")
            context.capability_versions["my_skill"] = "1.0.0"
            selected.add("my_skill")
        prepared = await AgentToolProvider(backend, [install_workspace_skill, load_skill_instructions]).prepare(
            context, [deepcopy(backend.detail["data"])], selected,
        )
        async with prepared:
            assert "my_skill" not in prepared.discoverable_capabilities
            if selection == "approved_install":
                result = await install(backend, context)
                assert result["installed_as_skill"] and result["ready_in_current_turn"]
            loaded = json.loads(await _load_skill_instructions(context, "my_skill"))
            assert loaded["skill"]["instructions"] == "Report narrative gaps."
            assert loaded["version"] == "1.0.0"
            assert context.consulted_capabilities == {"my_skill"}
    asyncio.run(run())


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_discovered_inline_skill_activates_frozen_dependencies_without_installing(tmp_path, monkeypatch, mode):
    monkeypatch.setenv("TEST_STORY_MCP_STATE_FILE", str(tmp_path / "discovery.jsonl"))

    async def run():
        backend = DynamicSkillBackend()
        backend.require(scripts=True, dependencies=_selected_skill()[0]["skill"]["dependencies"])
        context = context_for(backend)
        if mode == "background": context.agent_task_id = "background-task"
        if mode == "stateful": context.execution_attempt_id = "stateful-attempt"
        provider = AgentToolProvider(backend, [execute_skill_script, load_skill_instructions])
        prepared = await provider.prepare(context, [deepcopy(backend.detail["data"])], set())
        async with prepared:
            assert not context.routed_capabilities and not prepared.mcp_servers
            loaded = json.loads(await _load_skill_instructions(context, "my_skill"))
            assert loaded["skill"]["content_hash"] == "hash-1"
            assert context.capability_versions["my_skill"] == "1.0.0"
            assert context.routed_capabilities == {"my_skill"}
            assert context.skill_script_snapshots["my_skill"]["content_hash"] == "hash-1"
            assert {tool.name for tool in await prepared.mcp_servers[0].list_tools()} == {"lookup_story_fact", "save_story_fact"}
            assert not backend.installs
            count = len(prepared._managers)
            await _load_skill_instructions(context, "my_skill")
            assert len(prepared._managers) == count
            checkpoint = _serialize_agent_context(context)
            assert checkpoint["loaded_capabilities"]["my_skill"] == backend.detail["data"]
        assert context.skill_tool_scope is None
    asyncio.run(run())


@pytest.mark.parametrize("case", ["missing", "disabled", "background", "hash", "version", "scope", "dependency"])
def test_inline_discovery_rejects_catalog_changes_without_publishing_permissions(case):
    async def run():
        backend = DynamicSkillBackend()
        context = context_for(backend)
        definition = deepcopy(backend.detail["data"])
        if case == "disabled": definition["status"] = "disabled"
        if case == "background": definition["execution_mode"] = "background_task"
        if case == "hash": backend.detail["data"]["skill"]["content_hash"] = "different"
        if case == "version": backend.detail["data"]["version"] = "2.0.0"
        if case == "scope": backend.detail["data"]["skill"]["scope"] = "workspace"
        if case == "dependency":
            backend.require(dependencies=[{"type": "mcp", "value": "unconfigured/tool"}])
            definition = deepcopy(backend.detail["data"])
        provider = AgentToolProvider(backend, [execute_skill_script, load_skill_instructions])
        prepared = await provider.prepare(context, [] if case == "missing" else [definition], set())
        async with prepared:
            with pytest.raises((ValueError, RuntimeError)):
                await _load_skill_instructions(context, "my_skill")
            assert not context.routed_capabilities and not context.loaded_capabilities
            assert not context.capability_versions and not prepared.mcp_servers and not backend.installs
    asyncio.run(run())


def test_native_manager_keeps_dynamic_connections_on_their_own_lifecycle_tasks(tmp_path, monkeypatch):
    monkeypatch.setenv("TEST_STORY_MCP_STATE_FILE", str(tmp_path / "writes.jsonl"))
    events = []
    original_connect, original_cleanup = AuditedMCPServerStdio.connect, AuditedMCPServerStdio.cleanup

    async def connect(server):
        events.append(("connect", id(server), asyncio.current_task()))
        await original_connect(server)

    async def cleanup(server):
        events.append(("cleanup", id(server), asyncio.current_task()))
        await original_cleanup(server)

    monkeypatch.setattr(AuditedMCPServerStdio, "connect", connect)
    monkeypatch.setattr(AuditedMCPServerStdio, "cleanup", cleanup)

    async def run():
        backend = DynamicSkillBackend()
        dependencies = _selected_skill()[0]["skill"]["dependencies"]
        backend.require(scripts=True, dependencies=dependencies)
        context = context_for(backend)
        provider = AgentToolProvider(backend, [install_workspace_skill, execute_skill_script, load_skill_instructions])
        prepared = await provider.prepare(context, [], set())
        agent = SDKGuardrailPolicy().protect(Agent(name="Author", tools=prepared.tools, mcp_servers=prepared.mcp_servers))
        clone = agent.clone(name="Compatible author", output_type=None)
        async with prepared:
            before = {tool.name for tool in await clone.get_all_tools(RunContextWrapper(context))}
            assert before == {"install_workspace_skill", "load_skill_instructions"}
            result = await asyncio.create_task(install(backend, context))
            assert result["installed_as_skill"] and result["ready_in_current_turn"]
            loaded = await clone.get_all_tools(RunContextWrapper(context))
            assert {tool.name for tool in loaded} == before | {"execute_skill_script", "lookup_story_fact", "save_story_fact"}
            script = next(tool for tool in loaded if tool.name == "execute_skill_script")
            assert script.tool_input_guardrails and script.tool_output_guardrails and callable(script.needs_approval)
            assert context.skill_script_snapshots["my_skill"] == {"capability_id": "my_skill", "skill_name": "my-skill", "version": "1.0.0", "content_hash": "hash-1"}
            assert _serialize_agent_context(context)["loaded_capabilities"]["my_skill"] == backend.detail["data"]
            assert "skill_tool_scope" not in _serialize_agent_context(context)
            await prepared.activate_skill(backend.detail["data"])
            assert len([event for event in events if event[0] == "connect"]) == 1
        assert context.skill_tool_scope is None
        connected = {server: task for event, server, task in events if event == "connect"}
        cleaned = {server: task for event, server, task in events if event == "cleanup"}
        assert connected and connected == cleaned
        assert all(task.done() for task in connected.values())
    asyncio.run(run())


@pytest.mark.parametrize("case", ["disabled-server", "missing-server", "unallowlisted-tool", "connection-failure", "missing-script-tool", "read-only"])
def test_installed_receipt_survives_unavailable_dependency_without_activating_it(tmp_path, monkeypatch, case):
    monkeypatch.setenv("TEST_STORY_MCP_STATE_FILE", str(tmp_path / "writes.jsonl"))

    async def run():
        backend = DynamicSkillBackend()
        dependencies = _selected_skill()[0]["skill"]["dependencies"]
        if case == "disabled-server": backend.catalog["mcp_servers"][0]["enabled"] = False
        elif case == "missing-server": dependencies = [{"type": "mcp", "value": "missing/tool"}]
        elif case == "unallowlisted-tool": dependencies = [{"type": "mcp", "value": "story-fixture/unregistered"}]
        elif case == "connection-failure": backend.catalog["mcp_servers"][0]["args"] = [str(tmp_path / "missing-server.py")]
        backend.require(scripts=case == "missing-script-tool", dependencies=[] if case == "missing-script-tool" else dependencies)
        context = context_for(backend)
        provider = AgentToolProvider(backend, [install_workspace_skill, load_skill_instructions])
        prepared = await provider.prepare(context, [], set(), read_only=case == "read-only")
        async with prepared:
            result = await install(backend, context)
            assert result["installed_as_skill"] and not result["ready_in_current_turn"]
            assert result["readiness_reason"] == "CAPABILITY_REFRESH_FAILED"
            assert context.capability_versions == {} and context.loaded_capabilities == {}
            assert context.allowed_skill_scripts == set() and prepared.mcp_servers == []
            assert len(backend.installs) == 1
        assert context.skill_tool_scope is None
    asyncio.run(run())


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
@pytest.mark.parametrize("changed", [False, True])
def test_approved_upgrade_can_retry_activation_without_reinstall_or_early_permissions(monkeypatch, mode, changed):
    async def run():
        backend = DynamicSkillBackend()
        old = deepcopy(backend.detail["data"])
        old["version"], old["skill"]["content_hash"] = "0.9.0", "old-hash"
        backend.require(scripts=True)
        context = context_for(backend)
        if mode == "background":
            context.agent_task_id = "background-task"
        elif mode == "stateful":
            context.execution_attempt_id = "stateful-attempt"
        context.routed_capabilities.add("my_skill")
        context.capability_versions["my_skill"] = "0.9.0"
        context.loaded_capabilities["my_skill"] = deepcopy(old)
        context.raw_request["capability_ref"] = {"capability_id": "my_skill", "version": "0.9.0"}
        prepared = await AgentToolProvider(backend, [install_workspace_skill, execute_skill_script, load_skill_instructions]).prepare(context, [old], {"my_skill"})
        activate, attempts = prepared.activate_skill, []

        async def fail_once(definition):
            attempts.append(definition["version"])
            if len(attempts) == 1:
                raise TimeoutError("deterministic activation failure")
            return await activate(definition)

        monkeypatch.setattr(prepared, "activate_skill", fail_once)
        async with prepared:
            arguments = {**ARGS, "installation_id": "install-1", "expected_active_version_id": "version-old"}
            raw = json.dumps(arguments)
            wrapper = ToolContext(context, tool_name="install_workspace_skill", tool_call_id="sdk-skill", tool_arguments=raw)
            result = json.loads(await install_workspace_skill.on_invoke_tool(wrapper, raw))
            assert result["installed_as_skill"] and not result["ready_in_current_turn"]
            assert context.capability_versions["my_skill"] == "0.9.0" and not context.allowed_skill_scripts
            assert prepared.pending_installed_ids == {"my_skill"}
            assert len(backend.installs) == 1 and attempts == ["1.0.0"]
            if changed:
                backend.detail["data"]["skill"]["content_hash"] = "unapproved-change"
                with pytest.raises(ValueError):
                    await _load_skill_instructions(context, "my_skill")
                assert context.capability_versions["my_skill"] == "0.9.0" and not context.allowed_skill_scripts
                assert attempts == ["1.0.0"]
            else:
                loaded = json.loads(await _load_skill_instructions(context, "my_skill"))
                assert loaded["version"] == "1.0.0" and context.capability_versions["my_skill"] == "1.0.0"
                assert context.allowed_skill_scripts and not prepared.pending_installed_ids
                assert attempts == ["1.0.0", "1.0.0"]
            assert len(backend.installs) == 1
    asyncio.run(run())


def test_widening_an_existing_mcp_dependency_keeps_the_original_connection_until_exit(tmp_path, monkeypatch):
    monkeypatch.setenv("TEST_STORY_MCP_STATE_FILE", str(tmp_path / "writes.jsonl"))

    async def run():
        backend = DynamicSkillBackend()
        first = deepcopy(backend.detail["data"])
        first["capability_id"] = "first"
        first["skill"]["dependencies"] = [{"type": "mcp", "value": "story-fixture/lookup_story_fact"}]
        context = context_for(backend)
        context.capability_versions["first"] = "1.0.0"
        context.routed_capabilities.add("first")
        provider = AgentToolProvider(backend, [install_workspace_skill, execute_skill_script])
        prepared = await provider.prepare(context, [first], {"first"})
        async with prepared:
            original = prepared.mcp_servers[0]
            assert {tool.name for tool in await original.list_tools()} == {"lookup_story_fact"}
            backend.require(dependencies=[{"type": "mcp", "value": "story-fixture/save_story_fact"}])
            result = await asyncio.create_task(install(backend, context))
            assert result["ready_in_current_turn"]
            assert prepared.mcp_servers[0] is not original
            assert {tool.name for tool in await prepared.mcp_servers[0].list_tools()} == {"lookup_story_fact", "save_story_fact"}
            assert original.session is not None
        assert original.session is None and prepared.mcp_servers[0].session is None
    asyncio.run(run())


def test_cancelled_dynamic_activation_cleans_connection_without_publishing_permissions(tmp_path, monkeypatch):
    monkeypatch.setenv("TEST_STORY_MCP_STATE_FILE", str(tmp_path / "writes.jsonl"))

    async def run():
        connected = asyncio.Event()
        servers = []
        original = AuditedMCPServerStdio.list_tools

        async def wait_after_listing(server, *args, **kwargs):
            result = await original(server, *args, **kwargs)
            servers.append(server)
            connected.set()
            await asyncio.Event().wait()
            return result

        monkeypatch.setattr(AuditedMCPServerStdio, "list_tools", wait_after_listing)
        backend = DynamicSkillBackend()
        backend.require(dependencies=_selected_skill()[0]["skill"]["dependencies"])
        context = context_for(backend)
        prepared = await AgentToolProvider(backend, [install_workspace_skill]).prepare(context, [], set())
        async with prepared:
            task = asyncio.create_task(install(backend, context))
            await asyncio.wait_for(connected.wait(), timeout=10)
            task.cancel()
            with pytest.raises(asyncio.CancelledError):
                await task
            assert len(backend.installs) == 1
            assert not context.loaded_capabilities and not context.routed_capabilities and not context.allowed_skill_scripts
            assert not prepared.mcp_servers and servers and all(server.session is None for server in servers)
        assert context.skill_tool_scope is None
        assert not (asyncio.all_tasks() - {asyncio.current_task()})

    asyncio.run(run())


def test_concurrent_skill_activation_preserves_both_dependency_sets(tmp_path, monkeypatch):
    monkeypatch.setenv("TEST_STORY_MCP_STATE_FILE", str(tmp_path / "writes.jsonl"))

    async def run():
        backend = DynamicSkillBackend()
        context = context_for(backend)
        prepared = await AgentToolProvider(backend, [install_workspace_skill]).prepare(context, [], set())
        definitions = []
        for index, dependency in enumerate(_selected_skill()[0]["skill"]["dependencies"]):
            definition = deepcopy(backend.detail["data"])
            definition["capability_id"] = f"skill_{index}"
            definition["skill"]["dependencies"] = [dependency]
            definitions.append(definition)
        async with prepared:
            await asyncio.gather(*(prepared.activate_skill(definition) for definition in definitions))
            assert prepared.selected_ids == context.routed_capabilities == {"skill_0", "skill_1"}
            assert context.loaded_capabilities == {definition["capability_id"]: definition for definition in definitions}
            assert {tool.name for tool in await prepared.mcp_servers[0].list_tools()} == {"lookup_story_fact", "save_story_fact"}
        assert not (asyncio.all_tasks() - {asyncio.current_task()})

    asyncio.run(run())
