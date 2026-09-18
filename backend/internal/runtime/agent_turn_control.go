package runtime

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type ControlAgentTurnCommand struct {
	CommandMeta
	AgentTurnID string
}

func validateAgentTurnToolStartTx(ctx context.Context, tx *sql.Tx, turnID string) error {
	if turnID == "" {
		return nil
	}
	var inactive bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_turns WHERE agent_turn_id=? AND status NOT IN ('running','pausing'))`, turnID).Scan(&inactive); err != nil {
		return err
	}
	if inactive {
		return domainError("AGENT_ACTIVITY_STALE", "本轮已暂停、正在提交或已结束，不能启动工具。")
	}
	return nil
}

func (s *Store) RequestAgentTurnPause(ctx context.Context, command ControlAgentTurnCommand) (AgentTurn, error) {
	return s.controlAgentTurnPause(ctx, command, false)
}

func (s *Store) ResumeAgentTurn(ctx context.Context, command ControlAgentTurnCommand) (AgentTurn, error) {
	return s.controlAgentTurnPause(ctx, command, true)
}

func (s *Store) controlAgentTurnPause(ctx context.Context, command ControlAgentTurnCommand, resume bool) (AgentTurn, error) {
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
		if resume && receipt.Status != "accepted" && receipt.Status != "waiting_approval" || !resume && receipt.Status != "paused" && receipt.Status != "pausing" {
			return AgentTurn{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识不属于当前对话控制。")
		}
		return turn, nil
	}
	status, event := "paused", "agent.turn.paused"
	if resume {
		if turn.Status != "paused" {
			return AgentTurn{}, domainError("AGENT_TURN_STATE_CONFLICT", "本轮尚未暂停，不能继续。")
		}
		checkpoint, unresolved, err := loadAgentTurnCheckpoint(ctx, tx, turn.AgentTurnID)
		if err != nil {
			return AgentTurn{}, err
		}
		if turn.StartedAt != nil && len(checkpoint.RunState) == 0 {
			return AgentTurn{}, domainError("AGENT_RUN_STATE_CORRUPT", "已开始的对话缺少暂停状态，不能从头重跑。")
		}
		if err := s.prepareExternalToolResumeTx(ctx, tx, "conversation", turn.AgentTurnID, checkpoint.RunState); err != nil {
			return AgentTurn{}, err
		}
		status, event = "accepted", "agent.turn.resume_requested"
		if unresolved > 0 {
			status = "waiting_approval"
		}
	} else {
		switch turn.Status {
		case "running", "pausing":
			status, event = "pausing", "agent.turn.pause_requested"
		case "accepted", "waiting_approval", "paused":
		default:
			return AgentTurn{}, domainError("AGENT_TURN_STATE_CONFLICT", "本轮当前不能暂停。")
		}
	}
	now := s.now()
	if status != turn.Status || turn.InputPauseRequested {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_turns SET status=?,input_pause_requested=0,updated_at=? WHERE agent_turn_id=?`, status, formatTime(now), turn.AgentTurnID); err != nil {
			return AgentTurn{}, err
		}
		turn.InputPauseRequested = false
		turn.Status, turn.UpdatedAt = status, now
		if resume && turn.ErrorCode != nil && (*turn.ErrorCode == modelRecoveryCode || *turn.ErrorCode == externalToolRecoveryCode) {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_turns SET error_code=NULL,error_message=NULL WHERE agent_turn_id=?`, turn.AgentTurnID); err != nil {
				return AgentTurn{}, err
			}
			turn.ErrorCode, turn.ErrorMessage = nil, nil
		}
		if _, err := s.appendAgentTurnEventTx(ctx, tx, turn, event, false, map[string]any{"status": status}); err != nil {
			return AgentTurn{}, err
		}
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, turn, now); err != nil {
		return AgentTurn{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTurn{}, err
	}
	return turn, nil
}

func (s *Store) cancelProjectAgentTurnsTx(ctx context.Context, tx *sql.Tx, projectID, code, reason string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, agentTurnSelect+` WHERE project_id=? AND status NOT IN ('committed','failed','cancelled') ORDER BY agent_turn_id`, projectID)
	if err != nil {
		return err
	}
	var turns []AgentTurn
	for rows.Next() {
		turn, err := scanAgentTurn(rows)
		if err != nil {
			rows.Close()
			return err
		}
		turns = append(turns, turn)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, turn := range turns {
		if _, err := s.finishAgentTurnTx(ctx, tx, turn, "cancelled", code, reason, now); err != nil {
			return err
		}
	}
	return nil
}
