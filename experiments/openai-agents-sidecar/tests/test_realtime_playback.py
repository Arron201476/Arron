import pytest
import asyncio
from agents.realtime import RealtimeAgent
from agents.realtime.model_events import RealtimeModelAudioEvent

from content_agent_sidecar.native_realtime import RealtimeSessionError, managed_realtime_session
from test_native_realtime import Model, config, owner

from content_agent_sidecar.realtime_playback import ManagedPlaybackTracker


def test_playback_tracks_cumulative_ack_without_double_counting():
    tracker = ManagedPlaybackTracker()
    tracker.delivered("item", 0, b"\0" * 4800)
    tracker.acknowledge("item", 0, 50)
    tracker.acknowledge("item", 0, 50)
    tracker.acknowledge("item", 0, 100)
    assert tracker.get_state()["elapsed_ms"] == 100


@pytest.mark.parametrize("value", [-1, 101, float("nan"), float("inf"), True, "50"])
def test_playback_rejects_invalid_progress(value):
    tracker = ManagedPlaybackTracker()
    tracker.delivered("item", 0, b"\0" * 4800)
    with pytest.raises(ValueError):
        tracker.acknowledge("item", 0, value)


def test_playback_rejects_old_item_and_interrupted_acknowledgements():
    tracker = ManagedPlaybackTracker()
    tracker.delivered("first", 0, b"\0" * 4800)
    tracker.delivered("second", 0, b"\0" * 4800)
    tracker.acknowledge("first", 0, 100)
    tracker.acknowledge("second", 0, 20)
    with pytest.raises(ValueError):
        tracker.acknowledge("first", 0, 100)
    tracker.on_interrupted()
    with pytest.raises(ValueError):
        tracker.acknowledge("second", 0, 30)
    assert tracker.get_state()["current_item_id"] is None


def test_completed_items_do_not_exhaust_pending_queue():
    tracker = ManagedPlaybackTracker()
    for number in range(200):
        key = f"item-{number}"
        tracker.delivered(key, 0, b"\0" * 48)
        tracker.acknowledge(key, 0, 1)
    assert len(tracker._delivered) == 1
    assert tracker.get_state()["current_item_id"] == "item-199"


def test_oversized_delivery_does_not_mutate_tracker():
    tracker = ManagedPlaybackTracker()
    with pytest.raises(ValueError):
        tracker.delivered("oversized", 0, b"\0" * (24000 * 2 * 60 + 2))
    assert tracker._sequence == 0 and not tracker._delivered


def test_huge_integer_progress_is_rejected_without_overflow_or_mutation():
    tracker = ManagedPlaybackTracker()
    tracker.delivered("item", 0, b"\0" * 48)
    with pytest.raises(ValueError):
        tracker.acknowledge("item", 0, 10 ** 400)
    assert tracker.get_state()["current_item_id"] is None


def test_session_limit_still_allows_last_acknowledgement_and_interrupt():
    tracker = ManagedPlaybackTracker()
    for number in range(4096):
        key = f"item-{number}"
        tracker.delivered(key, 0, b"\0" * 48)
        tracker.acknowledge(key, 0, 1)
    before = tracker.get_state()
    with pytest.raises(ValueError, match="session item limit"):
        tracker.delivered("excess", 0, b"\0" * 48)
    assert tracker.get_state() == before
    tracker.acknowledge("item-4095", 0, 1)
    tracker.on_interrupted()
    assert not tracker._delivered and len(tracker._retired) == 4096
    assert tracker.get_state()["current_item_id"] is None


def test_sdk_audio_delivery_and_browser_ack_share_the_configured_tracker():
    async def run():
        model = Model()
        async with managed_realtime_session(RealtimeAgent(name="fixture"), model, owner(), config()) as connection:
            tracker = model.options["playback_tracker"]
            with pytest.raises(ValueError):
                connection.acknowledge_playback("audio-item", 0, 50)
            await model.listeners[0].on_event(RealtimeModelAudioEvent(data=b"\0" * 4800,
                response_id="response-1", item_id="audio-item", content_index=0))
            events = connection.__aiter__()
            try:
                async with asyncio.timeout(2):
                    while (await anext(events)).type != "audio":
                        pass
                connection.acknowledge_playback("audio-item", 0, 50)
                assert tracker.get_state() == {"current_item_id": "audio-item",
                    "current_item_content_index": 0, "elapsed_ms": 50}
            finally:
                await events.aclose()
            with pytest.raises(RealtimeSessionError):
                connection.acknowledge_playback("audio-item", 0, 100)
        assert model.closed
    asyncio.run(run())
