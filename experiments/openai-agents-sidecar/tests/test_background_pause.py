import asyncio
import json

import pytest
from agents import function_tool

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.background_worker import SDKBackgroundTaskWorker
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from test_agent_tools import AuditBackend, _runtime_catalog
from test_app import settings
from test_background_worker import FakeBackend
from test_stateful_execution import StreamingSequence, final_item, tool_item


class PauseBackend(FakeBackend, AuditBackend):
    def __init__(self, checkpoint, *, approval="never"):
        AuditBackend.__init__(self, _runtime_catalog(approval=approval))
        FakeBackend.__init__(self)
        self.checkpoint = checkpoint
        self.status = "running"
        self.observed = asyncio.Event()
        self.calls = {}
        self.tool_results = []
        self.decisions = []
        self.tokens = []
        self.receipt_lost = False

    async def claim_agent_task(self, **kwargs):
        if self.status != "running":
            return None
        self.claimed = False
        claim = await FakeBackend.claim_agent_task(self, **kwargs)
        claim["attempt_token"] = f"private-attempt-{len(self.tokens)}"
        self.tokens.append(claim["attempt_token"])
        if self.checkpoint.exists():
            saved = json.loads(self.checkpoint.read_text(encoding="utf-8"))
            claim["resume"] = {"run_state": saved["run_state"], "approval_decisions": self.decisions}
        return claim

    async def update_agent_task_progress(self, claim, **kwargs):
        await FakeBackend.update_agent_task_progress(self, claim, **kwargs)
        if self.status == "pausing":
            self.observed.set()
        return {"data": {"status": self.status}}

    async def request_pause(self):
        self.observed = asyncio.Event()
        self.status = "pausing"
        await asyncio.wait_for(self.observed.wait(), 2)
        await asyncio.sleep(0)

    async def complete_agent_task_pause(self, claim, **kwargs):
        assert self.status == "pausing"
        self.checkpoint.write_text(json.dumps(kwargs), encoding="utf-8")
        self.status = "paused"
        if self.receipt_lost:
            raise BackendError("checkpoint response connection lost")
        return {"data": {"status": self.status}}

    async def pause_agent_task_for_approval(self, claim, **kwargs):
        raise AssertionError("A user pause must retain the user-paused state, including pending approvals")

    async def begin_agent_tool_call(self, **kwargs):
        key = kwargs["sdk_tool_call_id"]
        if key not in self.calls:
            self.calls[key] = await AuditBackend.begin_agent_tool_call(self, **kwargs)
        return self.calls[key]

    async def complete_agent_tool_call(self, agent_tool_call_id, **payload):
        self.tool_results.append({"agent_tool_call_id": agent_tool_call_id, **payload})
        return {"agent_tool_call_id": agent_tool_call_id, "status": "completed"}

    def resume(self, decision=None):
        assert self.status == "paused"
        self.status = "running"
        self.decisions = []
        if decision:
            self.calls["save"]["status"] = "approved" if decision == "approve" else "rejected"
            self.decisions = [{"sdk_tool_call_id": "save", "action": decision}]


class PausingModel(StreamingSequence):
    def __init__(self, backend, responses):
        super().__init__(responses)
        self.backend = backend

    async def stream_response(self, *args, **kwargs):
        await self.backend.request_pause()
        async for event in super().stream_response(*args, **kwargs):
            yield event


def worker_for(backend, model, tool=None):
    worker = SDKBackgroundTaskWorker(settings(), backend)
    worker._heartbeat_interval_seconds = 0.001
    worker._model = model
    if tool is not None:
        worker._tool_provider = AgentToolProvider(backend, [tool])
    return worker


@pytest.mark.parametrize("decision", [None, "approve", "reject"])
def test_worker_user_pause_rebuild_preserves_sdk_outputs_and_approval(tmp_path, decision):
    backend = PauseBackend(tmp_path / "run-state.json", approval="always" if decision else "never")
    performed = []
    pause_after_write = False

    @function_tool
    async def echo_value(value: str) -> str:
        """Return the fixture result, persisting only when the approved fixture requests it."""
        performed.append(value)
        if pause_after_write:
            await backend.request_pause()
        return "saved-once"

    async def run():
        nonlocal pause_after_write
        first_model = PausingModel(backend, [[tool_item("echo_value", {"value": "fixture"}, "save")]])
        assert await worker_for(backend, first_model, echo_value).run_once()
        assert backend.status == "paused" and not backend.failed and backend.completed is None
        state = json.loads(backend.checkpoint.read_text(encoding="utf-8"))
        assert state["pending_sdk_tool_call_ids"] == (["save"] if decision else [])
        assert "private-attempt" not in json.dumps(state)
        assert performed == ([] if decision else ["fixture"])
        assert len(first_model.inputs) == 1 and not first_model.responses
        assert not await worker_for(backend, StreamingSequence([]), echo_value).run_once()

        backend.resume(decision)
        if decision == "approve":
            # Pause again after the approved write but before the next model turn.
            pause_after_write = True
            no_new_model_turn = StreamingSequence([])
            assert await worker_for(backend, no_new_model_turn, echo_value).run_once()
            assert backend.status == "paused" and performed == ["fixture"]
            assert not no_new_model_turn.inputs and not backend.failed
            saved = json.loads(backend.checkpoint.read_text(encoding="utf-8"))
            assert saved["pending_sdk_tool_call_ids"] == []
            backend.resume()

        model = StreamingSequence([[final_item({"summary": "Finished", "result": {"ok": True}, "artifact_draft": None})]])
        assert await worker_for(backend, model, echo_value).run_once()
        assert not backend.failed and backend.completed["result"]["summary"] == "Finished"
        assert performed == ([] if decision == "reject" else ["fixture"])
        assert len(backend.begun) == 1 and len(backend.tool_results) == (0 if decision == "reject" else 1)
        assert len(backend.tokens) == len(set(backend.tokens))
        assert backend.completed["usage"]["total_tokens"] == 24
        outputs = [item for item in model.inputs[0] if item.get("type") == "function_call_output" and item.get("call_id") == "save"]
        assert len(outputs) == 1
        assert ("rejected" if decision == "reject" else "saved-once") in outputs[0]["output"]

    asyncio.run(run())


def test_worker_pause_before_first_model_request_can_resume(tmp_path):
    backend = PauseBackend(tmp_path / "run-state.json")

    async def run():
        # The claim was already issued when the user's pause arrived.
        claim = await backend.claim_agent_task()
        backend.status = "pausing"

        async def already_claimed(**kwargs):
            return claim

        original_claim = backend.claim_agent_task
        backend.claim_agent_task = already_claimed
        initial_model = StreamingSequence([])
        assert await worker_for(backend, initial_model).run_once()
        assert backend.status == "paused" and not initial_model.inputs and not backend.failed
        backend.claim_agent_task = original_claim
        backend.resume()
        resumed_model = StreamingSequence([[final_item({"summary": "First turn", "result": {}, "artifact_draft": None})]])
        assert await worker_for(backend, resumed_model).run_once()
        assert not backend.failed and backend.completed["result"]["summary"] == "First turn"
        assert len(resumed_model.inputs) == 1

    asyncio.run(run())


def test_zero_model_resume_runs_initial_input_guardrail_before_any_model(tmp_path):
    backend = PauseBackend(tmp_path / "run-state.json")

    async def run():
        original_claim = backend.claim_agent_task
        for _ in range(2):
            claim = await original_claim()
            backend.status = "pausing"

            async def claimed(**kwargs):
                return claim

            backend.claim_agent_task = claimed
            model = StreamingSequence([])
            assert await worker_for(backend, model).run_once()
            assert backend.status == "paused" and not model.inputs and not backend.failed
            backend.resume()
        backend.claim_agent_task = original_claim
        model = StreamingSequence([])
        worker = worker_for(backend, model)
        worker._guardrails = SDKGuardrailPolicy(max_input_chars=1)
        assert await worker.run_once()
        assert backend.failed["error_code"] == "AGENT_INPUT_GUARDRAIL_REJECTED" and not backend.failed["retryable"]
        assert not model.inputs and backend.completed is None

    asyncio.run(run())


def test_worker_checkpoint_receipt_loss_does_not_fail_or_replay_saved_run(tmp_path):
    backend = PauseBackend(tmp_path / "run-state.json")
    backend.receipt_lost = True

    @function_tool
    async def echo_value(value: str) -> str:
        """Read fixture data."""
        return value

    async def run():
        first = worker_for(backend, PausingModel(backend, [[tool_item("echo_value", {"value": "read"}, "save")]]), echo_value)
        with pytest.raises(BackendError, match="connection lost"):
            await first.run_once()
        assert backend.status == "paused" and backend.failed is None and backend.completed is None
        assert not await worker_for(backend, StreamingSequence([]), echo_value).run_once()
        assert len(backend.begun) == 1 and len(backend.tool_results) == 1
        backend.receipt_lost = False
        backend.resume()
        assert await worker_for(backend, StreamingSequence([[final_item({"summary": "Recovered", "result": {}, "artifact_draft": None})]]), echo_value).run_once()
        assert backend.completed["result"]["summary"] == "Recovered" and len(backend.begun) == 1

    asyncio.run(run())


def test_worker_completed_final_output_wins_pause_race(tmp_path):
    backend = PauseBackend(tmp_path / "run-state.json")
    model = PausingModel(backend, [[final_item({"summary": "Already finished", "result": {}, "artifact_draft": None})]])
    assert asyncio.run(worker_for(backend, model).run_once())
    assert not backend.failed and backend.completed["result"]["summary"] == "Already finished"
    assert not backend.checkpoint.exists()


@pytest.mark.parametrize("decisions", [None, [], [{"sdk_tool_call_id": "foreign", "action": "approve"}]])
def test_worker_user_pause_never_skips_pending_sdk_approval(tmp_path, decisions):
    backend = PauseBackend(tmp_path / "run-state.json", approval="always")
    performed = []

    @function_tool
    async def echo_value(value: str) -> str:
        """Perform only after approval."""
        performed.append(value)
        return "written"

    async def run():
        assert await worker_for(backend, PausingModel(backend, [[tool_item("echo_value", {"value": "fixture"}, "save")]]), echo_value).run_once()
        backend.resume()
        backend.decisions = decisions
        model = StreamingSequence([])
        assert await worker_for(backend, model, echo_value).run_once()
        assert backend.failed["error_code"] == "AGENT_RUN_STATE_INVALID" and backend.failed["retryable"] is False
        assert not model.inputs and not performed and not backend.tool_results

    asyncio.run(run())
