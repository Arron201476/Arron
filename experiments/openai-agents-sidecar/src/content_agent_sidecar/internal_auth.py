"""Shared ASGI authentication boundary for internal HTTP and WebSocket routes."""

import hmac

from starlette.responses import JSONResponse
from starlette.types import ASGIApp, Receive, Scope, Send


class InternalAuthentication:
    def __init__(self, app: ASGIApp, token: str):
        self.app = app
        self.expected = ("Bearer " + token).encode("utf-8") if token else None

    async def __call__(self, scope: Scope, receive: Receive, send: Send):
        kind = scope["type"]
        if kind not in {"http", "websocket"} or not scope.get("path", "").startswith("/internal/"):
            await self.app(scope, receive, send)
            return
        headers = [value for key, value in scope.get("headers", []) if key.lower() == b"authorization"]
        valid = (self.expected is not None and len(headers) == 1
                 and hmac.compare_digest(headers[0], self.expected))
        if valid:
            await self.app(scope, receive, send)
            return
        if kind == "websocket":
            # Reject before accept; credentials in URLs or subprotocols are not accepted.
            await send({"type": "websocket.close", "code": 1013 if self.expected is None else 1008})
            return
        disabled = self.expected is None
        response = JSONResponse(status_code=503 if disabled else 401,
            content={"detail": "internal route is disabled" if disabled else "invalid internal credentials"})
        await response(scope, receive, send)
