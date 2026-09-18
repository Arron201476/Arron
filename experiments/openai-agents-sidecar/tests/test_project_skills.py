from __future__ import annotations

import asyncio
import copy
import json
from dataclasses import replace
from urllib.parse import parse_qs, urlsplit

import pytest
from agents import Agent, Runner, RunState, function_tool
from agents.items import ModelResponse
from agents.tool import set_function_tool_failure_error_function
from agents.tool_context import ToolContext
from agents.usage import Usage
from openai.types.responses import ResponseFunctionToolCall

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.background_worker import SDKBackgroundTaskWorker
from content_agent_sidecar.contracts import AgentExecutionRequest, AgentToolApprovalDecision
from content_agent_sidecar.observability import TurnObservation
from content_agent_sidecar.project_skills import install_workspace_skill, validate_workspace_skill
from content_agent_sidecar.runtime import AgentContext, _apply_approval_decisions, _load_skill_instructions, _restore_agent_context, _serialize_agent_context, _serialize_paused_run, load_skill_instructions
from test_run_state_approval import DurableApprovalBackend, SequenceModel, _text_response


ARGS = {"root_path": "draft/my-skill", "snapshot_hash": "snapshot-1", "scope": "project", "installation_id": "", "expected_active_version_id": ""}


class SkillBackend(DurableApprovalBackend):
    def __init__(self):
        super().__init__()
        self.installs = []
        self.saved = {
            "receipt": {"receipt_id": "receipt-1", "project_id": "p", "skill_installation_id": "install-1", "skill_version_id": "version-1"},
            "installation": {"skill_installation_id": "install-1", "capability_id": "my_skill", "skill_name": "my-skill", "scope": "project", "scope_ref": "p", "enabled": True, "registry_status": "available", "active_version_id": "version-1",
                "versions": [{"skill_version_id": "version-1", "version": "1.0.0", "content_hash": "hash-1", "execution_mode": "inline", "manifest": {"dependencies": [], "scripts": []}}]},
        }
        self.detail = {"data": {"status": "available", "capability_id": "my_skill", "version": "1.0.0", "execution_mode": "inline", "skill": {"instructions": "Report narrative gaps.", "content_hash": "hash-1", "scope": "project"}}}
        self.preview = {"project_id": "p", "root_path": ARGS["root_path"], "snapshot_hash": "snapshot-1", "status": "valid", "files": []}

    async def get_agent_tool_catalog(self):
        catalog = await super().get_agent_tool_catalog()
        catalog["tools"][0].update(id="runtime:install_workspace_skill", name="install_workspace_skill", timeout_seconds=120)
        return catalog

    async def preview_workspace_skill(self, project_id, root):
        assert project_id == "p"
        return copy.deepcopy(self.preview)

    async def install_workspace_skill(self, call_id, sdk_id, arguments):
        self.installs.append((call_id, sdk_id, arguments))
        return copy.deepcopy(self.saved)

    async def get_capability(self, capability_id, project_id, version):
        assert (capability_id, project_id, version) == ("my_skill", "p", "1.0.0")
        if isinstance(self.detail, Exception):
            raise self.detail
        return self.detail


def context_for(backend, audited=True):
    return AgentContext("p", "c", backend, raw_request={"content": "Create and install a Skill"}, agent_turn_id="t", idempotency_key="i",
        active_tool_calls={"sdk-skill": {"agent_tool_call_id": "tcall-durable"}} if audited else {})


def invoke(tool, context, arguments):
    raw = json.dumps(arguments)
    wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="sdk-skill", tool_arguments=raw)
    unhandled = set_function_tool_failure_error_function(replace(tool), None)
    return json.loads(asyncio.run(unhandled.on_invoke_tool(wrapper, raw)))


def test_validation_is_not_installation_and_exports_exact_snapshot():
    backend = SkillBackend()
    result = invoke(validate_workspace_skill, context_for(backend), {"root_path": ARGS["root_path"]})
    assert not result["installed_as_skill"] and not backend.installs
    assert parse_qs(urlsplit(result["archive_url"]).query) == {"root_path": [ARGS["root_path"]], "snapshot_hash": ["snapshot-1"]}
    backend.preview["status"] = "invalid"
    assert "archive_url" not in invoke(validate_workspace_skill, context_for(backend), {"root_path": ARGS["root_path"]})
    backend.preview["project_id"] = "foreign"
    with pytest.raises(ValueError, match="project"):
        invoke(validate_workspace_skill, context_for(backend), {"root_path": ARGS["root_path"]})


@pytest.mark.parametrize("upgrade_selected", [False, True])
def test_install_receipt_makes_verified_inline_skill_loadable_in_current_turn(upgrade_selected):
    backend = SkillBackend()
    context = context_for(backend)
    if upgrade_selected:
        context.raw_request["capability_ref"] = {"capability_id": "my_skill", "version": "0.9.0"}
        context.capability_versions["my_skill"] = "0.9.0"
    result = invoke(install_workspace_skill, context, ARGS)
    assert result["installed_as_skill"] and result["ready_in_current_turn"]
    assert context.capability_versions == {"my_skill": "1.0.0"}
    loaded = json.loads(asyncio.run(_load_skill_instructions(context, "my_skill")))
    assert loaded["skill"]["instructions"] == "Report narrative gaps."
    assert backend.installs == [("tcall-durable", "sdk-skill", ARGS)]


@pytest.mark.parametrize("case", ["disabled", "inactive-version", "scripts", "dependencies", "background", "shadowed", "hash-mismatch", "refresh-error", "visible-mode-mismatch", "visible-mode-missing"])
def test_install_does_not_claim_current_turn_readiness_without_verified_tools(case):
    backend = SkillBackend()
    installed = backend.saved["installation"]
    version = installed["versions"][0]
    if case == "disabled": installed["enabled"] = False
    elif case == "inactive-version": installed["active_version_id"] = "later-version"
    elif case in {"scripts", "dependencies"}: version["manifest"][case] = [{"path": "scripts/review.py"}]
    elif case == "background": version["execution_mode"] = "background_task"
    elif case == "shadowed": backend.detail["data"]["skill"]["scope"] = "user"
    elif case == "hash-mismatch": backend.detail["data"]["skill"]["content_hash"] = "different"
    elif case == "refresh-error": backend.detail = RuntimeError("offline")
    elif case == "visible-mode-mismatch": backend.detail["data"]["execution_mode"] = "background_task"
    elif case == "visible-mode-missing": backend.detail["data"].pop("execution_mode")
    context = context_for(backend)
    result = invoke(install_workspace_skill, context, ARGS)
    assert result["installed_as_skill"] and not result["ready_in_current_turn"]
    assert not context.routed_capabilities


@pytest.mark.parametrize("case", ["no-audit", "wrong-project", "wrong-scope", "wrong-ref", "no-receipt", "missing-version"])
def test_invalid_install_receipt_or_missing_audit_is_not_success(case):
    backend = SkillBackend()
    if case == "wrong-project": backend.saved["receipt"]["project_id"] = "foreign"
    elif case == "wrong-scope": backend.saved["installation"]["scope"] = "user"
    elif case == "wrong-ref": backend.saved["installation"]["scope_ref"] = "foreign"
    elif case == "no-receipt": backend.saved["receipt"].pop("receipt_id")
    elif case == "missing-version": backend.saved["installation"]["versions"] = []
    with pytest.raises(ValueError):
        invoke(install_workspace_skill, context_for(backend, audited=case != "no-audit"), ARGS)
    if case == "no-audit": assert not backend.installs


def test_read_only_and_fallback_catalog_enforce_install_approval():
    class NoCatalog(SkillBackend):
        get_agent_tool_catalog = None
    backend = NoCatalog()
    provider = AgentToolProvider(backend, [validate_workspace_skill, install_workspace_skill])
    context = context_for(backend)
    prepared = asyncio.run(provider.prepare(context, [], set(), read_only=True))
    assert {tool.name for tool in prepared.tools} == {"validate_workspace_skill"}
    install = provider._fallback_catalog.descriptors["runtime:install_workspace_skill"]
    assert install.approval == "always" and install.access == "write"


@pytest.mark.parametrize("loaded_before_pause", [False, True])
def test_background_installed_inline_skill_survives_later_approval_restore(loaded_before_pause):
    backend = SkillBackend()

    def task_context():
        context = context_for(backend)
        context.agent_turn_id = ""
        context.agent_task_id, context.agent_task_attempt_id, context.skill_invocation_id = "task", "attempt", "invocation"
        context.background_output_mode = "structured"
        context.capability_versions = {"primary_task": "0.1.0"}
        context.routed_capabilities = {"primary_task"}
        context.loaded_capabilities = {"primary_task": {"capability_id": "primary_task", "version": "0.1.0", "execution_mode": "background_task", "skill": {"instructions": "Original task instructions"}}}
        return context

    original = task_context()
    installed = invoke(install_workspace_skill, original, ARGS)
    assert installed["ready_in_current_turn"]
    if loaded_before_pause:
        asyncio.run(_load_skill_instructions(original, "my_skill"))
    checkpoint = {"run_state": {"context": {"context": _serialize_agent_context(original)}}}
    resumed = task_context()
    asyncio.run(SDKBackgroundTaskWorker._restore_context(resumed, checkpoint))
    assert resumed.capability_versions == {"primary_task": "0.1.0", "my_skill": "1.0.0"}
    assert resumed.loaded_capabilities["my_skill"]["skill"]["instructions"] == "Report narrative gaps."
    assert resumed.loaded_capabilities["primary_task"]["skill"]["instructions"] == "Original task instructions"


@pytest.mark.parametrize("case", ["identity", "primary-version", "metadata", "unavailable", "new-dependencies"])
def test_background_restore_rejects_unverified_or_changed_skills(case):
    backend = SkillBackend()
    context = context_for(backend)
    context.agent_turn_id = ""
    context.agent_task_id, context.agent_task_attempt_id, context.skill_invocation_id = "task", "attempt", "invocation"
    context.background_output_mode = "structured"
    context.capability_versions = {"primary_task": "0.1.0"}
    context.loaded_capabilities = {"primary_task": {"capability_id": "primary_task", "version": "0.1.0"}}
    context.routed_capabilities = {"primary_task"}
    payload = copy.deepcopy(_serialize_agent_context(context))
    payload["routed_capabilities"].append("my_skill")
    payload["capability_versions"]["my_skill"] = "1.0.0"
    if case == "identity": payload["agent_task_attempt_id"] = "another-attempt"
    elif case == "primary-version": payload["capability_versions"]["primary_task"] = "new-version"
    elif case == "metadata": payload["loaded_capabilities"]["primary_task"]["version"] = "new-version"
    elif case == "unavailable": backend.detail["data"]["status"] = "unavailable"
    elif case == "new-dependencies": backend.detail["data"]["skill"]["dependencies"] = [{"type": "mcp", "value": "new-server"}]
    with pytest.raises(ValueError):
        asyncio.run(SDKBackgroundTaskWorker._restore_context(context, {"run_state": {"context": {"context": payload}}}))


@pytest.mark.parametrize("mode", ["background_task", "stateful_workflow"])
def test_author_cannot_replace_its_own_primary_execution_version(mode):
    backend = SkillBackend()
    context = context_for(backend)
    if mode == "background_task": context.agent_task_id = "task"
    else: context.execution_attempt_id = "attempt"
    context.capability_versions["my_skill"] = "0.9.0"
    context.loaded_capabilities["my_skill"] = {"execution_mode": mode}
    result = invoke(install_workspace_skill, context, ARGS)
    assert result["installed_as_skill"] and not result["ready_in_current_turn"]
    assert context.capability_versions["my_skill"] == "0.9.0"


@pytest.mark.parametrize("dependency", ["none", "mcp", "script"])
def test_background_author_install_load_and_second_approval_survive_two_worker_restarts(tmp_path, monkeypatch, dependency):
    from pathlib import Path
    from test_app import settings
    from test_agent_tools import _mcp_catalog, _selected_skill
    from test_background_worker import FakeBackend
    from test_sdk_guardrails import message
    from content_agent_sidecar.runtime import execute_skill_script

    writes = []
    tool_inventories = []
    state_file = tmp_path / "background-writes.jsonl"
    monkeypatch.setenv("TEST_STORY_MCP_STATE_FILE", str(state_file))

    @function_tool
    async def second_confirmation(value: str) -> str:
        """Persist the separately approved value."""
        writes.append(value)
        return "saved"

    class DurableSkillTaskBackend(SkillBackend, FakeBackend):
        def __init__(self):
            SkillBackend.__init__(self)
            FakeBackend.__init__(self)
            self.checkpoint = None
            self.tool_completions = 0
            self.tool_failures = []
            manifest = self.saved["installation"]["versions"][0]["manifest"]
            if dependency == "mcp": manifest["dependencies"] = _selected_skill()[0]["skill"]["dependencies"]
            if dependency == "script": manifest["scripts"] = [{"id": "review", "path": "scripts/review.py", "runtime": "python"}]
            self.detail["data"]["skill"].update(copy.deepcopy(manifest), name="my-skill")

        async def claim_agent_task(self, **kwargs):
            self.claimed = False
            claim = await FakeBackend.claim_agent_task(self, **kwargs)
            claim["task"].update(project_id="p", conversation_id="c")
            if self.checkpoint:
                claim["resume"] = {"run_state": self.checkpoint["run_state"], "approval_decisions": [{"sdk_tool_call_id": self.checkpoint["pending_sdk_tool_call_ids"][0], "action": "approve"}]}
                claim["attempt_token"] = "renewed-attempt-token"
            return claim

        async def pause_agent_task_for_approval(self, claim, **kwargs):
            self.checkpoint = copy.deepcopy(kwargs)
            self.calls[kwargs["pending_sdk_tool_call_ids"][0]]["status"] = "approved"
            return {}

        async def get_agent_tool_catalog(self):
            catalog = await SkillBackend.get_agent_tool_catalog(self)
            prototype = catalog["tools"][0]
            catalog["tools"].extend([
                {**prototype, "id": "runtime:second_confirmation", "name": "second_confirmation"},
                {**prototype, "id": "runtime:load_skill_instructions", "name": "load_skill_instructions", "access": "read", "approval": "never", "timeout_seconds": 30},
                {**prototype, "id": "runtime:execute_skill_script", "name": "execute_skill_script", "access": "sensitive"},
            ])
            if dependency == "mcp":
                mcp = _mcp_catalog(Path(__file__).parent / "fixtures" / "story_mcp_server.py")
                catalog["tools"].extend(mcp["tools"])
                catalog["mcp_servers"] = mcp["mcp_servers"]
            return catalog

        async def execute_skill_script(self, call_id, sdk_id, arguments):
            writes.append("after authoring")
            assert arguments == {"skill_name": "my-skill", "script_id": "review", "input_json": "{}"}
            return {"stdout": "script fixture receipt", "exit_code": 0}

        async def get_capability(self, capability_id, project_id, version):
            if capability_id == "my_skill":
                return await SkillBackend.get_capability(self, capability_id, project_id, version)
            return await FakeBackend.get_capability(self, capability_id, project_id, version)

        async def begin_agent_tool_call(self, **payload):
            call = await SkillBackend.begin_agent_tool_call(self, **payload)
            if payload["tool_id"] == "runtime:load_skill_instructions":
                self.calls[payload["sdk_tool_call_id"]]["status"] = "approved"
                call["status"] = "approved"
            return call

        async def complete_agent_tool_call(self, agent_tool_call_id, **payload):
            self.tool_completions += 1
            return {"agent_tool_call_id": agent_tool_call_id, "status": "completed"}

        async def start_agent_tool_call(self, call_id, sdk_id, **kwargs):
            if sdk_id == "sdk-second" and dependency == "mcp":
                assert kwargs == {"configuration_hash": "sha256:fixture-service-configuration"}
            else:
                assert not kwargs
            return await SkillBackend.start_agent_tool_call(self, call_id, sdk_id)

        async def fail_agent_tool_call(self, call_id, **payload):
            self.tool_failures.append((call_id, payload))
            return {"agent_tool_call_id": call_id, "status": "failed"}

    def tool_response(name, call_id, arguments):
        return ModelResponse(output=[ResponseFunctionToolCall(id=None, call_id=call_id, arguments=json.dumps(arguments), name=name, type="function_call", status="completed")], usage=Usage(requests=1), response_id=f"response-{call_id}")

    backend = DurableSkillTaskBackend()

    from test_stateful_execution import StreamingSequence

    class InventoryModel(StreamingSequence):
        async def stream_response(self, *args, **kwargs):
            tools = kwargs.get("tools", args[3] if len(args) > 3 else [])
            tool_inventories.append({tool.name for tool in tools})
            async for event in super().stream_response(*args, **kwargs):
                yield event

    def worker(responses):
        instance = SDKBackgroundTaskWorker(settings(), backend)
        instance._model = InventoryModel([response.output for response in responses])
        instance._tool_provider = AgentToolProvider(backend, [install_workspace_skill, load_skill_instructions, execute_skill_script, second_confirmation])
        return instance

    assert asyncio.run(worker([tool_response("install_workspace_skill", "sdk-install", ARGS)]).run_once())
    assert backend.failed is None and backend.checkpoint is not None and not backend.installs
    assert "execute_skill_script" not in tool_inventories[0] and "save_story_fact" not in tool_inventories[0]
    name, arguments = "second_confirmation", {"value": "after authoring"}
    if dependency == "mcp": name, arguments = "save_story_fact", {"key": "review", "value": "after authoring"}
    if dependency == "script": name, arguments = "execute_skill_script", {"skill_name": "my-skill", "script_id": "review", "input_json": "{}"}
    assert asyncio.run(worker([tool_response("load_skill_instructions", "sdk-load", {"capability_id": "my_skill"}), tool_response(name, "sdk-second", arguments)]).run_once())
    assert backend.failed is None and len(backend.installs) == 1 and not writes
    assert not state_file.exists() and name in tool_inventories[1]
    checkpoint_context = backend.checkpoint["run_state"]["context"]["context"]
    assert checkpoint_context["loaded_capabilities"]["my_skill"]["skill"]["instructions"] == "Report narrative gaps."
    assert checkpoint_context["capability_versions"]["story_research_digest"] == "1.0.0"
    assert asyncio.run(worker([message(json.dumps({"summary": "Finished", "result": {}, "artifact_draft": None}))]).run_once())
    assert backend.failed is None and not backend.tool_failures and len(backend.installs) == 1
    if dependency == "mcp": assert [json.loads(line) for line in state_file.read_text(encoding="utf-8").splitlines()] == [arguments]
    else: assert writes == ["after authoring"]
    assert len(tool_inventories) == 4
    assert backend.completed["result"]["summary"] == "Finished"


@pytest.mark.parametrize("decision", ["approve", "reject"])
def test_sdk_approval_checkpoint_restore_installs_only_after_approval(decision):
    backend = SkillBackend()

    async def run():
        provider = AgentToolProvider(backend, [install_workspace_skill])
        initial_context = context_for(backend, audited=False)
        prepared = await provider.prepare(initial_context, [], set())
        model = SequenceModel([ModelResponse(output=[ResponseFunctionToolCall(id=None, call_id="sdk-skill", arguments=json.dumps(ARGS), name="install_workspace_skill", type="function_call", status="completed")], usage=Usage(requests=1), response_id="resp-install"), _text_response()])
        async with prepared:
            agent = Agent(name="Skill installer", model=model, tools=prepared.tools)
            paused = await Runner.run(agent, "Install this Skill", context=initial_context, max_turns=3)
            assert len(paused.interruptions) == 1 and not backend.installs
            state_json, _, pending_ids = _serialize_paused_run(paused)
            assert pending_ids == ["sdk-skill"]
        backend.calls["sdk-skill"]["status"] = "approved" if decision == "approve" else "rejected"
        request = AgentExecutionRequest(project_id="p", conversation_id="c", agent_turn_id="t", idempotency_key="i", request=initial_context.raw_request, run_state=state_json,
            approval_decisions=[AgentToolApprovalDecision(sdk_tool_call_id="sdk-skill", action=decision)])
        context = _restore_agent_context(state_json, request, backend, TurnObservation("fixture", "sequence", "local"))
        prepared = await provider.prepare(context, [], set())
        async with prepared:
            resumed_agent = Agent(name="Skill installer", model=model, tools=prepared.tools)
            state = await RunState.from_json(resumed_agent, state_json, context_override=context, strict_context=True)
            _apply_approval_decisions(state, request.approval_decisions)
            result = await Runner.run(resumed_agent, state, max_turns=3)
        assert result.final_output == "continued after the decision"
        assert len(backend.installs) == (1 if decision == "approve" else 0)
        if decision == "approve":
            assert context.routed_capabilities == {"my_skill"}
            assert backend.started == backend.completed == 1
        else:
            assert backend.started == backend.completed == 0

    asyncio.run(run())
