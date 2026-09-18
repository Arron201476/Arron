package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"content-agent/backend/internal/identity"
)

func migrateAgentMemoryPreferences(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_memory_preferences(
		project_id TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
		workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
		user_id TEXT NOT NULL REFERENCES users(user_id),
		archive_enabled INTEGER NOT NULL CHECK(archive_enabled IN (0,1)),
		generate_enabled INTEGER NOT NULL CHECK(generate_enabled IN (0,1) AND generate_enabled<=archive_enabled),
		revision INTEGER NOT NULL CHECK(revision>0),request_id TEXT NOT NULL,request_hash TEXT NOT NULL,
		PRIMARY KEY(project_id,user_id));`)
	return err
}

type AgentMemoryPreferences struct {
	ProjectID       string `json:"project_id"`
	UserID          string `json:"user_id"`
	ArchiveEnabled  bool   `json:"archive_enabled"`
	GenerateEnabled bool   `json:"generate_enabled"`
	Revision        int    `json:"revision"`
	RequestID       string `json:"request_id,omitempty"`
}

type UpdateAgentMemoryPreferencesCommand struct {
	ProjectID        string `json:"project_id"`
	ArchiveEnabled   bool   `json:"archive_enabled"`
	GenerateEnabled  bool   `json:"generate_enabled"`
	ExpectedRevision int    `json:"expected_revision"`
	RequestID        string `json:"request_id"`
}

func memoryPreferencesQuery(ctx context.Context, query rowQueryer, user identity.Principal, projectID string) (AgentMemoryPreferences, string, error) {
	value := AgentMemoryPreferences{ProjectID: projectID, UserID: user.UserID}
	var hash string
	err := query.QueryRowContext(ctx, `SELECT archive_enabled,generate_enabled,revision,request_id,request_hash FROM agent_memory_preferences WHERE project_id=? AND user_id=? AND workspace_id=?`, projectID, user.UserID, user.WorkspaceID).Scan(&value.ArchiveEnabled, &value.GenerateEnabled, &value.Revision, &value.RequestID, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return value, "", nil
	}
	return value, hash, err
}

func memoryPreferencesUser(ctx context.Context) error {
	user, ok := identity.FromContext(ctx)
	_, delegated := AgentActivityFromContext(ctx)
	if !ok || !user.ValidUser() || delegated {
		return domainError("AUTHENTICATION_REQUIRED", "Memory consent requires the user directly.")
	}
	return nil
}

func (s *Store) ResolveAgentMemoryArchivePolicy(ctx context.Context) (AgentMemoryPreferences, error) {
	var empty AgentMemoryPreferences
	activity, active := AgentActivityFromContext(ctx)
	if !active || activity.MemoryGenerationID != "" {
		return empty, domainError("AGENT_ACTIVITY_FORBIDDEN", "Archive policy requires an original execution.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	source, err := s.memoryRolloutSourceTx(ctx, tx)
	if err != nil {
		return empty, err
	}
	value, _, err := memoryPreferencesQuery(ctx, tx, identity.Principal{UserID: source.UserID, WorkspaceID: source.WorkspaceID}, source.ProjectID)
	return value, err
}

func (s *Store) GetAgentMemoryPreferences(ctx context.Context, projectID string) (AgentMemoryPreferences, error) {
	var empty AgentMemoryPreferences
	if err := memoryPreferencesUser(ctx); err != nil {
		return empty, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	user, err := instructionUserQuery(ctx, tx, projectID, false)
	if err != nil {
		return empty, err
	}
	value, _, err := memoryPreferencesQuery(ctx, tx, user, projectID)
	return value, err
}

func (s *Store) UpdateAgentMemoryPreferences(ctx context.Context, command UpdateAgentMemoryPreferencesCommand) (AgentMemoryPreferences, error) {
	var empty AgentMemoryPreferences
	if err := memoryPreferencesUser(ctx); err != nil {
		return empty, err
	}
	if command.ExpectedRevision < 0 || command.GenerateEnabled && !command.ArchiveEnabled || strings.TrimSpace(command.RequestID) == "" || len(command.RequestID) > 128 {
		return empty, domainError("REQUEST_VALIDATION_FAILED", "Memory consent and revision are invalid.")
	}
	encoded, err := json.Marshal(command)
	if err != nil {
		return empty, err
	}
	hash := sha256Hex(encoded)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	user, err := instructionUserQuery(ctx, tx, command.ProjectID, true)
	if err != nil {
		return empty, err
	}
	current, priorHash, err := memoryPreferencesQuery(ctx, tx, user, command.ProjectID)
	if err != nil {
		return empty, err
	}
	if current.RequestID == command.RequestID {
		if priorHash != hash {
			return empty, domainError("IDEMPOTENCY_KEY_REUSED", "Memory consent request changed.")
		}
		return current, nil
	}
	if current.Revision != command.ExpectedRevision {
		return empty, domainError("AGENT_MEMORY_CONFLICT", "Memory consent changed; refresh before updating.")
	}
	if current.Revision == 0 {
		if err := s.enforceWorkspaceQuotaTx(ctx, tx, user.WorkspaceID, "storage_bytes", 512); err != nil {
			return empty, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_memory_preferences(project_id,workspace_id,user_id,archive_enabled,generate_enabled,revision,request_id,request_hash)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(project_id,user_id) DO UPDATE SET archive_enabled=excluded.archive_enabled,generate_enabled=excluded.generate_enabled,revision=excluded.revision,request_id=excluded.request_id,request_hash=excluded.request_hash`, command.ProjectID, user.WorkspaceID, user.UserID, command.ArchiveEnabled, command.GenerateEnabled, current.Revision+1, command.RequestID, hash)
	if err != nil {
		return empty, err
	}
	value, _, err := memoryPreferencesQuery(ctx, tx, user, command.ProjectID)
	if err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return value, nil
}
