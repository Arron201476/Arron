package runtime

import (
	"context"
	"strings"
)

type ExecutionInputAttempt struct {
	AttemptID     string `json:"attempt_id"`
	TaskItemID    string `json:"task_item_id"`
	ItemKey       string `json:"item_key"`
	AttemptNo     int    `json:"attempt_no"`
	Status        string `json:"status"`
	InputCount    int    `json:"input_count"`
	IncludedCount int    `json:"included_count"`
	Current       bool   `json:"current"`
}

// History exposes receipt counts, not checkpoint contents, credentials or tokens.
func (s *Store) ListExecutionInputAttempts(ctx context.Context, projectID, runID, afterID string) ([]ExecutionInputAttempt, string, error) {
	if len(afterID) > 256 || strings.ContainsAny(afterID, "\x00\r\n") {
		return nil, "", domainError("REQUEST_VALIDATION_FAILED", "分页标识无效。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	if _, err := projectFilesWorkspace(ctx, tx, projectID, false); err != nil {
		return nil, "", err
	}
	run, err := getRunTx(ctx, tx, runID)
	if err != nil {
		return nil, "", err
	}
	if run.ProjectID != projectID {
		return nil, "", domainError("RUN_NOT_FOUND", "运行不属于当前作品。")
	}
	rows, err := tx.QueryContext(ctx, `SELECT ea.attempt_id, ea.task_item_id, ti.item_key, ea.attempt_no, ea.status,
		COUNT(i.input_id), SUM(CASE WHEN i.status='included' THEN 1 ELSE 0 END), COALESCE(ti.current_attempt_id=ea.attempt_id,0)
		FROM execution_attempts ea JOIN task_items ti ON ti.task_item_id=ea.task_item_id
		JOIN execution_inputs i ON i.attempt_id=ea.attempt_id
		WHERE ea.run_id=? AND ea.attempt_id>? GROUP BY ea.attempt_id ORDER BY ea.attempt_id LIMIT 51`, runID, afterID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := []ExecutionInputAttempt{}
	for rows.Next() {
		var item ExecutionInputAttempt
		if err := rows.Scan(&item.AttemptID, &item.TaskItemID, &item.ItemKey, &item.AttemptNo, &item.Status, &item.InputCount, &item.IncludedCount, &item.Current); err != nil {
			return nil, "", err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > 50 {
		items, next = items[:50], items[49].AttemptID
	}
	return items, next, nil
}
