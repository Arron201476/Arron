package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"novel2script-agent/backend/internal/agent"

	_ "modernc.org/sqlite"
)

const workspaceSchemaVersion = 2

func newWorkspaceID(prefix string) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(raw[:]), nil
}

type workspaceStore struct {
	db *sql.DB
}

func openWorkspaceStore(path string) (*workspaceStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create workspace store directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open workspace store: %w", err)
	}
	// Workspace writes are small and ordered. One connection prevents competing
	// writers inside this process; busy_timeout covers brief external overlap
	// during a safe backend handover.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &workspaceStore{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func workspaceStorePath() string {
	return filepath.Join(stateDirectory(), "workspace.db")
}

func (s *workspaceStore) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *workspaceStore) migrate() error {
	var userVersion int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		return fmt.Errorf("read workspace schema version: %w", err)
	}
	if userVersion > workspaceSchemaVersion {
		return fmt.Errorf("workspace database schema version %d is newer than supported version %d", userVersion, workspaceSchemaVersion)
	}
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE IF NOT EXISTS workspace_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS projects (
			project_id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			source_mode TEXT NOT NULL,
			status TEXT NOT NULL,
			active_run_id TEXT NOT NULL DEFAULT '',
			current_focus_artifact_id TEXT NOT NULL DEFAULT '',
			active_artifacts_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			message_id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			run_id TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			attachments_json TEXT NOT NULL DEFAULT '[]',
			selection_context_json TEXT NOT NULL DEFAULT '{}',
			intent TEXT NOT NULL DEFAULT '',
			decision_context_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			FOREIGN KEY(project_id) REFERENCES projects(project_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_project_created
			ON messages(project_id, created_at, message_id)`,
		`CREATE TABLE IF NOT EXISTS files (
			file_id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			filename TEXT NOT NULL,
			mime_type TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			text_preview TEXT NOT NULL DEFAULT '',
			text_content TEXT NOT NULL DEFAULT '',
			content_base64 TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			FOREIGN KEY(project_id) REFERENCES projects(project_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_files_project_created
			ON files(project_id, created_at, file_id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("migrate workspace store: %w", err)
		}
	}
	if err := s.ensureColumn("messages", "decision_context_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO workspace_meta(key, value) VALUES('schema_version', ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		fmt.Sprint(workspaceSchemaVersion),
	)
	if err != nil {
		return fmt.Errorf("write workspace schema version: %w", err)
	}
	if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, workspaceSchemaVersion)); err != nil {
		return fmt.Errorf("write workspace user version: %w", err)
	}
	return nil
}

func (s *workspaceStore) ensureColumn(table, column, definition string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("scan %s columns: %w", table, err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect %s columns: %w", table, err)
	}
	if _, err := s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

func (s *workspaceStore) health() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("workspace store is not configured")
	}
	var version string
	if err := s.db.QueryRow(`SELECT value FROM workspace_meta WHERE key='schema_version'`).Scan(&version); err != nil {
		return fmt.Errorf("workspace store health check: %w", err)
	}
	return nil
}

func (s *workspaceStore) load() (map[string]projectDTO, map[string][]messageDTO, map[string]fileDTO, error) {
	projects, err := s.loadProjects()
	if err != nil {
		return nil, nil, nil, err
	}
	messages, err := s.loadMessages()
	if err != nil {
		return nil, nil, nil, err
	}
	files, err := s.loadFiles()
	if err != nil {
		return nil, nil, nil, err
	}
	return projects, messages, files, nil
}

func (s *workspaceStore) loadProjects() (map[string]projectDTO, error) {
	rows, err := s.db.Query(`SELECT project_id, title, source_mode, status, active_run_id,
		current_focus_artifact_id, active_artifacts_json, created_at, updated_at FROM projects`)
	if err != nil {
		return nil, fmt.Errorf("load projects: %w", err)
	}
	defer rows.Close()
	out := map[string]projectDTO{}
	for rows.Next() {
		var project projectDTO
		var sourceMode, status, activeJSON, createdAt, updatedAt string
		if err := rows.Scan(
			&project.ProjectID, &project.Title, &sourceMode, &status, &project.ActiveRunID,
			&project.CurrentFocusArtifactID, &activeJSON, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		project.SourceMode = agent.SourceMode(sourceMode)
		project.Status = projectStatus(status)
		project.ActiveArtifacts = map[string]string{}
		if err := json.Unmarshal([]byte(activeJSON), &project.ActiveArtifacts); err != nil {
			return nil, fmt.Errorf("decode project active artifacts: %w", err)
		}
		project.CreatedAt, err = parseStoreTime(createdAt)
		if err != nil {
			return nil, err
		}
		project.UpdatedAt, err = parseStoreTime(updatedAt)
		if err != nil {
			return nil, err
		}
		out[project.ProjectID] = project
	}
	return out, rows.Err()
}

func (s *workspaceStore) loadMessages() (map[string][]messageDTO, error) {
	rows, err := s.db.Query(`SELECT message_id, project_id, run_id, role, content,
		attachments_json, selection_context_json, intent, decision_context_json, created_at
		FROM messages ORDER BY created_at, message_id`)
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	defer rows.Close()
	out := map[string][]messageDTO{}
	for rows.Next() {
		var message messageDTO
		var attachmentsJSON, selectionJSON, decisionJSON, createdAt string
		if err := rows.Scan(
			&message.MessageID, &message.ProjectID, &message.RunID, &message.Role, &message.Content,
			&attachmentsJSON, &selectionJSON, &message.Intent, &decisionJSON, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		if err := json.Unmarshal([]byte(attachmentsJSON), &message.Attachments); err != nil {
			return nil, fmt.Errorf("decode message attachments: %w", err)
		}
		if err := json.Unmarshal([]byte(selectionJSON), &message.SelectionContext); err != nil {
			return nil, fmt.Errorf("decode message selection: %w", err)
		}
		if err := json.Unmarshal([]byte(decisionJSON), &message.DecisionContext); err != nil {
			return nil, fmt.Errorf("decode message decision context: %w", err)
		}
		message.CreatedAt, err = parseStoreTime(createdAt)
		if err != nil {
			return nil, err
		}
		out[message.ProjectID] = append(out[message.ProjectID], message)
	}
	return out, rows.Err()
}

func (s *workspaceStore) loadFiles() (map[string]fileDTO, error) {
	rows, err := s.db.Query(`SELECT file_id, project_id, filename, mime_type, size_bytes,
		status, text_preview, text_content, content_base64, created_at FROM files`)
	if err != nil {
		return nil, fmt.Errorf("load files: %w", err)
	}
	defer rows.Close()
	out := map[string]fileDTO{}
	for rows.Next() {
		var file fileDTO
		var createdAt string
		if err := rows.Scan(
			&file.FileID, &file.ProjectID, &file.Filename, &file.MimeType, &file.SizeBytes,
			&file.Status, &file.TextPreview, &file.TextContent, &file.ContentBase64, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		file.CreatedAt, err = parseStoreTime(createdAt)
		if err != nil {
			return nil, err
		}
		out[file.FileID] = file
	}
	return out, rows.Err()
}

func (s *workspaceStore) upsertProject(project projectDTO) error {
	activeJSON, err := json.Marshal(project.ActiveArtifacts)
	if err != nil {
		return fmt.Errorf("encode project active artifacts: %w", err)
	}
	_, err = s.db.Exec(`INSERT INTO projects(
		project_id, title, source_mode, status, active_run_id, current_focus_artifact_id,
		active_artifacts_json, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(project_id) DO UPDATE SET
		title=excluded.title,
		source_mode=excluded.source_mode,
		status=excluded.status,
		active_run_id=excluded.active_run_id,
		current_focus_artifact_id=excluded.current_focus_artifact_id,
		active_artifacts_json=excluded.active_artifacts_json,
		updated_at=excluded.updated_at`,
		project.ProjectID, project.Title, project.SourceMode, project.Status, project.ActiveRunID,
		project.CurrentFocusArtifactID, string(activeJSON), storeTime(project.CreatedAt), storeTime(project.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("save project: %w", err)
	}
	return nil
}

func (s *workspaceStore) insertMessage(message messageDTO) error {
	attachmentsJSON, err := json.Marshal(message.Attachments)
	if err != nil {
		return fmt.Errorf("encode message attachments: %w", err)
	}
	selectionJSON, err := json.Marshal(message.SelectionContext)
	if err != nil {
		return fmt.Errorf("encode message selection: %w", err)
	}
	decisionJSON, err := json.Marshal(message.DecisionContext)
	if err != nil {
		return fmt.Errorf("encode message decision context: %w", err)
	}
	_, err = s.db.Exec(`INSERT INTO messages(
		message_id, project_id, run_id, role, content, attachments_json,
		selection_context_json, intent, decision_context_json, created_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		message.MessageID, message.ProjectID, message.RunID, message.Role, message.Content,
		string(attachmentsJSON), string(selectionJSON), message.Intent, string(decisionJSON), storeTime(message.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("save message: %w", err)
	}
	return nil
}

func (s *workspaceStore) insertFile(file fileDTO) error {
	_, err := s.db.Exec(`INSERT INTO files(
		file_id, project_id, filename, mime_type, size_bytes, status,
		text_preview, text_content, content_base64, created_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		file.FileID, file.ProjectID, file.Filename, file.MimeType, file.SizeBytes, file.Status,
		file.TextPreview, file.TextContent, file.ContentBase64, storeTime(file.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("save file: %w", err)
	}
	return nil
}

func (s *workspaceStore) deleteFile(fileID string) error {
	result, err := s.db.Exec(`DELETE FROM files WHERE file_id=?`, fileID)
	if err != nil {
		return fmt.Errorf("delete file: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted file count: %w", err)
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *workspaceStore) deleteProject(projectID string) error {
	result, err := s.db.Exec(`DELETE FROM projects WHERE project_id=?`, projectID)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted project count: %w", err)
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func storeTime(value time.Time) string {
	if value.IsZero() {
		value = time.Now().UTC()
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseStoreTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse workspace store time: %w", err)
	}
	return parsed, nil
}
