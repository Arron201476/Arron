package runtime

import (
	"context"
	"database/sql"
	"errors"

	"content-agent/backend/internal/identity"
)

type AgentMemoryGenerationSummary struct {
	GenerationID    string `json:"generation_id"`
	ProjectID       string `json:"project_id"`
	UserID          string `json:"user_id"`
	Status          string `json:"status"`
	Phase           string `json:"phase"`
	Revision        int    `json:"revision"`
	Attempt         int    `json:"attempt"`
	ModelID         string `json:"model_id"`
	ErrorCode       string `json:"error_code,omitempty"`
	CreatedAt       string `json:"created_at"`
	ResumeAvailable bool   `json:"resume_available"`
}

type AgentMemoryGenerationPage struct {
	Items      []AgentMemoryGenerationSummary `json:"items"`
	NextCursor string                         `json:"next_cursor,omitempty"`
}

func (s *Store) ListAgentMemoryGenerations(ctx context.Context, projectID, cursor string) (AgentMemoryGenerationPage, error) {
	page := AgentMemoryGenerationPage{Items: []AgentMemoryGenerationSummary{}}
	principal, ok := identity.FromContext(ctx)
	_, delegated := AgentActivityFromContext(ctx)
	if !ok || !principal.ValidUser() || delegated {
		return page, domainError("AUTHENTICATION_REQUIRED", "Memory generation listing requires the user directly.")
	}
	if len(cursor) > 256 {
		return page, domainError("REQUEST_VALIDATION_FAILED", "Memory generation cursor is invalid.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	user, err := instructionUserQuery(ctx, tx, projectID, false)
	if err != nil {
		return page, err
	}
	before := ""
	if cursor != "" {
		err := tx.QueryRowContext(ctx, `SELECT created_at FROM agent_memory_generations WHERE generation_id=? AND project_id=? AND workspace_id=? AND user_id=?`, cursor, projectID, user.WorkspaceID, user.UserID).Scan(&before)
		if errors.Is(err, sql.ErrNoRows) {
			return page, domainError("AGENT_MEMORY_GENERATION_NOT_FOUND", "Memory generation cursor was not found.")
		}
		if err != nil {
			return page, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT generation_id,project_id,user_id,status,phase,revision,attempt,model_id,error_code,created_at,(status='paused' AND (started=0 OR length(checkpoint_json)>0))
		FROM agent_memory_generations WHERE project_id=? AND workspace_id=? AND user_id=?
		AND (?='' OR created_at<? OR (created_at=? AND generation_id<?)) ORDER BY created_at DESC,generation_id DESC LIMIT 51`,
		projectID, user.WorkspaceID, user.UserID, cursor, before, before, cursor)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item AgentMemoryGenerationSummary
		if err := rows.Scan(&item.GenerationID, &item.ProjectID, &item.UserID, &item.Status, &item.Phase, &item.Revision, &item.Attempt, &item.ModelID, &item.ErrorCode, &item.CreatedAt, &item.ResumeAvailable); err != nil {
			return page, err
		}
		if len(page.Items) == 50 {
			page.NextCursor = page.Items[49].GenerationID
			break
		}
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}
