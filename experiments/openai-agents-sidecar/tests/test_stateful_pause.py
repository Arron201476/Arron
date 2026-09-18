import asyncio
from copy import deepcopy
from dataclasses import replace

import pytest

from content_agent_sidecar.backend import BackendError
from test_project_skills import ARGS
from test_stateful_execution import WorkerBackend, StreamingSequence, final_item, tool_item, worker_for, claim_fixture


class PauseBackend(WorkerBackend):
    def __init__(self, claim=None):
        super().__init__(claim)
        self.pause_requested = False
        self.pause_seen = asyncio.Event()

    async def heartbeat_execution_attempt(self, claim, **kwargs):
        receipt = await super().heartbeat_execution_attempt(claim, **kwargs)
        receipt["data"]["pause_requested"] = self.pause_requested
        if self.pause_requested:
            self.pause_seen.set()
        return receipt

    async def pause_execution_at_native_boundary(self, claim, **checkpoint):
        self.checkpoints.append(deepcopy(checkpoint))
        self.status = "rotated" if self.checkpoint_fault == "rotated" else "paused"
        if self.checkpoint_fault:
            raise BackendError("checkpoint acknowledgement lost")
        return {"data": {**claim["attempt"], "status": self.status}}

    def resume(self, action="approve"):
        super().resume(action)
        self.pause_requested = False
        self.pause_seen.clear()


class PauseModel(StreamingSequence):
    def __init__(self, backend, responses):
        super().__init__(responses)
        self.backend = backend

    async def stream_response(self, *args, **kwargs):
        self.backend.pause_requested = True
        await asyncio.wait_for(self.backend.pause_seen.wait(), 2)
        async for event in super().stream_response(*args, **kwargs):
            yield event
            await asyncio.sleep(0)


@pytest.mark.parametrize("fault", ["", "lost", "rotated"])
def test_stateful_native_pause_rebuild_preserves_write_and_usage(fault):
    async def run():
        backend = PauseBackend()
        backend.checkpoint_fault = fault
        patch = {"path": "notes.txt", "operation": "create_file", "expected_version": 0, "diff": "+saved"}
        first = PauseModel(backend, [[tool_item("apply_workspace_patch", patch, "write")]])
        assert await worker_for(backend, first).run_once()
        assert backend.status == ("rotated" if fault == "rotated" else "paused")
        assert len(backend.checkpoints) == len(first.inputs) == 1
        assert backend.checkpoints[0]["pending_sdk_tool_call_ids"] == []
        assert backend.writes == [("write", "saved")] and not backend.failures and not backend.submissions
        backend.checkpoint_fault = ""
        backend.resume()
        final = StreamingSequence([[final_item({"title": "done"})]])
        assert await worker_for(backend, final).run_once()
        assert not backend.failures and backend.commits == ["response-hash"]
        assert backend.writes == [("write", "saved")] and len(final.inputs) == 1
        assert len([item for item in final.inputs[0] if item.get("type") == "function_call_output" and item.get("call_id") == "write"]) == 1
        assert backend.submissions[0][1]["requests"] == 2 and backend.submissions[0][1]["total_tokens"] == 24
        assert not (asyncio.all_tasks() - {asyncio.current_task()})
    asyncio.run(run())


@pytest.mark.parametrize("action", ["approve", "reject"])
def test_stateful_native_pause_keeps_old_approval_until_explicit_resume(action):
    async def run():
        backend = PauseBackend()
        first = PauseModel(backend, [[tool_item("install_workspace_skill", ARGS, "install")]])
        assert await worker_for(backend, first).run_once()
        assert backend.status == "paused" and not backend.installs
        assert backend.checkpoints[0]["pending_sdk_tool_call_ids"] == ["install"]
        backend.calls["install"]["status"] = "approved" if action == "approve" else "rejected"
        unused = StreamingSequence([])
        assert await worker_for(backend, unused).run_once()
        assert backend.status == "paused" and not unused.inputs and not backend.installs
        backend.resume(action)
        final = StreamingSequence([[final_item({"title": "done"})]])
        assert await worker_for(backend, final).run_once()
        assert not backend.failures and backend.commits == ["response-hash"]
        assert len(backend.installs) == (1 if action == "approve" else 0)
    asyncio.run(run())


@pytest.mark.parametrize("blocked", [False, True])
@pytest.mark.parametrize("pause_count", [0, 2])
def test_stateful_repeated_zero_model_pause_preserves_initial_guardrail(blocked, pause_count):
    async def run():
        backend = PauseBackend()
        model = StreamingSequence([[final_item({"title": "done"})]])
        for _ in range(pause_count):
            backend.pause_requested = True
            assert await worker_for(backend, model).run_once()
            assert backend.status == "paused" and not model.inputs and not backend.failures
            checkpoint = backend.checkpoints[-1]
            assert checkpoint["run_state"]["current_turn"] == 0 and checkpoint["worker_state"]["unstarted_input"]
            backend.resume()
        worker = worker_for(backend, model)
        if blocked:
            worker._settings = replace(worker._settings, guardrail_max_input_chars=1)
        assert await worker.run_once()
        assert len(model.inputs) == (0 if blocked else 1)
        assert bool(backend.failures) == blocked and bool(backend.commits) != blocked
        if blocked:
            code, detail = backend.failures[0]
            assert code == "AGENT_INPUT_GUARDRAIL_REJECTED" and detail["stage"] == "input_validation" and not detail["retryable"]
    asyncio.run(run())


def test_stateful_terminal_output_wins_pause_race_without_checkpoint():
    async def run():
        backend = PauseBackend()
        model = PauseModel(backend, [[final_item({"title": "done"})]])
        assert await worker_for(backend, model).run_once()
        assert backend.commits and not backend.checkpoints and not backend.failures
    asyncio.run(run())


def test_stateful_native_checkpoint_ack_lost_after_resume_waits_for_old_approval():
    async def run():
        backend = PauseBackend()
        save = backend.pause_execution_at_native_boundary
        async def lost_after_resume(claim, **checkpoint):
            await save(claim, **checkpoint)
            backend.status = "waiting_approval"
            raise BackendError("checkpoint acknowledgement lost after user resumed")
        backend.pause_execution_at_native_boundary = lost_after_resume
        model = PauseModel(backend, [[tool_item("install_workspace_skill", ARGS, "install")]])
        assert await worker_for(backend, model).run_once()
        assert backend.status == "waiting_approval" and len(backend.checkpoints) == 1
        assert not backend.failures and not backend.installs and not backend.commits
    asyncio.run(run())


def test_stateful_native_pause_preserves_completed_source_batches():
    async def run():
        claim = claim_fixture()
        units = [{"source_unit_id": f"unit-{i}", "ordinal": i + 1, "text": f"evidence-{i}"} for i in range(81)]
        pack = claim["context_pack"]
        pack["task_cursor"] = {"batch": {"phase": "source_analysis", "source_units": units}}
        pack["output_contract"] = {"schema": {"type": "object", "required": ["units"], "properties": {"units": {"type": "array"}}}}
        pack["provider_result_contract"] = pack["output_contract"]
        payloads = [{"units": [{"source_unit_id": u["source_unit_id"], "summary": u["text"]} for u in units[start:start + 40]]} for start in (0, 40, 80)]
        backend = PauseBackend(claim)
        first = PauseModel(backend, [[final_item(payloads[0])]])
        assert await worker_for(backend, first).run_once()
        assert backend.status == "paused" and len(first.inputs) == 1 and not backend.failures
        saved = backend.checkpoints[0]["worker_state"]["completed_batches"][0]["payload"]
        assert [{"source_unit_id": u["source_unit_id"], "summary": u["summary"]} for u in saved["units"]] == payloads[0]["units"]
        backend.resume()
        final = StreamingSequence([[final_item(payloads[1])], [final_item(payloads[2])]])
        assert await worker_for(backend, final).run_once()
        assert not backend.failures and backend.commits
        assert len(final.inputs) == 2 and backend.submissions[0][1]["requests"] == 3
        assert len(backend.submissions[0][0]["units"]) == 81
    asyncio.run(run())
