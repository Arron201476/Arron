package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
)

// voiceOutput validates only the public audio projection. Private control
// frames deliberately do not share this schema and cannot pass this boundary.
type voiceOutput struct {
	sessionID  string
	generation int64
	next       int64
	totalBytes int
	ended      bool
}

func newVoiceOutput(sessionID string, generation int64) (*voiceOutput, error) {
	if sessionID == "" || generation < 1 {
		return nil, errors.New("invalid voice output binding")
	}
	return &voiceOutput{sessionID: sessionID, generation: generation, next: 1}, nil
}

func (v *voiceOutput) accept(raw []byte) (public json.RawMessage, returnedErr error) {
	defer func() {
		if returnedErr != nil {
			v.ended = true
		}
	}()
	if v.ended || len(raw) > 96*1024 {
		return nil, errors.New("voice output is closed or oversized")
	}
	var sessionID string
	var generation, sequence int64
	var payload json.RawMessage
	if err := decodeVoiceObject(raw, map[string]any{"session_id": &sessionID, "generation": &generation,
		"sequence": &sequence, "payload": &payload}); err != nil {
		return nil, err
	}
	if sessionID != v.sessionID || generation != v.generation || sequence != v.next {
		return nil, errors.New("voice output binding or sequence mismatch")
	}
	var projection struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &projection); err != nil {
		return nil, errors.New("invalid voice output projection")
	}
	var kind string
	switch projection.Type {
	case "audio":
		var format, encoded string
		var sampleRate, channels int
		if err := decodeVoiceObject(payload, map[string]any{"type": &kind, "format": &format,
			"sample_rate": &sampleRate, "channels": &channels, "audio": &encoded}); err != nil {
			return nil, err
		}
		if format != "pcm16" || sampleRate != 24000 || channels != 1 || len(encoded) > 64000 {
			return nil, errors.New("invalid voice output audio format")
		}
		pcm, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil || len(pcm) == 0 || len(pcm)%2 != 0 || base64.StdEncoding.EncodeToString(pcm) != encoded {
			return nil, errors.New("invalid voice output PCM encoding")
		}
		if len(pcm) > 24000*2*600-v.totalBytes {
			return nil, errors.New("voice output exceeds session duration limit")
		}
		v.totalBytes += len(pcm)
	case "turn_started", "turn_ended", "session_ended":
		if err := decodeVoiceObject(payload, map[string]any{"type": &kind}); err != nil {
			return nil, err
		}
		v.ended = kind == "session_ended"
	default:
		return nil, errors.New("voice output contains a nonpublic event")
	}
	v.next++
	return append(json.RawMessage(nil), raw...), nil
}
