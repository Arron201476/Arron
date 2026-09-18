package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"content-agent/backend/internal/identity"
)

const (
	maxProjectFileBytes        = 1 << 20
	maxProjectBinaryFileBytes  = 16 << 20
	maxProjectFileHistoryBytes = 64 << 20
	maxProjectFileVersions     = 4096
	maxProjectFilePaths        = 256
)

const projectFileSchema = `
CREATE TABLE IF NOT EXISTS project_file_versions (
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	path_key TEXT NOT NULL,
	path TEXT NOT NULL,
	version INTEGER NOT NULL CHECK(version > 0),
	content BLOB NOT NULL,
	is_binary INTEGER NOT NULL DEFAULT 0 CHECK(is_binary IN (0,1)),
	content_hash TEXT NOT NULL,
	size_bytes INTEGER NOT NULL CHECK(size_bytes >= 0),
	deleted INTEGER NOT NULL CHECK(deleted IN (0,1)),
	agent_tool_call_id TEXT NOT NULL REFERENCES agent_tool_calls(agent_tool_call_id),
	created_at TEXT NOT NULL,
	PRIMARY KEY(project_id, path_key, version),
	UNIQUE(agent_tool_call_id, path_key)
);
`

type ProjectFile struct {
	ProjectID       string    `json:"project_id"`
	Path            string    `json:"path"`
	Version         int       `json:"version"`
	ContentHash     string    `json:"content_hash"`
	SizeBytes       int       `json:"size_bytes"`
	Deleted         bool      `json:"deleted"`
	Binary          bool      `json:"binary,omitempty"`
	AgentToolCallID string    `json:"agent_tool_call_id"`
	CreatedAt       time.Time `json:"created_at"`
}

type ProjectFileContent struct {
	File       ProjectFile `json:"file"`
	Content    string      `json:"content"`
	Offset     int         `json:"offset"`
	NextOffset int         `json:"next_offset"`
	Truncated  bool        `json:"truncated"`
}

type ProjectFilePatch struct {
	Path            string `json:"path"`
	ExpectedVersion int    `json:"expected_version"`
	Operation       string `json:"operation"`
	Diff            string `json:"diff"`
}

// Project files are versioned data, never paths to open on the runtime host.
func validateProjectFilePath(value string) error {
	if value == "" || len(value) > 240 || !utf8.ValidString(value) || strings.TrimSpace(value) != value ||
		path.IsAbs(value) || path.Clean(value) != value || strings.ContainsAny(value, "\\:\x00<>\"|?*") {
		return domainError("PROJECT_FILE_PATH_INVALID", "文件路径必须是项目内的规范相对路径。")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.TrimRight(segment, " .") != segment {
			return domainError("PROJECT_FILE_PATH_INVALID", "文件路径包含不可用的目录或文件名。")
		}
		for _, character := range segment {
			if unicode.IsControl(character) {
				return domainError("PROJECT_FILE_PATH_INVALID", "文件路径不能包含控制字符。")
			}
		}
		base := strings.ToUpper(strings.SplitN(segment, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
			(len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return domainError("PROJECT_FILE_PATH_INVALID", "文件路径不能使用设备名称。")
		}
	}
	return nil
}

func projectFilesWorkspace(ctx context.Context, queryer rowQueryer, projectID string, write bool) (string, error) {
	if activity, ok := AgentActivityFromContext(ctx); ok && activity.ProjectID != projectID {
		return "", domainError("PROJECT_NOT_FOUND", "作品不存在。")
	}
	var workspaceID string
	err := queryer.QueryRowContext(ctx, `SELECT workspace_id FROM projects WHERE project_id = ? AND deleted_at IS NULL`, projectID).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domainError("PROJECT_NOT_FOUND", "作品不存在。")
	}
	if err != nil {
		return "", err
	}
	if user, ok := identity.UserFromContext(ctx); ok {
		if workspaceID != user.WorkspaceID {
			return "", domainError("PROJECT_NOT_FOUND", "作品不存在。")
		}
		if write && !user.Allows(identity.RoleEditor) {
			return "", domainError("ROLE_FORBIDDEN", "当前用户没有文件编辑权限。")
		}
		resolved, err := resolvePrincipalQuery(ctx, queryer, user)
		if err != nil {
			return "", err
		}
		if write && !resolved.Allows(identity.RoleEditor) {
			return "", domainError("ROLE_FORBIDDEN", "当前用户没有文件编辑权限。")
		}
	}
	return workspaceID, nil
}

const projectFileSelect = `SELECT project_id, path, version, content_hash, size_bytes, deleted, agent_tool_call_id, created_at, is_binary FROM project_file_versions`

func scanProjectFile(row rowScanner) (ProjectFile, error) {
	var file ProjectFile
	var created string
	err := row.Scan(&file.ProjectID, &file.Path, &file.Version, &file.ContentHash, &file.SizeBytes, &file.Deleted, &file.AgentToolCallID, &created, &file.Binary)
	if err != nil {
		return file, err
	}
	file.CreatedAt, err = parseTime(created)
	return file, err
}

func (s *Store) ListProjectFiles(ctx context.Context, projectID string) ([]ProjectFile, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := projectFilesWorkspace(ctx, tx, projectID, false); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, projectFileSelect+` AS f WHERE project_id = ? AND version = (
		SELECT MAX(version) FROM project_file_versions latest WHERE latest.project_id = f.project_id AND latest.path_key = f.path_key
	) ORDER BY path_key`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := []ProjectFile{}
	for rows.Next() {
		file, err := scanProjectFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (s *Store) ReadProjectFile(ctx context.Context, projectID, filePath string, version, offset, limit int) (ProjectFileContent, error) {
	if err := validateProjectFilePath(filePath); err != nil {
		return ProjectFileContent{}, err
	}
	if version < 0 || offset < 0 || limit < 1 || limit > maxProjectFileBytes {
		return ProjectFileContent{}, domainError("REQUEST_VALIDATION_FAILED", "文件版本或读取范围无效。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectFileContent{}, err
	}
	defer tx.Rollback()
	if _, err := projectFilesWorkspace(ctx, tx, projectID, false); err != nil {
		return ProjectFileContent{}, err
	}
	file, content, err := readProjectFileBytesTx(ctx, tx, projectID, filePath, version)
	if err != nil {
		return ProjectFileContent{File: file}, err
	}
	if file.Binary {
		return ProjectFileContent{File: file}, nil
	}
	characters := []rune(string(content))
	if offset > len(characters) {
		return ProjectFileContent{}, domainError("REQUEST_VALIDATION_FAILED", "读取位置超过文件长度。")
	}
	end := offset + min(limit, len(characters)-offset)
	return ProjectFileContent{File: file, Content: string(characters[offset:end]), Offset: offset, NextOffset: end, Truncated: end < len(characters)}, nil
}

func (s *Store) ReadProjectFileBytes(ctx context.Context, projectID, filePath string, version int) (ProjectFile, []byte, error) {
	if err := validateProjectFilePath(filePath); err != nil {
		return ProjectFile{}, nil, err
	}
	if version < 0 {
		return ProjectFile{}, nil, domainError("REQUEST_VALIDATION_FAILED", "文件版本无效。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectFile{}, nil, err
	}
	defer tx.Rollback()
	if _, err := projectFilesWorkspace(ctx, tx, projectID, false); err != nil {
		return ProjectFile{}, nil, err
	}
	return readProjectFileBytesTx(ctx, tx, projectID, filePath, version)
}

func readProjectFileBytesTx(ctx context.Context, tx *sql.Tx, projectID, filePath string, version int) (ProjectFile, []byte, error) {
	query := projectFileSelect + ` WHERE project_id = ? AND path_key = ?`
	args := []any{projectID, strings.ToLower(filePath)}
	if version > 0 {
		query += ` AND version = ?`
		args = append(args, version)
	}
	file, err := scanProjectFile(tx.QueryRowContext(ctx, query+` ORDER BY version DESC LIMIT 1`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectFile{}, nil, domainError("PROJECT_FILE_NOT_FOUND", "项目文件不存在。")
	}
	if err != nil {
		return ProjectFile{}, nil, err
	}
	if file.Deleted {
		return file, nil, domainError("PROJECT_FILE_DELETED", "该版本的项目文件已删除。")
	}
	if file.SizeBytes < 0 || file.SizeBytes > maxProjectBinaryFileBytes {
		return file, nil, domainError("PROJECT_FILE_INVALID", "文件大小超出存储限制。")
	}
	var content []byte
	if err := tx.QueryRowContext(ctx, `SELECT content FROM project_file_versions WHERE project_id = ? AND path_key = ? AND version = ?`, projectID, strings.ToLower(filePath), file.Version).Scan(&content); err != nil {
		return file, nil, err
	}
	if len(content) != file.SizeBytes || sha256Hex(content) != file.ContentHash || (!file.Binary && (!utf8.Valid(content) || strings.ContainsRune(string(content), '\x00'))) {
		return file, nil, domainError("PROJECT_FILE_INVALID", "文件正文与版本校验信息不一致。")
	}
	return file, content, nil
}

// The authenticated SDK host computes content with the SDK's patch algorithm.
// The runtime binds that result to the audited request, active call and CAS base.
func (s *Store) ApplyProjectFilePatch(ctx context.Context, callID, sdkCallID string, patch ProjectFilePatch, content string) (ProjectFile, error) {
	if err := validateProjectFilePath(patch.Path); err != nil {
		return ProjectFile{}, err
	}
	if patch.ExpectedVersion < 0 || !utf8.ValidString(content) || strings.ContainsRune(content, '\x00') || len(content) > maxProjectFileBytes || len(patch.Diff) > maxAgentToolArgumentsBytes {
		return ProjectFile{}, domainError("REQUEST_VALIDATION_FAILED", "文件内容、补丁大小或基础版本无效。")
	}
	if patch.Operation != "create_file" && patch.Operation != "update_file" && patch.Operation != "delete_file" {
		return ProjectFile{}, domainError("REQUEST_VALIDATION_FAILED", "不支持的文件补丁操作。")
	}
	deleted := patch.Operation == "delete_file"
	if deleted && (content != "" || patch.Diff != "") {
		return ProjectFile{}, domainError("REQUEST_VALIDATION_FAILED", "删除操作不能包含文件内容或补丁。")
	}
	arguments, err := json.Marshal(patch)
	if err != nil {
		return ProjectFile{}, err
	}
	_, argumentHash, _, err := summarizeAgentToolPayload(arguments, true)
	if err != nil {
		return ProjectFile{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectFile{}, err
	}
	defer tx.Rollback()
	call, err := getAgentToolCallTx(ctx, tx, callID)
	if err != nil {
		return ProjectFile{}, err
	}
	if call.ToolID != "runtime:apply_workspace_patch" || call.SDKToolCallID != sdkCallID || call.Status != "running" || call.ArgumentsHash != argumentHash {
		return ProjectFile{}, domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "文件操作与正在执行的已登记工具调用不匹配。")
	}
	workspaceID, err := projectFilesWorkspace(ctx, tx, call.ProjectID, true)
	if err != nil {
		return ProjectFile{}, err
	}
	if err := s.validateAgentTaskToolCallTx(ctx, tx, call.AgentToolCallID); err != nil {
		return ProjectFile{}, err
	}
	if call.AgentTurnID != nil {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM agent_turns WHERE agent_turn_id = ?`, *call.AgentTurnID).Scan(&status); err != nil {
			return ProjectFile{}, err
		}
		if status != "running" && status != "pausing" {
			return ProjectFile{}, domainError("AGENT_ACTIVITY_STALE", "Agent 对话已停止，不能继续写入文件。")
		}
	}
	key, digest := strings.ToLower(patch.Path), sha256Hex([]byte(content))
	existing, err := scanProjectFile(tx.QueryRowContext(ctx, projectFileSelect+` WHERE agent_tool_call_id = ?`, callID))
	if err == nil {
		if existing.Path != patch.Path || existing.ContentHash != digest || existing.Deleted != deleted {
			return ProjectFile{}, domainError("AGENT_TOOL_CALL_ID_CONFLICT", "同一工具调用不能提交不同文件内容。")
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ProjectFile{}, err
	}
	current, err := scanProjectFile(tx.QueryRowContext(ctx, projectFileSelect+` WHERE project_id = ? AND path_key = ? ORDER BY version DESC LIMIT 1`, call.ProjectID, key))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ProjectFile{}, err
	}
	if current.Version > 0 && current.Path != patch.Path {
		return ProjectFile{}, domainError("PROJECT_FILE_PATH_CONFLICT", "该路径与已有文件仅大小写不同。")
	}
	if current.Version != patch.ExpectedVersion || (patch.Operation == "create_file" && current.Version > 0 && !current.Deleted) || (patch.Operation != "create_file" && (current.Version == 0 || current.Deleted)) {
		return ProjectFile{}, domainError("PROJECT_FILE_VERSION_CONFLICT", "文件已经变化或操作不适用，请重新读取后修改。")
	}
	if patch.Operation == "update_file" && current.Binary {
		return ProjectFile{}, domainError("PROJECT_FILE_BINARY", "二进制文件不能使用文本补丁更新。")
	}
	if !deleted {
		var pathCollision bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM project_file_versions f WHERE project_id = ? AND deleted = 0
			AND version = (SELECT MAX(version) FROM project_file_versions latest WHERE latest.project_id = f.project_id AND latest.path_key = f.path_key)
			AND (substr(path_key,1,length(?)+1) = ? || '/' OR substr(?,1,length(path_key)+1) = path_key || '/')
		)`, call.ProjectID, key, key, key).Scan(&pathCollision); err != nil {
			return ProjectFile{}, err
		}
		if pathCollision {
			return ProjectFile{}, domainError("PROJECT_FILE_PATH_CONFLICT", "文件路径与已有文件的目录结构冲突。")
		}
	}
	var versionCount, pathCount, storedBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT path_key), COALESCE(SUM(size_bytes),0) FROM project_file_versions WHERE project_id = ?`, call.ProjectID).Scan(&versionCount, &pathCount, &storedBytes); err != nil {
		return ProjectFile{}, err
	}
	if versionCount >= maxProjectFileVersions || storedBytes+int64(len(content)) > maxProjectFileHistoryBytes || (current.Version == 0 && pathCount >= maxProjectFilePaths) {
		return ProjectFile{}, domainError("PROJECT_FILE_QUOTA_EXCEEDED", "项目文件或历史版本已达到容量限制。")
	}
	if err := s.enforceWorkspaceQuotaTx(ctx, tx, workspaceID, "storage_bytes", int64(len(content))); err != nil {
		return ProjectFile{}, err
	}
	file := ProjectFile{ProjectID: call.ProjectID, Path: patch.Path, Version: current.Version + 1, ContentHash: digest, SizeBytes: len(content), Deleted: deleted, AgentToolCallID: callID, CreatedAt: s.now()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_file_versions(project_id,path_key,path,version,content,content_hash,size_bytes,deleted,agent_tool_call_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, file.ProjectID, key, file.Path, file.Version, content, file.ContentHash, file.SizeBytes, file.Deleted, callID, formatTime(file.CreatedAt)); err != nil {
		return ProjectFile{}, err
	}
	if _, err := s.appendEvent(ctx, tx, file.ProjectID, nil, nil, "project.file.updated", "project", file.ProjectID, file); err != nil {
		return ProjectFile{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectFile{}, err
	}
	return file, nil
}
