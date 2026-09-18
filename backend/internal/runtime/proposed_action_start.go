package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"content-agent/backend/internal/capability"
)

func loadProposedActionTx(ctx context.Context, tx *sql.Tx, id string) (ProposedAction, error) {
	action, err := scanProposedAction(tx.QueryRowContext(ctx, `
		SELECT proposed_action_id, agent_decision_id, project_id, conversation_id,
			confirmation_message_id, action_type, version, status, capability_id,
			capability_version, input_json, config_json, snapshot_hash,
			requires_confirmation, consumed_run_id, consumed_task_id, created_at, updated_at
		FROM proposed_actions WHERE proposed_action_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ProposedAction{}, domainError("PROPOSED_ACTION_NOT_FOUND", "待确认动作不存在。")
	}
	return action, err
}

func (s *Store) authorizeProposedActionStartTx(ctx context.Context, tx *sql.Tx, projectID, conversationID, actionID, capabilityID, version, scope string) (ProposedAction, error) {
	action, err := loadProposedActionTx(ctx, tx, actionID)
	if err != nil {
		return ProposedAction{}, err
	}
	if err := authorizeProposedActionMutationTx(ctx, tx, action, scope); err != nil {
		return ProposedAction{}, err
	}
	if action.ProjectID != projectID || action.ConversationID != conversationID {
		return ProposedAction{}, domainError("RESOURCE_PROJECT_MISMATCH", "确认卡不属于当前作品和对话。")
	}
	if action.CapabilityRef == nil || action.CapabilityRef.CapabilityID != capabilityID || action.CapabilityRef.Version != version {
		return ProposedAction{}, domainError("CONFIRMATION_ACTION_MISMATCH", "确认的 Skill 与启动请求不一致。")
	}
	entry, ok, err := s.capabilityEntryForProposedActionQuery(ctx, tx, projectID, actionID, capabilityID, version)
	if err != nil {
		return ProposedAction{}, err
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil || entry.Definition.Version != version {
		return ProposedAction{}, domainError("CAPABILITY_UNAVAILABLE", "请求的能力当前不可用。")
	}
	return action, nil
}

func decodeProposedRunReceipt(cached json.RawMessage, action ProposedAction) (RunSnapshot, error) {
	receipt, err := decodeIdempotentResult[RunSnapshot](cached)
	if err != nil {
		return RunSnapshot{}, err
	}
	if action.Status != "consumed" || action.ActionType != "start_run" || action.ConsumedRunID == nil ||
		*action.ConsumedRunID != receipt.Run.RunID || receipt.Run.ProjectID != action.ProjectID || receipt.Run.ConversationID != action.ConversationID {
		return RunSnapshot{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识已用于其他启动动作。")
	}
	return receipt, nil
}

func decodeProposedTaskReceipt(cached json.RawMessage, action ProposedAction) (AgentTask, error) {
	receipt, err := decodeIdempotentResult[AgentTask](cached)
	if err != nil {
		return AgentTask{}, err
	}
	if action.Status != "consumed" || action.ActionType != "start_background_task" || action.ConsumedTaskID == nil ||
		*action.ConsumedTaskID != receipt.AgentTaskID || receipt.ProjectID != action.ProjectID || receipt.ConversationID != action.ConversationID {
		return AgentTask{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识已用于其他启动动作。")
	}
	return receipt, nil
}
