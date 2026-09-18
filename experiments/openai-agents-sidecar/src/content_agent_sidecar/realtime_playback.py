"""Validated browser playback acknowledgements for the SDK PCM16 tracker."""

import math

from agents.realtime.model import RealtimePlaybackTracker


class ManagedPlaybackTracker(RealtimePlaybackTracker):
    def __init__(self):
        super().__init__()
        self.set_audio_format("pcm16")
        self._delivered = {}
        self._retired = set()
        self._sequence = 0
        self._played_sequence = 0

    def delivered(self, item_id, content_index, audio):
        if (not isinstance(item_id, str) or not item_id or len(item_id) > 256
                or type(content_index) is not int or content_index < 0
                or not isinstance(audio, bytes) or not audio or len(audio) % 2
                or len(audio) > 24000 * 2 * 60):
            raise ValueError("Invalid realtime audio delivery")
        key = (item_id, content_index)
        if key in self._retired:
            raise ValueError("Realtime audio item has already been retired")
        if key not in self._delivered:
            if len(self._delivered) >= 128:
                raise ValueError("Realtime playback queue exceeds limit")
            if self._sequence >= 4096:
                raise ValueError("Realtime playback session item limit reached")
            self._sequence += 1
            self._delivered[key] = [self._sequence, 0, 0.0]
        entry = self._delivered[key]
        if entry[0] < self._played_sequence or entry[1] + len(audio) > 24000 * 2 * 60:
            raise ValueError("Realtime audio delivery is stale or exceeds limit")
        entry[1] += len(audio)

    def acknowledge(self, item_id, content_index, elapsed_ms):
        if (not isinstance(item_id, str) or type(content_index) is not int
                or type(elapsed_ms) not in {int, float}
                or type(elapsed_ms) is float and not math.isfinite(elapsed_ms)):
            raise ValueError("Invalid realtime playback acknowledgement")
        entry = self._delivered.get((item_id, content_index))
        if (entry is None or entry[0] < self._played_sequence
                or elapsed_ms < entry[2] or elapsed_ms > entry[1] / 48):
            raise ValueError("Realtime playback acknowledgement is stale or exceeds delivered audio")
        self.on_play_ms(item_id, content_index, elapsed_ms - entry[2])
        entry[2] = elapsed_ms
        self._played_sequence = entry[0]
        obsolete = [key for key, value in self._delivered.items() if value[0] < entry[0]]
        for key in obsolete:
            self._retired.add(key)
            del self._delivered[key]

    def on_interrupted(self):
        super().on_interrupted()
        self._retired.update(self._delivered)
        self._delivered.clear()
        self._played_sequence = self._sequence
