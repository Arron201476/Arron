package runtime

import (
	"context"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

type skillSelection struct {
	workspaceID string
	projectID   string
	userID      string
}

func selectedSkillScope(ctx context.Context, workspaceID, projectID string) skillSelection {
	selection := skillSelection{workspaceID: workspaceID, projectID: projectID, userID: identity.UserIDFromContext(ctx)}
	if transport, present := identity.FromContext(ctx); present && transport.Kind != identity.KindUser {
		if _, delegated := identity.UserFromContext(ctx); !delegated {
			selection.userID = ""
		}
	}
	return selection
}

func (selection skillSelection) includes(root capability.SkillRoot) bool {
	if root.Scope == capability.SkillScopeSystem {
		return root.WorkspaceID == "" && root.ScopeRef == ""
	}
	workspaceID := root.WorkspaceID
	if workspaceID == "" {
		workspaceID = identity.DefaultWorkspaceID
	}
	if workspaceID != selection.workspaceID {
		return false
	}
	switch root.Scope {
	case capability.SkillScopeWorkspace:
		return root.ScopeRef == "" || root.ScopeRef == selection.workspaceID
	case capability.SkillScopeUser:
		ownerID := root.ScopeRef
		if ownerID == "" {
			ownerID = identity.DefaultUserID
		}
		return selection.userID != "" && ownerID == selection.userID
	case capability.SkillScopeProject:
		return selection.projectID != "" && root.ScopeRef == selection.projectID
	default:
		return false
	}
}

func managedScopePriority(scope capability.SkillScope) int {
	switch scope {
	case capability.SkillScopeSystem:
		return 150
	case capability.SkillScopeWorkspace:
		return 250
	case capability.SkillScopeProject:
		return 350
	case capability.SkillScopeUser:
		return 450
	default:
		return 0
	}
}

func (s *Store) buildSelectionRegistry(ctx context.Context, query projectWorkspaceQuery, workspaceID, projectID string) (*capability.Registry, error) {
	if registry, frozen, err := s.agentTurnSkillRegistry(ctx, query, workspaceID, projectID); frozen || err != nil {
		return registry, err
	}
	return s.buildLiveSelectionRegistry(ctx, query, workspaceID, projectID)
}

func (s *Store) buildLiveSelectionRegistry(ctx context.Context, query projectWorkspaceQuery, workspaceID, projectID string) (*capability.Registry, error) {
	selection := selectedSkillScope(ctx, workspaceID, projectID)
	rows, err := query.QueryContext(ctx, `SELECT installation.scope, installation.scope_ref, installation.skill_name, installation.capability_id,
		installation.status, installation.enabled, version.version, version.content_hash, version.execution_mode, version.package_ref, version.skill_version_id
		FROM skill_installations installation JOIN skill_versions version ON version.skill_version_id = COALESCE(installation.active_version_id,
			(SELECT historical.skill_version_id FROM skill_versions historical WHERE historical.skill_installation_id = installation.skill_installation_id ORDER BY historical.created_at DESC, historical.skill_version_id DESC LIMIT 1))
		WHERE installation.workspace_id = ? AND (
			(installation.scope = 'workspace' AND installation.scope_ref = ?) OR
			(installation.scope = 'user' AND installation.scope_ref = ? AND ? != '') OR
			(installation.scope = 'project' AND installation.scope_ref = ? AND ? != ''))`,
		workspaceID, workspaceID, selection.userID, selection.userID, projectID, projectID)
	if err != nil {
		return nil, err
	}
	type storedPackage struct {
		scope                                                                            capability.SkillScope
		scopeRef, name, capabilityID, status, version, hash, mode, packageRef, versionID string
		enabled                                                                          bool
	}
	var stored []storedPackage
	for rows.Next() {
		var item storedPackage
		if err := rows.Scan(&item.scope, &item.scopeRef, &item.name, &item.capabilityID, &item.status, &item.enabled, &item.version, &item.hash, &item.mode, &item.packageRef, &item.versionID); err != nil {
			rows.Close()
			return nil, err
		}
		stored = append(stored, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	snapshots := make([]*capability.SkillPackage, 0, len(stored))
	for _, item := range stored {
		_, skill, err := s.inspectManagedSkillPackage(item.packageRef, item.name, item.capabilityID, item.version, item.mode, item.hash)
		if err != nil {
			skill = &capability.SkillPackage{Name: item.name, CapabilityID: item.capabilityID, Version: item.version, ContentHash: item.hash, ExecutionMode: item.mode,
				DisplayPath: item.packageRef + "/SKILL.md", Disabled: true,
				UnavailableReasonCode: "SKILL_PACKAGE_UNAVAILABLE", UnavailableMessage: "该 Skill 安装包缺失或内容校验失败，请重新安装。"}
		}
		skill.Scope, skill.ScopeRef, skill.WorkspaceID = item.scope, item.scopeRef, workspaceID
		skill.Priority = managedScopePriority(item.scope)
		skill.ManagedVersionID = item.versionID
		skill.Disabled = skill.Disabled || !item.enabled || item.status != "installed"
		snapshots = append(snapshots, skill)
	}
	registry, err := s.registry.ForkForSelection(selection.includes, snapshots)
	if err != nil {
		return nil, err
	}
	if err := s.applyWorkspaceToolDependencies(ctx, query, workspaceID, registry); err != nil {
		return nil, err
	}
	return registry, nil
}

func (s *Store) CapabilityRegistryForSelection(ctx context.Context, workspaceID string) (*capability.Registry, error) {
	if !validWorkspaceID(workspaceID) {
		return nil, domainError("WORKSPACE_NOT_FOUND", "工作区不存在。")
	}
	return s.buildSelectionRegistry(ctx, s.db, workspaceID, "")
}
