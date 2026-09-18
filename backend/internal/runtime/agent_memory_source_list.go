package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"content-agent/backend/internal/identity"
)

type AgentMemorySourceSummary struct {
	ActivityKey  string `json:"activity_key"`
	SegmentID    string `json:"segment_id"`
	SourceHash   string `json:"source_hash"`
	GenerationID string `json:"generation_id,omitempty"`
}

type AgentMemorySourcePage struct {
	ProjectID  string                     `json:"project_id"`
	UserID     string                     `json:"user_id"`
	Items      []AgentMemorySourceSummary `json:"items"`
	NextCursor string                     `json:"next_cursor,omitempty"`
}

func (s *Store) ListAgentMemorySources(ctx context.Context, projectID, cursor string) (AgentMemorySourcePage, error) {
	page := AgentMemorySourcePage{ProjectID: projectID, Items: []AgentMemorySourceSummary{}}
	principal, ok := identity.FromContext(ctx)
	_, delegated := AgentActivityFromContext(ctx)
	if !ok || !principal.ValidUser() || delegated {
		return page, domainError("AUTHENTICATION_REQUIRED", "Memory sources require the user directly.")
	}
	var after []string
	if cursor != "" {
		if len(cursor) > 768 {
			return page, domainError("REQUEST_VALIDATION_FAILED", "Memory source cursor is invalid.")
		}
		body, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(body, &after) != nil || len(after) != 2 || len(after[0]) == 0 || len(after[0]) > 270 || !memorySegmentPattern.MatchString(after[1]) {
			return page, domainError("REQUEST_VALIDATION_FAILED", "Memory source cursor is invalid.")
		}
	} else {
		after = []string{"", ""}
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
	page.UserID = user.UserID
	if cursor != "" {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_memory_rollouts WHERE project_id=? AND workspace_id=? AND user_id=? AND activity_key=? AND segment_id=? AND forgotten=0)`, projectID, user.WorkspaceID, user.UserID, after[0], after[1]).Scan(&exists); err != nil {
			return page, err
		}
		if !exists {
			return page, domainError("REQUEST_VALIDATION_FAILED", "Memory source cursor is unavailable; refresh the list.")
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.activity_key,r.segment_id,r.content_hash,COALESCE(g.generation_id,'')
		FROM agent_memory_rollouts r LEFT JOIN agent_memory_generations g ON g.activity_key=r.activity_key AND g.segment_id=r.segment_id
		AND g.project_id=r.project_id AND g.workspace_id=r.workspace_id AND g.user_id=r.user_id
		WHERE r.project_id=? AND r.workspace_id=? AND r.user_id=? AND r.forgotten=0
		AND (?='' OR r.activity_key<? OR (r.activity_key=? AND r.segment_id<?))
		ORDER BY r.activity_key DESC,r.segment_id DESC LIMIT 51`, projectID, user.WorkspaceID, user.UserID, cursor, after[0], after[0], after[1])
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item AgentMemorySourceSummary
		if err := rows.Scan(&item.ActivityKey, &item.SegmentID, &item.SourceHash, &item.GenerationID); err != nil {
			return page, err
		}
		if len(page.Items) == 50 {
			last := page.Items[49]
			body, err := json.Marshal([]string{last.ActivityKey, last.SegmentID})
			if err != nil {
				return page, err
			}
			page.NextCursor = base64.RawURLEncoding.EncodeToString(body)
			break
		}
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}
