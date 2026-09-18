package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

type NativeWorkspaceSnapshotReference struct {
	Version int    `json:"version"`
	SHA256  string `json:"sha256"`
}

type NativeWorkspacePublicationFile struct {
	SourcePath      string `json:"source_path"`
	Path            string `json:"path"`
	ExpectedVersion int    `json:"expected_version"`
}

type NativeWorkspacePublicationArguments struct {
	Snapshot NativeWorkspaceSnapshotReference `json:"snapshot"`
	Files    []NativeWorkspacePublicationFile `json:"files"`
}

type NativeWorkspacePublicationRequest struct {
	AgentToolCallID string                              `json:"agent_tool_call_id"`
	SDKToolCallID   string                              `json:"sdk_tool_call_id"`
	Arguments       NativeWorkspacePublicationArguments `json:"arguments"`
}

type NativeWorkspacePublicationReceipt struct {
	SessionID       string                           `json:"session_id"`
	AgentToolCallID string                           `json:"agent_tool_call_id"`
	Snapshot        NativeWorkspaceSnapshotReference `json:"snapshot"`
	Files           []ProjectFile                    `json:"files"`
}

func validateNativePublicationArguments(arguments NativeWorkspacePublicationArguments) error {
	if arguments.Snapshot.Version < 1 || !nativeLeaseKeyPattern.MatchString(arguments.Snapshot.SHA256) || len(arguments.Files) < 1 || len(arguments.Files) > maxProjectFilePaths {
		return domainError("NATIVE_WORKSPACE_PUBLICATION_INVALID", "Publication requires an exact saved snapshot and bounded files.")
	}
	targets := map[string]bool{}
	for _, file := range arguments.Files {
		key := strings.ToLower(file.Path)
		if strings.EqualFold(strings.Split(file.SourcePath, "/")[0], nativeMemoryDirectory) || strings.Split(key, "/")[0] == nativeMemoryDirectory || strings.EqualFold(strings.Split(file.SourcePath, "/")[0], nativeGenerationDirectory) || strings.Split(key, "/")[0] == nativeGenerationDirectory {
			return domainError("NATIVE_WORKSPACE_PUBLICATION_INVALID", "Private memory cannot be published as shared project files.")
		}
		if validateProjectFilePath(file.Path) != nil || validateProjectFilePath(file.SourcePath) != nil || file.ExpectedVersion < 0 || targets[key] || strings.Split(key, "/")[0] == ".skills" {
			return domainError("NATIVE_WORKSPACE_PUBLICATION_INVALID", "Publication paths or expected versions are invalid; installed Skill paths are not project destinations.")
		}
		for previous := range targets {
			if strings.HasPrefix(key, previous+"/") || strings.HasPrefix(previous, key+"/") {
				return domainError("PROJECT_FILE_PATH_CONFLICT", "Published files conflict with each other's directories.")
			}
		}
		targets[key] = true
	}
	return nil
}

func (s *Store) validateNativePublicationCallTx(ctx context.Context, tx *sql.Tx, request NativeWorkspacePublicationRequest, hash string) (AgentToolCall, error) {
	activity, ok := AgentActivityFromContext(ctx)
	if !ok {
		return AgentToolCall{}, domainError("AGENT_ACTIVITY_FORBIDDEN", "Publication requires its active execution identity.")
	}
	if err := validateAgentActivityToolCallQuery(ctx, tx, activity, request.AgentToolCallID); err != nil {
		return AgentToolCall{}, err
	}
	call, err := getAgentToolCallTx(ctx, tx, request.AgentToolCallID)
	if err != nil {
		return call, err
	}
	if call.ToolID != "runtime:publish_workspace_files" || call.ToolKind != "runtime_function" || call.SDKToolCallID != request.SDKToolCallID || call.ArgumentsHash != hash || call.ParentToolCallID != "" || call.AccessMode != "write" || call.ApprovalPolicy != "always" || call.ApprovalStatus != "approved" || call.ApprovalConsumedAt == nil || (call.Status != "running" && call.Status != "completed") {
		return call, domainError("AGENT_TOOL_APPROVAL_REQUIRED", "Publication must match its exact approved tool arguments.")
	}
	approval, err := getAgentToolApprovalForCallTx(ctx, tx, call.AgentToolCallID)
	if err != nil {
		return call, err
	}
	if approval.Status != "approved" {
		return call, domainError("AGENT_TOOL_APPROVAL_REQUIRED", "Publication approval is no longer valid.")
	}
	if err := s.validateAgentToolConfigurationTx(ctx, tx, call, ""); err != nil {
		return call, err
	}
	return call, nil
}

// PublishFiles copies an immutable, approved snapshot selection atomically into
// the existing project file history. It never reads or changes a live engine.
func (manager *NativeWorkspaceManager) PublishFiles(ctx context.Context, access NativeWorkspaceAccess, request NativeWorkspacePublicationRequest) (NativeWorkspacePublicationReceipt, error) {
	result := NativeWorkspacePublicationReceipt{SessionID: access.SessionID, AgentToolCallID: request.AgentToolCallID, Snapshot: request.Arguments.Snapshot, Files: []ProjectFile{}}
	if !manager.requirePolicy || request.AgentToolCallID == "" || len(request.AgentToolCallID) > 128 || request.SDKToolCallID == "" || len(request.SDKToolCallID) > 512 {
		return result, domainError("NATIVE_WORKSPACE_PUBLICATION_INVALID", "Publication requires current policy and bounded call identity.")
	}
	if err := validateNativePublicationArguments(request.Arguments); err != nil {
		return result, err
	}
	encoded, err := json.Marshal(request.Arguments)
	if err != nil {
		return result, err
	}
	_, argumentHash, _, err := summarizeAgentToolPayload(encoded, true)
	if err != nil {
		return result, err
	}
	tx, err := manager.store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	lease, err := manager.store.nativeWorkspaceAccessTx(ctx, tx, access)
	if err != nil {
		return result, err
	}
	if err := manager.validatePolicyTx(ctx, tx, lease.workspaceID); err != nil {
		return result, err
	}
	if err := nativeManifestPendingTx(ctx, tx, access.SessionID); err != nil {
		return result, err
	}
	call, err := manager.store.validateNativePublicationCallTx(ctx, tx, request, argumentHash)
	if err != nil {
		return result, err
	}
	if call.ProjectID != lease.projectID || call.WorkspaceID != lease.workspaceID {
		return result, domainError("NATIVE_WORKSPACE_SCOPE_MISMATCH", "Publication belongs to a different project or workspace.")
	}
	selected, err := readNativeWorkspaceSnapshotTx(ctx, tx, access.SessionID, request.Arguments.Snapshot.Version, request.Arguments.Snapshot.SHA256)
	if err != nil {
		return result, err
	}
	wanted := map[string]bool{}
	for _, file := range request.Arguments.Files {
		wanted[file.SourcePath] = true
	}
	contents := map[string][]byte{}
	reader := tar.NewReader(bytes.NewReader(selected.Snapshot.Archive))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, err
		}
		if header.Typeflag == tar.TypeReg && wanted[header.Name] {
			body, err := io.ReadAll(io.LimitReader(reader, maxProjectBinaryFileBytes+1))
			if err != nil || len(body) > maxProjectBinaryFileBytes || int64(len(body)) != header.Size {
				return result, domainError("NATIVE_WORKSPACE_SNAPSHOT_INVALID", "Selected publication bytes are invalid.")
			}
			contents[header.Name] = body
		}
	}
	if len(contents) != len(wanted) {
		return result, domainError("PROJECT_FILE_NOT_FOUND", "The selected snapshot does not contain every publication file.")
	}
	rows, err := tx.QueryContext(ctx, projectFileSelect+` WHERE agent_tool_call_id=? ORDER BY path_key`, request.AgentToolCallID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		file, err := scanProjectFile(rows)
		if err != nil {
			rows.Close()
			return result, err
		}
		result.Files = append(result.Files, file)
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(result.Files) > 0 {
		if len(result.Files) != len(request.Arguments.Files) {
			return result, domainError("NATIVE_WORKSPACE_PUBLICATION_INVALID", "Publication receipt has incomplete files.")
		}
		ordered := make([]ProjectFile, 0, len(result.Files))
		for _, expected := range request.Arguments.Files {
			matched := false
			for _, file := range result.Files {
				if file.ProjectID == lease.projectID && file.Path == expected.Path && file.Version == expected.ExpectedVersion+1 && !file.Deleted && file.ContentHash == sha256Hex(contents[expected.SourcePath]) {
					_, body, err := readProjectFileBytesTx(ctx, tx, file.ProjectID, file.Path, file.Version)
					if err != nil {
						return result, err
					}
					if !bytes.Equal(body, contents[expected.SourcePath]) {
						return result, domainError("NATIVE_WORKSPACE_PUBLICATION_INVALID", "Published bytes differ from the confirmed snapshot.")
					}
					ordered = append(ordered, file)
					matched = true
					break
				}
			}
			if !matched {
				return result, domainError("NATIVE_WORKSPACE_PUBLICATION_INVALID", "Publication receipt differs from its original selection.")
			}
		}
		result.Files = ordered
		return result, nil
	}
	if call.Status != "running" {
		return result, domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "A completed tool cannot create a new publication.")
	}
	if _, err := manager.store.nativeWorkspaceOwnerTx(ctx, tx, access.DispatchGeneration, false); err != nil {
		return result, err
	}
	var total, newPaths int64
	for _, file := range request.Arguments.Files {
		current, err := scanProjectFile(tx.QueryRowContext(ctx, projectFileSelect+` WHERE project_id=? AND path_key=? ORDER BY version DESC LIMIT 1`, lease.projectID, strings.ToLower(file.Path)))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return result, err
		}
		if current.Version != file.ExpectedVersion || current.Version > 0 && current.Path != file.Path {
			return result, domainError("PROJECT_FILE_VERSION_CONFLICT", "A publication destination has changed; no files were published.")
		}
		if current.Version == 0 {
			newPaths++
		}
		key := strings.ToLower(file.Path)
		var collision bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM project_file_versions f WHERE project_id=? AND deleted=0
			AND version=(SELECT MAX(version) FROM project_file_versions latest WHERE latest.project_id=f.project_id AND latest.path_key=f.path_key)
			AND (substr(path_key,1,length(?)+1)=?||'/' OR substr(?,1,length(path_key)+1)=path_key||'/'))`, lease.projectID, key, key, key).Scan(&collision); err != nil {
			return result, err
		}
		if collision {
			return result, domainError("PROJECT_FILE_PATH_CONFLICT", "A publication destination conflicts with an existing directory.")
		}
		total += int64(len(contents[file.SourcePath]))
	}
	var versions, paths, stored int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(DISTINCT path_key),COALESCE(SUM(size_bytes),0) FROM project_file_versions WHERE project_id=?`, lease.projectID).Scan(&versions, &paths, &stored); err != nil {
		return result, err
	}
	if versions+int64(len(request.Arguments.Files)) > maxProjectFileVersions || paths+newPaths > maxProjectFilePaths || stored+total > maxProjectFileHistoryBytes {
		return result, domainError("PROJECT_FILE_QUOTA_EXCEEDED", "Publication exceeds project file or history quotas.")
	}
	if err := manager.store.enforceWorkspaceQuotaTx(ctx, tx, lease.workspaceID, "storage_bytes", total); err != nil {
		return result, err
	}
	for _, requested := range request.Arguments.Files {
		body := contents[requested.SourcePath]
		file := ProjectFile{ProjectID: lease.projectID, Path: requested.Path, Version: requested.ExpectedVersion + 1,
			ContentHash: sha256Hex(body), SizeBytes: len(body), Binary: !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0,
			AgentToolCallID: request.AgentToolCallID, CreatedAt: manager.store.now()}
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_file_versions(project_id,path_key,path,version,content,content_hash,size_bytes,deleted,agent_tool_call_id,created_at,is_binary) VALUES(?,?,?,?,?,?,?,0,?,?,?)`,
			file.ProjectID, strings.ToLower(file.Path), file.Path, file.Version, body, file.ContentHash, file.SizeBytes, file.AgentToolCallID, formatTime(file.CreatedAt), file.Binary); err != nil {
			return result, err
		}
		if _, err := manager.store.appendEvent(ctx, tx, lease.projectID, nil, nil, "project.file.updated", "project", lease.projectID, file); err != nil {
			return result, err
		}
		result.Files = append(result.Files, file)
	}
	if _, err := manager.store.nativeWorkspaceAccessTx(ctx, tx, access); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}
