package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"content-agent/backend/internal/agentcontract"
)

func (s *Store) GetActiveProjectGoal(
	ctx context.Context,
	projectID string,
) (*ProjectGoal, error) {
	goal, err := scanProjectGoal(s.db.QueryRowContext(ctx, `
		SELECT goal_id, project_id, conversation_id, title, success_criteria_json,
			status, version, source_message_id, created_at, updated_at, resolved_at
		FROM project_goals WHERE project_id = ? AND status = 'active'`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &goal, nil
}

func (s *Store) applyGoalUpdateTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	conversationID string,
	sourceMessageID string,
	update *agentcontract.GoalUpdate,
	now time.Time,
) (*ProjectGoal, bool, error) {
	if update == nil {
		return nil, false, nil
	}
	existing, err := scanProjectGoal(tx.QueryRowContext(ctx, `
		SELECT goal_id, project_id, conversation_id, title, success_criteria_json,
			status, version, source_message_id, created_at, updated_at, resolved_at
		FROM project_goals WHERE project_id = ? AND status = 'active'`, projectID))
	hasExisting := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}

	switch update.Action {
	case "set":
		title := strings.TrimSpace(update.Title)
		criteria := normalizeGoalCriteria(update.SuccessCriteria)
		if hasExisting && existing.Title == title && slices.Equal(existing.SuccessCriteria, criteria) {
			return &existing, false, nil
		}
		version := 1
		if hasExisting {
			version = existing.Version + 1
			if _, err := tx.ExecContext(ctx, `
				UPDATE project_goals SET status = 'superseded', updated_at = ?, resolved_at = ?
				WHERE goal_id = ? AND status = 'active'`,
				formatTime(now), formatTime(now), existing.GoalID); err != nil {
				return nil, false, err
			}
		}
		criteriaJSON, err := json.Marshal(criteria)
		if err != nil {
			return nil, false, err
		}
		goal := ProjectGoal{
			GoalID: s.newID("gol"), ProjectID: projectID,
			ConversationID: conversationID, Title: title,
			SuccessCriteria: criteria, Status: "active", Version: version,
			SourceMessageID: sourceMessageID, CreatedAt: now, UpdatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO project_goals(
				goal_id, project_id, conversation_id, title, success_criteria_json,
				status, version, source_message_id, created_at, updated_at, resolved_at
			) VALUES(?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, NULL)`,
			goal.GoalID, projectID, conversationID, title, string(criteriaJSON),
			version, sourceMessageID, formatTime(now), formatTime(now)); err != nil {
			return nil, false, err
		}
		return &goal, true, nil

	case "complete", "cancel":
		if !hasExisting {
			return nil, false, domainError("PROJECT_GOAL_NOT_FOUND", "当前项目没有可更新的进行中目标。")
		}
		status := "completed"
		if update.Action == "cancel" {
			status = "cancelled"
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE project_goals SET status = ?, version = version + 1,
				updated_at = ?, resolved_at = ? WHERE goal_id = ? AND status = 'active'`,
			status, formatTime(now), formatTime(now), existing.GoalID); err != nil {
			return nil, false, err
		}
		existing.Status = status
		existing.Version++
		existing.UpdatedAt = now
		existing.ResolvedAt = &now
		return &existing, true, nil
	}
	return nil, false, domainError("AGENT_DECISION_REJECTED", "Goal 更新动作无效。")
}

func normalizeGoalCriteria(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func scanProjectGoal(row rowScanner) (ProjectGoal, error) {
	var goal ProjectGoal
	var criteriaJSON, createdAt, updatedAt string
	var resolvedAt sql.NullString
	if err := row.Scan(
		&goal.GoalID, &goal.ProjectID, &goal.ConversationID, &goal.Title,
		&criteriaJSON, &goal.Status, &goal.Version, &goal.SourceMessageID,
		&createdAt, &updatedAt, &resolvedAt,
	); err != nil {
		return ProjectGoal{}, err
	}
	if err := json.Unmarshal([]byte(criteriaJSON), &goal.SuccessCriteria); err != nil {
		return ProjectGoal{}, err
	}
	if goal.SuccessCriteria == nil {
		goal.SuccessCriteria = []string{}
	}
	var err error
	goal.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return ProjectGoal{}, err
	}
	goal.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return ProjectGoal{}, err
	}
	if resolvedAt.Valid {
		value, err := parseTime(resolvedAt.String)
		if err != nil {
			return ProjectGoal{}, err
		}
		goal.ResolvedAt = &value
	}
	return goal, nil
}
