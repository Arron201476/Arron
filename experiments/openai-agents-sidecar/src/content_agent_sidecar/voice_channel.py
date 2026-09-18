"""Bounded delivery of SDK voice output; no audio is persisted here."""

import asyncio
import base64
import sys
from array import array
from contextlib import aclosing

from .native_voice import VoiceInputError

MAX_OUTPUT_BYTES = 24000 * 2 * 600
SEND_TIMEOUT_SECONDS = 10


def _pcm_bytes(data):
    try:
        view = memoryview(data)
    except TypeError:
        raise VoiceInputError("Invalid SDK voice audio buffer") from None
    if (view.ndim != 1 or not view.c_contiguous or view.format not in {"h", "<h", ">h"}
            or view.itemsize != 2 or not 0 < view.nbytes <= 24000 * 2 * 60):
        raise VoiceInputError("SDK voice output requires bounded mono PCM16")
    pcm = view.tobytes()
    if view.format == ">h" or view.format == "h" and sys.byteorder == "big":
        samples = array("h")
        samples.frombytes(pcm)
        samples.byteswap()
        pcm = samples.tobytes()
    return pcm


async def serve_voice_turn(pipeline, audio_input, send, *, session_id, generation, max_seconds=900):
    if (not isinstance(session_id, str) or not session_id or len(session_id) > 256
            or not session_id.isprintable() or session_id.strip() != session_id
            or type(generation) is not int or generation < 1 or not callable(send)
            or type(max_seconds) is not int or not 1 <= max_seconds <= 3600):
        raise VoiceInputError("Invalid voice channel binding or deadline")
    sequence = 0
    total = 0
    ended = False

    async def emit(payload):
        nonlocal sequence
        sequence += 1
        async with asyncio.timeout(SEND_TIMEOUT_SECONDS):
            await send({"session_id": session_id, "generation": generation,
                        "sequence": sequence, "payload": payload})

    async with asyncio.timeout(max_seconds):
        result = await pipeline.run(audio_input)
        async with aclosing(result.stream()) as events:
            async for event in events:
                if ended:
                    raise VoiceInputError("SDK voice emitted output after session end")
                kind = getattr(event, "type", None)
                if kind == "voice_stream_event_audio":
                    if event.data is None:
                        continue
                    pcm = _pcm_bytes(event.data)
                    total += len(pcm)
                    if total > MAX_OUTPUT_BYTES:
                        raise VoiceInputError("Voice output exceeds session limit")
                    for offset in range(0, len(pcm), 48000):
                        await emit({"type": "audio", "format": "pcm16", "sample_rate": 24000,
                                    "channels": 1, "audio": base64.b64encode(pcm[offset:offset + 48000]).decode("ascii")})
                elif kind == "voice_stream_event_lifecycle":
                    if event.event == "session_ended":
                        ended = True
                    elif event.event in {"turn_started", "turn_ended"}:
                        await emit({"type": event.event})
                    else:
                        raise VoiceInputError("Invalid SDK voice lifecycle event")
                else:
                    raise VoiceInputError("SDK voice output failed")
        if not ended:
            raise VoiceInputError("SDK voice output ended without confirmation")
        await emit({"type": "session_ended"})
