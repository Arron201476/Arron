package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"content-agent/backend/internal/agentcontract"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func voiceControlFrame(id int, operation string, payload any) []byte {
	raw, _ := json.Marshal(map[string]any{"type": "voice_control_request", "session_id": "s",
		"generation": 1, "request_id": id, "operation": operation, "payload": payload})
	return raw
}

func voicePrepareFrame() []byte {
	return voiceControlFrame(1, "prepare_turn", map[string]any{
		"project_id": "p", "conversation_id": "c", "transcription": "Transcript"})
}

func voiceGrantedTurn() businessruntime.AgentTurn {
	return businessruntime.AgentTurn{ProjectID: "p", ConversationID: "c", AgentTurnID: "t",
		IdempotencyKey: "key", Status: "running", DispatchGeneration: 1,
		Request: agentcontract.MessageRequest{Content: "Transcript"}}
}

func TestVoiceControlOnlyAcknowledgesBoundDurableEvents(t *testing.T) {
	prepared, persisted := 0, 0
	control, err := newVoiceControl("s", 1, "p", "c",
		func(context.Context, string) (businessruntime.AgentTurn, bool, error) {
			prepared++
			return voiceGrantedTurn(), true, nil
		}, func(_ context.Context, turn businessruntime.AgentTurn, event shell.AgentTurnEvent) error {
			persisted++
			if turn.AgentTurnID != "t" || event.TurnID != "t" || event.EventType != "agent.turn.paused" || event.Terminal {
				t.Fatal("private event binding changed")
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.handle(context.Background(), voicePrepareFrame()); err != nil {
		t.Fatal(err)
	}
	event := voiceControlFrame(2, "persist_event", map[string]any{"agent_turn_id": "t", "dispatch_generation": 1,
		"event": map[string]any{"event": "agent.turn.paused", "data": map[string]any{"run_state": "private"}}})
	reply, err := control.handle(context.Background(), event)
	if err != nil || prepared != 1 || persisted != 1 {
		t.Fatalf("private exchange failed: prepared=%d persisted=%d error=%v", prepared, persisted, err)
	}
	var receipt struct {
		Result struct {
			Accepted bool   `json:"accepted"`
			TurnID   string `json:"agent_turn_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(reply, &receipt); err != nil || !receipt.Result.Accepted || receipt.Result.TurnID != "t" {
		t.Fatalf("invalid receipt: %s, %v", reply, err)
	}
	if _, err := control.handle(context.Background(), event); err == nil || persisted != 1 {
		t.Fatal("terminal event was replayed")
	}
}

func TestVoiceControlPersistenceFailureIsStickyAndDoesNotAcknowledge(t *testing.T) {
	calls := 0
	control, _ := newVoiceControl("s", 1, "p", "c",
		func(context.Context, string) (businessruntime.AgentTurn, bool, error) {
			return voiceGrantedTurn(), true, nil
		},
		func(context.Context, businessruntime.AgentTurn, shell.AgentTurnEvent) error {
			calls++
			return errors.New("fixture failure")
		})
	if _, err := control.handle(context.Background(), voicePrepareFrame()); err != nil {
		t.Fatal(err)
	}
	frame := voiceControlFrame(2, "persist_event", map[string]any{"agent_turn_id": "t", "dispatch_generation": 1,
		"event": map[string]any{"event": "agent.tool.started", "data": map[string]any{}}})
	if reply, err := control.handle(context.Background(), frame); err == nil || len(reply) != 0 {
		t.Fatal("unconfirmed persistence was acknowledged")
	}
	if _, err := control.handle(context.Background(), frame); err == nil || calls != 1 {
		t.Fatal("unconfirmed event was retried")
	}
}

func TestVoiceControlRejectsMalformedFramesBeforePreparing(t *testing.T) {
	for _, raw := range []string{
		`{"type":"voice_control_request","type":"voice_control_request"}`,
		`{"type":"voice_control_request","session_id":"s","generation":true,"request_id":1,"operation":"prepare_turn","payload":{}}`,
		`{"type":"voice_control_request","session_id":"foreign","generation":1,"request_id":1,"operation":"prepare_turn","payload":{}}`,
		`[]`, `{} {}`, `{"unknown":true}`,
	} {
		calls := 0
		control, _ := newVoiceControl("s", 1, "p", "c",
			func(context.Context, string) (businessruntime.AgentTurn, bool, error) {
				calls++
				return voiceGrantedTurn(), true, nil
			}, func(context.Context, businessruntime.AgentTurn, shell.AgentTurnEvent) error { return nil })
		if _, err := control.handle(context.Background(), []byte(raw)); err == nil || calls != 0 {
			t.Fatalf("invalid frame reached preparation: calls=%d error=%v", calls, err)
		}
	}
}
