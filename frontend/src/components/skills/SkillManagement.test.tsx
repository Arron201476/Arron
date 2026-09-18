// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../../api";
import { dynamicStoryEntry } from "../../testComposerFixtures";
import type { ProjectWorkspaceProjection, ScriptSandboxPolicy, SkillDirectoryUpdate, SkillInstallation, SkillLifecycleResult, SkillPackageResult, WorkspaceProjection } from "../../types";
import { SkillManagement as SkillManagementView } from "./SkillManagement";
import { lifecycleResult, packageResult } from "./skillLifecycleTestFixtures";
import { webcrypto } from "node:crypto";
import { IDBFactory } from "fake-indexeddb";
import { skillMutationJournal, SkillJournalError } from "../../skillMutationJournal";
import { skillArchiveBytes } from "../../skillPackage";
import type { ComponentProps } from "react";

function SkillManagement(props: Omit<ComponentProps<typeof SkillManagementView>, "userID" | "workspaceID"> & Partial<Pick<ComponentProps<typeof SkillManagementView>, "userID" | "workspaceID">>) {
  return <SkillManagementView userID="user-1" workspaceID="workspace-default" {...props} />;
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("Skill management", () => {
  it("lists directory-discovered skills even with no uploaded installation", async () => {
    vi.spyOn(api, "listSkills").mockResolvedValue([]);
    vi.spyOn(api, "getWorkspaceProjection").mockResolvedValue({
      ...projection,
      registries: { ...projection.registries, composer: [{ ...dynamicStoryEntry, skill: { ...manifest, path: ".agents/skills/dynamic-story/SKILL.md" } }] },
    });
    render(<SkillManagement workspaceName="测试工作区" />);
    expect(await screen.findByRole("heading", { name: "动态故事评审" })).toBeTruthy();
    expect(screen.getByText(".agents/skills/dynamic-story/SKILL.md")).toBeTruthy();
    expect(screen.getByText("1 个可用 · 0 个受管理")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "卸载" })).toBeNull();
    expect(screen.queryByRole("button", { name: "禁用" })).toBeNull();
  });

  beforeEach(() => {
    vi.stubGlobal("crypto", webcrypto);
    vi.stubGlobal("indexedDB", new IDBFactory());
    vi.spyOn(api, "listProjects").mockResolvedValue([]);
    vi.spyOn(api, "getSkillManagementOptions").mockResolvedValue({ install_scopes: ["user", "project", "workspace"], system_read_only: true, directory_updates: true, lifecycle_commands: true, package_commands: true });
    vi.spyOn(api, "listAgentTools").mockResolvedValue([]);
    vi.spyOn(api, "getAgentToolConfiguration").mockResolvedValue({ workspace_id: "workspace-test", version: 0, options: [] });
    vi.spyOn(api, "listSkills").mockResolvedValue([installation]);
    vi.spyOn(api, "listSkillInstallAttempts").mockResolvedValue([{
      skill_install_attempt_id: "attempt-1",
      source_type: "upload",
      source_name: "dynamic-story.zip",
      status: "completed",
      diagnostics: [{ code: "PACKAGE_VALIDATED", message: "validated" }],
      created_at: "2026-09-04T00:00:00Z",
    }]);
    vi.spyOn(api, "getWorkspaceProjection").mockResolvedValue(projection);
    vi.spyOn(api, "getScriptSandboxPolicy").mockResolvedValue(scriptPolicy);
  });

  it("round-trips FileReader archive buffers through the durable journal", async () => {
    const owner = { userID: "user-1", workspaceID: "workspace-default" };
    const request = { package: true as const, action: "install_zip" as const, key: "f0000000-0000-4000-8000-000000000001", file: new File([new Uint8Array([0, 255, 128])], "原包.zip"), target: { scope: "workspace" as const } };
    expect((await skillMutationJournal.claim(owner, request)).accepted).toBe(true);
    const recovered = await skillMutationJournal.read(owner);
    expect(recovered?.key).toBe(request.key);
    await skillMutationJournal.requireCurrent(owner, request);
  });

  it("rescans server directories and shows scanner diagnostics", async () => {
    const refresh = vi.spyOn(api, "refreshSkills").mockResolvedValue({ diagnostics: [{ code: "SKILL_INVALID", path: "skills/broken", message: "Invalid manifest" }] });
    render(<SkillManagement workspaceName="测试工作区" />);
    await screen.findByRole("heading", { name: "动态故事评审" });
    fireEvent.click(screen.getByRole("button", { name: "刷新 Skill 列表" }));
    await screen.findByRole("heading", { name: "目录扫描诊断" });
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Invalid manifest")).toBeTruthy();
    expect(api.listSkills).toHaveBeenCalledTimes(2);
  });

  it("adopts the exact discovered snapshot and opens its lifecycle controls", async () => {
    vi.mocked(api.listSkills).mockResolvedValueOnce([]).mockResolvedValue([installation]);
    vi.mocked(api.getWorkspaceProjection).mockResolvedValue({ ...projection, registries: { ...projection.registries, composer: [{ ...dynamicStoryEntry, skill: manifest }] } });
    const adopt = vi.spyOn(api, "installDiscoveredSkill").mockImplementation((capabilityID, version, contentHash, target, key) => packageResult(installation, { package: true, action: "adopt_directory", capabilityID, version, contentHash, target, key }));
    render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "纳入工作区管理" }));
    await screen.findByRole("button", { name: "禁用" });
    expect(adopt).toHaveBeenCalledWith(dynamicStoryEntry.capability_id, dynamicStoryEntry.version, manifest.content_hash, { scope: "workspace" }, expect.any(String));
    expect(screen.queryByRole("button", { name: "纳入工作区管理" })).toBeNull();
  });

  it("keeps the directory selected when adoption fails", async () => {
    vi.mocked(api.listSkills).mockResolvedValue([]);
    vi.mocked(api.getWorkspaceProjection).mockResolvedValue({ ...projection, registries: { ...projection.registries, composer: [{ ...dynamicStoryEntry, skill: manifest }] } });
    vi.spyOn(api, "installDiscoveredSkill").mockRejectedValue(new Error("request failed"));
    render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "纳入工作区管理" }));
    expect(await screen.findByText("无法读取 Skill 管理状态。")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "禁用" })).toBeNull();
  });

  it("shows versions, trusted views, dependencies, and install diagnostics", async () => {
    render(<SkillManagement workspaceName="测试工作区" />);

    expect(await screen.findByRole("heading", { name: "动态故事评审" })).toBeTruthy();
    expect(screen.getAllByText("dynamic_story_skill")).toHaveLength(2);
    expect(screen.getByText("source_materials")).toBeTruthy();
    expect(screen.getByText("json_schema")).toBeTruthy();
    expect(screen.getByText("story_search")).toBeTruthy();
    expect(screen.getByText(/PACKAGE_VALIDATED/)).toBeTruthy();
    expect(screen.getAllByText("1.1.0")).toHaveLength(2);
  });

  it("uploads a ZIP as a raw Skill package and rejects other file types", async () => {
    const install = vi.spyOn(api, "installSkill").mockImplementation((file, target, key) => packageResult(installation, { package: true, action: "install_zip", file, target, key }));
    const { container } = render(<SkillManagement workspaceName="测试工作区" />);
    await screen.findByRole("heading", { name: "动态故事评审" });
    const input = container.querySelectorAll<HTMLInputElement>('input[type="file"]')[0];

    fireEvent.click(screen.getByRole("button", { name: "安装 Skill" }));
    fireEvent.change(input, { target: { files: [new File(["bad"], "skill.txt", { type: "text/plain" })] } });
    expect(screen.getByText("Skill 安装包必须是 ZIP 文件。")).toBeTruthy();
    expect(install).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "安装 Skill" }));
    fireEvent.change(input, { target: { files: [new File(["zip"], "dynamic-story.zip", { type: "application/zip" })] } });
    await waitFor(() => expect(install).toHaveBeenCalledWith(expect.objectContaining({ name: "dynamic-story.zip" }), { scope: "workspace" }, expect.any(String)));
    await screen.findByText("原安装已完成。");
  });

  it("installs personal skills without allowing editors to change workspace packages", async () => {
    const install = vi.spyOn(api, "installSkill").mockImplementation((file, target, key) => packageResult(installation, { package: true, action: "install_zip", file, target, key }));
    const { container } = render(<SkillManagement workspaceName="测试工作区" role="editor" />);
    await screen.findByRole("heading", { name: "动态故事评审" });
    expect((screen.getByRole("combobox", { name: "安装范围" }) as HTMLSelectElement).value).toBe("user");
    expect(screen.queryByRole("option", { name: "工作区" })).toBeNull();
    expect((screen.getByRole("button", { name: "禁用" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "上传新版本" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "检查目录更新" }) as HTMLButtonElement).disabled).toBe(true);
    const file = new File(["zip"], "personal.zip", { type: "application/zip" });
    fireEvent.click(screen.getByRole("button", { name: "安装 Skill" }));
    fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [file] } });
    await waitFor(() => expect(install).toHaveBeenCalledWith(file, { scope: "user" }, expect.any(String)));
    await screen.findByText("原安装已完成。");
  });

  it("requires a project and sends the selected project scope", async () => {
    vi.mocked(api.listProjects).mockResolvedValue([{
      project_id: "prj_scope", title: "目标作品", workspace_id: "ws", owner_user_id: "user", version: 0,
      status: "active", primary_conversation_id: "conv", active_write_run_id: null, current_capability_id: null,
      latest_capability_id: null, latest_run_status: null, current_focus_artifact_version_id: null, updated_at: "2026-09-06T00:00:00Z",
    }]);
    vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue({ ...projection, registry_revision: "scope" } as ProjectWorkspaceProjection);
    const install = vi.spyOn(api, "installSkill").mockImplementation((file, target, key) => packageResult(installation, { package: true, action: "install_zip", file, target, key }));
    const { container } = render(<SkillManagement workspaceName="测试工作区" />);
    await screen.findByRole("heading", { name: "动态故事评审" });
    fireEvent.change(screen.getByRole("combobox", { name: "安装范围" }), { target: { value: "project" } });
    expect((screen.getByRole("button", { name: "安装 Skill" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByRole("combobox", { name: "安装项目" }), { target: { value: "prj_scope" } });
    await waitFor(() => expect((screen.getByRole("button", { name: "安装 Skill" }) as HTMLButtonElement).disabled).toBe(false));
    const file = new File(["zip"], "project.zip", { type: "application/zip" });
    fireEvent.click(screen.getByRole("button", { name: "安装 Skill" }));
    fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [file] } });
    await waitFor(() => expect(install).toHaveBeenCalledWith(file, { scope: "project", project_id: "prj_scope" }, expect.any(String)));
    await screen.findByText("原安装已完成。");
  });

  it("does not offer scoped installation against an older backend", async () => {
    vi.mocked(api.getSkillManagementOptions).mockRejectedValue(new ApiError("NOT_FOUND", "Not found", 404));
    render(<SkillManagement workspaceName="测试工作区" />);
    await screen.findByRole("heading", { name: "动态故事评审" });
    expect((screen.getByRole("option", { name: "个人" }) as HTMLOptionElement).disabled).toBe(true);
    expect((screen.getByRole("option", { name: "项目" }) as HTMLOptionElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "安装 Skill" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByRole("button", { name: "检查目录更新" })).toBeNull();
  });

  it("uses lifecycle APIs for version activation, disabling, and uninstalling", async () => {
    const activate = vi.spyOn(api, "activateSkillVersion").mockImplementation(async (item, version, key) => lifecycleResult(item, "activate", key, version));
    const enable = vi.spyOn(api, "setSkillEnabled").mockImplementation(async (item, enabled, key) => lifecycleResult(item, enabled ? "enable" : "disable", key));
    const uninstall = vi.spyOn(api, "uninstallSkill").mockImplementation(async (item, key) => lifecycleResult(item, "uninstall", key));
    vi.spyOn(window, "confirm").mockReturnValue(true);
    render(<SkillManagement workspaceName="测试工作区" />);
    await screen.findByRole("heading", { name: "动态故事评审" });

    fireEvent.click(screen.getByRole("button", { name: "设为当前" }));
    await waitFor(() => expect(activate).toHaveBeenCalledWith(installation, "1.0.0", expect.any(String)));

    await waitFor(() => expect((screen.getByRole("button", { name: "禁用" }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: "禁用" }));
    await waitFor(() => expect(enable).toHaveBeenCalledWith(installation, false, expect.any(String)));

    await waitFor(() => expect((screen.getByRole("button", { name: "卸载" }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: "卸载" }));
    await waitFor(() => expect(uninstall).toHaveBeenCalledWith(installation, expect.any(String)));
  });

  it("shows declared scripts and requires confirmation before enabling execution", async () => {
    const updatePolicy = vi.spyOn(api, "updateScriptSandboxPolicy").mockResolvedValue({
      ...scriptPolicy,
      enabled: true,
      version: 1,
    });
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    render(<SkillManagement workspaceName="测试工作区" />);

    expect(await screen.findByText("scripts/render.py")).toBeTruthy();
    expect(screen.getByText(/Render one result/)).toBeTruthy();
    const toggle = screen.getByRole("button", { name: "已关闭" });
    fireEvent.click(toggle);
    expect(updatePolicy).not.toHaveBeenCalled();
    fireEvent.click(toggle);

    await waitFor(() => expect(updatePolicy).toHaveBeenCalledWith(scriptPolicy, true));
    expect(confirm).toHaveBeenCalledTimes(2);
  });

  it("previews a directory version before applying its exact snapshot", async () => {
    const preview: SkillDirectoryUpdate = { skill_installation_id: "skill-1", capability_id: "dynamic_story_skill", current_version_id: "version-2", version: "1.2.0", content_hash: "sha256:new-version", source_path: ".agents/skills/dynamic-story/SKILL.md", source_scope: "workspace", status: "available" };
    const inspect = vi.spyOn(api, "previewSkillDirectoryUpdate").mockResolvedValue(preview);
    const update = vi.spyOn(api, "updateSkillFromDirectory").mockImplementation((preview, item, key) => packageResult(item, { package: true, action: "update_directory", preview, installation: item, key }));
    render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "检查目录更新" }));
    expect(await screen.findByText(preview.source_path!, { exact: false })).toBeTruthy();
    expect(screen.getByText(preview.content_hash!)).toBeTruthy();
    expect(inspect).toHaveBeenCalledWith("skill-1");
    expect(update).not.toHaveBeenCalled();
    const apply = screen.getByRole("button", { name: "更新到目录版本" }) as HTMLButtonElement;
    await waitFor(() => expect(apply.disabled).toBe(false));
    fireEvent.click(apply);
    await waitFor(() => expect(update).toHaveBeenCalledWith(preview, installation, expect.any(String)));
    await waitFor(() => expect(screen.queryByRole("heading", { name: "目录版本" })).toBeNull());
  });

  it("does not offer applying a conflicting directory version", async () => {
    vi.spyOn(api, "previewSkillDirectoryUpdate").mockResolvedValue({ skill_installation_id: "skill-1", capability_id: "dynamic_story_skill", current_version_id: "version-2", version: "1.1.0", content_hash: "sha256:conflict", source_path: ".agents/skills/dynamic-story/SKILL.md", status: "version_conflict", user_message: "目录版本号未更新。" });
    render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "检查目录更新" }));
    expect(await screen.findByText("目录版本号未更新。")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "更新到目录版本" })).toBeNull();
  });

  it("offers script sandbox controls for a directory skill without adopting it", async () => {
    vi.mocked(api.listSkills).mockResolvedValue([]);
    vi.mocked(api.getWorkspaceProjection).mockResolvedValue({ ...projection, registries: { ...projection.registries, composer: [{ ...dynamicStoryEntry, skill: manifest }] } });
    const update = vi.spyOn(api, "updateScriptSandboxPolicy").mockResolvedValue({ ...scriptPolicy, enabled: true, version: 1 });
    const adopt = vi.spyOn(api, "installDiscoveredSkill");
    vi.spyOn(window, "confirm").mockReturnValue(true);
    render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "已关闭" }));
    await waitFor(() => expect(update).toHaveBeenCalledWith(scriptPolicy, true));
    expect(adopt).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "卸载" })).toBeNull();
  });

  it("keeps a confirmed operation separate from failed refresh and only rereads on retry", async () => {
    vi.mocked(api.listSkills).mockResolvedValueOnce([installation]).mockRejectedValueOnce(new Error("offline")).mockResolvedValue([{ ...installation, enabled: false }]);
    const disable = vi.spyOn(api, "setSkillEnabled").mockImplementation(async (item, _, key) => lifecycleResult(item, "disable", key));
    const uninstall = vi.spyOn(api, "uninstallSkill");
    const scan = vi.spyOn(api, "refreshSkills");
    render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "禁用" }));
    expect(await screen.findByText("操作已提交，但最新目录尚未读取。请刷新状态，不要重复提交。")).toBeTruthy();
    for (const name of ["禁用", "卸载", "上传新版本", "安装 Skill", "设为当前", "已关闭", "刷新 Skill 列表"]) {
      expect((screen.getByRole("button", { name }) as HTMLButtonElement).disabled).toBe(true);
    }
    fireEvent.click(screen.getByRole("button", { name: "刷新状态" }));
    await waitFor(() => expect((screen.getByRole("button", { name: "启用" }) as HTMLButtonElement).disabled).toBe(false));
    expect(disable).toHaveBeenCalledTimes(1);
    expect(uninstall).not.toHaveBeenCalled();
    expect(scan).not.toHaveBeenCalled();
    expect(screen.queryByText("操作已提交，但最新目录尚未读取。请刷新状态，不要重复提交。")).toBeNull();
  });

  it("offers read-only recovery even when the initial snapshot failed", async () => {
    vi.mocked(api.listSkills).mockRejectedValueOnce(new Error("offline")).mockResolvedValue([installation]);
    render(<SkillManagement workspaceName="测试工作区" role="viewer" />);
    fireEvent.click(await screen.findByRole("button", { name: "刷新状态" }));
    await screen.findByRole("heading", { name: "动态故事评审" });
    expect((screen.getByRole("button", { name: "禁用" }) as HTMLButtonElement).disabled).toBe(true);
    expect(api.listSkills).toHaveBeenCalledTimes(2);
  });

  it("rejects synchronous competing submissions and selection changes", async () => {
    let resolve!: (value: SkillLifecycleResult) => void;
    const disable = vi.spyOn(api, "setSkillEnabled").mockImplementation(() => new Promise((done) => { resolve = done; }));
    const uninstall = vi.spyOn(api, "uninstallSkill");
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const second = { ...installation, skill_installation_id: "skill-2", skill_name: "other-skill", capability_id: "other_skill" };
    vi.mocked(api.listSkills).mockResolvedValue([installation, second]);
    render(<SkillManagement workspaceName="测试工作区" />);
    const button = await screen.findByRole("button", { name: "禁用" });
    const remove = screen.getByRole("button", { name: "卸载" });
    const other = screen.getByRole("button", { name: /other-skill/ });
    act(() => { button.click(); button.click(); remove.click(); other.click(); });
    await waitFor(() => expect(disable).toHaveBeenCalledTimes(1));
    expect(uninstall).not.toHaveBeenCalled();
    expect(other.className).not.toContain("selected");
    await act(async () => { resolve(lifecycleResult(installation, "disable", disable.mock.calls[0][2])); });
    await waitFor(() => expect((screen.getByRole("button", { name: "禁用" }) as HTMLButtonElement).disabled).toBe(false));
  });

  it("ignores an old adoption result after role changes", async () => {
    vi.mocked(api.listSkills).mockResolvedValue([]);
    vi.mocked(api.getWorkspaceProjection).mockResolvedValue({ ...projection, registries: { ...projection.registries, composer: [{ ...dynamicStoryEntry, skill: manifest }] } });
    let resolve!: (value: SkillPackageResult) => void;
    const adopt = vi.spyOn(api, "installDiscoveredSkill").mockImplementation(() => new Promise((done) => { resolve = done; }));
    const { rerender } = render(<SkillManagement workspaceName="测试工作区" role="owner" />);
    fireEvent.click(await screen.findByRole("button", { name: "纳入工作区管理" }));
    await waitFor(() => expect(adopt).toHaveBeenCalledTimes(1));
    rerender(<SkillManagement workspaceName="测试工作区" role="viewer" />);
    await waitFor(() => expect(api.listSkills).toHaveBeenCalledTimes(2));
    await act(async () => { resolve(await packageResult(installation, { package: true, action: "adopt_directory", capabilityID: dynamicStoryEntry.capability_id, version: dynamicStoryEntry.version, contentHash: manifest.content_hash, target: { scope: "workspace" }, key: adopt.mock.calls[0][4] })); });
    expect(api.listSkills).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("button", { name: "卸载" })).toBeNull();
    expect((screen.getByRole("button", { name: "纳入工作区管理" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it.each(["skill_installation_id", "capability_id", "current_version_id"] as const)("rejects a directory preview with mismatched %s", async (field) => {
    vi.spyOn(api, "previewSkillDirectoryUpdate").mockResolvedValue({ skill_installation_id: "skill-1", capability_id: "dynamic_story_skill", current_version_id: "version-2", version: "1.2.0", content_hash: "sha256:new", status: "available", [field]: "wrong" });
    const update = vi.spyOn(api, "updateSkillFromDirectory");
    render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "检查目录更新" }));
    expect(await screen.findByText("目录预览与所选 Skill 或当前版本不一致，请刷新后重试。")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "更新到目录版本" })).toBeNull();
    expect(update).not.toHaveBeenCalled();
  });

  it("does not retarget a ZIP after the installation scope changed while choosing a file", async () => {
    const install = vi.spyOn(api, "installSkill");
    const { container } = render(<SkillManagement workspaceName="测试工作区" />);
    await screen.findByRole("heading", { name: "动态故事评审" });
    fireEvent.click(screen.getByRole("button", { name: "安装 Skill" }));
    fireEvent.change(screen.getByRole("combobox", { name: "安装范围" }), { target: { value: "user" } });
    fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [new File(["zip"], "new.zip")] } });
    expect(screen.getByText("安装目标或版本已变化，请重新选择安装包。")).toBeTruthy();
    expect(install).not.toHaveBeenCalled();
  });

  it("does not upload an upgrade to a different selected installation", async () => {
    const upgrade = vi.spyOn(api, "upgradeSkill");
    vi.mocked(api.listSkills).mockResolvedValue([installation, { ...installation, skill_installation_id: "skill-2", skill_name: "other-skill", capability_id: "other_skill" }]);
    const { container } = render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "上传新版本" }));
    fireEvent.click(screen.getByRole("button", { name: /other-skill/ }));
    fireEvent.change(container.querySelectorAll('input[type="file"]')[1], { target: { files: [new File(["zip"], "new.zip")] } });
    expect(screen.getByText("安装目标或版本已变化，请重新选择安装包。")).toBeTruthy();
    expect(upgrade).not.toHaveBeenCalled();
  });

  it("uploads a ZIP upgrade when its chosen target is unchanged", async () => {
    const upgrade = vi.spyOn(api, "upgradeSkill").mockImplementation((item, file, key) => packageResult(item, { package: true, action: "upgrade_zip", file, installation: item, key }));
    const { container } = render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "上传新版本" }));
    const file = new File(["zip"], "new.zip");
    fireEvent.change(container.querySelectorAll('input[type="file"]')[1], { target: { files: [file] } });
    await waitFor(() => expect(upgrade).toHaveBeenCalledWith(installation, file, expect.any(String)));
    await waitFor(() => expect((screen.getByRole("button", { name: "上传新版本" }) as HTMLButtonElement).disabled).toBe(false));
  });

  it.each((["install_zip", "upgrade_zip", "adopt_directory", "update_directory"] as const).flatMap((action) => (["refresh", "remount"] as const).map((recovery) => ({ action, recovery }))))("recovers $action with the exact original package request after $recovery", async ({ action, recovery }) => {
    const preview: SkillDirectoryUpdate = { skill_installation_id: installation.skill_installation_id, capability_id: installation.capability_id, current_version_id: installation.active_version_id!, version: "1.2.0", content_hash: "sha256:next", status: "available" };
    const fail = new ApiError("COMMAND_IN_PROGRESS", "安装结果未知。", 409);
    const install = vi.spyOn(api, "installSkill").mockRejectedValueOnce(fail).mockImplementation((file, target, key) => packageResult(installation, { package: true, action: "install_zip", file, target, key }));
    const upgrade = vi.spyOn(api, "upgradeSkill").mockRejectedValueOnce(fail).mockImplementation((item, file, key) => packageResult(item, { package: true, action: "upgrade_zip", installation: item, file, key }));
    const adopt = vi.spyOn(api, "installDiscoveredSkill").mockRejectedValueOnce(fail).mockImplementation((capabilityID, version, contentHash, target, key) => packageResult(installation, { package: true, action: "adopt_directory", capabilityID, version, contentHash, target, key }));
    const update = vi.spyOn(api, "updateSkillFromDirectory").mockRejectedValueOnce(fail).mockImplementation((p, item, key) => packageResult(item, { package: true, action: "update_directory", preview: p, installation: item, key }));
    vi.spyOn(api, "previewSkillDirectoryUpdate").mockResolvedValue(preview);
    if (action === "adopt_directory") {
      vi.mocked(api.listSkills).mockResolvedValue([]);
      vi.mocked(api.getWorkspaceProjection).mockResolvedValue({ ...projection, registries: { ...projection.registries, composer: [{ ...dynamicStoryEntry, skill: manifest }] } });
    }
    let view = render(<SkillManagement workspaceName="测试工作区" userID="user-1" workspaceID={installation.workspace_id} />);
    const { container } = view;
    await screen.findByRole("heading", { name: "动态故事评审" });
    const file = new File([new Uint8Array([0, 255, 128, 13, 10])], "中文技能.zip", { type: "application/zip", lastModified: 1234 });
    if (action === "adopt_directory") fireEvent.click(screen.getByRole("button", { name: "纳入工作区管理" }));
    else if (action === "update_directory") {
      fireEvent.click(screen.getByRole("button", { name: "检查目录更新" }));
      const button = await screen.findByRole("button", { name: "更新到目录版本" });
      await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(false));
      fireEvent.click(button);
    } else {
      fireEvent.click(screen.getByRole("button", { name: action === "install_zip" ? "安装 Skill" : "上传新版本" }));
      fireEvent.change(container.querySelectorAll('input[type="file"]')[action === "install_zip" ? 0 : 1], { target: { files: [file] } });
    }
    await screen.findByText("安装结果未知。");
    const save = { install_zip: install, upgrade_zip: upgrade, adopt_directory: adopt, update_directory: update }[action];
    expect(save).toHaveBeenCalledTimes(1);
    expect((screen.getByRole("button", { name: "安装 Skill" }) as HTMLButtonElement).disabled).toBe(true);
    vi.mocked(api.listSkills).mockResolvedValue([{ ...installation, enabled: false, status: "uninstalled", active_version_id: undefined }]);
    if (recovery === "refresh") fireEvent.click(screen.getByRole("button", { name: "刷新状态" }));
    else {
      view.unmount();
      view = render(<SkillManagement workspaceName="测试工作区" userID="user-1" workspaceID={installation.workspace_id} />);
      await screen.findByRole("button", { name: "重试原操作" });
    }
    await waitFor(() => expect((screen.getByRole("button", { name: "重试原操作" }) as HTMLButtonElement).disabled).toBe(false));
    expect(save).toHaveBeenCalledTimes(1);
    expect((screen.getByRole("button", { name: "安装 Skill" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "重试原操作" }));
    await screen.findByText("原安装已完成。");
    expect(save).toHaveBeenCalledTimes(2);
    expect(save.mock.calls[1]).toEqual(save.mock.calls[0]);
    if (action === "install_zip" || action === "upgrade_zip") {
      const originalFile = action === "install_zip" ? install.mock.calls[0][0] : upgrade.mock.calls[0][1];
      const recoveredFile = action === "install_zip" ? install.mock.calls[1][0] : upgrade.mock.calls[1][1];
      expect(recoveredFile.name).toBe(originalFile.name);
      expect(recoveredFile.type).toBe(originalFile.type);
      expect(recoveredFile.lastModified).toBe(originalFile.lastModified);
      expect(new Uint8Array(await skillArchiveBytes(recoveredFile))).toEqual(new Uint8Array([0, 255, 128, 13, 10]));
      if (recovery === "remount") expect(recoveredFile).not.toBe(originalFile);
    }
    await waitFor(() => expect(screen.queryByRole("button", { name: "清理本地记录" })).toBeNull());
    view.unmount();
    render(<SkillManagement workspaceName="测试工作区" />);
    await waitFor(() => expect((screen.getByRole("button", { name: "安装 Skill" }) as HTMLButtonElement).disabled).toBe(false));
    expect(screen.queryByRole("button", { name: "重试原操作" })).toBeNull();
    expect(save).toHaveBeenCalledTimes(2);
  });

  it.each(["request_id", "action", "actor_ref", "workspace_id", "scope", "scope_ref", "skill_installation_id", "skill_installation_event_id", "skill_version_id", "capability_id", "version", "content_hash", "source_hash", "event_binding"])("does not acknowledge a ZIP receipt with incorrect %s", async (field) => {
    let first = true;
    const save = vi.spyOn(api, "upgradeSkill").mockImplementation(async (item, file, key) => {
      const result = await packageResult(item, { package: true, action: "upgrade_zip", installation: item, file, key });
      if (first) {
        first = false;
        if (field === "event_binding") result.installation.events.at(-1)!.payload.request_id = "wrong";
        else (result.receipt as unknown as Record<string, unknown>)[field] = "wrong";
      }
      return result;
    });
    const { container } = render(<SkillManagement workspaceName="测试工作区" userID="user-1" workspaceID={installation.workspace_id} />);
    fireEvent.click(await screen.findByRole("button", { name: "上传新版本" }));
    fireEvent.change(container.querySelectorAll('input[type="file"]')[1], { target: { files: [new File(["zip"], "升级.zip")] } });
    await screen.findByText("Skill 安装回执与原请求不一致，结果仍待确认。");
    expect(screen.queryByText("原安装已完成。")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "重试原操作" }));
    await screen.findByText("原安装已完成。");
    expect(save.mock.calls[1]).toEqual(save.mock.calls[0]);
  });

  it("only refreshes after a committed package whose directory reload failed", async () => {
    const save = vi.spyOn(api, "upgradeSkill").mockImplementation(async (item, file, key) => {
      vi.mocked(api.listSkills).mockRejectedValueOnce(new Error("offline after commit"));
      return packageResult(item, { package: true, action: "upgrade_zip", installation: item, file, key });
    });
    const { container } = render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "上传新版本" }));
    fireEvent.change(container.querySelectorAll('input[type="file"]')[1], { target: { files: [new File(["zip"], "v2.zip")] } });
    await screen.findByText("操作已提交，但最新目录尚未读取。请刷新状态，不要重复提交。");
    expect(screen.queryByRole("button", { name: "重试原操作" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "刷新状态" }));
    await waitFor(() => expect((screen.getByRole("button", { name: "上传新版本" }) as HTMLButtonElement).disabled).toBe(false));
    expect(save).toHaveBeenCalledTimes(1);
  });

  it("requires a fresh snapshot after a failed mutation and never retries it during refresh", async () => {
    const disable = vi.spyOn(api, "setSkillEnabled").mockRejectedValue(new ApiError("SKILL_DISCOVERY_CHANGED", "当前版本已变化。", 409));
    render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "禁用" }));
    await screen.findByText("当前版本已变化。");
    expect((await screen.findByRole("button", { name: "禁用" }) as HTMLButtonElement).disabled).toBe(true);
    vi.mocked(api.listSkills).mockRejectedValueOnce(new Error("still offline"));
    fireEvent.click(screen.getByRole("button", { name: "刷新状态" }));
    await screen.findByText("无法读取 Skill 管理状态。");
    expect((screen.getByRole("button", { name: "禁用" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "刷新状态" }));
    await waitFor(() => expect((screen.getByRole("button", { name: "禁用" }) as HTMLButtonElement).disabled).toBe(false));
    expect(disable).toHaveBeenCalledTimes(1);
  });

  it("does not send a command until its local record is committed", async () => {
    const claim = vi.spyOn(skillMutationJournal, "claim").mockRejectedValueOnce(new SkillJournalError("unavailable", "本地保存失败。"));
    const save = vi.spyOn(api, "setSkillEnabled").mockImplementation(async (item, _, key) => lifecycleResult(item, "disable", key));
    render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "禁用" }));
    await screen.findByText("本地保存失败。");
    expect(save).not.toHaveBeenCalled();
    expect((screen.getByRole("button", { name: "安装 Skill" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "重试原操作" }));
    await screen.findByText("原操作已完成。");
    expect(save).toHaveBeenCalledTimes(1);
    expect(claim.mock.calls[1][1].key).toBe(claim.mock.calls[0][1].key);
    await waitFor(() => expect(screen.queryByRole("button", { name: "清理本地记录" })).toBeNull());
  });

  it.each(["unavailable", "invalid"] as const)("keeps the catalog readable but locks writes when the journal is %s", async (code) => {
    const read = vi.spyOn(skillMutationJournal, "read").mockRejectedValue(new SkillJournalError(code, "本地记录不可读取。"));
    const claim = vi.spyOn(skillMutationJournal, "claim");
    const save = vi.spyOn(api, "setSkillEnabled");
    render(<SkillManagement workspaceName="测试工作区" />);
    await screen.findByRole("heading", { name: "动态故事评审" });
    await screen.findByText("本地记录不可读取。");
    for (const name of ["安装 Skill", "禁用", "上传新版本", "卸载"]) {
      const button = screen.getByRole("button", { name });
      expect((button as HTMLButtonElement).disabled).toBe(true);
      fireEvent.click(button);
    }
    expect(claim).not.toHaveBeenCalled();
    expect(save).not.toHaveBeenCalled();
    read.mockRestore();
    fireEvent.click(screen.getByRole("button", { name: "刷新状态" }));
    await waitFor(() => expect((screen.getByRole("button", { name: "禁用" }) as HTMLButtonElement).disabled).toBe(false));
    expect(save).not.toHaveBeenCalled();
  });

  it("adopts a competing page's original command without sending or overwriting a new one", async () => {
    const save = vi.spyOn(api, "setSkillEnabled");
    const owner = { workspaceID: installation.workspace_id, userID: "user-1" };
    const other = { action: "uninstall" as const, installation, key: "f0000000-0000-4000-8000-000000000002" };
    render(<SkillManagement workspaceName="测试工作区" />);
    await screen.findByRole("heading", { name: "动态故事评审" });
    await skillMutationJournal.claim(owner, other);
    fireEvent.click(screen.getByRole("button", { name: "禁用" }));
    await screen.findByText("已有待确认的原操作，未提交本次新操作。");
    expect(save).not.toHaveBeenCalled();
    expect((await skillMutationJournal.read(owner))?.key).toBe(other.key);
    expect(screen.getByRole("button", { name: "重试原操作" })).toBeTruthy();
    expect(screen.getByText(/卸载 · dynamic-story · 工作区 · workspace-default/)).toBeTruthy();
  });

  it("never recreates an original request that another page has settled", async () => {
    const save = vi.spyOn(api, "setSkillEnabled").mockRejectedValue(new Error("lost response"));
    const owner = { workspaceID: installation.workspace_id, userID: "user-1" };
    render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "禁用" }));
    await screen.findByText("无法读取 Skill 管理状态。");
    const original = (await skillMutationJournal.read(owner))!;
    await skillMutationJournal.settle(owner, original);
    fireEvent.click(screen.getByRole("button", { name: "重试原操作" }));
    await screen.findByText("本地原操作记录已变化，已停止重试。请刷新状态核对。");
    expect(save).toHaveBeenCalledTimes(1);
    expect(await skillMutationJournal.read(owner)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "刷新状态" }));
    await waitFor(() => expect((screen.getByRole("button", { name: "禁用" }) as HTMLButtonElement).disabled).toBe(false));
    expect(screen.queryByRole("button", { name: "重试原操作" })).toBeNull();
    expect(save).toHaveBeenCalledTimes(1);
  });

  it("restores the saved project target independently of the new page's scope selector", async () => {
    const owner = { workspaceID: installation.workspace_id, userID: "user-1" };
    const request = { package: true as const, action: "install_zip" as const, key: "f0000000-0000-4000-8000-000000000003", file: new File(["original"], "project.zip"), target: { scope: "project" as const, project_id: "original-project" } };
    await skillMutationJournal.claim(owner, request);
    const save = vi.spyOn(api, "installSkill").mockImplementation((file, target, key) => packageResult(installation, { package: true, action: "install_zip", file, target, key }));
    render(<SkillManagement workspaceName="测试工作区" />);
    const retry = await screen.findByRole("button", { name: "重试原操作" });
    expect(screen.getByText(/安装 ZIP · project.zip · 项目 · original-project/)).toBeTruthy();
    expect((screen.getByRole("combobox", { name: "安装范围" }) as HTMLSelectElement).value).toBe("workspace");
    expect(save).not.toHaveBeenCalled();
    fireEvent.click(retry);
    await screen.findByText("原安装已完成。");
    expect(save).toHaveBeenCalledWith(expect.any(File), request.target, request.key);
    await waitFor(() => expect(screen.queryByRole("button", { name: "清理本地记录" })).toBeNull());
  });

  it.each((["disable", "upgrade_zip"] as const).flatMap((action) => (["success", "rejected"] as const).map((outcome) => ({ action, outcome }))))("retries only local cleanup after $action is $outcome, including after role loss", async ({ action, outcome }) => {
    const settle = vi.spyOn(skillMutationJournal, "settle").mockRejectedValueOnce(new SkillJournalError("unavailable", "本地清理失败。"));
    const save = vi.spyOn(api, "setSkillEnabled").mockImplementation(async (item, _, key) => lifecycleResult(item, "disable", key));
    const upgrade = vi.spyOn(api, "upgradeSkill").mockImplementation((item, file, key) => packageResult(item, { package: true, action: "upgrade_zip", file, installation: item, key }));
    if (outcome === "rejected") (action === "disable" ? save : upgrade).mockRejectedValueOnce(new ApiError("SKILL_DISCOVERY_CHANGED", "原操作被拒绝。", 409));
    const view = render(<SkillManagement workspaceName="测试工作区" role="owner" />);
    fireEvent.click(await screen.findByRole("button", { name: action === "disable" ? "禁用" : "上传新版本" }));
    if (action === "upgrade_zip") fireEvent.change(view.container.querySelectorAll('input[type="file"]')[1], { target: { files: [new File(["zip"], "original.zip")] } });
    await screen.findByText("本地清理失败。");
    const remote = action === "disable" ? save : upgrade;
    expect(remote).toHaveBeenCalledTimes(1);
    view.rerender(<SkillManagement workspaceName="测试工作区" role="viewer" />);
    const button = screen.getByRole("button", { name: "清理本地记录" });
    await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(button);
    await waitFor(() => expect(screen.queryByRole("button", { name: "清理本地记录" })).toBeNull());
    expect(settle).toHaveBeenCalledTimes(2);
    expect(remote).toHaveBeenCalledTimes(1);
    expect(await skillMutationJournal.read({ workspaceID: installation.workspace_id, userID: "user-1" })).toBeNull();
    expect((screen.getByRole("button", { name: "安装 Skill" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("does not refresh or apply errors from an unmounted management page", async () => {
    let reject!: (error: Error) => void;
    vi.spyOn(api, "setSkillEnabled").mockImplementation(() => new Promise((_, fail) => { reject = fail; }));
    const { unmount } = render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "禁用" }));
    await waitFor(() => expect(api.setSkillEnabled).toHaveBeenCalledTimes(1));
    unmount();
    await act(async () => { reject(new Error("late")); });
    expect(api.listSkills).toHaveBeenCalledTimes(1);
  });

  it.each((["enable", "disable", "activate", "uninstall"] as const).flatMap((action) => (["refresh", "remount"] as const).map((recovery) => ({ action, recovery }))))("retries an unknown $action with its original snapshot after remote state changes and $recovery", async ({ action, recovery }) => {
    const original = { ...installation, enabled: action !== "enable" };
    vi.mocked(api.listSkills).mockResolvedValue([original]);
    vi.spyOn(window, "confirm").mockReturnValue(true);
    let first!: SkillLifecycleResult;
    let remote!: SkillInstallation;
    const requests: Array<{ item: SkillInstallation; key: string; version?: string }> = [];
    const save = async (item: SkillInstallation, key: string, version?: string) => {
      requests.push({ item, key, version });
      if (requests.length === 1) {
        first = lifecycleResult(item, action, key, version);
        remote = { ...first.installation, status: "installed", enabled: true, active_version_id: "version-2", events: [...first.installation.events, { ...first.installation.events.at(-1)!, skill_installation_event_id: "later-event", event_type: "skill.version.installed" }] };
        throw new ApiError("TEMPORARY", "连接中断。", 502);
      }
      return { ...first, installation: remote };
    };
    vi.spyOn(api, "setSkillEnabled").mockImplementation((item, _, key) => save(item, key));
    vi.spyOn(api, "activateSkillVersion").mockImplementation(saveVersion);
    function saveVersion(item: SkillInstallation, version: string, key: string) { return save(item, key, version); }
    vi.spyOn(api, "uninstallSkill").mockImplementation((item, key) => save(item, key));
    let view = render(<SkillManagement workspaceName="测试工作区" userID="user-1" />);
    fireEvent.click(await screen.findByRole("button", { name: { enable: "启用", disable: "禁用", activate: "设为当前", uninstall: "卸载" }[action] }));
    await screen.findByText("连接中断。");
    expect((screen.getByRole("combobox", { name: "安装范围" }) as HTMLSelectElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "上传新版本" }) as HTMLButtonElement).disabled).toBe(true);
    vi.mocked(api.listSkills).mockResolvedValue([remote]);
    if (recovery === "refresh") fireEvent.click(screen.getByRole("button", { name: "刷新状态" }));
    else {
      view.unmount();
      view = render(<SkillManagement workspaceName="测试工作区" userID="user-1" />);
      await screen.findByRole("button", { name: "重试原操作" });
    }
    await waitFor(() => expect((screen.getByRole("button", { name: "重试原操作" }) as HTMLButtonElement).disabled).toBe(false));
    expect(requests).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "重试原操作" }));
    await screen.findByText("原操作已完成，Skill 状态此后已有变化。");
    expect(requests).toHaveLength(2);
    expect(requests[1]).toEqual(requests[0]);
    expect(requests[1].item).toEqual(original);
    expect(screen.queryByRole("button", { name: "重试原操作" })).toBeNull();
    expect(window.confirm).toHaveBeenCalledTimes(action === "uninstall" ? 1 : 0);
    await waitFor(() => expect(screen.queryByRole("button", { name: "清理本地记录" })).toBeNull());
    view.unmount();
    render(<SkillManagement workspaceName="测试工作区" />);
    await waitFor(() => expect((screen.getByRole("button", { name: "安装 Skill" }) as HTMLButtonElement).disabled).toBe(false));
    expect(screen.queryByRole("button", { name: "重试原操作" })).toBeNull();
    expect(requests).toHaveLength(2);
  });

  it.each(["request", "action", "actor", "event", "version", "workspace", "scope", "scope_ref", "capability", "event_binding"])("retains the original request when a lifecycle receipt has wrong %s", async (field) => {
    const save = vi.spyOn(api, "setSkillEnabled").mockImplementationOnce(async (item, _, key) => {
      const result = structuredClone(lifecycleResult(item, "disable", key));
      if (field === "request") result.receipt.request_id = "wrong";
      if (field === "action") result.receipt.action = "enable";
      if (field === "actor") result.receipt.actor_ref = "someone-else";
      if (field === "event") result.receipt.skill_installation_event_id = "wrong";
      if (field === "version") result.receipt.skill_version_id = "version-1";
      if (field === "workspace") result.installation.workspace_id = "other-workspace";
      if (field === "scope") result.installation.scope = "user";
      if (field === "scope_ref") result.installation.scope_ref = "other-user";
      if (field === "capability") result.installation.capability_id = "other-skill";
      if (field === "event_binding") result.installation.events.at(-1)!.payload.request_id = "other-request";
      return result;
    }).mockImplementation(async (item, _, key) => lifecycleResult(item, "disable", key));
    render(<SkillManagement workspaceName="测试工作区" userID="user-1" />);
    fireEvent.click(await screen.findByRole("button", { name: "禁用" }));
    await screen.findByText("Skill 操作回执与原请求不一致，结果仍待确认。");
    expect(screen.queryByText("原操作已完成。")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "重试原操作" }));
    await screen.findByText("原操作已完成。");
    expect(save).toHaveBeenCalledTimes(2);
    expect(save.mock.calls[1]).toEqual(save.mock.calls[0]);
  });

  it("keeps an unknown request while the user's role is revoked and restores only its original retry", async () => {
    const save = vi.spyOn(api, "setSkillEnabled").mockRejectedValueOnce(new ApiError("COMMAND_IN_PROGRESS", "结果尚未确定。", 409)).mockImplementation(async (item, _, key) => lifecycleResult(item, "disable", key));
    const { rerender } = render(<SkillManagement workspaceName="测试工作区" role="owner" userID="user-1" />);
    fireEvent.click(await screen.findByRole("button", { name: "禁用" }));
    await screen.findByText("结果尚未确定。");
    rerender(<SkillManagement workspaceName="测试工作区" role="viewer" userID="user-1" />);
    expect((screen.getByRole("button", { name: "重试原操作" }) as HTMLButtonElement).disabled).toBe(true);
    expect(save).toHaveBeenCalledTimes(1);
    rerender(<SkillManagement workspaceName="测试工作区" role="owner" userID="user-1" />);
    await waitFor(() => expect((screen.getByRole("button", { name: "重试原操作" }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: "重试原操作" }));
    await screen.findByText("原操作已完成。");
    expect(save.mock.calls[1]).toEqual(save.mock.calls[0]);
  });

  it("does not offer unsafe lifecycle writes against an older backend", async () => {
    vi.mocked(api.getSkillManagementOptions).mockResolvedValue({ install_scopes: ["workspace"], system_read_only: true });
    render(<SkillManagement workspaceName="测试工作区" />);
    await screen.findByRole("heading", { name: "动态故事评审" });
    for (const name of ["禁用", "卸载", "设为当前"]) expect((screen.getByRole("button", { name }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("warns before leaving an unknown request and clears the warning after original-key recovery", async () => {
    const save = vi.spyOn(api, "setSkillEnabled").mockRejectedValueOnce(new Error("network failed")).mockImplementation(async (item, _, key) => lifecycleResult(item, "disable", key));
    render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "禁用" }));
    await screen.findByText("无法读取 Skill 管理状态。");
    const pendingLeave = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(pendingLeave);
    expect(pendingLeave.defaultPrevented).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "重试原操作" }));
    await screen.findByText("原操作已完成。");
    await waitFor(() => {
      const settledLeave = new Event("beforeunload", { cancelable: true });
      window.dispatchEvent(settledLeave);
      expect(settledLeave.defaultPrevented).toBe(false);
    });
    expect(screen.queryByRole("button", { name: "清理本地记录" })).toBeNull();
    expect(save.mock.calls[1]).toEqual(save.mock.calls[0]);
  });

  it("does not treat permission loss during receipt recovery as proof the original operation failed", async () => {
    const save = vi.spyOn(api, "setSkillEnabled").mockRejectedValueOnce(new Error("response lost")).mockRejectedValueOnce(new ApiError("ROLE_FORBIDDEN", "当前无权读取原操作。", 403)).mockImplementation(async (item, _, key) => lifecycleResult(item, "disable", key));
    render(<SkillManagement workspaceName="测试工作区" />);
    fireEvent.click(await screen.findByRole("button", { name: "禁用" }));
    await screen.findByText("无法读取 Skill 管理状态。");
    fireEvent.click(screen.getByRole("button", { name: "重试原操作" }));
    await screen.findByText("当前无权读取原操作。");
    expect((screen.getByRole("button", { name: "禁用" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "重试原操作" }));
    await screen.findByText("原操作已完成。");
    expect(save).toHaveBeenCalledTimes(3);
    expect(save.mock.calls[2]).toEqual(save.mock.calls[0]);
  });
});

const manifest = {
  name: "dynamic-story",
  scope: "workspace",
  path: "installed/dynamic-story",
  content_hash: "sha256:version-two",
  allow_implicit_invocation: true,
  interface: {
    display_name: "动态故事评审",
    short_description: "评审故事结构",
    default_prompt: dynamicStoryEntry.default_prompt,
  },
  dependencies: [{
    type: "tool",
    value: "story_search",
    description: "故事检索工具",
    status: "available",
  }],
  scripts: [{
    id: "render",
    path: "scripts/render.py",
    runtime: "python",
    description: "Render one result.",
  }],
};

const scriptPolicy: ScriptSandboxPolicy = {
  workspace_id: "workspace-default",
  enabled: false,
  version: 0,
  limits: {
    timeout_seconds: 30,
    cpu_count: 0.5,
    memory_bytes: 268435456,
    process_count: 32,
    disk_bytes: 16777216,
    temp_bytes: 16777216,
  },
  environment_allowlist: ["CONTENT_AGENT_INPUT", "CONTENT_AGENT_OUTPUT", "LANG", "PATH", "PYTHONHASHSEED"],
  sandbox: {
    available: true,
    adapter: "linux",
    engine: "docker",
    runtimes: ["python"],
  },
};

const installation: SkillInstallation = {
  skill_installation_id: "skill-1",
  workspace_id: "workspace-default",
  scope: "workspace",
  scope_ref: "workspace-default",
  skill_name: "dynamic-story",
  capability_id: "dynamic_story_skill",
  status: "installed",
  enabled: true,
  active_version_id: "version-2",
  created_by: "user-1",
  created_at: "2026-09-03T00:00:00Z",
  updated_at: "2026-09-04T00:00:00Z",
  versions: [
    {
      skill_version_id: "version-1", skill_installation_id: "skill-1", version: "1.0.0",
      content_hash: "sha256:version-one", execution_mode: "stateful_workflow", source_type: "upload",
      source_name: "dynamic-story.zip", manifest, status: "installed", installed_by: "user-1", created_at: "2026-09-03T00:00:00Z",
    },
    {
      skill_version_id: "version-2", skill_installation_id: "skill-1", version: "1.1.0",
      content_hash: "sha256:version-two", execution_mode: "stateful_workflow", source_type: "upload",
      source_name: "dynamic-story.zip", manifest, status: "installed", installed_by: "user-1", created_at: "2026-09-04T00:00:00Z",
    },
  ],
  events: [{ skill_installation_event_id: "initial-event", skill_installation_id: "skill-1", skill_version_id: "version-2", event_type: "skill.version.installed", actor_ref: "user-1", payload: {}, created_at: "2026-09-04T00:00:00Z" }],
  registry_status: "available",
};

const projection: WorkspaceProjection = {
  contract_version: "1.0.0",
  revision: "sha256:test",
  registries: {
    artifacts: [], navigation: [], tasks: [], approvals: [], interactions: [], composer: [dynamicStoryEntry],
  },
};
