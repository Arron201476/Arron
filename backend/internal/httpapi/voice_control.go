package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"content-agent/backend/internal/agentcontract"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

// voiceControl handles private Sidecar frames only. Its replies and incoming
// checkpoints must never be passed through to a browser audio connection.
type voiceControl struct {
	mu             sync.Mutex
	sessionID      string
	generation     int64
	projectID      string
	conversationID string
	next           int64
	closed         bool
	turn           *businessruntime.AgentTurn
	prepare        func(context.Context, string) (businessruntime.AgentTurn, bool, error)
	persist        func(context.Context, businessruntime.AgentTurn, shell.AgentTurnEvent) error
}

func newVoiceControl(sessionID string, generation int64, projectID, conversationID string,
	prepare func(context.Context, string) (businessruntime.AgentTurn, bool, error),
	persist func(context.Context, businessruntime.AgentTurn, shell.AgentTurnEvent) error,
) (*voiceControl, error) {
	if sessionID == "" || projectID == "" || conversationID == "" || generation < 1 || prepare == nil || persist == nil {
		return nil, errors.New("voice control requires an authorized binding and durable handlers")
	}
	return &voiceControl{sessionID: sessionID, generation: generation, projectID: projectID,
		conversationID: conversationID, next: 1, prepare: prepare, persist: persist}, nil
}

func decodeVoiceObject(raw []byte, fields map[string]any) error {
	if len(raw) > agentcontract.MaxSDKRunStateEnvelopeBytes || !utf8.Valid(raw) {
		return errors.New("invalid voice control envelope size or encoding")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("voice control requires an object")
	}
	seen := make(map[string]bool, len(fields))
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] || fields[name] == nil {
			return errors.New("unknown or duplicated voice control field")
		}
		seen[name] = true
		if err := decoder.Decode(fields[name]); err != nil {
			return errors.New("invalid voice control field value")
		}
	}
	if _, err := decoder.Token(); err != nil || len(seen) != len(fields) {
		return errors.New("incomplete voice control object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing voice control data")
	}
	return nil
}

func (v *voiceControl) handle(ctx context.Context, raw []byte) (reply []byte, returnedErr error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	defer func() {
		if returnedErr != nil {
			v.closed = true
		}
	}()
	if v.closed {
		return nil, errors.New("voice control is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var kind, sessionID, operation string
	var generation, requestID int64
	var payload json.RawMessage
	if err := decodeVoiceObject(raw, map[string]any{"type": &kind, "session_id": &sessionID,
		"generation": &generation, "request_id": &requestID, "operation": &operation, "payload": &payload}); err != nil {
		return nil, err
	}
	if kind != "voice_control_request" || sessionID != v.sessionID || generation != v.generation || requestID != v.next {
		return nil, errors.New("voice control request binding or sequence mismatch")
	}
	v.next++
	var result any
	switch operation {
	case "prepare_turn":
		if requestID != 1 || v.turn != nil {
			return nil, errors.New("voice execution preparation was already attempted")
		}
		var projectID, conversationID, transcription string
		if err := decodeVoiceObject(payload, map[string]any{"project_id": &projectID,
			"conversation_id": &conversationID, "transcription": &transcription}); err != nil {
			return nil, err
		}
		if projectID != v.projectID || conversationID != v.conversationID || strings.TrimSpace(transcription) == "" || utf8.RuneCountInString(transcription) > 65536 {
			return nil, errors.New("voice transcription does not match its conversation")
		}
		turn, claimed, err := v.prepare(ctx, transcription)
		if err != nil {
			return nil, err
		}
		if !claimed {
			v.closed = true
			result = map[string]any{"claimed": false, "request": nil}
			break
		}
		if turn.ProjectID != v.projectID || turn.ConversationID != v.conversationID || turn.AgentTurnID == "" ||
			turn.IdempotencyKey == "" || turn.Status != "running" || turn.DispatchGeneration < 1 || turn.Request.Content != transcription {
			return nil, errors.New("voice execution grant is unconfirmed")
		}
		v.turn = &turn
		result = map[string]any{"claimed": true, "request": map[string]any{
			"project_id": turn.ProjectID, "conversation_id": turn.ConversationID, "agent_turn_id": turn.AgentTurnID,
			"idempotency_key": turn.IdempotencyKey, "dispatch_generation": turn.DispatchGeneration, "request": turn.Request}}
	case "persist_event":
		if v.turn == nil {
			return nil, errors.New("voice event has no execution grant")
		}
		var turnID string
		var dispatchGeneration int64
		var eventRaw json.RawMessage
		if err := decodeVoiceObject(payload, map[string]any{"agent_turn_id": &turnID,
			"dispatch_generation": &dispatchGeneration, "event": &eventRaw}); err != nil {
			return nil, err
		}
		if turnID != v.turn.AgentTurnID || dispatchGeneration != v.turn.DispatchGeneration {
			return nil, errors.New("voice event execution binding mismatch")
		}
		var eventType string
		var data json.RawMessage
		if err := decodeVoiceObject(eventRaw, map[string]any{"event": &eventType, "data": &data}); err != nil {
			return nil, err
		}
		event := shell.AgentTurnEvent{SchemaVersion: "1.0.0", ProjectID: v.projectID, ConversationID: v.conversationID,
			TurnID: turnID, EventType: eventType, Payload: data,
			Terminal: eventType == "agent.turn.committed" || eventType == "agent.turn.failed" || eventType == "agent.turn.cancelled"}
		final, err := shell.ValidateAgentTurnEvent(event, v.projectID, v.conversationID, turnID)
		if err != nil {
			return nil, err
		}
		if err := v.persist(ctx, *v.turn, event); err != nil {
			return nil, err
		}
		v.closed = final
		result = map[string]any{"accepted": true, "agent_turn_id": turnID, "dispatch_generation": dispatchGeneration}
	default:
		return nil, errors.New("unsupported voice control operation")
	}
	return json.Marshal(map[string]any{"type": "voice_control_response", "session_id": v.sessionID,
		"generation": v.generation, "request_id": requestID, "operation": operation, "result": result})
}
