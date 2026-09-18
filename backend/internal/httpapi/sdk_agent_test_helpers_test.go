package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

type scriptedSDKAgent struct {
	store  *businessruntime.Store
	decide func(agentcontract.AgentInput) (agentcontract.AgentDecision, error)
}

func (*scriptedSDKAgent) GenericChatAvailable() bool { return true }

func (a *scriptedSDKAgent) StreamTurn(
	ctx context.Context,
	input agentcontract.AgentInput,
	idempotencyKey string,
	agentTurnID string,
	handle shell.AgentTurnStreamHandler,
) (bool, error) {
	decision, err := a.decide(input)
	if err != nil {
		return true, err
	}
	exchange, err := a.store.CreateMessageExchange(ctx, businessruntime.CreateMessageExchangeCommand{
		CommandMeta: businessruntime.CommandMeta{
			Scope: input.ProjectID, CommandType: "commit_sdk_agent_turn",
			IdempotencyKey: idempotencyKey, RequestHash: "scripted-sdk-" + agentTurnID,
		},
		ConversationID: input.ConversationID,
		Request:        input.Request,
		Decision:       decision,
	})
	if err != nil {
		return true, err
	}
	payload, err := json.Marshal(map[string]any{
		"exchange": map[string]any{"data": exchange},
		"observation": map[string]any{
			"schema_version": "agent_turn_observation.v1",
			"provider_id":    "scripted-sdk-test", "model_id": "deterministic",
			"release_id": "test",
		},
	})
	if err != nil {
		return true, err
	}
	if handle == nil {
		return true, fmt.Errorf("Agent turn stream handler is required")
	}
	return true, handle(shell.AgentTurnEvent{
		SchemaVersion: "1.0.0", EventID: "scripted_" + agentTurnID,
		EventType: "agent.turn.committed", ProjectID: input.ProjectID,
		ConversationID: input.ConversationID, TurnID: agentTurnID,
		Terminal: true, Payload: payload, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func startTestAgentTurnManager(t *testing.T, server *Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		wait, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := server.WaitAgentTurnManager(wait); err != nil {
			t.Errorf("WaitAgentTurnManager() error = %v", err)
		}
	})
	if err := server.StartAgentTurnManager(ctx); err != nil {
		t.Fatalf("StartAgentTurnManager() error = %v", err)
	}
}

func waitForCommittedTestExchange(
	t *testing.T,
	store *businessruntime.Store,
	projectID string,
	idempotencyKey string,
	agentTurnID string,
) businessruntime.MessageExchange {
	t.Helper()
	waitForAgentTurnStatus(t, store, agentTurnID, "committed")
	exchange, found, err := store.GetCommittedAgentTurn(
		context.Background(), projectID, idempotencyKey,
	)
	if err != nil || !found {
		t.Fatalf("GetCommittedAgentTurn() found = %v, error = %v", found, err)
	}
	return exchange
}

func testExchangeObject(t *testing.T, exchange businessruntime.MessageExchange) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(exchange)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
