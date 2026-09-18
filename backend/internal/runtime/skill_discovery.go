package runtime

import (
	"context"
	"path/filepath"
	"strings"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func (s *Store) RefreshWorkspaceSkills(ctx context.Context, projectIDs ...string) ([]capability.SkillDiagnostic, error) {
	s.skillMu.Lock()
	defer s.skillMu.Unlock()
	workspaceID := identity.WorkspaceIDFromContext(ctx)
	projectID := ""
	if len(projectIDs) > 1 {
		return nil, domainError("REQUEST_VALIDATION_FAILED", "只能指定一个项目。")
	}
	if len(projectIDs) == 1 {
		projectID = projectIDs[0]
	}
	if projectID != "" {
		visible, err := s.skillScopeVisible(ctx, managedSkillScope{workspaceID, capability.SkillScopeProject, projectID})
		if err != nil {
			return nil, err
		}
		if !visible {
			return nil, domainError("PROJECT_NOT_FOUND", "作品不存在。")
		}
	}
	registry, err := s.buildWorkspaceRegistry(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	s.registryMu.Lock()
	s.workspaceRegistries[workspaceID] = registry
	s.registryMu.Unlock()
	selection, err := s.buildSelectionRegistry(ctx, s.db, workspaceID, projectID)
	if err != nil {
		return nil, err
	}
	return selection.SkillDiagnostics(), nil
}

func (s *Store) InstallDiscoveredSkill(
	ctx context.Context, capabilityID, version, contentHash, actorRef string,
	targets ...SkillInstallTarget,
) (SkillInstallation, error) {
	if strings.TrimSpace(version) == "" || strings.TrimSpace(contentHash) == "" {
		return SkillInstallation{}, domainError("REQUEST_VALIDATION_FAILED", "需要指定目录 Skill 的版本和内容哈希。")
	}
	target, err := s.resolveSkillInstallTarget(ctx, targets)
	if err != nil {
		return SkillInstallation{}, err
	}
	projectID := ""
	if target.scope == capability.SkillScopeProject {
		projectID = target.ref
	}
	registry, err := s.buildSelectionRegistry(ctx, s.db, identity.WorkspaceIDFromContext(ctx), projectID)
	if err != nil {
		return SkillInstallation{}, err
	}
	entry, found := registry.Get(capabilityID)
	if !found || entry.Skill == nil {
		return SkillInstallation{}, domainError("SKILL_NOT_FOUND", "目录 Skill 不存在，请重新扫描。")
	}
	if entry.Skill.Version != version || entry.Skill.ContentHash != contentHash {
		return SkillInstallation{}, domainError("SKILL_DISCOVERY_CHANGED", "目录 Skill 已变化，请重新扫描后确认。")
	}
	sourceDirectory := entry.Skill.Directory
	sourceName := safeSkillSourceName(filepath.Base(sourceDirectory), "skill-directory")
	return s.installSkill(ctx, "directory", sourceName, actorRef, "", func(quarantineRoot string) (*capability.SkillPackage, error) {
		// Only copy a registered directory, and verify the copied snapshot against
		// what the user selected. Never accept a host path from an API caller.
		skill, err := prepareSkillDirectory(s.registry.ProjectRoot(), quarantineRoot, sourceDirectory)
		if err != nil {
			return nil, err
		}
		if skill.CapabilityID != capabilityID || skill.Name != entry.Skill.Name ||
			skill.Version != version || skill.ContentHash != contentHash {
			return nil, domainError("SKILL_DISCOVERY_CHANGED", "目录 Skill 在安装前发生变化，请重新扫描后确认。")
		}
		return skill, nil
	}, targets...)
}
