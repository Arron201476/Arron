import { ApiError } from "./api";
import type { SkillInstallation, SkillLifecycleAction, SkillLifecycleResult } from "./types";

export type SkillLifecycleRequest = { installation: SkillInstallation; action: SkillLifecycleAction; version?: string; key: string; hadUnknownResult?: boolean };

export function validateSkillLifecycleResult(result: SkillLifecycleResult, request: SkillLifecycleRequest, userID?: string) {
  const invalid = () => { throw new ApiError("SKILL_LIFECYCLE_RECEIPT_INVALID", "Skill 操作回执与原请求不一致，结果仍待确认。", 502); };
  const original = request.installation, receipt = result?.receipt, current = result?.installation;
  if (!receipt || !current || !receipt.actor_ref || userID && receipt.actor_ref !== userID || receipt.request_id !== request.key || receipt.action !== request.action || receipt.skill_installation_id !== original.skill_installation_id || current.skill_installation_id !== original.skill_installation_id || current.workspace_id !== original.workspace_id || current.scope !== original.scope || current.scope_ref !== original.scope_ref || current.capability_id !== original.capability_id || current.skill_name !== original.skill_name || !Array.isArray(current.versions) || !Array.isArray(current.events)) return invalid();
  const target = original.versions.find((version) => request.action === "activate" ? version.version === request.version : version.skill_version_id === original.active_version_id);
  const version = current.versions.find((version) => version.skill_version_id === receipt.skill_version_id);
  const events = current.events.filter((event) => event.skill_installation_event_id === receipt.skill_installation_event_id);
  const eventType = { enable: "skill.enabled", disable: "skill.disabled", activate: "skill.version.activated", uninstall: "skill.uninstalled" }[request.action];
  if (!target || !version || version.skill_installation_id !== original.skill_installation_id || receipt.skill_version_id !== target.skill_version_id || version.version !== target.version || version.content_hash !== target.content_hash || version.execution_mode !== target.execution_mode || events.length !== 1) return invalid();
  const event = events[0];
  if (event.skill_installation_id !== original.skill_installation_id || event.skill_version_id !== receipt.skill_version_id || event.event_type !== eventType || event.actor_ref !== receipt.actor_ref || event.payload?.request_id !== request.key || typeof event.payload?.request_hash !== "string" || !/^[a-f0-9]{64}$/.test(event.payload.request_hash)) return invalid();
}
