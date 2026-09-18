package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type PauseAgentTaskCommand struct {
	CommandMeta
	AgentTaskID string
	ActorRef    string
}

type ResumeAgentTaskCommand struct {
	CommandMeta
	AgentTaskID string
	ActorRef    string
}

func (s *Store) RequestAgentTaskPause(ctx context.Context, command PauseAgentTaskCommand) (AgentTask, error) {
	return s.controlAgentTaskPause(ctx, command.CommandMeta, command.AgentTaskID, command.ActorRef, false)
}

func (s *Store) ResumeAgentTask(ctx context.Context, command ResumeAgentTaskCommand) (AgentTask, error) {
	return s.controlAgentTaskPause(ctx, command.CommandMeta, command.AgentTaskID, command.ActorRef, true)
}

func (s *Store) controlAgentTaskPause(ctx context.Context, meta CommandMeta, taskID, actor string, resume bool) (AgentTask, error) {
	if taskID == "" {
		return AgentTask{}, domainError("REQUEST_VALIDATION_FAILED", "缺少后台任务 ID。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTask{}, err
	}
	defer tx.Rollback()
	task, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id=?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTask{}, domainError("AGENT_TASK_NOT_FOUND", "后台任务不存在。")
	}
	if err != nil {
		return AgentTask{}, err
	}
	if _, err := projectFilesWorkspace(ctx, tx, task.ProjectID, true); err != nil {
		return AgentTask{}, err
	}
	if meta.Scope == "" {
		meta.Scope = task.ProjectID
	}
	cached, hit, err := s.beginExecutionControlCommand(ctx, tx, meta, task.ProjectID, "agent_task", taskID)
	if err != nil {
		return AgentTask{}, err
	}
	if hit {
		return decodeIdempotentResult[AgentTask](cached)
	}
	now := s.now()
	status, message, event := "paused", "已暂停", "agent_task.paused"
	if resume {
		if task.Status != "paused" {
			return AgentTask{}, domainError("AGENT_TASK_STATE_CONFLICT", "后台任务尚未暂停，不能继续。")
		}
		entry, exists, err := s.capabilityEntryForInvocationQuery(ctx, tx, task.SkillInvocationID)
		if err != nil {
			return AgentTask{}, err
		}
		if _, err := validateBackgroundTaskEntry(entry, exists, task.CapabilityVersion); err != nil {
			return AgentTask{}, err
		}
		if err := authorizePersonalSkillExecution(ctx, entry); err != nil {
			return AgentTask{}, err
		}
		status, message, event = "queued", "等待继续执行", "agent_task.resume_requested"
		var pendingJSON, stateJSON, stateHash, attemptID string
		err = tx.QueryRowContext(ctx, `SELECT pending_sdk_tool_call_ids_json, state_json, state_hash, agent_task_attempt_id
			FROM agent_task_run_states WHERE agent_task_id=?`, taskID).Scan(&pendingJSON, &stateJSON, &stateHash, &attemptID)
		if errors.Is(err, sql.ErrNoRows) {
			var active int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_task_attempts WHERE agent_task_id=? AND status IN ('running','paused','waiting_approval')`, taskID).Scan(&active); err != nil {
				return AgentTask{}, err
			}
			if active > 0 {
				return AgentTask{}, domainError("AGENT_RUN_STATE_CORRUPT", "已暂停的执行缺少恢复状态，不能从头重跑。")
			}
		} else if err != nil {
			return AgentTask{}, err
		} else {
			if sha256Hex([]byte(stateJSON)) != stateHash {
				return AgentTask{}, domainError("AGENT_RUN_STATE_CORRUPT", "后台任务恢复状态校验失败。")
			}
			var pendingIDs []string
			if err := json.Unmarshal([]byte(pendingJSON), &pendingIDs); err != nil {
				return AgentTask{}, err
			}
			var attemptStatus string
			if err := tx.QueryRowContext(ctx, `SELECT status FROM agent_task_attempts WHERE agent_task_attempt_id=? AND agent_task_id=?`, attemptID, taskID).Scan(&attemptStatus); err != nil {
				return AgentTask{}, err
			}
			if attemptStatus != "paused" && attemptStatus != "waiting_approval" {
				return AgentTask{}, domainError("AGENT_TASK_STATE_CONFLICT", "后台任务恢复尝试状态不一致。")
			}
			if err := s.prepareExternalToolResumeTx(ctx, tx, "background_task", attemptID, json.RawMessage(stateJSON)); err != nil {
				return AgentTask{}, err
			}
			for _, id := range pendingIDs {
				var approval string
				if err := tx.QueryRowContext(ctx, `SELECT a.status FROM agent_tool_approvals a
					JOIN agent_tool_calls c ON c.agent_tool_call_id=a.agent_tool_call_id
					JOIN agent_task_tool_calls b ON b.agent_tool_call_id=c.agent_tool_call_id
					WHERE b.agent_task_attempt_id=? AND c.sdk_tool_call_id=?`, attemptID, id).Scan(&approval); err != nil {
					return AgentTask{}, err
				}
				if approval == "pending" {
					status, message = "waiting_approval", "等待工具授权"
				} else if approval != "approved" && approval != "rejected" {
					return AgentTask{}, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "恢复状态中的工具授权已经失效。")
				}
			}
		}
	} else {
		switch task.Status {
		case "running":
			status, message, event = "pausing", "等待当前轮完成后暂停", "agent_task.pause_requested"
		case "pausing":
			var inputPause bool
			if err := tx.QueryRowContext(ctx, `SELECT input_pause_requested FROM agent_tasks WHERE agent_task_id=?`, taskID).Scan(&inputPause); err != nil {
				return AgentTask{}, err
			}
			if !inputPause {
				return AgentTask{}, domainError("AGENT_TASK_STATE_CONFLICT", "后台任务已经在暂停中。")
			}
			status, message, event = "pausing", "等待当前轮完成后暂停", "agent_task.pause_requested"
		case "queued", "waiting_approval":
		default:
			return AgentTask{}, domainError("AGENT_TASK_STATE_CONFLICT", "后台任务当前不能暂停。")
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_tasks SET status=?, input_pause_requested=0, progress_message=?, queued_at=?, updated_at=? WHERE agent_task_id=?`, status, message, formatTime(now), formatTime(now), taskID); err != nil {
		return AgentTask{}, err
	}
	if _, err := s.appendEvent(ctx, tx, task.ProjectID, nil, nil, event, "agent_task", taskID, map[string]any{"actor_ref": actor}); err != nil {
		return AgentTask{}, err
	}
	task, err = scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id=?`, taskID))
	if err != nil {
		return AgentTask{}, err
	}
	if err := completeIdempotency(ctx, tx, meta, task, now); err != nil {
		return AgentTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTask{}, err
	}
	return task, nil
}
