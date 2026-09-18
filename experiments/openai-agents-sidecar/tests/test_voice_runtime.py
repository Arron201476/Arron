import asyncio

import pytest

from content_agent_sidecar.contracts import AgentExecutionRequest
from content_agent_sidecar.native_voice import VoiceInputError
from content_agent_sidecar.runtime import AgentExecutionOutcome, AgentExecutionPaused, AgentExecutionStream
from content_agent_sidecar.voice_runtime import voice_turn_workflow


def request(text="transcript"):
    return AgentExecutionRequest(project_id="p", conversation_id="c", agent_turn_id="t",
                                 idempotency_key="key", request={"content": text})


class Runtime:
    def __init__(self, operation):
        self.operation = operation
        self.requests = []
        self.stream = None

    def start_execution_stream(self, value):
        self.requests.append(value)
        self.stream = AgentExecutionStream(self.operation)
        return self.stream


@pytest.mark.parametrize("transcription", ["transcript", "  transcript\r\n", "\u3000transcript\u3000"])
def test_voice_uses_full_execution_stream_and_only_speaks_committed_text(transcription):
    async def run():
        recorded = []

        async def operation(emit):
            await emit({"event": "agent.tool.started", "data": {"tool_name": "skill_tool"}})
            return AgentExecutionOutcome({"data": {"agent_message": {"content": "committed reply"}}}, {})

        async def prepare(text):
            return request(text)

        async def persist(value, event):
            recorded.append(event["event"])
            assert value.agent_turn_id == "t"
            value.request["content"] = "sink mutation"
            return True

        runtime = Runtime(operation)
        workflow = voice_turn_workflow(runtime, "p", "c", prepare, persist)
        assert [text async for text in workflow(transcription)] == ["committed reply"]
        assert recorded == ["agent.tool.started", "agent.turn.committed"]
        assert runtime.requests[0].request["content"] == "transcript"
        with pytest.raises(VoiceInputError, match="already attempted"):
            await anext(workflow("transcript"))
        assert len(runtime.requests) == 1
    asyncio.run(run())


def test_voice_preserves_private_approval_checkpoint_without_speaking_it():
    async def run():
        recorded = []

        async def operation(_emit):
            return AgentExecutionPaused({"private": "state"}, "schema", ["sdk-call"], {})

        async def prepare(text):
            return request(text)

        async def persist(value, event):
            recorded.append(event)
            return True

        workflow = voice_turn_workflow(Runtime(operation), "p", "c", prepare, persist)
        assert [text async for text in workflow("transcript")] == []
        assert recorded[0]["event"] == "agent.turn.waiting_approval"
        assert recorded[0]["data"]["run_state"] == {"private": "state"}
    asyncio.run(run())


@pytest.mark.parametrize("receipt", [None, False, "accepted", 1, {}])
def test_voice_does_not_speak_without_explicit_durable_acknowledgement(receipt):
    async def run():
        async def operation(_emit):
            return AgentExecutionOutcome({"data": {"agent_message": {"content": "must not speak"}}}, {})

        async def prepare(text):
            return request(text)

        async def persist(*_):
            return receipt

        runtime = Runtime(operation)
        workflow = voice_turn_workflow(runtime, "p", "c", prepare, persist)
        with pytest.raises(VoiceInputError, match="persistence is unconfirmed"):
            await anext(workflow("transcript"))
        with pytest.raises(VoiceInputError, match="already attempted"):
            await anext(workflow("transcript"))
        assert len(runtime.requests) == 1 and runtime.stream._task.done()
    asyncio.run(run())


@pytest.mark.parametrize("changes", [{"project_id": "foreign"}, {"conversation_id": "foreign"},
    {"agent_turn_id": ""}, {"request": {"content": "different"}}, {"run_state": {"state": "old"}}])
def test_voice_rejects_unbound_prepared_turn(changes):
    async def run():
        async def prepare(_text):
            return request().model_copy(update=changes)

        async def persist(*_):
            pytest.fail("must not publish")

        runtime = Runtime(None)
        workflow = voice_turn_workflow(runtime, "p", "c", prepare, persist)
        with pytest.raises(VoiceInputError):
            await anext(workflow("transcript"))
        assert not runtime.requests
    asyncio.run(run())


def test_voice_sink_failure_cancels_agent_and_never_speaks():
    async def run():
        cleaned = asyncio.Event()

        async def operation(emit):
            try:
                await emit({"event": "agent.tool.started", "data": {}})
                await asyncio.Future()
            finally:
                cleaned.set()

        async def prepare(text):
            return request(text)

        async def persist(*_):
            raise RuntimeError("persistence unavailable")

        runtime = Runtime(operation)
        workflow = voice_turn_workflow(runtime, "p", "c", prepare, persist)
        with pytest.raises(RuntimeError, match="persistence unavailable"):
            await anext(workflow("transcript"))
        assert cleaned.is_set() and runtime.stream._task.done()
    asyncio.run(run())
