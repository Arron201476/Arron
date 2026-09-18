import asyncio
from copy import deepcopy
import json

import httpx2 as httpx
import pytest
from agents import function_tool
from openai import APIConnectionError

from content_agent_sidecar.model_failure_boundary import MODEL_RECOVERY_REASON
from test_background_inputs import InputBackend
from test_background_pause import worker_for as background_worker
from test_main_inputs import execute, inputs
from test_main_pause import PauseBackend as MainBackend, commit_item, request_fixture
from test_stateful_execution import StreamingSequence, final_item, tool_item, worker_for as stateful_worker
from test_stateful_inputs import additional
from test_stateful_pause import PauseBackend as StatefulBackend


def transient_error():
    return APIConnectionError(message="Fixture model connection failed", request=httpx.Request("POST", "https://fixture.invalid/responses"))


@pytest.mark.parametrize("after_tool", [False, True])
def test_main_model_failure_rebuild_keeps_native_state_and_input_receipt(tmp_path, after_tool):
    async def run():
        backend = MainBackend()
        steps = [[tool_item("validate_workspace_skill", {"root_path": "skills/my-skill"}, "read")]] if after_tool else []
        model = StreamingSequence([*steps, transient_error()])
        request = request_fixture().model_copy(update={"additional_inputs": inputs("Original extra requirement")})
        first = await execute(tmp_path, backend, model, request)
        assert first[-1]["event"] == "agent.turn.paused" and not backend.commits
        checkpoint = first[-1]["data"]
        assert checkpoint["recovery_reason"] == MODEL_RECOVERY_REASON
        assert checkpoint["pending_sdk_tool_call_ids"] == []
        assert checkpoint.get("included_input_ids", []) == (["input-1"] if after_tool else [])
        assert checkpoint["run_state"]["max_turns"] == 8
        restored = request.model_copy(update={"run_state": deepcopy(checkpoint["run_state"])})
        restored = restored.model_copy(update={"additional_inputs": inputs("Original extra requirement", "New requirement after failure")})
        second = await execute(tmp_path, backend, StreamingSequence([transient_error()]), restored)
        assert second[-1]["event"] == "agent.turn.paused"
        restored = restored.model_copy(update={"run_state": second[-1]["data"]["run_state"]})
        settling = [[tool_item("validate_workspace_skill", {"root_path": "skills/my-skill"}, "settle")]] if not after_tool else []
        final_model = StreamingSequence([*settling, [commit_item()]])
        final = await execute(tmp_path, backend, final_model, restored)
        assert final[-1]["event"] == "agent.turn.committed" and len(backend.commits) == 1
        assert final[-1]["data"]["included_input_ids"] == ["input-1", "input-2"]
        assert len(final_model.inputs) == (1 if after_tool else 2)
        assert [item["sdk_tool_call_id"] for item in backend.begins].count("read") == int(after_tool)
        assert sum(item.get("content") == "Original extra requirement" for item in final_model.inputs[-1]) == 1
        assert sum(item.get("content") == "New requirement after failure" for item in final_model.inputs[-1]) == 1
    asyncio.run(run())


class BackgroundRecoveryBackend(InputBackend):
    async def complete_agent_task_pause(self, claim, **checkpoint):
        assert self.status == "running" and checkpoint["recovery_reason"] == MODEL_RECOVERY_REASON
        self.checkpoint.write_text(json.dumps(checkpoint), encoding="utf-8")
        self.status = "paused"
        return {"data": {"status": self.status}}


@pytest.mark.parametrize("after_tool", [False, True])
def test_background_model_failure_rebuild_does_not_replay_completed_write(tmp_path, after_tool):
    async def run():
        backend = BackgroundRecoveryBackend(tmp_path / "native-state.json")
        backend.append("Original extra requirement")
        writes = []

        @function_tool
        async def echo_value(value: str) -> str:
            writes.append(value)
            return "saved-once"

        steps = [[tool_item("echo_value", {"value": "original"}, "write")]] if after_tool else []
        assert await background_worker(backend, StreamingSequence([*steps, transient_error()]), echo_value).run_once()
        assert backend.status == "paused" and not backend.failed and not backend.completed
        checkpoint = json.loads(backend.checkpoint.read_text(encoding="utf-8"))
        assert checkpoint.get("included_input_ids", []) == (["input-1"] if after_tool else [])
        assert checkpoint["run_state"]["max_turns"] == 16
        assert not await background_worker(backend, StreamingSequence([]), echo_value).run_once()
        backend.resume()
        backend.append("New requirement after failure")
        assert await background_worker(backend, StreamingSequence([transient_error()]), echo_value).run_once()
        assert backend.status == "paused" and not backend.failed
        backend.resume()
        settling = [[tool_item("echo_value", {"value": "first response"}, "settle")]] if not after_tool else []
        final = StreamingSequence([*settling, [final_item({"summary": "Done", "result": {"ok": True}, "artifact_draft": None})]])
        assert await background_worker(backend, final, echo_value).run_once()
        assert not backend.failed and backend.completed["included_input_ids"] == ["input-1", "input-2"]
        assert writes == (["original"] if after_tool else ["first response"]) and len(final.inputs) == (1 if after_tool else 2)
        assert sum(item.get("content") == "Original extra requirement" for item in final.inputs[-1]) == 1
        assert sum(item.get("content") == "New requirement after failure" for item in final.inputs[-1]) == 1
    asyncio.run(run())


@pytest.mark.parametrize("after_tool", [False, True])
def test_stateful_model_failure_rebuild_preserves_worker_state_and_write(after_tool):
    async def run():
        backend = StatefulBackend()
        backend.claim["additional_inputs"] = [additional("Original extra requirement")]
        patch = {"path": "notes.txt", "operation": "create_file", "expected_version": 0, "diff": "+original"}
        steps = [[tool_item("apply_workspace_patch", patch, "write")]] if after_tool else []
        assert await stateful_worker(backend, StreamingSequence([*steps, transient_error()])).run_once()
        assert backend.status == "paused" and not backend.failures and not backend.submissions
        assert len(backend.checkpoints) == 1
        checkpoint = backend.checkpoints[0]
        assert checkpoint["recovery_reason"] == MODEL_RECOVERY_REASON and checkpoint["pending_sdk_tool_call_ids"] == []
        assert "unstarted_input" not in checkpoint["worker_state"]
        assert checkpoint["run_state"]["max_turns"] == 16
        backend.resume()
        backend.claim["additional_inputs"].append(additional("New requirement after failure", sequence=2))
        assert await stateful_worker(backend, StreamingSequence([transient_error()])).run_once()
        assert backend.status == "paused" and not backend.failures
        backend.resume()
        settling = [[tool_item("load_skill_instructions", {"capability_id": "primary"}, "settle")]] if not after_tool else []
        final = StreamingSequence([*settling, [final_item({"title": "Done"})]])
        assert await stateful_worker(backend, final).run_once()
        assert not backend.failures and backend.commits == ["response-hash"]
        assert backend.writes == ([("write", "original")] if after_tool else []) and len(final.inputs) == (1 if after_tool else 2)
        assert backend.input_receipts[-1] == ["input-1", "input-2"]
        assert sum(item.get("content") == "Original extra requirement" for item in final.inputs[-1]) == 1
        assert sum(item.get("content") == "New requirement after failure" for item in final.inputs[-1]) == 1
    asyncio.run(run())


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_initial_recovery_terminal_response_does_not_acknowledge_deferred_input(tmp_path, mode):
    async def run():
        if mode == "main":
            backend = MainBackend()
            request = request_fixture().model_copy(update={"additional_inputs": inputs("Original extra requirement")})
            first = await execute(tmp_path, backend, StreamingSequence([transient_error()]), request)
            request = request.model_copy(update={"run_state": first[-1]["data"]["run_state"],
                                                 "additional_inputs": inputs("Original extra requirement", "Deferred requirement")})
            model = StreamingSequence([[commit_item()]])
            final = await execute(tmp_path, backend, model, request)
            assert final[-1]["event"] == "agent.turn.committed" and len(backend.commits) == 1
            assert final[-1]["data"]["included_input_ids"] == ["input-1"]
        elif mode == "background":
            backend = BackgroundRecoveryBackend(tmp_path / "native-state.json")
            backend.append("Original extra requirement")
            assert await background_worker(backend, StreamingSequence([transient_error()])).run_once()
            backend.resume()
            backend.append("Deferred requirement")
            model = StreamingSequence([[final_item({"summary": "Done", "result": {"ok": True}, "artifact_draft": None})]])
            assert await background_worker(backend, model).run_once()
            assert not backend.failed and backend.completed["included_input_ids"] == ["input-1"]
        else:
            backend = StatefulBackend()
            backend.claim["additional_inputs"] = [additional("Original extra requirement")]
            assert await stateful_worker(backend, StreamingSequence([transient_error()])).run_once()
            backend.resume()
            backend.claim["additional_inputs"].append(additional("Deferred requirement", sequence=2))
            model = StreamingSequence([[final_item({"title": "Done"})]])
            assert await stateful_worker(backend, model).run_once()
            assert not backend.failures and backend.commits == ["response-hash"]
            assert backend.input_receipts[-1] == ["input-1"]
        assert len(model.inputs) == 1
        assert sum(item.get("content") == "Original extra requirement" for item in model.inputs[0]) == 1
        assert not any(item.get("content") == "Deferred requirement" for item in model.inputs[0])

    asyncio.run(run())
