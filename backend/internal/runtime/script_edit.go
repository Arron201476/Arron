package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

const scriptHandoffRefreshMode = "script_handoff_refresh"

type pendingScriptHandoff struct {
	ScopeKey             string
	ScriptVersionID      string
	ScriptStatus         string
	HandoffArtifactID    string
	HandoffVersionID     string
	HandoffStatus        string
	HandoffScriptVersion string
	TaskItemID           string
	TaskStatus           string
	TaskOutputVersionID  string
}

type scriptHandoffRefreshCursor struct {
	Mode                    string `json:"mode"`
	ScriptArtifactVersionID string `json:"script_artifact_version_id"`
}

func parseScriptHandoffRefreshCursor(payload json.RawMessage) (scriptHandoffRefreshCursor, bool) {
	var cursor scriptHandoffRefreshCursor
	if err := json.Unmarshal(payload, &cursor); err != nil ||
		cursor.Mode != scriptHandoffRefreshMode ||
		cursor.ScriptArtifactVersionID == "" {
		return scriptHandoffRefreshCursor{}, false
	}
	return cursor, true
}

func (s *Store) CompleteScriptEdit(
	ctx context.Context,
	command CompleteScriptEditCommand,
) (ScriptEditCompletionResult, error) {
	if command.RunID == "" || len(command.ExpectedScriptVersionIDs) == 0 {
		return ScriptEditCompletionResult{}, domainError(
			"REQUEST_VALIDATION_FAILED",
			"完成剧本编辑必须提供当前待刷新的剧本版本。",
		)
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ScriptEditCompletionResult{}, err
	}
	defer tx.Rollback()

	var projectID, runStatus string
	var stepRunID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT project_id, status, current_step_run_id
		FROM runs WHERE run_id = ?`,
		command.RunID,
	).Scan(&projectID, &runStatus, &stepRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return ScriptEditCompletionResult{}, domainError("RUN_NOT_FOUND", "生成任务不存在。")
	}
	if err != nil {
		return ScriptEditCompletionResult{}, err
	}
	if err := authorizeArtifactCommandTx(ctx, tx, projectID, command.Scope); err != nil {
		return ScriptEditCompletionResult{}, err
	}
	if command.Scope == "" {
		command.Scope = projectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return ScriptEditCompletionResult{}, err
	}
	if hit {
		return decodeScriptEditReceiptTx(ctx, tx, cached, projectID, command)
	}
	if !stepRunID.Valid || runStatus != "waiting_approval" {
		return ScriptEditCompletionResult{}, domainError(
			"RUN_STATE_CONFLICT",
			"只能在剧本统一确认前完成本轮编辑。",
		)
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE project_id=? AND active_write_run_id=? AND deleted_at IS NULL)`, projectID, command.RunID).Scan(&active); err != nil {
		return ScriptEditCompletionResult{}, err
	}
	if !active {
		return ScriptEditCompletionResult{}, domainError("RUN_STATE_CONFLICT", "该任务已不是作品当前写任务，请刷新后重试。")
	}
	var stepStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT status FROM step_runs WHERE step_run_id = ? AND run_id = ?`,
		stepRunID.String,
		command.RunID,
	).Scan(&stepStatus); err != nil {
		return ScriptEditCompletionResult{}, err
	}
	if stepStatus != "waiting_approval" {
		return ScriptEditCompletionResult{}, domainError(
			"RUN_STATE_CONFLICT",
			"当前步骤不在剧本统一确认状态。",
		)
	}
	step, err := s.compiledStepForRunTx(ctx, tx, command.RunID, stepRunID.String)
	if err != nil {
		return ScriptEditCompletionResult{}, err
	}
	if !isScriptBundleStep(step) {
		return ScriptEditCompletionResult{}, domainError(
			"APPROVAL_COMMAND_MISMATCH",
			"当前步骤不是可完成编辑的剧本步骤。",
		)
	}

	pending, err := pendingScriptHandoffsTx(ctx, tx, command.RunID, stepRunID.String)
	if err != nil {
		return ScriptEditCompletionResult{}, err
	}
	if len(pending) == 0 {
		return ScriptEditCompletionResult{}, domainError(
			"RUN_STATE_CONFLICT",
			"当前没有待刷新的剧本交接。",
		)
	}
	currentVersionIDs := make([]string, 0, len(pending))
	for _, item := range pending {
		if item.ScriptStatus != "pending_approval" ||
			item.HandoffStatus != "stale" ||
			item.TaskStatus != "succeeded" ||
			item.TaskOutputVersionID != item.ScriptVersionID {
			return ScriptEditCompletionResult{}, domainError(
				"ARTIFACT_VERSION_CONFLICT",
				"待刷新的剧本或交接版本已经变化。",
			)
		}
		currentVersionIDs = append(currentVersionIDs, item.ScriptVersionID)
	}
	expected := append([]string(nil), command.ExpectedScriptVersionIDs...)
	sort.Strings(expected)
	sort.Strings(currentVersionIDs)
	if !equalStrings(expected, currentVersionIDs) {
		return ScriptEditCompletionResult{}, domainError(
			"ARTIFACT_VERSION_CONFLICT",
			"待刷新的剧本版本集合已经变化，请刷新后重试。",
		)
	}

	taskIDs := make([]string, 0, len(pending))
	scopes := make([]string, 0, len(pending))
	runRef, stepRef := command.RunID, stepRunID.String
	for _, item := range pending {
		cursor, err := json.Marshal(scriptHandoffRefreshCursor{
			Mode:                    scriptHandoffRefreshMode,
			ScriptArtifactVersionID: item.ScriptVersionID,
		})
		if err != nil {
			return ScriptEditCompletionResult{}, err
		}
		if err := updateExactlyOne(ctx, tx, `
			UPDATE task_items
			SET status = 'pending', current_attempt_id = NULL,
				cursor_json = ?, ended_at = NULL, failure = NULL, updated_at = ?
			WHERE task_item_id = ? AND step_run_id = ?
				AND status = 'succeeded'
				AND output_artifact_version_id = ?`,
			"剧本编辑完成时单集任务已经变化。",
			string(cursor),
			formatTime(now),
			item.TaskItemID,
			stepRunID.String,
			item.ScriptVersionID,
		); err != nil {
			return ScriptEditCompletionResult{}, err
		}
		taskIDs = append(taskIDs, item.TaskItemID)
		scopes = append(scopes, item.ScopeKey)
		if _, err := s.appendEvent(
			ctx,
			tx,
			projectID,
			&runRef,
			&stepRef,
			"script_handoff.refresh_requested",
			"task_item",
			item.TaskItemID,
			map[string]any{
				"scope_key":                  item.ScopeKey,
				"script_artifact_version_id": item.ScriptVersionID,
			},
		); err != nil {
			return ScriptEditCompletionResult{}, err
		}
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE step_runs
		SET status = 'running', ended_at = NULL
		WHERE step_run_id = ? AND status = 'waiting_approval'`,
		"剧本步骤已经变化。",
		stepRunID.String,
	); err != nil {
		return ScriptEditCompletionResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE runs SET status = 'running', updated_at = ?
		WHERE run_id = ? AND status = 'waiting_approval'`,
		"生成任务已经变化。",
		formatTime(now),
		command.RunID,
	); err != nil {
		return ScriptEditCompletionResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects SET status = 'running', updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		formatTime(now),
		projectID,
		command.RunID,
	); err != nil {
		return ScriptEditCompletionResult{}, err
	}
	if _, err := s.appendEvent(
		ctx,
		tx,
		projectID,
		&runRef,
		&stepRef,
		"script_edit.completed",
		"step_run",
		stepRunID.String,
		map[string]any{
			"refresh_task_count": len(taskIDs),
			"pending_scopes":     scopes,
		},
	); err != nil {
		return ScriptEditCompletionResult{}, err
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, command.RunID)
	if err != nil {
		return ScriptEditCompletionResult{}, err
	}
	result := ScriptEditCompletionResult{
		RunSnapshot:    snapshot,
		RefreshTaskIDs: taskIDs,
		PendingScopes:  scopes,
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, result, now); err != nil {
		return ScriptEditCompletionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ScriptEditCompletionResult{}, err
	}
	return result, nil
}

func pendingScriptHandoffScopesTx(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	stepRunID string,
) ([]string, error) {
	items, err := pendingScriptHandoffsTx(ctx, tx, runID, stepRunID)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.ScopeKey)
	}
	return result, nil
}

func pendingScriptHandoffsTx(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	stepRunID string,
) ([]pendingScriptHandoff, error) {
	return pendingScriptHandoffsQuery(ctx, tx, runID, stepRunID)
}

func pendingScriptHandoffsQuery(ctx context.Context, query runActionQuery, runID, stepRunID string) ([]pendingScriptHandoff, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT script.scope_key, script.current_version_id, script_version.status,
			handoff.artifact_id, handoff.current_version_id, handoff_version.status,
			handoff_version.payload_json, task.task_item_id, task.status,
			task.output_artifact_version_id
		FROM artifacts script
		JOIN artifact_versions script_version
			ON script_version.artifact_version_id = script.current_version_id
			AND script_version.artifact_id = script.artifact_id
		JOIN artifacts handoff
			ON handoff.run_id = script.run_id
			AND handoff.project_id = script.project_id
			AND handoff.step_run_id = script.step_run_id
			AND handoff.scope_key = script.scope_key
			AND handoff.artifact_type = 'script_handoff'
		JOIN artifact_versions handoff_version
			ON handoff_version.artifact_version_id = handoff.current_version_id
			AND handoff_version.artifact_id = handoff.artifact_id
		JOIN task_items task
			ON task.step_run_id = script.step_run_id
			AND task.item_key = script.scope_key
		WHERE script.run_id = ? AND script.step_run_id = ?
			AND script.artifact_type = 'script_unit'
		ORDER BY task.item_order ASC`,
		runID,
		stepRunID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []pendingScriptHandoff
	for rows.Next() {
		var item pendingScriptHandoff
		var handoffPayload string
		var taskOutputVersionID sql.NullString
		if err := rows.Scan(
			&item.ScopeKey,
			&item.ScriptVersionID,
			&item.ScriptStatus,
			&item.HandoffArtifactID,
			&item.HandoffVersionID,
			&item.HandoffStatus,
			&handoffPayload,
			&item.TaskItemID,
			&item.TaskStatus,
			&taskOutputVersionID,
		); err != nil {
			return nil, err
		}
		if taskOutputVersionID.Valid {
			item.TaskOutputVersionID = taskOutputVersionID.String
		}
		var payload struct {
			ScriptArtifactVersionID string `json:"script_artifact_version_id"`
		}
		if err := json.Unmarshal([]byte(handoffPayload), &payload); err != nil {
			return nil, err
		}
		item.HandoffScriptVersion = payload.ScriptArtifactVersionID
		if item.HandoffStatus == "stale" ||
			item.HandoffScriptVersion != item.ScriptVersionID {
			result = append(result, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if result == nil {
		result = []pendingScriptHandoff{}
	}
	return result, nil
}

func (s *Store) pendingScriptEditQuery(ctx context.Context, query runActionQuery, run Run, steps []StepRun) (*PendingScriptEdit, error) {
	if run.Status != "waiting_approval" || run.CurrentStepRunID == nil {
		return nil, nil
	}
	current := findStepRun(steps, *run.CurrentStepRunID)
	if current == nil || current.Status != "waiting_approval" {
		return nil, nil
	}
	var active bool
	if err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects p JOIN runs r ON r.run_id=p.active_write_run_id
		WHERE p.project_id=? AND p.deleted_at IS NULL AND r.run_id=? AND r.project_id=p.project_id
		AND r.status='waiting_approval' AND r.current_step_run_id=?)`, run.ProjectID, run.RunID, current.StepRunID).Scan(&active); err != nil {
		return nil, err
	}
	if !active {
		return nil, nil
	}
	// Reconstruct recovery from persisted versions, not a prior save response.
	pending, err := pendingScriptHandoffsQuery(ctx, query, run.RunID, current.StepRunID)
	if err != nil || len(pending) == 0 {
		return nil, err
	}
	result := &PendingScriptEdit{ProjectID: run.ProjectID, RunID: run.RunID, StepRunID: current.StepRunID, CanComplete: true}
	seen := make(map[string]bool, len(pending))
	for _, item := range pending {
		result.ExpectedScriptVersionIDs = append(result.ExpectedScriptVersionIDs, item.ScriptVersionID)
		result.PendingScopes = append(result.PendingScopes, item.ScopeKey)
		if item.ScriptVersionID == "" || seen[item.ScriptVersionID] || item.ScriptStatus != "pending_approval" ||
			item.HandoffStatus != "stale" || item.TaskStatus != "succeeded" || item.TaskOutputVersionID != item.ScriptVersionID {
			result.CanComplete = false
			result.DisabledReason = "剧本或交接状态已变化，暂时不能更新交接。"
		}
		seen[item.ScriptVersionID] = true
	}
	step, err := s.compiledStepForRunQuery(ctx, query, run.RunID, current.StepRunID)
	if err != nil {
		var domain *DomainError
		if !errors.As(err, &domain) {
			return nil, err
		}
		result.CanComplete = false
		result.DisabledReason = domain.Message
	} else if !isScriptBundleStep(step) {
		return nil, nil
	}
	return result, nil
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
