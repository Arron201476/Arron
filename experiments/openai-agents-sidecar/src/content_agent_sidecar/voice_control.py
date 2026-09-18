"""Private Go/Sidecar voice RPC. Never forward these frames to a browser."""

import asyncio
import json

from .contracts import AgentExecutionRequest
from .native_voice import VoiceInputError

CONTROL_TIMEOUT_SECONDS = 30
MAX_CONTROL_BYTES = 65 << 20


class VoiceControlBridge:
    def __init__(self, opening, send):
        self._binding = (opening.session_id, opening.generation, opening.project_id, opening.conversation_id)
        self._send = send
        self._pending = None
        self._sequence = 0
        self._closed = False
        self._prepare_attempted = False
        self._grant = None

    async def _request(self, operation, payload):
        if self._closed or self._pending is not None:
            raise VoiceInputError("Voice control is closed or already awaiting a receipt")
        self._sequence += 1
        future = asyncio.get_running_loop().create_future()
        self._pending = (self._sequence, operation, future)
        frame = {"type": "voice_control_request", "session_id": self._binding[0],
                 "generation": self._binding[1], "request_id": self._sequence,
                 "operation": operation, "payload": payload}
        try:
            if len(json.dumps(frame, ensure_ascii=False).encode("utf-8")) > MAX_CONTROL_BYTES:
                raise VoiceInputError("Voice control payload exceeds the execution envelope limit")
            async with asyncio.timeout(CONTROL_TIMEOUT_SECONDS):
                await self._send(frame)
                return await future
        except BaseException:
            self._closed = True
            raise
        finally:
            self._pending = None
            if not future.done():
                future.cancel()

    def accept_response(self, value):
        if self._closed or self._pending is None:
            raise VoiceInputError("Unexpected voice control receipt")
        request_id, operation, future = self._pending
        if (not isinstance(value, dict) or set(value) != {
                "type", "session_id", "generation", "request_id", "operation", "result"}
                or value["type"] != "voice_control_response" or value["session_id"] != self._binding[0]
                or type(value["generation"]) is not int or value["generation"] != self._binding[1]
                or type(value["request_id"]) is not int or value["request_id"] != request_id
                or value["operation"] != operation or not isinstance(value["result"], dict) or future.done()):
            raise VoiceInputError("Voice control receipt binding mismatch")
        future.set_result(value["result"])

    async def prepare_turn(self, transcription):
        if self._prepare_attempted:
            raise VoiceInputError("Voice execution preparation was already attempted")
        self._prepare_attempted = True
        result = await self._request("prepare_turn", {"project_id": self._binding[2],
            "conversation_id": self._binding[3], "transcription": transcription})
        if set(result) != {"claimed", "request"} or result["claimed"] is not True:
            raise VoiceInputError("Voice execution ownership was not granted")
        request = AgentExecutionRequest.model_validate(result["request"])
        if (request.project_id != self._binding[2] or request.conversation_id != self._binding[3]
                or not request.agent_turn_id or request.request.get("content") != transcription
                or request.run_state is not None or request.approval_decisions):
            raise VoiceInputError("Voice execution grant does not match the transcript")
        self._grant = (request.agent_turn_id, request.dispatch_generation)
        return request

    async def persist_event(self, request, event):
        if (request.project_id != self._binding[2] or request.conversation_id != self._binding[3]
                or self._grant != (request.agent_turn_id, request.dispatch_generation)):
            raise VoiceInputError("Voice event does not belong to this conversation")
        result = await self._request("persist_event", {"agent_turn_id": request.agent_turn_id,
            "dispatch_generation": request.dispatch_generation, "event": event})
        if (set(result) != {"accepted", "agent_turn_id", "dispatch_generation"}
                or result["accepted"] is not True or result["agent_turn_id"] != request.agent_turn_id
                or type(result["dispatch_generation"]) is not int
                or result["dispatch_generation"] != request.dispatch_generation):
            raise VoiceInputError("Voice event persistence receipt is unconfirmed")
        return True

    def close(self):
        self._closed = True
        if self._pending is not None:
            self._pending[2].cancel()
