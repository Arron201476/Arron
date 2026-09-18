"""Single voice turn through the existing durable Agent execution stream."""

from contextlib import aclosing
from copy import deepcopy

from .contracts import AgentExecutionRequest
from .native_voice import MAX_VOICE_TEXT_CHARS, VoiceInputError


def voice_turn_workflow(runtime, project_id, conversation_id, prepare_turn, persist_event):
    """Prepare must persist/authorize the transcript; persist_event is private.

    The event sink must return literal True only after durable acceptance. It
    must not forward raw execution events (which can contain RunState) to a UI.
    A workflow is single-use, including after an uncertain preparation result.
    """
    if any(not isinstance(value, str) or not value or len(value) > 256
           or not value.isprintable() or value.strip() != value
           for value in (project_id, conversation_id)):
        raise VoiceInputError("Invalid voice conversation identity")
    if not callable(prepare_turn) or not callable(persist_event):
        raise VoiceInputError("Voice requires durable turn preparation and event persistence")
    used = False

    async def run(transcription):
        nonlocal used
        if used:
            raise VoiceInputError("Voice turn already attempted; reconcile before retrying")
        if not isinstance(transcription, str) or not transcription.strip() or len(transcription) > MAX_VOICE_TEXT_CHARS:
            raise VoiceInputError("Invalid voice transcription")
        used = True
        # Match Store's message-envelope normalization before requesting a grant.
        transcription = transcription.strip()
        request = await prepare_turn(transcription)
        if not isinstance(request, AgentExecutionRequest):
            raise VoiceInputError("Voice preparation requires a durable execution request")
        request = request.model_copy(deep=True)
        if (request.project_id != project_id or request.conversation_id != conversation_id
                or not request.agent_turn_id or request.run_state is not None or request.approval_decisions
                or request.request.get("content") != transcription):
            raise VoiceInputError("Voice execution does not match its prepared transcript")
        stream = runtime.start_execution_stream(request)
        terminal = False
        spoken_text = None
        try:
            async with aclosing(stream.events()) as events:
                async for event in events:
                    if terminal:
                        raise VoiceInputError("Voice execution emitted events after its terminal result")
                    # Isolate the execution snapshot from a persistence adapter's mutation.
                    accepted = await persist_event(request.model_copy(deep=True), deepcopy(event))
                    if accepted is not True:
                        raise VoiceInputError("Voice execution event persistence is unconfirmed")
                    kind = event.get("event")
                    if kind == "agent.turn.committed":
                        terminal = True
                        data = event.get("data", {})
                        exchange = data.get("exchange", {})
                        message = exchange.get("data", {}).get("agent_message", {})
                        text = message.get("content")
                        if not isinstance(text, str) or len(text) > MAX_VOICE_TEXT_CHARS:
                            raise VoiceInputError("Voice committed response has no bounded text")
                        spoken_text = text
                    elif kind in {"agent.turn.waiting_approval", "agent.turn.paused", "agent.turn.cancelled"}:
                        terminal = True
            if not terminal:
                raise VoiceInputError("Voice execution ended without a terminal receipt")
        finally:
            await stream.aclose(reason="voice_workflow_closed")
        if spoken_text:
            yield spoken_text

    return run
