package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"content-agent/backend/internal/identity"
)

const maxAgentMemoryBytes = 1024 * 1024

func migrateAgentMemory(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_memory_versions (
		workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
		project_id TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
		user_id TEXT NOT NULL REFERENCES users(user_id),
		version INTEGER NOT NULL CHECK(version > 0),
		files_json TEXT NOT NULL,
		content_hash TEXT NOT NULL,
		enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
		forgotten INTEGER NOT NULL CHECK(forgotten IN (0,1)),
		request_id TEXT NOT NULL,
		request_hash TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(project_id,user_id,version),
		UNIQUE(project_id,user_id,request_id)
	);`)
	return err
}

type AgentMemoryDocument struct {
	ProjectID   string            `json:"project_id"`
	UserID      string            `json:"user_id"`
	Version     int               `json:"version"`
	Files       map[string]string `json:"files"`
	ContentHash string            `json:"content_hash"`
	Enabled     bool              `json:"enabled"`
	Forgotten   bool              `json:"forgotten"`
	UpdatedAt   string            `json:"updated_at,omitempty"`
	RequestID   string            `json:"request_id,omitempty"`
}

type UpdateAgentMemoryCommand struct {
	ProjectID       string            `json:"project_id"`
	ExpectedVersion int               `json:"expected_version"`
	Files           map[string]string `json:"files"`
	Enabled         bool              `json:"enabled"`
	Forget          bool              `json:"forget"`
	RequestID       string            `json:"request_id"`
}

func validateAgentMemory(command UpdateAgentMemoryCommand) ([]byte, error) {
	invalid := func() ([]byte, error) {
		return nil, domainError("REQUEST_VALIDATION_FAILED", "记忆文件、版本或请求标识无效。")
	}
	if command.ProjectID == "" || command.ExpectedVersion < 0 || strings.TrimSpace(command.RequestID) == "" || len(command.RequestID) > 128 ||
		!utf8.ValidString(command.RequestID) || strings.IndexFunc(command.RequestID, unicode.IsControl) >= 0 || len(command.Files) > 256 ||
		(command.Forget && (command.Enabled || len(command.Files) != 0)) {
		return invalid()
	}
	seen := map[string]string{}
	for path, content := range command.Files {
		if !utf8.ValidString(path) || len(path) > 240 || strings.ContainsAny(path, "\\:*?\"<>|\x00") || strings.IndexFunc(path, unicode.IsControl) >= 0 ||
			!utf8.ValidString(content) || strings.ContainsRune(content, '\x00') {
			return invalid()
		}
		normalized, err := normalizeSkillPackagePath(path)
		if err != nil || normalized != path {
			return invalid()
		}
		if err := registerSkillPackagePath(seen, path); err != nil {
			return invalid()
		}
	}
	if command.Enabled && strings.TrimSpace(command.Files["memory_summary.md"]) == "" {
		return invalid()
	}
	files := command.Files
	if files == nil {
		files = map[string]string{}
	}
	encoded, err := json.Marshal(files)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxAgentMemoryBytes {
		return invalid()
	}
	return encoded, nil
}

func agentMemoryQuery(ctx context.Context, query rowQueryer, user identity.Principal, projectID string, version int) (AgentMemoryDocument, error) {
	doc := AgentMemoryDocument{ProjectID: projectID, UserID: user.UserID, Files: map[string]string{}, ContentHash: sha256Hex([]byte("{}"))}
	statement := `SELECT version,files_json,content_hash,enabled,forgotten,updated_at,request_id FROM agent_memory_versions WHERE workspace_id=? AND project_id=? AND user_id=?`
	args := []any{user.WorkspaceID, projectID, user.UserID}
	if version > 0 {
		statement += ` AND version=?`
		args = append(args, version)
	}
	statement += ` ORDER BY version DESC LIMIT 1`
	var encoded string
	err := query.QueryRowContext(ctx, statement, args...).Scan(&doc.Version, &encoded, &doc.ContentHash, &doc.Enabled, &doc.Forgotten, &doc.UpdatedAt, &doc.RequestID)
	if errors.Is(err, sql.ErrNoRows) && version == 0 {
		return doc, nil
	}
	if err != nil {
		return AgentMemoryDocument{}, err
	}
	if sha256Hex([]byte(encoded)) != doc.ContentHash || json.Unmarshal([]byte(encoded), &doc.Files) != nil || doc.Files == nil {
		return AgentMemoryDocument{}, domainError("AGENT_MEMORY_INVALID", "已保存记忆未通过完整性校验。")
	}
	return doc, nil
}

func (s *Store) GetAgentMemory(ctx context.Context, projectID string) (AgentMemoryDocument, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentMemoryDocument{}, err
	}
	defer tx.Rollback()
	if projectID == "" {
		return AgentMemoryDocument{}, domainError("REQUEST_VALIDATION_FAILED", "需要指定作品。")
	}
	user, err := instructionUserQuery(ctx, tx, projectID, false)
	if err != nil {
		return AgentMemoryDocument{}, err
	}
	return agentMemoryQuery(ctx, tx, user, projectID, 0)
}

func (s *Store) UpdateAgentMemory(ctx context.Context, command UpdateAgentMemoryCommand) (AgentMemoryDocument, error) {
	principal, ok := identity.FromContext(ctx)
	if !ok || !principal.ValidUser() {
		return AgentMemoryDocument{}, domainError("AUTHENTICATION_REQUIRED", "需要用户身份。")
	}
	files, err := validateAgentMemory(command)
	if err != nil {
		return AgentMemoryDocument{}, err
	}
	// Freeze request bytes before entering the transaction; callers cannot mutate the saved body.
	command.Files = nil
	encoded, err := json.Marshal(struct {
		Command UpdateAgentMemoryCommand
		Files   json.RawMessage
	}{command, files})
	if err != nil {
		return AgentMemoryDocument{}, err
	}
	requestHash := sha256Hex(encoded)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentMemoryDocument{}, err
	}
	defer tx.Rollback()
	user, err := instructionUserQuery(ctx, tx, command.ProjectID, true)
	if err != nil {
		return AgentMemoryDocument{}, err
	}
	var priorVersion int
	var priorHash string
	err = tx.QueryRowContext(ctx, `SELECT version,request_hash FROM agent_memory_versions WHERE workspace_id=? AND project_id=? AND user_id=? AND request_id=?`,
		user.WorkspaceID, command.ProjectID, user.UserID, command.RequestID).Scan(&priorVersion, &priorHash)
	if err == nil {
		if priorHash != requestHash {
			return AgentMemoryDocument{}, domainError("IDEMPOTENCY_CONFLICT", "请求标识已用于不同的记忆修改。")
		}
		return agentMemoryQuery(ctx, tx, user, command.ProjectID, priorVersion)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AgentMemoryDocument{}, err
	}
	current, err := agentMemoryQuery(ctx, tx, user, command.ProjectID, 0)
	if err != nil {
		return AgentMemoryDocument{}, err
	}
	if current.Version != command.ExpectedVersion {
		return AgentMemoryDocument{}, domainError("AGENT_MEMORY_CONFLICT", "记忆版本已变化，请刷新后重试。")
	}
	if command.Forget {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_preferences SET archive_enabled=0,generate_enabled=0,request_id='',request_hash='',revision=revision+1 WHERE workspace_id=? AND project_id=? AND user_id=?`, user.WorkspaceID, command.ProjectID, user.UserID); err != nil {
			return AgentMemoryDocument{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_tool_calls SET arguments_json='' WHERE generation_id IN (SELECT generation_id FROM agent_memory_generations WHERE workspace_id=? AND project_id=? AND user_id=?)`, user.WorkspaceID, command.ProjectID, user.UserID); err != nil {
			return AgentMemoryDocument{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET status=CASE WHEN status='completed' THEN status ELSE 'cancelled' END,checkpoint_json='',checkpoint_hash=?,extraction_json='',extraction_receipt='',input_plan_json='',input_plan_hash='',token_hash='',lease_until='',error_code='MEMORY_GENERATION_FORGOTTEN',revision=revision+1 WHERE workspace_id=? AND project_id=? AND user_id=?`, sha256Hex(nil), user.WorkspaceID, command.ProjectID, user.UserID); err != nil {
			return AgentMemoryDocument{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_rollouts SET envelope_json='',forgotten=1,archive_revision=0,archive_generate=0,archive_generation_id='' WHERE workspace_id=? AND project_id=? AND user_id=?`, user.WorkspaceID, command.ProjectID, user.UserID); err != nil {
			return AgentMemoryDocument{}, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_memory_versions SET files_json='{}',content_hash=?,enabled=0,forgotten=1 WHERE workspace_id=? AND project_id=? AND user_id=?`,
			sha256Hex([]byte("{}")), user.WorkspaceID, command.ProjectID, user.UserID)
		if err != nil {
			return AgentMemoryDocument{}, err
		}
	} else {
		if err := s.enforceWorkspaceQuotaTx(ctx, tx, user.WorkspaceID, "storage_bytes", int64(len(files))); err != nil {
			return AgentMemoryDocument{}, err
		}
	}
	doc, err := s.insertAgentMemoryVersionTx(ctx, tx, user, command, files, requestHash)
	if err != nil {
		return AgentMemoryDocument{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentMemoryDocument{}, err
	}
	return doc, nil
}

func (s *Store) insertAgentMemoryVersionTx(ctx context.Context, tx *sql.Tx, user identity.Principal, command UpdateAgentMemoryCommand, files []byte, requestHash string) (AgentMemoryDocument, error) {
	doc := AgentMemoryDocument{ProjectID: command.ProjectID, UserID: user.UserID, Version: command.ExpectedVersion + 1, ContentHash: sha256Hex(files), Enabled: command.Enabled, Forgotten: command.Forget, UpdatedAt: formatTime(s.now()), RequestID: command.RequestID}
	if err := json.Unmarshal(files, &doc.Files); err != nil {
		return AgentMemoryDocument{}, err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_memory_versions(workspace_id,project_id,user_id,version,files_json,content_hash,enabled,forgotten,request_id,request_hash,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		user.WorkspaceID, doc.ProjectID, doc.UserID, doc.Version, string(files), doc.ContentHash, doc.Enabled, doc.Forgotten, command.RequestID, requestHash, doc.UpdatedAt)
	if err != nil {
		return AgentMemoryDocument{}, err
	}
	return doc, nil
}
