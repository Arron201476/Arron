package runtime

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"content-agent/backend/internal/identity"
)

func (s *Store) HeartbeatExecutionAttempt(ctx context.Context, command HeartbeatExecutionAttemptCommand) (ExecutionAttempt, error) {
	if command.ProgramStatus != "" && command.ProgramStatus != "running" && command.ProgramStatus != "completed" && command.ProgramStatus != "incomplete" {
		return ExecutionAttempt{}, domainError("REQUEST_VALIDATION_FAILED", "Invalid program progress status.")
	}
	if command.AttemptID == "" || len(command.AttemptID) > 256 || command.AttemptToken == "" || len(command.AttemptToken) > 512 ||
		command.InputSnapshotHash == "" || len(command.InputSnapshotHash) > 128 ||
		command.LeaseSeconds < minAttemptLeaseSeconds || command.LeaseSeconds > maxAttemptLeaseSeconds {
		return ExecutionAttempt{}, domainError("REQUEST_VALIDATION_FAILED", "执行续租请求无效。")
	}
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
	if activity, ok := AgentActivityFromContext(ctx); ok && (activity.ProjectID != state.ProjectID || activity.ExecutionAttemptID != command.AttemptID ||
		activity.AgentTurnID != "" || activity.AgentTaskAttemptID != "" || activity.AttemptToken != command.AttemptToken) {
		return ExecutionAttempt{}, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "续租请求不属于当前状态化执行。")
	}
	attempt, err := scanExecutionAttempt(tx.QueryRowContext(ctx, executionAttemptSelect+` WHERE attempt_id = ?`, command.AttemptID))
	if err != nil {
		return ExecutionAttempt{}, err
	}
	if attempt.InputSnapshotHash != command.InputSnapshotHash {
		return ExecutionAttempt{}, domainError("ATTEMPT_INPUT_CHANGED", "续租请求对应的输入快照已变化。")
	}
	if err := validateExecutionOwnerTx(ctx, tx, state); err != nil {
		return ExecutionAttempt{}, err
	}
	if state.AttemptStatus == "repair_pending" && !state.ProjectDeleted && state.CurrentAttemptID == state.AttemptID && state.TaskStatus == "repair_pending" &&
		(state.StepStatus == "running" || state.StepStatus == "paused") && (runAcceptsActiveAttemptResult(state.RunStatus) || state.RunStatus == "paused") {
		repair, err := loadExecutionResultRepairTx(ctx, tx, attempt, "queued")
		if err != nil {
			return ExecutionAttempt{}, err
		}
		if repair == nil {
			return ExecutionAttempt{}, domainError("AGENT_RUN_STATE_CORRUPT", "待修复执行缺少候选记录。")
		}
		return attempt, nil
	}
	// A heartbeat can race with result submission. Acknowledge the durable receipt
	// without renewing it; the existing commit path owns its remaining lifecycle.
	if state.AttemptStatus == "result_received" && !state.ProjectDeleted && state.CurrentAttemptID == state.AttemptID &&
		state.TaskStatus == "running" && state.StepStatus == "running" && runAcceptsActiveAttemptResult(state.RunStatus) {
		return attempt, nil
	}
	// Checkpoint persistence may complete before its HTTP response reaches the
	// Worker. Waiting is a durable handoff, not a lost or renewable running lease.
	if (state.AttemptStatus == "waiting_approval" || state.AttemptStatus == "paused") && !state.ProjectDeleted && state.CurrentAttemptID == state.AttemptID &&
		state.TaskStatus == state.AttemptStatus && (state.StepStatus == "running" || state.StepStatus == "paused") &&
		(runAcceptsActiveAttemptResult(state.RunStatus) || state.RunStatus == "paused") {
		var checkpoint bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_run_states WHERE attempt_id = ?)`, state.AttemptID).Scan(&checkpoint); err != nil {
			return ExecutionAttempt{}, err
		}
		if !checkpoint {
			return ExecutionAttempt{}, domainError("AGENT_RUN_STATE_CORRUPT", "等待审批的执行缺少 SDK 恢复状态。")
		}
		return attempt, nil
	}
	now := s.now()
	if err := validateExecutionToolState(state, now); err != nil {
		return ExecutionAttempt{}, err
	}
	lease := now.Add(time.Duration(command.LeaseSeconds) * time.Second)
	if lease.Before(state.LeaseUntil) {
		lease = state.LeaseUntil
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_attempts SET lease_until = ? WHERE attempt_id = ? AND status = 'running'`, formatTime(lease), command.AttemptID)
	if err != nil {
		return ExecutionAttempt{}, err
	}
	if err := requireOneRow(result, "ATTEMPT_STALE", "执行尝试已失效，不能续租。"); err != nil {
		return ExecutionAttempt{}, err
	}
	attempt.LeaseUntil = lease
	if command.ProgramStatus != "" && command.ProgramStatus != attempt.ProgramStatus {
		if _, err := tx.ExecContext(ctx, `INSERT INTO execution_program_progress(attempt_id,status) VALUES(?,?) ON CONFLICT(attempt_id) DO UPDATE SET status=excluded.status`, command.AttemptID, command.ProgramStatus); err != nil {
			return ExecutionAttempt{}, err
		}
		if _, err := s.appendEvent(ctx, tx, state.ProjectID, &attempt.RunID, &attempt.StepRunID, "task.progressed", "task_item", attempt.TaskItemID,
			map[string]any{"attempt_id": command.AttemptID, "program_status": command.ProgramStatus}); err != nil {
			return ExecutionAttempt{}, err
		}
		attempt.ProgramStatus = command.ProgramStatus
	}
	attempt.PauseRequested = state.RunStatus == "pausing"
	if !attempt.PauseRequested {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_inputs WHERE attempt_id=? AND withdrawn_at IS NULL AND claim_token_hash IS NOT ?)`, state.AttemptID, state.TokenHash).Scan(&attempt.PauseRequested); err != nil {
			return ExecutionAttempt{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ExecutionAttempt{}, err
	}
	return attempt, nil
}

func validateExecutionOwnerTx(ctx context.Context, tx *sql.Tx, state executionToolState) error {
	var role string
	err := tx.QueryRowContext(ctx, `SELECT wm.role FROM workspace_memberships wm
		JOIN users u ON u.user_id = wm.user_id JOIN workspaces w ON w.workspace_id = wm.workspace_id
		WHERE wm.workspace_id = ? AND wm.user_id = ? AND wm.status = 'active'
		AND u.status = 'active' AND w.status = 'active' AND u.deleted_at IS NULL AND w.deleted_at IS NULL`, state.WorkspaceID, state.UserID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return domainError("WORKSPACE_ACCESS_DENIED", "任务发起者已失去工作区访问权限。")
	}
	if err != nil {
		return err
	}
	if !(identity.Principal{Kind: identity.KindUser, Role: identity.Role(role)}).Allows(identity.RoleEditor) {
		return domainError("ROLE_FORBIDDEN", "任务发起者已失去执行权限。")
	}
	return nil
}
