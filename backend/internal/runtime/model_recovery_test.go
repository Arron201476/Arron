package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func recoveryCheckpointForTest() PauseAgentTurnForApprovalCommand {
	return PauseAgentTurnForApprovalCommand{SchemaVersion: "1.16", RecoveryReason: modelRecoveryReason,
		RunState: json.RawMessage(`{"$schemaVersion":"1.16","current_turn":2,"max_turns":8,"private_marker":9007199254740993}`)}
}

func TestModelRecoveryCheckpointRejectsUnknownCauseAndExhaustedBudget(t *testing.T) {
	for _, state := range []string{`{}`, `{"current_turn":0,"max_turns":8}`, `{"current_turn":8,"max_turns":8}`, `{"current_turn":9,"max_turns":8}`, `{"current_turn":true,"max_turns":8}`, `{"current_turn":1.5,"max_turns":8}`} {
		command := recoveryCheckpointForTest()
		command.RunState = json.RawMessage(state)
		_, err := validateModelRecoveryCheckpoint(command)
		assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	}
	command := recoveryCheckpointForTest()
	command.RecoveryReason = "tool_failed"
	_, err := validateModelRecoveryCheckpoint(command)
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	command = recoveryCheckpointForTest()
	command.PendingSDKToolCallIDs = []string{"unapproved"}
	_, err = validateModelRecoveryCheckpoint(command)
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
}

func TestModelRecoveryCheckpointRequiresExplicitResumeAcrossModes(t *testing.T) {
	for _, mode := range []string{"conversation", "background_task", "stateful_workflow"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			var store *Store
			var call AgentToolCall
			var save func() error
			var resume func()
			command := recoveryCheckpointForTest()
			var table string
			switch mode {
			case "conversation":
				var project Project
				var original AgentTurn
				var path string
				store, project, original, path = pauseTurnFixture(t)
				claimed := claimInputTurn(t, store, original.AgentTurnID)
				command.AgentTurnDispatchGeneration = claimed.DispatchGeneration
				var err error
				call, err = store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
					AgentTurnID: original.AgentTurnID, SDKToolCallID: "write", ToolID: "mcp:fixture/save_fact", Arguments: json.RawMessage(`{"value":"safe"}`),
					ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact")})
				if err != nil {
					t.Fatal(err)
				}
				table = "agent_turn_run_states"
				save = func() error { _, err := store.CompleteAgentTurnPause(ctx, original.AgentTurnID, command); return err }
				resume = func() {
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
					store, err = Open(path, loadTestRegistry(t))
					if err != nil {
						t.Fatal(err)
					}
					if turns, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil || len(turns) != 0 {
						t.Fatal("automatically resumed recovery", err)
					}
					paused, err := store.GetAgentTurn(ctx, original.AgentTurnID)
					if err != nil || paused.Status != "paused" || paused.ErrorCode == nil || *paused.ErrorCode != modelRecoveryCode {
						t.Fatal("missing recovery state", err)
					}
					if _, err := store.ResumeAgentTurn(ctx, ControlAgentTurnCommand{AgentTurnID: original.AgentTurnID}); err != nil {
						t.Fatal(err)
					}
					next := claimInputTurn(t, store, original.AgentTurnID)
					if next.DispatchGeneration != claimed.DispatchGeneration+1 || !next.StartedAt.Equal(*claimed.StartedAt) || next.ErrorCode != nil {
						t.Fatal("execution identity changed or stale failure")
					}
					state, err := store.GetAgentTurnResumeContext(ctx, original.AgentTurnID)
					if err != nil || string(state.RunState) != string(command.RunState) {
						t.Fatal("native state changed", err)
					}
					assertDomainCode(t, save(), "AGENT_TURN_STATE_CONFLICT")
				}
			case "background_task":
				var task AgentTask
				var claimCommand ClaimAgentTaskCommand
				store, task, claimCommand = pausedBackgroundStore(t)
				claim, err := store.ClaimAgentTask(ctx, claimCommand)
				if err != nil || claim == nil {
					t.Fatal(err)
				}
				call = backgroundApprovalForTest(t, store, *claim, "write")
				table = "agent_task_run_states"
				pause := pauseCheckpoint(claim)
				pause.PauseAgentTurnForApprovalCommand = command
				save = func() error { _, err := store.CompleteAgentTaskPause(ctx, pause); return err }
				resume = func() {
					path := filepath.Join(store.dataRoot, "pause.db")
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
					store, err = Open(path, loadBackgroundTaskRegistry(t))
					if err != nil {
						t.Fatal(err)
					}
					if next, err := store.ClaimAgentTask(ctx, claimCommand); err != nil || next != nil {
						t.Fatal("automatically resumed recovery", err)
					}
					paused, err := store.GetAgentTask(ctx, task.AgentTaskID)
					if err != nil || paused.Status != "paused" || paused.FailureCode == nil || *paused.FailureCode != modelRecoveryCode {
						t.Fatal("missing recovery state", err)
					}
					if _, err := store.ResumeAgentTask(ctx, ResumeAgentTaskCommand{AgentTaskID: task.AgentTaskID}); err != nil {
						t.Fatal(err)
					}
					next, err := store.ClaimAgentTask(ctx, claimCommand)
					if err != nil || next == nil || next.Resume == nil || next.Attempt.AgentTaskAttemptID != claim.Attempt.AgentTaskAttemptID || next.AttemptToken == claim.AttemptToken || next.Task.AttemptCount != 1 || next.Task.FailureCode != nil || string(next.Resume.RunState) != string(command.RunState) {
						t.Fatal("recovery changed attempt or state", err)
					}
					assertDomainCode(t, save(), "ATTEMPT_TOKEN_INVALID")
				}
			case "stateful_workflow":
				path := filepath.Join(t.TempDir(), "recovery.db")
				store = openProjectFilesTestStore(t, path)
				store.SetAgentToolRegistry(agentToolRegistryForTest(t))
				project, claim := statefulToolClaimForTest(t, store)
				call = statefulApprovalForTest(t, store, project, claim, "write")
				table = "execution_run_states"
				pause := statefulPauseForTest(claim)
				pause.UserPause, pause.PauseAgentTurnForApprovalCommand = true, command
				save = func() error { _, err := store.PauseExecutionForApproval(ctx, pause); return err }
				resume = func() {
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
					store = openProjectFilesTestStore(t, path)
					store.SetAgentToolRegistry(agentToolRegistryForTest(t))
					if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim)); err != nil || next != nil {
						t.Fatal("automatically resumed recovery", err)
					}
					snapshot, err := store.GetRunSnapshot(ctx, claim.Attempt.RunID)
					if err != nil || snapshot.Run.Status != "paused" {
						t.Fatal("run was not paused", err)
					}
					if _, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: claim.Attempt.RunID}); err != nil {
						t.Fatal(err)
					}
					next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
					if err != nil || next == nil || next.Resume == nil || next.Attempt.AttemptID != claim.Attempt.AttemptID || next.AttemptToken == claim.AttemptToken || next.Attempt.AttemptNo != 1 || next.Attempt.ErrorCode != nil || string(next.Resume.RunState) != string(command.RunState) {
						t.Fatal("recovery changed attempt or state", err)
					}
					assertDomainCode(t, save(), "ATTEMPT_TOKEN_INVALID")
				}
			}
			defer func() { store.Close() }()
			for _, status := range []string{"pending_approval", "approved", "running", "failed", "cancelled"} {
				if _, err := store.db.Exec(`UPDATE agent_tool_calls SET status=?,started_at=? WHERE agent_tool_call_id=?`, status, formatTime(store.now()), call.AgentToolCallID); err != nil {
					t.Fatal(err)
				}
				assertDomainCode(t, save(), "SDK_TOOL_REPLAY_RISK")
				var count int
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatal("unsafe recovery partially committed", err)
				}
			}
			if _, err := store.db.Exec(`UPDATE agent_tool_calls SET status='completed' WHERE agent_tool_call_id=?`, call.AgentToolCallID); err != nil {
				t.Fatal(err)
			}
			if err := save(); err != nil {
				t.Fatal("completed write blocked recovery", err)
			}
			if err := save(); err != nil {
				t.Fatal("lost acknowledgement changed checkpoint", err)
			}
			resume()
		})
	}
}
