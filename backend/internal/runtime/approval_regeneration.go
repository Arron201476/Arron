package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

type regenerationTarget struct {
	Artifact       Artifact
	CurrentVersion ArtifactVersion
	StepID         string
}

func (s *Store) RequestApprovalRegeneration(
	ctx context.Context,
	command RequestApprovalRegenerationCommand,
) (ApprovalRegenerationResult, error) {
	if command.ApprovalRequestID == "" || command.ExpectedApprovalVersion <= 0 ||
		command.SubjectSnapshotHash == "" ||
		(command.Action != "request_ai_revision" && command.Action != "regenerate_artifact") {
		return ApprovalRegenerationResult{}, domainError("REQUEST_VALIDATION_FAILED", "返修请求不完整。")
	}
	if command.Action == "request_ai_revision" && strings.TrimSpace(command.Instruction) == "" {
		return ApprovalRegenerationResult{}, domainError("REQUEST_VALIDATION_FAILED", "让 Agent 修改时必须说明修改要求。")
	}
	if command.ActorRef == "" {
		command.ActorRef = actorRefFromContext(ctx)
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	defer tx.Rollback()

	approval, err := getApprovalTx(ctx, tx, command.ApprovalRequestID)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if err := authorizeApprovalMutationTx(ctx, tx, approval, command.Scope); err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if command.Scope == "" {
		command.Scope = approval.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if hit {
		return decodeApprovalRegenerationReceiptTx(ctx, tx, cached, approval, command)
	}
	if approval.Status != "pending" || approval.Version != command.ExpectedApprovalVersion ||
		approval.SubjectSnapshotHash != command.SubjectSnapshotHash {
		return ApprovalRegenerationResult{}, domainError("APPROVAL_SUBJECT_CHANGED", "确认对象已经变化，请刷新后重试。")
	}
	if !slices.Contains(approval.Options, command.Action) {
		return ApprovalRegenerationResult{}, domainError("APPROVAL_ACTION_NOT_ALLOWED", "该确认请求不允许此返修操作。")
	}
	if blocked, err := approvalHasActiveRevisionTx(ctx, tx, approval); err != nil {
		return ApprovalRegenerationResult{}, err
	} else if blocked {
		return ApprovalRegenerationResult{}, domainError("REVISION_IN_PROGRESS", "当前版本存在未处理的修改请求，请先完成或取消修改。")
	}
	if approval.Scope == "quality_review" || approval.Scope == "final_selection" {
		return ApprovalRegenerationResult{}, domainError("APPROVAL_COMMAND_MISMATCH", "该确认请求必须使用对应的专用处理接口。")
	}

	targets, err := approvalRegenerationTargetsTx(ctx, tx, approval)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if len(targets) == 0 {
		return ApprovalRegenerationResult{}, domainError("ARTIFACT_VERSION_NOT_FOUND", "确认请求没有可返修的当前产物。")
	}
	run, err := getRunTx(ctx, tx, approval.RunID)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if run.Status != "waiting_approval" {
		return ApprovalRegenerationResult{}, domainError("RUN_STATE_CONFLICT", "当前生成任务不在待确认状态。")
	}

	review, source, err := s.createApprovalRegenerationReviewTx(ctx, tx, approval, targets, command, now)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	resolution, _ := json.Marshal(map[string]any{"action": command.Action, "instruction": strings.TrimSpace(command.Instruction)})
	if err := updateExactlyOne(ctx, tx, `
		UPDATE approvals SET status = 'approved', resolved_at = ?, resolution_json = ?, actor_ref = ?
		WHERE approval_request_id = ? AND status = 'pending'`,
		"确认请求已经变化。", formatTime(now), string(resolution), command.ActorRef, approval.ApprovalRequestID); err != nil {
		return ApprovalRegenerationResult{}, err
	}
	plan, err := s.planDownstreamRegenerationTx(ctx, tx, review, source, run, now)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if err := attachRevisionInstructionTx(ctx, tx, plan, strings.TrimSpace(command.Instruction)); err != nil {
		return ApprovalRegenerationResult{}, err
	}
	runRef, stepRef := approval.RunID, approval.StepRunID
	if _, err := s.appendEvent(ctx, tx, approval.ProjectID, &runRef, &stepRef,
		"approval.resolved", "approval", approval.ApprovalRequestID,
		map[string]any{"action": command.Action, "regeneration_plan_id": plan.RegenerationPlanID}); err != nil {
		return ApprovalRegenerationResult{}, err
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, approval.RunID)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	result := ApprovalRegenerationResult{ImpactReview: review, RegenerationPlan: *plan, RunSnapshot: snapshot}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, result, now); err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApprovalRegenerationResult{}, err
	}
	return result, nil
}

func approvalRegenerationTargetsTx(ctx context.Context, tx *sql.Tx, approval Approval) ([]regenerationTarget, error) {
	query := `
		SELECT a.artifact_id, a.project_id, a.run_id, a.step_run_id, a.capability_id,
			a.artifact_type, a.scope_key, a.current_version_id, a.created_at, a.updated_at,
			av.artifact_version_id, av.version, av.status, av.payload_json, av.schema_id,
			av.schema_version, av.created_by_kind, av.actor_ref, av.creation_reason,
			av.base_version_id, av.created_at, av.confirmed_at, sr.step_id
		FROM artifacts a
		JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id AND av.artifact_id = a.artifact_id
		JOIN step_runs sr ON sr.step_run_id = a.step_run_id
		WHERE a.project_id = ? AND a.run_id = ? AND a.step_run_id = ? AND `
	args := []any{approval.ProjectID, approval.RunID, approval.StepRunID}
	switch approval.SubjectKind {
	case "artifact_version":
		query += "av.artifact_version_id = ?"
		args = append(args, approval.SubjectRefID)
	case "artifact_version_set":
		query += `a.step_run_id = ? AND av.status IN ('pending_approval','confirmed')
			AND EXISTS (
				SELECT 1 FROM approval_subject_versions asv
				WHERE asv.approval_request_id = ?
					AND asv.artifact_version_id = av.artifact_version_id
			)`
		args = append(args, approval.StepRunID, approval.ApprovalRequestID)
	default:
		return nil, domainError("APPROVAL_ACTION_NOT_ALLOWED", "当前确认对象不支持返修。")
	}
	query += " ORDER BY a.scope_key ASC, a.artifact_type ASC, a.artifact_id ASC"
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []regenerationTarget
	for rows.Next() {
		var target regenerationTarget
		var artifactCreated, artifactUpdated, versionCreated, payload string
		var baseVersionID, confirmedAt sql.NullString
		if err := rows.Scan(
			&target.Artifact.ArtifactID, &target.Artifact.ProjectID, &target.Artifact.RunID,
			&target.Artifact.StepRunID, &target.Artifact.CapabilityID, &target.Artifact.ArtifactType,
			&target.Artifact.ScopeKey, &target.Artifact.CurrentVersionID, &artifactCreated, &artifactUpdated,
			&target.CurrentVersion.ArtifactVersionID, &target.CurrentVersion.Version, &target.CurrentVersion.Status,
			&payload, &target.CurrentVersion.SchemaID, &target.CurrentVersion.SchemaVersion,
			&target.CurrentVersion.CreatedByKind, &target.CurrentVersion.ActorRef,
			&target.CurrentVersion.CreationReason, &baseVersionID, &versionCreated, &confirmedAt, &target.StepID,
		); err != nil {
			return nil, err
		}
		target.CurrentVersion.Payload = json.RawMessage(payload)
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func (s *Store) createApprovalRegenerationReviewTx(
	ctx context.Context,
	tx *sql.Tx,
	approval Approval,
	targets []regenerationTarget,
	command RequestApprovalRegenerationCommand,
	now time.Time,
) (ImpactReview, Artifact, error) {
	var downstreamItems []ImpactReviewItem
	seenVersions := map[string]bool{}
	targetArtifacts := map[string]bool{}
	for _, target := range targets {
		targetArtifacts[target.Artifact.ArtifactID] = true
	}
	for _, target := range targets {
		downstream, _, err := s.calculateImpactItemsTx(ctx, tx, target.Artifact, target.CurrentVersion.ArtifactVersionID, target.CurrentVersion.Version)
		if err != nil {
			return ImpactReview{}, Artifact{}, err
		}
		for _, item := range downstream {
			if targetArtifacts[item.ArtifactID] {
				continue
			}
			if !seenVersions[item.ArtifactVersionID] {
				seenVersions[item.ArtifactVersionID] = true
				downstreamItems = append(downstreamItems, item)
			}
		}
	}

	var targetItems []ImpactReviewItem
	var source Artifact
	var sourceOldVersionID, sourceNewVersionID, sourceChangeSetID string
	for index, target := range targets {
		newVersionID := s.newID("av")
		nextVersion, err := nextArtifactVersionTx(ctx, tx, target.Artifact.ArtifactID)
		if err != nil {
			return ImpactReview{}, Artifact{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifact_versions(
				artifact_version_id, artifact_id, version, status, payload_json, schema_id,
				schema_version, created_by_kind, actor_ref, creation_reason, base_version_id, created_at
			) VALUES(?, ?, ?, 'pending_approval', ?, ?, ?, 'user', ?, ?, ?, ?)`,
			newVersionID, target.Artifact.ArtifactID, nextVersion, string(target.CurrentVersion.Payload),
			target.CurrentVersion.SchemaID, target.CurrentVersion.SchemaVersion, command.ActorRef,
			command.Action, target.CurrentVersion.ArtifactVersionID, formatTime(now)); err != nil {
			return ImpactReview{}, Artifact{}, err
		}
		if err := s.copyArtifactDependenciesTx(ctx, tx, target.Artifact.ProjectID, target.Artifact.RunID,
			target.CurrentVersion.ArtifactVersionID, newVersionID, now); err != nil {
			return ImpactReview{}, Artifact{}, err
		}
		if err := updateExactlyOne(ctx, tx, `UPDATE artifact_versions SET status = 'superseded' WHERE artifact_version_id = ? AND status IN ('pending_approval','confirmed','stale')`,
			"待返修产物状态已经变化。", target.CurrentVersion.ArtifactVersionID); err != nil {
			return ImpactReview{}, Artifact{}, err
		}
		if err := updateExactlyOne(ctx, tx, `UPDATE artifacts SET current_version_id = ?, updated_at = ? WHERE artifact_id = ? AND current_version_id = ?`,
			"待返修产物已经变化。", newVersionID, formatTime(now), target.Artifact.ArtifactID, target.CurrentVersion.ArtifactVersionID); err != nil {
			return ImpactReview{}, Artifact{}, err
		}
		if index == 0 {
			changeSet, err := s.createArtifactChangeSetTx(ctx, tx, target.Artifact.ProjectID, newVersionID,
				target.CurrentVersion.ArtifactVersionID, target.Artifact.ScopeKey, "unknown", now)
			if err != nil {
				return ImpactReview{}, Artifact{}, err
			}
			source = target.Artifact
			source.CurrentVersionID = newVersionID
			sourceOldVersionID = target.CurrentVersion.ArtifactVersionID
			sourceNewVersionID = newVersionID
			sourceChangeSetID = changeSet.ChangeSetID
		}
		if !seenVersions[newVersionID] {
			regenerateTaskKeys, err := regenerationTaskKeysForTargetTx(ctx, tx, target)
			if err != nil {
				return ImpactReview{}, Artifact{}, err
			}
			seenVersions[newVersionID] = true
			targetItems = append(targetItems, ImpactReviewItem{
				ArtifactID: target.Artifact.ArtifactID, ArtifactVersionID: newVersionID,
				ArtifactType: target.Artifact.ArtifactType, ScopeKey: target.Artifact.ScopeKey,
				ImpactPath: []string{impactPathLabel(target.Artifact.ArtifactType, nextVersion, target.Artifact.ScopeKey)},
				ReasonCode: "USER_REGENERATION_REQUEST", RegenerateFromStepID: target.StepID,
				RegenerateTaskKeys: regenerateTaskKeys,
			})
		}
	}
	items := append(targetItems, downstreamItems...)
	if len(items) == 0 {
		return ImpactReview{}, Artifact{}, domainError("REGENERATION_PLAN_CONFLICT", "返修请求没有可重生成产物。")
	}
	review := ImpactReview{
		ImpactReviewID: s.newID("imp"), ProjectID: approval.ProjectID, RunID: approval.RunID,
		SourceArtifactID: source.ArtifactID, OldVersionID: sourceOldVersionID,
		NewVersionID: sourceNewVersionID, ChangeSetID: sourceChangeSetID, Status: "resolved",
		AffectedItems: items, UnaffectedSummary: json.RawMessage(`{"mode":"regeneration_request"}`),
		RecommendedRegenerationStart: []string{items[0].RegenerateFromStepID}, CreatedAt: now, ResolvedAt: &now,
	}
	for index := range review.AffectedItems {
		review.AffectedItems[index].ImpactReviewID = review.ImpactReviewID
		review.AffectedItems[index].ImpactReviewItemID = s.newID("impi")
		review.AffectedItems[index].ItemOrder = index + 1
	}
	hash, err := impactReviewSnapshotHash(review)
	if err != nil {
		return ImpactReview{}, Artifact{}, err
	}
	review.SnapshotHash = hash
	starts, _ := json.Marshal(review.RecommendedRegenerationStart)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO impact_reviews(impact_review_id, project_id, run_id, source_artifact_id,
			old_version_id, new_version_id, change_set_id, status, unaffected_summary_json,
			recommended_regeneration_start_json, snapshot_hash, created_at, resolved_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, 'resolved', ?, ?, ?, ?, ?)`,
		review.ImpactReviewID, review.ProjectID, review.RunID, review.SourceArtifactID,
		review.OldVersionID, review.NewVersionID, review.ChangeSetID, string(review.UnaffectedSummary),
		string(starts), review.SnapshotHash, formatTime(now), formatTime(now)); err != nil {
		return ImpactReview{}, Artifact{}, err
	}
	for _, item := range review.AffectedItems {
		path, _ := json.Marshal(item.ImpactPath)
		taskKeys, _ := json.Marshal(item.RegenerateTaskKeys)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO impact_review_items(impact_review_item_id, impact_review_id, artifact_id,
				artifact_version_id, artifact_type, scope_key, impact_path_json, reason_code,
				regenerate_from_step_id, regenerate_task_keys_json, item_order)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.ImpactReviewItemID, review.ImpactReviewID, item.ArtifactID, item.ArtifactVersionID,
			item.ArtifactType, item.ScopeKey, string(path), item.ReasonCode,
			item.RegenerateFromStepID, string(taskKeys), item.ItemOrder); err != nil {
			return ImpactReview{}, Artifact{}, err
		}
	}
	return review, source, nil
}

func regenerationTaskKeysForTargetTx(ctx context.Context, tx *sql.Tx, target regenerationTarget) ([]string, error) {
	if target.Artifact.ArtifactType != "video_script_unit" {
		return []string{target.Artifact.ScopeKey}, nil
	}
	var taskKey string
	err := tx.QueryRowContext(ctx, `
		SELECT ti.item_key
		FROM task_items ti
		JOIN artifact_versions av ON av.artifact_version_id = ti.output_artifact_version_id
		WHERE av.artifact_id = ? AND ti.status = 'succeeded'
		ORDER BY ti.ended_at DESC, ti.item_order ASC
		LIMIT 1`, target.Artifact.ArtifactID,
	).Scan(&taskKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domainError("REGENERATION_PLAN_CONFLICT", "无法定位该集对应的原视频任务。")
	}
	if err != nil {
		return nil, err
	}
	return []string{taskKey}, nil
}

func attachRevisionInstructionTx(ctx context.Context, tx *sql.Tx, plan *RegenerationPlan, instruction string) error {
	if instruction == "" || len(plan.Groups) == 0 || plan.Groups[0].StepRunID == nil {
		return nil
	}
	stepRunID := *plan.Groups[0].StepRunID
	var stepCursorJSON string
	if err := tx.QueryRowContext(ctx, `SELECT task_cursor_json FROM step_runs WHERE step_run_id = ?`, stepRunID).Scan(&stepCursorJSON); err != nil {
		return err
	}
	stepCursor, err := cursorWithRevisionInstruction(stepCursorJSON, instruction)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE step_runs SET task_cursor_json = ? WHERE step_run_id = ?`, stepCursor, stepRunID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT task_item_id, cursor_json FROM task_items WHERE step_run_id = ?`, stepRunID)
	if err != nil {
		return err
	}
	type taskCursor struct{ id, encoded string }
	var taskCursors []taskCursor
	for rows.Next() {
		var taskID, cursorJSON string
		if err := rows.Scan(&taskID, &cursorJSON); err != nil {
			rows.Close()
			return err
		}
		encoded, err := cursorWithRevisionInstruction(cursorJSON, instruction)
		if err != nil {
			rows.Close()
			return err
		}
		taskCursors = append(taskCursors, taskCursor{id: taskID, encoded: encoded})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, cursor := range taskCursors {
		if _, err := tx.ExecContext(ctx, `UPDATE task_items SET cursor_json = ? WHERE task_item_id = ?`, cursor.encoded, cursor.id); err != nil {
			return err
		}
	}
	return nil
}

func cursorWithRevisionInstruction(encoded string, instruction string) (string, error) {
	cursor := map[string]any{}
	if strings.TrimSpace(encoded) != "" {
		if err := json.Unmarshal([]byte(encoded), &cursor); err != nil {
			return "", err
		}
	}
	cursor["revision_instruction"] = instruction
	result, err := json.Marshal(cursor)
	return string(result), err
}
