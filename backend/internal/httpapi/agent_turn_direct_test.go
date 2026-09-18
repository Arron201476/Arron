package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func directManagerFixture(t *testing.T) (*agentTurnManager, businessruntime.Project, businessruntime.CommandMeta) {
	t.Helper()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "direct.db"), capability.NewEmptyRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	project, err := store.CreateProject(context.Background(), "Direct manager")
	if err != nil {
		t.Fatal(err)
	}
	m := newAgentTurnManager(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.running = true
	return m, project, businessruntime.CommandMeta{Scope: project.ProjectID, CommandType: "create_message",
		IdempotencyKey: "11111111-1111-4111-8111-111111111111", RequestHash: "direct"}
}

func TestDirectManagerRegistersCancellationAndWaitsForCleanup(t *testing.T) {
	m, project, meta := directManagerFixture(t)
	started := make(chan string, 1)
	cleaned := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, claimed, err := m.runDirectTurn(context.Background(), project.PrimaryConversationID,
			agentcontract.MessageRequest{Content: "Transcript"}, meta,
			func(ctx context.Context, turn businessruntime.AgentTurn, _ shell.AgentTurnStreamHandler) error {
				defer close(cleaned)
				started <- turn.AgentTurnID
				<-ctx.Done()
				return ctx.Err()
			})
		if !claimed && err == nil {
			err = errors.New("direct execution was not claimed")
		}
		done <- err
	}()
	var turnID string
	select {
	case turnID = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("direct execution did not start")
	}
	if _, err := m.store.CancelAgentTurn(context.Background(), turnID); err != nil {
		t.Fatal(err)
	}
	if !m.cancel(turnID) {
		t.Fatal("direct execution was missing from manager cancellation")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected cancellation outcome: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("direct execution did not clean up")
	}
	<-cleaned
	m.workers.Wait()
	if len(m.sem) != 0 || m.cancel(turnID) {
		t.Fatal("direct execution leaked registration or concurrency slot")
	}
	turn, err := m.store.GetAgentTurn(context.Background(), turnID)
	if err != nil || turn.Status != "cancelled" {
		t.Fatalf("cancellation was not durable: %+v, %v", turn, err)
	}
}

func TestDirectManagerDoesNotReportSuccessWithoutTerminalOrReexecuteReplay(t *testing.T) {
	m, project, meta := directManagerFixture(t)
	invocations := 0
	run := func(context.Context, businessruntime.AgentTurn, shell.AgentTurnStreamHandler) error {
		invocations++
		return nil
	}
	turn, claimed, err := m.runDirectTurn(context.Background(), project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "Transcript"}, meta, run)
	if err == nil || !claimed || turn.Status != "failed" {
		t.Fatalf("missing terminal was treated as success: %+v, %v, %v", turn, claimed, err)
	}
	repeated, claimed, err := m.runDirectTurn(context.Background(), project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "Transcript"}, meta, run)
	if err != nil || claimed || repeated.AgentTurnID != turn.AgentTurnID || invocations != 1 {
		t.Fatalf("replay reexecuted: %+v, %v, %v, calls=%d", repeated, claimed, err, invocations)
	}
}

func TestDirectManagerRejectsEventsAfterTransportReturns(t *testing.T) {
	m, project, meta := directManagerFixture(t)
	var late shell.AgentTurnStreamHandler
	turn, _, _ := m.runDirectTurn(context.Background(), project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "Transcript"}, meta,
		func(_ context.Context, _ businessruntime.AgentTurn, handle shell.AgentTurnStreamHandler) error {
			late = handle
			return nil
		})
	if late == nil {
		t.Fatal("transport did not start")
	}
	err := late(shell.AgentTurnEvent{SchemaVersion: "1.0.0", ProjectID: project.ProjectID,
		ConversationID: project.PrimaryConversationID, TurnID: turn.AgentTurnID, EventType: "agent.tool.started"})
	if err == nil || err.Error() != "direct Agent event transport is closed" {
		t.Fatalf("late event was not rejected before persistence: %v", err)
	}
}
