import asyncio
from dataclasses import replace

import pytest
from fastapi.testclient import TestClient
from starlette.websockets import WebSocketDisconnect

from content_agent_sidecar.app import create_app
from content_agent_sidecar import native_voice
from test_app import settings
from test_voice_control import response
from test_voice_runtime import request as execution_request

HEADERS = {"authorization": "Bearer internal-test-token"}
REQUEST = {"session_id": "voice", "generation": 1, "project_id": "p", "conversation_id": "c",
           "audio": {"format": "pcm16", "sample_rate": 24000, "channels": 1, "audio": "AAA="}}


@pytest.mark.parametrize("configured", [True, False])
def test_voice_route_requires_auth_and_explicit_runtime(configured):
    called = []

    async def runner(*args):
        called.append(args)

    with TestClient(create_app(settings(), voice_runner=runner if configured else None)) as client:
        with pytest.raises(WebSocketDisconnect) as failure:
            with client.websocket_connect("/internal/v1/agent/voice", headers={} if configured else HEADERS):
                pass
        assert failure.value.code == (1008 if configured else 1013)
    assert not called


@pytest.mark.parametrize("invalid", [{"project_id": " bad "}, {"generation": True},
    {"audio": {"format": "pcm16", "sample_rate": 24000, "channels": 1, "audio": "AA=="}}])
def test_voice_route_validates_before_runner(invalid):
    called = []

    async def runner(*args):
        called.append(args)

    with TestClient(create_app(settings(), voice_runner=runner)) as client:
        with client.websocket_connect("/internal/v1/agent/voice", headers=HEADERS) as socket:
            socket.send_json({**REQUEST, **invalid})
            with pytest.raises(WebSocketDisconnect) as failure:
                socket.receive_json()
            assert failure.value.code == 1011
    assert not called


def test_voice_stop_cancels_runner_before_releasing_session():
    cleaned = []

    async def runner(request, send, bridge):
        try:
            await send({"type": "fixture_ready"})
            await asyncio.Future()
        finally:
            cleaned.append(request.session_id)

    with TestClient(create_app(settings(), voice_runner=runner)) as client:
        for _ in range(2):
            with client.websocket_connect("/internal/v1/agent/voice", headers=HEADERS) as socket:
                socket.send_json(REQUEST)
                assert socket.receive_json()["type"] == "fixture_ready"
                socket.send_json({"type": "stop", "session_id": "voice", "generation": 1})
                with pytest.raises(WebSocketDisconnect) as failure:
                    socket.receive_json()
                assert failure.value.code == 1000
    assert cleaned == ["voice", "voice"]


def test_voice_disconnect_cancels_runner():
    cleaned = []

    async def runner(request, send, bridge):
        try:
            await send({"type": "fixture_ready"})
            await asyncio.Future()
        finally:
            cleaned.append(True)

    with TestClient(create_app(settings(), voice_runner=runner)) as client:
        with client.websocket_connect("/internal/v1/agent/voice", headers=HEADERS) as socket:
            socket.send_json(REQUEST)
            socket.receive_json()
    assert cleaned == [True]


@pytest.mark.parametrize("grant,accept", [(True, True), (False, True), (True, False)])
def test_enabled_voice_route_wires_configured_runner_to_private_control(monkeypatch, grant, accept):
    runtime = object()
    executed = []

    async def pipeline(actual_runtime, project_id, conversation_id, prepare, persist, audio, send, *, session_id, generation):
        assert actual_runtime is runtime
        assert (project_id, conversation_id, session_id, generation) == ("p", "c", "voice", 1)
        assert audio == REQUEST["audio"]
        prepared = await prepare("transcript")
        executed.append(prepared.agent_turn_id)
        await persist(prepared, {"event": "agent.turn.committed", "data": {"exchange": {"private": "receipt"}}})
        await send({"session_id": session_id, "generation": generation, "sequence": 1,
                    "payload": {"type": "session_ended"}})

    monkeypatch.setattr(native_voice, "run_configured_voice_turn", pipeline)
    with TestClient(create_app(replace(settings(), voice_enabled=True), runtime=runtime)) as client:
        with client.websocket_connect("/internal/v1/agent/voice", headers=HEADERS) as socket:
            socket.send_json(REQUEST)
            prepare = socket.receive_json()
            assert prepare["type"] == "voice_control_request" and prepare["operation"] == "prepare_turn"
            socket.send_json(response(prepare, {"claimed": grant, "request": execution_request().model_dump()}))
            if grant:
                persist = socket.receive_json()
                assert persist["operation"] == "persist_event"
                assert persist["payload"]["event"]["data"]["exchange"] == {"private": "receipt"}
                socket.send_json(response(persist, {"accepted": accept, "agent_turn_id": "t", "dispatch_generation": 0}))
            if grant and accept:
                assert socket.receive_json()["payload"]["type"] == "session_ended"
            with pytest.raises(WebSocketDisconnect) as failure:
                socket.receive_json()
            assert failure.value.code == (1000 if grant and accept else 1011)
    assert executed == (["t"] if grant else [])
