import asyncio
import base64
from array import array
from types import SimpleNamespace

import pytest

from content_agent_sidecar import voice_channel
from content_agent_sidecar.native_voice import VoiceInputError


def lifecycle(name):
    return SimpleNamespace(type="voice_stream_event_lifecycle", event=name)


class Pipeline:
    def __init__(self, events, failure=None):
        self.events = events
        self.failure = failure
        self.closed = False
        self.started = False

    async def run(self, audio):
        self.started = True
        return self

    async def stream(self):
        try:
            for event in self.events:
                yield event
            if self.failure:
                raise self.failure
        finally:
            self.closed = True


def test_voice_channel_splits_pcm_and_confirms_after_cleanup():
    async def run():
        pipeline = Pipeline([lifecycle("turn_started"), SimpleNamespace(
            type="voice_stream_event_audio", data=array("h", [1] * 25000)), lifecycle("session_ended")])
        sent = []

        async def send(message):
            if message["payload"]["type"] == "session_ended":
                assert pipeline.closed
            sent.append(message)

        await voice_channel.serve_voice_turn(pipeline, object(), send, session_id="s", generation=1)
        chunks = [base64.b64decode(m["payload"]["audio"]) for m in sent if m["payload"]["type"] == "audio"]
        assert [len(chunk) for chunk in chunks] == [48000, 2000]
        assert b"".join(chunks) == b"\x01\x00" * 25000
        assert [m["sequence"] for m in sent] == [1, 2, 3, 4]
    asyncio.run(run())


def test_voice_channel_does_not_report_success_before_late_failure():
    async def run():
        pipeline = Pipeline([lifecycle("session_ended")], RuntimeError("private provider detail"))
        sent = []

        async def send(message):
            sent.append(message)

        with pytest.raises(RuntimeError):
            await voice_channel.serve_voice_turn(pipeline, object(), send, session_id="s", generation=1)
        assert sent == [] and pipeline.closed
    asyncio.run(run())


@pytest.mark.parametrize("mode", ["disconnect", "timeout"])
def test_voice_channel_closes_output_on_failed_send(monkeypatch, mode):
    monkeypatch.setattr(voice_channel, "SEND_TIMEOUT_SECONDS", 0.01)

    async def run():
        pipeline = Pipeline([lifecycle("turn_started")])

        async def send(_message):
            if mode == "disconnect":
                raise ConnectionError("disconnected")
            await asyncio.Future()

        with pytest.raises((ConnectionError, TimeoutError)):
            await voice_channel.serve_voice_turn(pipeline, object(), send, session_id="s", generation=1)
        assert pipeline.closed
    asyncio.run(run())


@pytest.mark.parametrize("buffer", [b"\0\0", array("f", [0.0]), array("h"), array("h", [0]) * 1440001])
def test_voice_channel_rejects_invalid_pcm(buffer):
    with pytest.raises(VoiceInputError):
        voice_channel._pcm_bytes(buffer)
