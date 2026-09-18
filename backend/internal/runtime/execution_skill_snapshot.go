package runtime

import (
	"context"
	"database/sql"
	"errors"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func (s *Store) capabilityEntryForProposedAction(ctx context.Context, projectID, actionID, capabilityID, version string) (capability.Entry, bool, error) {
	if _, err := s.CapabilityRegistryForProject(ctx, projectID); err != nil {
		return capability.Entry{}, false, err
	}
	return s.capabilityEntryForProposedActionQuery(ctx, s.db, projectID, actionID, capabilityID, version)
}

func (s *Store) capabilityEntryForProposedActionQuery(ctx context.Context, query projectWorkspaceQuery, projectID, actionID, capabilityID, version string) (capability.Entry, bool, error) {
	var invocationID string
	err := query.QueryRowContext(ctx, `SELECT skill_invocation_id FROM skill_invocations WHERE proposed_action_id = ? AND project_id = ? AND capability_id = ? AND capability_version = ?`, actionID, projectID, capabilityID, version).Scan(&invocationID)
	if err == nil {
		entry, found, err := s.capabilityEntryForInvocationQuery(ctx, query, invocationID)
		if err == nil && found {
			err = authorizePersonalSkillExecution(ctx, entry)
		}
		return entry, found, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return capability.Entry{}, false, err
	}
	return s.capabilityEntryForProjectVersionQuery(ctx, query, projectID, capabilityID, version)
}

func authorizePersonalSkillExecution(ctx context.Context, entry capability.Entry) error {
	if entry.Skill != nil && entry.Skill.Scope == capability.SkillScopeUser {
		selection := selectedSkillScope(ctx, identity.WorkspaceIDFromContext(ctx), "")
		if !selection.includes(capability.SkillRoot{Scope: entry.Skill.Scope, WorkspaceID: entry.Skill.WorkspaceID, ScopeRef: entry.Skill.ScopeRef}) {
			return domainError("ROLE_FORBIDDEN", "个人 Skill 只能由所属用户启动或重新执行。")
		}
	}
	return nil
}

func selectedManagedSkillVersionQuery(ctx context.Context, query projectWorkspaceQuery, projectID string, entry capability.Entry) (*string, error) {
	if entry.Skill == nil || entry.Skill.ManagedVersionID == "" {
		return nil, nil
	}
	var versionID string
	scope, scopeRef := entry.Skill.Scope, entry.Skill.ScopeRef
	if scope == capability.SkillScopeWorkspace && scopeRef == "" {
		if err := query.QueryRowContext(ctx, `SELECT workspace_id FROM projects WHERE project_id = ? AND deleted_at IS NULL`, projectID).Scan(&scopeRef); err != nil {
			return nil, err
		}
	}
	err := query.QueryRowContext(ctx, `SELECT version.skill_version_id
		FROM skill_versions version JOIN skill_installations installation ON installation.skill_installation_id = version.skill_installation_id
		JOIN projects project ON project.workspace_id = installation.workspace_id
		WHERE project.project_id = ? AND installation.scope = ? AND installation.scope_ref = ?
		AND installation.capability_id = ? AND version.version = ? AND version.content_hash = ? AND version.skill_version_id = ?
		AND installation.status = 'installed' AND installation.enabled = 1 AND version.status = 'installed'`,
		projectID, scope, scopeRef, entry.Skill.CapabilityID, entry.Skill.Version, entry.Skill.ContentHash, entry.Skill.ManagedVersionID,
	).Scan(&versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &versionID, nil
}

func (s *Store) capabilityEntryForSkillVersionQuery(ctx context.Context, query projectWorkspaceQuery, projectID, capabilityID, versionID string) (capability.Entry, bool, error) {
	var skillName, version, contentHash, executionMode, packageRef, workspaceID, scopeRef string
	var scope capability.SkillScope
	err := query.QueryRowContext(ctx, `SELECT installation.skill_name, version.version, version.content_hash, version.execution_mode, version.package_ref,
		installation.workspace_id, installation.scope, installation.scope_ref
		FROM skill_versions version JOIN skill_installations installation ON installation.skill_installation_id = version.skill_installation_id
		JOIN projects project ON project.workspace_id = installation.workspace_id
		WHERE project.project_id = ? AND project.deleted_at IS NULL AND version.skill_version_id = ?
		AND (installation.scope != 'project' OR installation.scope_ref = project.project_id)
		AND installation.capability_id = ? AND installation.status = 'installed' AND installation.enabled = 1 AND version.status = 'installed'`,
		projectID, versionID, capabilityID).Scan(&skillName, &version, &contentHash, &executionMode, &packageRef, &workspaceID, &scope, &scopeRef)
	if errors.Is(err, sql.ErrNoRows) {
		return capability.Entry{}, false, nil
	}
	if err != nil {
		return capability.Entry{}, false, err
	}
	_, skill, err := s.inspectManagedSkillPackage(packageRef, skillName, capabilityID, version, executionMode, contentHash)
	if err != nil {
		return capability.Entry{}, false, err
	}
	skill.Scope, skill.ScopeRef, skill.WorkspaceID, skill.Priority = scope, scopeRef, workspaceID, managedScopePriority(scope)
	skill.ManagedVersionID = versionID
	return s.executionSkillEntryWithTools(ctx, query, workspaceID, skill)
}

func (s *Store) capabilityEntryForInvocationQuery(ctx context.Context, query projectWorkspaceQuery, invocationID string) (capability.Entry, bool, error) {
	var projectID, capabilityID, version, versionID, snapshotID string
	err := query.QueryRowContext(ctx, `SELECT project_id, capability_id, capability_version, COALESCE(skill_version_id, ''), COALESCE(skill_snapshot_id, '') FROM skill_invocations WHERE skill_invocation_id = ?`, invocationID).Scan(&projectID, &capabilityID, &version, &versionID, &snapshotID)
	if errors.Is(err, sql.ErrNoRows) {
		return capability.Entry{}, false, nil
	}
	if err != nil {
		return capability.Entry{}, false, err
	}
	if snapshotID != "" {
		return s.capabilityEntryForExecutionSnapshotQuery(ctx, query, projectID, capabilityID, snapshotID)
	}
	if versionID != "" {
		return s.capabilityEntryForSkillVersionQuery(ctx, query, projectID, capabilityID, versionID)
	}
	return s.capabilityEntryForProjectVersionQuery(ctx, query, projectID, capabilityID, version)
}
