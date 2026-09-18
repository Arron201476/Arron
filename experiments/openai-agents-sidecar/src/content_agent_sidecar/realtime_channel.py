"""Bound bidirectional framing around an authorized managed realtime connection."""

import asyncio
import base64
import json
from contextlib import aclosing


def _decode_frame(raw, *, max_bytes=128 * 1024):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError("Duplicate realtime input field")
            result[key] = value
        return result

    def invalid_constant(_):
        raise ValueError("Invalid realtime number")

    if not isinstance(raw, (str, bytes)) or len(raw) > max_bytes:
        raise ValueError("Realtime input frame exceeds limit")
    if isinstance(raw, str) and len(raw.encode("utf-8")) > max_bytes:
        raise ValueError("Realtime input frame exceeds limit")
    value = json.loads(raw, object_pairs_hook=unique, parse_constant=invalid_constant)
    if not isinstance(value, dict):
        raise ValueError("Realtime input must be an object")
    return value


async def serve_realtime_channel(connection, receive, send, *, session_id, generation):
    """receive returns a JSON frame or None on disconnect; send accepts a dict.

    The API caller must authenticate and authorize the supplied connection and
    binding. This function does not create sessions or make approval decisions.
    """
    if (not isinstance(session_id, str) or not session_id or len(session_id) > 256
            or not session_id.isprintable() or type(generation) is not int or generation < 1):
        raise ValueError("Invalid realtime channel binding")
    output_lock = asyncio.Lock()
    output_sequence = 0
    stop_requested = False

    async def emit(payload):
        nonlocal output_sequence
        async with asyncio.timeout(10), output_lock:
            output_sequence += 1
            await send({"session_id": session_id, "generation": generation,
                        "sequence": output_sequence, "payload": payload})

    async def inputs():
        nonlocal stop_requested
        expected = 1
        base = {"session_id", "generation", "sequence", "type"}
        fields = {"audio": {"audio", "commit"}, "text": {"text"}, "interrupt": set(),
                  "playback": {"item_id", "content_index", "elapsed_ms"},
                  "approval_refresh": {"call_id"}, "stop": set()}
        while True:
            raw = await receive()
            if raw is None:
                return
            value = _decode_frame(raw)
            kind = value.get("type")
            if (not isinstance(kind, str) or kind not in fields or set(value) != base | fields[kind]
                    or value["session_id"] != session_id or type(value["generation"]) is not int
                    or value["generation"] != generation or type(value["sequence"]) is not int
                    or value["sequence"] != expected):
                raise ValueError("Realtime input binding or sequence mismatch")
            expected += 1
            status = None
            if kind == "audio":
                encoded = value["audio"]
                if not isinstance(encoded, str) or len(encoded) > 64000:
                    raise ValueError("Invalid realtime audio encoding")
                pcm = base64.b64decode(encoded, validate=True)
                if base64.b64encode(pcm).decode("ascii") != encoded:
                    raise ValueError("Noncanonical realtime audio encoding")
                await connection.send_audio(pcm, commit=value["commit"])
            elif kind == "text":
                await connection.send_message(value["text"])
            elif kind == "interrupt":
                await connection.interrupt()
            elif kind == "playback":
                connection.acknowledge_playback(value["item_id"], value["content_index"], value["elapsed_ms"])
            elif kind == "approval_refresh":
                call_id = value["call_id"]
                if not isinstance(call_id, str) or not call_id or len(call_id) > 256:
                    raise ValueError("Invalid realtime approval reference")
                status = await connection.resolve_approval(call_id)
            elif kind == "stop":
                stop_requested = True
                await connection.close()
            await emit({"type": "input_ack", "input_sequence": value["sequence"], "approval_status": status})
            if kind == "stop":
                return

    async def outputs():
        async with aclosing(connection.public_events()) as events:
            async for event in events:
                await emit(event)

    tasks = []
    try:
        await emit({"type": "ready"})
        tasks = [asyncio.create_task(inputs()), asyncio.create_task(outputs())]
        done, _ = await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
        for task in done:
            task.result()
        if stop_requested and not tasks[0].done():
            # SDK output ends during close; the control acknowledgement must
            # finish before the input direction is cancelled by channel cleanup.
            await tasks[0]
    finally:
        for task in tasks:
            if not task.done():
                task.cancel()
        try:
            await connection.close()
        finally:
            await asyncio.gather(*tasks, return_exceptions=True)
