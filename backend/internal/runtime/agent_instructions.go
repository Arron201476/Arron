package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"content-agent/backend/internal/identity"
)

const maxAgentInstructionBytes = 8192

func migrateAgentInstructions(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_instruction_versions (
		workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
		scope TEXT NOT NULL CHECK(scope IN ('user','workspace','project')),
		scope_ref TEXT NOT NULL,
		version INTEGER NOT NULL CHECK(version > 0),
		content TEXT NOT NULL,
		content_hash TEXT NOT NULL,
		enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
		updated_by TEXT NOT NULL REFERENCES users(user_id),
		updated_at TEXT NOT NULL,
		request_id TEXT NOT NULL,
		request_hash TEXT NOT NULL,
		PRIMARY KEY(workspace_id, scope, scope_ref, version),
		UNIQUE(workspace_id, scope, scope_ref, request_id)
	);
	CREATE TABLE IF NOT EXISTS agent_instruction_snapshots (
		activity_key TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		user_id TEXT NOT NULL REFERENCES users(user_id),
		references_json TEXT NOT NULL,
		content_hash TEXT NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS agent_instruction_proposals (
		agent_tool_call_id TEXT PRIMARY KEY REFERENCES agent_tool_calls(agent_tool_call_id) ON DELETE CASCADE,
		workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		user_id TEXT NOT NULL REFERENCES users(user_id),
		arguments_json TEXT NOT NULL,
		arguments_hash TEXT NOT NULL
	);`)
	return err
}

type AgentInstructionDocument struct {
	Scope       string `json:"scope"`
	ScopeRef    string `json:"scope_ref"`
	Version     int    `json:"version"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash"`
	Enabled     bool   `json:"enabled"`
	UpdatedBy   string `json:"updated_by,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	CanEdit     bool   `json:"can_edit"`
}

type AgentInstructionView struct {
	WorkspaceID string                     `json:"workspace_id"`
	ProjectID   string                     `json:"project_id,omitempty"`
	Documents   []AgentInstructionDocument `json:"documents"`
	AppliesTo   string                     `json:"applies_to"`
}

type UpdateAgentInstructionsCommand struct {
	ProjectID       string `json:"project_id,omitempty"`
	Scope           string `json:"scope"`
	ExpectedVersion int    `json:"expected_version"`
	Content         string `json:"content"`
	Enabled         bool   `json:"enabled"`
	RequestID       string `json:"request_id"`
}

func instructionScopeRef(user identity.Principal, scope, projectID string) (string, error) {
	switch scope {
	case "workspace":
		return user.WorkspaceID, nil
	case "user":
		return user.UserID, nil
	case "project":
		if projectID != "" {
			return projectID, nil
		}
	}
	return "", domainError("REQUEST_VALIDATION_FAILED", "指令范围或目标作品无效。")
}

func instructionDocumentQuery(ctx context.Context, query rowQueryer, workspaceID, scope, ref string, version int) (AgentInstructionDocument, error) {
	doc := AgentInstructionDocument{Scope: scope, ScopeRef: ref, ContentHash: sha256Hex(nil)}
	statement := `SELECT version, content, content_hash, enabled, updated_by, updated_at FROM agent_instruction_versions WHERE workspace_id=? AND scope=? AND scope_ref=?`
	arguments := []any{workspaceID, scope, ref}
	if version > 0 {
		statement += ` AND version=?`
		arguments = append(arguments, version)
	}
	err := query.QueryRowContext(ctx, statement+` ORDER BY version DESC LIMIT 1`, arguments...).
		Scan(&doc.Version, &doc.Content, &doc.ContentHash, &doc.Enabled, &doc.UpdatedBy, &doc.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) && version == 0 {
		return doc, nil
	}
	if err != nil {
		return doc, err
	}
	if len(doc.Content) > maxAgentInstructionBytes || !utf8.ValidString(doc.Content) || doc.ContentHash != sha256Hex([]byte(doc.Content)) {
		return AgentInstructionDocument{}, domainError("AGENT_INSTRUCTIONS_INVALID", "已保存指令未通过完整性校验。")
	}
	return doc, nil
}

func instructionUserQuery(ctx context.Context, query rowQueryer, projectID string, write bool) (identity.Principal, error) {
	user, ok := identity.UserFromContext(ctx)
	if !ok {
		return identity.Principal{}, domainError("AUTHENTICATION_REQUIRED", "需要用户身份。")
	}
	user, err := resolvePrincipalQuery(ctx, query, user)
	if err != nil {
		return identity.Principal{}, err
	}
	if write && !user.Allows(identity.RoleEditor) {
		return identity.Principal{}, domainError("WORKSPACE_ACCESS_DENIED", "当前用户没有指令编辑权限。")
	}
	if len(projectID) > 256 {
		return identity.Principal{}, domainError("REQUEST_VALIDATION_FAILED", "作品标识无效。")
	}
	if projectID != "" {
		workspace, err := projectFilesWorkspace(ctx, query, projectID, false)
		if err != nil {
			return identity.Principal{}, err
		}
		if workspace != user.WorkspaceID {
			return identity.Principal{}, domainError("PROJECT_NOT_FOUND", "作品不存在。")
		}
	}
	return user, nil
}

func agentInstructionsQuery(ctx context.Context, query rowQueryer, user identity.Principal, projectID string) (AgentInstructionView, error) {
	view := AgentInstructionView{WorkspaceID: user.WorkspaceID, ProjectID: projectID, Documents: []AgentInstructionDocument{}, AppliesTo: "new_executions"}
	scopes := []string{"workspace", "user"}
	if projectID != "" {
		scopes = append(scopes, "project")
	}
	for _, scope := range scopes {
		ref, err := instructionScopeRef(user, scope, projectID)
		if err != nil {
			return AgentInstructionView{}, err
		}
		doc, err := instructionDocumentQuery(ctx, query, user.WorkspaceID, scope, ref, 0)
		if err != nil {
			return AgentInstructionView{}, err
		}
		doc.CanEdit = user.Allows(identity.RoleEditor) && (scope != "workspace" || user.Allows(identity.RoleAdmin))
		view.Documents = append(view.Documents, doc)
	}
	return view, nil
}

func (s *Store) GetAgentInstructions(ctx context.Context, projectID string) (AgentInstructionView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentInstructionView{}, err
	}
	defer tx.Rollback()
	user, err := instructionUserQuery(ctx, tx, projectID, false)
	if err != nil {
		return AgentInstructionView{}, err
	}
	return agentInstructionsQuery(ctx, tx, user, projectID)
}

func validateInstructionUpdate(command UpdateAgentInstructionsCommand) error {
	if command.ExpectedVersion < 0 || strings.TrimSpace(command.RequestID) == "" || len(command.RequestID) > 128 || !utf8.ValidString(command.RequestID) || strings.IndexFunc(command.RequestID, unicode.IsControl) >= 0 ||
		len(command.Content) > maxAgentInstructionBytes || !utf8.ValidString(command.Content) || strings.ContainsRune(command.Content, '\x00') ||
		(command.Enabled && strings.TrimSpace(command.Content) == "") {
		return domainError("REQUEST_VALIDATION_FAILED", "指令内容、版本或请求标识无效。单个范围最多 8192 字节。")
	}
	return nil
}

func (s *Store) UpdateAgentInstructions(ctx context.Context, command UpdateAgentInstructionsCommand) (AgentInstructionDocument, error) {
	transport, ok := identity.FromContext(ctx)
	if !ok || !transport.ValidUser() {
		return AgentInstructionDocument{}, domainError("AUTHENTICATION_REQUIRED", "需要用户身份。")
	}
	if err := validateInstructionUpdate(command); err != nil {
		return AgentInstructionDocument{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentInstructionDocument{}, err
	}
	defer tx.Rollback()
	user, err := instructionUserQuery(ctx, tx, command.ProjectID, true)
	if err != nil {
		return AgentInstructionDocument{}, err
	}
	doc, err := s.updateAgentInstructionsTx(ctx, tx, user, command)
	if err != nil {
		return AgentInstructionDocument{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentInstructionDocument{}, err
	}
	return doc, nil
}

func (s *Store) updateAgentInstructionsTx(ctx context.Context, tx *sql.Tx, user identity.Principal, command UpdateAgentInstructionsCommand) (AgentInstructionDocument, error) {
	ref, err := instructionScopeRef(user, command.Scope, command.ProjectID)
	if err != nil {
		return AgentInstructionDocument{}, err
	}
	if !user.Allows(identity.RoleEditor) || (command.Scope == "workspace" && !user.Allows(identity.RoleAdmin)) {
		return AgentInstructionDocument{}, domainError("WORKSPACE_ACCESS_DENIED", "当前用户不能编辑此范围的指令。")
	}
	encoded, err := json.Marshal(command)
	if err != nil {
		return AgentInstructionDocument{}, err
	}
	hash := sha256Hex(encoded)
	var existingVersion int
	var existingHash string
	err = tx.QueryRowContext(ctx, `SELECT version, request_hash FROM agent_instruction_versions WHERE workspace_id=? AND scope=? AND scope_ref=? AND request_id=?`,
		user.WorkspaceID, command.Scope, ref, command.RequestID).Scan(&existingVersion, &existingHash)
	if err == nil {
		if existingHash != hash {
			return AgentInstructionDocument{}, domainError("IDEMPOTENCY_CONFLICT", "请求标识已用于不同的指令更新。")
		}
		doc, err := instructionDocumentQuery(ctx, tx, user.WorkspaceID, command.Scope, ref, existingVersion)
		doc.CanEdit = true
		return doc, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AgentInstructionDocument{}, err
	}
	current, err := instructionDocumentQuery(ctx, tx, user.WorkspaceID, command.Scope, ref, 0)
	if err != nil {
		return AgentInstructionDocument{}, err
	}
	if current.Version != command.ExpectedVersion {
		return AgentInstructionDocument{}, domainError("AGENT_INSTRUCTIONS_CONFLICT", "指令已被修改，请刷新当前版本后重试。")
	}
	if err := s.enforceWorkspaceQuotaTx(ctx, tx, user.WorkspaceID, "storage_bytes", int64(len(command.Content))); err != nil {
		return AgentInstructionDocument{}, err
	}
	doc := AgentInstructionDocument{Scope: command.Scope, ScopeRef: ref, Version: current.Version + 1, Content: command.Content,
		ContentHash: sha256Hex([]byte(command.Content)), Enabled: command.Enabled, UpdatedBy: user.UserID, UpdatedAt: formatTime(s.now()), CanEdit: true}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_instruction_versions(workspace_id, scope, scope_ref, version, content, content_hash, enabled, updated_by, updated_at, request_id, request_hash)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, user.WorkspaceID, doc.Scope, doc.ScopeRef, doc.Version, doc.Content, doc.ContentHash, doc.Enabled, doc.UpdatedBy, doc.UpdatedAt, command.RequestID, hash)
	return doc, err
}

type AgentInstructionSnapshot struct {
	ActivityKey string                     `json:"activity_key"`
	WorkspaceID string                     `json:"workspace_id"`
	ProjectID   string                     `json:"project_id"`
	UserID      string                     `json:"user_id"`
	Documents   []AgentInstructionDocument `json:"documents"`
	ContentHash string                     `json:"content_hash"`
}

type agentInstructionReference struct {
	Scope    string `json:"scope"`
	ScopeRef string `json:"scope_ref"`
	Version  int    `json:"version"`
}

func instructionActivityKey(activity AgentActivityIdentity) string {
	if activity.MemoryGenerationID != "" {
		return "memory:" + activity.MemoryGenerationID
	}
	if activity.AgentTurnID != "" {
		return "turn:" + activity.AgentTurnID
	}
	if activity.AgentTaskAttemptID != "" {
		return "background:" + activity.AgentTaskAttemptID
	}
	return "execution:" + activity.ExecutionAttemptID
}

func instructionLegacyCheckpoint(ctx context.Context, tx *sql.Tx, activity AgentActivityIdentity) (bool, error) {
	var found bool
	var err error
	switch {
	case activity.MemoryGenerationID != "":
		err = tx.QueryRowContext(ctx, `SELECT checkpoint_json<>'' FROM agent_memory_generations WHERE generation_id=?`, activity.MemoryGenerationID).Scan(&found)
		if err == nil && found {
			return false, domainError("AGENT_INSTRUCTIONS_INVALID", "Memory generation checkpoint is missing its original instruction snapshot.")
		}
	case activity.AgentTurnID != "":
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_turn_run_states WHERE agent_turn_id=?)`, activity.AgentTurnID).Scan(&found)
	case activity.AgentTaskAttemptID != "":
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_task_run_states WHERE agent_task_attempt_id=?)`, activity.AgentTaskAttemptID).Scan(&found)
	default:
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_run_states WHERE attempt_id=?) OR EXISTS(SELECT 1 FROM execution_result_rejections WHERE attempt_id=?)`, activity.ExecutionAttemptID, activity.ExecutionAttemptID).Scan(&found)
	}
	return found, err
}

// Freeze exact immutable instruction versions before the first SDK model call.
// A resumed execution reuses them; edits and revocations affect new executions.
func (s *Store) ResolveAgentInstructionSnapshot(ctx context.Context) (AgentInstructionSnapshot, error) {
	transport, ok := identity.FromContext(ctx)
	activity, active := AgentActivityFromContext(ctx)
	if !ok || transport.Kind != identity.KindService || !active || activity.AllowTerminal {
		return AgentInstructionSnapshot{}, domainError("AGENT_ACTIVITY_FORBIDDEN", "指令快照需要有效的内部 Agent 执行身份。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentInstructionSnapshot{}, err
	}
	defer tx.Rollback()
	user, err := s.resolveAgentActivityPrincipalTx(ctx, tx, activity)
	if err != nil {
		return AgentInstructionSnapshot{}, err
	}
	if !user.Allows(identity.RoleEditor) {
		return AgentInstructionSnapshot{}, domainError("WORKSPACE_ACCESS_DENIED", "当前执行用户没有 Agent 执行权限。")
	}
	key := instructionActivityKey(activity)
	var storedWorkspace, storedProject, storedUser, raw, digest string
	err = tx.QueryRowContext(ctx, `SELECT workspace_id, project_id, user_id, references_json, content_hash FROM agent_instruction_snapshots WHERE activity_key=?`, key).
		Scan(&storedWorkspace, &storedProject, &storedUser, &raw, &digest)
	refs := []agentInstructionReference{}
	if err == nil {
		if storedWorkspace != user.WorkspaceID || storedProject != activity.ProjectID || storedUser != user.UserID || len(digest) != 64 || len(raw) > 4096 || json.Unmarshal([]byte(raw), &refs) != nil || refs == nil || len(refs) > 3 {
			return AgentInstructionSnapshot{}, domainError("AGENT_INSTRUCTIONS_INVALID", "执行指令快照身份或内容无效。")
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		legacy, err := instructionLegacyCheckpoint(ctx, tx, activity)
		if err != nil {
			return AgentInstructionSnapshot{}, err
		}
		// Old SDK checkpoints did not include managed instructions. Do not apply
		// newly saved rules halfway through those executions during an upgrade.
		if !legacy {
			view, err := agentInstructionsQuery(ctx, tx, user, activity.ProjectID)
			if err != nil {
				return AgentInstructionSnapshot{}, err
			}
			for _, doc := range view.Documents {
				if doc.Enabled {
					refs = append(refs, agentInstructionReference{Scope: doc.Scope, ScopeRef: doc.ScopeRef, Version: doc.Version})
				}
			}
		}
		encoded, err := json.Marshal(refs)
		if err != nil {
			return AgentInstructionSnapshot{}, err
		}
		raw = string(encoded)
	} else {
		return AgentInstructionSnapshot{}, err
	}
	result := AgentInstructionSnapshot{ActivityKey: key, WorkspaceID: user.WorkspaceID, ProjectID: activity.ProjectID, UserID: user.UserID, Documents: []AgentInstructionDocument{}}
	seen := map[string]bool{}
	for _, ref := range refs {
		expected, err := instructionScopeRef(user, ref.Scope, activity.ProjectID)
		if err != nil || expected != ref.ScopeRef || ref.Version < 1 || seen[ref.Scope] {
			return AgentInstructionSnapshot{}, domainError("AGENT_INSTRUCTIONS_INVALID", "执行指令快照范围无效。")
		}
		seen[ref.Scope] = true
		doc, err := instructionDocumentQuery(ctx, tx, user.WorkspaceID, ref.Scope, ref.ScopeRef, ref.Version)
		if err != nil {
			return AgentInstructionSnapshot{}, fmt.Errorf("read pinned Agent instruction: %w", err)
		}
		if !doc.Enabled {
			return AgentInstructionSnapshot{}, domainError("AGENT_INSTRUCTIONS_INVALID", "执行指令快照包含未启用版本。")
		}
		result.Documents = append(result.Documents, doc)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return AgentInstructionSnapshot{}, err
	}
	result.ContentHash = sha256Hex(encoded)
	if digest != "" && digest != result.ContentHash {
		return AgentInstructionSnapshot{}, domainError("AGENT_INSTRUCTIONS_INVALID", "执行指令快照校验失败。")
	}
	if digest == "" {
		if err := s.enforceWorkspaceQuotaTx(ctx, tx, user.WorkspaceID, "storage_bytes", int64(len(raw))); err != nil {
			return AgentInstructionSnapshot{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_instruction_snapshots(activity_key, workspace_id, project_id, user_id, references_json, content_hash, created_at) VALUES(?,?,?,?,?,?,?)`,
			key, user.WorkspaceID, activity.ProjectID, user.UserID, raw, result.ContentHash, formatTime(s.now())); err != nil {
			return AgentInstructionSnapshot{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AgentInstructionSnapshot{}, err
	}
	return result, nil
}
