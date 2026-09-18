import json
import pytest
from types import SimpleNamespace

from agents.realtime.events import RealtimeAudio, RealtimeError, RealtimeHistoryUpdated, RealtimeRawModelEvent
from agents.realtime.items import AssistantMessageItem, AssistantAudio, SystemMessageItem, InputText, InputImage, UserMessageItem
from agents.realtime.model_events import RealtimeModelAudioEvent, RealtimeModelTranscriptDeltaEvent

from content_agent_sidecar.realtime_events import project_realtime_event


def test_audio_projection_does_not_include_context_or_raw_extra_fields():
    event = RealtimeAudio(audio=RealtimeModelAudioEvent(data=b"\0\0", response_id="r", item_id="i", content_index=0),
                          item_id="i", content_index=0, info=SimpleNamespace(secret="private-context"))
    result = project_realtime_event(event)
    assert result["audio"] == "AAA=" and result["sample_rate"] == 24000
    assert "private-context" not in json.dumps(result)


def test_history_projection_omits_system_prompt_and_audio_payload():
    event = RealtimeHistoryUpdated(history=[
        SystemMessageItem(item_id="s", content=[InputText(text="private-system")]),
        AssistantMessageItem(item_id="a", content=[AssistantAudio(audio="private-audio", transcript="spoken text")]),
    ], info=SimpleNamespace(secret="private-context"))
    assert project_realtime_event(event) == {"type": "history_updated", "items": [
        {"item_id": "a", "role": "assistant", "text": "spoken text"}]}


def test_error_projection_is_fixed_and_transcript_is_allowlisted():
    assert project_realtime_event(RealtimeError(error=RuntimeError("private-key"), info=None)) == {
        "type": "error", "code": "REALTIME_EXECUTION_FAILED"}
    event = RealtimeRawModelEvent(data=RealtimeModelTranscriptDeltaEvent(item_id="i", delta="hello", response_id="r"), info=None)
    assert project_realtime_event(event) == {"type": "transcript_delta", "item_id": "i", "delta": "hello"}
    assert project_realtime_event(RealtimeRawModelEvent(data=SimpleNamespace(type="unknown", key="secret"), info=None)) is None


def test_image_extra_transcript_is_not_exposed_as_message_text():
    item = UserMessageItem(item_id="user", content=[InputImage(image_url="private-image",
                           transcript="private-extra-metadata"), InputText(text="visible")])
    result = project_realtime_event(RealtimeHistoryUpdated(history=[item], info=None))
    assert result["items"][0]["text"] == "visible"
    assert "private" not in json.dumps(result)


def test_message_text_limit_is_checked_across_content_parts():
    item = UserMessageItem(item_id="user", content=[InputText(text="x" * 40000), InputText(text="y" * 40000)])
    with pytest.raises(ValueError, match="history text"):
        project_realtime_event(RealtimeHistoryUpdated(history=[item], info=None))


def test_history_aggregate_limit_is_enforced_without_truncation():
    items = [UserMessageItem(item_id=f"item-{index}", content=[InputText(text="x" * 64000)]) for index in range(16)]
    with pytest.raises(ValueError, match="history text"):
        project_realtime_event(RealtimeHistoryUpdated(history=items, info=None))
