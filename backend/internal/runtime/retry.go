package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"content-agent/backend/internal/capability"
)

func (s *Store) RetryFailedStep(
	ctx context.Context,
	command RetryFailedStepCommand,
) (RunSnapshot, error) {
	if command.StepRunID == "" {
		return RunSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "缺少需要重试的步骤。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunSnapshot{}, err
	}
	defer tx.Rollback()

	var runID, projectID, capabilityID, capabilityVersion, stepID, stepStatus, runStatus string
	var currentStepRunID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT sr.run_id, r.project_id, r.capability_id, r.capability_version,
			sr.step_id, sr.status, r.status, r.current_step_run_id
		FROM step_runs sr
		JOIN runs r ON r.run_id = sr.run_id
		WHERE sr.step_run_id = ?`, command.StepRunID).Scan(
		&runID,
		&projectID,
		&capabilityID,
		&capabilityVersion,
		&stepID,
		&stepStatus,
		&runStatus,
		&currentStepRunID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RunSnapshot{}, domainError("STEP_RUN_NOT_FOUND", "生成步骤不存在。")
	}
	if err != nil {
		return RunSnapshot{}, err
	}
	if command.Scope == "" {
		command.Scope = projectID
	}
	cached, hit, err := s.beginExecutionControlCommand(ctx, tx, command.CommandMeta, projectID, "run", runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if hit {
		return decodeIdempotentResult[RunSnapshot](cached)
	}
	entry, ok, registryErr := s.capabilityEntryForRunQuery(
		ctx, tx, runID, capabilityID,
	)
	if registryErr != nil {
		return RunSnapshot{}, registryErr
	}
	if err := authorizePersonalSkillExecution(ctx, entry); err != nil {
		return RunSnapshot{}, err
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil ||
		entry.Definition.Version != capabilityVersion {
		return RunSnapshot{}, domainError("CAPABILITY_VERSION_UNAVAILABLE", "原能力版本当前不可重试。")
	}
	var stepDefinition *capability.CompiledStep
	for index := range entry.Definition.Steps {
		if entry.Definition.Steps[index].ID == stepID {
			stepDefinition = &entry.Definition.Steps[index]
			break
		}
	}
	if stepDefinition == nil || !stepDefinition.Retry.UserRetryAllowed {
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "当前步骤不允许用户重试。")
	}
	if runStatus == "running" && stepStatus == "running" &&
		stepDefinition.Batch != nil && stepDefinition.Batch.Execution == "sequential" &&
		stepDefinition.Batch.FailurePolicy == "preserve_success_retry_failed" {
		var failedCount, runningCount int
		var failureCode sql.NullString
		if err := tx.QueryRowContext(ctx, `
			SELECT
				SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END),
				SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END)
			FROM task_items WHERE step_run_id = ?`, command.StepRunID,
		).Scan(&failedCount, &runningCount); err != nil {
			return RunSnapshot{}, err
		}
		if failedCount > 0 && runningCount == 0 {
			if err := tx.QueryRowContext(ctx, `
				SELECT failure FROM task_items
				WHERE step_run_id = ? AND status = 'failed'
				ORDER BY item_order ASC, task_item_id ASC LIMIT 1`,
				command.StepRunID,
			).Scan(&failureCode); err != nil {
				return RunSnapshot{}, err
			}
			if !failureCode.Valid || failureCode.String == "" {
				failureCode.String = "OUTPUT_REPAIR_FAILED"
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE task_items
				SET status = 'failed', failure = ?, ended_at = ?, updated_at = ?
				WHERE step_run_id = ? AND status = 'pending'`,
				failureCode.String, formatTime(now), formatTime(now), command.StepRunID,
			); err != nil {
				return RunSnapshot{}, err
			}
			if err := s.failStepRunAndProjectTx(
				ctx, tx, projectID, runID, command.StepRunID, failureCode.String, now,
			); err != nil {
				return RunSnapshot{}, err
			}
			runStatus, stepStatus = "failed", "failed"
		}
	}
	if runStatus != "failed" || stepStatus != "failed" ||
		!currentStepRunID.Valid || currentStepRunID.String != command.StepRunID {
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "当前步骤不处于可重试的失败状态。")
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT task_item_id, failure
		FROM task_items
		WHERE step_run_id = ? AND status = 'failed'
		ORDER BY item_order ASC, task_item_id ASC`, command.StepRunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	var failedTaskIDs []string
	for rows.Next() {
		var taskID string
		var failure sql.NullString
		if err := rows.Scan(&taskID, &failure); err != nil {
			rows.Close()
			return RunSnapshot{}, err
		}
		if !failure.Valid || !slices.Contains(stepDefinition.Retry.RetryableErrors, failure.String) {
			rows.Close()
			return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "失败原因不允许重试。")
		}
		failedTaskIDs = append(failedTaskIDs, taskID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RunSnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return RunSnapshot{}, err
	}
	if len(failedTaskIDs) == 0 {
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "当前步骤没有可重试的失败任务。")
	}
	if risk, err := failedStepExecutionReplayRisk(ctx, tx, command.StepRunID); err != nil {
		return RunSnapshot{}, err
	} else if risk {
		return RunSnapshot{}, domainError("SDK_TOOL_REPLAY_RISK", "失败任务已有写入调用或 SDK checkpoint，不能从头重试；请先核对已有结果，避免重复执行。")
	}

	replannedLegacyBatch, err := s.replanLegacyFailedBatchTaskTx(
		ctx,
		tx,
		runID,
		command.StepRunID,
		*stepDefinition,
		failedTaskIDs,
		now,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	if !replannedLegacyBatch {
		if _, err := tx.ExecContext(ctx, `
			UPDATE task_items
			SET status = 'pending', current_attempt_id = NULL, ended_at = NULL,
				failure = NULL, updated_at = ?
			WHERE step_run_id = ? AND status = 'failed'`,
			formatTime(now), command.StepRunID,
		); err != nil {
			return RunSnapshot{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE step_runs
		SET status = 'running', attempt_count = attempt_count + 1, ended_at = NULL
		WHERE step_run_id = ? AND status = 'failed'`, command.StepRunID); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = 'running', ended_at = NULL, updated_at = ?
		WHERE run_id = ? AND status = 'failed'`, formatTime(now), runID); err != nil {
		return RunSnapshot{}, err
	}
	if err := reactivateFailedRegenerationForRetryTx(ctx, tx, command.StepRunID); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET version = version + 1, status = 'running', updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		formatTime(now), projectID, runID,
	); err != nil {
		return RunSnapshot{}, err
	}

	runRef, stepRef := runID, command.StepRunID
	for _, taskID := range failedTaskIDs {
		if _, err := s.appendEvent(
			ctx,
			tx,
			projectID,
			&runRef,
			&stepRef,
			"task.retry_scheduled",
			"task_item",
			taskID,
			map[string]any{"trigger": "user"},
		); err != nil {
			return RunSnapshot{}, err
		}
	}
	if _, err := s.appendEvent(
		ctx,
		tx,
		projectID,
		&runRef,
		&stepRef,
		"run.retry_scheduled",
		"run",
		runID,
		map[string]any{"failed_step_run_id": command.StepRunID},
	); err != nil {
		return RunSnapshot{}, err
	}

	snapshot, err := s.getRunSnapshotTx(ctx, tx, runID)
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

func reactivateFailedRegenerationForRetryTx(
	ctx context.Context,
	tx *sql.Tx,
	stepRunID string,
) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE regeneration_plan_groups
		SET status = 'running'
		WHERE step_run_id = ? AND status = 'failed'
			AND group_order = (
				SELECT rp.current_group_order
				FROM regeneration_plans rp
				WHERE rp.regeneration_plan_id = regeneration_plan_groups.regeneration_plan_id
					AND rp.status = 'failed'
			)`, stepRunID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE regeneration_plans
		SET status = 'running'
		WHERE status = 'failed'
			AND EXISTS (
				SELECT 1 FROM regeneration_plan_groups rpg
				WHERE rpg.regeneration_plan_id = regeneration_plans.regeneration_plan_id
					AND rpg.step_run_id = ?
					AND rpg.group_order = regeneration_plans.current_group_order
					AND rpg.status = 'running'
			)`, stepRunID)
	return err
}

// Refresh failed split preparation tasks at the user retry boundary. This also
// upgrades persisted cursors when source-boundary planning changes, while later
// batch tasks keep their exact cursor and completed checkpoints.
func (s *Store) replanLegacyFailedBatchTaskTx(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	stepRunID string,
	step capability.CompiledStep,
	failedTaskIDs []string,
	now time.Time,
) (bool, error) {
	if !isEpisodeSplitStep(step) || len(failedTaskIDs) != 1 {
		return false, nil
	}
	var cursorJSON, stepInputJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT ti.cursor_json, sr.input_version_snapshot_json
		FROM task_items ti
		JOIN step_runs sr ON sr.step_run_id = ti.step_run_id
		WHERE ti.task_item_id = ? AND ti.step_run_id = ? AND ti.status = 'failed'`,
		failedTaskIDs[0], stepRunID,
	).Scan(&cursorJSON, &stepInputJSON); err != nil {
		return false, err
	}
	if stage, planned := batchStageForCursor(step.Batch, json.RawMessage(cursorJSON)); planned {
		if stage.ID != step.Batch.Preparation.ID {
			return false, nil
		}
	}
	run, err := getRunTx(ctx, tx, runID)
	if err != nil {
		return false, err
	}
	configPayload, err := stepConfigPayloadTx(ctx, tx, run, step)
	if err != nil {
		return false, err
	}
	targetCount, err := targetEpisodeCount(configPayload)
	if err != nil {
		return false, err
	}
	sourceUnits, err := s.episodeSplitSourceUnitsTx(
		ctx,
		tx,
		run.ProjectID,
		json.RawMessage(stepInputJSON),
		targetCount,
	)
	if err != nil {
		return false, err
	}
	var cursor map[string]any
	if err := json.Unmarshal([]byte(cursorJSON), &cursor); err != nil {
		return false, domainError("RUN_CURSOR_INVALID", "失败任务的重生成游标无效。")
	}
	if cursor == nil {
		cursor = map[string]any{}
	}
	batchSize := step.Batch.MaxItemsPerTask
	if batchSize <= 0 {
		batchSize = 1
	}
	cursor["batch"] = map[string]any{
		"phase":                step.Batch.Preparation.ID,
		"target_episode_count": targetCount,
		"max_items_per_task":   batchSize,
		"source_units":         sourceUnits,
	}
	replannedCursor, err := json.Marshal(cursor)
	if err != nil {
		return false, err
	}
	taskInput, err := json.Marshal(map[string]any{
		"run_input_snapshot_version_id": run.CurrentInputSnapshotVersionID,
		"step_input_versions":           json.RawMessage(stepInputJSON),
	})
	if err != nil {
		return false, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE task_items
		SET item_key = ?, item_order = 1, status = 'pending',
			current_attempt_id = NULL, input_snapshot_json = ?, cursor_json = ?,
			ended_at = NULL, failure = NULL, updated_at = ?
		WHERE task_item_id = ? AND step_run_id = ? AND status = 'failed'`,
		"失败的拆集任务已经变化。",
		"preparation:"+step.Batch.Preparation.ID,
		string(taskInput),
		string(replannedCursor),
		formatTime(now),
		failedTaskIDs[0],
		stepRunID,
	); err != nil {
		return false, err
	}
	return true, nil
}
