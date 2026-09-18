from __future__ import annotations

import asyncio
import json

import pytest
from agents import function_tool
from agents.items import ModelResponse
from agents.usage import Usage
from openai.types.responses import ResponseFunctionToolCall

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.background_worker import SDKBackgroundTaskWorker
from test_agent_tools import AuditBackend, StaticModel, _runtime_catalog
from test_app import settings
from test_background_worker import FakeBackend
from test_sdk_guardrails import message
from test_stateful_execution import StreamingSequence, final_item, tool_item


@pytest.mark.parametrize("decision", ["approve", "reject"])
def test_background_sdk_pause_restart_and_resume_without_replaying_model(decision) -> None:
    performed = []

    @function_tool
    async def echo_value(value: str) -> str:
        """Persist the approved fixture value."""
        performed.append(value)
        return "written"

    class DurableBackend(FakeBackend, AuditBackend):
        def __init__(self):
            FakeBackend.__init__(self)
            AuditBackend.__init__(self, _runtime_catalog(approval="always"))
            self.saved = None
            self.calls = {}

        async def claim_agent_task(self, **kwargs):
            self.claimed = False
            claim = await FakeBackend.claim_agent_task(self, **kwargs)
            if self.saved:
                claim["resume"] = {"run_state": self.saved["run_state"], "approval_decisions": [{"sdk_tool_call_id": "sdk_background", "action": decision}]}
                claim["attempt_token"] = "new-attempt-token"
            return claim

        async def pause_agent_task_for_approval(self, claim, **kwargs):
            self.saved = json.loads(json.dumps(kwargs))
            self.calls["sdk_background"]["status"] = "approved" if decision == "approve" else "rejected"
            return {}

        async def begin_agent_tool_call(self, **kwargs):
            key = kwargs["sdk_tool_call_id"]
            if key not in self.calls:
                self.calls[key] = await AuditBackend.begin_agent_tool_call(self, **kwargs)
            return self.calls[key]

    class ResumedModel(StreamingSequence):
        async def stream_response(self, *args, **kwargs):
            serialized = json.dumps(args[1])
            assert "function_call_output" in serialized
            assert ("written" if decision == "approve" else "rejected") in serialized
            async for event in super().stream_response(*args, **kwargs):
                yield event

    backend = DurableBackend()
    worker = SDKBackgroundTaskWorker(settings(), backend)
    worker._tool_provider = AgentToolProvider(backend, [echo_value])
    worker._model = StreamingSequence([[tool_item("echo_value", {"value": "fixture"}, "sdk_background")]])
    assert asyncio.run(worker.run_once())
    assert backend.saved is not None
    assert performed == []
    assert not backend.failed
    assert "attempt_token" not in json.dumps(backend.saved)
    assert backend.saved["pending_sdk_tool_call_ids"] == ["sdk_background"]

    resumed = SDKBackgroundTaskWorker(settings(), backend)
    resumed._tool_provider = AgentToolProvider(backend, [echo_value])
    resumed._model = ResumedModel([[final_item({"summary": "Finished", "result": {}, "artifact_draft": None})]])
    assert asyncio.run(resumed.run_once())
    assert not backend.failed
    assert performed == (["fixture"] if decision == "approve" else [])
    assert backend.completed["result"]["summary"] == "Finished"


def test_background_sdk_refuses_to_restart_after_a_tool_side_effect(monkeypatch) -> None:
    from agents import ModelBehaviorError
    from content_agent_sidecar.runtime import AgentContext
    from agents import Agent
    worker = SDKBackgroundTaskWorker(settings(), FakeBackend())
    context = AgentContext("project", "conversation", worker._backend)
    calls = []

    async def run(*args, **kwargs):
        calls.append(True)
        context.active_tool_calls["started-write"] = {"status": "running"}
        raise ModelBehaviorError("invalid final JSON after a write")

    monkeypatch.setattr(worker, "_run_streamed", run)
    with pytest.raises(ModelBehaviorError):
        asyncio.run(worker._run_agent(Agent(name="Fixture", model=StaticModel(message("unused"))), "Execute", context))
    assert calls == [True]


@pytest.mark.parametrize("field", ["agent_turn_id", "execution_attempt_id"])
def test_background_sdk_restore_rejects_mixed_execution_mode(field):
    from content_agent_sidecar.runtime import AgentContext, _serialize_agent_context

    context = AgentContext("project", "conversation", FakeBackend(), agent_task_id="task", agent_task_attempt_id="attempt")
    payload = _serialize_agent_context(context)
    payload[field] = "another-execution"
    with pytest.raises(ValueError, match="another execution mode"):
        asyncio.run(SDKBackgroundTaskWorker._restore_context(context, {"run_state": {"context": {"context": payload}}}))
