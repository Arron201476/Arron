package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

const projectSkillDraftSchema = `
CREATE TABLE IF NOT EXISTS project_skill_install_receipts (
	receipt_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	arguments_hash TEXT NOT NULL,
	skill_installation_id TEXT NOT NULL REFERENCES skill_installations(skill_installation_id),
	skill_version_id TEXT NOT NULL REFERENCES skill_versions(skill_version_id),
	agent_tool_call_id TEXT UNIQUE REFERENCES agent_tool_calls(agent_tool_call_id),
	created_at TEXT NOT NULL
);
`

type ProjectSkillDraft struct {
	ProjectID     string                          `json:"project_id"`
	RootPath      string                          `json:"root_path"`
	SnapshotHash  string                          `json:"snapshot_hash"`
	Files         []ProjectFile                   `json:"files"`
	Status        string                          `json:"status"`
	CapabilityID  string                          `json:"capability_id,omitempty"`
	Version       string                          `json:"version,omitempty"`
	ExecutionMode string                          `json:"execution_mode,omitempty"`
	Manifest      *capability.PublicSkill         `json:"manifest,omitempty"`
	Diagnostics   []capability.SkillDiagnostic    `json:"diagnostics"`
	Installations []ProjectSkillDraftInstallation `json:"installations"`
}

type ProjectSkillDraftInstallation struct {
	InstallationID  string `json:"skill_installation_id"`
	Scope           string `json:"scope"`
	ScopeRef        string `json:"scope_ref"`
	ActiveVersionID string `json:"active_version_id"`
	Version         string `json:"version"`
}

type ProjectSkillInstallArguments struct {
	RootPath                string                `json:"root_path"`
	SnapshotHash            string                `json:"snapshot_hash"`
	Scope                   capability.SkillScope `json:"scope"`
	InstallationID          string                `json:"installation_id"`
	ExpectedActiveVersionID string                `json:"expected_active_version_id"`
}

type ProjectSkillInstallReceipt struct {
	ReceiptID      string    `json:"receipt_id"`
	ProjectID      string    `json:"project_id"`
	InstallationID string    `json:"skill_installation_id"`
	VersionID      string    `json:"skill_version_id"`
	CreatedAt      time.Time `json:"created_at"`
}

type ProjectSkillInstallResult struct {
	Receipt      ProjectSkillInstallReceipt `json:"receipt"`
	Installation SkillInstallation          `json:"installation"`
}

type projectSkillInstallGuard struct {
	projectID, receiptID, argumentHash, callID, sdkCallID string
	arguments                                             ProjectSkillInstallArguments
}

type projectSkillDraftSnapshot struct {
	ProjectSkillDraft
	contents map[string]string
}

func projectSkillDraftSnapshotTx(ctx context.Context, tx *sql.Tx, projectID, root string) (projectSkillDraftSnapshot, error) {
	result := projectSkillDraftSnapshot{ProjectSkillDraft: ProjectSkillDraft{ProjectID: projectID, RootPath: root, Files: []ProjectFile{}, Diagnostics: []capability.SkillDiagnostic{}}, contents: map[string]string{}}
	if root != "" {
		if err := validateProjectFilePath(root); err != nil {
			return result, err
		}
	}
	if _, err := projectFilesWorkspace(ctx, tx, projectID, false); err != nil {
		return result, err
	}
	prefix := ""
	if root != "" {
		prefix = root + "/"
	}
	rows, err := tx.QueryContext(ctx, `SELECT project_id,path,version,content_hash,size_bytes,deleted,agent_tool_call_id,created_at,content,is_binary
		FROM project_file_versions f WHERE project_id = ? AND deleted = 0
		AND version = (SELECT MAX(version) FROM project_file_versions latest WHERE latest.project_id=f.project_id AND latest.path_key=f.path_key)
		AND substr(path,1,length(?)) = ? ORDER BY path_key`, projectID, prefix, prefix)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var total int
	for rows.Next() {
		var file ProjectFile
		var created, content string
		if err := rows.Scan(&file.ProjectID, &file.Path, &file.Version, &file.ContentHash, &file.SizeBytes, &file.Deleted, &file.AgentToolCallID, &created, &content, &file.Binary); err != nil {
			return result, err
		}
		file.CreatedAt, err = parseTime(created)
		if err != nil {
			return result, err
		}
		if file.SizeBytes != len(content) || file.ContentHash != sha256Hex([]byte(content)) {
			return result, domainError("PROJECT_FILE_INVALID", "Skill 草稿文件与版本校验信息不一致。")
		}
		total += len(content)
		if total > maxSkillExpandedBytes || len(result.Files) >= maxSkillPackageFiles {
			return result, domainError("SKILL_PACKAGE_EXPANDED_TOO_LARGE", "Skill 草稿超过包容量限制。")
		}
		result.Files = append(result.Files, file)
		result.contents[strings.TrimPrefix(file.Path, prefix)] = content
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	encoded, err := json.Marshal(struct {
		ProjectID, RootPath string
		Files               []ProjectFile
	}{projectID, root, result.Files})
	if err != nil {
		return result, err
	}
	result.SnapshotHash = sha256Hex(encoded)
	return result, nil
}

func (s *Store) projectSkillDraftSnapshot(ctx context.Context, projectID, root string) (projectSkillDraftSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return projectSkillDraftSnapshot{}, err
	}
	defer tx.Rollback()
	return projectSkillDraftSnapshotTx(ctx, tx, projectID, root)
}

func projectSkillDraftArchive(snapshot projectSkillDraftSnapshot) ([]byte, error) {
	if _, ok := snapshot.contents["SKILL.md"]; !ok {
		return nil, domainError("SKILL_PACKAGE_INVALID", "所选目录缺少 SKILL.md。")
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	prefix := snapshot.RootPath
	if prefix != "" {
		prefix += "/"
	}
	for _, file := range snapshot.Files {
		name := strings.TrimPrefix(file.Path, prefix)
		if err := validateSkillPackageFile(name); err != nil {
			writer.Close()
			return nil, err
		}
		// Stored entries are deterministic and cannot trip the ZIP-bomb ratio gate.
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			writer.Close()
			return nil, err
		}
		if _, err := entry.Write([]byte(snapshot.contents[name])); err != nil {
			writer.Close()
			return nil, err
		}
		if buffer.Len() > maxSkillArchiveBytes {
			writer.Close()
			return nil, domainError("SKILL_ARCHIVE_TOO_LARGE", "Skill 草稿归档超过 20 MiB 限制。")
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if buffer.Len() > maxSkillArchiveBytes {
		return nil, domainError("SKILL_ARCHIVE_TOO_LARGE", "Skill 草稿归档超过 20 MiB 限制。")
	}
	return buffer.Bytes(), nil
}

func (s *Store) inspectProjectSkillDraft(snapshot projectSkillDraftSnapshot) (ProjectSkillDraft, []byte, error) {
	result := snapshot.ProjectSkillDraft
	result.Status = "invalid"
	archive, err := projectSkillDraftArchive(snapshot)
	if err == nil {
		quarantine, createErr := os.MkdirTemp(filepath.Join(s.skillDataRoot, "quarantine"), "draft-")
		if createErr != nil {
			return result, nil, createErr
		}
		defer removeManagedSkillPath(s.skillDataRoot, quarantine)
		skill, inspectErr := prepareSkillZIP(s.registry.ProjectRoot(), quarantine, "draft.zip", bytes.NewReader(archive))
		err = inspectErr
		if err == nil {
			result.Status = "valid"
			result.CapabilityID, result.Version, result.ExecutionMode = skill.CapabilityID, skill.Version, skill.ExecutionMode
			result.Manifest = skill.Public(false)
			result.Manifest.Path = "SKILL.md"
			if result.RootPath != "" {
				result.Manifest.Path = result.RootPath + "/SKILL.md"
			}
		}
	}
	if err != nil {
		var domain *DomainError
		if !errors.As(err, &domain) {
			return result, nil, err
		}
		result.Diagnostics = append(result.Diagnostics, capability.SkillDiagnostic{Code: domain.Code, Message: domain.Message, Path: result.RootPath})
	}
	return result, archive, nil
}

func (s *Store) PreviewProjectSkillDraft(ctx context.Context, projectID, root string) (ProjectSkillDraft, error) {
	s.skillMu.Lock()
	defer s.skillMu.Unlock()
	snapshot, err := s.projectSkillDraftSnapshot(ctx, projectID, root)
	if err != nil {
		return ProjectSkillDraft{}, err
	}
	result, _, err := s.inspectProjectSkillDraft(snapshot)
	result.Installations = []ProjectSkillDraftInstallation{}
	if err == nil && result.Manifest != nil {
		rows, queryErr := s.db.QueryContext(ctx, `SELECT managed.skill_installation_id,managed.scope,managed.scope_ref,COALESCE(managed.active_version_id,''),COALESCE(v.version,'') FROM skill_installations managed
			LEFT JOIN skill_versions v ON v.skill_version_id=managed.active_version_id WHERE `+visibleManagedSkillSQL+`
			AND managed.skill_name=? AND managed.status<>'uninstalled' AND (managed.scope<>'project' OR managed.scope_ref=?) ORDER BY managed.scope`, append(skillVisibilityArgs(ctx), result.Manifest.Name, projectID)...)
		if queryErr != nil {
			return result, queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var installed ProjectSkillDraftInstallation
			if err := rows.Scan(&installed.InstallationID, &installed.Scope, &installed.ScopeRef, &installed.ActiveVersionID, &installed.Version); err != nil {
				return result, err
			}
			result.Installations = append(result.Installations, installed)
		}
		if err := rows.Err(); err != nil {
			return result, err
		}
	}
	return result, err
}

func (s *Store) ExportProjectSkillDraft(ctx context.Context, projectID, root, expectedHash string) (ProjectSkillDraft, []byte, error) {
	s.skillMu.Lock()
	defer s.skillMu.Unlock()
	snapshot, err := s.projectSkillDraftSnapshot(ctx, projectID, root)
	if err != nil {
		return ProjectSkillDraft{}, nil, err
	}
	if expectedHash == "" || snapshot.SnapshotHash != expectedHash {
		return ProjectSkillDraft{}, nil, domainError("SKILL_DRAFT_CHANGED", "Skill 草稿已变化，请重新校验后下载。")
	}
	preview, archive, err := s.inspectProjectSkillDraft(snapshot)
	if err != nil {
		return preview, nil, err
	}
	if preview.Status != "valid" {
		return preview, nil, domainError("SKILL_PACKAGE_INVALID", "Skill 草稿未通过校验，不能导出为可安装包。")
	}
	return preview, archive, nil
}

func (s *Store) InstallProjectSkillDraft(ctx context.Context, projectID string, args ProjectSkillInstallArguments, idempotencyKey string, confirmed bool) (ProjectSkillInstallResult, error) {
	principal, ok := identity.FromContext(ctx)
	if !ok || !principal.ValidUser() {
		return ProjectSkillInstallResult{}, domainError("ROLE_FORBIDDEN", "安装 Skill 需要用户确认。")
	}
	if !confirmed {
		return ProjectSkillInstallResult{}, domainError("REQUIRED_CONFIRMATION_MISSING", "安装前需要明确确认。")
	}
	if len(idempotencyKey) < 16 || len(idempotencyKey) > 128 {
		return ProjectSkillInstallResult{}, domainError("IDEMPOTENCY_KEY_REQUIRED", "安装请求缺少有效幂等键。")
	}
	guard := projectSkillInstallGuard{projectID: projectID, receiptID: "user:" + principal.UserID + ":" + projectID + ":" + idempotencyKey, arguments: args}
	return s.installProjectSkillDraft(ctx, guard)
}

func (s *Store) InstallProjectSkillDraftForTool(ctx context.Context, callID, sdkCallID string, args ProjectSkillInstallArguments) (ProjectSkillInstallResult, error) {
	principal, ok := identity.FromContext(ctx)
	if !ok || principal.Kind != identity.KindService {
		return ProjectSkillInstallResult{}, domainError("ROLE_FORBIDDEN", "Agent Skill 安装需要服务执行身份。")
	}
	if _, ok := identity.UserFromContext(ctx); !ok {
		return ProjectSkillInstallResult{}, domainError("ROLE_FORBIDDEN", "Agent Skill 安装缺少委托用户身份。")
	}
	call, err := s.GetAgentToolCall(ctx, callID)
	if err != nil {
		return ProjectSkillInstallResult{}, err
	}
	guard := projectSkillInstallGuard{projectID: call.ProjectID, receiptID: "tool:" + callID, callID: callID, sdkCallID: sdkCallID, arguments: args}
	return s.installProjectSkillDraft(ctx, guard)
}

func (s *Store) installProjectSkillDraft(ctx context.Context, guard projectSkillInstallGuard) (ProjectSkillInstallResult, error) {
	args := guard.arguments
	if args.SnapshotHash == "" || (args.InstallationID == "") != (args.ExpectedActiveVersionID == "") {
		return ProjectSkillInstallResult{}, domainError("REQUEST_VALIDATION_FAILED", "请提供校验快照；升级时还需提供安装 ID 与当前版本 ID。")
	}
	targetInput := SkillInstallTarget{Scope: args.Scope}
	if args.Scope == capability.SkillScopeProject {
		targetInput.ProjectID = guard.projectID
	}
	if args.Scope == "" {
		return ProjectSkillInstallResult{}, domainError("REQUEST_VALIDATION_FAILED", "请选择 Skill 安装范围。")
	}
	target, err := s.resolveSkillInstallTarget(ctx, []SkillInstallTarget{targetInput})
	if err != nil {
		return ProjectSkillInstallResult{}, err
	}
	raw, _ := json.Marshal(args)
	_, guard.argumentHash, _, err = summarizeAgentToolPayload(raw, true)
	if err != nil {
		return ProjectSkillInstallResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectSkillInstallResult{}, err
	}
	if err := guard.authorizeTx(ctx, s, tx, target); err != nil {
		tx.Rollback()
		return ProjectSkillInstallResult{}, err
	}
	receipt, hit, err := projectSkillInstallReceiptTx(ctx, tx, guard)
	if err != nil {
		tx.Rollback()
		return ProjectSkillInstallResult{}, err
	}
	tx.Rollback()
	if hit {
		return s.projectSkillInstallResult(ctx, receipt)
	}
	snapshot, err := s.projectSkillDraftSnapshot(ctx, guard.projectID, args.RootPath)
	if err != nil {
		return ProjectSkillInstallResult{}, err
	}
	if snapshot.SnapshotHash != args.SnapshotHash {
		return ProjectSkillInstallResult{}, domainError("SKILL_DRAFT_CHANGED", "Skill 草稿已变化，请重新校验并确认。")
	}
	archive, err := projectSkillDraftArchive(snapshot)
	if err != nil {
		return ProjectSkillInstallResult{}, err
	}
	user, _ := identity.UserFromContext(ctx)
	_, err = s.installSkillCheckedWithDraft(ctx, "zip", "project-skill-draft.zip", user.UserID, args.InstallationID, args.ExpectedActiveVersionID,
		func(quarantine string) (*capability.SkillPackage, error) {
			return prepareSkillZIP(s.registry.ProjectRoot(), quarantine, "project-skill-draft.zip", bytes.NewReader(archive))
		}, &guard, targetInput)
	installErr := err
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectSkillInstallResult{}, err
	}
	if err := guard.authorizeTx(ctx, s, tx, target); err != nil {
		tx.Rollback()
		return ProjectSkillInstallResult{}, err
	}
	receipt, hit, err = projectSkillInstallReceiptTx(ctx, tx, guard)
	tx.Rollback()
	if err != nil {
		return ProjectSkillInstallResult{}, err
	}
	if !hit {
		if installErr != nil {
			return ProjectSkillInstallResult{}, installErr
		}
		return ProjectSkillInstallResult{}, domainError("SKILL_INSTALL_RECEIPT_NOT_FOUND", "安装回执不可读取。")
	}
	return s.projectSkillInstallResult(ctx, receipt)
}

func (guard projectSkillInstallGuard) authorizeTx(ctx context.Context, s *Store, tx *sql.Tx, target managedSkillScope) error {
	workspaceID, err := projectFilesWorkspace(ctx, tx, guard.projectID, true)
	if err != nil {
		return err
	}
	if target.workspaceID != workspaceID {
		return domainError("PROJECT_NOT_FOUND", "作品不存在。")
	}
	user, ok := identity.UserFromContext(ctx)
	if !ok {
		return domainError("ROLE_FORBIDDEN", "安装缺少用户身份。")
	}
	if target.scope != guard.arguments.Scope || (target.scope == capability.SkillScopeProject && target.ref != guard.projectID) || (target.scope == capability.SkillScopeUser && target.ref != user.UserID) {
		return domainError("SKILL_INSTALLATION_STATE_CONFLICT", "目标安装范围与确认的范围不一致。")
	}
	var role identity.Role
	err = tx.QueryRowContext(ctx, `SELECT role FROM workspace_memberships WHERE workspace_id=? AND user_id=? AND status='active'`, workspaceID, user.UserID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return domainError("ROLE_FORBIDDEN", "工作区权限已撤销。")
	}
	if err != nil {
		return err
	}
	user.Role = role
	required := identity.RoleEditor
	if target.scope == capability.SkillScopeWorkspace {
		required = identity.RoleAdmin
	}
	if !user.Allows(required) {
		return domainError("ROLE_FORBIDDEN", "当前角色不能安装该范围的 Skill。")
	}
	if guard.callID == "" {
		return nil
	}
	call, err := getAgentToolCallTx(ctx, tx, guard.callID)
	if err != nil {
		return err
	}
	if call.ToolID != "runtime:install_workspace_skill" || call.SDKToolCallID != guard.sdkCallID || call.Status != "running" || call.ArgumentsHash != guard.argumentHash {
		return domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "安装与已批准的工具调用不匹配。")
	}
	approval, err := getAgentToolApprovalForCallTx(ctx, tx, guard.callID)
	if err != nil {
		return err
	}
	if approval.Status != "approved" {
		return domainError("AGENT_TOOL_APPROVAL_REQUIRED", "安装 Skill 前需要用户批准。")
	}
	if err := s.validateAgentTaskToolCallTx(ctx, tx, guard.callID); err != nil {
		return err
	}
	if call.AgentTurnID != nil {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM agent_turns WHERE agent_turn_id=?`, *call.AgentTurnID).Scan(&status); err != nil {
			return err
		}
		if status != "running" && status != "pausing" {
			return domainError("AGENT_ACTIVITY_STALE", "Agent 对话已停止，不能安装 Skill。")
		}
	}
	return nil
}

func (guard projectSkillInstallGuard) validateCommitTx(ctx context.Context, s *Store, tx *sql.Tx, target managedSkillScope, skill *capability.SkillPackage) error {
	if err := guard.authorizeTx(ctx, s, tx, target); err != nil {
		return err
	}
	if _, hit, err := projectSkillInstallReceiptTx(ctx, tx, guard); err != nil {
		return err
	} else if hit {
		return domainError("SKILL_INSTALL_ALREADY_COMMITTED", "相同安装已完成，请读取安装回执。")
	}
	snapshot, err := projectSkillDraftSnapshotTx(ctx, tx, guard.projectID, guard.arguments.RootPath)
	if err != nil {
		return err
	}
	if snapshot.SnapshotHash != guard.arguments.SnapshotHash {
		return domainError("SKILL_DRAFT_CHANGED", "获批草稿已经变化，请重新校验并确认。")
	}
	var id, version, status string
	err = tx.QueryRowContext(ctx, `SELECT skill_installation_id,COALESCE(active_version_id,''),status FROM skill_installations WHERE workspace_id=? AND scope=? AND scope_ref=? AND skill_name=?`, target.workspaceID, target.scope, target.ref, skill.Name).Scan(&id, &version, &status)
	if errors.Is(err, sql.ErrNoRows) && guard.arguments.InstallationID == "" {
		return nil
	}
	if err == nil && status == "uninstalled" && guard.arguments.InstallationID == "" {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if id == "" || id != guard.arguments.InstallationID || version != guard.arguments.ExpectedActiveVersionID {
		return domainError("SKILL_INSTALLATION_STATE_CONFLICT", "同名安装或当前版本已变化，请明确选择升级目标并重新确认。")
	}
	return nil
}

func projectSkillInstallReceiptTx(ctx context.Context, tx *sql.Tx, guard projectSkillInstallGuard) (ProjectSkillInstallReceipt, bool, error) {
	var receipt ProjectSkillInstallReceipt
	var hash, created string
	err := tx.QueryRowContext(ctx, `SELECT receipt_id,project_id,arguments_hash,skill_installation_id,skill_version_id,created_at FROM project_skill_install_receipts WHERE receipt_id=?`, guard.receiptID).Scan(&receipt.ReceiptID, &receipt.ProjectID, &hash, &receipt.InstallationID, &receipt.VersionID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return receipt, false, nil
	}
	if err != nil {
		return receipt, false, err
	}
	if hash != guard.argumentHash || receipt.ProjectID != guard.projectID {
		return receipt, false, domainError("IDEMPOTENCY_CONFLICT", "同一安装请求不能用于不同草稿或安装范围。")
	}
	receipt.CreatedAt, err = parseTime(created)
	return receipt, true, err
}

func (s *Store) projectSkillInstallResult(ctx context.Context, receipt ProjectSkillInstallReceipt) (ProjectSkillInstallResult, error) {
	installation, err := s.GetSkillInstallation(ctx, receipt.InstallationID)
	return ProjectSkillInstallResult{Receipt: receipt, Installation: installation}, err
}

// Only this execution's approved installations extend its frozen catalog. Other
// turns and sibling attempts keep their original package bindings.
func (s *Store) applyInstalledDraftsToSnapshot(ctx context.Context, query projectWorkspaceQuery, projectID string, snapshots []*capability.SkillPackage) ([]*capability.SkillPackage, error) {
	activity, present := AgentActivityFromContext(ctx)
	if !present {
		return snapshots, nil
	}
	rows, err := query.QueryContext(ctx, `SELECT receipt.skill_version_id, installation.capability_id
		FROM project_skill_install_receipts receipt
		JOIN skill_installations installation ON installation.skill_installation_id=receipt.skill_installation_id
		JOIN agent_tool_calls call ON call.agent_tool_call_id=receipt.agent_tool_call_id
		LEFT JOIN agent_task_tool_calls task_call ON task_call.agent_tool_call_id=call.agent_tool_call_id
		LEFT JOIN execution_tool_calls execution_call ON execution_call.agent_tool_call_id=call.agent_tool_call_id
		WHERE receipt.project_id=? AND ((?<>'' AND execution_call.execution_attempt_id=?) OR
		(?='' AND ?<>'' AND task_call.agent_task_attempt_id=?) OR
		(?='' AND ?='' AND ?<>'' AND call.agent_turn_id=? AND task_call.agent_task_attempt_id IS NULL AND execution_call.execution_attempt_id IS NULL))
		ORDER BY receipt.rowid`, projectID, activity.ExecutionAttemptID, activity.ExecutionAttemptID,
		activity.ExecutionAttemptID, activity.AgentTaskAttemptID, activity.AgentTaskAttemptID,
		activity.ExecutionAttemptID, activity.AgentTaskAttemptID, activity.AgentTurnID, activity.AgentTurnID)
	if err != nil {
		return nil, err
	}
	type installedDraft struct{ versionID, capabilityID string }
	var installed []installedDraft
	for rows.Next() {
		var draft installedDraft
		if err := rows.Scan(&draft.versionID, &draft.capabilityID); err != nil {
			rows.Close()
			return nil, err
		}
		installed = append(installed, draft)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, draft := range installed {
		entry, found, err := s.capabilityEntryForSkillVersionQuery(ctx, query, projectID, draft.capabilityID, draft.versionID)
		if err != nil {
			return nil, err
		}
		if !found || entry.Skill == nil {
			continue
		}
		if err := authorizePersonalSkillExecution(ctx, entry); err != nil {
			return nil, err
		}
		index := -1
		for i, previous := range snapshots {
			if previous.CapabilityID == draft.capabilityID {
				index = i
				break
			}
		}
		if index == -1 {
			snapshots = append(snapshots, entry.Skill)
		} else if snapshots[index].Priority <= entry.Skill.Priority {
			snapshots[index] = entry.Skill
		}
	}
	return snapshots, nil
}
