import { canonicalJSONStringify } from "./contextTargeting";
import { createUUID } from "./uuid";
import type { AgentComposerContext, AssetSetSnapshot, AttachmentRef, ComposerRegistryEntry } from "./types";

export type MessageOwner = { workspace_id: string; user_id: string; project_id: string; conversation_id: string };
export type MessageBinding = { context: AgentComposerContext; clientInstanceID: string; recovering?: boolean };
export type MessageDraft = Pick<MessageBinding, "context" | "clientInstanceID"> & { content: string; capability: ComposerRegistryEntry | null; attachments: AttachmentRef[]; video_batch: AssetSetSnapshot | null };
export type SavedMessage = { schema: 1; owner: MessageOwner; key: string; origin: "starter" | "composer"; phase: "prepared" | "pending"; draft: MessageDraft };
export type SendMessage = (content: string, capability: ComposerRegistryEntry | null, attachments: AttachmentRef[], videoBatch: AssetSetSnapshot | null, signal: AbortSignal, key?: string, binding?: MessageBinding) => Promise<void>;

export class MessageStorageError extends Error {}
const unavailable = () => new MessageStorageError("无法读取或保存待发送消息，已停止提交。请恢复浏览器存储后重试。");
const invalid = () => new MessageStorageError("本地待发送消息记录不完整或归属不匹配，未发送且未删除原记录。");
const changed = () => new MessageStorageError("待发送消息记录已变化，未提交新请求。请刷新后核对原消息。");
const maxBytes = 2 * 1024 * 1024;
const object = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === "object" && !Array.isArray(value);
const text = (value: unknown): value is string => typeof value === "string" && value.length > 0 && value.length <= maxBytes;

export function messageStorageKey(owner: MessageOwner): string {
  if (![owner.workspace_id, owner.user_id, owner.project_id, owner.conversation_id].every(text)) throw invalid();
  return `content-agent-message-command:${JSON.stringify([owner.workspace_id, owner.user_id, owner.project_id, owner.conversation_id])}`;
}

export function composerSkillIdentity(entry: ComposerRegistryEntry) {
  return canonicalJSONStringify([entry.capability_id, entry.version, entry.skill?.scope, entry.skill?.content_hash]);
}

export function starterContext(projectID: string, capability: ComposerRegistryEntry | null, batch: AssetSetSnapshot | null): AgentComposerContext {
  return { view: { project_id: projectID, artifact_id: null, artifact_version_id: null, artifact_type: null, run_id: null, capability_id: capability?.capability_id ?? null, scope_key: null, artifact_label: null, asset_set_version_id: batch?.version.asset_set_version_id ?? null }, selection: null };
}

function validate(raw: unknown, owner: MessageOwner): asserts raw is SavedMessage {
  if (!object(raw) || raw.schema !== 1 || !object(raw.owner) || canonicalJSONStringify(raw.owner) !== canonicalJSONStringify(owner) || !text(raw.key) || !/^[a-f\d]{8}-[a-f\d]{4}-[a-f\d]{4}-[a-f\d]{4}-[a-f\d]{12}$/i.test(raw.key) || !["starter", "composer"].includes(String(raw.origin)) || !["prepared", "pending"].includes(String(raw.phase)) || !object(raw.draft)) throw invalid();
  const d = raw.draft;
  if (!text(d.content) || !d.content.trim() || !text(d.clientInstanceID) || !Array.isArray(d.attachments) || d.attachments.length > 256 || d.attachments.some((a) => !object(a) || !text(a.asset_id) || !text(a.asset_snapshot_id) || !text(a.display_name) || a.kind !== undefined && !text(a.kind)) || new Set(d.attachments.map((a) => a.asset_id)).size !== d.attachments.length) throw invalid();
  if (d.capability !== null) {
    const c = d.capability;
    if (!object(c) || ![c.capability_id, c.version, c.label, c.execution_mode, c.view_key, c.status].every(text) || !Array.isArray(c.accepted_asset_kinds) || !c.accepted_asset_kinds.every(text)) throw invalid();
    if (c.skill != null && (!object(c.skill) || !text(c.skill.scope) || !text(c.skill.content_hash) || c.skill.dependencies !== undefined && !Array.isArray(c.skill.dependencies))) throw invalid();
  }
  const c = d.context;
  if (!object(c) || !object(c.view) || c.view.project_id !== owner.project_id || ["artifact_id", "artifact_version_id", "artifact_type", "run_id", "capability_id", "scope_key", "artifact_label"].some((key) => c.view && (c.view as Record<string, unknown>)[key] !== null && typeof (c.view as Record<string, unknown>)[key] !== "string")) throw invalid();
  if (c.selection !== null && (!object(c.selection) || !text(c.selection.artifact_id) || !object(c.selection.display) || !text(c.selection.display.artifact_label) || !text(c.selection.display.selected_text_summary) || !c.view.artifact_version_id)) throw invalid();
  if (d.video_batch !== null) {
    const b = d.video_batch;
    if (!object(b) || !object(b.asset_set) || !object(b.version) || b.asset_set.project_id !== owner.project_id || b.asset_set.status !== "sealed" || !text(b.asset_set.asset_set_id) || b.version.asset_set_id !== b.asset_set.asset_set_id || !text(b.version.asset_set_version_id) || b.version.version !== b.asset_set.current_version || !Array.isArray(b.members) || !b.members.length || b.members.some((m) => !object(m) || !text(m.asset_id) || typeof m.included !== "boolean") || !object(b.version.completeness)) throw invalid();
  }
}

function serialized(record: SavedMessage): string {
  validate(record, record.owner);
  const value = JSON.stringify(record);
  if (new TextEncoder().encode(value).byteLength > maxBytes) throw invalid();
  return value;
}

function read(storage: Storage, owner: MessageOwner): SavedMessage | null {
  const value = storage.getItem(messageStorageKey(owner));
  if (value === null) return null;
  if (value.length > maxBytes || new TextEncoder().encode(value).byteLength > maxBytes) throw invalid();
  let raw: unknown;
  try { raw = JSON.parse(value); } catch { throw invalid(); }
  validate(raw, owner);
  return raw;
}

function withStorage<T>(operation: (storage: Storage) => T): T {
  try { return operation(globalThis.sessionStorage); }
  catch (error) { throw error instanceof MessageStorageError ? error : unavailable(); }
}

const sameRequest = (left: SavedMessage, right: SavedMessage) => canonicalJSONStringify({ ...left, phase: "pending" }) === canonicalJSONStringify({ ...right, phase: "pending" });

// sessionStorage is tab-scoped. Claim synchronously before dispatch so StrictMode
// and a new mount see "pending", but retain the original bytes for explicit retry.
export const messageSubmission = {
  read: (owner: MessageOwner) => withStorage((storage) => read(storage, owner)),
  hasLegacy: (projectID: string) => withStorage((storage) => storage.getItem(`content-agent-starter-handoff:${projectID}`) !== null),
  prepare(owner: MessageOwner, draft: MessageDraft, origin: SavedMessage["origin"], key = createUUID()): SavedMessage {
    return withStorage((storage) => {
      const record: SavedMessage = { schema: 1, owner, draft, origin, key, phase: "prepared" };
      const value = serialized(record), previous = read(storage, owner);
      if (previous) {
        if (canonicalJSONStringify(previous.draft) === canonicalJSONStringify(draft) && previous.origin === origin) return previous;
        throw changed();
      }
      storage.setItem(messageStorageKey(owner), value);
      return JSON.parse(value) as SavedMessage;
    });
  },
  claim(record: SavedMessage): SavedMessage {
    return withStorage((storage) => {
      const previous = read(storage, record.owner);
      if (!previous || !sameRequest(previous, record)) throw changed();
      const pending = { ...previous, phase: "pending" as const };
      storage.setItem(messageStorageKey(record.owner), serialized(pending));
      return pending;
    });
  },
  clear(record: SavedMessage): void {
    withStorage((storage) => {
      const previous = read(storage, record.owner);
      if (!previous) return;
      if (!sameRequest(previous, record)) throw changed();
      storage.removeItem(messageStorageKey(record.owner));
    });
  },
};
