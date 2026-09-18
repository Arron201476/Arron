package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

func (s *Store) ListApprovals(ctx context.Context, projectID, status string) ([]Approval, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	query := `
		SELECT approval_request_id, project_id, run_id, step_run_id, scope, status, version,
			title, reason, options_json, subject_kind, subject_ref_id, subject_version,
			subject_snapshot_hash, requested_at, resolved_at, resolution_json, actor_ref
		FROM approvals WHERE project_id = ?`
	arguments := []any{projectID}
	if status != "" {
		query += " AND status = ?"
		arguments = append(arguments, status)
	}
	query += " ORDER BY requested_at ASC, approval_request_id ASC"
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Approval
	for rows.Next() {
		approval, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, approval)
	}
	if result == nil {
		result = []Approval{}
	}
	return result, rows.Err()
}

func (s *Store) GetApproval(ctx context.Context, approvalID string) (Approval, error) {
	approval, err := scanApproval(s.db.QueryRowContext(ctx, `
		SELECT approval_request_id, project_id, run_id, step_run_id, scope, status, version,
			title, reason, options_json, subject_kind, subject_ref_id, subject_version,
			subject_snapshot_hash, requested_at, resolved_at, resolution_json, actor_ref
		FROM approvals WHERE approval_request_id = ?`, approvalID))
	if errors.Is(err, sql.ErrNoRows) {
		return Approval{}, domainError("APPROVAL_NOT_FOUND", "确认请求不存在。")
	}
	return approval, err
}

func (s *Store) pendingApprovalForRun(ctx context.Context, runID string) (*Approval, error) {
	approval, err := scanApproval(s.db.QueryRowContext(ctx, `
		SELECT approval_request_id, project_id, run_id, step_run_id, scope, status, version,
			title, reason, options_json, subject_kind, subject_ref_id, subject_version,
			subject_snapshot_hash, requested_at, resolved_at, resolution_json, actor_ref
		FROM approvals
		WHERE run_id = ? AND status = 'pending' AND scope <> 'final_selection'
		ORDER BY requested_at DESC LIMIT 1`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &approval, nil
}

func (s *Store) ResolveApproval(ctx context.Context, command ResolveApprovalCommand) (RunSnapshot, error) {
	if command.ActorRef == "" {
		command.ActorRef = actorRefFromContext(ctx)
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunSnapshot{}, err
	}
	defer tx.Rollback()

	approval, err := getApprovalTx(ctx, tx, command.ApprovalRequestID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if approval.Scope == "final_selection" {
		return RunSnapshot{}, domainError(
			"APPROVAL_COMMAND_MISMATCH",
			"最终稿确认必须使用最终稿选择接口。",
		)
	}
	if err := authorizeApprovalMutationTx(ctx, tx, approval, command.Scope); err != nil {
		return RunSnapshot{}, err
	}
	if command.Scope == "" {
		command.Scope = approval.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return RunSnapshot{}, err
	}
	if hit {
		return decodeApprovalReceiptTx(ctx, tx, cached, approval, command)
	}
	if approval.Status != "pending" {
		return RunSnapshot{}, domainError("APPROVAL_ALREADY_RESOLVED", "该确认请求已经处理。")
	}
	if approval.Version != command.ExpectedApprovalVersion {
		return RunSnapshot{}, domainError("APPROVAL_VERSION_CONFLICT", "确认请求版本已更新。")
	}
	if approval.SubjectSnapshotHash != command.SubjectSnapshotHash {
		return RunSnapshot{}, domainError("APPROVAL_SUBJECT_CHANGED", "确认对象已经变化，请刷新后重试。")
	}
	if !slices.Contains(approval.Options, command.Action) {
		return RunSnapshot{}, domainError("APPROVAL_ACTION_NOT_ALLOWED", "该确认请求不允许此操作。")
	}
	if blocked, err := approvalHasActiveRevisionTx(ctx, tx, approval); err != nil {
		return RunSnapshot{}, err
	} else if blocked {
		return RunSnapshot{}, domainError("REVISION_IN_PROGRESS", "当前版本存在未处理的修改请求，请先完成或取消修改。")
	}
	if command.Action == "select_adaptation_strategy" {
		return s.resolveAdaptationStrategyApprovalTx(ctx, tx, command, approval, now)
	}
	if command.Action == "select_single_option" {
		return s.resolveSingleOptionApprovalTx(ctx, tx, command, approval, now)
	}
	var pendingImpactReviewCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM impact_reviews
		WHERE new_version_id = ? AND status = 'pending'`,
		approval.SubjectRefID,
	).Scan(&pendingImpactReviewCount); err != nil {
		return RunSnapshot{}, err
	}
	if pendingImpactReviewCount > 0 {
		return RunSnapshot{}, domainError(
			"IMPACT_REVIEW_REQUIRED",
			"该版本存在待确认的下游影响，请先选择保留或重新生成。",
		)
	}
	if command.Action != "approve" {
		return RunSnapshot{}, domainError("APPROVAL_ACTION_NOT_ALLOWED", "当前 Runtime 增量只实现 approve。")
	}
	if approval.SubjectKind == "transition" {
		return s.resolveTransitionApprovalTx(ctx, tx, command, approval, now)
	}
	if approval.SubjectKind == "artifact_version_set" {
		return s.resolveArtifactVersionSetApprovalTx(
			ctx,
			tx,
			command,
			approval,
			now,
		)
	}

	var artifactID, artifactType, currentVersionID, versionStatus, payloadJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT av.artifact_id, a.artifact_type, a.current_version_id, av.status, av.payload_json
		FROM artifact_versions av
		JOIN artifacts a ON a.artifact_id = av.artifact_id
		WHERE av.artifact_version_id = ?`, approval.SubjectRefID).
		Scan(&artifactID, &artifactType, &currentVersionID, &versionStatus, &payloadJSON); errors.Is(err, sql.ErrNoRows) {
		return RunSnapshot{}, domainError("ARTIFACT_VERSION_NOT_FOUND", "确认对象不存在。")
	} else if err != nil {
		return RunSnapshot{}, err
	}
	if currentVersionID != approval.SubjectRefID || versionStatus != "pending_approval" {
		return RunSnapshot{}, domainError("APPROVAL_SUBJECT_CHANGED", "确认对象已经不是当前待确认版本。")
	}
	if artifactType == "episode_cards" {
		run, err := getRunTx(ctx, tx, approval.RunID)
		if err != nil {
			return RunSnapshot{}, err
		}
		step, err := s.compiledStepForRunTx(ctx, tx, approval.RunID, approval.StepRunID)
		if err != nil {
			return RunSnapshot{}, err
		}
		config, err := stepConfigPayloadTx(ctx, tx, run, step)
		if err != nil {
			return RunSnapshot{}, err
		}
		if err := validateEpisodeCardsTargetCount(config, json.RawMessage(payloadJSON)); err != nil {
			return RunSnapshot{}, err
		}
	}
	confirmedAt := formatTime(now)
	if _, err := tx.ExecContext(ctx, `
		UPDATE artifact_versions SET status = 'confirmed', confirmed_at = ?
		WHERE artifact_version_id = ?`, confirmedAt, approval.SubjectRefID); err != nil {
		return RunSnapshot{}, err
	}
	resolutionJSON, err := json.Marshal(map[string]any{
		"action":                command.Action,
		"resolved_subject_refs": []string{approval.SubjectRefID},
	})
	if err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE approvals
		SET status = 'approved', resolved_at = ?, resolution_json = ?, actor_ref = ?
		WHERE approval_request_id = ?`,
		confirmedAt, string(resolutionJSON), command.ActorRef, approval.ApprovalRequestID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE step_runs SET status = 'completed', ended_at = ? WHERE step_run_id = ?`,
		confirmedAt, approval.StepRunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	regenerationHandled, err := s.advanceRegenerationPlanAfterApprovalTx(
		ctx,
		tx,
		approval,
		[]string{approval.SubjectRefID},
		now,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	if regenerationHandled {
		return s.finalizeResolvedApprovalTx(ctx, tx, command, approval, now)
	}

	if _, _, err := s.advanceRunAfterConfirmedArtifactTx(
		ctx,
		tx,
		approval.RunID,
		approval.StepRunID,
		approval.SubjectRefID,
		now,
		"approved",
	); err != nil {
		return RunSnapshot{}, err
	}

	return s.finalizeResolvedApprovalTx(ctx, tx, command, approval, now)
}

func approvalHasActiveRevisionTx(ctx context.Context, tx *sql.Tx, approval Approval) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM revision_requests rr
		WHERE rr.project_id = ?
		  AND rr.status IN ('waiting_target_confirmation', 'waiting_safe_checkpoint', 'queued', 'running', 'proposed')
		  AND (
			rr.base_artifact_version_id = ?
			OR EXISTS (
				SELECT 1 FROM approval_subject_versions asv
				WHERE asv.approval_request_id = ?
				  AND asv.artifact_version_id = rr.base_artifact_version_id
			)
		  )`, approval.ProjectID, approval.SubjectRefID, approval.ApprovalRequestID).Scan(&count)
	return count > 0, err
}

func (s *Store) resolveArtifactVersionSetApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	command ResolveApprovalCommand,
	approval Approval,
	now time.Time,
) (RunSnapshot, error) {
	mode := ""
	if len(command.ResolutionPayload) > 0 {
		var payload map[string]json.RawMessage
		if json.Unmarshal(command.ResolutionPayload, &payload) != nil {
			return RunSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "整步确认内容必须是 JSON 对象。")
		}
		if raw, present := payload["episode_execution_mode"]; present {
			if approval.Scope != "episode_checkpoint" || json.Unmarshal(raw, &mode) != nil || !validEpisodeExecutionMode(mode) {
				return RunSnapshot{}, domainError("APPROVAL_COMMAND_MISMATCH", "只有逐集确认可以附带 continuous 或 review_each 生成方式。")
			}
		}
	}
	if approval.SubjectRefID != approval.StepRunID {
		return RunSnapshot{}, domainError(
			"APPROVAL_SUBJECT_CHANGED",
			"整步确认对象已经变化。",
		)
	}
	step, err := s.compiledStepForRunTx(
		ctx,
		tx,
		approval.RunID,
		approval.StepRunID,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT asv.artifact_version_id, asv.item_order, asv.scope_key,
			a.artifact_type, a.current_version_id, av.status, av.payload_json,
			ti.output_artifact_version_id, ti.status
		FROM approval_subject_versions asv
		JOIN artifact_versions av
			ON av.artifact_version_id = asv.artifact_version_id
		JOIN artifacts a ON a.artifact_id = av.artifact_id
		JOIN step_runs sr ON sr.step_run_id = a.step_run_id
		JOIN task_items ti
			ON ti.step_run_id = a.step_run_id AND ti.run_id = a.run_id
			AND ti.item_key = CASE WHEN a.scope_key = 'singleton'
				THEN 'step:' || sr.step_id ELSE a.scope_key END
		WHERE asv.approval_request_id = ?
			AND a.step_run_id = ? AND a.scope_key = asv.scope_key
		ORDER BY asv.item_order ASC`,
		approval.ApprovalRequestID,
		approval.StepRunID,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	var versionIDs []string
	var snapshotItems []approvalVersionSetItem
	scriptVersionsByScope := map[string]string{}
	handoffScriptVersionsByScope := map[string]string{}
	for rows.Next() {
		var versionID, scopeKey, artifactType, currentVersionID, versionStatus string
		var payloadJSON string
		var taskOutputVersionID sql.NullString
		var taskStatus string
		var itemOrder int
		if err := rows.Scan(
			&versionID,
			&itemOrder,
			&scopeKey,
			&artifactType,
			&currentVersionID,
			&versionStatus,
			&payloadJSON,
			&taskOutputVersionID,
			&taskStatus,
		); err != nil {
			rows.Close()
			return RunSnapshot{}, err
		}
		primaryOutput := len(step.OutputRefs) > 0 &&
			artifactType == step.OutputRefs[0].ArtifactType
		if currentVersionID != versionID ||
			versionStatus != "pending_approval" ||
			taskStatus != "succeeded" ||
			(primaryOutput &&
				(!taskOutputVersionID.Valid ||
					taskOutputVersionID.String != versionID)) {
			rows.Close()
			return RunSnapshot{}, domainError(
				"APPROVAL_SUBJECT_CHANGED",
				"整步确认中的单集版本已经变化，请刷新后重试。",
			)
		}
		versionIDs = append(versionIDs, versionID)
		snapshotItems = append(snapshotItems, approvalVersionSetItem{
			ArtifactVersionID: versionID,
			ItemOrder:         itemOrder,
			ScopeKey:          scopeKey,
			ArtifactType:      artifactType,
		})
		switch artifactType {
		case "script_unit":
			scriptVersionsByScope[scopeKey] = versionID
		case "script_handoff":
			var payload struct {
				ScriptArtifactVersionID string `json:"script_artifact_version_id"`
			}
			if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil ||
				payload.ScriptArtifactVersionID == "" {
				rows.Close()
				return RunSnapshot{}, domainError(
					"APPROVAL_SUBJECT_CHANGED",
					"剧本交接版本绑定无效，必须重新生成。",
				)
			}
			handoffScriptVersionsByScope[scopeKey] = payload.ScriptArtifactVersionID
		}
	}
	if err := rows.Close(); err != nil {
		return RunSnapshot{}, err
	}
	if len(versionIDs) == 0 {
		return RunSnapshot{}, domainError(
			"APPROVAL_SUBJECT_CHANGED",
			"整步确认没有绑定单集版本。",
		)
	}
	scopes := map[string]struct{}{}
	for _, item := range snapshotItems {
		scopes[item.ScopeKey] = struct{}{}
	}
	scopeCount := len(scopes)
	if scopeCount*len(step.OutputRefs) != len(versionIDs) {
		return RunSnapshot{}, domainError(
			"APPROVAL_SUBJECT_CHANGED",
			"整步确认绑定的单集版本不完整。",
		)
	}
	if approval.Scope == "episode_checkpoint" && scopeCount != 1 {
		return RunSnapshot{}, domainError("APPROVAL_SUBJECT_CHANGED", "逐集确认必须只绑定一集产物。")
	}
	if approval.Scope != "episode_checkpoint" {
		var pendingVersionCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM artifacts a
			JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
			WHERE a.step_run_id = ? AND av.status = 'pending_approval'`,
			approval.StepRunID).Scan(&pendingVersionCount); err != nil {
			return RunSnapshot{}, err
		}
		if pendingVersionCount != len(versionIDs) {
			return RunSnapshot{}, domainError("APPROVAL_SUBJECT_CHANGED", "整步确认没有覆盖全部待确认版本。")
		}
	}
	if isScriptBundleStep(step) {
		if len(scriptVersionsByScope) != scopeCount ||
			len(handoffScriptVersionsByScope) != scopeCount {
			return RunSnapshot{}, domainError(
				"APPROVAL_SUBJECT_CHANGED",
				"剧本或交接版本不完整，必须重新生成。",
			)
		}
		for scopeKey, scriptVersionID := range scriptVersionsByScope {
			if handoffScriptVersionsByScope[scopeKey] != scriptVersionID {
				return RunSnapshot{}, domainError(
					"APPROVAL_SUBJECT_CHANGED",
					"剧本正文已变化，对应交接必须重新生成。",
				)
			}
		}
	}
	snapshotHash, err := approvalVersionSetHash(snapshotItems)
	if err != nil {
		return RunSnapshot{}, err
	}
	if snapshotHash != approval.SubjectSnapshotHash {
		return RunSnapshot{}, domainError(
			"APPROVAL_SUBJECT_CHANGED",
			"整步确认快照已经变化。",
		)
	}

	configID := ""
	if mode != "" {
		run, err := getRunTx(ctx, tx, approval.RunID)
		if err != nil {
			return RunSnapshot{}, err
		}
		config, err := s.setEpisodeExecutionModeTx(ctx, tx, run, mode, now)
		if err != nil {
			return RunSnapshot{}, err
		}
		configID = config.ConfigSnapshotID
	}
	resolvedAt := formatTime(now)
	for _, versionID := range versionIDs {
		if err := updateExactlyOne(ctx, tx, `
			UPDATE artifact_versions
			SET status = 'confirmed', confirmed_at = ?
			WHERE artifact_version_id = ? AND status = 'pending_approval'`,
			"整步确认中的单集版本已经变化。",
			resolvedAt,
			versionID,
		); err != nil {
			return RunSnapshot{}, err
		}
	}
	resolution := map[string]any{
		"action":                command.Action,
		"resolved_subject_refs": versionIDs,
	}
	if configID != "" {
		resolution["episode_execution_mode"] = mode
		resolution["execution_config_snapshot_id"] = configID
	}
	resolutionJSON, err := json.Marshal(resolution)
	if err != nil {
		return RunSnapshot{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE approvals
		SET status = 'approved', resolved_at = ?, resolution_json = ?, actor_ref = ?
		WHERE approval_request_id = ? AND status = 'pending'`,
		"整步确认请求已经变化。",
		resolvedAt,
		string(resolutionJSON),
		command.ActorRef,
		approval.ApprovalRequestID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if approval.Scope == "episode_checkpoint" {
		var remainingTasks, failedTasks int
		if err := tx.QueryRowContext(ctx, `
			SELECT
				SUM(CASE WHEN status IN ('pending', 'paused', 'running') THEN 1 ELSE 0 END),
				SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END)
			FROM task_items WHERE step_run_id = ?`, approval.StepRunID).Scan(&remainingTasks, &failedTasks); err != nil {
			return RunSnapshot{}, err
		}
		if failedTasks > 0 {
			return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "当前逐集任务存在失败项，请先重试失败项。")
		}
		if remainingTasks > 0 {
			if err := s.pauseAfterEpisodeCheckpointTx(ctx, tx, approval, now); err != nil {
				return RunSnapshot{}, err
			}
			runRef, stepRef := approval.RunID, approval.StepRunID
			for _, versionID := range versionIDs {
				if _, err := s.appendEvent(ctx, tx, approval.ProjectID, &runRef, &stepRef,
					"artifact.confirmed", "artifact_version", versionID,
					map[string]any{"approval_request_id": approval.ApprovalRequestID}); err != nil {
					return RunSnapshot{}, err
				}
			}
			return s.finalizeResolvedApprovalTx(ctx, tx, command, approval, now)
		}
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE step_runs SET status = 'completed', ended_at = ?
		WHERE step_run_id = ? AND status = 'waiting_approval'`,
		"整步确认步骤已经变化。", resolvedAt, approval.StepRunID); err != nil {
		return RunSnapshot{}, err
	}
	regenerationHandled, err := s.advanceRegenerationPlanAfterApprovalTx(
		ctx, tx, approval, versionIDs, now,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	if !regenerationHandled {
		if _, _, err := s.advanceRunAfterConfirmedArtifactsTx(
			ctx,
			tx,
			approval.RunID,
			approval.StepRunID,
			versionIDs,
			now,
			"approved",
		); err != nil {
			return RunSnapshot{}, err
		}
	}
	runRef, stepRef := approval.RunID, approval.StepRunID
	for _, versionID := range versionIDs {
		if _, err := s.appendEvent(
			ctx,
			tx,
			approval.ProjectID,
			&runRef,
			&stepRef,
			"artifact.confirmed",
			"artifact_version",
			versionID,
			map[string]any{"approval_request_id": approval.ApprovalRequestID},
		); err != nil {
			return RunSnapshot{}, err
		}
	}
	return s.finalizeResolvedApprovalTx(ctx, tx, command, approval, now)
}

func (s *Store) pauseAfterEpisodeCheckpointTx(
	ctx context.Context,
	tx *sql.Tx,
	approval Approval,
	now time.Time,
) error {
	if err := updateExactlyOne(ctx, tx, `
		UPDATE step_runs SET status = 'paused'
		WHERE step_run_id = ? AND status = 'waiting_approval'`,
		"逐集确认步骤已经变化。", approval.StepRunID); err != nil {
		return err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE runs SET status = 'paused', updated_at = ?
		WHERE run_id = ? AND status = 'waiting_approval'`,
		"逐集确认任务已经变化。", formatTime(now), approval.RunID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects SET status = 'paused', updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		formatTime(now), approval.ProjectID, approval.RunID); err != nil {
		return err
	}
	return nil
}

func (s *Store) resolveTransitionApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	command ResolveApprovalCommand,
	approval Approval,
	now time.Time,
) (RunSnapshot, error) {
	if approval.Scope == "workflow_transition" {
		return s.resolveWorkflowContinueTx(ctx, tx, command, approval, now)
	}
	if approval.Scope != "transition" {
		return RunSnapshot{}, domainError("APPROVAL_SUBJECT_CHANGED", "未知的流程转换确认类型。")
	}
	var stepStatus, inputJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT status, input_version_snapshot_json
		FROM step_runs
		WHERE step_run_id = ? AND run_id = ?`,
		approval.StepRunID,
		approval.RunID,
	).Scan(&stepStatus, &inputJSON); err != nil {
		return RunSnapshot{}, err
	}
	if stepStatus != "waiting_approval" ||
		approval.SubjectRefID != approval.StepRunID {
		return RunSnapshot{}, domainError(
			"APPROVAL_SUBJECT_CHANGED",
			"转换确认对象已经变化。",
		)
	}
	var inputs []struct {
		ArtifactVersionID string `json:"artifact_version_id"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &inputs); err != nil ||
		len(inputs) == 0 ||
		inputs[0].ArtifactVersionID == "" {
		return RunSnapshot{}, domainError(
			"DEPENDENCY_INCOMPLETE",
			"转换确认缺少已确认的上游版本。",
		)
	}
	expansionStrategy := "只补充因果桥段和必要过渡，不新增关键设定和主线。"
	if len(command.ResolutionPayload) > 0 {
		var payload struct {
			ExpansionStrategy string `json:"expansion_strategy"`
		}
		if err := json.Unmarshal(command.ResolutionPayload, &payload); err != nil {
			return RunSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "扩写策略无法读取。")
		}
		if strings.TrimSpace(payload.ExpansionStrategy) == "" {
			return RunSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "扩写策略不能为空。")
		}
		expansionStrategy = strings.TrimSpace(payload.ExpansionStrategy)
	}
	decisionPayload, err := json.Marshal(map[string]any{
		"decision_type":      "volume_fit",
		"expansion_strategy": expansionStrategy,
		"confirmed":          true,
	})
	if err != nil {
		return RunSnapshot{}, err
	}
	decision, err := s.createRunDecisionSnapshotTx(
		ctx, tx, approval.ProjectID, approval.RunID, approval.StepRunID,
		"volume_fit", "artifact_version", inputs[0].ArtifactVersionID,
		decisionPayload, now,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	resolvedAt := formatTime(now)
	resolutionJSON, err := json.Marshal(map[string]any{
		"action":                       command.Action,
		"expansion_strategy_confirmed": true,
		"expansion_strategy":           expansionStrategy,
		"decision_snapshot_id":         decision.DecisionSnapshotID,
		"resolved_subject_refs":        []string{approval.StepRunID},
	})
	if err != nil {
		return RunSnapshot{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE approvals
		SET status = 'approved', resolved_at = ?, resolution_json = ?, actor_ref = ?
		WHERE approval_request_id = ? AND status = 'pending'`,
		"转换确认请求已经变化。",
		resolvedAt, string(resolutionJSON), command.ActorRef, approval.ApprovalRequestID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE step_runs SET status = 'completed', ended_at = ?
		WHERE step_run_id = ? AND status = 'waiting_approval'`,
		"转换确认步骤已经变化。",
		resolvedAt, approval.StepRunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, _, err := s.advanceRunAfterConfirmedArtifactTx(
		ctx,
		tx,
		approval.RunID,
		approval.StepRunID,
		inputs[0].ArtifactVersionID,
		now,
		"approved", "expansion_strategy_confirmed",
	); err != nil {
		return RunSnapshot{}, err
	}
	runRef, stepRef := approval.RunID, approval.StepRunID
	if _, err := s.appendEvent(
		ctx,
		tx,
		approval.ProjectID,
		&runRef,
		&stepRef,
		"volume_fit.strategy_confirmed",
		"decision_snapshot",
		decision.DecisionSnapshotID,
		map[string]any{"expansion_strategy": expansionStrategy},
	); err != nil {
		return RunSnapshot{}, err
	}
	return s.finalizeResolvedApprovalTx(ctx, tx, command, approval, now)
}

func (s *Store) finalizeResolvedApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	command ResolveApprovalCommand,
	approval Approval,
	now time.Time,
) (RunSnapshot, error) {
	runRef, stepRef := approval.RunID, approval.StepRunID
	var finalRunStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT status FROM runs WHERE run_id = ?`,
		approval.RunID,
	).Scan(&finalRunStatus); err != nil {
		return RunSnapshot{}, err
	}
	runEventType := "run." + finalRunStatus
	var finalStepStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM step_runs WHERE step_run_id = ?`, approval.StepRunID).Scan(&finalStepStatus); err != nil {
		return RunSnapshot{}, err
	}
	events := []struct {
		eventType   string
		subjectType string
		subjectID   string
	}{
		{"approval.resolved", "approval", approval.ApprovalRequestID},
		{"step." + finalStepStatus, "step_run", approval.StepRunID},
		{runEventType, "run", approval.RunID},
	}
	if approval.SubjectKind == "artifact_version" {
		events = append(events, struct {
			eventType   string
			subjectType string
			subjectID   string
		}{"artifact.confirmed", "artifact_version", approval.SubjectRefID})
	}
	for _, event := range events {
		if _, err := s.appendEvent(ctx, tx, approval.ProjectID, &runRef, &stepRef, event.eventType, event.subjectType, event.subjectID, map[string]any{"prototype_boundary": true}); err != nil {
			return RunSnapshot{}, err
		}
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, approval.RunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, snapshot, now); err != nil {
		return RunSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunSnapshot{}, err
	}
	return snapshot, nil
}

func getApprovalTx(ctx context.Context, tx *sql.Tx, approvalID string) (Approval, error) {
	approval, err := scanApproval(tx.QueryRowContext(ctx, `
		SELECT approval_request_id, project_id, run_id, step_run_id, scope, status, version,
			title, reason, options_json, subject_kind, subject_ref_id, subject_version,
			subject_snapshot_hash, requested_at, resolved_at, resolution_json, actor_ref
		FROM approvals WHERE approval_request_id = ?`, approvalID))
	if errors.Is(err, sql.ErrNoRows) {
		return Approval{}, domainError("APPROVAL_NOT_FOUND", "确认请求不存在。")
	}
	return approval, err
}

func scanApproval(row rowScanner) (Approval, error) {
	var approval Approval
	var optionsJSON, requestedAt string
	var resolvedAt, resolutionJSON, actorRef sql.NullString
	if err := row.Scan(
		&approval.ApprovalRequestID, &approval.ProjectID, &approval.RunID, &approval.StepRunID,
		&approval.Scope, &approval.Status, &approval.Version, &approval.Title, &approval.Reason,
		&optionsJSON, &approval.SubjectKind, &approval.SubjectRefID, &approval.SubjectVersion,
		&approval.SubjectSnapshotHash, &requestedAt, &resolvedAt, &resolutionJSON, &actorRef,
	); err != nil {
		return Approval{}, err
	}
	if err := json.Unmarshal([]byte(optionsJSON), &approval.Options); err != nil {
		return Approval{}, fmt.Errorf("decode approval options: %w", err)
	}
	var err error
	approval.RequestedAt, err = parseTime(requestedAt)
	if err != nil {
		return Approval{}, err
	}
	approval.ResolvedAt, err = optionalTime(resolvedAt)
	if err != nil {
		return Approval{}, err
	}
	if resolutionJSON.Valid {
		approval.Resolution = json.RawMessage(resolutionJSON.String)
	}
	approval.ActorRef = stringPointer(actorRef)
	return approval, nil
}

func (s *Store) getRunSnapshotTx(ctx context.Context, tx *sql.Tx, runID string) (RunSnapshot, error) {
	run, err := scanRun(tx.QueryRowContext(ctx, `
		SELECT run_id, project_id, conversation_id, capability_id, capability_version,
			run_kind, status, current_step_run_id, current_input_snapshot_version_id,
			input_snapshot_status, config_snapshot_json, started_at, ended_at, created_at, updated_at
		FROM runs WHERE run_id = ?`, runID))
	if err != nil {
		return RunSnapshot{}, err
	}
	if err := s.enrichRunEpisodeExecutionMode(ctx, tx, &run); err != nil {
		return RunSnapshot{}, err
	}
	stepRows, err := tx.QueryContext(ctx, `
		SELECT step_run_id, run_id, step_id, status, attempt_count, approval_policy,
			input_version_snapshot_json, task_cursor_json, started_at, ended_at
		FROM step_runs WHERE run_id = ? ORDER BY rowid ASC`, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	var steps []StepRun
	for stepRows.Next() {
		step, err := scanStepRun(stepRows)
		if err != nil {
			stepRows.Close()
			return RunSnapshot{}, err
		}
		steps = append(steps, step)
	}
	if err := stepRows.Close(); err != nil {
		return RunSnapshot{}, err
	}
	artifactRows, err := tx.QueryContext(ctx, `
		SELECT artifact_id, project_id, COALESCE(run_id, ''), COALESCE(step_run_id, ''),
			origin_type, origin_id, capability_id,
			artifact_type, scope_key, current_version_id, created_at, updated_at
		FROM artifacts WHERE run_id = ? ORDER BY created_at ASC, artifact_id ASC`, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	var artifacts []Artifact
	for artifactRows.Next() {
		artifact, err := scanArtifact(artifactRows)
		if err != nil {
			artifactRows.Close()
			return RunSnapshot{}, err
		}
		artifacts = append(artifacts, artifact)
	}
	if err := artifactRows.Close(); err != nil {
		return RunSnapshot{}, err
	}
	var currentApproval *Approval
	approval, err := scanApproval(tx.QueryRowContext(ctx, `
		SELECT approval_request_id, project_id, run_id, step_run_id, scope, status, version,
			title, reason, options_json, subject_kind, subject_ref_id, subject_version,
			subject_snapshot_hash, requested_at, resolved_at, resolution_json, actor_ref
		FROM approvals
		WHERE run_id = ? AND status = 'pending' AND scope <> 'final_selection'
		ORDER BY requested_at DESC LIMIT 1`, runID))
	if err == nil {
		currentApproval = &approval
	} else if !errors.Is(err, sql.ErrNoRows) {
		return RunSnapshot{}, err
	}
	var cursor EventCursor
	if err := tx.QueryRowContext(ctx, `
		SELECT p.project_event_seq, r.run_event_seq
		FROM projects p JOIN runs r ON r.project_id = p.project_id
		WHERE r.run_id = ?`, runID).Scan(&cursor.ProjectEventSeq, &cursor.RunEventSeq); err != nil {
		return RunSnapshot{}, err
	}
	if steps == nil {
		steps = []StepRun{}
	}
	if artifacts == nil {
		artifacts = []Artifact{}
	}
	snapshot := RunSnapshot{
		Run:             run,
		Steps:           steps,
		CurrentApproval: currentApproval,
		Artifacts:       artifacts,
		EventCursor:     cursor,
	}
	snapshot.AvailableActions, err = s.runAvailableActions(ctx, tx, run, steps, currentApproval)
	if err != nil {
		return RunSnapshot{}, err
	}
	snapshot.PendingScriptEdit, err = s.pendingScriptEditQuery(ctx, tx, run, steps)
	if err != nil {
		return RunSnapshot{}, err
	}
	return snapshot, nil
}
