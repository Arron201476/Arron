package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func sdkResultRepairClaim(t *testing.T, store *Store) (Project, *TaskClaim) {
	t.Helper()
	project, _, initial := startNovelRunForLifecycle(t, store, "SDK result repair")
	approveAndResumeLifecycleRun(t, store, project, initial)
	claim, err := store.ClaimExecutionTask(context.Background(), ClaimExecutionTaskCommand{
		WorkerID: "sdk-repair-fixture", ProviderID: "openai_agents_sdk", ExecutorIDs: []string{"worker.structured_content"}, LeaseSeconds: 60})
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	return project, claim
}

func TestSDKResultRepairCorruptionDoesNotBlockOtherRuns(t *testing.T) {
	for _, kind := range []string{"context", "candidate", "claims", "received"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "queue.db"))
			defer store.Close()
			_, first := sdkResultRepairClaim(t, store)
			_, second := sdkResultRepairClaim(t, store)
			if kind == "received" {
				received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{AttemptID: first.Attempt.AttemptID, AttemptToken: first.AttemptToken, InputSnapshotHash: first.Attempt.InputSnapshotHash, ResponsePayload: validSourceAnalysisProviderResponse(t, first)})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.Exec(`UPDATE execution_attempts SET response_payload_json='{}',lease_until='2000-01-01T00:00:00Z' WHERE attempt_id=?`, received.AttemptID); err != nil {
					t.Fatal(err)
				}
				_, err = store.CommitExecutionResult(ctx, CommitExecutionResultCommand{AttemptID: received.AttemptID, ExpectedResponseHash: *received.ResponseHash})
				assertDomainCode(t, err, "ATTEMPT_RESULT_CHANGED")
			} else {
				queueResultRepair(t, store, first)
				query := map[string]string{"context": `UPDATE context_packs SET payload_json='{}' WHERE attempt_id=?`, "candidate": `UPDATE execution_result_rejections SET response_payload_json='{}' WHERE attempt_id=?`, "claims": `UPDATE execution_result_rejections SET claim_count=3 WHERE attempt_id=?`}[kind]
				if _, err := store.db.Exec(query, first.Attempt.AttemptID); err != nil {
					t.Fatal(err)
				}
			}
			queueResultRepair(t, store, second)
			next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(second))
			if err != nil || next == nil || next.Attempt.AttemptID != second.Attempt.AttemptID {
				t.Fatalf("corrupt queue head blocked other Run: %+v %v", next, err)
			}
			failed, err := store.GetExecutionAttempt(ctx, first.Attempt.AttemptID)
			if err != nil || failed.Status != "failed" {
				t.Fatalf("corrupt receipt not terminal: %+v %v", failed, err)
			}
		})
	}
}

func sdkManagedResultRepairClaim(t *testing.T, store *Store) *TaskClaim {
	t.Helper()
	ctx := context.Background()
	if _, err := store.InstallSkillDirectory(ctx, filepath.Join(testProjectRoot(t), "fixtures", "skills", "stateful", "story-review-workflow"), "repair-fixture"); err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, "Managed result repair")
	if err != nil {
		t.Fatal(err)
	}
	asset := createTextAsset(t, store, project.ProjectID, "premise.txt", "A reporter discovers evidence concealed by a mentor.")
	reference := &agentcontract.CapabilityRef{CapabilityID: "story_review_workflow", Version: "1.0.0", SelectionMode: "explicit"}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		CommandMeta:    CommandMeta{Scope: project.ProjectID, CommandType: "commit_agent_turn", IdempotencyKey: "repair-proposal", RequestHash: "repair-proposal"},
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{Content: "Review the story.", CapabilityRef: reference,
			AttachmentRefs: []agentcontract.AttachmentRef{{AssetID: asset.Asset.AssetID, AssetSnapshotID: asset.Snapshot.AssetSnapshotID}}},
		Decision: agentcontract.AgentDecision{Reply: "Configure review.", Intent: "propose_capability", Confidence: 1, CapabilityRef: reference,
			ProposedAction: &agentcontract.ProposedActionDraft{ActionType: "collect_run_configuration", CapabilityRef: reference, Input: json.RawMessage(`{}`), Config: json.RawMessage(`{}`)}},
	})
	if err != nil || exchange.Action == nil {
		t.Fatalf("proposal: %v", err)
	}
	configured, err := store.ConfigureProposedAction(ctx, ConfigureProposedActionCommand{
		CommandMeta:      CommandMeta{Scope: project.ProjectID, CommandType: "configure_proposed_action", IdempotencyKey: "repair-config", RequestHash: "repair-config"},
		ProposedActionID: exchange.Action.ProposedActionID, ExpectedVersion: exchange.Action.Version,
		Input: exchange.Action.Input, Config: json.RawMessage(`{"review_depth":"deep"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.StartRun(ctx, StartRunCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "start_run", IdempotencyKey: "repair-start", RequestHash: "repair-start"},
		ProjectID:   project.ProjectID, ConversationID: project.PrimaryConversationID,
		CapabilityID: reference.CapabilityID, CapabilityVersion: reference.Version, RunKind: "generation",
		Input: configured.Input, Config: configured.Config, Confirmed: true,
		ProposedActionID: configured.ProposedActionID, ProposedActionVersion: configured.Version,
		ConfirmationMessageID: configured.ConfirmationMessageID, ConfirmationSnapshotHash: configured.SnapshotHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{WorkerID: "repair-fixture", ProviderID: "openai_agents_sdk", ExecutorIDs: []string{"worker.structured_content"}, LeaseSeconds: 60})
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	return claim
}

func TestSDKResultRepairQueueRechecksRunAfterSiblingFailure(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "siblings.db"))
	defer store.Close()
	target := sdkManagedResultRepairClaim(t, store)
	corrupt := cloneQueuedExecutionForTest(t, store, target, 0, "")
	queueResultRepair(t, store, corrupt)
	queueResultRepair(t, store, target)
	if _, err := store.db.Exec(`UPDATE execution_attempts SET started_at=? WHERE attempt_id=?`, formatTime(store.now().Add(-time.Hour)), corrupt.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE execution_result_rejections SET response_payload_json='{}' WHERE attempt_id=?`, corrupt.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(target)); err != nil || next != nil {
		t.Fatalf("failed sibling Run still claimed: claim_present=%t err=%v", next != nil, err)
	}
	run, err := store.GetRun(ctx, target.Attempt.RunID)
	if err != nil || run.Status != "failed" {
		t.Fatalf("corrupt Run not failed: %+v %v", run, err)
	}
	var claims int
	if err := store.db.QueryRow(`SELECT claim_count FROM execution_result_rejections WHERE attempt_id=?`, target.Attempt.AttemptID).Scan(&claims); err != nil || claims != 0 {
		t.Fatalf("healthy sibling consumed a repair: %d %v", claims, err)
	}
}

func TestSDKResultRepairQueuePreservesHealthyBatchSibling(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "batch.db"))
	defer store.Close()
	_, target := sdkResultRepairClaim(t, store)
	corrupt := cloneQueuedExecutionForTest(t, store, target, 0, "")
	queueResultRepair(t, store, corrupt)
	queueResultRepair(t, store, target)
	if _, err := store.db.Exec(`UPDATE execution_attempts SET started_at=? WHERE attempt_id=?`, formatTime(store.now().Add(-time.Hour)), corrupt.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE execution_result_rejections SET response_payload_json='{}' WHERE attempt_id=?`, corrupt.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(target))
	if err != nil || next == nil || next.Attempt.AttemptID != target.Attempt.AttemptID {
		t.Fatalf("healthy batch sibling not claimed: claim_present=%t err=%v", next != nil, err)
	}
	run, err := store.GetRun(ctx, target.Attempt.RunID)
	if err != nil || run.Status != "running" {
		t.Fatalf("batch ended before healthy sibling settled: status=%s err=%v", run.Status, err)
	}
	received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{AttemptID: next.Attempt.AttemptID, AttemptToken: next.AttemptToken,
		InputSnapshotHash: next.Attempt.InputSnapshotHash, ResponsePayload: json.RawMessage(`{"wrong":"second-rejection"}`), Usage: json.RawMessage(`{"requests":3,"total_tokens":36}`)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{AttemptID: received.AttemptID, AttemptToken: next.AttemptToken, ExpectedResponseHash: *received.ResponseHash, RepairOutput: true})
	if err != nil || result.CommitStatus != "output_repair_failed" {
		t.Fatalf("second rejection: status=%s err=%v", result.CommitStatus, err)
	}
	run, err = store.GetRun(ctx, target.Attempt.RunID)
	if err != nil || run.Status != "failed" {
		t.Fatalf("settled batch not failed: status=%s err=%v", run.Status, err)
	}
}

func TestSDKResultRecoveryPreservesLegacyProviderProtocol(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "legacy.db"))
	defer store.Close()
	_, claim := statefulToolClaimForTest(t, store)
	_, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash, ResponsePayload: validSourceAnalysisProviderResponse(t, claim)})
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return claim.Attempt.LeaseUntil.Add(time.Second) }
	if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim)); err != nil || next != nil {
		t.Fatalf("legacy Worker received SDK-only recovery: claim_present=%t err=%v", next != nil, err)
	}
	stored, err := store.GetExecutionAttempt(ctx, claim.Attempt.AttemptID)
	if err != nil || stored.Status != "result_received" {
		t.Fatalf("legacy receipt changed: status=%s err=%v", stored.Status, err)
	}
}

func TestSDKResultRepairQueueSkipsFullPageForOtherModel(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "pages.db"))
	defer store.Close()
	_, target := sdkResultRepairClaim(t, store)
	for index := 0; index < 101; index++ {
		other := cloneQueuedExecutionForTest(t, store, target, index, "model")
		queueResultRepair(t, store, other)
		if _, err := store.db.Exec(`UPDATE execution_attempts SET started_at=? WHERE attempt_id=?`, formatTime(store.now().Add(-time.Hour)), other.Attempt.AttemptID); err != nil {
			t.Fatal(err)
		}
	}
	queueResultRepair(t, store, target)
	next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(target))
	if err != nil || next == nil || next.Repair == nil || next.Attempt.AttemptID != target.Attempt.AttemptID {
		t.Fatalf("repair starved behind 101 other-model receipts: %+v %v", next, err)
	}
	var claimed int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_result_rejections WHERE status='claimed'`).Scan(&claimed); err != nil || claimed != 1 {
		t.Fatalf("other-model receipt modified: %d %v", claimed, err)
	}
}

func TestSDKResultRepairRejectsUsageRegression(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "usage.db"))
	defer store.Close()
	_, claim := sdkResultRepairClaim(t, store)
	queueResultRepair(t, store, claim)
	claim, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	for _, usage := range []string{`{}`, `{"requests":1,"total_tokens":24}`, `{"requests":2,"total_tokens":23}`, `{"requests":null,"total_tokens":24}`, `{"requests":2.5,"total_tokens":24}`} {
		_, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash, ResponsePayload: validSourceAnalysisProviderResponse(t, claim), Usage: json.RawMessage(usage)})
		assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM execution_result_rejections WHERE attempt_id=?`, claim.Attempt.AttemptID).Scan(&status); err != nil || status != "claimed" {
		t.Fatalf("invalid usage changed receipt: %s %v", status, err)
	}
}

func TestSDKResultRepairCommitRechecksOwnershipInWriteTransaction(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "commit-owner.db"))
	defer store.Close()
	_, claim := sdkResultRepairClaim(t, store)
	received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash, ResponsePayload: validSourceAnalysisProviderResponse(t, claim)})
	if err != nil {
		t.Fatal(err)
	}
	command := CommitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: "stale-token", ExpectedResponseHash: *received.ResponseHash, RepairOutput: true}
	_, err = store.commitExecutionResult(ctx, command)
	assertDomainCode(t, err, "ATTEMPT_TOKEN_INVALID")
	command.AttemptToken = claim.AttemptToken
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET status='disabled' WHERE user_id=(SELECT user_id FROM runs WHERE run_id=?)`, claim.Attempt.RunID); err != nil {
		t.Fatal(err)
	}
	_, err = store.commitExecutionResult(ctx, command)
	assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
	current, err := store.GetExecutionAttempt(ctx, claim.Attempt.AttemptID)
	if err != nil || current.Status != "result_received" {
		t.Fatalf("denied commit changed receipt: %+v %v", current, err)
	}
}

func TestSDKResultRepairMigration46PreservesReceivedResult(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "migration.db")
	store := openProjectFilesTestStore(t, database)
	_, claim := sdkResultRepairClaim(t, store)
	received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash, ResponsePayload: json.RawMessage(`{"wrong":"v46-candidate"}`), Usage: json.RawMessage(`{"requests":2,"total_tokens":24}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE execution_result_rejections; PRAGMA user_version=46;`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	defer store.Close()
	var backup string
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=46 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil || backup == "" {
		t.Fatalf("migration backup: %s %v", backup, err)
	}
	result, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{AttemptID: received.AttemptID, ExpectedResponseHash: *received.ResponseHash, AttemptToken: claim.AttemptToken, RepairOutput: true})
	if err != nil || result.CommitStatus != "output_repair_queued" {
		t.Fatalf("migration lost result: %+v %v", result, err)
	}
	resumed, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
	if err != nil || resumed == nil || resumed.Repair == nil || resumed.Repair.Candidate != `{"wrong":"v46-candidate"}` || resumed.Attempt.InputSnapshotHash != claim.Attempt.InputSnapshotHash || !resumed.Attempt.StartedAt.Equal(claim.Attempt.StartedAt) {
		t.Fatalf("migration changed frozen receipt: %+v %v", resumed, err)
	}
}

func TestSDKResultRepairPublicDeletionCommandsCancelAttempt(t *testing.T) {
	for _, scope := range []string{"project", "workspace"} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.Background()
			store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "delete.db"))
			defer store.Close()
			project, claim := sdkResultRepairClaim(t, store)
			queueResultRepair(t, store, claim)
			if scope == "project" {
				preview, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.ConfirmProjectDelete(ctx, ConfirmProjectDeleteCommand{ProjectID: project.ProjectID, PreviewHash: preview.SnapshotHash, Confirmed: true}); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := store.DeleteWorkspace(ctx, DeleteWorkspaceCommand{WorkspaceID: identity.DefaultWorkspaceID, UserID: identity.DefaultUserID, Confirmation: identity.DefaultWorkspaceID}); err != nil {
					t.Fatal(err)
				}
			}
			var status string
			if err := store.db.QueryRow(`SELECT status FROM execution_attempts WHERE attempt_id=?`, claim.Attempt.AttemptID).Scan(&status); err != nil || status != "cancelled" {
				t.Fatalf("deleted repair still live: %s %v", status, err)
			}
			if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim)); err != nil || next != nil {
				t.Fatalf("deleted repair claimed: %+v %v", next, err)
			}
		})
	}
}

func TestSDKCommitRejectionQueuesSameAttemptRepair(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "repair.db")
	store := openProjectFilesTestStore(t, database)
	defer func() { store.Close() }()
	_, claim := sdkResultRepairClaim(t, store)
	received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID,
		AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash,
		ResponsePayload: json.RawMessage(`{"wrong":"rejected-candidate"}`), Usage: json.RawMessage(`{"requests":2,"total_tokens":24}`)})
	if err != nil || received.ResponseHash == nil {
		t.Fatalf("submit: %+v %v", received, err)
	}
	command := CommitExecutionResultCommand{AttemptID: received.AttemptID, ExpectedResponseHash: *received.ResponseHash, AttemptToken: claim.AttemptToken, RepairOutput: true}
	result, err := store.CommitExecutionResult(ctx, command)
	if err != nil || result.CommitStatus != "output_repair_queued" {
		t.Fatalf("commit rejection did not become a durable repair: %+v %v", result, err)
	}
	assertTaskOutputRepair(t, store, claim, "queued")
	if duplicate, err := store.CommitExecutionResult(ctx, command); err != nil || duplicate.CommitStatus != result.CommitStatus {
		t.Fatalf("lost acknowledgement: %+v %v", duplicate, err)
	}
	wrong := command
	wrong.ExpectedResponseHash = "wrong"
	_, err = store.CommitExecutionResult(ctx, wrong)
	assertDomainCode(t, err, "ATTEMPT_RESULT_CHANGED")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	resumed, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
	if err != nil || resumed == nil || resumed.Attempt.AttemptID != claim.Attempt.AttemptID || resumed.Attempt.AttemptNo != 1 {
		t.Fatalf("repair replaced the original attempt: %+v %v", resumed, err)
	}
	if resumed.Repair == nil || resumed.Repair.Candidate != `{"wrong":"rejected-candidate"}` || resumed.Repair.ResponseHash != *received.ResponseHash ||
		resumed.Repair.InputSnapshotHash != claim.Attempt.InputSnapshotHash || !resumed.Attempt.StartedAt.Equal(claim.Attempt.StartedAt) || resumed.AttemptToken == claim.AttemptToken {
		t.Fatalf("repair lost identity or candidate: %+v", resumed)
	}
	assertTaskOutputRepair(t, store, resumed, "claimed")
	_, err = store.CommitExecutionResult(ctx, command)
	assertDomainCode(t, err, "ATTEMPT_TOKEN_INVALID")
	_, err = store.BeginAgentToolCall(ctx, statefulToolCommand(Project{ProjectID: resumed.ContextPack.ProjectID, PrimaryConversationID: resumed.ContextPack.ConversationID}, resumed, "runtime:inspect_project", "unexpected", json.RawMessage(`{}`)))
	assertDomainCode(t, err, "AGENT_TOOL_EXECUTION_MODE_UNSUPPORTED")
	repaired, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{AttemptID: resumed.Attempt.AttemptID, AttemptToken: resumed.AttemptToken,
		InputSnapshotHash: resumed.Attempt.InputSnapshotHash, ResponsePayload: validSourceAnalysisProviderResponse(t, resumed), Usage: json.RawMessage(`{"requests":3,"total_tokens":36}`)})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{AttemptID: repaired.AttemptID, ExpectedResponseHash: *repaired.ResponseHash, AttemptToken: resumed.AttemptToken, RepairOutput: true})
	if err != nil || committed.CommitStatus != "task_checkpointed" {
		t.Fatalf("repaired commit: %+v %v", committed, err)
	}
	var original, usage, status string
	if err := store.db.QueryRow(`SELECT response_payload_json,usage_json,status FROM execution_result_rejections WHERE attempt_id=?`, received.AttemptID).Scan(&original, &usage, &status); err != nil || original != `{"wrong":"rejected-candidate"}` || usage != `{"requests":2,"total_tokens":24}` || status != "closed" {
		t.Fatalf("rejected receipt overwritten: %s %s %s %v", original, usage, status, err)
	}
}

func assertTaskOutputRepair(t *testing.T, store *Store, claim *TaskClaim, status string) {
	t.Helper()
	tasks, err := store.ListTaskItems(context.Background(), claim.Attempt.StepRunID)
	if err != nil || len(tasks) != 1 || tasks[0].OutputRepair == nil || tasks[0].OutputRepair.Status != status || tasks[0].OutputRepair.RejectionNo != 1 || tasks[0].OutputRepair.ErrorCode != "OUTPUT_SCHEMA_VALIDATION_FAILED" {
		t.Fatalf("public repair projection: %+v %v", tasks, err)
	}
	payload, err := json.Marshal(tasks)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"rejected-candidate", "response_payload_json", "error_detail", "usage_hash", claim.AttemptToken} {
		if strings.Contains(string(payload), private) {
			t.Fatalf("public tasks leaked %s", private)
		}
	}
}

func queueResultRepair(t *testing.T, store *Store, claim *TaskClaim) CommitExecutionResultCommand {
	t.Helper()
	received, err := store.SubmitExecutionResult(context.Background(), SubmitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash, ResponsePayload: json.RawMessage(`{"wrong":"private-repair-candidate"}`), Usage: json.RawMessage(`{"requests":2,"total_tokens":24}`)})
	if err != nil {
		t.Fatal(err)
	}
	command := CommitExecutionResultCommand{AttemptID: received.AttemptID, ExpectedResponseHash: *received.ResponseHash, AttemptToken: claim.AttemptToken, RepairOutput: true}
	if result, err := store.CommitExecutionResult(context.Background(), command); err != nil || result.CommitStatus != "output_repair_queued" {
		t.Fatalf("queue: %+v %v", result, err)
	}
	return command
}

func TestSDKResultRepairControlAndCorruptionBoundaries(t *testing.T) {
	for _, mode := range []string{"pause-before", "pause-after", "cancel", "delete", "candidate", "usage", "missing", "owner", "wrong-model", "wrong-provider"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "boundary.db"))
			defer store.Close()
			project, claim := sdkResultRepairClaim(t, store)
			if mode == "pause-before" {
				if _, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: claim.Attempt.RunID}); err != nil {
					t.Fatal(err)
				}
			}
			command := queueResultRepair(t, store, claim)
			switch mode {
			case "pause-after":
				if _, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: claim.Attempt.RunID}); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if _, err := store.CancelRun(ctx, CancelRunCommand{RunID: claim.Attempt.RunID, Confirmed: true}); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if _, err := store.db.Exec(`UPDATE projects SET deleted_at=? WHERE project_id=?`, formatTime(store.now()), project.ProjectID); err != nil {
					t.Fatal(err)
				}
			case "candidate":
				if _, err := store.db.Exec(`UPDATE execution_result_rejections SET response_payload_json='{}' WHERE attempt_id=?`, claim.Attempt.AttemptID); err != nil {
					t.Fatal(err)
				}
			case "usage":
				if _, err := store.db.Exec(`UPDATE execution_result_rejections SET usage_json='{}' WHERE attempt_id=?`, claim.Attempt.AttemptID); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if _, err := store.db.Exec(`DELETE FROM execution_result_rejections WHERE attempt_id=?`, claim.Attempt.AttemptID); err != nil {
					t.Fatal(err)
				}
			case "owner":
				if _, err := store.db.Exec(`UPDATE workspace_memberships SET status='disabled' WHERE user_id=(SELECT user_id FROM runs WHERE run_id=?)`, claim.Attempt.RunID); err != nil {
					t.Fatal(err)
				}
			}
			claimCommand := statefulResumeCommand(claim)
			if mode == "wrong-model" {
				claimCommand.ModelID = "changed-model"
			}
			if mode == "wrong-provider" {
				claimCommand.ProviderID = "another-provider"
			}
			if next, err := store.ClaimExecutionTask(ctx, claimCommand); err != nil || next != nil {
				t.Fatalf("blocked repair claimed: %+v %v", next, err)
			}
			if strings.HasPrefix(mode, "pause-") {
				run, err := store.GetRunSnapshot(ctx, claim.Attempt.RunID)
				if err != nil || run.Run.Status != "paused" {
					t.Fatalf("pause lost: %+v %v", run, err)
				}
				if _, err := store.CommitExecutionResult(ctx, command); err != nil {
					t.Fatal(err)
				}
				if _, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: claim.Attempt.RunID}); err != nil {
					t.Fatal(err)
				}
				next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
				if err != nil || next == nil || next.Repair == nil || next.Attempt.AttemptID != claim.Attempt.AttemptID {
					t.Fatalf("paused repair lost: %+v %v", next, err)
				}
			}
		})
	}
}

func TestSDKResultRepairSecondRejectionIsDurableTerminal(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "terminal.db"))
	defer store.Close()
	_, claim := sdkResultRepairClaim(t, store)
	queueResultRepair(t, store, claim)
	repair, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
	if err != nil || repair == nil {
		t.Fatalf("repair: %+v %v", repair, err)
	}
	received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{AttemptID: repair.Attempt.AttemptID, AttemptToken: repair.AttemptToken, InputSnapshotHash: repair.Attempt.InputSnapshotHash,
		ResponsePayload: json.RawMessage(`{"wrong":"private-repair-candidate"}`), Usage: json.RawMessage(`{"requests":3,"total_tokens":36}`)})
	if err != nil {
		t.Fatal(err)
	}
	command := CommitExecutionResultCommand{AttemptID: received.AttemptID, ExpectedResponseHash: *received.ResponseHash, AttemptToken: repair.AttemptToken, RepairOutput: true}
	for range 2 {
		result, err := store.CommitExecutionResult(ctx, command)
		if err != nil || result.CommitStatus != "output_repair_failed" {
			t.Fatalf("terminal: %+v %v", result, err)
		}
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_result_rejections WHERE attempt_id=?`, received.AttemptID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("rejection ledger: %d %v", count, err)
	}
	if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(repair)); err != nil || next != nil {
		t.Fatalf("unbounded repair: %+v %v", next, err)
	}
	tasks, err := store.ListTaskItems(ctx, repair.Attempt.StepRunID)
	if err != nil || len(tasks) != 1 || tasks[0].Status != "failed" || tasks[0].FailureDetail == nil || tasks[0].FailureDetail.Retryable {
		t.Fatalf("terminal task: %+v %v", tasks, err)
	}
}

func TestSDKResultRepairLeaseRecoveryIsBoundedAndNeverRegenerates(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "lease.db"))
	defer store.Close()
	_, claim := sdkResultRepairClaim(t, store)
	queueResultRepair(t, store, claim)
	for range maxExecutionRepairClaims {
		next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
		if err != nil || next == nil || next.Repair == nil || next.Attempt.AttemptID != claim.Attempt.AttemptID {
			t.Fatalf("lease restarted main generation: %+v %v", next, err)
		}
		now := next.Attempt.LeaseUntil.Add(time.Second)
		store.now = func() time.Time { return now }
	}
	if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim)); err != nil || next != nil {
		t.Fatalf("repair lease retries unbounded: %+v %v", next, err)
	}
	var attempts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_attempts WHERE task_item_id=?`, claim.Task.TaskItemID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("new primary attempt: %d %v", attempts, err)
	}
}

func TestSDKReceivedResultRecoveryAfterLeaseAndUserPause(t *testing.T) {
	for _, pause := range []bool{false, true} {
		t.Run(map[bool]string{false: "lease", true: "pause"}[pause], func(t *testing.T) {
			ctx := context.Background()
			store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "received.db"))
			defer store.Close()
			_, claim := sdkResultRepairClaim(t, store)
			received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash, ResponsePayload: validSourceAnalysisProviderResponse(t, claim)})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := store.HeartbeatExecutionAttempt(ctx, HeartbeatExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash, LeaseSeconds: 60})
			if err != nil || receipt.Status != "result_received" || receipt.ResponseHash == nil || *receipt.ResponseHash != *received.ResponseHash {
				t.Fatalf("heartbeat lost durable result hash: %+v %v", receipt, err)
			}
			if pause {
				if _, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: claim.Attempt.RunID}); err != nil {
					t.Fatal(err)
				}
			}
			now := claim.Attempt.LeaseUntil.Add(time.Minute)
			store.now = func() time.Time { return now }
			next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
			if err != nil {
				t.Fatal(err)
			}
			if pause {
				if next != nil {
					t.Fatal("paused result recommitted automatically")
				}
				if _, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: claim.Attempt.RunID}); err != nil {
					t.Fatal(err)
				}
				next, err = store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
			}
			if err != nil || next == nil || next.Attempt.Status != "result_received" || next.Repair != nil || next.Attempt.AttemptID != claim.Attempt.AttemptID || *next.Attempt.ResponseHash != *received.ResponseHash {
				t.Fatalf("durable result lost: %+v %v", next, err)
			}
			if _, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{AttemptID: next.Attempt.AttemptID, ExpectedResponseHash: *next.Attempt.ResponseHash, AttemptToken: next.AttemptToken, RepairOutput: true}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
