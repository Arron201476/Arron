"""Owned SDK realtime connection lifecycle; durable API/UI wiring is separate."""

from __future__ import annotations

import asyncio
import json
from dataclasses import replace
from contextlib import aclosing, asynccontextmanager
from urllib.parse import urlsplit
from weakref import WeakSet

from agents.realtime import RealtimeAgent, RealtimeRunner
from agents.realtime.model import RealtimeModel
from agents.realtime.events import RealtimeToolApprovalRequired, RealtimeAudio
from .realtime_playback import ManagedPlaybackTracker
from .realtime_events import project_realtime_event
from agents.realtime.openai_realtime import OpenAIRealtimeWebSocketModel

_owned_models = WeakSet()


def configured_realtime_session(settings, agent, context, run_config=None, *, approval_refresher=None):
    config = settings.realtime_model_config()
    return managed_realtime_session(agent, OpenAIRealtimeWebSocketModel(), context, config, run_config,
                                    approval_refresher=approval_refresher,
                                    max_session_seconds=settings.realtime_max_session_seconds)


def realtime_approval_refresher(provider, context):
    async def refresh(event):
        if event.info.context.context is not context:
            raise RealtimeSessionError("Realtime approval context mismatch")
        active = getattr(context, "active_tool_calls", {})
        call = active.get(event.call_id)
        if not isinstance(call, dict) or call.get("sdk_tool_call_id") != event.call_id or not call.get("tool_id"):
            raise RealtimeSessionError("Realtime approval has no registered tool binding")
        arguments = json.loads(event.arguments)
        if not isinstance(arguments, dict):
            raise RealtimeSessionError("Invalid realtime approval arguments")
        receipt = await provider.ensure_call(event.info.context, call["tool_id"], arguments, event.call_id,
            configuration_hash=call.get("_configuration_hash", ""), refresh=True)
        if not isinstance(receipt, dict):
            raise RealtimeSessionError("Realtime approval receipt unconfirmed")
        return receipt.get("status")
    return refresh


class RealtimeSessionError(RuntimeError):
    pass


def _conversation_binding(context):
    if any(getattr(context, name, "") for name in (
            "agent_task_attempt_id", "execution_attempt_id", "memory_generation_id")):
        raise RealtimeSessionError("Realtime requires one authorized conversation identity")
    values = tuple(getattr(context, name, "") for name in ("project_id", "conversation_id", "agent_turn_id"))
    generation = getattr(context, "dispatch_generation", 0)
    if (any(not isinstance(value, str) or not value or len(value) > 256 or not value.isprintable()
            or value.strip() != value for value in values)
            or type(generation) is not int or generation < 0):
        raise RealtimeSessionError("Invalid realtime conversation binding")
    return (*values, generation)


class ManagedRealtimeConnection:
    """Owned input boundary; approval controls require separate durable wiring."""

    def __init__(self, session, approval_refresher=None, playback_tracker=None, *, context):
        self._session = session
        self._closed = False
        self._input_lock = asyncio.Lock()
        self._active_input = None
        self._approval_refresher = approval_refresher
        self._pending_approvals = {}
        self._playback = playback_tracker
        self._context = context
        self._binding = _conversation_binding(context)

    def _check_binding(self):
        if _conversation_binding(self._context) != self._binding:
            raise RealtimeSessionError("Realtime conversation binding changed")

    @property
    def binding(self):
        self._check_binding()
        return self._binding

    async def __aiter__(self):
        try:
            async with aclosing(self._session.__aiter__()) as events:
                async for event in events:
                    if self._closed:
                        return
                    self._check_binding()
                    if isinstance(event, RealtimeAudio):
                        self._playback.delivered(event.item_id, event.content_index, event.audio.data)
                    if isinstance(event, RealtimeToolApprovalRequired):
                        if not callable(self._approval_refresher):
                            raise RealtimeSessionError("Realtime approval requires durable receipt integration")
                        if event.call_id in self._pending_approvals:
                            raise RealtimeSessionError("Duplicate realtime approval event")
                        self._pending_approvals[event.call_id] = replace(event)
                    yield event
        finally:
            await self.close()

    async def _send(self, operation, *args, **kwargs):
        async with self._input_lock:
            if self._closed:
                raise RealtimeSessionError("Realtime session closed")
            self._active_input = asyncio.current_task()
            try:
                self._check_binding()
                result = await operation(*args, **kwargs)
                self._check_binding()
                if self._closed:
                    raise RealtimeSessionError("Realtime input unconfirmed after stop")
                return result
            except BaseException as exc:
                try:
                    if not self._closed:
                        await self.close()
                except BaseException:
                    exc.add_note("Realtime session cleanup unconfirmed")
                raise
            finally:
                self._active_input = None

    async def public_events(self):
        async with aclosing(self.__aiter__()) as events:
            async for event in events:
                payload = project_realtime_event(event)
                if payload is not None:
                    yield payload

    async def send_audio(self, audio: bytes, *, commit: bool = False):
        if (not isinstance(audio, bytes) or type(commit) is not bool or len(audio) > 48000
                or len(audio) % 2 or not audio and not commit):
            raise RealtimeSessionError("Invalid realtime PCM16 audio chunk")
        await self._send(self._session.send_audio, audio, commit=commit)

    async def send_message(self, text: str):
        if not isinstance(text, str) or not text.strip() or len(text) > 65536:
            raise RealtimeSessionError("Invalid realtime user text")
        await self._send(self._session.send_message, text)

    async def interrupt(self):
        await self._send(self._session.interrupt)

    def acknowledge_playback(self, item_id, content_index, elapsed_ms):
        if self._closed or self._playback is None:
            raise RealtimeSessionError("Realtime playback unavailable")
        self._check_binding()
        self._playback.acknowledge(item_id, content_index, elapsed_ms)

    async def resolve_approval(self, call_id: str):
        async def resolve():
            event = self._pending_approvals.get(call_id)
            if event is None:
                raise RealtimeSessionError("Unknown realtime approval")
            status = await self._approval_refresher(event)
            self._check_binding()
            if self._closed:
                raise RealtimeSessionError("Realtime session closed during approval refresh")
            if status == "pending_approval":
                return status
            if status not in {"approved", "rejected"}:
                raise RealtimeSessionError("Realtime approval outcome unconfirmed")
            self._pending_approvals.pop(call_id)
            if status == "approved":
                await self._session.approve_tool_call(call_id, always=False)
            else:
                await self._session.reject_tool_call(call_id, always=False)
            return status
        return await self._send(resolve)

    async def close(self):
        self._closed = True
        self._pending_approvals.clear()
        active = self._active_input
        if active is not None and active is not asyncio.current_task() and not active.done() and not active.cancelling():
            active.cancel()
        await self._session.close()
        if active is not None and active is not asyncio.current_task():
            await asyncio.gather(active, return_exceptions=True)


@asynccontextmanager
async def managed_realtime_session(agent, model, context, model_config, run_config=None, *, approval_refresher=None,
                                   max_session_seconds=900):
    """Use a fresh model and already-authorized tools supplied by Runtime.

    This is not a credential endpoint or approval store. Callers must enforce
    session ownership on every input and persist approval decisions before
    invoking the SDK's approval methods.
    """
    if type(max_session_seconds) is not int or not 1 <= max_session_seconds <= 3600:
        raise RealtimeSessionError("Realtime session limit must be between 1 and 3600 seconds")
    _conversation_binding(context)
    if (not isinstance(agent, RealtimeAgent) or not isinstance(model, RealtimeModel)
            or not all(getattr(context, name, "") for name in ("project_id", "conversation_id", "agent_turn_id"))
            or any(getattr(context, name, "") for name in ("agent_task_attempt_id", "execution_attempt_id", "memory_generation_id"))):
        raise RealtimeSessionError("Realtime requires one authorized conversation identity")
    if not isinstance(model_config, dict):
        raise RealtimeSessionError("Realtime requires explicit connection configuration")
    config = dict(model_config)
    key, address = config.get("api_key"), config.get("url")
    try:
        parsed = urlsplit(address) if isinstance(address, str) else None
        valid_url = (parsed is not None and parsed.scheme == "wss" and parsed.hostname
                     and parsed.username is None and parsed.password is None and not parsed.fragment
                     and "\\" not in address and address.isprintable() and len(address) <= 8192)
        if valid_url:
            parsed.port
    except ValueError:
        valid_url = False
    settings = config.get("initial_model_settings")
    if (not valid_url or not (callable(key) or isinstance(key, str) and bool(key.strip()))
            or "headers" in config or "call_id" in config or not isinstance(settings, dict)
            or not isinstance(settings.get("model_name"), str) or not settings["model_name"].strip()):
        raise RealtimeSessionError("Realtime requires explicit model, secure endpoint and credentials")
    audio = settings.get("audio", {})
    if not isinstance(audio, dict) or not isinstance(audio.get("input", {}), dict):
        raise RealtimeSessionError("Invalid realtime audio configuration")
    audio_input = audio.get("input", {})
    audio_output = audio.get("output", {})
    if not isinstance(audio_output, dict):
        raise RealtimeSessionError("Invalid realtime output audio configuration")
    formats = (settings.get("input_audio_format", "pcm16"), audio_input.get("format", "pcm16"),
               settings.get("output_audio_format", "pcm16"), audio_output.get("format", "pcm16"))
    if any(value != "pcm16" and value != {"type": "audio/pcm", "rate": 24000} for value in formats):
        raise RealtimeSessionError("Realtime input requires 24kHz PCM16")
    config["initial_model_settings"] = {**settings, "tracing": None,
        "audio": {**audio, "input": {**audio_input, "format": "pcm16"},
                  "output": {**audio_output, "format": "pcm16"}}}
    playback = ManagedPlaybackTracker()
    config["playback_tracker"] = playback
    if model in _owned_models:
        raise RealtimeSessionError("Realtime requires a fresh model connection")
    _owned_models.add(model)
    runner = RealtimeRunner(agent, model=model, config={**(run_config or {}), "tracing_disabled": True})
    session = await runner.run(context=context, model_config=config)
    connection = ManagedRealtimeConnection(session, approval_refresher, playback, context=context)
    primary = None
    try:
        async with asyncio.timeout(max_session_seconds):
            await session.enter()
            yield connection
    except BaseException as exc:
        primary = exc
        raise
    finally:
        try:
            await connection.close()
        except BaseException:
            if primary is None:
                raise
            primary.add_note("Realtime session cleanup unconfirmed")
