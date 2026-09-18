package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
)

func TestDirectTurnClaimDoesNotGrantExecutionOnReplayOrSchedulerPickup(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "Direct turn")
	if err != nil {
		t.Fatal(err)
	}
	meta := CommandMeta{Scope: project.ProjectID, CommandType: "create_message",
		IdempotencyKey: "11111111-1111-4111-8111-111111111111", RequestHash: "direct-v1"}
	request := agentcontract.MessageRequest{Content: "Transcript"}
	turn, claimed, err := store.AcceptAndClaimAgentTurn(ctx, project.PrimaryConversationID, request, meta)
	if err != nil || !claimed || turn.Status != "running" || turn.DispatchGeneration != 1 {
		t.Fatalf("direct claim: turn=%+v claimed=%v error=%v", turn, claimed, err)
	}
	replayed, claimed, err := store.AcceptAndClaimAgentTurn(ctx, project.PrimaryConversationID, request, meta)
	if err != nil || claimed || replayed.AgentTurnID != turn.AgentTurnID || replayed.DispatchGeneration != 1 {
		t.Fatalf("replay granted a new execution: turn=%+v claimed=%v error=%v", replayed, claimed, err)
	}
	runnable, err := store.ClaimRunnableAgentTurns(ctx, 10)
	if err != nil || len(runnable) != 0 {
		t.Fatalf("scheduler reclaimed direct turn: %+v, %v", runnable, err)
	}
	var started int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE subject_type = 'agent_turn' AND subject_id = ? AND event_type = 'agent.turn.started'`, turn.AgentTurnID).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != 1 {
		t.Fatalf("started events = %d, want 1", started)
	}
}

func TestDirectTurnClaimDoesNotJumpPendingConversationOrLeavePartialTurn(t *testing.T) {
	for _, status := range []string{"accepted", "running", "waiting_approval", "paused", "pausing", "cancel_requested", "committing"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(filepath.Join(t.TempDir(), "agent.db"), loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			project, err := store.CreateProject(ctx, "Queued turn")
			if err != nil {
				t.Fatal(err)
			}
			meta := CommandMeta{Scope: project.ProjectID, CommandType: "create_message",
				IdempotencyKey: "11111111-1111-4111-8111-111111111111", RequestHash: "queued-v1"}
			request := agentcontract.MessageRequest{Content: "Transcript"}
			prior, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, request, meta)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE agent_turns SET status = ? WHERE agent_turn_id = ?`, status, prior.AgentTurnID); err != nil {
				t.Fatal(err)
			}
			meta.IdempotencyKey = "22222222-2222-4222-8222-222222222222"
			meta.RequestHash = "direct-v2"
			_, claimed, err := store.AcceptAndClaimAgentTurn(ctx, project.PrimaryConversationID, request, meta)
			if err == nil || claimed {
				t.Fatalf("direct claim bypassed %s: claimed=%v error=%v", status, claimed, err)
			}
			turns, err := store.ListProjectAgentTurns(ctx, project.ProjectID, 10)
			if err != nil || len(turns) != 1 || turns[0].AgentTurnID != prior.AgentTurnID {
				t.Fatalf("rejected claim left partial acceptance: %+v, %v", turns, err)
			}
		})
	}
}

func TestDirectTurnClaimRecoveryAndCancellationNeverRegrantExecution(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "restart", true: "cancel_and_restart"}[cancel], func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(filepath.Join(t.TempDir(), "agent.db"), loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			project, err := store.CreateProject(ctx, "Interrupted direct turn")
			if err != nil {
				t.Fatal(err)
			}
			meta := CommandMeta{Scope: project.ProjectID, CommandType: "create_message",
				IdempotencyKey: "11111111-1111-4111-8111-111111111111", RequestHash: "direct-recovery"}
			request := agentcontract.MessageRequest{Content: "Transcript"}
			turn, claimed, err := store.AcceptAndClaimAgentTurn(ctx, project.PrimaryConversationID, request, meta)
			if err != nil || !claimed {
				t.Fatalf("claim failed: claimed=%v error=%v", claimed, err)
			}
			if cancel {
				if _, err := store.CancelAgentTurn(ctx, turn.AgentTurnID); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.RecoverInterruptedAgentTurns(ctx); err != nil {
				t.Fatal(err)
			}
			replayed, claimed, err := store.AcceptAndClaimAgentTurn(ctx, project.PrimaryConversationID, request, meta)
			wantStatus := "failed"
			if cancel {
				wantStatus = "cancelled"
			}
			if err != nil || claimed || replayed.AgentTurnID != turn.AgentTurnID || replayed.Status != wantStatus {
				t.Fatalf("recovery replay: turn=%+v claimed=%v error=%v", replayed, claimed, err)
			}
			_, submission, err := store.LookupAgentTurnSubmission(ctx, project.PrimaryConversationID, meta.IdempotencyKey)
			if err != nil || submission == nil || submission.Turn.AgentTurnID != turn.AgentTurnID || submission.Request.Content != request.Content {
				t.Fatalf("original message receipt was not preserved: %+v, %v", submission, err)
			}
		})
	}
}

func TestDirectTurnClaimRequiresNormalMessageIdempotencyBeforeDatabaseAccess(t *testing.T) {
	for _, meta := range []CommandMeta{
		{CommandType: "voice", IdempotencyKey: "key"},
		{CommandType: "create_message"},
	} {
		var store *Store
		_, claimed, err := store.AcceptAndClaimAgentTurn(context.Background(), "c", agentcontract.MessageRequest{Content: "Transcript"}, meta)
		if err == nil || claimed {
			t.Fatalf("invalid submission admitted: claimed=%v error=%v", claimed, err)
		}
	}
}
