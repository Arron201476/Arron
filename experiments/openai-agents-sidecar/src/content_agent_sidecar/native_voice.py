"""SDK voice construction; transport ownership and durable turns belong to Runtime."""

from __future__ import annotations

import base64
import binascii
from collections.abc import AsyncGenerator, Callable
from contextlib import AsyncExitStack, aclosing, asynccontextmanager
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from agents.voice import AudioInput, STTModel, TTSModel, VoicePipeline


VOICE_SAMPLE_RATE = 24000
MAX_VOICE_TURN_BYTES = VOICE_SAMPLE_RATE * 2 * 60
MAX_VOICE_TEXT_CHARS = 65536


class VoiceInputError(ValueError):
    pass


class VoiceUnavailableError(RuntimeError):
    pass


@asynccontextmanager
async def configured_voice_pipeline(runtime, project_id, conversation_id, prepare_turn, persist_event):
    """Own explicit SDK model clients for one fully consumed voice pipeline."""
    stt = runtime.settings.voice_model_config("stt")
    tts = runtime.settings.voice_model_config("tts")
    try:
        from agents.voice.models.openai_stt import OpenAISTTModel
        from agents.voice.models.openai_tts import OpenAITTSModel
    except ImportError:
        raise VoiceUnavailableError("SDK voice dependencies are unavailable") from None
    from openai import AsyncOpenAI

    async with AsyncExitStack() as resources:
        stt_client = await resources.enter_async_context(AsyncOpenAI(**stt["client"]))
        tts_client = await resources.enter_async_context(AsyncOpenAI(**tts["client"]))
        yield runtime.create_voice_pipeline(
            project_id, conversation_id, prepare_turn, persist_event,
            stt_model=OpenAISTTModel(stt["model"], stt_client),
            tts_model=OpenAITTSModel(tts["model"], tts_client),
        )


async def run_configured_voice_turn(runtime, project_id, conversation_id, prepare_turn, persist_event,
                                    audio_payload, send, *, session_id, generation):
    from .voice_channel import serve_voice_turn

    audio_input = voice_audio_input(audio_payload)
    async with configured_voice_pipeline(runtime, project_id, conversation_id, prepare_turn, persist_event) as pipeline:
        await serve_voice_turn(pipeline, audio_input, send, session_id=session_id, generation=generation)


def configured_voice_runner(runtime_factory, *, owns_runtime=False):
    async def run(request, send, bridge):
        runtime = runtime_factory()
        terminal = None

        async def send_after_cleanup(frame):
            nonlocal terminal
            if terminal is not None:
                raise VoiceInputError("Voice emitted output after session completion")
            if frame.get("payload", {}).get("type") == "session_ended":
                terminal = frame
            else:
                await send(frame)

        try:
            await run_configured_voice_turn(runtime, request.project_id, request.conversation_id,
                bridge.prepare_turn, bridge.persist_event, request.audio, send_after_cleanup,
                session_id=request.session_id, generation=request.generation)
        finally:
            if owns_runtime:
                await runtime.close()
        if terminal is None:
            raise VoiceInputError("Voice pipeline ended without session completion")
        await send(terminal)
    return run


def decode_voice_turn(payload: dict) -> bytes:
    """Validate one bounded PCM16 turn, not a complete realtime session."""
    if not isinstance(payload, dict) or set(payload) != {"format", "sample_rate", "channels", "audio"}:
        raise VoiceInputError("Invalid voice input fields")
    if (payload["format"] != "pcm16" or type(payload["sample_rate"]) is not int
            or payload["sample_rate"] != VOICE_SAMPLE_RATE or type(payload["channels"]) is not int
            or payload["channels"] != 1):
        raise VoiceInputError("Voice input requires 24kHz mono PCM16")
    encoded = payload["audio"]
    if not isinstance(encoded, str) or not encoded or len(encoded) > 4 * ((MAX_VOICE_TURN_BYTES + 2) // 3):
        raise VoiceInputError("Voice turn exceeds input limit or is empty")
    try:
        pcm = base64.b64decode(encoded, validate=True)
    except (ValueError, binascii.Error):
        raise VoiceInputError("Invalid voice audio encoding") from None
    if not pcm or len(pcm) % 2 or len(pcm) > MAX_VOICE_TURN_BYTES:
        raise VoiceInputError("Invalid voice PCM sample length")
    if base64.b64encode(pcm).decode("ascii") != encoded:
        raise VoiceInputError("Noncanonical voice audio encoding")
    return pcm


def voice_audio_input(payload: dict) -> AudioInput:
    pcm = decode_voice_turn(payload)
    try:
        import numpy as np
        from agents.voice import AudioInput
    except ImportError:
        raise VoiceUnavailableError("SDK voice dependencies are unavailable") from None
    # Explicit little endian wire samples, converted to native int16 for the SDK.
    samples = np.frombuffer(pcm, dtype="<i2").astype(np.int16, copy=True)
    return AudioInput(buffer=samples, frame_rate=VOICE_SAMPLE_RATE, sample_width=2, channels=1)


def managed_voice_pipeline(
    run_transcription: Callable[[str], AsyncGenerator[str, None]],
    *, stt_model: STTModel, tts_model: TTSModel,
) -> VoicePipeline:
    """Caller callback must use the authorized durable conversation path.

    No default model/client, local history, or independent approval bypass is
    introduced here. The caller must consume and close the SDK output iterator.
    """
    if not callable(run_transcription):
        raise VoiceInputError("Voice requires a conversation callback")
    try:
        from agents.voice import STTModel, TTSModel, VoicePipeline, VoicePipelineConfig, VoiceWorkflowBase
    except ImportError:
        raise VoiceUnavailableError("SDK voice dependencies are unavailable") from None
    if not isinstance(stt_model, STTModel) or not isinstance(tts_model, TTSModel):
        raise VoiceInputError("Voice requires explicit SDK STT and TTS models")

    class RuntimeVoiceWorkflow(VoiceWorkflowBase):
        async def run(self, transcription):
            if not isinstance(transcription, str) or not transcription.strip() or len(transcription) > MAX_VOICE_TEXT_CHARS:
                raise VoiceInputError("Invalid voice transcription")
            output = run_transcription(transcription)
            if not isinstance(output, AsyncGenerator):
                raise VoiceInputError("Voice callback requires a closeable async generator")
            count = 0
            async with aclosing(output):
                async for text in output:
                    if not isinstance(text, str):
                        raise VoiceInputError("Invalid voice text output")
                    count += len(text)
                    if count > MAX_VOICE_TEXT_CHARS:
                        raise VoiceInputError("Voice text output exceeds limit")
                    if text:
                        yield text

    return VoicePipeline(workflow=RuntimeVoiceWorkflow(), stt_model=stt_model, tts_model=tts_model,
        config=VoicePipelineConfig(tracing_disabled=True, trace_include_sensitive_data=False,
                                   trace_include_sensitive_audio_data=False))
