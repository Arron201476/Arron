package runtime

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"content-agent/backend/internal/identity"
)

type executionToolState struct {
	AttemptID, ProjectID, RunID, ConversationID, UserID, WorkspaceID string
	TokenHash, AttemptStatus, TaskStatus, StepStatus, RunStatus      string
	CurrentAttemptID                                                 string
	LeaseUntil                                                       time.Time
	ProjectDeleted                                                   bool
	OutputRepairOnly                                                 bool
}

func loadExecutionToolStateTx(ctx context.Context, tx *sql.Tx, attemptID string) (executionToolState, error) {
	var state executionToolState
	var lease string
	err := tx.QueryRowContext(ctx, `SELECT ea.attempt_id, r.project_id, r.run_id, r.conversation_id,
		r.user_id, p.workspace_id, ea.token_hash, ea.status, ti.status, sr.status, r.status,
		COALESCE(ti.current_attempt_id, ''), ea.lease_until, p.deleted_at IS NOT NULL,
		EXISTS(SELECT 1 FROM execution_result_rejections repair WHERE repair.attempt_id = ea.attempt_id AND repair.status = 'claimed')
		FROM execution_attempts ea JOIN runs r ON r.run_id = ea.run_id
		JOIN projects p ON p.project_id = r.project_id
		JOIN step_runs sr ON sr.step_run_id = ea.step_run_id AND sr.run_id = r.run_id
		JOIN task_items ti ON ti.task_item_id = ea.task_item_id AND ti.step_run_id = sr.step_run_id AND ti.run_id = r.run_id
		WHERE ea.attempt_id = ?`, attemptID).Scan(&state.AttemptID, &state.ProjectID, &state.RunID,
		&state.ConversationID, &state.UserID, &state.WorkspaceID, &state.TokenHash, &state.AttemptStatus,
		&state.TaskStatus, &state.StepStatus, &state.RunStatus, &state.CurrentAttemptID, &lease, &state.ProjectDeleted, &state.OutputRepairOnly)
	if errors.Is(err, sql.ErrNoRows) {
		return state, domainError("EXECUTION_ATTEMPT_NOT_FOUND", "状态化执行尝试不存在。")
	}
	if err == nil {
		state.LeaseUntil, err = parseTime(lease)
	}
	return state, err
}

func validateExecutionToolState(state executionToolState, now time.Time) error {
	if state.ProjectDeleted || state.AttemptStatus != "running" || state.CurrentAttemptID != state.AttemptID ||
		state.TaskStatus != "running" || state.StepStatus != "running" || !runAcceptsActiveAttemptResult(state.RunStatus) {
		return domainError("ATTEMPT_STALE", "状态化工具调用的执行尝试已失效。")
	}
	if !state.LeaseUntil.After(now) {
		return domainError("ATTEMPT_LEASE_EXPIRED", "状态化工具调用的执行租约已过期。")
	}
	return nil
}

func validateExecutionToolToken(state executionToolState, token string) error {
	if token == "" || subtle.ConstantTimeCompare([]byte(state.TokenHash), []byte(sha256Hex([]byte(token)))) != 1 {
		return domainError("ATTEMPT_TOKEN_INVALID", "状态化执行身份 Token 无效。")
	}
	return nil
}

func (s *Store) validateExecutionToolBeginTx(ctx context.Context, tx *sql.Tx, command BeginAgentToolCallCommand) error {
	if command.ExecutionAttemptID == "" {
		return nil
	}
	if err := requireExecutionToolActivity(ctx); err != nil {
		return err
	}
	if command.AgentTurnID != "" || command.AgentTaskAttemptID != "" || len(command.ExecutionAttemptID) > 256 || len(command.AttemptToken) > 512 {
		return domainError("AGENT_ACTIVITY_INVALID", "工具调用只能绑定一种执行身份。")
	}
	state, err := loadExecutionToolStateTx(ctx, tx, command.ExecutionAttemptID)
	if err != nil {
		return err
	}
	if err := validateExecutionToolToken(state, command.AttemptToken); err != nil {
		return err
	}
	if state.ProjectID != command.ProjectID || state.ConversationID != command.ConversationID {
		return domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "状态化工具调用与作品或会话不匹配。")
	}
	if state.OutputRepairOnly {
		return domainError("AGENT_TOOL_EXECUTION_MODE_UNSUPPORTED", "输出修复阶段不能重新执行工具。")
	}
	if command.SkillInvocationID != "" {
		var bound bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM skill_invocations
			WHERE skill_invocation_id = ? AND run_id = ? AND project_id = ?)`,
			command.SkillInvocationID, state.RunID, state.ProjectID).Scan(&bound); err != nil {
			return err
		}
		if !bound {
			return domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Skill 调用不属于当前状态化任务。")
		}
	}
	return validateExecutionToolState(state, s.now())
}

func (s *Store) validateExecutionToolCallTx(ctx context.Context, tx *sql.Tx, callID string) error {
	var attemptID string
	err := tx.QueryRowContext(ctx, `SELECT execution_attempt_id FROM execution_tool_calls WHERE agent_tool_call_id = ?`, callID).Scan(&attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := requireExecutionToolActivity(ctx); err != nil {
		return err
	}
	state, err := loadExecutionToolStateTx(ctx, tx, attemptID)
	if err != nil {
		return err
	}
	if activity, ok := AgentActivityFromContext(ctx); ok {
		if activity.ExecutionAttemptID != attemptID || activity.ProjectID != state.ProjectID {
			return domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "工具调用不属于当前状态化执行。")
		}
		if err := validateExecutionToolToken(state, activity.AttemptToken); err != nil {
			return err
		}
	}
	if state.OutputRepairOnly {
		return domainError("AGENT_TOOL_EXECUTION_MODE_UNSUPPORTED", "输出修复阶段不能重新执行工具。")
	}
	return validateExecutionToolState(state, s.now())
}

func requireExecutionToolActivity(ctx context.Context) error {
	if principal, ok := identity.FromContext(ctx); ok && principal.Kind == identity.KindService {
		if activity, present := AgentActivityFromContext(ctx); !present || activity.ExecutionAttemptID == "" {
			return domainError("AGENT_ACTIVITY_REQUIRED", "状态化工具服务请求必须携带执行身份。")
		}
	}
	return nil
}

func cancelExecutionToolsTx(ctx context.Context, tx *sql.Tx, runID, attemptID string, now time.Time) error {
	const bound = `SELECT b.agent_tool_call_id FROM execution_tool_calls b
		JOIN execution_attempts a ON a.attempt_id = b.execution_attempt_id
		WHERE a.run_id = ? AND (? = '' OR a.attempt_id = ?)`
	if _, err := tx.ExecContext(ctx, `UPDATE agent_tool_approvals SET status = 'cancelled', version = version + 1, resolved_at = ?
		WHERE status = 'pending' AND agent_tool_call_id IN (`+bound+`)`, formatTime(now), runID, attemptID, attemptID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE agent_tool_calls SET status = 'cancelled',
		approval_status = CASE WHEN approval_status IN ('pending', 'approved') THEN 'cancelled' ELSE approval_status END,
		completed_at = ?, updated_at = ? WHERE status IN ('pending_approval', 'approved', 'running')
		AND agent_tool_call_id IN (`+bound+`)`, formatTime(now), formatTime(now), runID, attemptID, attemptID)
	return err
}

func executionToolReplayRiskTx(ctx context.Context, tx *sql.Tx, attemptID string) (bool, error) {
	var risk bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_tool_calls b
		JOIN agent_tool_calls c ON c.agent_tool_call_id = b.agent_tool_call_id
		WHERE b.execution_attempt_id = ? AND c.access_mode != 'read' AND c.started_at IS NOT NULL)
		OR EXISTS(SELECT 1 FROM execution_run_states WHERE attempt_id = ?)`, attemptID, attemptID).Scan(&risk)
	return risk, err
}

// Retrying a failed task creates a new attempt, so inspect all of its prior
// attempts. Successful sibling tasks do not participate in a failed-only retry.
func failedStepExecutionReplayRisk(ctx context.Context, query runActionQuery, stepRunID string) (bool, error) {
	var risk bool
	err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_attempts ea
		JOIN task_items ti ON ti.task_item_id = ea.task_item_id AND ti.step_run_id = ea.step_run_id
		WHERE ti.step_run_id = ? AND ti.status = 'failed' AND (
			EXISTS(SELECT 1 FROM execution_tool_calls b JOIN agent_tool_calls c ON c.agent_tool_call_id = b.agent_tool_call_id
				WHERE b.execution_attempt_id = ea.attempt_id AND c.access_mode != 'read' AND c.started_at IS NOT NULL)
			OR EXISTS(SELECT 1 FROM execution_run_states WHERE attempt_id = ea.attempt_id)))`, stepRunID).Scan(&risk)
	return risk, err
}
