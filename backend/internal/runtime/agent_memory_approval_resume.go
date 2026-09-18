package runtime

import (
	"context"
	"database/sql"
	"errors"
)

// Run at claim time as approval may arrive before the SDK pause is committed.
func (s *Store) resumeReadyMemoryApprovalsTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT g.generation_id FROM agent_memory_generations g
		WHERE g.status='paused' AND g.error_code='MEMORY_GENERATION_APPROVAL_PENDING' AND g.checkpoint_json<>''
		AND EXISTS(SELECT 1 FROM agent_memory_tool_calls m JOIN agent_tool_calls c ON c.agent_tool_call_id=m.agent_tool_call_id
			WHERE m.generation_id=g.generation_id AND m.phase=g.phase AND c.status IN ('approved','rejected'))
		AND NOT EXISTS(SELECT 1 FROM agent_memory_tool_calls m JOIN agent_tool_calls c ON c.agent_tool_call_id=m.agent_tool_call_id
			WHERE m.generation_id=g.generation_id AND m.phase=g.phase AND c.status='pending_approval')
		ORDER BY g.created_at,g.generation_id LIMIT 50`)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		job, err := memoryGenerationQuery(ctx, tx, id)
		if err == nil {
			_, err = s.memoryGenerationSourceTx(ctx, tx, job)
		}
		if err != nil {
			var domain *DomainError
			if !errors.As(err, &domain) && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET error_code='MEMORY_GENERATION_RESUME_REQUIRES_REVIEW',revision=revision+1 WHERE generation_id=?`, id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET status='queued',error_code='',token_hash='',lease_until='',revision=revision+1 WHERE generation_id=?`, id); err != nil {
			return err
		}
	}
	return nil
}
