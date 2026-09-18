// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ComponentProps } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../../api";
import type { ProjectSkillInstallResult } from "../../types";
import { SkillDraftPanel as DraftPanel } from "./SkillDraftPanel";
import { skillDraft, skillInstallResult, skillPrincipal } from "./skillDraftTestFixtures";

const draft = skillDraft(), result = skillInstallResult(draft);
const SkillDraftPanel = (props: ComponentProps<typeof DraftPanel>) => <DraftPanel principal={skillPrincipal} {...props} />;
beforeEach(() => {
  vi.spyOn(api, "previewProjectSkillDraft").mockResolvedValue(structuredClone(draft));
  vi.spyOn(api, "getSkillManagementOptions").mockResolvedValue({ install_scopes: ["user", "project"], system_read_only: true });
  vi.spyOn(api, "installProjectSkillDraft").mockResolvedValue(result);
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("selects an authorized scope after a role downgrade before submission", async () => {
  vi.mocked(api.getSkillManagementOptions).mockResolvedValue({ install_scopes: ["workspace", "project"], system_read_only: true });
  const props = { projectID: "p", root: draft.root_path, onBack: () => {} };
  const view = render(<SkillDraftPanel {...props} principal={{ ...skillPrincipal, role: "admin" }} />);
  await screen.findByText("校验通过");
  fireEvent.change(screen.getByRole("combobox", { name: "安装范围" }), { target: { value: "workspace" } });
  view.rerender(<SkillDraftPanel {...props} principal={{ ...skillPrincipal, role: "editor" }} />);
  expect(screen.queryByRole("option", { name: "整个工作区" })).toBeNull();
  expect(screen.getByRole("combobox", { name: "安装范围" })).toHaveProperty("value", "project");
  const install = screen.getByRole("button", { name: "确认安装" });
  expect(install).toHaveProperty("disabled", false);
  fireEvent.click(install);
  await screen.findByText("已安装，后续对话可调用。");
  expect(api.installProjectSkillDraft).toHaveBeenCalledWith("p", expect.objectContaining({ scope: "project" }), expect.any(String));
});

it("never changes an uncertain workspace installation into a project installation", async () => {
  vi.mocked(api.getSkillManagementOptions).mockResolvedValue({ install_scopes: ["workspace", "project"], system_read_only: true });
  vi.mocked(api.installProjectSkillDraft).mockRejectedValue(new Error("Response lost"));
  const props = { projectID: "p", root: draft.root_path, onBack: () => {} };
  const admin = { ...skillPrincipal, role: "admin" as const };
  const view = render(<SkillDraftPanel {...props} principal={admin} />);
  await screen.findByText("校验通过");
  fireEvent.change(screen.getByRole("combobox", { name: "安装范围" }), { target: { value: "workspace" } });
  fireEvent.click(screen.getByRole("button", { name: "确认安装" }));
  await screen.findByText("Response lost");
  view.rerender(<SkillDraftPanel {...props} principal={{ ...skillPrincipal, role: "editor" }} />);
  expect(screen.getByRole("button", { name: "重试原安装" })).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "重试原安装" }));
  expect(api.installProjectSkillDraft).toHaveBeenCalledTimes(1);
  view.rerender(<SkillDraftPanel {...props} principal={admin} />);
  fireEvent.click(screen.getByRole("button", { name: "重试原安装" }));
  await waitFor(() => expect(api.installProjectSkillDraft).toHaveBeenCalledTimes(2));
  expect(vi.mocked(api.installProjectSkillDraft).mock.calls[1]).toEqual(vi.mocked(api.installProjectSkillDraft).mock.calls[0]);
  expect(vi.mocked(api.installProjectSkillDraft).mock.calls[1][1].scope).toBe("workspace");
  await screen.findByText("Response lost");
});

describe("Skill draft confirmation", () => {
  it("requires an explicit install click and exposes a version-bound ZIP", async () => {
    const onInstalled = vi.fn();
    render(<SkillDraftPanel projectID="p" root={draft.root_path} onBack={() => {}} onInstalled={onInstalled} />);
    await screen.findByText("校验通过");
    expect(api.installProjectSkillDraft).not.toHaveBeenCalled();
    expect(screen.getByRole("link", { name: "下载 Skill ZIP" }).getAttribute("href")).toBe(api.projectSkillDraftDownloadURL("p", draft.root_path, draft.snapshot_hash));
    expect(screen.queryByRole("option", { name: "整个工作区" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "确认安装" }));
    await screen.findByText("已安装，后续对话可调用。");
    expect(api.installProjectSkillDraft).toHaveBeenCalledWith("p", { root_path: draft.root_path, snapshot_hash: draft.snapshot_hash, scope: "project", installation_id: "", expected_active_version_id: "" }, expect.any(String));
    expect(onInstalled).toHaveBeenCalledTimes(1);
  });
  it("binds upgrades to the scope and current version shown before confirmation", async () => {
    vi.mocked(api.installProjectSkillDraft).mockResolvedValue(skillInstallResult(draft, "user", "install-user"));
    vi.mocked(api.previewProjectSkillDraft).mockResolvedValue({ ...draft, installations: [{ skill_installation_id: "install-user", scope: "user", scope_ref: "u", active_version_id: "version-old", version: "0.9.0" }] });
    render(<SkillDraftPanel projectID="p" root={draft.root_path} onBack={() => {}} />);
    await screen.findByText("校验通过");
    fireEvent.change(screen.getByRole("combobox", { name: "安装范围" }), { target: { value: "user" } });
    expect(screen.getByText(/当前版本：0.9.0/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "确认升级" }));
    await screen.findByText("已安装，后续对话可调用。");
    expect(api.installProjectSkillDraft).toHaveBeenCalledWith("p", expect.objectContaining({ scope: "user", installation_id: "install-user", expected_active_version_id: "version-old" }), expect.any(String));
  });
  it("keeps an uncertain request's idempotency key for a user retry", async () => {
    vi.mocked(api.installProjectSkillDraft).mockRejectedValueOnce(new Error("连接中断")).mockResolvedValueOnce(result);
    render(<SkillDraftPanel projectID="p" root={draft.root_path} onBack={() => {}} />);
    fireEvent.click(await screen.findByRole("button", { name: "确认安装" }));
    await screen.findByText("连接中断");
    fireEvent.click(screen.getByRole("button", { name: "重试原安装" }));
    await screen.findByText("已安装，后续对话可调用。");
    expect(vi.mocked(api.installProjectSkillDraft).mock.calls[0]).toEqual(vi.mocked(api.installProjectSkillDraft).mock.calls[1]);
  });
  it("invalidates changed drafts instead of silently installing different contents", async () => {
    vi.mocked(api.installProjectSkillDraft).mockRejectedValueOnce(new ApiError("SKILL_DRAFT_CHANGED", "草稿已变化", 409));
    render(<SkillDraftPanel projectID="p" root={draft.root_path} onBack={() => {}} />);
    fireEvent.click(await screen.findByRole("button", { name: "确认安装" }));
    await screen.findByText("草稿已变化");
    expect(screen.queryByRole("button", { name: "确认安装" })).toBeNull();
    expect(screen.queryByRole("link", { name: "下载 Skill ZIP" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "重新校验 Skill" }));
    await screen.findByRole("button", { name: "确认安装" });
    expect(api.installProjectSkillDraft).toHaveBeenCalledTimes(1);
  });
  it("does not export or install invalid drafts", async () => {
    vi.mocked(api.previewProjectSkillDraft).mockResolvedValue({ ...draft, status: "invalid", manifest: undefined, diagnostics: [{ code: "SKILL_PACKAGE_INVALID", message: "缺少 description" }] });
    render(<SkillDraftPanel projectID="p" root={draft.root_path} onBack={() => {}} />);
    await screen.findByText("缺少 description");
    expect(screen.queryByRole("button", { name: "确认安装" })).toBeNull();
    expect(screen.queryByRole("link")).toBeNull();
  });
  it("supports read-only users without giving them an install control", async () => {
    vi.mocked(api.getSkillManagementOptions).mockResolvedValue({ install_scopes: [], system_read_only: true });
    render(<SkillDraftPanel projectID="p" root={draft.root_path} onBack={() => {}} />);
    await screen.findByText("当前角色无安装权限。");
    expect(screen.getByRole("link", { name: "下载 Skill ZIP" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "确认安装" })).toBeNull();
  });
  it("does not submit twice while waiting and distinguishes a refresh failure", async () => {
    let resolve!: (value: ProjectSkillInstallResult) => void;
    vi.mocked(api.installProjectSkillDraft).mockReturnValue(new Promise((done) => { resolve = done; }));
    render(<SkillDraftPanel projectID="p" root={draft.root_path} onBack={() => {}} onInstalled={async () => { throw new Error("offline"); }} />);
    const button = await screen.findByRole("button", { name: "确认安装" });
    fireEvent.click(button);
    fireEvent.click(button);
    expect(api.installProjectSkillDraft).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("combobox", { name: "安装范围" }).hasAttribute("disabled")).toBe(true);
    resolve(result);
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("Skill 已安装"));
    expect(screen.queryByRole("button", { name: "确认安装" })).toBeNull();
  });
});

it.each(["project", "root", "hash", "capability", "targets"])("rejects an invalid %s draft preview before installing or downloading", async (field) => {
  const invalid = skillDraft();
  if (field === "project") invalid.project_id = "foreign";
  if (field === "root") invalid.root_path = "foreign";
  if (field === "hash") invalid.snapshot_hash = "";
  if (field === "capability") invalid.capability_id = undefined;
  if (field === "targets") invalid.installations = [{ skill_installation_id: "id", active_version_id: "", scope: "project", scope_ref: "p", version: "1" }];
  vi.mocked(api.previewProjectSkillDraft).mockResolvedValue(invalid);
  render(<SkillDraftPanel projectID="p" root={draft.root_path} onBack={vi.fn()} />);
  await screen.findByRole("alert");
  expect(screen.queryByRole("link")).toBeNull();
  expect(screen.queryByRole("button", { name: "确认安装" })).toBeNull();
  expect(api.installProjectSkillDraft).not.toHaveBeenCalled();
});

it.each(["project", "workspace", "scope", "scope-ref", "installation", "capability", "name", "version", "version-owner", "hash", "mode"])("rejects a wrong %s installation receipt and retains its original retry", async (field) => {
  const invalid = skillInstallResult();
  if (field === "project") invalid.receipt.project_id = "foreign";
  if (field === "workspace") invalid.installation.workspace_id = "foreign";
  if (field === "scope") invalid.installation.scope = "workspace";
  if (field === "scope-ref") invalid.installation.scope_ref = "foreign";
  if (field === "installation") invalid.receipt.skill_installation_id = "foreign";
  if (field === "capability") invalid.installation.capability_id = "foreign";
  if (field === "name") invalid.installation.skill_name = "foreign";
  if (field === "version") invalid.installation.versions[0].version = "wrong";
  if (field === "version-owner") invalid.installation.versions[0].skill_installation_id = "foreign";
  if (field === "hash") invalid.installation.versions[0].content_hash = "wrong";
  if (field === "mode") invalid.installation.versions[0].execution_mode = "stateful";
  vi.mocked(api.installProjectSkillDraft).mockResolvedValueOnce(invalid).mockResolvedValue(result);
  const onInstalled = vi.fn();
  render(<SkillDraftPanel projectID="p" root={draft.root_path} onBack={vi.fn()} onInstalled={onInstalled} />);
  fireEvent.click(await screen.findByRole("button", { name: "确认安装" }));
  await screen.findByRole("alert");
  expect(onInstalled).not.toHaveBeenCalled();
  expect(screen.queryByText("已安装，后续对话可调用。")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "重试原安装" }));
  await screen.findByText("已安装，后续对话可调用。");
  expect(vi.mocked(api.installProjectSkillDraft).mock.calls[1]).toEqual(vi.mocked(api.installProjectSkillDraft).mock.calls[0]);
});

it("preserves an unknown upgrade across new file revisions and freezes scope and validation", async () => {
  vi.mocked(api.installProjectSkillDraft).mockRejectedValueOnce(new ApiError("COMMAND_IN_PROGRESS", "Still pending", 409)).mockResolvedValue(result);
  const props = { projectID: "p", root: draft.root_path, onBack: vi.fn() };
  const view = render(<SkillDraftPanel {...props} revisionKey="one" />);
  fireEvent.click(await screen.findByRole("button", { name: "确认安装" }));
  await screen.findByText("Still pending");
  view.rerender(<SkillDraftPanel {...props} revisionKey="two" />);
  expect(api.previewProjectSkillDraft).toHaveBeenCalledOnce();
  for (const name of ["返回文件", "重新校验 Skill"]) expect(screen.getByRole("button", { name }).hasAttribute("disabled")).toBe(true);
  expect(screen.getByRole("combobox", { name: "安装范围" }).hasAttribute("disabled")).toBe(true);
  expect(screen.queryByRole("link")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "重试原安装" }));
  await screen.findByText("已安装，后续对话可调用。");
  expect(vi.mocked(api.installProjectSkillDraft).mock.calls[1]).toEqual(vi.mocked(api.installProjectSkillDraft).mock.calls[0]);
});

it("requires explicit revalidation before installing after a file revision changed", async () => {
  const props = { projectID: "p", root: draft.root_path, onBack: vi.fn() };
  const view = render(<SkillDraftPanel {...props} revisionKey="one" />);
  await screen.findByText("校验通过");
  view.rerender(<SkillDraftPanel {...props} revisionKey="two" />);
  expect(screen.getByRole("button", { name: "确认安装" }).hasAttribute("disabled")).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "重新校验 Skill" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "确认安装" }).hasAttribute("disabled")).toBe(false));
  expect(api.previewProjectSkillDraft).toHaveBeenCalledTimes(2);
});

it("does not claim that an installed historical version is currently enabled", async () => {
  const historical = skillInstallResult(); historical.installation.active_version_id = "newer-version";
  vi.mocked(api.installProjectSkillDraft).mockResolvedValue(historical);
  render(<SkillDraftPanel projectID="p" root={draft.root_path} onBack={vi.fn()} />);
  fireEvent.click(await screen.findByRole("button", { name: "确认安装" }));
  await screen.findByText("该版本已保存，但已不是当前启用版本。");
  expect(screen.queryByText("已安装，后续对话可调用。")).toBeNull();
});

it("retries a catalog refresh without submitting another install", async () => {
  const onInstalled = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValue(undefined);
  render(<SkillDraftPanel projectID="p" root={draft.root_path} onBack={vi.fn()} onInstalled={onInstalled} />);
  fireEvent.click(await screen.findByRole("button", { name: "确认安装" }));
  fireEvent.click(await screen.findByRole("button", { name: "刷新能力目录" }));
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  expect(onInstalled).toHaveBeenCalledTimes(2); expect(api.installProjectSkillDraft).toHaveBeenCalledOnce();
});

it("hides write controls on a role change and ignores a late install response", async () => {
  let resolve!: (saved: ProjectSkillInstallResult) => void;
  vi.mocked(api.installProjectSkillDraft).mockReturnValue(new Promise((done) => { resolve = done; }));
  const props = { projectID: "p", root: draft.root_path, onBack: vi.fn(), onInstalled: vi.fn() };
  const view = render(<SkillDraftPanel {...props} />);
  fireEvent.click(await screen.findByRole("button", { name: "确认安装" }));
  view.rerender(<SkillDraftPanel {...props} principal={{ ...skillPrincipal, role: "viewer" }} />);
  await act(async () => resolve(result));
  expect(screen.queryByRole("button", { name: "重试原安装" })).toBeNull();
  expect(screen.queryByText("已安装，后续对话可调用。")).toBeNull();
  expect(props.onInstalled).not.toHaveBeenCalled();
});
