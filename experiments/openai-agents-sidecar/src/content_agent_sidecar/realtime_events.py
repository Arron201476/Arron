"""Explicit realtime wire projection; never serialize SDK contexts or raw errors."""

import base64
import json


def _text(value, limit=65536):
    if not isinstance(value, str) or len(value) > limit:
        raise ValueError("Invalid realtime event text")
    return value


def _identity(value):
    value = _text(value, 256)
    if not value or not value.isprintable():
        raise ValueError("Invalid realtime event identity")
    return value


def project_realtime_event(event):
    kind = event.type
    if kind == "raw_model_event":
        raw = event.data
        if raw.type in {"transcript_delta", "output_text_delta"}:
            result = {"type": raw.type, "item_id": _identity(raw.item_id), "delta": _text(raw.delta)}
        elif raw.type == "input_audio_transcription_completed":
            result = {"type": "input_transcript", "item_id": _identity(raw.item_id), "text": _text(raw.transcript)}
        else:
            return None
    elif kind in {"audio", "audio_end", "audio_interrupted"}:
        if type(event.content_index) is not int or event.content_index < 0:
            raise ValueError("Invalid realtime audio index")
        result = {"type": kind, "item_id": _identity(event.item_id), "content_index": event.content_index}
        if kind == "audio":
            data = event.audio.data
            if not isinstance(data, bytes) or not data or len(data) > 24000 * 2 * 60 or len(data) % 2:
                raise ValueError("Invalid realtime audio event")
            result.update(audio=base64.b64encode(data).decode("ascii"), format="pcm16", sample_rate=24000, channels=1)
    elif kind == "tool_approval_required":
        result = {"type": kind, "call_id": _identity(event.call_id), "tool_name": _identity(event.tool.name)}
    elif kind in {"tool_start", "tool_end"}:
        result = {"type": kind, "tool_name": _identity(event.tool.name)}
    elif kind in {"error", "guardrail_tripped"}:
        result = {"type": kind, "code": "REALTIME_EXECUTION_FAILED" if kind == "error" else "REALTIME_GUARDRAIL_BLOCKED"}
    elif kind in {"agent_start", "agent_end", "handoff", "input_audio_timeout_triggered"}:
        result = {"type": kind}
    elif kind in {"history_added", "history_updated"}:
        history = [event.item] if kind == "history_added" else event.history
        if not isinstance(history, list) or len(history) > 2048:
            raise ValueError("Realtime history projection exceeds limit")
        items = []
        total_chars = 0
        for item in history:
            if getattr(item, "type", None) != "message" or getattr(item, "role", None) not in {"user", "assistant"}:
                continue
            texts = []
            item_chars = 0
            if not isinstance(item.content, list) or len(item.content) > 256:
                raise ValueError("Realtime message content exceeds limit")
            for part in item.content:
                if part.type in {"input_text", "text"}:
                    value = getattr(part, "text", None)
                elif part.type in {"input_audio", "audio"}:
                    value = getattr(part, "transcript", None)
                else:
                    continue
                if value is not None:
                    value = _text(value)
                    item_chars += len(value)
                    total_chars += len(value)
                    if item_chars > 65536 or total_chars > 1_000_000:
                        raise ValueError("Realtime history text exceeds limit")
                    texts.append(value)
            text = _text("".join(texts))
            items.append({"item_id": _identity(item.item_id), "role": item.role, "text": text})
        result = {"type": kind, "items": items}
    else:
        return None
    if len(json.dumps(result, ensure_ascii=False, allow_nan=False).encode("utf-8")) > 4 * 1024 * 1024:
        raise ValueError("Realtime event projection exceeds limit")
    return result
