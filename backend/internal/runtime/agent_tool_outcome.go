package runtime

import (
	"context"
	"database/sql"
)

func executionToolSelection(mode string) (string, error) {
	switch mode {
	case "conversation":
		return `c.agent_turn_id=?`, nil
	case "background_task":
		return `c.agent_tool_call_id IN (SELECT agent_tool_call_id FROM agent_task_tool_calls WHERE agent_task_attempt_id=?)`, nil
	case "stateful_workflow":
		return `c.agent_tool_call_id IN (SELECT agent_tool_call_id FROM execution_tool_calls WHERE execution_attempt_id=?)`, nil
	default:
		return "", domainError("REQUEST_VALIDATION_FAILED", "执行类型无效。")
	}
}

// MCP errors do not prove that the remote operation was rolled back. Keep this
// check in the result transaction so concurrent tool starts cannot race success.
func validateExternalToolOutcomesTx(ctx context.Context, tx *sql.Tx, mode, id string) error {
	selection, err := executionToolSelection(mode)
	if err != nil {
		return err
	}
	var unresolved bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_tool_calls c WHERE (`+selection+`) AND
		c.tool_kind='mcp' AND c.access_mode!='read' AND
		c.status IN ('pending_approval','approved','running'))`, id).Scan(&unresolved)
	if err != nil {
		return err
	}
	if unresolved {
		return domainError("SDK_TOOL_OUTCOME_UNRESOLVED", "外部工具尚未结束或写入结果未确认，不能提交成功；请先核对原操作。")
	}
	calls, err := uncertainExternalCallsTx(ctx, tx, mode, id)
	if err != nil {
		return err
	}
	for _, call := range calls {
		resolved, err := reviewedExternalInputTx(ctx, tx, call, mode, id, false)
		if err != nil {
			return err
		}
		if !resolved {
			return domainError(externalToolRecoveryCode, "外部写入仍未核对，或核对事实尚未被原执行领取，不能提交成功。")
		}
	}
	return nil
}
