import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { skillMutationJournal as journal, type SkillMutationRequest } from "./skillMutationJournal";
import type { SkillInstallation } from "./types";

const owner = { workspaceID: "workspace-1", userID: "user-1" };
const key = "a0000000-0000-4000-8000-000000000001", anotherKey = "a0000000-0000-4000-8000-000000000002";
const installation = {
  skill_installation_id: "skill-1", workspace_id: owner.workspaceID, scope: "workspace", scope_ref: owner.workspaceID,
  skill_name: "test-skill", capability_id: "test_skill", status: "installed", enabled: true, active_version_id: "version-1",
  versions: [{ skill_version_id: "version-1", skill_installation_id: "skill-1", version: "1.0.0", content_hash: "sha256:one", execution_mode: "inline" }],
  events: [{ skill_installation_event_id: "event-1", skill_installation_id: "skill-1" }],
} as SkillInstallation;

beforeEach(() => vi.stubGlobal("indexedDB", new IDBFactory()));
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

function requestFor(action: SkillMutationRequest["action"]): SkillMutationRequest {
  const file = new File([new Uint8Array([0, 255, 128, 99])], "中文 原包.zip", { type: "application/zip", lastModified: 1234 });
  switch (action) {
    case "install_zip": return { package: true, action, key, file, target: { scope: "project", project_id: "project-1" } };
    case "upgrade_zip": return { package: true, action, key, file, installation };
    case "adopt_directory": return { package: true, action, key, capabilityID: "test_skill", version: "1.0.0", contentHash: "sha256:one", target: { scope: "user" } };
    case "update_directory": return { package: true, action, key, installation, preview: { skill_installation_id: "skill-1", current_version_id: "version-1", capability_id: "test_skill", version: "2.0.0", content_hash: "sha256:two", status: "available" } };
    default: return { action, key, installation, ...(action === "activate" ? { version: "1.0.0" } : {}) };
  }
}

async function editRawRecord(edit: (record: Record<string, unknown>) => void) {
  await new Promise<void>((resolve, reject) => {
    const open = indexedDB.open("content-agent-skill-commands", 1);
    open.onerror = () => reject(open.error);
    open.onsuccess = () => {
      const db = open.result, tx = db.transaction("pending", "readwrite"), store = tx.objectStore("pending");
      const get = store.get(JSON.stringify([owner.workspaceID, owner.userID]));
      get.onsuccess = () => { edit(get.result); store.put(get.result); };
      tx.oncomplete = () => { db.close(); resolve(); };
      tx.onabort = () => { db.close(); reject(tx.error); };
    };
  });
}

describe("durable Skill mutation journal", () => {
  it.each(["enable", "disable", "activate", "uninstall", "install_zip", "upgrade_zip", "adopt_directory", "update_directory"] as const)("restores the exact %s request through independent database connections", async (action) => {
    const original = requestFor(action);
    expect(await journal.read(owner)).toBeNull();
    expect((await journal.claim(owner, original)).accepted).toBe(true);
    const restored = await journal.read(owner);
    expect(restored).toMatchObject({ key, action, hadUnknownResult: true });
    expect(restored).not.toBe(original);
    if ("file" in original && restored && "file" in restored) {
      expect(restored.file).toBeInstanceOf(File);
      expect(restored.file.name).toBe(original.file.name);
      expect(restored.file.type).toBe(original.file.type);
      expect(restored.file.lastModified).toBe(original.file.lastModified);
      expect(new Uint8Array(await restored.file.arrayBuffer())).toEqual(new Uint8Array([0, 255, 128, 99]));
    }
    await journal.requireCurrent(owner, restored!);
    expect((await journal.claim(owner, restored!)).accepted).toBe(true);
    expect(await journal.settle(owner, restored!)).toBeNull();
    expect(await journal.read(owner)).toBeNull();
  });

  it("serializes competing pages without replacing either original payload", async () => {
    const one = requestFor("install_zip"), two = { ...requestFor("uninstall"), key: anotherKey };
    const claims = await Promise.all([journal.claim(owner, one), journal.claim(owner, two)]);
    expect(claims.filter((result) => result.accepted)).toHaveLength(1);
    const winner = claims[0].request;
    expect(claims[1].request.key).toBe(winner.key);
    const loser = winner.key === one.key ? two : one;
    expect((await journal.settle(owner, loser))?.key).toBe(winner.key);
    expect((await journal.read(owner))?.key).toBe(winner.key);
  });

  it("does not recreate a cleared original request during retry", async () => {
    const original = requestFor("disable");
    await journal.claim(owner, original);
    const restored = await journal.read(owner);
    await journal.settle(owner, original);
    await expect(journal.requireCurrent(owner, restored!)).rejects.toMatchObject({ code: "changed" });
    expect(await journal.read(owner)).toBeNull();
  });

  it("keeps separate users and workspaces isolated, including identical keys", async () => {
    const request = requestFor("adopt_directory");
    await journal.claim(owner, request);
    for (const other of [{ ...owner, userID: "other-user" }, { ...owner, workspaceID: "other-workspace" }]) {
      expect(await journal.read(other)).toBeNull();
      expect((await journal.claim(other, { ...request, key: anotherKey })).accepted).toBe(true);
      await journal.settle(other, { ...request, key: anotherKey });
      expect((await journal.read(owner))?.key).toBe(key);
    }
    await expect(journal.claim({ ...owner, userID: "" }, request)).rejects.toMatchObject({ code: "invalid" });
  });

  it.each(["metadata", "metadataHash", "archive", "schema"])("retains a corrupt %s record instead of deleting or replacing it", async (field) => {
    await journal.claim(owner, requestFor("install_zip"));
    await editRawRecord((raw) => { if (field === "archive") raw.archive = new Uint8Array([1, 2, 3]).buffer; else raw[field] = "corrupt"; });
    await expect(journal.read(owner)).rejects.toMatchObject({ code: "invalid" });
    await expect(journal.claim(owner, { ...requestFor("disable"), key: anotherKey })).rejects.toMatchObject({ code: "invalid" });
    await expect(journal.read(owner)).rejects.toMatchObject({ code: "invalid" });
  });

  it("rejects forged installation scope and unknown actions before storing", async () => {
    const valid = requestFor("disable");
    await expect(journal.claim(owner, { ...valid, installation: { ...installation, workspace_id: "wrong" } })).rejects.toMatchObject({ code: "invalid" });
    await expect(journal.claim(owner, { ...valid, action: "run_shell" } as unknown as SkillMutationRequest)).rejects.toMatchObject({ code: "invalid" });
    expect(await journal.read(owner)).toBeNull();
  });

  it("does not report success when an add succeeds but its transaction aborts", async () => {
    const add = IDBObjectStore.prototype.add;
    vi.spyOn(IDBObjectStore.prototype, "add").mockImplementationOnce(function (...args) {
      const result = add.apply(this, args);
      result.addEventListener("success", () => this.transaction.abort());
      return result;
    });
    await expect(journal.claim(owner, requestFor("disable"))).rejects.toMatchObject({ code: "unavailable" });
    expect(await journal.read(owner)).toBeNull();
  });

  it("does not discard the original after a deletion fails", async () => {
    const request = requestFor("upgrade_zip");
    await journal.claim(owner, request);
    vi.spyOn(IDBObjectStore.prototype, "delete").mockImplementationOnce(() => { throw new DOMException("full", "QuotaExceededError"); });
    await expect(journal.settle(owner, request)).rejects.toMatchObject({ code: "unavailable" });
    expect((await journal.read(owner))?.key).toBe(key);
    await journal.settle(owner, request);
    expect(await journal.read(owner)).toBeNull();
  });

  it("fails closed when IndexedDB is disabled", async () => {
    vi.stubGlobal("indexedDB", undefined);
    await expect(journal.read(owner)).rejects.toMatchObject({ code: "unavailable" });
    await expect(journal.claim(owner, requestFor("disable"))).rejects.toMatchObject({ code: "unavailable" });
  });
});
