package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func cloneQueuedExecutionForTest(t *testing.T, store *Store, source *TaskClaim, index int, mismatch string) *TaskClaim {
	t.Helper()
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var claim TaskClaim
	if err := json.Unmarshal(encoded, &claim); err != nil {
		t.Fatal(err)
	}
	claim.Task.TaskItemID = fmt.Sprintf("queued-task-%03d", index)
	claim.Task.ItemKey = claim.Task.TaskItemID
	claim.Attempt.AttemptID = fmt.Sprintf("queued-attempt-%03d", index)
	claim.Attempt.TaskItemID = claim.Task.TaskItemID
	claim.ContextPack.ContextPackID = fmt.Sprintf("queued-context-%03d", index)
	claim.ContextPack.Step.TaskItemID = claim.Task.TaskItemID
	switch mismatch {
	case "provider":
		claim.Attempt.ProviderID = "other-provider"
	case "executor":
		claim.Attempt.ExecutorID, claim.ExecutorID = "other-executor", "other-executor"
	case "model":
		model := "other-model"
		claim.ContextPack.Budget.ModelID = &model
	}
	hash, err := calculateStepExecutionContextHash(claim.ContextPack)
	if err != nil {
		t.Fatal(err)
	}
	claim.ContextPack.ContextHash, claim.Attempt.InputSnapshotHash = hash, hash
	encodedPack, err := json.Marshal(claim.ContextPack)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO task_items(task_item_id, step_run_id, run_id, item_key, item_order, status,
		attempt_count, input_snapshot_json, cursor_json, current_attempt_id, started_at, created_at, updated_at)
		SELECT ?, step_run_id, run_id, ?, item_order, 'running', attempt_count, input_snapshot_json, cursor_json, ?, started_at, created_at, updated_at
		FROM task_items WHERE task_item_id = ?`, claim.Task.TaskItemID, claim.Task.ItemKey, claim.Attempt.AttemptID, source.Task.TaskItemID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO execution_attempts(attempt_id, run_id, step_run_id, task_item_id, attempt_no,
		executor_id, provider_id, worker_id, request_fingerprint, input_snapshot_hash, status, token_hash, lease_until, usage_json, started_at)
		SELECT ?, run_id, step_run_id, ?, attempt_no, ?, ?, worker_id, request_fingerprint, ?, 'running', token_hash, lease_until, usage_json, started_at
		FROM execution_attempts WHERE attempt_id = ?`, claim.Attempt.AttemptID, claim.Task.TaskItemID, claim.Attempt.ExecutorID,
		claim.Attempt.ProviderID, hash, source.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO context_packs(context_pack_id, attempt_id, project_id, run_id, step_run_id,
		task_item_id, pack_type, pack_version, context_hash, payload_json, created_at)
		SELECT ?, ?, project_id, run_id, step_run_id, ?, pack_type, pack_version, ?, ?, created_at
		FROM context_packs WHERE attempt_id = ?`, claim.ContextPack.ContextPackID, claim.Attempt.AttemptID, claim.Task.TaskItemID,
		hash, string(encodedPack), source.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return &claim
}

func TestStatefulResumeQueueDoesNotStarveBehindOtherWorkers(t *testing.T) {
	for _, mismatch := range []string{"provider", "executor", "model"} {
		t.Run(mismatch, func(t *testing.T) {
			ctx := context.Background()
			store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "queue.db"))
			defer store.Close()
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			project, target := statefulToolClaimForTest(t, store)
			oldest := formatTime(store.now().Add(-time.Hour))
			for i := 0; i < 101; i++ {
				claim := cloneQueuedExecutionForTest(t, store, target, i, mismatch)
				call := statefulApprovalForTest(t, store, project, claim, fmt.Sprintf("queued-write-%03d", i))
				if _, err := store.PauseExecutionForApproval(ctx, statefulPauseForTest(claim, call.SDKToolCallID)); err != nil {
					t.Fatal(err)
				}
				resolveBackgroundApprovalForTest(t, store, call, "approve")
				if _, err := store.db.Exec(`UPDATE execution_run_states SET updated_at = ? WHERE attempt_id = ?`, oldest, claim.Attempt.AttemptID); err != nil {
					t.Fatal(err)
				}
			}
			call := statefulApprovalForTest(t, store, project, target, "target-write")
			if _, err := store.PauseExecutionForApproval(ctx, statefulPauseForTest(target, call.SDKToolCallID)); err != nil {
				t.Fatal(err)
			}
			resolveBackgroundApprovalForTest(t, store, call, "approve")
			resumed, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(target))
			if err != nil || resumed == nil || resumed.Attempt.AttemptID != target.Attempt.AttemptID || resumed.Resume == nil {
				t.Fatalf("healthy task starved behind 101 incompatible tasks: %+v %v", resumed, err)
			}
			if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(target)); err != nil || next != nil {
				t.Fatalf("exhausted matching queue: %+v %v", next, err)
			}
			var waiting int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_attempts WHERE status = 'waiting_approval'`).Scan(&waiting); err != nil || waiting != 101 {
				t.Fatalf("incompatible tasks changed: %d %v", waiting, err)
			}
		})
	}
}

func TestStatefulResumeQueueRechecksRunAfterCorruptSiblingFailsIt(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "queue.db"))
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, target := statefulToolClaimForTest(t, store)
	corrupt := cloneQueuedExecutionForTest(t, store, target, 0, "")
	for _, claim := range []*TaskClaim{corrupt, target} {
		call := statefulApprovalForTest(t, store, project, claim, claim.Attempt.AttemptID)
		if _, err := store.PauseExecutionForApproval(ctx, statefulPauseForTest(claim, call.SDKToolCallID)); err != nil {
			t.Fatal(err)
		}
		resolveBackgroundApprovalForTest(t, store, call, "approve")
	}
	if _, err := store.db.Exec(`UPDATE execution_run_states SET state_json = '{}', updated_at = ? WHERE attempt_id = ?`,
		formatTime(store.now().Add(-time.Hour)), corrupt.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(target)); err != nil || next != nil {
		t.Fatalf("previous candidate failed the Run but its sibling resumed: %+v %v", next, err)
	}
	snapshot, err := store.GetRunSnapshot(ctx, target.Attempt.RunID)
	if err != nil || snapshot.Run.Status != "failed" {
		t.Fatalf("corrupt Run did not settle: %+v %v", snapshot.Run, err)
	}
}

func TestFirstExecutionClaimScansPastUnsupportedQueueWindow(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "first-queue.db"))
	defer store.Close()
	_, source := statefulToolClaimForTest(t, store)
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO step_runs(step_run_id,run_id,step_id,status,attempt_count,started_at,approval_policy,input_version_snapshot_json,task_cursor_json)
		SELECT 'unsupported-step',run_id,'unavailable-executor-step','running',1,started_at,approval_policy,input_version_snapshot_json,task_cursor_json FROM step_runs WHERE step_run_id=?`, source.Task.StepRunID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 102; i++ {
		stepID, id := "unsupported-step", fmt.Sprintf("pending-%03d", i)
		if i == 101 {
			stepID = source.Task.StepRunID
		}
		if _, err := tx.Exec(`INSERT INTO task_items(task_item_id,step_run_id,run_id,item_key,item_order,status,attempt_count,input_snapshot_json,cursor_json,created_at,updated_at)
			SELECT ?,?,run_id,?,?,'pending',0,input_snapshot_json,cursor_json,created_at,updated_at FROM task_items WHERE task_item_id=?`, id, stepID, id, i+100, source.Task.TaskItemID); err != nil {
			t.Fatal(err)
		}
	}
	// The source is an earlier item in a sequential step. Complete it so the
	// target is eligible independently of the unsupported queue entries.
	if _, err := tx.Exec(`UPDATE task_items SET status='succeeded' WHERE task_item_id=?`, source.Task.TaskItemID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE execution_attempts SET status='succeeded' WHERE attempt_id=?`, source.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(source))
	if err != nil || claim == nil || claim.Task.TaskItemID != "pending-101" || claim.Attempt.AttemptNo != 1 {
		t.Fatalf("healthy first claim starved: %+v %v", claim, err)
	}
	if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(source)); err != nil || next != nil {
		t.Fatalf("claimed unsupported task: %+v %v", next, err)
	}
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_items WHERE step_run_id='unsupported-step' AND status='pending' AND current_attempt_id IS NULL`).Scan(&remaining); err != nil || remaining != 101 {
		t.Fatalf("incompatible tasks changed: %d %v", remaining, err)
	}
}
