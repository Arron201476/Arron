package runtime

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func (s *Store) PauseAgentTaskForApproval(ctx context.Context, command PauseAgentTaskForApprovalCommand) (AgentTask, error) {
	return s.checkpointAgentTaskPause(ctx, command, false)
}

func (s *Store) CompleteAgentTaskPause(ctx context.Context, command PauseAgentTaskForApprovalCommand) (AgentTask, error) {
	return s.checkpointAgentTaskPause(ctx, command, true)
}

func (s *Store) checkpointAgentTaskPause(ctx context.Context, command PauseAgentTaskForApprovalCommand, requestedPause bool) (AgentTask, error) {
	recovery, recoveryErr := validateModelRecoveryCheckpoint(command.PauseAgentTurnForApprovalCommand)
	if recoveryErr != nil {
		return AgentTask{}, recoveryErr
	}
	if recovery && !requestedPause {
		return AgentTask{}, domainError("REQUEST_VALIDATION_FAILED", "模型恢复必须使用暂停通道。")
	}
	if command.AgentTaskAttemptID == "" || command.AttemptToken == "" || command.SchemaVersion == "" || len(command.SchemaVersion) > 64 ||
		len(command.RunState) == 0 || len(command.RunState) > maxAgentTurnRunState || (!requestedPause && len(command.PendingSDKToolCallIDs) == 0) || len(command.PendingSDKToolCallIDs) > maxAgentTurnApprovals {
		return AgentTask{}, domainError("REQUEST_VALIDATION_FAILED", "后台任务审批 checkpoint 不完整。")
	}
	var envelope map[string]json.RawMessage
	var schema string
	if json.Unmarshal(command.RunState, &envelope) != nil || envelope == nil || json.Unmarshal(envelope["$schemaVersion"], &schema) != nil || schema != command.SchemaVersion {
		return AgentTask{}, domainError("AGENT_RUN_STATE_SCHEMA_MISMATCH", "后台任务 SDK RunState 版本不匹配。")
	}
	pendingIDs := []string{}
	if len(command.PendingSDKToolCallIDs) > 0 {
		var err error
		pendingIDs, err = normalizeSDKToolCallIDs(command.PendingSDKToolCallIDs)
		if err != nil {
			return AgentTask{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTask{}, err
	}
	defer tx.Rollback()
	state, err := loadAgentTaskAttemptStateTx(ctx, tx, command.AgentTaskAttemptID)
	if err != nil {
		return AgentTask{}, err
	}
	now := s.now()
	if (state.AttemptStatus == "waiting_approval" || state.AttemptStatus == "paused") && !state.CancelRequested && !state.ProjectDeleted {
		var hash string
		if err := tx.QueryRowContext(ctx, `SELECT state_hash FROM agent_task_run_states WHERE agent_task_attempt_id = ?`, state.AttemptID).Scan(&hash); err != nil {
			return AgentTask{}, err
		}
		if hash == sha256Hex(command.RunState) && subtle.ConstantTimeCompare([]byte(state.TokenHash), []byte(sha256Hex([]byte(command.AttemptToken)))) == 1 {
			return scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id = ?`, state.TaskID))
		}
	}
	if err := validateActiveAgentTaskAttempt(state, command.AttemptToken, now); err != nil {
		return AgentTask{}, err
	}
	if requestedPause && state.TaskStatus != "pausing" && !recovery {
		return AgentTask{}, domainError("AGENT_TASK_STATE_CONFLICT", "后台任务没有有效的用户暂停请求。")
	}
	if recovery {
		if err := validateRecoveryToolsTx(ctx, tx, "background_task", state.AttemptID, command.PauseAgentTurnForApprovalCommand); err != nil {
			return AgentTask{}, err
		}
	}
	unresolved := 0
	for _, sdkID := range pendingIDs {
		var callStatus, approvalStatus string
		err := tx.QueryRowContext(ctx, `
			SELECT c.status, a.status FROM agent_tool_calls c
			JOIN agent_task_tool_calls b ON b.agent_tool_call_id = c.agent_tool_call_id
			JOIN agent_tool_approvals a ON a.agent_tool_call_id = c.agent_tool_call_id
			WHERE b.agent_task_attempt_id = ? AND c.sdk_tool_call_id = ?`, state.AttemptID, sdkID).Scan(&callStatus, &approvalStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return AgentTask{}, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "审批未绑定到当前后台执行尝试。")
		}
		if err != nil {
			return AgentTask{}, err
		}
		if !((callStatus == "pending_approval" && approvalStatus == "pending") || (callStatus == "approved" && approvalStatus == "approved") || (callStatus == "rejected" && approvalStatus == "rejected")) {
			return AgentTask{}, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "后台工具审批状态不一致。")
		}
		if approvalStatus == "pending" {
			unresolved++
		}
	}
	var currentPending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_task_tool_calls b
		JOIN agent_tool_calls c ON c.agent_tool_call_id = b.agent_tool_call_id
		WHERE b.agent_task_attempt_id = ? AND c.status = 'pending_approval'`, state.AttemptID).Scan(&currentPending); err != nil {
		return AgentTask{}, err
	}
	if currentPending != unresolved {
		return AgentTask{}, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "后台待审批集合与 SDK 不一致。")
	}
	if err := includeAgentTaskInputsTx(ctx, tx, state.TaskID, state.AttemptID, command.IncludedInputIDs, now); err != nil {
		return AgentTask{}, err
	}
	pendingJSON, _ := json.Marshal(pendingIDs)
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_task_run_states(agent_task_id, agent_task_attempt_id,
		schema_version, state_json, state_hash, pending_sdk_tool_call_ids_json, checkpoint_version, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, 1, ?)
		ON CONFLICT(agent_task_id) DO UPDATE SET agent_task_attempt_id = excluded.agent_task_attempt_id,
		schema_version = excluded.schema_version, state_json = excluded.state_json, state_hash = excluded.state_hash,
		pending_sdk_tool_call_ids_json = excluded.pending_sdk_tool_call_ids_json,
		checkpoint_version = checkpoint_version + 1, updated_at = excluded.updated_at`,
		state.TaskID, state.AttemptID, agentTurnRunStateSchema+":"+schema, string(command.RunState), sha256Hex(command.RunState), string(pendingJSON), formatTime(now)); err != nil {
		return AgentTask{}, err
	}
	status, attemptStatus, message, event := "waiting_approval", "waiting_approval", "等待工具授权", "agent_task.waiting_approval"
	if unresolved == 0 {
		status = "queued"
	}
	if state.TaskStatus == "pausing" {
		status, attemptStatus, message, event = "paused", "paused", "已暂停", "agent_task.paused"
		var inputPause bool
		if err := tx.QueryRowContext(ctx, `SELECT input_pause_requested FROM agent_tasks WHERE agent_task_id=?`, state.TaskID).Scan(&inputPause); err != nil {
			return AgentTask{}, err
		}
		if inputPause && len(pendingIDs) == 0 {
			status, message, event = "queued", "等待将追加要求送入执行", "agent_task.queued"
		}
	}
	if recovery {
		code, recoveryMessage := recoveryDetails(command.RecoveryReason)
		status, attemptStatus, message, event = "paused", "paused", recoveryMessage, "agent_task.paused"
		if _, err := tx.ExecContext(ctx, `UPDATE agent_tasks SET failure_code=?,failure_message=? WHERE agent_task_id=?`, code, message, state.TaskID); err != nil {
			return AgentTask{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_task_attempts SET status = ? WHERE agent_task_attempt_id = ?`, attemptStatus, state.AttemptID); err != nil {
		return AgentTask{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_tasks SET status = ?, input_pause_requested=0, progress_message = ?, queued_at = ?, updated_at = ? WHERE agent_task_id = ?`, status, message, formatTime(now), formatTime(now), state.TaskID); err != nil {
		return AgentTask{}, err
	}
	if _, err := s.appendEvent(ctx, tx, state.ProjectID, nil, nil, event, "agent_task", state.TaskID,
		map[string]any{"attempt_id": state.AttemptID, "pending_approval_count": unresolved}); err != nil {
		return AgentTask{}, err
	}
	task, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id = ?`, state.TaskID))
	if err != nil {
		return AgentTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTask{}, err
	}
	return task, nil
}

func (s *Store) claimAgentTaskResumeTx(ctx context.Context, tx *sql.Tx, task AgentTask, command ClaimAgentTaskCommand, now time.Time) (*AgentTaskClaim, error) {
	corrupt := domainError("AGENT_RUN_STATE_CORRUPT", "后台任务恢复状态缺失或校验失败，不能从头重新执行。")
	var attemptID, stateJSON, pendingJSON, stateHash, storedSchema string
	var checkpointVersion int
	err := tx.QueryRowContext(ctx, `SELECT agent_task_attempt_id, state_json, pending_sdk_tool_call_ids_json, state_hash, schema_version, checkpoint_version
		FROM agent_task_run_states WHERE agent_task_id = ?`, task.AgentTaskID).Scan(&attemptID, &stateJSON, &pendingJSON, &stateHash, &storedSchema, &checkpointVersion)
	if errors.Is(err, sql.ErrNoRows) {
		var paused bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_task_attempts
			WHERE agent_task_id=? AND status IN ('waiting_approval','paused'))`, task.AgentTaskID).Scan(&paused); err != nil {
			return nil, err
		}
		if paused {
			return nil, corrupt
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	var sdkSchema string
	if len(stateJSON) > maxAgentTurnRunState || checkpointVersion < 1 || sha256Hex([]byte(stateJSON)) != stateHash ||
		json.Unmarshal([]byte(stateJSON), &envelope) != nil || envelope == nil || json.Unmarshal(envelope["$schemaVersion"], &sdkSchema) != nil ||
		sdkSchema == "" || storedSchema != agentTurnRunStateSchema+":"+sdkSchema {
		return nil, corrupt
	}
	savedAttempt, err := scanAgentTaskAttempt(tx.QueryRowContext(ctx, agentTaskAttemptSelect+` WHERE agent_task_attempt_id=? AND agent_task_id=?`, attemptID, task.AgentTaskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, corrupt
	}
	if err != nil {
		return nil, err
	}
	if savedAttempt.AttemptNo != task.AttemptCount || (savedAttempt.Status != "waiting_approval" && savedAttempt.Status != "paused") {
		return nil, corrupt
	}
	var pendingIDs []string
	if json.Unmarshal([]byte(pendingJSON), &pendingIDs) != nil || pendingIDs == nil || len(pendingIDs) > maxAgentTurnApprovals || (len(pendingIDs) == 0 && savedAttempt.Status != "paused") {
		return nil, corrupt
	}
	if _, err := normalizeSDKToolCallIDs(pendingIDs); err != nil {
		return nil, corrupt
	}
	decisions := make([]agentcontract.AgentToolApprovalDecision, 0, len(pendingIDs))
	for _, id := range pendingIDs {
		var status, callStatus string
		if err := tx.QueryRowContext(ctx, `SELECT a.status, c.status FROM agent_tool_approvals a
			JOIN agent_tool_calls c ON c.agent_tool_call_id = a.agent_tool_call_id
			JOIN agent_task_tool_calls b ON b.agent_tool_call_id = c.agent_tool_call_id
			WHERE b.agent_task_attempt_id = ? AND c.sdk_tool_call_id = ?`, attemptID, id).Scan(&status, &callStatus); errors.Is(err, sql.ErrNoRows) {
			return nil, corrupt
		} else if err != nil {
			return nil, err
		}
		if (status != "approved" && status != "rejected") || callStatus != status {
			return nil, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "后台任务仍有未处理的审批。")
		}
		action := "approve"
		if status == "rejected" {
			action = "reject"
		}
		decisions = append(decisions, agentcontract.AgentToolApprovalDecision{SDKToolCallID: id, Action: action})
	}
	var unlistedPending bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_task_tool_calls b
		JOIN agent_tool_calls c ON c.agent_tool_call_id=b.agent_tool_call_id
		WHERE b.agent_task_attempt_id=? AND c.status='pending_approval')`, attemptID).Scan(&unlistedPending); err != nil {
		return nil, err
	}
	if unlistedPending {
		return nil, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "后台任务仍有未处理的审批。")
	}
	token := s.newID("agtok")
	result, err := tx.ExecContext(ctx, `UPDATE agent_task_attempts SET status = 'running', token_hash = ?,
		worker_id = ?, provider_id = ?, model_id = ?, lease_until = ?
		WHERE agent_task_attempt_id = ? AND agent_task_id = ? AND status IN ('waiting_approval','paused')`, sha256Hex([]byte(token)), command.WorkerID, command.ProviderID, command.ModelID,
		formatTime(now.Add(time.Duration(command.LeaseSeconds)*time.Second)), attemptID, task.AgentTaskID)
	if err != nil {
		return nil, err
	}
	if err := requireOneRow(result, "AGENT_TASK_CLAIM_CONFLICT", "后台任务审批恢复状态已变化。"); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_tasks SET status = 'running', failure_code=NULL,failure_message=NULL, progress_message = '继续执行已保存的任务', updated_at = ? WHERE agent_task_id = ?`, formatTime(now), task.AgentTaskID); err != nil {
		return nil, err
	}
	attempt, err := scanAgentTaskAttempt(tx.QueryRowContext(ctx, agentTaskAttemptSelect+` WHERE agent_task_attempt_id = ?`, attemptID))
	if err != nil {
		return nil, err
	}
	task.Status, task.UpdatedAt, task.ProgressMessage = "running", now, "继续执行已保存的任务"
	task.FailureCode, task.FailureMessage = nil, nil
	if _, err := s.appendEvent(ctx, tx, task.ProjectID, nil, nil, "agent_task.resumed", "agent_task", task.AgentTaskID, map[string]any{"attempt_id": attemptID}); err != nil {
		return nil, err
	}
	return &AgentTaskClaim{Task: task, Attempt: attempt, AttemptToken: token, Resume: &AgentTurnResumeContext{RunState: json.RawMessage(stateJSON), ApprovalDecisions: decisions}}, nil
}

func (s *Store) failAgentTaskResumeTx(ctx context.Context, tx *sql.Tx, task AgentTask, now time.Time) error {
	const code = "AGENT_RUN_STATE_CORRUPT"
	const message = "后台任务恢复状态缺失或不一致，已停止恢复；请核对已有结果，不能直接从头重新执行。"
	if err := cancelAgentTaskToolsTx(ctx, tx, task.AgentTaskID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_task_attempts SET status='failed', error_code=?, error_message=?, ended_at=?
		WHERE agent_task_id=? AND status IN ('waiting_approval','paused')`, code, message, formatTime(now), task.AgentTaskID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_tasks SET status='failed', failure_code=?, failure_message=?,
		input_pause_requested=0, progress_message='', completed_at=?, updated_at=? WHERE agent_task_id=? AND status='queued'`,
		code, message, formatTime(now), formatTime(now), task.AgentTaskID)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "AGENT_TASK_CLAIM_CONFLICT", "后台任务恢复状态已变化。"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE skill_invocations SET status='failed', updated_at=?
		WHERE skill_invocation_id=? AND agent_task_id=?`, formatTime(now), task.SkillInvocationID, task.AgentTaskID); err != nil {
		return err
	}
	// Retain the checkpoint and attempt failure for audit and replay protection.
	_, err = s.appendEvent(ctx, tx, task.ProjectID, nil, nil, "agent_task.failed", "agent_task", task.AgentTaskID, map[string]any{"error_code": code})
	return err
}

func (s *Store) resumeApprovedAgentTaskTx(ctx context.Context, tx *sql.Tx, callID string, now time.Time) error {
	var taskID, attemptID, projectID string
	err := tx.QueryRowContext(ctx, `SELECT at.agent_task_id, b.agent_task_attempt_id, at.project_id FROM agent_task_tool_calls b
		JOIN agent_task_attempts ata ON ata.agent_task_attempt_id = b.agent_task_attempt_id
		JOIN agent_tasks at ON at.agent_task_id = ata.agent_task_id WHERE b.agent_tool_call_id = ?`, callID).Scan(&taskID, &attemptID, &projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_tasks SET status = 'queued', queued_at = ?, updated_at = ?
		WHERE agent_task_id = ? AND status = 'waiting_approval' AND cancel_requested = 0
		AND EXISTS (SELECT 1 FROM agent_task_run_states WHERE agent_task_id = agent_tasks.agent_task_id)
		AND NOT EXISTS (SELECT 1 FROM agent_task_tool_calls b JOIN agent_tool_calls c ON c.agent_tool_call_id = b.agent_tool_call_id
			WHERE b.agent_task_attempt_id = ? AND c.status = 'pending_approval')`, formatTime(now), formatTime(now), taskID, attemptID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected > 0 {
		_, err = s.appendEvent(ctx, tx, projectID, nil, nil, "agent_task.queued", "agent_task", taskID, map[string]any{"resume": true})
		return err
	}
	return nil
}

func (s *Store) validateAgentTaskToolBeginTx(ctx context.Context, tx *sql.Tx, command BeginAgentToolCallCommand) error {
	if command.AgentTaskAttemptID == "" {
		var background int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_tasks WHERE skill_invocation_id = ?`, command.SkillInvocationID).Scan(&background); err != nil {
			return err
		}
		if background > 0 {
			return domainError("AGENT_TASK_ATTEMPT_REQUIRED", "后台工具调用必须绑定执行尝试。")
		}
		return nil
	}
	state, err := loadAgentTaskAttemptStateTx(ctx, tx, command.AgentTaskAttemptID)
	if err != nil {
		return err
	}
	if state.ProjectID != command.ProjectID || state.SkillInvocationID != command.SkillInvocationID || command.AgentTurnID != "" {
		return domainError("AGENT_TASK_ATTEMPT_SCOPE_MISMATCH", "后台工具调用身份不匹配。")
	}
	return validateActiveAgentTaskAttempt(state, command.AttemptToken, s.now())
}

func (s *Store) validateAgentTaskToolCallTx(ctx context.Context, tx *sql.Tx, callID string) error {
	if bound, err := s.validateMemoryGenerationToolCallTx(ctx, tx, callID); bound || err != nil {
		return err
	}
	var attemptID string
	err := tx.QueryRowContext(ctx, `SELECT agent_task_attempt_id FROM agent_task_tool_calls WHERE agent_tool_call_id = ?`, callID).Scan(&attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return s.validateExecutionToolCallTx(ctx, tx, callID)
	}
	if err != nil {
		return err
	}
	state, err := loadAgentTaskAttemptStateTx(ctx, tx, attemptID)
	if err != nil {
		return err
	}
	if activity, ok := AgentActivityFromContext(ctx); ok {
		if activity.AgentTaskAttemptID != attemptID || activity.ProjectID != state.ProjectID || activity.AgentTurnID != "" || activity.ExecutionAttemptID != "" {
			return domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "工具调用不属于当前后台执行。")
		}
		if activity.AttemptToken == "" || subtle.ConstantTimeCompare([]byte(state.TokenHash), []byte(sha256Hex([]byte(activity.AttemptToken)))) != 1 {
			return domainError("ATTEMPT_TOKEN_INVALID", "后台任务执行 Token 无效。")
		}
	} else if principal, ok := identity.FromContext(ctx); ok && principal.Kind == identity.KindService {
		return domainError("AGENT_ACTIVITY_REQUIRED", "后台工具服务请求必须携带执行身份。")
	}
	if state.CancelRequested || state.TaskStatus == "cancelled" {
		return domainError("AGENT_TASK_CANCELLED", "后台任务已取消。")
	}
	if (state.TaskStatus != "running" && state.TaskStatus != "pausing") || state.AttemptStatus != "running" || !state.LeaseUntil.After(s.now()) {
		return domainError("AGENT_TASK_ATTEMPT_STALE", "后台工具调用的执行尝试已失效。")
	}
	var deleted sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT deleted_at FROM projects WHERE project_id = ?`, state.ProjectID).Scan(&deleted); err != nil {
		return err
	}
	if deleted.Valid {
		return domainError("AGENT_TASK_CANCELLED", "后台任务所属项目已删除。")
	}
	return nil
}

func cancelAgentTaskToolsTx(ctx context.Context, tx *sql.Tx, taskID string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE agent_tool_approvals SET status = 'cancelled', version = version + 1, resolved_at = ?
		WHERE status = 'pending' AND agent_tool_call_id IN (SELECT b.agent_tool_call_id FROM agent_task_tool_calls b
			JOIN agent_task_attempts a ON a.agent_task_attempt_id = b.agent_task_attempt_id WHERE a.agent_task_id = ?)`, formatTime(now), taskID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE agent_tool_calls SET status = 'cancelled', approval_status = CASE WHEN approval_status = 'pending' THEN 'cancelled' ELSE approval_status END,
		completed_at = ?, updated_at = ? WHERE status IN ('pending_approval','approved','running')
		AND agent_tool_call_id IN (SELECT b.agent_tool_call_id FROM agent_task_tool_calls b
			JOIN agent_task_attempts a ON a.agent_task_attempt_id = b.agent_task_attempt_id WHERE a.agent_task_id = ?)`, formatTime(now), formatTime(now), taskID)
	return err
}

func deleteAgentTaskRunStateTx(ctx context.Context, tx *sql.Tx, taskID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM agent_task_run_states WHERE agent_task_id = ?`, taskID)
	return err
}

func agentTaskReplayRiskTx(ctx context.Context, tx *sql.Tx, taskID, attemptID string) (bool, error) {
	var risk bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_task_run_states WHERE agent_task_id = ?)
		OR EXISTS(SELECT 1 FROM agent_task_tool_calls b JOIN agent_tool_calls c ON c.agent_tool_call_id = b.agent_tool_call_id
			WHERE b.agent_task_attempt_id = ? AND c.access_mode != 'read' AND c.started_at IS NOT NULL)`, taskID, attemptID).Scan(&risk)
	return risk, err
}

func (s *Store) cancelProjectAgentTasksTx(ctx context.Context, tx *sql.Tx, projectID string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT agent_task_id FROM agent_tasks WHERE project_id = ? AND status NOT IN ('completed','cancelled')`, projectID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := cancelAgentTaskToolsTx(ctx, tx, id, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_task_attempts SET status = 'cancelled', error_code = 'PROJECT_DELETED', ended_at = ?
			WHERE agent_task_id = ? AND status IN ('running','waiting_approval','paused')`, formatTime(now), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_tasks SET status = 'cancelled', cancel_requested = 1, failure_code = 'PROJECT_DELETED',
			completed_at = ?, updated_at = ? WHERE agent_task_id = ?`, formatTime(now), formatTime(now), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE skill_invocations SET status = 'cancelled', updated_at = ? WHERE agent_task_id = ?`, formatTime(now), id); err != nil {
			return err
		}
		if _, err := s.appendEvent(ctx, tx, projectID, nil, nil, "agent_task.cancelled", "agent_task", id, map[string]any{"reason": "project_deleted"}); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM agent_task_run_states WHERE agent_task_id IN (SELECT agent_task_id FROM agent_tasks WHERE project_id = ?)`, projectID)
	return err
}
