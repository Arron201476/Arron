package runtime

import (
	"context"
	"database/sql"
	"encoding/json"

	"content-agent/backend/internal/identity"
)

func authorizeAgentToolApprovalCommandTx(ctx context.Context, tx *sql.Tx, approval AgentToolApproval, call AgentToolCall, scope string) error {
	if _, active := AgentActivityFromContext(ctx); active {
		return domainError("WORKSPACE_ACCESS_DENIED", "工具授权必须由用户确认，Agent 执行不能自行批准或拒绝。")
	}
	if principal, present := identity.FromContext(ctx); present && !principal.ValidUser() {
		return domainError("WORKSPACE_ACCESS_DENIED", "工具审批需要有效的用户身份。")
	}
	workspaceID, err := projectFilesWorkspace(ctx, tx, approval.ProjectID, true)
	if err != nil {
		return err
	}
	if scope != "" && scope != approval.ProjectID {
		return domainError("REQUEST_VALIDATION_FAILED", "工具审批范围与作品不一致。")
	}
	if approval.WorkspaceID != workspaceID || call.WorkspaceID != workspaceID || call.ProjectID != approval.ProjectID ||
		call.ConversationID != approval.ConversationID || call.AgentToolCallID != approval.AgentToolCallID {
		return domainError("AGENT_TOOL_APPROVAL_SUBJECT_CHANGED", "工具审批与调用归属不一致。")
	}
	return nil
}

func decodeAgentToolApprovalReceipt(cached json.RawMessage, approval AgentToolApproval, command ResolveAgentToolApprovalCommand) (AgentToolApproval, error) {
	receipt, err := decodeIdempotentResult[AgentToolApproval](cached)
	if err != nil {
		return AgentToolApproval{}, err
	}
	status := "approved"
	if command.Action == "reject" {
		status = "rejected"
	}
	var resolution struct {
		Action string `json:"action"`
	}
	if receipt.AgentToolApprovalID != approval.AgentToolApprovalID || receipt.AgentToolCallID != approval.AgentToolCallID ||
		receipt.ProjectID != approval.ProjectID || receipt.WorkspaceID != approval.WorkspaceID || receipt.ConversationID != approval.ConversationID ||
		receipt.SubjectSnapshotHash != command.SubjectSnapshotHash || receipt.Version != command.ExpectedVersion+1 ||
		receipt.Status != status || json.Unmarshal(receipt.Resolution, &resolution) != nil || resolution.Action != command.Action {
		return AgentToolApproval{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识的回执不属于当前工具审批。")
	}
	return receipt, nil
}
