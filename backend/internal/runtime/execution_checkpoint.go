package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
)

const MaxExecutionWorkerStateBytes = 4 << 20

func (s *Store) PauseExecutionForApproval(ctx context.Context, command PauseExecutionForApprovalCommand) (ExecutionAttempt, error) {
	recovery, recoveryErr := validateModelRecoveryCheckpoint(command.PauseAgentTurnForApprovalCommand)
	if recoveryErr != nil {
		return ExecutionAttempt{}, recoveryErr
	}
	if recovery && !command.UserPause {
		return ExecutionAttempt{}, domainError("REQUEST_VALIDATION_FAILED", "模型恢复必须使用暂停通道。")
	}
	if command.AttemptID == "" || len(command.AttemptID) > 256 || command.AttemptToken == "" || len(command.AttemptToken) > 512 ||
		command.InputSnapshotHash == "" || len(command.InputSnapshotHash) > 128 || command.SchemaVersion == "" || len(command.SchemaVersion) > 64 ||
		len(command.RunState) == 0 || len(command.RunState) > maxAgentTurnRunState || (!command.UserPause && len(command.PendingSDKToolCallIDs) == 0) || len(command.PendingSDKToolCallIDs) > maxAgentTurnApprovals ||
		len(command.WorkerState) > MaxExecutionWorkerStateBytes || !jsonObject(command.WorkerState) {
		return ExecutionAttempt{}, domainError("REQUEST_VALIDATION_FAILED", "状态化任务审批 checkpoint 不完整。")
	}
	var envelope map[string]json.RawMessage
	var schema string
	if json.Unmarshal(command.RunState, &envelope) != nil || envelope == nil || json.Unmarshal(envelope["$schemaVersion"], &schema) != nil || schema != command.SchemaVersion {
		return ExecutionAttempt{}, domainError("AGENT_RUN_STATE_SCHEMA_MISMATCH", "状态化任务 SDK RunState 版本不匹配。")
	}
	ids, err := normalizeSDKToolCallIDs(command.PendingSDKToolCallIDs)
	if err != nil {
		return ExecutionAttempt{}, err
	}
	slices.Sort(ids)
	pendingJSON, _ := json.Marshal(ids)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExecutionAttempt{}, err
	}
	defer tx.Rollback()
	state, err := loadExecutionToolStateTx(ctx, tx, command.AttemptID)
	if err != nil {
		return ExecutionAttempt{}, err
	}
	if err := validateExecutionToolToken(state, command.AttemptToken); err != nil {
		return ExecutionAttempt{}, err
	}
	if activity, ok := AgentActivityFromContext(ctx); ok && (activity.ProjectID != state.ProjectID || activity.ExecutionAttemptID != command.AttemptID || activity.AttemptToken != command.AttemptToken || activity.AgentTurnID != "" || activity.AgentTaskAttemptID != "") {
		return ExecutionAttempt{}, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "审批保存请求不属于当前状态化执行。")
	}
	if err := validateExecutionOwnerTx(ctx, tx, state); err != nil {
		return ExecutionAttempt{}, err
	}
	attempt, err := scanExecutionAttempt(tx.QueryRowContext(ctx, executionAttemptSelect+` WHERE attempt_id = ?`, command.AttemptID))
	if err != nil {
		return ExecutionAttempt{}, err
	}
	if attempt.InputSnapshotHash != command.InputSnapshotHash {
		return ExecutionAttempt{}, domainError("ATTEMPT_INPUT_CHANGED", "审批保存对应的输入快照已变化。")
	}
	targetStatus := "waiting_approval"
	if command.UserPause {
		targetStatus = "paused"
	}
	if (state.AttemptStatus == targetStatus || (command.UserPause && state.AttemptStatus == "waiting_approval")) && state.TaskStatus == state.AttemptStatus && !state.ProjectDeleted && state.CurrentAttemptID == state.AttemptID &&
		(state.StepStatus == "running" || state.StepStatus == "paused") &&
		(state.RunStatus == "running" || state.RunStatus == "pausing" || state.RunStatus == "paused") {
		var stateHash, workerHash, pending string
		err := tx.QueryRowContext(ctx, `SELECT state_hash, worker_state_hash, pending_sdk_tool_call_ids_json FROM execution_run_states WHERE attempt_id = ?`, state.AttemptID).Scan(&stateHash, &workerHash, &pending)
		if err != nil {
			return ExecutionAttempt{}, err
		}
		if stateHash == sha256Hex(command.RunState) && workerHash == sha256Hex(command.WorkerState) && pending == string(pendingJSON) {
			return attempt, nil
		}
		return ExecutionAttempt{}, domainError("AGENT_RUN_STATE_CHECKPOINT_CONFLICT", "已保存的状态化审批 checkpoint 与重试请求不同。")
	}
	now := s.now()
	if err := validateExecutionToolState(state, now); err != nil {
		return ExecutionAttempt{}, err
	}
	if recovery {
		if err := validateRecoveryToolsTx(ctx, tx, "stateful_workflow", state.AttemptID, command.PauseAgentTurnForApprovalCommand); err != nil {
			return ExecutionAttempt{}, err
		}
	}
	if err := validateExecutionInputCheckpointTx(ctx, tx, state.AttemptID, command.AttemptToken, command.WorkerState); err != nil {
		return ExecutionAttempt{}, err
	}
	_, unresolved, err := executionApprovalDecisionsTx(ctx, tx, state.AttemptID, ids)
	if err != nil {
		return ExecutionAttempt{}, err
	}
	var pendingCount, runningCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(CASE WHEN c.status = 'pending_approval' THEN 1 END), COUNT(CASE WHEN c.status = 'running' THEN 1 END)
		FROM execution_tool_calls b JOIN agent_tool_calls c ON c.agent_tool_call_id = b.agent_tool_call_id WHERE b.execution_attempt_id = ?`, state.AttemptID).Scan(&pendingCount, &runningCount); err != nil {
		return ExecutionAttempt{}, err
	}
	if pendingCount != unresolved || runningCount != 0 {
		return ExecutionAttempt{}, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "工具待审批集合不一致或仍有工具未结束。")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO execution_run_states(attempt_id, schema_version, state_json, state_hash,
		pending_sdk_tool_call_ids_json, worker_state_json, worker_state_hash, checkpoint_version, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, 1, ?) ON CONFLICT(attempt_id) DO UPDATE SET
		schema_version = excluded.schema_version, state_json = excluded.state_json, state_hash = excluded.state_hash,
		pending_sdk_tool_call_ids_json = excluded.pending_sdk_tool_call_ids_json, worker_state_json = excluded.worker_state_json,
		worker_state_hash = excluded.worker_state_hash, checkpoint_version = checkpoint_version + 1, updated_at = excluded.updated_at`,
		state.AttemptID, agentTurnRunStateSchema+":"+schema, string(command.RunState), sha256Hex(command.RunState), string(pendingJSON),
		string(command.WorkerState), sha256Hex(command.WorkerState), formatTime(now)); err != nil {
		return ExecutionAttempt{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_attempts SET status = ? WHERE attempt_id = ?`, targetStatus, state.AttemptID); err != nil {
		return ExecutionAttempt{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_items SET status = ?, updated_at = ? WHERE task_item_id = ? AND current_attempt_id = ?`, targetStatus, formatTime(now), attempt.TaskItemID, state.AttemptID); err != nil {
		return ExecutionAttempt{}, err
	}
	if _, err := s.appendEvent(ctx, tx, state.ProjectID, &attempt.RunID, &attempt.StepRunID, "task."+targetStatus, "task_item", attempt.TaskItemID,
		map[string]any{"attempt_id": state.AttemptID, "pending_approval_count": unresolved}); err != nil {
		return ExecutionAttempt{}, err
	}
	if recovery {
		code, _ := recoveryDetails(command.RecoveryReason)
		if _, err := tx.ExecContext(ctx, `UPDATE execution_attempts SET error_code=? WHERE attempt_id=?`, code, attempt.AttemptID); err != nil {
			return ExecutionAttempt{}, err
		}
		attempt.ErrorCode = &code
		if _, err := tx.ExecContext(ctx, `UPDATE task_items SET failure=? WHERE task_item_id=?`, code, attempt.TaskItemID); err != nil {
			return ExecutionAttempt{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status='pausing',updated_at=? WHERE run_id=? AND status='running'`, formatTime(now), attempt.RunID); err != nil {
			return ExecutionAttempt{}, err
		}
		if _, err := s.appendEvent(ctx, tx, state.ProjectID, &attempt.RunID, &attempt.StepRunID, "run.pause_requested", "run", attempt.RunID, map[string]any{"reason": command.RecoveryReason, "attempt_id": attempt.AttemptID}); err != nil {
			return ExecutionAttempt{}, err
		}
	}
	if err := s.pauseAfterCompletedTaskIfRequestedTx(ctx, tx, attempt.RunID, now, "sdk_checkpoint_saved"); err != nil {
		return ExecutionAttempt{}, err
	}
	attempt.Status = targetStatus
	if err := tx.Commit(); err != nil {
		return ExecutionAttempt{}, err
	}
	return attempt, nil
}

func executionApprovalDecisionsTx(ctx context.Context, tx *sql.Tx, attemptID string, ids []string) ([]agentcontract.AgentToolApprovalDecision, int, error) {
	decisions := make([]agentcontract.AgentToolApprovalDecision, 0, len(ids))
	unresolved := 0
	for _, id := range ids {
		var callStatus, approvalStatus string
		err := tx.QueryRowContext(ctx, `SELECT c.status, a.status FROM execution_tool_calls b
			JOIN agent_tool_calls c ON c.agent_tool_call_id = b.agent_tool_call_id
			JOIN agent_tool_approvals a ON a.agent_tool_call_id = c.agent_tool_call_id
			WHERE b.execution_attempt_id = ? AND c.sdk_tool_call_id = ?`, attemptID, id).Scan(&callStatus, &approvalStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "审批未绑定到当前状态化尝试。")
		}
		if err != nil {
			return nil, 0, err
		}
		switch {
		case callStatus == "pending_approval" && approvalStatus == "pending":
			unresolved++
		case callStatus == "approved" && approvalStatus == "approved":
			decisions = append(decisions, agentcontract.AgentToolApprovalDecision{SDKToolCallID: id, Action: "approve"})
		case callStatus == "rejected" && approvalStatus == "rejected":
			decisions = append(decisions, agentcontract.AgentToolApprovalDecision{SDKToolCallID: id, Action: "reject"})
		default:
			return nil, 0, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "状态化工具审批状态与 SDK checkpoint 不一致。")
		}
	}
	return decisions, unresolved, nil
}

func (s *Store) claimExecutionResumeTx(ctx context.Context, tx *sql.Tx, command ClaimExecutionTaskCommand, now time.Time) (*TaskClaim, error) {
	executors, err := json.Marshal(command.ExecutorIDs)
	if err != nil {
		return nil, err
	}
	var lastUpdated, lastID string
	// Skip incompatible/disabled candidates without repeatedly stopping at the
	// first page. Keep only one bounded page of IDs in memory within the claim transaction.
	for {
		rows, err := tx.QueryContext(ctx, `SELECT ea.attempt_id, COALESCE(checkpoint.updated_at, '') FROM execution_attempts ea
		LEFT JOIN execution_run_states checkpoint ON checkpoint.attempt_id = ea.attempt_id
		JOIN task_items ti ON ti.current_attempt_id = ea.attempt_id AND ti.task_item_id = ea.task_item_id
		JOIN step_runs sr ON sr.step_run_id = ea.step_run_id JOIN runs r ON r.run_id = ea.run_id
		JOIN projects p ON p.project_id = r.project_id
		WHERE ea.status IN ('waiting_approval', 'paused') AND ti.status = ea.status AND sr.status = 'running' AND r.status = 'running' AND p.deleted_at IS NULL
		AND ea.provider_id = ? AND ea.executor_id IN (SELECT value FROM json_each(?))
		AND (COALESCE(checkpoint.updated_at, ''), ea.attempt_id) > (?, ?)
		AND NOT EXISTS (SELECT 1 FROM execution_tool_calls b JOIN agent_tool_calls c ON c.agent_tool_call_id = b.agent_tool_call_id
			WHERE b.execution_attempt_id = ea.attempt_id AND c.status = 'pending_approval')
		ORDER BY COALESCE(checkpoint.updated_at, ''), ea.attempt_id LIMIT 100`, command.ProviderID, string(executors), lastUpdated, lastID)
		if err != nil {
			return nil, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id, &lastUpdated); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
			lastID = id
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
			attempt, err := scanExecutionAttempt(tx.QueryRowContext(ctx, executionAttemptSelect+` WHERE attempt_id = ?`, id))
			if err != nil {
				return nil, err
			}
			if !slices.Contains(command.ExecutorIDs, attempt.ExecutorID) || attempt.ProviderID != command.ProviderID {
				continue
			}
			state, err := loadExecutionToolStateTx(ctx, tx, id)
			if err != nil {
				return nil, err
			}
			if state.ProjectDeleted || state.CurrentAttemptID != id || !slices.Contains([]string{"waiting_approval", "paused"}, state.AttemptStatus) || state.TaskStatus != state.AttemptStatus || state.StepStatus != "running" || state.RunStatus != "running" {
				continue
			}
			if err := validateExecutionOwnerTx(ctx, tx, state); err != nil {
				if isDomainErrorCode(err, "WORKSPACE_ACCESS_DENIED") || isDomainErrorCode(err, "ROLE_FORBIDDEN") {
					continue
				}
				return nil, err
			}
			claim, err := loadExecutionResumeTx(ctx, tx, attempt, state)
			if err != nil {
				if isDomainErrorCode(err, "AGENT_RUN_STATE_CORRUPT") || isDomainErrorCode(err, "AGENT_RUN_STATE_APPROVAL_MISMATCH") {
					if err := s.failExecutionResumeTx(ctx, tx, attempt, state, now); err != nil {
						return nil, err
					}
					continue
				}
				return nil, err
			}
			modelID := ""
			if claim.ContextPack.Budget.ModelID != nil {
				modelID = *claim.ContextPack.Budget.ModelID
			}
			if modelID != command.ModelID {
				continue
			}
			entry, available, err := s.capabilityEntryForRunQuery(ctx, tx, attempt.RunID, claim.CapabilityID)
			if err != nil {
				return nil, err
			}
			if !available || entry.Status != capability.Available || entry.Definition == nil || entry.Definition.Version != claim.CapabilityVersion {
				continue
			}
			token := s.newID("tok")
			lease := now.Add(time.Duration(command.LeaseSeconds) * time.Second)
			result, err := tx.ExecContext(ctx, `UPDATE execution_attempts SET status = 'running', error_code=NULL, token_hash = ?, worker_id = ?, lease_until = ?
			WHERE attempt_id = ? AND status IN ('waiting_approval', 'paused')`, sha256Hex([]byte(token)), command.WorkerID, formatTime(lease), id)
			if err != nil {
				return nil, err
			}
			if err := requireOneRow(result, "EXECUTION_TASK_CLAIM_CONFLICT", "状态化任务审批恢复已被领取。"); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE task_items SET status = 'running', failure=NULL, updated_at = ? WHERE task_item_id = ? AND current_attempt_id = ?`, formatTime(now), attempt.TaskItemID, id); err != nil {
				return nil, err
			}
			claim.Attempt.Status, claim.Attempt.WorkerID, claim.Attempt.LeaseUntil = "running", command.WorkerID, lease
			claim.Attempt.ErrorCode, claim.Task.Failure = nil, nil
			claim.Task.Status, claim.Task.UpdatedAt, claim.AttemptToken = "running", now, token
			if _, err := s.appendEvent(ctx, tx, state.ProjectID, &attempt.RunID, &attempt.StepRunID, "task.resumed", "task_item", attempt.TaskItemID,
				map[string]any{"attempt_id": id, "checkpoint_version": claim.Resume.CheckpointVersion}); err != nil {
				return nil, err
			}
			return claim, nil
		}
	}
}

func loadExecutionResumeTx(ctx context.Context, tx *sql.Tx, attempt ExecutionAttempt, state executionToolState) (*TaskClaim, error) {
	var stateJSON, schema, stateHash, pendingJSON, workerJSON, workerHash string
	var version int
	corrupt := domainError("AGENT_RUN_STATE_CORRUPT", "状态化任务 SDK checkpoint 或冻结上下文校验失败。")
	if err := tx.QueryRowContext(ctx, `SELECT schema_version, state_json, state_hash, pending_sdk_tool_call_ids_json,
		worker_state_json, worker_state_hash, checkpoint_version FROM execution_run_states WHERE attempt_id = ?`, attempt.AttemptID).
		Scan(&schema, &stateJSON, &stateHash, &pendingJSON, &workerJSON, &workerHash, &version); errors.Is(err, sql.ErrNoRows) {
		return nil, corrupt
	} else if err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	var sdkSchema string
	if len(stateJSON) > maxAgentTurnRunState || len(workerJSON) > MaxExecutionWorkerStateBytes || version < 1 || sha256Hex([]byte(stateJSON)) != stateHash || sha256Hex([]byte(workerJSON)) != workerHash ||
		json.Unmarshal([]byte(stateJSON), &envelope) != nil || envelope == nil || json.Unmarshal(envelope["$schemaVersion"], &sdkSchema) != nil || schema != agentTurnRunStateSchema+":"+sdkSchema || !jsonObject([]byte(workerJSON)) {
		return nil, corrupt
	}
	var pending []string
	if json.Unmarshal([]byte(pendingJSON), &pending) != nil || pending == nil || (len(pending) == 0 && attempt.Status != "paused") || len(pending) > maxAgentTurnApprovals {
		return nil, corrupt
	}
	if _, err := normalizeSDKToolCallIDs(pending); err != nil {
		return nil, corrupt
	}
	decisions, unresolved, err := executionApprovalDecisionsTx(ctx, tx, attempt.AttemptID, pending)
	if err != nil {
		return nil, err
	}
	if unresolved != 0 {
		return nil, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "状态化任务仍有未完成审批。")
	}
	claim, err := loadFrozenExecutionClaimTx(ctx, tx, attempt, state)
	if err != nil {
		return nil, err
	}
	claim.Resume = &ExecutionResumeContext{AgentTurnResumeContext: AgentTurnResumeContext{RunState: json.RawMessage(stateJSON), ApprovalDecisions: decisions},
		SchemaVersion: sdkSchema, WorkerState: json.RawMessage(workerJSON), CheckpointVersion: version}
	claim.Repair, err = loadExecutionResultRepairTx(ctx, tx, attempt, "claimed")
	return claim, err
}

func loadFrozenExecutionClaimTx(ctx context.Context, tx *sql.Tx, attempt ExecutionAttempt, state executionToolState) (*TaskClaim, error) {
	corrupt := domainError("AGENT_RUN_STATE_CORRUPT", "状态化任务冻结上下文校验失败。")
	var packJSON string
	if err := tx.QueryRowContext(ctx, `SELECT payload_json FROM context_packs WHERE attempt_id = ?`, attempt.AttemptID).Scan(&packJSON); errors.Is(err, sql.ErrNoRows) {
		return nil, corrupt
	} else if err != nil {
		return nil, err
	}
	var pack StepExecutionContextPack
	if json.Unmarshal([]byte(packJSON), &pack) != nil || pack.ProjectID != state.ProjectID || pack.ConversationID != state.ConversationID ||
		pack.Run.RunID != attempt.RunID || pack.Step.StepRunID != attempt.StepRunID || pack.Step.TaskItemID != attempt.TaskItemID || pack.ContextHash != attempt.InputSnapshotHash {
		return nil, corrupt
	}
	hash, err := calculateStepExecutionContextHash(pack)
	if err != nil || hash != attempt.InputSnapshotHash {
		return nil, corrupt
	}
	task, err := scanTaskItem(tx.QueryRowContext(ctx, taskItemSelect+` WHERE task_item_id = ?`, attempt.TaskItemID))
	if err != nil {
		return nil, err
	}
	inputs, err := loadExecutionInputs(ctx, tx, attempt.AttemptID)
	inputs = activeExecutionInputs(inputs)
	if err != nil {
		return nil, err
	}
	return &TaskClaim{Task: task, Attempt: attempt, CapabilityID: pack.Capability.CapabilityID, CapabilityVersion: pack.Capability.CapabilityVersion,
		ExecutorID: attempt.ExecutorID, ConfigSnapshot: pack.ConfigSnapshot, ContextPack: pack, AdditionalInputs: inputs}, nil
}

func (s *Store) failExecutionResumeTx(ctx context.Context, tx *sql.Tx, attempt ExecutionAttempt, state executionToolState, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE execution_attempts SET status = 'failed', error_code = 'AGENT_RUN_STATE_CORRUPT', ended_at = ? WHERE attempt_id = ?`, formatTime(now), attempt.AttemptID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_items SET status = 'failed', current_attempt_id = NULL, failure = 'AGENT_RUN_STATE_CORRUPT', ended_at = ?, updated_at = ? WHERE task_item_id = ?`, formatTime(now), formatTime(now), attempt.TaskItemID); err != nil {
		return err
	}
	return s.failStepRunAndProjectTx(ctx, tx, state.ProjectID, attempt.RunID, attempt.StepRunID, "AGENT_RUN_STATE_CORRUPT", now)
}

// A business pause cannot discard an in-flight SDK checkpoint or issued write
// and then make ResumeRun restart it as a fresh model task.
func (s *Store) guardExecutionPauseTx(ctx context.Context, tx *sql.Tx, run Run, stepID string, now time.Time) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT ea.attempt_id, ea.task_item_id, ea.lease_until
		FROM execution_attempts ea JOIN task_items ti ON ti.current_attempt_id = ea.attempt_id
		WHERE ea.run_id = ? AND ea.step_run_id = ? AND ea.status = 'running'`, run.RunID, stepID)
	if err != nil {
		return false, err
	}
	type active struct{ id, taskID, lease string }
	var attempts []active
	for rows.Next() {
		var item active
		if err := rows.Scan(&item.id, &item.taskID, &item.lease); err != nil {
			rows.Close()
			return false, err
		}
		attempts = append(attempts, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, item := range attempts {
		risk, err := executionToolReplayRiskTx(ctx, tx, item.id)
		if err != nil {
			return false, err
		}
		if !risk {
			if err := cancelExecutionToolsTx(ctx, tx, run.RunID, item.id, now); err != nil {
				return false, err
			}
			continue
		}
		lease, err := parseTime(item.lease)
		if err != nil {
			return false, err
		}
		if lease.After(now) {
			return false, domainError("SDK_EXECUTION_CHECKPOINT_REQUIRED", "当前 SDK 任务必须先保存安全 checkpoint，不能丢弃已执行的工具状态。")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE execution_attempts SET status = 'expired', ended_at = ?, error_code = 'SDK_TOOL_REPLAY_RISK' WHERE attempt_id = ?`, formatTime(now), item.id); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task_items SET status = 'failed', current_attempt_id = NULL, failure = 'SDK_TOOL_REPLAY_RISK', ended_at = ?, updated_at = ? WHERE task_item_id = ?`, formatTime(now), formatTime(now), item.taskID); err != nil {
			return false, err
		}
		return true, s.failStepRunAndProjectTx(ctx, tx, run.ProjectID, run.RunID, stepID, "SDK_TOOL_REPLAY_RISK", now)
	}
	return false, nil
}
