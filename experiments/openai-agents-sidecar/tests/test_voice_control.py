import asyncio
from types import SimpleNamespace

import pytest

from content_agent_sidecar import voice_control
from content_agent_sidecar.native_voice import VoiceInputError
from content_agent_sidecar.voice_control import VoiceControlBridge
from test_voice_runtime import request


def opening():
    return SimpleNamespace(session_id="s", generation=1, project_id="p", conversation_id="c")


def response(frame, result):
    return {key: frame[key] for key in ("session_id", "generation", "request_id", "operation")} | {
        "type": "voice_control_response", "result": result}


def test_voice_control_requires_execution_grant_and_correlated_durable_receipt():
    async def run():
        frames = []

        async def send(frame):
            frames.append(frame)
            result = ({"claimed": True, "request": request().model_dump()} if frame["operation"] == "prepare_turn"
                      else {"accepted": True, "agent_turn_id": "t", "dispatch_generation": 0})
            bridge.accept_response(response(frame, result))

        bridge = VoiceControlBridge(opening(), send)
        prepared = await bridge.prepare_turn("transcript")
        assert await bridge.persist_event(prepared, {"event": "agent.turn.paused", "data": {"run_state": "private"}})
        assert [f["request_id"] for f in frames] == [1, 2]
        assert frames[1]["payload"]["event"]["data"]["run_state"] == "private"
        bridge.close()
        with pytest.raises(VoiceInputError):
            await bridge.prepare_turn("transcript")
    asyncio.run(run())


@pytest.mark.parametrize("changes", [{"session_id": "foreign"}, {"generation": True}, {"request_id": True},
    {"request_id": 9}, {"operation": "persist_event"}])
def test_voice_control_rejects_foreign_or_replayed_receipts(changes):
    async def run():
        async def send(frame):
            bridge.accept_response(response(frame, {"claimed": True, "request": request().model_dump()}) | changes)

        bridge = VoiceControlBridge(opening(), send)
        with pytest.raises(VoiceInputError, match="binding mismatch"):
            await bridge.prepare_turn("transcript")
        assert bridge._pending is None
    asyncio.run(run())


@pytest.mark.parametrize("claimed", [False, None, 1, "true"])
def test_voice_control_does_not_treat_replayed_turn_as_new_execution(claimed):
    async def run():
        async def send(frame):
            bridge.accept_response(response(frame, {"claimed": claimed, "request": request().model_dump()}))

        bridge = VoiceControlBridge(opening(), send)
        with pytest.raises(VoiceInputError, match="ownership"):
            await bridge.prepare_turn("transcript")
    asyncio.run(run())


def test_voice_control_timeout_does_not_resend(monkeypatch):
    monkeypatch.setattr(voice_control, "CONTROL_TIMEOUT_SECONDS", 0.01)

    async def run():
        frames = []

        async def send(frame):
            frames.append(frame)

        bridge = VoiceControlBridge(opening(), send)
        with pytest.raises(TimeoutError):
            await bridge.prepare_turn("transcript")
        assert len(frames) == 1 and bridge._pending is None
        with pytest.raises(VoiceInputError, match="already attempted"):
            await bridge.prepare_turn("transcript")
        assert len(frames) == 1
        with pytest.raises(VoiceInputError, match="Unexpected"):
            bridge.accept_response(response(frames[0], {}))
    asyncio.run(run())


def test_voice_control_cannot_persist_before_receiving_an_execution_grant():
    async def run():
        async def send(_frame):
            pytest.fail("ungranted execution reached backend")

        bridge = VoiceControlBridge(opening(), send)
        with pytest.raises(VoiceInputError, match="does not belong"):
            await bridge.persist_event(request(), {"event": "agent.turn.committed"})
    asyncio.run(run())
