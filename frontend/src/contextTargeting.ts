import type { AgentComposerContext, AgentTargetEntity, AgentTargetSelection, ProjectActivity } from "./types";

export type SelectionCaptureBase = {
  artifactID: string;
  artifactType: string;
  artifactLabel: string;
  scopeKey?: string;
  fieldPath?: string;
  entity?: AgentTargetEntity;
};

export function captureDOMSelection(root: HTMLElement, base: SelectionCaptureBase): AgentTargetSelection | null {
  const selection = window.getSelection();
  if (!selection || selection.rangeCount !== 1 || selection.isCollapsed) return null;
  const range = selection.getRangeAt(0);
  if (!root.contains(range.commonAncestorContainer)) return null;
  const selectedText = selection.toString();
  if (!selectedText.trim()) return null;

  const beforeRange = range.cloneRange();
  beforeRange.selectNodeContents(root);
  beforeRange.setEnd(range.startContainer, range.startOffset);
  const start = beforeRange.toString().length;
  const end = start + selectedText.length;
  const fullText = root.innerText || root.textContent || "";
  const targetElement = elementFromNode(range.startContainer)?.closest<HTMLElement>("[data-field-path], [data-line-id], [data-scene-id]");
  const entity = compactEntity({
    ...base.entity,
    episode_id: targetElement?.dataset.episodeId ?? base.entity?.episode_id,
    scene_id: targetElement?.dataset.sceneId ?? base.entity?.scene_id,
    line_id: targetElement?.dataset.lineId ?? base.entity?.line_id,
    block_type: targetElement?.dataset.blockType ?? base.entity?.block_type,
    entity_id: targetElement?.dataset.entityId ?? base.entity?.entity_id,
  });
  const fieldPath = targetElement?.dataset.fieldPath ?? base.fieldPath;
  const locationLabel = targetElement?.dataset.locationLabel || targetLocationLabel(entity, fieldPath);

  return {
    schema_version: "1.0.0",
    artifact_id: base.artifactID,
    artifact_type: base.artifactType,
    target_scope: "selection",
    ...(base.scopeKey ? { scope_key: base.scopeKey } : {}),
    ...(fieldPath ? { field_path: fieldPath } : {}),
    ...(Object.keys(entity).length ? { entity } : {}),
    text_range: {
      start,
      end,
      selected_text: selectedText,
      before_context: fullText.slice(Math.max(0, start - 240), start),
      after_context: fullText.slice(end, end + 240),
    },
    display: {
      artifact_label: base.artifactLabel,
      ...(locationLabel ? { location_label: locationLabel } : {}),
      selected_text_summary: summarizeSelection(selectedText),
    },
  };
}

export async function messageContextPayload(context: AgentComposerContext | null) {
  if (!context) return { selection_snapshot: null, client_context: defaultClientContext() };
  const { view, selection } = context;
  return {
    selection_snapshot: selection && view.artifact_version_id ? {
      artifact_version_id: view.artifact_version_id,
      snapshot_hash: await sha256Hex(canonicalJSONStringify(selection)),
      selection,
    } : null,
    client_context: {
      ...defaultClientContext(),
      ...(view.artifact_id ? { current_artifact_id: view.artifact_id } : {}),
      ...(view.artifact_version_id ? { current_artifact_version_id: view.artifact_version_id } : {}),
      ...(view.run_id ? { viewed_run_id: view.run_id } : {}),
      ...(view.capability_id ? { viewed_capability_id: view.capability_id } : {}),
      ...(view.scope_key ? { current_scope_key: view.scope_key } : {}),
      ...(view.asset_set_version_id ? { current_asset_set_version_id: view.asset_set_version_id } : {}),
    },
  };
}

export function targetSummary(selection: AgentTargetSelection) {
  return [selection.display.artifact_label, selection.display.location_label].filter(Boolean).join(" · ");
}

export function episodeNumberFromScope(scopeKey?: string | null): number | null {
  if (!scopeKey) return null;
  const match = scopeKey.match(/(?:episode|ep|第)[^0-9]*(\d+)/i) ?? scopeKey.match(/^\s*(\d+)\s*$/);
  if (!match) return null;
  const value = Number(match[1]);
  return Number.isInteger(value) && value > 0 ? value : null;
}

export function resolveViewedCapabilityID(
  artifactRunID: string | null | undefined,
  activities: ProjectActivity[],
  activeWriteRunID: string | null,
  currentCapabilityID: string | null,
): string | null {
  if (!artifactRunID) return null;
  const runCapability = activities.find((item) => item.run_id === artifactRunID && item.capability_id)?.capability_id;
  if (runCapability) return runCapability;
  return artifactRunID === activeWriteRunID ? currentCapabilityID : null;
}

export function canonicalJSONStringify(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJSONStringify).join(",")}]`;
  if (value && typeof value === "object") {
    const record = value as Record<string, unknown>;
    return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSONStringify(record[key])}`).join(",")}}`;
  }
  return JSON.stringify(value) ?? "null";
}

function defaultClientContext() {
  return { locale: "zh-CN", timezone: "Asia/Shanghai", surface: "web", current_view: "workbench" };
}

export async function sha256Hex(value: string) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return [...new Uint8Array(digest)].map((item) => item.toString(16).padStart(2, "0")).join("");
}

function elementFromNode(node: Node): Element | null {
  return node.nodeType === Node.ELEMENT_NODE ? node as Element : node.parentElement;
}

function compactEntity(entity: AgentTargetEntity): AgentTargetEntity {
  return Object.fromEntries(Object.entries(entity).filter(([, value]) => Boolean(value))) as AgentTargetEntity;
}

function targetLocationLabel(entity: AgentTargetEntity, fieldPath?: string) {
  const parts = [
    entity.episode_id ? `第 ${entity.episode_id} 集` : "",
    entity.scene_id ? `场景 ${entity.scene_id}` : "",
    entity.line_id ? `台词 ${entity.line_id}` : "",
    !entity.episode_id && !entity.scene_id && !entity.line_id && fieldPath ? fieldPath : "",
  ];
  return parts.filter(Boolean).join(" / ");
}

function summarizeSelection(value: string) {
  const compact = value.replace(/\s+/g, " ").trim();
  return compact.length > 48 ? `${compact.slice(0, 48)}…` : compact;
}
