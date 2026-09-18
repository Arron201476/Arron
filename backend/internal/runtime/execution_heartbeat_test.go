package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestExecutionHeartbeatRenewsWithoutChangingIdentityAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "heartbeat.db")
	store := openProjectFilesTestStore(t, database)
	defer func() { store.Close() }()
	_, claim := statefulToolClaimForTest(t, store)
	command := HeartbeatExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash, LeaseSeconds: 90}
	now := claim.Attempt.LeaseUntil.Add(-10 * time.Second)
	store.now = func() time.Time { return now }
	first, err := store.HeartbeatExecutionAttempt(ctx, command)
	if err != nil || !first.LeaseUntil.Equal(now.Add(90*time.Second)) || first.WorkerID != claim.Attempt.WorkerID ||
		first.InputSnapshotHash != claim.Attempt.InputSnapshotHash || first.AttemptNo != claim.Attempt.AttemptNo {
		t.Fatalf("renewal changed identity or lease: %+v %v", first, err)
	}
	command.LeaseSeconds = 30
	second, err := store.HeartbeatExecutionAttempt(ctx, command)
	if err != nil || !second.LeaseUntil.Equal(first.LeaseUntil) {
		t.Fatalf("short renewal reduced lease: %+v %v", second, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	store.now = func() time.Time { return now.Add(31 * time.Second) }
	third, err := store.HeartbeatExecutionAttempt(ctx, command)
	if err != nil || !third.LeaseUntil.Equal(first.LeaseUntil) {
		t.Fatalf("renewal lost after database reopen: %+v %v", third, err)
	}
	other, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{WorkerID: "other", ProviderID: "fixture",
		ExecutorIDs: []string{"worker.structured_content"}, LeaseSeconds: 60})
	if err != nil || other != nil {
		t.Fatalf("renewed task reclaimed by another worker: %+v %v", other, err)
	}
}

func TestExecutionProgramProgressPersistsAndDoesNotPolluteGenericHeartbeats(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "program-progress.db"))
	defer store.Close()
	_, claim := statefulToolClaimForTest(t, store)
	command := HeartbeatExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash, LeaseSeconds: 60, ProgramStatus: "running"}
	ctx := context.Background()
	for _, phase := range []string{"running", "", "completed", "", "incomplete"} {
		command.ProgramStatus = phase
		receipt, err := store.HeartbeatExecutionAttempt(ctx, command)
		if err != nil || (phase != "" && receipt.ProgramStatus != phase) || receipt.Status != "running" {
			t.Fatalf("program progress changed task lifecycle: %+v %v", receipt, err)
		}
	}
	stored, err := store.GetExecutionAttempt(ctx, command.AttemptID)
	if err != nil || stored.ProgramStatus != "incomplete" {
		t.Fatalf("missing stored progress: %+v %v", stored, err)
	}
	task, err := scanTaskItem(store.db.QueryRowContext(ctx, taskItemSelect+` WHERE task_item_id=?`, claim.Attempt.TaskItemID))
	if err != nil || task.ProgramStatus != "incomplete" {
		t.Fatalf("task projection missing: %+v %v", task, err)
	}
	command.ProgramStatus = "raw-provider-text"
	_, err = store.HeartbeatExecutionAttempt(ctx, command)
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
}

func TestExecutionHeartbeatRejectsInvalidOwnershipAndLease(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "heartbeat.db"))
	defer store.Close()
	project, claim := statefulToolClaimForTest(t, store)
	ctx := context.Background()
	command := HeartbeatExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash, LeaseSeconds: 60}
	for _, item := range []struct {
		name, code string
		edit       func(*HeartbeatExecutionAttemptCommand)
	}{
		{"token", "ATTEMPT_TOKEN_INVALID", func(c *HeartbeatExecutionAttemptCommand) { c.AttemptToken = "other-token" }},
		{"snapshot", "ATTEMPT_INPUT_CHANGED", func(c *HeartbeatExecutionAttemptCommand) { c.InputSnapshotHash = "other-snapshot" }},
		{"lease-low", "REQUEST_VALIDATION_FAILED", func(c *HeartbeatExecutionAttemptCommand) { c.LeaseSeconds = 29 }},
		{"lease-high", "REQUEST_VALIDATION_FAILED", func(c *HeartbeatExecutionAttemptCommand) { c.LeaseSeconds = 1801 }},
		{"no-token", "REQUEST_VALIDATION_FAILED", func(c *HeartbeatExecutionAttemptCommand) { c.AttemptToken = "" }},
	} {
		t.Run(item.name, func(t *testing.T) {
			other := command
			item.edit(&other)
			_, err := store.HeartbeatExecutionAttempt(ctx, other)
			assertDomainCode(t, err, item.code)
		})
	}
	activity := AgentActivityIdentity{ProjectID: project.ProjectID, ExecutionAttemptID: "foreign", AttemptToken: claim.AttemptToken}
	_, err := store.HeartbeatExecutionAttempt(WithAgentActivity(ctx, activity), command)
	assertDomainCode(t, err, "AGENT_ACTIVITY_SCOPE_MISMATCH")
	store.now = func() time.Time { return claim.Attempt.LeaseUntil }
	_, err = store.HeartbeatExecutionAttempt(ctx, command)
	assertDomainCode(t, err, "ATTEMPT_LEASE_EXPIRED")
	unchanged, err := store.GetExecutionAttempt(ctx, command.AttemptID)
	if err != nil || !unchanged.LeaseUntil.Equal(claim.Attempt.LeaseUntil) {
		t.Fatalf("rejected renewal changed lease: %+v %v", unchanged, err)
	}
}

func TestExecutionHeartbeatChecksCancellationPermissionsAndSafePause(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "heartbeat.db"))
	defer store.Close()
	project, claim := statefulToolClaimForTest(t, store)
	ctx := context.Background()
	command := HeartbeatExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash, LeaseSeconds: 60}
	for _, item := range []struct{ name, update, restore, code string }{
		{"membership", `UPDATE workspace_memberships SET status = 'disabled'`, `UPDATE workspace_memberships SET status = 'active'`, "WORKSPACE_ACCESS_DENIED"},
		{"role", `UPDATE workspace_memberships SET role = 'viewer'`, `UPDATE workspace_memberships SET role = 'owner'`, "ROLE_FORBIDDEN"},
		{"workspace", `UPDATE workspaces SET status = 'deleting'`, `UPDATE workspaces SET status = 'active'`, "WORKSPACE_ACCESS_DENIED"},
		{"project", `UPDATE projects SET deleted_at = '2026-09-06T00:00:00Z'`, `UPDATE projects SET deleted_at = NULL`, "ATTEMPT_STALE"},
	} {
		t.Run(item.name, func(t *testing.T) {
			if _, err := store.db.Exec(item.update); err != nil {
				t.Fatal(err)
			}
			_, err := store.HeartbeatExecutionAttempt(ctx, command)
			assertDomainCode(t, err, item.code)
			if _, err := store.db.Exec(item.restore); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: claim.Attempt.RunID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.HeartbeatExecutionAttempt(ctx, command); err != nil {
		t.Fatalf("safe-boundary pause interrupted active work: %v", err)
	}
	if _, err := store.CancelRun(ctx, CancelRunCommand{RunID: claim.Attempt.RunID, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	_, err := store.HeartbeatExecutionAttempt(ctx, command)
	assertDomainCode(t, err, "ATTEMPT_STALE")
	activity := AgentActivityIdentity{ProjectID: project.ProjectID, ExecutionAttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken}
	_, err = store.ResolveAgentActivityPrincipal(ctx, activity)
	assertDomainCode(t, err, "ATTEMPT_STALE")
}

func TestExecutionHeartbeatAcknowledgesSubmittedResultWithoutRenewing(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "heartbeat.db"))
	defer store.Close()
	_, claim := statefulToolClaimForTest(t, store)
	ctx := context.Background()
	received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID,
		AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash,
		ResponsePayload: validSourceAnalysisProviderResponse(t, claim)})
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return claim.Attempt.LeaseUntil.Add(time.Second) }
	command := HeartbeatExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash, LeaseSeconds: 60}
	ack, err := store.HeartbeatExecutionAttempt(ctx, command)
	if err != nil || ack.Status != "result_received" || !ack.LeaseUntil.Equal(received.LeaseUntil) ||
		ack.ResponseHash == nil || *ack.ResponseHash != *received.ResponseHash {
		t.Fatalf("result receipt changed: %+v %v", ack, err)
	}
	if _, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, ExpectedResponseHash: *received.ResponseHash}); err != nil {
		t.Fatal(err)
	}
	_, err = store.HeartbeatExecutionAttempt(ctx, command)
	assertDomainCode(t, err, "ATTEMPT_STALE")
}

func TestExecutionHeartbeatAcknowledgesDurableApprovalWithoutRenewingOrReplaying(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "approval-heartbeat.db")
	store := openProjectFilesTestStore(t, path)
	defer func() { store.Close() }()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, claim := statefulToolClaimForTest(t, store)
	call := statefulApprovalForTest(t, store, project, claim, "heartbeat-approval")
	if _, err := store.PauseExecutionForApproval(ctx, statefulPauseForTest(claim, call.SDKToolCallID)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, path)
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	store.now = func() time.Time { return claim.Attempt.LeaseUntil.Add(time.Hour) }
	command := HeartbeatExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash, LeaseSeconds: 60}
	checkReceipt := func() {
		t.Helper()
		ack, err := store.HeartbeatExecutionAttempt(ctx, command)
		if err != nil || ack.Status != "waiting_approval" || !ack.LeaseUntil.Equal(claim.Attempt.LeaseUntil) || ack.AttemptNo != claim.Attempt.AttemptNo {
			t.Fatalf("approval heartbeat changed the lease or identity: %+v %v", ack, err)
		}
	}
	checkReceipt()
	resolveBackgroundApprovalForTest(t, store, call, "approve")
	checkReceipt()
	other, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
	if err != nil || other == nil || other.AttemptToken == claim.AttemptToken {
		t.Fatalf("resume: %+v %v", other, err)
	}
	_, err = store.HeartbeatExecutionAttempt(ctx, command)
	assertDomainCode(t, err, "ATTEMPT_TOKEN_INVALID")
	command.AttemptToken = other.AttemptToken
	if ack, err := store.HeartbeatExecutionAttempt(ctx, command); err != nil || ack.Status != "running" {
		t.Fatalf("resumed heartbeat: %+v %v", ack, err)
	}
}

func TestExecutionHeartbeatApprovalReceiptStillChecksCheckpointAndCancellation(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "invalid-approval.db"))
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, claim := statefulToolClaimForTest(t, store)
	ctx := context.Background()
	call := statefulApprovalForTest(t, store, project, claim, "invalid-approval")
	if _, err := store.PauseExecutionForApproval(ctx, statefulPauseForTest(claim, call.SDKToolCallID)); err != nil {
		t.Fatal(err)
	}
	command := HeartbeatExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash, LeaseSeconds: 60}
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer'`); err != nil {
		t.Fatal(err)
	}
	_, err := store.HeartbeatExecutionAttempt(ctx, command)
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='owner'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM execution_run_states WHERE attempt_id=?`, claim.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	_, err = store.HeartbeatExecutionAttempt(ctx, command)
	assertDomainCode(t, err, "AGENT_RUN_STATE_CORRUPT")
	if _, err := store.CancelRun(ctx, CancelRunCommand{RunID: claim.Attempt.RunID, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	_, err = store.HeartbeatExecutionAttempt(ctx, command)
	assertDomainCode(t, err, "ATTEMPT_STALE")
}
