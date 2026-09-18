import asyncio
from copy import deepcopy
from dataclasses import replace
import hashlib
import json
from pathlib import Path

import pytest
from agents import ModelBehaviorError
from openai.types.responses import Response, ResponseCompletedEvent, ResponseFunctionToolCall, ResponseOutputMessage, ResponseOutputText, ResponseUsage

from content_agent_sidecar.backend import BackendError, _activity_headers
from content_agent_sidecar.execution_tools import skill_execution_tools
from content_agent_sidecar.stateful_execution import StatefulExecution
from content_agent_sidecar.task_worker import SDKTaskWorker
from test_app import settings
from test_project_skills import SkillBackend, ARGS
from test_run_state_approval import SequenceModel


def tool_item(name, arguments, sdk_id):
    return ResponseFunctionToolCall(id="item-" + sdk_id, call_id=sdk_id, name=name, arguments=json.dumps(arguments), type="function_call")


def final_item(payload):
    return ResponseOutputMessage(id="final", type="message", role="assistant", status="completed",
        content=[ResponseOutputText(text=json.dumps(payload), annotations=[], type="output_text")])


class StreamingSequence(SequenceModel):
    def __init__(self, responses):
        super().__init__(responses)
        self.inputs, self.tools, self.schemas = [], [], []

    async def stream_response(self, *args, **kwargs):
        self.inputs.append(deepcopy(args[1]))
        self.tools.append([tool.name for tool in args[3]])
        self.schemas.append(args[4])
        if not self.responses:
            raise AssertionError("model received an unexpected extra request")
        output = self.responses.pop(0)
        if isinstance(output, BaseException):
            raise output
        response = Response.model_construct(id=f"response-{len(self.inputs)}", created_at=0, model="fixture", object="response",
            status="completed", output=output, tools=[], tool_choice="auto", parallel_tool_calls=True,
            usage=ResponseUsage(input_tokens=10, output_tokens=2, total_tokens=12,
                input_tokens_details={"cached_tokens": 0, "cache_write_tokens": 0}, output_tokens_details={"reasoning_tokens": 0}))
        yield ResponseCompletedEvent(type="response.completed", sequence_number=1, response=response)


def claim_fixture():
    attempt = {"attempt_id": "attempt", "run_id": "run", "step_run_id": "step", "task_item_id": "task", "executor_id": "worker.structured_content", "input_snapshot_hash": "input-hash"}
    return {"attempt": attempt, "task": {key: attempt[key] for key in ("run_id", "step_run_id", "task_item_id")},
        "attempt_token": "original-private-token", "executor_id": attempt["executor_id"], "capability_id": "primary", "capability_version": "1.0.0",
        "context_pack": {"project_id": "p", "conversation_id": "c", "context_hash": "input-hash",
            "run": {"run_id": "run"}, "step": {"step_run_id": "step", "task_item_id": "task"},
            "capability": {"capability_id": "primary", "capability_version": "1.0.0"},
            "skill_instructions": {"content": "Execute the original workflow."}, "prompt": {"content": "Return the required result."},
            "output_contract": {"schema": {"type": "object", "required": ["title"], "properties": {"title": {"type": "string"}}}},
        }}


class WorkerBackend(SkillBackend):
    get_agent_tool_catalog = None

    def __init__(self, claim=None):
        super().__init__()
        self.claim = claim or claim_fixture()
        self.status = "running"
        self.primary = {"data": {"status": "available", "capability_id": "primary", "version": "1.0.0", "execution_mode": "stateful_workflow",
            "skill": {"instructions": "Execute the original workflow.", "content_hash": "primary-hash", "scope": "workspace"}}}
        self.checkpoints, self.submissions, self.commits, self.failures, self.begins, self.writes = [], [], [], [], [], []
        self.checkpoint_fault = ""
        self.input_receipts = []

    async def record_execution_inputs_included(self, attempt_id, attempt_token, input_hash, input_ids):
        assert attempt_id == self.claim["attempt"]["attempt_id"] and attempt_token == self.claim["attempt_token"]
        assert input_hash == self.claim["attempt"]["input_snapshot_hash"]
        self.input_receipts.append(list(input_ids))

    async def claim_execution_task(self, **kwargs):
        return self.claim

    async def heartbeat_execution_attempt(self, claim, **kwargs):
        if self.status == "rotated":
            raise BackendError("ATTEMPT_TOKEN_INVALID")
        return {"data": {**claim["attempt"], "status": self.status}}

    async def get_capability(self, capability_id, project_id, version):
        assert _activity_headers.get()["X-Agent-Execution-Attempt-ID"] == "attempt"
        assert project_id == "p" and version == "1.0.0"
        return deepcopy(self.primary if capability_id == "primary" else self.detail)

    async def begin_agent_tool_call(self, **payload):
        self.begins.append(payload)
        assert payload["execution_attempt_id"] == "attempt"
        assert not payload["agent_turn_id"] and not payload["agent_task_attempt_id"]
        assert payload["attempt_token"] == self.claim["attempt_token"]
        sdk_id = payload["sdk_tool_call_id"]
        if sdk_id not in self.calls:
            self.calls[sdk_id] = {"agent_tool_call_id": "call-" + sdk_id, "sdk_tool_call_id": sdk_id, "tool_id": payload["tool_id"],
                "arguments_hash": hashlib.sha256(json.dumps(payload["arguments"], sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode()).hexdigest(),
                "status": "pending_approval" if payload["tool_id"] in {"runtime:install_workspace_skill", "runtime:execute_skill_script", "mcp:story-fixture/save_story_fact"} else "running"}
        return deepcopy(self.calls[sdk_id])

    async def start_agent_tool_call(self, call_id, sdk_id, configuration_hash=""):
        call = self.calls[sdk_id]
        assert call["status"] in {"running", "approved"}
        call["status"] = "running"
        self.started += 1
        return deepcopy(call)

    async def apply_workspace_patch(self, call_id, sdk_id, patch, content):
        self.writes.append((sdk_id, content))
        return {"project_id": "p", "path": patch["path"], "version": patch["expected_version"] + 1, "deleted": False}

    async def pause_execution_for_approval(self, claim, **checkpoint):
        assert _activity_headers.get() is None
        self.checkpoints.append(deepcopy(checkpoint))
        self.status = "rotated" if self.checkpoint_fault == "rotated" else "waiting_approval"
        if self.checkpoint_fault:
            raise BackendError("checkpoint acknowledgement lost")
        return {"data": {**claim["attempt"], "status": self.status}}

    async def submit_execution_result(self, claim, payload, usage, trace):
        self.submissions.append((deepcopy(payload), deepcopy(usage), trace))
        self.status = "result_received"
        return {"data": {**claim["attempt"], "status": self.status, "response_hash": "response-hash"}}

    async def commit_execution_result(self, claim, response_hash):
        assert response_hash == "response-hash"
        self.commits.append(response_hash)
        return {"data": {"commit_status": "committed"}}

    async def fail_execution_attempt(self, claim, code, detail):
        self.failures.append((code, detail))

    def resume(self, action="approve"):
        checkpoint = deepcopy(self.checkpoints[-1])
        decisions = []
        for sdk_id in checkpoint.pop("pending_sdk_tool_call_ids"):
            self.calls[sdk_id]["status"] = "approved" if action == "approve" else "rejected"
            decisions.append({"sdk_tool_call_id": sdk_id, "action": action})
        self.claim = deepcopy(self.claim)
        self.claim["attempt_token"] = "rotated-private-token"
        self.claim["resume"] = {**checkpoint, "checkpoint_version": len(self.checkpoints), "approval_decisions": decisions}
        self.status = "running"


def worker_for(backend, model):
    worker = SDKTaskWorker(replace(settings(), backend_base_url="http://127.0.0.1:1"), backend)
    worker._model = model
    worker._heartbeat_interval_seconds = 0.05
    return worker


@pytest.mark.parametrize("action", ["approve", "reject"])
def test_actual_stateful_worker_uses_unified_tools_and_native_approval_after_rebuild(action):
    async def run():
        backend = WorkerBackend()
        model = StreamingSequence([[tool_item("install_workspace_skill", ARGS, "install")], [final_item({"title": "done"})]])
        assert await worker_for(backend, model).run_once()
        assert backend.status == "waiting_approval" and len(backend.checkpoints) == 1
        assert not backend.installs and not backend.submissions and not backend.failures
        native_only = {"prepare_workspace_publication", "publish_workspace_files", "prepare_agent_memory_publication", "publish_agent_memory"}
        assert {tool.name for tool in skill_execution_tools()} - {"execute_skill_script"} - native_only <= set(model.tools[0])
        assert not native_only.intersection(model.tools[0])
        saved = json.dumps(backend.checkpoints)
        assert "original-private-token" not in saved
        backend.resume(action)
        assert await worker_for(backend, model).run_once()
        assert not backend.failures and backend.commits == ["response-hash"]
        assert len(backend.installs) == (1 if action == "approve" else 0)
        assert len(model.inputs) == 2 and not model.responses
        assert backend.submissions[0][0] == {"artifact": {"title": "done"}}
        assert backend.submissions[0][1]["requests"] == 2
        assert backend.submissions[0][1]["total_tokens"] == 24
        if action == "approve":
            assert backend.begins[-1]["attempt_token"] == "rotated-private-token"
        assert not (asyncio.all_tasks() - {asyncio.current_task()})
    asyncio.run(run())


@pytest.mark.parametrize("fault", ["lost", "rotated"])
def test_actual_worker_lost_checkpoint_ack_does_not_fail_or_replay(fault):
    async def run():
        backend = WorkerBackend()
        backend.checkpoint_fault = fault
        model = StreamingSequence([[tool_item("install_workspace_skill", ARGS, "install")]])
        assert await worker_for(backend, model).run_once()
        assert len(backend.checkpoints) == len(model.inputs) == 1
        assert not backend.failures and not backend.installs and not backend.commits
    asyncio.run(run())


def test_actual_worker_plain_json_mode_survives_approval():
    async def run():
        backend = WorkerBackend()
        model = StreamingSequence([ModelBehaviorError("structured transport rejected"), [tool_item("install_workspace_skill", ARGS, "install")], [final_item({"title": "done"})]])
        assert await worker_for(backend, model).run_once()
        assert backend.checkpoints[0]["worker_state"]["output_mode"] == "json"
        backend.resume()
        assert await worker_for(backend, model).run_once()
        assert backend.commits and len(backend.installs) == 1 and not backend.failures
        assert model.schemas[0] is not None and model.schemas[1:] == [None, None]
    asyncio.run(run())


def test_actual_worker_does_not_fresh_retry_structured_failure_after_writing():
    async def run():
        backend = WorkerBackend()
        patch = {"path": "notes.txt", "operation": "create_file", "expected_version": 0, "diff": "+saved"}
        model = StreamingSequence([[tool_item("apply_workspace_patch", patch, "write")], ModelBehaviorError("invalid response")])
        assert await worker_for(backend, model).run_once()
        assert backend.writes == [("write", "saved")]
        assert len(model.inputs) == 2 and backend.failures[0][0] == "SDK_TOOL_REPLAY_RISK"
        assert backend.failures[0][1]["retryable"] is False and not backend.submissions
    asyncio.run(run())


def test_actual_worker_restores_source_batch_cursor_without_regenerating_completed_batch():
    async def run():
        claim = claim_fixture()
        units = [{"source_unit_id": f"unit-{i}", "ordinal": i + 1, "text": f"evidence-{i}"} for i in range(81)]
        claim["context_pack"]["task_cursor"] = {"batch": {"phase": "source_analysis", "source_units": units}}
        claim["context_pack"]["output_contract"] = {"schema": {"type": "object", "required": ["units"], "properties": {"units": {"type": "array"}}}}
        claim["context_pack"]["provider_result_contract"] = claim["context_pack"]["output_contract"]
        payloads = [{"units": [{"source_unit_id": u["source_unit_id"], "summary": u["text"]} for u in units[start:start + 40]]} for start in (0, 40, 80)]
        backend = WorkerBackend(claim)
        model = StreamingSequence([[final_item(payloads[0])], [tool_item("install_workspace_skill", ARGS, "install")], [final_item(payloads[1])], [final_item(payloads[2])]])
        assert await worker_for(backend, model).run_once()
        completed = backend.checkpoints[0]["worker_state"]["completed_batches"]
        assert len(completed) == 1 and len(completed[0]["payload"]["units"]) == 40
        backend.resume()
        assert await worker_for(backend, model).run_once()
        assert not backend.failures and backend.commits and len(backend.installs) == 1
        output = backend.submissions[0][0]
        assert [unit["source_unit_id"] for unit in output["units"]] == [unit["source_unit_id"] for unit in units]
        assert len(model.inputs) == 4 and not model.responses
        assert backend.submissions[0][1]["requests"] == 4
        assert backend.submissions[0][1]["total_tokens"] == 48
    asyncio.run(run())


@pytest.mark.parametrize("corruption", ["identity", "input", "mode", "phase", "schema", "missing-schema", "mixed-mode", "primary", "metadata", "reads", "cursor", "decision", "native-state"])
def test_actual_worker_rejects_checkpoint_corruption_before_replaying_model_or_tools(corruption):
    async def run():
        backend = WorkerBackend()
        model = StreamingSequence([[tool_item("install_workspace_skill", ARGS, "install")]])
        assert await worker_for(backend, model).run_once()
        backend.resume()
        resume = backend.claim["resume"]
        worker, state = resume["worker_state"], resume["run_state"]
        context = state["context"]["context"]
        if corruption == "identity": worker["identity"]["attempt_id"] = "other"
        elif corruption == "input": context["raw_request"]["config"] = {"changed": True}
        elif corruption == "mode": worker["output_mode"] = []
        elif corruption == "phase": worker["phase"] = "repair"
        elif corruption == "schema": resume["schema_version"] = "unknown"
        elif corruption == "missing-schema": resume.pop("schema_version")
        elif corruption == "mixed-mode": context["agent_task_attempt_id"] = "background"
        elif corruption == "primary": context["capability_versions"]["primary"] = "2.0.0"
        elif corruption == "metadata": backend.primary["data"]["skill"]["content_hash"] = "changed"
        elif corruption == "reads": worker["context_artifact_version_ids"] = ["unknown-artifact"]
        elif corruption == "cursor": worker["completed_batches"] = [{"payload": {}, "usage": {}, "trace_ref": ""}]
        elif corruption == "decision": resume["approval_decisions"][0]["sdk_tool_call_id"] = "other"
        elif corruption == "native-state": state["current_agent"] = "missing-agent"
        assert await worker_for(backend, model).run_once()
        assert not backend.installs and not backend.commits and len(model.inputs) == 1
        assert backend.failures[0][0] == "SDK_EXECUTION_STATE_INVALID"
        assert not backend.failures[0][1]["retryable"]
    asyncio.run(run())


@pytest.mark.parametrize("action", ["approve", "reject"])
def test_actual_worker_reconnects_stdio_mcp_after_native_approval(tmp_path, monkeypatch, action):
    from test_agent_tools import _mcp_catalog, _selected_skill

    state_file = tmp_path / "writes.jsonl"
    monkeypatch.setenv("TEST_STORY_MCP_STATE_FILE", str(state_file))
    catalog = _mcp_catalog(Path(__file__).parent / "fixtures" / "story_mcp_server.py")

    class MCPBackend(WorkerBackend):
        async def get_agent_tool_catalog(self):
            return deepcopy(catalog)

    async def run():
        backend = MCPBackend()
        backend.primary["data"]["skill"]["dependencies"] = _selected_skill()[0]["skill"]["dependencies"]
        model = StreamingSequence([[tool_item("lookup_story_fact", {"key": "hero"}, "read")],
            [tool_item("save_story_fact", {"key": "hero", "value": "verified"}, "write")], [final_item({"title": "done"})]])
        assert await worker_for(backend, model).run_once()
        assert len(backend.checkpoints) == 1 and not state_file.exists() and backend.started == 1
        assert "沈砚" in json.dumps(model.inputs[1], ensure_ascii=False)
        backend.resume(action)
        assert await worker_for(backend, model).run_once()
        assert not backend.failures and backend.commits and len(model.inputs) == 3
        if action == "approve":
            assert [json.loads(line) for line in state_file.read_text(encoding="utf-8").splitlines()] == [{"key": "hero", "value": "verified"}]
        else:
            assert not state_file.exists()
    asyncio.run(run())


def test_actual_worker_restores_declared_script_snapshot_before_execution():
    class ScriptBackend(WorkerBackend):
        def __init__(self):
            super().__init__()
            self.scripts = []

        async def execute_skill_script(self, call_id, sdk_id, arguments):
            self.scripts.append((call_id, sdk_id, arguments))
            return {"stdout": "fixture script receipt", "exit_code": 0}

    async def run():
        backend = ScriptBackend()
        backend.primary["data"]["skill"].update(name="primary-skill", scripts=[{"id": "render", "path": "scripts/render.py", "runtime": "python"}])
        args = {"skill_name": "primary-skill", "script_id": "render", "input_json": "{}"}
        model = StreamingSequence([[tool_item("execute_skill_script", args, "script")], [final_item({"title": "done"})]])
        assert await worker_for(backend, model).run_once()
        assert len(backend.checkpoints) == 1 and not backend.scripts
        backend.resume()
        assert await worker_for(backend, model).run_once()
        assert not backend.failures and backend.commits and len(backend.scripts) == 1
        assert backend.begins[-1]["skill_snapshot"] == {"capability_id": "primary", "version": "1.0.0", "content_hash": "primary-hash"}
    asyncio.run(run())


@pytest.mark.parametrize("new_dependency", [False, True])
def test_actual_worker_installed_skill_survives_second_mcp_approval_and_rebuild(tmp_path, monkeypatch, new_dependency):
    from test_agent_tools import _mcp_catalog, _selected_skill

    state_file = tmp_path / "second-approval.jsonl"
    monkeypatch.setenv("TEST_STORY_MCP_STATE_FILE", str(state_file))

    class AuthorBackend(WorkerBackend):
        async def get_agent_tool_catalog(self):
            catalog = _mcp_catalog(Path(__file__).parent / "fixtures" / "story_mcp_server.py")
            author_tools = await SkillBackend.get_agent_tool_catalog(self)
            catalog["tools"].extend(author_tools["tools"])
            loader = deepcopy(author_tools["tools"][0])
            loader.update(id="runtime:load_skill_instructions", name="load_skill_instructions", access="read", approval="never", timeout_seconds=30)
            catalog["tools"].append(loader)
            return catalog

    async def run():
        backend = AuthorBackend()
        dependencies = _selected_skill()[0]["skill"]["dependencies"]
        if new_dependency:
            backend.saved["installation"]["versions"][0]["manifest"]["dependencies"] = deepcopy(dependencies)
            backend.detail["data"]["skill"]["dependencies"] = deepcopy(dependencies)
        else:
            backend.primary["data"]["skill"]["dependencies"] = dependencies
        model = StreamingSequence([
            [tool_item("install_workspace_skill", ARGS, "install")],
            [tool_item("load_skill_instructions", {"capability_id": "my_skill"}, "load")],
            [tool_item("save_story_fact", {"key": "review", "value": "checked"}, "save")],
            [final_item({"title": "done"})],
        ])
        assert await worker_for(backend, model).run_once()
        assert len(backend.checkpoints) == 1 and not backend.installs
        if new_dependency:
            assert "save_story_fact" not in model.tools[0]
        backend.resume()
        assert await worker_for(backend, model).run_once()
        assert len(backend.checkpoints) == 2 and len(backend.installs) == 1
        assert "save_story_fact" in model.tools[1]
        assert "Report narrative gaps." in json.dumps(model.inputs[2])
        backend.resume()
        assert await worker_for(backend, model).run_once()
        assert not backend.failures and backend.commits and len(backend.installs) == 1
        assert len(model.inputs) == 4 and len(state_file.read_text(encoding="utf-8").splitlines()) == 1
    asyncio.run(run())


def test_actual_worker_oversized_checkpoint_does_not_become_retryable_provider_failure(monkeypatch):
    monkeypatch.setattr(StatefulExecution, "checkpoint", lambda self: {"oversized": "x" * (4 << 20)})

    async def run():
        backend = WorkerBackend()
        model = StreamingSequence([[tool_item("install_workspace_skill", ARGS, "install")]])
        assert await worker_for(backend, model).run_once()
        assert not backend.checkpoints and not backend.installs and not backend.commits
        assert len(model.inputs) == 1 and backend.failures[0][0] == "SDK_CHECKPOINT_FAILED"
        assert backend.failures[0][1]["stage"] == "run_state_checkpoint" and not backend.failures[0][1]["retryable"]
    asyncio.run(run())


def test_actual_worker_checks_claim_hash_before_model_or_tool_requests():
    async def run():
        backend = WorkerBackend()
        backend.claim["context_pack"]["context_hash"] = "different-context"
        model = StreamingSequence([])
        assert await worker_for(backend, model).run_once()
        assert not model.inputs and not backend.begins and not backend.checkpoints
        assert backend.failures[0][0] == "SDK_EXECUTION_STATE_INVALID"
    asyncio.run(run())
