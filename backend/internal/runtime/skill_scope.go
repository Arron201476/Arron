package runtime

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

// Scope references are resolved from the authenticated user or a checked project,
// never from a client-supplied filesystem path or user identifier.
type SkillInstallTarget struct {
	Scope     capability.SkillScope `json:"scope"`
	ProjectID string                `json:"project_id,omitempty"`
}

type managedSkillScope struct {
	workspaceID string
	scope       capability.SkillScope
	ref         string
}

const visibleManagedSkillSQL = `managed.workspace_id = ? AND ((managed.scope = 'workspace' AND managed.scope_ref = managed.workspace_id) OR
	(managed.scope = 'user' AND managed.scope_ref = ? AND ? != '') OR (managed.scope = 'project' AND EXISTS
	(SELECT 1 FROM projects p WHERE p.project_id = managed.scope_ref AND p.workspace_id = managed.workspace_id AND p.deleted_at IS NULL)))`

func skillVisibilityArgs(ctx context.Context) []any {
	workspaceID := identity.WorkspaceIDFromContext(ctx)
	userID := selectedSkillScope(ctx, workspaceID, "").userID
	return []any{workspaceID, userID, userID}
}

func installationScope(item SkillInstallation) managedSkillScope {
	return managedSkillScope{item.WorkspaceID, capability.SkillScope(item.Scope), item.ScopeRef}
}

func (s *Store) resolveSkillInstallTarget(ctx context.Context, targets []SkillInstallTarget) (managedSkillScope, error) {
	if len(targets) > 1 {
		return managedSkillScope{}, domainError("REQUEST_VALIDATION_FAILED", "只能指定一个 Skill 安装范围。")
	}
	var input SkillInstallTarget
	if len(targets) == 1 {
		input = targets[0]
	}
	if input.Scope == "" {
		input.Scope = capability.SkillScopeWorkspace
	}
	target := managedSkillScope{workspaceID: identity.WorkspaceIDFromContext(ctx), scope: input.Scope}
	switch input.Scope {
	case capability.SkillScopeWorkspace:
		target.ref = target.workspaceID
	case capability.SkillScopeUser:
		target.ref = selectedSkillScope(ctx, target.workspaceID, "").userID
	case capability.SkillScopeProject:
		target.ref = input.ProjectID
	default:
		return managedSkillScope{}, domainError("REQUEST_VALIDATION_FAILED", "安装范围必须是 user、project 或 workspace；system 由平台管理。")
	}
	if input.Scope != capability.SkillScopeProject && input.ProjectID != "" {
		return managedSkillScope{}, domainError("REQUEST_VALIDATION_FAILED", "只有项目范围可以指定 project_id。")
	}
	return target, s.authorizeSkillScopeMutation(ctx, target)
}

func (s *Store) skillScopeVisible(ctx context.Context, target managedSkillScope) (bool, error) {
	return skillScopeVisibleQuery(ctx, s.db, target)
}

func skillScopeVisibleQuery(ctx context.Context, query rowQueryer, target managedSkillScope) (bool, error) {
	if target.workspaceID != identity.WorkspaceIDFromContext(ctx) || !validWorkspaceID(target.workspaceID) || !validWorkspaceID(target.ref) {
		return false, nil
	}
	switch target.scope {
	case capability.SkillScopeWorkspace:
		return target.ref == target.workspaceID, nil
	case capability.SkillScopeUser:
		return target.ref == selectedSkillScope(ctx, target.workspaceID, "").userID, nil
	case capability.SkillScopeProject:
		var count int
		err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE project_id = ? AND workspace_id = ? AND deleted_at IS NULL`, target.ref, target.workspaceID).Scan(&count)
		return count == 1, err
	default:
		return false, nil
	}
}

func (s *Store) authorizeSkillScopeMutation(ctx context.Context, target managedSkillScope) error {
	return authorizeSkillScopeMutationQuery(ctx, s.db, target)
}

func authorizeSkillScopeMutationQuery(ctx context.Context, query rowQueryer, target managedSkillScope) error {
	visible, err := skillScopeVisibleQuery(ctx, query, target)
	if err != nil {
		return err
	}
	if !visible {
		return domainError("SKILL_INSTALLATION_NOT_FOUND", "Skill 安装范围不存在或不可访问。")
	}
	principal, ok := identity.UserFromContext(ctx)
	if !ok {
		if _, explicit := identity.FromContext(ctx); explicit {
			return domainError("ROLE_FORBIDDEN", "当前身份不能管理 Skill 安装。")
		}
		principal = identity.DefaultLocalPrincipal()
	}
	resolved, err := resolvePrincipalQuery(ctx, query, principal)
	if err != nil {
		return err
	}
	required := identity.RoleEditor
	if target.scope == capability.SkillScopeWorkspace {
		required = identity.RoleAdmin
	}
	if !resolved.Allows(required) {
		return domainError("ROLE_FORBIDDEN", "当前角色无权管理该范围的 Skill。")
	}
	return nil
}

func (s *Store) scopedSkillActiveRoot(target managedSkillScope) (string, error) {
	if !validWorkspaceID(target.workspaceID) || !validWorkspaceID(target.ref) {
		return "", domainError("REQUEST_VALIDATION_FAILED", "Skill 安装范围无效。")
	}
	if target.scope == capability.SkillScopeWorkspace && target.ref == target.workspaceID {
		return s.managedSkillActiveRoot(target.workspaceID), nil
	}
	if target.scope != capability.SkillScopeUser && target.scope != capability.SkillScopeProject {
		return "", domainError("REQUEST_VALIDATION_FAILED", "Skill 安装范围无效。")
	}
	return filepath.Join(s.skillDataRoot, "scoped-active", target.workspaceID, string(target.scope), target.ref), nil
}

func (s *Store) lifecycleSkillRegistry(ctx context.Context, target managedSkillScope) (*capability.Registry, error) {
	if target.scope == capability.SkillScopeWorkspace {
		return s.CapabilityRegistryForWorkspace(ctx, target.workspaceID)
	}
	root, err := s.scopedSkillActiveRoot(target)
	if err != nil {
		return nil, err
	}
	registry, err := s.registry.ForkForSelection(func(root capability.SkillRoot) bool { return root.Scope == capability.SkillScopeSystem }, nil)
	if err != nil {
		return nil, err
	}
	err = registry.AddSkillRoot(capability.SkillRoot{Scope: target.scope, ScopeRef: target.ref, WorkspaceID: target.workspaceID, Path: root, Priority: managedScopePriority(target.scope)})
	return registry, err
}

func migrateSkillInstallAttemptScopes(db *sql.DB) error {
	for _, field := range []struct{ name, definition string }{{"scope", "TEXT NOT NULL DEFAULT 'workspace'"}, {"scope_ref", "TEXT NOT NULL DEFAULT ''"}} {
		exists, err := tableHasColumn(db, "skill_install_attempts", field.name)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := db.Exec(`ALTER TABLE skill_install_attempts ADD COLUMN ` + field.name + ` ` + field.definition); err != nil {
				return err
			}
		}
	}
	_, err := db.Exec(`UPDATE skill_install_attempts SET scope_ref = workspace_id WHERE scope = 'workspace' AND scope_ref = ''`)
	return err
}

func (s *Store) cleanScopedSkillActivations(expected map[string]map[string]struct{}) error {
	base := filepath.Join(s.skillDataRoot, "scoped-active")
	if _, err := os.Stat(base); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() || path == base {
			return nil
		}
		relative, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		// Only inspect generated workspace/scope/owner directories. WalkDir does
		// not follow symlinks; package contents are handled by the bounded remover.
		if filepath.Dir(filepath.Dir(relative)) == "." {
			return nil
		}
		children, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, child := range children {
			if _, keep := expected[path][child.Name()]; keep {
				continue
			}
			if err := removeManagedSkillPath(s.skillDataRoot, filepath.Join(path, child.Name())); err != nil {
				return err
			}
		}
		return filepath.SkipDir
	})
}
