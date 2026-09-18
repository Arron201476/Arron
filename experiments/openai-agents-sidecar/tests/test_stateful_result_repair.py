import asyncio
from copy import deepcopy
import hashlib
import json

import pytest

from content_agent_sidecar.backend import BackendError
from test_stateful_execution import StreamingSequence, final_item, worker_for
from test_stateful_pause import PauseBackend


class ResultReceiptBackend(PauseBackend):
    async def submit_execution_result(self, *args):
        result = await super().submit_execution_result(*args)
        self.result_receipt = deepcopy(result["data"])
        return result

    async def heartbeat_execution_attempt(self, claim, **kwargs):
        result = await super().heartbeat_execution_attempt(claim, **kwargs)
        if self.status == "result_received":
            result["data"]["response_hash"] = self.result_receipt["response_hash"]
        return result


class RejectionBackend(ResultReceiptBackend):
    async def commit_execution_result(self, claim, response_hash):
        if not getattr(self, "rejection", None):
            payload, usage, trace = self.submissions[-1]
            candidate = json.dumps(payload, ensure_ascii=False)
            self.rejection = {"schema_version": "execution_result_repair.v1", "rejection_no": 1,
                "input_snapshot_hash": claim["attempt"]["input_snapshot_hash"],
                "response_hash": hashlib.sha256(candidate.encode()).hexdigest(), "candidate": candidate,
                "usage": deepcopy(usage), "trace_ref": trace, "error_code": "OUTPUT_LENGTH_INSUFFICIENT", "error_detail": "The candidate is too short."}
            self.status = "repair_pending"
            return {"data": {"commit_status": "output_repair_queued"}}
        return await super().commit_execution_result(claim, response_hash)

    async def claim_execution_task(self, **kwargs):
        if self.status == "repair_pending":
            self.claim = deepcopy(self.claim)
            self.claim["repair"] = deepcopy(self.rejection)
            self.claim["attempt_token"] = "repair-claimed-token"
            self.status = "running"
        return self.claim


@pytest.mark.parametrize("pause", [False, True])
def test_worker_repairs_durable_commit_rejection_without_primary_generation(pause):
    async def run():
        backend = RejectionBackend()
        first = StreamingSequence([[final_item({"title": "short"})]])
        assert await worker_for(backend, first).run_once()
        assert backend.status == "repair_pending" and not backend.failures and not backend.commits
        if pause:
            backend.pause_requested = True
            unused = StreamingSequence([])
            assert await worker_for(backend, unused).run_once()
            assert backend.status == "paused" and not unused.inputs and not backend.failures
            assert backend.checkpoints[-1]["worker_state"]["repair_origin"]["response_hash"] == backend.rejection["response_hash"]
            backend.resume()
        final = StreamingSequence([[final_item({"title": "fully repaired"})]])
        assert await worker_for(backend, final).run_once()
        assert backend.commits and not backend.failures and len(final.inputs) == 1 and final.tools == [[]]
        assert json.loads(final.inputs[0][0]["content"])["candidate"] == backend.rejection["candidate"]
        assert backend.submissions[-1][1]["requests"] == 2 and backend.submissions[-1][1]["total_tokens"] == 24
    asyncio.run(run())


@pytest.mark.parametrize("corruption", ["hash", "input", "usage", "reads", "candidate", "origin"])
def test_durable_repair_receipt_corruption_cannot_start_model(corruption):
    async def run():
        backend = RejectionBackend()
        assert await worker_for(backend, StreamingSequence([[final_item({"title": "short"})]])).run_once()
        if corruption == "origin":
            backend.pause_requested = True
            assert await worker_for(backend, StreamingSequence([])).run_once()
            backend.resume()
            backend.claim["resume"]["worker_state"]["repair_origin"]["response_hash"] = "foreign"
        else:
            receipt = backend.rejection
            if corruption == "hash": receipt["response_hash"] = "wrong"
            elif corruption == "input": receipt["input_snapshot_hash"] = "foreign"
            elif corruption == "usage": receipt["usage"]["requests"] = -1
            elif corruption == "reads": receipt["usage"]["context_artifact_version_ids"] = "wrong"
            elif corruption == "candidate": receipt["candidate"] = "changed"
        unused = StreamingSequence([])
        assert await worker_for(backend, unused).run_once()
        assert not unused.inputs and not backend.commits and backend.failures[0][0] == "SDK_EXECUTION_STATE_INVALID"
    asyncio.run(run())


@pytest.mark.parametrize("boundary", ["submit", "commit"])
def test_uncertain_result_ack_does_not_fail_or_regenerate_durable_result(boundary):
    async def run():
        backend = ResultReceiptBackend()
        original_submit, original_commit = backend.submit_execution_result, backend.commit_execution_result
        calls = []
        async def submit(*args):
            result = await original_submit(*args)
            if boundary == "submit":
                raise BackendError("result acknowledgement lost")
            return result
        async def commit(*args):
            calls.append(args)
            if boundary == "commit" and len(calls) == 1:
                raise BackendError("commit acknowledgement lost")
            return await original_commit(*args)
        backend.submit_execution_result, backend.commit_execution_result = submit, commit
        model = StreamingSequence([[final_item({"title": "ready"})]])
        assert await worker_for(backend, model).run_once()
        assert not backend.failures and len(model.inputs) == 1 and len(backend.submissions) == 1
        if boundary == "commit":
            assert not backend.commits
            assert await worker_for(backend, model).run_once()
        assert backend.commits and not backend.failures and len(model.inputs) == 1 and len(backend.submissions) == 1
        assert len(calls) == (2 if boundary == "commit" else 1)
    asyncio.run(run())
