package runtime

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"content-agent/backend/internal/identity"
)

func (s *Store) RenewAgentMemoryGeneration(ctx context.Context, id, workerID, token string, attempt, leaseSeconds int) (AgentMemoryGeneration, error) {
	var empty AgentMemoryGeneration
	if err := memoryGenerationService(ctx); err != nil {
		return empty, err
	}
	if leaseSeconds < minAttemptLeaseSeconds || leaseSeconds > maxAttemptLeaseSeconds {
		return empty, domainError("REQUEST_VALIDATION_FAILED", "Memory worker lease duration is invalid.")
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
	now := s.now()
	lease, err := parseTime(job.LeaseUntil)
	if err != nil || job.Status != "running" || workerID != job.WorkerID || attempt != job.Attempt || !lease.After(now) || subtle.ConstantTimeCompare([]byte(job.TokenHash), []byte(sha256Hex([]byte(token)))) != 1 {
		return empty, domainError("AGENT_ACTIVITY_STALE", "Memory generation worker lease is no longer valid.")
	}
	if _, err := s.memoryGenerationSourceTx(ctx, tx, job); err != nil {
		return empty, err
	}
	until := now.Add(time.Duration(leaseSeconds) * time.Second)
	if !until.After(lease) {
		return job, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET lease_until=?,revision=revision+1 WHERE generation_id=?`, until.UTC().Format(memoryGenerationLeaseLayout), id); err != nil {
		return empty, err
	}
	job, err = memoryGenerationQuery(ctx, tx, id)
	if err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return job, nil
}

func memoryGenerationOwnerTx(ctx context.Context, tx *sql.Tx, projectID, id string, write bool) (AgentMemoryGeneration, error) {
	var empty AgentMemoryGeneration
	transport, ok := identity.FromContext(ctx)
	_, delegated := AgentActivityFromContext(ctx)
	if !ok || !transport.ValidUser() || delegated {
		return empty, domainError("AUTHENTICATION_REQUIRED", "Memory generation controls require the user directly.")
	}
	user, err := instructionUserQuery(ctx, tx, projectID, write)
	if err != nil {
		return empty, err
	}
	// Scope the lookup before reading private checkpoint bytes or their integrity.
	var found string
	if err := tx.QueryRowContext(ctx, `SELECT generation_id FROM agent_memory_generations WHERE generation_id=? AND project_id=? AND workspace_id=? AND user_id=?`, id, projectID, user.WorkspaceID, user.UserID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return empty, domainError("AGENT_MEMORY_GENERATION_NOT_FOUND", "Memory generation was not found.")
		}
		return empty, err
	}
	return memoryGenerationQuery(ctx, tx, found)
}

func (s *Store) GetAgentMemoryGeneration(ctx context.Context, projectID, id string) (AgentMemoryGeneration, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentMemoryGeneration{}, err
	}
	defer tx.Rollback()
	return memoryGenerationOwnerTx(ctx, tx, projectID, id, false)
}

func (s *Store) ControlAgentMemoryGeneration(ctx context.Context, projectID, id, action string, expectedRevision int) (AgentMemoryGeneration, error) {
	var empty AgentMemoryGeneration
	if expectedRevision < 1 || (action != "pause" && action != "resume" && action != "cancel") {
		return empty, domainError("REQUEST_VALIDATION_FAILED", "A generation action and current revision are required.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	job, err := memoryGenerationOwnerTx(ctx, tx, projectID, id, true)
	if err != nil {
		return empty, err
	}
	if job.Revision != expectedRevision {
		return empty, domainError("AGENT_MEMORY_CONFLICT", "Memory generation changed; reload its current state.")
	}
	status, code := "", ""
	switch action {
	case "pause":
		if job.Status == "paused" && job.ErrorCode == "MEMORY_GENERATION_USER_PAUSED" {
			return job, nil
		}
		if job.Status != "queued" && job.Status != "running" && job.Status != "paused" {
			return empty, domainError("AGENT_MEMORY_CONFLICT", "This generation cannot be paused.")
		}
		if job.Status == "running" && job.Started {
			if job.ErrorCode == "MEMORY_GENERATION_PAUSE_REQUESTED" {
				return job, nil
			}
			if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET error_code='MEMORY_GENERATION_PAUSE_REQUESTED',revision=revision+1 WHERE generation_id=?`, id); err != nil {
				return empty, err
			}
			job, err = memoryGenerationQuery(ctx, tx, id)
			if err != nil {
				return empty, err
			}
			if err := tx.Commit(); err != nil {
				return empty, err
			}
			return job, nil
		}
		status, code = "paused", "MEMORY_GENERATION_USER_PAUSED"
	case "cancel":
		if job.Status == "cancelled" {
			return job, nil
		}
		if job.Status == "completed" || job.Status == "failed" {
			return empty, domainError("AGENT_MEMORY_CONFLICT", "This generation has already ended.")
		}
		status, code = "cancelled", "MEMORY_GENERATION_USER_CANCELLED"
	case "resume":
		if job.Status != "paused" || (job.Started && job.Checkpoint == "") {
			return empty, domainError("AGENT_MEMORY_CONFLICT", "Started generation requires a saved SDK checkpoint before resuming.")
		}
		if _, err := s.memoryGenerationSourceTx(ctx, tx, job); err != nil {
			return empty, err
		}
		if err := s.recoverMemoryWorkspacePhaseTx(ctx, tx, job); err != nil {
			return empty, err
		}
		status = "queued"
	}
	// Revoking the token fences the old worker before a replacement is admitted.
	if err := updateExactlyOne(ctx, tx, `UPDATE agent_memory_generations SET status=?,error_code=?,token_hash='',lease_until='',revision=revision+1 WHERE generation_id=?`,
		"Memory generation changed during control.", status, code, id); err != nil {
		return empty, err
	}
	job, err = memoryGenerationQuery(ctx, tx, id)
	if err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return job, nil
}
