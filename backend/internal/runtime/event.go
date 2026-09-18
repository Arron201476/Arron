package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
)

const (
	defaultEventBatchLimit = 100
	maxEventBatchLimit     = 500
)

func (s *Store) CurrentProjectEventSeq(ctx context.Context, projectID string) (int64, error) {
	return s.currentEventSeq(ctx, projectID, false)
}

func (s *Store) CurrentRunEventSeq(ctx context.Context, runID string) (int64, error) {
	return s.currentEventSeq(ctx, runID, true)
}

func (s *Store) currentEventSeq(ctx context.Context, resourceID string, runScope bool) (int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	current, err := eventScopeCursorQuery(ctx, tx, resourceID, runScope)
	if err != nil {
		return 0, err
	}
	return current, tx.Commit()
}

// Every cursor and replay batch rechecks access in its read transaction; an
// authenticated long-lived connection must not retain revoked membership.
func eventScopeCursorQuery(ctx context.Context, queryer rowQueryer, resourceID string, runScope bool) (int64, error) {
	query := `SELECT project_id,project_event_seq FROM projects WHERE project_id=? AND deleted_at IS NULL`
	notFound := "PROJECT_NOT_FOUND"
	if runScope {
		query = `SELECT p.project_id,r.run_event_seq FROM runs r JOIN projects p ON p.project_id=r.project_id WHERE r.run_id=? AND p.deleted_at IS NULL`
		notFound = "RUN_NOT_FOUND"
	}
	var current int64
	var projectID string
	err := queryer.QueryRowContext(ctx, query, resourceID).Scan(&projectID, &current)
	if err == sql.ErrNoRows {
		return 0, domainError(notFound, "请求的事件流不存在。")
	}
	if err != nil {
		return 0, err
	}
	if _, err := projectFilesWorkspace(ctx, queryer, projectID, false); err != nil {
		return 0, err
	}
	return current, nil
}

func (s *Store) ListProjectEvents(
	ctx context.Context,
	projectID string,
	afterSeq int64,
	limit int,
) (EventBatch, error) {
	if afterSeq < 0 {
		return EventBatch{}, domainError("EVENT_CURSOR_INVALID", "事件游标不能小于 0。")
	}
	limit = normalizeEventBatchLimit(limit)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EventBatch{}, err
	}
	defer tx.Rollback()

	current, err := eventScopeCursorQuery(ctx, tx, projectID, false)
	if err != nil {
		return EventBatch{}, err
	}
	if afterSeq > current {
		return EventBatch{}, domainError("EVENT_CURSOR_AHEAD", "事件游标超过服务端当前游标。")
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT
			event_id, event_type, schema_version, project_id, run_id, step_run_id,
			project_event_seq, run_event_seq, actor_kind, actor_ref, subject_type,
			subject_id, payload_json, occurred_at
		FROM events
		WHERE project_id = ? AND project_event_seq > ?
		ORDER BY project_event_seq ASC
		LIMIT ?`,
		projectID, afterSeq, limit+1,
	)
	if err != nil {
		return EventBatch{}, err
	}
	defer rows.Close()
	items, hasMore, err := scanEventRows(rows, limit)
	if err != nil {
		return EventBatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return EventBatch{}, err
	}
	return newEventBatch("project", afterSeq, current, items, hasMore, false), nil
}

func (s *Store) ListRunEvents(
	ctx context.Context,
	runID string,
	afterSeq int64,
	limit int,
) (EventBatch, error) {
	if afterSeq < 0 {
		return EventBatch{}, domainError("EVENT_CURSOR_INVALID", "事件游标不能小于 0。")
	}
	limit = normalizeEventBatchLimit(limit)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EventBatch{}, err
	}
	defer tx.Rollback()

	current, err := eventScopeCursorQuery(ctx, tx, runID, true)
	if err != nil {
		return EventBatch{}, err
	}
	if afterSeq > current {
		return EventBatch{}, domainError("EVENT_CURSOR_AHEAD", "事件游标超过服务端当前游标。")
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT
			event_id, event_type, schema_version, project_id, run_id, step_run_id,
			project_event_seq, run_event_seq, actor_kind, actor_ref, subject_type,
			subject_id, payload_json, occurred_at
		FROM events
		WHERE run_id = ? AND run_event_seq > ?
		ORDER BY run_event_seq ASC
		LIMIT ?`,
		runID, afterSeq, limit+1,
	)
	if err != nil {
		return EventBatch{}, err
	}
	defer rows.Close()
	items, hasMore, err := scanEventRows(rows, limit)
	if err != nil {
		return EventBatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return EventBatch{}, err
	}
	return newEventBatch("run", afterSeq, current, items, hasMore, true), nil
}

func normalizeEventBatchLimit(limit int) int {
	if limit <= 0 {
		return defaultEventBatchLimit
	}
	if limit > maxEventBatchLimit {
		return maxEventBatchLimit
	}
	return limit
}

func scanEventRows(rows *sql.Rows, limit int) ([]Event, bool, error) {
	items := make([]Event, 0, limit)
	for rows.Next() {
		var event Event
		var runID, stepRunID sql.NullString
		var runSeq sql.NullInt64
		var payloadJSON, occurredAt string
		if err := rows.Scan(
			&event.EventID,
			&event.EventType,
			&event.SchemaVersion,
			&event.ProjectID,
			&runID,
			&stepRunID,
			&event.ProjectEventSeq,
			&runSeq,
			&event.ActorKind,
			&event.ActorRef,
			&event.SubjectType,
			&event.SubjectID,
			&payloadJSON,
			&occurredAt,
		); err != nil {
			return nil, false, err
		}
		event.RunID = stringPointer(runID)
		event.StepRunID = stringPointer(stepRunID)
		event.RunEventSeq = int64Pointer(runSeq)
		event.Payload = json.RawMessage(payloadJSON)
		parsed, err := parseTime(occurredAt)
		if err != nil {
			return nil, false, err
		}
		event.OccurredAt = parsed
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func newEventBatch(
	scope string,
	afterSeq int64,
	currentSeq int64,
	items []Event,
	hasMore bool,
	runScope bool,
) EventBatch {
	nextSeq := afterSeq
	if len(items) > 0 {
		last := items[len(items)-1]
		if runScope && last.RunEventSeq != nil {
			nextSeq = *last.RunEventSeq
		} else {
			nextSeq = last.ProjectEventSeq
		}
	}
	return EventBatch{
		Items:       items,
		AfterSeq:    afterSeq,
		NextSeq:     nextSeq,
		CurrentSeq:  currentSeq,
		HasMore:     hasMore,
		StreamScope: scope,
	}
}

func int64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}
