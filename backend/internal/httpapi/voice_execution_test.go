package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	businessruntime "content-agent/backend/internal/runtime"
)

func TestManagedVoiceCancellationRetainsSlotUntilTransportCleanup(t *testing.T) {
	m, project, meta := directManagerFixture(t)
	_, execution, err := newManagedVoiceControl(context.Background(), m, "s", 1,
		project.ProjectID, project.PrimaryConversationID,
		func(_ context.Context, text string) (agentcontract.MessageRequest, businessruntime.CommandMeta, error) {
			return agentcontract.MessageRequest{Content: text}, meta, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { execution.finish(errors.New("fixture cleanup")) })
	turn, claimed, err := execution.prepare(context.Background(), "Transcript")
	if err != nil || !claimed {
		t.Fatalf("voice was not claimed: %v", err)
	}
	if !m.cancel(turn.AgentTurnID) {
		t.Fatal("manager could not cancel voice")
	}
	select {
	case <-execution.transportContext().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("manager cancellation did not reach transport owner")
	}
	select {
	case <-execution.done:
		t.Fatal("execution finished before transport cleanup")
	default:
	}
	if len(m.sem) != 1 {
		t.Fatal("manager released live voice capacity")
	}
	if err := execution.finish(nil); err == nil {
		t.Fatal("cancelled voice was reported as successful")
	}
	m.workers.Wait()
	if len(m.sem) != 0 {
		t.Fatal("manager retained capacity after transport cleanup")
	}
}

func TestManagedVoiceProtocolOwnsTurnAndDrainsManagerBeforeReturn(t *testing.T) {
	m, project, meta := directManagerFixture(t)
	factory := func(_ context.Context, text string) (agentcontract.MessageRequest, businessruntime.CommandMeta, error) {
		return agentcontract.MessageRequest{Content: text}, meta, nil
	}
	control, execution, err := newManagedVoiceControl(context.Background(), m, "s", 1,
		project.ProjectID, project.PrimaryConversationID, factory)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.finish(errors.New("fixture cleanup"))
	prepare := voiceControlFrame(1, "prepare_turn", map[string]any{"project_id": project.ProjectID,
		"conversation_id": project.PrimaryConversationID, "transcription": "Transcript"})
	raw, err := control.handle(context.Background(), prepare)
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Result struct {
			Claimed bool `json:"claimed"`
			Request struct {
				TurnID     string `json:"agent_turn_id"`
				Generation int64  `json:"dispatch_generation"`
			} `json:"request"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil || !receipt.Result.Claimed || receipt.Result.Request.TurnID == "" {
		t.Fatalf("missing managed grant: %s, %v", raw, err)
	}
	persist := voiceControlFrame(2, "persist_event", map[string]any{
		"agent_turn_id": receipt.Result.Request.TurnID, "dispatch_generation": receipt.Result.Request.Generation,
		"event": map[string]any{"event": "agent.tool.started", "data": map[string]any{"tool_name": "inspect_project"}}})
	if _, err := control.handle(context.Background(), persist); err != nil {
		t.Fatal(err)
	}
	if err := execution.finish(errors.New("transport disconnected")); err == nil {
		t.Fatal("disconnected voice execution reported success")
	}
	m.workers.Wait()
	if len(m.sem) != 0 || m.cancel(receipt.Result.Request.TurnID) {
		t.Fatal("voice execution leaked manager ownership")
	}
	replayed, replayExecution, err := newManagedVoiceControl(context.Background(), m, "s", 1,
		project.ProjectID, project.PrimaryConversationID, factory)
	if err != nil {
		t.Fatal(err)
	}
	defer replayExecution.finish(nil)
	raw, err = replayed.handle(context.Background(), prepare)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &receipt); err != nil || receipt.Result.Claimed {
		t.Fatal("voice replay was granted another execution")
	}
}

func TestManagedVoiceClosedBeforePrepareDoesNotCreateMessage(t *testing.T) {
	calls := 0
	control, execution, err := newManagedVoiceControl(context.Background(), &agentTurnManager{}, "s", 1, "p", "c",
		func(context.Context, string) (agentcontract.MessageRequest, businessruntime.CommandMeta, error) {
			calls++
			return agentcontract.MessageRequest{}, businessruntime.CommandMeta{}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.finish(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := control.handle(context.Background(), voicePrepareFrame()); err == nil || calls != 0 {
		t.Fatalf("closed voice admitted work: calls=%d error=%v", calls, err)
	}
}
