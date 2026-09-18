package runtime

import (
	"context"
	"database/sql"
	"errors"
	"os"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

type projectWorkspaceQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) CapabilityRegistryForWorkspace(
	ctx context.Context, workspaceID string,
) (*capability.Registry, error) {
	if !validWorkspaceID(workspaceID) {
		return nil, domainError("WORKSPACE_NOT_FOUND", "工作区不存在。")
	}
	s.registryMu.RLock()
	registry := s.workspaceRegistries[workspaceID]
	s.registryMu.RUnlock()
	if registry != nil {
		return registry, nil
	}
	s.registryMu.Lock()
	defer s.registryMu.Unlock()
	if registry = s.workspaceRegistries[workspaceID]; registry != nil {
		return registry, nil
	}
	registry, err := s.buildWorkspaceRegistry(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	s.workspaceRegistries[workspaceID] = registry
	return registry, nil
}

func (s *Store) CapabilityRegistryForProject(
	ctx context.Context, projectID string,
) (*capability.Registry, error) {
	var workspaceID string
	err := s.db.QueryRowContext(ctx, `
		SELECT workspace_id FROM projects WHERE project_id = ? AND deleted_at IS NULL`,
		projectID,
	).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domainError("PROJECT_NOT_FOUND", "作品不存在。")
	}
	if err != nil {
		return nil, err
	}
	return s.buildSelectionRegistry(ctx, s.db, workspaceID, projectID)
}

// CapabilityDefinitionForProject resolves the exact requested capability
// version before projecting its public schemas. A proposal selects its frozen
// execution package; without one an empty version selects the active package.
func (s *Store) CapabilityDefinitionForProject(
	ctx context.Context, projectID, capabilityID, version, proposedActionID string,
) (capability.PublicDefinition, bool, error) {
	if proposedActionID != "" {
		return s.proposedActionCapabilityDefinition(ctx, projectID, capabilityID, version, proposedActionID)
	}
	registry, err := s.CapabilityRegistryForProject(ctx, projectID)
	if err != nil {
		return capability.PublicDefinition{}, false, err
	}
	if version == "" {
		return registry.PublicDefinitionWithSchemas(capabilityID)
	}
	entry, ok, err := s.capabilityEntryForProjectVersion(
		ctx, projectID, capabilityID, version,
	)
	if err != nil || !ok {
		return capability.PublicDefinition{}, ok, err
	}
	definition, err := registry.PublicDefinitionForEntryWithSchemas(entry)
	if err != nil {
		return capability.PublicDefinition{}, true, err
	}
	return definition, true, nil
}

func (s *Store) proposedActionCapabilityDefinition(ctx context.Context, projectID, capabilityID, version, actionID string) (capability.PublicDefinition, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return capability.PublicDefinition{}, false, err
	}
	defer tx.Rollback()
	action, err := loadProposedActionTx(ctx, tx, actionID)
	if err != nil {
		return capability.PublicDefinition{}, false, err
	}
	if action.ProjectID != projectID || action.CapabilityRef == nil || action.CapabilityRef.CapabilityID != capabilityID || action.CapabilityRef.Version != version {
		return capability.PublicDefinition{}, false, domainError("PROPOSED_ACTION_NOT_FOUND", "待确认动作不存在。")
	}
	var workspaceID string
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM projects WHERE project_id=? AND deleted_at IS NULL`, projectID).Scan(&workspaceID); err != nil {
		return capability.PublicDefinition{}, false, err
	}
	if principal, ok := identity.UserFromContext(ctx); ok {
		if principal.WorkspaceID != workspaceID {
			return capability.PublicDefinition{}, false, domainError("PROPOSED_ACTION_NOT_FOUND", "待确认动作不存在。")
		}
		if _, err := resolvePrincipalQuery(ctx, tx, principal); err != nil {
			return capability.PublicDefinition{}, false, err
		}
	}
	entry, ok, err := s.capabilityEntryForProposedActionQuery(ctx, tx, projectID, actionID, capabilityID, version)
	if err != nil || !ok {
		return capability.PublicDefinition{}, false, err
	}
	registry, err := s.buildSelectionRegistry(ctx, tx, workspaceID, projectID)
	if err != nil {
		return capability.PublicDefinition{}, false, err
	}
	definition, err := registry.PublicDefinitionForEntryWithSchemas(entry)
	return definition, true, err
}

func (s *Store) buildWorkspaceRegistry(
	ctx context.Context, workspaceID string,
) (*capability.Registry, error) {
	activeRoot := s.managedSkillActiveRoot(workspaceID)
	if err := os.MkdirAll(activeRoot, 0o755); err != nil {
		return nil, err
	}
	selection := selectedSkillScope(ctx, workspaceID, "")
	registry, err := s.registry.ForkForSelection(func(root capability.SkillRoot) bool {
		return (root.Scope == capability.SkillScopeSystem || root.Scope == capability.SkillScopeWorkspace) && selection.includes(root)
	}, nil)
	if err != nil {
		return nil, err
	}
	if err := registry.AddSkillRoot(capability.SkillRoot{Scope: capability.SkillScopeWorkspace, WorkspaceID: workspaceID, ScopeRef: workspaceID, Path: activeRoot, Priority: managedSkillRootPriority}); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT si.capability_id, COALESCE(sv.version, ''), COALESCE(sv.content_hash, ''), si.enabled, si.status
		FROM skill_installations si
		LEFT JOIN skill_versions sv ON sv.skill_version_id = si.active_version_id
		WHERE si.workspace_id = ? AND si.scope = 'workspace' AND si.scope_ref = ?
		ORDER BY si.skill_installation_id`, workspaceID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var capabilityID, version, contentHash, status string
		var enabled bool
		if err := rows.Scan(&capabilityID, &version, &contentHash, &enabled, &status); err != nil {
			return nil, err
		}
		registry.ClearSkillState(capabilityID)
		if status == "uninstalled" {
			_ = registry.SetSkillEnabled(capabilityID, false)
			continue
		}
		_ = registry.PinSkillSnapshot(capabilityID, version, contentHash)
		if !enabled {
			_ = registry.SetSkillEnabled(capabilityID, false)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return registry, nil
}

func (s *Store) invalidateWorkspaceRegistry(workspaceID string) {
	s.registryMu.Lock()
	delete(s.workspaceRegistries, workspaceID)
	s.registryMu.Unlock()
}

func (s *Store) refreshWorkspaceRegistry(ctx context.Context, workspaceID string) error {
	s.invalidateWorkspaceRegistry(workspaceID)
	_, err := s.CapabilityRegistryForWorkspace(ctx, workspaceID)
	return err
}

func (s *Store) capabilityEntryForProject(
	ctx context.Context, projectID, capabilityID string,
) (capability.Entry, bool, error) {
	registry, err := s.CapabilityRegistryForProject(ctx, projectID)
	if err != nil {
		return capability.Entry{}, false, err
	}
	entry, ok := registry.Get(capabilityID)
	return entry, ok, nil
}

func (s *Store) capabilityEntryForProjectVersion(
	ctx context.Context, projectID, capabilityID, version string,
) (capability.Entry, bool, error) {
	if _, err := s.CapabilityRegistryForProject(ctx, projectID); err != nil {
		return capability.Entry{}, false, err
	}
	return s.capabilityEntryForProjectVersionQuery(
		ctx, s.db, projectID, capabilityID, version,
	)
}

func (s *Store) capabilityEntryForProjectQuery(
	ctx context.Context, query projectWorkspaceQuery, projectID, capabilityID string,
) (capability.Entry, bool, error) {
	var workspaceID string
	err := query.QueryRowContext(ctx, `
		SELECT workspace_id FROM projects WHERE project_id = ? AND deleted_at IS NULL`,
		projectID,
	).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return capability.Entry{}, false, domainError("PROJECT_NOT_FOUND", "作品不存在。")
	}
	if err != nil {
		return capability.Entry{}, false, err
	}
	registry, err := s.buildSelectionRegistry(ctx, query, workspaceID, projectID)
	if err != nil {
		return capability.Entry{}, false, err
	}
	entry, ok := registry.Get(capabilityID)
	return entry, ok, nil
}

func (s *Store) capabilityEntryForProjectVersionQuery(
	ctx context.Context,
	query projectWorkspaceQuery,
	projectID, capabilityID, version string,
) (capability.Entry, bool, error) {
	if activity, ok := AgentActivityFromContext(ctx); ok && activity.ExecutionAttemptID != "" {
		var versionID, snapshotID string
		err := query.QueryRowContext(ctx, `SELECT COALESCE(run.skill_version_id, ''), COALESCE(run.skill_snapshot_id, '')
			FROM execution_attempts attempt JOIN runs run ON run.run_id = attempt.run_id
			WHERE attempt.attempt_id = ? AND run.project_id = ? AND run.capability_id = ? AND run.capability_version = ?`,
			activity.ExecutionAttemptID, projectID, capabilityID, version).Scan(&versionID, &snapshotID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return capability.Entry{}, false, err
		}
		if snapshotID != "" {
			return s.capabilityEntryForExecutionSnapshotQuery(ctx, query, projectID, capabilityID, snapshotID)
		}
		if versionID != "" {
			return s.capabilityEntryForSkillVersionQuery(ctx, query, projectID, capabilityID, versionID)
		}
	}
	if activity, ok := AgentActivityFromContext(ctx); ok && activity.AgentTaskAttemptID != "" {
		var versionID, snapshotID string
		err := query.QueryRowContext(ctx, `SELECT COALESCE(invocation.skill_version_id, ''), COALESCE(invocation.skill_snapshot_id, '')
			FROM agent_task_attempts attempt JOIN agent_tasks task ON task.agent_task_id = attempt.agent_task_id
			JOIN skill_invocations invocation ON invocation.skill_invocation_id = task.skill_invocation_id
			WHERE attempt.agent_task_attempt_id = ? AND invocation.project_id = ? AND invocation.capability_id = ? AND invocation.capability_version = ?`,
			activity.AgentTaskAttemptID, projectID, capabilityID, version).Scan(&versionID, &snapshotID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return capability.Entry{}, false, err
		}
		if snapshotID != "" {
			return s.capabilityEntryForExecutionSnapshotQuery(ctx, query, projectID, capabilityID, snapshotID)
		}
		if versionID != "" {
			return s.capabilityEntryForSkillVersionQuery(ctx, query, projectID, capabilityID, versionID)
		}
	}
	var workspaceID string
	err := query.QueryRowContext(ctx, `
		SELECT workspace_id FROM projects WHERE project_id = ? AND deleted_at IS NULL`,
		projectID,
	).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return capability.Entry{}, false, domainError("PROJECT_NOT_FOUND", "作品不存在。")
	}
	if err != nil {
		return capability.Entry{}, false, err
	}
	registry, err := s.buildSelectionRegistry(ctx, query, workspaceID, projectID)
	if err != nil {
		return capability.Entry{}, false, err
	}
	selected, selectedOK := registry.Get(capabilityID)
	if selectedOK && selected.Definition != nil && (selected.Definition.Version == version || selected.Status != capability.Available) {
		return selected, true, nil
	}
	if !selectedOK || selected.Skill == nil {
		return capability.Entry{}, false, nil
	}
	if selected.Skill.ExecutionSnapshotID != "" {
		return capability.Entry{}, false, nil
	}
	selectedRef := selected.Skill.ScopeRef
	if selectedRef == "" && selected.Skill.Scope == capability.SkillScopeWorkspace {
		selectedRef = workspaceID
	}
	if selectedRef == "" && selected.Skill.Scope == capability.SkillScopeUser {
		selectedRef = identity.DefaultUserID
	}

	var skillName, contentHash, executionMode, packageRef, scopeRef, versionID string
	var scope capability.SkillScope
	selection := selectedSkillScope(ctx, workspaceID, projectID)
	err = query.QueryRowContext(ctx, `
		SELECT si.skill_name, sv.content_hash, sv.execution_mode, sv.package_ref, si.scope, si.scope_ref, sv.skill_version_id
		FROM skill_installations si
		JOIN skill_versions sv ON sv.skill_installation_id = si.skill_installation_id
		WHERE si.workspace_id = ? AND ((si.scope = 'workspace' AND si.scope_ref = ?) OR (si.scope = 'user' AND si.scope_ref = ? AND ? != '') OR (si.scope = 'project' AND si.scope_ref = ?))
			AND si.capability_id = ? AND si.status = 'installed' AND si.enabled = 1
			AND si.scope = ? AND si.scope_ref = ?
			AND sv.version = ? AND sv.status = 'installed'
		ORDER BY CASE si.scope WHEN 'user' THEN 400 WHEN 'project' THEN 300 ELSE 200 END DESC LIMIT 1`,
		workspaceID, workspaceID, selection.userID, selection.userID, projectID, capabilityID, selected.Skill.Scope, selectedRef, version,
	).Scan(&skillName, &contentHash, &executionMode, &packageRef, &scope, &scopeRef, &versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return capability.Entry{}, false, nil
	}
	if err != nil {
		return capability.Entry{}, false, err
	}
	_, skill, err := s.inspectManagedSkillPackage(
		packageRef, skillName, capabilityID, version, executionMode, contentHash,
	)
	if err != nil {
		return capability.Entry{}, false, err
	}
	skill.Scope, skill.ScopeRef, skill.WorkspaceID, skill.Priority = scope, scopeRef, workspaceID, managedScopePriority(scope)
	skill.ManagedVersionID = versionID
	entry, ok := registry.EntryForSkillPackage(skill)
	return entry, ok, nil
}

func (s *Store) capabilityEntryForRunQuery(
	ctx context.Context, query projectWorkspaceQuery, runID, capabilityID string,
) (capability.Entry, bool, error) {
	var projectID, storedCapabilityID, version, versionID, snapshotID string
	err := query.QueryRowContext(ctx, `
		SELECT project_id, capability_id, capability_version, COALESCE(skill_version_id, ''), COALESCE(skill_snapshot_id, '') FROM runs WHERE run_id = ?`,
		runID,
	).Scan(&projectID, &storedCapabilityID, &version, &versionID, &snapshotID)
	if errors.Is(err, sql.ErrNoRows) {
		return capability.Entry{}, false, domainError("RUN_NOT_FOUND", "任务不存在。")
	}
	if err != nil {
		return capability.Entry{}, false, err
	}
	if capabilityID != storedCapabilityID {
		return capability.Entry{}, false, nil
	}
	if snapshotID != "" {
		return s.capabilityEntryForExecutionSnapshotQuery(ctx, query, projectID, storedCapabilityID, snapshotID)
	}
	if versionID != "" {
		return s.capabilityEntryForSkillVersionQuery(ctx, query, projectID, storedCapabilityID, versionID)
	}
	return s.capabilityEntryForProjectVersionQuery(
		ctx, query, projectID, storedCapabilityID, version,
	)
}

func (s *Store) capabilityEntryForRun(
	ctx context.Context, runID, capabilityID string,
) (capability.Entry, bool, error) {
	var projectID, storedCapabilityID, version string
	err := s.db.QueryRowContext(ctx, `
		SELECT project_id, capability_id, capability_version FROM runs WHERE run_id = ?`,
		runID,
	).Scan(&projectID, &storedCapabilityID, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return capability.Entry{}, false, domainError("RUN_NOT_FOUND", "任务不存在。")
	}
	if err != nil {
		return capability.Entry{}, false, err
	}
	if capabilityID != storedCapabilityID {
		return capability.Entry{}, false, nil
	}
	if _, err := s.CapabilityRegistryForProject(ctx, projectID); err != nil {
		return capability.Entry{}, false, err
	}
	return s.capabilityEntryForRunQuery(ctx, s.db, runID, capabilityID)
}
