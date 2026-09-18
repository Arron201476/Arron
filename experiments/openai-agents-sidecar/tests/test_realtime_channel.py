import asyncio
import json

import pytest
from agents.realtime import RealtimeAgent

from content_agent_sidecar.native_realtime import managed_realtime_session
from content_agent_sidecar.realtime_channel import _decode_frame, serve_realtime_channel
from test_native_realtime import Model, config, owner


def frame(sequence, kind, **payload):
    return json.dumps({"session_id": "session", "generation": 1, "sequence": sequence, "type": kind, **payload})


def test_channel_routes_audio_text_and_stop_through_sdk():
    async def run():
        model, messages = Model(), []
        pending = iter([frame(1, "audio", audio="AAA=", commit=False), frame(2, "text", text="hello"), frame(3, "stop")])
        async def receive():
            return next(pending, None)
        async def send(value):
            messages.append(value)
        async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config()) as connection:
            await serve_realtime_channel(connection, receive, send, session_id="session", generation=1)
        assert model.closed and len(model.events) == 2
        assert messages[0]["payload"]["type"] == "ready"
        assert [m["payload"]["input_sequence"] for m in messages if m["payload"]["type"] == "input_ack"] == [1, 2, 3]
        assert [m["sequence"] for m in messages] == list(range(1, len(messages) + 1))
    asyncio.run(run())


def test_channel_rejects_replayed_input_and_closes_connection():
    async def run():
        model = Model()
        pending = iter([frame(1, "text", text="once"), frame(1, "text", text="replay")])
        async def receive():
            return next(pending, None)
        async def send(_):
            pass
        async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config()) as connection:
            with pytest.raises(ValueError, match="sequence"):
                await serve_realtime_channel(connection, receive, send, session_id="session", generation=1)
        assert model.closed and len(model.events) == 1
    asyncio.run(run())


@pytest.mark.parametrize("raw", ['{"type":"text","type":"stop"}', '{"sequence":NaN}', '[]'])
def test_channel_rejects_ambiguous_json(raw):
    with pytest.raises(ValueError):
        _decode_frame(raw)


def test_channel_stop_waits_for_delayed_stop_acknowledgement():
    async def run():
        model, messages = Model(), []
        async def receive():
            return frame(1, "stop")
        async def send(value):
            if value["payload"]["type"] == "input_ack":
                await asyncio.sleep(0.02)
            messages.append(value)
        async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config()) as connection:
            await serve_realtime_channel(connection, receive, send, session_id="session", generation=1)
        assert model.closed
        assert any(message["payload"].get("input_sequence") == 1 for message in messages)
    asyncio.run(run())
