// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { webcrypto } from "node:crypto";
import { Composer } from "./App";
import { api, ApiError } from "./api";
import { messageStorageKey, messageSubmission as journal, starterContext, type MessageDraft, type MessageOwner } from "./messageSubmission";
import { legacyComposerEntries } from "./testComposerFixtures";
import type { AssetSetSnapshot, ComposerRegistryEntry } from "./types";

const owner: MessageOwner = { workspace_id: "workspace-1", user_id: "user-1", project_id: "project-1", conversation_id: "conversation-1" };
const key = "11111111-1111-4111-8111-111111111111";
const nextKey = "22222222-2222-4222-8222-222222222222";
const context = starterContext(owner.project_id, null, null);
const batch = {
  asset_set: { asset_set_id: "set-1", project_id: owner.project_id, current_version: 1, status: "sealed" },
  version: { asset_set_id: "set-1", asset_set_version_id: "set-version-1", version: 1, member_count: 1, completeness: { status: "complete", missing_episode_numbers: [], duplicate_episode_numbers: [], unrecognized_asset_ids: [], recognized_episode_numbers: [1], user_confirmed_upload_complete: true } },
  members: [{ asset_id: "asset-1", included: true, episode_no: 1, episode_order: 1, raw_name: "第1集.mp4" }],
} as AssetSetSnapshot;

function draft(capability: ComposerRegistryEntry | null = null): MessageDraft {
  const video = capability?.capability_id === "video_reference_creation";
  const videoBatch = video ? batch : null;
  return { content: "原始用户请求", capability, attachments: [{ asset_id: "asset-1", asset_snapshot_id: "snapshot-1", display_name: video ? "第1集.mp4" : "source.txt", kind: video ? "video" : "text" }], video_batch: videoBatch, context: starterContext(owner.project_id, capability, videoBatch), clientInstanceID: "original-client" };
}

function props(onSend = vi.fn().mockResolvedValue(undefined)) {
  return { projectID: owner.project_id, submissionOwner: owner, projectAssets: [], capabilities: legacyComposerEntries, runSnapshot: null, context, onClearSelection: vi.fn(), onSend };
}

function storageMethods(storage: Storage) {
  return { getItem: storage.getItem.bind(storage), setItem: storage.setItem.bind(storage), removeItem: storage.removeItem.bind(storage), clear: storage.clear.bind(storage) };
}

beforeEach(() => { sessionStorage.clear(); vi.stubGlobal("crypto", webcrypto); vi.spyOn(api, "getAssetSet").mockResolvedValue(batch); });
afterEach(() => { cleanup(); sessionStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("message submission persistence", () => {
  it("claims before dispatch without consuming the exact original request", () => {
    const original = journal.prepare(owner, draft(), "starter", key);
    const pending = journal.claim(original);
    expect(pending.phase).toBe("pending");
    expect(journal.read(owner)).toEqual(pending);
    expect(journal.prepare(owner, draft(), "starter", nextKey).key).toBe(key);
    journal.clear(pending);
    expect(journal.read(owner)).toBeNull();
    expect(() => journal.claim(pending)).toThrow(/记录已变化/);
    expect(journal.read(owner)).toBeNull();
  });

  it("does not overwrite another request or let old cleanup delete a new one", () => {
    const original = journal.prepare(owner, draft(), "starter", key);
    expect(() => journal.prepare(owner, { ...draft(), content: "new" }, "starter", nextKey)).toThrow(/记录已变化/);
    journal.clear(original);
    const next = journal.prepare(owner, { ...draft(), content: "new" }, "starter", nextKey);
    expect(() => journal.clear(original)).toThrow(/记录已变化/);
    expect(journal.read(owner)).toEqual(next);
  });

  it.each(["workspace_id", "user_id", "project_id", "conversation_id"] as const)("isolates %s even when request keys are identical", (field) => {
    journal.prepare(owner, draft(), "starter", key);
    const other = { ...owner, [field]: "other" };
    expect(journal.read(other)).toBeNull();
    const copied = sessionStorage.getItem(messageStorageKey(owner))!;
    sessionStorage.setItem(messageStorageKey(other), copied);
    expect(() => journal.read(other)).toThrow(/归属不匹配/);
    expect(sessionStorage.getItem(messageStorageKey(other))).toBe(copied);
  });

  it.each(["key", "schema", "phase", "draft", "owner"])("retains invalid %s instead of generating a replacement identity", (field) => {
    const original = journal.prepare(owner, draft(), "starter", key);
    const corrupt = JSON.stringify({ ...original, [field]: "invalid" });
    sessionStorage.setItem(messageStorageKey(owner), corrupt);
    expect(() => journal.read(owner)).toThrow(/未删除原记录/);
    expect(() => journal.prepare(owner, draft(), "starter", nextKey)).toThrow();
    expect(sessionStorage.getItem(messageStorageKey(owner))).toBe(corrupt);
  });

  it("rejects oversized metadata before writing", () => {
    expect(() => journal.prepare(owner, { ...draft(), content: "中".repeat(1024 * 1024) }, "starter", key)).toThrow();
    expect(journal.read(owner)).toBeNull();
  });

  it.each(["getItem", "setItem"] as const)("does not send a new message if %s fails", async (method) => {
    const view = props();
    render(<Composer {...view} />);
    fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "New request" } });
    const storage = sessionStorage;
    const denied = vi.fn(() => { throw new DOMException("denied", "SecurityError"); });
    vi.stubGlobal("sessionStorage", { ...storageMethods(storage), [method]: denied });
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await screen.findByText(/无法读取或保存待发送消息/);
    expect(view.onSend).not.toHaveBeenCalled();
    expect(denied).toHaveBeenCalled();
    vi.stubGlobal("sessionStorage", storage);
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() => expect(view.onSend).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(journal.read(owner)).toBeNull());
  });

  it.each(legacyComposerEntries)("restores $capability_id after remount without switching to a new version, target, context or key", async (capability) => {
    const original = journal.prepare(owner, draft(capability), "starter", key);
    const view = props(vi.fn().mockRejectedValueOnce(new ApiError("UNAVAILABLE", "response lost", 503)).mockResolvedValue(undefined));
    const first = render(<Composer {...view} />);
    await screen.findByText("response lost");
    expect(journal.read(owner)?.phase).toBe("pending");
    expect(view.onSend).toHaveBeenCalledTimes(1);
    first.unmount();
    const second = render(<Composer {...view} capabilities={[{ ...capability, version: "99.0.0" }]} context={{ ...context, view: { ...context.view, artifact_id: "later-artifact", artifact_version_id: "later-version" } }} />);
    await screen.findByText("上次发送结果未确认，已恢复原消息。");
    expect(view.onSend).toHaveBeenCalledTimes(1);
    expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", original.draft.content);
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() => expect(view.onSend).toHaveBeenCalledTimes(2));
    expect(view.onSend.mock.calls[1].slice(0, 4)).toEqual(view.onSend.mock.calls[0].slice(0, 4));
    expect(view.onSend.mock.calls[0][6].recovering).toBe(false);
    expect(view.onSend.mock.calls[1].slice(5)).toEqual([key, { context: original.draft.context, clientInstanceID: original.draft.clientInstanceID, recovering: true }]);
    await waitFor(() => expect(journal.read(owner)).toBeNull());
    second.unmount();
    render(<Composer {...view} />);
    expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", "");
    expect(view.onSend).toHaveBeenCalledTimes(2);
  });

  it("also restores a normal message with its original context after leaving the workbench", async () => {
    const view = props(vi.fn().mockRejectedValueOnce(new ApiError("UNAVAILABLE", "normal response lost", 503)).mockResolvedValue(undefined));
    const first = render(<Composer {...view} />);
    fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Normal message" } });
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await screen.findByText("normal response lost");
    const original = journal.read(owner)!;
    first.unmount();
    render(<Composer {...view} />);
    await screen.findByText("上次发送结果未确认，已恢复原消息。");
    expect(view.onSend).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() => expect(view.onSend).toHaveBeenCalledTimes(2));
    expect(view.onSend.mock.calls[1][5]).toBe(original.key);
    expect(view.onSend.mock.calls[1][6]).toEqual({ ...view.onSend.mock.calls[0][6], recovering: true });
    await waitFor(() => expect(journal.read(owner)).toBeNull());
  });

  it("does not silently substitute or drop an explicit Skill before the first dispatch", async () => {
    journal.prepare(owner, draft(legacyComposerEntries[0]), "starter", key);
    const view = props();
    render(<Composer {...view} capabilities={[]} />);
    await screen.findByText(/原消息未发送/);
    expect(view.onSend).not.toHaveBeenCalled();
    expect(journal.read(owner)?.phase).toBe("prepared");
  });

  it("keeps original data on stop and ignores a late response after an explicitly authorized new request", async () => {
    let finish!: () => void;
    const view = props(vi.fn().mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve; })).mockRejectedValue(new ApiError("UNAVAILABLE", "new response lost", 503)));
    journal.prepare(owner, draft(), "starter", key);
    render(<Composer {...view} />);
    await waitFor(() => expect(view.onSend).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: "停止等待" }));
    expect(journal.read(owner)?.key).toBe(key);
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValue(true);
    fireEvent.click(screen.getByRole("button", { name: "按新请求编辑" }));
    expect(journal.read(owner)?.key).toBe(key);
    fireEvent.click(screen.getByRole("button", { name: "按新请求编辑" }));
    fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "New request" } });
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await screen.findByText("new response lost");
    const next = journal.read(owner)!;
    expect(next.key).not.toBe(key);
    await act(async () => { finish(); });
    expect(journal.read(owner)?.key).toBe(next.key);
    expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", "New request");
    expect(confirm).toHaveBeenCalledTimes(2);
  });

  it("retries only local cleanup after a confirmed response, even after role loss", async () => {
    journal.prepare(owner, draft(), "starter", key);
    const view = props();
    const storage = sessionStorage;
    const remove = vi.fn(storage.removeItem.bind(storage)).mockImplementationOnce(() => { throw new DOMException("denied", "SecurityError"); });
    vi.stubGlobal("sessionStorage", { ...storageMethods(storage), removeItem: remove });
    const page = render(<Composer {...view} />);
    await screen.findByText("消息已提交，本地记录待清理。");
    expect(view.onSend).toHaveBeenCalledTimes(1);
    page.rerender(<Composer {...view} readOnly />);
    fireEvent.click(screen.getByRole("button", { name: "清理消息记录" }));
    await waitFor(() => expect(journal.read(owner)).toBeNull());
    expect(remove).toHaveBeenCalledTimes(2);
    expect(view.onSend).toHaveBeenCalledTimes(1);
  });

  it("never reads or submits another user's pending request", async () => {
    journal.prepare(owner, draft(), "starter", key);
    const view = props();
    render(<Composer {...view} submissionOwner={{ ...owner, user_id: "other" }} />);
    expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", "");
    expect(view.onSend).not.toHaveBeenCalled();
    expect(journal.read(owner)?.key).toBe(key);
  });

  it("does not auto-resubmit while the first page's request is still unresolved", async () => {
    journal.prepare(owner, draft(), "starter", key);
    let finish!: () => void;
    const view = props(vi.fn().mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve; })).mockResolvedValue(undefined));
    const first = render(<Composer {...view} />);
    await waitFor(() => expect(view.onSend).toHaveBeenCalledTimes(1));
    first.unmount();
    render(<Composer {...view} />);
    await screen.findByText("上次发送结果未确认，已恢复原消息。");
    expect(view.onSend).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() => expect(journal.read(owner)).toBeNull());
    const next = journal.prepare(owner, { ...draft(), content: "Next request" }, "composer", nextKey);
    await act(async () => { finish(); });
    expect(journal.read(owner)).toEqual(next);
    expect(view.onSend).toHaveBeenCalledTimes(2);
  });

  it("refuses a mismatched page target before exposing a persisted draft", () => {
    journal.prepare(owner, draft(), "starter", key);
    const view = props();
    render(<Composer {...view} projectID="another-project" />);
    expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", "");
    expect(screen.getByText("消息身份与当前作品不匹配，未读取或发送原消息。")).toBeTruthy();
    expect(view.onSend).not.toHaveBeenCalled();
    expect(journal.read(owner)?.key).toBe(key);
  });

  it("does not dispatch or replace a pending request while the owner is read-only", async () => {
    journal.prepare(owner, draft(), "starter", key);
    const view = props();
    const page = render(<Composer {...view} readOnly />);
    expect(screen.getByRole("button", { name: "发送" })).toHaveProperty("disabled", true);
    expect(screen.getByRole("button", { name: "按新请求编辑" })).toHaveProperty("disabled", true);
    expect(view.onSend).not.toHaveBeenCalled();
    page.rerender(<Composer {...view} />);
    await waitFor(() => expect(view.onSend).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(journal.read(owner)).toBeNull());
  });

  it("leaves unowned legacy records untouched and does not expose or auto-send their content", async () => {
    const legacy = JSON.stringify({ content: "private legacy draft", capability_id: null, attachments: [], video_batch: null });
    sessionStorage.setItem(`content-agent-starter-handoff:${owner.project_id}`, legacy);
    const view = props();
    render(<Composer {...view} />);
    await screen.findByText(/缺少身份和版本信息的旧版交接记录/);
    expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", "");
    expect(view.onSend).not.toHaveBeenCalled();
    expect(sessionStorage.getItem(`content-agent-starter-handoff:${owner.project_id}`)).toBe(legacy);
  });
});
