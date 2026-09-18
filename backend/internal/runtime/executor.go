package runtime

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"content-agent/backend/internal/capability"
)

const (
	defaultAttemptLeaseSeconds = 300
	minAttemptLeaseSeconds     = 30
	maxAttemptLeaseSeconds     = 1800
	maxProviderResponseBytes   = 4 << 20
)

func (s *Store) ensureStepTasksForResume(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	stepRunID string,
	step capability.CompiledStep,
	stepInput json.RawMessage,
	cursor json.RawMessage,
	now time.Time,
) error {
	var taskCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM task_items WHERE step_run_id = ?`, stepRunID,
	).Scan(&taskCount); err != nil {
		return err
	}
	if taskCount > 0 {
		return nil
	}
	if isScriptQualityReviewStep(step) {
		return s.planQualityReviewTasksTx(ctx, tx, run, stepRunID, step, stepInput, now)
	}
	if step.Batch != nil || step.Kind == "batch" {
		return s.planBatchStepTasksTx(
			ctx,
			tx,
			run,
			stepRunID,
			step,
			stepInput,
			cursor,
			now,
		)
	}
	if !workerExecutableStepKind(step.Kind) {
		return domainError("RUN_STATE_CONFLICT", "当前步骤应由 Runtime 执行，不能交给 Worker。")
	}
	inputSnapshot, err := json.Marshal(map[string]any{
		"run_input_snapshot_version_id": run.CurrentInputSnapshotVersionID,
		"step_input_versions":           json.RawMessage(stepInput),
	})
	if err != nil {
		return err
	}
	taskID := s.newID("tsk")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO task_items(
			task_item_id, step_run_id, run_id, item_key, item_order, status,
			attempt_count, input_snapshot_json, cursor_json, created_at, updated_at
		) VALUES(?, ?, ?, ?, 1, 'pending', 0, ?, ?, ?, ?)`,
		taskID, stepRunID, run.RunID, "step:"+step.ID, string(inputSnapshot),
		string(cursor), formatTime(now), formatTime(now),
	); err != nil {
		return err
	}
	runRef, stepRef := run.RunID, stepRunID
	if _, err := s.appendEvent(ctx, tx, run.ProjectID, &runRef, &stepRef, "task.queued", "task_item", taskID, map[string]any{
		"executor_id": step.ExecutorRef,
		"item_key":    "step:" + step.ID,
	}); err != nil {
		return err
	}
	return nil
}

func workerExecutableStepKind(kind string) bool {
	return kind == "model" || kind == "tool" || kind == "aggregate" ||
		kind == "shared_workflow"
}

func (s *Store) ListTaskItems(ctx context.Context, stepRunID string) ([]TaskItem, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM step_runs WHERE step_run_id = ?`, stepRunID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, domainError("STEP_RUN_NOT_FOUND", "执行步骤不存在。")
	}
	rows, err := s.db.QueryContext(ctx, taskItemSelect+`
		WHERE step_run_id = ? ORDER BY item_order ASC, task_item_id ASC`, stepRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TaskItem
	for rows.Next() {
		item, err := scanTaskItem(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	failureDetails, err := s.listLatestFailureDetails(ctx, stepRunID)
	if err != nil {
		return nil, err
	}
	repairs, err := s.listTaskOutputRepairs(ctx, stepRunID)
	if err != nil {
		return nil, err
	}
	for index := range result {
		if repair, ok := repairs[result[index].TaskItemID]; ok {
			result[index].OutputRepair = &repair
		}
		if result[index].Failure == nil ||
			(result[index].Status != "failed" && result[index].Status != "pending") {
			continue
		}
		if detail, ok := failureDetails[result[index].TaskItemID]; ok {
			copy := detail
			result[index].FailureDetail = &copy
		}
	}
	if result == nil {
		result = []TaskItem{}
	}
	return result, nil
}

func (s *Store) listLatestFailureDetails(
	ctx context.Context,
	stepRunID string,
) (map[string]ExecutionFailureDetail, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT efd.task_item_id, efd.error_code, efd.stage, efd.summary,
			efd.technical_detail, efd.provider_output, efd.provider_status_code, efd.provider_request_id,
			efd.transport_category, efd.retryable
		FROM execution_failure_details efd
		JOIN execution_attempts ea ON ea.attempt_id = efd.attempt_id
		WHERE ea.step_run_id = ? AND ea.attempt_no = (
			SELECT MAX(latest.attempt_no)
			FROM execution_attempts latest
			JOIN execution_failure_details latest_detail ON latest_detail.attempt_id = latest.attempt_id
			WHERE latest.task_item_id = ea.task_item_id
		)`, stepRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]ExecutionFailureDetail)
	for rows.Next() {
		var taskItemID string
		var detail ExecutionFailureDetail
		var technicalDetail, providerOutput, providerRequestID, transportCategory sql.NullString
		var providerStatusCode sql.NullInt64
		var retryable int
		if err := rows.Scan(
			&taskItemID, &detail.ErrorCode, &detail.Stage, &detail.Summary,
			&technicalDetail, &providerOutput, &providerStatusCode, &providerRequestID,
			&transportCategory, &retryable,
		); err != nil {
			return nil, err
		}
		detail.TechnicalDetail = stringPointer(technicalDetail)
		detail.ProviderOutput = stringPointer(providerOutput)
		detail.ProviderRequestID = stringPointer(providerRequestID)
		detail.TransportCategory = stringPointer(transportCategory)
		if providerStatusCode.Valid {
			value := int(providerStatusCode.Int64)
			detail.ProviderStatusCode = &value
		}
		detail.Retryable = retryable != 0
		result[taskItemID] = detail
	}
	return result, rows.Err()
}

func (s *Store) GetExecutionAttempt(ctx context.Context, attemptID string) (ExecutionAttempt, error) {
	attempt, err := scanExecutionAttempt(s.db.QueryRowContext(ctx, executionAttemptSelect+`
		WHERE attempt_id = ?`, attemptID))
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutionAttempt{}, domainError("EXECUTION_ATTEMPT_NOT_FOUND", "执行尝试不存在。")
	}
	return attempt, err
}

func (s *Store) ClaimExecutionTask(
	ctx context.Context,
	command ClaimExecutionTaskCommand,
) (*TaskClaim, error) {
	if command.WorkerID == "" || command.ProviderID == "" || len(command.ExecutorIDs) == 0 {
		return nil, domainError("REQUEST_VALIDATION_FAILED", "任务领取缺少 Worker、Provider 或 Executor。")
	}
	if command.LeaseSeconds == 0 {
		command.LeaseSeconds = defaultAttemptLeaseSeconds
	}
	if command.LeaseSeconds < minAttemptLeaseSeconds || command.LeaseSeconds > maxAttemptLeaseSeconds {
		return nil, domainError("REQUEST_VALIDATION_FAILED", "任务 Lease 时长不在允许范围内。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.requeueExpiredAttempts(ctx, tx, now); err != nil {
		return nil, err
	}
	if err := s.convergePausingRunsTx(ctx, tx, now); err != nil {
		return nil, err
	}
	if result, err := s.claimExecutionResultTx(ctx, tx, command, now); err != nil {
		return nil, err
	} else if result != nil {
		if err := bindExecutionInputsTx(ctx, tx, result); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return result, nil
	}
	if resume, err := s.claimExecutionResumeTx(ctx, tx, command, now); err != nil {
		return nil, err
	} else if resume != nil {
		if err := bindExecutionInputsTx(ctx, tx, resume); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return resume, nil
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT ti.task_item_id, ti.step_run_id, ti.run_id, ti.item_key, ti.item_order,
			ti.status, ti.attempt_count, ti.input_snapshot_json, ti.cursor_json,
			ti.current_attempt_id, ti.output_artifact_version_id, ti.started_at,
			ti.ended_at, ti.failure, ti.created_at, ti.updated_at,
			r.project_id, r.capability_id, r.capability_version, r.config_snapshot_json,
			r.conversation_id, r.run_kind, sr.step_id
		FROM task_items ti
		JOIN runs r ON r.run_id = ti.run_id
		JOIN step_runs sr ON sr.step_run_id = ti.step_run_id
		WHERE ti.status = 'pending' AND ti.current_attempt_id IS NULL
			AND r.status = 'running' AND sr.status = 'running'
		ORDER BY ti.item_order ASC, ti.created_at ASC, ti.task_item_id ASC`)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		task              TaskItem
		projectID         string
		capabilityID      string
		capabilityVersion string
		configSnapshot    json.RawMessage
		configSnapshotRef *RunConfigSnapshot
		conversationID    string
		runKind           string
		stepID            string
		executorID        string
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		var inputJSON, cursorJSON, configJSON, createdAt, updatedAt string
		var currentAttemptID, outputVersionID, startedAt, endedAt, failure sql.NullString
		if err := rows.Scan(
			&item.task.TaskItemID, &item.task.StepRunID, &item.task.RunID,
			&item.task.ItemKey, &item.task.ItemOrder, &item.task.Status,
			&item.task.AttemptCount, &inputJSON, &cursorJSON, &currentAttemptID,
			&outputVersionID, &startedAt, &endedAt, &failure, &createdAt, &updatedAt,
			&item.projectID, &item.capabilityID, &item.capabilityVersion,
			&configJSON, &item.conversationID, &item.runKind, &item.stepID,
		); err != nil {
			rows.Close()
			return nil, err
		}
		item.task.InputSnapshot = json.RawMessage(inputJSON)
		item.task.Cursor = json.RawMessage(cursorJSON)
		item.task.CurrentAttemptID = stringPointer(currentAttemptID)
		item.task.OutputArtifactVersionID = stringPointer(outputVersionID)
		item.task.StartedAt, err = optionalTime(startedAt)
		if err != nil {
			rows.Close()
			return nil, err
		}
		item.task.EndedAt, err = optionalTime(endedAt)
		if err != nil {
			rows.Close()
			return nil, err
		}
		item.task.Failure = stringPointer(failure)
		item.task.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			rows.Close()
			return nil, err
		}
		item.task.UpdatedAt, err = parseTime(updatedAt)
		if err != nil {
			rows.Close()
			return nil, err
		}
		item.configSnapshot = json.RawMessage(configJSON)
		entry, ok, registryErr := s.capabilityEntryForRunQuery(ctx, tx, item.task.RunID, item.capabilityID)
		if registryErr != nil {
			rows.Close()
			return nil, registryErr
		}
		if !ok || entry.Definition == nil || entry.Definition.Version != item.capabilityVersion {
			continue
		}
		configRef := ""
		sequential := false
		for _, step := range entry.Definition.Steps {
			if step.ID == item.stepID {
				item.executorID = step.ExecutorRef
				sequential = step.Batch != nil && step.Batch.Execution == "sequential"
				if step.ConfigRef != nil {
					configRef = *step.ConfigRef
				}
				break
			}
		}
		if configRef == "" {
			configRef, _ = configReference(item.configSnapshot)
		}
		if configRef != "" {
			snapshot, snapshotErr := latestRunConfigSnapshotTx(ctx, tx, item.task.RunID, configRef)
			if snapshotErr != nil {
				// A malformed historical run must not prevent workers from claiming
				// unrelated healthy tasks that appear later in the queue.
				if isDomainErrorCode(snapshotErr, "CONFIG_SNAPSHOT_NOT_FOUND") {
					continue
				}
				rows.Close()
				return nil, snapshotErr
			}
			item.configSnapshot = snapshot.Payload
			item.configSnapshotRef = &snapshot
		}
		if item.executorID == "" || !slices.Contains(command.ExecutorIDs, item.executorID) {
			continue
		}
		if sequential {
			var unfinishedEarlier int
			if err := tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM task_items
				WHERE step_run_id = ? AND item_order < ?
					AND status != 'succeeded'`,
				item.task.StepRunID,
				item.task.ItemOrder,
			).Scan(&unfinishedEarlier); err != nil {
				rows.Close()
				return nil, err
			}
			if unfinishedEarlier != 0 {
				continue
			}
		}
		// Filter before stopping the scan so incompatible or dependency-blocked
		// tasks cannot starve a healthy task beyond a fixed queue window.
		candidates = append(candidates, item)
		break
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	selected := candidates[0]
	attemptID := s.newID("atm")
	attemptToken := s.newID("tok")
	tokenHash := sha256Hex([]byte(attemptToken))
	contextPack, err := s.buildStepExecutionContextPack(ctx, tx, stepContextBuildRequest{
		ProjectID:         selected.projectID,
		ConversationID:    selected.conversationID,
		RunID:             selected.task.RunID,
		RunKind:           selected.runKind,
		CapabilityID:      selected.capabilityID,
		CapabilityVersion: selected.capabilityVersion,
		StepRunID:         selected.task.StepRunID,
		StepID:            selected.stepID,
		TaskItemID:        selected.task.TaskItemID,
		ItemKey:           selected.task.ItemKey,
		ProviderID:        command.ProviderID,
		ModelID:           command.ModelID,
		ConfigSnapshot:    selected.configSnapshot,
		ConfigSnapshotRef: selected.configSnapshotRef,
		TaskInputSnapshot: selected.task.InputSnapshot,
		TaskCursor:        selected.task.Cursor,
		CreatedAt:         now,
	})
	if err != nil {
		return nil, err
	}
	contextPayload, err := json.Marshal(contextPack)
	if err != nil {
		return nil, err
	}
	inputHash := contextPack.ContextHash
	requestFingerprint := sha256Hex([]byte(fmt.Sprintf(
		"%s\n%s\n%s\n%s\n%d",
		selected.task.TaskItemID,
		selected.executorID,
		command.ProviderID,
		inputHash,
		selected.task.AttemptCount+1,
	)))
	leaseUntil := now.Add(time.Duration(command.LeaseSeconds) * time.Second)
	result, err := tx.ExecContext(ctx, `
		UPDATE task_items
		SET status = 'running', attempt_count = attempt_count + 1,
			current_attempt_id = ?, failure = NULL,
			started_at = COALESCE(started_at, ?), updated_at = ?
		WHERE task_item_id = ? AND status = 'pending' AND current_attempt_id IS NULL`,
		attemptID, formatTime(now), formatTime(now), selected.task.TaskItemID,
	)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, domainError("EXECUTION_TASK_CLAIM_CONFLICT", "任务已被其他 Worker 领取。")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO execution_attempts(
			attempt_id, run_id, step_run_id, task_item_id, attempt_no, executor_id,
			provider_id, worker_id, request_fingerprint, input_snapshot_hash, status,
			token_hash, lease_until, usage_json, started_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'running', ?, ?, '{}', ?)`,
		attemptID, selected.task.RunID, selected.task.StepRunID, selected.task.TaskItemID,
		selected.task.AttemptCount+1, selected.executorID, command.ProviderID, command.WorkerID,
		requestFingerprint, inputHash, tokenHash, formatTime(leaseUntil), formatTime(now),
	); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO context_packs(
			context_pack_id, attempt_id, project_id, run_id, step_run_id, task_item_id,
			pack_type, pack_version, context_hash, payload_json, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		contextPack.ContextPackID, attemptID, selected.projectID, selected.task.RunID,
		selected.task.StepRunID, selected.task.TaskItemID, contextPack.PackType,
		contextPack.ContextPackVersion, contextPack.ContextHash, string(contextPayload),
		formatTime(now),
	); err != nil {
		return nil, err
	}
	runRef, stepRef := selected.task.RunID, selected.task.StepRunID
	if _, err := s.appendEvent(ctx, tx, selected.projectID, &runRef, &stepRef, "context.build_completed", "context_pack", contextPack.ContextPackID, map[string]any{
		"context_hash": contextPack.ContextHash,
		"pack_type":    contextPack.PackType,
	}); err != nil {
		return nil, err
	}
	if _, err := s.appendEvent(ctx, tx, selected.projectID, &runRef, &stepRef, "task.started", "task_item", selected.task.TaskItemID, map[string]any{
		"attempt_id":  attemptID,
		"executor_id": selected.executorID,
	}); err != nil {
		return nil, err
	}
	claimedTask, err := scanTaskItem(tx.QueryRowContext(ctx, taskItemSelect+`
		WHERE task_item_id = ?`, selected.task.TaskItemID))
	if err != nil {
		return nil, err
	}
	attempt, err := scanExecutionAttempt(tx.QueryRowContext(ctx, executionAttemptSelect+`
		WHERE attempt_id = ?`, attemptID))
	if err != nil {
		return nil, err
	}
	claim := &TaskClaim{
		Task:              claimedTask,
		Attempt:           attempt,
		AttemptToken:      attemptToken,
		CapabilityID:      selected.capabilityID,
		CapabilityVersion: selected.capabilityVersion,
		ExecutorID:        selected.executorID,
		ConfigSnapshot:    selected.configSnapshot,
		ContextPack:       contextPack,
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claim, nil
}

func (s *Store) SubmitExecutionResult(
	ctx context.Context,
	command SubmitExecutionResultCommand,
) (ExecutionAttempt, error) {
	if command.AttemptID == "" || command.AttemptToken == "" || command.InputSnapshotHash == "" ||
		len(command.ResponsePayload) == 0 || len(command.ResponsePayload) > maxProviderResponseBytes ||
		!json.Valid(command.ResponsePayload) {
		return ExecutionAttempt{}, domainError("REQUEST_VALIDATION_FAILED", "执行结果请求无效或超过限制。")
	}
	if len(command.Usage) == 0 {
		command.Usage = json.RawMessage(`{}`)
	}
	if !jsonObject(command.Usage) {
		return ExecutionAttempt{}, domainError("REQUEST_VALIDATION_FAILED", "Usage 必须是 JSON 对象。")
	}
	now := s.now()
	responseHash := sha256Hex(command.ResponsePayload)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExecutionAttempt{}, err
	}
	defer tx.Rollback()

	var attempt ExecutionAttempt
	var leaseUntil, usageJSON, startedAt string
	var responseHashValue, traceRefValue, endedAtValue, errorCodeValue sql.NullString
	var tokenHash string
	var currentAttemptValue sql.NullString
	var runStatus, stepStatus, taskStatus, projectID string
	if err := tx.QueryRowContext(ctx, `
		SELECT ea.attempt_id, ea.run_id, ea.step_run_id, ea.task_item_id, ea.attempt_no,
			ea.executor_id, ea.provider_id, ea.worker_id, ea.request_fingerprint,
			ea.input_snapshot_hash, ea.status, ea.lease_until, ea.response_hash,
			ea.usage_json, ea.trace_ref, ea.started_at, ea.ended_at, ea.error_code,
			ea.token_hash, ea.response_payload_json,
			r.status, sr.status, ti.status, ti.current_attempt_id, r.project_id
		FROM execution_attempts ea
		JOIN task_items ti ON ti.task_item_id = ea.task_item_id
		JOIN step_runs sr ON sr.step_run_id = ea.step_run_id
		JOIN runs r ON r.run_id = ea.run_id
		WHERE ea.attempt_id = ?`, command.AttemptID).Scan(
		&attempt.AttemptID, &attempt.RunID, &attempt.StepRunID, &attempt.TaskItemID,
		&attempt.AttemptNo, &attempt.ExecutorID, &attempt.ProviderID, &attempt.WorkerID,
		&attempt.RequestFingerprint, &attempt.InputSnapshotHash, &attempt.Status,
		&leaseUntil, &responseHashValue, &usageJSON, &traceRefValue, &startedAt,
		&endedAtValue, &errorCodeValue, &tokenHash, new(sql.NullString),
		&runStatus, &stepStatus, &taskStatus, &currentAttemptValue, &projectID,
	); errors.Is(err, sql.ErrNoRows) {
		return ExecutionAttempt{}, domainError("EXECUTION_ATTEMPT_NOT_FOUND", "执行尝试不存在。")
	} else if err != nil {
		return ExecutionAttempt{}, err
	}
	attempt.ResponseHash = stringPointer(responseHashValue)
	attempt.Usage = json.RawMessage(usageJSON)
	attempt.TraceRef = stringPointer(traceRefValue)
	attempt.ErrorCode = stringPointer(errorCodeValue)
	attempt.LeaseUntil, err = parseTime(leaseUntil)
	if err != nil {
		return ExecutionAttempt{}, err
	}
	attempt.StartedAt, err = parseTime(startedAt)
	if err != nil {
		return ExecutionAttempt{}, err
	}
	attempt.EndedAt, err = optionalTime(endedAtValue)
	if err != nil {
		return ExecutionAttempt{}, err
	}
	if subtle.ConstantTimeCompare([]byte(tokenHash), []byte(sha256Hex([]byte(command.AttemptToken)))) != 1 {
		return ExecutionAttempt{}, domainError("ATTEMPT_TOKEN_INVALID", "执行尝试 Token 无效。")
	}
	if attempt.InputSnapshotHash != command.InputSnapshotHash {
		return ExecutionAttempt{}, domainError("ATTEMPT_INPUT_CHANGED", "执行结果对应的输入快照已变化。")
	}
	if attempt.Status == "result_received" {
		if attempt.ResponseHash != nil && *attempt.ResponseHash == responseHash {
			return scanExecutionAttempt(tx.QueryRowContext(ctx, executionAttemptSelect+`
				WHERE attempt_id = ?`, command.AttemptID))
		}
		return ExecutionAttempt{}, domainError("ATTEMPT_RESULT_CONFLICT", "该执行尝试已经提交过不同结果。")
	}
	if attempt.Status != "running" || !currentAttemptValue.Valid || currentAttemptValue.String != attempt.AttemptID ||
		!runAcceptsActiveAttemptResult(runStatus) || stepStatus != "running" || taskStatus != "running" {
		return ExecutionAttempt{}, domainError("ATTEMPT_STALE", "执行结果已过期，不能提交。")
	}
	if !attempt.LeaseUntil.After(now) {
		return ExecutionAttempt{}, domainError("ATTEMPT_LEASE_EXPIRED", "执行尝试 Lease 已过期。")
	}
	if err := validateAgentMemoryCommitTx(ctx, tx, "stateful_workflow", attempt.AttemptID); err != nil {
		return ExecutionAttempt{}, err
	}
	if err := validateExternalToolOutcomesTx(ctx, tx, "stateful_workflow", attempt.AttemptID); err != nil {
		return ExecutionAttempt{}, err
	}
	traceRef := nullableStringPointer(command.TraceRef)
	if err := validateExecutionRepairUsageTx(ctx, tx, attempt, command.Usage); err != nil {
		return ExecutionAttempt{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_result_rejections SET status = 'closed', updated_at = ? WHERE attempt_id = ? AND status = 'claimed'`, formatTime(now), command.AttemptID); err != nil {
		return ExecutionAttempt{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE execution_attempts
		SET status = 'result_received', response_hash = ?, response_payload_json = ?,
			usage_json = ?, trace_ref = ?, ended_at = ?
		WHERE attempt_id = ? AND status = 'running'`,
		responseHash, string(command.ResponsePayload), string(command.Usage), traceRef,
		formatTime(now), command.AttemptID,
	); err != nil {
		return ExecutionAttempt{}, err
	}
	runRef, stepRef := attempt.RunID, attempt.StepRunID
	if _, err := s.appendEvent(ctx, tx, projectID, &runRef, &stepRef, "task.progressed", "task_item", attempt.TaskItemID, map[string]any{
		"attempt_id": attempt.AttemptID,
		"phase":      "result_received",
	}); err != nil {
		return ExecutionAttempt{}, err
	}
	received, err := scanExecutionAttempt(tx.QueryRowContext(ctx, executionAttemptSelect+`
		WHERE attempt_id = ?`, command.AttemptID))
	if err != nil {
		return ExecutionAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExecutionAttempt{}, err
	}
	return received, nil
}

func runAcceptsActiveAttemptResult(status string) bool {
	return status == "running" || status == "pausing"
}

func (s *Store) requeueExpiredAttempts(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT ea.attempt_id, ea.task_item_id, ea.run_id, ea.step_run_id, r.project_id
		FROM execution_attempts ea
		JOIN task_items ti ON ti.current_attempt_id = ea.attempt_id
		JOIN runs r ON r.run_id = ea.run_id
		JOIN step_runs sr ON sr.step_run_id = ea.step_run_id
		WHERE ea.status = 'running' AND ea.lease_until <= ?
			AND ti.status = 'running' AND sr.status = 'running' AND (r.status = 'running' OR (r.status = 'pausing' AND EXISTS (
				SELECT 1 FROM execution_result_rejections rejected WHERE rejected.attempt_id = ea.attempt_id AND rejected.status = 'claimed')))`,
		formatTime(now),
	)
	if err != nil {
		return err
	}
	type expiredAttempt struct {
		attemptID string
		taskID    string
		runID     string
		stepRunID string
		projectID string
	}
	var expired []expiredAttempt
	for rows.Next() {
		var item expiredAttempt
		if err := rows.Scan(
			&item.attemptID,
			&item.taskID,
			&item.runID,
			&item.stepRunID,
			&item.projectID,
		); err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range expired {
		if repaired, err := s.requeueExpiredOutputRepairTx(ctx, tx, item.attemptID, item.taskID, now); err != nil {
			return err
		} else if repaired {
			continue
		}
		replayRisk, err := executionToolReplayRiskTx(ctx, tx, item.attemptID)
		if err != nil {
			return err
		}
		if err := cancelExecutionToolsTx(ctx, tx, item.runID, item.attemptID, now); err != nil {
			return err
		}
		if replayRisk {
			if _, err := tx.ExecContext(ctx, `UPDATE execution_attempts SET status = 'expired', ended_at = ?, error_code = 'SDK_TOOL_REPLAY_RISK' WHERE attempt_id = ? AND status = 'running'`, formatTime(now), item.attemptID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE task_items SET status = 'failed', current_attempt_id = NULL, failure = 'SDK_TOOL_REPLAY_RISK', ended_at = ?, updated_at = ? WHERE task_item_id = ? AND current_attempt_id = ?`, formatTime(now), formatTime(now), item.taskID, item.attemptID); err != nil {
				return err
			}
			if err := s.failStepRunAndProjectTx(ctx, tx, item.projectID, item.runID, item.stepRunID, "SDK_TOOL_REPLAY_RISK", now); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE execution_attempts
			SET status = 'expired', ended_at = ?, error_code = 'ATTEMPT_LEASE_EXPIRED'
			WHERE attempt_id = ? AND status = 'running'`,
			formatTime(now), item.attemptID,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE task_items
			SET status = 'pending', current_attempt_id = NULL, updated_at = ?
			WHERE task_item_id = ? AND current_attempt_id = ?`,
			formatTime(now), item.taskID, item.attemptID,
		); err != nil {
			return err
		}
		runRef, stepRef := item.runID, item.stepRunID
		if _, err := s.appendEvent(ctx, tx, item.projectID, &runRef, &stepRef, "task.retry_scheduled", "task_item", item.taskID, map[string]any{
			"expired_attempt_id": item.attemptID,
			"reason":             "ATTEMPT_LEASE_EXPIRED",
		}); err != nil {
			return err
		}
	}
	return nil
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func nullableStringPointer(value string) any {
	if value == "" {
		return nil
	}
	return value
}

const taskItemSelect = `
	SELECT task_item_id, step_run_id, run_id, item_key, item_order, status,
		attempt_count, input_snapshot_json, cursor_json, current_attempt_id,
		output_artifact_version_id, started_at, ended_at, failure, created_at, updated_at,
		COALESCE((SELECT status FROM execution_program_progress WHERE attempt_id=task_items.current_attempt_id), '')
	FROM task_items`

func scanTaskItem(row rowScanner) (TaskItem, error) {
	var task TaskItem
	var inputJSON, cursorJSON, createdAt, updatedAt string
	var currentAttemptID, outputVersionID, startedAt, endedAt, failure sql.NullString
	if err := row.Scan(
		&task.TaskItemID, &task.StepRunID, &task.RunID, &task.ItemKey, &task.ItemOrder,
		&task.Status, &task.AttemptCount, &inputJSON, &cursorJSON, &currentAttemptID,
		&outputVersionID, &startedAt, &endedAt, &failure, &createdAt, &updatedAt, &task.ProgramStatus,
	); err != nil {
		return TaskItem{}, err
	}
	task.InputSnapshot = json.RawMessage(inputJSON)
	task.Cursor = json.RawMessage(cursorJSON)
	task.CurrentAttemptID = stringPointer(currentAttemptID)
	task.OutputArtifactVersionID = stringPointer(outputVersionID)
	task.Failure = stringPointer(failure)
	var err error
	task.StartedAt, err = optionalTime(startedAt)
	if err != nil {
		return TaskItem{}, err
	}
	task.EndedAt, err = optionalTime(endedAt)
	if err != nil {
		return TaskItem{}, err
	}
	task.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return TaskItem{}, err
	}
	task.UpdatedAt, err = parseTime(updatedAt)
	return task, err
}

const executionAttemptSelect = `
	SELECT attempt_id, run_id, step_run_id, task_item_id, attempt_no, executor_id,
		provider_id, worker_id, request_fingerprint, input_snapshot_hash, status,
		lease_until, response_hash, usage_json, trace_ref, started_at, ended_at, error_code,
		COALESCE((SELECT status FROM execution_program_progress WHERE attempt_id=execution_attempts.attempt_id), '')
	FROM execution_attempts`

func scanExecutionAttempt(row rowScanner) (ExecutionAttempt, error) {
	var attempt ExecutionAttempt
	var leaseUntil, usageJSON, startedAt string
	var responseHash, traceRef, endedAt, errorCode sql.NullString
	if err := row.Scan(
		&attempt.AttemptID, &attempt.RunID, &attempt.StepRunID, &attempt.TaskItemID,
		&attempt.AttemptNo, &attempt.ExecutorID, &attempt.ProviderID, &attempt.WorkerID,
		&attempt.RequestFingerprint, &attempt.InputSnapshotHash, &attempt.Status,
		&leaseUntil, &responseHash, &usageJSON, &traceRef, &startedAt, &endedAt, &errorCode, &attempt.ProgramStatus,
	); err != nil {
		return ExecutionAttempt{}, err
	}
	attempt.ResponseHash = stringPointer(responseHash)
	attempt.Usage = json.RawMessage(usageJSON)
	attempt.TraceRef = stringPointer(traceRef)
	attempt.ErrorCode = stringPointer(errorCode)
	var err error
	attempt.LeaseUntil, err = parseTime(leaseUntil)
	if err != nil {
		return ExecutionAttempt{}, err
	}
	attempt.StartedAt, err = parseTime(startedAt)
	if err != nil {
		return ExecutionAttempt{}, err
	}
	attempt.EndedAt, err = optionalTime(endedAt)
	return attempt, err
}
