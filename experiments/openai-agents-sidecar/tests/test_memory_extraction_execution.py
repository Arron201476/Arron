import asyncio
from hashlib import sha256
import json

import pytest
from agents import RunConfig, function_tool

from content_agent_sidecar.backend import BackendError, _activity_headers
from content_agent_sidecar.memory_execution import execute_memory_extraction
from test_memory_execution import setup, output
from test_memory_generation_claim import raw_claim
from test_run_state_approval import SequenceModel, _tool_call_response
from test_native_workspace_transport import response as http_response, wire


@pytest.mark.parametrize("mode", ["memory", "empty", "pause", "wrong_receipt", "timeout", "cancel"])
def test_sdk_extraction_ends_only_with_confirmed_durable_receipt(raw_claim, mode):
    async def run():
        response = _tool_call_response() if mode == "pause" else output()
        if mode == "empty":
            response.output[0].content[0].text = json.dumps({"rollout_slug": "", "rollout_summary": "", "raw_memory": ""})
        backend, claim, stage, binding, context = setup(raw_claim, SequenceModel([response]))
        sent = []
        @function_tool(needs_approval=True)
        async def write_value(value: str) -> str:
            """This fixture must remain interrupted."""
            pytest.fail("unapproved tool executed")
        if mode == "pause":
            stage.agent.tools.append(write_value)

        async def persist(operation, payload):
            assert _activity_headers.get() is None
            assert backend.starts == 1
            assert payload["attempt_token"] == claim.attempt_token
            assert payload["attempt"] == claim.job.attempt
            assert payload["generation_id"] == claim.job.generation_id
            sent.append(operation)
            before = backend.renewals
            await asyncio.sleep(.03)
            assert backend.renewals == before
            if mode == "timeout":
                await asyncio.sleep(1)
            if mode == "cancel":
                raise asyncio.CancelledError
            value = payload["checkpoint"] if mode == "pause" else payload["output"]
            digest = sha256(json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()
            if mode == "pause":
                assert operation == "pause"
                return {"job": {**claim.job.model_dump(), "status": "paused", "started": True,
                    "lease_until": "", "checkpoint_hash": digest, "revision": claim.job.revision + 2,
                    "error_code": "MEMORY_GENERATION_APPROVAL_PENDING"}}
            assert operation == "extraction"
            return {"receipt": {"generation_id": claim.job.generation_id,
                "content_hash": "0" * 64 if mode == "wrong_receipt" else digest,
                "has_memory": mode != "empty"}}

        backend.memory_generation_request = persist
        call = execute_memory_extraction(backend, claim, "worker", stage, binding, context=context,
            run_config=RunConfig(tracing_disabled=True), interval_seconds=.01,
            request_timeout_seconds=.1 if mode == "timeout" else 10)
        if mode in {"wrong_receipt", "timeout"}:
            with pytest.raises(BackendError) as error:
                await call
            if mode == "timeout":
                assert error.value.code == "MEMORY_GENERATION_COMMIT_UNCONFIRMED"
        elif mode == "cancel":
            with pytest.raises(asyncio.CancelledError):
                await call
        else:
            result = await call
            if mode == "pause":
                assert result.pause.status == "paused" and result.extraction is None
            else:
                assert result.pause is None and result.extraction["has_memory"] == (mode == "memory")
        assert len(sent) == 1 and _activity_headers.get() is None
    asyncio.run(run())


def test_extraction_executor_rejects_other_phase_before_start(raw_claim):
    backend, claim, stage, binding, context = setup(raw_claim, SequenceModel([output()]))
    other = claim.model_copy(update={"job": claim.job.model_copy(update={"phase": "consolidation"})})
    with pytest.raises(BackendError, match="another phase"):
        asyncio.run(execute_memory_extraction(backend, other, "worker", stage, binding,
            context=context, run_config=RunConfig(tracing_disabled=True)))
    assert backend.starts == 0


def test_extraction_actual_backend_client_start_and_commit(raw_claim):
    async def run():
        _, claim, stage, binding, context = setup(raw_claim, SequenceModel([output()]))
        def reply(request):
            method, path, headers, body = request
            assert method == "POST" and not headers["X-Agent-Memory-Generation-ID"]
            payload = json.loads(body)
            assert payload["attempt_token"] == claim.attempt_token
            if path.endswith("/start"):
                return http_response({"job": {**claim.job.model_dump(), "started": True, "revision": claim.job.revision + 1}})
            assert path.endswith("/extraction")
            digest = sha256(json.dumps(payload["output"], ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()
            return http_response({"receipt": {"generation_id": claim.job.generation_id, "content_hash": digest, "has_memory": True}})
        with wire(reply, reply) as (backend, requests):
            context.backend = backend
            result = await execute_memory_extraction(backend, claim, "worker", stage, binding,
                context=context, run_config=RunConfig(tracing_disabled=True))
            assert result.extraction["has_memory"] and result.pause is None
            assert [request[1].rsplit("/", 1)[-1] for request in requests] == ["start", "extraction"]
    asyncio.run(run())
