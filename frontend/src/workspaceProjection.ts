import type { Approval, ArtifactPresentation, ComposerRegistryEntry, WorkspaceRegistries } from "./types";

export type ComposerViewKey = "source_materials" | "video_asset_set" | "inspector";
export type ConfigViewKey = "script_generation" | "script_continuation" | "video_extraction" | "json_schema" | "none" | "inspector";
export type ApprovalViewKey = "quality_review" | "adaptation_strategy" | "single_option" | "volume_fit" | "action_list" | "inspector";
export type TaskViewKey = "task_progress" | "task_result" | "task_failure" | "inspector";
export type AgentToolViewKey = "agent_tool_approval" | "inspector";

const composerViewKeys = new Set<ComposerViewKey>(["source_materials", "video_asset_set", "inspector"]);
const configViewKeys = new Set<ConfigViewKey>(["script_generation", "script_continuation", "video_extraction", "json_schema", "none", "inspector"]);
const approvalViewKeys = new Set<ApprovalViewKey>(["quality_review", "adaptation_strategy", "single_option", "volume_fit", "action_list", "inspector"]);
const taskViewKeys = new Set<TaskViewKey>(["task_progress", "task_result", "task_failure", "inspector"]);
const agentToolViewKeys = new Set<AgentToolViewKey>(["agent_tool_approval", "inspector"]);
const artifactViewKeys = new Set<ArtifactPresentation["renderer"]>(["document", "episode_plan", "script", "script_collection", "video_script", "table", "media", "form"]);

export function trustedComposerViewKey(value: string): ComposerViewKey {
  return composerViewKeys.has(value as ComposerViewKey) ? value as ComposerViewKey : "inspector";
}

export function trustedConfigViewKey(value: string): ConfigViewKey {
  return configViewKeys.has(value as ConfigViewKey) ? value as ConfigViewKey : "inspector";
}

export function capabilityLabel(entries: ComposerRegistryEntry[], capabilityID: string | null | undefined, fallback: string): string {
  if (!capabilityID) return fallback;
  return entries.find((entry) => entry.capability_id === capabilityID)?.label ?? fallback;
}

export function capabilityEntry(entries: ComposerRegistryEntry[], capabilityID: string | null | undefined): ComposerRegistryEntry | null {
  if (!capabilityID) return null;
  return entries.find((entry) => entry.capability_id === capabilityID) ?? null;
}

export function capabilityAccepts(entry: ComposerRegistryEntry | null, assetKind: string): boolean {
  return Boolean(entry?.accepted_asset_kinds.includes(assetKind));
}

export function capabilityDependenciesReady(entry: ComposerRegistryEntry): boolean {
  return entry.status === "available" && (entry.skill?.dependencies ?? []).every((dependency) => !dependency.status || dependency.status === "available");
}

export function resolveApprovalViewKey(registry: WorkspaceRegistries["approvals"], approval: Approval): ApprovalViewKey {
  const match = registry.find((entry) => {
    if (entry.match.scope && entry.match.scope !== approval.scope) return false;
    if (entry.match.subject_kind && entry.match.subject_kind !== approval.subject_kind) return false;
    if (entry.match.option && !approval.options.includes(entry.match.option)) return false;
    return true;
  });
  if (!match || !approvalViewKeys.has(match.view_key as ApprovalViewKey)) return "inspector";
  return match.view_key as ApprovalViewKey;
}

export function resolveTaskViewKey(registry: WorkspaceRegistries["tasks"], status: string): TaskViewKey {
  const viewKey = registry.find((entry) => entry.status === status)?.view_key;
  return taskViewKeys.has(viewKey as TaskViewKey) ? viewKey as TaskViewKey : "inspector";
}

export function resolveAgentToolInteraction(registry: WorkspaceRegistries["interactions"]): { viewKey: AgentToolViewKey; commands: string[] } {
  const entry = registry.find((item) => item.registry_key === "agent_tool");
  if (!entry || !agentToolViewKeys.has(entry.view_key as AgentToolViewKey)) {
    return { viewKey: "inspector", commands: [] };
  }
  const commands = entry.commands.filter((command) => command === "approve" || command === "deny");
  return { viewKey: entry.view_key as AgentToolViewKey, commands };
}

export function artifactPresentationsFromRegistries(registries: WorkspaceRegistries): ArtifactPresentation[] {
  const navigation = new Map(registries.navigation.map((entry) => [entry.artifact_type, entry]));
  return registries.artifacts.map((entry) => {
    const navigationEntry = navigation.get(entry.artifact_type);
    const trusted = artifactViewKeys.has(entry.view_key as ArtifactPresentation["renderer"]);
    return {
      artifact_type: entry.artifact_type,
      label: entry.label,
      description: entry.description,
      renderer: trusted ? entry.view_key as ArtifactPresentation["renderer"] : "document",
      editable: trusted && entry.editable,
      preferred_fields: entry.preferred_fields,
      navigation: {
        order: navigationEntry?.order ?? Number.MAX_SAFE_INTEGER,
        group_mode: navigationEntry?.group_mode === "episode_directory" ? "episode_directory" : "single",
        visibility: navigationEntry?.visibility === "internal" ? "internal" : "user",
      },
      available_actions: trusted ? entry.available_actions : ["inspect"],
      collection_member_type: entry.collection_member_type,
    };
  });
}
