package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"content-agent/backend/internal/capability"
)

func isScriptHandoffRefreshPack(pack StepExecutionContextPack) bool {
	_, cursorOK := parseScriptHandoffRefreshCursor(pack.TaskCursor)
	return cursorOK &&
		pack.Intent.Operation == "refresh_script_handoff" &&
		pack.Intent.ArtifactType == "script_handoff"
}

func validateScriptHandoffRefreshContext(
	step capability.CompiledStep,
	pack StepExecutionContextPack,
) error {
	cursor, ok := parseScriptHandoffRefreshCursor(pack.TaskCursor)
	if !ok || !isScriptBundleStep(step) ||
		pack.OutputContract.ArtifactType != "script_handoff" ||
		pack.OutputContract.SchemaRef != step.OutputRefs[1].SchemaRef ||
		len(pack.OutputContracts) != 1 {
		return domainError(
			"CONTEXT_LINEAGE_CONFLICT",
			"剧本交接刷新合同与能力定义不一致。",
		)
	}
	declared := make([]ContextUpstreamArtifact, 0, len(pack.UpstreamContext))
	scriptCount := 0
	for _, upstream := range pack.UpstreamContext {
		if upstream.ArtifactType == "script_unit" {
			scriptCount++
			if upstream.ArtifactVersionID != cursor.ScriptArtifactVersionID ||
				upstream.ScopeKey != pack.Target.ScopeKey ||
				upstream.Status != "pending_approval" {
				return domainError(
					"CONTEXT_LINEAGE_CONFLICT",
					"交接刷新没有绑定当前待确认剧本版本。",
				)
			}
			continue
		}
		declared = append(declared, upstream)
	}
	if scriptCount != 1 {
		return domainError(
			"CONTEXT_REQUIRED_UPSTREAM_MISSING",
			"交接刷新缺少唯一的当前剧本版本。",
		)
	}
	return validateContextUpstreamCoverage(step.InputRefs, declared)
}

func (s *Store) commitScriptHandoffRefreshTx(
	ctx context.Context,
	tx *sql.Tx,
	attemptID string,
	taskItemID string,
	runID string,
	projectID string,
	stepRunID string,
	scopeKey string,
	step capability.CompiledStep,
	contextPack StepExecutionContextPack,
	response json.RawMessage,
	now time.Time,
) (ArtifactCommitResult, error) {
	cursor, ok := parseScriptHandoffRefreshCursor(contextPack.TaskCursor)
	if !ok {
		return ArtifactCommitResult{}, domainError(
			"CONTEXT_LINEAGE_CONFLICT",
			"交接刷新缺少固定剧本版本。",
		)
	}
	episodeNo, err := episodeNumberFromScope(scopeKey)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	handoffPayload, err := s.contracts.AdaptAndValidate(
		"adapter.legacy_script_handoff_to_v1",
		step.OutputRefs[1].SchemaRef,
		"script_handoff",
		response,
	)
	if err != nil {
		return ArtifactCommitResult{}, domainError(
			"OUTPUT_SCHEMA_VALIDATION_FAILED",
			fmt.Sprintf("刷新后的剧本交接未通过产物合同校验：%v", err),
		)
	}
	var handoff map[string]any
	if err := json.Unmarshal(handoffPayload, &handoff); err != nil {
		return ArtifactCommitResult{}, err
	}
	handoffEpisode, ok := jsonNumberAsInt(handoff["episode_no"])
	if !ok || handoffEpisode != episodeNo {
		return ArtifactCommitResult{}, domainError(
			"BATCH_COVERAGE_INVALID",
			"刷新后的剧本交接 episode_no 与 Task 作用域不一致。",
		)
	}
	handoff["script_artifact_version_id"] = cursor.ScriptArtifactVersionID
	handoffPayload, err = json.Marshal(handoff)
	if err != nil {
		return ArtifactCommitResult{}, err
	}

	var scriptArtifactID, scriptCurrentVersionID, scriptStatus string
	err = tx.QueryRowContext(ctx, `
		SELECT a.artifact_id, a.current_version_id, av.status
		FROM artifacts a
		JOIN artifact_versions av
			ON av.artifact_version_id = a.current_version_id
		WHERE a.run_id = ? AND a.step_run_id = ?
			AND a.artifact_type = 'script_unit' AND a.scope_key = ?`,
		runID,
		stepRunID,
		scopeKey,
	).Scan(&scriptArtifactID, &scriptCurrentVersionID, &scriptStatus)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if scriptCurrentVersionID != cursor.ScriptArtifactVersionID ||
		scriptStatus != "pending_approval" {
		return ArtifactCommitResult{}, domainError(
			"ARTIFACT_VERSION_CONFLICT",
			"刷新提交时剧本版本已经变化。",
		)
	}

	var handoffArtifactID, oldHandoffVersionID, oldHandoffStatus string
	var oldHandoffVersion int
	err = tx.QueryRowContext(ctx, `
		SELECT a.artifact_id, a.current_version_id, av.version, av.status
		FROM artifacts a
		JOIN artifact_versions av
			ON av.artifact_version_id = a.current_version_id
		WHERE a.run_id = ? AND a.step_run_id = ?
			AND a.artifact_type = 'script_handoff' AND a.scope_key = ?`,
		runID,
		stepRunID,
		scopeKey,
	).Scan(
		&handoffArtifactID,
		&oldHandoffVersionID,
		&oldHandoffVersion,
		&oldHandoffStatus,
	)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if oldHandoffStatus != "stale" {
		return ArtifactCommitResult{}, domainError(
			"ARTIFACT_VERSION_CONFLICT",
			"待刷新的交接版本已经变化。",
		)
	}

	newHandoffVersionID := s.newID("av")
	if err := updateExactlyOne(ctx, tx, `
		UPDATE artifact_versions SET status = 'superseded'
		WHERE artifact_version_id = ? AND status = 'stale'`,
		"待刷新的交接版本已经变化。",
		oldHandoffVersionID,
	); err != nil {
		return ArtifactCommitResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json,
			schema_id, schema_version, created_by_kind, actor_ref,
			creation_reason, base_version_id, created_at
		) VALUES(?, ?, ?, 'pending_approval', ?, 'script_handoff', '1.0.0',
			'model', ?, 'regeneration', ?, ?)`,
		newHandoffVersionID,
		handoffArtifactID,
		oldHandoffVersion+1,
		string(handoffPayload),
		attemptID,
		oldHandoffVersionID,
		formatTime(now),
	); err != nil {
		return ArtifactCommitResult{}, err
	}
	for _, upstream := range contextPack.UpstreamContext {
		relation := "derived_from"
		if upstream.ArtifactType == "script_unit" {
			relation = "describes"
		}
		if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
			ProjectID:                   projectID,
			RunID:                       runID,
			DownstreamArtifactVersionID: newHandoffVersionID,
			UpstreamKind:                "artifact_version",
			UpstreamRefID:               upstream.ArtifactVersionID,
			Relation:                    relation,
			UpstreamScope: dependencyScope(
				scopeKind(upstream.ScopeKey),
				upstream.ScopeKey,
			),
			DownstreamScope: dependencyScope("episode", scopeKey),
			ImpactPolicyID:  "scope_intersection",
		}, now); err != nil {
			return ArtifactCommitResult{}, err
		}
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE artifacts SET current_version_id = ?, updated_at = ?
		WHERE artifact_id = ? AND current_version_id = ?`,
		"待刷新的交接产物已经变化。",
		newHandoffVersionID,
		formatTime(now),
		handoffArtifactID,
		oldHandoffVersionID,
	); err != nil {
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
		SET status = 'succeeded', failure = NULL, ended_at = ?, updated_at = ?
		WHERE task_item_id = ? AND status = 'running'
			AND current_attempt_id = ?
			AND output_artifact_version_id = ?`,
		"交接刷新任务状态已变化。",
		formatTime(now),
		formatTime(now),
		taskItemID,
		attemptID,
		cursor.ScriptArtifactVersionID,
	); err != nil {
		return ArtifactCommitResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET version = version + 1,
			current_focus_artifact_version_id = ?, updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		cursor.ScriptArtifactVersionID,
		formatTime(now),
		projectID,
		runID,
	); err != nil {
		return ArtifactCommitResult{}, err
	}

	runRef, stepRef := runID, stepRunID
	for _, event := range []struct {
		eventType   string
		subjectType string
		subjectID   string
		payload     any
	}{
		{
			"artifact.version_created",
			"artifact_version",
			newHandoffVersionID,
			map[string]any{
				"artifact_type":              "script_handoff",
				"scope_key":                  scopeKey,
				"version":                    oldHandoffVersion + 1,
				"creation_reason":            "regeneration",
				"script_artifact_version_id": cursor.ScriptArtifactVersionID,
			},
		},
		{
			"script_handoff.refresh_completed",
			"artifact_version",
			newHandoffVersionID,
			map[string]any{
				"scope_key":                  scopeKey,
				"script_artifact_version_id": cursor.ScriptArtifactVersionID,
			},
		},
		{
			"task.completed",
			"task_item",
			taskItemID,
			map[string]any{"attempt_id": attemptID, "scope_key": scopeKey},
		},
	} {
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

	var unfinishedTasks int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM task_items
		WHERE step_run_id = ? AND status != 'succeeded'`,
		stepRunID,
	).Scan(&unfinishedTasks); err != nil {
		return ArtifactCommitResult{}, err
	}
	var approval Approval
	episodeMode, err := episodeExecutionModeForRun(ctx, tx, runID, nil)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if episodeMode == episodeExecutionModeReviewEach {
		pendingHandoffs, err := pendingScriptHandoffsTx(ctx, tx, runID, stepRunID)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		if len(pendingHandoffs) != 0 {
			return ArtifactCommitResult{}, domainError("DEPENDENCY_INCOMPLETE", "当前单集剧本交接尚未绑定修改后的剧本版本。")
		}
		approval, err = s.createEpisodeCheckpointApprovalTx(
			ctx, tx, projectID, runID, stepRunID, scopeKey, "script_bundle", step, now,
		)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		if err := s.markStepWaitingApprovalTx(ctx, tx, projectID, runID, stepRunID, approval, now); err != nil {
			return ArtifactCommitResult{}, err
		}
	} else if unfinishedTasks == 0 {
		pendingHandoffs, err := pendingScriptHandoffsTx(ctx, tx, runID, stepRunID)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		if len(pendingHandoffs) != 0 {
			return ArtifactCommitResult{}, domainError(
				"DEPENDENCY_INCOMPLETE",
				"仍有剧本交接未绑定当前剧本版本。",
			)
		}
		approval, err = s.createArtifactVersionSetApprovalTx(
			ctx,
			tx,
			projectID,
			runID,
			stepRunID,
			"script_bundle",
			step,
			now,
		)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		if err := s.markStepWaitingApprovalTx(ctx, tx, projectID, runID, stepRunID, approval, now); err != nil {
			return ArtifactCommitResult{}, err
		}
	}
	return s.committedArtifactResultTx(ctx, tx, newHandoffVersionID, runID)
}

func (s *Store) committedScriptHandoffRefreshResultTx(
	ctx context.Context,
	tx *sql.Tx,
	attemptID string,
	runID string,
) (ArtifactCommitResult, error) {
	var versionID string
	err := tx.QueryRowContext(ctx, `
		SELECT artifact_version_id
		FROM artifact_versions
		WHERE actor_ref = ? AND schema_id = 'script_handoff'
			AND creation_reason = 'regeneration'
		ORDER BY created_at DESC LIMIT 1`,
		attemptID,
	).Scan(&versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactCommitResult{}, domainError(
			"ATTEMPT_STALE",
			"交接刷新结果不存在。",
		)
	}
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	return s.committedArtifactResultTx(ctx, tx, versionID, runID)
}
