package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"

	"content-agent/backend/internal/identity"
)

const updateSavedInstructionsTool = "runtime:update_saved_instructions"

type SavedInstructionArguments struct {
	Scope           string `json:"scope"`
	ExpectedVersion int    `json:"expected_version"`
	Content         string `json:"content"`
	Enabled         bool   `json:"enabled"`
}

func decodeSavedInstructionArguments(raw json.RawMessage) (SavedInstructionArguments, error) {
	var args SavedInstructionArguments
	var fields map[string]json.RawMessage
	if len(raw) > 64*1024 || json.Unmarshal(raw, &fields) != nil || len(fields) != 4 {
		return args, domainError("REQUEST_VALIDATION_FAILED", "规则更新参数不完整。")
	}
	for _, field := range []string{"scope", "expected_version", "content", "enabled"} {
		if len(fields[field]) == 0 || bytes.Equal(fields[field], []byte("null")) {
			return args, domainError("REQUEST_VALIDATION_FAILED", "规则更新参数不完整。")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&args) != nil || decoder.Decode(new(any)) != io.EOF {
		return args, domainError("REQUEST_VALIDATION_FAILED", "规则更新参数无效。")
	}
	return args, validateInstructionUpdate(args.command("validation", "validation"))
}

func (args SavedInstructionArguments) command(projectID, requestID string) UpdateAgentInstructionsCommand {
	return UpdateAgentInstructionsCommand{ProjectID: projectID, Scope: args.Scope, ExpectedVersion: args.ExpectedVersion, Content: args.Content, Enabled: args.Enabled, RequestID: requestID}
}

func (s *Store) instructionToolUserTx(ctx context.Context, tx *sql.Tx, projectID string) (identity.Principal, AgentActivityIdentity, error) {
	transport, ok := identity.FromContext(ctx)
	activity, active := AgentActivityFromContext(ctx)
	if !ok || transport.Kind != identity.KindService || !active || activity.ProjectID != projectID || activity.AllowTerminal {
		return identity.Principal{}, activity, domainError("AGENT_ACTIVITY_FORBIDDEN", "规则工具需要有效的内部执行身份。")
	}
	user, err := s.resolveAgentActivityPrincipalTx(ctx, tx, activity)
	if err != nil {
		return user, activity, err
	}
	if !user.Allows(identity.RoleEditor) {
		return user, activity, domainError("WORKSPACE_ACCESS_DENIED", "当前用户无权修改规则。")
	}
	return user, activity, nil
}

func (s *Store) prepareInstructionToolTx(ctx context.Context, tx *sql.Tx, command BeginAgentToolCallCommand) (json.RawMessage, error) {
	args, err := decodeSavedInstructionArguments(command.Arguments)
	if err != nil {
		return nil, err
	}
	user, activity, err := s.instructionToolUserTx(ctx, tx, command.ProjectID)
	if err != nil {
		return nil, err
	}
	if activity.AgentTurnID != command.AgentTurnID || activity.AgentTaskAttemptID != command.AgentTaskAttemptID || activity.ExecutionAttemptID != command.ExecutionAttemptID || activity.AttemptToken != command.AttemptToken {
		return nil, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "规则工具登记与当前执行身份不一致。")
	}
	if _, err := instructionScopeRef(user, args.Scope, command.ProjectID); err != nil {
		return nil, err
	}
	if args.Scope == "workspace" && !user.Allows(identity.RoleAdmin) {
		return nil, domainError("WORKSPACE_ACCESS_DENIED", "只有管理员可以修改工作区规则。")
	}
	return json.Marshal(map[string]any{"scope": args.Scope, "expected_version": args.ExpectedVersion, "enabled": args.Enabled,
		"content_hash": sha256Hex([]byte(args.Content)), "content_bytes": len(args.Content), "private_proposal": true})
}

func (s *Store) saveInstructionProposalTx(ctx context.Context, tx *sql.Tx, command BeginAgentToolCallCommand, callID, argumentHash string) error {
	user, _, err := s.instructionToolUserTx(ctx, tx, command.ProjectID)
	if err != nil {
		return err
	}
	if err := s.enforceWorkspaceQuotaTx(ctx, tx, user.WorkspaceID, "storage_bytes", int64(len(command.Arguments))); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_instruction_proposals(agent_tool_call_id,workspace_id,project_id,user_id,arguments_json,arguments_hash) VALUES(?,?,?,?,?,?)`,
		callID, user.WorkspaceID, command.ProjectID, user.UserID, string(command.Arguments), argumentHash)
	return err
}

type AgentInstructionProposal struct {
	AgentToolCallID string                    `json:"agent_tool_call_id"`
	ProjectID       string                    `json:"project_id"`
	UserID          string                    `json:"user_id"`
	ArgumentsHash   string                    `json:"arguments_hash"`
	Arguments       SavedInstructionArguments `json:"arguments"`
	Current         AgentInstructionDocument  `json:"current"`
	CanApprove      bool                      `json:"can_approve"`
	CanReject       bool                      `json:"can_reject"`
}

func instructionProposalQuery(ctx context.Context, tx *sql.Tx, callID string) (AgentInstructionProposal, string, error) {
	var result AgentInstructionProposal
	var workspace, raw string
	result.AgentToolCallID = callID
	err := tx.QueryRowContext(ctx, `SELECT workspace_id,project_id,user_id,arguments_json,arguments_hash FROM agent_instruction_proposals WHERE agent_tool_call_id=?`, callID).
		Scan(&workspace, &result.ProjectID, &result.UserID, &raw, &result.ArgumentsHash)
	if err == sql.ErrNoRows {
		return result, workspace, domainError("AGENT_INSTRUCTION_PROPOSAL_NOT_FOUND", "规则变更提案不存在。")
	}
	if err != nil {
		return result, workspace, err
	}
	result.Arguments, err = decodeSavedInstructionArguments(json.RawMessage(raw))
	if err != nil {
		return result, workspace, domainError("AGENT_INSTRUCTIONS_INVALID", "规则提案内容无效。")
	}
	_, hash, _, err := summarizeAgentToolPayload(json.RawMessage(raw), true)
	if err != nil || hash != result.ArgumentsHash {
		return result, workspace, domainError("AGENT_INSTRUCTIONS_INVALID", "规则提案完整性校验失败。")
	}
	return result, workspace, nil
}

func (s *Store) instructionProposalApproverTx(ctx context.Context, tx *sql.Tx, callID string) (AgentInstructionProposal, identity.Principal, error) {
	proposal, workspace, err := instructionProposalQuery(ctx, tx, callID)
	if err != nil {
		return proposal, identity.Principal{}, err
	}
	transport, ok := identity.FromContext(ctx)
	if !ok || !transport.ValidUser() {
		return proposal, identity.Principal{}, domainError("WORKSPACE_ACCESS_DENIED", "只有本人可以查看或确认规则变更提案。")
	}
	user, err := instructionUserQuery(ctx, tx, proposal.ProjectID, false)
	if err != nil {
		return proposal, user, err
	}
	if user.UserID != proposal.UserID || user.WorkspaceID != workspace {
		return AgentInstructionProposal{}, user, domainError("WORKSPACE_ACCESS_DENIED", "只有发起用户可以查看或确认规则变更提案。")
	}
	ref, err := instructionScopeRef(user, proposal.Arguments.Scope, proposal.ProjectID)
	if err != nil {
		return proposal, user, err
	}
	proposal.Current, err = instructionDocumentQuery(ctx, tx, workspace, proposal.Arguments.Scope, ref, 0)
	if err != nil {
		return proposal, user, err
	}
	proposal.CanApprove = user.Allows(identity.RoleEditor) && (proposal.Arguments.Scope != "workspace" || user.Allows(identity.RoleAdmin)) && proposal.Current.Version == proposal.Arguments.ExpectedVersion
	proposal.CanReject = user.Allows(identity.RoleEditor)
	return proposal, user, nil
}

func (s *Store) GetAgentInstructionProposal(ctx context.Context, callID string) (AgentInstructionProposal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentInstructionProposal{}, err
	}
	defer tx.Rollback()
	proposal, _, err := s.instructionProposalApproverTx(ctx, tx, callID)
	return proposal, err
}

func (s *Store) ApplyAgentInstructionTool(ctx context.Context, callID, sdkID string, raw json.RawMessage) (AgentInstructionDocument, error) {
	args, err := decodeSavedInstructionArguments(raw)
	if err != nil {
		return AgentInstructionDocument{}, err
	}
	_, hash, _, err := summarizeAgentToolPayload(raw, true)
	if err != nil {
		return AgentInstructionDocument{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentInstructionDocument{}, err
	}
	defer tx.Rollback()
	call, err := getAgentToolCallTx(ctx, tx, callID)
	if err != nil {
		return AgentInstructionDocument{}, err
	}
	user, activity, err := s.instructionToolUserTx(ctx, tx, call.ProjectID)
	if err != nil {
		return AgentInstructionDocument{}, err
	}
	if err := validateAgentActivityToolCallQuery(ctx, tx, activity, callID); err != nil {
		return AgentInstructionDocument{}, err
	}
	if call.ToolID != updateSavedInstructionsTool || call.SDKToolCallID != sdkID || call.Status != "running" || call.ArgumentsHash != hash {
		return AgentInstructionDocument{}, domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "规则更新与已批准调用不一致。")
	}
	approval, err := getAgentToolApprovalForCallTx(ctx, tx, callID)
	if err != nil {
		return AgentInstructionDocument{}, err
	}
	if approval.Status != "approved" {
		return AgentInstructionDocument{}, domainError("AGENT_TOOL_APPROVAL_REQUIRED", "规则更新需要本人确认。")
	}
	proposal, workspace, err := instructionProposalQuery(ctx, tx, callID)
	if err != nil {
		return AgentInstructionDocument{}, err
	}
	if proposal.UserID != user.UserID || workspace != user.WorkspaceID || proposal.ArgumentsHash != hash || proposal.Arguments != args {
		return AgentInstructionDocument{}, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "规则更新不属于当前执行用户。")
	}
	doc, err := s.updateAgentInstructionsTx(ctx, tx, user, args.command(call.ProjectID, callID))
	if err != nil {
		return AgentInstructionDocument{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentInstructionDocument{}, err
	}
	return doc, nil
}

// SDK function tools can return validation errors as ordinary tool output.
// A completed write must have an independent durable mutation receipt.
func verifyInstructionWriteReceiptTx(ctx context.Context, tx *sql.Tx, call AgentToolCall) error {
	proposal, workspace, err := instructionProposalQuery(ctx, tx, call.AgentToolCallID)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(proposal.Arguments.command(call.ProjectID, call.AgentToolCallID))
	if err != nil {
		return err
	}
	ref := workspace
	if proposal.Arguments.Scope == "user" {
		ref = proposal.UserID
	} else if proposal.Arguments.Scope == "project" {
		ref = proposal.ProjectID
	}
	var found bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_instruction_versions WHERE workspace_id=? AND scope=? AND scope_ref=? AND version=? AND request_id=? AND request_hash=? AND updated_by=?)`,
		workspace, proposal.Arguments.Scope, ref, proposal.Arguments.ExpectedVersion+1, call.AgentToolCallID, sha256Hex(encoded), proposal.UserID).Scan(&found)
	if err != nil {
		return err
	}
	if !found || workspace != call.WorkspaceID || proposal.ProjectID != call.ProjectID || proposal.ArgumentsHash != call.ArgumentsHash {
		return domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "规则写入尚无持久回执，不能标记为已保存。")
	}
	return nil
}
