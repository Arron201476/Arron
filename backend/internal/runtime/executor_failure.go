package runtime

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"content-agent/backend/internal/capability"
)

var allowedExecutionErrorCodes = []string{
	"CONTEXT_MODALITY_NOT_SUPPORTED",
	"CONTEXT_ASSET_UNAVAILABLE",
	"MEDIA_INPUT_INVALID",
	"VIDEO_DURATION_EXCEEDED",
	"MEDIA_PROCESSING_FAILED",
	"MEDIA_OUTPUT_TOO_LARGE",
	"MEDIA_PROBE_FAILED",
	"SUBTITLE_OCR_INPUT_INVALID",
	"SUBTITLE_OCR_UPLOAD_PREPARE_FAILED",
	"SUBTITLE_OCR_UPLOAD_FAILED",
	"SUBTITLE_OCR_REQUEST_INVALID",
	"SUBTITLE_OCR_SUBMIT_FAILED",
	"SUBTITLE_OCR_TIMEOUT",
	"SUBTITLE_OCR_QUERY_FAILED",
	"SUBTITLE_OCR_TASK_FAILED",
	"SUBTITLE_OCR_RESULT_INVALID",
	"SUBTITLE_OCR_AUTH_FAILED",
	"SUBTITLE_OCR_UNAVAILABLE",
	"SUBTITLE_OCR_RESPONSE_INVALID",
	"SUBTITLE_OCR_FAILED",
	"MODEL_TIMEOUT",
	"MODEL_RATE_LIMIT",
	"PROVIDER_TEMPORARY_FAILURE",
	"PROVIDER_AUTH_FAILED",
	"PROVIDER_REQUEST_INVALID",
	"PROVIDER_RESPONSE_INVALID",
	"PROVIDER_CONTENT_POLICY_BLOCKED",
	"OUTPUT_REPAIR_FAILED",
	"RUNTIME_COMMIT_FAILED",
	"WORKER_INTERNAL_ERROR",
	"SDK_CHECKPOINT_FAILED",
	"SDK_EXECUTION_STATE_INVALID",
	"SDK_TOOL_REPLAY_RISK",
	"AGENT_TOOL_CONFIGURATION_INVALID",
	"AGENT_INPUT_GUARDRAIL_REJECTED",
	"AGENT_OUTPUT_GUARDRAIL_REJECTED",
}

func (s *Store) FailExecutionAttempt(
	ctx context.Context,
	command FailExecutionAttemptCommand,
) (AttemptFailureResult, error) {
	if command.AttemptID == "" || command.AttemptToken == "" ||
		command.InputSnapshotHash == "" ||
		!slices.Contains(allowedExecutionErrorCodes, command.ErrorCode) {
		return AttemptFailureResult{}, domainError(
			"REQUEST_VALIDATION_FAILED",
			"执行失败上报缺少有效字段。",
		)
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AttemptFailureResult{}, err
	}
	defer tx.Rollback()

	var attempt ExecutionAttempt
	var leaseUntil, usageJSON, startedAt, tokenHash string
	var responseHash, traceRef, endedAt, storedErrorCode sql.NullString
	var currentAttemptID sql.NullString
	var taskStatus, stepStatus, runStatus, projectID string
	var capabilityID, capabilityVersion, stepID string
	err = tx.QueryRowContext(ctx, `
		SELECT ea.attempt_id, ea.run_id, ea.step_run_id, ea.task_item_id, ea.attempt_no,
			ea.executor_id, ea.provider_id, ea.worker_id, ea.request_fingerprint,
			ea.input_snapshot_hash, ea.status, ea.lease_until, ea.response_hash,
			ea.usage_json, ea.trace_ref, ea.started_at, ea.ended_at, ea.error_code,
			ea.token_hash, ti.current_attempt_id, ti.status, sr.status, r.status,
			r.project_id, r.capability_id, r.capability_version, sr.step_id
		FROM execution_attempts ea
		JOIN task_items ti ON ti.task_item_id = ea.task_item_id
		JOIN step_runs sr ON sr.step_run_id = ea.step_run_id
		JOIN runs r ON r.run_id = ea.run_id
		WHERE ea.attempt_id = ?`, command.AttemptID).Scan(
		&attempt.AttemptID, &attempt.RunID, &attempt.StepRunID, &attempt.TaskItemID,
		&attempt.AttemptNo, &attempt.ExecutorID, &attempt.ProviderID, &attempt.WorkerID,
		&attempt.RequestFingerprint, &attempt.InputSnapshotHash, &attempt.Status,
		&leaseUntil, &responseHash, &usageJSON, &traceRef, &startedAt, &endedAt,
		&storedErrorCode, &tokenHash, &currentAttemptID, &taskStatus, &stepStatus,
		&runStatus, &projectID, &capabilityID, &capabilityVersion, &stepID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AttemptFailureResult{}, domainError(
			"EXECUTION_ATTEMPT_NOT_FOUND",
			"执行尝试不存在。",
		)
	}
	if err != nil {
		return AttemptFailureResult{}, err
	}
	attempt.ResponseHash = stringPointer(responseHash)
	attempt.Usage = []byte(usageJSON)
	attempt.TraceRef = stringPointer(traceRef)
	attempt.ErrorCode = stringPointer(storedErrorCode)
	attempt.LeaseUntil, err = parseTime(leaseUntil)
	if err != nil {
		return AttemptFailureResult{}, err
	}
	attempt.StartedAt, err = parseTime(startedAt)
	if err != nil {
		return AttemptFailureResult{}, err
	}
	attempt.EndedAt, err = optionalTime(endedAt)
	if err != nil {
		return AttemptFailureResult{}, err
	}
	if subtle.ConstantTimeCompare(
		[]byte(tokenHash),
		[]byte(sha256Hex([]byte(command.AttemptToken))),
	) != 1 {
		return AttemptFailureResult{}, domainError(
			"ATTEMPT_TOKEN_INVALID",
			"执行尝试 Token 无效。",
		)
	}
	if attempt.InputSnapshotHash != command.InputSnapshotHash {
		return AttemptFailureResult{}, domainError(
			"ATTEMPT_INPUT_CHANGED",
			"执行失败对应的输入快照已变化。",
		)
	}
	if attempt.Status == "failed" && attempt.ErrorCode != nil &&
		*attempt.ErrorCode == command.ErrorCode {
		task, err := scanTaskItem(tx.QueryRowContext(ctx, taskItemSelect+`
			WHERE task_item_id = ?`, attempt.TaskItemID))
		if err != nil {
			return AttemptFailureResult{}, err
		}
		return AttemptFailureResult{
			Attempt:   attempt,
			Task:      task,
			Retryable: task.Status == "pending",
		}, nil
	}
	if (attempt.Status != "running" && attempt.Status != "result_received") ||
		!currentAttemptID.Valid || currentAttemptID.String != attempt.AttemptID ||
		taskStatus != "running" || stepStatus != "running" || !runAcceptsActiveAttemptResult(runStatus) {
		return AttemptFailureResult{}, domainError(
			"ATTEMPT_STALE",
			"执行尝试已经失效，不能上报失败。",
		)
	}
	if !attempt.LeaseUntil.After(now) {
		return AttemptFailureResult{}, domainError(
			"ATTEMPT_LEASE_EXPIRED",
			"执行尝试 Lease 已过期。",
		)
	}

	entry, ok, registryErr := s.capabilityEntryForRunQuery(
		ctx, tx, attempt.RunID, capabilityID,
	)
	if registryErr != nil {
		return AttemptFailureResult{}, registryErr
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil ||
		entry.Definition.Version != capabilityVersion {
		return AttemptFailureResult{}, domainError(
			"CAPABILITY_VERSION_UNAVAILABLE",
			"执行失败对应的能力版本当前不可用。",
		)
	}
	var step *capability.CompiledStep
	for index := range entry.Definition.Steps {
		if entry.Definition.Steps[index].ID == stepID {
			step = &entry.Definition.Steps[index]
			break
		}
	}
	if step == nil {
		return AttemptFailureResult{}, domainError(
			"CAPABILITY_VERSION_UNAVAILABLE",
			"执行失败对应的步骤定义当前不可用。",
		)
	}
	retryable := isAutomaticallyRetryableExecutionError(command.ErrorCode) &&
		slices.Contains(step.Retry.RetryableErrors, command.ErrorCode) &&
		attempt.AttemptNo <= step.Retry.AutomaticAttempts
	replayRisk, err := executionToolReplayRiskTx(ctx, tx, attempt.AttemptID)
	if err != nil {
		return AttemptFailureResult{}, err
	}
	if replayRisk {
		retryable = false
	}
	preserveBatchFailure := step.Batch != nil &&
		step.Batch.FailurePolicy == "preserve_success_retry_failed"
	failureDetail := normalizeExecutionFailureDetail(command.ErrorCode, command.FailureDetail, retryable)
	if replayRisk {
		failureDetail.Summary = "执行中已发出写入或敏感工具调用，或保留了 SDK checkpoint；自动重试和从头重试已停止，请先核对已有结果，避免重复执行。"
	}
	if err := cancelExecutionToolsTx(ctx, tx, attempt.RunID, attempt.AttemptID, now); err != nil {
		return AttemptFailureResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO execution_failure_details(
			attempt_id, task_item_id, error_code, stage, summary, technical_detail,
			provider_output, provider_status_code, provider_request_id, transport_category, retryable, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(attempt_id) DO UPDATE SET
			error_code = excluded.error_code,
			stage = excluded.stage,
			summary = excluded.summary,
			technical_detail = excluded.technical_detail,
			provider_output = excluded.provider_output,
			provider_status_code = excluded.provider_status_code,
			provider_request_id = excluded.provider_request_id,
			transport_category = excluded.transport_category,
			retryable = excluded.retryable`,
		attempt.AttemptID, attempt.TaskItemID, failureDetail.ErrorCode,
		failureDetail.Stage, failureDetail.Summary, failureDetail.TechnicalDetail,
		failureDetail.ProviderOutput,
		failureDetail.ProviderStatusCode, failureDetail.ProviderRequestID,
		failureDetail.TransportCategory, boolInteger(failureDetail.Retryable), formatTime(now),
	); err != nil {
		return AttemptFailureResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE execution_attempts
		SET status = 'failed', ended_at = ?, error_code = ?
		WHERE attempt_id = ? AND status IN ('running', 'result_received')`,
		formatTime(now), command.ErrorCode, attempt.AttemptID,
	); err != nil {
		return AttemptFailureResult{}, err
	}
	if retryable {
		if _, err := tx.ExecContext(ctx, `
			UPDATE task_items
			SET status = 'pending', current_attempt_id = NULL, failure = ?, updated_at = ?
			WHERE task_item_id = ? AND current_attempt_id = ? AND status = 'running'`,
			command.ErrorCode, formatTime(now), attempt.TaskItemID, attempt.AttemptID,
		); err != nil {
			return AttemptFailureResult{}, err
		}
		if runStatus == "pausing" {
			if err := s.pauseAfterCompletedTaskIfRequestedTx(
				ctx, tx, attempt.RunID, now, "task_failed_retry_pending",
			); err != nil {
				return AttemptFailureResult{}, err
			}
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE task_items
			SET status = 'failed', current_attempt_id = NULL, failure = ?,
				ended_at = ?, updated_at = ?
			WHERE task_item_id = ? AND current_attempt_id = ? AND status = 'running'`,
			command.ErrorCode, formatTime(now), formatTime(now),
			attempt.TaskItemID, attempt.AttemptID,
		); err != nil {
			return AttemptFailureResult{}, err
		}
		if preserveBatchFailure {
			if step.Batch.Execution == "sequential" {
				if _, err := tx.ExecContext(ctx, `
					UPDATE task_items
					SET status = 'failed', failure = ?, ended_at = ?, updated_at = ?
					WHERE step_run_id = ? AND status = 'pending'`,
					command.ErrorCode, formatTime(now), formatTime(now), attempt.StepRunID,
				); err != nil {
					return AttemptFailureResult{}, err
				}
			}
			if _, err := s.finalizePreservedBatchFailureIfSettledTx(
				ctx, tx, projectID, attempt.RunID, attempt.StepRunID, command.ErrorCode, now,
			); err != nil {
				return AttemptFailureResult{}, err
			}
		} else {
			if err := s.failStepRunAndProjectTx(
				ctx, tx, projectID, attempt.RunID, attempt.StepRunID, command.ErrorCode, now,
			); err != nil {
				return AttemptFailureResult{}, err
			}
		}
	}

	runRef, stepRef := attempt.RunID, attempt.StepRunID
	if retryable {
		if _, err := s.appendEvent(
			ctx,
			tx,
			projectID,
			&runRef,
			&stepRef,
			"task.retry_scheduled",
			"task_item",
			attempt.TaskItemID,
			map[string]any{
				"failed_attempt_id": attempt.AttemptID,
				"reason":            command.ErrorCode,
			},
		); err != nil {
			return AttemptFailureResult{}, err
		}
	} else if preserveBatchFailure {
		if _, err := s.appendEvent(
			ctx,
			tx,
			projectID,
			&runRef,
			&stepRef,
			"task.failed",
			"task_item",
			attempt.TaskItemID,
			map[string]any{
				"failure_code": command.ErrorCode,
				"retryable":    false,
				"preserved":    true,
			},
		); err != nil {
			return AttemptFailureResult{}, err
		}
	} else if _, err := s.appendEvent(
		ctx,
		tx,
		projectID,
		&runRef,
		&stepRef,
		"task.failed",
		"task_item",
		attempt.TaskItemID,
		map[string]any{
			"failure_code": command.ErrorCode,
			"retryable":    false,
		},
	); err != nil {
		return AttemptFailureResult{}, err
	}
	failedAttempt, err := scanExecutionAttempt(tx.QueryRowContext(ctx, executionAttemptSelect+`
		WHERE attempt_id = ?`, attempt.AttemptID))
	if err != nil {
		return AttemptFailureResult{}, err
	}
	task, err := scanTaskItem(tx.QueryRowContext(ctx, taskItemSelect+`
		WHERE task_item_id = ?`, attempt.TaskItemID))
	if err != nil {
		return AttemptFailureResult{}, err
	}
	result := AttemptFailureResult{
		Attempt:   failedAttempt,
		Task:      task,
		Retryable: retryable,
	}
	if err := tx.Commit(); err != nil {
		return AttemptFailureResult{}, err
	}
	return result, nil
}

func isAutomaticallyRetryableExecutionError(errorCode string) bool {
	switch errorCode {
	case "MODEL_TIMEOUT", "MODEL_RATE_LIMIT", "PROVIDER_TEMPORARY_FAILURE",
		"SUBTITLE_OCR_TIMEOUT", "SUBTITLE_OCR_QUERY_FAILED", "SUBTITLE_OCR_UNAVAILABLE":
		return true
	default:
		return false
	}
}

func normalizeExecutionFailureDetail(
	errorCode string,
	detail *ExecutionFailureDetail,
	retryable bool,
) ExecutionFailureDetail {
	result := ExecutionFailureDetail{
		ErrorCode: errorCode,
		Stage:     "worker_execution",
		Summary:   "执行任务失败。",
		Retryable: retryable,
	}
	if detail != nil {
		result = *detail
		result.ErrorCode = errorCode
		result.Retryable = retryable
	}
	result.Stage = truncateUTF8(strings.TrimSpace(result.Stage), 64)
	if result.Stage == "" {
		result.Stage = "worker_execution"
	}
	result.Summary = truncateUTF8(strings.TrimSpace(result.Summary), 512)
	if result.Summary == "" {
		result.Summary = "执行任务失败。"
	}
	result.TechnicalDetail = normalizedFailurePointer(result.TechnicalDetail, 4096)
	result.ProviderOutput = normalizedFailurePointer(result.ProviderOutput, 200000)
	result.ProviderRequestID = normalizedFailurePointer(result.ProviderRequestID, 256)
	result.TransportCategory = normalizedFailurePointer(result.TransportCategory, 64)
	return result
}

func normalizedFailurePointer(value *string, limit int) *string {
	if value == nil {
		return nil
	}
	normalized := truncateUTF8(strings.TrimSpace(*value), limit)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) finalizePreservedBatchFailureIfSettledTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runID string,
	stepRunID string,
	fallbackFailureCode string,
	now time.Time,
) (bool, error) {
	var unsettled int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM task_items
		WHERE step_run_id = ? AND status IN ('pending', 'running', 'repair_pending', 'waiting_approval', 'paused')`,
		stepRunID,
	).Scan(&unsettled); err != nil {
		return false, err
	}
	if unsettled > 0 {
		return false, nil
	}
	var failureCode sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT failure FROM task_items
		WHERE step_run_id = ? AND status = 'failed'
		ORDER BY item_order ASC, task_item_id ASC LIMIT 1`,
		stepRunID,
	).Scan(&failureCode); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if !failureCode.Valid || failureCode.String == "" {
		failureCode.String = fallbackFailureCode
	}
	if err := s.failStepRunAndProjectTx(
		ctx, tx, projectID, runID, stepRunID, failureCode.String, now,
	); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) failStepRunAndProjectTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runID string,
	stepRunID string,
	failureCode string,
	now time.Time,
) error {
	if err := cancelExecutionToolsTx(ctx, tx, runID, "", now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE regeneration_plan_groups SET status = 'failed'
		WHERE step_run_id = ? AND status IN ('ready','running')`, stepRunID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE regeneration_plans SET status = 'failed'
		WHERE regeneration_plan_id IN (
			SELECT regeneration_plan_id FROM regeneration_plan_groups WHERE step_run_id = ?
		) AND status IN ('pending','running','waiting_approval')`, stepRunID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE step_runs SET status = 'failed', ended_at = ?
		WHERE step_run_id = ? AND status = 'running'`,
		formatTime(now), stepRunID,
	); err != nil {
		return err
	}
	if _, err := s.appendEvent(ctx, tx, projectID, &runID, &stepRunID, "step.failed", "step_run", stepRunID,
		map[string]any{"failure_code": failureCode, "retryable": false}); err != nil {
		return err
	}
	if advanced, err := s.advanceRunAfterFailedStepTx(ctx, tx, projectID, runID, stepRunID, failureCode, now); err != nil || advanced {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = 'failed', updated_at = ?
		WHERE run_id = ? AND status IN ('running', 'pausing')`,
		formatTime(now), runID,
	); err != nil {
		return err
	}
	if err := syncSkillInvocationForRunTx(ctx, tx, runID, "failed", now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET version = version + 1, status = 'failed', updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		formatTime(now), projectID, runID,
	); err != nil {
		return err
	}
	runRef, stepRef := runID, stepRunID
	for _, event := range []struct {
		eventType   string
		subjectType string
		subjectID   string
	}{
		{"run.failed", "run", runID},
	} {
		if _, err := s.appendEvent(
			ctx, tx, projectID, &runRef, &stepRef,
			event.eventType, event.subjectType, event.subjectID,
			map[string]any{"failure_code": failureCode, "retryable": false},
		); err != nil {
			return err
		}
	}
	return nil
}
