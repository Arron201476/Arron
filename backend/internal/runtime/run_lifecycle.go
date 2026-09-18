package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"content-agent/backend/internal/capability"
)

func (s *Store) RequestRunPause(ctx context.Context, command PauseRunCommand) (RunSnapshot, error) {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunSnapshot{}, err
	}
	defer tx.Rollback()

	run, err := getRunTx(ctx, tx, command.RunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if command.Scope == "" {
		command.Scope = run.ProjectID
	}
	cached, hit, err := s.beginExecutionControlCommand(ctx, tx, command.CommandMeta, run.ProjectID, "run", run.RunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if hit {
		return decodeIdempotentResult[RunSnapshot](cached)
	}
	runRef := run.RunID
	if _, err := s.appendEvent(ctx, tx, run.ProjectID, &runRef, run.CurrentStepRunID, "run.pause_requested", "run", run.RunID, nil); err != nil {
		return RunSnapshot{}, err
	}

	switch run.Status {
	case "running":
		active, activeErr := runHasActiveExecutionTx(ctx, tx, run)
		if activeErr != nil {
			return RunSnapshot{}, activeErr
		}
		if active {
			if _, err := tx.ExecContext(ctx, `
				UPDATE runs SET status = 'pausing', updated_at = ?
				WHERE run_id = ? AND status = 'running'`,
				formatTime(now), run.RunID,
			); err != nil {
				return RunSnapshot{}, err
			}
		} else if err := s.pauseRunningStepTx(ctx, tx, run, nil, now, "idle_checkpoint"); err != nil {
			return RunSnapshot{}, err
		}
	case "waiting_approval":
		if _, err := tx.ExecContext(ctx, `
			UPDATE runs SET status = 'paused', updated_at = ?
			WHERE run_id = ? AND status = 'waiting_approval'`,
			formatTime(now), run.RunID,
		); err != nil {
			return RunSnapshot{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE projects
			SET version = version + 1, status = 'paused', updated_at = ?
			WHERE project_id = ? AND active_write_run_id = ?`,
			formatTime(now), run.ProjectID, run.RunID,
		); err != nil {
			return RunSnapshot{}, err
		}
	default:
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "当前生成任务状态不允许暂停。")
	}

	if run.Status == "waiting_approval" {
		if _, err := s.appendEvent(ctx, tx, run.ProjectID, &runRef, run.CurrentStepRunID, "run.paused", "run", run.RunID, map[string]any{"safe_boundary": "waiting_approval"}); err != nil {
			return RunSnapshot{}, err
		}
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, run.RunID)
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

func (s *Store) CompleteRunPauseAtSafeBoundary(
	ctx context.Context,
	command CompleteRunPauseCommand,
) (RunSnapshot, error) {
	if !jsonObject(command.TaskCursor) {
		return RunSnapshot{}, domainError("RUN_CURSOR_INVALID", "暂停游标必须是 JSON 对象。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunSnapshot{}, err
	}
	defer tx.Rollback()

	run, err := getRunTx(ctx, tx, command.RunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if command.Scope == "" {
		command.Scope = run.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return RunSnapshot{}, err
	}
	if hit {
		return decodeIdempotentResult[RunSnapshot](cached)
	}
	if run.Status != "pausing" || run.CurrentStepRunID == nil {
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "生成任务当前不等待安全暂停。")
	}
	if err := s.pauseRunningStepTx(ctx, tx, run, &command.TaskCursor, now, "executor_cursor_saved"); err != nil {
		return RunSnapshot{}, err
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, run.RunID)
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

func (s *Store) ResumeRun(ctx context.Context, command ResumeRunCommand) (RunSnapshot, error) {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunSnapshot{}, err
	}
	defer tx.Rollback()

	run, err := getRunTx(ctx, tx, command.RunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if command.Scope == "" {
		command.Scope = run.ProjectID
	}
	cached, hit, err := s.beginExecutionControlCommand(ctx, tx, command.CommandMeta, run.ProjectID, "run", run.RunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if hit {
		return decodeIdempotentResult[RunSnapshot](cached)
	}
	if run.Status == "paused" || run.Status == "pausing" {
		if err := s.prepareExternalRunResumeTx(ctx, tx, run.RunID); err != nil {
			return RunSnapshot{}, err
		}
	}
	if run.Status == "pausing" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE runs SET status = 'running', updated_at = ?
			WHERE run_id = ? AND status = 'pausing'`,
			formatTime(now), run.RunID,
		); err != nil {
			return RunSnapshot{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE projects
			SET version = version + 1, status = 'running', updated_at = ?
			WHERE project_id = ? AND active_write_run_id = ?`,
			formatTime(now), run.ProjectID, run.RunID,
		); err != nil {
			return RunSnapshot{}, err
		}
		runRef := run.RunID
		if _, err := s.appendEvent(ctx, tx, run.ProjectID, &runRef, run.CurrentStepRunID, "run.pause_cancelled", "run", run.RunID, nil); err != nil {
			return RunSnapshot{}, err
		}
		snapshot, err := s.getRunSnapshotTx(ctx, tx, run.RunID)
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
	if run.Status != "paused" || run.CurrentStepRunID == nil {
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "只有已暂停或正在暂停的生成任务可以恢复。")
	}
	var pendingApprovalCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM approvals
		WHERE run_id = ? AND status = 'pending' AND scope <> 'final_selection'`,
		run.RunID,
	).Scan(&pendingApprovalCount); err != nil {
		return RunSnapshot{}, err
	}
	if pendingApprovalCount != 0 {
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "生成任务仍有待确认内容，确认后才能恢复。")
	}
	entry, ok, registryErr := s.capabilityEntryForRunQuery(
		ctx, tx, run.RunID, run.CapabilityID,
	)
	if registryErr != nil {
		return RunSnapshot{}, registryErr
	}
	if err := authorizePersonalSkillExecution(ctx, entry); err != nil {
		return RunSnapshot{}, err
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil ||
		entry.Definition.Version != run.CapabilityVersion {
		return RunSnapshot{}, domainError("CAPABILITY_VERSION_UNAVAILABLE", "原能力版本当前不可恢复。")
	}
	if run.InputSnapshotStatus != "sealed" {
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "当前输入快照尚未封存，不能恢复。")
	}
	var inputPayload string
	if err := tx.QueryRowContext(ctx, `
		SELECT payload_json FROM run_input_snapshot_versions
		WHERE run_input_snapshot_version_id = ? AND run_id = ? AND status = 'sealed'`,
		run.CurrentInputSnapshotVersionID, run.RunID,
	).Scan(&inputPayload); errors.Is(err, sql.ErrNoRows) {
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "恢复所需输入快照不存在。")
	} else if err != nil {
		return RunSnapshot{}, err
	}
	var source sourceInputRequest
	if err := json.Unmarshal([]byte(inputPayload), &source); err != nil {
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "恢复所需输入快照无法读取。")
	}
	references := make([]assetInputReference, 0, len(source.Assets))
	for _, raw := range source.Assets {
		reference, err := validateAssetReference(raw)
		if err != nil {
			return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "恢复所需材料引用无效。")
		}
		if reference.Role != sourceAssetRole(entry.Definition) {
			return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "恢复所需材料角色与 Skill 输入绑定不匹配。")
		}
		references = append(references, reference)
	}
	if err := validateAssetOwnership(ctx, tx, run.ProjectID, references); err != nil {
		return RunSnapshot{}, err
	}
	if err := validateAcceptedAssetKinds(
		ctx, tx, run.ProjectID, references, entry.Definition.AcceptedAssetKinds,
	); err != nil {
		return RunSnapshot{}, err
	}
	currentStepRunID := *run.CurrentStepRunID
	var currentStepID, stepStatus, cursorJSON, stepInputJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT step_id, status, task_cursor_json, input_version_snapshot_json
		FROM step_runs WHERE step_run_id = ? AND run_id = ?`,
		currentStepRunID, run.RunID,
	).Scan(&currentStepID, &stepStatus, &cursorJSON, &stepInputJSON); errors.Is(err, sql.ErrNoRows) {
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "恢复步骤不存在。")
	} else if err != nil {
		return RunSnapshot{}, err
	}
	if stepStatus != "pending" && stepStatus != "paused" {
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "当前步骤状态不允许恢复。")
	}
	if !jsonObject(json.RawMessage(cursorJSON)) {
		return RunSnapshot{}, domainError("RUN_CURSOR_INVALID", "当前步骤恢复游标无效。")
	}
	if stepStatus == "pending" {
		if err := s.validateWorkflowFailureResumeTx(ctx, tx, run, currentStepRunID, currentStepID, stepInputJSON, cursorJSON); err != nil {
			return RunSnapshot{}, err
		}
	}
	var stepDefinition *capability.CompiledStep
	for index := range entry.Definition.Steps {
		if entry.Definition.Steps[index].ID == currentStepID {
			stepDefinition = &entry.Definition.Steps[index]
			break
		}
	}
	if stepDefinition == nil {
		return RunSnapshot{}, domainError("CAPABILITY_VERSION_UNAVAILABLE", "当前步骤不在原能力版本中。")
	}
	finishResume := func() (RunSnapshot, error) {
		snapshot, err := s.getRunSnapshotTx(ctx, tx, run.RunID)
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
	executedRuntimeSteps := make(map[string]bool)
	for strings.HasPrefix(stepDefinition.ExecutorRef, "runtime.") {
		// A cyclic Runtime chain yields at its next pending step instead of
		// keeping the transaction open indefinitely or enqueueing a Worker task.
		if executedRuntimeSteps[stepDefinition.ID] {
			return finishResume()
		}
		executedRuntimeSteps[stepDefinition.ID] = true
		outcome, err := s.executeRuntimeStepForResumeTx(
			ctx,
			tx,
			run,
			entry.Definition,
			currentStepRunID,
			*stepDefinition,
			stepInputJSON,
			now,
		)
		if err != nil {
			return RunSnapshot{}, err
		}
		run, err = getRunTx(ctx, tx, run.RunID)
		if err != nil {
			return RunSnapshot{}, err
		}
		if run.Status == "waiting_approval" || run.Status == "completed" {
			return finishResume()
		}
		if outcome.NextStepRunID == nil || run.CurrentStepRunID == nil ||
			*outcome.NextStepRunID != *run.CurrentStepRunID {
			return RunSnapshot{}, domainError(
				"RUN_STATE_CONFLICT",
				"Runtime 步骤完成后没有可执行的下一步骤。",
			)
		}
		currentStepRunID = *outcome.NextStepRunID
		if err := tx.QueryRowContext(ctx, `
			SELECT step_id, status, task_cursor_json, input_version_snapshot_json
			FROM step_runs WHERE step_run_id = ? AND run_id = ?`,
			currentStepRunID, run.RunID,
		).Scan(&currentStepID, &stepStatus, &cursorJSON, &stepInputJSON); err != nil {
			return RunSnapshot{}, err
		}
		stepDefinition = nil
		for index := range entry.Definition.Steps {
			if entry.Definition.Steps[index].ID == currentStepID {
				stepDefinition = &entry.Definition.Steps[index]
				break
			}
		}
		if stepDefinition == nil {
			return RunSnapshot{}, domainError(
				"CAPABILITY_VERSION_UNAVAILABLE",
				"Runtime 步骤的下一步骤不可执行。",
			)
		}
		if isScriptQualityReviewStep(*stepDefinition) && stepStatus == "running" && run.Status == "running" {
			// The progression command has already planned this review atomically.
			return finishResume()
		}
		if stepStatus != "pending" || run.Status != "paused" {
			return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "Runtime 后继步骤状态已经变化。")
		}
		if !jsonObject(json.RawMessage(cursorJSON)) {
			return RunSnapshot{}, domainError("RUN_CURSOR_INVALID", "后继步骤恢复游标无效。")
		}
	}
	if err := s.ensureStepTasksForResume(
		ctx,
		tx,
		run,
		currentStepRunID,
		*stepDefinition,
		json.RawMessage(stepInputJSON),
		json.RawMessage(cursorJSON),
		now,
	); err != nil {
		return RunSnapshot{}, err
	}
	var activeRunID sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT active_write_run_id FROM projects WHERE project_id = ? AND deleted_at IS NULL`,
		run.ProjectID,
	).Scan(&activeRunID); err != nil {
		return RunSnapshot{}, err
	}
	if !activeRunID.Valid || activeRunID.String != run.RunID {
		return RunSnapshot{}, domainError("PROJECT_WRITE_RUN_CONFLICT", "作品写锁不属于当前生成任务。")
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE step_runs
		SET status = 'running',
			attempt_count = CASE WHEN attempt_count = 0 THEN 1 ELSE attempt_count END,
			started_at = COALESCE(started_at, ?)
		WHERE step_run_id = ?`,
		formatTime(now), currentStepRunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE regeneration_plan_groups
		SET status = 'running'
		WHERE step_run_id = ? AND status = 'ready'`,
		currentStepRunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE regeneration_plans
		SET status = 'running'
		WHERE regeneration_plan_id IN (
			SELECT regeneration_plan_id
			FROM regeneration_plan_groups
			WHERE step_run_id = ? AND status = 'running'
		) AND status = 'pending'`,
		currentStepRunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_attempts SET status = 'waiting_approval'
		WHERE step_run_id = ? AND status = 'paused'
		AND EXISTS (SELECT 1 FROM task_items ti WHERE ti.current_attempt_id = execution_attempts.attempt_id AND ti.status = 'paused')
		AND EXISTS (SELECT 1 FROM execution_tool_calls b JOIN agent_tool_calls c ON c.agent_tool_call_id = b.agent_tool_call_id
			WHERE b.execution_attempt_id = execution_attempts.attempt_id AND c.status = 'pending_approval')`, currentStepRunID); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_items SET status = 'waiting_approval', updated_at = ?
		WHERE step_run_id = ? AND status = 'paused' AND current_attempt_id IN (SELECT attempt_id FROM execution_attempts WHERE status = 'waiting_approval')`, formatTime(now), currentStepRunID); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE task_items
		SET status = 'pending', current_attempt_id = NULL, updated_at = ?
		WHERE step_run_id = ? AND status = 'paused' AND current_attempt_id IS NULL`,
		formatTime(now), currentStepRunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = 'running', updated_at = ?
		WHERE run_id = ? AND status = 'paused'`,
		formatTime(now), run.RunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET version = version + 1, status = 'running', updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		formatTime(now), run.ProjectID, run.RunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	runRef := run.RunID
	if _, err := s.appendEvent(ctx, tx, run.ProjectID, &runRef, &currentStepRunID, "run.resumed", "run", run.RunID, nil); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := s.appendEvent(ctx, tx, run.ProjectID, &runRef, &currentStepRunID, "step.started", "step_run", currentStepRunID, nil); err != nil {
		return RunSnapshot{}, err
	}
	return finishResume()
}

const pauseResultCommitGrace = 30 * time.Second

func runHasActiveExecutionTx(ctx context.Context, tx *sql.Tx, run Run) (bool, error) {
	if run.CurrentStepRunID == nil {
		return false, nil
	}
	var count int
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM execution_attempts ea
		JOIN task_items ti ON ti.current_attempt_id = ea.attempt_id
		WHERE ea.run_id = ? AND ea.step_run_id = ?
			AND ea.status IN ('running', 'result_received')
			AND ti.status = 'running'`,
		run.RunID, *run.CurrentStepRunID,
	).Scan(&count)
	return count > 0, err
}

func (s *Store) pauseRunningStepTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	cursor *json.RawMessage,
	now time.Time,
	safeBoundary string,
) error {
	if run.CurrentStepRunID == nil {
		return domainError("RUN_STATE_CONFLICT", "生成任务没有可暂停的当前步骤。")
	}
	stepRunID := *run.CurrentStepRunID
	if stopped, err := s.guardExecutionPauseTx(ctx, tx, run, stepRunID, now); err != nil || stopped {
		return err
	}
	query := `UPDATE step_runs SET status = 'paused' WHERE step_run_id = ? AND status = 'running'`
	args := []any{stepRunID}
	if cursor != nil {
		query = `UPDATE step_runs SET status = 'paused', task_cursor_json = ? WHERE step_run_id = ? AND status = 'running'`
		args = []any{string(*cursor), stepRunID}
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return domainError("RUN_STATE_CONFLICT", "当前步骤不在可暂停的运行状态。")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE execution_attempts
		SET status = 'abandoned', ended_at = COALESCE(ended_at, ?), error_code = 'RUN_PAUSED'
		WHERE step_run_id = ? AND status = 'running'`,
		formatTime(now), stepRunID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE task_items
		SET status = 'paused', current_attempt_id = CASE WHEN current_attempt_id IN (
			SELECT attempt_id FROM execution_attempts WHERE status = 'result_received'
		) THEN current_attempt_id ELSE NULL END, updated_at = ?
		WHERE step_run_id = ? AND status IN ('pending', 'running')`,
		formatTime(now), stepRunID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = 'paused', updated_at = ?
		WHERE run_id = ? AND status IN ('running', 'pausing')`,
		formatTime(now), run.RunID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET version = version + 1, status = 'paused', updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		formatTime(now), run.ProjectID, run.RunID,
	); err != nil {
		return err
	}
	runRef := run.RunID
	_, err = s.appendEvent(ctx, tx, run.ProjectID, &runRef, &stepRunID, "run.paused", "run", run.RunID, map[string]any{"safe_boundary": safeBoundary})
	return err
}

func (s *Store) pauseAfterCompletedTaskIfRequestedTx(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	now time.Time,
	safeBoundary string,
) error {
	run, err := getRunTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	if run.Status != "pausing" {
		return nil
	}
	if active, err := runHasActiveExecutionTx(ctx, tx, run); err != nil || active {
		return err
	}
	return s.pauseRunningStepTx(ctx, tx, run, nil, now, safeBoundary)
}

func (s *Store) convergePausingRunsTx(ctx context.Context, tx *sql.Tx, now time.Time) error {
	commitGraceCutoff := formatTime(now.Add(-pauseResultCommitGrace))
	rows, err := tx.QueryContext(ctx, `
		SELECT r.run_id
		FROM runs r
		WHERE r.status = 'pausing' AND r.current_step_run_id IS NOT NULL
			AND NOT EXISTS (
				SELECT 1
				FROM execution_attempts ea
				JOIN task_items ti ON ti.current_attempt_id = ea.attempt_id
				WHERE ea.run_id = r.run_id AND ea.step_run_id = r.current_step_run_id
					AND ti.status = 'running'
					AND (
						(ea.status = 'running' AND ea.lease_until > ?)
						OR (ea.status = 'result_received' AND ea.ended_at > ?)
					)
			)`,
		formatTime(now), commitGraceCutoff,
	)
	if err != nil {
		return err
	}
	var runIDs []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return err
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, runID := range runIDs {
		run, err := getRunTx(ctx, tx, runID)
		if err != nil {
			return err
		}
		if err := s.pauseRunningStepTx(ctx, tx, run, nil, now, "execution_lease_recovered"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CancelRun(ctx context.Context, command CancelRunCommand) (RunSnapshot, error) {
	if !command.Confirmed {
		return RunSnapshot{}, domainError("REQUIRED_CONFIRMATION_MISSING", "结束生成任务前需要用户明确确认。")
	}
	if command.ActorRef == "" {
		command.ActorRef = actorRefFromContext(ctx)
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunSnapshot{}, err
	}
	defer tx.Rollback()

	run, err := getRunTx(ctx, tx, command.RunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if command.Scope == "" {
		command.Scope = run.ProjectID
	}
	cached, hit, err := s.beginExecutionControlCommand(ctx, tx, command.CommandMeta, run.ProjectID, "run", run.RunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if hit {
		return decodeIdempotentResult[RunSnapshot](cached)
	}
	switch run.Status {
	case "pending", "running", "waiting_approval", "pausing", "paused", "failed":
	default:
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "当前生成任务状态不允许结束。")
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT approval_request_id, subject_kind, subject_ref_id
		FROM approvals
		WHERE run_id = ? AND status = 'pending' AND scope <> 'final_selection'`,
		run.RunID,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	var pendingApprovals []struct {
		approvalID  string
		subjectKind string
		subjectID   string
	}
	for rows.Next() {
		var item struct {
			approvalID  string
			subjectKind string
			subjectID   string
		}
		if err := rows.Scan(
			&item.approvalID,
			&item.subjectKind,
			&item.subjectID,
		); err != nil {
			rows.Close()
			return RunSnapshot{}, err
		}
		pendingApprovals = append(pendingApprovals, item)
	}
	if err := rows.Close(); err != nil {
		return RunSnapshot{}, err
	}
	impactRows, err := tx.QueryContext(ctx, `
		SELECT impact_review_id
		FROM impact_reviews WHERE run_id = ? AND status = 'pending'
		ORDER BY impact_review_id ASC`,
		run.RunID,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	var pendingImpactReviewIDs []string
	for impactRows.Next() {
		var impactReviewID string
		if err := impactRows.Scan(&impactReviewID); err != nil {
			impactRows.Close()
			return RunSnapshot{}, err
		}
		pendingImpactReviewIDs = append(pendingImpactReviewIDs, impactReviewID)
	}
	if err := impactRows.Close(); err != nil {
		return RunSnapshot{}, err
	}
	resolution, err := json.Marshal(map[string]any{"action": "cancel_run"})
	if err != nil {
		return RunSnapshot{}, err
	}
	for _, approval := range pendingApprovals {
		switch approval.subjectKind {
		case "artifact_version":
			if _, err := tx.ExecContext(ctx, `
				UPDATE artifact_versions SET status = 'invalidated'
				WHERE artifact_version_id = ? AND status = 'pending_approval'`,
				approval.subjectID,
			); err != nil {
				return RunSnapshot{}, err
			}
		case "artifact_version_set":
			if _, err := tx.ExecContext(ctx, `
				UPDATE artifact_versions
				SET status = 'invalidated'
				WHERE status = 'pending_approval'
					AND artifact_version_id IN (
						SELECT artifact_version_id
						FROM approval_subject_versions
						WHERE approval_request_id = ?
					)`,
				approval.approvalID,
			); err != nil {
				return RunSnapshot{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE approvals
			SET status = 'cancelled', resolved_at = ?, resolution_json = ?, actor_ref = ?
			WHERE approval_request_id = ? AND status = 'pending'`,
			formatTime(now), string(resolution), command.ActorRef, approval.approvalID,
		); err != nil {
			return RunSnapshot{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE impact_reviews
		SET status = 'expired', resolved_at = ?
		WHERE run_id = ? AND status = 'pending'`,
		formatTime(now),
		run.RunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if err := cancelActiveRegenerationPlansTx(ctx, tx, run.RunID, now); err != nil {
		return RunSnapshot{}, err
	}
	if err := cancelExecutionToolsTx(ctx, tx, run.RunID, "", now); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE step_runs
		SET status = 'cancelled', ended_at = ?
		WHERE run_id = ? AND status IN ('pending', 'running', 'waiting_approval', 'paused', 'failed')`,
		formatTime(now), run.RunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE execution_attempts
		SET status = 'cancelled', ended_at = ?, error_code = 'RUN_CANCELLED'
		WHERE run_id = ? AND status IN ('running', 'result_received', 'repair_pending', 'waiting_approval', 'paused')`,
		formatTime(now), run.RunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE task_items
		SET status = 'cancelled', current_attempt_id = NULL, ended_at = ?, updated_at = ?
		WHERE run_id = ? AND status IN ('pending', 'running', 'repair_pending', 'waiting_approval', 'paused', 'failed')`,
		formatTime(now), formatTime(now), run.RunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = 'cancelled', ended_at = ?, updated_at = ?
		WHERE run_id = ?`,
		formatTime(now), formatTime(now), run.RunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if err := syncSkillInvocationForRunTx(ctx, tx, run.RunID, "cancelled", now); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET version = version + 1, status = 'ready', active_write_run_id = NULL,
			current_capability_id = NULL, updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		formatTime(now), run.ProjectID, run.RunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	runRef := run.RunID
	if _, err := s.appendEvent(ctx, tx, run.ProjectID, &runRef, run.CurrentStepRunID, "run.cancel_requested", "run", run.RunID, nil); err != nil {
		return RunSnapshot{}, err
	}
	for _, approval := range pendingApprovals {
		if _, err := s.appendEvent(ctx, tx, run.ProjectID, &runRef, run.CurrentStepRunID, "approval.cancelled", "approval", approval.approvalID, nil); err != nil {
			return RunSnapshot{}, err
		}
	}
	for _, impactReviewID := range pendingImpactReviewIDs {
		if _, err := s.appendEvent(ctx, tx, run.ProjectID, &runRef, run.CurrentStepRunID, "impact_review.expired", "impact_review", impactReviewID, map[string]any{"reason": "run_cancelled"}); err != nil {
			return RunSnapshot{}, err
		}
	}
	if _, err := s.appendEvent(ctx, tx, run.ProjectID, &runRef, run.CurrentStepRunID, "run.cancelled", "run", run.RunID, nil); err != nil {
		return RunSnapshot{}, err
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, run.RunID)
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

func cancelActiveRegenerationPlansTx(ctx context.Context, tx *sql.Tx, runID string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT rpg.stale_version_ids_json
		FROM regeneration_plan_groups rpg
		JOIN regeneration_plans rp ON rp.regeneration_plan_id = rpg.regeneration_plan_id
		WHERE rp.run_id = ?
			AND rp.status IN ('pending', 'running', 'waiting_approval', 'failed')`, runID)
	if err != nil {
		return err
	}
	var staleVersionIDs []string
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			rows.Close()
			return err
		}
		var ids []string
		if err := json.Unmarshal([]byte(encoded), &ids); err != nil {
			rows.Close()
			return err
		}
		staleVersionIDs = append(staleVersionIDs, ids...)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, staleVersionID := range staleVersionIDs {
		var artifactID, currentVersionID, staleStatus string
		err := tx.QueryRowContext(ctx, `
			SELECT av.artifact_id, a.current_version_id, av.status
			FROM artifact_versions av
			JOIN artifacts a ON a.artifact_id = av.artifact_id
			WHERE av.artifact_version_id = ?`, staleVersionID,
		).Scan(&artifactID, &currentVersionID, &staleStatus)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if currentVersionID == staleVersionID {
			var creationReason string
			var baseVersionID sql.NullString
			if err := tx.QueryRowContext(ctx, `
				SELECT creation_reason, base_version_id
				FROM artifact_versions WHERE artifact_version_id = ?`, staleVersionID,
			).Scan(&creationReason, &baseVersionID); err != nil {
				return err
			}
			if staleStatus == "stale" && baseVersionID.Valid &&
				(creationReason == "regenerate_artifact" || creationReason == "regenerate_step") {
				if _, err := tx.ExecContext(ctx, `
					UPDATE artifact_versions SET status = 'invalidated'
					WHERE artifact_version_id = ? AND status = 'stale'`, staleVersionID); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `
					UPDATE artifacts SET current_version_id = ?, updated_at = ?
					WHERE artifact_id = ? AND current_version_id = ?`,
					baseVersionID.String, formatTime(now), artifactID, staleVersionID); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `
					UPDATE artifact_versions SET status = 'confirmed'
					WHERE artifact_version_id = ? AND status = 'superseded'`, baseVersionID.String); err != nil {
					return err
				}
				continue
			}
			if staleStatus == "stale" {
				if _, err := tx.ExecContext(ctx, `
					UPDATE artifact_versions SET status = 'confirmed'
					WHERE artifact_version_id = ? AND status = 'stale'`, staleVersionID); err != nil {
					return err
				}
			}
			continue
		}

		var currentStatus, creationReason string
		var baseVersionID sql.NullString
		if err := tx.QueryRowContext(ctx, `
			SELECT status, creation_reason, base_version_id
			FROM artifact_versions WHERE artifact_version_id = ?`, currentVersionID,
		).Scan(&currentStatus, &creationReason, &baseVersionID); err != nil {
			return err
		}
		if currentStatus != "pending_approval" || creationReason != "regeneration" ||
			!baseVersionID.Valid || baseVersionID.String != staleVersionID {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE artifact_versions SET status = 'invalidated'
			WHERE artifact_version_id = ? AND status = 'pending_approval'`, currentVersionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE artifacts SET current_version_id = ?, updated_at = ?
			WHERE artifact_id = ? AND current_version_id = ?`,
			staleVersionID, formatTime(now), artifactID, currentVersionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE artifact_versions SET status = 'confirmed'
			WHERE artifact_version_id = ? AND status IN ('stale', 'superseded')`, staleVersionID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE regeneration_plan_groups SET status = 'cancelled'
		WHERE regeneration_plan_id IN (
			SELECT regeneration_plan_id FROM regeneration_plans
			WHERE run_id = ? AND status IN ('pending', 'running', 'waiting_approval', 'failed')
		) AND status IN ('blocked', 'ready', 'running', 'waiting_approval', 'failed')`, runID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE regeneration_plans
		SET status = 'cancelled', completed_at = COALESCE(completed_at, ?)
		WHERE run_id = ? AND status IN ('pending', 'running', 'waiting_approval', 'failed')`,
		formatTime(now), runID)
	return err
}

func getRunTx(ctx context.Context, tx *sql.Tx, runID string) (Run, error) {
	run, err := scanRun(tx.QueryRowContext(ctx, `
		SELECT run_id, project_id, conversation_id, capability_id, capability_version,
			run_kind, status, current_step_run_id, current_input_snapshot_version_id,
			input_snapshot_status, config_snapshot_json, started_at, ended_at, created_at, updated_at
		FROM runs WHERE run_id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, domainError("RUN_NOT_FOUND", "生成任务不存在。")
	}
	return run, err
}
