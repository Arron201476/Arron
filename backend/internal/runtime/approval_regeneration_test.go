package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestRequestApprovalRegenerationPlansTargetBeforeDownstream(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, initial, _, storyVersion := prepareStoryBibleForImpact(t, store, "approval regeneration")
	addConfirmedImpactDownstream(t, store, project.ProjectID, initial.Run.RunID, storyVersion.ArtifactVersionID)
	snapshot, err := store.GetRunSnapshot(ctx, initial.Run.RunID)
	if err != nil {
		t.Fatalf("GetRunSnapshot() error = %v", err)
	}
	approval := snapshot.CurrentApproval
	if approval == nil {
		t.Fatal("initial run has no approval")
	}
	command := RequestApprovalRegenerationCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "request_approval_regeneration",
			IdempotencyKey: "a1111111-1111-4111-8111-111111111111",
			RequestHash:    "approval_regeneration_v1",
		},
		ApprovalRequestID:       approval.ApprovalRequestID,
		ExpectedApprovalVersion: approval.Version,
		SubjectSnapshotHash:     approval.SubjectSnapshotHash,
		Action:                  "regenerate_artifact",
	}

	result, err := store.RequestApprovalRegeneration(ctx, command)
	if err != nil {
		t.Fatalf("RequestApprovalRegeneration() error = %v", err)
	}
	if result.RegenerationPlan.Status != "pending" || len(result.RegenerationPlan.Groups) == 0 {
		t.Fatalf("regeneration plan = %+v", result.RegenerationPlan)
	}
	if got := result.RegenerationPlan.Groups[0].StepID; got != "build_story_bible" {
		t.Fatalf("first regeneration step = %q, want target step build_story_bible", got)
	}
	if len(result.RegenerationPlan.Groups) < 2 || result.RegenerationPlan.Groups[1].StepID != "split_episodes" {
		t.Fatalf("downstream regeneration groups = %+v", result.RegenerationPlan.Groups)
	}
	if result.RunSnapshot.Run.Status != "paused" {
		t.Fatalf("run status = %q, want paused", result.RunSnapshot.Run.Status)
	}
	resolved, err := store.GetApproval(ctx, approval.ApprovalRequestID)
	if err != nil {
		t.Fatalf("GetApproval() error = %v", err)
	}
	if resolved.Status != "approved" {
		t.Fatalf("approval status = %q, want approved", resolved.Status)
	}

	repeated, err := store.RequestApprovalRegeneration(ctx, command)
	if err != nil {
		t.Fatalf("RequestApprovalRegeneration() retry error = %v", err)
	}
	if repeated.RegenerationPlan.RegenerationPlanID != result.RegenerationPlan.RegenerationPlanID {
		t.Fatalf("idempotent retry changed plan: first=%q retry=%q",
			result.RegenerationPlan.RegenerationPlanID, repeated.RegenerationPlan.RegenerationPlanID)
	}
}

func TestRequestApprovalRegenerationAllocatesAfterHistoricalVersion(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, initial, _, storyVersion := prepareStoryBibleForImpact(t, store, "approval regeneration history")
	var artifactID string
	if err := store.db.QueryRowContext(ctx, `
		SELECT artifact_id FROM artifact_versions WHERE artifact_version_id = ?`,
		storyVersion.ArtifactVersionID,
	).Scan(&artifactID); err != nil {
		t.Fatalf("load artifact id error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason, base_version_id, created_at
		) SELECT 'av_historical_v2', artifact_id, 2, 'superseded', payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, 'regeneration', artifact_version_id, created_at
		FROM artifact_versions WHERE artifact_version_id = ?`, storyVersion.ArtifactVersionID); err != nil {
		t.Fatalf("insert historical version error = %v", err)
	}

	snapshot, err := store.GetRunSnapshot(ctx, initial.Run.RunID)
	if err != nil {
		t.Fatalf("GetRunSnapshot() error = %v", err)
	}
	approval := snapshot.CurrentApproval
	result, err := store.RequestApprovalRegeneration(ctx, RequestApprovalRegenerationCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "request_approval_regeneration",
			IdempotencyKey: "a3333333-3333-4333-8333-333333333333",
			RequestHash:    "approval_regeneration_history_v1",
		},
		ApprovalRequestID:       approval.ApprovalRequestID,
		ExpectedApprovalVersion: approval.Version,
		SubjectSnapshotHash:     approval.SubjectSnapshotHash,
		Action:                  "regenerate_artifact",
	})
	if err != nil {
		t.Fatalf("RequestApprovalRegeneration() error = %v", err)
	}
	if result.ImpactReview.NewVersionID == "" {
		t.Fatal("regeneration did not create a replacement version")
	}
	var version int
	if err := store.db.QueryRowContext(ctx, `
		SELECT version FROM artifact_versions WHERE artifact_version_id = ?`,
		result.ImpactReview.NewVersionID,
	).Scan(&version); err != nil {
		t.Fatalf("load replacement version error = %v", err)
	}
	if version != 3 {
		t.Fatalf("replacement version = %d, want 3 for artifact %s", version, artifactID)
	}
}

func TestRequestApprovalAIRevisionAttachesInstruction(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, initial, _, _ := prepareStoryBibleForImpact(t, store, "approval ai revision")
	snapshot, err := store.GetRunSnapshot(ctx, initial.Run.RunID)
	if err != nil {
		t.Fatalf("GetRunSnapshot() error = %v", err)
	}
	approval := snapshot.CurrentApproval
	result, err := store.RequestApprovalRegeneration(ctx, RequestApprovalRegenerationCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "request_approval_regeneration",
			IdempotencyKey: "a2222222-2222-4222-8222-222222222222",
			RequestHash:    "approval_ai_revision_v1",
		},
		ApprovalRequestID:       approval.ApprovalRequestID,
		ExpectedApprovalVersion: approval.Version,
		SubjectSnapshotHash:     approval.SubjectSnapshotHash,
		Action:                  "request_ai_revision",
		Instruction:             "保留人物关系，强化开场冲突。",
	})
	if err != nil {
		t.Fatalf("RequestApprovalRegeneration() error = %v", err)
	}
	group := result.RegenerationPlan.Groups[0]
	if group.StepRunID == nil {
		t.Fatal("first regeneration group has no step run")
	}
	var cursor map[string]any
	for _, step := range result.RunSnapshot.Steps {
		if step.StepRunID == *group.StepRunID {
			if err := json.Unmarshal(step.TaskCursor, &cursor); err != nil {
				t.Fatalf("decode task cursor error = %v", err)
			}
			break
		}
	}
	if got := cursor["revision_instruction"]; got != "保留人物关系，强化开场冲突。" {
		t.Fatalf("revision instruction = %#v", got)
	}
}

func TestRequestApprovalRevisionCreatesSDKProposalRequestWithoutRegeneration(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, initial, _, storyVersion := prepareStoryBibleForImpact(t, store, "approval SDK revision")
	snapshot, err := store.GetRunSnapshot(ctx, initial.Run.RunID)
	if err != nil {
		t.Fatalf("GetRunSnapshot() error = %v", err)
	}
	approval := snapshot.CurrentApproval
	revision, err := store.RequestApprovalRevision(ctx, RequestApprovalRegenerationCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "request_approval_revision",
			IdempotencyKey: "a5555555-5555-4555-8555-555555555555", RequestHash: "approval_sdk_revision_v1",
		},
		ApprovalRequestID: approval.ApprovalRequestID, ExpectedApprovalVersion: approval.Version,
		SubjectSnapshotHash: approval.SubjectSnapshotHash, Action: "request_ai_revision",
		Instruction: "只把主角职业改为律师。",
	})
	if err != nil {
		t.Fatalf("RequestApprovalRevision() error = %v", err)
	}
	if revision.Status != "queued" || revision.BaseVersionID == nil || *revision.BaseVersionID != storyVersion.ArtifactVersionID {
		t.Fatalf("revision = %+v", revision)
	}
	currentApproval, err := store.GetApproval(ctx, approval.ApprovalRequestID)
	if err != nil || currentApproval.Status != "pending" {
		t.Fatalf("approval = %+v, error = %v", currentApproval, err)
	}
	var regenerationPlans int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM regeneration_plans WHERE run_id = ?`, initial.Run.RunID).Scan(&regenerationPlans); err != nil {
		t.Fatalf("count regeneration plans: %v", err)
	}
	if regenerationPlans != 0 {
		t.Fatalf("regeneration plans = %d, want 0", regenerationPlans)
	}
	var artifactVersions int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_versions WHERE artifact_id = (SELECT artifact_id FROM artifact_versions WHERE artifact_version_id = ?)`, storyVersion.ArtifactVersionID).Scan(&artifactVersions); err != nil {
		t.Fatalf("count artifact versions: %v", err)
	}
	if artifactVersions != 1 {
		t.Fatalf("artifact versions = %d, want unchanged base only", artifactVersions)
	}
}

func TestRequestTargetedRegenerationUsesArtifactScopeWithoutRevisionProposal(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, initial, _, storyVersion := prepareStoryBibleForImpact(t, store, "targeted regeneration")
	var artifactID string
	if err := store.db.QueryRowContext(ctx, `
		SELECT artifact_id FROM artifact_versions WHERE artifact_version_id = ?`,
		storyVersion.ArtifactVersionID,
	).Scan(&artifactID); err != nil {
		t.Fatalf("load artifact id error = %v", err)
	}
	result, err := store.RequestTargetedRegeneration(ctx, RequestTargetedRegenerationCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "request_targeted_regeneration",
			IdempotencyKey: "a4444444-4444-4444-8444-444444444444", RequestHash: "targeted_regeneration_v1",
		},
		ProjectID: project.ProjectID, ArtifactID: artifactID, Instruction: "重新跑当前产物",
	})
	if err != nil {
		t.Fatalf("RequestTargetedRegeneration() error = %v", err)
	}
	if len(result.RegenerationPlan.Groups) == 0 || result.RegenerationPlan.Groups[0].StepID != "build_story_bible" {
		t.Fatalf("regeneration plan = %+v", result.RegenerationPlan)
	}
	if got := result.RegenerationPlan.Groups[0].TaskKeys; len(got) != 1 || got[0] != "singleton" {
		t.Fatalf("targeted task keys = %#v", got)
	}
	var revisions int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM revision_requests WHERE project_id = ?`, project.ProjectID).Scan(&revisions); err != nil {
		t.Fatalf("count revisions error = %v", err)
	}
	if revisions != 0 {
		t.Fatalf("targeted regeneration created %d revision requests", revisions)
	}
	if result.RunSnapshot.Run.RunID != initial.Run.RunID || result.RunSnapshot.Run.Status != "paused" {
		t.Fatalf("run snapshot = %+v", result.RunSnapshot.Run)
	}
	_, conflictErr := store.RequestTargetedRegeneration(ctx, RequestTargetedRegenerationCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "request_targeted_regeneration",
			IdempotencyKey: "a4544444-4444-4444-8444-444444444444", RequestHash: "targeted_regeneration_conflict_v1",
		},
		ProjectID: project.ProjectID, ArtifactID: artifactID, Instruction: "再次重新跑当前产物",
	})
	var conflict *DomainError
	if !errors.As(conflictErr, &conflict) || conflict.Code != "REGENERATION_PLAN_CONFLICT" ||
		conflict.Message != "当前重生成仍在执行，请等待完成或先暂停任务。" {
		t.Fatalf("regeneration conflict = %#v, error = %v", conflict, conflictErr)
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE regeneration_plans SET status = 'waiting_approval' WHERE regeneration_plan_id = ?;
		UPDATE regeneration_plan_groups SET status = 'waiting_approval' WHERE regeneration_plan_id = ?;`,
		result.RegenerationPlan.RegenerationPlanID, result.RegenerationPlan.RegenerationPlanID); err != nil {
		t.Fatalf("mark regeneration waiting approval error = %v", err)
	}
	replaced, err := store.RequestTargetedRegeneration(ctx, RequestTargetedRegenerationCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "request_targeted_regeneration",
			IdempotencyKey: "a4644444-4444-4444-8444-444444444444", RequestHash: "targeted_regeneration_replace_v1",
		},
		ProjectID: project.ProjectID, ArtifactID: artifactID, Instruction: "替换上一份未确认候选稿",
	})
	if err != nil {
		t.Fatalf("replace waiting regeneration error = %v", err)
	}
	var replacedPlanStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM regeneration_plans WHERE regeneration_plan_id = ?`, result.RegenerationPlan.RegenerationPlanID).Scan(&replacedPlanStatus); err != nil {
		t.Fatalf("load replaced regeneration plan error = %v", err)
	}
	if replacedPlanStatus != "cancelled" {
		t.Fatalf("replaced regeneration status = %q, want cancelled", replacedPlanStatus)
	}
	result = replaced
	cancelled, err := store.CancelRun(ctx, CancelRunCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "cancel_run",
			IdempotencyKey: "a5444444-4444-4444-8444-444444444444", RequestHash: "cancel_targeted_regeneration_v1",
		},
		RunID: initial.Run.RunID, Confirmed: true, ActorRef: "test_user",
	})
	if err != nil || cancelled.Run.Status != "cancelled" {
		t.Fatalf("CancelRun(targeted regeneration) = %+v, error = %v", cancelled.Run, err)
	}
	var planStatus, groupStatus, restoredStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM regeneration_plans WHERE regeneration_plan_id = ?`, result.RegenerationPlan.RegenerationPlanID).Scan(&planStatus); err != nil {
		t.Fatalf("load cancelled regeneration plan error = %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM regeneration_plan_groups WHERE regeneration_plan_id = ?`, result.RegenerationPlan.RegenerationPlanID).Scan(&groupStatus); err != nil {
		t.Fatalf("load cancelled regeneration group error = %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM artifact_versions WHERE artifact_version_id = ?`, storyVersion.ArtifactVersionID).Scan(&restoredStatus); err != nil {
		t.Fatalf("load restored artifact version error = %v", err)
	}
	if planStatus != "cancelled" || groupStatus != "cancelled" || restoredStatus != "confirmed" {
		t.Fatalf("cancelled regeneration state = plan:%s group:%s artifact:%s", planStatus, groupStatus, restoredStatus)
	}
	result, err = store.RequestTargetedRegeneration(ctx, RequestTargetedRegenerationCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "request_targeted_regeneration",
			IdempotencyKey: "a5544444-4444-4444-8444-444444444444", RequestHash: "targeted_regeneration_after_cancel_v1",
		},
		ProjectID: project.ProjectID, ArtifactID: artifactID, Instruction: "再次重新跑当前产物",
	})
	if err != nil {
		t.Fatalf("RequestTargetedRegeneration(after cancel) error = %v", err)
	}
	resumed, err := store.ResumeRun(ctx, ResumeRunCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "resume_run",
			IdempotencyKey: "a5555555-5555-4555-8555-555555555555", RequestHash: "resume_targeted_regeneration_v1",
		},
		RunID: initial.Run.RunID,
	})
	if err != nil {
		t.Fatalf("ResumeRun(targeted regeneration) error = %v", err)
	}
	if resumed.Run.Status != "running" {
		t.Fatalf("resumed targeted regeneration run = %+v", resumed.Run)
	}
}

func TestCancelFailedTargetedRegenerationRestoresStaleVersion(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, initial, _, storyVersion := prepareStoryBibleForImpact(t, store, "failed targeted regeneration")
	var artifactID string
	if err := store.db.QueryRowContext(ctx, `
		SELECT artifact_id FROM artifact_versions WHERE artifact_version_id = ?`,
		storyVersion.ArtifactVersionID,
	).Scan(&artifactID); err != nil {
		t.Fatalf("load artifact id error = %v", err)
	}
	result, err := store.RequestTargetedRegeneration(ctx, RequestTargetedRegenerationCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "request_targeted_regeneration",
			IdempotencyKey: "b4444444-4444-4444-8444-444444444444", RequestHash: "failed_targeted_regeneration_v1",
		},
		ProjectID: project.ProjectID, ArtifactID: artifactID, Instruction: "重新跑当前产物",
	})
	if err != nil {
		t.Fatalf("RequestTargetedRegeneration() error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE regeneration_plan_groups SET status = 'failed' WHERE regeneration_plan_id = ?;
		UPDATE regeneration_plans SET status = 'failed' WHERE regeneration_plan_id = ?;
		UPDATE runs SET status = 'failed' WHERE run_id = ?;
		UPDATE projects SET status = 'failed' WHERE project_id = ?;`,
		result.RegenerationPlan.RegenerationPlanID,
		result.RegenerationPlan.RegenerationPlanID,
		initial.Run.RunID,
		project.ProjectID,
	); err != nil {
		t.Fatalf("simulate failed regeneration error = %v", err)
	}

	cancelled, err := store.CancelRun(ctx, CancelRunCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "cancel_run",
			IdempotencyKey: "b5444444-4444-4444-8444-444444444444", RequestHash: "cancel_failed_targeted_regeneration_v1",
		},
		RunID: initial.Run.RunID, Confirmed: true, ActorRef: "test_user",
	})
	if err != nil || cancelled.Run.Status != "cancelled" {
		t.Fatalf("CancelRun(failed targeted regeneration) = %+v, error = %v", cancelled.Run, err)
	}
	var planStatus, groupStatus, restoredStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM regeneration_plans WHERE regeneration_plan_id = ?`, result.RegenerationPlan.RegenerationPlanID).Scan(&planStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM regeneration_plan_groups WHERE regeneration_plan_id = ?`, result.RegenerationPlan.RegenerationPlanID).Scan(&groupStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM artifact_versions WHERE artifact_version_id = ?`, storyVersion.ArtifactVersionID).Scan(&restoredStatus); err != nil {
		t.Fatal(err)
	}
	if planStatus != "cancelled" || groupStatus != "cancelled" || restoredStatus != "confirmed" {
		t.Fatalf("cancelled failed regeneration state = plan:%s group:%s artifact:%s", planStatus, groupStatus, restoredStatus)
	}
}

func TestRetryFailedTargetedRegenerationReactivatesCurrentPlanGroup(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, initial, _, storyVersion := prepareStoryBibleForImpact(t, store, "retry targeted regeneration")
	var artifactID string
	if err := store.db.QueryRowContext(ctx, `
		SELECT artifact_id FROM artifact_versions WHERE artifact_version_id = ?`,
		storyVersion.ArtifactVersionID,
	).Scan(&artifactID); err != nil {
		t.Fatal(err)
	}
	result, err := store.RequestTargetedRegeneration(ctx, RequestTargetedRegenerationCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "request_targeted_regeneration",
			IdempotencyKey: "b6444444-4444-4444-8444-444444444444", RequestHash: "retry_targeted_regeneration_v1",
		},
		ProjectID: project.ProjectID, ArtifactID: artifactID, Instruction: "修改当前产物",
	})
	if err != nil {
		t.Fatalf("RequestTargetedRegeneration() error = %v", err)
	}
	var stepRunID, taskItemID string
	if err := store.db.QueryRowContext(ctx, `
		SELECT rpg.step_run_id, ti.task_item_id
		FROM regeneration_plan_groups rpg
		JOIN task_items ti ON ti.step_run_id = rpg.step_run_id
		WHERE rpg.regeneration_plan_id = ? LIMIT 1`,
		result.RegenerationPlan.RegenerationPlanID,
	).Scan(&stepRunID, &taskItemID); err != nil {
		t.Fatal(err)
	}
	updates := []struct {
		query string
		args  []any
	}{
		{`UPDATE task_items SET status = 'failed', failure = 'OUTPUT_REPAIR_FAILED' WHERE task_item_id = ?`, []any{taskItemID}},
		{`UPDATE step_runs SET status = 'failed' WHERE step_run_id = ?`, []any{stepRunID}},
		{`UPDATE regeneration_plan_groups SET status = 'failed' WHERE regeneration_plan_id = ?`, []any{result.RegenerationPlan.RegenerationPlanID}},
		{`UPDATE regeneration_plans SET status = 'failed' WHERE regeneration_plan_id = ?`, []any{result.RegenerationPlan.RegenerationPlanID}},
		{`UPDATE runs SET status = 'failed', current_step_run_id = ? WHERE run_id = ?`, []any{stepRunID, initial.Run.RunID}},
		{`UPDATE projects SET status = 'failed' WHERE project_id = ?`, []any{project.ProjectID}},
	}
	for _, update := range updates {
		if _, err := store.db.ExecContext(ctx, update.query, update.args...); err != nil {
			t.Fatal(err)
		}
	}
	var failedRunStatus, failedStepStatus string
	var failedCurrentStep sql.NullString
	if err := store.db.QueryRowContext(ctx, `
		SELECT r.status, r.current_step_run_id, sr.status
		FROM runs r JOIN step_runs sr ON sr.run_id = r.run_id
		WHERE r.run_id = ? AND sr.step_run_id = ?`,
		initial.Run.RunID, stepRunID,
	).Scan(&failedRunStatus, &failedCurrentStep, &failedStepStatus); err != nil {
		t.Fatal(err)
	}
	if failedRunStatus != "failed" || failedStepStatus != "failed" ||
		!failedCurrentStep.Valid || failedCurrentStep.String != stepRunID {
		t.Fatalf("simulated retry state = run:%s current:%v step:%s want step:%s",
			failedRunStatus, failedCurrentStep, failedStepStatus, stepRunID)
	}

	if _, err := store.RetryFailedStep(ctx, RetryFailedStepCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "retry_failed_step",
			IdempotencyKey: "b7444444-4444-4444-8444-444444444444", RequestHash: "retry_targeted_regeneration_step_v1",
		},
		StepRunID: stepRunID,
	}); err != nil {
		t.Fatalf("RetryFailedStep() error = %v", err)
	}
	var planStatus, groupStatus string
	if err := store.db.QueryRowContext(ctx, `
		SELECT rp.status, rpg.status
		FROM regeneration_plans rp
		JOIN regeneration_plan_groups rpg ON rpg.regeneration_plan_id = rp.regeneration_plan_id
		WHERE rp.regeneration_plan_id = ?`,
		result.RegenerationPlan.RegenerationPlanID,
	).Scan(&planStatus, &groupStatus); err != nil {
		t.Fatal(err)
	}
	if planStatus != "running" || groupStatus != "running" {
		t.Fatalf("retried regeneration state = plan:%s group:%s", planStatus, groupStatus)
	}
}

func TestCursorWithRevisionInstructionPreservesVideoContext(t *testing.T) {
	encoded, err := cursorWithRevisionInstruction(`{
		"regeneration_plan_id":"rgn_1",
		"video":{"asset_id":"asset_1","asset_snapshot_id":"snapshot_1","episode_no":1}
	}`, "重新跑第1集")
	if err != nil {
		t.Fatalf("cursorWithRevisionInstruction() error = %v", err)
	}
	var cursor struct {
		RevisionInstruction string `json:"revision_instruction"`
		Video               struct {
			AssetID   string `json:"asset_id"`
			EpisodeNo int    `json:"episode_no"`
		} `json:"video"`
	}
	if err := json.Unmarshal([]byte(encoded), &cursor); err != nil {
		t.Fatalf("decode cursor error = %v", err)
	}
	if cursor.RevisionInstruction != "重新跑第1集" || cursor.Video.AssetID != "asset_1" || cursor.Video.EpisodeNo != 1 {
		t.Fatalf("merged cursor = %+v", cursor)
	}
}
