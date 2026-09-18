import asyncio
from copy import deepcopy
from dataclasses import replace
import json

import pytest

from content_agent_sidecar.backend import BackendError, _activity_headers
from test_stateful_execution import StreamingSequence, claim_fixture, final_item, tool_item, worker_for
from test_stateful_pause import PauseBackend, PauseModel


def test_stateful_repair_preserves_primary_usage():
    async def run():
        backend = PauseBackend()
        model = StreamingSequence([[final_item({"wrong": True})], [final_item({"title": "repaired"})]])
        assert await worker_for(backend, model).run_once()
        assert not backend.failures and backend.commits
        assert backend.submissions[0][1]["requests"] == 2
        assert backend.submissions[0][1]["total_tokens"] == 24
    asyncio.run(run())


def test_stateful_repair_pauses_before_model_and_resumes_candidate():
    async def run():
        backend = PauseBackend()
        first = PauseModel(backend, [[final_item({"wrong": True})]])
        assert await worker_for(backend, first).run_once()
        assert backend.status == "paused" and not backend.failures and not backend.submissions
        checkpoint = backend.checkpoints[-1]
        assert checkpoint["worker_state"]["phase"] == "repair_validate"
        assert checkpoint["run_state"]["current_turn"] == 0
        backend.resume()
        final = StreamingSequence([[final_item({"title": "repaired"})]])
        assert await worker_for(backend, final).run_once()
        assert not backend.failures and backend.commits and len(final.inputs) == 1
        assert json.loads(final.inputs[0][0]["content"])["candidate"] == '{"wrong": true}'
        assert backend.submissions[0][1]["requests"] == 2
    asyncio.run(run())


@pytest.mark.parametrize("phase", ["parse", "validate", "backend"])
@pytest.mark.parametrize("fault", ["", "lost", "rotated"])
def test_stateful_repair_checkpoint_rebuild_does_not_replay_write_or_submission(phase, fault):
    async def run():
        backend = PauseBackend()
        backend.checkpoint_fault = fault
        rejected = []
        submit = backend.submit_execution_result
        async def reject_once(claim, payload, usage, trace):
            assert _activity_headers.get()["X-Agent-Execution-Attempt-ID"] == "attempt"
            if not rejected:
                rejected.append(deepcopy((payload, usage, trace)))
                raise BackendError("OUTPUT_LENGTH_INSUFFICIENT")
            return await submit(claim, payload, usage, trace)
        if phase == "backend":
            backend.submit_execution_result = reject_once
        patch = {"path": "notes.txt", "operation": "create_file", "expected_version": 0, "diff": "+saved"}
        class CandidateModel(StreamingSequence):
            async def stream_response(self, *args, **kwargs):
                if self.inputs:
                    backend.pause_requested = True
                    await asyncio.wait_for(backend.pause_seen.wait(), 2)
                async for event in super().stream_response(*args, **kwargs):
                    yield event
                    await asyncio.sleep(0)
        candidate = final_item({"title": "too short"} if phase == "backend" else {"wrong": True})
        if phase == "parse":
            candidate.content[0].text = "invalid candidate"
        first = CandidateModel([[tool_item("apply_workspace_patch", patch, "write")], [candidate]])
        worker = worker_for(backend, first)
        if phase == "parse":
            worker._content_model_name = "gemini-3p5-flash-routerhub"
        assert await worker.run_once()
        assert backend.status == ("rotated" if fault == "rotated" else "paused") and not backend.failures
        assert backend.writes == [("write", "saved")] and not backend.commits and not backend.submissions
        saved = backend.checkpoints[-1]["worker_state"]
        assert saved["phase"] == "repair_" + phase and saved["repair"]["usage"]["requests"] == 2
        assert saved["output_mode"] == ("json" if phase == "parse" else "structured")
        assert backend.checkpoints[-1]["pending_sdk_tool_call_ids"] == []
        assert "original-private-token" not in json.dumps(backend.checkpoints)
        backend.checkpoint_fault = ""
        backend.resume()
        final = StreamingSequence([[final_item({"title": "repaired"})]])
        assert await worker_for(backend, final).run_once()
        assert not backend.failures and backend.commits and len(final.inputs) == 1
        assert not final.tools[0]
        if phase == "parse":
            assert final.schemas == [None]
        assert json.loads(final.inputs[0][0]["content"])["candidate"] == saved["repair"]["candidate"]
        assert backend.submissions[0][1]["requests"] == 3 and backend.submissions[0][1]["total_tokens"] == 36
        assert backend.writes == [("write", "saved")] and len(rejected) == (1 if phase == "backend" else 0)
        assert not (asyncio.all_tasks() - {asyncio.current_task()})
    asyncio.run(run())


@pytest.mark.parametrize("blocked", [False, True])
def test_repeated_repair_zero_model_pause_preserves_guardrails(blocked):
    async def run():
        backend = PauseBackend()
        assert await worker_for(backend, PauseModel(backend, [[final_item({"wrong": True})]])).run_once()
        for _ in range(2):
            backend.resume()
            backend.pause_requested = True
            unused = StreamingSequence([])
            assert await worker_for(backend, unused).run_once()
            assert backend.status == "paused" and not unused.inputs and not backend.failures
        backend.resume()
        final = StreamingSequence([[final_item({"title": "done"})]])
        worker = worker_for(backend, final)
        if blocked:
            worker._settings = replace(worker._settings, guardrail_max_input_chars=1)
        assert await worker.run_once()
        assert len(final.inputs) == (0 if blocked else 1)
        assert bool(backend.failures) == blocked and bool(backend.commits) != blocked
        if blocked:
            assert backend.failures[0][0] == "AGENT_INPUT_GUARDRAIL_REJECTED" and not backend.failures[0][1]["retryable"]
        else:
            assert backend.submissions[0][1]["requests"] == 2
    asyncio.run(run())


@pytest.mark.parametrize("corruption", ["phase", "candidate", "pack", "input", "usage", "schema", "context", "mode"])
def test_repair_checkpoint_corruption_fails_before_any_model_or_tool(corruption):
    async def run():
        backend = PauseBackend()
        assert await worker_for(backend, PauseModel(backend, [[final_item({"wrong": True})]])).run_once()
        backend.resume()
        saved = backend.claim["resume"]
        worker = saved["worker_state"]
        if corruption == "phase": worker["phase"] = "generate"
        elif corruption == "candidate": worker["repair"]["candidate"] = "changed"
        elif corruption == "pack": worker["repair"]["pack_hash"] = "foreign-batch"
        elif corruption == "input": saved["run_state"]["original_input"] = "changed"
        elif corruption == "usage": worker["repair"]["usage"]["requests"] = -1
        elif corruption == "schema": worker["schema_version"] = "stateful_worker.v1"
        elif corruption == "context": saved["run_state"]["context"]["context"]["execution_attempt_id"] = "foreign"
        elif corruption == "mode": worker["output_mode"] = "unknown"
        unused = StreamingSequence([])
        assert await worker_for(backend, unused).run_once()
        assert not unused.inputs and not backend.writes and not backend.commits
        assert backend.failures[0][0] == "SDK_EXECUTION_STATE_INVALID"
    asyncio.run(run())


def test_repair_compatibility_pause_preserves_consumed_usage_and_json_mode():
    async def run():
        backend = PauseBackend()
        malformed = final_item({})
        malformed.content[0].text = "malformed provider JSON"
        class RepairModel(StreamingSequence):
            async def stream_response(self, *args, **kwargs):
                if self.inputs:
                    backend.pause_requested = True
                    await asyncio.wait_for(backend.pause_seen.wait(), 2)
                async for event in super().stream_response(*args, **kwargs):
                    yield event
                    await asyncio.sleep(0)
        first = RepairModel([[final_item({"wrong": True})], [malformed]])
        assert await worker_for(backend, first).run_once()
        assert backend.status == "paused" and not backend.failures
        saved = backend.checkpoints[-1]["worker_state"]
        assert saved["phase"] == "repair_validate" and saved["output_mode"] == "json"
        assert saved["repair"]["usage"]["requests"] == 2
        backend.resume()
        final = StreamingSequence([[final_item({"title": "done"})]])
        assert await worker_for(backend, final).run_once()
        assert final.schemas == [None] and backend.commits and not backend.failures
        assert backend.submissions[0][1]["requests"] == 3
    asyncio.run(run())


@pytest.mark.parametrize("resume_error", [False, True])
def test_executed_native_repair_state_resumes_without_resetting_turn_budget(resume_error):
    async def run():
        backend = PauseBackend()
        class MidRepairModel(StreamingSequence):
            async def stream_response(self, *args, **kwargs):
                if self.inputs:
                    backend.pause_requested = True
                    await asyncio.wait_for(backend.pause_seen.wait(), 2)
                async for event in super().stream_response(*args, **kwargs):
                    yield event
                    await asyncio.sleep(0)
        first = MidRepairModel([[final_item({"wrong": True})], []])
        assert await worker_for(backend, first).run_once()
        assert backend.status == "paused" and not backend.failures
        assert backend.checkpoints[-1]["run_state"]["current_turn"] == 1
        assert "unstarted_input" not in backend.checkpoints[-1]["worker_state"]
        backend.resume()
        output = final_item({"title": "done"})
        if resume_error:
            output.content[0].text = "invalid output"
        final = StreamingSequence([[output]])
        assert await worker_for(backend, final).run_once()
        assert len(final.inputs) == 1
        if resume_error:
            assert backend.failures[0][0] == "SDK_TOOL_REPLAY_RISK" and not backend.commits
            assert not backend.failures[0][1]["retryable"]
        else:
            assert backend.commits and not backend.failures
            assert backend.submissions[0][1]["requests"] == 3
    asyncio.run(run())


@pytest.mark.parametrize("backend_repair", [False, True])
def test_source_batch_repair_rebuild_preserves_cursor_completed_outputs_and_usage(backend_repair):
    async def run():
        claim = claim_fixture()
        units = [{"source_unit_id": f"unit-{i}", "ordinal": i + 1, "text": f"evidence-{i}"} for i in range(81)]
        pack = claim["context_pack"]
        pack["task_cursor"] = {"batch": {"phase": "source_analysis", "source_units": units}}
        pack["output_contract"] = {"schema": {"type": "object", "required": ["units"], "properties": {"units": {"type": "array"}}}}
        pack["provider_result_contract"] = pack["output_contract"]
        payloads = [{"units": [{"source_unit_id": u["source_unit_id"], "summary": u["text"]} for u in units[start:start + 40]]} for start in (0, 40, 80)]
        backend = PauseBackend(claim)
        rejected = []
        submit = backend.submit_execution_result
        async def reject_once(claim, payload, usage, trace):
            if not rejected:
                rejected.append(deepcopy(payload))
                backend.pause_requested = True
                await asyncio.wait_for(backend.pause_seen.wait(), 2)
                raise BackendError("BATCH_COVERAGE_INVALID")
            return await submit(claim, payload, usage, trace)
        if backend_repair:
            backend.submit_execution_result = reject_once
            first = StreamingSequence([[final_item(payload)] for payload in payloads])
        else:
            class BatchModel(StreamingSequence):
                async def stream_response(self, *args, **kwargs):
                    if self.inputs:
                        backend.pause_requested = True
                        await asyncio.wait_for(backend.pause_seen.wait(), 2)
                    async for event in super().stream_response(*args, **kwargs):
                        yield event
                        await asyncio.sleep(0)
            first = BatchModel([[final_item(payloads[0])], [final_item({"wrong": True})]])
        assert await worker_for(backend, first).run_once()
        assert backend.status == "paused" and not backend.failures
        saved = backend.checkpoints[-1]["worker_state"]
        assert len(saved["completed_batches"]) == (3 if backend_repair else 1)
        assert saved["phase"] == ("repair_backend" if backend_repair else "repair_validate")
        backend.resume()
        final = StreamingSequence([[final_item(rejected[0])]] if backend_repair else [[final_item(payloads[1])], [final_item(payloads[2])]])
        assert await worker_for(backend, final).run_once()
        assert not backend.failures and backend.commits
        assert len(final.inputs) == (1 if backend_repair else 2)
        payload, usage, _ = backend.submissions[0]
        assert len(payload["units"]) == 81 and usage["requests"] == 4 and usage["total_tokens"] == 48
        assert [unit["source_unit_id"] for unit in payload["units"]] == [unit["source_unit_id"] for unit in units]
    asyncio.run(run())


@pytest.mark.parametrize("code", ["OUTPUT_SCHEMA_VALIDATION_FAILED", "OUTPUT_LENGTH_INSUFFICIENT"])
def test_backend_rejection_after_repaired_resume_is_terminal_not_an_unbounded_loop(code):
    async def run():
        backend = PauseBackend()
        rejections = []
        async def reject(claim, payload, usage, trace):
            rejections.append(payload)
            backend.pause_requested = True
            await asyncio.wait_for(backend.pause_seen.wait(), 2)
            raise BackendError(code)
        backend.submit_execution_result = reject
        assert await worker_for(backend, StreamingSequence([[final_item({"title": "first"})]])).run_once()
        assert backend.status == "paused" and not backend.failures
        backend.resume()
        final = StreamingSequence([[final_item({"title": "repaired"})]])
        assert await worker_for(backend, final).run_once()
        assert len(rejections) == 2 and len(final.inputs) == 1 and not backend.commits
        assert backend.failures[0][0] == "OUTPUT_REPAIR_FAILED" and not backend.failures[0][1]["retryable"]
    asyncio.run(run())


def test_repair_output_guardrail_is_terminal_and_never_submits_rejected_output():
    async def run():
        backend = PauseBackend()
        assert await worker_for(backend, PauseModel(backend, [[final_item({"wrong": True})]])).run_once()
        backend.resume()
        final = StreamingSequence([[final_item({"title": "blocked"})]])
        worker = worker_for(backend, final)
        worker._settings = replace(worker._settings, guardrail_max_output_chars=1)
        assert await worker.run_once()
        assert len(final.inputs) == 1 and not backend.submissions and not backend.commits
        assert backend.failures[0][0] == "AGENT_OUTPUT_GUARDRAIL_REJECTED"
        assert not backend.failures[0][1]["retryable"]
    asyncio.run(run())


def test_generation_checkpoint_v1_remains_resumable():
    async def run():
        backend = PauseBackend()
        backend.pause_requested = True
        assert await worker_for(backend, StreamingSequence([])).run_once()
        backend.resume()
        backend.claim["resume"]["worker_state"]["schema_version"] = "stateful_worker.v1"
        model = StreamingSequence([[final_item({"title": "legacy"})]])
        assert await worker_for(backend, model).run_once()
        assert backend.commits and not backend.failures and len(model.inputs) == 1
    asyncio.run(run())
