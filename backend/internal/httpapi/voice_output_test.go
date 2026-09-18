package httpapi

import (
	"encoding/json"
	"testing"
)

func voiceOutputFrame(sequence int, payload any) []byte {
	raw, _ := json.Marshal(map[string]any{"session_id": "s", "generation": 1, "sequence": sequence, "payload": payload})
	return raw
}

func TestVoiceOutputAllowsBoundPCMAndClosesAtSessionEnd(t *testing.T) {
	output, _ := newVoiceOutput("s", 1)
	audio := voiceOutputFrame(1, map[string]any{"type": "audio", "format": "pcm16", "sample_rate": 24000, "channels": 1, "audio": "AAA="})
	if public, err := output.accept(audio); err != nil || string(public) != string(audio) || output.totalBytes != 2 {
		t.Fatalf("bounded audio rejected: %v", err)
	}
	if _, err := output.accept(voiceOutputFrame(2, map[string]any{"type": "session_ended"})); err != nil {
		t.Fatal(err)
	}
	if _, err := output.accept(voiceOutputFrame(3, map[string]any{"type": "turn_started"})); err == nil {
		t.Fatal("accepted output after session end")
	}
}

func TestVoiceOutputDoesNotExposePrivateFramesOrUnknownFields(t *testing.T) {
	frames := [][]byte{
		voicePrepareFrame(),
		voiceOutputFrame(1, map[string]any{"type": "session_ended", "run_state": "private"}),
		voiceOutputFrame(1, map[string]any{"type": "voice_control_response", "result": "private"}),
		voiceOutputFrame(1, map[string]any{"type": "error", "message": "private exception"}),
		voiceOutputFrame(2, map[string]any{"type": "turn_started"}),
		voiceOutputFrame(1, map[string]any{"type": "audio", "format": "pcm16", "sample_rate": 24000, "channels": 1, "audio": "AA=="}),
		voiceOutputFrame(1, map[string]any{"type": "audio", "format": "pcm16", "sample_rate": 24000, "channels": 1, "audio": "AAA=\n"}),
	}
	for _, frame := range frames {
		output, _ := newVoiceOutput("s", 1)
		if public, err := output.accept(frame); err == nil || len(public) != 0 || !output.ended {
			t.Fatalf("unsafe public projection accepted: %s, %v", frame, err)
		}
	}
}
