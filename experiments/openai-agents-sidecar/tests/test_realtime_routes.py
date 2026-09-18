import asyncio
from contextlib import asynccontextmanager

import pytest
from agents.realtime import RealtimeAgent
from fastapi.testclient import TestClient
from starlette.websockets import WebSocketDisconnect

from content_agent_sidecar.app import create_app
from content_agent_sidecar import realtime_routes
from content_agent_sidecar.native_realtime import managed_realtime_session
from test_app import settings
from test_native_realtime import Model, config, owner


HEADERS = {"authorization": "Bearer internal-test-token"}
REQUEST = {"session_id": "session", "generation": 1, "project_id": "p", "conversation_id": "c",
           "agent_turn_id": "t", "dispatch_generation": 0}


@pytest.mark.parametrize("field", ["session_id", "project_id", "conversation_id", "agent_turn_id"])
@pytest.mark.parametrize("value", [" leading", "trailing ", "line\nbreak", "null\x00byte", "\t", ""])
def test_realtime_route_rejects_invalid_identity_before_opener(field, value):
    calls = []

    def opener(request):
        calls.append(request)
        raise AssertionError("invalid identity reached opener")

    with TestClient(create_app(settings(), realtime_opener=opener)) as client:
        with client.websocket_connect("/internal/v1/agent/realtime", headers=HEADERS) as websocket:
            websocket.send_json({**REQUEST, field: value})
            with pytest.raises(WebSocketDisconnect) as failure:
                websocket.receive_json()
            assert failure.value.code == 1011
    assert calls == []


def test_realtime_route_requires_internal_auth_before_opener():
    called = []
    def opener(request):
        called.append(request)
        raise AssertionError("unauthorized opener")
    with TestClient(create_app(settings(), realtime_opener=opener)) as client:
        with pytest.raises(WebSocketDisconnect) as failure:
            with client.websocket_connect("/internal/v1/agent/realtime"):
                pass
    assert failure.value.code == 1008 and called == []


def test_realtime_route_without_runtime_fails_closed():
    with TestClient(create_app(settings())) as client:
        with pytest.raises(WebSocketDisconnect) as failure:
            with client.websocket_connect("/internal/v1/agent/realtime", headers=HEADERS):
                pass
    assert failure.value.code == 1013


def test_realtime_route_opens_sdk_channel_and_closes_after_stop():
    model = Model()
    @asynccontextmanager
    async def opener(request):
        assert request.model_dump() == REQUEST
        async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config()) as connection:
            yield connection
    with TestClient(create_app(settings(), realtime_opener=opener)) as client:
        with client.websocket_connect("/internal/v1/agent/realtime", headers=HEADERS) as websocket:
            websocket.send_json(REQUEST)
            assert websocket.receive_json()["payload"]["type"] == "ready"
            websocket.send_json({"session_id": "session", "generation": 1, "sequence": 1, "type": "stop"})
            while websocket.receive_json()["payload"]["type"] != "input_ack":
                pass
    assert model.closed


def test_realtime_route_rejects_opener_binding_mismatch_and_cleans_up():
    model = Model()
    @asynccontextmanager
    async def opener(request):
        context = owner()
        context.project_id = "foreign-project"
        async with managed_realtime_session(RealtimeAgent(name="fixture"), model, context, config()) as connection:
            yield connection
    with TestClient(create_app(settings(), realtime_opener=opener)) as client:
        with client.websocket_connect("/internal/v1/agent/realtime", headers=HEADERS) as websocket:
            websocket.send_json(REQUEST)
            with pytest.raises(WebSocketDisconnect) as failure:
                websocket.receive_json()
    assert model.closed and failure.value.code == 1011
    assert failure.value.reason == "Realtime session failed"


@pytest.mark.parametrize("changes", [
    {},
    {"session_id": "other", "generation": 2, "dispatch_generation": 1},
    {"agent_turn_id": "other"},
])
def test_realtime_route_rejects_duplicate_before_opening_model(changes):
    models = []

    @asynccontextmanager
    async def opener(request):
        model = Model()
        models.append(model)
        async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config()) as connection:
            yield connection

    with TestClient(create_app(settings(), realtime_opener=opener)) as client:
        with client.websocket_connect("/internal/v1/agent/realtime", headers=HEADERS) as first:
            first.send_json(REQUEST)
            assert first.receive_json()["payload"]["type"] == "ready"
            # A rejected connection must not release the first connection's reservation.
            for _ in range(2):
                with client.websocket_connect("/internal/v1/agent/realtime", headers=HEADERS) as duplicate:
                    duplicate.send_json({**REQUEST, **changes})
                    with pytest.raises(WebSocketDisconnect) as failure:
                        duplicate.receive_json()
                    assert failure.value.code == 1008
            assert len(models) == 1 and not models[0].closed
    assert models[0].closed


def test_realtime_route_releases_reservation_after_opener_failure():
    calls = []

    @asynccontextmanager
    async def opener(request):
        calls.append(request)
        if len(calls) == 1:
            raise RuntimeError("private upstream error")
        async with managed_realtime_session(RealtimeAgent(name="fixture"), Model(), owner(), config()) as connection:
            yield connection

    with TestClient(create_app(settings(), realtime_opener=opener)) as client:
        with client.websocket_connect("/internal/v1/agent/realtime", headers=HEADERS) as first:
            first.send_json(REQUEST)
            with pytest.raises(WebSocketDisconnect) as failure:
                first.receive_json()
            assert failure.value.code == 1011
        with client.websocket_connect("/internal/v1/agent/realtime", headers=HEADERS) as retry:
            retry.send_json(REQUEST)
            assert retry.receive_json()["payload"]["type"] == "ready"
    assert len(calls) == 2


def test_realtime_route_cancels_stalled_opener_and_allows_retry(monkeypatch):
    monkeypatch.setattr(realtime_routes, "OPEN_TIMEOUT_SECONDS", 0.05)
    attempts = []
    cleaned = []

    @asynccontextmanager
    async def opener(request):
        attempts.append(request)
        if len(attempts) == 1:
            try:
                await asyncio.Event().wait()
            finally:
                cleaned.append(True)
        async with managed_realtime_session(RealtimeAgent(name="fixture"), Model(), owner(), config()) as connection:
            yield connection

    with TestClient(create_app(settings(), realtime_opener=opener)) as client:
        with client.websocket_connect("/internal/v1/agent/realtime", headers=HEADERS) as first:
            first.send_json(REQUEST)
            with pytest.raises(WebSocketDisconnect) as failure:
                first.receive_json()
            assert failure.value.code == 1011
            assert cleaned == [True]
        with client.websocket_connect("/internal/v1/agent/realtime", headers=HEADERS) as retry:
            retry.send_json(REQUEST)
            assert retry.receive_json()["payload"]["type"] == "ready"
            # Opening timeout must not become the connected session's lifetime.
            client.portal.call(asyncio.sleep, 0.1)
            retry.send_json({"session_id": "session", "generation": 1, "sequence": 1, "type": "stop"})
            while retry.receive_json()["payload"]["type"] != "input_ack":
                pass
    assert len(attempts) == 2
