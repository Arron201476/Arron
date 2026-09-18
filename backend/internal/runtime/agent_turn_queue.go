package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

type UpdateQueuedAgentTurnCommand struct {
	CommandMeta
	AgentTurnID     string
	ExpectedContent string
	Content         string
}

// Queue edits change only an unstarted user request, never a native SDK checkpoint.
func (s *Store) UpdateQueuedAgentTurn(ctx context.Context, command UpdateQueuedAgentTurnCommand) (AgentTurn, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTurn{}, err
	}
	defer tx.Rollback()
	turn, err := scanAgentTurn(tx.QueryRowContext(ctx, agentTurnSelect+` WHERE agent_turn_id=?`, command.AgentTurnID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTurn{}, domainError("AGENT_TURN_NOT_FOUND", "消息不存在。")
	}
	if err != nil {
		return AgentTurn{}, err
	}
	if err := authorizeAgentTurnCommandTx(ctx, tx, turn, command.Scope, true); err != nil {
		return AgentTurn{}, err
	}
	if command.Scope == "" {
		command.Scope = turn.AgentTurnID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return AgentTurn{}, err
	}
	if hit {
		receipt, err := decodeAgentTurnCommandReceipt(cached, turn)
		if err != nil {
			return AgentTurn{}, err
		}
		if receipt.Request.Content != strings.TrimSpace(command.Content) {
			return AgentTurn{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识不属于当前消息修改。")
		}
		return turn, nil
	}
	var checkpoints int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_turn_run_states WHERE agent_turn_id=?`, turn.AgentTurnID).Scan(&checkpoints); err != nil {
		return AgentTurn{}, err
	}
	if turn.Status != "accepted" || turn.StartedAt != nil || checkpoints != 0 {
		return AgentTurn{}, domainError("AGENT_TURN_STATE_CONFLICT", "消息已开始处理或不在队列中，不能修改。")
	}
	if turn.Request.Content != command.ExpectedContent {
		return AgentTurn{}, domainError("AGENT_TURN_CONTENT_CONFLICT", "排队消息已被修改，请刷新后核对。")
	}
	turn.Request.Content = command.Content
	if err := validateMessageEnvelope(&turn.Request); err != nil {
		return AgentTurn{}, err
	}
	requestJSON, err := json.Marshal(turn.Request)
	if err != nil {
		return AgentTurn{}, err
	}
	turn.UpdatedAt = s.now()
	result, err := tx.ExecContext(ctx, `UPDATE agent_turns SET request_json=?, request_hash=?, updated_at=?
		WHERE agent_turn_id=? AND status='accepted' AND started_at IS NULL`, string(requestJSON), sha256Hex(requestJSON), formatTime(turn.UpdatedAt), turn.AgentTurnID)
	if err != nil {
		return AgentTurn{}, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return AgentTurn{}, err
		}
		return AgentTurn{}, domainError("AGENT_TURN_STATE_CONFLICT", "消息已开始处理，不能修改。")
	}
	if _, err := s.appendAgentTurnEventTx(ctx, tx, turn, "agent.turn.queued_updated", false, map[string]any{"status": turn.Status}); err != nil {
		return AgentTurn{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, turn, turn.UpdatedAt); err != nil {
		return AgentTurn{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTurn{}, err
	}
	return turn, nil
}
