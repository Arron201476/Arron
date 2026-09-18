import asyncio
from types import SimpleNamespace

import pytest
from agents.realtime import RealtimeAgent
from agents.realtime.model import RealtimeModel

from content_agent_sidecar.native_realtime import RealtimeSessionError, managed_realtime_session


class Model(RealtimeModel):
    def __init__(self, failure=None):
        self.listeners = []
        self.options = None
        self.events = []
        self.closed = False
        self.failure = failure

    async def connect(self, options):
        self.options = options
        if self.failure:
            raise self.failure

    def add_listener(self, listener):
        self.listeners.append(listener)

    def remove_listener(self, listener):
        if listener in self.listeners:
            self.listeners.remove(listener)

    async def send_event(self, event):
        self.events.append(event)

    async def close(self):
        self.closed = True


def owner():
    return SimpleNamespace(project_id="p", conversation_id="c", agent_turn_id="t")


def config():
    return {"api_key": "fixture-not-a-key", "url": "wss://example.test/realtime",
            "initial_model_settings": {"model_name": "fixture-model"}}


def test_realtime_uses_sdk_session_and_closes_owned_model():
    async def run():
        model = Model()
        async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config()) as session:
            await session.send_audio(b"\0\0")
            await session.interrupt()
            assert model.options["initial_model_settings"]["model_name"] == "fixture-model"
            assert model.options["initial_model_settings"]["tracing"] is None
            assert len(model.events) == 2
        assert model.closed and not model.listeners
    asyncio.run(run())


@pytest.mark.parametrize("failure", [RuntimeError("connect failed"), asyncio.CancelledError()])
def test_realtime_failed_connect_also_closes_model(failure):
    async def run():
        model = Model(failure)
        with pytest.raises(type(failure)):
            async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config()):
                pytest.fail("failed session yielded")
        assert model.closed and not model.listeners
    asyncio.run(run())


@pytest.mark.parametrize("change", [{"url": "ws://example.test"}, {"api_key": ""},
    {"initial_model_settings": {}}, {"headers": {}}, {"call_id": "foreign"}])
def test_realtime_invalid_config_never_connects(change):
    async def run():
        model = Model()
        with pytest.raises(RealtimeSessionError):
            async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), {**config(), **change}):
                pytest.fail("invalid config accepted")
        assert model.options is None
    asyncio.run(run())


def test_realtime_connection_rejects_input_after_stop():
    async def run():
        model = Model()
        async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config()) as connection:
            await connection.close()
            for operation in (lambda: connection.send_audio(b"\0\0"),
                              lambda: connection.send_message("hello"), connection.interrupt):
                with pytest.raises(RealtimeSessionError, match="closed"):
                    await operation()
            assert model.events == []
    asyncio.run(run())


@pytest.mark.parametrize("audio,commit", [(b"", False), (b"x", False), (b"\0" * 48002, False),
                                         ("text", False), (b"\0\0", 1)],
                         ids=["empty", "partial-sample", "oversized", "wrong-type", "nonboolean-commit"])
def test_realtime_invalid_audio_never_reaches_model(audio, commit):
    async def run():
        model = Model()
        async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config()) as connection:
            with pytest.raises(RealtimeSessionError, match="PCM16"):
                await connection.send_audio(audio, commit=commit)
            assert model.events == []
    asyncio.run(run())


def test_realtime_empty_commit_is_supported():
    async def run():
        model = Model()
        async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config()) as connection:
            await connection.send_audio(b"", commit=True)
            assert model.events[0].audio == b"" and model.events[0].commit is True
    asyncio.run(run())


def test_closed_realtime_does_not_deliver_queued_history():
    async def run():
        async with managed_realtime_session(RealtimeAgent(name="fixture"), Model(), owner(), config()) as connection:
            await connection.close()
            assert [event async for event in connection] == []
    asyncio.run(run())


def test_realtime_model_cannot_be_reused_after_close():
    async def run():
        model = Model()
        async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config()):
            pass
        with pytest.raises(RealtimeSessionError, match="fresh"):
            async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config()):
                pytest.fail("model reused")
    asyncio.run(run())


def test_realtime_stop_cancels_blocked_send():
    async def run():
        started = asyncio.Event()
        class BlockingModel(Model):
            async def send_event(self, event):
                started.set()
                await asyncio.Future()
        model = BlockingModel()
        async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config()) as connection:
            task = asyncio.create_task(connection.send_audio(b"\0\0"))
            await asyncio.wait_for(started.wait(), timeout=2)
            await asyncio.wait_for(connection.close(), timeout=2)
            assert task.cancelled() and model.closed
    asyncio.run(run())


@pytest.mark.parametrize("during_connect", [False, True])
def test_realtime_session_deadline_closes_connection(during_connect):
    async def run():
        class SlowModel(Model):
            async def connect(self, options):
                await super().connect(options)
                if during_connect:
                    await asyncio.Future()
        model = SlowModel()
        with pytest.raises(TimeoutError):
            async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config(),
                                                max_session_seconds=1):
                await asyncio.Future()
        assert model.closed and not model.listeners
    asyncio.run(run())


@pytest.mark.parametrize("limit", [0, -1, True, 3601, 1.5])
def test_realtime_invalid_deadline_never_connects(limit):
    async def run():
        model = Model()
        with pytest.raises(RealtimeSessionError, match="session limit"):
            async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config(),
                                                max_session_seconds=limit):
                pytest.fail("invalid limit accepted")
        assert model.options is None
    asyncio.run(run())


@pytest.mark.parametrize("field,value", [("project_id", "foreign"), ("conversation_id", "foreign"),
                                        ("agent_turn_id", "foreign"), ("dispatch_generation", 1)])
def test_realtime_context_rebinding_closes_before_sending(field, value):
    async def run():
        model, context = Model(), owner()
        async with managed_realtime_session(RealtimeAgent(name="fixture"), model, context, config()) as connection:
            setattr(context, field, value)
            with pytest.raises(RealtimeSessionError, match="binding changed"):
                await connection.send_message("must not send")
            assert not model.events and model.closed
    asyncio.run(run())


@pytest.mark.parametrize("field,value", [("project_id", 1), ("conversation_id", " bad "),
                                        ("agent_turn_id", "bad\n"), ("dispatch_generation", True)])
def test_realtime_invalid_binding_never_connects(field, value):
    async def run():
        model, context = Model(), owner()
        setattr(context, field, value)
        with pytest.raises(RealtimeSessionError, match="binding"):
            async with managed_realtime_session(RealtimeAgent(name="fixture"), model, context, config()):
                pytest.fail("invalid binding accepted")
        assert model.options is None
    asyncio.run(run())
