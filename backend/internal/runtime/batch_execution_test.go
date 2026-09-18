package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

func TestAttemptLeaseBoundsCoverVideoWorkerTimeout(t *testing.T) {
	const videoWorkerLeaseSeconds = 1500
	if videoWorkerLeaseSeconds > maxAttemptLeaseSeconds {
		t.Fatalf("video worker lease %d exceeds runtime maximum %d", videoWorkerLeaseSeconds, maxAttemptLeaseSeconds)
	}
}

func TestBatchStepPlanningAndSequentialClaim(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, initial := prepareStoryBibleForBatch(
		t,
		store,
		"批量拆集",
		fixedNovelFixtureContent(t),
	)
	approval, err := store.pendingApprovalForRun(ctx, initial.Run.RunID)
	if err != nil || approval == nil {
		t.Fatalf("pending story approval = %+v, error = %v", approval, err)
	}
	approved, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       approval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: approval.Version,
		SubjectSnapshotHash:     approval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval(story) error = %v", err)
	}
	if approved.Run.CurrentStepRunID == nil {
		t.Fatal("approved run has no split step")
	}
	running, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: initial.Run.RunID})
	if err != nil || running.Run.Status != "running" {
		t.Fatalf("ResumeRun(split) = %+v, error = %v", running, err)
	}
	if running.Run.CurrentStepRunID == nil {
		t.Fatal("running split has no current step")
	}
	splitStepRunID := *running.Run.CurrentStepRunID
	tasks, err := store.ListTaskItems(ctx, splitStepRunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("split tasks = %+v, error = %v", tasks, err)
	}
	if tasks[0].ItemKey != "preparation:global_plan" ||
		tasks[0].ItemOrder != 1 ||
		tasks[0].Status != "pending" {
		t.Fatalf("global split task = %+v", tasks[0])
	}

	global, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
		WorkerID:     "split_worker_1",
		ExecutorIDs:  []string{"workflow.novel_episode_split"},
		ProviderID:   "provider_test",
		LeaseSeconds: 60,
	})
	if err != nil || global == nil ||
		global.Task.ItemKey != "preparation:global_plan" ||
		global.ContextPack.Intent.Operation != "generate_task_checkpoint" ||
		global.ContextPack.OutputContract.ArtifactType != "task_checkpoint:global_plan" {
		t.Fatalf("global split claim = %+v, error = %v", global, err)
	}
	second, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
		WorkerID:     "split_worker_2",
		ExecutorIDs:  []string{"workflow.novel_episode_split"},
		ProviderID:   "provider_test",
		LeaseSeconds: 60,
	})
	if err != nil {
		t.Fatalf("second split claim error = %v", err)
	}
	if second != nil {
		t.Fatalf("sequential split allowed out-of-order claim = %+v", second)
	}
	globalCommit := commitSplitGlobalPlan(t, store, global, 12)
	if globalCommit.CommitStatus != "task_checkpointed" ||
		globalCommit.TaskCheckpoint == nil ||
		globalCommit.RunSnapshot.Run.Status != "running" {
		t.Fatalf("global split commit = %+v", globalCommit)
	}
	tasks, err = store.ListTaskItems(ctx, splitStepRunID)
	if err != nil || len(tasks) != 2 ||
		tasks[0].Status != "succeeded" ||
		tasks[1].ItemKey != "episode:1-5" ||
		tasks[1].Status != "pending" {
		t.Fatalf("tasks after global plan = %+v, error = %v", tasks, err)
	}

	first, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
		WorkerID:     "split_worker_1",
		ExecutorIDs:  []string{"workflow.novel_episode_split"},
		ProviderID:   "provider_test",
		LeaseSeconds: 60,
	})
	if err != nil || first == nil || first.Task.ItemKey != "episode:1-5" {
		t.Fatalf("first split batch claim = %+v, error = %v", first, err)
	}
	assertPreviousBatchResultCount(t, first, 0)
	var cursor struct {
		Batch struct {
			BatchIndex         int `json:"batch_index"`
			BatchCount         int `json:"batch_count"`
			EpisodeStart       int `json:"episode_start"`
			EpisodeEnd         int `json:"episode_end"`
			TargetEpisodeCount int `json:"target_episode_count"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(first.ContextPack.TaskCursor, &cursor); err != nil {
		t.Fatalf("decode split cursor: %v", err)
	}
	if cursor.Batch.BatchIndex != 1 ||
		cursor.Batch.BatchCount != 3 ||
		cursor.Batch.EpisodeStart != 1 ||
		cursor.Batch.EpisodeEnd != 5 ||
		cursor.Batch.TargetEpisodeCount != 12 {
		t.Fatalf("split context cursor = %+v", cursor)
	}
	if project.ProjectID != first.ContextPack.ProjectID {
		t.Fatalf("claim project = %s, want %s", first.ContextPack.ProjectID, project.ProjectID)
	}

	firstCommit := commitSplitBatchResult(t, store, first)
	if firstCommit.CommitStatus != "task_checkpointed" ||
		firstCommit.TaskCheckpoint == nil ||
		firstCommit.RunSnapshot.Run.Status != "running" {
		t.Fatalf("first batch commit = %+v", firstCommit)
	}
	second, err = store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
		WorkerID:     "split_worker_2",
		ExecutorIDs:  []string{"workflow.novel_episode_split"},
		ProviderID:   "provider_test",
		LeaseSeconds: 60,
	})
	if err != nil || second == nil || second.Task.ItemKey != "episode:6-10" {
		t.Fatalf("second split claim after checkpoint = %+v, error = %v", second, err)
	}
	assertPreviousBatchResultCount(t, second, 1)
	secondCommit := commitSplitBatchResult(t, store, second)
	if secondCommit.CommitStatus != "task_checkpointed" ||
		secondCommit.TaskCheckpoint == nil {
		t.Fatalf("second batch commit = %+v", secondCommit)
	}
	third, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
		WorkerID:     "split_worker_3",
		ExecutorIDs:  []string{"workflow.novel_episode_split"},
		ProviderID:   "provider_test",
		LeaseSeconds: 60,
	})
	if err != nil || third == nil || third.Task.ItemKey != "episode:11-12" {
		t.Fatalf("third split claim = %+v, error = %v", third, err)
	}
	assertPreviousBatchResultCount(t, third, 2)
	finalCommit := commitSplitBatchResult(t, store, third)
	if finalCommit.CommitStatus != "waiting_approval" ||
		finalCommit.ArtifactVersion.Status != "pending_approval" ||
		finalCommit.RunSnapshot.Run.Status != "waiting_approval" {
		t.Fatalf("final batch commit = %+v", finalCommit)
	}
	var merged struct {
		TargetEpisodeCount int `json:"target_episode_count"`
		ActualEpisodeCount int `json:"actual_episode_count"`
		Episodes           []struct {
			EpisodeNo int `json:"episode_no"`
		} `json:"episodes"`
	}
	if err := json.Unmarshal(finalCommit.ArtifactVersion.Payload, &merged); err != nil {
		t.Fatalf("decode merged split: %v", err)
	}
	if merged.TargetEpisodeCount != 12 ||
		merged.ActualEpisodeCount != 12 ||
		len(merged.Episodes) != 12 {
		t.Fatalf("merged split = %+v", merged)
	}
	for index, episode := range merged.Episodes {
		if episode.EpisodeNo != index+1 {
			t.Fatalf("merged episode %d = %+v", index, episode)
		}
	}
	tasks, err = store.ListTaskItems(ctx, splitStepRunID)
	if err != nil {
		t.Fatalf("ListTaskItems(after merge) error = %v", err)
	}
	for _, task := range tasks {
		if task.Status != "succeeded" ||
			task.OutputArtifactVersionID == nil ||
			*task.OutputArtifactVersionID != finalCommit.ArtifactVersion.ArtifactVersionID {
			t.Fatalf("task after merge = %+v", task)
		}
	}

	splitApproval, err := store.pendingApprovalForRun(ctx, initial.Run.RunID)
	if err != nil || splitApproval == nil {
		t.Fatalf("pending split approval = %+v, error = %v", splitApproval, err)
	}
	approvedSplit, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       splitApproval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: splitApproval.Version,
		SubjectSnapshotHash:     splitApproval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval(split) error = %v", err)
	}
	if approvedSplit.Run.CurrentStepRunID == nil {
		t.Fatal("approved split run has no episode cards step")
	}
	cardsRunning, err := store.ResumeRun(ctx, ResumeRunCommand{
		RunID: initial.Run.RunID,
	})
	if err != nil || cardsRunning.Run.Status != "running" {
		t.Fatalf("ResumeRun(episode cards) = %+v, error = %v", cardsRunning, err)
	}
	cardsStepRunID := *cardsRunning.Run.CurrentStepRunID
	cardTasks, err := store.ListTaskItems(ctx, cardsStepRunID)
	if err != nil || len(cardTasks) != 3 {
		t.Fatalf("episode card tasks = %+v, error = %v", cardTasks, err)
	}
	expectedCardKeys := []string{"episode:1-5", "episode:6-10", "episode:11-12"}
	for index, task := range cardTasks {
		if task.ItemKey != expectedCardKeys[index] ||
			task.ItemOrder != index+1 ||
			task.Status != "pending" {
			t.Fatalf("episode card task %d = %+v", index, task)
		}
	}

	var cardsFinal ArtifactCommitResult
	for index, expectedKey := range expectedCardKeys {
		claim, claimErr := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
			WorkerID:     fmt.Sprintf("cards_worker_%d", index+1),
			ExecutorIDs:  []string{"workflow.episode_cards"},
			ProviderID:   "provider_test",
			LeaseSeconds: 60,
		})
		if claimErr != nil || claim == nil || claim.Task.ItemKey != expectedKey {
			t.Fatalf("episode card claim %d = %+v, error = %v", index, claim, claimErr)
		}
		assertPreviousBatchResultCount(t, claim, min(index, 1))
		assertStructuredRunStateFacts(t, claim, index, []string{
			"完成第1-5集分集卡。",
			"完成第6-10集分集卡。",
		})
		if index == 0 {
			assertStructuredRunStateIdempotent(t, store, claim)
		}
		if index == 2 {
			assertPreviousBatchResultKeys(t, claim, []string{"episode:6-10"})
		}
		committed := commitEpisodeCardsBatchResult(t, store, claim)
		if index < len(expectedCardKeys)-1 {
			if committed.CommitStatus != "task_checkpointed" ||
				committed.TaskCheckpoint == nil ||
				committed.RunSnapshot.Run.Status != "running" {
				t.Fatalf("episode card checkpoint %d = %+v", index, committed)
			}
			continue
		}
		cardsFinal = committed
	}
	if cardsFinal.CommitStatus != "waiting_approval" ||
		cardsFinal.ArtifactVersion.Status != "pending_approval" ||
		cardsFinal.RunSnapshot.Run.Status != "waiting_approval" {
		t.Fatalf("final episode cards commit = %+v", cardsFinal)
	}
	var mergedCards struct {
		Episodes []struct {
			EpisodeNo int `json:"episode_no"`
		} `json:"episodes"`
		ContinuityDelta struct {
			NewFacts []string `json:"new_facts"`
		} `json:"continuity_delta"`
	}
	if err := json.Unmarshal(cardsFinal.ArtifactVersion.Payload, &mergedCards); err != nil {
		t.Fatalf("decode merged episode cards: %v", err)
	}
	if len(mergedCards.Episodes) != 12 ||
		len(mergedCards.ContinuityDelta.NewFacts) != 3 {
		t.Fatalf("merged episode cards = %+v", mergedCards)
	}
	for index, episode := range mergedCards.Episodes {
		if episode.EpisodeNo != index+1 {
			t.Fatalf("merged episode card %d = %+v", index, episode)
		}
	}

	cardsApproval, err := store.pendingApprovalForRun(ctx, initial.Run.RunID)
	if err != nil || cardsApproval == nil {
		t.Fatalf("pending episode cards approval = %+v, error = %v", cardsApproval, err)
	}
	approvedCards, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       cardsApproval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: cardsApproval.Version,
		SubjectSnapshotHash:     cardsApproval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval(episode cards) error = %v", err)
	}
	if approvedCards.Run.CurrentStepRunID == nil {
		t.Fatal("approved episode cards run has no script units step")
	}
	scriptsRunning, err := store.ResumeRun(ctx, ResumeRunCommand{
		RunID: initial.Run.RunID,
	})
	if err != nil || scriptsRunning.Run.Status != "running" {
		t.Fatalf("ResumeRun(script units) = %+v, error = %v", scriptsRunning, err)
	}
	scriptStepRunID := *scriptsRunning.Run.CurrentStepRunID
	scriptTasks, err := store.ListTaskItems(ctx, scriptStepRunID)
	if err != nil || len(scriptTasks) != 12 {
		t.Fatalf("script unit tasks = %+v, error = %v", scriptTasks, err)
	}
	for index, task := range scriptTasks {
		expectedKey := fmt.Sprintf("episode:%d", index+1)
		if task.ItemKey != expectedKey ||
			task.ItemOrder != index+1 ||
			task.Status != "pending" {
			t.Fatalf("script unit task %d = %+v", index, task)
		}
	}

	var scriptFinal ArtifactCommitResult
	for episodeNo := 1; episodeNo <= 12; episodeNo++ {
		claim, claimErr := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
			WorkerID:     fmt.Sprintf("script_worker_%d", episodeNo),
			ExecutorIDs:  []string{"workflow.shared_script_generation"},
			ProviderID:   "provider_test",
			LeaseSeconds: 60,
		})
		if claimErr != nil || claim == nil ||
			claim.Task.ItemKey != fmt.Sprintf("episode:%d", episodeNo) {
			t.Fatalf("script unit claim %d = %+v, error = %v", episodeNo, claim, claimErr)
		}
		if episodeNo > 1 {
			assertScriptContinuityContext(t, claim, episodeNo)
		}
		if episodeNo == 2 {
			failed, failErr := store.FailExecutionAttempt(ctx, FailExecutionAttemptCommand{
				AttemptID:         claim.Attempt.AttemptID,
				AttemptToken:      claim.AttemptToken,
				InputSnapshotHash: claim.Attempt.InputSnapshotHash,
				ErrorCode:         "MODEL_RATE_LIMIT",
			})
			if failErr != nil || !failed.Retryable ||
				failed.Task.Status != "pending" {
				t.Fatalf("retryable script failure = %+v, error = %v", failed, failErr)
			}
			claim, claimErr = store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
				WorkerID:     "script_worker_2_retry",
				ExecutorIDs:  []string{"workflow.shared_script_generation"},
				ProviderID:   "provider_test",
				LeaseSeconds: 60,
			})
			if claimErr != nil || claim == nil ||
				claim.Task.ItemKey != "episode:2" ||
				claim.Attempt.AttemptNo != 2 {
				t.Fatalf("script unit retry claim = %+v, error = %v", claim, claimErr)
			}
		}
		committed := commitScriptUnitResult(t, store, claim, episodeNo)
		if episodeNo == 2 {
			updatedTasks, listErr := store.ListTaskItems(ctx, scriptStepRunID)
			if listErr != nil || updatedTasks[1].Status != "succeeded" || updatedTasks[1].Failure != nil {
				t.Fatalf("successful retry retained stale failure = %+v, error = %v", updatedTasks[1], listErr)
			}
		}
		if episodeNo < 12 {
			if committed.CommitStatus != "task_artifact_saved" ||
				committed.Artifact.ScopeKey != fmt.Sprintf("episode:%d", episodeNo) ||
				committed.ArtifactVersion.Status != "pending_approval" ||
				committed.RunSnapshot.Run.Status != "running" {
				t.Fatalf("script unit commit %d = %+v", episodeNo, committed)
			}
			continue
		}
		scriptFinal = committed
	}
	if scriptFinal.CommitStatus != "waiting_approval" ||
		scriptFinal.Approval.SubjectKind != "artifact_version_set" ||
		scriptFinal.Approval.SubjectRefID != scriptStepRunID ||
		scriptFinal.RunSnapshot.Run.Status != "waiting_approval" {
		t.Fatalf("final script unit commit = %+v", scriptFinal)
	}
	var approvalVersionCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM approval_subject_versions
		WHERE approval_request_id = ?`,
		scriptFinal.Approval.ApprovalRequestID,
	).Scan(&approvalVersionCount); err != nil || approvalVersionCount != 24 {
		t.Fatalf("script approval versions = %d, error = %v", approvalVersionCount, err)
	}
	artifacts, err := store.ListArtifactsByRun(ctx, initial.Run.RunID)
	if err != nil {
		t.Fatalf("ListArtifactsByRun(script units) error = %v", err)
	}
	scriptArtifactCount := 0
	handoffArtifactCount := 0
	contextArtifactCount := 0
	var episodeFiveArtifact Artifact
	for _, artifact := range artifacts {
		switch artifact.ArtifactType {
		case "script_unit":
			scriptArtifactCount++
			if artifact.ScopeKey == "episode:5" {
				episodeFiveArtifact = artifact
			}
		case "script_handoff":
			handoffArtifactCount++
		case "script_context":
			contextArtifactCount++
		}
	}
	if scriptArtifactCount != 12 ||
		handoffArtifactCount != 12 ||
		contextArtifactCount != 12 ||
		episodeFiveArtifact.ArtifactID == "" {
		t.Fatalf(
			"script artifacts = %d, handoffs = %d, contexts = %d, episode 5 = %+v",
			scriptArtifactCount,
			handoffArtifactCount,
			contextArtifactCount,
			episodeFiveArtifact,
		)
	}
	episodeFiveVersion, err := store.GetArtifactVersion(
		ctx,
		episodeFiveArtifact.CurrentVersionID,
	)
	if err != nil {
		t.Fatalf("GetArtifactVersion(episode 5) error = %v", err)
	}
	var episodeFivePayload map[string]any
	if err := json.Unmarshal(episodeFiveVersion.Payload, &episodeFivePayload); err != nil {
		t.Fatalf("decode episode 5 payload: %v", err)
	}
	for _, internalField := range []string{
		"continuity_delta",
		"used_generation_basis",
		"risk_notes",
		"self_check",
	} {
		if _, exists := episodeFivePayload[internalField]; exists {
			t.Fatalf("formal script contains internal field %q: %+v", internalField, episodeFivePayload)
		}
	}

	var describesCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM artifact_dependencies
		WHERE run_id = ? AND relation = 'describes'`,
		initial.Run.RunID,
	).Scan(&describesCount); err != nil || describesCount != 12 {
		t.Fatalf("script handoff describes dependencies = %d, error = %v", describesCount, err)
	}

	episodeFivePayload["title"] = "第5集（用户修改）"
	editedEpisodeFivePayload, err := json.Marshal(episodeFivePayload)
	if err != nil {
		t.Fatalf("encode edited episode 5 payload: %v", err)
	}
	editedEpisodeFive, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
		ArtifactID:    episodeFiveArtifact.ArtifactID,
		BaseVersionID: episodeFiveVersion.ArtifactVersionID,
		BaseVersion:   episodeFiveVersion.Version,
		ChangeMode:    "whole_artifact",
		NewPayload:    editedEpisodeFivePayload,
	})
	if err != nil {
		t.Fatalf("CreateArtifactVersion(episode 5) error = %v", err)
	}
	if !editedEpisodeFive.HandoffRefreshRequired ||
		!equalStrings(editedEpisodeFive.PendingRefreshVersionIDs, []string{editedEpisodeFive.ArtifactVersion.ArtifactVersionID}) ||
		len(editedEpisodeFive.PendingRefreshScopes) != 1 ||
		editedEpisodeFive.PendingRefreshScopes[0] != "episode:5" ||
		editedEpisodeFive.Approval.ApprovalRequestID != "" {
		t.Fatalf("edited episode 5 result = %+v", editedEpisodeFive)
	}
	_, err = store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       scriptFinal.Approval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: scriptFinal.Approval.Version,
		SubjectSnapshotHash:     scriptFinal.Approval.SubjectSnapshotHash,
	})
	assertDomainCode(t, err, "APPROVAL_ALREADY_RESOLVED")

	editCommand := CompleteScriptEditCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "complete_script_edit", IdempotencyKey: "complete-episode-five", RequestHash: "episode-five-v2"},
		RunID:       initial.Run.RunID,
		ExpectedScriptVersionIDs: []string{
			editedEpisodeFive.ArtifactVersion.ArtifactVersionID,
		},
	}
	assertScriptEditRejectsRevokedEditor(t, store, editCommand, episodeFiveArtifact.ArtifactID)
	assertScriptEditRecoverySnapshot(t, store, editCommand, []string{"episode:5"})
	editCompletion, err := store.CompleteScriptEdit(ctx, editCommand)
	if err != nil {
		t.Fatalf("CompleteScriptEdit() error = %v", err)
	}
	if editCompletion.RunSnapshot.Run.Status != "running" ||
		editCompletion.RunSnapshot.PendingScriptEdit != nil ||
		len(editCompletion.RefreshTaskIDs) != 1 ||
		len(editCompletion.PendingScopes) != 1 ||
		editCompletion.PendingScopes[0] != "episode:5" {
		t.Fatalf("script edit completion = %+v", editCompletion)
	}
	assertScriptEditRejectsRevokedEditor(t, store, editCommand, episodeFiveArtifact.ArtifactID)
	repeatedCompletion, err := store.CompleteScriptEdit(ctx, editCommand)
	if err != nil || !equalStrings(repeatedCompletion.RefreshTaskIDs, editCompletion.RefreshTaskIDs) {
		t.Fatalf("running handoff lost its completion receipt: %+v %v", repeatedCompletion, err)
	}
	wrongCompletion := editCommand
	wrongCompletion.ExpectedScriptVersionIDs = []string{episodeFiveVersion.ArtifactVersionID}
	_, err = store.CompleteScriptEdit(ctx, wrongCompletion)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	_, err = store.CreateArtifactVersion(ctx, CreateVersionCommand{
		ArtifactID:    episodeFiveArtifact.ArtifactID,
		BaseVersionID: editedEpisodeFive.ArtifactVersion.ArtifactVersionID,
		BaseVersion:   editedEpisodeFive.ArtifactVersion.Version,
		ChangeMode:    "whole_artifact",
		NewPayload:    editedEpisodeFivePayload,
	})
	assertDomainCode(t, err, "RUN_STATE_CONFLICT")
	refreshClaim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
		WorkerID:     "script_handoff_refresh_worker",
		ExecutorIDs:  []string{"workflow.shared_script_generation"},
		ProviderID:   "provider_test",
		LeaseSeconds: 60,
	})
	if err != nil || refreshClaim == nil ||
		refreshClaim.Task.ItemKey != "episode:5" ||
		refreshClaim.ContextPack.Intent.Operation != "refresh_script_handoff" ||
		len(refreshClaim.ContextPack.OutputContracts) != 1 ||
		refreshClaim.ContextPack.OutputContract.ArtifactType != "script_handoff" {
		t.Fatalf("handoff refresh claim = %+v, error = %v", refreshClaim, err)
	}
	refreshResponse := mustJSONNoTest(map[string]any{
		"script_handoff": validScriptHandoffPayload(5),
	})
	refreshReceived, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
		AttemptID:         refreshClaim.Attempt.AttemptID,
		AttemptToken:      refreshClaim.AttemptToken,
		InputSnapshotHash: refreshClaim.Attempt.InputSnapshotHash,
		ResponsePayload:   refreshResponse,
	})
	if err != nil || refreshReceived.ResponseHash == nil {
		t.Fatalf("SubmitExecutionResult(handoff refresh) = %+v, error = %v", refreshReceived, err)
	}
	refreshedHandoff, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{
		AttemptID:            refreshClaim.Attempt.AttemptID,
		ExpectedResponseHash: *refreshReceived.ResponseHash,
	})
	if err != nil {
		t.Fatalf("CommitExecutionResult(handoff refresh) error = %v", err)
	}
	if refreshedHandoff.CommitStatus != "waiting_approval" ||
		refreshedHandoff.Artifact.ArtifactType != "script_handoff" ||
		refreshedHandoff.ArtifactVersion.Version != 2 ||
		refreshedHandoff.ArtifactVersion.CreationReason != "regeneration" ||
		refreshedHandoff.Approval.SubjectKind != "artifact_version_set" {
		t.Fatalf("refreshed handoff result = %+v", refreshedHandoff)
	}
	var refreshedHandoffPayload struct {
		ScriptArtifactVersionID string `json:"script_artifact_version_id"`
	}
	if err := json.Unmarshal(
		refreshedHandoff.ArtifactVersion.Payload,
		&refreshedHandoffPayload,
	); err != nil ||
		refreshedHandoffPayload.ScriptArtifactVersionID !=
			editedEpisodeFive.ArtifactVersion.ArtifactVersionID {
		t.Fatalf(
			"refreshed handoff payload = %+v, error = %v",
			refreshedHandoffPayload,
			err,
		)
	}
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM approval_subject_versions
		WHERE approval_request_id = ?`,
		refreshedHandoff.Approval.ApprovalRequestID,
	).Scan(&approvalVersionCount); err != nil || approvalVersionCount != 24 {
		t.Fatalf("refreshed script approval versions = %d, error = %v", approvalVersionCount, err)
	}
	scriptFinal = refreshedHandoff

	approvedScripts, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       scriptFinal.Approval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: scriptFinal.Approval.Version,
		SubjectSnapshotHash:     scriptFinal.Approval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval(script version set) error = %v", err)
	}
	if approvedScripts.Run.Status != "running" ||
		approvedScripts.Run.CurrentStepRunID == nil {
		t.Fatalf("approved scripts run = %+v", approvedScripts.Run)
	}
	completed := completePassingQualityReview(t, store)
	if completed.Run.Status != "completed" || completed.Run.CurrentStepRunID == nil {
		t.Fatalf("quality review completion = %+v", completed.Run)
	}
	var aggregateStepID, aggregateInputJSON string
	if err := store.db.QueryRowContext(ctx, `
		SELECT step_id, input_version_snapshot_json
		FROM step_runs WHERE step_run_id = ?`,
		*completed.Run.CurrentStepRunID,
	).Scan(&aggregateStepID, &aggregateInputJSON); err != nil {
		t.Fatalf("load aggregate step: %v", err)
	}
	var aggregateInputs []map[string]any
	if err := json.Unmarshal([]byte(aggregateInputJSON), &aggregateInputs); err != nil {
		t.Fatalf("decode aggregate inputs: %v", err)
	}
	if aggregateStepID != "aggregate_scripts" || len(aggregateInputs) != 12 {
		t.Fatalf("aggregate step = %s, inputs = %+v", aggregateStepID, aggregateInputs)
	}
	candidatesBeforeEdit, err := store.ListScriptCandidates(ctx, project.ProjectID)
	if err != nil || len(candidatesBeforeEdit) != 1 {
		t.Fatalf("initial candidates: %+v %v", candidatesBeforeEdit, err)
	}
	finalBeforeEdit := selectCandidateVersionForTest(t, store, candidatesBeforeEdit[0])
	originalCandidate := finalBeforeEdit.Candidate
	originalExport := candidateVersionExportForTest(t, store, originalCandidate)
	originalUnits := candidateUnitVersionsForTest(t, store, originalCandidate)

	currentEpisodeFiveArtifact, err := store.GetArtifact(ctx, episodeFiveArtifact.ArtifactID)
	if err != nil {
		t.Fatalf("GetArtifact(completed episode 5) error = %v", err)
	}
	currentEpisodeFiveVersion, err := store.GetArtifactVersion(ctx, currentEpisodeFiveArtifact.CurrentVersionID)
	if err != nil {
		t.Fatalf("GetArtifactVersion(completed episode 5) error = %v", err)
	}
	var postCompletionPayload map[string]any
	if err := json.Unmarshal(currentEpisodeFiveVersion.Payload, &postCompletionPayload); err != nil {
		t.Fatalf("decode completed episode 5 payload: %v", err)
	}
	postCompletionPayload["title"] = "第5集（完成后局部修改）"
	postCompletionJSON, _ := json.Marshal(postCompletionPayload)
	postCompletionEdit, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
		ArtifactID:    episodeFiveArtifact.ArtifactID,
		BaseVersionID: currentEpisodeFiveVersion.ArtifactVersionID,
		BaseVersion:   currentEpisodeFiveVersion.Version,
		ChangeMode:    "full_payload",
		NewPayload:    postCompletionJSON,
	})
	if err != nil {
		t.Fatalf("CreateArtifactVersion(completed script) error = %v", err)
	}
	if !postCompletionEdit.HandoffRefreshRequired ||
		len(postCompletionEdit.PendingRefreshScopes) != 1 ||
		postCompletionEdit.PendingRefreshScopes[0] != "episode:5" {
		t.Fatalf("completed script edit = %+v", postCompletionEdit)
	}
	assertCandidateHistoryForTest(t, store, originalCandidate, originalExport, originalUnits)
	reopenedProject, err := store.GetProject(ctx, project.ProjectID)
	if err != nil || reopenedProject.ActiveWriteRunID == nil ||
		*reopenedProject.ActiveWriteRunID != initial.Run.RunID {
		t.Fatalf("reopened completed script project = %+v, error = %v", reopenedProject, err)
	}
	postCompletionRefresh, err := store.CompleteScriptEdit(ctx, CompleteScriptEditCommand{
		RunID: initial.Run.RunID,
		ExpectedScriptVersionIDs: []string{
			postCompletionEdit.ArtifactVersion.ArtifactVersionID,
		},
	})
	if err != nil || postCompletionRefresh.RunSnapshot.Run.Status != "running" {
		t.Fatalf("CompleteScriptEdit(completed script) = %+v, error = %v", postCompletionRefresh, err)
	}
	postCompletionClaim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
		WorkerID:     "post_completion_handoff_worker",
		ExecutorIDs:  []string{"workflow.shared_script_generation"},
		ProviderID:   "provider_test",
		LeaseSeconds: 60,
	})
	if err != nil || postCompletionClaim == nil ||
		postCompletionClaim.ContextPack.Intent.Operation != "refresh_script_handoff" {
		t.Fatalf("post-completion handoff claim = %+v, error = %v", postCompletionClaim, err)
	}
	postCompletionResponse := mustJSONNoTest(map[string]any{
		"script_handoff": validScriptHandoffPayload(5),
	})
	postCompletionReceived, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
		AttemptID:         postCompletionClaim.Attempt.AttemptID,
		AttemptToken:      postCompletionClaim.AttemptToken,
		InputSnapshotHash: postCompletionClaim.Attempt.InputSnapshotHash,
		ResponsePayload:   postCompletionResponse,
	})
	if err != nil || postCompletionReceived.ResponseHash == nil {
		t.Fatalf("SubmitExecutionResult(post-completion refresh) = %+v, error = %v", postCompletionReceived, err)
	}
	postCompletionHandoff, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{
		AttemptID:            postCompletionClaim.Attempt.AttemptID,
		ExpectedResponseHash: *postCompletionReceived.ResponseHash,
	})
	if err != nil || postCompletionHandoff.CommitStatus != "waiting_approval" {
		t.Fatalf("CommitExecutionResult(post-completion refresh) = %+v, error = %v", postCompletionHandoff, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM approval_subject_versions WHERE approval_request_id=?`,
		postCompletionHandoff.Approval.ApprovalRequestID).Scan(&approvalVersionCount); err != nil || approvalVersionCount != 2 {
		t.Fatalf("completed edit should approve only the changed script and handoff: %d %v", approvalVersionCount, err)
	}
	assertCandidateHistoryForTest(t, store, originalCandidate, originalExport, originalUnits)
	postCompletionApproved, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       postCompletionHandoff.Approval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: postCompletionHandoff.Approval.Version,
		SubjectSnapshotHash:     postCompletionHandoff.Approval.SubjectSnapshotHash,
	})
	if err != nil || postCompletionApproved.Run.CurrentStepRunID == nil {
		t.Fatalf("ResolveApproval(post-completion scripts) = %+v, error = %v", postCompletionApproved, err)
	}
	var postCompletionStepID string
	if err := store.db.QueryRowContext(ctx, `SELECT step_id FROM step_runs WHERE step_run_id = ?`,
		*postCompletionApproved.Run.CurrentStepRunID).Scan(&postCompletionStepID); err != nil ||
		postCompletionStepID != "review_script_set" || postCompletionApproved.Run.Status != "running" {
		t.Fatalf("post-completion next step = %q, error = %v", postCompletionStepID, err)
	}
	var scriptGenerationStepCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM step_runs WHERE run_id = ? AND step_id = 'generate_script_units'`,
		initial.Run.RunID).Scan(&scriptGenerationStepCount); err != nil || scriptGenerationStepCount != 1 {
		t.Fatalf("script generation step count = %d, error = %v", scriptGenerationStepCount, err)
	}
	postCompletionRun := completePassingQualityReview(t, store)
	if postCompletionRun.Run.Status != "completed" {
		t.Fatalf("post-completion quality review = %+v", postCompletionRun.Run)
	}
	var aggregateArtifactCount, aggregateVersion int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*), MAX(av.version)
		FROM artifacts a JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
		WHERE a.run_id = ? AND a.artifact_type = 'scripts' AND a.scope_key = 'singleton'`,
		initial.Run.RunID).Scan(&aggregateArtifactCount, &aggregateVersion); err != nil ||
		aggregateArtifactCount != 1 || aggregateVersion != 2 {
		t.Fatalf("post-completion aggregate artifact count = %d, version = %d, error = %v",
			aggregateArtifactCount, aggregateVersion, err)
	}
	assertCandidateHistoryForTest(t, store, originalCandidate, originalExport, originalUnits)
	candidatesAfterEdit, err := store.ListScriptCandidates(ctx, project.ProjectID)
	if err != nil || len(candidatesAfterEdit) != 2 {
		t.Fatalf("new aggregate must retain both candidates: %+v %v", candidatesAfterEdit, err)
	}
	var revisedCandidate ScriptCandidate
	for _, item := range candidatesAfterEdit {
		if item.CandidateID != originalCandidate.CandidateID {
			revisedCandidate = item
		}
	}
	if revisedCandidate.Status != "candidate" || revisedCandidate.SourceRunID != originalCandidate.SourceRunID ||
		revisedCandidate.ScriptsArtifactVersionID == originalCandidate.ScriptsArtifactVersionID ||
		revisedCandidate.SupersedesCandidateID == nil || *revisedCandidate.SupersedesCandidateID != originalCandidate.CandidateID {
		t.Fatalf("revised candidate is not a separate version: %+v", revisedCandidate)
	}
	revisedExport := candidateVersionExportForTest(t, store, revisedCandidate)
	if bytes.Equal(originalExport, revisedExport) || !bytes.Contains(revisedExport, []byte("第5集（完成后局部修改）")) {
		t.Fatal("new candidate did not export the revised text")
	}
	selectCandidateVersionForTest(t, store, revisedCandidate)
	if !bytes.Equal(originalExport, candidateVersionExportForTest(t, store, originalCandidate)) {
		t.Fatal("replacing the final changed historical content")
	}
	selectCandidateVersionForTest(t, store, originalCandidate)
	assertCandidateHistoryForTest(t, store, originalCandidate, originalExport, originalUnits)
}

func completePassingQualityReview(t *testing.T, store *Store) RunSnapshot {
	t.Helper()
	for claimIndex := 0; claimIndex < 100; claimIndex++ {
		claim := claimTaskForExecutor(
			t,
			store,
			"workflow.shared_script_quality_review",
		)
		cursor, err := parseQualityReviewCursor(claim.ContextPack.TaskCursor)
		if err != nil {
			t.Fatalf("parse quality review cursor: %v", err)
		}
		if cursor.Review.Phase == "episode_batch" {
			commitNonNovelResponse(
				t,
				store,
				claim,
				validQualityReviewBatchResponse(t, claim),
			)
			continue
		}
		if cursor.Review.Phase != "global" {
			t.Fatalf("quality review phase = %q", cursor.Review.Phase)
		}
		return commitNonNovelResponse(
			t,
			store,
			claim,
			validQualityReviewPassedResponse(t, claim),
		).RunSnapshot
	}
	t.Fatal("quality review did not reach the global phase")
	return RunSnapshot{}
}

func assertScriptContinuityContext(t *testing.T, claim *TaskClaim, episodeNo int) {
	t.Helper()
	handoffs := 0
	scripts := 0
	previousHandoff := false
	previousScript := false
	for _, item := range claim.ContextPack.UpstreamContext {
		if item.SelectionPolicy != "state_adjacent" {
			continue
		}
		switch item.ArtifactType {
		case "script_handoff":
			handoffs++
			previousHandoff = previousHandoff || item.ScopeKey == fmt.Sprintf("episode:%d", episodeNo-1)
		case "script_unit":
			scripts++
			previousScript = previousScript || item.ScopeKey == fmt.Sprintf("episode:%d", episodeNo-1)
		}
	}
	expected := 0
	if episodeNo > 1 {
		expected = 1
	}
	if handoffs != expected || scripts != expected || !previousHandoff || !previousScript {
		t.Fatalf(
			"episode %d continuity context: handoffs=%d scripts=%d previous_handoff=%v previous_script=%v upstream=%+v",
			episodeNo, handoffs, scripts, previousHandoff, previousScript, claim.ContextPack.UpstreamContext,
		)
	}
}

func TestVolumeFitInsufficientRequiresTransitionApproval(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	_, initial, _, _ := prepareStoryBibleForImpact(t, store, "体量不足")
	storyApproval, err := store.pendingApprovalForRun(ctx, initial.Run.RunID)
	if err != nil || storyApproval == nil {
		t.Fatalf("pending story approval = %+v, error = %v", storyApproval, err)
	}
	approvedStory, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       storyApproval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: storyApproval.Version,
		SubjectSnapshotHash:     storyApproval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval(story) error = %v", err)
	}
	waiting, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: initial.Run.RunID})
	if err != nil {
		t.Fatalf("ResumeRun(volume fit) error = %v", err)
	}
	if waiting.Run.Status != "waiting_approval" ||
		waiting.CurrentApproval == nil ||
		waiting.CurrentApproval.SubjectKind != "transition" {
		t.Fatalf("volume fit waiting snapshot = %+v", waiting)
	}
	continued, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       waiting.CurrentApproval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: waiting.CurrentApproval.Version,
		SubjectSnapshotHash:     waiting.CurrentApproval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval(volume fit) error = %v", err)
	}
	if continued.Run.Status != "paused" ||
		continued.Run.CurrentStepRunID == nil ||
		*continued.Run.CurrentStepRunID == *approvedStory.Run.CurrentStepRunID {
		t.Fatalf("run after volume fit approval = %+v", continued.Run)
	}
	var current StepRun
	for _, step := range continued.Steps {
		if step.StepRunID == *continued.Run.CurrentStepRunID {
			current = step
			break
		}
	}
	if current.StepID != "split_episodes" || current.Status != "pending" {
		t.Fatalf("step after volume fit approval = %+v", current)
	}
}

func TestEpisodeSplitInternalStagesRejectInvalidModelChoices(t *testing.T) {
	t.Run("global plan must use increasing source units", func(t *testing.T) {
		cursor := mustJSONNoTest(map[string]any{
			"batch": map[string]any{
				"target_episode_count": 2,
				"source_units": []any{
					map[string]any{"unit_id": "U000001"},
					map[string]any{"unit_id": "U000002"},
				},
			},
		})
		result := mustJSONNoTest(map[string]any{
			"episode_skeletons": []any{
				map[string]any{"episode_id": 1, "approx_end_unit_id": "U000002"},
				map[string]any{"episode_id": 2, "approx_end_unit_id": "U000002"},
			},
			"global_risks": []string{},
		})
		_, _, _, err := validateEpisodeSplitGlobalPlan(cursor, result)
		assertDomainCode(t, err, "BATCH_COVERAGE_INVALID")
	})

	t.Run("batch must select a runtime candidate", func(t *testing.T) {
		cursor := mustJSONNoTest(map[string]any{
			"batch": map[string]any{
				"episode_start":           1,
				"episode_end":             1,
				"target_episode_count":    1,
				"previous_end_unit_order": 0,
				"global_plan": map[string]any{
					"global_risks": []string{},
				},
				"source_units": []any{
					map[string]any{
						"unit_id":           "U000001",
						"asset_id":          "ast_1",
						"asset_snapshot_id": "ass_1",
					},
				},
				"candidates": []any{
					map[string]any{
						"candidate_id": "E001-U000001",
						"episode_id":   1,
						"end_unit_id":  "U000001",
						"unit_order":   1,
					},
				},
			},
		})
		result := mustJSONNoTest(map[string]any{
			"episodes": []any{
				map[string]any{
					"episode_id":              1,
					"end_candidate_id":        "invented-candidate",
					"source_summary":          "",
					"core_event":              "",
					"character_turn":          "",
					"boundary_reason":         "",
					"hook_strength":           "high",
					"hook_type":               "conflict",
					"information_density":     "medium",
					"pacing_risk":             "none",
					"split_confidence":        "high",
					"requires_user_attention": false,
				},
			},
			"batch_risks": []string{},
		})
		_, err := normalizeInternalBatchResult("episode_split", cursor, result)
		assertDomainCode(t, err, "BATCH_COVERAGE_INVALID")
	})
}

func prepareStoryBibleForBatch(
	t *testing.T,
	store *Store,
	title string,
	content string,
) (Project, RunSnapshot) {
	t.Helper()
	ctx := context.Background()
	project, _, initial := startNovelRunWithContent(t, store, title, content)
	_ = approveAndResumeLifecycleRun(t, store, project, initial)
	claim := claimStructuredTask(t, store)
	received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
		AttemptID:         claim.Attempt.AttemptID,
		AttemptToken:      claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash,
		ResponsePayload:   validStoryBibleProviderResponseForClaim(t, claim),
	})
	if err != nil || received.ResponseHash == nil {
		t.Fatalf("SubmitExecutionResult(story) = %+v, error = %v", received, err)
	}
	if _, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{
		AttemptID:            claim.Attempt.AttemptID,
		ExpectedResponseHash: *received.ResponseHash,
	}); err != nil {
		t.Fatalf("CommitExecutionResult(story) error = %v", err)
	}
	return project, initial
}

func validStoryBibleProviderResponseForClaim(
	t *testing.T,
	claim *TaskClaim,
) json.RawMessage {
	t.Helper()
	var cursor struct {
		Batch struct {
			SourceAnalysis struct {
				Units []struct {
					SourceUnitID string `json:"source_unit_id"`
					Summary      string `json:"summary"`
				} `json:"units"`
			} `json:"source_analysis"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(claim.ContextPack.TaskCursor, &cursor); err != nil ||
		len(cursor.Batch.SourceAnalysis.Units) == 0 {
		t.Fatalf("story aggregate cursor = %s, error = %v", claim.ContextPack.TaskCursor, err)
	}
	var response map[string]any
	if err := json.Unmarshal(validStoryBibleProviderResponse(), &response); err != nil {
		t.Fatalf("decode story response fixture: %v", err)
	}
	structure := make([]map[string]any, 0, len(cursor.Batch.SourceAnalysis.Units))
	for _, unit := range cursor.Batch.SourceAnalysis.Units {
		structure = append(structure, map[string]any{
			"source_unit_id":             unit.SourceUnitID,
			"source_range":               unit.SourceUnitID,
			"summary":                    unit.Summary,
			"key_events":                 []string{"来源单元事件"},
			"character_changes":          []string{},
			"conflict_stage":             "发展",
			"hook_or_suspense_potential": "medium",
			"source_evidence":            []any{},
		})
	}
	response["story_bible"].(map[string]any)["source_structure"] = structure
	return mustJSONNoTest(response)
}

func commitSplitBatchResult(
	t *testing.T,
	store *Store,
	claim *TaskClaim,
) ArtifactCommitResult {
	t.Helper()
	ctx := context.Background()
	response, start, end := validEpisodeSplitBatchProviderResponse(t, claim)
	received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
		AttemptID:         claim.Attempt.AttemptID,
		AttemptToken:      claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash,
		ResponsePayload:   response,
	})
	if err != nil || received.ResponseHash == nil {
		t.Fatalf("SubmitExecutionResult(batch %d-%d) = %+v, error = %v", start, end, received, err)
	}
	committed, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{
		AttemptID:            claim.Attempt.AttemptID,
		ExpectedResponseHash: *received.ResponseHash,
	})
	if err != nil {
		t.Fatalf("CommitExecutionResult(batch %d-%d) error = %v", start, end, err)
	}
	return committed
}

func commitSplitGlobalPlan(
	t *testing.T,
	store *Store,
	claim *TaskClaim,
	target int,
) ArtifactCommitResult {
	t.Helper()
	var cursor struct {
		Batch struct {
			SourceUnits []episodeSplitSourceUnit `json:"source_units"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(claim.ContextPack.TaskCursor, &cursor); err != nil {
		t.Fatalf("decode global split cursor: %v", err)
	}
	if len(cursor.Batch.SourceUnits) < target {
		t.Fatalf("source units = %d, want at least %d", len(cursor.Batch.SourceUnits), target)
	}
	skeletons := make([]map[string]any, 0, target)
	for episodeNo := 1; episodeNo <= target; episodeNo++ {
		order := episodeNo * len(cursor.Batch.SourceUnits) / target
		if episodeNo == target {
			order = len(cursor.Batch.SourceUnits)
		}
		skeletons = append(skeletons, map[string]any{
			"episode_id":         episodeNo,
			"approx_end_unit_id": cursor.Batch.SourceUnits[order-1].UnitID,
			"core_event":         fmt.Sprintf("第%d集核心事件。", episodeNo),
			"character_turn":     "主角推进调查。",
			"desired_hook":       "留下悬念。",
		})
	}
	response := mustJSONNoTest(map[string]any{
		"episode_skeletons": skeletons,
		"global_risks":      []string{},
	})
	received, err := store.SubmitExecutionResult(context.Background(), SubmitExecutionResultCommand{
		AttemptID:         claim.Attempt.AttemptID,
		AttemptToken:      claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash,
		ResponsePayload:   response,
	})
	if err != nil || received.ResponseHash == nil {
		t.Fatalf("SubmitExecutionResult(global) = %+v, error = %v", received, err)
	}
	committed, err := store.CommitExecutionResult(context.Background(), CommitExecutionResultCommand{
		AttemptID:            claim.Attempt.AttemptID,
		ExpectedResponseHash: *received.ResponseHash,
	})
	if err != nil {
		t.Fatalf("CommitExecutionResult(global) error = %v", err)
	}
	return committed
}

func commitEpisodeCardsBatchResult(
	t *testing.T,
	store *Store,
	claim *TaskClaim,
) ArtifactCommitResult {
	t.Helper()
	response, start, end := validEpisodeCardsBatchProviderResponse(t, claim)
	received, err := store.SubmitExecutionResult(context.Background(), SubmitExecutionResultCommand{
		AttemptID:         claim.Attempt.AttemptID,
		AttemptToken:      claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash,
		ResponsePayload:   response,
	})
	if err != nil || received.ResponseHash == nil {
		t.Fatalf("SubmitExecutionResult(cards %d-%d) = %+v, error = %v", start, end, received, err)
	}
	committed, err := store.CommitExecutionResult(context.Background(), CommitExecutionResultCommand{
		AttemptID:            claim.Attempt.AttemptID,
		ExpectedResponseHash: *received.ResponseHash,
	})
	if err != nil {
		t.Fatalf("CommitExecutionResult(cards %d-%d) error = %v", start, end, err)
	}
	return committed
}

func validEpisodeCardsBatchProviderResponse(
	t *testing.T,
	claim *TaskClaim,
) (json.RawMessage, int, int) {
	t.Helper()
	var cursor struct {
		Batch struct {
			EpisodeStart int `json:"episode_start"`
			EpisodeEnd   int `json:"episode_end"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(claim.ContextPack.TaskCursor, &cursor); err != nil {
		t.Fatalf("decode episode cards cursor: %v", err)
	}
	episodes := make(
		[]map[string]any,
		0,
		cursor.Batch.EpisodeEnd-cursor.Batch.EpisodeStart+1,
	)
	for episodeNo := cursor.Batch.EpisodeStart; episodeNo <= cursor.Batch.EpisodeEnd; episodeNo++ {
		episodes = append(episodes, map[string]any{
			"episode_no":         episodeNo,
			"source_refs":        []any{},
			"source_summary":     fmt.Sprintf("第%d集来源摘要。", episodeNo),
			"episode_function":   "推进主线冲突。",
			"opening_state":      "主角继续调查。",
			"main_conflict":      "主角与幕后势力正面交锋。",
			"payoff_or_reversal": "主角取得阶段性线索。",
			"character_turn":     "主角立场更加坚定。",
			"ending_hook": map[string]any{
				"hook_text":     "新的幕后人物现身。",
				"hook_strength": "high",
				"hook_source":   "本集结尾。",
			},
			"card_point_function":           "制造追看动机。",
			"pacing_plan":                   map[string]any{"tempo": "fast"},
			"visual_strategy":               map[string]any{"focus": "conflict"},
			"must_keep_facts":               []string{},
			"must_keep_dialogue_or_moments": []string{},
			"scene_outline":                 []any{map[string]any{"scene": "冲突现场"}},
			"adaptation_suggestions":        []string{},
			"risk_notes":                    []string{},
		})
	}
	return mustJSONNoTest(map[string]any{
		"episode_cards": map[string]any{
			"episodes": episodes,
			"continuity_delta": map[string]any{
				"new_facts":               []string{fmt.Sprintf("完成第%d-%d集分集卡。", cursor.Batch.EpisodeStart, cursor.Batch.EpisodeEnd)},
				"character_state_changes": []string{},
				"relationship_changes":    []string{},
				"hooks_opened":            []string{},
				"hooks_resolved":          []string{},
			},
		},
	}), cursor.Batch.EpisodeStart, cursor.Batch.EpisodeEnd
}

func commitScriptUnitResult(
	t *testing.T,
	store *Store,
	claim *TaskClaim,
	episodeNo int,
) ArtifactCommitResult {
	t.Helper()
	response := validScriptUnitProviderResponse(episodeNo)
	contextVersionIDs := []string{}
	previousScope := fmt.Sprintf("episode:%d", episodeNo-1)
	for _, item := range claim.ContextPack.UpstreamContext {
		if item.SelectionPolicy == "sdk_context_candidate" && item.ScopeKey == previousScope {
			contextVersionIDs = append(contextVersionIDs, item.ArtifactVersionID)
		}
	}
	received, err := store.SubmitExecutionResult(context.Background(), SubmitExecutionResultCommand{
		AttemptID:         claim.Attempt.AttemptID,
		AttemptToken:      claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash,
		ResponsePayload:   response,
		Usage: mustJSONNoTest(map[string]any{
			"context_artifact_version_ids": contextVersionIDs,
		}),
	})
	if err != nil || received.ResponseHash == nil {
		t.Fatalf("SubmitExecutionResult(script %d) = %+v, error = %v", episodeNo, received, err)
	}
	committed, err := store.CommitExecutionResult(context.Background(), CommitExecutionResultCommand{
		AttemptID:            claim.Attempt.AttemptID,
		ExpectedResponseHash: *received.ResponseHash,
	})
	if err != nil {
		t.Fatalf("CommitExecutionResult(script %d) error = %v", episodeNo, err)
	}
	return committed
}

func validScriptUnitProviderResponse(episodeNo int) json.RawMessage {
	return mustJSONNoTest(map[string]any{
		"script_unit":    validScriptUnitPayload(episodeNo),
		"script_handoff": validScriptHandoffPayload(episodeNo),
	})
}

func validScriptHandoffPayload(episodeNo int) map[string]any {
	return map[string]any{
		"episode_no":                 episodeNo,
		"script_artifact_version_id": "",
		"source_refs":                []any{},
		"continuity_delta": map[string]any{
			"new_facts":               []string{},
			"character_state_changes": []string{},
			"relationship_changes":    []string{},
			"hooks_opened":            []string{},
			"hooks_resolved":          []string{},
		},
		"runtime_check":                     map[string]any{},
		"critical_presentation_constraints": []string{},
		"review_focus":                      []string{},
		"next_episode_must_address":         nil,
		"self_check": map[string]any{
			"format_risks":          []string{},
			"continuity_risks":      []string{},
			"source_fidelity_risks": []string{},
		},
	}
}

func validScriptUnitPayload(episodeNo int) map[string]any {
	return map[string]any{
		"episode_no": episodeNo,
		"title":      fmt.Sprintf("第%d集", episodeNo),
		"script_text": fmt.Sprintf(
			"%d-1\n地点：旧宅 | 内 | 夜\n△主角推门进入旧宅。",
			episodeNo,
		),
		"scenes": []any{
			map[string]any{
				"scene_id":          fmt.Sprintf("%d-1", episodeNo),
				"heading":           "旧宅 内 夜",
				"location":          "旧宅",
				"interior_exterior": "内",
				"time_of_day":       "夜",
				"characters":        []string{"主角"},
				"blocks": []any{
					map[string]any{
						"line_id":     fmt.Sprintf("E%03d-L001", episodeNo),
						"block_type":  "action",
						"text":        "主角推门进入旧宅。",
						"source_refs": []any{},
						"uncertainty": "none",
					},
				},
				"source_refs": []any{},
			},
		},
		"source_refs": []any{},
		"continuity_delta": map[string]any{
			"new_facts":               []string{},
			"character_state_changes": []string{},
			"relationship_changes":    []string{},
			"hooks_opened":            []string{},
			"hooks_resolved":          []string{},
		},
		"used_generation_basis": []string{"episode_cards"},
		"risk_notes":            []string{},
		"self_check": map[string]any{
			"format_risks":          []string{},
			"continuity_risks":      []string{},
			"source_fidelity_risks": []string{},
		},
	}
}

func TestEnsureMinimumEpisodeSplitSourceUnits(t *testing.T) {
	units := []episodeSplitSourceUnit{
		{UnitID: "U000001", SourceUnitID: "SRC-1", StartOffset: 0, EndOffset: 12, Text: "甲发现线索。乙开始追查。"},
		{UnitID: "U000002", SourceUnitID: "SRC-2", StartOffset: 12, EndOffset: 18, Text: "真相揭晓。"},
	}

	got := ensureMinimumEpisodeSplitSourceUnits(units, 3)
	if len(got) != 3 {
		t.Fatalf("unit count = %d, want 3", len(got))
	}
	for index, unit := range got {
		if unit.UnitID != unitIDForOrder(index+1) {
			t.Fatalf("unit %d id = %q", index, unit.UnitID)
		}
		if unit.Text == "" || unit.EndOffset <= unit.StartOffset {
			t.Fatalf("unit %d is empty or has invalid offsets: %+v", index, unit)
		}
	}
	if got[0].SourceUnitID != got[1].SourceUnitID {
		t.Fatalf("split units lost their authoritative source id: %+v", got)
	}
	if got[0].EndOffset != got[1].StartOffset {
		t.Fatalf("split offsets are not contiguous: %+v", got)
	}
}

func assertPreviousBatchResultCount(t *testing.T, claim *TaskClaim, want int) {
	t.Helper()
	var cursor struct {
		Batch struct {
			PreviousBatchResults []struct {
				ItemKey      string          `json:"item_key"`
				ArtifactType string          `json:"artifact_type"`
				Payload      json.RawMessage `json:"payload"`
			} `json:"previous_batch_results"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(claim.ContextPack.TaskCursor, &cursor); err != nil {
		t.Fatalf("decode previous batch results: %v", err)
	}
	if len(cursor.Batch.PreviousBatchResults) != want {
		t.Fatalf(
			"%s previous batch result count = %d, want %d: %s",
			claim.Task.ItemKey,
			len(cursor.Batch.PreviousBatchResults),
			want,
			claim.ContextPack.TaskCursor,
		)
	}
	provenanceCount := 0
	for _, item := range claim.ContextPack.Provenance {
		if item.Kind == "task_result_checkpoint" {
			provenanceCount++
		}
	}
	if provenanceCount != want {
		t.Fatalf("%s checkpoint provenance count = %d, want %d", claim.Task.ItemKey, provenanceCount, want)
	}
	for _, previous := range cursor.Batch.PreviousBatchResults {
		if previous.ItemKey == "" || previous.ArtifactType == "" || len(previous.Payload) == 0 {
			t.Fatalf("%s contains incomplete previous batch result: %+v", claim.Task.ItemKey, previous)
		}
	}
}

func assertPreviousBatchResultKeys(t *testing.T, claim *TaskClaim, want []string) {
	t.Helper()
	var cursor struct {
		Batch struct {
			PreviousBatchResults []struct {
				ItemKey string `json:"item_key"`
			} `json:"previous_batch_results"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(claim.ContextPack.TaskCursor, &cursor); err != nil {
		t.Fatalf("decode previous batch result keys: %v", err)
	}
	if len(cursor.Batch.PreviousBatchResults) != len(want) {
		t.Fatalf("previous batch result keys = %+v, want %v", cursor.Batch.PreviousBatchResults, want)
	}
	for index, key := range want {
		if cursor.Batch.PreviousBatchResults[index].ItemKey != key {
			t.Fatalf("previous batch result key %d = %q, want %q", index, cursor.Batch.PreviousBatchResults[index].ItemKey, key)
		}
	}
}

func assertStructuredRunStateFacts(
	t *testing.T,
	claim *TaskClaim,
	wantCount int,
	allFacts []string,
) {
	t.Helper()
	if claim.ContextPack.StructuredRunState == nil {
		t.Fatalf("%s has no structured run state", claim.Task.ItemKey)
	}
	var snapshot struct {
		StateVersion int `json:"state_version"`
		State        struct {
			Facts []string `json:"facts"`
		} `json:"state"`
		SourceRefs []struct {
			Kind string `json:"kind"`
			Ref  string `json:"ref"`
		} `json:"source_refs"`
	}
	if err := json.Unmarshal(claim.ContextPack.StructuredRunState.Content, &snapshot); err != nil {
		t.Fatalf("decode structured run state: %v", err)
	}
	if snapshot.StateVersion != wantCount+1 ||
		claim.ContextPack.StructuredRunState.Version != wantCount+1 {
		t.Fatalf(
			"%s state version = payload:%d ref:%d, want %d",
			claim.Task.ItemKey,
			snapshot.StateVersion,
			claim.ContextPack.StructuredRunState.Version,
			wantCount+1,
		)
	}
	if len(snapshot.State.Facts) != wantCount || len(snapshot.SourceRefs) != wantCount {
		t.Fatalf(
			"%s state facts=%v refs=%v, want %d accumulated sources",
			claim.Task.ItemKey,
			snapshot.State.Facts,
			snapshot.SourceRefs,
			wantCount,
		)
	}
	for index := 0; index < wantCount; index++ {
		if snapshot.State.Facts[index] != allFacts[index] ||
			snapshot.SourceRefs[index].Kind != "task_result_checkpoint" {
			t.Fatalf("%s state source %d = facts:%v refs:%v", claim.Task.ItemKey, index, snapshot.State.Facts, snapshot.SourceRefs)
		}
	}
}

func assertStructuredRunStateIdempotent(t *testing.T, store *Store, claim *TaskClaim) {
	t.Helper()
	state := claim.ContextPack.StructuredRunState
	if state == nil {
		t.Fatal("cannot verify state idempotency without a state snapshot")
	}
	var before int
	if err := store.db.QueryRow(`
		SELECT COUNT(*) FROM artifact_versions WHERE artifact_id = ?`,
		state.ArtifactID,
	).Scan(&before); err != nil {
		t.Fatalf("count state versions before idempotency check: %v", err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin state idempotency transaction: %v", err)
	}
	reused, err := store.persistStructuredRunStateTx(
		context.Background(),
		tx,
		stepContextBuildRequest{
			ProjectID: claim.ContextPack.ProjectID, RunID: claim.Task.RunID,
			StepRunID: claim.Task.StepRunID, CapabilityID: claim.CapabilityID,
		},
		state.SchemaRef,
		state.Content,
		claim.ContextPack.CreatedAt,
	)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("repeat structured state persistence: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit state idempotency transaction: %v", err)
	}
	if reused.ArtifactVersionID != state.ArtifactVersionID || reused.Version != state.Version {
		t.Fatalf("state persistence was not idempotent: first=%+v repeated=%+v", state, reused)
	}
	var after int
	if err := store.db.QueryRow(`
		SELECT COUNT(*) FROM artifact_versions WHERE artifact_id = ?`,
		state.ArtifactID,
	).Scan(&after); err != nil {
		t.Fatalf("count state versions after idempotency check: %v", err)
	}
	if after != before {
		t.Fatalf("state version count changed on identical replay: before=%d after=%d", before, after)
	}
}

func validEpisodeSplitBatchProviderResponse(
	t *testing.T,
	claim *TaskClaim,
) (json.RawMessage, int, int) {
	t.Helper()
	var cursor struct {
		Batch struct {
			EpisodeStart     int                             `json:"episode_start"`
			EpisodeEnd       int                             `json:"episode_end"`
			PreviousEndOrder int                             `json:"previous_end_unit_order"`
			Candidates       []episodeSplitBoundaryCandidate `json:"candidates"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(claim.ContextPack.TaskCursor, &cursor); err != nil {
		t.Fatalf("decode batch cursor: %v", err)
	}
	episodes := make(
		[]map[string]any,
		0,
		cursor.Batch.EpisodeEnd-cursor.Batch.EpisodeStart+1,
	)
	previousOrder := cursor.Batch.PreviousEndOrder
	for episodeNo := cursor.Batch.EpisodeStart; episodeNo <= cursor.Batch.EpisodeEnd; episodeNo++ {
		var selected episodeSplitBoundaryCandidate
		for _, candidate := range cursor.Batch.Candidates {
			if candidate.EpisodeID == episodeNo && candidate.UnitOrder > previousOrder {
				selected = candidate
				break
			}
		}
		if selected.CandidateID == "" {
			t.Fatalf("no valid candidate for episode %d", episodeNo)
		}
		previousOrder = selected.UnitOrder
		episodes = append(episodes, map[string]any{
			"episode_id":              episodeNo,
			"end_candidate_id":        selected.CandidateID,
			"source_summary":          fmt.Sprintf("第%d集来源内容。", episodeNo),
			"core_event":              fmt.Sprintf("第%d集核心事件。", episodeNo),
			"character_turn":          "主角推进调查。",
			"boundary_reason":         "当前事件形成完整单元。",
			"hook_strength":           "high",
			"hook_type":               "悬念",
			"information_density":     "medium",
			"pacing_risk":             "low",
			"split_confidence":        "high",
			"requires_user_attention": false,
		})
	}
	return mustJSONNoTest(map[string]any{
		"episodes":    episodes,
		"batch_risks": []string{},
	}), cursor.Batch.EpisodeStart, cursor.Batch.EpisodeEnd
}
