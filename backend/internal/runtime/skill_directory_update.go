package runtime

import (
	"context"
	"path/filepath"
	"strings"

	"content-agent/backend/internal/capability"
)

type SkillDirectoryUpdate struct {
	SkillInstallationID string                `json:"skill_installation_id"`
	CurrentVersionID    string                `json:"current_version_id"`
	CapabilityID        string                `json:"capability_id"`
	Version             string                `json:"version,omitempty"`
	ContentHash         string                `json:"content_hash,omitempty"`
	SourcePath          string                `json:"source_path,omitempty"`
	SourceScope         capability.SkillScope `json:"source_scope,omitempty"`
	Status              string                `json:"status"`
	UserMessage         string                `json:"user_message,omitempty"`
}

type UpdateSkillFromDirectoryCommand struct {
	ExpectedActiveVersionID string `json:"expected_active_version_id"`
	Version                 string `json:"version"`
	ContentHash             string `json:"content_hash"`
}

func (s *Store) directoryUpdateSource(ctx context.Context, installation SkillInstallation) (*capability.SkillPackage, error) {
	projectID := ""
	if installation.Scope == string(capability.SkillScopeProject) {
		projectID = installation.ScopeRef
	}
	selection := selectedSkillScope(ctx, installation.WorkspaceID, projectID)
	registry, err := s.registry.ForkForSelection(selection.includes, nil)
	if err != nil {
		return nil, err
	}
	entry, ok := registry.Get(installation.CapabilityID)
	if !ok || entry.Skill == nil || entry.Skill.Name != installation.SkillName || entry.Skill.ManagedVersionID != "" || entry.Skill.ExecutionSnapshotID != "" {
		return nil, nil
	}
	// A managed archive is not an editable source, even if an operator has
	// accidentally registered its directory as a discovery root.
	relative, err := filepath.Rel(s.skillDataRoot, entry.Skill.Directory)
	if err != nil && strings.EqualFold(filepath.VolumeName(s.skillDataRoot), filepath.VolumeName(entry.Skill.Directory)) {
		return nil, err
	}
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
		return nil, nil
	}
	return entry.Skill, nil
}

func (s *Store) PreviewSkillDirectoryUpdate(ctx context.Context, installationID string) (SkillDirectoryUpdate, error) {
	installation, err := s.GetSkillInstallation(ctx, installationID)
	if err != nil {
		return SkillDirectoryUpdate{}, err
	}
	if err := s.authorizeSkillScopeMutation(ctx, installationScope(installation)); err != nil {
		return SkillDirectoryUpdate{}, err
	}
	preview := SkillDirectoryUpdate{SkillInstallationID: installationID, CapabilityID: installation.CapabilityID, Status: "not_found", UserMessage: "当前可见的已注册目录中没有同名 Skill。"}
	if installation.ActiveVersionID == nil || installation.Status == "uninstalled" {
		return preview, domainError("SKILL_INSTALLATION_STATE_CONFLICT", "已卸载的 Skill 需要重新安装。")
	}
	preview.CurrentVersionID = *installation.ActiveVersionID
	source, err := s.directoryUpdateSource(ctx, installation)
	if err != nil || source == nil {
		return preview, err
	}
	preview.Version, preview.ContentHash, preview.SourcePath, preview.SourceScope = source.Version, source.ContentHash, source.DisplayPath, source.Scope
	preview.Status, preview.UserMessage = "available", ""
	for _, version := range installation.Versions {
		if version.Version == source.Version && version.ContentHash != source.ContentHash {
			preview.Status, preview.UserMessage = "version_conflict", "目录内容已变，但显式版本号未变；请先更新目录 manifest 中的版本号。"
			break
		}
		if version.SkillVersionID == preview.CurrentVersionID && version.ContentHash == source.ContentHash {
			preview.Status, preview.UserMessage = "current", "已是当前目录版本。"
			break
		}
	}
	return preview, nil
}

func (s *Store) UpdateSkillFromDirectory(ctx context.Context, installationID string, command UpdateSkillFromDirectoryCommand, actorRef string) (SkillInstallation, error) {
	if command.ExpectedActiveVersionID == "" || command.Version == "" || command.ContentHash == "" {
		return SkillInstallation{}, domainError("REQUEST_VALIDATION_FAILED", "请先检查目录更新并确认版本与哈希。")
	}
	preview, err := s.PreviewSkillDirectoryUpdate(ctx, installationID)
	if err != nil {
		return SkillInstallation{}, err
	}
	if preview.CurrentVersionID != command.ExpectedActiveVersionID || preview.Version != command.Version || preview.ContentHash != command.ContentHash {
		return SkillInstallation{}, domainError("SKILL_DISCOVERY_CHANGED", "目录或当前版本已变化，请重新检查更新。")
	}
	if preview.Status == "version_conflict" {
		return SkillInstallation{}, domainError("SKILL_VERSION_IMMUTABLE", preview.UserMessage)
	}
	installation, err := s.GetSkillInstallation(ctx, installationID)
	if err != nil {
		return SkillInstallation{}, err
	}
	source, err := s.directoryUpdateSource(ctx, installation)
	if err != nil {
		return SkillInstallation{}, err
	}
	if source == nil {
		return SkillInstallation{}, domainError("SKILL_NOT_FOUND", "目录 Skill 不存在，请重新检查更新。")
	}
	return s.installSkillChecked(ctx, "directory", safeSkillSourceName(filepath.Base(source.Directory), "skill-directory"), actorRef, installationID, command.ExpectedActiveVersionID,
		func(quarantineRoot string) (*capability.SkillPackage, error) {
			copied, err := prepareSkillDirectory(s.registry.ProjectRoot(), quarantineRoot, source.Directory)
			if err != nil {
				return nil, err
			}
			if copied.CapabilityID != installation.CapabilityID || copied.Name != installation.SkillName || copied.Version != command.Version || copied.ContentHash != command.ContentHash {
				return nil, domainError("SKILL_DISCOVERY_CHANGED", "目录 Skill 在更新前发生变化，请重新检查更新。")
			}
			return copied, nil
		})
}
