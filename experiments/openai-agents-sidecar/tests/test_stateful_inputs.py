import asyncio
from copy import deepcopy
from dataclasses import replace
from hashlib import sha256
import json

import pytest

from content_agent_sidecar.stateful_inputs import StatefulInputs
from test_project_skills import ARGS
from test_stateful_execution import StreamingSequence, claim_fixture, final_item, tool_item, worker_for
from test_stateful_pause import PauseBackend, PauseModel
from test_stateful_result_repair import RejectionBackend


def additional(content="Use a concise conclusion.", sequence=1, status="received"):
    return {"attempt_id": "attempt", "input_id": f"input-{sequence}", "sequence": sequence,
            "content": content, "content_hash": sha256(content.encode()).hexdigest(), "status": status}


def user_contents(items):
    return [item["content"] for item in items if item.get("role") == "user" and isinstance(item.get("content"), str)]


@pytest.mark.parametrize("fault", ["attempt", "order", "duplicate", "hash", "status", "size", "total", "missing-checkpoint"])
def test_stateful_input_ledger_rejects_invalid_claim(fault):
    inputs = [additional()]
    if fault == "attempt": inputs[0]["attempt_id"] = "foreign"
    elif fault == "order": inputs[0]["sequence"] = True
    elif fault == "duplicate": inputs.append({**inputs[0], "sequence": 2})
    elif fault == "hash": inputs[0]["content"] = "changed"
    elif fault == "status": inputs[0]["status"] = "done"
    elif fault == "size": inputs = [additional("x" * ((32 << 10) + 1))]
    elif fault == "total": inputs = [additional("x" * (32 << 10), i + 1) for i in range(17)]
    elif fault == "missing-checkpoint": inputs[0]["status"] = "included"
    with pytest.raises(ValueError):
        StatefulInputs.from_claim("attempt", inputs).restore(None)


@pytest.mark.parametrize("boundary", ["input", "output"])
def test_stateful_model_receipt_is_independent_of_final_output_acceptance(boundary):
    async def run():
        claim = claim_fixture()
        claim["additional_inputs"] = [additional()]
        backend = PauseBackend(claim)
        model = StreamingSequence([[final_item({"title": "rejected"})]])
        worker = worker_for(backend, model)
        worker._settings = replace(worker._settings, **{f"guardrail_max_{boundary}_chars": 1})
        executions, prepare = [], worker._prepare_execution
        async def capture(current):
            execution = await prepare(current)
            executions.append(execution)
            return execution
        worker._prepare_execution = capture
        assert await worker.run_once()
        assert backend.failures and not backend.commits
        assert len(model.inputs) == (1 if boundary == "output" else 0)
        assert executions[0].inputs.included == (["input-1"] if boundary == "output" else [])
    asyncio.run(run())


@pytest.mark.parametrize("action", ["approve", "reject"])
def test_stateful_append_after_approval_uses_native_input_without_replaying_prior_tools(action):
    async def run():
        backend = PauseBackend()
        first = StreamingSequence([[tool_item("install_workspace_skill", ARGS, "install")]])
        assert await worker_for(backend, first).run_once()
        assert backend.status == "waiting_approval" and not backend.installs
        backend.resume(action)
        backend.claim["additional_inputs"] = [additional()]
        second = PauseModel(backend, [[]])
        assert await worker_for(backend, second).run_once()
        assert backend.status == "paused" and not backend.failures
        assert len(backend.installs) == (1 if action == "approve" else 0)
        assert user_contents(second.inputs[0]).count(additional()["content"]) == 1
        saved = backend.checkpoints[-1]["worker_state"]
        assert saved["schema_version"] == "stateful_worker.v3"
        assert saved["input_ledger"]["included"] == ["input-1"]
        assert saved["input_ledger"]["original_ids"] == []
        backend.resume(action)
        backend.claim["additional_inputs"].append(additional("Keep the central conflict.", 2))
        final = StreamingSequence([[final_item({"title": "done"})]])
        assert await worker_for(backend, final).run_once()
        assert backend.commits and not backend.failures and len(final.inputs) == 1
        assert len(backend.installs) == (1 if action == "approve" else 0)
        for item in backend.claim["additional_inputs"]:
            assert user_contents(final.inputs[0]).count(item["content"]) == 1
        assert backend.submissions[0][1]["requests"] == 3
    asyncio.run(run())


@pytest.mark.parametrize("blocked", [False, True])
def test_stateful_inputs_survive_repeated_zero_model_pause_and_guardrails(blocked):
    async def run():
        claim = claim_fixture()
        claim["additional_inputs"] = [additional()]
        backend = PauseBackend(claim)
        for index in range(2):
            backend.pause_requested = True
            unused = StreamingSequence([])
            assert await worker_for(backend, unused).run_once()
            assert backend.status == "paused" and not backend.failures and not unused.inputs
            assert backend.checkpoints[-1]["worker_state"]["input_ledger"]["included"] == []
            backend.resume()
            backend.claim["additional_inputs"].append(additional(f"Late requirement {index}.", index + 2))
        model = StreamingSequence([[final_item({"title": "done"})]])
        worker = worker_for(backend, model)
        if blocked:
            worker._settings = replace(worker._settings, guardrail_max_input_chars=1)
        assert await worker.run_once()
        if blocked:
            assert not model.inputs and backend.failures[0][0] == "AGENT_INPUT_GUARDRAIL_REJECTED"
        else:
            assert backend.commits and not backend.failures
            for item in backend.claim["additional_inputs"]:
                assert user_contents(model.inputs[0]).count(item["content"]) == 1
        assert backend.claim["context_pack"]["context_hash"] == "input-hash"
    asyncio.run(run())


@pytest.mark.parametrize("fault", ["", "blocked", "pending"])
def test_stateful_native_pending_input_survives_zero_model_repause(fault):
    async def run():
        backend = PauseBackend()
        assert await worker_for(backend, PauseModel(backend, [[]])).run_once()
        assert backend.status == "paused" and not backend.failures
        backend.resume()
        backend.claim["additional_inputs"] = [additional()]
        backend.pause_requested = True
        unused = StreamingSequence([])
        assert await worker_for(backend, unused).run_once()
        assert backend.status == "paused" and not backend.failures and not unused.inputs
        checkpoint = backend.checkpoints[-1]
        assert checkpoint["run_state"]["pending_input"] == [{"role": "user", "content": additional()["content"]}]
        assert checkpoint["worker_state"]["input_ledger"]["included"] == []
        backend.resume()
        backend.claim["additional_inputs"].append(additional("Use one recommendation.", 2))
        if fault == "pending":
            backend.claim["resume"]["run_state"]["pending_input"][0]["content"] = "unregistered instruction"
        final = StreamingSequence([[final_item({"title": "done"})]])
        worker = worker_for(backend, final)
        if fault == "blocked":
            worker._settings = replace(worker._settings, guardrail_max_input_chars=1)
        assert await worker.run_once()
        if fault:
            assert not final.inputs and not backend.commits
            assert backend.failures[0][0] == ("SDK_EXECUTION_STATE_INVALID" if fault == "pending" else "AGENT_INPUT_GUARDRAIL_REJECTED")
        else:
            assert backend.commits and not backend.failures
            for item in backend.claim["additional_inputs"]:
                assert user_contents(final.inputs[0]).count(item["content"]) == 1
    asyncio.run(run())


@pytest.mark.parametrize("fault", ["content", "deleted", "included", "pending", "original", "version"])
def test_stateful_input_restore_rejects_corruption_before_model(fault):
    async def run():
        claim = claim_fixture()
        claim["additional_inputs"] = [additional()]
        backend = PauseBackend(claim)
        backend.pause_requested = True
        assert await worker_for(backend, StreamingSequence([])).run_once()
        backend.resume()
        worker = backend.claim["resume"]["worker_state"]
        if fault == "content": backend.claim["additional_inputs"] = [additional("replaced")]
        elif fault == "deleted": backend.claim["additional_inputs"] = []
        elif fault == "included": backend.claim["additional_inputs"][0]["status"] = "included"
        elif fault == "pending": worker["input_ledger"]["stage_included"] = ["input-1"]
        elif fault == "original": worker["input_ledger"]["original_ids"] = []
        elif fault == "version": worker["schema_version"] = "stateful_worker.v2"
        unused = StreamingSequence([])
        assert await worker_for(backend, unused).run_once()
        assert not unused.inputs and not backend.commits and backend.failures[0][0] == "SDK_EXECUTION_STATE_INVALID"
    asyncio.run(run())


@pytest.mark.parametrize("phase", ["local", "backend"])
def test_stateful_additional_inputs_survive_repair_phase_and_rebuild(phase):
    async def run():
        backend = RejectionBackend() if phase == "backend" else PauseBackend()
        backend.claim["additional_inputs"] = [additional()]
        if phase == "backend":
            assert await worker_for(backend, StreamingSequence([[final_item({"title": "short"})]])).run_once()
            assert backend.status == "repair_pending"
            backend.claim["additional_inputs"][0]["status"] = "included"
            backend.pause_requested = True
            assert await worker_for(backend, StreamingSequence([])).run_once()
        else:
            assert await worker_for(backend, PauseModel(backend, [[final_item({"wrong": True})]])).run_once()
        assert backend.status == "paused" and not backend.failures
        original_candidate = backend.checkpoints[-1]["worker_state"]["repair"]["candidate"]
        backend.resume()
        backend.claim["additional_inputs"].append(additional("Retain the source evidence.", 2))
        middle = PauseModel(backend, [[]])
        assert await worker_for(backend, middle).run_once()
        assert backend.status == "paused" and not backend.failures
        assert middle.tools == [[]]
        backend.resume()
        backend.claim["additional_inputs"].append(additional("End with one recommendation.", 3))
        final = StreamingSequence([[final_item({"title": "repaired"})]])
        assert await worker_for(backend, final).run_once()
        assert backend.commits and not backend.failures and final.tools == [[]]
        assert json.loads(final.inputs[0][0]["content"])["candidate"] == original_candidate
        for item in backend.claim["additional_inputs"]:
            assert user_contents(final.inputs[0]).count(item["content"]) == 1
        assert backend.submissions[-1][1]["requests"] == 3
    asyncio.run(run())


def test_stateful_inputs_apply_to_remaining_batches_without_regenerating_completed_batch():
    async def run():
        claim = claim_fixture()
        units = [{"source_unit_id": f"unit-{i}", "ordinal": i + 1, "text": f"evidence-{i}"} for i in range(81)]
        pack = claim["context_pack"]
        pack["task_cursor"] = {"batch": {"phase": "source_analysis", "source_units": units}}
        pack["output_contract"] = {"schema": {"type": "object", "required": ["units"], "properties": {"units": {"type": "array"}}}}
        pack["provider_result_contract"] = deepcopy(pack["output_contract"])
        payloads = [{"units": [{"source_unit_id": u["source_unit_id"], "summary": u["text"]} for u in units[start:start + 40]]} for start in (0, 40, 80)]
        backend = PauseBackend(claim)
        assert await worker_for(backend, PauseModel(backend, [[final_item(payloads[0])]])).run_once()
        assert backend.status == "paused" and not backend.failures
        assert len(backend.checkpoints[-1]["worker_state"]["completed_batches"]) == 1
        completed = deepcopy(backend.checkpoints[-1]["worker_state"]["completed_batches"][0]["payload"])
        backend.resume()
        backend.claim["additional_inputs"] = [additional()]
        final = StreamingSequence([[final_item(payloads[1])], [final_item(payloads[2])]])
        assert await worker_for(backend, final).run_once()
        assert backend.commits and not backend.failures and len(final.inputs) == 2
        assert all(user_contents(items).count(additional()["content"]) == 1 for items in final.inputs)
        assert backend.submissions[-1][0]["units"][:40] == completed["units"]
        assert backend.submissions[-1][1]["requests"] == 3
    asyncio.run(run())
