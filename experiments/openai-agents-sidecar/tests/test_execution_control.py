import asyncio
from copy import deepcopy
from dataclasses import replace
import json

import pytest
from agents.tool import set_function_tool_failure_error_function
from agents.tool_context import ToolContext

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.contracts import AgentExecutionRequest, AgentToolApprovalDecision
from content_agent_sidecar.execution_control import control_execution, inspect_execution_controls, list_execution_targets
from content_agent_sidecar.runtime import OpenAIAgentsRuntime
from test_app import settings
from test_main_dynamic_skill_tools import MainAuthorBackend
from test_project_skills import context_for
from test_stateful_execution import StreamingSequence, tool_item


class ControlBackend(MainAuthorBackend):
    def __init__(self, action="pause_run"):
        super().__init__()
        self.arguments = {"target_type": "agent_task" if "agent_task" in action else "run", "target_id": "target-1", "action_id": action, "snapshot_hash": "a" * 64}
        self.snapshot = {"project_id": "p", "target_type": self.arguments["target_type"], "target_id": "target-1", "snapshot_hash": "a" * 64,
                         "status": "running", "available_actions": [{"action_id": action, "enabled": True}]}
        self.control_requests = []

    async def get_agent_tool_catalog(self):
        catalog = await super().get_agent_tool_catalog()
        prototype = next(tool for tool in catalog["tools"] if tool["name"] == "load_skill_instructions")
        catalog["tools"].extend({**prototype, "name": name, "id": "runtime:" + name} for name in ["list_execution_targets", "inspect_execution_controls"])
        catalog["tools"].append({**prototype, "name": "control_execution", "id": "runtime:control_execution", "access": "write", "approval": "always"})
        return catalog

    async def begin_agent_tool_call(self, **payload):
        call = await super().begin_agent_tool_call(**payload)
        if payload["tool_id"] in {"runtime:list_execution_targets", "runtime:inspect_execution_controls"}:
            self.calls[payload["sdk_tool_call_id"]]["status"] = "approved"
            call["status"] = "approved"
        return call

    async def list_execution_targets(self, project_id, target_type, after_id):
        assert (project_id, target_type, after_id) == ("p", self.arguments["target_type"], "")
        return {"items": [deepcopy(self.snapshot)], "next_after_id": ""}

    async def inspect_execution_controls(self, project_id, target_type, target_id):
        assert (project_id, target_type, target_id) == ("p", self.arguments["target_type"], "target-1")
        return deepcopy(self.snapshot)

    async def control_execution(self, call_id, sdk_id, arguments):
        assert arguments == self.arguments and sdk_id == "control"
        self.control_requests.append((call_id, sdk_id, deepcopy(arguments)))
        statuses = {"pause_run": "pausing", "resume_run": "running", "cancel_run": "cancelled", "retry_failed_step": "running", "pause_agent_task": "pausing", "resume_agent_task": "queued", "cancel_agent_task": "cancelled", "retry_agent_task": "queued"}
        return {"project_id": "p", **arguments, "status": statuses[arguments["action_id"]]}


@pytest.mark.parametrize("action", ["pause_run", "resume_run", "cancel_run", "retry_failed_step", "pause_agent_task", "resume_agent_task", "cancel_agent_task", "retry_agent_task"])
@pytest.mark.parametrize("decision", ["approve", "reject"])
def test_actual_main_runtime_controls_exact_target_after_native_approval_and_rebuild(tmp_path, action, decision):
    backend = ControlBackend(action)
    arguments = backend.arguments
    model = StreamingSequence([
        [tool_item("list_execution_targets", {"target_type": arguments["target_type"], "after_id": ""}, "list")],
        [tool_item("inspect_execution_controls", {"target_type": arguments["target_type"], "target_id": "target-1"}, "inspect")],
        [tool_item("control_execution", arguments, "control")],
        [tool_item("commit_agent_action", {"decision": {"intent": "chat", "reply": "Control request processed" if decision == "approve" else "Control not executed", "confidence": 1}}, "commit")],
    ])
    config = replace(settings(), backend_base_url="http://127.0.0.1:1", model_base_url="http://127.0.0.1:1", session_db_path=str(tmp_path / "session.db"))

    async def run():
        request = AgentExecutionRequest(project_id="p", conversation_id="c", agent_turn_id="t", idempotency_key="i", request={"content": "Control the requested task"})

        async def execute():
            runtime = OpenAIAgentsRuntime(config, backend)
            runtime._execution_agent.model = model
            try:
                return [event async for event in runtime.start_execution_stream(request).events()]
            finally:
                await runtime._model_client.close()

        events = await execute()
        assert events[-1]["event"] == "agent.turn.waiting_approval", events
        assert not backend.control_requests and not backend.commits
        checkpoint = events[-1]["data"]
        assert checkpoint["pending_sdk_tool_call_ids"] == ["control"]
        backend.calls["control"]["status"] = "approved" if decision == "approve" else "rejected"
        request = request.model_copy(update={"run_state": checkpoint["run_state"], "approval_decisions": [AgentToolApprovalDecision(sdk_tool_call_id="control", action=decision)]})
        resumed = await execute()
        assert resumed[-1]["event"] == "agent.turn.committed", resumed
        assert not backend.failures and len(backend.commits) == 1 and len(model.inputs) == 4 and not model.responses
        assert len(backend.control_requests) == (1 if decision == "approve" else 0)
        if decision == "approve":
            assert backend.control_requests[0][2] == arguments
            outputs = [json.loads(item["output"]) for item in model.inputs[-1] if item.get("type") == "function_call_output" and item.get("call_id") == "control"]
            assert len(outputs) == 1 and outputs[0]["action_id"] == action and outputs[0]["target_id"] == "target-1"
            assert outputs[0]["status"] in {"pausing", "running", "cancelled", "queued"}

    asyncio.run(run())


def test_control_tool_fallback_cannot_bypass_approval_or_read_only_scope():
    class NoCatalog(ControlBackend):
        get_agent_tool_catalog = None

    backend = NoCatalog()
    provider = AgentToolProvider(backend, [list_execution_targets, inspect_execution_controls, control_execution])
    descriptor = provider._fallback_catalog.descriptors["runtime:control_execution"]
    assert descriptor.access == "write" and descriptor.approval == "always"
    prepared = asyncio.run(provider.prepare(context_for(backend), [], set(), read_only=True))
    assert {tool.name for tool in prepared.tools} == {"list_execution_targets", "inspect_execution_controls"}


@pytest.mark.parametrize("field", ["project_id", "target_type", "target_id", "snapshot_hash"])
def test_control_inspection_rejects_mismatched_or_missing_snapshot(field):
    backend = ControlBackend()
    backend.snapshot[field] = "" if field == "snapshot_hash" else "foreign"
    context = context_for(backend)
    tool = set_function_tool_failure_error_function(replace(inspect_execution_controls), None)
    raw = json.dumps({"target_type": "run", "target_id": "target-1"})
    wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="inspect", tool_arguments=raw)
    with pytest.raises(ValueError, match="snapshot"):
        asyncio.run(tool.on_invoke_tool(wrapper, raw))
    assert not backend.control_requests
