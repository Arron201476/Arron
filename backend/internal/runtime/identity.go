package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"content-agent/backend/internal/identity"
)

var tenantIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func actorRefFromContext(ctx context.Context) string {
	return identity.ActorRefFromContext(ctx)
}

func validWorkspaceID(value string) bool {
	return tenantIdentifierPattern.MatchString(value)
}

type WorkspaceQuota struct {
	WorkspaceID         string    `json:"workspace_id"`
	MaxProjects         int64     `json:"max_projects"`
	MaxStorageBytes     int64     `json:"max_storage_bytes"`
	MaxActiveAgentTurns int64     `json:"max_active_agent_turns"`
	MaxInstalledSkills  int64     `json:"max_installed_skills"`
	AuditRetentionDays  int       `json:"audit_retention_days"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type SecurityAuditEvent struct {
	WorkspaceID   string
	UserID        string
	PrincipalKind identity.Kind
	Action        string
	ResourceType  string
	ResourceID    string
	Outcome       string
	StatusCode    int
	RequestID     string
	RemoteAddr    string
	UserAgent     string
}

type WorkspaceMCPCredential struct {
	CredentialID    string     `json:"credential_id"`
	WorkspaceID     string     `json:"workspace_id"`
	ServerID        string     `json:"server_id"`
	CredentialName  string     `json:"credential_name"`
	SecretRef       string     `json:"secret_ref"`
	Status          string     `json:"status"`
	CreatedByUserID string     `json:"created_by_user_id"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
}

type PutWorkspaceMCPCredentialCommand struct {
	CommandMeta
	WorkspaceID    string
	UserID         string
	ServerID       string
	CredentialName string
	SecretRef      string
}

type DeleteWorkspaceMCPCredentialCommand struct {
	CommandMeta
	WorkspaceID  string
	CredentialID string
}

type WorkspaceMCPCredentialDeleteResult struct {
	CredentialID string    `json:"credential_id"`
	DeletedAt    time.Time `json:"deleted_at"`
}

func (s *Store) BootstrapPrincipal(ctx context.Context, principal identity.Principal) error {
	if !principal.ValidUser() {
		return domainError("IDENTITY_INVALID", "用户身份配置无效。")
	}
	if !validWorkspaceID(principal.WorkspaceID) || !tenantIdentifierPattern.MatchString(principal.UserID) {
		return domainError("IDENTITY_INVALID", "用户或工作区标识无效。")
	}
	if strings.TrimSpace(principal.DisplayName) == "" {
		principal.DisplayName = principal.UserID
	}
	if strings.TrimSpace(principal.WorkspaceName) == "" {
		principal.WorkspaceName = principal.WorkspaceID
	}
	now := formatTime(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO users(user_id, display_name, status, created_at, updated_at)
		VALUES(?, ?, 'active', ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			display_name = excluded.display_name,
			status = CASE WHEN users.status = 'deleted' THEN users.status ELSE 'active' END,
			updated_at = excluded.updated_at`,
		principal.UserID, principal.DisplayName, now, now,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workspaces(workspace_id, name, status, created_at, updated_at)
		VALUES(?, ?, 'active', ?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET
			name = excluded.name,
			status = CASE WHEN workspaces.status = 'deleted' THEN workspaces.status ELSE 'active' END,
			updated_at = excluded.updated_at`,
		principal.WorkspaceID, principal.WorkspaceName, now, now,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workspace_memberships(
			workspace_id, user_id, role, status, created_at, updated_at
		) VALUES(?, ?, ?, 'active', ?, ?)
		ON CONFLICT(workspace_id, user_id) DO UPDATE SET
			role = excluded.role, status = 'active', updated_at = excluded.updated_at`,
		principal.WorkspaceID, principal.UserID, principal.Role, now, now,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO workspace_quotas(workspace_id, updated_at) VALUES(?, ?)`,
		principal.WorkspaceID, now,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.refreshWorkspaceRegistry(ctx, principal.WorkspaceID)
}

func (s *Store) ResolvePrincipal(ctx context.Context, claimed identity.Principal) (identity.Principal, error) {
	return resolvePrincipalQuery(ctx, s.db, claimed)
}

func resolvePrincipalQuery(ctx context.Context, query rowQueryer, claimed identity.Principal) (identity.Principal, error) {
	if claimed.Kind == identity.KindService || claimed.Kind == identity.KindSystem {
		return claimed, nil
	}
	if !claimed.ValidUser() {
		return identity.Principal{}, domainError("AUTHENTICATION_REQUIRED", "需要登录后才能访问。")
	}
	var resolved identity.Principal
	resolved.Kind = identity.KindUser
	resolved.UserID = claimed.UserID
	resolved.WorkspaceID = claimed.WorkspaceID
	resolved.AuthMethod = claimed.AuthMethod
	var role string
	err := query.QueryRowContext(ctx, `
		SELECT u.display_name, w.name, wm.role
		FROM workspace_memberships wm
		JOIN users u ON u.user_id = wm.user_id
		JOIN workspaces w ON w.workspace_id = wm.workspace_id
		WHERE wm.workspace_id = ? AND wm.user_id = ?
		  AND wm.status = 'active' AND u.status = 'active' AND w.status = 'active'
		  AND u.deleted_at IS NULL AND w.deleted_at IS NULL`,
		claimed.WorkspaceID, claimed.UserID,
	).Scan(&resolved.DisplayName, &resolved.WorkspaceName, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Principal{}, domainError("WORKSPACE_ACCESS_DENIED", "当前用户无权访问该工作区。")
	}
	if err != nil {
		return identity.Principal{}, err
	}
	resolved.Role = identity.Role(role)
	if !identity.ValidRole(resolved.Role) {
		return identity.Principal{}, domainError("IDENTITY_INVALID", "工作区角色配置无效。")
	}
	return resolved, nil
}

func (s *Store) GetWorkspaceQuota(ctx context.Context, workspaceID string) (WorkspaceQuota, error) {
	var quota WorkspaceQuota
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT workspace_id, max_projects, max_storage_bytes, max_active_agent_turns,
		       max_installed_skills, audit_retention_days, updated_at
		FROM workspace_quotas WHERE workspace_id = ?`, workspaceID).Scan(
		&quota.WorkspaceID, &quota.MaxProjects, &quota.MaxStorageBytes,
		&quota.MaxActiveAgentTurns, &quota.MaxInstalledSkills,
		&quota.AuditRetentionDays, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceQuota{}, domainError("WORKSPACE_NOT_FOUND", "工作区不存在。")
	}
	if err != nil {
		return WorkspaceQuota{}, err
	}
	quota.UpdatedAt, err = parseTime(updatedAt)
	return quota, err
}

func (s *Store) enforceWorkspaceQuotaTx(
	ctx context.Context, tx *sql.Tx, workspaceID, resource string, delta int64,
) error {
	var limit int64
	var countQuery string
	switch resource {
	case "projects":
		countQuery = `SELECT COUNT(*) FROM projects WHERE workspace_id = ? AND deleted_at IS NULL`
		if err := tx.QueryRowContext(ctx, `SELECT max_projects FROM workspace_quotas WHERE workspace_id = ?`, workspaceID).Scan(&limit); err != nil {
			return err
		}
	case "active_agent_turns":
		countQuery = `SELECT COUNT(*) FROM agent_turns WHERE workspace_id = ? AND status IN ('accepted','running','waiting_approval','pausing','paused','cancel_requested','committing')`
		if err := tx.QueryRowContext(ctx, `SELECT max_active_agent_turns FROM workspace_quotas WHERE workspace_id = ?`, workspaceID).Scan(&limit); err != nil {
			return err
		}
	case "installed_skills":
		countQuery = `SELECT COUNT(*) FROM skill_installations WHERE workspace_id = ? AND status != 'uninstalled'`
		if err := tx.QueryRowContext(ctx, `SELECT max_installed_skills FROM workspace_quotas WHERE workspace_id = ?`, workspaceID).Scan(&limit); err != nil {
			return err
		}
	case "storage_bytes":
		countQuery = `
			WITH quota_workspace AS (SELECT ? AS workspace_id), workspace_projects AS (
				SELECT project_id FROM projects WHERE workspace_id = (SELECT workspace_id FROM quota_workspace) AND deleted_at IS NULL
			)
			SELECT
				COALESCE((
					SELECT SUM(a.size_bytes) FROM assets a
					WHERE a.project_id IN (SELECT project_id FROM workspace_projects)
					  AND a.deleted_at IS NULL
				), 0) +
				COALESCE((
					SELECT SUM(ui.declared_size_bytes)
					FROM upload_items ui
					JOIN upload_sessions us ON us.upload_session_id = ui.upload_session_id
					WHERE us.project_id IN (SELECT project_id FROM workspace_projects)
					  AND ui.status IN ('pending','validating')
				), 0) +
				COALESCE((SELECT SUM(size_bytes) FROM project_file_versions
					WHERE project_id IN (SELECT project_id FROM workspace_projects)), 0) +
				COALESCE((SELECT SUM(length(snapshot.archive)+length(CAST(snapshot.entries_json AS BLOB))) FROM native_workspace_snapshots snapshot
					JOIN native_workspace_leases lease ON lease.session_id=snapshot.session_id
					WHERE lease.workspace_id = (SELECT workspace_id FROM quota_workspace)), 0) +
				COALESCE((SELECT SUM(length(command.stdout)+length(command.stderr)+command.reserved_bytes) FROM native_workspace_commands command
					JOIN native_workspace_leases lease ON lease.session_id=command.session_id
					WHERE lease.workspace_id = (SELECT workspace_id FROM quota_workspace)), 0) +
				COALESCE((SELECT SUM(length(CAST(operation.result_json AS BLOB))+operation.reserved_bytes) FROM native_workspace_pty_operations operation
					JOIN native_workspace_leases lease ON lease.session_id=operation.session_id
					WHERE lease.workspace_id = (SELECT workspace_id FROM quota_workspace)), 0) +
				COALESCE((SELECT SUM(length(CAST(operation.result_json AS BLOB))+operation.reserved_bytes) FROM native_workspace_file_operations operation
					JOIN native_workspace_leases lease ON lease.session_id=operation.session_id
					WHERE lease.workspace_id = (SELECT workspace_id FROM quota_workspace)), 0) +
				COALESCE((SELECT SUM(262144) FROM native_workspace_manifests manifest
					JOIN native_workspace_leases lease ON lease.session_id=manifest.session_id
					WHERE manifest.status!='deleted' AND lease.workspace_id = (SELECT workspace_id FROM quota_workspace)), 0) +
				COALESCE((SELECT SUM(length(CAST(content AS BLOB))) FROM agent_instruction_versions
					WHERE workspace_id = (SELECT workspace_id FROM quota_workspace)), 0) +
				COALESCE((SELECT SUM(length(CAST(files_json AS BLOB))) FROM agent_memory_versions
					WHERE workspace_id = (SELECT workspace_id FROM quota_workspace)), 0) +
				COALESCE((SELECT COUNT(*) * 512 FROM agent_memory_preferences
					WHERE workspace_id = (SELECT workspace_id FROM quota_workspace)), 0) +
				COALESCE((SELECT SUM(2048 + LENGTH(CAST(arguments_json AS BLOB))) FROM agent_memory_publications
					WHERE workspace_id = (SELECT workspace_id FROM quota_workspace)), 0) +
				COALESCE((SELECT SUM(1024 + LENGTH(CAST(envelope_json AS BLOB))) FROM agent_memory_rollouts
					WHERE workspace_id = (SELECT workspace_id FROM quota_workspace)), 0) +
				COALESCE((SELECT SUM(2048 + LENGTH(CAST(checkpoint_json AS BLOB)) + LENGTH(CAST(extraction_json AS BLOB)) + LENGTH(CAST(extraction_receipt AS BLOB)) + LENGTH(CAST(input_plan_json AS BLOB)) + LENGTH(CAST(input_plan_hash AS BLOB))) FROM agent_memory_generations
					WHERE workspace_id = (SELECT workspace_id FROM quota_workspace)), 0) +
				COALESCE((SELECT SUM(512 + LENGTH(CAST(b.arguments_json AS BLOB))) FROM agent_memory_tool_calls b JOIN agent_memory_generations g ON g.generation_id=b.generation_id
					WHERE g.workspace_id = (SELECT workspace_id FROM quota_workspace)), 0) +
				COALESCE((SELECT SUM(length(CAST(references_json AS BLOB))) FROM agent_instruction_snapshots
					WHERE workspace_id = (SELECT workspace_id FROM quota_workspace)), 0) +
				COALESCE((SELECT SUM(length(CAST(arguments_json AS BLOB))) FROM agent_instruction_proposals
					WHERE workspace_id = (SELECT workspace_id FROM quota_workspace)), 0) +
				COALESCE((SELECT SUM(length(CAST(result_json AS BLOB))) FROM agent_subtask_results
					WHERE project_id IN (SELECT project_id FROM workspace_projects)), 0) +
				COALESCE((SELECT SUM(length(CAST(resolution_json AS BLOB))) FROM agent_tool_reconciliations
					WHERE project_id IN (SELECT project_id FROM workspace_projects)), 0) +
				COALESCE((SELECT SUM(i.content_size_bytes) FROM agent_tool_reconciliation_inputs i
					JOIN agent_tool_reconciliations r ON r.agent_tool_call_id=i.agent_tool_call_id
					WHERE r.project_id IN (SELECT project_id FROM workspace_projects)), 0)`
		if err := tx.QueryRowContext(ctx, `SELECT max_storage_bytes FROM workspace_quotas WHERE workspace_id = ?`, workspaceID).Scan(&limit); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported quota resource %q", resource)
	}
	var current int64
	if err := tx.QueryRowContext(ctx, countQuery, workspaceID).Scan(&current); err != nil {
		return err
	}
	if limit >= 0 && current+delta > limit {
		return domainError("WORKSPACE_QUOTA_EXCEEDED", "工作区配额不足，无法完成本次操作。")
	}
	return nil
}

func (s *Store) CheckWorkspaceStorageQuota(ctx context.Context, workspaceID string, additionalBytes int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return s.enforceWorkspaceQuotaTx(ctx, tx, workspaceID, "storage_bytes", additionalBytes)
}

func (s *Store) RecordSecurityAudit(ctx context.Context, event SecurityAuditEvent) error {
	if strings.TrimSpace(event.RequestID) == "" {
		event.RequestID = s.newID("req")
	}
	if strings.TrimSpace(event.Outcome) == "" {
		event.Outcome = "unknown"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO security_audit_events(
			audit_event_id, workspace_id, user_id, principal_kind, action,
			resource_type, resource_id, outcome, status_code, request_id,
			remote_addr, user_agent, created_at
		) VALUES(?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?)`,
		s.newID("audit"), event.WorkspaceID, event.UserID, event.PrincipalKind,
		event.Action, event.ResourceType, event.ResourceID, event.Outcome,
		event.StatusCode, event.RequestID, event.RemoteAddr, event.UserAgent,
		formatTime(s.now()),
	)
	return err
}

func (s *Store) PruneSecurityAuditEvents(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM security_audit_events
		WHERE workspace_id IS NOT NULL AND created_at < (
			SELECT datetime('now', '-' || q.audit_retention_days || ' days')
			FROM workspace_quotas q WHERE q.workspace_id = security_audit_events.workspace_id
		)`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ResolveResourceWorkspace(ctx context.Context, resourceType, resourceID string) (string, error) {
	query := ""
	switch resourceType {
	case "project":
		// Keep deleted projects resolvable so the project deletion API can remain
		// idempotent without weakening the tenant boundary.
		query = `SELECT workspace_id FROM projects WHERE project_id = ?`
	case "conversation":
		query = `SELECT p.workspace_id FROM conversations x JOIN projects p ON p.project_id = x.project_id WHERE x.conversation_id = ? AND p.deleted_at IS NULL`
	case "agent_turn":
		query = `SELECT workspace_id FROM agent_turns WHERE agent_turn_id = ?`
	case "skill":
		query = `SELECT workspace_id FROM skill_installations WHERE skill_installation_id = ?`
	case "upload_session":
		query = `SELECT p.workspace_id FROM upload_sessions x JOIN projects p ON p.project_id = x.project_id WHERE x.upload_session_id = ? AND p.deleted_at IS NULL`
	case "upload_item":
		query = `SELECT p.workspace_id FROM upload_items x JOIN upload_sessions u ON u.upload_session_id = x.upload_session_id JOIN projects p ON p.project_id = u.project_id WHERE x.upload_item_id = ? AND p.deleted_at IS NULL`
	case "asset":
		query = `SELECT p.workspace_id FROM assets x JOIN projects p ON p.project_id = x.project_id WHERE x.asset_id = ? AND p.deleted_at IS NULL`
	case "asset_set":
		query = `SELECT p.workspace_id FROM asset_sets x JOIN projects p ON p.project_id = x.project_id WHERE x.asset_set_id = ? AND p.deleted_at IS NULL`
	case "asset_set_version":
		query = `SELECT p.workspace_id FROM asset_set_versions x JOIN asset_sets a ON a.asset_set_id = x.asset_set_id JOIN projects p ON p.project_id = a.project_id WHERE x.asset_set_version_id = ? AND p.deleted_at IS NULL`
	case "run":
		query = `SELECT p.workspace_id FROM runs x JOIN projects p ON p.project_id = x.project_id WHERE x.run_id = ? AND p.deleted_at IS NULL`
	case "step":
		query = `SELECT p.workspace_id FROM step_runs x JOIN runs r ON r.run_id = x.run_id JOIN projects p ON p.project_id = r.project_id WHERE x.step_run_id = ? AND p.deleted_at IS NULL`
	case "artifact":
		query = `SELECT p.workspace_id FROM artifacts x JOIN projects p ON p.project_id = x.project_id WHERE x.artifact_id = ? AND p.deleted_at IS NULL`
	case "artifact_version":
		query = `SELECT p.workspace_id FROM artifact_versions x JOIN artifacts a ON a.artifact_id = x.artifact_id JOIN projects p ON p.project_id = a.project_id WHERE x.artifact_version_id = ? AND p.deleted_at IS NULL`
	case "approval":
		query = `SELECT p.workspace_id FROM approvals x JOIN projects p ON p.project_id = x.project_id WHERE x.approval_request_id = ? AND p.deleted_at IS NULL`
	case "quality_review":
		query = `SELECT p.workspace_id FROM quality_reviews x JOIN projects p ON p.project_id = x.project_id WHERE x.quality_review_id = ? AND p.deleted_at IS NULL`
	case "revision_request":
		query = `SELECT p.workspace_id FROM revision_requests x JOIN projects p ON p.project_id = x.project_id WHERE x.revision_request_id = ? AND p.deleted_at IS NULL`
	case "target_resolution":
		query = `SELECT p.workspace_id FROM target_resolutions x JOIN projects p ON p.project_id = x.project_id WHERE x.target_resolution_id = ? AND p.deleted_at IS NULL`
	case "proposed_action":
		query = `SELECT p.workspace_id FROM proposed_actions x JOIN projects p ON p.project_id = x.project_id WHERE x.proposed_action_id = ? AND p.deleted_at IS NULL`
	case "agent_task":
		query = `SELECT workspace_id FROM agent_tasks WHERE agent_task_id = ?`
	case "agent_tool_call":
		query = `SELECT workspace_id FROM agent_tool_calls WHERE agent_tool_call_id = ?`
	case "agent_tool_approval":
		query = `SELECT workspace_id FROM agent_tool_approvals WHERE agent_tool_approval_id = ?`
	case "script_candidate":
		query = `SELECT p.workspace_id FROM script_candidates x JOIN projects p ON p.project_id = x.project_id WHERE x.candidate_id = ? AND p.deleted_at IS NULL`
	case "export":
		query = `SELECT p.workspace_id FROM exports x JOIN projects p ON p.project_id = x.project_id WHERE x.export_id = ? AND p.deleted_at IS NULL`
	case "impact_review":
		query = `SELECT p.workspace_id FROM impact_reviews x JOIN runs r ON r.run_id = x.run_id JOIN projects p ON p.project_id = r.project_id WHERE x.impact_review_id = ? AND p.deleted_at IS NULL`
	case "regeneration_plan":
		query = `SELECT p.workspace_id FROM regeneration_plans x JOIN runs r ON r.run_id = x.run_id JOIN projects p ON p.project_id = r.project_id WHERE x.regeneration_plan_id = ? AND p.deleted_at IS NULL`
	case "mcp_credential":
		query = `SELECT workspace_id FROM workspace_mcp_credentials WHERE credential_id = ? AND deleted_at IS NULL`
	default:
		return "", fmt.Errorf("unsupported resource type %q", resourceType)
	}
	var workspaceID string
	if err := s.db.QueryRowContext(ctx, query, resourceID).Scan(&workspaceID); errors.Is(err, sql.ErrNoRows) {
		return "", domainError("RESOURCE_NOT_FOUND", "请求的资源不存在。")
	} else if err != nil {
		return "", err
	}
	return workspaceID, nil
}

func (s *Store) ListWorkspaceMCPCredentials(ctx context.Context, workspaceID string) ([]WorkspaceMCPCredential, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT credential_id, workspace_id, server_id, credential_name, secret_ref,
		       status, created_by_user_id, created_at, updated_at, deleted_at
		FROM workspace_mcp_credentials
		WHERE workspace_id = ? AND deleted_at IS NULL
		ORDER BY server_id, credential_name, credential_id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]WorkspaceMCPCredential, 0)
	for rows.Next() {
		item, err := scanWorkspaceMCPCredential(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) PutWorkspaceMCPCredential(
	ctx context.Context, workspaceID, userID, serverID, name, secretRef string,
) (WorkspaceMCPCredential, error) {
	return s.PutWorkspaceMCPCredentialCommand(ctx, PutWorkspaceMCPCredentialCommand{
		WorkspaceID: workspaceID, UserID: userID, ServerID: serverID,
		CredentialName: name, SecretRef: secretRef,
	})
}

func (s *Store) PutWorkspaceMCPCredentialCommand(
	ctx context.Context, command PutWorkspaceMCPCredentialCommand,
) (WorkspaceMCPCredential, error) {
	command.WorkspaceID = strings.TrimSpace(command.WorkspaceID)
	command.UserID = strings.TrimSpace(command.UserID)
	command.ServerID = strings.TrimSpace(command.ServerID)
	command.CredentialName = strings.TrimSpace(command.CredentialName)
	command.SecretRef = strings.TrimSpace(command.SecretRef)
	serverID := command.ServerID
	name := command.CredentialName
	secretRef := command.SecretRef
	if serverID == "" || name == "" || secretRef == "" || len(secretRef) > 512 ||
		strings.ContainsAny(secretRef, "\r\n") {
		return WorkspaceMCPCredential{}, domainError("REQUEST_VALIDATION_FAILED", "MCP 凭据引用无效。")
	}
	if !strings.HasPrefix(secretRef, "env://") && !strings.HasPrefix(secretRef, "vault://") {
		return WorkspaceMCPCredential{}, domainError("MCP_SECRET_REF_REQUIRED", "MCP 凭据只能保存 env:// 或 vault:// 密钥引用。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkspaceMCPCredential{}, err
	}
	defer tx.Rollback()
	if command.Scope == "" {
		command.Scope = command.WorkspaceID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return WorkspaceMCPCredential{}, err
	}
	if hit {
		return decodeIdempotentResult[WorkspaceMCPCredential](cached)
	}
	var membership int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM workspace_memberships
		WHERE workspace_id = ? AND user_id = ? AND status = 'active'`,
		command.WorkspaceID, command.UserID,
	).Scan(&membership); err != nil {
		return WorkspaceMCPCredential{}, err
	}
	if membership != 1 {
		return WorkspaceMCPCredential{}, domainError("WORKSPACE_ACCESS_DENIED", "当前用户无权访问该工作区。")
	}
	credentialID := s.newID("mcpcred")
	_, err = tx.ExecContext(ctx, `
		INSERT INTO workspace_mcp_credentials(
			credential_id, workspace_id, server_id, credential_name, secret_ref,
			status, created_by_user_id, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, 'active', ?, ?, ?)
		ON CONFLICT(workspace_id, server_id, credential_name) DO UPDATE SET
			secret_ref = excluded.secret_ref, status = 'active',
			created_by_user_id = excluded.created_by_user_id,
			updated_at = excluded.updated_at, deleted_at = NULL`,
		credentialID, command.WorkspaceID, serverID, name, secretRef, command.UserID,
		formatTime(now), formatTime(now),
	)
	if err != nil {
		return WorkspaceMCPCredential{}, err
	}
	item, err := scanWorkspaceMCPCredential(tx.QueryRowContext(ctx, `
		SELECT credential_id, workspace_id, server_id, credential_name, secret_ref,
		       status, created_by_user_id, created_at, updated_at, deleted_at
		FROM workspace_mcp_credentials
		WHERE workspace_id = ? AND server_id = ? AND credential_name = ? AND deleted_at IS NULL`,
		command.WorkspaceID, serverID, name,
	))
	if err != nil {
		return WorkspaceMCPCredential{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, item, now); err != nil {
		return WorkspaceMCPCredential{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkspaceMCPCredential{}, err
	}
	return item, nil
}

func (s *Store) DeleteWorkspaceMCPCredential(ctx context.Context, workspaceID, credentialID string) error {
	_, err := s.DeleteWorkspaceMCPCredentialCommand(ctx, DeleteWorkspaceMCPCredentialCommand{
		WorkspaceID: workspaceID, CredentialID: credentialID,
	})
	return err
}

func (s *Store) DeleteWorkspaceMCPCredentialCommand(
	ctx context.Context, command DeleteWorkspaceMCPCredentialCommand,
) (WorkspaceMCPCredentialDeleteResult, error) {
	if command.Scope == "" {
		command.Scope = command.WorkspaceID
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkspaceMCPCredentialDeleteResult{}, err
	}
	defer tx.Rollback()
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return WorkspaceMCPCredentialDeleteResult{}, err
	}
	if hit {
		return decodeIdempotentResult[WorkspaceMCPCredentialDeleteResult](cached)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE workspace_mcp_credentials
		SET status = 'deleted', deleted_at = ?, updated_at = ?
		WHERE credential_id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		formatTime(now), formatTime(now), command.CredentialID, command.WorkspaceID,
	)
	if err != nil {
		return WorkspaceMCPCredentialDeleteResult{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return WorkspaceMCPCredentialDeleteResult{}, domainError("MCP_CREDENTIAL_NOT_FOUND", "MCP 凭据不存在。")
	}
	deleted := WorkspaceMCPCredentialDeleteResult{CredentialID: command.CredentialID, DeletedAt: now}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, deleted, now); err != nil {
		return WorkspaceMCPCredentialDeleteResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkspaceMCPCredentialDeleteResult{}, err
	}
	return deleted, nil
}

func scanWorkspaceMCPCredential(row rowScanner) (WorkspaceMCPCredential, error) {
	var item WorkspaceMCPCredential
	var createdAt, updatedAt string
	var deletedAt sql.NullString
	if err := row.Scan(
		&item.CredentialID, &item.WorkspaceID, &item.ServerID, &item.CredentialName,
		&item.SecretRef, &item.Status, &item.CreatedByUserID,
		&createdAt, &updatedAt, &deletedAt,
	); err != nil {
		return WorkspaceMCPCredential{}, err
	}
	var err error
	item.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return WorkspaceMCPCredential{}, err
	}
	item.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return WorkspaceMCPCredential{}, err
	}
	item.DeletedAt, err = optionalTime(deletedAt)
	return item, err
}
