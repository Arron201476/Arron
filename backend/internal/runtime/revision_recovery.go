package runtime

import (
	"context"
	"errors"
	"time"
)

// A revision adapter has a bounded HTTP lifetime. Expired attempts become
// failed for explicit retry; recovery never replays generation automatically.
func (s *Store) RecoverExpiredRevisionAttempts(ctx context.Context, maxAge time.Duration) error {
	if maxAge <= 0 {
		return domainError("REQUEST_VALIDATION_FAILED", "返修执行超时必须为正数。")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.revision_attempt_id,a.revision_request_id
		FROM revision_attempts a JOIN revision_requests r ON r.revision_request_id=a.revision_request_id
		WHERE a.status='running' AND r.status='running' AND a.created_at < ?
		AND a.attempt_no=(SELECT MAX(latest.attempt_no) FROM revision_attempts latest WHERE latest.revision_request_id=a.revision_request_id)
		ORDER BY a.created_at,a.revision_attempt_id LIMIT 100`, formatTime(s.now().Add(-maxAge)))
	if err != nil {
		return err
	}
	var attempts []FailRevisionAttemptCommand
	for rows.Next() {
		var command FailRevisionAttemptCommand
		if err := rows.Scan(&command.RevisionAttemptID, &command.RevisionRequestID); err != nil {
			rows.Close()
			return err
		}
		command.FailureCode = "SDK_REVISION_ATTEMPT_EXPIRED"
		attempts = append(attempts, command)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, command := range attempts {
		if err := s.FailRevisionAttempt(ctx, command); err != nil {
			var domain *DomainError
			if !errors.As(err, &domain) || domain.Code != "RUN_STATE_CONFLICT" {
				return err
			}
		}
	}
	return nil
}
