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

	"content-agent/backend/internal/identity"
)

const publishAgentMemoryTool = "runtime:publish_agent_memory"

// Only this generation's durable, approved publication can advance its base.
func memoryGenerationOwnPublicationQuery(ctx context.Context, query rowQueryer, job AgentMemoryGeneration, current AgentMemoryDocument) (bool, error) {
	if job.Phase != "consolidation" || !current.Enabled || current.Forgotten || current.Version != job.BaseVersion+1 {
		return false, nil
	}
	var found bool
	err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_memory_publications p
		JOIN agent_memory_tool_calls m ON m.agent_tool_call_id=p.agent_tool_call_id
		JOIN agent_tool_calls c ON c.agent_tool_call_id=p.agent_tool_call_id
		JOIN agent_memory_versions v ON v.project_id=p.project_id AND v.user_id=p.user_id AND v.version=p.version
		WHERE m.generation_id=? AND m.phase='consolidation' AND m.original_attempt<=?
		AND p.activity_key=? AND p.workspace_id=? AND p.project_id=? AND p.user_id=?
		AND p.base_version=? AND p.base_hash=? AND p.expected_version=? AND p.version=? AND p.content_hash=?
		AND c.tool_id=? AND c.arguments_hash=p.arguments_hash AND c.approval_status='approved'
		AND c.project_id=p.project_id AND c.workspace_id=p.workspace_id AND c.agent_turn_id IS NULL
		AND c.skill_invocation_id IS NULL AND c.tool_kind='runtime_function' AND c.approval_policy='always' AND c.access_mode='write'
		AND c.approval_consumed_at IS NOT NULL AND c.status IN ('running','completed')
		AND v.workspace_id=p.workspace_id AND v.content_hash=p.content_hash AND v.enabled=1 AND v.forgotten=0
		AND v.request_id='tool:'||c.agent_tool_call_id AND v.request_hash=p.arguments_hash)`,
		job.GenerationID, job.Attempt, "memory:"+job.GenerationID, job.WorkspaceID, job.ProjectID, job.UserID,
		job.BaseVersion, job.BaseHash, job.BaseVersion, current.Version, current.ContentHash, publishAgentMemoryTool).Scan(&found)
	return found, err
}

func migrateAgentMemoryPublications(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_memory_publications (
		agent_tool_call_id TEXT PRIMARY KEY REFERENCES agent_tool_calls(agent_tool_call_id),
		activity_key TEXT NOT NULL REFERENCES agent_memory_snapshots(activity_key),
		workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		user_id TEXT NOT NULL REFERENCES users(user_id),
		base_version INTEGER NOT NULL CHECK(base_version >= 0),
		base_hash TEXT NOT NULL,
		expected_version INTEGER NOT NULL CHECK(expected_version >= 0),
		arguments_hash TEXT NOT NULL,
		arguments_json TEXT NOT NULL DEFAULT '',
		session_id TEXT NOT NULL DEFAULT '',
		version INTEGER NOT NULL DEFAULT 0 CHECK(version >= 0),
		content_hash TEXT NOT NULL DEFAULT ''
	);`)
	if err != nil {
		return err
	}
	for _, column := range []string{"arguments_json", "session_id"} {
		present, err := tableHasColumn(db, "agent_memory_publications", column)
		if err != nil {
			return err
		}
		if !present {
			if _, err := db.Exec("ALTER TABLE agent_memory_publications ADD COLUMN " + column + " TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
	}
	return nil
}

type AgentMemoryPublicationArguments struct {
	Snapshot        NativeWorkspaceSnapshotReference `json:"snapshot"`
	ExpectedVersion int                              `json:"expected_version"`
	Files           []string                         `json:"files"`
}

type AgentMemoryPublicationRequest struct {
	AgentToolCallID string                          `json:"agent_tool_call_id"`
	SDKToolCallID   string                          `json:"sdk_tool_call_id"`
	Arguments       AgentMemoryPublicationArguments `json:"arguments"`
}

type AgentMemoryPublicationReceipt struct {
	AgentToolCallID string                           `json:"agent_tool_call_id"`
	SessionID       string                           `json:"session_id"`
	ProjectID       string                           `json:"project_id"`
	UserID          string                           `json:"user_id"`
	Version         int                              `json:"version"`
	ContentHash     string                           `json:"content_hash"`
	Snapshot        NativeWorkspaceSnapshotReference `json:"snapshot"`
}

func decodeMemoryPublicationArguments(raw json.RawMessage) (AgentMemoryPublicationArguments, error) {
	var args AgentMemoryPublicationArguments
	invalid := domainError("REQUEST_VALIDATION_FAILED", "Memory publication requires exact snapshot, version and private file selection.")
	var fields map[string]json.RawMessage
	if len(raw) > 64<<10 || json.Unmarshal(raw, &fields) != nil || len(fields) != 3 {
		return args, invalid
	}
	for _, name := range []string{"snapshot", "expected_version", "files"} {
		if len(fields[name]) == 0 || bytes.Equal(fields[name], []byte("null")) {
			return args, invalid
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&args) != nil || decoder.Decode(new(any)) != io.EOF || args.ExpectedVersion < 0 || args.Snapshot.Version < 1 || !nativeLeaseKeyPattern.MatchString(args.Snapshot.SHA256) || len(args.Files) < 1 || len(args.Files) > 256 {
		return args, invalid
	}
	seen := map[string]string{}
	for _, file := range args.Files {
		if validateProjectFilePath(file) != nil || !strings.HasPrefix(file, nativeMemoryDirectory+"/") || registerSkillPackagePath(seen, file) != nil {
			return args, invalid
		}
	}
	if seen[strings.ToLower(nativeMemoryDirectory+"/memory_summary.md")] != nativeMemoryDirectory+"/memory_summary.md" {
		return args, invalid
	}
	return args, nil
}

func (s *Store) memoryPublicationBindingTx(ctx context.Context, tx *sql.Tx, command BeginAgentToolCallCommand) (AgentMemorySnapshot, AgentMemoryPublicationArguments, error) {
	args, err := decodeMemoryPublicationArguments(command.Arguments)
	if err != nil {
		return AgentMemorySnapshot{}, args, err
	}
	user, activity, err := s.instructionToolUserTx(ctx, tx, command.ProjectID)
	if err != nil {
		return AgentMemorySnapshot{}, args, err
	}
	if activity.AgentTurnID != command.AgentTurnID || activity.AgentTaskAttemptID != command.AgentTaskAttemptID || activity.ExecutionAttemptID != command.ExecutionAttemptID || activity.MemoryGenerationID != command.MemoryGenerationID || activity.MemoryGenerationAttempt != command.MemoryGenerationAttempt || activity.AttemptToken != command.AttemptToken {
		return AgentMemorySnapshot{}, args, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Memory publication belongs to another execution.")
	}
	snapshot, err := agentMemorySnapshotQuery(ctx, tx, activity, user)
	return snapshot, args, err
}

func (s *Store) saveMemoryPublicationIntentTx(ctx context.Context, tx *sql.Tx, command BeginAgentToolCallCommand, callID, hash string) error {
	snapshot, args, err := s.memoryPublicationBindingTx(ctx, tx, command)
	if err != nil {
		return err
	}
	current, err := agentMemoryQuery(ctx, tx, identity.Principal{UserID: snapshot.UserID, WorkspaceID: snapshot.WorkspaceID}, snapshot.ProjectID, 0)
	if err != nil {
		return err
	}
	if current.Version != args.ExpectedVersion {
		return domainError("AGENT_MEMORY_CONFLICT", "Memory has changed; prepare a new publication.")
	}
	var session string
	workspaceKey := snapshot.ActivityKey
	if command.MemoryGenerationID != "" {
		owner, err := s.nativeWorkspaceOwnerTx(ctx, tx, 0, false)
		if err != nil {
			return err
		}
		workspaceKey = owner.activityKey
	}
	err = tx.QueryRowContext(ctx, `SELECT session_id FROM native_workspace_leases WHERE activity_key=? AND workspace_id=? AND project_id=? AND user_id=?`, workspaceKey, snapshot.WorkspaceID, snapshot.ProjectID, snapshot.UserID).Scan(&session)
	if err != nil {
		return err
	}
	files, err := memoryPublicationFilesTx(ctx, tx, session, args)
	if err != nil {
		return err
	}
	if err := validateMemoryGenerationSelectionTx(ctx, tx, files); err != nil {
		return err
	}
	if err := s.enforceWorkspaceQuotaTx(ctx, tx, snapshot.WorkspaceID, "storage_bytes", int64(2048+len(command.Arguments))); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_memory_publications(agent_tool_call_id,activity_key,workspace_id,project_id,user_id,base_version,base_hash,expected_version,arguments_hash,arguments_json,session_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		callID, snapshot.ActivityKey, snapshot.WorkspaceID, snapshot.ProjectID, snapshot.UserID, snapshot.Version, snapshot.ContentHash, args.ExpectedVersion, hash, string(command.Arguments), session)
	return err
}

func authorizeMemoryPublicationApprovalTx(ctx context.Context, tx *sql.Tx, call AgentToolCall) error {
	user, ok := identity.UserFromContext(ctx)
	if !ok {
		return domainError("WORKSPACE_ACCESS_DENIED", "Only the memory owner can decide its publication.")
	}
	user, err := resolvePrincipalQuery(ctx, tx, user)
	if err != nil {
		return err
	}
	var owner, workspace string
	err = tx.QueryRowContext(ctx, `SELECT user_id,workspace_id FROM agent_memory_publications WHERE agent_tool_call_id=?`, call.AgentToolCallID).Scan(&owner, &workspace)
	if err != nil {
		return err
	}
	if user.UserID != owner || user.WorkspaceID != workspace || !user.Allows(identity.RoleEditor) {
		return domainError("WORKSPACE_ACCESS_DENIED", "Only the memory owner can decide its publication.")
	}
	return nil
}

func verifyMemoryPublicationReceiptTx(ctx context.Context, tx *sql.Tx, call AgentToolCall) error {
	var found bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_memory_publications p JOIN agent_memory_versions v ON v.project_id=p.project_id AND v.user_id=p.user_id AND v.version=p.version
		WHERE p.agent_tool_call_id=? AND p.arguments_hash=? AND p.version>0 AND v.content_hash=p.content_hash AND v.forgotten=0)`, call.AgentToolCallID, call.ArgumentsHash).Scan(&found)
	if err != nil {
		return err
	}
	if !found {
		return domainError("AGENT_MEMORY_CONFLICT", "Memory publication has no confirmed persistence receipt.")
	}
	return nil
}

func (manager *NativeWorkspaceManager) PublishMemory(ctx context.Context, access NativeWorkspaceAccess, request AgentMemoryPublicationRequest) (AgentMemoryPublicationReceipt, error) {
	result := AgentMemoryPublicationReceipt{AgentToolCallID: request.AgentToolCallID, SessionID: access.SessionID, Snapshot: request.Arguments.Snapshot}
	raw, err := json.Marshal(request.Arguments)
	if err != nil {
		return result, err
	}
	args, err := decodeMemoryPublicationArguments(raw)
	if err != nil {
		return result, err
	}
	_, hash, _, err := summarizeAgentToolPayload(raw, true)
	if err != nil {
		return result, err
	}
	if !manager.requirePolicy {
		return result, domainError("AGENT_ACTIVITY_FORBIDDEN", "Memory publication requires current workspace policy.")
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
	activity, _ := AgentActivityFromContext(ctx)
	if err := validateAgentActivityToolCallQuery(ctx, tx, activity, request.AgentToolCallID); err != nil {
		return result, err
	}
	call, err := getAgentToolCallTx(ctx, tx, request.AgentToolCallID)
	if err != nil {
		return result, err
	}
	if call.ToolID != publishAgentMemoryTool || call.ToolKind != "runtime_function" || call.ParentToolCallID != "" || call.SDKToolCallID != request.SDKToolCallID || call.ArgumentsHash != hash || call.ApprovalPolicy != "always" || call.AccessMode != "write" || call.ApprovalStatus != "approved" || call.ApprovalConsumedAt == nil || (call.Status != "running" && call.Status != "completed") {
		return result, domainError("AGENT_TOOL_APPROVAL_REQUIRED", "Memory publication requires its exact approved SDK call.")
	}
	approval, err := getAgentToolApprovalForCallTx(ctx, tx, request.AgentToolCallID)
	if err != nil {
		return result, err
	}
	if approval.Status != "approved" {
		return result, domainError("AGENT_TOOL_APPROVAL_REQUIRED", "Memory publication approval is unavailable.")
	}
	if err := manager.store.validateAgentToolConfigurationTx(ctx, tx, call, ""); err != nil {
		return result, err
	}
	var key, workspace, argumentsHash, session string
	var expectedVersion int
	err = tx.QueryRowContext(ctx, `SELECT activity_key,workspace_id,project_id,user_id,arguments_hash,expected_version,version,content_hash,session_id FROM agent_memory_publications WHERE agent_tool_call_id=?`, request.AgentToolCallID).
		Scan(&key, &workspace, &result.ProjectID, &result.UserID, &argumentsHash, &expectedVersion, &result.Version, &result.ContentHash, &session)
	if err != nil {
		return result, err
	}
	if session != access.SessionID || key != instructionActivityKey(activity) || workspace != lease.workspaceID || result.ProjectID != lease.projectID || result.UserID != lease.userID || argumentsHash != hash || expectedVersion != args.ExpectedVersion {
		return result, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Memory publication intent does not match this execution.")
	}
	if result.Version > 0 {
		if err := verifyMemoryPublicationReceiptTx(ctx, tx, call); err != nil {
			return result, err
		}
		return result, nil
	}
	if call.Status != "running" {
		return result, domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "A completed tool cannot create a memory publication.")
	}
	files, err := memoryPublicationFilesTx(ctx, tx, session, args)
	if err != nil {
		return result, err
	}
	if err := validateMemoryGenerationSelectionTx(ctx, tx, files); err != nil {
		return result, err
	}
	command := UpdateAgentMemoryCommand{ProjectID: lease.projectID, ExpectedVersion: args.ExpectedVersion, Files: files, Enabled: true, RequestID: "tool:" + call.AgentToolCallID}
	encoded, err := validateAgentMemory(command)
	if err != nil {
		return result, err
	}
	user := identity.Principal{UserID: result.UserID, WorkspaceID: workspace}
	current, err := agentMemoryQuery(ctx, tx, user, lease.projectID, 0)
	if err != nil {
		return result, err
	}
	if current.Version != args.ExpectedVersion {
		return result, domainError("AGENT_MEMORY_CONFLICT", "Memory changed after this publication was prepared.")
	}
	if err := manager.store.enforceWorkspaceQuotaTx(ctx, tx, workspace, "storage_bytes", int64(len(encoded))); err != nil {
		return result, err
	}
	doc, err := manager.store.insertAgentMemoryVersionTx(ctx, tx, user, command, encoded, hash)
	if err != nil {
		return result, err
	}
	result.Version, result.ContentHash = doc.Version, doc.ContentHash
	if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_publications SET version=?,content_hash=? WHERE agent_tool_call_id=? AND version=0`, doc.Version, doc.ContentHash, call.AgentToolCallID); err != nil {
		return result, err
	}
	if _, err := manager.store.nativeWorkspaceAccessTx(ctx, tx, access); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func validateMemoryGenerationSelectionTx(ctx context.Context, tx *sql.Tx, files map[string]string) error {
	activity, _ := AgentActivityFromContext(ctx)
	if activity.MemoryGenerationID == "" {
		return nil
	}
	job, err := memoryGenerationQuery(ctx, tx, activity.MemoryGenerationID)
	if err != nil {
		return err
	}
	var plan AgentMemoryInputPlan
	if job.Phase != "consolidation" || json.Unmarshal([]byte(job.InputPlan), &plan) != nil {
		return domainError("AGENT_MEMORY_CONFLICT", "Memory publication requires its consolidation input plan.")
	}
	expected := plan.Files[nativeGenerationDirectory+"/phase_two_selection.json"]
	if expected == "" || files["phase_two_selection.json"] != expected {
		return domainError("AGENT_MEMORY_CONFLICT", "Memory publication must preserve its exact SDK selection candidate.")
	}
	return nil
}

// Preview and publication must interpret the same immutable archive selection.
func memoryPublicationFilesTx(ctx context.Context, tx *sql.Tx, session string, args AgentMemoryPublicationArguments) (map[string]string, error) {
	selected, err := readNativeWorkspaceSnapshotTx(ctx, tx, session, args.Snapshot.Version, args.Snapshot.SHA256)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, name := range args.Files {
		wanted[name] = true
	}
	files := map[string]string{}
	reader := tar.NewReader(bytes.NewReader(selected.Snapshot.Archive))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeReg && wanted[header.Name] {
			resource := strings.TrimPrefix(header.Name, nativeMemoryDirectory+"/")
			if _, duplicate := files[resource]; duplicate {
				return nil, domainError("AGENT_MEMORY_INVALID", "Memory snapshot has duplicate selected files.")
			}
			body, err := io.ReadAll(io.LimitReader(reader, maxAgentMemoryBytes+1))
			if err != nil || len(body) > maxAgentMemoryBytes || !utf8.Valid(body) || int64(len(body)) != header.Size {
				return nil, domainError("AGENT_MEMORY_INVALID", "Memory snapshot content is invalid.")
			}
			files[resource] = string(body)
		}
	}
	if len(files) != len(wanted) {
		return nil, domainError("AGENT_MEMORY_INVALID", "Memory snapshot is missing selected files.")
	}
	if _, err := validateAgentMemory(UpdateAgentMemoryCommand{ProjectID: "preview", ExpectedVersion: args.ExpectedVersion, Files: files, Enabled: true, RequestID: "preview"}); err != nil {
		return nil, err
	}
	return files, nil
}
