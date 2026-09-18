package runtime

import (
	"context"
	"database/sql"
)

const defaultProjectActivityLimit = 240

// ListProjectActivities returns significant lifecycle events in display order.
// Runtime events remain the source of truth; this is only a stable read model.
func (s *Store) ListProjectActivities(ctx context.Context, projectID string, limit int) ([]ProjectActivity, error) {
	if limit <= 0 {
		limit = defaultProjectActivityLimit
	}
	if limit > maxEventBatchLimit {
		limit = maxEventBatchLimit
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_id, event_type, run_id, capability_id, step_id, occurred_at
		FROM (
			SELECT e.event_id, e.event_type, e.run_id, r.capability_id, sr.step_id,
				e.occurred_at, e.project_event_seq
			FROM events e
			LEFT JOIN runs r ON r.run_id = e.run_id
			LEFT JOIN step_runs sr ON sr.step_run_id = e.step_run_id
			WHERE e.project_id = ? AND e.event_type IN (
				'run.started', 'run.paused', 'run.resumed', 'run.failed',
				'run.completed', 'run.cancelled',
				'step.started', 'step.completed', 'step.failed',
				'workflow.failure_transition_prepared', 'workflow.failure_transition_blocked',
				'approval.requested', 'approval.resolved',
				'quality_review.started', 'quality_review.passed',
				'quality_review.action_required', 'quality_review.override_confirmed',
				'script_candidate.created', 'final_selection.changed'
			)
			ORDER BY e.project_event_seq DESC
			LIMIT ?
		)
		ORDER BY project_event_seq ASC`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]ProjectActivity, 0)
	for rows.Next() {
		var item ProjectActivity
		var runID, capabilityID, stepID sql.NullString
		var occurredAt string
		if err := rows.Scan(&item.EventID, &item.EventType, &runID, &capabilityID, &stepID, &occurredAt); err != nil {
			return nil, err
		}
		item.RunID = stringPointer(runID)
		item.CapabilityID = stringPointer(capabilityID)
		item.StepID = stringPointer(stepID)
		parsed, err := parseTime(occurredAt)
		if err != nil {
			return nil, err
		}
		item.OccurredAt = parsed
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
