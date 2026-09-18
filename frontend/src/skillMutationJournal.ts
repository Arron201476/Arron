import type { SkillLifecycleRequest } from "./skillLifecycle";
import { skillArchiveBytes, skillBytesHash, type SkillPackageRequest } from "./skillPackage";

export type SkillMutationRequest = SkillLifecycleRequest | SkillPackageRequest;
export type SkillMutationOwner = { workspaceID: string; userID: string };
type SavedRecord = { owner: string; schema: 1; metadata: string; metadataHash: string; archive?: ArrayBuffer };
const databaseName = "content-agent-skill-commands", storeName = "pending";
const maxMetadataBytes = 2 * 1024 * 1024;

export class SkillJournalError extends Error {
  constructor(public code: "unavailable" | "invalid" | "changed", message: string) { super(message); }
}
const unavailable = () => new SkillJournalError("unavailable", "无法保存或读取本地待确认操作，已暂停新的 Skill 操作。请恢复浏览器存储后刷新状态。");
const invalid = () => new SkillJournalError("invalid", "本地待确认操作记录不完整或不匹配，已暂停新的 Skill 操作；原记录未删除。");
const ownerKey = (owner: SkillMutationOwner) => {
  if (!owner.workspaceID || !owner.userID) throw invalid();
  return JSON.stringify([owner.workspaceID, owner.userID]);
};
const object = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
const text = (value: unknown): value is string => typeof value === "string" && value.length > 0 && value.length < 8192;
function archiveBuffer(value: unknown): value is ArrayBuffer {
  // IndexedDB/structured clone may return a buffer from a different realm.
  try { return Object.getOwnPropertyDescriptor(ArrayBuffer.prototype, "byteLength")!.get!.call(value) <= 20 * 1024 * 1024; }
  catch { return false; }
}

function validateRequest(value: unknown, owner: SkillMutationOwner): asserts value is SkillMutationRequest {
  if (!object(value) || !text(value.key) || !/^[a-f\d]{8}-[a-f\d]{4}-[a-f\d]{4}-[a-f\d]{4}-[a-f\d]{12}$/i.test(value.key)) throw invalid();
  const isPackage = value.package === true;
  if (isPackage ? !["install_zip", "upgrade_zip", "adopt_directory", "update_directory"].includes(String(value.action)) : !["enable", "disable", "activate", "uninstall"].includes(String(value.action))) throw invalid();
  if (!isPackage || value.action === "upgrade_zip" || value.action === "update_directory") {
    const i = value.installation;
    if (!object(i) || i.workspace_id !== owner.workspaceID || !text(i.skill_installation_id) || !text(i.skill_name) || !text(i.capability_id) || !text(i.active_version_id) || !text(i.status) || typeof i.enabled !== "boolean" || !["workspace", "user", "project"].includes(String(i.scope)) || !text(i.scope_ref) || i.scope === "workspace" && i.scope_ref !== owner.workspaceID || i.scope === "user" && i.scope_ref !== owner.userID || !Array.isArray(i.versions) || !Array.isArray(i.events) || i.versions.length === 0 || i.events.length === 0) throw invalid();
    if (i.versions.some((v) => !object(v) || !text(v.skill_version_id) || v.skill_installation_id !== i.skill_installation_id || !text(v.version) || !text(v.content_hash) || !text(v.execution_mode)) || i.events.some((e) => !object(e) || !text(e.skill_installation_event_id) || e.skill_installation_id !== i.skill_installation_id)) throw invalid();
    if (i.versions.filter((v) => v.skill_version_id === i.active_version_id).length !== 1 || new Set(i.events.map((e) => e.skill_installation_event_id)).size !== i.events.length) throw invalid();
    if (value.action === "activate" && (!text(value.version) || i.versions.filter((v) => v.version === value.version).length !== 1)) throw invalid();
    if (value.action === "update_directory") {
      const p = value.preview;
      if (!object(p) || p.skill_installation_id !== i.skill_installation_id || p.current_version_id !== i.active_version_id || p.capability_id !== i.capability_id || !text(p.version) || !text(p.content_hash)) throw invalid();
    }
  } else {
    const target = value.target;
    if (!object(target) || !["workspace", "user", "project"].includes(String(target.scope)) || (target.scope === "project" ? !text(target.project_id) : target.project_id !== undefined)) throw invalid();
    if (value.action === "adopt_directory" && (!text(value.capabilityID) || !text(value.version) || !text(value.contentHash))) throw invalid();
  }
  if ((value.action === "install_zip" || value.action === "upgrade_zip") && !(value.file instanceof File)) throw invalid();
}

async function encode(owner: SkillMutationOwner, request: SkillMutationRequest): Promise<SavedRecord> {
  validateRequest(request, owner);
  const { hadUnknownResult: _unknown, ...fields } = request;
  let archive: ArrayBuffer | undefined;
  let fileMetadata: Record<string, unknown> | undefined;
  if ("file" in request) {
    archive = await skillArchiveBytes(request.file);
    fileMetadata = { name: request.file.name, type: request.file.type, lastModified: request.file.lastModified, size: archive.byteLength, hash: await skillBytesHash(archive) };
  }
  const metadata = JSON.stringify({ owner: { workspaceID: owner.workspaceID, userID: owner.userID }, request: { ...fields, ...(fileMetadata ? { file: fileMetadata } : {}) } });
  const bytes = new TextEncoder().encode(metadata);
  if (bytes.byteLength > maxMetadataBytes) throw invalid();
  return { schema: 1, owner: ownerKey(owner), metadata, metadataHash: await skillBytesHash(bytes.buffer), ...(archive ? { archive } : {}) };
}

async function decode(owner: SkillMutationOwner, raw: unknown): Promise<SkillMutationRequest> {
  if (!object(raw) || raw.schema !== 1 || raw.owner !== ownerKey(owner) || typeof raw.metadata !== "string" || raw.metadata.length > maxMetadataBytes || typeof raw.metadataHash !== "string") throw invalid();
  const bytes = new TextEncoder().encode(raw.metadata);
  if (bytes.byteLength > maxMetadataBytes || await skillBytesHash(bytes.buffer) !== raw.metadataHash) throw invalid();
  let saved: unknown;
  try { saved = JSON.parse(raw.metadata); } catch { throw invalid(); }
  if (!object(saved) || !object(saved.owner) || saved.owner.workspaceID !== owner.workspaceID || saved.owner.userID !== owner.userID || !object(saved.request)) throw invalid();
  const request = saved.request;
  if (request.action === "install_zip" || request.action === "upgrade_zip") {
    const f = request.file;
    if (!object(f) || !text(f.name) || !f.name.toLowerCase().endsWith(".zip") || typeof f.type !== "string" || !Number.isSafeInteger(f.lastModified) || !archiveBuffer(raw.archive) || raw.archive.byteLength !== f.size || await skillBytesHash(raw.archive) !== f.hash) throw invalid();
    request.file = new File([raw.archive], f.name, { type: f.type, lastModified: f.lastModified as number });
  } else if (raw.archive !== undefined || request.file !== undefined) throw invalid();
  validateRequest(request, owner);
  request.hadUnknownResult = true;
  return request;
}

function openDatabase(factory: IDBFactory): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    let settled = false;
    const timer = setTimeout(() => { settled = true; reject(unavailable()); }, 10000);
    const fail = () => { clearTimeout(timer); settled = true; reject(unavailable()); };
    try {
      const request = factory.open(databaseName, 1);
      request.onupgradeneeded = () => { request.result.createObjectStore(storeName, { keyPath: "owner" }); };
      request.onerror = fail;
      request.onblocked = fail;
      request.onsuccess = () => {
        clearTimeout(timer);
        const db = request.result;
        db.onversionchange = () => db.close();
        if (settled) db.close(); else { settled = true; resolve(db); }
      };
    } catch { fail(); }
  });
}

// The read/claim/delete decision stays in one IndexedDB transaction. A second
// tab cannot replace a pending command, and a late completion cannot delete it.
async function transaction(owner: SkillMutationOwner, mode: IDBTransactionMode, operation: (store: IDBObjectStore, existing: unknown) => unknown, factory: IDBFactory): Promise<unknown> {
  const key = ownerKey(owner), db = await openDatabase(factory);
  try {
    return await new Promise((resolve, reject) => {
      let result: unknown, reason: unknown;
      const tx = db.transaction(storeName, mode, { durability: "strict" });
      const timer = setTimeout(() => { reason = unavailable(); try { tx.abort(); } catch { reject(reason); } }, 10000);
      tx.oncomplete = () => { clearTimeout(timer); resolve(result); };
      tx.onabort = () => { clearTimeout(timer); reject(reason ?? unavailable()); };
      tx.onerror = () => { reason ??= unavailable(); };
      const store = tx.objectStore(storeName), get = store.get(key);
      get.onsuccess = () => {
        try { result = operation(store, get.result); }
        catch (error) { reason = error; tx.abort(); }
      };
    });
  } catch (error) { throw error instanceof SkillJournalError ? error : unavailable(); }
  finally { db.close(); }
}

export const skillMutationJournal = {
  async read(owner: SkillMutationOwner): Promise<SkillMutationRequest | null> {
    const raw = await transaction(owner, "readonly", (_store, value) => value, globalThis.indexedDB);
    return raw === undefined ? null : decode(owner, raw);
  },
  async claim(owner: SkillMutationOwner, request: SkillMutationRequest): Promise<{ accepted: boolean; request: SkillMutationRequest }> {
    const factory = globalThis.indexedDB;
    const record = await encode(owner, request);
    const raw = await transaction(owner, "readwrite", (store, existing) => { if (existing === undefined) { store.add(record); return record; } return existing; }, factory);
    const restored = await decode(owner, raw);
    const accepted = object(raw) && raw.metadataHash === record.metadataHash && raw.metadata === record.metadata;
    return { accepted, request: restored };
  },
  async requireCurrent(owner: SkillMutationOwner, request: SkillMutationRequest): Promise<void> {
    const factory = globalThis.indexedDB;
    const record = await encode(owner, request);
    const raw = await transaction(owner, "readonly", (_store, value) => value, factory);
    if (raw === undefined) throw new SkillJournalError("changed", "本地原操作记录已变化，已停止重试。请刷新状态核对。");
    await decode(owner, raw);
    if (!object(raw) || raw.metadataHash !== record.metadataHash || raw.metadata !== record.metadata) throw new SkillJournalError("changed", "另一个页面已有不同的待确认操作，请刷新状态核对。");
  },
  async settle(owner: SkillMutationOwner, request: SkillMutationRequest): Promise<SkillMutationRequest | null> {
    const factory = globalThis.indexedDB;
    const record = await encode(owner, request);
    const raw = await transaction(owner, "readwrite", (store, existing) => {
      if (existing === undefined) return undefined;
      if (!object(existing) || existing.metadataHash !== record.metadataHash || existing.metadata !== record.metadata) return existing;
      store.delete(record.owner);
      return undefined;
    }, factory);
    return raw === undefined ? null : decode(owner, raw);
  },
};
