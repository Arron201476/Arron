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

const maxExecutionRepairClaims = 3

func (s *Store) listTaskOutputRepairs(ctx context.Context, stepRunID string) (map[string]TaskOutputRepair, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ti.task_item_id, r.status, r.rejection_no, r.error_code
		FROM task_items ti JOIN execution_result_rejections r ON r.attempt_id=ti.current_attempt_id
		WHERE ti.step_run_id=? AND r.rejection_no=(SELECT MAX(latest.rejection_no) FROM execution_result_rejections latest WHERE latest.attempt_id=r.attempt_id)`, stepRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]TaskOutputRepair)
	for rows.Next() {
		var id string
		var repair TaskOutputRepair
		if err := rows.Scan(&id, &repair.Status, &repair.RejectionNo, &repair.ErrorCode); err != nil {
			return nil, err
		}
		result[id] = repair
	}
	return result, rows.Err()
}

func outputRepairErrorCode(err error) string {
	for _, code := range []string{"OUTPUT_SCHEMA_VALIDATION_FAILED", "BATCH_COVERAGE_INVALID", "OUTPUT_LENGTH_INSUFFICIENT"} {
		if isDomainErrorCode(err, code) {
			return code
		}
	}
	return ""
}

func (s *Store) CommitExecutionResult(ctx context.Context, command CommitExecutionResultCommand) (ArtifactCommitResult, error) {
	if command.AttemptID == "" || command.ExpectedResponseHash == "" {
		return ArtifactCommitResult{}, domainError("REQUEST_VALIDATION_FAILED", "正式提交缺少执行尝试或响应摘要。")
	}
	if command.RepairOutput {
		cached, err := s.executionRepairCommitReceipt(ctx, command)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		if cached != nil {
			return *cached, nil
		}
	}
	result, err := s.commitExecutionResult(ctx, command)
	if err == nil || !command.RepairOutput || outputRepairErrorCode(err) == "" {
		return result, err
	}
	// Validation has rolled back. A second transaction conditionally records the
	// exact rejected receipt; a crash between the two leaves it available to recommit.
	return s.queueExecutionResultRepair(ctx, command, err)
}

func validateOutputRepairOwnerTx(ctx context.Context, tx *sql.Tx, command CommitExecutionResultCommand) (executionToolState, error) {
	state, err := loadExecutionToolStateTx(ctx, tx, command.AttemptID)
	if err != nil {
		return state, err
	}
	if err := validateExecutionToolToken(state, command.AttemptToken); err != nil {
		return state, err
	}
	if activity, ok := AgentActivityFromContext(ctx); ok && (activity.ProjectID != state.ProjectID || activity.ExecutionAttemptID != state.AttemptID ||
		activity.AgentTurnID != "" || activity.AgentTaskAttemptID != "" || activity.AttemptToken != command.AttemptToken) {
		return state, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "提交请求不属于当前状态化执行。")
	}
	if err := validateExecutionOwnerTx(ctx, tx, state); err != nil {
		return state, err
	}
	var provider, executor string
	if err := tx.QueryRowContext(ctx, `SELECT provider_id, executor_id FROM execution_attempts WHERE attempt_id = ?`, command.AttemptID).Scan(&provider, &executor); err != nil {
		return state, err
	}
	if provider != "openai_agents_sdk" || executor == "workflow.video_script_extract" {
		return state, domainError("REQUEST_VALIDATION_FAILED", "当前执行器不支持 SDK 输出修复。")
	}
	if state.ProjectDeleted {
		return state, domainError("ATTEMPT_STALE", "执行所属作品已删除。")
	}
	return state, nil
}

func (s *Store) executionRepairCommitReceipt(ctx context.Context, command CommitExecutionResultCommand) (*ArtifactCommitResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	state, err := validateOutputRepairOwnerTx(ctx, tx, command)
	if err != nil {
		return nil, err
	}
	return s.executionRepairCommitReceiptTx(ctx, tx, command, state)
}

func (s *Store) executionRepairCommitReceiptTx(ctx context.Context, tx *sql.Tx, command CommitExecutionResultCommand, state executionToolState) (*ArtifactCommitResult, error) {
	if state.AttemptStatus != "repair_pending" && state.AttemptStatus != "failed" {
		return nil, nil
	}
	var hash, status string
	err := tx.QueryRowContext(ctx, `SELECT response_hash, status FROM execution_result_rejections WHERE attempt_id = ? ORDER BY rejection_no DESC LIMIT 1`, command.AttemptID).Scan(&hash, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if hash != command.ExpectedResponseHash {
		return nil, domainError("ATTEMPT_RESULT_CHANGED", "执行结果摘要不匹配。")
	}
	commitStatus := "output_repair_failed"
	if state.AttemptStatus == "repair_pending" && status == "queued" {
		if state.CurrentAttemptID != state.AttemptID || state.TaskStatus != "repair_pending" ||
			!slices.Contains([]string{"running", "paused"}, state.StepStatus) ||
			!slices.Contains([]string{"running", "pausing", "paused"}, state.RunStatus) {
			return nil, domainError("ATTEMPT_STALE", "输出修复已失效。")
		}
		commitStatus = "output_repair_queued"
	} else if state.AttemptStatus != "failed" || status != "terminal" {
		return nil, nil
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, state.RunID)
	if err != nil {
		return nil, err
	}
	return &ArtifactCommitResult{CommitStatus: commitStatus, RunSnapshot: snapshot}, nil
}

func (s *Store) queueExecutionResultRepair(ctx context.Context, command CommitExecutionResultCommand, rejection error) (ArtifactCommitResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	defer tx.Rollback()
	state, err := validateOutputRepairOwnerTx(ctx, tx, command)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if cached, err := s.executionRepairCommitReceiptTx(ctx, tx, command, state); err != nil {
		return ArtifactCommitResult{}, err
	} else if cached != nil {
		return *cached, nil
	}
	if state.AttemptStatus != "result_received" || state.CurrentAttemptID != state.AttemptID || state.TaskStatus != "running" ||
		state.StepStatus != "running" || !runAcceptsActiveAttemptResult(state.RunStatus) {
		return ArtifactCommitResult{}, domainError("ATTEMPT_STALE", "当前结果不可进入输出修复。")
	}
	var payload, hash, usage, trace, taskID string
	if err := tx.QueryRowContext(ctx, `SELECT response_payload_json, response_hash, usage_json, COALESCE(trace_ref, ''), task_item_id
		FROM execution_attempts WHERE attempt_id = ?`, state.AttemptID).Scan(&payload, &hash, &usage, &trace, &taskID); err != nil {
		return ArtifactCommitResult{}, err
	}
	if hash != command.ExpectedResponseHash || sha256Hex([]byte(payload)) != hash {
		return ArtifactCommitResult{}, domainError("ATTEMPT_RESULT_CHANGED", "被拒结果摘要不匹配。")
	}
	var number, unfinished int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) + 1 FROM execution_result_rejections WHERE attempt_id = ?`, state.AttemptID).Scan(&number); err != nil {
		return ArtifactCommitResult{}, err
	}
	if number > 2 {
		return ArtifactCommitResult{}, domainError("ATTEMPT_RESULT_CONFLICT", "输出修复次数已耗尽。")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_tool_calls b JOIN agent_tool_calls c ON c.agent_tool_call_id = b.agent_tool_call_id
		WHERE b.execution_attempt_id = ? AND c.status IN ('pending_approval','approved','running')`, state.AttemptID).Scan(&unfinished); err != nil {
		return ArtifactCommitResult{}, err
	}
	if unfinished != 0 {
		return ArtifactCommitResult{}, domainError("SDK_EXECUTION_CHECKPOINT_REQUIRED", "仍有未结束的工具，不能进入无工具输出修复。")
	}
	now := s.now()
	status, commitStatus := "queued", "output_repair_queued"
	if number == 2 {
		status, commitStatus = "terminal", "output_repair_failed"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO execution_result_rejections(attempt_id, rejection_no, response_hash, response_payload_json,
		usage_json, usage_hash, trace_ref, error_code, error_detail, status, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		state.AttemptID, number, hash, payload, usage, sha256Hex([]byte(usage)), trace, outputRepairErrorCode(rejection), boundedExecutionRepairDetail(rejection.Error()), status, formatTime(now), formatTime(now)); err != nil {
		return ArtifactCommitResult{}, err
	}
	if number == 2 {
		if err := s.failExecutionOutputRepairTx(ctx, tx, state, taskID, "候选修复后仍未通过正式校验，已停止，未从头重新生成。", now); err != nil {
			return ArtifactCommitResult{}, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE execution_attempts SET status = 'repair_pending' WHERE attempt_id = ? AND status = 'result_received'`, state.AttemptID); err != nil {
			return ArtifactCommitResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task_items SET status = 'repair_pending', updated_at = ? WHERE task_item_id = ? AND current_attempt_id = ?`, formatTime(now), taskID, state.AttemptID); err != nil {
			return ArtifactCommitResult{}, err
		}
		if _, err := s.appendEvent(ctx, tx, state.ProjectID, &state.RunID, nil, "task.progressed", "task_item", taskID,
			map[string]any{"attempt_id": state.AttemptID, "phase": "output_repair_queued", "error_code": outputRepairErrorCode(rejection)}); err != nil {
			return ArtifactCommitResult{}, err
		}
		if err := s.pauseAfterCompletedTaskIfRequestedTx(ctx, tx, state.RunID, now, "output_repair_queued"); err != nil {
			return ArtifactCommitResult{}, err
		}
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, state.RunID)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ArtifactCommitResult{}, err
	}
	return ArtifactCommitResult{CommitStatus: commitStatus, RunSnapshot: snapshot}, nil
}

func boundedExecutionRepairDetail(value string) string {
	runes := []rune(value)
	if len(runes) > 4000 {
		runes = runes[:4000]
	}
	return string(runes)
}

func (s *Store) failExecutionOutputRepairTx(ctx context.Context, tx *sql.Tx, state executionToolState, taskID, summary string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE execution_result_rejections SET status='terminal',updated_at=? WHERE attempt_id=? AND status IN ('queued','claimed')`, formatTime(now), state.AttemptID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_attempts SET status = 'failed', ended_at = ?, error_code = 'OUTPUT_REPAIR_FAILED' WHERE attempt_id = ?`, formatTime(now), state.AttemptID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_items SET status = 'failed', failure = 'OUTPUT_REPAIR_FAILED', ended_at = ?, updated_at = ? WHERE task_item_id = ? AND current_attempt_id = ?`, formatTime(now), formatTime(now), taskID, state.AttemptID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO execution_failure_details(attempt_id, task_item_id, error_code, stage, summary, retryable, created_at)
		VALUES(?,?,'OUTPUT_REPAIR_FAILED','output_validation',?,0,?) ON CONFLICT(attempt_id) DO UPDATE SET
		error_code=excluded.error_code, stage=excluded.stage, summary=excluded.summary, retryable=0`, state.AttemptID, taskID, summary, formatTime(now)); err != nil {
		return err
	}
	var stepID, stepRunID string
	if err := tx.QueryRowContext(ctx, `SELECT sr.step_id, sr.step_run_id FROM execution_attempts ea JOIN step_runs sr ON sr.step_run_id = ea.step_run_id WHERE ea.attempt_id = ?`, state.AttemptID).Scan(&stepID, &stepRunID); err != nil {
		return err
	}
	if _, err := s.appendEvent(ctx, tx, state.ProjectID, &state.RunID, &stepRunID, "task.failed", "task_item", taskID,
		map[string]any{"attempt_id": state.AttemptID, "error_code": "OUTPUT_REPAIR_FAILED", "retryable": false}); err != nil {
		return err
	}
	var capabilityID string
	if err := tx.QueryRowContext(ctx, `SELECT capability_id FROM runs WHERE run_id = ?`, state.RunID).Scan(&capabilityID); err != nil {
		return err
	}
	entry, ok, err := s.capabilityEntryForRunQuery(ctx, tx, state.RunID, capabilityID)
	if err != nil {
		return err
	}
	if ok && entry.Definition != nil {
		step := compiledStep(entry.Definition.Steps, stepID)
		if step != nil && step.Batch != nil && step.Batch.FailurePolicy == "preserve_success_retry_failed" {
			_, err := s.finalizePreservedBatchFailureIfSettledTx(ctx, tx, state.ProjectID, state.RunID, stepRunID, "OUTPUT_REPAIR_FAILED", now)
			return err
		}
	}
	return s.failStepRunAndProjectTx(ctx, tx, state.ProjectID, state.RunID, stepRunID, "OUTPUT_REPAIR_FAILED", now)
}

func loadExecutionResultRepairTx(ctx context.Context, tx *sql.Tx, attempt ExecutionAttempt, status string) (*ExecutionResultRepair, error) {
	var repair ExecutionResultRepair
	var usage, usageHash string
	var claims int
	err := tx.QueryRowContext(ctx, `SELECT rejection_no, response_hash, response_payload_json, usage_json, usage_hash, trace_ref, error_code, error_detail, claim_count
		FROM execution_result_rejections WHERE attempt_id = ? AND status = ? ORDER BY rejection_no DESC LIMIT 1`, attempt.AttemptID, status).
		Scan(&repair.RejectionNo, &repair.ResponseHash, &repair.Candidate, &usage, &usageHash, &repair.TraceRef, &repair.ErrorCode, &repair.ErrorDetail, &claims)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if repair.RejectionNo != 1 || len(repair.Candidate) > maxProviderResponseBytes || !json.Valid([]byte(repair.Candidate)) || sha256Hex([]byte(repair.Candidate)) != repair.ResponseHash ||
		claims < 0 || claims > maxExecutionRepairClaims || (status == "queued" && claims >= maxExecutionRepairClaims) || (status == "claimed" && claims == 0) ||
		!jsonObject([]byte(usage)) || sha256Hex([]byte(usage)) != usageHash || !slices.Contains([]string{"OUTPUT_SCHEMA_VALIDATION_FAILED", "BATCH_COVERAGE_INVALID", "OUTPUT_LENGTH_INSUFFICIENT"}, repair.ErrorCode) {
		return nil, domainError("AGENT_RUN_STATE_CORRUPT", "持久候选修复记录校验失败。")
	}
	if _, err := executionRepairUsageCounters(json.RawMessage(usage)); err != nil {
		return nil, err
	}
	repair.SchemaVersion, repair.InputSnapshotHash, repair.Usage = "execution_result_repair.v1", attempt.InputSnapshotHash, json.RawMessage(usage)
	return &repair, nil
}

func (s *Store) executionClaimAvailableTx(ctx context.Context, tx *sql.Tx, claim *TaskClaim, command ClaimExecutionTaskCommand) (bool, error) {
	modelID := ""
	if claim.ContextPack.Budget.ModelID != nil {
		modelID = *claim.ContextPack.Budget.ModelID
	}
	if modelID != command.ModelID || claim.Attempt.ProviderID != command.ProviderID || !slices.Contains(command.ExecutorIDs, claim.ExecutorID) {
		return false, nil
	}
	entry, available, err := s.capabilityEntryForRunQuery(ctx, tx, claim.Attempt.RunID, claim.CapabilityID)
	return available && entry.Status == capability.Available && entry.Definition != nil && entry.Definition.Version == claim.CapabilityVersion, err
}

func (s *Store) claimExecutionResultTx(ctx context.Context, tx *sql.Tx, command ClaimExecutionTaskCommand, now time.Time) (*TaskClaim, error) {
	if command.ProviderID != "openai_agents_sdk" {
		return nil, nil
	}
	executors, err := json.Marshal(command.ExecutorIDs)
	if err != nil {
		return nil, err
	}
	lastID, lastStarted := "", ""
	for {
		rows, err := tx.QueryContext(ctx, `SELECT ea.attempt_id, ea.started_at FROM execution_attempts ea
			JOIN task_items ti ON ti.current_attempt_id=ea.attempt_id AND ti.task_item_id=ea.task_item_id
			JOIN step_runs sr ON sr.step_run_id=ea.step_run_id JOIN runs r ON r.run_id=ea.run_id JOIN projects p ON p.project_id=r.project_id
			WHERE ((ea.status='repair_pending' AND ti.status='repair_pending') OR (ea.status='result_received' AND ti.status IN ('running','paused') AND (ea.lease_until<=? OR ti.status='paused')))
			AND r.status='running' AND sr.status='running' AND p.deleted_at IS NULL AND ea.provider_id=?
			AND ea.executor_id IN (SELECT value FROM json_each(?)) AND (ea.started_at,ea.attempt_id)>(?,?)
			ORDER BY ea.started_at, ea.attempt_id LIMIT 100`, formatTime(now), command.ProviderID, string(executors), lastStarted, lastID)
		if err != nil {
			return nil, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id, &lastStarted); err != nil {
				rows.Close()
				return nil, err
			}
			ids, lastID = append(ids, id), id
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, nil
		}
		for _, id := range ids {
			attempt, err := scanExecutionAttempt(tx.QueryRowContext(ctx, executionAttemptSelect+` WHERE attempt_id=?`, id))
			if err != nil {
				return nil, err
			}
			state, err := loadExecutionToolStateTx(ctx, tx, id)
			if err != nil {
				return nil, err
			}
			// A preceding failure in this page can have terminated the same Run.
			if state.ProjectDeleted || state.CurrentAttemptID != id || state.RunStatus != "running" || state.StepStatus != "running" ||
				!((state.AttemptStatus == "repair_pending" && state.TaskStatus == "repair_pending") ||
					(state.AttemptStatus == "result_received" && slices.Contains([]string{"running", "paused"}, state.TaskStatus))) {
				continue
			}
			if err := validateExecutionOwnerTx(ctx, tx, state); err != nil {
				if isDomainErrorCode(err, "WORKSPACE_ACCESS_DENIED") || isDomainErrorCode(err, "ROLE_FORBIDDEN") {
					continue
				}
				return nil, err
			}
			claim, err := loadFrozenExecutionClaimTx(ctx, tx, attempt, state)
			if err != nil {
				if isDomainErrorCode(err, "AGENT_RUN_STATE_CORRUPT") {
					if err := s.failExecutionOutputRepairTx(ctx, tx, state, attempt.TaskItemID, "冻结输入缺失或损坏，已停止结果恢复。", now); err != nil {
						return nil, err
					}
					continue
				}
				return nil, err
			}
			available, err := s.executionClaimAvailableTx(ctx, tx, claim, command)
			if err != nil {
				return nil, err
			}
			if !available {
				continue
			}
			target := attempt.Status
			if target == "repair_pending" {
				claim.Repair, err = loadExecutionResultRepairTx(ctx, tx, attempt, "queued")
				if err != nil || claim.Repair == nil {
					if err != nil && !isDomainErrorCode(err, "AGENT_RUN_STATE_CORRUPT") {
						return nil, err
					}
					if err := s.failExecutionOutputRepairTx(ctx, tx, state, attempt.TaskItemID, "持久候选修复记录缺失或损坏，已停止执行。", now); err != nil {
						return nil, err
					}
					continue
				}
				target = "running"
				if _, err := tx.ExecContext(ctx, `UPDATE execution_result_rejections SET status='claimed', claim_count=claim_count+1, updated_at=? WHERE attempt_id=? AND status='queued'`, formatTime(now), id); err != nil {
					return nil, err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM execution_run_states WHERE attempt_id=?`, id); err != nil {
					return nil, err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE execution_attempts SET response_hash=NULL, response_payload_json=NULL, ended_at=NULL WHERE attempt_id=?`, id); err != nil {
					return nil, err
				}
				claim.Attempt.ResponseHash, claim.Attempt.EndedAt = nil, nil
			} else {
				var payload string
				if err := tx.QueryRowContext(ctx, `SELECT COALESCE(response_payload_json,'') FROM execution_attempts WHERE attempt_id=?`, id).Scan(&payload); err != nil {
					return nil, err
				}
				if attempt.ResponseHash == nil || len(payload) > maxProviderResponseBytes || !json.Valid([]byte(payload)) || sha256Hex([]byte(payload)) != *attempt.ResponseHash {
					if err := s.failExecutionOutputRepairTx(ctx, tx, state, attempt.TaskItemID, "已接收结果缺失或损坏，已停止提交恢复。", now); err != nil {
						return nil, err
					}
					continue
				}
			}
			token, lease := s.newID("tok"), now.Add(time.Duration(command.LeaseSeconds)*time.Second)
			if _, err := tx.ExecContext(ctx, `UPDATE execution_attempts SET status=?,token_hash=?,worker_id=?,lease_until=? WHERE attempt_id=?`, target, sha256Hex([]byte(token)), command.WorkerID, formatTime(lease), id); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE task_items SET status='running',updated_at=? WHERE task_item_id=? AND current_attempt_id=?`, formatTime(now), attempt.TaskItemID, id); err != nil {
				return nil, err
			}
			claim.Attempt.Status, claim.Attempt.WorkerID, claim.Attempt.LeaseUntil = target, command.WorkerID, lease
			claim.AttemptToken, claim.Task.Status, claim.Task.UpdatedAt = token, "running", now
			phase := "result_commit_recovered"
			if claim.Repair != nil {
				phase = "output_repair_started"
			}
			if _, err := s.appendEvent(ctx, tx, state.ProjectID, &attempt.RunID, &attempt.StepRunID, "task.progressed", "task_item", attempt.TaskItemID, map[string]any{"attempt_id": id, "phase": phase}); err != nil {
				return nil, err
			}
			return claim, nil
		}
	}
}

func executionRepairUsageCounters(usage json.RawMessage) ([4]int64, error) {
	var counters [4]int64
	var fields map[string]json.RawMessage
	invalid := domainError("AGENT_RUN_STATE_CORRUPT", "输出修复的累计用量无效。")
	if json.Unmarshal(usage, &fields) != nil || fields == nil {
		return counters, invalid
	}
	for index, key := range []string{"requests", "input_tokens", "output_tokens", "total_tokens"} {
		if value, ok := fields[key]; ok {
			if string(value) == "null" || json.Unmarshal(value, &counters[index]) != nil || counters[index] < 0 {
				return counters, invalid
			}
		}
	}
	return counters, nil
}

func validateExecutionRepairUsageTx(ctx context.Context, tx *sql.Tx, attempt ExecutionAttempt, usage json.RawMessage) error {
	repair, err := loadExecutionResultRepairTx(ctx, tx, attempt, "claimed")
	if err != nil || repair == nil {
		return err
	}
	previous, err := executionRepairUsageCounters(repair.Usage)
	if err != nil {
		return err
	}
	next, err := executionRepairUsageCounters(usage)
	if err != nil {
		return domainError("REQUEST_VALIDATION_FAILED", "输出修复必须提交有效的累计用量。")
	}
	for index := range previous {
		if next[index] < previous[index] {
			return domainError("REQUEST_VALIDATION_FAILED", "输出修复不能减少已经记录的用量。")
		}
	}
	return nil
}

func (s *Store) requeueExpiredOutputRepairTx(ctx context.Context, tx *sql.Tx, attemptID, taskID string, now time.Time) (bool, error) {
	var claims int
	err := tx.QueryRowContext(ctx, `SELECT claim_count FROM execution_result_rejections WHERE attempt_id=? AND status='claimed'`, attemptID).Scan(&claims)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	state, err := loadExecutionToolStateTx(ctx, tx, attemptID)
	if err != nil {
		return false, err
	}
	if claims >= maxExecutionRepairClaims {
		if _, err := tx.ExecContext(ctx, `UPDATE execution_result_rejections SET status='terminal',updated_at=? WHERE attempt_id=? AND status='claimed'`, formatTime(now), attemptID); err != nil {
			return false, err
		}
		return true, s.failExecutionOutputRepairTx(ctx, tx, state, taskID, "输出修复执行多次失去租约，已停止，未重新生成主任务。", now)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_result_rejections SET status='queued',updated_at=? WHERE attempt_id=? AND status='claimed'`, formatTime(now), attemptID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_attempts SET status='repair_pending',token_hash=?,ended_at=? WHERE attempt_id=? AND status='running'`, sha256Hex([]byte(s.newID("tok"))), formatTime(now), attemptID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_items SET status='repair_pending',updated_at=? WHERE task_item_id=? AND current_attempt_id=?`, formatTime(now), taskID, attemptID); err != nil {
		return false, err
	}
	_, err = s.appendEvent(ctx, tx, state.ProjectID, &state.RunID, nil, "task.progressed", "task_item", taskID, map[string]any{"attempt_id": attemptID, "phase": "output_repair_lease_recovered"})
	return true, err
}
