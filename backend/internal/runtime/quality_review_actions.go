package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"

	"content-agent/backend/internal/capability"
)

func (s *Store) resolveQualityReviewRegeneration(
	ctx context.Context,
	command ResolveQualityReviewActionCommand,
) (QualityReviewActionResult, error) {
	if strings.TrimSpace(command.Instruction) == "" {
		return QualityReviewActionResult{}, domainError("REQUEST_VALIDATION_FAILED", "质量审核返修必须说明处理要求。")
	}
	if command.ActorRef == "" {
		command.ActorRef = actorRefFromContext(ctx)
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QualityReviewActionResult{}, err
	}
	defer tx.Rollback()

	review, err := getQualityReviewTx(ctx, tx, command.QualityReviewID)
	if err != nil {
		return QualityReviewActionResult{}, normalizeQualityReviewNotFound(err)
	}
	if command.Scope == "" {
		command.Scope = review.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return QualityReviewActionResult{}, err
	}
	if hit {
		return decodeIdempotentResult[QualityReviewActionResult](cached)
	}
	if review.Status != command.ExpectedReviewStatus || review.Status != "action_required" {
		return QualityReviewActionResult{}, domainError("QUALITY_REVIEW_STATUS_CONFLICT", "质量审核状态已经变化。")
	}
	if review.InputSnapshotHash != command.InputSnapshotHash {
		return QualityReviewActionResult{}, domainError("QUALITY_REVIEW_INPUT_CHANGED", "质量审核输入快照已经变化。")
	}
	if review.RecommendedRoute == nil {
		return QualityReviewActionResult{}, domainError("QUALITY_REVIEW_ACTION_NOT_ALLOWED", "质量审核缺少唯一返工路由。")
	}
	if command.Action == "confirm_change" && *review.RecommendedRoute != "user" {
		return QualityReviewActionResult{}, domainError("QUALITY_REVIEW_ACTION_NOT_ALLOWED", "当前问题不需要补充或确认故事变更。")
	}
	if command.Action == "ai_revise" && *review.RecommendedRoute == "user" {
		return QualityReviewActionResult{}, domainError("QUALITY_REVIEW_ACTION_NOT_ALLOWED", "当前问题需要先由用户确认故事变更。")
	}
	var approvalID string
	if err := tx.QueryRowContext(ctx, `
		SELECT approval_request_id FROM approvals
		WHERE subject_kind = 'quality_review' AND subject_ref_id = ? AND status = 'pending'
		ORDER BY requested_at DESC LIMIT 1`, review.QualityReviewID).Scan(&approvalID); err != nil {
		if err == sql.ErrNoRows {
			return QualityReviewActionResult{}, domainError("QUALITY_REVIEW_STATUS_CONFLICT", "质量审核确认请求已经变化。")
		}
		return QualityReviewActionResult{}, err
	}
	approval, err := getApprovalTx(ctx, tx, approvalID)
	if err != nil {
		return QualityReviewActionResult{}, err
	}
	if !slices.Contains(approval.Options, command.Action) {
		return QualityReviewActionResult{}, domainError("QUALITY_REVIEW_ACTION_NOT_ALLOWED", "当前质量审核不允许该处理方式。")
	}
	run, err := getRunTx(ctx, tx, review.RunID)
	if err != nil {
		return QualityReviewActionResult{}, err
	}
	if run.Status != "waiting_approval" {
		return QualityReviewActionResult{}, domainError("RUN_STATE_CONFLICT", "当前生成任务不在质量审核待处理状态。")
	}

	entry, ok, registryErr := s.capabilityEntryForRunQuery(
		ctx, tx, run.RunID, run.CapabilityID,
	)
	if registryErr != nil {
		return QualityReviewActionResult{}, registryErr
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil ||
		entry.Definition.Version != run.CapabilityVersion {
		return QualityReviewActionResult{}, domainError(
			"CAPABILITY_VERSION_UNAVAILABLE",
			"质量审核对应的 Skill 版本当前不可用。",
		)
	}
	targetStep, err := qualityReviewTargetStep(entry.Definition, *review.RecommendedRoute)
	if err != nil {
		return QualityReviewActionResult{}, err
	}
	targets, err := qualityReviewRegenerationTargetsTx(ctx, tx, review, *targetStep)
	if err != nil {
		return QualityReviewActionResult{}, err
	}
	if len(targets) == 0 {
		return QualityReviewActionResult{}, domainError("QUALITY_REVIEW_ACTION_NOT_ALLOWED", "返工路由没有找到可重生成的当前产物。")
	}
	regenerationCommand := RequestApprovalRegenerationCommand{
		Action:      command.Action,
		Instruction: strings.TrimSpace(command.Instruction),
		ActorRef:    command.ActorRef,
	}
	reviewApproval := Approval{ProjectID: review.ProjectID, RunID: review.RunID}
	impactReview, source, err := s.createApprovalRegenerationReviewTx(
		ctx, tx, reviewApproval, targets, regenerationCommand, now,
	)
	if err != nil {
		return QualityReviewActionResult{}, err
	}
	resolution, _ := json.Marshal(map[string]any{
		"action": command.Action, "instruction": strings.TrimSpace(command.Instruction),
		"quality_review_id": review.QualityReviewID,
	})
	if err := updateExactlyOne(ctx, tx, `
		UPDATE approvals SET status = 'approved', resolved_at = ?, resolution_json = ?, actor_ref = ?
		WHERE approval_request_id = ? AND status = 'pending'`,
		"质量审核确认请求已经变化。", formatTime(now), string(resolution), command.ActorRef, approvalID); err != nil {
		return QualityReviewActionResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE quality_reviews SET status = 'superseded', finished_at = ?
		WHERE quality_review_id = ? AND status = 'action_required'`,
		"质量审核状态已经变化。", formatTime(now), review.QualityReviewID); err != nil {
		return QualityReviewActionResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE step_runs SET status = 'superseded', ended_at = COALESCE(ended_at, ?)
		WHERE step_run_id = ? AND status = 'waiting_approval'`, formatTime(now), review.StepRunID); err != nil {
		return QualityReviewActionResult{}, err
	}
	plan, err := s.planDownstreamRegenerationTx(ctx, tx, impactReview, source, run, now)
	if err != nil {
		return QualityReviewActionResult{}, err
	}
	contextInstruction := strings.TrimSpace(command.Instruction) + "\n\n质量审核结果：" + string(review.Result)
	if err := attachRevisionInstructionTx(ctx, tx, plan, contextInstruction); err != nil {
		return QualityReviewActionResult{}, err
	}
	runRef, stepRef := review.RunID, review.StepRunID
	if _, err := s.appendEvent(ctx, tx, review.ProjectID, &runRef, &stepRef,
		"quality_review.superseded", "quality_review", review.QualityReviewID,
		map[string]any{"action": command.Action, "regeneration_plan_id": plan.RegenerationPlanID}); err != nil {
		return QualityReviewActionResult{}, err
	}
	resolvedReview, err := getQualityReviewTx(ctx, tx, review.QualityReviewID)
	if err != nil {
		return QualityReviewActionResult{}, err
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, review.RunID)
	if err != nil {
		return QualityReviewActionResult{}, err
	}
	result := QualityReviewActionResult{
		Review: resolvedReview, ImpactReview: &impactReview,
		RegenerationPlan: plan, RunSnapshot: &snapshot,
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, result, now); err != nil {
		return QualityReviewActionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return QualityReviewActionResult{}, err
	}
	return result, nil
}

func qualityReviewTargetStep(
	definition *capability.CompiledDefinition,
	route string,
) (*capability.CompiledStep, error) {
	if definition != nil {
		for index := range definition.Steps {
			if slices.Contains(definition.Steps[index].QualityReviewRoutes, route) {
				return &definition.Steps[index], nil
			}
		}
	}
	return nil, domainError("QUALITY_REVIEW_ACTION_NOT_ALLOWED", "质量审核返工路由无法映射到当前能力步骤。")
}

func qualityReviewRegenerationTargetsTx(
	ctx context.Context,
	tx *sql.Tx,
	review QualityReview,
	step capability.CompiledStep,
) ([]regenerationTarget, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT a.artifact_id, a.project_id, a.run_id, a.step_run_id, a.capability_id,
			a.artifact_type, a.scope_key, a.current_version_id,
			av.artifact_version_id, av.version, av.status, av.payload_json, av.schema_id,
			av.schema_version, av.created_by_kind, av.actor_ref, av.creation_reason,
			av.base_version_id
		FROM artifacts a
		JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
		JOIN step_runs sr ON sr.step_run_id = a.step_run_id
		WHERE a.run_id = ? AND sr.step_id = ? AND av.status IN ('confirmed','pending_approval')
		ORDER BY a.updated_at DESC, a.scope_key ASC, a.artifact_type ASC`, review.RunID, step.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	episodes := map[int]bool{}
	for _, episodeNo := range review.AffectedEpisodeNos {
		episodes[episodeNo] = true
	}
	seen := map[string]bool{}
	var targets []regenerationTarget
	for rows.Next() {
		var target regenerationTarget
		var payload string
		var baseVersionID sql.NullString
		if err := rows.Scan(
			&target.Artifact.ArtifactID, &target.Artifact.ProjectID, &target.Artifact.RunID,
			&target.Artifact.StepRunID, &target.Artifact.CapabilityID, &target.Artifact.ArtifactType,
			&target.Artifact.ScopeKey, &target.Artifact.CurrentVersionID,
			&target.CurrentVersion.ArtifactVersionID, &target.CurrentVersion.Version,
			&target.CurrentVersion.Status, &payload, &target.CurrentVersion.SchemaID,
			&target.CurrentVersion.SchemaVersion, &target.CurrentVersion.CreatedByKind,
			&target.CurrentVersion.ActorRef, &target.CurrentVersion.CreationReason, &baseVersionID,
		); err != nil {
			return nil, err
		}
		if isScriptBundleStep(step) && len(episodes) > 0 {
			episodeNo, err := episodeNumberFromScope(target.Artifact.ScopeKey)
			if err != nil || !episodes[episodeNo] {
				continue
			}
		}
		key := target.Artifact.ArtifactType + "\x00" + target.Artifact.ScopeKey
		if seen[key] {
			continue
		}
		seen[key] = true
		target.CurrentVersion.Payload = json.RawMessage(payload)
		target.StepID = step.ID
		targets = append(targets, target)
	}
	return targets, rows.Err()
}
