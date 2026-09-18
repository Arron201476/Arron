import asyncio
from copy import deepcopy
import json

import pytest
from agents import ModelBehaviorError, function_tool

from test_background_pause import PauseBackend, PausingModel, worker_for
from test_stateful_execution import StreamingSequence, final_item, tool_item
from content_agent_sidecar.guardrails import SDKGuardrailPolicy


class InputBackend(PauseBackend):
    def __init__(self, path, **kwargs):
        super().__init__(path, **kwargs)
        self.inputs = []

    def append(self, content):
        self.inputs.append({"input_id": f"input-{len(self.inputs)+1}", "agent_task_id": "agt_1", "sequence": len(self.inputs)+1, "content": content, "status": "received"})

    async def claim_agent_task(self, **kwargs):
        claim = await super().claim_agent_task(**kwargs)
        if claim is not None:
            claim["additional_inputs"] = deepcopy(self.inputs)
        return claim


def test_background_append_pause_rebuild_and_second_append_do_not_replay(tmp_path):
    backend = InputBackend(tmp_path / "state.json")
    writes = []

    @function_tool
    async def echo_value(value: str) -> str:
        writes.append(value)
        return "saved-once"

    class AppendModel(PausingModel):
        async def stream_response(self, *args, **kwargs):
            backend.append("New requirement")
            async for event in super().stream_response(*args, **kwargs):
                yield event

    async def run():
        assert await worker_for(backend, AppendModel(backend, [[tool_item("echo_value", {"value": "first"}, "save")]]), echo_value).run_once()
        saved = json.loads(backend.checkpoint.read_text(encoding="utf-8"))
        assert not saved.get("included_input_ids") and writes == ["first"]
        backend.resume()
        second = PausingModel(backend, [[tool_item("echo_value", {"value": "second"}, "read-again")]])
        assert await worker_for(backend, second, echo_value).run_once()
        assert [item["content"] for item in second.inputs[0] if item.get("role") == "user"][-1:] == ["New requirement"]
        saved = json.loads(backend.checkpoint.read_text(encoding="utf-8"))
        assert saved["included_input_ids"] == ["input-1"] and not backend.failed
        backend.append("Another requirement")
        backend.resume()
        final = StreamingSequence([[final_item({"summary": "Done", "result": {}, "artifact_draft": None})]])
        assert await worker_for(backend, final, echo_value).run_once()
        messages = [item["content"] for item in final.inputs[0] if item.get("role") == "user"]
        assert messages[-2:] == ["New requirement", "Another requirement"]
        assert messages.count("New requirement") == 1
        assert backend.completed["included_input_ids"] == ["input-1", "input-2"]
        assert writes == ["first", "second"] and not backend.failed

    asyncio.run(run())


@pytest.mark.parametrize("decision", ["approve", "reject"])
def test_background_appended_input_waits_for_old_tool_decision(tmp_path, decision):
    backend = InputBackend(tmp_path / "state.json", approval="always")
    writes = []

    @function_tool
    async def echo_value(value: str) -> str:
        writes.append(value)
        return "saved"

    async def run():
        assert await worker_for(backend, PausingModel(backend, [[tool_item("echo_value", {"value": "original"}, "save")]]), echo_value).run_once()
        backend.append("Updated instruction")
        assert writes == []
        backend.resume(decision)
        model = StreamingSequence([[final_item({"summary": "Done", "result": {}, "artifact_draft": None})]])
        assert await worker_for(backend, model, echo_value).run_once()
        assert writes == (["original"] if decision == "approve" else [])
        assert backend.completed["included_input_ids"] == ["input-1"]
        assert model.inputs[0][-1] == {"role": "user", "content": "Updated instruction"}

    asyncio.run(run())


def test_queued_inputs_survive_zero_model_pause_and_keep_original_request(tmp_path):
    backend = InputBackend(tmp_path / "state.json")
    backend.append("Before first model")

    async def run():
        claim = await backend.claim_agent_task()
        backend.status = "pausing"
        original_claim = backend.claim_agent_task
        async def claimed(**kwargs): return claim
        backend.claim_agent_task = claimed
        no_model = StreamingSequence([])
        assert await worker_for(backend, no_model).run_once()
        assert not no_model.inputs and not backend.failed
        saved = json.loads(backend.checkpoint.read_text(encoding="utf-8"))
        assert not saved.get("included_input_ids")
        backend.claim_agent_task = original_claim
        backend.resume()
        model = StreamingSequence([[final_item({"summary": "Done", "result": {}, "artifact_draft": None})]])
        assert await worker_for(backend, model).run_once()
        messages = [item["content"] for item in model.inputs[0] if item.get("role") == "user"]
        assert len(messages) == 2 and messages[1] == "Before first model" and "USER_REQUEST:" in messages[0]
        assert backend.completed["included_input_ids"] == ["input-1"] and not backend.failed

    asyncio.run(run())


@pytest.mark.parametrize("field,value", [("input_id", ""), ("agent_task_id", "foreign"), ("sequence", 0), ("content", " ")])
def test_invalid_input_identity_fails_before_model(tmp_path, field, value):
    backend = InputBackend(tmp_path / "state.json")
    backend.append("Valid")
    backend.inputs[0][field] = value
    model = StreamingSequence([])
    assert asyncio.run(worker_for(backend, model).run_once())
    assert backend.failed["error_code"] == "AGENT_RUN_STATE_INVALID" and not backend.failed["retryable"]
    assert model.inputs == [] and backend.completed is None


def test_background_json_fallback_keeps_additional_native_user_messages(tmp_path):
    backend = InputBackend(tmp_path / "state.json")
    backend.append("Keep this requirement during output compatibility retry")
    model = StreamingSequence([ModelBehaviorError("unsupported structured output"), [final_item({"summary": "Done", "result": {}, "artifact_draft": None})]])
    assert asyncio.run(worker_for(backend, model).run_once())
    assert not backend.failed and backend.completed["included_input_ids"] == ["input-1"]
    assert model.inputs[0] == model.inputs[1]
    assert model.inputs[1][-1]["content"] == backend.inputs[0]["content"]


def test_pending_input_guardrail_rejection_is_not_a_retryable_provider_failure(tmp_path):
    backend = InputBackend(tmp_path / "state.json")

    @function_tool
    async def echo_value(value: str) -> str:
        return value

    async def run():
        assert await worker_for(backend, PausingModel(backend, [[tool_item("echo_value", {"value": "read"}, "save")]]), echo_value).run_once()
        original_checkpoint = backend.checkpoint.read_text(encoding="utf-8")
        backend.append("x" * 11)
        backend.resume()
        model = StreamingSequence([])
        worker = worker_for(backend, model, echo_value)
        worker._guardrails = SDKGuardrailPolicy(max_input_chars=10)
        assert await worker.run_once()
        assert backend.failed["error_code"] == "AGENT_INPUT_GUARDRAIL_REJECTED" and not backend.failed["retryable"]
        assert backend.completed is None and model.inputs == []
        assert backend.checkpoint.read_text(encoding="utf-8") == original_checkpoint

    asyncio.run(run())
