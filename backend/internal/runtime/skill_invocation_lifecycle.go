package runtime

import (
	"context"
	"database/sql"
	"time"
)

func syncSkillInvocationForRunTx(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	status string,
	now time.Time,
) error {
	if status != "completed" && status != "failed" && status != "cancelled" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE skill_invocations
		SET status = ?, updated_at = ?
		WHERE run_id = ? AND execution_mode = 'stateful_workflow'
			AND status = 'delegated_to_run'`,
		status, formatTime(now), runID,
	)
	return err
}
