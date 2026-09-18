import asyncio
from types import SimpleNamespace

import pytest

from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.runtime import AgentContext
from content_agent_sidecar.stateful_execution import StatefulExecution
from content_agent_sidecar.task_worker import SDKTaskWorker


class LeaseBackend:
    def __init__(self):
        self.claim = {
            "attempt": {"attempt_id": "stateful-attempt", "input_snapshot_hash": "frozen-input"},
            "attempt_token": "lease-fixture-token",
            "context_pack": {"project_id": "p", "output_contract": {"schema": {"type": "object", "required": ["title"]}}},
        }
        self.heartbeats = 0
        self.submissions = []
        self.commits = []
        self.failures = []

    async def claim_execution_task(self, **kwargs):
        return self.claim

    async def heartbeat_execution_attempt(self, claim, *, lease_seconds):
        assert claim is self.claim and lease_seconds == 60
        self.heartbeats += 1
        return {"data": {**claim["attempt"], "status": "running"}}

    async def submit_execution_result(self, claim, payload, usage, trace):
        self.submissions.append(payload)
        return {"data": {"response_hash": "receipt"}}

    async def commit_execution_result(self, claim, response_hash):
        assert response_hash == "receipt"
        self.commits.append(response_hash)
        return {"data": {"commit_status": "committed"}}

    async def fail_execution_attempt(self, claim, code, detail):
        self.failures.append((code, detail))


def lease_worker(backend):
    worker = object.__new__(SDKTaskWorker)
    worker._settings = SimpleNamespace(task_worker_lease_seconds=60)
    worker._backend = backend
    worker._worker_id = "fixture-worker"
    worker._content_model_name = "fixture-model"
    worker._heartbeat_interval_seconds = 0.01

    async def prepare(claim):
        return StatefulExecution(AgentContext("p", "c", backend), {})

    worker._prepare_execution = prepare

    async def execute(claim, **kwargs):
        return {"title": "completed"}, {}, "trace"

    worker._execute_generation = execute
    return worker


def test_stateful_worker_renews_before_generation_and_through_slow_execution():
    async def run():
        backend = LeaseBackend()
        worker = lease_worker(backend)

        async def execute(claim, **kwargs):
            assert backend.heartbeats == 1
            while backend.heartbeats < 4:
                await asyncio.sleep(0.001)
            return {"title": "completed"}, {}, "trace"

        worker._execute_generation = execute
        assert await asyncio.wait_for(worker.run_once(), 2)
        assert backend.heartbeats >= 4
        assert backend.submissions == [{"title": "completed"}]
        assert backend.commits == ["receipt"] and not backend.failures
        count = backend.heartbeats
        await asyncio.sleep(0.04)
        assert backend.heartbeats == count
        assert not (asyncio.all_tasks() - {asyncio.current_task()})

    asyncio.run(run())


@pytest.mark.parametrize("fail_at", [1, 2])
def test_stateful_worker_lost_lease_stops_without_submission_or_provider_retry(fail_at):
    async def run():
        backend = LeaseBackend()
        worker = lease_worker(backend)
        started, stopped = asyncio.Event(), asyncio.Event()
        renew = backend.heartbeat_execution_attempt

        async def heartbeat(claim, **kwargs):
            result = await renew(claim, **kwargs)
            if backend.heartbeats == fail_at:
                raise BackendError("ATTEMPT_STALE")
            return result

        async def execute(claim, **kwargs):
            started.set()
            try:
                await asyncio.Event().wait()
            finally:
                stopped.set()

        backend.heartbeat_execution_attempt = heartbeat
        worker._execute_generation = execute
        assert await asyncio.wait_for(worker.run_once(), 2)
        assert started.is_set() == stopped.is_set() == (fail_at > 1)
        assert not backend.submissions and not backend.commits and not backend.failures
        assert not (asyncio.all_tasks() - {asyncio.current_task()})

    asyncio.run(run())


def test_stateful_worker_hung_heartbeat_is_bounded_and_cancels_execution():
    async def run():
        backend = LeaseBackend()
        worker = lease_worker(backend)
        stopped, heartbeat_stopped = asyncio.Event(), asyncio.Event()
        renew = backend.heartbeat_execution_attempt

        async def heartbeat(claim, **kwargs):
            result = await renew(claim, **kwargs)
            if backend.heartbeats > 1:
                try:
                    await asyncio.Event().wait()
                finally:
                    heartbeat_stopped.set()
            return result

        async def execute(claim, **kwargs):
            try:
                await asyncio.Event().wait()
            finally:
                stopped.set()

        backend.heartbeat_execution_attempt = heartbeat
        worker._execute_generation = execute
        assert await asyncio.wait_for(worker.run_once(), 2)
        assert stopped.is_set() and heartbeat_stopped.is_set()
        assert not backend.submissions and not backend.commits and not backend.failures
        assert not (asyncio.all_tasks() - {asyncio.current_task()})

    asyncio.run(run())


def test_stateful_worker_shutdown_cleans_execution_and_heartbeat():
    async def run():
        backend = LeaseBackend()
        worker = lease_worker(backend)
        started, stopped = asyncio.Event(), asyncio.Event()

        async def execute(claim, **kwargs):
            started.set()
            try:
                await asyncio.Event().wait()
            finally:
                stopped.set()

        worker._execute_generation = execute
        task = asyncio.create_task(worker.run_once())
        await asyncio.wait_for(started.wait(), 2)
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task
        count = backend.heartbeats
        await asyncio.sleep(0.04)
        assert stopped.is_set() and backend.heartbeats == count
        assert not backend.submissions and not backend.commits and not backend.failures
        assert not (asyncio.all_tasks() - {asyncio.current_task()})

    asyncio.run(run())


def test_stateful_worker_keeps_lease_during_backend_output_repair():
    async def run():
        backend = LeaseBackend()
        worker = lease_worker(backend)
        submit = backend.submit_execution_result
        repairs = []

        async def rejected_submit(*args):
            result = await submit(*args)
            if len(backend.submissions) == 1:
                raise BackendError("OUTPUT_SCHEMA_VALIDATION_FAILED")
            return result

        async def repair(pack, payload, error, **kwargs):
            assert "OUTPUT_SCHEMA_VALIDATION_FAILED" in str(error)
            start = backend.heartbeats
            while backend.heartbeats < start + 3:
                await asyncio.sleep(0.001)
            repairs.append(payload)
            return {"title": "repaired"}, SimpleNamespace(raw_responses=[], context_wrapper=SimpleNamespace(usage=None))

        backend.submit_execution_result = rejected_submit
        worker._repair = repair
        assert await asyncio.wait_for(worker.run_once(), 2)
        assert len(repairs) == 1 and backend.heartbeats >= 4
        assert backend.submissions == [{"title": "completed"}, {"artifact": {"title": "repaired"}}]
        assert backend.commits == ["receipt"] and not backend.failures

    asyncio.run(run())


def test_stateful_worker_provider_error_still_reports_failure_and_stops_heartbeat():
    async def run():
        backend = LeaseBackend()
        worker = lease_worker(backend)

        async def execute(claim, **kwargs):
            raise RuntimeError("provider unavailable")

        worker._execute_generation = execute
        assert await worker.run_once()
        assert len(backend.failures) == 1 and backend.failures[0][0] == "PROVIDER_TEMPORARY_FAILURE"
        assert not backend.submissions and not backend.commits
        assert not (asyncio.all_tasks() - {asyncio.current_task()})

    asyncio.run(run())


@pytest.mark.parametrize("field,value", [("attempt_id", "foreign"), ("input_snapshot_hash", "changed"), ("status", "cancelled")])
def test_stateful_worker_rejects_wrong_heartbeat_receipt(field, value):
    backend = LeaseBackend()
    worker = lease_worker(backend)

    async def heartbeat(*args, **kwargs):
        return {"data": {**backend.claim["attempt"], "status": "running", field: value}}

    backend.heartbeat_execution_attempt = heartbeat
    assert asyncio.run(worker.run_once())
    assert not backend.submissions and not backend.commits and not backend.failures


def test_stateful_worker_durable_result_receipt_is_committed_without_model_replay():
    backend = LeaseBackend()
    worker = lease_worker(backend)

    async def heartbeat(*args, **kwargs):
        return {"data": {**backend.claim["attempt"], "status": "result_received", "response_hash": "receipt"}}

    async def execute(*args, **kwargs):
        pytest.fail("durable result was generated again")

    backend.heartbeat_execution_attempt = heartbeat
    worker._execute_generation = execute
    assert asyncio.run(worker.run_once())
    assert backend.commits == ["receipt"] and not backend.submissions and not backend.failures


def test_stateful_worker_durable_approval_receipt_does_not_generate_or_commit():
    backend = LeaseBackend()
    worker = lease_worker(backend)

    async def heartbeat(*args, **kwargs):
        return {"data": {**backend.claim["attempt"], "status": "waiting_approval"}}

    async def execute(*args, **kwargs):
        pytest.fail("waiting checkpoint was generated again")

    backend.heartbeat_execution_attempt = heartbeat
    worker._execute_generation = execute
    assert asyncio.run(worker.run_once())
    assert not backend.commits and not backend.submissions and not backend.failures


def test_stateful_worker_waiting_heartbeat_does_not_cancel_slow_checkpoint_ack():
    async def run():
        backend = LeaseBackend()
        worker = lease_worker(backend)
        renew = backend.heartbeat_execution_attempt
        saved = asyncio.Event()

        async def heartbeat(*args, **kwargs):
            response = await renew(*args, **kwargs)
            if saved.is_set():
                response["data"]["status"] = "waiting_approval"
            return response

        async def checkpoint(claim):
            saved.set()
            while backend.heartbeats < 4:
                await asyncio.sleep(0.001)
            return {"data": {**claim["attempt"], "status": "waiting_approval"}}

        backend.heartbeat_execution_attempt = heartbeat
        worker._execute_and_submit = checkpoint
        assert await asyncio.wait_for(worker.run_once(), 2)
        assert backend.heartbeats >= 4
        assert not backend.commits and not backend.submissions and not backend.failures
        assert not (asyncio.all_tasks() - {asyncio.current_task()})

    asyncio.run(run())


def test_stateful_lost_lease_cancels_native_sdk_stream_without_extra_model_request():
    from agents import Agent
    from test_run_state_approval import SequenceModel

    async def run():
        backend = LeaseBackend()
        worker = lease_worker(backend)
        started, stopped = asyncio.Event(), asyncio.Event()
        renew = backend.heartbeat_execution_attempt

        class StreamingModel(SequenceModel):
            calls = 0

            async def stream_response(self, *args, **kwargs):
                self.calls += 1
                started.set()
                try:
                    await asyncio.Event().wait()
                    yield None
                finally:
                    stopped.set()

        model = StreamingModel([])
        agent = Agent(name="Stateful liveness fixture", instructions="Wait", model=model)
        worker._heartbeat_interval_seconds = 0.05

        async def heartbeat(claim, **kwargs):
            response = await renew(claim, **kwargs)
            if started.is_set():
                raise BackendError("ATTEMPT_STALE")
            return response

        async def execute(claim, **kwargs):
            await worker._run_streamed(agent, [{"role": "user", "content": "fixture"}], 5)
            pytest.fail("cancelled SDK stream returned a result")

        backend.heartbeat_execution_attempt = heartbeat
        worker._execute_generation = execute
        assert await asyncio.wait_for(worker.run_once(), 5)
        await asyncio.wait_for(stopped.wait(), 1)
        assert model.calls == 1 and not backend.submissions and not backend.commits and not backend.failures

    asyncio.run(run())
