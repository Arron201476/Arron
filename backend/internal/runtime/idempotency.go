package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (s *Store) beginIdempotency(
	ctx context.Context,
	tx *sql.Tx,
	meta CommandMeta,
) (json.RawMessage, bool, error) {
	if meta.IdempotencyKey == "" {
		return nil, false, nil
	}
	if meta.Scope == "" || meta.CommandType == "" || meta.RequestHash == "" {
		return nil, false, domainError("IDEMPOTENCY_METADATA_INVALID", "幂等元数据不完整。")
	}

	var existingHash, status string
	var responseJSON, errorCode, errorMessage sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT request_hash, status, response_json, error_code, error_message
		FROM idempotency_records
		WHERE scope = ? AND command_type = ? AND idempotency_key = ?`,
		meta.Scope, meta.CommandType, meta.IdempotencyKey,
	).Scan(&existingHash, &status, &responseJSON, &errorCode, &errorMessage)
	if errors.Is(err, sql.ErrNoRows) {
		now := s.now()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO idempotency_records(
				idempotency_record_id, scope, command_type, idempotency_key,
				request_hash, status, created_at, expires_at
			) VALUES(?, ?, ?, ?, ?, 'running', ?, ?)`,
			s.newID("idem"), meta.Scope, meta.CommandType, meta.IdempotencyKey,
			meta.RequestHash, formatTime(now), formatTime(now.Add(7*24*time.Hour)),
		); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if existingHash != meta.RequestHash {
		return nil, false, domainError("IDEMPOTENCY_KEY_REUSED", "该 Idempotency-Key 已用于不同请求。")
	}
	switch status {
	case "completed":
		if !responseJSON.Valid {
			return nil, false, domainError("IDEMPOTENCY_RESULT_INVALID", "幂等结果快照缺失。")
		}
		return json.RawMessage(responseJSON.String), true, nil
	case "failed":
		code := "COMMAND_FAILED"
		message := "命令此前执行失败。"
		if errorCode.Valid {
			code = errorCode.String
		}
		if errorMessage.Valid {
			message = errorMessage.String
		}
		return nil, false, domainError(code, message)
	default:
		return nil, false, domainError("COMMAND_IN_PROGRESS", "相同命令正在执行。")
	}
}

func completeIdempotency(
	ctx context.Context,
	tx *sql.Tx,
	meta CommandMeta,
	response any,
	now time.Time,
) error {
	if meta.IdempotencyKey == "" {
		return nil
	}
	responseJSON, err := json.Marshal(response)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE idempotency_records
		SET status = 'completed', response_json = ?, completed_at = ?
		WHERE scope = ? AND command_type = ? AND idempotency_key = ?
			AND request_hash = ? AND status = 'running'`,
		string(responseJSON), formatTime(now), meta.Scope, meta.CommandType,
		meta.IdempotencyKey, meta.RequestHash,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return domainError("IDEMPOTENCY_STATE_CONFLICT", "幂等记录状态发生冲突。")
	}
	return nil
}

func decodeIdempotentResult[T any](raw json.RawMessage) (T, error) {
	var result T
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, domainError("IDEMPOTENCY_RESULT_INVALID", "幂等结果快照无法读取。")
	}
	return result, nil
}

// GetCommittedAgentTurn resolves the authoritative exchange committed by the
// SDK tool under the same client idempotency key as its durable AgentTurn.
func (s *Store) GetCommittedAgentTurn(
	ctx context.Context,
	projectID string,
	idempotencyKey string,
) (MessageExchange, bool, error) {
	if projectID == "" || idempotencyKey == "" {
		return MessageExchange{}, false, nil
	}
	var status string
	var responseJSON, errorCode, errorMessage sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT status, response_json, error_code, error_message
		FROM idempotency_records
		WHERE scope = ? AND command_type = 'commit_sdk_agent_turn' AND idempotency_key = ?`,
		projectID, idempotencyKey,
	).Scan(&status, &responseJSON, &errorCode, &errorMessage)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageExchange{}, false, nil
	}
	if err != nil {
		return MessageExchange{}, false, err
	}
	switch status {
	case "completed":
		if !responseJSON.Valid {
			return MessageExchange{}, false, domainError("IDEMPOTENCY_RESULT_INVALID", "幂等结果快照缺失。")
		}
		result, err := decodeIdempotentResult[MessageExchange](json.RawMessage(responseJSON.String))
		return result, err == nil, err
	case "failed":
		code, message := "COMMAND_FAILED", "命令此前执行失败。"
		if errorCode.Valid {
			code = errorCode.String
		}
		if errorMessage.Valid {
			message = errorMessage.String
		}
		return MessageExchange{}, false, domainError(code, message)
	default:
		return MessageExchange{}, false, domainError("COMMAND_IN_PROGRESS", "相同消息正在处理中。")
	}
}
