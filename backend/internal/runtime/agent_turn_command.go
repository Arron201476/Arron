package runtime

import (
	"context"
	"database/sql"
	"encoding/json"

	"content-agent/backend/internal/identity"
)

func authorizeAgentTurnCommandTx(ctx context.Context, tx *sql.Tx, turn AgentTurn, scope string, authorOnly bool) error {
	if _, active := AgentActivityFromContext(ctx); active {
		return domainError("ROLE_FORBIDDEN", "Agent 不能代替用户控制已提交的对话消息。")
	}
	if principal, present := identity.FromContext(ctx); present && !principal.ValidUser() {
		return domainError("ROLE_FORBIDDEN", "对话控制需要有效的用户身份。")
	}
	workspaceID, err := projectFilesWorkspace(ctx, tx, turn.ProjectID, true)
	if err != nil {
		return err
	}
	if workspaceID != turn.WorkspaceID {
		return domainError("AGENT_TURN_CONTEXT_MISMATCH", "对话执行与作品归属不一致。")
	}
	if authorOnly {
		if user, ok := identity.UserFromContext(ctx); ok && user.UserID != turn.UserID {
			return domainError("ROLE_FORBIDDEN", "只能编辑、暂停或继续本人提交的对话消息。")
		}
	}
	if scope != "" && scope != turn.AgentTurnID {
		return domainError("REQUEST_VALIDATION_FAILED", "对话命令范围与执行不一致。")
	}
	return nil
}

func decodeAgentTurnCommandReceipt(cached json.RawMessage, turn AgentTurn) (AgentTurn, error) {
	receipt, err := decodeIdempotentResult[AgentTurn](cached)
	if err != nil {
		return AgentTurn{}, err
	}
	if receipt.AgentTurnID != turn.AgentTurnID || receipt.ProjectID != turn.ProjectID || receipt.WorkspaceID != turn.WorkspaceID || receipt.UserID != turn.UserID || receipt.ConversationID != turn.ConversationID {
		return AgentTurn{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识的回执不属于当前对话执行。")
	}
	return receipt, nil
}
