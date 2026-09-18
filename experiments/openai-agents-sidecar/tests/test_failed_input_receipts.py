import asyncio

import pytest

from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.runtime import RuntimeCompatibilityError
from test_background_inputs import InputBackend
from test_background_pause import worker_for
from test_main_inputs import inputs
from test_main_pause import PauseBackend, make_runtime, request_fixture
from test_stateful_execution import StreamingSequence, final_item


@pytest.mark.parametrize("blocked_at", ["input", "output"])
def test_background_failure_retains_only_the_actual_native_model_receipt(tmp_path, blocked_at):
    backend = InputBackend(tmp_path / "state.json")
    backend.append("Additional requirement")
    model = StreamingSequence([[final_item({"summary": "Long output", "result": {}, "artifact_draft": None})]])
    worker = worker_for(backend, model)
    worker._guardrails = SDKGuardrailPolicy(**{f"max_{blocked_at}_chars": 1})
    assert asyncio.run(worker.run_once())
    assert backend.failed and not backend.completed
    assert backend.failed["error_code"] == f"AGENT_{blocked_at.upper()}_GUARDRAIL_REJECTED" and not backend.failed["retryable"]
    assert backend.failed.get("included_input_ids", []) == ([] if blocked_at == "input" else ["input-1"])
    assert len(model.inputs) == (0 if blocked_at == "input" else 1)


@pytest.mark.parametrize("blocked_at", ["input", "output"])
def test_main_failure_emits_receipt_before_the_terminal_error(tmp_path, blocked_at):
    async def run():
        backend = PauseBackend()
        model = StreamingSequence([[final_item({"summary": "Long output"})]])
        runtime = make_runtime(tmp_path, backend, model)
        runtime._guardrails = SDKGuardrailPolicy(**{f"max_{blocked_at}_chars": 1})
        events = []
        try:
            stream = runtime.start_execution_stream(request_fixture().model_copy(update={"additional_inputs": inputs("Additional requirement")}))
            with pytest.raises(RuntimeCompatibilityError):
                async for event in stream.events():
                    events.append(event)
        finally:
            await runtime._model_client.close()
        receipts = [event["data"]["included_input_ids"] for event in events if event["event"] == "agent.turn.inputs_included"]
        assert receipts == ([] if blocked_at == "input" else [["input-1"]])
        assert len(model.inputs) == (0 if blocked_at == "input" else 1)
        assert not backend.commits

    asyncio.run(run())
