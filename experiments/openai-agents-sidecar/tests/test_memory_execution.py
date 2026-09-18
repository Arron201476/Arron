import asyncio
from dataclasses import replace
import json
from datetime import datetime, timedelta, timezone

import pytest
from agents import Agent, RunConfig, function_tool

from content_agent_sidecar.backend import BackendError, _activity_headers
from content_agent_sidecar.memory_checkpoint import MemoryGenerationBinding
from content_agent_sidecar.memory_execution import run_memory_stage, execute_memory_extraction
from hashlib import sha256
from test_stateful_execution import StreamingSequence
from test_run_state_approval import _tool_call_response
from content_agent_sidecar.native_memory import memory_extraction_stage
from content_agent_sidecar.runtime import AgentContext
from test_memory_generation_claim import POLICY, decode, raw_claim
from test_run_state_approval import SequenceModel, _text_response


class Backend:
    def __init__(self, claim, *, fail_start=False, fail_renew=False):
        self.claim, self.fail_start, self.fail_renew = claim, fail_start, fail_renew
        self.starts = self.renewals = 0

    async def start_memory_generation(self, claim, worker):
        assert _activity_headers.get() is None
        self.starts += 1
        if self.fail_start:
            raise BackendError("start unconfirmed")
        return claim.job.model_copy(update={"started": True, "revision": claim.job.revision + 1})

    async def renew_memory_generation(self, claim, worker, job, seconds):
        assert _activity_headers.get() is None
        self.renewals += 1
        if self.fail_renew:
            raise BackendError("PRIVATE_TRANSPORT_DETAIL")
        return job


def setup(raw, model, **backend_options):
    claim = decode(raw)
    backend = Backend(claim, **backend_options)
    context = AgentContext(project_id=claim.job.project_id, conversation_id=claim.conversation_id, backend=backend,
        memory_generation_id=claim.job.generation_id, memory_generation_attempt=claim.job.attempt, attempt_token=claim.attempt_token)
    stage = memory_extraction_stage(Agent(name="template", model=model), claim.source.rollout_jsonl)
    binding = MemoryGenerationBinding(generation_id=claim.job.generation_id, phase=claim.job.phase,
        project_id=claim.job.project_id, user_id=claim.job.user_id, source_hash=claim.job.source_hash,
        base_version=claim.job.base_version, base_hash=claim.job.base_hash, model_id=claim.job.model_id, policy_hash=POLICY)
    return backend, claim, stage, binding, context


def output():
    message = _text_response()
    message.output[0].content[0].text = json.dumps({"rollout_slug": "fixture", "rollout_summary": "summary", "raw_memory": "memory"})
    return message


def test_cooperative_pause_arrives_via_renewal_and_commits_user_checkpoint(raw_claim):
    async def run():
        backend, claim, stage, binding, context = setup(raw_claim, StreamingSequence([_tool_call_response().output]))
        entered, requested = asyncio.Event(), asyncio.Event()
        writes = []
        @function_tool
        async def write_value(value: str) -> str:
            """Finish an in-flight write at a safe boundary."""
            entered.set()
            await requested.wait()
            await asyncio.sleep(.03)
            writes.append(value)
            return "written"
        stage.agent.tools.append(write_value)
        async def renew(actual, worker, job, seconds):
            await entered.wait()
            requested.set()
            return job.model_copy(update={"error_code": "MEMORY_GENERATION_PAUSE_REQUESTED", "revision": job.revision + 1})
        async def persist(operation, payload):
            assert operation == "pause" and payload["checkpoint"]["pause_kind"] == "user"
            assert writes == ["approved payload"]
            digest = sha256(json.dumps(payload["checkpoint"], ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()
            return {"job": {**claim.job.model_dump(), "started": True, "status": "paused", "lease_until": "",
                "error_code": "MEMORY_GENERATION_USER_PAUSED", "revision": claim.job.revision + 3, "checkpoint_hash": digest}}
        backend.renew_memory_generation = renew
        backend.memory_generation_request = persist
        outcome = await asyncio.wait_for(execute_memory_extraction(backend, claim, "worker", stage, binding,
            context=context, run_config=RunConfig(tracing_disabled=True), cooperative_pause=True,
            interval_seconds=.01, request_timeout_seconds=2), 5)
        assert outcome.pause.error_code == "MEMORY_GENERATION_USER_PAUSED" and outcome.extraction is None
        assert _activity_headers.get() is None
    asyncio.run(run())


@pytest.mark.parametrize("lease_failure", [False, True])
def test_cooperative_stream_drains_model_on_cancel_or_lost_lease(raw_claim, lease_failure):
    async def run():
        entered, stopped = asyncio.Event(), asyncio.Event()
        class Waiting(StreamingSequence):
            async def stream_response(self, *args, **kwargs):
                entered.set()
                try:
                    await asyncio.Event().wait()
                    async for event in super().stream_response(*args, **kwargs):
                        yield event
                finally:
                    stopped.set()
        backend, claim, stage, binding, context = setup(raw_claim, Waiting([output().output]))
        async def renew(*args):
            await entered.wait()
            if lease_failure:
                raise BackendError("PRIVATE_LEASE_FAILURE")
            return args[2]
        backend.renew_memory_generation = renew
        task = asyncio.create_task(run_memory_stage(backend, claim, "worker", stage, binding,
            context=context, run_config=RunConfig(tracing_disabled=True), cooperative_pause=True,
            interval_seconds=.01, request_timeout_seconds=2))
        await asyncio.wait_for(entered.wait(), 2)
        if lease_failure:
            with pytest.raises(BackendError) as failure:
                await asyncio.wait_for(task, 3)
            assert failure.value.code == "MEMORY_GENERATION_LEASE_UNCONFIRMED"
            assert "PRIVATE_LEASE_FAILURE" not in str(failure.value)
        else:
            task.cancel()
            with pytest.raises(asyncio.CancelledError):
                await task
        assert stopped.is_set() and task.done() and _activity_headers.get() is None
    asyncio.run(run())


def test_stage_uses_actual_sdk_runner_only_after_start_ack(raw_claim):
    class Observed(SequenceModel):
        async def get_response(self, *args, **kwargs):
            assert backend.starts == 1
            assert _activity_headers.get()["X-Agent-Memory-Generation-ID"] == claim.job.generation_id
            return await super().get_response(*args, **kwargs)
    backend, claim, stage, binding, context = setup(raw_claim, Observed([output()]))
    result = asyncio.run(run_memory_stage(backend, claim, "worker", stage, binding, context=context, run_config=RunConfig(tracing_disabled=True)))
    assert result.final_output.raw_memory == "memory"
    assert _activity_headers.get() is None


def test_unconfirmed_start_or_changed_source_never_calls_model(raw_claim):
    model = SequenceModel([output()])
    backend, claim, stage, binding, context = setup(raw_claim, model, fail_start=True)
    with pytest.raises(BackendError, match="start unconfirmed"):
        asyncio.run(run_memory_stage(backend, claim, "worker", stage, binding, context=context, run_config=RunConfig(tracing_disabled=True)))
    assert len(model.responses) == 1
    with pytest.raises(BackendError, match="frozen source"):
        asyncio.run(run_memory_stage(backend, claim, "worker", replace(stage, input="different"), binding, context=context, run_config=RunConfig(tracing_disabled=True)))
    assert backend.starts == 1


def test_renewal_failure_cancels_model_and_clears_scope(raw_claim):
    class Slow(SequenceModel):
        cancelled = False
        async def get_response(self, *args, **kwargs):
            self.entered.set()
            try:
                await asyncio.sleep(5)
                return await super().get_response(*args, **kwargs)
            finally:
                self.cancelled = True
    model = Slow([output()])
    backend, claim, stage, binding, context = setup(raw_claim, model, fail_renew=True)
    async def run():
        model.entered = asyncio.Event()
        original_renew = backend.renew_memory_generation
        async def renew_after_model_entry(*args):
            await model.entered.wait()
            return await original_renew(*args)
        backend.renew_memory_generation = renew_after_model_entry
        with pytest.raises(BackendError) as error:
            await run_memory_stage(backend, claim, "worker", stage, binding, context=context,
                run_config=RunConfig(tracing_disabled=True), interval_seconds=.01, request_timeout_seconds=10)
        assert error.value.code == "MEMORY_GENERATION_LEASE_UNCONFIRMED"
        assert "PRIVATE_TRANSPORT_DETAIL" not in str(error.value)
        assert asyncio.current_task().cancelling() == 0
        assert _activity_headers.get() is None
    asyncio.run(run())
    assert model.cancelled and backend.renewals == 1 and len(model.responses) == 1


def test_external_cancellation_is_not_reported_as_lease_failure(raw_claim):
    async def run():
        entered = asyncio.Event()
        class Slow(SequenceModel):
            async def get_response(self, *args, **kwargs):
                entered.set()
                await asyncio.sleep(5)
                return await super().get_response(*args, **kwargs)
        backend, claim, stage, binding, context = setup(raw_claim, Slow([output()]))
        task = asyncio.create_task(run_memory_stage(backend, claim, "worker", stage, binding,
            context=context, run_config=RunConfig(tracing_disabled=True)))
        await asyncio.wait_for(entered.wait(), 2)
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert task.cancelled() and _activity_headers.get() is None
    asyncio.run(run())


def test_successful_renewals_allow_one_sdk_result(raw_claim):
    class Slow(SequenceModel):
        async def get_response(self, *args, **kwargs):
            await asyncio.sleep(.05)
            return await super().get_response(*args, **kwargs)
    backend, claim, stage, binding, context = setup(raw_claim, Slow([output()]))
    result = asyncio.run(run_memory_stage(backend, claim, "worker", stage, binding, context=context,
        run_config=RunConfig(tracing_disabled=True), interval_seconds=.01, request_timeout_seconds=.01))
    assert backend.starts == 1 and backend.renewals > 0 and result.final_output.raw_memory == "memory"


def test_short_remaining_lease_never_starts_model(raw_claim):
    model = SequenceModel([output()])
    backend, claim, stage, binding, context = setup(raw_claim, model)
    async def short_start(*args):
        return claim.job.model_copy(update={"started": True, "lease_until": (datetime.now(timezone.utc) + timedelta(seconds=1)).isoformat()})
    backend.start_memory_generation = short_start
    with pytest.raises(BackendError, match="insufficient"):
        asyncio.run(run_memory_stage(backend, claim, "worker", stage, binding, context=context, run_config=RunConfig(tracing_disabled=True)))
    assert len(model.responses) == 1
