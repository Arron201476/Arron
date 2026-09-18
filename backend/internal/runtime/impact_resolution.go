package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"content-agent/backend/internal/capability"
)

func (s *Store) ResolveImpactReview(
	ctx context.Context,
	command ResolveImpactReviewCommand,
) (ImpactReviewResolutionResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImpactReviewResolutionResult{}, err
	}
	defer tx.Rollback()
	result, err := s.resolveImpactReviewTx(ctx, tx, command)
	if err != nil {
		return ImpactReviewResolutionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ImpactReviewResolutionResult{}, err
	}
	return result, nil
}

func (s *Store) resolveImpactReviewTx(ctx context.Context, tx *sql.Tx, command ResolveImpactReviewCommand) (ImpactReviewResolutionResult, error) {
	if command.ImpactReviewID == "" || command.SnapshotHash == "" {
		return ImpactReviewResolutionResult{}, domainError(
			"REQUEST_VALIDATION_FAILED",
			"影响决策缺少预览或快照摘要。",
		)
	}
	if command.Action != "keep_downstream" &&
		command.Action != "regenerate_downstream" {
		return ImpactReviewResolutionResult{}, domainError(
			"REQUEST_VALIDATION_FAILED",
			"影响决策动作不受支持。",
		)
	}
	if command.ActorRef == "" {
		command.ActorRef = actorRefFromContext(ctx)
	}
	now := s.now()

	review, err := getImpactReviewTx(ctx, tx, command.ImpactReviewID)
	if err != nil {
		return ImpactReviewResolutionResult{}, err
	}
	if command.Scope == "" {
		command.Scope = review.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return ImpactReviewResolutionResult{}, err
	}
	if hit {
		return decodeIdempotentResult[ImpactReviewResolutionResult](cached)
	}
	source, approval, run, err := s.validateImpactReviewResolutionTx(
		ctx,
		tx,
		review,
		command.SnapshotHash,
	)
	if err != nil {
		return ImpactReviewResolutionResult{}, err
	}

	preservedIDs := []string{}
	staleIDs := []string{}
	for _, item := range review.AffectedItems {
		if command.Action == "keep_downstream" {
			preservedIDs = append(preservedIDs, item.ArtifactVersionID)
		} else {
			staleIDs = append(staleIDs, item.ArtifactVersionID)
		}
	}
	decision, err := s.createDependencyDecisionTx(
		ctx,
		tx,
		review,
		command.Action,
		preservedIDs,
		staleIDs,
		command.ActorRef,
		now,
	)
	if err != nil {
		return ImpactReviewResolutionResult{}, err
	}
	if err := confirmImpactSourceAndResolveApprovalTx(
		ctx,
		tx,
		review,
		approval,
		command.Action,
		command.ActorRef,
		now,
	); err != nil {
		return ImpactReviewResolutionResult{}, err
	}

	var plan *RegenerationPlan
	switch command.Action {
	case "keep_downstream":
		if err := s.finishKeepDownstreamTx(ctx, tx, review, run, now); err != nil {
			return ImpactReviewResolutionResult{}, err
		}
	case "regenerate_downstream":
		plan, err = s.planDownstreamRegenerationTx(
			ctx,
			tx,
			review,
			source,
			run,
			now,
		)
		if err != nil {
			return ImpactReviewResolutionResult{}, err
		}
	}

	runRef := review.RunID
	eventStepRef := run.CurrentStepRunID
	if _, err := s.appendEvent(
		ctx,
		tx,
		review.ProjectID,
		&runRef,
		eventStepRef,
		"approval.resolved",
		"approval",
		approval.ApprovalRequestID,
		map[string]any{"action": command.Action},
	); err != nil {
		return ImpactReviewResolutionResult{}, err
	}
	if _, err := s.appendEvent(
		ctx,
		tx,
		review.ProjectID,
		&runRef,
		eventStepRef,
		"artifact.confirmed",
		"artifact_version",
		review.NewVersionID,
		nil,
	); err != nil {
		return ImpactReviewResolutionResult{}, err
	}
	eventType := "downstream.kept"
	subjectType := "dependency_decision"
	subjectID := decision.DependencyDecisionID
	if plan != nil {
		eventType = "downstream.regeneration_planned"
		subjectType = "regeneration_plan"
		subjectID = plan.RegenerationPlanID
	}
	if _, err := s.appendEvent(
		ctx,
		tx,
		review.ProjectID,
		&runRef,
		eventStepRef,
		eventType,
		subjectType,
		subjectID,
		map[string]any{
			"impact_review_id": review.ImpactReviewID,
			"affected_count":   len(review.AffectedItems),
		},
	); err != nil {
		return ImpactReviewResolutionResult{}, err
	}

	resolvedReview, err := getImpactReviewTx(ctx, tx, review.ImpactReviewID)
	if err != nil {
		return ImpactReviewResolutionResult{}, err
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, review.RunID)
	if err != nil {
		return ImpactReviewResolutionResult{}, err
	}
	result := ImpactReviewResolutionResult{
		ImpactReview:     resolvedReview,
		Decision:         decision,
		RegenerationPlan: plan,
		RunSnapshot:      snapshot,
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, result, now); err != nil {
		return ImpactReviewResolutionResult{}, err
	}
	return result, nil
}

func (s *Store) GetRegenerationPlan(
	ctx context.Context,
	regenerationPlanID string,
) (RegenerationPlan, error) {
	plan, err := scanRegenerationPlan(s.db.QueryRowContext(ctx, `
		SELECT regeneration_plan_id, project_id, run_id, impact_review_id,
			source_new_version_id, status, current_group_order, created_at,
			completed_at
		FROM regeneration_plans WHERE regeneration_plan_id = ?`,
		regenerationPlanID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return RegenerationPlan{}, domainError(
			"REGENERATION_PLAN_NOT_FOUND",
			"下游重生成计划不存在。",
		)
	}
	if err != nil {
		return RegenerationPlan{}, err
	}
	groups, err := listRegenerationPlanGroups(ctx, s.db, regenerationPlanID)
	if err != nil {
		return RegenerationPlan{}, err
	}
	plan.Groups = groups
	return plan, nil
}

func getImpactReviewTx(
	ctx context.Context,
	tx *sql.Tx,
	impactReviewID string,
) (ImpactReview, error) {
	review, err := scanImpactReview(tx.QueryRowContext(ctx, `
		SELECT impact_review_id, project_id, run_id, source_artifact_id,
			old_version_id, new_version_id, change_set_id, status,
			unaffected_summary_json, recommended_regeneration_start_json,
			snapshot_hash, created_at, resolved_at
		FROM impact_reviews WHERE impact_review_id = ?`,
		impactReviewID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return ImpactReview{}, domainError("IMPACT_REVIEW_NOT_FOUND", "影响预览不存在。")
	}
	if err != nil {
		return ImpactReview{}, err
	}
	items, err := listImpactReviewItems(ctx, tx, impactReviewID)
	if err != nil {
		return ImpactReview{}, err
	}
	review.AffectedItems = items
	return review, nil
}

func (s *Store) validateImpactReviewResolutionTx(
	ctx context.Context,
	tx *sql.Tx,
	review ImpactReview,
	expectedSnapshotHash string,
) (Artifact, Approval, Run, error) {
	if review.Status != "pending" {
		return Artifact{}, Approval{}, Run{}, domainError(
			"IMPACT_REVIEW_EXPIRED",
			"影响预览已经处理或失效。",
		)
	}
	calculatedHash, err := impactReviewSnapshotHash(review)
	if err != nil {
		return Artifact{}, Approval{}, Run{}, err
	}
	if review.SnapshotHash != expectedSnapshotHash ||
		calculatedHash != review.SnapshotHash {
		return Artifact{}, Approval{}, Run{}, domainError(
			"IMPACT_REVIEW_CONFLICT",
			"影响范围已经变化，请刷新后重新确认。",
		)
	}

	var source Artifact
	var createdAt, updatedAt string
	if err := tx.QueryRowContext(ctx, `
		SELECT artifact_id, project_id, run_id, step_run_id, capability_id,
			artifact_type, scope_key, current_version_id, created_at, updated_at
		FROM artifacts WHERE artifact_id = ?`,
		review.SourceArtifactID,
	).Scan(
		&source.ArtifactID,
		&source.ProjectID,
		&source.RunID,
		&source.StepRunID,
		&source.CapabilityID,
		&source.ArtifactType,
		&source.ScopeKey,
		&source.CurrentVersionID,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Artifact{}, Approval{}, Run{}, err
	}
	if source.ProjectID != review.ProjectID ||
		source.RunID != review.RunID ||
		source.CurrentVersionID != review.NewVersionID {
		return Artifact{}, Approval{}, Run{}, domainError(
			"IMPACT_REVIEW_CONFLICT",
			"影响预览对应的源产物已经变化。",
		)
	}
	var newStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT status FROM artifact_versions
		WHERE artifact_version_id = ? AND artifact_id = ?`,
		review.NewVersionID,
		source.ArtifactID,
	).Scan(&newStatus); err != nil {
		return Artifact{}, Approval{}, Run{}, err
	}
	if newStatus != "pending_approval" {
		return Artifact{}, Approval{}, Run{}, domainError(
			"IMPACT_REVIEW_CONFLICT",
			"影响预览对应的新版本不再等待确认。",
		)
	}
	for _, item := range review.AffectedItems {
		var currentVersionID, status, projectID string
		if err := tx.QueryRowContext(ctx, `
			SELECT a.current_version_id, av.status, a.project_id
			FROM artifacts a
			JOIN artifact_versions av ON av.artifact_id = a.artifact_id
			WHERE a.artifact_id = ? AND av.artifact_version_id = ?`,
			item.ArtifactID,
			item.ArtifactVersionID,
		).Scan(&currentVersionID, &status, &projectID); err != nil {
			return Artifact{}, Approval{}, Run{}, err
		}
		if projectID != review.ProjectID ||
			currentVersionID != item.ArtifactVersionID ||
			(status != "confirmed" && status != "pending_approval") {
			return Artifact{}, Approval{}, Run{}, domainError(
				"IMPACT_REVIEW_CONFLICT",
				"受影响产物已经变化，请重新生成影响预览。",
			)
		}
	}
	approval, err := scanApproval(tx.QueryRowContext(ctx, `
		SELECT approval_request_id, project_id, run_id, step_run_id, scope,
			status, version, title, reason, options_json, subject_kind,
			subject_ref_id, subject_version, subject_snapshot_hash, requested_at,
			resolved_at, resolution_json, actor_ref
		FROM approvals
		WHERE subject_ref_id = ? AND status = 'pending'
		ORDER BY requested_at DESC LIMIT 1`,
		review.NewVersionID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, Approval{}, Run{}, domainError(
			"IMPACT_REVIEW_CONFLICT",
			"影响预览对应的确认请求已经变化。",
		)
	}
	if err != nil {
		return Artifact{}, Approval{}, Run{}, err
	}
	run, err := getRunTx(ctx, tx, review.RunID)
	if err != nil {
		return Artifact{}, Approval{}, Run{}, err
	}
	if run.Status != "waiting_approval" && run.Status != "paused" && run.Status != "completed" {
		return Artifact{}, Approval{}, Run{}, domainError(
			"RUN_STATE_CONFLICT",
			"当前生成任务状态不允许处理下游影响。",
		)
	}
	if run.Status == "completed" {
		var activeWriteRunID sql.NullString
		if err := tx.QueryRowContext(ctx, `
			SELECT active_write_run_id FROM projects WHERE project_id = ?`,
			review.ProjectID,
		).Scan(&activeWriteRunID); err != nil {
			return Artifact{}, Approval{}, Run{}, err
		}
		if activeWriteRunID.Valid && activeWriteRunID.String != review.RunID {
			return Artifact{}, Approval{}, Run{}, domainError(
				"PROJECT_WRITE_RUN_CONFLICT",
				"作品存在另一条正在执行的生成任务，请完成或结束后再修改历史产物。",
			)
		}
	}
	return source, approval, run, nil
}

func (s *Store) createDependencyDecisionTx(
	ctx context.Context,
	tx *sql.Tx,
	review ImpactReview,
	action string,
	preservedIDs []string,
	staleIDs []string,
	actorRef string,
	now time.Time,
) (DependencyDecision, error) {
	preservedJSON, err := json.Marshal(preservedIDs)
	if err != nil {
		return DependencyDecision{}, err
	}
	staleJSON, err := json.Marshal(staleIDs)
	if err != nil {
		return DependencyDecision{}, err
	}
	decision := DependencyDecision{
		DependencyDecisionID:          s.newID("decd"),
		ProjectID:                     review.ProjectID,
		RunID:                         review.RunID,
		ImpactReviewID:                review.ImpactReviewID,
		Action:                        action,
		OldUpstreamVersionID:          review.OldVersionID,
		NewUpstreamVersionID:          review.NewVersionID,
		PreservedDownstreamVersionIDs: slices.Clone(preservedIDs),
		StaleDownstreamVersionIDs:     slices.Clone(staleIDs),
		ActorRef:                      actorRef,
		ResolvedAt:                    now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO dependency_decisions(
			dependency_decision_id, project_id, run_id, impact_review_id,
			action, old_upstream_version_id, new_upstream_version_id,
			preserved_downstream_version_ids_json,
			stale_downstream_version_ids_json, actor_ref, resolved_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		decision.DependencyDecisionID,
		decision.ProjectID,
		decision.RunID,
		decision.ImpactReviewID,
		decision.Action,
		decision.OldUpstreamVersionID,
		decision.NewUpstreamVersionID,
		string(preservedJSON),
		string(staleJSON),
		decision.ActorRef,
		formatTime(now),
	); err != nil {
		if isUniqueConstraint(err) {
			return DependencyDecision{}, domainError(
				"IMPACT_REVIEW_CONFLICT",
				"影响预览已经生成决策。",
			)
		}
		return DependencyDecision{}, err
	}
	return decision, nil
}

func confirmImpactSourceAndResolveApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	review ImpactReview,
	approval Approval,
	action string,
	actorRef string,
	now time.Time,
) error {
	if err := updateExactlyOne(ctx, tx, `
		UPDATE artifact_versions
		SET status = 'confirmed', confirmed_at = ?
		WHERE artifact_version_id = ? AND status = 'pending_approval'`,
		"影响预览对应的新版本已经变化。",
		formatTime(now),
		review.NewVersionID,
	); err != nil {
		return err
	}
	resolution, err := json.Marshal(map[string]any{
		"action":                 action,
		"impact_review_id":       review.ImpactReviewID,
		"resolved_subject_refs":  []string{review.NewVersionID},
		"impact_review_snapshot": review.SnapshotHash,
	})
	if err != nil {
		return err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE approvals
		SET status = 'approved', resolved_at = ?, resolution_json = ?, actor_ref = ?
		WHERE approval_request_id = ? AND status = 'pending'`,
		"影响预览对应的确认请求已经变化。",
		formatTime(now),
		string(resolution),
		actorRef,
		approval.ApprovalRequestID,
	); err != nil {
		return err
	}
	resolvedStatus := "kept"
	if action == "regenerate_downstream" {
		resolvedStatus = "regeneration_planned"
	}
	return updateExactlyOne(ctx, tx, `
		UPDATE impact_reviews
		SET status = ?, resolved_at = ?
		WHERE impact_review_id = ? AND status = 'pending'`,
		"影响预览已经处理。",
		resolvedStatus,
		formatTime(now),
		review.ImpactReviewID,
	)
}

func (s *Store) finishKeepDownstreamTx(
	ctx context.Context,
	tx *sql.Tx,
	review ImpactReview,
	run Run,
	now time.Time,
) error {
	var pendingStepRunID, pendingSubjectID string
	err := tx.QueryRowContext(ctx, `
		SELECT step_run_id, subject_ref_id
		FROM approvals
		WHERE run_id = ? AND status = 'pending' AND scope <> 'final_selection'
		ORDER BY requested_at DESC LIMIT 1`,
		review.RunID,
	).Scan(&pendingStepRunID, &pendingSubjectID)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `
			UPDATE runs SET status = 'paused', updated_at = ? WHERE run_id = ?`,
			formatTime(now),
			review.RunID,
		); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE projects
			SET version = version + 1, status = 'paused',
				current_focus_artifact_version_id = ?, updated_at = ?
			WHERE project_id = ? AND active_write_run_id = ?`,
			review.NewVersionID,
			formatTime(now),
			review.ProjectID,
			run.RunID,
		)
		return err
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs
		SET status = 'waiting_approval', current_step_run_id = ?, updated_at = ?
		WHERE run_id = ?`,
		pendingStepRunID,
		formatTime(now),
		review.RunID,
	); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE projects
		SET version = version + 1, status = 'waiting_approval',
			current_focus_artifact_version_id = ?, updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		pendingSubjectID,
		formatTime(now),
		review.ProjectID,
		run.RunID,
	)
	return err
}

func (s *Store) planDownstreamRegenerationTx(
	ctx context.Context,
	tx *sql.Tx,
	review ImpactReview,
	source Artifact,
	run Run,
	now time.Time,
) (*RegenerationPlan, error) {
	if len(review.AffectedItems) == 0 {
		return nil, domainError(
			"REGENERATION_PLAN_CONFLICT",
			"影响预览没有可重生成的下游。",
		)
	}
	for _, item := range review.AffectedItems {
		if err := updateExactlyOne(ctx, tx, `
			UPDATE artifact_versions SET status = 'stale'
			WHERE artifact_version_id = ?
				AND status IN ('confirmed', 'pending_approval')`,
			"受影响产物状态已经变化。",
			item.ArtifactVersionID,
		); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE approvals
			SET status = 'expired', resolved_at = ?
			WHERE subject_ref_id = ? AND status = 'pending'`,
			formatTime(now),
			item.ArtifactVersionID,
		); err != nil {
			return nil, err
		}
	}

	groups := groupImpactItemsForRegeneration(review.AffectedItems)
	if len(groups) == 0 {
		return nil, domainError(
			"REGENERATION_PLAN_CONFLICT",
			"无法确定下游重生成边界。",
		)
	}
	plan := RegenerationPlan{
		RegenerationPlanID: s.newID("rgn"),
		ProjectID:          review.ProjectID,
		RunID:              review.RunID,
		ImpactReviewID:     review.ImpactReviewID,
		SourceNewVersionID: review.NewVersionID,
		Status:             "pending",
		CurrentGroupOrder:  1,
		Groups:             groups,
		CreatedAt:          now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO regeneration_plans(
			regeneration_plan_id, project_id, run_id, impact_review_id,
			source_new_version_id, status, current_group_order, created_at
		) VALUES(?, ?, ?, ?, ?, 'pending', 1, ?)`,
		plan.RegenerationPlanID,
		plan.ProjectID,
		plan.RunID,
		plan.ImpactReviewID,
		plan.SourceNewVersionID,
		formatTime(now),
	); err != nil {
		if isUniqueConstraint(err) {
			return nil, domainError(
				"REGENERATION_PLAN_CONFLICT",
				"影响预览已经创建重生成计划。",
			)
		}
		return nil, err
	}
	for index := range plan.Groups {
		group := &plan.Groups[index]
		group.RegenerationPlanGroupID = s.newID("rgng")
		group.RegenerationPlanID = plan.RegenerationPlanID
		group.GroupOrder = index + 1
		group.Status = "blocked"
		taskKeysJSON, _ := json.Marshal(group.TaskKeys)
		staleJSON, _ := json.Marshal(group.StaleVersionIDs)
		preservedJSON, _ := json.Marshal(group.PreservedVersionIDs)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO regeneration_plan_groups(
				regeneration_plan_group_id, regeneration_plan_id, group_order,
				step_id, task_keys_json, stale_version_ids_json,
				preserved_version_ids_json, status
			) VALUES(?, ?, ?, ?, ?, ?, ?, 'blocked')`,
			group.RegenerationPlanGroupID,
			group.RegenerationPlanID,
			group.GroupOrder,
			group.StepID,
			string(taskKeysJSON),
			string(staleJSON),
			string(preservedJSON),
		); err != nil {
			return nil, err
		}
	}
	stepRunID, err := s.materializeRegenerationGroupTx(
		ctx,
		tx,
		&plan,
		0,
		review,
		source,
		run,
		now,
	)
	if err != nil {
		return nil, err
	}
	for _, item := range review.AffectedItems {
		if _, err := tx.ExecContext(ctx, `
			UPDATE step_runs
			SET status = 'superseded', ended_at = COALESCE(ended_at, ?)
			WHERE step_run_id = (
				SELECT step_run_id FROM artifacts WHERE artifact_id = ?
			) AND step_run_id != ?
				AND status IN ('pending', 'running', 'waiting_approval', 'paused')`,
			formatTime(now),
			item.ArtifactID,
			stepRunID,
		); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs
		SET status = 'paused', current_step_run_id = ?, ended_at = NULL, updated_at = ?
		WHERE run_id = ?`,
		stepRunID,
		formatTime(now),
		review.RunID,
	); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET version = version + 1, status = 'paused',
			active_write_run_id = ?, current_capability_id = ?,
			current_focus_artifact_version_id = ?, updated_at = ?
		WHERE project_id = ?
			AND (active_write_run_id IS NULL OR active_write_run_id = ?)`,
		review.RunID,
		run.CapabilityID,
		review.NewVersionID,
		formatTime(now),
		review.ProjectID,
		run.RunID,
	); err != nil {
		return nil, err
	}
	for _, item := range review.AffectedItems {
		runRef, stepRef := review.RunID, stepRunID
		if _, err := s.appendEvent(
			ctx,
			tx,
			review.ProjectID,
			&runRef,
			&stepRef,
			"artifact.marked_stale",
			"artifact_version",
			item.ArtifactVersionID,
			map[string]any{"impact_review_id": review.ImpactReviewID},
		); err != nil {
			return nil, err
		}
	}
	return &plan, nil
}

func groupImpactItemsForRegeneration(items []ImpactReviewItem) []RegenerationPlanGroup {
	var groups []RegenerationPlanGroup
	indexByStep := map[string]int{}
	for _, item := range items {
		groupIndex, exists := indexByStep[item.RegenerateFromStepID]
		if !exists {
			groupIndex = len(groups)
			indexByStep[item.RegenerateFromStepID] = groupIndex
			groups = append(groups, RegenerationPlanGroup{
				StepID:              item.RegenerateFromStepID,
				TaskKeys:            []string{},
				StaleVersionIDs:     []string{},
				PreservedVersionIDs: []string{},
			})
		}
		group := &groups[groupIndex]
		for _, taskKey := range item.RegenerateTaskKeys {
			if !slices.Contains(group.TaskKeys, taskKey) {
				group.TaskKeys = append(group.TaskKeys, taskKey)
			}
		}
		if !slices.Contains(group.StaleVersionIDs, item.ArtifactVersionID) {
			group.StaleVersionIDs = append(group.StaleVersionIDs, item.ArtifactVersionID)
		}
	}
	for index := range groups {
		sort.Strings(groups[index].TaskKeys)
		sort.Strings(groups[index].StaleVersionIDs)
	}
	return groups
}

func (s *Store) materializeRegenerationGroupTx(
	ctx context.Context,
	tx *sql.Tx,
	plan *RegenerationPlan,
	groupIndex int,
	review ImpactReview,
	source Artifact,
	run Run,
	now time.Time,
) (string, error) {
	if groupIndex < 0 || groupIndex >= len(plan.Groups) {
		return "", domainError(
			"REGENERATION_PLAN_CONFLICT",
			"重生成组序号无效。",
		)
	}
	group := &plan.Groups[groupIndex]
	entry, ok, registryErr := s.capabilityEntryForRunQuery(
		ctx, tx, run.RunID, run.CapabilityID,
	)
	if registryErr != nil {
		return "", registryErr
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil ||
		entry.Definition.Version != run.CapabilityVersion {
		return "", domainError(
			"CAPABILITY_VERSION_UNAVAILABLE",
			"重生成所需能力版本不可用。",
		)
	}
	var step *capability.CompiledStep
	for index := range entry.Definition.Steps {
		if entry.Definition.Steps[index].ID == group.StepID {
			step = &entry.Definition.Steps[index]
			break
		}
	}
	if step == nil {
		return "", domainError(
			"REGENERATION_PLAN_CONFLICT",
			"重生成步骤不在能力定义中。",
		)
	}
	var affected *ImpactReviewItem
	for index := range review.AffectedItems {
		if review.AffectedItems[index].RegenerateFromStepID == group.StepID {
			affected = &review.AffectedItems[index]
			break
		}
	}
	if affected == nil {
		return "", domainError(
			"REGENERATION_PLAN_CONFLICT",
			"重生成组缺少对应的受影响产物。",
		)
	}
	inputVersions, err := s.regenerationStepInputsTx(
		ctx,
		tx,
		*step,
		*affected,
		source,
		review.NewVersionID,
	)
	if err != nil {
		return "", err
	}
	inputJSON, err := json.Marshal(inputVersions)
	if err != nil {
		return "", err
	}
	cursor, err := json.Marshal(map[string]any{
		"regeneration_plan_id": plan.RegenerationPlanID,
		"plan_group_order":     group.GroupOrder,
		"impact_review_id":     review.ImpactReviewID,
	})
	if err != nil {
		return "", err
	}
	stepRunID := s.newID("step")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO step_runs(
			step_run_id, run_id, step_id, status, attempt_count,
			approval_policy, input_version_snapshot_json, task_cursor_json
		) VALUES(?, ?, ?, 'pending', 0, ?, ?, ?)`,
		stepRunID,
		run.RunID,
		step.ID,
		step.Approval.Type,
		string(inputJSON),
		string(cursor),
	); err != nil {
		return "", err
	}
	group.StepRunID = &stepRunID
	group.Status = "ready"
	if _, err := tx.ExecContext(ctx, `
		UPDATE regeneration_plan_groups
		SET step_run_id = ?, status = 'ready'
		WHERE regeneration_plan_group_id = ? AND status = 'blocked'`,
		stepRunID,
		group.RegenerationPlanGroupID,
	); err != nil {
		return "", err
	}

	taskInput, err := json.Marshal(map[string]any{
		"run_input_snapshot_version_id": run.CurrentInputSnapshotVersionID,
		"step_input_versions":           inputVersions,
	})
	if err != nil {
		return "", err
	}
	if step.Batch != nil && step.Batch.ItemKey == "source_analysis" {
		if err := s.planNovelSourceAnalysisTaskTx(
			ctx,
			tx,
			run,
			stepRunID,
			*step,
			json.RawMessage(inputJSON),
			cursor,
			now,
		); err != nil {
			return "", err
		}
		return stepRunID, nil
	}
	if isVideoScriptExtractionStep(*step) {
		if err := s.planVideoAssetTasksTx(
			ctx, tx, run, stepRunID, *step, json.RawMessage(inputJSON), cursor, group.TaskKeys, now,
		); err != nil {
			return "", err
		}
		return stepRunID, nil
	}
	if regenerationRequiresFullBatchPlanning(*step) {
		if err := s.planBatchStepTasksTx(
			ctx,
			tx,
			run,
			stepRunID,
			*step,
			json.RawMessage(inputJSON),
			cursor,
			now,
		); err != nil {
			return "", err
		}
		return stepRunID, nil
	}
	taskKeys := slices.Clone(group.TaskKeys)
	if step.Batch == nil && step.Kind != "batch" {
		taskKeys = []string{"step:" + step.ID}
	}
	if len(taskKeys) == 0 {
		taskKeys = []string{"step:" + step.ID}
	}
	for index, taskKey := range taskKeys {
		taskID := s.newID("tsk")
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_items(
				task_item_id, step_run_id, run_id, item_key, item_order,
				status, attempt_count, input_snapshot_json, cursor_json,
				created_at, updated_at
			) VALUES(?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?, ?)`,
			taskID,
			stepRunID,
			run.RunID,
			taskKey,
			index+1,
			string(taskInput),
			string(cursor),
			formatTime(now),
			formatTime(now),
		); err != nil {
			return "", err
		}
		runRef, stepRef := run.RunID, stepRunID
		if _, err := s.appendEvent(
			ctx,
			tx,
			run.ProjectID,
			&runRef,
			&stepRef,
			"task.queued",
			"task_item",
			taskID,
			map[string]any{
				"executor_id":          step.ExecutorRef,
				"item_key":             taskKey,
				"regeneration_plan_id": plan.RegenerationPlanID,
			},
		); err != nil {
			return "", err
		}
	}
	return stepRunID, nil
}

func regenerationRequiresFullBatchPlanning(step capability.CompiledStep) bool {
	return step.Batch != nil &&
		len(step.OutputRefs) == 1 &&
		step.OutputRefs[0].Cardinality == "one"
}

func (s *Store) regenerationStepInputsTx(
	ctx context.Context,
	tx *sql.Tx,
	step capability.CompiledStep,
	firstAffected ImpactReviewItem,
	source Artifact,
	newSourceVersionID string,
) ([]map[string]any, error) {
	var newSourceVersion int
	if err := tx.QueryRowContext(ctx, `
		SELECT version FROM artifact_versions WHERE artifact_version_id = ?`,
		newSourceVersionID,
	).Scan(&newSourceVersion); err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(step.InputRefs))
	for _, inputRef := range step.InputRefs {
		if inputRef.Cardinality == "many" {
			rows, err := tx.QueryContext(ctx, `
				SELECT artifact.artifact_id, artifact.current_version_id,
					current_version.version, current_version.status, artifact.scope_key
				FROM artifacts artifact
				JOIN artifact_versions current_version
					ON current_version.artifact_version_id = artifact.current_version_id
				WHERE artifact.run_id = ? AND artifact.artifact_type = ?
					AND current_version.status = 'confirmed'
				ORDER BY artifact.scope_key ASC, artifact.artifact_id ASC`,
				source.RunID,
				inputRef.ArtifactType,
			)
			if err != nil {
				return nil, err
			}
			count := 0
			for rows.Next() {
				var artifactID, versionID, status, scopeKey string
				var version int
				if err := rows.Scan(&artifactID, &versionID, &version, &status, &scopeKey); err != nil {
					rows.Close()
					return nil, err
				}
				result = append(result, map[string]any{
					"artifact_id":         artifactID,
					"artifact_version_id": versionID,
					"version":             version,
					"status":              status,
					"scope_key":           scopeKey,
				})
				count++
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
			if count == 0 {
				return nil, domainError(
					"DEPENDENCY_INCOMPLETE",
					fmt.Sprintf("重生成步骤缺少 %s 输入版本。", inputRef.ArtifactType),
				)
			}
			continue
		}
		if inputRef.ArtifactType == source.ArtifactType {
			result = append(result, map[string]any{
				"artifact_id":         source.ArtifactID,
				"artifact_version_id": newSourceVersionID,
				"version":             newSourceVersion,
				"status":              "confirmed",
			})
			continue
		}
		var artifactID, currentVersionID, dependencyVersionID string
		var currentVersion, dependencyVersion int
		var currentStatus, dependencyStatus string
		err := tx.QueryRowContext(ctx, `
			SELECT upstream_artifact.artifact_id, upstream_artifact.current_version_id,
				current_version.version, current_version.status,
				upstream_version.artifact_version_id, upstream_version.version,
				upstream_version.status
			FROM artifact_dependencies d
			JOIN artifact_versions upstream_version
				ON upstream_version.artifact_version_id = d.upstream_ref_id
			JOIN artifacts upstream_artifact
				ON upstream_artifact.artifact_id = upstream_version.artifact_id
			JOIN artifact_versions current_version
				ON current_version.artifact_version_id = upstream_artifact.current_version_id
			WHERE d.downstream_artifact_version_id = ?
				AND d.upstream_kind = 'artifact_version'
				AND upstream_artifact.artifact_type = ?
			ORDER BY d.dependency_id ASC LIMIT 1`,
			firstAffected.ArtifactVersionID,
			inputRef.ArtifactType,
		).Scan(
			&artifactID,
			&currentVersionID,
			&currentVersion,
			&currentStatus,
			&dependencyVersionID,
			&dependencyVersion,
			&dependencyStatus,
		)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, `
				SELECT artifact.artifact_id, artifact.current_version_id,
					current_version.version, current_version.status
				FROM artifacts artifact
				JOIN artifact_versions current_version
					ON current_version.artifact_version_id = artifact.current_version_id
				WHERE artifact.run_id = ? AND artifact.artifact_type = ?
				ORDER BY artifact.updated_at DESC, artifact.artifact_id ASC
				LIMIT 1`,
				source.RunID,
				inputRef.ArtifactType,
			).Scan(
				&artifactID,
				&currentVersionID,
				&currentVersion,
				&currentStatus,
			)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, domainError(
					"DEPENDENCY_INCOMPLETE",
					fmt.Sprintf("重生成步骤缺少 %s 输入版本。", inputRef.ArtifactType),
				)
			}
			if err != nil {
				return nil, err
			}
			dependencyVersionID = currentVersionID
			dependencyVersion = currentVersion
			dependencyStatus = currentStatus
		}
		if err != nil {
			return nil, err
		}
		versionID := dependencyVersionID
		version := dependencyVersion
		status := dependencyStatus
		if currentStatus == "confirmed" {
			versionID = currentVersionID
			version = currentVersion
			status = currentStatus
		}
		if status != "confirmed" {
			return nil, domainError(
				"DEPENDENCY_INCOMPLETE",
				fmt.Sprintf("重生成步骤的 %s 输入尚未确认。", inputRef.ArtifactType),
			)
		}
		result = append(result, map[string]any{
			"artifact_id":         artifactID,
			"artifact_version_id": versionID,
			"version":             version,
			"status":              status,
		})
	}
	return result, nil
}

func (s *Store) advanceRegenerationPlanAfterApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	approval Approval,
	confirmedVersionIDs []string,
	now time.Time,
) (bool, error) {
	if len(confirmedVersionIDs) == 0 {
		return false, domainError("DEPENDENCY_INCOMPLETE", "重生成确认缺少产物版本。")
	}
	confirmedVersionID := confirmedVersionIDs[0]
	binding, err := regenerationBindingForStepTx(ctx, tx, approval.StepRunID)
	if err != nil {
		return false, err
	}
	if binding == nil {
		return false, nil
	}
	if binding.PlanStatus != "waiting_approval" ||
		binding.GroupStatus != "waiting_approval" ||
		binding.GroupOrder != binding.CurrentOrder {
		return true, domainError(
			"REGENERATION_PLAN_CONFLICT",
			"当前重生成计划已经变化，不能继续推进。",
		)
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE regeneration_plan_groups SET status = 'completed'
		WHERE regeneration_plan_group_id = ? AND status = 'waiting_approval'`,
		"当前重生成组已经变化。",
		binding.GroupID,
	); err != nil {
		return true, err
	}

	plan, err := scanRegenerationPlan(tx.QueryRowContext(ctx, `
		SELECT regeneration_plan_id, project_id, run_id, impact_review_id,
			source_new_version_id, status, current_group_order, created_at,
			completed_at
		FROM regeneration_plans WHERE regeneration_plan_id = ?`,
		binding.PlanID,
	))
	if err != nil {
		return true, err
	}
	plan.Groups, err = listRegenerationPlanGroups(ctx, tx, plan.RegenerationPlanID)
	if err != nil {
		return true, err
	}

	nextIndex := binding.GroupOrder
	var nextStepRunID *string
	advancedToNextGroup := false
	if nextIndex < len(plan.Groups) {
		review, err := getImpactReviewTx(ctx, tx, plan.ImpactReviewID)
		if err != nil {
			return true, err
		}
		source, err := scanArtifact(tx.QueryRowContext(ctx, `
			SELECT artifact_id, project_id, COALESCE(run_id, ''), COALESCE(step_run_id, ''),
				origin_type, origin_id, capability_id,
				artifact_type, scope_key, current_version_id, created_at, updated_at
			FROM artifacts WHERE artifact_id = ?`,
			review.SourceArtifactID,
		))
		if err != nil {
			return true, err
		}
		run, err := getRunTx(ctx, tx, plan.RunID)
		if err != nil {
			return true, err
		}
		stepRunID, err := s.materializeRegenerationGroupTx(
			ctx,
			tx,
			&plan,
			nextIndex,
			review,
			source,
			run,
			now,
		)
		if err != nil {
			return true, err
		}
		nextStepRunID = &stepRunID
		advancedToNextGroup = true
		if err := updateExactlyOne(ctx, tx, `
			UPDATE regeneration_plans
			SET status = 'pending', current_group_order = ?
			WHERE regeneration_plan_id = ? AND status = 'waiting_approval'
				AND current_group_order = ?`,
			"当前重生成计划已经变化。",
			binding.GroupOrder+1, binding.PlanID, binding.GroupOrder,
		); err != nil {
			return true, err
		}
		if err := updateExactlyOne(ctx, tx, `
			UPDATE runs SET status = 'paused', current_step_run_id = ?, updated_at = ?
			WHERE run_id = ? AND status = 'waiting_approval'`,
			"生成任务状态已经变化。",
			stepRunID, formatTime(now), approval.RunID,
		); err != nil {
			return true, err
		}
	} else {
		if err := updateExactlyOne(ctx, tx, `
			UPDATE regeneration_plans
			SET status = 'completed', completed_at = ?
			WHERE regeneration_plan_id = ? AND status = 'waiting_approval'
				AND current_group_order = ?`,
			"当前重生成计划已经变化。",
			formatTime(now), binding.PlanID, binding.GroupOrder,
		); err != nil {
			return true, err
		}
		nextStepRunID, _, err = s.advanceRunAfterConfirmedArtifactsTx(
			ctx,
			tx,
			approval.RunID,
			approval.StepRunID,
			confirmedVersionIDs,
			now,
			"approved",
		)
		if err != nil {
			return true, err
		}
	}
	if advancedToNextGroup {
		if err := updateExactlyOne(ctx, tx, `
			UPDATE projects
			SET version = version + 1, status = 'paused',
				current_focus_artifact_version_id = ?, updated_at = ?
			WHERE project_id = ? AND active_write_run_id = ?`,
			"作品写锁已经变化。",
			confirmedVersionID, formatTime(now), approval.ProjectID, approval.RunID,
		); err != nil {
			return true, err
		}
	}

	runRef, stepRef := approval.RunID, approval.StepRunID
	if _, err := s.appendEvent(
		ctx,
		tx,
		approval.ProjectID,
		&runRef,
		&stepRef,
		"regeneration.group_completed",
		"regeneration_plan_group",
		binding.GroupID,
		map[string]any{"group_order": binding.GroupOrder},
	); err != nil {
		return true, err
	}
	if advancedToNextGroup {
		if _, err := s.appendEvent(
			ctx,
			tx,
			approval.ProjectID,
			&runRef,
			nextStepRunID,
			"regeneration.group_ready",
			"regeneration_plan",
			binding.PlanID,
			map[string]any{
				"group_order": binding.GroupOrder + 1,
				"step_run_id": *nextStepRunID,
			},
		); err != nil {
			return true, err
		}
	} else {
		payload := map[string]any{}
		if nextStepRunID != nil {
			payload["next_step_run_id"] = *nextStepRunID
		}
		if _, err := s.appendEvent(
			ctx,
			tx,
			approval.ProjectID,
			&runRef,
			&stepRef,
			"regeneration.plan_completed",
			"regeneration_plan",
			binding.PlanID,
			payload,
		); err != nil {
			return true, err
		}
	}
	return true, nil
}

func scanRegenerationPlan(row rowScanner) (RegenerationPlan, error) {
	var plan RegenerationPlan
	var createdAt string
	var completedAt sql.NullString
	if err := row.Scan(
		&plan.RegenerationPlanID,
		&plan.ProjectID,
		&plan.RunID,
		&plan.ImpactReviewID,
		&plan.SourceNewVersionID,
		&plan.Status,
		&plan.CurrentGroupOrder,
		&createdAt,
		&completedAt,
	); err != nil {
		return RegenerationPlan{}, err
	}
	var err error
	plan.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return RegenerationPlan{}, err
	}
	plan.CompletedAt, err = optionalTime(completedAt)
	return plan, err
}

func listRegenerationPlanGroups(
	ctx context.Context,
	queryer interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	},
	regenerationPlanID string,
) ([]RegenerationPlanGroup, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT regeneration_plan_group_id, regeneration_plan_id, group_order,
			step_id, task_keys_json, stale_version_ids_json,
			preserved_version_ids_json, step_run_id, status
		FROM regeneration_plan_groups
		WHERE regeneration_plan_id = ?
		ORDER BY group_order ASC`,
		regenerationPlanID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []RegenerationPlanGroup
	for rows.Next() {
		var group RegenerationPlanGroup
		var taskKeys, staleIDs, preservedIDs string
		var stepRunID sql.NullString
		if err := rows.Scan(
			&group.RegenerationPlanGroupID,
			&group.RegenerationPlanID,
			&group.GroupOrder,
			&group.StepID,
			&taskKeys,
			&staleIDs,
			&preservedIDs,
			&stepRunID,
			&group.Status,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(taskKeys), &group.TaskKeys); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(staleIDs), &group.StaleVersionIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(preservedIDs), &group.PreservedVersionIDs); err != nil {
			return nil, err
		}
		group.StepRunID = stringPointer(stepRunID)
		groups = append(groups, group)
	}
	if groups == nil {
		groups = []RegenerationPlanGroup{}
	}
	return groups, rows.Err()
}
