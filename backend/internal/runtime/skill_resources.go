package runtime

import (
	"context"
	"slices"

	"content-agent/backend/internal/capability"
)

func (s *Store) skillPackageForProjectVersion(ctx context.Context, projectID, capabilityID, version string) (*capability.SkillPackage, error) {
	if version == "" {
		return nil, domainError("SKILL_VERSION_REQUIRED", "读取 Skill 资源需要指定已选择的版本。")
	}
	entry, ok, err := s.capabilityEntryForProjectVersion(ctx, projectID, capabilityID, version)
	if err != nil {
		return nil, err
	}
	if !ok || entry.Status != capability.Available || entry.Skill == nil {
		return nil, domainError("SKILL_UNAVAILABLE", "该 Skill 版本当前不可用。")
	}
	return entry.Skill, nil
}

func (s *Store) ListSkillResources(ctx context.Context, projectID, capabilityID, version string) ([]capability.SkillResource, error) {
	skill, err := s.skillPackageForProjectVersion(ctx, projectID, capabilityID, version)
	if err != nil {
		return nil, err
	}
	return slices.Clone(skill.Resources), nil
}

func (s *Store) ReadSkillResource(ctx context.Context, projectID, capabilityID, version, resourcePath string, offset, limit int) (capability.SkillResourcePage, error) {
	skill, err := s.skillPackageForProjectVersion(ctx, projectID, capabilityID, version)
	if err != nil {
		return capability.SkillResourcePage{}, err
	}
	page, err := skill.ReadResource(resourcePath, offset, limit)
	if err != nil {
		return capability.SkillResourcePage{}, domainError("SKILL_RESOURCE_UNAVAILABLE", "资源路径、格式或版本校验失败；支持当前 Skill 版本中的 UTF-8 文本、PNG/JPEG/GIF/WebP 图片及 PDF/DOCX 文档，二进制资源的 offset 必须为 0。")
	}
	return page, nil
}
