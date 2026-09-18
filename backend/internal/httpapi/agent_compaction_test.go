package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestNativeCompactionProtocolFailureRemainsActionableInTurnRecord(t *testing.T) {
	registry := capability.NewEmptyRegistry()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "compaction.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Native compaction")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Continue"}, businessruntime.CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	manager := newAgentTurnManager(store, shell.New(registry), slog.Default())
	if err := manager.persistEvent(ctx, turn, shell.AgentTurnEvent{EventType: "agent.turn.failed", Payload: json.RawMessage(`{"failure_stage":"session_compaction"}`)}); err != nil {
		t.Fatal(err)
	}
	failed, err := store.GetAgentTurn(ctx, turn.AgentTurnID)
	if err != nil || failed.ErrorCode == nil || *failed.ErrorCode != "NATIVE_COMPACTION_UNAVAILABLE" || failed.ErrorMessage == nil || !strings.Contains(*failed.ErrorMessage, "原会话已保留") {
		t.Fatalf("compaction failure lost: %+v err=%v", failed, err)
	}
	if failed.Observation.FailureStage != "session_compaction" {
		t.Fatalf("observation=%+v", failed.Observation)
	}
}
