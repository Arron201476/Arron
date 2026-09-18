package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
)

func TestInstalledBatchSkillPersistsScopesDependenciesAndApproval(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "content_agent.db")
	store, err := Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	started, sourceSnapshotID, invocationID := startInstalledBatchSkill(t, store)
	tasks, err := store.ListTaskItems(ctx, started.Steps[0].StepRunID)
	if err != nil || len(tasks) != 3 {
		t.Fatalf("batch tasks = %+v, %v", tasks, err)
	}
	for i, task := range tasks {
		if task.ItemKey != fmt.Sprintf("episode:%d", i+1) || task.Status != "pending" {
			t.Fatalf("task = %+v", task)
		}
	}
	first := claimManagedBatchTask(t, store)
	firstCommit := commitManagedBatchTask(t, store, first)
	if firstCommit.CommitStatus != "task_artifact_saved" || firstCommit.RunSnapshot.CurrentApproval != nil {
		t.Fatalf("partial commit = %+v", firstCommit)
	}
	if paused, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: started.Run.RunID}); err != nil || paused.Run.Status != "paused" {
		t.Fatalf("pause = %+v, %v", paused, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	if resumed, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: started.Run.RunID}); err != nil || resumed.Run.Status != "running" {
		t.Fatalf("resume = %+v, %v", resumed, err)
	}
	commits := []ArtifactCommitResult{firstCommit}
	for i := 0; i < 2; i++ {
		claim := claimManagedBatchTask(t, store)
		if claim.Task.TaskItemID == first.Task.TaskItemID {
			t.Fatal("resume re-executed a committed task")
		}
		commits = append(commits, commitManagedBatchTask(t, store, claim))
	}
	last := commits[2]
	if last.CommitStatus != "waiting_approval" || last.Approval.SubjectKind != "artifact_version_set" ||
		last.Approval.SubjectRefID != started.Steps[0].StepRunID || last.Approval.Scope != "batch" {
		t.Fatalf("batch approval = %+v", last)
	}
	for _, commit := range commits {
		var assets, configs int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_dependencies
			WHERE downstream_artifact_version_id = ? AND upstream_kind = 'asset_snapshot' AND upstream_ref_id = ?`,
			commit.ArtifactVersion.ArtifactVersionID, sourceSnapshotID).Scan(&assets); err != nil {
			t.Fatal(err)
		}
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_dependencies
			WHERE downstream_artifact_version_id = ? AND upstream_kind = 'config_snapshot'`,
			commit.ArtifactVersion.ArtifactVersionID).Scan(&configs); err != nil || assets != 1 || configs != 1 {
			t.Fatalf("scope %s dependencies: assets=%d configs=%d error=%v", commit.Artifact.ScopeKey, assets, configs, err)
		}
	}
	var versions int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM approval_subject_versions WHERE approval_request_id = ?`,
		last.Approval.ApprovalRequestID).Scan(&versions); err != nil || versions != 3 {
		t.Fatalf("approved version set = %d, %v", versions, err)
	}
	command := ResolveApprovalCommand{
		CommandMeta:       CommandMeta{Scope: started.Run.ProjectID, CommandType: "resolve_approval", IdempotencyKey: "batch-approve", RequestHash: "batch-approve-v1"},
		ApprovalRequestID: last.Approval.ApprovalRequestID, Action: "approve",
		ExpectedApprovalVersion: last.Approval.Version, SubjectSnapshotHash: last.Approval.SubjectSnapshotHash,
	}
	completed, err := store.ResolveApproval(ctx, command)
	if err != nil || completed.Run.Status != "completed" || completed.Steps[0].Status != "completed" {
		t.Fatalf("complete = %+v, %v", completed, err)
	}
	replayed, err := store.ResolveApproval(ctx, command)
	if err != nil || replayed.EventCursor != completed.EventCursor {
		t.Fatalf("approval replay = %+v, %v", replayed, err)
	}
	invocation, err := scanSkillInvocation(store.db.QueryRowContext(ctx, skillInvocationSelect+` WHERE skill_invocation_id = ?`, invocationID))
	if err != nil || invocation.Status != "completed" {
		t.Fatalf("invocation = %+v, %v", invocation, err)
	}
}

func TestInstalledBatchSkillManualRetryPreservesSuccessfulOutputs(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started, _, _ := startInstalledBatchSkill(t, store)
	first := commitManagedBatchTask(t, store, claimManagedBatchTask(t, store))
	failedClaim := claimManagedBatchTask(t, store)
	// Spend the declared automatic retry first; manual retry uses the same policy.
	failed, err := store.FailExecutionAttempt(ctx, FailExecutionAttemptCommand{
		AttemptID: failedClaim.Attempt.AttemptID, AttemptToken: failedClaim.AttemptToken,
		InputSnapshotHash: failedClaim.Attempt.InputSnapshotHash, ErrorCode: "MODEL_TIMEOUT",
	})
	if err != nil || !failed.Retryable || failed.Task.Status != "pending" {
		t.Fatalf("automatic retry = %+v, %v", failed, err)
	}
	automatic := claimManagedBatchTask(t, store)
	if automatic.Task.TaskItemID != failedClaim.Task.TaskItemID {
		t.Fatalf("automatic retry claimed wrong task = %+v", automatic)
	}
	failedClaim = automatic
	failed, err = store.FailExecutionAttempt(ctx, FailExecutionAttemptCommand{
		AttemptID: failedClaim.Attempt.AttemptID, AttemptToken: failedClaim.AttemptToken,
		InputSnapshotHash: failedClaim.Attempt.InputSnapshotHash, ErrorCode: "MODEL_TIMEOUT",
	})
	if err != nil || failed.Retryable || failed.Task.Status != "failed" {
		t.Fatalf("failure = %+v, %v", failed, err)
	}
	last := commitManagedBatchTask(t, store, claimManagedBatchTask(t, store))
	if last.RunSnapshot.Run.Status != "failed" || last.RunSnapshot.CurrentApproval != nil {
		t.Fatalf("unsettled batch = %+v", last)
	}
	if _, err := store.RetryFailedStep(ctx, RetryFailedStepCommand{StepRunID: started.Steps[0].StepRunID}); err != nil {
		t.Fatal(err)
	}
	retry := claimManagedBatchTask(t, store)
	if retry.Task.TaskItemID != failedClaim.Task.TaskItemID || retry.Attempt.AttemptNo != failedClaim.Attempt.AttemptNo+1 {
		t.Fatalf("retry selected wrong task = %+v", retry)
	}
	committed := commitManagedBatchTask(t, store, retry)
	if committed.RunSnapshot.Run.Status != "waiting_approval" {
		t.Fatalf("retry commit = %+v", committed)
	}
	var current string
	if err := store.db.QueryRowContext(ctx, `SELECT current_version_id FROM artifacts WHERE artifact_id = ?`, first.Artifact.ArtifactID).Scan(&current); err != nil || current != first.ArtifactVersion.ArtifactVersionID {
		t.Fatalf("successful version replaced: %s, %v", current, err)
	}
}

func TestInstalledParallelBatchSkillApprovesOutOfOrderResultsTogether(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	startInstalledBatchSkill(t, store)
	claims := []*TaskClaim{claimManagedBatchTask(t, store), claimManagedBatchTask(t, store), claimManagedBatchTask(t, store)}
	for i, index := range []int{2, 0, 1} {
		committed := commitManagedBatchTask(t, store, claims[index])
		if i < 2 && committed.RunSnapshot.CurrentApproval != nil {
			t.Fatal("parallel batch was approved before all claimed items committed")
		}
		if i == 2 && (committed.RunSnapshot.Run.Status != "waiting_approval" || committed.Approval.SubjectKind != "artifact_version_set") {
			t.Fatalf("final parallel commit = %+v", committed)
		}
	}
}

func TestInstalledSequentialBatchSkillWaitsForPreviousItem(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	directory := filepath.Join(t.TempDir(), "episode-review-workflow")
	source := filepath.Join(testProjectRoot(t), "fixtures", "skills", "batched", "episode-review-workflow")
	if err := os.CopyFS(directory, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(directory, "content-agent", "workflow.json")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	var workflow capability.Manifest
	if err := json.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	workflow.Steps[0].Batch.Execution = "sequential"
	data, err = json.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflowPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	startInstalledBatchSkillFromDirectory(t, store, directory)
	first := claimManagedBatchTask(t, store)
	if next, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
		WorkerID: "second-batch-worker", ExecutorIDs: []string{"worker.structured_content"}, ProviderID: "openai", ModelID: "test-model", LeaseSeconds: 60,
	}); err != nil || next != nil {
		t.Fatalf("claimed past running predecessor: %+v, %v", next, err)
	}
	commitManagedBatchTask(t, store, first)
	second := claimManagedBatchTask(t, store)
	if first.Task.ItemKey != "episode:1" || second.Task.ItemKey != "episode:2" {
		t.Fatalf("sequential items = %s, %s", first.Task.ItemKey, second.Task.ItemKey)
	}
}

func TestInstalledBatchSkillRejectsWrongEpisodeBeforeWritingArtifact(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started, _, _ := startInstalledBatchSkill(t, store)
	claim := claimManagedBatchTask(t, store)
	received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
		AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash,
		ResponsePayload:   json.RawMessage(`{"episode_no":99,"title":"Wrong scope","content":"Not the claimed episode."}`),
	})
	if err != nil || received.ResponseHash == nil {
		t.Fatalf("receive = %+v, %v", received, err)
	}
	_, err = store.CommitExecutionResult(ctx, CommitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, ExpectedResponseHash: *received.ResponseHash})
	assertDomainCode(t, err, "BATCH_COVERAGE_INVALID")
	snapshot, err := store.GetRunSnapshot(ctx, started.Run.RunID)
	if err != nil || len(snapshot.Artifacts) != 0 || snapshot.CurrentApproval != nil {
		t.Fatalf("invalid scope wrote output: %+v, %v", snapshot, err)
	}
}

func TestBatchTargetEpisodeCountIsBounded(t *testing.T) {
	for _, value := range []string{"0", "-1", "10001", "999999999999999999999999", "1.5", "null", `"3"`} {
		_, err := targetEpisodeCount(json.RawMessage(`{"payload":{"target_episode_count":` + value + `}}`))
		assertDomainCode(t, err, "RUN_STATE_CONFLICT")
	}
	for _, value := range []int{1, MaxAssetSetEpisodeNo} {
		got, err := targetEpisodeCount(json.RawMessage(fmt.Sprintf(`{"payload":{"target_episode_count":%d}}`, value)))
		if err != nil || got != value {
			t.Fatalf("target %d = %d, %v", value, got, err)
		}
	}
}

func startInstalledBatchSkill(t *testing.T, store *Store) (RunSnapshot, string, string) {
	t.Helper()
	source := filepath.Join(testProjectRoot(t), "fixtures", "skills", "batched", "episode-review-workflow")
	return startInstalledBatchSkillFromDirectory(t, store, source)
}

func startInstalledBatchSkillFromDirectory(t *testing.T, store *Store, source string) (RunSnapshot, string, string) {
	t.Helper()
	ctx := context.Background()
	if installed, err := store.InstallSkillDirectory(ctx, source, "batch-test"); err != nil || !installed.Enabled {
		t.Fatalf("install = %+v, %v", installed, err)
	}
	project, err := store.CreateProject(ctx, "Batch Skill test")
	if err != nil {
		t.Fatal(err)
	}
	asset := createTextAsset(t, store, project.ProjectID, "episodes.txt", "Episode 1: a discovery. Episode 2: a betrayal. Episode 3: a reckoning.")
	ref := &agentcontract.CapabilityRef{CapabilityID: "episode_review_workflow", Version: "1.0.0", SelectionMode: "explicit"}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		CommandMeta:    CommandMeta{Scope: project.ProjectID, CommandType: "commit_agent_turn", IdempotencyKey: "batch-proposal", RequestHash: "batch-proposal-v1"},
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{Content: "Review all three episodes.", CapabilityRef: ref,
			AttachmentRefs: []agentcontract.AttachmentRef{{AssetID: asset.Asset.AssetID, AssetSnapshotID: asset.Snapshot.AssetSnapshotID}}},
		Decision: agentcontract.AgentDecision{Reply: "Configure the review.", Intent: "propose_capability", Confidence: 1, CapabilityRef: ref,
			ProposedAction: &agentcontract.ProposedActionDraft{ActionType: "collect_run_configuration", CapabilityRef: ref, Input: json.RawMessage(`{}`), Config: json.RawMessage(`{}`)}},
	})
	if err != nil || exchange.Action == nil || exchange.Invocation == nil {
		t.Fatalf("proposal = %+v, %v", exchange, err)
	}
	configured, err := store.ConfigureProposedAction(ctx, ConfigureProposedActionCommand{
		CommandMeta:      CommandMeta{Scope: project.ProjectID, CommandType: "configure_proposed_action", IdempotencyKey: "batch-config", RequestHash: "batch-config-v1"},
		ProposedActionID: exchange.Action.ProposedActionID, ExpectedVersion: exchange.Action.Version,
		Input: exchange.Action.Input, Config: json.RawMessage(`{"target_episode_count":3}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.StartRun(ctx, StartRunCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "start_run", IdempotencyKey: "batch-start", RequestHash: "batch-start-v1"},
		ProjectID:   project.ProjectID, ConversationID: project.PrimaryConversationID, CapabilityID: ref.CapabilityID, CapabilityVersion: ref.Version,
		RunKind: "generation", Input: configured.Input, Config: configured.Config, Confirmed: true,
		ProposedActionID: configured.ProposedActionID, ProposedActionVersion: configured.Version,
		ConfirmationMessageID: configured.ConfirmationMessageID, ConfirmationSnapshotHash: configured.SnapshotHash,
	})
	if err != nil || started.Run.Status != "running" || len(started.Steps) != 1 {
		t.Fatalf("start = %+v, %v", started, err)
	}
	return started, asset.Snapshot.AssetSnapshotID, exchange.Invocation.SkillInvocationID
}

func claimManagedBatchTask(t *testing.T, store *Store) *TaskClaim {
	t.Helper()
	claim, err := store.ClaimExecutionTask(context.Background(), ClaimExecutionTaskCommand{
		WorkerID: "sdk-batch-test", ExecutorIDs: []string{"worker.structured_content"}, ProviderID: "openai", ModelID: "test-model", LeaseSeconds: 60,
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %+v, %v", claim, err)
	}
	pack := claim.ContextPack
	if pack.ProviderResultContract == nil || string(pack.ProviderResultContract.Schema) != string(pack.OutputContract.Schema) ||
		!strings.Contains(string(pack.OutputContract.Schema), "episode_no") || len(pack.AssetContext) != 1 || pack.SkillInstructions == nil {
		t.Fatalf("direct batch context = %+v", pack)
	}
	return claim
}

func commitManagedBatchTask(t *testing.T, store *Store, claim *TaskClaim) ArtifactCommitResult {
	t.Helper()
	episodeNo, err := episodeNumberFromScope(claim.Task.ItemKey)
	if err != nil {
		t.Fatal(err)
	}
	received, err := store.SubmitExecutionResult(context.Background(), SubmitExecutionResultCommand{
		AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash,
		ResponsePayload: json.RawMessage(fmt.Sprintf(`{"episode_no":%d,"title":"Review","content":"Strengthen the closing hook."}`, episodeNo)),
	})
	if err != nil || received.ResponseHash == nil {
		t.Fatalf("submit = %+v, %v", received, err)
	}
	committed, err := store.CommitExecutionResult(context.Background(), CommitExecutionResultCommand{
		AttemptID: claim.Attempt.AttemptID, ExpectedResponseHash: *received.ResponseHash,
	})
	if err != nil || committed.Artifact.ScopeKey != claim.Task.ItemKey || committed.ArtifactVersion.Status != "pending_approval" {
		t.Fatalf("commit = %+v, %v", committed, err)
	}
	return committed
}
