import asyncio
from contextlib import asynccontextmanager
from types import SimpleNamespace

import pytest

from content_agent_sidecar import runtime as runtime_module
from content_agent_sidecar.contracts import AgentExecutionRequest
from content_agent_sidecar.runtime import AgentExecutionOutcome, OpenAIAgentsRuntime
from content_agent_sidecar.turn_admission import TurnAlreadyRunningError


def request():
    return AgentExecutionRequest(project_id="p", conversation_id="c", agent_turn_id="t",
                                 idempotency_key="key", request={"content": "original"})


def runtime(operation):
    value = object.__new__(OpenAIAgentsRuntime)
    value._settings = SimpleNamespace(session_db_path="unused", model_name="fixture", release_id="fixture")
    value._execute_turn_locked = operation
    return value


@pytest.fixture(autouse=True)
def isolated_session_lock(monkeypatch):
    @asynccontextmanager
    async def turn(*_):
        yield
    monkeypatch.setattr(runtime_module, "sdk_session_turn", turn)


def test_execution_admission_is_shared_across_runtime_instances_and_modes():
    async def run():
        async def operation(*_, **__):
            return AgentExecutionOutcome({"done": True}, {})

        first, second = runtime(operation), runtime(operation)
        stream = first.start_execution_stream(request())
        with pytest.raises(TurnAlreadyRunningError):
            second.start_execution_stream(request().model_copy(update={"dispatch_generation": 1}))
        with pytest.raises(TurnAlreadyRunningError):
            await second.execute_turn(request())
        await stream.aclose()
        assert await second.execute_turn(request()) == {"done": True}
    asyncio.run(run())


def test_admission_stays_reserved_until_terminal_stream_is_consumed():
    async def run():
        async def operation(*_, **__):
            return AgentExecutionOutcome({"done": True}, {})

        instance = runtime(operation)
        stream = instance.start_execution_stream(request())
        await stream._task
        with pytest.raises(TurnAlreadyRunningError):
            instance.start_execution_stream(request())
        assert [event["event"] async for event in stream.events()] == ["agent.turn.committed"]
        await instance.execute_turn(request())
    asyncio.run(run())


def test_cancelling_stream_close_does_not_release_running_cleanup():
    async def run():
        started, cleaning, finish = asyncio.Event(), asyncio.Event(), asyncio.Event()

        async def operation(*_, **__):
            started.set()
            try:
                await asyncio.Future()
            finally:
                cleaning.set()
                await finish.wait()

        instance = runtime(operation)
        stream = instance.start_execution_stream(request())
        await started.wait()
        closing = asyncio.create_task(stream.aclose())
        await cleaning.wait()
        closing.cancel()
        await closing
        with pytest.raises(TurnAlreadyRunningError):
            instance.start_execution_stream(request())
        finish.set()
        await asyncio.gather(stream._task, return_exceptions=True)
        await asyncio.sleep(0)
        next_stream = instance.start_execution_stream(request())
        await next_stream.aclose()
    asyncio.run(run())


def test_stream_uses_snapshot_of_request_after_admission():
    async def run():
        seen = []

        async def operation(value, *_, **__):
            seen.append(value)
            return AgentExecutionOutcome({}, {})

        original = request()
        instance = runtime(operation)
        stream = instance.start_execution_stream(original)
        original.request["content"] = "changed"
        original.agent_turn_id = "other"
        assert [event async for event in stream.events()]
        assert seen[0].request["content"] == "original" and seen[0].agent_turn_id == "t"
    asyncio.run(run())


def test_admission_rejects_same_turn_from_another_thread_event_loop():
    async def run():
        async def operation(*_, **__):
            return AgentExecutionOutcome({"done": True}, {})

        instance = runtime(operation)
        stream = instance.start_execution_stream(request())
        try:
            with pytest.raises(TurnAlreadyRunningError):
                await asyncio.to_thread(lambda: asyncio.run(runtime(operation).execute_turn(request())))
        finally:
            await stream.aclose()
        assert await asyncio.to_thread(lambda: asyncio.run(runtime(operation).execute_turn(request()))) == {"done": True}
    asyncio.run(run())
