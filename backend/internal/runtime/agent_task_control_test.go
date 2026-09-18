package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"content-agent/backend/internal/identity"
)

func pausedBackgroundStore(t *testing.T) (*Store, AgentTask, ClaimAgentTaskCommand) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "pause.db"), loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	return store, createBackgroundTaskForTest(t, store), ClaimAgentTaskCommand{WorkerID: "worker", ProviderID: "sdk", ModelID: "fixture", LeaseSeconds: 60}
}

func pauseCheckpoint(claim *AgentTaskClaim, pending ...string) PauseAgentTaskForApprovalCommand {
	return PauseAgentTaskForApprovalCommand{AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken,
		PauseAgentTurnForApprovalCommand: PauseAgentTurnForApprovalCommand{SchemaVersion: "1.16", RunState: json.RawMessage(`{"$schemaVersion":"1.16","private_marker":"paused-state"}`), PendingSDKToolCallIDs: pending}}
}

func TestBackgroundTaskUserPauseQueuedAndIdempotentResume(t *testing.T) {
	store, task, claimCommand := pausedBackgroundStore(t)
	ctx := context.Background()
	paused, err := store.RequestAgentTaskPause(ctx, PauseAgentTaskCommand{AgentTaskID: task.AgentTaskID})
	if err != nil || paused.Status != "paused" || paused.AttemptCount != 0 {
		t.Fatalf("pause: %+v %v", paused, err)
	}
	if claim, err := store.ClaimAgentTask(ctx, claimCommand); err != nil || claim != nil {
		t.Fatalf("claimed paused task: %+v %v", claim, err)
	}
	resume := ResumeAgentTaskCommand{CommandMeta: CommandMeta{Scope: task.ProjectID, CommandType: "resume_agent_task", IdempotencyKey: "resume", RequestHash: "same"}, AgentTaskID: task.AgentTaskID}
	queued, err := store.ResumeAgentTask(ctx, resume)
	if err != nil || queued.Status != "queued" || queued.AttemptCount != 0 {
		t.Fatalf("resume: %+v %v", queued, err)
	}
	claim, err := store.ClaimAgentTask(ctx, claimCommand)
	if err != nil || claim == nil || claim.Resume != nil || claim.Task.AttemptCount != 1 {
		t.Fatalf("first claim: %+v %v", claim, err)
	}
	replayed, err := store.ResumeAgentTask(ctx, resume)
	if err != nil || replayed.Status != "queued" {
		t.Fatalf("resume receipt: %+v %v", replayed, err)
	}
	_, err = store.CompleteAgentTaskPause(ctx, pauseCheckpoint(claim))
	assertDomainCode(t, err, "AGENT_TASK_STATE_CONFLICT")
}

func TestBackgroundTaskUserPauseNativeCheckpointAndTokenRotation(t *testing.T) {
	store, task, claimCommand := pausedBackgroundStore(t)
	ctx := context.Background()
	claim, err := store.ClaimAgentTask(ctx, claimCommand)
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	call := backgroundApprovalForTest(t, store, *claim, "write-once")
	resolveBackgroundApprovalForTest(t, store, call, "approve")
	if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: toolConfigurationHashForTest(t, store, call.ToolID)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: json.RawMessage(`{"saved":true}`)}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		paused, err := store.RequestAgentTaskPause(ctx, PauseAgentTaskCommand{AgentTaskID: task.AgentTaskID})
		if err != nil || paused.Status != "pausing" {
			t.Fatalf("pause request: %+v %v", paused, err)
		}
		_, err = store.ResumeAgentTask(ctx, ResumeAgentTaskCommand{AgentTaskID: task.AgentTaskID})
		assertDomainCode(t, err, "AGENT_TASK_STATE_CONFLICT")
		progress, err := store.UpdateAgentTaskProgress(ctx, UpdateAgentTaskProgressCommand{AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken, Current: 1, Total: 3, LeaseSeconds: 60})
		if err != nil || progress.Status != "pausing" {
			t.Fatalf("heartbeat: %+v %v", progress, err)
		}
		command := pauseCheckpoint(claim)
		paused, err = store.CompleteAgentTaskPause(ctx, command)
		if err != nil || paused.Status != "paused" || paused.AttemptCount != 1 {
			t.Fatalf("checkpoint: %+v %v", paused, err)
		}
		if _, err := store.CompleteAgentTaskPause(ctx, command); err != nil {
			t.Fatalf("lost checkpoint receipt: %v", err)
		}
		if index == 0 {
			path := filepath.Join(store.dataRoot, "pause.db")
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(path, loadBackgroundTaskRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
		}
		store.now = func() time.Time { return claim.Attempt.LeaseUntil.Add(time.Hour) }
		if next, err := store.ClaimAgentTask(ctx, claimCommand); err != nil || next != nil {
			t.Fatalf("paused attempt expired/replayed: %+v %v", next, err)
		}
		oldToken, attemptID := claim.AttemptToken, claim.Attempt.AgentTaskAttemptID
		if _, err := store.ResumeAgentTask(ctx, ResumeAgentTaskCommand{AgentTaskID: task.AgentTaskID}); err != nil {
			t.Fatal(err)
		}
		claim, err = store.ClaimAgentTask(ctx, claimCommand)
		if err != nil || claim == nil || claim.Resume == nil || len(claim.Resume.ApprovalDecisions) != 0 || claim.AttemptToken == oldToken || claim.Attempt.AgentTaskAttemptID != attemptID || claim.Task.AttemptCount != 1 {
			t.Fatalf("resume retained attempt: %+v %v", claim, err)
		}
		_, err = store.CompleteAgentTaskPause(ctx, command)
		assertDomainCode(t, err, "ATTEMPT_TOKEN_INVALID")
		store.now = time.Now
	}
	var writes, leaks int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_tool_calls WHERE tool_id=? AND started_at IS NOT NULL`, call.ToolID).Scan(&writes); err != nil || writes != 1 {
		t.Fatalf("write ledger replayed: %d %v", writes, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE payload_json LIKE '%private_marker%'`).Scan(&leaks); err != nil || leaks != 0 {
		t.Fatalf("private checkpoint leaked: %d %v", leaks, err)
	}
}

func TestBackgroundTaskUserPauseApprovalNeverAutoResumes(t *testing.T) {
	for _, decision := range []string{"approve", "reject"} {
		for _, ordering := range []string{"pause-before-checkpoint", "pause-after-checkpoint", "resume-before-approval"} {
			t.Run(decision+"/"+ordering, func(t *testing.T) {
				store, task, claimCommand := pausedBackgroundStore(t)
				ctx := context.Background()
				claim, err := store.ClaimAgentTask(ctx, claimCommand)
				if err != nil || claim == nil {
					t.Fatalf("claim: %+v %v", claim, err)
				}
				call := backgroundApprovalForTest(t, store, *claim, "pending")
				pause := func() {
					if _, err := store.RequestAgentTaskPause(ctx, PauseAgentTaskCommand{AgentTaskID: task.AgentTaskID}); err != nil {
						t.Fatal(err)
					}
				}
				if ordering == "pause-before-checkpoint" {
					pause()
				}
				if _, err := store.PauseAgentTaskForApproval(ctx, pauseCheckpoint(claim, "pending")); err != nil {
					t.Fatal(err)
				}
				if ordering != "pause-before-checkpoint" {
					pause()
				}
				if ordering == "resume-before-approval" {
					waiting, err := store.ResumeAgentTask(ctx, ResumeAgentTaskCommand{AgentTaskID: task.AgentTaskID})
					if err != nil || waiting.Status != "waiting_approval" {
						t.Fatalf("pending approval discarded: %+v %v", waiting, err)
					}
				}
				resolveBackgroundApprovalForTest(t, store, call, decision)
				if ordering != "resume-before-approval" {
					if decision == "approve" {
						_, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: toolConfigurationHashForTest(t, store, call.ToolID)})
						assertDomainCode(t, err, "AGENT_TASK_ATTEMPT_STALE")
					}
					if next, err := store.ClaimAgentTask(ctx, claimCommand); err != nil || next != nil {
						t.Fatalf("approval resumed paused task: %+v %v", next, err)
					}
					if _, err := store.ResumeAgentTask(ctx, ResumeAgentTaskCommand{AgentTaskID: task.AgentTaskID}); err != nil {
						t.Fatal(err)
					}
				}
				resumed, err := store.ClaimAgentTask(ctx, claimCommand)
				if err != nil || resumed == nil || resumed.Resume == nil || len(resumed.Resume.ApprovalDecisions) != 1 || resumed.Resume.ApprovalDecisions[0].Action != decision {
					t.Fatalf("approval decision lost: %+v %v", resumed, err)
				}
			})
		}
	}
}

func TestBackgroundTaskUserPauseCancelCompletionAndFailureRaces(t *testing.T) {
	for _, action := range []string{"cancel-pausing", "cancel-paused", "complete", "fail", "lease", "missing-state", "corrupt-state"} {
		t.Run(action, func(t *testing.T) {
			store, task, claimCommand := pausedBackgroundStore(t)
			ctx := context.Background()
			claim, err := store.ClaimAgentTask(ctx, claimCommand)
			if err != nil || claim == nil {
				t.Fatalf("claim: %+v %v", claim, err)
			}
			if _, err := store.RequestAgentTaskPause(ctx, PauseAgentTaskCommand{AgentTaskID: task.AgentTaskID}); err != nil {
				t.Fatal(err)
			}
			command := pauseCheckpoint(claim)
			if action == "cancel-paused" || action == "missing-state" || action == "corrupt-state" {
				if _, err := store.CompleteAgentTaskPause(ctx, command); err != nil {
					t.Fatal(err)
				}
			}
			switch action {
			case "cancel-pausing", "cancel-paused":
				if _, err := store.CancelAgentTask(ctx, CancelAgentTaskCommand{AgentTaskID: task.AgentTaskID}); err != nil {
					t.Fatal(err)
				}
				_, err := store.CompleteAgentTaskPause(ctx, command)
				assertDomainCode(t, err, "AGENT_TASK_CANCELLED")
			case "complete":
				completed, err := store.CompleteAgentTask(ctx, CompleteAgentTaskCommand{AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken, Result: json.RawMessage(`{"summary":"done"}`)})
				if err != nil || completed.Status != "completed" {
					t.Fatalf("completion lost: %+v %v", completed, err)
				}
			case "fail":
				failed, err := store.FailAgentTask(ctx, FailAgentTaskCommand{AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken, ErrorCode: "PROVIDER_TEMPORARY_FAILURE", ErrorMessage: "failed", Retryable: true})
				if err != nil || failed.Status != "failed" {
					t.Fatalf("failure auto restarted: %+v %v", failed, err)
				}
			case "lease":
				store.now = func() time.Time { return claim.Attempt.LeaseUntil.Add(time.Hour) }
				if next, err := store.ClaimAgentTask(ctx, claimCommand); err != nil || next != nil {
					t.Fatalf("lost pause auto restarted: %+v %v", next, err)
				}
				failed, _ := store.GetAgentTask(ctx, task.AgentTaskID)
				if failed.Status != "failed" || failed.FailureCode == nil || *failed.FailureCode != "AGENT_TASK_PAUSE_INTERRUPTED" {
					t.Fatalf("lost checkpoint hidden: %+v", failed)
				}
			case "missing-state", "corrupt-state":
				query := `DELETE FROM agent_task_run_states WHERE agent_task_id=?`
				if action == "corrupt-state" {
					query = `UPDATE agent_task_run_states SET state_json='{}' WHERE agent_task_id=?`
				}
				if _, err := store.db.Exec(query, task.AgentTaskID); err != nil {
					t.Fatal(err)
				}
				_, err := store.ResumeAgentTask(ctx, ResumeAgentTaskCommand{AgentTaskID: task.AgentTaskID})
				assertDomainCode(t, err, "AGENT_RUN_STATE_CORRUPT")
			}
		})
	}
}

func TestBackgroundTaskUserPauseDeletionInvalidatesCheckpointAndTools(t *testing.T) {
	for _, target := range []string{"project", "workspace"} {
		for _, checkpointed := range []bool{false, true} {
			t.Run(target+"/"+map[bool]string{false: "pausing", true: "paused"}[checkpointed], func(t *testing.T) {
				store, task, claimCommand := pausedBackgroundStore(t)
				ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
				claim, err := store.ClaimAgentTask(ctx, claimCommand)
				if err != nil || claim == nil {
					t.Fatalf("claim: %+v %v", claim, err)
				}
				call := backgroundApprovalForTest(t, store, *claim, "pending-write")
				if _, err := store.RequestAgentTaskPause(ctx, PauseAgentTaskCommand{AgentTaskID: task.AgentTaskID}); err != nil {
					t.Fatal(err)
				}
				checkpoint := pauseCheckpoint(claim, call.SDKToolCallID)
				if checkpointed {
					if _, err := store.CompleteAgentTaskPause(ctx, checkpoint); err != nil {
						t.Fatal(err)
					}
				}
				if target == "project" {
					preview, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: task.ProjectID})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := store.ConfirmProjectDelete(ctx, ConfirmProjectDeleteCommand{ProjectID: task.ProjectID, PreviewHash: preview.SnapshotHash, Confirmed: true}); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := store.DeleteWorkspace(ctx, DeleteWorkspaceCommand{WorkspaceID: identity.DefaultWorkspaceID, UserID: identity.DefaultUserID, Confirmation: identity.DefaultWorkspaceID}); err != nil {
						t.Fatal(err)
					}
				}
				var taskStatus, attemptStatus, toolStatus, approvalStatus string
				if err := store.db.QueryRow(`SELECT task.status, attempt.status, tool.status, approval.status
					FROM agent_tasks task JOIN agent_task_attempts attempt ON attempt.agent_task_id=task.agent_task_id
					JOIN agent_task_tool_calls binding ON binding.agent_task_attempt_id=attempt.agent_task_attempt_id
					JOIN agent_tool_calls tool ON tool.agent_tool_call_id=binding.agent_tool_call_id
					JOIN agent_tool_approvals approval ON approval.agent_tool_call_id=tool.agent_tool_call_id
					WHERE task.agent_task_id=?`, task.AgentTaskID).Scan(&taskStatus, &attemptStatus, &toolStatus, &approvalStatus); err != nil {
					t.Fatal(err)
				}
				if taskStatus != "cancelled" || attemptStatus != "cancelled" || toolStatus != "cancelled" || approvalStatus != "cancelled" {
					t.Fatalf("deletion left live execution: %s/%s/%s/%s", taskStatus, attemptStatus, toolStatus, approvalStatus)
				}
				var count int
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_task_run_states WHERE agent_task_id=?`, task.AgentTaskID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("checkpoint retained: %d %v", count, err)
				}
				if _, err := store.ResumeAgentTask(ctx, ResumeAgentTaskCommand{AgentTaskID: task.AgentTaskID}); err == nil {
					t.Fatal("deleted task resumed")
				}
				if _, err := store.CompleteAgentTaskPause(ctx, checkpoint); err == nil {
					t.Fatal("deleted task restored checkpoint")
				}
			})
		}
	}
}
