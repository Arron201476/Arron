import type { SkillInstallation, SkillLifecycleAction, SkillLifecycleResult, SkillPackageResult } from "../../types";
import { skillArchiveHash, type SkillPackageRequest } from "../../skillPackage";

export async function packageResult(base: SkillInstallation, request: SkillPackageRequest): Promise<SkillPackageResult> {
  const item = structuredClone("installation" in request ? request.installation : base);
  if ("target" in request) { item.scope = request.target.scope; item.scope_ref = request.target.scope === "workspace" ? item.workspace_id : request.target.scope === "project" ? request.target.project_id! : "user-1"; }
  const version = { ...item.versions.find((v) => v.skill_version_id === item.active_version_id)!, skill_version_id: `version-${request.key}` };
  if (request.action === "adopt_directory") { version.version = request.version; version.content_hash = request.contentHash; }
  if (request.action === "update_directory") { version.version = request.preview.version!; version.content_hash = request.preview.content_hash!; }
  const hash = "file" in request ? await skillArchiveHash(request.file) : "";
  const event = { skill_installation_event_id: `event-${request.key}`, skill_installation_id: item.skill_installation_id, skill_version_id: version.skill_version_id, event_type: "skill.version.installed", actor_ref: "user-1", payload: { request_id: request.key, request_hash: "a".repeat(64), source_hash: hash, content_hash: version.content_hash }, created_at: "2026-09-10T00:00:00Z" };
  return { receipt: { request_id: request.key, action: request.action, actor_ref: event.actor_ref, skill_installation_id: item.skill_installation_id, skill_installation_event_id: event.skill_installation_event_id, skill_version_id: version.skill_version_id, workspace_id: item.workspace_id, scope: item.scope, scope_ref: item.scope_ref, skill_name: item.skill_name, capability_id: item.capability_id, version: version.version, content_hash: version.content_hash, source_hash: hash }, installation: { ...item, active_version_id: version.skill_version_id, versions: [...item.versions, version], events: [...item.events, event] } };
}

export function lifecycleResult(installation: SkillInstallation, action: SkillLifecycleAction, key: string, version?: string): SkillLifecycleResult {
  const target = installation.versions.find((item) => action === "activate" ? item.version === version : item.skill_version_id === installation.active_version_id)!;
  const event = {
    skill_installation_event_id: `event-${key}`, skill_installation_id: installation.skill_installation_id, skill_version_id: target.skill_version_id,
    event_type: { enable: "skill.enabled", disable: "skill.disabled", activate: "skill.version.activated", uninstall: "skill.uninstalled" }[action],
    actor_ref: "user-1", payload: { request_id: key, request_hash: "a".repeat(64) }, created_at: "2026-09-10T00:00:00Z",
  };
  return {
    receipt: { request_id: key, skill_installation_id: installation.skill_installation_id, skill_installation_event_id: event.skill_installation_event_id, skill_version_id: target.skill_version_id, action, actor_ref: event.actor_ref },
    installation: {
      ...installation, active_version_id: action === "uninstall" ? undefined : target.skill_version_id,
      enabled: action === "uninstall" || action === "disable" ? false : action === "enable" ? true : installation.enabled,
      status: action === "uninstall" ? "uninstalled" : installation.status,
      events: [...installation.events, event],
    },
  };
}
