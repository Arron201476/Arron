package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"content-agent/backend/internal/capability"
)

type approvalVersionSetItem struct {
	ArtifactVersionID string `json:"artifact_version_id"`
	ItemOrder         int    `json:"item_order"`
	ScopeKey          string `json:"scope_key"`
	ArtifactType      string `json:"artifact_type"`
}

func (s *Store) commitScopedBatchArtifactTx(
	ctx context.Context,
	tx *sql.Tx,
	attemptID string,
	taskItemID string,
	runID string,
	projectID string,
	stepRunID string,
	capabilityID string,
	scopeKey string,
	output capability.ArtifactOutput,
	step capability.CompiledStep,
	contextPack StepExecutionContextPack,
	directSkillOutput bool,
	payload json.RawMessage,
	now time.Time,
) (ArtifactCommitResult, error) {
	episodeNo, err := episodeNumberFromScope(scopeKey)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	var payloadIdentity struct {
		EpisodeNo int `json:"episode_no"`
	}
	if err := json.Unmarshal(payload, &payloadIdentity); err != nil ||
		payloadIdentity.EpisodeNo != episodeNo {
		return ArtifactCommitResult{}, domainError(
			"BATCH_COVERAGE_INVALID",
			"单集产物的 episode_no 与 Task 作用域不一致。",
		)
	}

	binding, err := regenerationBindingForStepTx(ctx, tx, stepRunID)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if binding != nil && (binding.PlanStatus != "running" || binding.GroupStatus != "running" ||
		binding.GroupOrder != binding.CurrentOrder) {
		return ArtifactCommitResult{}, domainError("REGENERATION_PLAN_CONFLICT", "当前重生成组已经变化，执行结果不能提交。")
	}

	artifactID := s.newID("art")
	versionID := s.newID("av")
	versionNumber := 1
	creationReason := "initial"
	createdArtifact := true
	var baseVersionID *string
	var existingArtifactID, existingVersionID, existingStatus string
	var existingVersion int
	lookupErr := tx.QueryRowContext(ctx, `
		SELECT a.artifact_id, a.current_version_id, av.version, av.status
		FROM artifacts a
		JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
		WHERE a.run_id = ? AND a.artifact_type = ? AND a.scope_key = ?`,
		runID, output.ArtifactType, scopeKey,
	).Scan(&existingArtifactID, &existingVersionID, &existingVersion, &existingStatus)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return ArtifactCommitResult{}, lookupErr
	}
	if lookupErr == nil {
		if binding == nil || existingStatus != "stale" ||
			!slices.Contains(binding.StaleVersionIDs, existingVersionID) {
			return ArtifactCommitResult{}, domainError("ARTIFACT_COMMIT_CONFLICT", "该集正式产物已经存在。")
		}
		artifactID = existingArtifactID
		versionNumber = existingVersion + 1
		creationReason = "regeneration"
		createdArtifact = false
		baseVersionID = &existingVersionID
	} else if binding != nil {
		creationReason = "regeneration"
	}

	if createdArtifact {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifacts(
				artifact_id, project_id, run_id, step_run_id, capability_id, artifact_type,
				scope_key, current_version_id, created_at, updated_at
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			artifactID, projectID, runID, stepRunID, capabilityID, output.ArtifactType,
			scopeKey, versionID, formatTime(now), formatTime(now),
		); err != nil {
			if isUniqueConstraint(err) {
				return ArtifactCommitResult{}, domainError("ARTIFACT_COMMIT_CONFLICT", "该集正式产物已经存在。")
			}
			return ArtifactCommitResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason, base_version_id, created_at
		) VALUES(?, ?, ?, 'pending_approval', ?, ?, '1.0.0', 'model', ?, ?, ?, ?)`,
		versionID, artifactID, versionNumber, string(payload), output.ArtifactType,
		attemptID, creationReason, baseVersionID, formatTime(now),
	); err != nil {
		return ArtifactCommitResult{}, err
	}
	if !createdArtifact {
		if err := updateExactlyOne(ctx, tx, `
			UPDATE artifacts SET current_version_id = ?, step_run_id = ?, updated_at = ?
			WHERE artifact_id = ? AND current_version_id = ?`,
			"待替代产物已经变化。", versionID, stepRunID, formatTime(now), artifactID, existingVersionID); err != nil {
			return ArtifactCommitResult{}, err
		}
		if err := updateExactlyOne(ctx, tx, `
			UPDATE artifact_versions SET status = 'superseded'
			WHERE artifact_version_id = ? AND status = 'stale'`,
			"待替代版本已经变化。", existingVersionID); err != nil {
			return ArtifactCommitResult{}, err
		}
	}
	for _, input := range contextPack.UpstreamContext {
		if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
			ProjectID:                   projectID,
			RunID:                       runID,
			DownstreamArtifactVersionID: versionID,
			UpstreamKind:                "artifact_version",
			UpstreamRefID:               input.ArtifactVersionID,
			Relation:                    "derived_from",
			UpstreamScope:               dependencyScope(scopeKind(input.ScopeKey), input.ScopeKey),
			DownstreamScope:             dependencyScope("episode", scopeKey),
			ImpactPolicyID:              "scope_intersection",
		}, now); err != nil {
			return ArtifactCommitResult{}, err
		}
	}
	if err := s.insertTaskContextDependenciesTx(ctx, tx, projectID, runID, versionID,
		scopeKey, contextPack, directSkillOutput, now); err != nil {
		return ArtifactCommitResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE execution_attempts SET status = 'succeeded', ended_at = ?
		WHERE attempt_id = ? AND status = 'result_received'`,
		"执行尝试状态已变化。",
		formatTime(now),
		attemptID,
	); err != nil {
		return ArtifactCommitResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE task_items
		SET status = 'succeeded', output_artifact_version_id = ?,
			failure = NULL, ended_at = ?, updated_at = ?
		WHERE task_item_id = ? AND status = 'running' AND current_attempt_id = ?`,
		"执行任务状态已变化。",
		versionID,
		formatTime(now),
		formatTime(now),
		taskItemID,
		attemptID,
	); err != nil {
		return ArtifactCommitResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET version = version + 1, current_focus_artifact_version_id = ?, updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		versionID,
		formatTime(now),
		projectID,
		runID,
	); err != nil {
		return ArtifactCommitResult{}, err
	}

	runRef, stepRef := runID, stepRunID
	type scopedArtifactEvent struct {
		eventType   string
		subjectType string
		subjectID   string
		payload     any
	}
	events := []scopedArtifactEvent{}
	if createdArtifact {
		events = append(events, scopedArtifactEvent{"artifact.created", "artifact", artifactID, map[string]any{
			"artifact_type": output.ArtifactType, "scope_key": scopeKey,
		}})
	}
	events = append(events,
		scopedArtifactEvent{"artifact.version_created", "artifact_version", versionID, map[string]any{
			"version":   versionNumber,
			"scope_key": scopeKey,
		}},
		scopedArtifactEvent{"task.completed", "task_item", taskItemID, map[string]any{
			"attempt_id": attemptID,
			"scope_key":  scopeKey,
		}},
	)
	for _, event := range events {
		if _, err := s.appendEvent(
			ctx,
			tx,
			projectID,
			&runRef,
			&stepRef,
			event.eventType,
			event.subjectType,
			event.subjectID,
			event.payload,
		); err != nil {
			return ArtifactCommitResult{}, err
		}
	}

	var totalTasks, completedTasks, failedTasks int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*),
			SUM(CASE WHEN status = 'succeeded' THEN 1 ELSE 0 END),
			SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END)
		FROM task_items WHERE step_run_id = ?`,
		stepRunID,
	).Scan(&totalTasks, &completedTasks, &failedTasks); err != nil {
		return ArtifactCommitResult{}, err
	}
	var approval Approval
	commitStatus := "task_artifact_saved"
	if completedTasks+failedTasks < totalTasks {
		if err := s.pauseAfterCompletedTaskIfRequestedTx(
			ctx, tx, runID, now, "task_completed",
		); err != nil {
			return ArtifactCommitResult{}, err
		}
	} else if failedTasks > 0 && completedTasks+failedTasks == totalTasks &&
		step.Batch != nil && step.Batch.FailurePolicy == "preserve_success_retry_failed" {
		if _, err := s.finalizePreservedBatchFailureIfSettledTx(
			ctx, tx, projectID, runID, stepRunID, "WORKER_INTERNAL_ERROR", now,
		); err != nil {
			return ArtifactCommitResult{}, err
		}
		commitStatus = "batch_failed_preserving_success"
	} else if totalTasks > 0 && completedTasks == totalTasks {
		if binding != nil {
			if err := updateExactlyOne(ctx, tx, `
				UPDATE regeneration_plan_groups SET status = 'waiting_approval'
				WHERE regeneration_plan_group_id = ? AND status = 'running'`,
				"当前重生成组已经变化。", binding.GroupID); err != nil {
				return ArtifactCommitResult{}, err
			}
			if err := updateExactlyOne(ctx, tx, `
				UPDATE regeneration_plans SET status = 'waiting_approval'
				WHERE regeneration_plan_id = ? AND status = 'running'
					AND current_group_order = ?`,
				"当前重生成计划已经变化。", binding.PlanID, binding.GroupOrder); err != nil {
				return ArtifactCommitResult{}, err
			}
		}
		approval, err = s.createArtifactVersionSetApprovalTx(
			ctx,
			tx,
			projectID,
			runID,
			stepRunID,
			output.ArtifactType,
			step,
			now,
		)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		if err := s.markStepWaitingApprovalTx(ctx, tx, projectID, runID, stepRunID, approval, now); err != nil {
			return ArtifactCommitResult{}, err
		}
		commitStatus = "waiting_approval"
	}

	artifact, err := scanArtifact(tx.QueryRowContext(ctx, `
		SELECT artifact_id, project_id, COALESCE(run_id, ''), COALESCE(step_run_id, ''),
			origin_type, origin_id, capability_id,
			artifact_type, scope_key, current_version_id, created_at, updated_at
		FROM artifacts WHERE artifact_id = ?`,
		artifactID,
	))
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	version, err := scanArtifactVersion(tx.QueryRowContext(ctx, `
		SELECT artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason, base_version_id,
			created_at, confirmed_at
		FROM artifact_versions WHERE artifact_version_id = ?`,
		versionID,
	))
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, runID)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	return ArtifactCommitResult{
		CommitStatus:    commitStatus,
		Artifact:        artifact,
		ArtifactVersion: version,
		Approval:        approval,
		RunSnapshot:     snapshot,
	}, nil
}

func (s *Store) createArtifactVersionSetApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runID string,
	stepRunID string,
	artifactType string,
	step capability.CompiledStep,
	now time.Time,
) (Approval, error) {
	return s.createArtifactVersionSetApprovalForScopeTx(
		ctx, tx, projectID, runID, stepRunID, "", artifactType, step, now,
	)
}

func (s *Store) createEpisodeCheckpointApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runID string,
	stepRunID string,
	scopeKey string,
	artifactType string,
	step capability.CompiledStep,
	now time.Time,
) (Approval, error) {
	return s.createArtifactVersionSetApprovalForScopeTx(
		ctx, tx, projectID, runID, stepRunID, scopeKey, artifactType, step, now,
	)
}

func (s *Store) createArtifactVersionSetApprovalForScopeTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runID string,
	stepRunID string,
	scopeKey string,
	artifactType string,
	step capability.CompiledStep,
	now time.Time,
) (Approval, error) {
	items, err := approvalVersionSetForStepScopeTx(ctx, tx, stepRunID, scopeKey)
	if err != nil {
		return Approval{}, err
	}
	if len(items) == 0 {
		return Approval{}, domainError(
			"DEPENDENCY_INCOMPLETE",
			"整步确认缺少单集产物版本。",
		)
	}
	scopes := map[string]struct{}{}
	for _, item := range items {
		scopes[item.ScopeKey] = struct{}{}
	}
	expectedItemCount := len(scopes) * len(step.OutputRefs)
	if expectedItemCount != len(items) {
		return Approval{}, domainError(
			"DEPENDENCY_INCOMPLETE",
			"整步确认绑定的当前单集版本不完整。",
		)
	}
	snapshotHash, err := approvalVersionSetHash(items)
	if err != nil {
		return Approval{}, err
	}
	optionsJSON, err := json.Marshal(step.Approval.AllowedActions)
	if err != nil {
		return Approval{}, err
	}
	approvalID := s.newID("apr")
	title, reason := approvalPresentation(artifactType)
	approvalScope := step.Approval.Scope
	if scopeKey != "" {
		episodeNo, episodeErr := episodeNumberFromScope(scopeKey)
		if episodeErr != nil {
			return Approval{}, episodeErr
		}
		approvalScope = "episode_checkpoint"
		title = fmt.Sprintf("确认第 %d 集剧本", episodeNo)
		reason = "确认或修改本集剧本后，再生成下一集。"
	} else if artifactType == "script_unit" || artifactType == "script_bundle" {
		title = "确认全部单集剧本"
		reason = "确认全部单集剧本及其生成交接后才能进入检查步骤。"
	}
	var nextVersion int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1
		FROM approvals
		WHERE step_run_id = ? AND subject_kind = 'artifact_version_set'`,
		stepRunID,
	).Scan(&nextVersion); err != nil {
		return Approval{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO approvals(
			approval_request_id, project_id, run_id, step_run_id, scope, status, version,
			title, reason, options_json, subject_kind, subject_ref_id, subject_version,
			subject_snapshot_hash, requested_at
		) VALUES(?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?,
			'artifact_version_set', ?, ?, ?, ?)`,
		approvalID,
		projectID,
		runID,
		stepRunID,
		approvalScope,
		nextVersion,
		title,
		reason,
		string(optionsJSON),
		stepRunID,
		nextVersion,
		snapshotHash,
		formatTime(now),
	); err != nil {
		return Approval{}, err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO approval_subject_versions(
				approval_request_id, artifact_version_id, item_order, scope_key
			) VALUES(?, ?, ?, ?)`,
			approvalID,
			item.ArtifactVersionID,
			item.ItemOrder,
			item.ScopeKey,
		); err != nil {
			return Approval{}, err
		}
	}
	return getApprovalTx(ctx, tx, approvalID)
}

func approvalVersionSetForStepTx(
	ctx context.Context,
	tx *sql.Tx,
	stepRunID string,
) ([]approvalVersionSetItem, error) {
	return approvalVersionSetForStepScopeTx(ctx, tx, stepRunID, "")
}

func approvalVersionSetForStepScopeTx(
	ctx context.Context,
	tx *sql.Tx,
	stepRunID string,
	scopeKey string,
) ([]approvalVersionSetItem, error) {
	arguments := []any{stepRunID}
	scopeClause := ""
	if scopeKey != "" {
		scopeClause = " AND a.scope_key = ?"
		arguments = append(arguments, scopeKey)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT av.artifact_version_id, ti.item_order, a.scope_key, a.artifact_type
		FROM artifacts a
		JOIN artifact_versions av
			ON av.artifact_version_id = a.current_version_id
		JOIN step_runs sr ON sr.step_run_id = a.step_run_id
		JOIN task_items ti
			ON ti.step_run_id = a.step_run_id AND ti.run_id = a.run_id
			AND ti.item_key = CASE WHEN a.scope_key = 'singleton'
				THEN 'step:' || sr.step_id ELSE a.scope_key END
		WHERE a.step_run_id = ? AND ti.status = 'succeeded'
			AND a.current_version_id = av.artifact_version_id
			AND av.status = 'pending_approval'`+scopeClause+`
		ORDER BY ti.item_order ASC,
			CASE a.artifact_type
				WHEN 'script_unit' THEN 1
				WHEN 'script_handoff' THEN 2
				ELSE 1
			END ASC,
			a.artifact_type ASC`,
		arguments...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []approvalVersionSetItem
	for rows.Next() {
		var item approvalVersionSetItem
		if err := rows.Scan(
			&item.ArtifactVersionID,
			&item.ItemOrder,
			&item.ScopeKey,
			&item.ArtifactType,
		); err != nil {
			return nil, err
		}
		item.ItemOrder = len(result) + 1
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) markStepWaitingApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runID string,
	stepRunID string,
	approval Approval,
	now time.Time,
) error {
	if err := updateExactlyOne(ctx, tx, `
		UPDATE step_runs SET status = 'waiting_approval'
		WHERE step_run_id = ? AND status = 'running'`,
		"执行步骤状态已变化。", stepRunID); err != nil {
		return err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE runs SET status = 'waiting_approval', updated_at = ?
		WHERE run_id = ? AND status IN ('running', 'pausing')`,
		"生成任务状态已变化。", formatTime(now), runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects SET status = 'waiting_approval', updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		formatTime(now), projectID, runID); err != nil {
		return err
	}
	runRef, stepRef := runID, stepRunID
	for _, event := range []struct{ eventType, subjectType, subjectID string }{
		{"step.waiting_approval", "step_run", stepRunID},
		{"approval.requested", "approval", approval.ApprovalRequestID},
		{"run.waiting_approval", "run", runID},
	} {
		if _, err := s.appendEvent(ctx, tx, projectID, &runRef, &stepRef,
			event.eventType, event.subjectType, event.subjectID, nil); err != nil {
			return err
		}
	}
	return nil
}

func approvalVersionSetHash(items []approvalVersionSetItem) (string, error) {
	payload, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return sha256Hex(payload), nil
}

func (s *Store) compiledStepForRunTx(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	stepRunID string,
) (capability.CompiledStep, error) {
	return s.compiledStepForRunQuery(ctx, tx, runID, stepRunID)
}

func (s *Store) compiledStepForRunQuery(ctx context.Context, query runActionQuery, runID, stepRunID string) (capability.CompiledStep, error) {
	var capabilityID, capabilityVersion, stepID string
	if err := query.QueryRowContext(ctx, `
		SELECT r.capability_id, r.capability_version, sr.step_id
		FROM runs r
		JOIN step_runs sr ON sr.run_id = r.run_id
		WHERE r.run_id = ? AND sr.step_run_id = ?`,
		runID,
		stepRunID,
	).Scan(&capabilityID, &capabilityVersion, &stepID); err != nil {
		return capability.CompiledStep{}, err
	}
	entry, ok, registryErr := s.capabilityEntryForRunQuery(ctx, query, runID, capabilityID)
	if registryErr != nil {
		return capability.CompiledStep{}, registryErr
	}
	if !ok || entry.Status != capability.Available ||
		entry.Definition == nil ||
		entry.Definition.Version != capabilityVersion {
		return capability.CompiledStep{}, domainError(
			"CAPABILITY_VERSION_UNAVAILABLE",
			"整步确认对应的能力版本不可用。",
		)
	}
	for _, step := range entry.Definition.Steps {
		if step.ID == stepID {
			return step, nil
		}
	}
	return capability.CompiledStep{}, domainError(
		"CAPABILITY_VERSION_UNAVAILABLE",
		"整步确认对应的步骤定义不可用。",
	)
}

func episodeNumberFromScope(scopeKey string) (int, error) {
	if !strings.HasPrefix(scopeKey, "episode:") {
		return 0, domainError(
			"RUN_CURSOR_INVALID",
			"单集产物 Task 缺少 episode 作用域。",
		)
	}
	raw := strings.TrimPrefix(scopeKey, "episode:")
	if strings.Contains(raw, "-") {
		return 0, domainError(
			"RUN_CURSOR_INVALID",
			"单集产物 Task 不能覆盖多个集号。",
		)
	}
	episodeNo, err := strconv.Atoi(raw)
	if err != nil || episodeNo <= 0 {
		return 0, domainError(
			"RUN_CURSOR_INVALID",
			fmt.Sprintf("单集产物作用域 %q 无效。", scopeKey),
		)
	}
	return episodeNo, nil
}
