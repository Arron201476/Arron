package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
)

func TestExternalWriteUncertainOutcomeBlocksSuccessfulExecution(t *testing.T) {
	for _, mode := range []string{"conversation", "background_task", "stateful_workflow"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			var store *Store
			var call AgentToolCall
			var finish func() error
			switch mode {
			case "conversation":
				var project Project
				var turn AgentTurn
				store, project, turn, _ = pauseTurnFixture(t)
				claimInputTurn(t, store, turn.AgentTurnID)
				var err error
				call, err = store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: project.ProjectID,
					ConversationID: project.PrimaryConversationID, AgentTurnID: turn.AgentTurnID,
					ToolID: "mcp:fixture/save_fact", SDKToolCallID: "uncertain-write", Arguments: json.RawMessage(`{"value":"fact"}`),
					ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact")})
				if err != nil {
					t.Fatal(err)
				}
				finish = func() error { _, err := store.BeginAgentTurnCommit(ctx, turn.AgentTurnID); return err }
			case "background_task":
				var claimCommand ClaimAgentTaskCommand
				store, _, claimCommand = pausedBackgroundStore(t)
				claim, err := store.ClaimAgentTask(ctx, claimCommand)
				if err != nil || claim == nil {
					t.Fatal(err)
				}
				call = backgroundApprovalForTest(t, store, *claim, "uncertain-write")
				finish = func() error {
					_, err := store.CompleteAgentTask(ctx, CompleteAgentTaskCommand{AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID,
						AttemptToken: claim.AttemptToken, Result: json.RawMessage(`{"summary":"done"}`)})
					return err
				}
			case "stateful_workflow":
				store = openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "outcome.db"))
				store.SetAgentToolRegistry(agentToolRegistryForTest(t))
				project, claim := statefulToolClaimForTest(t, store)
				call = statefulApprovalForTest(t, store, project, claim, "uncertain-write")
				finish = func() error {
					_, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID,
						AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash,
						ResponsePayload: json.RawMessage(`{"summary":"done"}`)})
					return err
				}
			}
			defer store.Close()
			for _, status := range []string{"failed", "cancelled", "running", "approved", "pending_approval"} {
				if _, err := store.db.Exec(`UPDATE agent_tool_calls SET status=?,started_at=? WHERE agent_tool_call_id=?`, status, formatTime(store.now()), call.AgentToolCallID); err != nil {
					t.Fatal(err)
				}
				if err := finish(); err == nil {
					t.Fatalf("%s external write allowed successful execution", status)
				}
			}
			if _, err := store.db.Exec(`UPDATE agent_tool_calls SET status='completed',result_hash='confirmed' WHERE agent_tool_call_id=?`, call.AgentToolCallID); err != nil {
				t.Fatal(err)
			}
			if err := finish(); err != nil {
				t.Fatal("completed write blocked execution", err)
			}
		})
	}
}

func TestExternalWriteCannotStartAfterMainCommitBegins(t *testing.T) {
	ctx := context.Background()
	store, project, turn, _ := pauseTurnFixture(t)
	defer store.Close()
	claimInputTurn(t, store, turn.AgentTurnID)
	command := BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		AgentTurnID: turn.AgentTurnID, SDKToolCallID: "late-write", ToolID: "mcp:fixture/save_fact", Arguments: json.RawMessage(`{"value":"fact"}`),
		ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact")}
	if _, err := store.BeginAgentTurnCommit(ctx, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAgentToolCall(ctx, command); err == nil {
		t.Fatal("new write registered after commit began")
	}
}

func TestExternalWriteAndMainCommitHaveOneTransactionWinner(t *testing.T) {
	ctx := context.Background()
	store, project, turn, _ := pauseTurnFixture(t)
	defer store.Close()
	claimInputTurn(t, store, turn.AgentTurnID)
	start := make(chan struct{})
	var group sync.WaitGroup
	var writeErr, commitErr error
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		_, writeErr = store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: project.ProjectID,
			ConversationID: project.PrimaryConversationID, AgentTurnID: turn.AgentTurnID,
			SDKToolCallID: "race", ToolID: "mcp:fixture/save_fact", Arguments: json.RawMessage(`{"value":"fact"}`),
			ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact")})
	}()
	go func() { defer group.Done(); <-start; _, commitErr = store.BeginAgentTurnCommit(ctx, turn.AgentTurnID) }()
	close(start)
	group.Wait()
	if (writeErr == nil) == (commitErr == nil) {
		t.Fatalf("expected one winner: write=%v commit=%v", writeErr, commitErr)
	}
	if writeErr != nil {
		assertDomainCode(t, writeErr, "AGENT_ACTIVITY_STALE")
	}
	if commitErr != nil {
		assertDomainCode(t, commitErr, "AGENT_TOOL_APPROVAL_REQUIRED")
	}
}

func TestExternalWriteLateCompletionGateAndNonWrites(t *testing.T) {
	ctx := context.Background()
	store, project, turn, _ := pauseTurnFixture(t)
	defer store.Close()
	claimInputTurn(t, store, turn.AgentTurnID)
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: project.ProjectID,
		ConversationID: project.PrimaryConversationID, AgentTurnID: turn.AgentTurnID,
		SDKToolCallID: "boundary", ToolID: "mcp:fixture/save_fact", Arguments: json.RawMessage(`{"value":"fact"}`),
		ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact")})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []struct {
		status  string
		started any
		access  string
		blocked bool
	}{
		{"failed", formatTime(store.now()), "write", true},
		{"cancelled", formatTime(store.now()), "write", true},
		{"cancelled", nil, "write", false},
		{"rejected", nil, "write", false},
		{"failed", formatTime(store.now()), "read", false},
		{"completed", formatTime(store.now()), "write", false},
	} {
		if _, err := store.db.Exec(`UPDATE agent_tool_calls SET status=?,started_at=?,access_mode=? WHERE agent_tool_call_id=?`, state.status, state.started, state.access, call.AgentToolCallID); err != nil {
			t.Fatal(err)
		}
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = validateExternalToolOutcomesTx(ctx, tx, "conversation", turn.AgentTurnID)
		tx.Rollback()
		if (err != nil) != state.blocked {
			t.Fatalf("wrong outcome gate for %+v: %v", state, err)
		}
	}
	if _, err := store.BeginAgentTurnCommit(ctx, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_tool_calls SET status='approved' WHERE agent_tool_call_id=?`, call.AgentToolCallID); err != nil {
		t.Fatal(err)
	}
	_, err = store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID,
		ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact")})
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	_, err = store.CompleteAgentTurnCommit(ctx, turn.AgentTurnID, MessageExchange{})
	assertDomainCode(t, err, "SDK_TOOL_OUTCOME_UNRESOLVED")
}

func TestExternalWritePreexistingStatefulResultCannotCommit(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "outcome.db"))
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, claim := statefulToolClaimForTest(t, store)
	call := statefulApprovalForTest(t, store, project, claim, "uncertain-write")
	payload := validSourceAnalysisProviderResponse(t, claim)
	hash := sha256Hex(payload)
	if _, err := store.db.Exec(`UPDATE execution_attempts SET status='result_received',response_payload_json=?,response_hash=? WHERE attempt_id=?`, string(payload), hash, claim.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_tool_calls SET status='failed',started_at=? WHERE agent_tool_call_id=?`, formatTime(store.now()), call.AgentToolCallID); err != nil {
		t.Fatal(err)
	}
	_, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, ExpectedResponseHash: hash})
	assertDomainCode(t, err, "SDK_TOOL_OUTCOME_UNRESOLVED")
	var status string
	if err := store.db.QueryRow(`SELECT status FROM execution_attempts WHERE attempt_id=?`, claim.Attempt.AttemptID).Scan(&status); err != nil || status != "result_received" {
		t.Fatal("blocked result mutated", status, err)
	}
}
