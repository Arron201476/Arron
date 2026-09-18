"""Internal realtime route; the application supplies its authorized session opener."""

import asyncio
from contextlib import AsyncExitStack

from fastapi import WebSocket, WebSocketDisconnect
from pydantic import BaseModel, ConfigDict, Field, field_validator

from .native_realtime import ManagedRealtimeConnection
from .realtime_channel import _decode_frame, serve_realtime_channel

OPEN_TIMEOUT_SECONDS = 30


class RealtimeOpenRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)
    session_id: str = Field(min_length=1, max_length=256)
    generation: int = Field(ge=1)
    project_id: str = Field(min_length=1, max_length=256)
    conversation_id: str = Field(min_length=1, max_length=256)
    agent_turn_id: str = Field(min_length=1, max_length=256)
    dispatch_generation: int = Field(ge=0)

    @field_validator("session_id", "project_id", "conversation_id", "agent_turn_id")
    @classmethod
    def validate_identity(cls, value: str) -> str:
        if not value.isprintable() or value != value.strip():
            raise ValueError("Invalid realtime identity")
        return value


def register_realtime_routes(app, opener):
    active_sessions: set[str] = set()
    active_turns: set[tuple[str, str, str]] = set()

    @app.websocket("/internal/v1/agent/realtime")
    async def realtime(websocket: WebSocket):
        if opener is None:
            await websocket.close(code=1013, reason="Realtime runtime is not configured")
            return
        await websocket.accept()
        reserved = False
        try:
            async with asyncio.timeout(10):
                request = RealtimeOpenRequest.model_validate(_decode_frame(await websocket.receive_text()))
            turn = (request.project_id, request.conversation_id, request.agent_turn_id)
            # No await between checking and reserving on the application event loop.
            if request.session_id in active_sessions or turn in active_turns:
                await websocket.close(code=1008, reason="Realtime session is already active")
                return
            active_sessions.add(request.session_id)
            active_turns.add(turn)
            reserved = True
            async with AsyncExitStack() as resources:
                async with asyncio.timeout(OPEN_TIMEOUT_SECONDS):
                    connection = await resources.enter_async_context(opener(request))
                if not isinstance(connection, ManagedRealtimeConnection) or connection.binding != (
                        request.project_id, request.conversation_id, request.agent_turn_id, request.dispatch_generation):
                    raise ValueError("Realtime opener returned a different execution binding")

                async def receive():
                    try:
                        return await websocket.receive_text()
                    except WebSocketDisconnect:
                        return None

                await serve_realtime_channel(connection, receive, websocket.send_json,
                                              session_id=request.session_id, generation=request.generation)
            await websocket.close(code=1000)
        except WebSocketDisconnect:
            return
        except Exception:
            # Never expose SDK exception strings, credentials, or tool arguments.
            try:
                await websocket.close(code=1011, reason="Realtime session failed")
            except (WebSocketDisconnect, RuntimeError):
                pass
        finally:
            if reserved:
                active_sessions.discard(request.session_id)
                active_turns.discard(turn)
