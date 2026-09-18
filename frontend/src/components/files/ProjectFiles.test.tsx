// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../../api";
import type { ProjectFile, ProjectFileContent } from "../../types";
import { ProjectFiles } from "./ProjectFiles";
import { skillDraft, skillInstallResult, skillPrincipal } from "./skillDraftTestFixtures";

const file: ProjectFile = { project_id: "prj-files", path: "draft/SKILL.md", version: 2, content_hash: "hash", size_bytes: 12, deleted: false, agent_tool_call_id: "call-files", created_at: "2026-09-06T00:00:00Z" };
const page: ProjectFileContent = { file, content: "File text", offset: 0, next_offset: 9, truncated: false };
const dialogMethods = ["showModal", "close"] as const;
const originalDialogMethods = dialogMethods.map((name) => Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, name));

beforeAll(() => {
  for (const name of dialogMethods) Object.defineProperty(HTMLDialogElement.prototype, name, { configurable: true, writable: true, value() {} });
});

it("preserves the original install across a file event, closing and reopening the actual file dialog", async () => {
  const draft = skillDraft(file.project_id, "draft");
  vi.spyOn(api, "previewProjectSkillDraft").mockResolvedValue(draft);
  vi.spyOn(api, "getSkillManagementOptions").mockResolvedValue({ install_scopes: ["user", "project"], system_read_only: true });
  const install = vi.spyOn(api, "installProjectSkillDraft").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Install unknown", 503)).mockResolvedValue(skillInstallResult(draft));
  const onInstalled = vi.fn();
  const props = { projectID: file.project_id, principal: skillPrincipal, onInstalled };
  const view = render(<ProjectFiles {...props} revisionKey="one" />);
  fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
  fireEvent.click(await screen.findByRole("button", { name: /draft\/SKILL.md/ }));
  fireEvent.click(await screen.findByRole("button", { name: "校验 Skill" }));
  fireEvent.click(await screen.findByRole("button", { name: "确认安装" }));
  await screen.findByText("Install unknown");
  expect(screen.getByRole("button", { name: /draft\/SKILL.md/ }).hasAttribute("disabled")).toBe(true);
  expect(screen.getByRole("button", { name: "刷新工作文件" }).hasAttribute("disabled")).toBe(true);
  view.rerender(<ProjectFiles {...props} revisionKey="two" />);
  fireEvent.click(screen.getByRole("button", { name: "关闭工作文件" }));
  expect(screen.queryByRole("dialog")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
  fireEvent.click(await screen.findByRole("button", { name: "重试原安装" }));
  await screen.findByText("已安装，后续对话可调用。");
  expect(install.mock.calls[1]).toEqual(install.mock.calls[0]);
  expect(api.previewProjectSkillDraft).toHaveBeenCalledOnce();
  expect(onInstalled).toHaveBeenCalledOnce();
});

it("revalidates an edited Skill and upgrades the same installation using its previous version", async () => {
  const draft = skillDraft(file.project_id, "draft"), first = skillInstallResult(draft);
  first.receipt.skill_version_id = first.installation.active_version_id = first.installation.versions[0].skill_version_id = "version-first";
  const next = structuredClone(draft); next.version = "2.0.0"; next.snapshot_hash = "draft-next"; next.manifest!.content_hash = "package-next";
  next.installations = [{ scope: "project", scope_ref: file.project_id, skill_installation_id: first.installation.skill_installation_id, active_version_id: "version-first", version: "1.0.0" }];
  vi.spyOn(api, "previewProjectSkillDraft").mockResolvedValueOnce(draft).mockResolvedValue(next);
  vi.spyOn(api, "getSkillManagementOptions").mockResolvedValue({ install_scopes: ["project"], system_read_only: true });
  const install = vi.spyOn(api, "installProjectSkillDraft").mockResolvedValueOnce(first).mockResolvedValue(skillInstallResult(next));
  const props = { projectID: file.project_id, principal: skillPrincipal };
  const view = render(<ProjectFiles {...props} revisionKey="one" />);
  fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
  fireEvent.click(await screen.findByRole("button", { name: /draft\/SKILL.md/ }));
  fireEvent.click(await screen.findByRole("button", { name: "校验 Skill" }));
  fireEvent.click(await screen.findByRole("button", { name: "确认安装" }));
  await screen.findByText("已安装，后续对话可调用。");
  view.rerender(<ProjectFiles {...props} revisionKey="two" />);
  expect(screen.getByText("已安装，后续对话可调用。")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "重新校验 Skill" }));
  fireEvent.click(await screen.findByRole("button", { name: "确认升级" }));
  await screen.findByText("已安装，后续对话可调用。");
  expect(install.mock.calls[1][1]).toEqual({ root_path: "draft", scope: "project", snapshot_hash: "draft-next", installation_id: "install-new", expected_active_version_id: "version-first" });
  expect(install.mock.calls[1][2]).not.toBe(install.mock.calls[0][2]);
});

it("retains an in-flight Skill installation when the dialog is closed with Escape", async () => {
  const draft = skillDraft(file.project_id, "draft");
  vi.spyOn(api, "previewProjectSkillDraft").mockResolvedValue(draft);
  vi.spyOn(api, "getSkillManagementOptions").mockResolvedValue({ install_scopes: ["project"], system_read_only: true });
  let resolve!: (value: ReturnType<typeof skillInstallResult>) => void;
  vi.spyOn(api, "installProjectSkillDraft").mockReturnValue(new Promise((done) => { resolve = done; }));
  render(<ProjectFiles projectID={file.project_id} revisionKey="one" principal={skillPrincipal} />);
  fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
  fireEvent.click(await screen.findByRole("button", { name: /draft\/SKILL.md/ }));
  fireEvent.click(await screen.findByRole("button", { name: "校验 Skill" }));
  fireEvent.click(await screen.findByRole("button", { name: "确认安装" }));
  fireEvent(screen.getByRole("dialog"), new Event("cancel", { cancelable: true }));
  expect(screen.queryByRole("dialog")).toBeNull();
  await act(async () => resolve(skillInstallResult(draft)));
  fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
  await screen.findByText("已安装，后续对话可调用。");
  expect(api.installProjectSkillDraft).toHaveBeenCalledOnce();
});

it.each(["project", "path", "version", "offset"])("rejects a wrong %s file response before enabling Skill validation or download", async (field) => {
  const wrong = structuredClone(page);
  if (field === "project") wrong.file.project_id = "foreign";
  if (field === "path") wrong.file.path = "other/SKILL.md";
  if (field === "version") wrong.file.version = 3;
  if (field === "offset") wrong.offset = 10;
  vi.mocked(api.readProjectFile).mockResolvedValue(wrong);
  render(<ProjectFiles projectID={file.project_id} revisionKey="" />);
  fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
  fireEvent.click(await screen.findByRole("button", { name: /draft\/SKILL.md/ }));
  await screen.findByRole("alert");
  expect(screen.queryByRole("link", { name: "下载此版本" })).toBeNull();
  expect(screen.queryByRole("button", { name: "校验 Skill" })).toBeNull();
});
afterAll(() => {
  dialogMethods.forEach((name, index) => {
    const descriptor = originalDialogMethods[index];
    if (descriptor) Object.defineProperty(HTMLDialogElement.prototype, name, descriptor);
    else Reflect.deleteProperty(HTMLDialogElement.prototype, name);
  });
});

beforeEach(() => {
  vi.spyOn(HTMLDialogElement.prototype, "showModal").mockImplementation(function (this: HTMLDialogElement) { this.setAttribute("open", ""); });
  vi.spyOn(HTMLDialogElement.prototype, "close").mockImplementation(function (this: HTMLDialogElement) {
    this.removeAttribute("open");
    queueMicrotask(() => this.dispatchEvent(new Event("close")));
  });
  vi.spyOn(api, "listProjectFiles").mockResolvedValue([file]);
  vi.spyOn(api, "readProjectFile").mockResolvedValue(page);
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("project working files", () => {
  it("shows binary metadata and exact download without treating bytes as empty text or a Skill", async () => {
    vi.mocked(api.listProjectFiles).mockResolvedValue([{ ...file, binary: true }]);
    vi.mocked(api.readProjectFile).mockResolvedValue({ ...page, file: { ...file, binary: true }, content: "", next_offset: 0 });
    render(<ProjectFiles projectID="prj-files" revisionKey="" />);
    fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
    fireEvent.click(await screen.findByRole("button", { name: /draft\/SKILL.md/ }));
    await screen.findByText("二进制文件");
    expect(screen.getByText("hash")).toBeTruthy();
    expect(screen.queryByText("（空文件）")).toBeNull();
    expect(screen.queryByRole("button", { name: "下一页" })).toBeNull();
    expect(screen.queryByRole("button", { name: "校验 Skill" })).toBeNull();
    expect(screen.getByRole("link", { name: "下载此版本" }).getAttribute("href")).toBe(api.projectFileDownloadURL("prj-files", file.path, 2));
    vi.mocked(api.readProjectFile).mockResolvedValue({ ...page, file: { ...file, version: 1 }, content: "Prior text" });
    fireEvent.change(screen.getByRole("combobox", { name: "文件版本" }), { target: { value: "1" } });
    await screen.findByText("Prior text");
    expect(screen.queryByText("二进制文件")).toBeNull();
    expect(screen.getByRole("button", { name: "下一页" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "下载此版本" }).getAttribute("href")).toContain("version=1");
  });

  it("does not expose Skill validation when reading its version fails", async () => {
    vi.mocked(api.readProjectFile).mockRejectedValue(new Error("File unavailable"));
    render(<ProjectFiles projectID="prj-files" revisionKey="" />);
    fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
    fireEvent.click(await screen.findByRole("button", { name: /draft\/SKILL.md/ }));
    await screen.findByRole("alert");
    expect(screen.queryByRole("button", { name: "校验 Skill" })).toBeNull();
  });

  it("stays open after the Strict Mode effect cleanup close event", async () => {
    render(<StrictMode><ProjectFiles projectID="prj-files" revisionKey="" /></StrictMode>);
    fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
    await screen.findByRole("button", { name: /draft\/SKILL.md/ });
    expect(screen.getByRole("dialog", { name: "工作文件" })).toBeTruthy();
  });
  it("opens files on demand and downloads the exact viewed version", async () => {
    render(<ProjectFiles projectID="prj-files" revisionKey="" />);
    expect(api.listProjectFiles).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
    fireEvent.click(await screen.findByRole("button", { name: /draft\/SKILL.md/ }));
    await screen.findByText("File text");
    expect(screen.getByRole("link", { name: "下载此版本" }).getAttribute("href")).toBe(api.projectFileDownloadURL("prj-files", file.path, 2));
    vi.mocked(api.readProjectFile).mockResolvedValue({ ...page, file: { ...file, version: 1 }, content: "Old version" });
    fireEvent.change(screen.getByRole("combobox", { name: "文件版本" }), { target: { value: "1" } });
    await screen.findByText("Old version");
    expect(api.readProjectFile).toHaveBeenLastCalledWith("prj-files", file.path, 1, 0, expect.any(AbortSignal));
    expect(screen.getByRole("link", { name: "下载此版本" }).getAttribute("href")).toContain("version=1");
    fireEvent.click(screen.getByRole("button", { name: "关闭工作文件" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("pages by the backend cursor and keeps dangerous markup as plain text", async () => {
    vi.mocked(api.readProjectFile).mockResolvedValueOnce({ ...page, content: "<script>alert(1)</script>", next_offset: 16000, truncated: true })
      .mockResolvedValueOnce({ ...page, content: "Second page", offset: 16000, next_offset: 16010 });
    const { container } = render(<ProjectFiles projectID="prj-files" revisionKey="" />);
    fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
    fireEvent.click(await screen.findByRole("button", { name: /draft\/SKILL.md/ }));
    await screen.findByText("<script>alert(1)</script>");
    expect(container.querySelector("script")).toBeNull();
    expect(screen.getByRole("button", { name: "上一页" }).hasAttribute("disabled")).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    await screen.findByText("Second page");
    expect(api.readProjectFile).toHaveBeenLastCalledWith("prj-files", file.path, 2, 16000, expect.any(AbortSignal));
    expect(screen.getByRole("button", { name: "下一页" }).hasAttribute("disabled")).toBe(true);
  });

  it("exposes read errors without offering an unverified download", async () => {
    vi.mocked(api.readProjectFile).mockRejectedValue(new Error("该版本已删除"));
    render(<ProjectFiles projectID="prj-files" revisionKey="" />);
    fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
    fireEvent.click(await screen.findByRole("button", { name: /draft\/SKILL.md/ }));
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "该版本已删除");
    expect(screen.queryByRole("link", { name: "下载此版本" })).toBeNull();
    expect(screen.getByRole("combobox", { name: "文件版本" })).toBeTruthy();
  });

  it("refreshes after file tool completion without changing the pinned version", async () => {
    const { rerender } = render(<ProjectFiles projectID="prj-files" revisionKey="running" />);
    fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
    fireEvent.click(await screen.findByRole("button", { name: /draft\/SKILL.md/ }));
    await screen.findByText("File text");
    vi.mocked(api.listProjectFiles).mockResolvedValue([{ ...file, version: 3 }]);
    rerender(<ProjectFiles projectID="prj-files" revisionKey="completed" />);
    await screen.findByRole("option", { name: "v3" });
    expect((screen.getByRole("combobox", { name: "文件版本" }) as HTMLSelectElement).value).toBe("2");
    expect(api.readProjectFile).toHaveBeenCalledTimes(1);
  });

  it("does not let a slow prior file response overwrite the new selection", async () => {
    let resolveFirst!: (value: ProjectFileContent) => void;
    vi.mocked(api.listProjectFiles).mockResolvedValue([file, { ...file, path: "other.txt" }]);
    vi.mocked(api.readProjectFile).mockReturnValueOnce(new Promise((resolve) => { resolveFirst = resolve; }))
      .mockResolvedValueOnce({ ...page, file: { ...file, path: "other.txt" }, content: "Current file" });
    render(<ProjectFiles projectID="prj-files" revisionKey="" />);
    fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
    fireEvent.click(await screen.findByRole("button", { name: /draft\/SKILL.md/ }));
    fireEvent.click(screen.getByRole("button", { name: /other.txt/ }));
    await screen.findByText("Current file");
    resolveFirst(page);
    await waitFor(() => expect(screen.queryByText("File text")).toBeNull());
    expect(screen.getByRole("link", { name: "下载此版本" }).getAttribute("href")).toContain("path=other.txt");
  });

  it("allows viewing the prior version of a deleted file", async () => {
    vi.mocked(api.listProjectFiles).mockResolvedValue([{ ...file, version: 3, deleted: true }]);
    render(<ProjectFiles projectID="prj-files" revisionKey="" />);
    fireEvent.click(screen.getByRole("button", { name: "工作文件" }));
    fireEvent.click(await screen.findByRole("button", { name: /已删除/ }));
    await screen.findByText("File text");
    expect(api.readProjectFile).toHaveBeenCalledWith("prj-files", file.path, 2, 0, expect.any(AbortSignal));
  });
});
