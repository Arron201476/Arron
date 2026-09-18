package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func (s *Store) CreateProject(ctx context.Context, title string) (Project, error) {
	return s.CreateProjectCommand(ctx, title, CommandMeta{})
}

func (s *Store) CreateProjectCommand(ctx context.Context, title string, meta CommandMeta) (Project, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "未命名作品"
	}
	now := s.now()
	projectID := s.newID("prj")
	conversationID := s.newID("con")
	workspaceID := identity.WorkspaceIDFromContext(ctx)
	ownerUserID := identity.UserIDFromContext(ctx)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	if meta.Scope == "" {
		meta.Scope = workspaceID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, meta)
	if err != nil {
		return Project{}, err
	}
	if hit {
		return decodeIdempotentResult[Project](cached)
	}
	if err := s.enforceWorkspaceQuotaTx(ctx, tx, workspaceID, "projects", 1); err != nil {
		return Project{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO projects(
			project_id, workspace_id, owner_user_id, title, version, status, primary_conversation_id,
			project_event_seq, created_at, updated_at
		) VALUES(?, ?, ?, ?, 1, 'ready', ?, 0, ?, ?)`,
		projectID, workspaceID, ownerUserID, title, conversationID, formatTime(now), formatTime(now),
	); err != nil {
		return Project{}, fmt.Errorf("create project: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO conversations(conversation_id, project_id, is_primary, created_at, updated_at)
		VALUES(?, ?, 1, ?, ?)`,
		conversationID, projectID, formatTime(now), formatTime(now),
	); err != nil {
		return Project{}, fmt.Errorf("create primary conversation: %w", err)
	}
	if _, err := s.appendEvent(ctx, tx, projectID, nil, nil, "project.created", "project", projectID, nil); err != nil {
		return Project{}, err
	}
	project := Project{
		ProjectID:             projectID,
		WorkspaceID:           workspaceID,
		OwnerUserID:           ownerUserID,
		Title:                 title,
		Version:               1,
		Status:                "ready",
		PrimaryConversationID: conversationID,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := completeIdempotency(ctx, tx, meta, project, now); err != nil {
		return Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return project, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	query := `
		SELECT project_id, workspace_id, owner_user_id, title, version, status, primary_conversation_id,
			active_write_run_id, current_capability_id, current_focus_artifact_version_id,
			(SELECT final_selection_id FROM final_selections
			 WHERE project_id = projects.project_id AND status = 'active'),
			(SELECT capability_id FROM runs
			 WHERE project_id = projects.project_id ORDER BY created_at DESC, run_id DESC LIMIT 1),
			(SELECT status FROM runs
			 WHERE project_id = projects.project_id ORDER BY created_at DESC, run_id DESC LIMIT 1),
			created_at, updated_at, deleted_at
		FROM projects
		WHERE deleted_at IS NULL`
	args := []any{}
	if principal, ok := identity.UserFromContext(ctx); ok {
		query += ` AND workspace_id = ?`
		args = append(args, principal.WorkspaceID)
	}
	query += ` ORDER BY updated_at DESC, project_id ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []Project
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if projects == nil {
		projects = []Project{}
	}
	return projects, nil
}

func (s *Store) GetProject(ctx context.Context, projectID string) (Project, error) {
	query := `
		SELECT project_id, workspace_id, owner_user_id, title, version, status, primary_conversation_id,
			active_write_run_id, current_capability_id, current_focus_artifact_version_id,
			(SELECT final_selection_id FROM final_selections
			 WHERE project_id = projects.project_id AND status = 'active'),
			(SELECT capability_id FROM runs
			 WHERE project_id = projects.project_id ORDER BY created_at DESC, run_id DESC LIMIT 1),
			(SELECT status FROM runs
			 WHERE project_id = projects.project_id ORDER BY created_at DESC, run_id DESC LIMIT 1),
			created_at, updated_at, deleted_at
		FROM projects
		WHERE project_id = ? AND deleted_at IS NULL`
	args := []any{projectID}
	if principal, ok := identity.UserFromContext(ctx); ok {
		query += ` AND workspace_id = ?`
		args = append(args, principal.WorkspaceID)
	}
	row := s.db.QueryRowContext(ctx, query, args...)
	project, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, domainError("PROJECT_NOT_FOUND", "作品不存在。")
	}
	return project, err
}

func (s *Store) RenameProject(ctx context.Context, projectID, title string, expectedVersion int) (Project, error) {
	return s.RenameProjectCommand(ctx, projectID, title, expectedVersion, CommandMeta{})
}

func (s *Store) RenameProjectCommand(
	ctx context.Context,
	projectID string,
	title string,
	expectedVersion int,
	meta CommandMeta,
) (Project, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Project{}, domainError("REQUEST_VALIDATION_FAILED", "作品名称不能为空。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	if meta.Scope == "" {
		meta.Scope = projectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, meta)
	if err != nil {
		return Project{}, err
	}
	if hit {
		return decodeIdempotentResult[Project](cached)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET title = ?, version = version + 1, updated_at = ?
		WHERE project_id = ? AND deleted_at IS NULL AND version = ?`,
		title, formatTime(now), projectID, expectedVersion,
	)
	if err != nil {
		return Project{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Project{}, err
	}
	if affected == 0 {
		var currentVersion int
		if queryErr := tx.QueryRowContext(ctx, `SELECT version FROM projects WHERE project_id = ? AND deleted_at IS NULL`, projectID).Scan(&currentVersion); errors.Is(queryErr, sql.ErrNoRows) {
			return Project{}, domainError("PROJECT_NOT_FOUND", "作品不存在。")
		} else if queryErr != nil {
			return Project{}, queryErr
		}
		return Project{}, domainError("PROJECT_VERSION_CONFLICT", fmt.Sprintf("作品版本已更新，当前版本为 %d。", currentVersion))
	}
	if _, err := s.appendEvent(ctx, tx, projectID, nil, nil, "project.renamed", "project", projectID, map[string]any{"title": title}); err != nil {
		return Project{}, err
	}
	project, err := scanProject(tx.QueryRowContext(ctx, `
		SELECT project_id, workspace_id, owner_user_id, title, version, status, primary_conversation_id,
			active_write_run_id, current_capability_id, current_focus_artifact_version_id,
			(SELECT final_selection_id FROM final_selections
			 WHERE project_id = projects.project_id AND status = 'active'),
			(SELECT capability_id FROM runs
			 WHERE project_id = projects.project_id ORDER BY created_at DESC, run_id DESC LIMIT 1),
			(SELECT status FROM runs
			 WHERE project_id = projects.project_id ORDER BY created_at DESC, run_id DESC LIMIT 1),
			created_at, updated_at, deleted_at
		FROM projects WHERE project_id = ? AND deleted_at IS NULL`, projectID))
	if err != nil {
		return Project{}, err
	}
	if err := completeIdempotency(ctx, tx, meta, project, now); err != nil {
		return Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return project, nil
}

func (s *Store) ListConversations(ctx context.Context, projectID string) ([]Conversation, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT conversation_id, project_id, is_primary, created_at, updated_at
		FROM conversations
		WHERE project_id = ?
		ORDER BY is_primary DESC, conversation_id ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Conversation
	for rows.Next() {
		var conversation Conversation
		var isPrimary int
		var createdAt, updatedAt string
		if err := rows.Scan(&conversation.ConversationID, &conversation.ProjectID, &isPrimary, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		conversation.IsPrimary = isPrimary != 0
		conversation.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		conversation.UpdatedAt, err = parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		result = append(result, conversation)
	}
	if result == nil {
		result = []Conversation{}
	}
	return result, rows.Err()
}

func (s *Store) ConversationProjectID(ctx context.Context, conversationID string) (string, error) {
	var projectID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT project_id FROM conversations WHERE conversation_id = ?`,
		conversationID,
	).Scan(&projectID); errors.Is(err, sql.ErrNoRows) {
		return "", domainError("CONVERSATION_NOT_FOUND", "对话不存在。")
	} else if err != nil {
		return "", err
	}
	return projectID, nil
}

func (s *Store) CreateUserMessage(ctx context.Context, conversationID, content string) (Message, error) {
	return s.CreateUserMessageCommand(ctx, conversationID, content, CommandMeta{})
}

func (s *Store) CreateUserMessageCommand(
	ctx context.Context,
	conversationID string,
	content string,
	meta CommandMeta,
) (Message, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return Message{}, domainError("REQUEST_VALIDATION_FAILED", "消息内容不能为空。")
	}
	var projectID string
	if err := s.db.QueryRowContext(ctx, `SELECT project_id FROM conversations WHERE conversation_id = ?`, conversationID).Scan(&projectID); errors.Is(err, sql.ErrNoRows) {
		return Message{}, domainError("CONVERSATION_NOT_FOUND", "对话不存在。")
	} else if err != nil {
		return Message{}, err
	}
	now := s.now()
	message := Message{
		MessageID:      s.newID("msg"),
		ConversationID: conversationID,
		ProjectID:      projectID,
		Role:           "user",
		Content:        content,
		CreatedAt:      now,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	if meta.Scope == "" {
		meta.Scope = projectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, meta)
	if err != nil {
		return Message{}, err
	}
	if hit {
		return decodeIdempotentResult[Message](cached)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO messages(message_id, conversation_id, project_id, role, content, created_at)
		VALUES(?, ?, ?, ?, ?, ?)`,
		message.MessageID, message.ConversationID, message.ProjectID, message.Role, message.Content, formatTime(message.CreatedAt),
	); err != nil {
		return Message{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET updated_at = ? WHERE conversation_id = ?`, formatTime(now), conversationID); err != nil {
		return Message{}, err
	}
	if _, err := s.appendEvent(ctx, tx, projectID, nil, nil, "message.created", "message", message.MessageID, nil); err != nil {
		return Message{}, err
	}
	if err := completeIdempotency(ctx, tx, meta, message, now); err != nil {
		return Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, err
	}
	return message, nil
}

func (s *Store) ListMessages(ctx context.Context, conversationID string) ([]Message, error) {
	return s.SearchMessages(ctx, conversationID, "", 0)
}

// SearchMessages returns authoritative conversation history without creating a
// second memory store. A zero limit preserves the existing unbounded list API.
func (s *Store) SearchMessages(
	ctx context.Context,
	conversationID string,
	query string,
	limit int,
) ([]Message, error) {
	query = strings.TrimSpace(query)
	if limit < 0 {
		limit = 0
	}
	if limit > 100 {
		limit = 100
	}
	where := "m.conversation_id = ?"
	args := []any{conversationID}
	if query != "" {
		where += " AND instr(lower(m.content), lower(?)) > 0"
		args = append(args, query)
	}
	order := "m.rowid ASC"
	if limit > 0 {
		order = "m.rowid DESC"
	}
	statement := `
		SELECT m.message_id, m.conversation_id, m.project_id, m.role, m.content, m.created_at,
		       mc.capability_ref_json, mc.attachment_refs_json, mc.selection_snapshot_json, mc.client_context_json,
		       COALESCE(mrc.scope, 'project'), mrc.invocation_id, mrc.run_id, mrc.capability_id, mrc.artifact_id
		FROM messages m
		LEFT JOIN message_contexts mc ON mc.message_id = m.message_id
		LEFT JOIN message_routing_contexts mrc ON mrc.message_id = m.message_id
		WHERE ` + where + `
		ORDER BY ` + order
	if limit > 0 {
		statement += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Message
	for rows.Next() {
		var message Message
		var createdAt string
		var capabilityJSON, attachmentsJSON, selectionJSON, clientJSON sql.NullString
		var routingScope string
		var routingInvocationID, routingRunID, routingCapabilityID, routingArtifactID sql.NullString
		if err := rows.Scan(&message.MessageID, &message.ConversationID, &message.ProjectID, &message.Role, &message.Content, &createdAt,
			&capabilityJSON, &attachmentsJSON, &selectionJSON, &clientJSON,
			&routingScope, &routingInvocationID, &routingRunID, &routingCapabilityID, &routingArtifactID); err != nil {
			return nil, err
		}
		message.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		if capabilityJSON.Valid || attachmentsJSON.Valid || selectionJSON.Valid || clientJSON.Valid {
			context := MessageContext{
				AttachmentRefs: []agentcontract.AttachmentRef{},
				RoutingContext: MessageRoutingContext{
					Scope: routingScope, InvocationID: stringPointer(routingInvocationID), RunID: stringPointer(routingRunID),
					CapabilityID: stringPointer(routingCapabilityID), ArtifactID: stringPointer(routingArtifactID),
				},
			}
			if capabilityJSON.Valid && capabilityJSON.String != "null" {
				context.CapabilityRef = &agentcontract.CapabilityRef{}
				if err := json.Unmarshal([]byte(capabilityJSON.String), context.CapabilityRef); err != nil {
					return nil, err
				}
			}
			if attachmentsJSON.Valid && attachmentsJSON.String != "" {
				if err := json.Unmarshal([]byte(attachmentsJSON.String), &context.AttachmentRefs); err != nil {
					return nil, err
				}
			}
			if selectionJSON.Valid && selectionJSON.String != "null" {
				context.SelectionSnapshot = &agentcontract.SelectionSnapshot{}
				if err := json.Unmarshal([]byte(selectionJSON.String), context.SelectionSnapshot); err != nil {
					return nil, err
				}
			}
			if clientJSON.Valid && clientJSON.String != "" {
				if err := json.Unmarshal([]byte(clientJSON.String), &context.ClientContext); err != nil {
					return nil, err
				}
			}
			message.Context = &context
		} else {
			message.Context = &MessageContext{AttachmentRefs: []agentcontract.AttachmentRef{}, RoutingContext: MessageRoutingContext{
				Scope: routingScope, InvocationID: stringPointer(routingInvocationID), RunID: stringPointer(routingRunID),
				CapabilityID: stringPointer(routingCapabilityID), ArtifactID: stringPointer(routingArtifactID),
			}}
		}
		result = append(result, message)
	}
	if result == nil {
		result = []Message{}
	}
	if limit > 0 {
		slices.Reverse(result)
	}
	return result, rows.Err()
}

func (s *Store) appendEvent(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runID *string,
	stepRunID *string,
	eventType string,
	subjectType string,
	subjectID string,
	payload any,
) (Event, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return Event{}, err
	}
	var projectSeq int64
	if err := tx.QueryRowContext(ctx, `SELECT project_event_seq FROM projects WHERE project_id = ?`, projectID).Scan(&projectSeq); err != nil {
		return Event{}, err
	}
	projectSeq++
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET project_event_seq = ? WHERE project_id = ?`, projectSeq, projectID); err != nil {
		return Event{}, err
	}

	var runSeq *int64
	if runID != nil {
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT run_event_seq FROM runs WHERE run_id = ?`, *runID).Scan(&current); err != nil {
			return Event{}, err
		}
		current++
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET run_event_seq = ? WHERE run_id = ?`, current, *runID); err != nil {
			return Event{}, err
		}
		runSeq = &current
	}
	now := s.now()
	event := Event{
		EventID:         s.newID("evt"),
		EventType:       eventType,
		SchemaVersion:   1,
		ProjectID:       projectID,
		RunID:           runID,
		StepRunID:       stepRunID,
		ProjectEventSeq: projectSeq,
		RunEventSeq:     runSeq,
		ActorKind:       "runtime",
		ActorRef:        "content_agent_runtime",
		SubjectType:     subjectType,
		SubjectID:       subjectID,
		Payload:         payloadJSON,
		OccurredAt:      now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO events(
			event_id, event_type, schema_version, project_id, run_id, step_run_id,
			project_event_seq, run_event_seq, actor_kind, actor_ref, subject_type,
			subject_id, payload_json, occurred_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.EventType, event.SchemaVersion, event.ProjectID, nullableString(runID),
		nullableString(stepRunID), event.ProjectEventSeq, nullableInt64(runSeq), event.ActorKind,
		event.ActorRef, event.SubjectType, event.SubjectID, string(event.Payload), formatTime(event.OccurredAt),
	); err != nil {
		return Event{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO event_outbox(
			event_id, project_id, run_id, status, delivery_attempts,
			available_at, created_at, published_at
		) VALUES(?, ?, ?, 'pending', 0, ?, ?, NULL)`,
		event.EventID, event.ProjectID, nullableString(runID), formatTime(now), formatTime(now),
	); err != nil {
		return Event{}, err
	}
	return event, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProject(row rowScanner) (Project, error) {
	var project Project
	var activeRunID, capabilityID, focusVersionID, finalSelectionID, latestCapabilityID, latestRunStatus, deletedAt sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(
		&project.ProjectID, &project.WorkspaceID, &project.OwnerUserID, &project.Title, &project.Version, &project.Status,
		&project.PrimaryConversationID, &activeRunID, &capabilityID, &focusVersionID,
		&finalSelectionID, &latestCapabilityID, &latestRunStatus,
		&createdAt, &updatedAt, &deletedAt,
	); err != nil {
		return Project{}, err
	}
	project.ActiveWriteRunID = stringPointer(activeRunID)
	project.CurrentCapabilityID = stringPointer(capabilityID)
	project.LatestCapabilityID = stringPointer(latestCapabilityID)
	project.LatestRunStatus = stringPointer(latestRunStatus)
	project.CurrentFocusArtifactVersionID = stringPointer(focusVersionID)
	project.CurrentFinalSelectionID = stringPointer(finalSelectionID)
	var err error
	project.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Project{}, err
	}
	project.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return Project{}, err
	}
	if deletedAt.Valid {
		value, err := parseTime(deletedAt.String)
		if err != nil {
			return Project{}, err
		}
		project.DeletedAt = &value
	}
	return project, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func stringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	copy := value.String
	return &copy
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
