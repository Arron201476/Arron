package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const delegateSubtaskTool = "runtime:delegate_subtask"
const maxExecutionSubtasks = 8

func validateSubtaskRequest(arguments json.RawMessage) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(arguments, &fields) != nil || len(fields) != 3 {
		return domainError("REQUEST_VALIDATION_FAILED", "子任务需要标题、明确任务和材料。")
	}
	for field, limit := range map[string]int{"title": 120, "task": 16000, "materials": 64000} {
		var value string
		if raw := fields[field]; len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &value) != nil ||
			utf8.RuneCountInString(value) > limit || strings.ContainsRune(value, 0) || (field != "materials" && strings.TrimSpace(value) == "") {
			return domainError("REQUEST_VALIDATION_FAILED", "子任务字段缺失、类型错误或超出长度限制。")
		}
	}
	return nil
}

func validateSubtaskBudgetTx(ctx context.Context, tx *sql.Tx, command BeginAgentToolCallCommand) error {
	if command.ToolID != delegateSubtaskTool {
		return nil
	}
	if err := validateSubtaskRequest(command.Arguments); err != nil {
		return err
	}
	identities := 0
	for _, id := range []string{command.AgentTurnID, command.AgentTaskAttemptID, command.ExecutionAttemptID} {
		if id != "" {
			identities++
		}
	}
	if identities != 1 {
		return domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "子任务必须绑定一个确切的原执行。")
	}
	if command.AgentTurnID != "" {
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_turns WHERE agent_turn_id=? AND project_id=? AND conversation_id=? AND status IN ('running','pausing'))`, command.AgentTurnID, command.ProjectID, command.ConversationID).Scan(&active); err != nil {
			return err
		}
		if !active {
			return domainError("AGENT_ACTIVITY_STALE", "子任务必须属于正在执行的对话。")
		}
	}
	query := `SELECT COUNT(*) FROM agent_tool_calls c WHERE c.project_id=? AND c.tool_id=? AND c.agent_turn_id=?`
	identity := command.AgentTurnID
	if command.AgentTaskAttemptID != "" {
		query = `SELECT COUNT(*) FROM agent_tool_calls c JOIN agent_task_tool_calls b ON b.agent_tool_call_id=c.agent_tool_call_id
			WHERE c.project_id=? AND c.tool_id=? AND b.agent_task_attempt_id=?`
		identity = command.AgentTaskAttemptID
	} else if command.ExecutionAttemptID != "" {
		query = `SELECT COUNT(*) FROM agent_tool_calls c JOIN execution_tool_calls b ON b.agent_tool_call_id=c.agent_tool_call_id
			WHERE c.project_id=? AND c.tool_id=? AND b.execution_attempt_id=?`
		identity = command.ExecutionAttemptID
	}
	var count int
	if err := tx.QueryRowContext(ctx, query, command.ProjectID, delegateSubtaskTool, identity).Scan(&count); err != nil {
		return err
	}
	if count >= maxExecutionSubtasks {
		return domainError("AGENT_SUBTASK_LIMIT", "本次执行已达到 8 个子任务的上限，请汇总已有结果。")
	}
	return nil
}
