import asyncio
from hashlib import sha256
import json

import pytest
from agents import Runner, RunConfig, function_tool

from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.memory_execution import persist_memory_pause
from test_memory_execution import setup, output
from test_memory_generation_claim import raw_claim
from test_native_workspace_transport import response, wire
from test_run_state_approval import SequenceModel, _tool_call_response
from test_stateful_execution import StreamingSequence


@pytest.mark.parametrize("wrong", [None, "generation_id", "checkpoint_hash", "status", "revision", "started", "lease_until", "error_code"])
@pytest.mark.parametrize("user_won_race", [False, True])
def test_actual_sdk_pause_persists_exact_checkpoint_without_credentials(raw_claim, wrong, user_won_race):
    async def run():
        _, claim, stage, binding, context = setup(raw_claim, SequenceModel([_tool_call_response()]))
        @function_tool(needs_approval=True)
        async def write_value(value: str) -> str:
            """Write only after a confirmed user decision."""
            pytest.fail("unapproved write ran")
        stage.agent.tools.append(write_value)
        result = await Runner.run(stage.agent, stage.input, context=context, run_config=RunConfig(tracing_disabled=True))
        assert result.interruptions and result.final_output is None
        def reply(request):
            method, path, headers, body = request
            assert method == "POST" and path.endswith("/generations/pause")
            assert headers["X-Agent-Memory-Generation-ID"] is None
            payload = json.loads(body)
            assert payload["attempt_token"] == claim.attempt_token and payload["expected_hash"] == claim.job.checkpoint_hash
            checkpoint = payload["checkpoint"]
            assert claim.attempt_token not in json.dumps(checkpoint)
            assert json.loads(checkpoint["state_json"])["context"]["context"] == {}
            digest = sha256(json.dumps(checkpoint, ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()
            job = {**claim.job.model_dump(), "status": "paused", "started": True, "lease_until": "",
                   "revision": claim.job.revision + 2, "checkpoint_hash": digest, "error_code": "MEMORY_GENERATION_USER_PAUSED" if user_won_race else "MEMORY_GENERATION_APPROVAL_PENDING"}
            if wrong:
                job[wrong] = {"generation_id": "other", "checkpoint_hash": "f" * 64, "status": "running",
                    "revision": claim.job.revision, "started": False, "lease_until": "2099-01-01T00:00:00Z", "error_code": "other"}[wrong]
            return response({"job": job})
        with wire(reply) as (backend, requests):
            if wrong:
                with pytest.raises(BackendError, match="not confirmed"):
                    await persist_memory_pause(backend, claim, "worker", result, stage, binding)
            else:
                job = await persist_memory_pause(backend, claim, "worker", result, stage, binding)
                assert job.status == "paused" and job.started and not job.lease_until
            assert len(requests) == 1
    asyncio.run(run())


def test_completed_sdk_result_cannot_be_persisted_as_approval_pause(raw_claim):
    async def run():
        _, claim, stage, binding, context = setup(raw_claim, SequenceModel([output()]))
        result = await Runner.run(stage.agent, stage.input, context=context, run_config=RunConfig(tracing_disabled=True))
        with wire() as (backend, requests):
            with pytest.raises(ValueError, match="paused"):
                await persist_memory_pause(backend, claim, "worker", result, stage, binding)
            assert not requests
    asyncio.run(run())


@pytest.mark.parametrize("reported_code", ["MEMORY_GENERATION_USER_PAUSED", "MEMORY_GENERATION_APPROVAL_PENDING"])
def test_user_pause_receipt_cannot_be_substituted_with_approval_pause(raw_claim, reported_code):
    async def run():
        _, claim, stage, binding, context = setup(raw_claim, StreamingSequence([_tool_call_response().output]))
        @function_tool
        async def write_value(value: str) -> str:
            """Complete one operation before pausing."""
            result.cancel(mode="after_turn")
            return value
        stage.agent.tools.append(write_value)
        result = Runner.run_streamed(stage.agent, stage.input, context=context, run_config=RunConfig(tracing_disabled=True))
        async for _ in result.stream_events():
            pass
        if result.run_loop_task is not None:
            await result.run_loop_task
        def reply(request):
            payload = json.loads(request[3])
            checkpoint = payload["checkpoint"]
            assert checkpoint["pause_kind"] == "user"
            digest = sha256(json.dumps(checkpoint, ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()
            return response({"job": {**claim.job.model_dump(), "status": "paused", "started": True,
                "lease_until": "", "revision": claim.job.revision + 2, "checkpoint_hash": digest,
                "error_code": reported_code}})
        with wire(reply) as (backend, requests):
            if reported_code == "MEMORY_GENERATION_USER_PAUSED":
                job = await persist_memory_pause(backend, claim, "worker", result, stage, binding, user_requested=True)
                assert job.error_code == reported_code
            else:
                with pytest.raises(BackendError, match="not confirmed"):
                    await persist_memory_pause(backend, claim, "worker", result, stage, binding, user_requested=True)
            assert len(requests) == 1
    asyncio.run(run())
