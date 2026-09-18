// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api, ApiError } from "../../api";
import type { AgentToolConfiguration, AgentToolDescriptor } from "../../types";
import { AgentToolInventory } from "./AgentToolInventory";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("distinguishes absent MCP configuration from configured runtime tools", async () => {
  vi.spyOn(api, "getAgentToolConfiguration").mockRejectedValue(new ApiError("NOT_FOUND", "Old backend", 404));
  vi.spyOn(api, "listAgentTools").mockResolvedValue([{ id: "runtime:read", kind: "runtime_function", name: "read", description: "Read project", access: "read", approval: "never", enabled: true }]);
  render(<AgentToolInventory />);
  expect(await screen.findByText("1 / 1 已启用")).toBeTruthy();
  expect(screen.getAllByText("未配置")).toHaveLength(2);
  expect(screen.queryByRole("switch")).toBeNull();
});

const descriptor: AgentToolDescriptor = { id: "hosted:native-web-search", kind: "hosted", name: "web_search", description: "Web search", access: "read", approval: "never", enabled: false };
const settings: AgentToolConfiguration = { workspace_id: "workspace-a", version: 3, options: [
  { id: descriptor.id, kind: "hosted", name: "web_search", description: "Web search", enabled: false, configurable: true },
  { id: "hosted:native-file-search", kind: "hosted", name: "file_search", description: "File search", enabled: false, configurable: false, user_message: "缺少获准的向量库。" },
] };

it("confirms enablement and saves the workspace version before refreshing dependencies", async () => {
  vi.spyOn(api, "listAgentTools").mockResolvedValue([descriptor]);
  const next = { ...settings, version: 4, options: settings.options.map((option) => ({ ...option, enabled: option.id === descriptor.id })) };
  vi.spyOn(api, "getAgentToolConfiguration").mockResolvedValueOnce(settings).mockResolvedValue(next);
  const save = vi.spyOn(api, "updateAgentToolConfiguration").mockResolvedValue(next);
  const confirmation = vi.spyOn(window, "confirm").mockReturnValue(true);
  const refresh = vi.fn().mockResolvedValue(undefined);
  render(<AgentToolInventory canManage onConfigurationChange={refresh} />);
  fireEvent.click(await screen.findByText("Hosted tools"));
  fireEvent.click(screen.getByRole("switch", { name: "网页搜索启用状态" }));
  await waitFor(() => expect(save).toHaveBeenCalledWith(3, { [descriptor.id]: true }));
  await waitFor(() => expect(refresh).toHaveBeenCalledOnce());
  expect(confirmation).toHaveBeenCalledOnce();
  expect((screen.getByRole("switch", { name: "网页搜索启用状态" }) as HTMLInputElement).checked).toBe(true);
  expect((screen.getByRole("switch", { name: "文件搜索启用状态" }) as HTMLInputElement).disabled).toBe(true);
});

it("keeps non-admin controls read-only and does not enable on a declined confirmation", async () => {
  vi.spyOn(api, "listAgentTools").mockResolvedValue([descriptor]);
  vi.spyOn(api, "getAgentToolConfiguration").mockResolvedValue(settings);
  const save = vi.spyOn(api, "updateAgentToolConfiguration");
  vi.spyOn(window, "confirm").mockReturnValue(false);
  const view = render(<AgentToolInventory />);
  fireEvent.click(await screen.findByText("Hosted tools"));
  expect((screen.getByRole("switch", { name: "网页搜索启用状态" }) as HTMLInputElement).disabled).toBe(true);
  view.rerender(<AgentToolInventory canManage />);
  fireEvent.click(screen.getByRole("switch", { name: "网页搜索启用状态" }));
  expect(save).not.toHaveBeenCalled();
});

it("refreshes a conflicting configuration without pretending the write succeeded", async () => {
  vi.spyOn(api, "listAgentTools").mockResolvedValue([descriptor]);
  const read = vi.spyOn(api, "getAgentToolConfiguration").mockResolvedValueOnce(settings).mockResolvedValue({ ...settings, version: 5 });
  vi.spyOn(api, "updateAgentToolConfiguration").mockRejectedValue(new ApiError("AGENT_TOOL_CONFIGURATION_CONFLICT", "配置已变化，请重试。", 409));
  vi.spyOn(window, "confirm").mockReturnValue(true);
  const refresh = vi.fn();
  render(<AgentToolInventory canManage onConfigurationChange={refresh} />);
  fireEvent.click(await screen.findByText("Hosted tools"));
  fireEvent.click(screen.getByRole("switch", { name: "网页搜索启用状态" }));
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", "配置已变化，请重试。");
  await waitFor(() => expect(read).toHaveBeenCalledTimes(2));
  expect(refresh).not.toHaveBeenCalled();
  expect((screen.getByRole("switch", { name: "网页搜索启用状态" }) as HTMLInputElement).checked).toBe(false);
});

it("distinguishes separately configured hosted tools of the same type", async () => {
  vi.spyOn(api, "listAgentTools").mockResolvedValue([descriptor, { ...descriptor, id: "hosted:team-search" }]);
  vi.spyOn(api, "getAgentToolConfiguration").mockResolvedValue({ ...settings, options: [settings.options[0], { ...settings.options[0], id: "hosted:team-search" }] });
  render(<AgentToolInventory canManage />);
  fireEvent.click(await screen.findByText("Hosted tools"));
  expect(screen.getByRole("switch", { name: "网页搜索 (hosted:native-web-search)启用状态" })).toBeTruthy();
  expect(screen.getByRole("switch", { name: "网页搜索 (hosted:team-search)启用状态" })).toBeTruthy();
  expect(screen.queryByText("web_search")).toBeNull();
});

it("renders and updates the programmatic option without an executable descriptor", async () => {
  const option = { id: "programmatic:runtime", kind: "hosted" as const, name: "programmatic_tool_calling", description: "PTC", enabled: false, configurable: true };
  const initial = { ...settings, options: [option] };
  const next = { ...initial, version: 4, options: [{ ...option, enabled: true }] };
  vi.spyOn(api, "listAgentTools").mockResolvedValue([]);
  vi.spyOn(api, "getAgentToolConfiguration").mockResolvedValueOnce(initial).mockResolvedValue(next);
  const save = vi.spyOn(api, "updateAgentToolConfiguration").mockResolvedValue(next);
  vi.spyOn(window, "confirm").mockReturnValue(true);
  render(<AgentToolInventory canManage />);
  fireEvent.click(await screen.findByText("Hosted tools"));
  expect(screen.getByText("0 / 1 已启用")).toBeTruthy();
  fireEvent.click(screen.getByRole("switch", { name: "程序化工具调用启用状态" }));
  await waitFor(() => expect(save).toHaveBeenCalledWith(3, { "programmatic:runtime": true }));
  expect(await screen.findByText("1 / 1 已启用")).toBeTruthy();
});

it("keeps programmatic execution unavailable without operator grants", async () => {
  vi.spyOn(api, "listAgentTools").mockResolvedValue([]);
  vi.spyOn(api, "getAgentToolConfiguration").mockResolvedValue({ ...settings, options: [{
    id: "programmatic:runtime", kind: "hosted", name: "programmatic_tool_calling", description: "PTC",
    enabled: false, configurable: false, user_message: "当前工作区未配置获准的程序调用工具。",
  }] });
  const save = vi.spyOn(api, "updateAgentToolConfiguration");
  render(<AgentToolInventory canManage />);
  fireEvent.click(await screen.findByText("Hosted tools"));
  expect((screen.getByRole("switch", { name: "程序化工具调用启用状态" }) as HTMLInputElement).disabled).toBe(true);
  expect(screen.getByText("当前工作区未配置获准的程序调用工具。")).toBeTruthy();
  expect(save).not.toHaveBeenCalled();
});
