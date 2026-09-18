package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"content-agent/backend/internal/identity"
)

func appendExecutionInputForTest(t *testing.T, store *Store, project Project, claim *TaskClaim, key string) ExecutionInput {
	t.Helper()
	input, err := store.AppendExecutionInput(identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal()), AppendExecutionInputCommand{
		CommandMeta: CommandMeta{CommandType: "append_execution_input", IdempotencyKey: key, RequestHash: key}, ProjectID: project.ProjectID, AttemptID: claim.Attempt.AttemptID, Content: "Requirement " + key})
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func inputCheckpointForTest(t *testing.T, claim *TaskClaim, included []string) PauseExecutionForApprovalCommand {
	t.Helper()
	pause := statefulPauseForTest(claim)
	pause.UserPause = true
	ids, hashes := []string{}, []string{}
	for _, input := range claim.AdditionalInputs {
		ids = append(ids, input.InputID)
		hashes = append(hashes, input.ContentHash)
	}
	if len(ids) > 0 {
		encoded, err := json.Marshal(map[string]any{"schema_version": "stateful_worker.v3", "input_ledger": map[string]any{"ids": ids, "hashes": hashes, "included": included}})
		if err != nil {
			t.Fatal(err)
		}
		pause.WorkerState = encoded
	}
	return pause
}

func TestExecutionInputAutomaticResumeReceiptAndRebuild(t *testing.T) {
	ctx := context.Background()
	user := identity.WithPrincipal(ctx, identity.DefaultLocalPrincipal())
	database := filepath.Join(t.TempDir(), "inputs.db")
	store := openProjectFilesTestStore(t, database)
	defer func() { store.Close() }()
	project, claim := sdkResultRepairClaim(t, store)
	original, _ := json.Marshal(claim.ContextPack)
	first := appendExecutionInputForTest(t, store, project, claim, "first")
	heartbeat := HeartbeatExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash, LeaseSeconds: 60}
	if receipt, err := store.HeartbeatExecutionAttempt(ctx, heartbeat); err != nil || !receipt.PauseRequested {
		t.Fatalf("new input did not request native pause: %v", err)
	}
	record := RecordExecutionInputsCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash, IncludedInputIDs: []string{first.InputID}}
	assertDomainCode(t, store.RecordExecutionInputsIncluded(ctx, record), "EXECUTION_INPUT_CONFLICT")
	if _, err := store.PauseExecutionForApproval(ctx, inputCheckpointForTest(t, claim, nil)); err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRun(ctx, claim.Attempt.RunID)
	if err != nil || run.Status != "running" {
		t.Fatalf("automatic input pause changed Run status: %s %v", run.Status, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	resumed, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
	if err != nil || resumed == nil || len(resumed.AdditionalInputs) != 1 {
		t.Fatalf("resume lost input: %v", err)
	}
	pack, _ := json.Marshal(resumed.ContextPack)
	if string(pack) != string(original) || resumed.Attempt.AttemptID != claim.Attempt.AttemptID || resumed.AttemptToken == claim.AttemptToken {
		t.Fatal("input changed execution identity")
	}
	assertDomainCode(t, store.RecordExecutionInputsIncluded(ctx, record), "ATTEMPT_TOKEN_INVALID")
	record.AttemptToken = resumed.AttemptToken
	if err := store.RecordExecutionInputsIncluded(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordExecutionInputsIncluded(ctx, record); err != nil {
		t.Fatal(err)
	}
	view, err := store.GetExecutionInputs(user, project.ProjectID, claim.Attempt.AttemptID)
	if err != nil || !view.CanAppend || view.Inputs[0].Status != "included" || view.Inputs[0].IncludedAt == nil {
		t.Fatalf("missing public receipt: %v", err)
	}
	second := appendExecutionInputForTest(t, store, project, resumed, "second")
	record.IncludedInputIDs = []string{first.InputID, second.InputID}
	assertDomainCode(t, store.RecordExecutionInputsIncluded(ctx, record), "EXECUTION_INPUT_CONFLICT")
	if _, err := store.PauseExecutionForApproval(ctx, inputCheckpointForTest(t, resumed, []string{first.InputID})); err != nil {
		t.Fatal(err)
	}
	resumed, err = store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
	if err != nil || resumed == nil || len(resumed.AdditionalInputs) != 2 {
		t.Fatalf("late input lost: %v", err)
	}
	record.AttemptToken = resumed.AttemptToken
	for _, ids := range [][]string{{second.InputID}, {first.InputID, first.InputID}, {first.InputID, second.InputID, "foreign"}} {
		wrong := record
		wrong.IncludedInputIDs = ids
		assertDomainCode(t, store.RecordExecutionInputsIncluded(ctx, wrong), "EXECUTION_INPUT_CONFLICT")
	}
	if err := store.RecordExecutionInputsIncluded(ctx, record); err != nil {
		t.Fatal(err)
	}
	duplicate := appendExecutionInputForTest(t, store, project, resumed, "first")
	if duplicate.InputID != first.InputID || duplicate.Status != "included" {
		t.Fatal("idempotent append returned stale or duplicate receipt")
	}
}

func TestExecutionInputOldApprovalRequiresExplicitRunResume(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "approval.db"))
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, claim := sdkResultRepairClaim(t, store)
	call := statefulApprovalForTest(t, store, project, claim, "approve")
	if _, err := store.PauseExecutionForApproval(ctx, statefulPauseForTest(claim, call.SDKToolCallID)); err != nil {
		t.Fatal(err)
	}
	appendExecutionInputForTest(t, store, project, claim, "during-approval")
	resolveBackgroundApprovalForTest(t, store, call, "approve")
	if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim)); err != nil || next != nil {
		t.Fatalf("input silently resumed approved tool: %v", err)
	}
	if _, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: claim.Attempt.RunID}); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
	if err != nil || resumed == nil || len(resumed.AdditionalInputs) != 1 || len(resumed.Resume.ApprovalDecisions) != 1 {
		t.Fatalf("explicit resume lost approval/input: %v", err)
	}
}

func TestExecutionInputCheckpointCannotOmitClaimedInputOrForgeReceipt(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "checkpoint.db"))
	defer store.Close()
	project, claim := sdkResultRepairClaim(t, store)
	input := appendExecutionInputForTest(t, store, project, claim, "first")
	if _, err := store.PauseExecutionForApproval(ctx, inputCheckpointForTest(t, claim, nil)); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	missing := statefulPauseForTest(claim)
	missing.UserPause = true
	_, err = store.PauseExecutionForApproval(ctx, missing)
	assertDomainCode(t, err, "EXECUTION_INPUT_CONFLICT")
	_, err = store.PauseExecutionForApproval(ctx, inputCheckpointForTest(t, claim, []string{input.InputID}))
	assertDomainCode(t, err, "EXECUTION_INPUT_CONFLICT")
	if _, err := store.PauseExecutionForApproval(ctx, inputCheckpointForTest(t, claim, nil)); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionInputUserAndTerminalBoundaries(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "owner.db"))
	defer store.Close()
	project, claim := sdkResultRepairClaim(t, store)
	user := identity.WithPrincipal(ctx, identity.DefaultLocalPrincipal())
	command := AppendExecutionInputCommand{ProjectID: project.ProjectID, AttemptID: claim.Attempt.AttemptID, Content: "request"}
	_, err := store.AppendExecutionInput(ctx, command)
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
	_, err = store.AppendExecutionInput(WithAgentActivity(user, AgentActivityIdentity{ExecutionAttemptID: claim.Attempt.AttemptID}), command)
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
	other := identity.DefaultLocalPrincipal()
	other.UserID = "other-author"
	if err := store.BootstrapPrincipal(ctx, other); err != nil {
		t.Fatal(err)
	}
	_, err = store.AppendExecutionInput(identity.WithPrincipal(ctx, other), command)
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
	oversized := command
	oversized.Content = strings.Repeat("x", (32<<10)+1)
	_, err = store.AppendExecutionInput(user, oversized)
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	input := appendExecutionInputForTest(t, store, project, claim, "late")
	received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash, ResponsePayload: validSourceAnalysisProviderResponse(t, claim)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AppendExecutionInput(user, command)
	assertDomainCode(t, err, "EXECUTION_INPUT_CONFLICT")
	if _, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, ExpectedResponseHash: *received.ResponseHash}); err != nil {
		t.Fatal(err)
	}
	view, err := store.GetExecutionInputs(user, project.ProjectID, claim.Attempt.AttemptID)
	if err != nil || view.CanAppend || view.Inputs[0].InputID != input.InputID || view.Inputs[0].Status != "received" {
		t.Fatalf("late input incorrectly marked included: %v", err)
	}
}

func TestExecutionInputRepairClaimAndExpiredReceipt(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "repair.db"))
	defer store.Close()
	project, claim := sdkResultRepairClaim(t, store)
	queueResultRepair(t, store, claim)
	input := appendExecutionInputForTest(t, store, project, claim, "repair")
	resumed, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
	if err != nil || resumed == nil || resumed.Repair == nil || len(resumed.AdditionalInputs) != 1 {
		t.Fatalf("repair missing input: %v", err)
	}
	store.now = func() time.Time { return resumed.Attempt.LeaseUntil.Add(time.Second) }
	err = store.RecordExecutionInputsIncluded(ctx, RecordExecutionInputsCommand{AttemptID: resumed.Attempt.AttemptID, AttemptToken: resumed.AttemptToken, InputSnapshotHash: resumed.Attempt.InputSnapshotHash, IncludedInputIDs: []string{input.InputID}})
	assertDomainCode(t, err, "ATTEMPT_LEASE_EXPIRED")
}

func TestExecutionInputRechecksRoleWithinWriteTransaction(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "input-revoked.db"))
	defer store.Close()
	project, claim := sdkResultRepairClaim(t, store)
	principal := identity.DefaultLocalPrincipal()
	stale := identity.WithPrincipal(ctx, principal)
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, principal.WorkspaceID, principal.UserID); err != nil {
		t.Fatal(err)
	}
	_, err := store.AppendExecutionInput(stale, AppendExecutionInputCommand{ProjectID: project.ProjectID, AttemptID: claim.Attempt.AttemptID, Content: "Stale permission"})
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_inputs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked principal wrote input: %d %v", count, err)
	}
}

func TestExecutionInputMigration47AndConcurrentRetries(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "migration-input.db")
	store := openProjectFilesTestStore(t, database)
	project, claim := sdkResultRepairClaim(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE execution_inputs; PRAGMA user_version=47;`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	defer store.Close()
	var backup string
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=47 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil || backup == "" {
		t.Fatalf("migration backup: %s %v", backup, err)
	}
	user := identity.WithPrincipal(ctx, identity.DefaultLocalPrincipal())
	command := AppendExecutionInputCommand{CommandMeta: CommandMeta{CommandType: "append_execution_input", IdempotencyKey: "concurrent", RequestHash: "same"}, ProjectID: project.ProjectID, AttemptID: claim.Attempt.AttemptID, Content: "Same input"}
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := store.AppendExecutionInput(user, command); errors <- err }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	view, err := store.GetExecutionInputs(user, project.ProjectID, claim.Attempt.AttemptID)
	if err != nil || len(view.Inputs) != 1 || !view.CanAppend {
		t.Fatalf("migration/retry lost attempt or duplicated input: %+v %v", view, err)
	}
	command.RequestHash = "different"
	_, err = store.AppendExecutionInput(user, command)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	if _, err := store.CancelRun(ctx, CancelRunCommand{RunID: claim.Attempt.RunID, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	command.CommandMeta = CommandMeta{}
	_, err = store.AppendExecutionInput(user, command)
	assertDomainCode(t, err, "EXECUTION_INPUT_CONFLICT")
	view, err = store.GetExecutionInputs(user, project.ProjectID, claim.Attempt.AttemptID)
	if err != nil || view.CanAppend || view.Inputs[0].Status != "received" {
		t.Fatalf("cancel changed receipt: %+v %v", view, err)
	}
}
