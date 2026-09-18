package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestQualityReviewGateAndRiskOverride(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	runID, stepRunID, projectID := seedQualityReviewRun(t, store)
	review, err := store.StartQualityReview(ctx, StartQualityReviewCommand{
		RunID:             runID,
		StepRunID:         stepRunID,
		InputSnapshotHash: "review_input_v1",
		Scope:             "full_script",
	})
	if err != nil {
		t.Fatalf("StartQualityReview() error = %v", err)
	}
	repeated, err := store.StartQualityReview(ctx, StartQualityReviewCommand{
		RunID:             runID,
		StepRunID:         stepRunID,
		InputSnapshotHash: "review_input_v1",
		Scope:             "full_script",
	})
	if err != nil || repeated.QualityReviewID != review.QualityReviewID {
		t.Fatalf("idempotent review = %+v, error = %v", repeated, err)
	}

	result := qualityReviewResult(t, "review_input_v1", QualityIssueCounts{High: 1})
	completed, err := store.CompleteQualityReview(ctx, CompleteQualityReviewCommand{
		QualityReviewID:   review.QualityReviewID,
		InputSnapshotHash: review.InputSnapshotHash,
		Result:            result,
	})
	if err != nil {
		t.Fatalf("CompleteQualityReview() error = %v", err)
	}
	if completed.Status != "action_required" ||
		completed.RecommendedRoute == nil ||
		*completed.RecommendedRoute != "script_generation" {
		t.Fatalf("completed review = %+v", completed)
	}

	meta := CommandMeta{
		Scope:          projectID,
		CommandType:    "resolve_quality_review_action",
		IdempotencyKey: "12121212-1212-4212-8212-121212121212",
		RequestHash:    "quality_override_v1",
	}
	action, err := store.ResolveQualityReviewAction(ctx, ResolveQualityReviewActionCommand{
		CommandMeta:          meta,
		QualityReviewID:      review.QualityReviewID,
		ExpectedReviewStatus: "action_required",
		InputSnapshotHash:    review.InputSnapshotHash,
		Action:               "accept_with_risk",
		ActorRef:             "shared_internal_user",
		IgnoredIssueIDs:      []string{"ISSUE-1"},
	})
	if err != nil || action.Override == nil {
		t.Fatalf("ResolveQualityReviewAction() = %+v, error = %v", action, err)
	}
	repeatedAction, err := store.ResolveQualityReviewAction(ctx, ResolveQualityReviewActionCommand{
		CommandMeta:          meta,
		QualityReviewID:      review.QualityReviewID,
		ExpectedReviewStatus: "action_required",
		InputSnapshotHash:    review.InputSnapshotHash,
		Action:               "accept_with_risk",
		ActorRef:             "shared_internal_user",
		IgnoredIssueIDs:      []string{"ISSUE-1"},
	})
	if err != nil || repeatedAction.Override == nil ||
		repeatedAction.Override.QualityOverrideID != action.Override.QualityOverrideID {
		t.Fatalf("idempotent override = %+v, error = %v", repeatedAction, err)
	}

	newReview, err := store.StartQualityReview(ctx, StartQualityReviewCommand{
		RunID:             runID,
		StepRunID:         stepRunID,
		InputSnapshotHash: "review_input_v2",
		Scope:             "impacted_scope",
	})
	if err != nil {
		t.Fatalf("StartQualityReview(v2) error = %v", err)
	}
	oldReview, err := store.GetQualityReview(ctx, review.QualityReviewID)
	if err != nil || oldReview.Status != "superseded" {
		t.Fatalf("old review = %+v, error = %v", oldReview, err)
	}
	blocked, err := store.CompleteQualityReview(ctx, CompleteQualityReviewCommand{
		QualityReviewID:   newReview.QualityReviewID,
		InputSnapshotHash: newReview.InputSnapshotHash,
		Result: qualityReviewResult(
			t,
			"review_input_v2",
			QualityIssueCounts{Blocker: 1},
		),
	})
	if err != nil || blocked.Status != "action_required" {
		t.Fatalf("blocker review = %+v, error = %v", blocked, err)
	}
	_, err = store.ResolveQualityReviewAction(ctx, ResolveQualityReviewActionCommand{
		QualityReviewID:      blocked.QualityReviewID,
		ExpectedReviewStatus: "action_required",
		InputSnapshotHash:    blocked.InputSnapshotHash,
		Action:               "accept_with_risk",
		ActorRef:             "shared_internal_user",
	})
	assertDomainCode(t, err, "QUALITY_REVIEW_ACTION_NOT_ALLOWED")
}

func TestQualityReviewPassesWithOnlyLowIssues(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	runID, stepRunID, _ := seedQualityReviewRun(t, store)
	review, err := store.StartQualityReview(ctx, StartQualityReviewCommand{
		RunID:             runID,
		StepRunID:         stepRunID,
		InputSnapshotHash: "low_only",
	})
	if err != nil {
		t.Fatalf("StartQualityReview() error = %v", err)
	}
	result := qualityReviewResult(t, "low_only", QualityIssueCounts{Low: 1})
	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	payload["recommended_route"] = nil
	result = mustJSON(t, payload)
	completed, err := store.CompleteQualityReview(ctx, CompleteQualityReviewCommand{
		QualityReviewID:   review.QualityReviewID,
		InputSnapshotHash: review.InputSnapshotHash,
		Result:            result,
	})
	if err != nil || completed.Status != "passed" {
		t.Fatalf("low-only review = %+v, error = %v", completed, err)
	}
}

func TestQualityReviewUsesExecutorContractInsteadOfStepID(t *testing.T) {
	registry := loadTestRegistry(t)
	entry, ok := registry.Get("novel_to_script")
	if !ok || entry.Definition == nil {
		t.Fatal("novel_to_script definition is missing")
	}
	for index := range entry.Definition.Steps {
		step := &entry.Definition.Steps[index]
		if step.ID == "generate_script_units" {
			step.Next[0].To = "custom_review_gate"
		}
		if step.ID == "review_script_set" {
			step.ID = "custom_review_gate"
		}
	}
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	runID, stepRunID, _ := seedQualityReviewRunWithStepID(
		t,
		store,
		"custom_review_gate",
	)
	review, err := store.StartQualityReview(context.Background(), StartQualityReviewCommand{
		RunID: runID, StepRunID: stepRunID, InputSnapshotHash: "custom-review-hash",
	})
	if err != nil {
		t.Fatalf("StartQualityReview(custom step ID) error = %v", err)
	}
	if review.StepRunID != stepRunID || review.Status != "running" {
		t.Fatalf("custom quality review = %+v", review)
	}
}

func TestQualityReviewAIRevisionCreatesRegenerationPlan(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	runID, reviewStepRunID, projectID := seedQualityReviewRun(t, store)
	seedQualityReviewScriptArtifacts(t, store, runID, projectID)
	review, err := store.StartQualityReview(ctx, StartQualityReviewCommand{
		RunID: runID, StepRunID: reviewStepRunID, InputSnapshotHash: "quality_rework_v1",
	})
	if err != nil {
		t.Fatalf("StartQualityReview() error = %v", err)
	}
	review, err = store.CompleteQualityReview(ctx, CompleteQualityReviewCommand{
		QualityReviewID: review.QualityReviewID, InputSnapshotHash: review.InputSnapshotHash,
		Result: qualityReviewResult(t, review.InputSnapshotHash, QualityIssueCounts{High: 1}),
	})
	if err != nil {
		t.Fatalf("CompleteQualityReview() error = %v", err)
	}
	seedQualityReviewApproval(t, store, review)
	command := ResolveQualityReviewActionCommand{
		CommandMeta: CommandMeta{
			Scope: projectID, CommandType: "resolve_quality_review_action",
			IdempotencyKey: "13131313-1313-4313-8313-131313131313",
			RequestHash:    "quality_ai_revise_v1",
		},
		QualityReviewID: review.QualityReviewID, ExpectedReviewStatus: "action_required",
		InputSnapshotHash: review.InputSnapshotHash, Action: "ai_revise",
		Instruction: "修复第一集承接关系，保留主冲突。", ActorRef: "quality_reviewer",
	}
	result, err := store.ResolveQualityReviewAction(ctx, command)
	if err != nil {
		t.Fatalf("ResolveQualityReviewAction() error = %v", err)
	}
	if result.Review.Status != "superseded" || result.RegenerationPlan == nil ||
		len(result.RegenerationPlan.Groups) == 0 ||
		result.RegenerationPlan.Groups[0].StepID != "generate_script_units" ||
		result.RunSnapshot == nil || result.RunSnapshot.Run.Status != "paused" {
		t.Fatalf("quality rework result = %+v", result)
	}
	group := result.RegenerationPlan.Groups[0]
	if len(group.TaskKeys) != 1 || group.TaskKeys[0] != "episode:1" || group.StepRunID == nil {
		t.Fatalf("quality rework group = %+v", group)
	}
	var cursor map[string]any
	for _, step := range result.RunSnapshot.Steps {
		if step.StepRunID == *group.StepRunID {
			if err := json.Unmarshal(step.TaskCursor, &cursor); err != nil {
				t.Fatalf("decode rework cursor: %v", err)
			}
		}
	}
	if instruction, _ := cursor["revision_instruction"].(string); instruction == "" ||
		!strings.Contains(instruction, "修复第一集承接关系") ||
		!strings.Contains(instruction, "质量审核结果") {
		t.Fatalf("revision instruction = %#v", cursor["revision_instruction"])
	}
	repeated, err := store.ResolveQualityReviewAction(ctx, command)
	if err != nil || repeated.RegenerationPlan == nil ||
		repeated.RegenerationPlan.RegenerationPlanID != result.RegenerationPlan.RegenerationPlanID {
		t.Fatalf("idempotent quality rework = %+v, error = %v", repeated, err)
	}
	startStatements := []struct {
		query string
		args  []any
	}{
		{`UPDATE regeneration_plans SET status = 'running' WHERE regeneration_plan_id = ?`, []any{result.RegenerationPlan.RegenerationPlanID}},
		{`UPDATE regeneration_plan_groups SET status = 'running' WHERE regeneration_plan_id = ?`, []any{result.RegenerationPlan.RegenerationPlanID}},
		{`UPDATE step_runs SET status = 'running', attempt_count = 1 WHERE step_run_id = ?`, []any{*group.StepRunID}},
		{`UPDATE runs SET status = 'running', current_step_run_id = ? WHERE run_id = ?`, []any{*group.StepRunID, runID}},
		{`UPDATE projects SET status = 'running', active_write_run_id = ? WHERE project_id = ?`, []any{runID, projectID}},
	}
	for _, statement := range startStatements {
		result, execErr := store.db.ExecContext(ctx, statement.query, statement.args...)
		if execErr != nil {
			t.Fatalf("start quality rework group: %v", execErr)
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
			t.Fatalf("start quality rework group affected = %d, error = %v", affected, rowsErr)
		}
	}
	claim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
		WorkerID:     "quality_rework_script_worker",
		ExecutorIDs:  []string{"workflow.shared_script_generation"},
		ProviderID:   "provider_test",
		LeaseSeconds: 60,
	})
	if err != nil || claim == nil || claim.Task.ItemKey != "episode:1" {
		t.Fatalf("quality rework claim = %+v, error = %v", claim, err)
	}
	committed := commitScriptUnitResult(t, store, claim, 1)
	if committed.CommitStatus != "waiting_approval" || len(committed.Outputs) != 2 {
		t.Fatalf("quality rework commit = %+v", committed)
	}
	for _, output := range committed.Outputs {
		if output.ArtifactVersion.Version != 3 ||
			output.ArtifactVersion.CreationReason != "regeneration" ||
			output.ArtifactVersion.BaseVersionID == nil {
			t.Fatalf("quality rework output = %+v", output)
		}
	}
	var scopedArtifacts int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM artifacts
		WHERE run_id = ? AND scope_key = 'episode:1'
			AND artifact_type IN ('script_unit','script_handoff')`,
		runID,
	).Scan(&scopedArtifacts); err != nil || scopedArtifacts != 2 {
		t.Fatalf("quality rework artifact count = %d, error = %v", scopedArtifacts, err)
	}
}

func TestQualityReviewConfirmedChangeCreatesScriptRegenerationPlan(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	runID, reviewStepRunID, projectID := seedQualityReviewRun(t, store)
	seedQualityReviewScriptArtifacts(t, store, runID, projectID)
	review, err := store.StartQualityReview(ctx, StartQualityReviewCommand{
		RunID: runID, StepRunID: reviewStepRunID, InputSnapshotHash: "quality_change_v1",
	})
	if err != nil {
		t.Fatalf("StartQualityReview() error = %v", err)
	}
	resultPayload := qualityReviewResult(t, review.InputSnapshotHash, QualityIssueCounts{Blocker: 1})
	var resultMap map[string]any
	if err := json.Unmarshal(resultPayload, &resultMap); err != nil {
		t.Fatal(err)
	}
	resultMap["recommended_route"] = "user"
	issues := resultMap["issues"].([]any)
	issues[0].(map[string]any)["recommended_route"] = "user"
	review, err = store.CompleteQualityReview(ctx, CompleteQualityReviewCommand{
		QualityReviewID: review.QualityReviewID, InputSnapshotHash: review.InputSnapshotHash,
		Result: mustJSON(t, resultMap),
	})
	if err != nil {
		t.Fatalf("CompleteQualityReview() error = %v", err)
	}
	seedQualityReviewApproval(t, store, review)
	result, err := store.ResolveQualityReviewAction(ctx, ResolveQualityReviewActionCommand{
		CommandMeta: CommandMeta{
			Scope: projectID, CommandType: "resolve_quality_review_action",
			IdempotencyKey: "14141414-1414-4414-8414-141414141414",
			RequestHash:    "quality_confirm_change_v1",
		},
		QualityReviewID: review.QualityReviewID, ExpectedReviewStatus: "action_required",
		InputSnapshotHash: review.InputSnapshotHash, Action: "confirm_change",
		Instruction: "确认主角在第一集提前知道师父身份。", ActorRef: "quality_reviewer",
	})
	if err != nil {
		t.Fatalf("ResolveQualityReviewAction(confirm_change) error = %v", err)
	}
	if result.RegenerationPlan == nil || len(result.RegenerationPlan.Groups) == 0 ||
		result.RegenerationPlan.Groups[0].StepID != "generate_script_units" {
		t.Fatalf("confirmed change result = %+v", result)
	}
}

func TestQualityReviewManualEditSupersedesReviewAndRefreshesHandoff(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	runID, reviewStepRunID, projectID := seedQualityReviewRun(t, store)
	seedQualityReviewScriptArtifacts(t, store, runID, projectID)
	seedScriptEditTaskAndApproval(t, store, runID, projectID)
	review, err := store.StartQualityReview(ctx, StartQualityReviewCommand{
		RunID: runID, StepRunID: reviewStepRunID, InputSnapshotHash: "quality_manual_v1",
	})
	if err != nil {
		t.Fatalf("StartQualityReview() error = %v", err)
	}
	review, err = store.CompleteQualityReview(ctx, CompleteQualityReviewCommand{
		QualityReviewID: review.QualityReviewID, InputSnapshotHash: review.InputSnapshotHash,
		Result: qualityReviewResult(t, review.InputSnapshotHash, QualityIssueCounts{High: 1}),
	})
	if err != nil {
		t.Fatalf("CompleteQualityReview() error = %v", err)
	}
	seedQualityReviewApproval(t, store, review)

	artifact, version := qualityScriptArtifact(t, store, runID)
	edit, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
		CommandMeta: CommandMeta{
			Scope: projectID, CommandType: "create_artifact_version",
			IdempotencyKey: "15151515-1515-4515-8515-151515151515",
			RequestHash:    "quality_manual_edit_v1",
		},
		ArtifactID: artifact.ArtifactID, BaseVersionID: version.ArtifactVersionID,
		BaseVersion: version.Version, ChangeMode: "whole_artifact",
		NewPayload: mustJSON(t, map[string]any{"script": "手动修订后的第一集"}),
		ActorRef:   "quality_reviewer",
	})
	if err != nil {
		t.Fatalf("CreateArtifactVersion() error = %v", err)
	}
	if !edit.HandoffRefreshRequired || len(edit.PendingRefreshScopes) != 1 ||
		edit.PendingRefreshScopes[0] != "episode:1" {
		t.Fatalf("manual edit result = %+v", edit)
	}
	superseded, err := store.GetQualityReview(ctx, review.QualityReviewID)
	if err != nil || superseded.Status != "superseded" {
		t.Fatalf("quality review after manual edit = %+v, error = %v", superseded, err)
	}
	completion, err := store.CompleteScriptEdit(ctx, CompleteScriptEditCommand{
		CommandMeta: CommandMeta{
			Scope: projectID, CommandType: "complete_script_edit",
			IdempotencyKey: "16161616-1616-4616-8616-161616161616",
			RequestHash:    "quality_manual_complete_v1",
		},
		RunID: runID, ExpectedScriptVersionIDs: []string{edit.ArtifactVersion.ArtifactVersionID},
	})
	if err != nil {
		t.Fatalf("CompleteScriptEdit() error = %v", err)
	}
	if completion.RunSnapshot.Run.Status != "running" || len(completion.RefreshTaskIDs) != 1 {
		t.Fatalf("script edit completion = %+v", completion)
	}
}

func qualityScriptArtifact(t *testing.T, store *Store, runID string) (Artifact, ArtifactVersion) {
	t.Helper()
	artifact, err := scanArtifact(store.db.QueryRow(`SELECT artifact_id, project_id, COALESCE(run_id, ''),
		COALESCE(step_run_id, ''), origin_type, origin_id, capability_id, artifact_type, scope_key, current_version_id, created_at, updated_at
		FROM artifacts WHERE run_id = ? AND artifact_type = 'script_unit' AND scope_key = 'episode:1'`, runID))
	if err != nil {
		t.Fatalf("load script artifact: %v", err)
	}
	version, err := store.GetArtifactVersion(context.Background(), artifact.CurrentVersionID)
	if err != nil {
		t.Fatalf("load script version: %v", err)
	}
	return artifact, version
}

func seedScriptEditTaskAndApproval(t *testing.T, store *Store, runID, projectID string) {
	t.Helper()
	artifact, version := qualityScriptArtifact(t, store, runID)
	now := formatTime(store.now())
	if _, err := store.db.Exec(`INSERT INTO task_items(
		task_item_id, step_run_id, run_id, item_key, item_order, status, attempt_count,
		input_snapshot_json, cursor_json, output_artifact_version_id,
		started_at, ended_at, created_at, updated_at
	) VALUES(?, ?, ?, 'episode:1', 1, 'succeeded', 1, '{}', '{}', ?, ?, ?, ?, ?)`,
		store.newID("tsk"), artifact.StepRunID, runID, version.ArtifactVersionID,
		now, now, now, now); err != nil {
		t.Fatalf("insert script task: %v", err)
	}
	options, _ := json.Marshal([]string{"approve", "edit_artifact", "request_ai_revision", "regenerate_artifact"})
	if _, err := store.db.Exec(`INSERT INTO approvals(
		approval_request_id, project_id, run_id, step_run_id, scope, status, version,
		title, reason, options_json, subject_kind, subject_ref_id, subject_version,
		subject_snapshot_hash, requested_at, resolved_at, resolution_json, actor_ref
	) VALUES(?, ?, ?, ?, 'artifact', 'approved', 1, '确认整个剧本', 'fixture', ?,
		'artifact_version_set', ?, 1, 'fixture_hash', ?, ?, '{"action":"approve"}', 'fixture')`,
		store.newID("apr"), projectID, runID, artifact.StepRunID, string(options),
		artifact.StepRunID, now, now); err != nil {
		t.Fatalf("insert script approval: %v", err)
	}
}

func seedQualityReviewScriptArtifacts(t *testing.T, store *Store, runID, projectID string) {
	t.Helper()
	now := formatTime(store.now())
	steps := map[string]string{
		"episode_cards":  "build_episode_cards",
		"script_context": "build_script_context",
		"scripts":        "generate_script_units",
	}
	for _, stepID := range steps {
		stepRunID := "step_fixture_" + stepID
		if _, err := store.db.Exec(`INSERT INTO step_runs(
			step_run_id, run_id, step_id, status, attempt_count, approval_policy,
			input_version_snapshot_json, task_cursor_json, started_at, ended_at
		) VALUES(?, ?, ?, 'completed', 1, 'none', '[]', '{}', ?, ?)`,
			stepRunID, runID, stepID, now, now); err != nil {
			t.Fatalf("insert fixture step %s: %v", stepID, err)
		}
	}
	insert := func(artifactType, scopeKey, stepID string) {
		artifactID := store.newID("art")
		versionID := store.newID("av")
		if _, err := store.db.Exec(`INSERT INTO artifacts(
			artifact_id, project_id, run_id, step_run_id, capability_id,
			artifact_type, scope_key, current_version_id, created_at, updated_at
		) VALUES(?, ?, ?, ?, 'novel_to_script', ?, ?, ?, ?, ?)`,
			artifactID, projectID, runID, "step_fixture_"+stepID,
			artifactType, scopeKey, versionID, now, now); err != nil {
			t.Fatalf("insert fixture artifact %s: %v", artifactType, err)
		}
		payload, _ := json.Marshal(map[string]any{"fixture": artifactType, "episode_no": 1})
		if _, err := store.db.Exec(`INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason, created_at, confirmed_at
		) VALUES(?, ?, 1, 'confirmed', ?, ?, '1.0.0', 'model', 'fixture', 'generated', ?, ?)`,
			versionID, artifactID, string(payload), artifactType, now, now); err != nil {
			t.Fatalf("insert fixture version %s: %v", artifactType, err)
		}
	}
	insert("episode_cards", "singleton", steps["episode_cards"])
	insert("script_context", "episode:1", steps["script_context"])
	insert("script_unit", "episode:1", steps["scripts"])
	insert("script_handoff", "episode:1", steps["scripts"])
}

func seedQualityReviewApproval(t *testing.T, store *Store, review QualityReview) {
	t.Helper()
	now := formatTime(store.now())
	options, _ := json.Marshal([]string{"ai_revise", "manual_edit", "confirm_change", "accept_with_risk"})
	if _, err := store.db.Exec(`INSERT INTO approvals(
		approval_request_id, project_id, run_id, step_run_id, scope, status, version,
		title, reason, options_json, subject_kind, subject_ref_id, subject_version,
		subject_snapshot_hash, requested_at
	) VALUES(?, ?, ?, ?, 'quality_review', 'pending', 1, '质量审核需要处理',
		'请确认处理方式', ?, 'quality_review', ?, ?, ?, ?)`,
		store.newID("apr"), review.ProjectID, review.RunID, review.StepRunID,
		string(options), review.QualityReviewID, review.ReviewVersion,
		review.InputSnapshotHash, now); err != nil {
		t.Fatalf("insert quality approval: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE step_runs SET status = 'waiting_approval' WHERE step_run_id = ?`, review.StepRunID); err != nil {
		t.Fatalf("update review step: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE runs SET status = 'waiting_approval' WHERE run_id = ?`, review.RunID); err != nil {
		t.Fatalf("update review run: %v", err)
	}
}

func seedQualityReviewRun(t *testing.T, store *Store) (string, string, string) {
	t.Helper()
	return seedQualityReviewRunWithStepID(t, store, "review_script_set")
}

func seedQualityReviewRunWithStepID(
	t *testing.T,
	store *Store,
	stepID string,
) (string, string, string) {
	t.Helper()
	project, err := store.CreateProject(context.Background(), "质量审核测试")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	runID := store.newID("run")
	stepRunID := store.newID("step")
	now := formatTime(store.now())
	if _, err := store.db.Exec(`
		INSERT INTO runs(
			run_id, project_id, conversation_id, capability_id, capability_version,
			run_kind, write_intent, status, current_step_run_id,
			current_input_snapshot_version_id, input_snapshot_status,
			config_snapshot_json, run_event_seq, started_at, created_at, updated_at
		) VALUES(?, ?, ?, 'novel_to_script', '1.4.0', 'generation', 1, 'running',
			?, 'risv_quality', 'sealed', '{}', 0, ?, ?, ?)`,
		runID,
		project.ProjectID,
		project.PrimaryConversationID,
		stepRunID,
		now,
		now,
		now,
	); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO run_config_snapshots(
			config_snapshot_id, run_id, config_ref, version, status,
			payload_json, snapshot_hash, created_at, sealed_at
		) VALUES(?, ?, 'creation', 1, 'sealed', ?, 'quality_creation_hash', ?, ?)`,
		store.newID("cfg"),
		runID,
		`{"config_ref":"creation","payload":{"target_episode_count":1,"episode_duration_minutes":1}}`,
		now,
		now,
	); err != nil {
		t.Fatalf("insert creation config snapshot: %v", err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO run_input_snapshot_versions(
			run_input_snapshot_version_id, run_id, version, status,
			payload_json, created_at, sealed_at
		) VALUES('risv_quality', ?, 1, 'sealed', '{}', ?, ?)`,
		runID,
		now,
		now,
	); err != nil {
		t.Fatalf("insert run input snapshot: %v", err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO step_runs(
			step_run_id, run_id, step_id, status, attempt_count, approval_policy,
			input_version_snapshot_json, task_cursor_json, started_at
		) VALUES(?, ?, ?, 'running', 1, 'conditional_review',
			'[]', '{}', ?)`,
		stepRunID,
		runID,
		stepID,
		now,
	); err != nil {
		t.Fatalf("insert review step: %v", err)
	}
	return runID, stepRunID, project.ProjectID
}

func qualityReviewResult(
	t *testing.T,
	inputHash string,
	counts QualityIssueCounts,
) json.RawMessage {
	t.Helper()
	severity := "low"
	switch {
	case counts.Blocker > 0:
		severity = "blocker"
	case counts.High > 0:
		severity = "high"
	case counts.Medium > 0:
		severity = "medium"
	}
	issueCount := counts.Blocker + counts.High + counts.Medium + counts.Low
	issues := make([]map[string]any, 0, issueCount)
	for index := 0; index < issueCount; index++ {
		issues = append(issues, map[string]any{
			"issue_id":          "ISSUE-1",
			"severity":          severity,
			"episode_nos":       []int{1},
			"category":          "continuity",
			"evidence":          "第1集场次1-1",
			"impact":            "影响上下集承接",
			"must_preserve":     []string{},
			"revision_target":   "修复承接关系",
			"recommended_route": "script_generation",
		})
	}
	return mustJSON(t, map[string]any{
		"review_version":       1,
		"input_snapshot_hash":  inputHash,
		"scope":                "full_script",
		"issue_counts":         counts,
		"issues":               issues,
		"recommended_route":    "script_generation",
		"affected_episode_nos": []int{1},
		"compliance_status":    "NEEDS_POLICY",
	})
}
