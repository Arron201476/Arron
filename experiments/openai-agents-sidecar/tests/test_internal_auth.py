import asyncio
from unittest.mock import AsyncMock

import pytest

from content_agent_sidecar.internal_auth import InternalAuthentication


@pytest.mark.parametrize("kind", ["http", "websocket"])
@pytest.mark.parametrize("headers", [[], [(b"authorization", b"Bearer wrong")],
    [(b"authorization", b"Bearer fixture"), (b"authorization", b"Bearer fixture")]])
def test_internal_auth_rejects_missing_wrong_or_duplicate_credentials(kind, headers):
    async def run():
        app, send = AsyncMock(), AsyncMock()
        guard = InternalAuthentication(app, "fixture")
        await guard({"type": kind, "path": "/internal/v1/realtime", "headers": headers,
                     "query_string": b"token=fixture"}, AsyncMock(), send)
        app.assert_not_awaited()
        first = send.await_args_list[0].args[0]
        assert first["type"] == ("http.response.start" if kind == "http" else "websocket.close")
        assert first.get("status", first.get("code")) == (401 if kind == "http" else 1008)
    asyncio.run(run())


@pytest.mark.parametrize("kind", ["http", "websocket"])
def test_internal_auth_valid_header_reaches_application(kind):
    async def run():
        app, send, receive = AsyncMock(), AsyncMock(), AsyncMock()
        scope = {"type": kind, "path": "/internal/v1/realtime",
                 "headers": [(b"Authorization", b"Bearer fixture")]}
        await InternalAuthentication(app, "fixture")(scope, receive, send)
        app.assert_awaited_once_with(scope, receive, send)
    asyncio.run(run())


@pytest.mark.parametrize("kind", ["http", "websocket"])
def test_internal_auth_disabled_rejects_before_application(kind):
    async def run():
        app, send = AsyncMock(), AsyncMock()
        await InternalAuthentication(app, "")({"type": kind, "path": "/internal/v1/realtime", "headers": []}, AsyncMock(), send)
        app.assert_not_awaited()
        first = send.await_args_list[0].args[0]
        assert first.get("status", first.get("code")) == (503 if kind == "http" else 1013)
    asyncio.run(run())
