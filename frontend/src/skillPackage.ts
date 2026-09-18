import { ApiError } from "./api";
import type { SkillDirectoryUpdate, SkillInstallation, SkillInstallTarget, SkillPackageResult } from "./types";

export type SkillPackageInput =
  | { action: "install_zip"; file: File; target: SkillInstallTarget }
  | { action: "upgrade_zip"; file: File; installation: SkillInstallation }
  | { action: "adopt_directory"; capabilityID: string; version: string; contentHash: string; target: SkillInstallTarget }
  | { action: "update_directory"; preview: SkillDirectoryUpdate; installation: SkillInstallation };
export type SkillPackageRequest = SkillPackageInput & { package: true; key: string; hadUnknownResult?: boolean };

export async function skillArchiveBytes(file: File) {
  if (file.size > 20 * 1024 * 1024) throw new ApiError("SKILL_ARCHIVE_TOO_LARGE", "Skill ZIP 超过 20 MiB 限制。", 400);
  return typeof file.arrayBuffer === "function" ? await file.arrayBuffer() : await new Promise<ArrayBuffer>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as ArrayBuffer);
    reader.onerror = () => reject(reader.error);
    reader.onabort = () => reject(new Error("Skill archive read aborted"));
    reader.readAsArrayBuffer(file);
  });
}

export async function skillBytesHash(bytes: ArrayBuffer) { return Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)), (value) => value.toString(16).padStart(2, "0")).join(""); }
export async function skillArchiveHash(file: File) { return skillBytesHash(await skillArchiveBytes(file)); }

export function skillPackageScope(request: SkillPackageRequest) { return "installation" in request ? request.installation.scope : request.target.scope; }
export function skillPackageLabel(request: SkillPackageRequest) { return "file" in request ? request.file.name : "installation" in request ? request.installation.skill_name : request.capabilityID; }

export function validateSkillPackageResult(result: SkillPackageResult, request: SkillPackageRequest, sourceHash: string, userID?: string, workspaceID?: string) {
  const invalid = () => { throw new ApiError("SKILL_PACKAGE_RECEIPT_INVALID", "Skill 安装回执与原请求不一致，结果仍待确认。", 502); };
  const r = result?.receipt, item = result?.installation;
  if (!r || !item || !r.actor_ref || userID && r.actor_ref !== userID || workspaceID && r.workspace_id !== workspaceID || r.request_id !== request.key || r.action !== request.action || r.source_hash !== sourceHash || !r.skill_installation_id || item.skill_installation_id !== r.skill_installation_id || item.workspace_id !== r.workspace_id || item.scope !== r.scope || item.scope_ref !== r.scope_ref || item.skill_name !== r.skill_name || item.capability_id !== r.capability_id || !Array.isArray(item.versions) || !Array.isArray(item.events)) return invalid();
  if ("installation" in request) {
    const old = request.installation;
    if (item.skill_installation_id !== old.skill_installation_id || item.workspace_id !== old.workspace_id || item.scope !== old.scope || item.scope_ref !== old.scope_ref || item.capability_id !== old.capability_id || item.skill_name !== old.skill_name) return invalid();
  } else if (r.scope !== request.target.scope || request.target.scope === "project" && r.scope_ref !== request.target.project_id || request.target.scope === "user" && userID && r.scope_ref !== userID || request.target.scope === "workspace" && r.scope_ref !== r.workspace_id) return invalid();
  if (request.action === "adopt_directory" && (r.capability_id !== request.capabilityID || r.version !== request.version || r.content_hash !== request.contentHash)) return invalid();
  if (request.action === "update_directory" && (r.capability_id !== request.preview.capability_id || r.version !== request.preview.version || r.content_hash !== request.preview.content_hash)) return invalid();
  const versions = item.versions.filter((v) => v.skill_version_id === r.skill_version_id);
  const events = item.events.filter((e) => e.skill_installation_event_id === r.skill_installation_event_id);
  if (versions.length !== 1 || events.length !== 1) return invalid();
  const v = versions[0], e = events[0];
  if (v.skill_installation_id !== item.skill_installation_id || v.version !== r.version || v.content_hash !== r.content_hash || e.skill_installation_id !== item.skill_installation_id || e.skill_version_id !== r.skill_version_id || e.actor_ref !== r.actor_ref || !["skill.version.installed", "skill.version.reused"].includes(e.event_type) || e.payload?.request_id !== request.key || e.payload?.source_hash !== sourceHash || e.payload?.content_hash !== r.content_hash || typeof e.payload?.request_hash !== "string" || !/^[a-f0-9]{64}$/.test(e.payload.request_hash)) return invalid();
}
