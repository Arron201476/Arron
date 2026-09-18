package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

// AgentTurnSubmission keeps the immutable acceptance separate from later queue
// edits. The transport must verify RequestHash before returning Turn to a client.
type AgentTurnSubmission struct {
	RequestHash string
	Request     agentcontract.MessageRequest
	Turn        AgentTurn
}

func authorizeAgentTurnSubmissionTx(ctx context.Context, tx *sql.Tx, conversationID string) (string, string, error) {
	if _, active := AgentActivityFromContext(ctx); active {
		return "", "", domainError("ROLE_FORBIDDEN", "Agent 不能代替用户提交新的对话消息。")
	}
	if principal, present := identity.FromContext(ctx); present && !principal.ValidUser() {
		return "", "", domainError("ROLE_FORBIDDEN", "对话提交需要有效的用户身份。")
	}
	var projectID, workspaceID string
	err := tx.QueryRowContext(ctx, `
		SELECT c.project_id, p.workspace_id FROM conversations c
		JOIN projects p ON p.project_id=c.project_id
		WHERE c.conversation_id=? AND p.deleted_at IS NULL`, conversationID).Scan(&projectID, &workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", domainError("CONVERSATION_NOT_FOUND", "对话不存在。")
	}
	if err != nil {
		return "", "", err
	}
	if principal, ok := identity.UserFromContext(ctx); ok {
		if principal.WorkspaceID != workspaceID {
			return "", "", domainError("CONVERSATION_NOT_FOUND", "对话不存在。")
		}
		if !principal.Allows(identity.RoleEditor) {
			return "", "", domainError("ROLE_FORBIDDEN", "当前用户没有提交任务的权限。")
		}
		if err := validateExecutionOwnerTx(ctx, tx, executionToolState{WorkspaceID: workspaceID, UserID: principal.UserID}); err != nil {
			return "", "", err
		}
	}
	return projectID, workspaceID, nil
}

func readAcceptedAgentTurnTx(ctx context.Context, tx *sql.Tx, raw json.RawMessage, projectID, workspaceID, conversationID, key string) (AgentTurn, AgentTurn, error) {
	accepted, err := decodeIdempotentResult[AgentTurn](raw)
	if err != nil {
		return AgentTurn{}, AgentTurn{}, err
	}
	turn, err := scanAgentTurn(tx.QueryRowContext(ctx, agentTurnSelect+`
		WHERE agent_turn_id=? AND workspace_id=? AND project_id=? AND conversation_id=? AND user_id=? AND idempotency_key=?`,
		accepted.AgentTurnID, workspaceID, projectID, conversationID, identity.UserIDFromContext(ctx), key))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTurn{}, AgentTurn{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识不属于当前用户的原消息。")
	}
	if err != nil {
		return AgentTurn{}, AgentTurn{}, err
	}
	if accepted.WorkspaceID != turn.WorkspaceID || accepted.ProjectID != turn.ProjectID || accepted.ConversationID != turn.ConversationID || accepted.UserID != turn.UserID || !accepted.CreatedAt.Equal(turn.CreatedAt) || accepted.Status != "accepted" || (accepted.SubmissionID != "" && accepted.SubmissionID != turn.SubmissionID) {
		return AgentTurn{}, AgentTurn{}, domainError("IDEMPOTENCY_RESULT_INVALID", "原消息回执与执行记录不一致。")
	}
	return accepted, turn, nil
}

// LookupAgentTurnSubmission is read-only and rechecks current authorization.
// It never validates against today's Skill catalog or creates a new command.
func (s *Store) LookupAgentTurnSubmission(ctx context.Context, conversationID, key string) (string, *AgentTurnSubmission, error) {
	if key == "" {
		return "", nil, domainError("IDEMPOTENCY_METADATA_INVALID", "原消息请求标识缺失。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	projectID, workspaceID, err := authorizeAgentTurnSubmissionTx(ctx, tx, conversationID)
	if err != nil {
		return "", nil, err
	}
	var hash, status string
	var raw sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT request_hash,status,response_json
		FROM idempotency_records WHERE scope=? AND command_type='create_message' AND idempotency_key=?`, projectID, key).
		Scan(&hash, &status, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_turns WHERE project_id=? AND idempotency_key=?)`, projectID, key).Scan(&exists); err != nil {
			return "", nil, err
		}
		if exists {
			return "", nil, domainError("IDEMPOTENCY_RESULT_INVALID", "原消息已存在但提交回执缺失，未创建新消息。")
		}
		return projectID, nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if status != "completed" {
		// An incomplete acceptance is never evidence that the key is unused.
		return "", nil, domainError("COMMAND_IN_PROGRESS", "原消息的提交结果尚未确认，未创建新消息。")
	}
	if hash == "" || !raw.Valid {
		return "", nil, domainError("IDEMPOTENCY_RESULT_INVALID", "原消息提交回执不完整。")
	}
	accepted, turn, err := readAcceptedAgentTurnTx(ctx, tx, json.RawMessage(raw.String), projectID, workspaceID, conversationID, key)
	if err != nil {
		return "", nil, err
	}
	return projectID, &AgentTurnSubmission{RequestHash: hash, Request: accepted.Request, Turn: turn}, nil
}
