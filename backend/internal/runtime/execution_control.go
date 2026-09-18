package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"content-agent/backend/internal/identity"
)

type ExecutionControlArguments struct {
	TargetType   string `json:"target_type"`
	TargetID     string `json:"target_id"`
	ActionID     string `json:"action_id"`
	SnapshotHash string `json:"snapshot_hash"`
}

type ExecutionControlTarget struct {
	ProjectID        string            `json:"project_id"`
	TargetType       string            `json:"target_type"`
	TargetID         string            `json:"target_id"`
	CapabilityID     string            `json:"capability_id"`
	Status           string            `json:"status"`
	CreatedAt        time.Time         `json:"created_at"`
	CurrentStepRunID string            `json:"current_step_run_id,omitempty"`
	SnapshotHash     string            `json:"snapshot_hash"`
	AvailableActions []AvailableAction `json:"available_actions"`
}

type ExecutionControlResult struct {
	ProjectID  string `json:"project_id"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	ActionID   string `json:"action_id"`
	Status     string `json:"status"`
}

type executionControlKey struct{}
type executionControlGuard struct {
	callID, sdkID, projectID, argumentHash string
	arguments                              ExecutionControlArguments
}

func validateExecutionControlTarget(targetType, targetID string) error {
	if (targetType != "run" && targetType != "agent_task") || targetID == "" || len(targetID) > 256 || strings.TrimSpace(targetID) != targetID {
		return domainError("REQUEST_VALIDATION_FAILED", "请提供确切的运行或后台任务标识。")
	}
	return nil
}

func (s *Store) InspectExecutionControl(ctx context.Context, projectID, targetType, targetID string) (ExecutionControlTarget, error) {
	if err := validateExecutionControlTarget(targetType, targetID); err != nil {
		return ExecutionControlTarget{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExecutionControlTarget{}, err
	}
	defer tx.Rollback()
	if _, err := projectFilesWorkspace(ctx, tx, projectID, false); err != nil {
		return ExecutionControlTarget{}, err
	}
	return s.executionControlTargetTx(ctx, tx, projectID, targetType, targetID)
}

// Enumerate stable IDs in bounded pages. Details and authorization snapshots are
// loaded separately so a large project does not pre-inject all task contents.
func (s *Store) ListExecutionControlTargets(ctx context.Context, projectID, targetType, afterID string) ([]ExecutionControlTarget, string, error) {
	if targetType != "run" && targetType != "agent_task" || len(afterID) > 256 {
		return nil, "", domainError("REQUEST_VALIDATION_FAILED", "执行目标类型或分页标识无效。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	if _, err := projectFilesWorkspace(ctx, tx, projectID, false); err != nil {
		return nil, "", err
	}
	query := `SELECT run_id, capability_id, status, created_at FROM runs WHERE project_id=? AND run_id>? ORDER BY run_id LIMIT 51`
	if targetType == "agent_task" {
		query = `SELECT agent_task_id, capability_id, status, created_at FROM agent_tasks WHERE project_id=? AND agent_task_id>? ORDER BY agent_task_id LIMIT 51`
	}
	rows, err := tx.QueryContext(ctx, query, projectID, afterID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := []ExecutionControlTarget{}
	for rows.Next() {
		item := ExecutionControlTarget{ProjectID: projectID, TargetType: targetType}
		var createdAt string
		if err := rows.Scan(&item.TargetID, &item.CapabilityID, &item.Status, &createdAt); err != nil {
			return nil, "", err
		}
		if item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, "", err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > 50 {
		items, next = items[:50], items[49].TargetID
	}
	return items, next, nil
}

func (s *Store) executionControlTargetTx(ctx context.Context, tx *sql.Tx, projectID, targetType, targetID string) (ExecutionControlTarget, error) {
	result := ExecutionControlTarget{ProjectID: projectID, TargetType: targetType, TargetID: targetID, AvailableActions: []AvailableAction{}}
	var version any
	if targetType == "run" {
		run, err := getRunTx(ctx, tx, targetID)
		if err != nil {
			return result, err
		}
		if run.ProjectID != projectID {
			return result, domainError("RUN_NOT_FOUND", "运行不存在。")
		}
		snapshot, err := s.getRunSnapshotTx(ctx, tx, targetID)
		if err != nil {
			return result, err
		}
		result.Status, result.CapabilityID = run.Status, run.CapabilityID
		result.CreatedAt = run.CreatedAt
		if run.CurrentStepRunID != nil {
			result.CurrentStepRunID = *run.CurrentStepRunID
		}
		for _, action := range snapshot.AvailableActions {
			if slices.Contains([]string{"pause_run", "resume_run", "cancel_run", "retry_failed_step"}, action.ActionID) {
				result.AvailableActions = append(result.AvailableActions, action)
			}
		}
		version = []any{run.UpdatedAt, run.CurrentInputSnapshotVersionID}
	} else {
		task, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id=? AND project_id=?`, targetID, projectID))
		if errors.Is(err, sql.ErrNoRows) {
			return result, domainError("AGENT_TASK_NOT_FOUND", "后台任务不存在。")
		}
		if err != nil {
			return result, err
		}
		result.Status, result.CapabilityID = task.Status, task.CapabilityID
		result.CreatedAt = task.CreatedAt
		if slices.Contains([]string{"queued", "running", "waiting_approval"}, task.Status) {
			result.AvailableActions = append(result.AvailableActions, AvailableAction{ActionID: "pause_agent_task", TargetType: targetType, TargetID: targetID, Enabled: true})
		}
		if task.Status == "paused" {
			result.AvailableActions = append(result.AvailableActions, AvailableAction{ActionID: "resume_agent_task", TargetType: targetType, TargetID: targetID, Enabled: true})
		}
		if slices.Contains([]string{"queued", "running", "waiting_approval", "pausing", "paused", "failed"}, task.Status) {
			result.AvailableActions = append(result.AvailableActions, AvailableAction{ActionID: "cancel_agent_task", TargetType: targetType, TargetID: targetID, Enabled: true})
		}
		if task.Status == "failed" || task.Status == "cancelled" {
			risk, err := agentTaskReplayRisk(ctx, tx, targetID)
			if err != nil {
				return result, err
			}
			if !risk {
				result.AvailableActions = append(result.AvailableActions, AvailableAction{ActionID: "retry_agent_task", TargetType: targetType, TargetID: targetID, Enabled: true})
			}
		}
		// Heartbeats change UpdatedAt, but do not change the authorized task attempt.
		version = []any{task.AttemptCount, task.QueuedAt, task.CancelRequested}
	}
	encoded, err := json.Marshal([]any{result, version})
	if err != nil {
		return result, err
	}
	result.SnapshotHash = sha256Hex(encoded)
	return result, nil
}

func (s *Store) ControlExecution(ctx context.Context, callID, sdkID string, arguments ExecutionControlArguments) (ExecutionControlResult, error) {
	result := ExecutionControlResult{TargetType: arguments.TargetType, TargetID: arguments.TargetID, ActionID: arguments.ActionID}
	if err := validateExecutionControlTarget(arguments.TargetType, arguments.TargetID); err != nil {
		return result, err
	}
	if len(arguments.SnapshotHash) != 64 || sdkID == "" || callID == "" {
		return result, domainError("REQUEST_VALIDATION_FAILED", "控制操作需要已检查的快照和已批准的 SDK 工具调用。")
	}
	call, err := s.GetAgentToolCall(ctx, callID)
	if err != nil {
		return result, err
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return result, err
	}
	_, hash, _, err := summarizeAgentToolPayload(encoded, true)
	if err != nil {
		return result, err
	}
	guard := executionControlGuard{callID, sdkID, call.ProjectID, hash, arguments}
	ctx = context.WithValue(ctx, executionControlKey{}, guard)
	meta := CommandMeta{Scope: call.ProjectID, CommandType: arguments.ActionID, IdempotencyKey: "agent-control:" + callID, RequestHash: hash}
	result.ProjectID = call.ProjectID
	if cached, hit, err := s.cachedExecutionControl(ctx, guard, meta); err != nil || hit {
		return cached, err
	}
	if arguments.TargetType == "run" {
		var snapshot RunSnapshot
		switch arguments.ActionID {
		case "pause_run":
			snapshot, err = s.RequestRunPause(ctx, PauseRunCommand{CommandMeta: meta, RunID: arguments.TargetID})
		case "resume_run":
			snapshot, err = s.ResumeRun(ctx, ResumeRunCommand{CommandMeta: meta, RunID: arguments.TargetID})
		case "cancel_run":
			snapshot, err = s.CancelRun(ctx, CancelRunCommand{CommandMeta: meta, RunID: arguments.TargetID, Confirmed: true, ActorRef: actorRefFromContext(ctx)})
		case "retry_failed_step":
			run, loadErr := s.GetRun(ctx, arguments.TargetID)
			if loadErr != nil {
				return result, loadErr
			}
			if run.CurrentStepRunID == nil {
				return result, domainError("RUN_STATE_CONFLICT", "运行没有当前步骤。")
			}
			snapshot, err = s.RetryFailedStep(ctx, RetryFailedStepCommand{CommandMeta: meta, StepRunID: *run.CurrentStepRunID})
		default:
			return result, domainError("EXECUTION_CONTROL_UNAVAILABLE", "该运行控制操作不可用。")
		}
		result.Status = snapshot.Run.Status
	} else {
		var task AgentTask
		switch arguments.ActionID {
		case "pause_agent_task":
			task, err = s.RequestAgentTaskPause(ctx, PauseAgentTaskCommand{CommandMeta: meta, AgentTaskID: arguments.TargetID, ActorRef: actorRefFromContext(ctx)})
		case "resume_agent_task":
			task, err = s.ResumeAgentTask(ctx, ResumeAgentTaskCommand{CommandMeta: meta, AgentTaskID: arguments.TargetID, ActorRef: actorRefFromContext(ctx)})
		case "cancel_agent_task":
			task, err = s.CancelAgentTask(ctx, CancelAgentTaskCommand{CommandMeta: meta, AgentTaskID: arguments.TargetID, ActorRef: actorRefFromContext(ctx)})
		case "retry_agent_task":
			task, err = s.RetryAgentTask(ctx, RetryAgentTaskCommand{CommandMeta: meta, AgentTaskID: arguments.TargetID, ActorRef: actorRefFromContext(ctx)})
		default:
			return result, domainError("EXECUTION_CONTROL_UNAVAILABLE", "该后台任务控制操作不可用。")
		}
		result.Status = task.Status
	}
	return result, err
}

// A committed command's receipt survives later target progress, including a
// completed Run no longer having a current step. It never authorizes a new write.
func (s *Store) cachedExecutionControl(ctx context.Context, guard executionControlGuard, meta CommandMeta) (ExecutionControlResult, bool, error) {
	result := ExecutionControlResult{ProjectID: guard.projectID, TargetType: guard.arguments.TargetType, TargetID: guard.arguments.TargetID, ActionID: guard.arguments.ActionID}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, false, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM idempotency_records WHERE scope=? AND command_type=? AND idempotency_key=?)`, meta.Scope, meta.CommandType, meta.IdempotencyKey).Scan(&exists); err != nil || !exists {
		return result, false, err
	}
	if err := guard.authorizeTx(ctx, s, tx); err != nil {
		return result, false, err
	}
	raw, hit, err := s.beginIdempotency(ctx, tx, meta)
	if err != nil || !hit {
		return result, false, err
	}
	if result.TargetType == "run" {
		snapshot, err := decodeIdempotentResult[RunSnapshot](raw)
		if err != nil || snapshot.Run.RunID != result.TargetID || snapshot.Run.ProjectID != result.ProjectID {
			return result, false, domainError("IDEMPOTENCY_RESULT_INVALID", "控制回执与目标不一致。")
		}
		result.Status = snapshot.Run.Status
	} else {
		task, err := decodeIdempotentResult[AgentTask](raw)
		if err != nil || task.AgentTaskID != result.TargetID || task.ProjectID != result.ProjectID {
			return result, false, domainError("IDEMPOTENCY_RESULT_INVALID", "控制回执与目标不一致。")
		}
		result.Status = task.Status
	}
	return result, true, nil
}

// The public buttons and SDK tools use the same state transition. The SDK path
// adds its approval and snapshot checks inside that transition's transaction.
func (s *Store) beginExecutionControlCommand(ctx context.Context, tx *sql.Tx, meta CommandMeta, projectID, targetType, targetID string) (json.RawMessage, bool, error) {
	guard, controlled := ctx.Value(executionControlKey{}).(executionControlGuard)
	if !controlled {
		if _, active := AgentActivityFromContext(ctx); active {
			return nil, false, domainError("AGENT_TOOL_APPROVAL_REQUIRED", "Agent 控制任务必须通过已批准的控制工具。")
		}
		if _, err := projectFilesWorkspace(ctx, tx, projectID, true); err != nil {
			return nil, false, err
		}
		if meta.Scope != "" && meta.Scope != projectID {
			return nil, false, domainError("REQUEST_VALIDATION_FAILED", "控制操作范围与作品不一致。")
		}
		cached, hit, err := s.beginIdempotency(ctx, tx, meta)
		if err != nil || !hit {
			return cached, hit, err
		}
		if targetType == "run" {
			snapshot, decodeErr := decodeIdempotentResult[RunSnapshot](cached)
			if decodeErr != nil || snapshot.Run.RunID != targetID || snapshot.Run.ProjectID != projectID {
				return nil, false, domainError("IDEMPOTENCY_RESULT_INVALID", "控制回执与目标不一致。")
			}
		} else {
			task, decodeErr := decodeIdempotentResult[AgentTask](cached)
			if decodeErr != nil || task.AgentTaskID != targetID || task.ProjectID != projectID {
				return nil, false, domainError("IDEMPOTENCY_RESULT_INVALID", "控制回执与目标不一致。")
			}
		}
		return cached, true, nil
	}
	if guard.projectID != projectID || guard.arguments.TargetType != targetType || guard.arguments.TargetID != targetID || guard.arguments.ActionID != meta.CommandType {
		return nil, false, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "控制目标与已确认工具参数不一致。")
	}
	if err := guard.authorizeTx(ctx, s, tx); err != nil {
		return nil, false, err
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, meta)
	if err != nil || hit {
		return cached, hit, err
	}
	target, err := s.executionControlTargetTx(ctx, tx, projectID, targetType, targetID)
	if err != nil {
		return nil, false, err
	}
	if target.SnapshotHash != guard.arguments.SnapshotHash {
		return nil, false, domainError("EXECUTION_CONTROL_STALE", "任务状态已经变化，请重新检查并确认控制操作。")
	}
	if !slices.ContainsFunc(target.AvailableActions, func(action AvailableAction) bool { return action.Enabled && action.ActionID == meta.CommandType }) {
		return nil, false, domainError("EXECUTION_CONTROL_UNAVAILABLE", "任务当前不允许该操作。")
	}
	return nil, false, nil
}

func (guard executionControlGuard) authorizeTx(ctx context.Context, s *Store, tx *sql.Tx) error {
	workspaceID, err := projectFilesWorkspace(ctx, tx, guard.projectID, true)
	if err != nil {
		return err
	}
	user, ok := identity.UserFromContext(ctx)
	if !ok {
		return domainError("ROLE_FORBIDDEN", "控制任务缺少用户身份。")
	}
	err = tx.QueryRowContext(ctx, `SELECT role FROM workspace_memberships WHERE workspace_id=? AND user_id=? AND status='active'`, workspaceID, user.UserID).Scan(&user.Role)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !user.Allows(identity.RoleEditor) {
		return domainError("ROLE_FORBIDDEN", "当前用户无权控制任务。")
	}
	if err != nil {
		return err
	}
	activity, ok := AgentActivityFromContext(ctx)
	if !ok || activity.ProjectID != guard.projectID {
		return domainError("AGENT_ACTIVITY_REQUIRED", "控制任务需要当前 Agent 执行身份。")
	}
	call, err := getAgentToolCallTx(ctx, tx, guard.callID)
	if err != nil {
		return err
	}
	if call.ToolID != "runtime:control_execution" || call.SDKToolCallID != guard.sdkID || call.Status != "running" || call.ArgumentsHash != guard.argumentHash {
		return domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "控制操作与已批准工具调用不匹配。")
	}
	approval, err := getAgentToolApprovalForCallTx(ctx, tx, guard.callID)
	if err != nil {
		return err
	}
	if approval.Status != "approved" {
		return domainError("AGENT_TOOL_APPROVAL_REQUIRED", "控制任务前需要用户批准。")
	}
	if err := s.validateAgentTaskToolCallTx(ctx, tx, guard.callID); err != nil {
		return err
	}
	if call.AgentTurnID != nil {
		if activity.AgentTurnID != *call.AgentTurnID {
			return domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "工具不属于当前对话执行。")
		}
		var status, userID string
		if err := tx.QueryRowContext(ctx, `SELECT status,user_id FROM agent_turns WHERE agent_turn_id=?`, *call.AgentTurnID).Scan(&status, &userID); err != nil {
			return err
		}
		if (status != "running" && status != "pausing") || userID != user.UserID {
			return domainError("AGENT_ACTIVITY_STALE", "对话已停止或执行用户不匹配。")
		}
	} else {
		return domainError("EXECUTION_CONTROL_UNAVAILABLE", "请在主对话中控制后台任务或运行，执行中的任务不能自行控制任务生命周期。")
	}
	return nil
}

func agentTaskReplayRisk(ctx context.Context, query rowQueryer, taskID string) (bool, error) {
	var risk bool
	err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_task_attempts a
		JOIN agent_task_tool_calls b ON b.agent_task_attempt_id=a.agent_task_attempt_id
		JOIN agent_tool_calls c ON c.agent_tool_call_id=b.agent_tool_call_id
		WHERE a.agent_task_id=? AND c.access_mode!='read' AND c.started_at IS NOT NULL)
		OR EXISTS(SELECT 1 FROM agent_task_run_states WHERE agent_task_id=?)
		OR EXISTS(SELECT 1 FROM agent_task_attempts WHERE agent_task_id=? AND error_code='AGENT_RUN_STATE_CORRUPT')`, taskID, taskID, taskID).Scan(&risk)
	return risk, err
}

func projectAgentTaskRetry(ctx context.Context, query rowQueryer, task *AgentTask) error {
	if task.Status != "failed" && task.Status != "cancelled" {
		return nil
	}
	risk, err := agentTaskReplayRisk(ctx, query, task.AgentTaskID)
	if err == nil && risk {
		task.RetryBlockedReason = "已有写入调用或执行恢复状态，请先核对已有结果，不能直接从头重新执行。"
	}
	return err
}
