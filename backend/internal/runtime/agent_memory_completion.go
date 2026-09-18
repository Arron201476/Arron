package runtime

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"

	"content-agent/backend/internal/identity"
)

// Completion records durable publication, never an Agent's final text.
func (s *Store) CompleteAgentMemoryGeneration(ctx context.Context, id, workerID, token string, attempt int) (AgentMemoryGeneration, error) {
	var empty AgentMemoryGeneration
	if err := memoryGenerationService(ctx); err != nil {
		return empty, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	job, err := memoryGenerationQuery(ctx, tx, id)
	if err != nil {
		return empty, err
	}
	if token == "" || workerID == "" || attempt < 1 || job.Phase != "consolidation" || job.WorkerID != workerID || job.Attempt != attempt ||
		subtle.ConstantTimeCompare([]byte(job.TokenHash), []byte(sha256Hex([]byte(token)))) != 1 {
		return empty, domainError("AGENT_ACTIVITY_STALE", "Memory completion requires its original worker identity.")
	}
	if job.Status != "completed" {
		lease, err := parseTime(job.LeaseUntil)
		if err != nil || job.Status != "running" || !job.Started || !lease.After(s.now()) {
			return empty, domainError("AGENT_ACTIVITY_STALE", "Memory completion requires a live started worker.")
		}
	}
	job, err = s.completeMemoryGenerationTx(ctx, tx, job)
	if err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return job, nil
}

func (s *Store) completeMemoryGenerationTx(ctx context.Context, tx *sql.Tx, job AgentMemoryGeneration) (AgentMemoryGeneration, error) {
	var empty AgentMemoryGeneration
	if job.Phase != "consolidation" || !job.Started || (job.Status != "running" && job.Status != "completed") {
		return empty, domainError("AGENT_ACTIVITY_STALE", "Memory completion requires an admitted consolidation.")
	}
	if _, err := s.memoryGenerationSourceTx(ctx, tx, job); err != nil {
		return empty, err
	}
	current, err := agentMemoryQuery(ctx, tx, identity.Principal{UserID: job.UserID, WorkspaceID: job.WorkspaceID}, job.ProjectID, 0)
	if err != nil {
		return empty, err
	}
	confirmed, err := memoryGenerationOwnPublicationQuery(ctx, tx, job, current)
	if err != nil {
		return empty, err
	}
	if !confirmed {
		return empty, domainError("AGENT_MEMORY_CONFLICT", "Memory completion has no confirmed private publication.")
	}
	var unsettled bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_memory_tool_calls m
		JOIN agent_tool_calls c ON c.agent_tool_call_id=m.agent_tool_call_id
		WHERE m.generation_id=? AND m.phase='consolidation'
		AND c.status NOT IN ('completed','failed','cancelled','rejected'))`, job.GenerationID).Scan(&unsettled); err != nil {
		return empty, err
	}
	if unsettled {
		return empty, domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "Memory generation still has unsettled tool calls.")
	}
	if job.Status == "completed" {
		return job, nil
	}
	// Retain only the hashed worker identity for an exact lost-response retry.
	// Terminal status and the empty lease fence all delegated execution paths.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET status='completed',lease_until='',checkpoint_json='',checkpoint_hash=?,error_code='',revision=revision+1 WHERE generation_id=?`, sha256Hex(nil), job.GenerationID); err != nil {
		return empty, err
	}
	return memoryGenerationQuery(ctx, tx, job.GenerationID)
}

// Reconcile only expired running attempts. User-cancelled or paused work is
// never revived, and durable publication is checked before any terminal write.
func (s *Store) reconcileExpiredMemoryPublicationsTx(ctx context.Context, tx *sql.Tx, now string) error {
	rows, err := tx.QueryContext(ctx, `SELECT generation_id FROM agent_memory_generations
		WHERE status='running' AND started=1 AND phase='consolidation' AND lease_until<=?`, now)
	if err != nil {
		return err
	}
	var ids []string
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
			_, err = s.completeMemoryGenerationTx(ctx, tx, job)
		}
		if err != nil {
			var domain *DomainError
			if !errors.As(err, &domain) && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
	}
	return nil
}
