// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { NewProjectStarter as NewProjectStarterView, projectTitleFromPrompt } from "./App";
import { messageSubmission } from "./messageSubmission";
import type { ComponentProps } from "react";
import { api, ApiError } from "./api";
import { dynamicStoryEntry, legacyComposerEntries } from "./testComposerFixtures";
import type { AssetSetSnapshot, Project } from "./types";

const principal = { workspace_id: "workspace-test", user_id: "user-test", role: "owner" as const };
function NewProjectStarter(props: Omit<ComponentProps<typeof NewProjectStarterView>, "principal">) { return <NewProjectStarterView {...props} principal={principal} />; }
function savedHandoff(projectID = "project-new") { return messageSubmission.read({ workspace_id: principal.workspace_id, user_id: principal.user_id, project_id: projectID, conversation_id: "conversation-new" })!.draft; }

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  sessionStorage.clear();
  history.replaceState({}, "", "/");
});

describe("new project starter", () => {
  it("derives a compact project title from the first request", () => {
    expect(projectTitleFromPrompt("  请帮我写一个三集悬疑故事  ")).toBe("请帮我写一个三集悬疑故事");
    expect(projectTitleFromPrompt("这是一段超过二十四个字符且需要被安全截断的新作品创作要求，后面还有内容")).toHaveLength(25);
  });

  it("switches explicit Skills without overwriting user-edited content", () => {
    render(<NewProjectStarter capabilities={capabilities} />);
    const input = screen.getByLabelText("新作品要求") as HTMLTextAreaElement;

    fireEvent.click(screen.getByRole("radio", { name: /小说转剧本/ }));
    expect(input.value).toBe(promptFor("novel_to_script"));
    fireEvent.click(screen.getByRole("radio", { name: /非小说文本转剧本/ }));
    expect(input.value).toBe(promptFor("non_novel_to_script"));

    fireEvent.change(input, { target: { value: "保留我的具体要求" } });
    fireEvent.click(screen.getByRole("radio", { name: /视频参考创作/ }));
    expect(input.value).toBe("保留我的具体要求");
  });

  it("projects a newly installed Skill onto the starter without product-specific code", () => {
    render(<NewProjectStarter capabilities={[...capabilities, dynamicStoryEntry]} />);

    fireEvent.click(screen.getByRole("radio", { name: /动态故事评审/ }));

    expect((screen.getByLabelText("新作品要求") as HTMLTextAreaElement).value).toBe(dynamicStoryEntry.default_prompt);
    expect(screen.getByText("已选择 动态故事评审")).toBeTruthy();
  });

  it("creates an isolated project and hands the first Skill request to its workbench", async () => {
    const created = project();
    vi.spyOn(api, "createProject").mockResolvedValue(created);
    render(<NewProjectStarter capabilities={capabilities} />);

    fireEvent.click(screen.getByRole("radio", { name: /小说转剧本/ }));
    fireEvent.change(screen.getByLabelText("新作品要求"), { target: { value: "改编成三集都市悬疑短剧" } });
    fireEvent.click(screen.getByRole("button", { name: "创建作品并发送" }));

    await waitFor(() => expect(location.pathname).toBe(`/projects/${created.project_id}`));
    expect(api.createProject).toHaveBeenCalledWith("改编成三集都市悬疑短剧");
    expect(savedHandoff(created.project_id)).toMatchObject({
      content: "改编成三集都市悬疑短剧",
      capability: { capability_id: "novel_to_script", version: "1.4.0" },
      attachments: [],
    });
  });

  it("submits a filled request with Enter and keeps Shift+Enter for a newline", async () => {
    const created = project();
    vi.spyOn(api, "createProject").mockResolvedValue(created);
    render(<NewProjectStarter capabilities={capabilities} />);
    const input = screen.getByLabelText("新作品要求");

    fireEvent.change(input, { target: { value: "先写一个三集故事" } });
    fireEvent.keyDown(input, { key: "Enter", shiftKey: true });
    expect(api.createProject).not.toHaveBeenCalled();

    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(location.pathname).toBe(`/projects/${created.project_id}`));
    expect(api.createProject).toHaveBeenCalledTimes(1);
  });

  it("does not submit Enter while an input method composition is active", () => {
    vi.spyOn(api, "createProject").mockResolvedValue(project());
    render(<NewProjectStarter capabilities={capabilities} />);
    const input = screen.getByLabelText("新作品要求");

    fireEvent.change(input, { target: { value: "中文输入中" } });
    fireEvent.keyDown(input, { key: "Enter", isComposing: true });

    expect(api.createProject).not.toHaveBeenCalled();
  });

  it("reuses the created draft when an attachment upload is retried", async () => {
    const created = project();
    vi.spyOn(api, "createProject").mockResolvedValue(created);
    vi.spyOn(api, "uploadFiles").mockRejectedValueOnce(new Error("failed")).mockResolvedValueOnce([{ asset_id: "asset-1", asset_snapshot_id: "snapshot-1", display_name: "source.txt", kind: "text" }]);
    const { container } = render(<NewProjectStarter capabilities={capabilities} />);
    fireEvent.change(screen.getByLabelText("新作品要求"), { target: { value: "先讨论这个故事" } });
    fireEvent.change(container.querySelector<HTMLInputElement>('input[type="file"]')!, { target: { files: [new File(["source"], "source.txt", { type: "text/plain" })] } });

    fireEvent.click(screen.getByRole("button", { name: "创建作品并发送" }));
    await screen.findByText("暂时无法连接服务，请检查后端是否已启动。");
    fireEvent.click(screen.getByRole("button", { name: "创建作品并发送" }));

    await waitFor(() => expect(location.pathname).toBe(`/projects/${created.project_id}`));
    expect(api.createProject).toHaveBeenCalledTimes(1);
    expect(api.uploadFiles).toHaveBeenCalledTimes(2);
  });

  it("requires and confirms a sealed video batch before entering the workbench", async () => {
    const created = project();
    const uploaded = { asset_id: "video-1", asset_snapshot_id: "snapshot-1", display_name: "第01集.mp4", kind: "video" };
    vi.spyOn(api, "createProject").mockResolvedValue(created);
    vi.spyOn(api, "uploadFiles").mockResolvedValue([uploaded]);
    vi.spyOn(api, "appendVideoBatch").mockResolvedValue(videoBatch("draft"));
    vi.spyOn(api, "sealVideoBatch").mockResolvedValue(videoBatch("sealed"));
    vi.spyOn(api, "getAssetSet").mockResolvedValue(videoBatch("sealed"));
    const { container } = render(<NewProjectStarter capabilities={capabilities} />);

    fireEvent.click(screen.getByRole("radio", { name: /视频参考创作/ }));
    fireEvent.change(container.querySelector<HTMLInputElement>('input[type="file"]')!, { target: { files: [new File(["video"], "第01集.mp4", { type: "video/mp4" })] } });
    fireEvent.click(screen.getByRole("button", { name: "创建作品并发送" }));

    expect(await screen.findByRole("heading", { name: "确认视频批次" })).toBeTruthy();
    expect(location.pathname).toBe("/");
    fireEvent.click(screen.getByRole("button", { name: "确认已上传完成" }));

    await waitFor(() => expect(location.pathname).toBe(`/projects/${created.project_id}`));
    const handoff = savedHandoff(created.project_id);
    expect(handoff.capability?.capability_id).toBe("video_reference_creation");
    expect(handoff.attachments[0].asset_id).toBe("video-1");
    expect(handoff.video_batch.asset_set.status).toBe("sealed");
  });

  it("adds a later upload to the existing collecting batch", async () => {
    vi.spyOn(api, "createProject").mockResolvedValue(project());
    const first = { asset_id: "video-1", asset_snapshot_id: "snapshot-1", display_name: "第01集.mp4", kind: "video" };
    const second = { ...first, asset_id: "video-2", asset_snapshot_id: "snapshot-2", display_name: "第02集.mp4" };
    vi.spyOn(api, "uploadFiles").mockResolvedValueOnce([first]).mockResolvedValueOnce([second]);
    const base = videoBatch("draft"), expanded = structuredClone(base);
    expanded.members.push({ ...expanded.members[0], asset_id: second.asset_id, episode_order: 2, episode_no: 2 });
    expanded.version.member_count = 2;
    const append = vi.spyOn(api, "appendVideoBatch").mockResolvedValueOnce(base).mockResolvedValueOnce(expanded);
    const { container } = render(<NewProjectStarter capabilities={capabilities} />);
    fireEvent.click(screen.getByRole("radio", { name: /视频参考创作/ }));
    const input = container.querySelector<HTMLInputElement>('input[type="file"]')!;
    fireEvent.change(input, { target: { files: [new File(["one"], first.display_name)] } });
    fireEvent.click(screen.getByRole("button", { name: "创建作品并发送" }));
    await screen.findByRole("heading", { name: "确认视频批次" });
    fireEvent.click(screen.getByRole("button", { name: "继续上传" }));
    fireEvent.change(input, { target: { files: [new File(["two"], second.display_name)] } });
    fireEvent.click(screen.getByRole("button", { name: "创建作品并发送" }));
    await waitFor(() => expect(append).toHaveBeenCalledTimes(2));
    expect(append.mock.calls[1].slice(0, 3)).toEqual(["project-new", base, [second]]);
    expect(api.createProject).toHaveBeenCalledOnce();
  });
});

const capabilities = legacyComposerEntries.filter((entry) => entry.capability_id !== "script_continuation");

it("keeps earlier successful uploads after a later file fails and supports removing them before retry", async () => {
  vi.spyOn(api, "createProject").mockResolvedValue(project());
  const one = new File(["one"], "one.txt"), two = new File(["two"], "two.txt");
  const ref = (id: string) => ({ asset_id: id, asset_snapshot_id: `${id}-v1`, display_name: `${id}.txt`, kind: "text" });
  const upload = vi.spyOn(api, "uploadFiles").mockResolvedValueOnce([ref("one")]).mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Second file failed", 503)).mockResolvedValueOnce([ref("two")]);
  const { container } = render(<NewProjectStarter capabilities={capabilities} />);
  fireEvent.change(screen.getByLabelText("新作品要求"), { target: { value: "Review material" } });
  fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [one, two] } });
  fireEvent.click(screen.getByRole("button", { name: "创建作品并发送" }));
  await screen.findByText("Second file failed");
  fireEvent.click(screen.getByRole("button", { name: "移除 one.txt" }));
  fireEvent.click(screen.getByRole("button", { name: "创建作品并发送" }));
  await waitFor(() => expect(location.pathname).toBe("/projects/project-new"));
  expect(upload.mock.calls.map((call) => call[1])).toEqual([[one], [two], [two]]);
  expect(savedHandoff().attachments).toEqual([ref("two")]);
  expect(api.createProject).toHaveBeenCalledOnce();
});

it("can reopen an unknown seal from the starter while other actions remain blocked", async () => {
  vi.spyOn(api, "createProject").mockResolvedValue(project());
  vi.spyOn(api, "uploadFiles").mockResolvedValue([{ asset_id: "video-1", asset_snapshot_id: "snapshot-1", display_name: "第01集.mp4", kind: "video" }]);
  vi.spyOn(api, "appendVideoBatch").mockResolvedValue(videoBatch("draft"));
  const seal = vi.spyOn(api, "sealVideoBatch").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Seal unknown", 503)).mockResolvedValue(videoBatch("sealed"));
  vi.spyOn(api, "getAssetSet").mockResolvedValue(videoBatch("sealed"));
  const { container } = render(<NewProjectStarter capabilities={capabilities} />);
  fireEvent.click(screen.getByRole("radio", { name: /视频参考创作/ }));
  fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [new File(["one"], "第01集.mp4")] } });
  fireEvent.click(screen.getByRole("button", { name: "创建作品并发送" }));
  fireEvent.click(await screen.findByRole("button", { name: "确认已上传完成" }));
  await screen.findByText("Seal unknown");
  fireEvent.click(screen.getByRole("button", { name: "关闭" }));
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.getByRole("button", { name: "创建作品并发送" }).hasAttribute("disabled")).toBe(true);
  expect(screen.getByRole("button", { name: "添加文件" }).hasAttribute("disabled")).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "查看视频批次" }));
  fireEvent.click(screen.getByRole("button", { name: "重试原操作" }));
  await waitFor(() => expect(location.pathname).toBe("/projects/project-new"));
  expect(seal.mock.calls[1]).toEqual(seal.mock.calls[0]);
});

function promptFor(capabilityID: string): string {
  return capabilities.find((entry) => entry.capability_id === capabilityID)?.default_prompt ?? "";
}

function project(): Project {
  return {
    project_id: "project-new",
    title: "新作品",
    version: 1,
    status: "active",
    primary_conversation_id: "conversation-new",
    active_write_run_id: null,
    current_capability_id: null,
    latest_capability_id: null,
    latest_run_status: null,
    current_focus_artifact_version_id: null,
    updated_at: new Date().toISOString(),
  };
}

function videoBatch(status: "draft" | "sealed"): AssetSetSnapshot {
  return {
    asset_set: { asset_set_id: "set-1", project_id: "project-new", current_version_id: status === "sealed" ? "set-version-3" : "set-version-2", current_version: status === "sealed" ? 3 : 2, status: status === "sealed" ? "sealed" : "collecting" },
    version: {
      asset_set_id: "set-1",
      asset_set_version_id: status === "sealed" ? "set-version-3" : "set-version-2",
      version: status === "sealed" ? 3 : 2,
      member_count: 1,
      status,
      completeness: { recognized_episode_count: 1, unrecognized_count: 0, duplicate_episode_numbers: [], missing_episode_numbers: [], failed_asset_ids: [], order_confirmed: true },
    },
    members: [{ asset_id: "video-1", episode_order: 1, episode_no: 1, episode_label: "第 1 集", filename_candidate: { raw: "第01集.mp4" }, included: true }],
  };
}
