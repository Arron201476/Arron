package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"slices"
	"sort"
	"strings"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/scriptsandbox"
)

const nativeManifestBudget = 256 << 10

const nativeWorkspaceManifestSchema = `
CREATE TABLE IF NOT EXISTS native_workspace_manifests (
	session_id TEXT PRIMARY KEY REFERENCES native_workspace_leases(session_id),
	manifest_hash TEXT NOT NULL,
	manifest_json TEXT NOT NULL,
	done_json TEXT NOT NULL DEFAULT '[]',
	status TEXT NOT NULL CHECK(status IN ('prepared','completed','failed','deleted')),
	updated_at TEXT NOT NULL
);`

// Sources are version references, never caller-supplied file contents or host paths.
type NativeWorkspaceSkillSource struct {
	CapabilityID string `json:"capability_id"`
	Version      string `json:"version"`
	ContentHash  string `json:"content_hash"`
}

type NativeWorkspaceProjectSource struct {
	Path        string `json:"path"`
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
}

type NativeWorkspaceManifestSources struct {
	Skills     []NativeWorkspaceSkillSource     `json:"skills"`
	Files      []NativeWorkspaceProjectSource   `json:"files"`
	Memory     *NativeWorkspaceMemorySource     `json:"memory,omitempty"`
	Generation *NativeWorkspaceGenerationSource `json:"generation,omitempty"`
}

type NativeWorkspaceMemorySource struct {
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
}

const nativeMemoryDirectory = ".agent-memory"

func (s *Store) nativeMemoryDocumentTx(ctx context.Context, tx *sql.Tx, projectID string, source NativeWorkspaceMemorySource) (AgentMemoryDocument, error) {
	activity, ok := AgentActivityFromContext(ctx)
	if !ok || activity.ProjectID != projectID || activity.AllowTerminal || source.Version < 1 {
		return AgentMemoryDocument{}, domainError("AGENT_MEMORY_INVALID", "Memory source requires its current execution identity.")
	}
	user, err := s.resolveAgentActivityPrincipalTx(ctx, tx, activity)
	if err != nil {
		return AgentMemoryDocument{}, err
	}
	snapshot, err := agentMemorySnapshotQuery(ctx, tx, activity, user)
	if err != nil {
		return AgentMemoryDocument{}, err
	}
	if !snapshot.ReadEnabled || snapshot.Version != source.Version || snapshot.ContentHash != source.ContentHash {
		return AgentMemoryDocument{}, domainError("AGENT_MEMORY_CONFLICT", "Memory source differs from its execution binding.")
	}
	return agentMemoryQuery(ctx, tx, user, projectID, snapshot.Version)
}

type NativeWorkspaceManifestFile struct {
	Path         string                           `json:"path"`
	SHA256       string                           `json:"sha256"`
	SizeBytes    int64                            `json:"size_bytes"`
	Skill        *NativeWorkspaceSkillSource      `json:"skill,omitempty"`
	ResourcePath string                           `json:"resource_path,omitempty"`
	ProjectFile  *NativeWorkspaceProjectSource    `json:"project_file,omitempty"`
	Memory       *NativeWorkspaceMemorySource     `json:"memory,omitempty"`
	Generation   *NativeWorkspaceGenerationSource `json:"generation,omitempty"`
}

type NativeWorkspaceManifest struct {
	SessionID    string                        `json:"session_id"`
	ManifestHash string                        `json:"manifest_hash"`
	Files        []NativeWorkspaceManifestFile `json:"files"`
}

type nativeManifestRecord struct {
	hash, body, done, status string
}

type nativeManifestOperation struct {
	hash, key string
}

func readNativeManifestTx(ctx context.Context, tx *sql.Tx, sessionID string) (nativeManifestRecord, error) {
	var r nativeManifestRecord
	err := tx.QueryRowContext(ctx, `SELECT manifest_hash,manifest_json,done_json,status FROM native_workspace_manifests WHERE session_id=?`, sessionID).Scan(&r.hash, &r.body, &r.done, &r.status)
	if err == nil && (len(r.body) > 64<<10 || len(r.done) > 160<<10 || sha256Hex([]byte(r.body)) != r.hash) {
		err = domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Stored materialization identity is invalid.")
	}
	return r, err
}

func nativeManifestPendingTx(ctx context.Context, tx *sql.Tx, sessionID string) error {
	var pending bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_workspace_manifests WHERE session_id=? AND status!='completed')`, sessionID).Scan(&pending)
	if err == nil && pending {
		return domainError("NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED", "Initial resources have not been completely materialized and sealed.")
	}
	return err
}

func nativeManifestFiles(r nativeManifestRecord) ([]NativeWorkspaceManifestFile, error) {
	var files []NativeWorkspaceManifestFile
	if json.Unmarshal([]byte(r.body), &files) != nil || len(files) > 256 || files == nil {
		return nil, domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Stored resource inventory is invalid.")
	}
	return files, nil
}

func (s *Store) nativeManifestFileBytes(ctx context.Context, tx *sql.Tx, projectID string, file NativeWorkspaceManifestFile) ([]byte, error) {
	var data []byte
	count := 0
	if file.Skill != nil {
		count++
	}
	if file.ProjectFile != nil {
		count++
	}
	if file.Memory != nil {
		count++
	}
	if file.Generation != nil {
		count++
	}
	if count != 1 {
		return nil, domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Each resource requires exactly one source.")
	}
	if file.Generation != nil {
		inputs, err := s.nativeGenerationInputsTx(ctx, tx, projectID, *file.Generation)
		if err != nil {
			return nil, err
		}
		content, found := inputs[file.ResourcePath]
		if !found || file.Path != nativeGenerationInputPath(*file.Generation, file.ResourcePath) {
			return nil, domainError("AGENT_MEMORY_INVALID", "Generation input path differs from its private destination.")
		}
		data = []byte(content)
	} else if file.Memory != nil {
		doc, err := s.nativeMemoryDocumentTx(ctx, tx, projectID, *file.Memory)
		if err != nil {
			return nil, err
		}
		content, found := doc.Files[file.ResourcePath]
		if !found || file.Path != nativeMemoryDirectory+"/"+file.ResourcePath {
			return nil, domainError("AGENT_MEMORY_INVALID", "Memory resource path differs from its private destination.")
		}
		data = []byte(content)
	} else if file.Skill != nil && file.ProjectFile == nil {
		entry, found, err := s.capabilityEntryForProjectVersionQuery(ctx, tx, projectID, file.Skill.CapabilityID, file.Skill.Version)
		if err != nil {
			return nil, err
		}
		if !found || entry.Status != capability.Available || entry.Skill == nil || entry.Skill.ContentHash != file.Skill.ContentHash {
			return nil, domainError("NATIVE_WORKSPACE_RESOURCE_CHANGED", "The selected execution Skill version is no longer available.")
		}
		data, err = entry.Skill.ReadResourceBytes(file.ResourcePath)
		if err != nil {
			return nil, domainError("NATIVE_WORKSPACE_RESOURCE_CHANGED", "The selected Skill resource failed its version or hash check.")
		}
	} else if file.ProjectFile != nil && file.Skill == nil {
		var content, hash string
		var size int64
		err := tx.QueryRowContext(ctx, `SELECT content,content_hash,size_bytes FROM project_file_versions WHERE project_id=? AND path_key=? AND path=? AND version=? AND deleted=0`, projectID, strings.ToLower(file.ProjectFile.Path), file.ProjectFile.Path, file.ProjectFile.Version).Scan(&content, &hash, &size)
		if err != nil || hash != file.ProjectFile.ContentHash || size != int64(len(content)) {
			return nil, domainError("NATIVE_WORKSPACE_RESOURCE_CHANGED", "The selected project file version is unavailable or invalid.")
		}
		data = []byte(content)
	} else {
		return nil, domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Each resource requires exactly one source.")
	}
	if int64(len(data)) != file.SizeBytes || sha256Hex(data) != file.SHA256 {
		return nil, domainError("NATIVE_WORKSPACE_RESOURCE_CHANGED", "Resource bytes do not match their frozen inventory.")
	}
	return data, nil
}

func (manager *NativeWorkspaceManager) PrepareManifest(ctx context.Context, access NativeWorkspaceAccess, sources NativeWorkspaceManifestSources) (NativeWorkspaceManifest, error) {
	var result NativeWorkspaceManifest
	if activity, ok := AgentActivityFromContext(ctx); ok && activity.MemoryGenerationID != "" && (len(sources.Skills) != 0 || len(sources.Files) != 0) {
		return result, domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Memory generation can only initialize its private memory baseline.")
	}
	if !manager.requirePolicy || len(sources.Skills) > 32 || len(sources.Files) > 256 {
		return result, domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Resource selection exceeds its policy or inventory bounds.")
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
	if _, err := manager.store.nativeWorkspaceOwnerTx(ctx, tx, access.DispatchGeneration, false); err != nil {
		return result, err
	}
	files := []NativeWorkspaceManifestFile{}
	registry, err := manager.store.buildSelectionRegistry(ctx, tx, lease.workspaceID, lease.projectID)
	if err != nil {
		return result, err
	}
	for _, source := range sources.Skills {
		var selected *capability.SkillPackage
		for _, entry := range registry.Entries() {
			if entry.Status == capability.Available && entry.Skill != nil && entry.Skill.CapabilityID == source.CapabilityID && entry.Skill.Version == source.Version && entry.Skill.ContentHash == source.ContentHash {
				selected = entry.Skill
				break
			}
		}
		if selected == nil {
			return result, domainError("NATIVE_WORKSPACE_RESOURCE_CHANGED", "Skill selection is not in this execution's frozen catalog.")
		}
		for _, resource := range selected.Resources {
			files = append(files, NativeWorkspaceManifestFile{Path: ".skills/" + selected.Name + "/" + resource.Path, SHA256: strings.TrimPrefix(resource.ContentHash, "sha256:"), SizeBytes: resource.SizeBytes, Skill: &source, ResourcePath: resource.Path})
		}
	}
	for _, source := range sources.Files {
		if source.Version < 1 || validateProjectFilePath(source.Path) != nil || strings.EqualFold(strings.Split(source.Path, "/")[0], ".skills") || strings.EqualFold(strings.Split(source.Path, "/")[0], nativeMemoryDirectory) {
			return result, domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Project sources require exact versions and cannot replace installed Skills.")
		}
		var size int64
		if err := tx.QueryRowContext(ctx, `SELECT size_bytes FROM project_file_versions WHERE project_id=? AND path_key=? AND path=? AND version=? AND content_hash=? AND deleted=0`, lease.projectID, strings.ToLower(source.Path), source.Path, source.Version, source.ContentHash).Scan(&size); err != nil {
			return result, domainError("NATIVE_WORKSPACE_RESOURCE_CHANGED", "Selected project file version or hash is unavailable.")
		}
		files = append(files, NativeWorkspaceManifestFile{Path: source.Path, SHA256: strings.TrimPrefix(source.ContentHash, "sha256:"), SizeBytes: size, ProjectFile: &source})
	}
	generationInputs := map[string]string{}
	if sources.Generation != nil {
		generationInputs, err = manager.store.nativeGenerationInputsTx(ctx, tx, lease.projectID, *sources.Generation)
		if err != nil {
			return result, err
		}
	}
	if sources.Memory != nil {
		doc, err := manager.store.nativeMemoryDocumentTx(ctx, tx, lease.projectID, *sources.Memory)
		if err != nil {
			return result, err
		}
		for resource, content := range doc.Files {
			if sources.Generation != nil && sources.Generation.PlanHash != "" {
				if replacement, found := generationInputs[nativeMemoryDirectory+"/"+resource]; found {
					if resource != "raw_memories.md" && replacement != content {
						return result, domainError("AGENT_MEMORY_CONFLICT", "Preparation cannot overwrite existing memory content.")
					}
					continue
				}
			}
			files = append(files, NativeWorkspaceManifestFile{Path: nativeMemoryDirectory + "/" + resource, SHA256: sha256Hex([]byte(content)), SizeBytes: int64(len(content)), Memory: sources.Memory, ResourcePath: resource})
		}
	}
	if sources.Generation != nil {
		for resource, content := range generationInputs {
			files = append(files, NativeWorkspaceManifestFile{Path: nativeGenerationInputPath(*sources.Generation, resource), SHA256: sha256Hex([]byte(content)), SizeBytes: int64(len(content)), Generation: sources.Generation, ResourcePath: resource})
		}
	}
	if len(files) > 256 {
		return result, domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Initial resources exceed 256 files.")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	paths := map[string]bool{}
	var size int64
	for _, file := range files {
		key := strings.ToLower(file.Path)
		if validateProjectFilePath(file.Path) != nil || paths[key] || !nativeLeaseKeyPattern.MatchString(file.SHA256) || file.SizeBytes < 0 || file.SizeBytes > scriptsandbox.WorkspaceFileBytes {
			return result, domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Initial file paths, hashes or sizes are invalid.")
		}
		paths[key] = true
		size += file.SizeBytes
		if size > manager.limits.DiskBytes {
			return result, domainError("NATIVE_WORKSPACE_QUOTA_EXCEEDED", "Initial resources exceed the workspace disk quota.")
		}
		if _, err := manager.store.nativeManifestFileBytes(ctx, tx, lease.projectID, file); err != nil {
			return result, err
		}
	}
	if _, err := nativeManifestAllowed(files); err != nil {
		return result, err
	}
	body, err := json.Marshal(files)
	if err != nil {
		return result, err
	}
	if len(body) > 64<<10 {
		return result, domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Initial resource metadata exceeds its bound.")
	}
	result = NativeWorkspaceManifest{SessionID: access.SessionID, ManifestHash: sha256Hex(body), Files: files}
	previous, err := readNativeManifestTx(ctx, tx, access.SessionID)
	if err == nil {
		if previous.hash != result.ManifestHash || (previous.status != "prepared" && previous.status != "completed") {
			return result, domainError("NATIVE_WORKSPACE_MANIFEST_CONFLICT", "An execution cannot replace its initial resource inventory.")
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_workspace_environments WHERE session_id=?)`, access.SessionID).Scan(&exists); err != nil {
		return result, err
	}
	if exists || lease.SnapshotVersion != 0 {
		return result, domainError("NATIVE_WORKSPACE_MANIFEST_CONFLICT", "Initial resources must be selected before any environment or snapshot exists.")
	}
	if err := manager.store.enforceWorkspaceQuotaTx(ctx, tx, lease.workspaceID, "storage_bytes", nativeManifestBudget); err != nil {
		return result, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO native_workspace_manifests(session_id,manifest_hash,manifest_json,status,updated_at) VALUES(?,?,?,'prepared',?)`, access.SessionID, result.ManifestHash, string(body), formatTime(manager.store.now()))
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

// Keys describe only the SDK mkdir/write/chmod operations needed by this exact
// inventory. No shell, remove, arbitrary path or later overwrite is authorized.
func nativeManifestOperationKey(operation scriptsandbox.WorkspaceFileOperation, contentHash string) string {
	var mode int64 = -1
	if operation.Mode != nil {
		mode = *operation.Mode
	}
	body, _ := json.Marshal([]any{operation.Operation, operation.Path, contentHash, operation.Parents, operation.Recursive, mode})
	return sha256Hex(body)
}

func nativeManifestAllowed(files []NativeWorkspaceManifestFile) (map[string]bool, error) {
	allowed, kinds, names := map[string]bool{}, map[string]bool{}, map[string]string{}
	dirs := map[string]bool{}
	for _, file := range files {
		for name := file.Path; name != "."; name = path.Dir(name) {
			key, directory := strings.ToLower(name), name != file.Path
			if old, found := kinds[key]; found && (old != directory || names[key] != name || !directory) {
				return nil, domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Initial resources contain path or directory collisions.")
			}
			kinds[key], names[key] = directory, name
			if directory {
				dirs[name] = true
			}
		}
		mode := int64(0644)
		allowed[nativeManifestOperationKey(scriptsandbox.WorkspaceFileOperation{Operation: "write", Path: file.Path}, file.SHA256)] = true
		allowed[nativeManifestOperationKey(scriptsandbox.WorkspaceFileOperation{Operation: "chmod", Path: file.Path, Mode: &mode}, "")] = true
	}
	if len(kinds) > 1024 {
		return nil, domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Initial resources exceed the directory bound.")
	}
	for name := range dirs {
		mode := int64(0755)
		allowed[nativeManifestOperationKey(scriptsandbox.WorkspaceFileOperation{Operation: "mkdir", Path: name, Parents: true}, "")] = true
		allowed[nativeManifestOperationKey(scriptsandbox.WorkspaceFileOperation{Operation: "chmod", Path: name, Mode: &mode}, "")] = true
	}
	return allowed, nil
}

func validateNativeManifestOperationTx(ctx context.Context, tx *sql.Tx, sessionID string, operation nativeManifestOperation) error {
	r, err := readNativeManifestTx(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	var done []string
	if r.hash != operation.hash || r.status != "prepared" || json.Unmarshal([]byte(r.done), &done) != nil || slices.Contains(done, operation.key) {
		return domainError("NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED", "Materialization is sealed, invalid or already applied; it cannot be replayed.")
	}
	return nil
}

func beginNativeManifestFileTx(ctx context.Context, tx *sql.Tx, sessionID, hash string, file scriptsandbox.WorkspaceFileOperation) (*nativeManifestOperation, error) {
	r, err := readNativeManifestTx(ctx, tx, sessionID)
	if err != nil {
		return nil, err
	}
	files, err := nativeManifestFiles(r)
	if err != nil {
		return nil, err
	}
	allowed, err := nativeManifestAllowed(files)
	if err != nil {
		return nil, err
	}
	contentHash := ""
	if file.Operation == "write" {
		contentHash = sha256Hex(file.Data)
	}
	operation := &nativeManifestOperation{hash: hash, key: nativeManifestOperationKey(file, contentHash)}
	if !allowed[operation.key] {
		return nil, domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "File mutation is not an exact authorized initial resource operation.")
	}
	return operation, validateNativeManifestOperationTx(ctx, tx, sessionID, *operation)
}

func finishNativeManifestFileTx(ctx context.Context, tx *sql.Tx, state nativeEnvironment, operationErr error, now string) error {
	if state.manifestOperation == nil {
		return nil
	}
	r, err := readNativeManifestTx(ctx, tx, state.SessionID)
	if err != nil {
		return err
	}
	if r.status != "prepared" || r.hash != state.manifestOperation.hash {
		return domainError("NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED", "A late file operation cannot replace a different materialization.")
	}
	status := "failed"
	if operationErr == nil {
		var done []string
		if json.Unmarshal([]byte(r.done), &done) != nil || len(done) >= 2048 || slices.Contains(done, state.manifestOperation.key) {
			return domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Invalid materialization receipt history.")
		}
		done = append(done, state.manifestOperation.key)
		body, _ := json.Marshal(done)
		r.done, status = string(body), "prepared"
	}
	_, err = tx.ExecContext(ctx, `UPDATE native_workspace_manifests SET done_json=?,status=?,updated_at=? WHERE session_id=?`, r.done, status, now, state.SessionID)
	return err
}

func (manager *NativeWorkspaceManager) ReadManifestFile(ctx context.Context, access NativeWorkspaceAccess, hash, filePath string) ([]byte, error) {
	tx, err := manager.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	lease, err := manager.store.nativeWorkspaceAccessTx(ctx, tx, access)
	if err != nil {
		return nil, err
	}
	if !manager.requirePolicy {
		return nil, domainError("NATIVE_WORKSPACE_POLICY_REQUIRED", "Resource reads require the current sandbox policy.")
	}
	if err := manager.validatePolicyTx(ctx, tx, lease.workspaceID); err != nil {
		return nil, err
	}
	r, err := readNativeManifestTx(ctx, tx, access.SessionID)
	if err != nil {
		return nil, err
	}
	if r.hash != hash || r.status != "prepared" {
		return nil, domainError("NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED", "Initial resources are unavailable after materialization or failure.")
	}
	files, err := nativeManifestFiles(r)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if file.Path == filePath {
			return manager.store.nativeManifestFileBytes(ctx, tx, lease.projectID, file)
		}
	}
	return nil, domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Resource path is not in the selected inventory.")
}

func (manager *NativeWorkspaceManager) SealManifest(ctx context.Context, access NativeWorkspaceAccess, hash string) error {
	tx, err := manager.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	lease, err := manager.store.nativeWorkspaceAccessTx(ctx, tx, access)
	if err != nil {
		return err
	}
	if !manager.requirePolicy {
		return domainError("NATIVE_WORKSPACE_POLICY_REQUIRED", "Materialization requires the current sandbox policy.")
	}
	if err := manager.validatePolicyTx(ctx, tx, lease.workspaceID); err != nil {
		return err
	}
	if _, err := manager.store.nativeWorkspaceOwnerTx(ctx, tx, access.DispatchGeneration, false); err != nil {
		return err
	}
	r, err := readNativeManifestTx(ctx, tx, access.SessionID)
	if err != nil {
		return err
	}
	if r.hash != hash || (r.status != "prepared" && r.status != "completed") {
		return domainError("NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED", "Materialization cannot be sealed.")
	}
	files, err := nativeManifestFiles(r)
	if err != nil {
		return err
	}
	allowed, err := nativeManifestAllowed(files)
	if err != nil {
		return err
	}
	var done []string
	if json.Unmarshal([]byte(r.done), &done) != nil || len(done) != len(allowed) {
		return domainError("NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED", "Some initial file or metadata operations are unconfirmed.")
	}
	for _, key := range done {
		if !allowed[key] {
			return domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Materialization receipts do not match the inventory.")
		}
		delete(allowed, key)
	}
	var ready bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_workspace_environments WHERE session_id=? AND status='ready' AND operation_id='')`, access.SessionID).Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return domainError("NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED", "Materialization requires an idle confirmed environment.")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE native_workspace_manifests SET status='completed',updated_at=? WHERE session_id=?`, formatTime(manager.store.now()), access.SessionID); err != nil {
		return err
	}
	return tx.Commit()
}
