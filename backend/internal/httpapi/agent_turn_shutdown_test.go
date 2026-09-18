package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestAgentTurnManagerShutdownWaitsBeforeDatabaseCleanup(t *testing.T) {
	registry := capability.NewEmptyRegistry()
	database := filepath.Join(t.TempDir(), "shutdown.db")
	store, err := businessruntime.Open(database, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	agent := newSlowStreamingAgent(store)
	server := NewWithRuntime(shell.NewWithAgentService(registry, agent), store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	project, err := store.CreateProject(ctx, "Shutdown fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Wait"}, businessruntime.CommandMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := server.StartAgentTurnManager(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-agent.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent did not start")
	}
	cancel()
	wait, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err := server.WaitAgentTurnManager(wait); err != nil {
		t.Fatal(err)
	}
	agent.mu.Lock()
	active := agent.active
	agent.mu.Unlock()
	if active != 0 || len(server.agentTurns.sem) != 0 {
		t.Fatal("shutdown returned with active Agent executions")
	}
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("store closed before Agent cleanup: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(database); err != nil {
		t.Fatalf("database still held after manager shutdown: %v", err)
	}
}

func TestAgentTurnManagerWaitIsBoundedAndOptional(t *testing.T) {
	if err := (&Server{}).WaitAgentTurnManager(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := &Server{agentTurns: &agentTurnManager{done: make(chan struct{})}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := server.WaitAgentTurnManager(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait was not bounded: %v", err)
	}
	close(server.agentTurns.done)
	if err := server.WaitAgentTurnManager(context.Background()); err != nil {
		t.Fatal(err)
	}
}
