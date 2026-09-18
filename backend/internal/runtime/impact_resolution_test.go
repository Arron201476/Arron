package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
)

func TestImpactReviewResolution(t *testing.T) {
	t.Run("snapshot conflict changes nothing", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		_, _, storyVersion, edited := createSingleImpactReview(t, store, "快照冲突")
		_, err = store.ResolveImpactReview(ctx, ResolveImpactReviewCommand{
			ImpactReviewID: edited.ImpactReview.ImpactReviewID,
			Action:         "keep_downstream",
			SnapshotHash:   "wrong_snapshot_hash",
		})
		assertDomainCode(t, err, "IMPACT_REVIEW_CONFLICT")
		sourceAfter, err := store.GetArtifactVersion(ctx, edited.ArtifactVersion.ArtifactVersionID)
		if err != nil || sourceAfter.Status != "pending_approval" {
			t.Fatalf("source after conflict = %+v, error = %v", sourceAfter, err)
		}
		storyAfter, err := store.GetArtifactVersion(ctx, storyVersion.ArtifactVersionID)
		if err != nil || storyAfter.Status != "pending_approval" {
			t.Fatalf("story after conflict = %+v, error = %v", storyAfter, err)
		}
		reviewAfter, err := store.GetImpactReview(ctx, edited.ImpactReview.ImpactReviewID)
		if err != nil || reviewAfter.Status != "pending" {
			t.Fatalf("review after conflict = %+v, error = %v", reviewAfter, err)
		}
		var decisionCount, planCount int
		if err := store.db.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(*) FROM dependency_decisions),
				(SELECT COUNT(*) FROM regeneration_plans)`,
		).Scan(&decisionCount, &planCount); err != nil {
			t.Fatalf("count conflict records: %v", err)
		}
		if decisionCount != 0 || planCount != 0 {
			t.Fatalf("conflict decisions = %d, plans = %d", decisionCount, planCount)
		}
	})

	t.Run("keep confirms source and preserves exact old lineage", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		initial, sourceVersion, storyVersion, edited := createSingleImpactReview(
			t,
			store,
			"保留下游",
		)
		sourceManifestVersionID := currentArtifactVersionIDByType(
			t,
			store,
			initial.Run.RunID,
			"source_manifest",
		)
		command := ResolveImpactReviewCommand{
			CommandMeta: CommandMeta{
				Scope:          initial.Run.ProjectID,
				CommandType:    "resolve_impact_review",
				IdempotencyKey: "74dd60bb-5396-49a1-8bf0-2451b37c1591",
				RequestHash:    "keep_downstream_v1",
			},
			ImpactReviewID: edited.ImpactReview.ImpactReviewID,
			Action:         "keep_downstream",
			SnapshotHash:   edited.ImpactReview.SnapshotHash,
		}
		result, err := store.ResolveImpactReview(ctx, command)
		if err != nil {
			t.Fatalf("ResolveImpactReview(keep) error = %v", err)
		}
		if result.ImpactReview.Status != "kept" ||
			result.RegenerationPlan != nil ||
			result.Decision.Action != "keep_downstream" ||
			len(result.Decision.PreservedDownstreamVersionIDs) != 1 ||
			result.Decision.PreservedDownstreamVersionIDs[0] != storyVersion.ArtifactVersionID ||
			len(result.Decision.StaleDownstreamVersionIDs) != 0 {
			t.Fatalf("keep result = %+v", result)
		}
		sourceAfter, err := store.GetArtifactVersion(ctx, edited.ArtifactVersion.ArtifactVersionID)
		if err != nil || sourceAfter.Status != "confirmed" {
			t.Fatalf("new source after keep = %+v, error = %v", sourceAfter, err)
		}
		storyAfter, err := store.GetArtifactVersion(ctx, storyVersion.ArtifactVersionID)
		if err != nil || storyAfter.Status != "pending_approval" {
			t.Fatalf("story after keep = %+v, error = %v", storyAfter, err)
		}
		lineage, err := store.GetArtifactVersionLineage(ctx, storyVersion.ArtifactVersionID, 1)
		lineageRefs := map[string]bool{}
		configDependencies := 0
		if err == nil {
			for _, upstream := range lineage.Upstream {
				lineageRefs[upstream.UpstreamRefID] = true
				if upstream.UpstreamKind == "config_snapshot" {
					configDependencies++
				}
			}
		}
		if err != nil || len(lineage.Upstream) != 3 || configDependencies != 1 ||
			!lineageRefs[sourceVersion.ArtifactVersionID] ||
			!lineageRefs[sourceManifestVersionID] {
			t.Fatalf("kept lineage = %+v, error = %v", lineage, err)
		}
		if result.RunSnapshot.Run.Status != "waiting_approval" ||
			result.RunSnapshot.CurrentApproval == nil ||
			result.RunSnapshot.CurrentApproval.SubjectRefID != storyVersion.ArtifactVersionID {
			t.Fatalf("run after keep = %+v", result.RunSnapshot)
		}
		repeated, err := store.ResolveImpactReview(ctx, command)
		if err != nil ||
			repeated.Decision.DependencyDecisionID != result.Decision.DependencyDecisionID {
			t.Fatalf("idempotent keep = %+v, error = %v", repeated, err)
		}
		_, err = store.ResolveImpactReview(ctx, ResolveImpactReviewCommand{
			CommandMeta: CommandMeta{
				Scope:          initial.Run.ProjectID,
				CommandType:    "resolve_impact_review",
				IdempotencyKey: command.IdempotencyKey,
				RequestHash:    "different_request",
			},
			ImpactReviewID: command.ImpactReviewID,
			Action:         "regenerate_downstream",
			SnapshotHash:   command.SnapshotHash,
		})
		assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	})

	t.Run("regenerate creates resumable groups and stale versions", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, initial, storyArtifact, storyVersion := prepareStoryBibleForImpactWithContent(
			t,
			store,
			"重生成下游",
			`第一章 少年下山。
第二章 初遇强敌。
第三章 暗查线索。
第四章 身份暴露。
第五章 陷入危局。
第六章 绝境反击。
第七章 盟友现身。
第八章 深入敌营。
第九章 真相浮现。
第十章 决战之前。
第十一章 正面对决。
第十二章 新的危机。`,
		)
		episodeVersionID := addConfirmedImpactDownstream(
			t,
			store,
			project.ProjectID,
			initial.Run.RunID,
			storyVersion.ArtifactVersionID,
		)
		sourceArtifact := initial.Artifacts[0]
		sourceVersion, err := store.GetArtifactVersion(ctx, sourceArtifact.CurrentVersionID)
		if err != nil {
			t.Fatalf("GetArtifactVersion(source) error = %v", err)
		}
		sourceManifestVersionID := currentArtifactVersionIDByType(
			t,
			store,
			initial.Run.RunID,
			"source_manifest",
		)
		edited, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
			ArtifactID:    sourceArtifact.ArtifactID,
			BaseVersionID: sourceVersion.ArtifactVersionID,
			BaseVersion:   sourceVersion.Version,
			ChangeMode:    "whole_artifact",
			NewPayload:    sourceVersion.Payload,
		})
		if err != nil {
			t.Fatalf("CreateArtifactVersion() error = %v", err)
		}
		result, err := store.ResolveImpactReview(ctx, ResolveImpactReviewCommand{
			CommandMeta: CommandMeta{
				Scope:          project.ProjectID,
				CommandType:    "resolve_impact_review",
				IdempotencyKey: "c5df152b-9c79-4ad8-a9ab-1f4fc7a24e39",
				RequestHash:    "regenerate_downstream_v1",
			},
			ImpactReviewID: edited.ImpactReview.ImpactReviewID,
			Action:         "regenerate_downstream",
			SnapshotHash:   edited.ImpactReview.SnapshotHash,
		})
		if err != nil {
			t.Fatalf("ResolveImpactReview(regenerate) error = %v", err)
		}
		if result.ImpactReview.Status != "regeneration_planned" ||
			result.RegenerationPlan == nil ||
			result.RegenerationPlan.Status != "pending" ||
			len(result.RegenerationPlan.Groups) != 2 ||
			result.RegenerationPlan.Groups[0].StepID != "build_story_bible" ||
			result.RegenerationPlan.Groups[0].Status != "ready" ||
			result.RegenerationPlan.Groups[0].StepRunID == nil ||
			result.RegenerationPlan.Groups[1].StepID != "split_episodes" ||
			result.RegenerationPlan.Groups[1].Status != "blocked" {
			t.Fatalf("regeneration result = %+v", result)
		}
		for _, versionID := range []string{
			storyVersion.ArtifactVersionID,
			episodeVersionID,
		} {
			version, err := store.GetArtifactVersion(ctx, versionID)
			if err != nil || version.Status != "stale" {
				t.Fatalf("stale version %s = %+v, error = %v", versionID, version, err)
			}
		}
		sourceAfter, err := store.GetArtifactVersion(ctx, edited.ArtifactVersion.ArtifactVersionID)
		if err != nil || sourceAfter.Status != "confirmed" {
			t.Fatalf("source after regeneration = %+v, error = %v", sourceAfter, err)
		}
		storedPlan, err := store.GetRegenerationPlan(
			ctx,
			result.RegenerationPlan.RegenerationPlanID,
		)
		if err != nil || len(storedPlan.Groups) != 2 ||
			storedPlan.Groups[0].StepRunID == nil {
			t.Fatalf("stored plan = %+v, error = %v", storedPlan, err)
		}
		if result.RunSnapshot.Run.Status != "paused" ||
			result.RunSnapshot.Run.CurrentStepRunID == nil ||
			*result.RunSnapshot.Run.CurrentStepRunID != *storedPlan.Groups[0].StepRunID {
			t.Fatalf("run after regeneration = %+v", result.RunSnapshot)
		}
		tasks, err := store.ListTaskItems(ctx, *storedPlan.Groups[0].StepRunID)
		if err != nil || len(tasks) != 1 || tasks[0].Status != "pending" {
			t.Fatalf("regeneration tasks = %+v, error = %v", tasks, err)
		}
		resumed, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: initial.Run.RunID})
		if err != nil || resumed.Run.Status != "running" {
			t.Fatalf("ResumeRun(regeneration) = %+v, error = %v", resumed, err)
		}
		claim := claimStructuredTask(t, store)
		contextRefs := map[string]bool{}
		for _, upstream := range claim.ContextPack.UpstreamContext {
			contextRefs[upstream.ArtifactVersionID] = true
		}
		if len(claim.ContextPack.UpstreamContext) != 2 ||
			!contextRefs[edited.ArtifactVersion.ArtifactVersionID] ||
			!contextRefs[sourceManifestVersionID] ||
			claim.ContextPack.Step.StepID != "build_story_bible" {
			t.Fatalf("regeneration context = %+v", claim.ContextPack)
		}
		storyReceived, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
			AttemptID:         claim.Attempt.AttemptID,
			AttemptToken:      claim.AttemptToken,
			InputSnapshotHash: claim.Attempt.InputSnapshotHash,
			ResponsePayload:   validStoryBibleProviderResponseForClaim(t, claim),
		})
		if err != nil || storyReceived.ResponseHash == nil {
			t.Fatalf("SubmitExecutionResult(story regeneration) = %+v, error = %v", storyReceived, err)
		}
		storyReplacement, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{
			AttemptID:            claim.Attempt.AttemptID,
			ExpectedResponseHash: *storyReceived.ResponseHash,
		})
		if err != nil {
			t.Fatalf("CommitExecutionResult(story regeneration) error = %v", err)
		}
		if storyReplacement.Artifact.ArtifactID != storyArtifact.ArtifactID ||
			storyReplacement.ArtifactVersion.Version != 2 ||
			storyReplacement.ArtifactVersion.CreationReason != "regeneration" ||
			storyReplacement.ArtifactVersion.BaseVersionID == nil ||
			*storyReplacement.ArtifactVersion.BaseVersionID != storyVersion.ArtifactVersionID {
			t.Fatalf("story replacement = %+v", storyReplacement)
		}
		oldStory, err := store.GetArtifactVersion(ctx, storyVersion.ArtifactVersionID)
		if err != nil || oldStory.Status != "superseded" {
			t.Fatalf("old story after replacement = %+v, error = %v", oldStory, err)
		}
		storyLineage, err := store.GetArtifactVersionLineage(
			ctx,
			storyReplacement.ArtifactVersion.ArtifactVersionID,
			1,
		)
		replacementRefs := map[string]bool{}
		replacementConfigDependencies := 0
		if err == nil {
			for _, upstream := range storyLineage.Upstream {
				replacementRefs[upstream.UpstreamRefID] = true
				if upstream.UpstreamKind == "config_snapshot" {
					replacementConfigDependencies++
				}
			}
		}
		if err != nil || len(storyLineage.Upstream) != 3 || replacementConfigDependencies != 1 ||
			!replacementRefs[edited.ArtifactVersion.ArtifactVersionID] ||
			!replacementRefs[sourceManifestVersionID] {
			t.Fatalf("story replacement lineage = %+v, error = %v", storyLineage, err)
		}
		waitingPlan, err := store.GetRegenerationPlan(ctx, storedPlan.RegenerationPlanID)
		if err != nil || waitingPlan.Status != "waiting_approval" ||
			waitingPlan.Groups[0].Status != "waiting_approval" {
			t.Fatalf("waiting story plan = %+v, error = %v", waitingPlan, err)
		}
		afterStoryApproval, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
			ApprovalRequestID:       storyReplacement.Approval.ApprovalRequestID,
			Action:                  "approve",
			ExpectedApprovalVersion: storyReplacement.Approval.Version,
			SubjectSnapshotHash:     storyReplacement.Approval.SubjectSnapshotHash,
		})
		if err != nil {
			t.Fatalf("ResolveApproval(story regeneration) error = %v", err)
		}
		advancedPlan, err := store.GetRegenerationPlan(ctx, storedPlan.RegenerationPlanID)
		if err != nil || advancedPlan.Status != "pending" ||
			advancedPlan.CurrentGroupOrder != 2 ||
			advancedPlan.Groups[0].Status != "completed" ||
			advancedPlan.Groups[1].Status != "ready" ||
			advancedPlan.Groups[1].StepRunID == nil {
			t.Fatalf("advanced regeneration plan = %+v, error = %v", advancedPlan, err)
		}
		if afterStoryApproval.Run.Status != "paused" ||
			afterStoryApproval.Run.CurrentStepRunID == nil ||
			*afterStoryApproval.Run.CurrentStepRunID != *advancedPlan.Groups[1].StepRunID {
			t.Fatalf("run after story replacement approval = %+v", afterStoryApproval)
		}

		runningSplit, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: initial.Run.RunID})
		if err != nil || runningSplit.Run.Status != "running" {
			t.Fatalf("ResumeRun(split regeneration) = %+v, error = %v", runningSplit, err)
		}
		splitClaim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
			WorkerID:     "worker_split_test",
			ExecutorIDs:  []string{"workflow.novel_episode_split"},
			ProviderID:   "provider_test",
			LeaseSeconds: 60,
		})
		if err != nil || splitClaim == nil {
			t.Fatalf("ClaimExecutionTask(split regeneration) = %+v, error = %v", splitClaim, err)
		}
		splitContextRefs := map[string]bool{}
		for _, upstream := range splitClaim.ContextPack.UpstreamContext {
			splitContextRefs[upstream.ArtifactVersionID] = true
		}
		var splitCursor struct {
			Batch struct {
				Phase              string `json:"phase"`
				TargetEpisodeCount int    `json:"target_episode_count"`
			} `json:"batch"`
		}
		if err := json.Unmarshal(splitClaim.ContextPack.TaskCursor, &splitCursor); err != nil {
			t.Fatalf("decode split regeneration cursor: %v", err)
		}
		if len(splitClaim.ContextPack.UpstreamContext) != 3 ||
			!splitContextRefs[edited.ArtifactVersion.ArtifactVersionID] ||
			!splitContextRefs[storyReplacement.ArtifactVersion.ArtifactVersionID] ||
			!splitContextRefs[sourceManifestVersionID] ||
			splitClaim.ContextPack.Step.StepID != "split_episodes" ||
			splitCursor.Batch.Phase != "global_plan" ||
			splitClaim.ContextPack.OutputContract.ArtifactType != "task_checkpoint:global_plan" {
			t.Fatalf("split regeneration context = %+v", splitClaim.ContextPack)
		}
		commitSplitGlobalPlan(t, store, splitClaim, splitCursor.Batch.TargetEpisodeCount)
		var splitReplacement ArtifactCommitResult
		for batchNo := 1; batchNo <= splitCursor.Batch.TargetEpisodeCount; batchNo++ {
			splitBatchClaim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
				WorkerID:     "worker_split_batch_test",
				ExecutorIDs:  []string{"workflow.novel_episode_split"},
				ProviderID:   "provider_test",
				LeaseSeconds: 60,
			})
			if err != nil || splitBatchClaim == nil {
				t.Fatalf("ClaimExecutionTask(split batch regeneration) = %+v, error = %v", splitBatchClaim, err)
			}
			splitReplacement = commitSplitBatchResult(t, store, splitBatchClaim)
			if splitReplacement.ArtifactVersion.ArtifactVersionID != "" {
				break
			}
		}
		if splitReplacement.ArtifactVersion.ArtifactVersionID == "" {
			t.Fatal("split regeneration did not produce an aggregate artifact")
		}
		if splitReplacement.ArtifactVersion.Version != 2 ||
			splitReplacement.ArtifactVersion.CreationReason != "regeneration" ||
			splitReplacement.ArtifactVersion.BaseVersionID == nil ||
			*splitReplacement.ArtifactVersion.BaseVersionID != episodeVersionID {
			t.Fatalf("split replacement = %+v", splitReplacement)
		}
		splitLineage, err := store.GetArtifactVersionLineage(
			ctx,
			splitReplacement.ArtifactVersion.ArtifactVersionID,
			1,
		)
		splitLineageRefs := map[string]bool{}
		splitConfigDependencies := 0
		for _, upstream := range splitLineage.Upstream {
			splitLineageRefs[upstream.UpstreamRefID] = true
			if upstream.UpstreamKind == "config_snapshot" {
				splitConfigDependencies++
			}
		}
		if err != nil || len(splitLineage.Upstream) != 4 || splitConfigDependencies != 1 ||
			!splitLineageRefs[edited.ArtifactVersion.ArtifactVersionID] ||
			!splitLineageRefs[storyReplacement.ArtifactVersion.ArtifactVersionID] ||
			!splitLineageRefs[sourceManifestVersionID] {
			t.Fatalf("split replacement lineage = %+v, error = %v", splitLineage, err)
		}
		afterSplitApproval, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
			ApprovalRequestID:       splitReplacement.Approval.ApprovalRequestID,
			Action:                  "approve",
			ExpectedApprovalVersion: splitReplacement.Approval.Version,
			SubjectSnapshotHash:     splitReplacement.Approval.SubjectSnapshotHash,
		})
		if err != nil {
			t.Fatalf("ResolveApproval(split regeneration) error = %v", err)
		}
		completedPlan, err := store.GetRegenerationPlan(ctx, storedPlan.RegenerationPlanID)
		if err != nil || completedPlan.Status != "completed" ||
			completedPlan.CompletedAt == nil ||
			completedPlan.Groups[1].Status != "completed" {
			t.Fatalf("completed regeneration plan = %+v, error = %v", completedPlan, err)
		}
		if afterSplitApproval.Run.Status != "paused" ||
			afterSplitApproval.Run.CurrentStepRunID == nil {
			t.Fatalf("run after completed regeneration = %+v", afterSplitApproval.Run)
		}
		var nextStep StepRun
		for _, step := range afterSplitApproval.Steps {
			if step.StepRunID == *afterSplitApproval.Run.CurrentStepRunID {
				nextStep = step
				break
			}
		}
		if nextStep.StepID != "build_episode_cards" || nextStep.Status != "pending" {
			t.Fatalf("next step after regeneration = %+v", nextStep)
		}
		var nextInputs []struct {
			ArtifactVersionID string `json:"artifact_version_id"`
		}
		if err := json.Unmarshal(nextStep.InputVersionSnapshot, &nextInputs); err != nil {
			t.Fatalf("decode next step inputs: %v", err)
		}
		if len(nextInputs) != 2 ||
			nextInputs[0].ArtifactVersionID !=
				splitReplacement.ArtifactVersion.ArtifactVersionID ||
			nextInputs[1].ArtifactVersionID !=
				storyReplacement.ArtifactVersion.ArtifactVersionID {
			t.Fatalf("next step lineage inputs = %+v", nextInputs)
		}
	})
}

func TestGroupImpactItemsForRegenerationMergesBatchTaskKeysByStep(t *testing.T) {
	items := []ImpactReviewItem{
		{ArtifactVersionID: "script-1", RegenerateFromStepID: "generate_script_units", RegenerateTaskKeys: []string{"episode:1"}},
		{ArtifactVersionID: "handoff-1", RegenerateFromStepID: "generate_script_units", RegenerateTaskKeys: []string{"episode:1"}},
		{ArtifactVersionID: "script-2", RegenerateFromStepID: "generate_script_units", RegenerateTaskKeys: []string{"episode:2"}},
		{ArtifactVersionID: "scripts", RegenerateFromStepID: "aggregate_scripts", RegenerateTaskKeys: []string{"singleton"}},
	}

	groups := groupImpactItemsForRegeneration(items)
	if len(groups) != 2 {
		t.Fatalf("groups = %+v, want 2 groups", groups)
	}
	if len(groups[0].StaleVersionIDs) != 3 || len(groups[0].TaskKeys) != 2 ||
		groups[0].TaskKeys[0] != "episode:1" || groups[0].TaskKeys[1] != "episode:2" {
		t.Fatalf("script generation group = %+v", groups[0])
	}
	if groups[1].StepID != "aggregate_scripts" {
		t.Fatalf("aggregate group = %+v", groups[1])
	}
}

func TestRegenerationStepInputsIncludeEveryManyInput(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	runID, _, projectID := seedQualityReviewRun(t, store)
	seedQualityReviewScriptArtifacts(t, store, runID, projectID)
	now := formatTime(store.now())
	contextArtifactID := store.newID("art")
	contextVersionID := store.newID("av")
	if _, err := store.db.Exec(`INSERT INTO artifacts(
		artifact_id, project_id, run_id, step_run_id, capability_id,
		artifact_type, scope_key, current_version_id, created_at, updated_at
	) VALUES(?, ?, ?, 'step_fixture_build_script_context', 'novel_to_script',
		'script_context', 'episode:2', ?, ?, ?)`,
		contextArtifactID, projectID, runID, contextVersionID, now, now); err != nil {
		t.Fatalf("insert second script context artifact: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO artifact_versions(
		artifact_version_id, artifact_id, version, status, payload_json, schema_id,
		schema_version, created_by_kind, actor_ref, creation_reason, created_at, confirmed_at
	) VALUES(?, ?, 1, 'confirmed', '{"episode_no":2}', 'script_context',
		'1.0.0', 'model', 'fixture', 'generated', ?, ?)`,
		contextVersionID, contextArtifactID, now, now); err != nil {
		t.Fatalf("insert second script context version: %v", err)
	}

	run, err := store.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	entry, ok := store.registry.Get(run.CapabilityID)
	if !ok || entry.Definition == nil {
		t.Fatal("novel capability is unavailable")
	}
	var scriptStep capability.CompiledStep
	for _, step := range entry.Definition.Steps {
		if step.ID == "generate_script_units" {
			scriptStep = step
			break
		}
	}
	source, sourceVersion := qualityScriptArtifact(t, store, runID)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	defer tx.Rollback()
	inputs, err := store.regenerationStepInputsTx(
		ctx,
		tx,
		scriptStep,
		ImpactReviewItem{ArtifactVersionID: sourceVersion.ArtifactVersionID},
		source,
		sourceVersion.ArtifactVersionID,
	)
	if err != nil {
		t.Fatalf("regenerationStepInputsTx() error = %v", err)
	}
	contextScopes := []string{}
	for _, input := range inputs {
		if scope, _ := input["scope_key"].(string); strings.HasPrefix(scope, "episode:") {
			contextScopes = append(contextScopes, scope)
		}
	}
	if len(inputs) != 3 || !slices.Equal(contextScopes, []string{"episode:1", "episode:2"}) {
		t.Fatalf("regeneration inputs = %+v", inputs)
	}
}

func TestRegenerationRequiresFullBatchPlanning(t *testing.T) {
	tests := []struct {
		name string
		step capability.CompiledStep
		want bool
	}{
		{
			name: "aggregate episode cards",
			step: capability.CompiledStep{
				Batch:      &capability.BatchPolicy{ItemKey: "episode_no", MaxItemsPerTask: 5},
				OutputRefs: []capability.ArtifactOutput{{ArtifactType: "episode_cards", Cardinality: "one"}},
			},
			want: true,
		},
		{
			name: "scoped script units",
			step: capability.CompiledStep{
				Batch:      &capability.BatchPolicy{ItemKey: "episode_no", MaxItemsPerTask: 1},
				OutputRefs: []capability.ArtifactOutput{{ArtifactType: "script_unit", Cardinality: "many"}},
			},
			want: false,
		},
		{
			name: "staged aggregate replans internal stages",
			step: capability.CompiledStep{
				Batch: &capability.BatchPolicy{
					ItemKey:     "episode_no",
					Preparation: &capability.BatchInternalTaskStage{ID: "global_plan"},
					TaskStage:   &capability.BatchInternalTaskStage{ID: "boundary_batch"},
				},
				OutputRefs: []capability.ArtifactOutput{{ArtifactType: "episode_split", Cardinality: "one"}},
			},
			want: true,
		},
		{name: "non batch step", step: capability.CompiledStep{}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := regenerationRequiresFullBatchPlanning(test.step); got != test.want {
				t.Fatalf("regenerationRequiresFullBatchPlanning() = %v, want %v", got, test.want)
			}
		})
	}
}

func validEpisodeSplitProviderResponse() json.RawMessage {
	return mustJSONNoTest(map[string]any{
		"episode_split": map[string]any{
			"target_episode_count": 1,
			"actual_episode_count": 1,
			"split_strategy":       "按核心冲突拆分。",
			"episodes": []map[string]any{{
				"episode_no":      1,
				"source_refs":     []any{},
				"source_summary":  "少年下山并卷入旧案。",
				"core_event":      "少年决定追查真相。",
				"character_turn":  "主角从被动转为主动。",
				"boundary_reason": "核心行动成立。",
				"boundary_check": map[string]any{
					"previous_episode_end":   "",
					"next_episode_start":     "",
					"cut_after_anchor":       "决定追查",
					"cut_before_anchor":      "",
					"continuity_risk":        "low",
					"manual_review_required": false,
				},
				"hook_strength":           "high",
				"hook_type":               "悬念",
				"information_density":     "medium",
				"pacing_risk":             "low",
				"split_confidence":        "high",
				"requires_user_attention": false,
				"adaptation_added":        false,
			}},
			"coverage_check": map[string]any{
				"covered_ranges":    []string{"第一章"},
				"missing_ranges":    []string{},
				"duplicated_ranges": []string{},
				"order_issues":      []string{},
			},
			"global_risks": []string{},
			"source_trace": map[string]any{
				"grounded": []any{},
				"inferred": []any{},
			},
		},
	})
}

func createSingleImpactReview(
	t *testing.T,
	store *Store,
	title string,
) (RunSnapshot, ArtifactVersion, ArtifactVersion, VersionResult) {
	t.Helper()
	ctx := context.Background()
	_, initial, _, storyVersion := prepareStoryBibleForImpact(t, store, title)
	sourceArtifact := initial.Artifacts[0]
	sourceVersion, err := store.GetArtifactVersion(ctx, sourceArtifact.CurrentVersionID)
	if err != nil {
		t.Fatalf("GetArtifactVersion(source) error = %v", err)
	}
	edited, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
		ArtifactID:    sourceArtifact.ArtifactID,
		BaseVersionID: sourceVersion.ArtifactVersionID,
		BaseVersion:   sourceVersion.Version,
		ChangeMode:    "whole_artifact",
		NewPayload:    sourceVersion.Payload,
	})
	if err != nil {
		t.Fatalf("CreateArtifactVersion() error = %v", err)
	}
	if edited.ImpactReview == nil || len(edited.ImpactReview.AffectedItems) != 1 {
		t.Fatalf("single impact review = %+v", edited.ImpactReview)
	}
	return initial, sourceVersion, storyVersion, edited
}

func currentArtifactVersionIDByType(
	t *testing.T,
	store *Store,
	runID string,
	artifactType string,
) string {
	t.Helper()
	artifacts, err := store.ListArtifactsByRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListArtifactsByRun(%s) error = %v", artifactType, err)
	}
	for _, artifact := range artifacts {
		if artifact.ArtifactType == artifactType {
			return artifact.CurrentVersionID
		}
	}
	t.Fatalf("artifact type %s not found in run %s", artifactType, runID)
	return ""
}
