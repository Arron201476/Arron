import asyncio
from copy import deepcopy
from dataclasses import replace
import json

import pytest

from content_agent_sidecar.contracts import AgentExecutionRequest, AgentToolApprovalDecision
from content_agent_sidecar.agent_tools import PreparedAgentTools
from content_agent_sidecar.runtime import OpenAIAgentsRuntime
from test_app import settings
from test_dynamic_skill_tools import DynamicSkillBackend
from test_agent_tools import _selected_skill
from test_project_skills import ARGS
from test_stateful_execution import StreamingSequence, tool_item


class MainAuthorBackend(DynamicSkillBackend):
    def __init__(self):
        super().__init__()
        self.detail["data"]["entry_policy"] = {"requires_user_confirmation": False, "auto_route": True}
        self.commits, self.failures, self.scripts, self.begins = [], [], [], []

    async def get_conversation_messages(self, conversation_id, limit=0):
        assert conversation_id == "c"
        return {"items": []}

    async def search_artifacts(self, project_id, **kwargs):
        assert project_id == "p"
        return {"items": []}

    async def get_capabilities(self, project_id):
        assert project_id == "p"
        return {"data": {"items": [deepcopy(self.detail["data"])] if self.installs else []}}

    async def get_agent_tool_catalog(self):
        catalog = await super().get_agent_tool_catalog()
        prototype = next(tool for tool in catalog["tools"] if tool["name"] == "load_skill_instructions")
        catalog["tools"].append({**prototype, "id": "runtime:commit_agent_action", "name": "commit_agent_action", "access": "write"})
        return catalog

    async def begin_agent_tool_call(self, **payload):
        self.begins.append(deepcopy(payload))
        assert payload["agent_turn_id"] == "t" and not payload["agent_task_attempt_id"]
        assert not payload.get("execution_attempt_id")
        call = await super().begin_agent_tool_call(**payload)
        if payload["tool_id"] in {"runtime:load_skill_instructions", "runtime:commit_agent_action"}:
            self.calls[payload["sdk_tool_call_id"]]["status"] = "approved"
            call["status"] = "approved"
        return call

    async def start_agent_tool_call(self, call_id, sdk_id, configuration_hash=""):
        if self.calls[sdk_id]["tool_id"].startswith("mcp:"):
            assert configuration_hash == "sha256:fixture-service-configuration"
        return await super().start_agent_tool_call(call_id, sdk_id)

    async def fail_agent_tool_call(self, call_id, **payload):
        self.failures.append((call_id, payload))
        return {"status": "failed"}

    async def execute_skill_script(self, call_id, sdk_id, arguments):
        self.scripts.append(deepcopy(arguments))
        return {"stdout": "approved script result", "exit_code": 0}

    async def commit_agent_turn(self, project_id, conversation_id, raw_request, decision, idempotency_key, agent_turn_id):
        assert (project_id, conversation_id) == ("p", "c")
        assert (idempotency_key, agent_turn_id) == ("i", "t")
        self.commits.append(deepcopy(decision))
        return {"data": {"reply": decision["reply"]}}


@pytest.mark.parametrize("decision", ["approve", "reject"])
def test_main_runner_discovers_unrouted_skill_and_resumes_its_native_approval(tmp_path, monkeypatch, decision):
    state_file = tmp_path / "discovered-mcp.jsonl"
    monkeypatch.setenv("TEST_STORY_MCP_STATE_FILE", str(state_file))

    class DiscoveryBackend(MainAuthorBackend):
        async def get_capabilities(self, project_id):
            assert project_id == "p"
            return {"data": {"items": [deepcopy(self.detail["data"])]}}

    backend = DiscoveryBackend()
    backend.require(dependencies=_selected_skill()[0]["skill"]["dependencies"])
    arguments = {"key": "discovered", "value": "approved"}
    model = StreamingSequence([
        [tool_item("load_skill_instructions", {"capability_id": "my_skill"}, "load")],
        [tool_item("mcp_story-fixture__save_story_fact", arguments, "use")],
        [tool_item("commit_agent_action", {"decision": {"intent": "chat", "reply": decision, "confidence": 1}}, "commit")],
    ])
    config = replace(settings(), backend_base_url="http://127.0.0.1:1", model_base_url="http://127.0.0.1:1", session_db_path=str(tmp_path / "discovery-session.sqlite3"))

    async def run():
        request = AgentExecutionRequest(project_id="p", conversation_id="c", agent_turn_id="t", idempotency_key="i", request={"content": "Use an appropriate local Skill to save the research fact"})

        async def execute():
            runtime = OpenAIAgentsRuntime(config, backend)
            runtime._execution_agent.model = model
            async def no_preselection(*_args): return set()
            runtime._route_skills = no_preselection
            try: return [event async for event in runtime.start_execution_stream(request).events()]
            finally: await runtime._model_client.close()

        first = await execute()
        assert first[-1]["event"] == "agent.turn.waiting_approval", first
        assert not backend.installs and not state_file.exists()
        assert "mcp_story-fixture__save_story_fact" not in model.tools[0]
        assert "mcp_story-fixture__save_story_fact" in model.tools[1]
        assert "Report narrative gaps." in json.dumps(model.inputs[1])
        checkpoint = first[-1]["data"]
        backend.calls["use"]["status"] = "approved" if decision == "approve" else "rejected"
        request = request.model_copy(update={"run_state": checkpoint["run_state"], "approval_decisions": [AgentToolApprovalDecision(sdk_tool_call_id="use", action=decision)]})
        final = await execute()
        assert final[-1]["event"] == "agent.turn.committed", final
        assert not backend.installs and not backend.failures and len(backend.commits) == 1
        assert len(model.inputs) == 3 and not model.responses
        assert state_file.exists() == (decision == "approve")
        if decision == "approve":
            assert [json.loads(line) for line in state_file.read_text(encoding="utf-8").splitlines()] == [arguments]
    asyncio.run(run())


@pytest.mark.parametrize("dependency,decision,fail_connect", [
    ("mcp", "approve", False), ("script", "approve", False),
    ("mcp", "reject", False), ("script", "reject", False),
    ("mcp", "approve", True), ("mcp", "reject", True),
], ids=["approve-mcp", "approve-script", "reject-mcp", "reject-script", "retry-approve-mcp", "retry-reject-mcp"])
def test_main_runtime_activates_new_dependency_and_restores_second_approval(tmp_path, monkeypatch, dependency, decision, fail_connect):
    state_file = tmp_path / "mcp-writes.jsonl"
    monkeypatch.setenv("TEST_STORY_MCP_STATE_FILE", str(state_file))
    backend = MainAuthorBackend()
    backend.require(scripts=dependency == "script", dependencies=_selected_skill()[0]["skill"]["dependencies"] if dependency == "mcp" else [])
    activation_failures = []
    if fail_connect:
        original_activate = PreparedAgentTools.activate_skill

        async def fail_first_activation(scope, definition):
            if not activation_failures:
                activation_failures.append(definition["version"])
                assert not scope.selected_ids and not scope.mcp_servers
                raise TimeoutError("deterministic fixture connection failure")
            return await original_activate(scope, definition)

        monkeypatch.setattr(PreparedAgentTools, "activate_skill", fail_first_activation)
    name = "execute_skill_script" if dependency == "script" else "mcp_story-fixture__save_story_fact"
    arguments = {"skill_name": "my-skill", "script_id": "review", "input_json": "{}"} if dependency == "script" else {"key": "review", "value": "approved"}
    reply = "Tool executed" if decision == "approve" else "Tool rejected; not executed"
    model = StreamingSequence([
        [tool_item("install_workspace_skill", ARGS, "install")],
        [tool_item("load_skill_instructions", {"capability_id": "my_skill"}, "load")],
        [tool_item(name, arguments, "use")],
        [tool_item("commit_agent_action", {"decision": {"intent": "chat", "reply": reply, "confidence": 1}}, "commit")],
    ])
    config = replace(settings(), backend_base_url="http://127.0.0.1:1", model_base_url="http://127.0.0.1:1",
                     session_db_path=str(tmp_path / "sdk-session.sqlite3"))

    async def run():
        request = AgentExecutionRequest(project_id="p", conversation_id="c", agent_turn_id="t", idempotency_key="i", request={"content": "Create and install a Skill, then use it"})

        async def execute():
            runtime = OpenAIAgentsRuntime(config, backend)
            runtime._execution_agent.model = model
            try:
                return [event async for event in runtime.start_execution_stream(request).events()]
            finally:
                await runtime._model_client.close()

        first = await execute()
        assert first[-1]["event"] == "agent.turn.waiting_approval", first
        assert not backend.installs and name not in model.tools[0]
        checkpoint = first[-1]["data"]
        backend.calls["install"]["status"] = "approved"
        request = request.model_copy(update={"run_state": checkpoint["run_state"], "approval_decisions": [AgentToolApprovalDecision(sdk_tool_call_id="install", action="approve")]})
        second = await execute()
        assert second[-1]["event"] == "agent.turn.waiting_approval", second
        assert len(backend.installs) == 1 and not backend.scripts and not state_file.exists()
        assert (name in model.tools[1]) != fail_connect
        assert name in model.tools[2] and "Report narrative gaps." in json.dumps(model.inputs[2])
        if fail_connect:
            assert activation_failures == ["1.0.0"]
            assert "CAPABILITY_REFRESH_FAILED" in json.dumps(model.inputs[1])
        checkpoint = second[-1]["data"]
        assert checkpoint["pending_sdk_tool_call_ids"] == ["use"]
        backend.calls["use"]["status"] = "approved" if decision == "approve" else "rejected"
        request = request.model_copy(update={"run_state": checkpoint["run_state"], "approval_decisions": [AgentToolApprovalDecision(sdk_tool_call_id="use", action=decision)]})
        third = await execute()
        assert third[-1]["event"] == "agent.turn.committed", third
        assert not backend.failures and len(backend.installs) == len(backend.commits) == 1
        assert backend.commits[0]["reply"] == reply and not model.responses and len(model.inputs) == 4
        if dependency == "mcp":
            assert state_file.exists() == (decision == "approve")
            if decision == "approve":
                assert [json.loads(line) for line in state_file.read_text(encoding="utf-8").splitlines()] == [arguments]
        else:
            assert backend.scripts == ([arguments] if decision == "approve" else [])
            if decision == "approve":
                assert next(call for call in reversed(backend.begins) if call["tool_id"] == "runtime:execute_skill_script")["skill_snapshot"] == {"capability_id": "my_skill", "version": "1.0.0", "content_hash": "hash-1"}

    asyncio.run(run())
