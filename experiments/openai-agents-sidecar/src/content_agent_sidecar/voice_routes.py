"""Authenticated internal voice transport; durable callbacks belong to Runtime."""

import asyncio
from typing import Literal

from fastapi import WebSocket, WebSocketDisconnect
from pydantic import BaseModel, ConfigDict, Field, field_validator

from .native_voice import MAX_VOICE_TURN_BYTES, decode_voice_turn
from .realtime_channel import _decode_frame
from .voice_control import MAX_CONTROL_BYTES, VoiceControlBridge


class VoiceOpenRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)
    session_id: str = Field(min_length=1, max_length=256)
    generation: int = Field(ge=1)
    project_id: str = Field(min_length=1, max_length=256)
    conversation_id: str = Field(min_length=1, max_length=256)
    audio: dict

    @field_validator("session_id", "project_id", "conversation_id")
    @classmethod
    def valid_identity(cls, value):
        if not value.isprintable() or value != value.strip():
            raise ValueError("Invalid voice identity")
        return value


class VoiceStopRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)
    type: Literal["stop"]
    session_id: str
    generation: int = Field(ge=1)


def register_voice_routes(app, runner):
    active_sessions = set()
    active_conversations = set()

    @app.websocket("/internal/v1/agent/voice")
    async def voice(websocket: WebSocket):
        if runner is None:
            await websocket.close(code=1013, reason="Voice runtime is not configured")
            return
        await websocket.accept()
        reserved = False
        tasks = []
        bridge = None
        try:
            async with asyncio.timeout(15):
                raw = await websocket.receive_text()
                request = VoiceOpenRequest.model_validate(_decode_frame(
                    raw, max_bytes=4 * ((MAX_VOICE_TURN_BYTES + 2) // 3) + 8192))
                decode_voice_turn(request.audio)
                del raw
            conversation = (request.project_id, request.conversation_id)
            if request.session_id in active_sessions or conversation in active_conversations:
                await websocket.close(code=1008, reason="Voice conversation is already active")
                return
            active_sessions.add(request.session_id)
            active_conversations.add(conversation)
            reserved = True

            send_lock = asyncio.Lock()

            async def send(value):
                async with asyncio.timeout(10), send_lock:
                    await websocket.send_json(value)

            bridge = VoiceControlBridge(request, send)

            async def control():
                while True:
                    try:
                        value = _decode_frame(await websocket.receive_text(), max_bytes=MAX_CONTROL_BYTES)
                    except WebSocketDisconnect:
                        return
                    if value.get("type") == "stop":
                        stop = VoiceStopRequest.model_validate(value)
                        if stop.session_id != request.session_id or stop.generation != request.generation:
                            raise ValueError("Voice stop binding mismatch")
                        return
                    bridge.accept_response(value)

            async with asyncio.timeout(900):
                tasks = [asyncio.create_task(runner(request, send, bridge)),
                         asyncio.create_task(control())]
                done, _ = await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
                for task in done:
                    task.result()
                for task in tasks:
                    if not task.done():
                        task.cancel()
                results = await asyncio.gather(*tasks, return_exceptions=True)
                for result in results:
                    if isinstance(result, BaseException) and not isinstance(result, asyncio.CancelledError):
                        raise result
            await websocket.close(code=1000)
        except WebSocketDisconnect:
            return
        except Exception:
            try:
                await websocket.close(code=1011, reason="Voice session failed")
            except (WebSocketDisconnect, RuntimeError):
                pass
        finally:
            if bridge is not None:
                bridge.close()
            for task in tasks:
                if not task.done():
                    task.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)
            if reserved:
                active_sessions.discard(request.session_id)
                active_conversations.discard(conversation)
