package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"content-agent/backend/internal/capability"
)

func (s *Store) ListArtifactsByProject(ctx context.Context, projectID string) ([]Artifact, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	return s.listArtifacts(ctx, `
		SELECT a.artifact_id, a.project_id, COALESCE(a.run_id, ''), COALESCE(a.step_run_id, ''),
			a.origin_type, a.origin_id, a.capability_id,
			a.artifact_type, a.scope_key, a.current_version_id,
			COALESCE(av.status, ''), COALESCE(av.payload_json, '{}'), a.created_at, a.updated_at
		FROM artifacts a
		LEFT JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
		WHERE a.project_id = ?
		ORDER BY a.updated_at DESC, a.artifact_id ASC`, projectID)
}

func (s *Store) ListArtifactsByRun(ctx context.Context, runID string) ([]Artifact, error) {
	return s.listArtifacts(ctx, `
		SELECT a.artifact_id, a.project_id, COALESCE(a.run_id, ''), COALESCE(a.step_run_id, ''),
			a.origin_type, a.origin_id, a.capability_id,
			a.artifact_type, a.scope_key, a.current_version_id,
			COALESCE(av.status, ''), COALESCE(av.payload_json, '{}'), a.created_at, a.updated_at
		FROM artifacts a
		LEFT JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
		WHERE a.run_id = ?
		ORDER BY a.created_at ASC, a.artifact_id ASC`, runID)
}

func (s *Store) listArtifacts(ctx context.Context, query string, argument any) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, query, argument)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Artifact
	for rows.Next() {
		artifact, err := scanArtifactWithStatus(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, artifact)
	}
	if result == nil {
		result = []Artifact{}
	}
	return result, rows.Err()
}

func (s *Store) GetArtifact(ctx context.Context, artifactID string) (Artifact, error) {
	return getArtifactQuery(ctx, s.db, artifactID)
}

func getArtifactQuery(ctx context.Context, query rowQueryer, artifactID string) (Artifact, error) {
	artifact, err := scanArtifact(query.QueryRowContext(ctx, `
		SELECT artifact_id, project_id, COALESCE(run_id, ''), COALESCE(step_run_id, ''),
			origin_type, origin_id, capability_id,
			artifact_type, scope_key, current_version_id, created_at, updated_at
		FROM artifacts WHERE artifact_id = ?`, artifactID))
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, domainError("ARTIFACT_NOT_FOUND", "产物不存在。")
	}
	return artifact, err
}

func (s *Store) ListArtifactVersions(ctx context.Context, artifactID string) ([]ArtifactVersion, error) {
	if _, err := s.GetArtifact(ctx, artifactID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason, base_version_id,
			created_at, confirmed_at
		FROM artifact_versions WHERE artifact_id = ?
		ORDER BY version ASC`, artifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ArtifactVersion
	for rows.Next() {
		version, err := scanArtifactVersion(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, version)
	}
	if result == nil {
		result = []ArtifactVersion{}
	}
	return result, rows.Err()
}

func (s *Store) GetArtifactVersion(ctx context.Context, versionID string) (ArtifactVersion, error) {
	return getArtifactVersionQuery(ctx, s.db, versionID)
}

func getArtifactVersionQuery(ctx context.Context, query rowQueryer, versionID string) (ArtifactVersion, error) {
	version, err := scanArtifactVersion(query.QueryRowContext(ctx, `
		SELECT artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason, base_version_id,
			created_at, confirmed_at
		FROM artifact_versions WHERE artifact_version_id = ?`, versionID))
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactVersion{}, domainError("ARTIFACT_VERSION_NOT_FOUND", "产物版本不存在。")
	}
	return version, err
}

func (s *Store) CreateArtifactVersion(ctx context.Context, command CreateVersionCommand) (VersionResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return VersionResult{}, err
	}
	defer tx.Rollback()
	result, err := s.createArtifactVersionTx(ctx, tx, command)
	if err != nil {
		return VersionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return VersionResult{}, err
	}
	return result, nil
}

func (s *Store) createArtifactVersionTx(ctx context.Context, tx *sql.Tx, command CreateVersionCommand) (VersionResult, error) {
	if !jsonObject(command.NewPayload) {
		return VersionResult{}, domainError("ARTIFACT_PAYLOAD_INVALID", "产物内容必须是 JSON 对象。")
	}
	if command.ActorRef == "" {
		command.ActorRef = actorRefFromContext(ctx)
	}
	now := s.now()
	newVersionID := s.newID("av")
	newApprovalID := s.newID("apr")

	var artifact Artifact
	var createdAt, updatedAt string
	if err := tx.QueryRowContext(ctx, `
		SELECT artifact_id, project_id, run_id, step_run_id, capability_id,
			artifact_type, scope_key, current_version_id, created_at, updated_at
		FROM artifacts WHERE artifact_id = ?`, command.ArtifactID).Scan(
		&artifact.ArtifactID, &artifact.ProjectID, &artifact.RunID, &artifact.StepRunID,
		&artifact.CapabilityID, &artifact.ArtifactType, &artifact.ScopeKey,
		&artifact.CurrentVersionID, &createdAt, &updatedAt,
	); errors.Is(err, sql.ErrNoRows) {
		return VersionResult{}, domainError("ARTIFACT_NOT_FOUND", "产物不存在。")
	} else if err != nil {
		return VersionResult{}, err
	}
	if err := authorizeArtifactCommandTx(ctx, tx, artifact.ProjectID, command.Scope); err != nil {
		return VersionResult{}, err
	}
	if command.Scope == "" {
		command.Scope = artifact.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return VersionResult{}, err
	}
	if hit {
		return decodeArtifactVersionReceiptTx(ctx, tx, cached, artifact, command)
	}
	var currentVersion int
	if err := tx.QueryRowContext(ctx, `
		SELECT version FROM artifact_versions
		WHERE artifact_version_id = ? AND artifact_id = ?`,
		artifact.CurrentVersionID, artifact.ArtifactID,
	).Scan(&currentVersion); err != nil {
		return VersionResult{}, err
	}
	if command.BaseVersionID != artifact.CurrentVersionID || command.BaseVersion != currentVersion {
		return VersionResult{}, domainError(
			"ARTIFACT_VERSION_CONFLICT",
			fmt.Sprintf("产物已更新，当前版本为 %d。", currentVersion),
		)
	}
	if err := s.validateArtifactEditTx(ctx, tx, artifact, command.NewPayload); err != nil {
		return VersionResult{}, err
	}

	var template Approval
	var optionsJSON, requestedAt string
	var resolvedAt, resolutionJSON, actorRef sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT approval_request_id, project_id, run_id, step_run_id, scope, status, version,
			title, reason, options_json, subject_kind, subject_ref_id, subject_version,
			subject_snapshot_hash, requested_at, resolved_at, resolution_json, actor_ref
		FROM approvals
		WHERE subject_ref_id = ?
		ORDER BY requested_at DESC LIMIT 1`, artifact.CurrentVersionID).Scan(
		&template.ApprovalRequestID, &template.ProjectID, &template.RunID, &template.StepRunID,
		&template.Scope, &template.Status, &template.Version, &template.Title, &template.Reason,
		&optionsJSON, &template.SubjectKind, &template.SubjectRefID, &template.SubjectVersion,
		&template.SubjectSnapshotHash, &requestedAt, &resolvedAt, &resolutionJSON, &actorRef,
	); errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `
			SELECT approval_request_id, project_id, run_id, step_run_id, scope, status, version,
				title, reason, options_json, subject_kind, subject_ref_id, subject_version,
				subject_snapshot_hash, requested_at, resolved_at, resolution_json, actor_ref
			FROM approvals
			WHERE step_run_id = ? AND subject_kind = 'artifact_version_set'
			ORDER BY requested_at DESC LIMIT 1`,
			artifact.StepRunID,
		).Scan(
			&template.ApprovalRequestID, &template.ProjectID, &template.RunID,
			&template.StepRunID, &template.Scope, &template.Status, &template.Version,
			&template.Title, &template.Reason, &optionsJSON, &template.SubjectKind,
			&template.SubjectRefID, &template.SubjectVersion,
			&template.SubjectSnapshotHash, &requestedAt, &resolvedAt,
			&resolutionJSON, &actorRef,
		)
		if err != nil {
			return VersionResult{}, fmt.Errorf("load batch approval template: %w", err)
		}
	} else if err != nil {
		return VersionResult{}, fmt.Errorf("load approval template: %w", err)
	}
	if err := json.Unmarshal([]byte(optionsJSON), &template.Options); err != nil {
		return VersionResult{}, err
	}
	var batchStep capability.CompiledStep
	var partialContinuationPayload json.RawMessage
	var approvalEventPayload map[string]any
	scriptEditRequiresHandoffRefresh := false
	reopenedCompletedScriptEdit := false
	if template.SubjectKind == "artifact_version_set" {
		batchStep, err = s.compiledStepForRunTx(
			ctx,
			tx,
			artifact.RunID,
			artifact.StepRunID,
		)
		if err != nil {
			return VersionResult{}, err
		}
		scriptEditRequiresHandoffRefresh =
			artifact.ArtifactType == "script_unit" &&
				isScriptBundleStep(batchStep)
		if template.Status == "pending" && !scriptEditRequiresHandoffRefresh {
			var partialRequest bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events
				WHERE project_id=? AND run_id=? AND step_run_id=? AND event_type='approval.requested'
				AND subject_type='approval' AND subject_id=? AND json_extract(payload_json, '$.decision_snapshot_id') IS NOT NULL)`,
				artifact.ProjectID, artifact.RunID, artifact.StepRunID, template.ApprovalRequestID).Scan(&partialRequest); err != nil {
				return VersionResult{}, err
			}
			if partialRequest {
				failed, partial, err := partialBatchContinuationForStepApprovalTx(ctx, tx, artifact.RunID, artifact.StepRunID, nil, &template)
				if err != nil {
					return VersionResult{}, err
				}
				if !partial {
					return VersionResult{}, domainError("PARTIAL_CONTINUATION_CONFLICT", "部分结果确认的任务集合已经变化。")
				}
				var succeeded int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_items WHERE run_id=? AND step_run_id=? AND status='succeeded'`, artifact.RunID, artifact.StepRunID).Scan(&succeeded); err != nil {
					return VersionResult{}, err
				}
				partialContinuationPayload, err = json.Marshal(map[string]any{
					"mode": "continue_with_partial_results", "succeeded_count": succeeded, "failed_item_keys": failed,
				})
				if err != nil {
					return VersionResult{}, err
				}
			}
		}
	}
	if scriptEditRequiresHandoffRefresh {
		var runStatus, stepStatus string
		if err := tx.QueryRowContext(ctx, `
			SELECT r.status, sr.status
			FROM runs r
			JOIN step_runs sr ON sr.step_run_id = ? AND sr.run_id = r.run_id
			WHERE r.run_id = ?`,
			artifact.StepRunID,
			artifact.RunID,
		).Scan(&runStatus, &stepStatus); err != nil {
			return VersionResult{}, err
		}
		qualityReviewManualEdit := false
		var qualityReviewID, qualityApprovalID, qualityStepRunID string
		if runStatus == "waiting_approval" && stepStatus == "completed" {
			err := tx.QueryRowContext(ctx, `
				SELECT qr.quality_review_id, approval.approval_request_id, qr.step_run_id
				FROM quality_reviews qr
				JOIN approvals approval ON approval.subject_kind = 'quality_review'
					AND approval.subject_ref_id = qr.quality_review_id
				WHERE qr.run_id = ? AND qr.status = 'action_required'
					AND approval.status = 'pending'
				ORDER BY qr.created_at DESC LIMIT 1`, artifact.RunID).Scan(
				&qualityReviewID, &qualityApprovalID, &qualityStepRunID,
			)
			qualityReviewManualEdit = err == nil
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return VersionResult{}, err
			}
		}
		reopenedCompletedScriptEdit = runStatus == "completed" && stepStatus == "completed"
		if runStatus != "waiting_approval" && !reopenedCompletedScriptEdit ||
			(runStatus == "waiting_approval" && stepStatus != "waiting_approval" && !qualityReviewManualEdit) {
			return VersionResult{}, domainError(
				"RUN_STATE_CONFLICT",
				"交接刷新执行期间不能继续保存剧本，请等待本轮刷新完成。",
			)
		}
		if reopenedCompletedScriptEdit {
			if err := updateExactlyOne(ctx, tx, `
				UPDATE step_runs SET status = 'waiting_approval', ended_at = NULL
				WHERE step_run_id = ? AND run_id = ? AND status = 'completed'`,
				"剧本步骤已经变化。", artifact.StepRunID, artifact.RunID); err != nil {
				return VersionResult{}, err
			}
			if err := updateExactlyOne(ctx, tx, `
				UPDATE runs SET status = 'waiting_approval', current_step_run_id = ?,
					ended_at = NULL, updated_at = ?
				WHERE run_id = ? AND status = 'completed'`,
				"生成任务状态已经变化。", artifact.StepRunID, formatTime(now), artifact.RunID); err != nil {
				return VersionResult{}, err
			}
			if err := updateExactlyOne(ctx, tx, `
				UPDATE projects SET version = version + 1, status = 'waiting_approval',
					active_write_run_id = ?, current_capability_id = ?, updated_at = ?
				WHERE project_id = ? AND active_write_run_id IS NULL`,
				"作品当前有其他写任务，暂时不能修改剧本。",
				artifact.RunID, artifact.CapabilityID, formatTime(now), artifact.ProjectID); err != nil {
				return VersionResult{}, err
			}
			// Unchanged confirmed units still belong to the previous candidate.
			// Only the new edited version and its refreshed handoff need approval.
		}
		if qualityReviewManualEdit {
			resolution, _ := json.Marshal(map[string]any{"action": "manual_edit", "artifact_id": artifact.ArtifactID})
			if err := updateExactlyOne(ctx, tx, `
				UPDATE approvals SET status = 'approved', resolved_at = ?, resolution_json = ?, actor_ref = ?
				WHERE approval_request_id = ? AND status = 'pending'`,
				"质量审核确认请求已经变化。", formatTime(now), string(resolution), command.ActorRef, qualityApprovalID); err != nil {
				return VersionResult{}, err
			}
			if err := updateExactlyOne(ctx, tx, `
				UPDATE quality_reviews SET status = 'superseded', finished_at = ?
				WHERE quality_review_id = ? AND status = 'action_required'`,
				"质量审核状态已经变化。", formatTime(now), qualityReviewID); err != nil {
				return VersionResult{}, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE step_runs SET status = 'superseded', ended_at = COALESCE(ended_at, ?) WHERE step_run_id = ? AND status = 'waiting_approval'`,
				formatTime(now), qualityStepRunID); err != nil {
				return VersionResult{}, err
			}
			if err := updateExactlyOne(ctx, tx, `UPDATE step_runs SET status = 'waiting_approval', ended_at = NULL WHERE step_run_id = ? AND status = 'completed'`,
				"剧本步骤已经变化。", artifact.StepRunID); err != nil {
				return VersionResult{}, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE runs SET current_step_run_id = ?, updated_at = ? WHERE run_id = ? AND status = 'waiting_approval'`,
				artifact.StepRunID, formatTime(now), artifact.RunID); err != nil {
				return VersionResult{}, err
			}
			runRef, reviewStepRef := artifact.RunID, qualityStepRunID
			if _, err := s.appendEvent(ctx, tx, artifact.ProjectID, &runRef, &reviewStepRef,
				"quality_review.superseded", "quality_review", qualityReviewID,
				map[string]any{"action": "manual_edit", "artifact_id": artifact.ArtifactID}); err != nil {
				return VersionResult{}, err
			}
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE artifact_versions SET status = 'superseded'
		WHERE artifact_version_id = ?`, artifact.CurrentVersionID); err != nil {
		return VersionResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE approvals
		SET status = 'expired', resolved_at = ?
		WHERE approval_request_id = ? AND status = 'pending'`,
		formatTime(now), template.ApprovalRequestID,
	); err != nil {
		return VersionResult{}, err
	}
	nextVersion, err := nextArtifactVersionTx(ctx, tx, artifact.ArtifactID)
	if err != nil {
		return VersionResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason, base_version_id, created_at
		) VALUES(?, ?, ?, 'pending_approval', ?, ?, '1.0.0', 'user', ?, 'manual_edit', ?, ?)`,
		newVersionID, artifact.ArtifactID, nextVersion, string(command.NewPayload), artifact.ArtifactType,
		command.ActorRef, artifact.CurrentVersionID, formatTime(now),
	); err != nil {
		return VersionResult{}, err
	}
	if err := s.copyArtifactDependenciesTx(
		ctx,
		tx,
		artifact.ProjectID,
		artifact.RunID,
		artifact.CurrentVersionID,
		newVersionID,
		now,
	); err != nil {
		return VersionResult{}, err
	}
	changeSet, err := s.createArtifactChangeSetTx(
		ctx,
		tx,
		artifact.ProjectID,
		newVersionID,
		artifact.CurrentVersionID,
		artifact.ScopeKey,
		command.ChangeMode,
		now,
	)
	if err != nil {
		return VersionResult{}, err
	}
	var impactReview *ImpactReview
	var expiredImpactReviewIDs []string
	if !scriptEditRequiresHandoffRefresh {
		impactReview, expiredImpactReviewIDs, err = s.createImpactReviewTx(
			ctx,
			tx,
			artifact,
			artifact.CurrentVersionID,
			currentVersion,
			newVersionID,
			changeSet,
			now,
		)
		if err != nil {
			return VersionResult{}, err
		}
	}
	newSubjectHash := approvalSubjectHash(newVersionID, nextVersion)
	if template.SubjectKind != "artifact_version_set" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO approvals(
				approval_request_id, project_id, run_id, step_run_id, scope, status, version,
				title, reason, options_json, subject_kind, subject_ref_id, subject_version,
				subject_snapshot_hash, requested_at
			) VALUES(?, ?, ?, ?, ?, 'pending', 1, ?, ?, ?, 'artifact_version', ?, ?, ?, ?)`,
			newApprovalID, template.ProjectID, template.RunID, template.StepRunID, template.Scope,
			template.Title, template.Reason, optionsJSON, newVersionID, nextVersion,
			newSubjectHash, formatTime(now),
		); err != nil {
			return VersionResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE artifacts SET current_version_id = ?, updated_at = ? WHERE artifact_id = ?`,
		newVersionID, formatTime(now), artifact.ArtifactID,
	); err != nil {
		return VersionResult{}, err
	}
	var replacementBatchApproval *Approval
	staleHandoffArtifactID := ""
	if template.SubjectKind == "artifact_version_set" {
		if len(batchStep.OutputRefs) == 0 {
			return VersionResult{}, domainError("APPROVAL_SUBJECT_CHANGED", "确认步骤缺少输出声明。")
		}
		secondarySkillOutput := batchStep.Batch == nil && batchStep.ExecutorRef == "worker.structured_content" &&
			len(batchStep.OutputRefs) > 1 && capability.SupportsDirectSkillOutput(batchStep) &&
			artifact.ArtifactType != batchStep.OutputRefs[0].ArtifactType
		if !secondarySkillOutput {
			if err := updateExactlyOne(ctx, tx, `
			UPDATE task_items
			SET output_artifact_version_id = ?, updated_at = ?
			WHERE step_run_id = ? AND output_artifact_version_id = ?
				AND status = 'succeeded'`,
				"单集剧本 Task 绑定版本已经变化。",
				newVersionID,
				formatTime(now),
				artifact.StepRunID,
				artifact.CurrentVersionID,
			); err != nil {
				return VersionResult{}, err
			}
		}
		if scriptEditRequiresHandoffRefresh {
			if err := tx.QueryRowContext(ctx, `
				SELECT artifact_id FROM artifacts
				WHERE run_id = ? AND step_run_id = ?
					AND artifact_type = 'script_handoff' AND scope_key = ?`,
				artifact.RunID,
				artifact.StepRunID,
				artifact.ScopeKey,
			).Scan(&staleHandoffArtifactID); err != nil {
				return VersionResult{}, err
			}
			if err := updateExactlyOne(ctx, tx, `
				UPDATE artifact_versions
				SET status = 'stale'
				WHERE artifact_version_id = (
					SELECT current_version_id FROM artifacts
					WHERE run_id = ? AND artifact_type = 'script_handoff'
						AND scope_key = ?
				)
				AND status IN ('confirmed', 'pending_approval', 'stale')`,
				"对应剧本交接版本已经变化。",
				artifact.RunID,
				artifact.ScopeKey,
			); err != nil {
				return VersionResult{}, err
			}
		} else {
			created, err := s.createArtifactVersionSetApprovalTx(
				ctx,
				tx,
				artifact.ProjectID,
				artifact.RunID,
				artifact.StepRunID,
				artifact.ArtifactType,
				batchStep,
				now,
			)
			if err != nil {
				return VersionResult{}, err
			}
			if len(partialContinuationPayload) > 0 {
				decision, err := s.createRunDecisionSnapshotTx(ctx, tx, artifact.ProjectID, artifact.RunID, artifact.StepRunID,
					"partial_batch_continuation", "step_run", artifact.StepRunID, partialContinuationPayload, now)
				if err != nil {
					return VersionResult{}, err
				}
				if err := updateExactlyOne(ctx, tx, `UPDATE approvals SET title=?, reason=? WHERE approval_request_id=? AND status='pending'`,
					"部分结果的新确认请求已经变化。", template.Title, template.Reason, created.ApprovalRequestID); err != nil {
					return VersionResult{}, err
				}
				created.Title, created.Reason = template.Title, template.Reason
				approvalEventPayload = map[string]any{"decision_snapshot_id": decision.DecisionSnapshotID, "replaces_approval_request_id": template.ApprovalRequestID}
			}
			replacementBatchApproval = &created
			newApprovalID = created.ApprovalRequestID
			newSubjectHash = created.SubjectSnapshotHash
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET version = version + 1, current_focus_artifact_version_id = ?, updated_at = ?
		WHERE project_id = ?`,
		newVersionID, formatTime(now), artifact.ProjectID,
	); err != nil {
		return VersionResult{}, err
	}

	runRef, stepRef := artifact.RunID, artifact.StepRunID
	if _, err := s.appendEvent(ctx, tx, artifact.ProjectID, &runRef, &stepRef, "artifact.version_created", "artifact_version", newVersionID, map[string]any{"version": nextVersion}); err != nil {
		return VersionResult{}, err
	}
	if _, err := s.appendEvent(ctx, tx, artifact.ProjectID, &runRef, &stepRef, "artifact.change_set_created", "change_set", changeSet.ChangeSetID, map[string]any{"change_mode": changeSet.ChangeMode}); err != nil {
		return VersionResult{}, err
	}
	for _, expiredImpactReviewID := range expiredImpactReviewIDs {
		if _, err := s.appendEvent(ctx, tx, artifact.ProjectID, &runRef, &stepRef, "impact_review.expired", "impact_review", expiredImpactReviewID, map[string]any{"reason": "source_reedited"}); err != nil {
			return VersionResult{}, err
		}
	}
	if impactReview != nil {
		if _, err := s.appendEvent(ctx, tx, artifact.ProjectID, &runRef, &stepRef, "impact_review.created", "impact_review", impactReview.ImpactReviewID, map[string]any{
			"affected_count": len(impactReview.AffectedItems),
			"snapshot_hash":  impactReview.SnapshotHash,
		}); err != nil {
			return VersionResult{}, err
		}
	}
	if template.Status == "pending" {
		if _, err := s.appendEvent(ctx, tx, artifact.ProjectID, &runRef, &stepRef, "approval.expired", "approval", template.ApprovalRequestID, nil); err != nil {
			return VersionResult{}, err
		}
	}
	if scriptEditRequiresHandoffRefresh {
		if _, err := s.appendEvent(ctx, tx, artifact.ProjectID, &runRef, &stepRef, "script_handoff.stale", "artifact", staleHandoffArtifactID, map[string]any{"scope_key": artifact.ScopeKey}); err != nil {
			return VersionResult{}, err
		}
		if _, err := s.appendEvent(ctx, tx, artifact.ProjectID, &runRef, &stepRef, "script_edit.awaiting_handoff_refresh", "artifact_version", newVersionID, map[string]any{"scope_key": artifact.ScopeKey}); err != nil {
			return VersionResult{}, err
		}
		if reopenedCompletedScriptEdit {
			if _, err := s.appendEvent(ctx, tx, artifact.ProjectID, &runRef, &stepRef, "run.reopened", "run", artifact.RunID, map[string]any{"reason": "script_edit"}); err != nil {
				return VersionResult{}, err
			}
		}
	} else {
		if _, err := s.appendEvent(ctx, tx, artifact.ProjectID, &runRef, &stepRef, "approval.requested", "approval", newApprovalID, approvalEventPayload); err != nil {
			return VersionResult{}, err
		}
	}
	baseVersionID := artifact.CurrentVersionID
	resultApproval := Approval{}
	if !scriptEditRequiresHandoffRefresh {
		resultApproval = Approval{
			ApprovalRequestID:   newApprovalID,
			ProjectID:           template.ProjectID,
			RunID:               template.RunID,
			StepRunID:           template.StepRunID,
			Scope:               template.Scope,
			Status:              "pending",
			Version:             1,
			Title:               template.Title,
			Reason:              template.Reason,
			Options:             slices.Clone(template.Options),
			SubjectKind:         "artifact_version",
			SubjectRefID:        newVersionID,
			SubjectVersion:      nextVersion,
			SubjectSnapshotHash: newSubjectHash,
			RequestedAt:         now,
		}
	}
	if replacementBatchApproval != nil {
		resultApproval = *replacementBatchApproval
	}
	var pendingRefreshScopes []string
	var pendingRefreshVersionIDs []string
	if scriptEditRequiresHandoffRefresh {
		pending, pendingErr := pendingScriptHandoffsTx(
			ctx,
			tx,
			artifact.RunID,
			artifact.StepRunID,
		)
		if pendingErr != nil {
			return VersionResult{}, pendingErr
		}
		for _, item := range pending {
			pendingRefreshScopes = append(pendingRefreshScopes, item.ScopeKey)
			pendingRefreshVersionIDs = append(pendingRefreshVersionIDs, item.ScriptVersionID)
		}
	}
	versionResult := VersionResult{
		ArtifactVersion: ArtifactVersion{
			ArtifactVersionID: newVersionID,
			ArtifactID:        artifact.ArtifactID,
			Version:           nextVersion,
			Status:            "pending_approval",
			Payload:           slices.Clone(command.NewPayload),
			SchemaID:          artifact.ArtifactType,
			SchemaVersion:     "1.0.0",
			CreatedByKind:     "user",
			ActorRef:          command.ActorRef,
			CreationReason:    "manual_edit",
			BaseVersionID:     &baseVersionID,
			CreatedAt:         now,
		},
		Approval:                 resultApproval,
		ImpactReview:             impactReview,
		HandoffRefreshRequired:   scriptEditRequiresHandoffRefresh,
		PendingRefreshScopes:     pendingRefreshScopes,
		PendingRefreshVersionIDs: pendingRefreshVersionIDs,
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, versionResult, now); err != nil {
		return VersionResult{}, err
	}
	return versionResult, nil
}

func scanArtifact(row rowScanner) (Artifact, error) {
	var artifact Artifact
	var createdAt, updatedAt string
	if err := row.Scan(
		&artifact.ArtifactID, &artifact.ProjectID, &artifact.RunID, &artifact.StepRunID,
		&artifact.OriginType, &artifact.OriginID,
		&artifact.CapabilityID, &artifact.ArtifactType, &artifact.ScopeKey,
		&artifact.CurrentVersionID, &createdAt, &updatedAt,
	); err != nil {
		return Artifact{}, err
	}
	var err error
	artifact.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Artifact{}, err
	}
	artifact.UpdatedAt, err = parseTime(updatedAt)
	return artifact, err
}

func scanArtifactWithStatus(row rowScanner) (Artifact, error) {
	var artifact Artifact
	var payloadJSON, createdAt, updatedAt string
	if err := row.Scan(
		&artifact.ArtifactID, &artifact.ProjectID, &artifact.RunID, &artifact.StepRunID,
		&artifact.OriginType, &artifact.OriginID,
		&artifact.CapabilityID, &artifact.ArtifactType, &artifact.ScopeKey,
		&artifact.CurrentVersionID, &artifact.Status, &payloadJSON, &createdAt, &updatedAt,
	); err != nil {
		return Artifact{}, err
	}
	if artifact.ArtifactType == "generic_document" || artifact.ArtifactType == "generic_table" {
		var payload struct {
			ArtifactLabel string `json:"artifact_label"`
			Title         string `json:"title"`
		}
		if json.Unmarshal([]byte(payloadJSON), &payload) == nil {
			artifact.Title = strings.TrimSpace(payload.ArtifactLabel)
			if artifact.Title == "" {
				artifact.Title = strings.TrimSpace(payload.Title)
			}
		}
	}
	var err error
	artifact.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Artifact{}, err
	}
	artifact.UpdatedAt, err = parseTime(updatedAt)
	return artifact, err
}

func scanArtifactVersion(row rowScanner) (ArtifactVersion, error) {
	var version ArtifactVersion
	var payloadJSON, createdAt string
	var baseVersionID, confirmedAt sql.NullString
	if err := row.Scan(
		&version.ArtifactVersionID, &version.ArtifactID, &version.Version, &version.Status,
		&payloadJSON, &version.SchemaID, &version.SchemaVersion, &version.CreatedByKind,
		&version.ActorRef, &version.CreationReason, &baseVersionID, &createdAt, &confirmedAt,
	); err != nil {
		return ArtifactVersion{}, err
	}
	version.Payload = json.RawMessage(payloadJSON)
	version.BaseVersionID = stringPointer(baseVersionID)
	var err error
	version.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return ArtifactVersion{}, err
	}
	version.ConfirmedAt, err = optionalTime(confirmedAt)
	return version, err
}
