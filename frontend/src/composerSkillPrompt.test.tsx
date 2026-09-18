// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Composer, episodeNoFromFilename } from "./App";
import { api, ApiError } from "./api";
import { dynamicStoryEntry, legacyComposerEntries } from "./testComposerFixtures";
import type { AgentComposerContext, AssetSetSnapshot, ComposerRegistryEntry } from "./types";
import { messageSubmission, starterContext, type MessageOwner } from "./messageSubmission";

afterEach(() => { cleanup(); vi.restoreAllMocks(); sessionStorage.clear(); });

describe("video episode filename parsing", () => {
  it("recognizes spaced and fullwidth Chinese episode numbers", () => {
    expect(episodeNoFromFilename("I Loved the Wrong One All Along 第 1 集.mp4")).toBe(1);
    expect(episodeNoFromFilename("第１２集.mov")).toBe(12);
    expect(episodeNoFromFilename("幕后花絮.mp4")).toBeNull();
  });
});

describe("Composer Skill default prompts", () => {
  it("retains a failed message, Skill and attachments and retries the same request identity", async () => {
    const onSend = vi.fn().mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Response unavailable", 503)).mockResolvedValue(undefined);
    const attachment = { asset_id: "source", asset_snapshot_id: "source-v1", display_name: "source.txt", kind: "text" };
    vi.spyOn(api, "uploadFiles").mockResolvedValue([attachment]);
    const { container } = renderComposer(onSend);
    chooseSkill("小说转剧本");
    fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [new File(["source"], "source.txt")] } });
    await screen.findByText("source.txt");
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
    expect(screen.getByText("Response unavailable")).toBeTruthy();
    expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", promptFor("novel_to_script"));
    expect(screen.getByText("source.txt")).toBeTruthy();
    expect(onSend.mock.calls[0][5]).toMatch(/^[0-9a-f-]{36}$/);
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
    expect(onSend.mock.calls[1].slice(0, 4)).toEqual(onSend.mock.calls[0].slice(0, 4));
    expect(onSend.mock.calls[1][5]).toBe(onSend.mock.calls[0][5]);
    expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", "");
    expect(screen.queryByText("source.txt")).toBeNull();
  });

  it("requires confirmation before replacing an unconfirmed submission", async () => {
    const onSend = vi.fn().mockRejectedValue(new ApiError("UNAVAILABLE", "Response unavailable", 503));
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValue(true);
    renderComposer(onSend);
    fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "First request" } });
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
    fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Changed request" } });
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
    expect(onSend).toHaveBeenCalledTimes(1);
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
    expect(onSend).toHaveBeenCalledTimes(2);
    expect(onSend.mock.calls[1][5]).not.toBe(onSend.mock.calls[0][5]);
    expect(confirm).toHaveBeenCalledTimes(2);
  });

  it("preserves a failed homepage handoff for manual retry with its original key", async () => {
    const onSend = vi.fn().mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Handoff response lost", 503)).mockResolvedValue(undefined);
    seedHandoff("Homepage retry", capabilities[0], [], "11111111-1111-4111-8111-111111111111");
    renderComposer(onSend, capabilities, owner);
    await screen.findByText("Handoff response lost");
    expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", "Homepage retry");
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
    expect(onSend).toHaveBeenCalledTimes(2);
    expect(onSend.mock.calls[1][5]).toBe("11111111-1111-4111-8111-111111111111");
  });

  it("ignores a late rejection after stopping an in-flight send", async () => {
    let reject!: (reason: Error) => void;
    renderComposer(vi.fn(() => new Promise<void>((_resolve, fail) => { reject = fail; })));
    fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Old request" } });
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("disabled", true);
    fireEvent.click(screen.getByRole("button", { name: "停止等待" }));
    fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "New draft" } });
    await act(async () => { reject(new ApiError("UNAVAILABLE", "Stale send error", 503)); });
    expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", "New draft");
    expect(screen.queryByText("Stale send error")).toBeNull();
  });

  it.each([{ isComposing: true }, { keyCode: 229 }])("does not send on the IME confirmation Enter: %j", (composition) => {
    const onSend = vi.fn().mockResolvedValue(undefined);
    renderComposer(onSend);
    const input = screen.getByLabelText("给 Agent 的消息");
    fireEvent.change(input, { target: { value: "尚在输入的中文" } });
    fireEvent.keyDown(input, { key: "Enter", ...composition });
    expect(onSend).not.toHaveBeenCalled();
  });

  it("keeps Enter blocked until an attachment upload finishes", async () => {
    const onSend = vi.fn().mockResolvedValue(undefined);
    let finish!: (value: Awaited<ReturnType<typeof api.uploadFiles>>) => void;
    vi.spyOn(api, "uploadFiles").mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    const { container } = renderComposer(onSend);
    fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Read this upload" } });
    fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [new File(["source"], "source.txt")] } });
    fireEvent.keyDown(screen.getByLabelText("给 Agent 的消息"), { key: "Enter" });
    expect(onSend).not.toHaveBeenCalled();
    await act(async () => { finish([{ asset_id: "source", asset_snapshot_id: "snapshot", display_name: "source.txt", kind: "text" }]); });
    await act(async () => { fireEvent.keyDown(screen.getByLabelText("给 Agent 的消息"), { key: "Enter" }); });
    expect(onSend.mock.calls[0][2]).toHaveLength(1);
  });

  it("reads concise invocation prompts from the Workspace Registry", () => {
    expect(Object.fromEntries(capabilities.map((entry) => [entry.capability_id, entry.default_prompt]))).toEqual({
      novel_to_script: "请将我提供的小说改编成短剧剧本。",
      non_novel_to_script: "请将我提供的故事大纲、集纲或其他非小说文本改编成短剧剧本。",
      video_reference_creation: "请解析我提供的参考视频，并基于解析结果改编生成新的短剧剧本。",
      script_continuation: "请分析我提供的已有剧本，先给出 5 个续写方向供我选择，再按选定方向续写完整剧本。",
    });
  });

  it("fills an empty composer and replaces only an untouched Skill prompt", () => {
    renderComposer();
    const input = screen.getByLabelText("给 Agent 的消息") as HTMLTextAreaElement;

    chooseSkill("小说转剧本");
    expect(input.value).toBe(promptFor("novel_to_script"));

    chooseSkill("非小说文本转剧本");
    expect(input.value).toBe(promptFor("non_novel_to_script"));
  });

  it("renders an installed stateful Skill without adding an App branch", () => {
    renderComposer(vi.fn().mockResolvedValue(undefined), [...capabilities, dynamicStoryEntry]);
    const input = screen.getByLabelText("给 Agent 的消息") as HTMLTextAreaElement;

    chooseSkill("动态故事评审");

    expect(input.value).toBe(dynamicStoryEntry.default_prompt);
    expect(screen.getByText("动态故事评审")).toBeTruthy();
  });

  it("never overwrites text the user has entered or edited", () => {
    renderComposer();
    const input = screen.getByLabelText("给 Agent 的消息") as HTMLTextAreaElement;

    chooseSkill("小说转剧本");
    fireEvent.change(input, { target: { value: "请改成三集轻喜剧，保留原人物关系。" } });
    chooseSkill("非小说文本转剧本");

    expect(input.value).toBe("请改成三集轻喜剧，保留原人物关系。");
  });

  it("lets users cancel the selected Skill without deleting their own text", () => {
    renderComposer();
    const input = screen.getByLabelText("给 Agent 的消息") as HTMLTextAreaElement;

    chooseSkill("小说转剧本");
    chooseSkill("小说转剧本");
    expect(input.value).toBe("");
    expect(screen.getByText("Agent 输入")).toBeTruthy();

    chooseSkill("小说转剧本");
    fireEvent.change(input, { target: { value: "保留我写的要求" } });
    chooseSkill("小说转剧本");
    expect(input.value).toBe("保留我写的要求");
    expect(screen.getByText("Agent 输入")).toBeTruthy();
  });

  it("prepares a video batch before automatic Skill routing", async () => {
    const uploaded = { asset_id: "video-1", asset_snapshot_id: "snapshot-1", display_name: "第01集.mp4" };
    vi.spyOn(api, "uploadFiles").mockResolvedValue([uploaded]);
    const append = vi.spyOn(api, "appendVideoBatch").mockResolvedValue(videoBatch);
    const { container } = renderComposer();
    const fileInput = container.querySelector<HTMLInputElement>('input[type="file"]')!;

    fireEvent.change(fileInput, { target: { files: [new File(["video"], "第01集.mp4", { type: "video/mp4" })] } });
    await waitFor(() => expect(api.uploadFiles).toHaveBeenCalled());
    await waitFor(() => expect(append).toHaveBeenCalled());
    expect(screen.getByRole("heading", { name: "确认视频批次" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "关闭" }));
    expect(screen.getByText("视频批次 · 1 集 · 待确认")).toBeTruthy();
    expect((screen.getByRole("button", { name: "发送" }) as HTMLButtonElement).disabled).toBe(true);

    chooseSkill("小说转剧本");
    expect(screen.queryByText("视频批次 · 1 集 · 待确认")).toBeNull();
    expect(screen.getByText("第01集.mp4")).toBeTruthy();
    expect((screen.getByRole("button", { name: "发送" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("keeps earlier videos in the same collecting batch after another upload", async () => {
    const first = { asset_id: "video-1", asset_snapshot_id: "snapshot-1", display_name: "第01集.mp4", kind: "video" };
    const second = { ...first, asset_id: "video-2", display_name: "第02集.mp4" };
    vi.spyOn(api, "uploadFiles").mockResolvedValueOnce([first]).mockResolvedValueOnce([second]);
    const append = vi.spyOn(api, "appendVideoBatch").mockResolvedValue(videoBatch);
    const { container } = renderComposer(); const input = container.querySelector<HTMLInputElement>('input[type="file"]')!;
    fireEvent.change(input, { target: { files: [new File(["one"], first.display_name, { type: "video/mp4" })] } });
    await screen.findByRole("heading", { name: "确认视频批次" });
    fireEvent.click(screen.getByRole("button", { name: "继续上传" }));
    fireEvent.change(input, { target: { files: [new File(["two"], second.display_name, { type: "video/mp4" })] } });
    await waitFor(() => expect(append).toHaveBeenCalledTimes(2));
    expect(append.mock.calls[1].slice(0, 3)).toEqual(["project-1", videoBatch, [second]]);
  });

  it("sends the filled prompt as the explicit Skill invocation and then clears it", async () => {
    const onSend = vi.fn().mockResolvedValue(undefined);
    renderComposer(onSend);
    const input = screen.getByLabelText("给 Agent 的消息") as HTMLTextAreaElement;

    chooseSkill("小说转剧本");
    fireEvent.click(screen.getByRole("button", { name: "发送" }));

    await waitFor(() => expect(onSend).toHaveBeenCalled());
    expect(onSend.mock.calls[0][0]).toBe(promptFor("novel_to_script"));
    expect(onSend.mock.calls[0][1].capability_id).toBe("novel_to_script");
    expect(input.value).toBe("");
  });

  it("dispatches a homepage request after entering the new project workbench", async () => {
    const onSend = vi.fn().mockResolvedValue(undefined);
    seedHandoff("根据附件生成三集剧本", capabilities[0], [{ asset_id: "asset-1", asset_snapshot_id: "snapshot-1", display_name: "source.txt", kind: "text" }]);

    renderComposer(onSend, capabilities, owner);

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(onSend.mock.calls[0][0]).toBe("根据附件生成三集剧本");
    expect(onSend.mock.calls[0][1].capability_id).toBe("novel_to_script");
    expect(onSend.mock.calls[0][2][0].asset_id).toBe("asset-1");
    expect(onSend.mock.calls[0][5]).toMatch(/^[0-9a-f-]{36}$/);
    expect(messageSubmission.read(owner)).toBeNull();
  });

  it("claims but retains the homepage handoff until the Agent request completes", async () => {
    let complete!: () => void;
    const onSend = vi.fn(() => new Promise<void>((resolve) => { complete = resolve; }));
    seedHandoff("切出页面后仍需恢复", null, [], "11111111-1111-4111-8111-111111111111");

    renderComposer(onSend, capabilities, owner);

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(onSend.mock.calls[0][5]).toBe("11111111-1111-4111-8111-111111111111");
    expect(messageSubmission.read(owner)?.phase).toBe("pending");
    await act(async () => { complete(); });
    expect(messageSubmission.read(owner)).toBeNull();
  });

  it("preserves the homepage handoff across StrictMode's repeated render", async () => {
    const onSend = vi.fn().mockResolvedValue(undefined);
    seedHandoff("严格模式首条消息", null);

    render(<StrictMode><Composer
      projectID="project-1"
      submissionOwner={owner}
      projectAssets={[]}
      capabilities={capabilities}
      runSnapshot={null}
      context={context}
      onClearSelection={vi.fn()}
      onSend={onSend}
    /></StrictMode>);

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(onSend.mock.calls[0][0]).toBe("严格模式首条消息");
  });

  it("returns to the send state immediately when the current response is stopped", async () => {
    const onSend = vi.fn((
      _content: string,
      _capability: ComposerRegistryEntry | null,
      _attachments: unknown[],
      _videoBatch: AssetSetSnapshot | null,
      signal: AbortSignal,
    ) => new Promise<void>((resolve) => signal.addEventListener("abort", () => resolve(), { once: true })));
    renderComposer(onSend);
    fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "停止测试" } });
    fireEvent.click(screen.getByRole("button", { name: "发送" }));

    const stop = await screen.findByRole("button", { name: "停止等待" });
    fireEvent.click(stop);

    expect(onSend.mock.calls[0][4].aborted).toBe(true);
    expect(screen.getByRole("button", { name: "发送" })).toBeTruthy();
  });
});

function chooseSkill(label: string) {
  fireEvent.click(screen.getByRole("button", { name: "Skill" }));
  fireEvent.click(screen.getByRole("button", { name: new RegExp(label) }));
}

it("retains successfully uploaded Composer files when a later upload fails", async () => {
  const onSend = vi.fn().mockResolvedValue(undefined);
  const one = { asset_id: "one", asset_snapshot_id: "one-v1", display_name: "one.txt", kind: "text" };
  vi.spyOn(api, "uploadFiles").mockResolvedValueOnce([one]).mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Second file failed", 503));
  const { container } = renderComposer(onSend);
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Review the successful file" } });
  fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [new File(["one"], "one.txt"), new File(["two"], "two.txt")] } });
  await screen.findByText(/two.txt 上传失败/);
  expect(screen.getByRole("button", { name: "移除 one.txt" })).toBeTruthy();
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "发送" })));
  expect(onSend.mock.calls[0][2]).toEqual([one]);
});

it.each([false, true])("sends only included video members and preserves ancillary files in generic mode=%s", async (generic) => {
  const onSend = vi.fn().mockResolvedValue(undefined);
  const ref = (id: string) => ({ asset_id: id, asset_snapshot_id: `${id}-v1`, display_name: `${id}.mp4`, kind: "video", container_asset_id: "zip", hidden: true });
  const note = { asset_id: "note", asset_snapshot_id: "note-v1", display_name: "notes.txt", kind: "text" };
  vi.spyOn(api, "uploadFiles").mockResolvedValue([{ asset_id: "zip", asset_snapshot_id: "zip-v1", display_name: "episodes.zip", kind: "archive" }, ref("video-1"), ref("video-2"), ...(generic ? [{ ...note, container_asset_id: "zip", hidden: true }] : [])]);
  const base = structuredClone(videoBatch); base.version.completeness.order_confirmed = true;
  base.members.push({ ...base.members[0], asset_id: "video-2", included: false, episode_order: 2, episode_no: 2 }); base.version.member_count = 2;
  const sealed = structuredClone(base);
  sealed.asset_set.status = "sealed"; sealed.version.status = "sealed";
  sealed.asset_set.current_version = sealed.version.version = 3;
  sealed.asset_set.current_version_id = sealed.version.asset_set_version_id = "set-version-3";
  vi.spyOn(api, "appendVideoBatch").mockResolvedValue(base);
  vi.spyOn(api, "sealVideoBatch").mockResolvedValue(sealed);
  vi.spyOn(api, "getAssetSet").mockResolvedValue(sealed);
  const { container } = renderComposer(onSend);
  if (generic) fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Use the notes and selected videos" } });
  else chooseSkill("视频参考创作");
  fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [new File(["zip"], "episodes.zip")] } });
  fireEvent.click(await screen.findByRole("button", { name: "确认已上传完成" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  await waitFor(() => expect(screen.getByRole("button", { name: "发送" }).hasAttribute("disabled")).toBe(false));
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "发送" })));
  expect(onSend).toHaveBeenCalledOnce();
  expect(onSend.mock.calls[0][2]).toEqual([{ asset_id: "video-1", asset_snapshot_id: "video-1-v1", display_name: "video-1.mp4", kind: "video" }, ...(generic ? [note] : [])]);
  expect(onSend.mock.calls[0][3]).toEqual(generic ? null : sealed);
});

it("detaches a stale batch when material is removed under another Skill", async () => {
  const one = { asset_id: "video-1", asset_snapshot_id: "one-v1", display_name: "one.mp4", kind: "video" };
  const two = { asset_id: "video-2", asset_snapshot_id: "two-v1", display_name: "two.mp4", kind: "video" };
  vi.spyOn(api, "uploadFiles").mockResolvedValueOnce([one]).mockResolvedValueOnce([two]);
  const base = structuredClone(videoBatch);
  base.members.push({ ...base.members[0], asset_id: "video-2", episode_order: 2, episode_no: 2 }); base.version.member_count = 2;
  const next = structuredClone(videoBatch); next.members[0].asset_id = "video-2";
  next.asset_set.asset_set_id = next.version.asset_set_id = "new-set";
  const append = vi.spyOn(api, "appendVideoBatch").mockResolvedValueOnce(base).mockResolvedValueOnce(next);
  const { container } = renderComposer();
  chooseSkill("视频参考创作");
  fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [new File(["one"], "one.mp4"), new File(["two"], "two.mp4")] } });
  fireEvent.click(await screen.findByRole("button", { name: "继续上传" }));
  chooseSkill("小说转剧本");
  fireEvent.click(screen.getByRole("button", { name: "移除 one.mp4" }));
  chooseSkill("视频参考创作");
  await waitFor(() => expect(append).toHaveBeenCalledTimes(2));
  expect(append.mock.calls[1].slice(0, 3)).toEqual(["project-1", null, [two]]);
});

it("freezes other material actions after unknown preparation and retries with the original key", async () => {
  const video = { asset_id: "video-1", asset_snapshot_id: "one-v1", display_name: "one.mp4", kind: "video" };
  vi.spyOn(api, "uploadFiles").mockResolvedValue([video]);
  const append = vi.spyOn(api, "appendVideoBatch").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Preparation unknown", 503)).mockResolvedValue(videoBatch);
  const { container } = renderComposer();
  chooseSkill("视频参考创作");
  fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [new File(["one"], "one.mp4")] } });
  await screen.findByText("Preparation unknown");
  for (const name of ["发送", "添加文件", "Skill"]) expect(screen.getByRole("button", { name }).hasAttribute("disabled")).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "重试整理视频批次" }));
  await screen.findByRole("dialog");
  expect(append.mock.calls[1]).toEqual(append.mock.calls[0]);
});

const owner: MessageOwner = { workspace_id: "workspace-test", user_id: "user-test", project_id: "project-1", conversation_id: "conversation-1" };
function seedHandoff(content: string, capability: ComposerRegistryEntry | null, attachments: Parameters<typeof api.sendMessage>[3] = [], key?: string) {
  return messageSubmission.prepare(owner, { content, capability, attachments, video_batch: null, context: starterContext(owner.project_id, capability, null), clientInstanceID: "original-client" }, "starter", key);
}

function renderComposer(onSend = vi.fn().mockResolvedValue(undefined), entries = capabilities, submissionOwner?: MessageOwner) {
  return render(<Composer
    projectID="project-1"
    submissionOwner={submissionOwner}
    projectAssets={[]}
    capabilities={entries}
    runSnapshot={null}
    context={context}
    onClearSelection={vi.fn()}
    onSend={onSend}
  />);
}

it.each(["version", "content", "disabled", "removed"])("blocks a stale selected Skill after %s changes without discarding the user's message", async (change) => {
  const onSend = vi.fn().mockResolvedValue(undefined);
  const entry = structuredClone(dynamicStoryEntry);
  const props = { projectID: "project-1", projectAssets: [], runSnapshot: null, context, onClearSelection: vi.fn(), onSend };
  const view = render(<Composer {...props} capabilities={[entry]} />);
  chooseSkill(entry.label);
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Keep this exact request" } });
  const next = structuredClone(entry);
  if (change === "version") next.version = "2.0.0";
  if (change === "content") next.skill = { name: "dynamic-story", content_hash: "new-hash", scope: "project", path: "SKILL.md", allow_implicit_invocation: true, interface: {}, dependencies: [], scripts: [] };
  if (change === "disabled") next.status = "unavailable";
  view.rerender(<Composer {...props} capabilities={change === "removed" ? [] : [next]} />);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "发送" })));
  expect(onSend).not.toHaveBeenCalled();
  expect(screen.getByText("所选 Skill 已更新或不可用，请重新选择后发送。")).toBeTruthy();
  expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", "Keep this exact request");
  if (change === "version" || change === "content") chooseSkill(next.label);
  else { fireEvent.click(screen.getByRole("button", { name: "Skill" })); fireEvent.click(screen.getByRole("button", { name: "通用 Agent" })); }
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "发送" })));
  expect(onSend).toHaveBeenCalledOnce();
  expect(onSend.mock.calls[0][1]).toEqual(change === "version" || change === "content" ? next : null);
});

it("retries the exact unconfirmed message with its original Skill version even after an upgrade", async () => {
  const onSend = vi.fn().mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Send unknown", 503)).mockResolvedValue(undefined);
  const entry = structuredClone(dynamicStoryEntry);
  const props = { projectID: "project-1", projectAssets: [], runSnapshot: null, context, onClearSelection: vi.fn(), onSend };
  const view = render(<Composer {...props} capabilities={[entry]} />);
  chooseSkill(entry.label);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "发送" })));
  view.rerender(<Composer {...props} capabilities={[{ ...entry, version: "2.0.0" }]} />);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "发送" })));
  expect(onSend).toHaveBeenCalledTimes(2);
  expect(onSend.mock.calls[1].slice(0, 4)).toEqual(onSend.mock.calls[0].slice(0, 4));
  expect(onSend.mock.calls[1][5]).toBe(onSend.mock.calls[0][5]);
});

const capabilities = legacyComposerEntries;

function promptFor(capabilityID: string): string {
  return capabilities.find((entry) => entry.capability_id === capabilityID)?.default_prompt ?? "";
}

const context: AgentComposerContext = {
  view: {
    project_id: "project-1",
    run_id: null,
    capability_id: null,
    artifact_id: null,
    artifact_version_id: null,
    artifact_type: null,
    scope_key: null,
    artifact_label: null,
  },
  selection: null,
};

const videoBatch: AssetSetSnapshot = {
  asset_set: { asset_set_id: "set-1", project_id: "project-1", current_version_id: "set-version-2", current_version: 2, status: "collecting" },
  version: {
    asset_set_id: "set-1",
    asset_set_version_id: "set-version-2",
    version: 2,
    member_count: 1,
    status: "draft",
    completeness: {
      recognized_episode_count: 1,
      unrecognized_count: 0,
      duplicate_episode_numbers: [],
      missing_episode_numbers: [],
      failed_asset_ids: [],
      order_confirmed: false,
    },
  },
  members: [{
    asset_id: "video-1",
    episode_order: 1,
    episode_no: 1,
    episode_label: "第 1 集",
    filename_candidate: { raw: "第01集.mp4" },
    included: true,
  }],
};
