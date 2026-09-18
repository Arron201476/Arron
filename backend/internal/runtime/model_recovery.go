package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
)

const modelRecoveryReason = "model_temporary_failure"
const modelRecoveryCode = "SDK_MODEL_RECOVERY_REQUIRED"
const modelRecoveryMessage = "模型连接暂时失败，原执行状态已保留；可继续原执行，不会从头重新生成。"

func validateModelRecoveryCheckpoint(command PauseAgentTurnForApprovalCommand) (bool, error) {
	if command.RecoveryReason == "" {
		return false, nil
	}
	var state struct {
		CurrentTurn int `json:"current_turn"`
		MaxTurns    int `json:"max_turns"`
	}
	if (command.RecoveryReason != modelRecoveryReason && command.RecoveryReason != externalToolRecoveryReason) || len(command.PendingSDKToolCallIDs) != 0 ||
		json.Unmarshal(command.RunState, &state) != nil || state.CurrentTurn <= 0 || state.MaxTurns <= state.CurrentTurn {
		return false, domainError("REQUEST_VALIDATION_FAILED", "模型失败恢复 checkpoint 无效或原执行预算已耗尽。")
	}
	return true, nil
}

func recoveryDetails(reason string) (string, string) {
	if reason == externalToolRecoveryReason {
		return externalToolRecoveryCode, "外部操作结果未确认，原执行已暂停；请逐项核对原操作，再明确继续。"
	}
	return modelRecoveryCode, modelRecoveryMessage
}

func validateRecoveryToolsTx(ctx context.Context, tx *sql.Tx, mode, id string, command PauseAgentTurnForApprovalCommand) error {
	if command.RecoveryReason == externalToolRecoveryReason {
		return validateExternalToolCheckpointTx(ctx, tx, mode, id, command.RunState)
	}
	return validateModelRecoveryToolsTx(ctx, tx, mode, id)
}

// A native model-boundary checkpoint is insufficient when the platform still
// has an unfinished tool or cannot establish the outcome of an issued write.
func validateModelRecoveryToolsTx(ctx context.Context, tx *sql.Tx, mode, id string) error {
	if err := validateAgentMemoryCommitTx(ctx, tx, mode, id); err != nil {
		return err
	}
	selection, err := executionToolSelection(mode)
	if err != nil {
		return err
	}
	var unsafe bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_tool_calls c WHERE (`+selection+`) AND
		(c.status IN ('running','pending_approval','approved') OR (c.tool_kind!='mcp' AND c.access_mode!='read' AND c.started_at IS NOT NULL AND c.status!='completed'
		AND NOT (c.tool_id='runtime:commit_agent_action' AND c.status='failed' AND COALESCE(c.error_code,'')='RUNTIME_COMMIT_DEFERRED'))))`, id).Scan(&unsafe)
	if err != nil {
		return err
	}
	if unsafe {
		return domainError("SDK_TOOL_REPLAY_RISK", "工具尚未完成或已发出写入的结果未确认，不能直接继续；请先核对已有操作。")
	}
	if err := validateExternalToolOutcomesTx(ctx, tx, mode, id); err != nil {
		return domainError("SDK_TOOL_REPLAY_RISK", "外部写入尚未完成核对与原执行输入绑定，不能直接继续。")
	}
	return nil
}
