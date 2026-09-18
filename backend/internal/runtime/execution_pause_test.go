package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestStatefulNativePauseRebuildPreservesAttemptAndCheckpoint(t *testing.T) {
	for _, decision := range []string{"none", "approve", "reject"} {
		t.Run(decision, func(t *testing.T) {
			ctx := context.Background()
			database := filepath.Join(t.TempDir(), "native-pause.db")
			store := openProjectFilesTestStore(t, database)
			defer func() { store.Close() }()
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			project, claim := statefulToolClaimForTest(t, store)
			original, _ := json.Marshal(claim.ContextPack)
			pause := statefulPauseForTest(claim)
			pause.UserPause = true
			var call AgentToolCall
			if decision != "none" {
				call = statefulApprovalForTest(t, store, project, claim, "install")
				pause.PendingSDKToolCallIDs = []string{call.SDKToolCallID}
			}
			if snapshot, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: claim.Attempt.RunID}); err != nil || snapshot.Run.Status != "pausing" {
				t.Fatalf("pause request: %+v %v", snapshot, err)
			}
			heartbeat := HeartbeatExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash, LeaseSeconds: 60}
			if receipt, err := store.HeartbeatExecutionAttempt(ctx, heartbeat); err != nil || !receipt.PauseRequested || receipt.Status != "running" {
				t.Fatalf("pause signal: %+v %v", receipt, err)
			}
			if receipt, err := store.PauseExecutionForApproval(ctx, pause); err != nil || receipt.Status != "paused" {
				t.Fatalf("checkpoint: %+v %v", receipt, err)
			}
			if _, err := store.PauseExecutionForApproval(ctx, pause); err != nil {
				t.Fatal(err)
			}
			if receipt, err := store.HeartbeatExecutionAttempt(ctx, heartbeat); err != nil || receipt.Status != "paused" {
				t.Fatalf("durable acknowledgement: %+v %v", receipt, err)
			}
			snapshot, err := store.GetRunSnapshot(ctx, claim.Attempt.RunID)
			if err != nil || snapshot.Run.Status != "paused" {
				t.Fatalf("Run did not reach boundary: %+v %v", snapshot, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store = openProjectFilesTestStore(t, database)
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			store.now = func() time.Time { return claim.Attempt.LeaseUntil.Add(time.Hour) }
			if decision != "none" {
				resolveBackgroundApprovalForTest(t, store, call, decision)
				_, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: toolConfigurationHashForTest(t, store, call.ToolID)})
				if err == nil {
					t.Fatal("paused approval started its tool")
				}
			}
			if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim)); err != nil || next != nil {
				t.Fatalf("paused task was claimed: %+v %v", next, err)
			}
			if _, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: claim.Attempt.RunID}); err != nil {
				t.Fatal(err)
			}
			next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
			if err != nil || next == nil || next.Resume == nil {
				t.Fatalf("resume: %+v %v", next, err)
			}
			pack, _ := json.Marshal(next.ContextPack)
			if string(original) != string(pack) || next.Attempt.AttemptID != claim.Attempt.AttemptID || next.Attempt.AttemptNo != claim.Attempt.AttemptNo || !next.Attempt.StartedAt.Equal(claim.Attempt.StartedAt) || next.AttemptToken == claim.AttemptToken {
				t.Fatal("resume changed the original execution or failed to rotate its token")
			}
			_, err = store.PauseExecutionForApproval(ctx, pause)
			assertDomainCode(t, err, "ATTEMPT_TOKEN_INVALID")
			if decision == "none" && len(next.Resume.ApprovalDecisions) != 0 {
				t.Fatal("pause invented an approval")
			}
		})
	}
}

func TestStatefulNativePauseRejectsUnsafeCheckpointAndCancellation(t *testing.T) {
	for _, mode := range []string{"missing-state", "corrupt-state", "running-tool", "cancel", "delete", "pause-withdrawn"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "pause.db"))
			defer store.Close()
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			project, claim := statefulToolClaimForTest(t, store)
			if _, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: claim.Attempt.RunID}); err != nil {
				t.Fatal(err)
			}
			pause := statefulPauseForTest(claim)
			pause.UserPause = true
			if mode == "running-tool" {
				if _, err := store.BeginAgentToolCall(ctx, statefulToolCommand(project, claim, "runtime:inspect_project", "read", json.RawMessage(`{}`))); err != nil {
					t.Fatal(err)
				}
				_, err := store.PauseExecutionForApproval(ctx, pause)
				assertDomainCode(t, err, "AGENT_RUN_STATE_APPROVAL_MISMATCH")
				return
			}
			if mode == "pause-withdrawn" {
				if _, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: claim.Attempt.RunID}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.PauseExecutionForApproval(ctx, pause); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing-state", "corrupt-state":
				query := `DELETE FROM execution_run_states`
				if mode == "corrupt-state" {
					query = `UPDATE execution_run_states SET state_json = '{}'`
				}
				if _, err := store.db.Exec(query); err != nil {
					t.Fatal(err)
				}
				if _, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: claim.Attempt.RunID}); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if _, err := store.CancelRun(ctx, CancelRunCommand{RunID: claim.Attempt.RunID, Confirmed: true}); err != nil {
					t.Fatal(err)
				}
			case "delete":
				preview, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.ConfirmProjectDelete(ctx, ConfirmProjectDeleteCommand{ProjectID: project.ProjectID, PreviewHash: preview.SnapshotHash, Confirmed: true, ActorRef: "test"}); err != nil {
					t.Fatal(err)
				}
			}
			next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
			if err != nil || (next != nil) != (mode == "pause-withdrawn") {
				t.Fatalf("unsafe claim: %+v %v", next, err)
			}
			if mode == "cancel" || mode == "delete" {
				attempt, err := store.GetExecutionAttempt(ctx, claim.Attempt.AttemptID)
				if err != nil || attempt.Status != "cancelled" {
					t.Fatalf("paused attempt not cancelled: %+v %v", attempt, err)
				}
			}
		})
	}
}

func TestStatefulNativePauseWaitsForEveryActiveSibling(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "siblings.db"))
	defer store.Close()
	_, claim := statefulToolClaimForTest(t, store)
	sibling := *claim
	sibling.Attempt.AttemptID = "native-sibling-attempt"
	sibling.Attempt.TaskItemID = "native-sibling-task"
	if _, err := store.db.Exec(`INSERT INTO task_items(task_item_id, step_run_id, run_id, item_key, item_order, status, attempt_count, input_snapshot_json, cursor_json, current_attempt_id, created_at, updated_at)
		SELECT ?, step_run_id, run_id, 'sibling', item_order + 1, 'running', 1, input_snapshot_json, cursor_json, ?, created_at, updated_at FROM task_items WHERE task_item_id = ?`, sibling.Attempt.TaskItemID, sibling.Attempt.AttemptID, claim.Task.TaskItemID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO execution_attempts(attempt_id, run_id, step_run_id, task_item_id, attempt_no, executor_id, provider_id, worker_id, request_fingerprint, input_snapshot_hash, status, token_hash, lease_until, usage_json, started_at)
		SELECT ?, run_id, step_run_id, ?, 1, executor_id, provider_id, worker_id, request_fingerprint, input_snapshot_hash, 'running', token_hash, lease_until, usage_json, started_at FROM execution_attempts WHERE attempt_id = ?`, sibling.Attempt.AttemptID, sibling.Attempt.TaskItemID, claim.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: claim.Attempt.RunID}); err != nil {
		t.Fatal(err)
	}
	for index, current := range []*TaskClaim{claim, &sibling} {
		pause := statefulPauseForTest(current)
		pause.UserPause = true
		if _, err := store.PauseExecutionForApproval(ctx, pause); err != nil {
			t.Fatal(err)
		}
		run, err := store.GetRun(ctx, claim.Attempt.RunID)
		want := "pausing"
		if index == 1 {
			want = "paused"
		}
		if err != nil || run.Status != want {
			t.Fatalf("sibling pause %d: %+v %v", index, run, err)
		}
	}
	for _, current := range []*TaskClaim{claim, &sibling} {
		attempt, err := store.GetExecutionAttempt(ctx, current.Attempt.AttemptID)
		if err != nil || attempt.Status != "paused" {
			t.Fatalf("sibling discarded: %+v %v", attempt, err)
		}
	}
}

func TestStatefulNativePauseResumeWithUnresolvedApprovalKeepsDurableReceipt(t *testing.T) {
	for _, decision := range []string{"approve", "reject"} {
		t.Run(decision, func(t *testing.T) {
			ctx := context.Background()
			store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "unresolved.db"))
			defer store.Close()
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			project, claim := statefulToolClaimForTest(t, store)
			call := statefulApprovalForTest(t, store, project, claim, "pending")
			if _, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: claim.Attempt.RunID}); err != nil {
				t.Fatal(err)
			}
			pause := statefulPauseForTest(claim, call.SDKToolCallID)
			pause.UserPause = true
			if _, err := store.PauseExecutionForApproval(ctx, pause); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: claim.Attempt.RunID}); err != nil {
				t.Fatal(err)
			}
			attempt, err := store.GetExecutionAttempt(ctx, claim.Attempt.AttemptID)
			if err != nil || attempt.Status != "waiting_approval" {
				t.Fatalf("resumed pending task: %+v %v", attempt, err)
			}
			if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim)); err != nil || next != nil {
				t.Fatalf("approval bypassed: %+v %v", next, err)
			}
			if receipt, err := store.PauseExecutionForApproval(ctx, pause); err != nil || receipt.Status != "waiting_approval" {
				t.Fatalf("lost acknowledgement: %+v %v", receipt, err)
			}
			resolveBackgroundApprovalForTest(t, store, call, decision)
			next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
			if err != nil || next == nil || next.Attempt.AttemptID != claim.Attempt.AttemptID || len(next.Resume.ApprovalDecisions) != 1 || next.Resume.ApprovalDecisions[0].Action != decision {
				t.Fatalf("approval restore: %+v %v", next, err)
			}
		})
	}
}
