import type { Principal, ProjectSkillDraft, ProjectSkillInstallResult, SkillInstallTarget } from "../../types";

export const skillPrincipal: Principal = { kind: "user", user_id: "u", workspace_id: "ws", role: "editor", display_name: "User", workspace_name: "Workspace", auth_method: "test" };

export function skillDraft(projectID = "p", root = "draft/my-skill"): ProjectSkillDraft {
  return { project_id: projectID, root_path: root, snapshot_hash: "draft-hash", status: "valid", capability_id: "my-skill", files: [], diagnostics: [], installations: [], version: "1.0.0", execution_mode: "inline", manifest: { name: "my-skill", scope: "project", path: `${root}/SKILL.md`, content_hash: "package-hash", allow_implicit_invocation: true, interface: {}, dependencies: [], scripts: [] } };
}

export function skillInstallResult(draft = skillDraft(), scope: SkillInstallTarget["scope"] = "project", installationID = "install-new"): ProjectSkillInstallResult {
  const now = "2026-09-09T00:00:00Z";
  return {
    receipt: { receipt_id: "receipt-new", project_id: draft.project_id, skill_installation_id: installationID, skill_version_id: "version-new", created_at: now },
    installation: {
      skill_installation_id: installationID, workspace_id: skillPrincipal.workspace_id, scope, scope_ref: scope === "project" ? draft.project_id : scope === "user" ? skillPrincipal.user_id : skillPrincipal.workspace_id,
      skill_name: draft.manifest!.name, capability_id: draft.capability_id!, status: "installed", enabled: true, active_version_id: "version-new", registry_status: "available", created_by: "u", created_at: now, updated_at: now, events: [],
      versions: [{ skill_version_id: "version-new", skill_installation_id: installationID, version: draft.version!, content_hash: draft.manifest!.content_hash, execution_mode: draft.execution_mode!, source_type: "zip", source_name: "project-skill-draft.zip", manifest: draft.manifest!, status: "installed", installed_by: "u", created_at: now }],
    },
  };
}
