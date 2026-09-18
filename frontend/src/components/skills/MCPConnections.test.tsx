// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api, ApiError } from "../../api";
import type { MCPConnectionInventory } from "../../types";
import { MCPConnections } from "./MCPConnections";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const inventory: MCPConnectionInventory = { storage_available: true, can_manage_personal: true, can_manage_workspace: false, items: [{
  server_id: "story-service", display_name: "story-service", fields: ["api-token"], enabled: true, effective_scope: "workspace",
  personal: { scope: "user", version: 0, status: "missing" }, workspace: { scope: "workspace", version: 2, status: "active" },
}] };

it("saves masked personal values with scoped CAS and clears them immediately", async () => {
  const next = structuredClone(inventory);
  next.items[0].personal = { scope: "user", version: 1, status: "active" }; next.items[0].effective_scope = "user";
  vi.spyOn(api, "listMCPConnections").mockResolvedValueOnce(inventory).mockResolvedValue(next);
  const save = vi.spyOn(api, "updateMCPConnection").mockResolvedValue(next.items[0].personal);
  const changed = vi.fn().mockResolvedValue(undefined);
  render(<MCPConnections onChange={changed} />);
  fireEvent.click(await screen.findByRole("button", { name: "配置凭据" }));
  const field = screen.getByLabelText("api-token") as HTMLInputElement;
  expect(field.type).toBe("password"); expect(field.value).toBe("");
  fireEvent.change(field, { target: { value: "Bearer isolated-secret" } });
  fireEvent.click(screen.getByRole("button", { name: "保存凭据" }));
  expect(screen.queryByLabelText("api-token")).toBeNull();
  await waitFor(() => expect(save).toHaveBeenCalledWith(expect.objectContaining({ server_id: "story-service", scope: "user", expected_version: 0, request_id: expect.any(String), values: { "api-token": "Bearer isolated-secret" } })));
  await waitFor(() => expect(changed).toHaveBeenCalledOnce());
  expect(screen.getByText("使用个人凭据")).toBeTruthy();
  expect(document.body.textContent).not.toContain("isolated-secret");
  fireEvent.click(screen.getByRole("button", { name: "更新凭据" }));
  expect(screen.getByLabelText("api-token")).toHaveProperty("value", "");
});

it("clears unsaved values when switching scope and keeps workspace read-only for an editor", async () => {
  vi.spyOn(api, "listMCPConnections").mockResolvedValue(inventory);
  const save = vi.spyOn(api, "updateMCPConnection");
  render(<MCPConnections />);
  fireEvent.click(await screen.findByRole("button", { name: "配置凭据" }));
  fireEvent.change(screen.getByLabelText("api-token"), { target: { value: "unsaved-value" } });
  fireEvent.click(screen.getByRole("radio", { name: "工作区" }));
  expect(screen.queryByLabelText("api-token")).toBeNull();
  expect(screen.queryByRole("button", { name: "更新凭据" })).toBeNull();
  expect(screen.queryByRole("button", { name: "撤销凭据" })).toBeNull();
  fireEvent.click(screen.getByRole("radio", { name: "个人" }));
  fireEvent.click(screen.getByRole("button", { name: "配置凭据" }));
  expect(screen.getByLabelText("api-token")).toHaveProperty("value", "");
  expect(save).not.toHaveBeenCalled();
});

it("confirms revocation, sends no values, and retains deletion without an encryption key", async () => {
  vi.spyOn(api, "listMCPConnections").mockResolvedValue({ ...inventory, storage_available: false, can_manage_workspace: true });
  const save = vi.spyOn(api, "updateMCPConnection").mockResolvedValue({ scope: "workspace", version: 3, status: "deleted" });
  const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValue(true);
  render(<MCPConnections />);
  await screen.findByText("story-service");
  fireEvent.click(screen.getByRole("radio", { name: "工作区" }));
  expect(screen.getByRole("button", { name: "更新凭据" })).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "撤销凭据" })); expect(save).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "撤销凭据" }));
  await waitFor(() => expect(save).toHaveBeenCalledWith(expect.objectContaining({ scope: "workspace", expected_version: 2, delete: true })));
  expect(save.mock.calls[0][0]).not.toHaveProperty("values"); expect(confirm).toHaveBeenCalledTimes(2);
});

it("refreshes conflict state without claiming success or retaining the typed secret", async () => {
  const load = vi.spyOn(api, "listMCPConnections").mockResolvedValue(inventory);
  vi.spyOn(api, "updateMCPConnection").mockRejectedValue(new ApiError("MCP_CREDENTIAL_CONFLICT", "凭据已变化，请刷新。", 409));
  const changed = vi.fn();
  render(<MCPConnections onChange={changed} />);
  fireEvent.click(await screen.findByRole("button", { name: "配置凭据" }));
  fireEvent.change(screen.getByLabelText("api-token"), { target: { value: "private" } });
  fireEvent.click(screen.getByRole("button", { name: "保存凭据" }));
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", "凭据已变化，请刷新。");
  await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
  expect(changed).not.toHaveBeenCalled(); expect(screen.queryByLabelText("api-token")).toBeNull();
});

it("does not present mutation controls to viewers", async () => {
  vi.spyOn(api, "listMCPConnections").mockResolvedValue({ ...inventory, can_manage_personal: false });
  render(<MCPConnections />);
  await screen.findByText("story-service");
  expect(screen.queryByRole("button", { name: "配置凭据" })).toBeNull();
});

it("keeps the selected workspace scope after a successful version change", async () => {
  const initial = { ...inventory, can_manage_workspace: true };
  const next = structuredClone(initial); next.items[0].workspace.version = 3;
  vi.spyOn(api, "listMCPConnections").mockResolvedValueOnce(initial).mockResolvedValue(next);
  vi.spyOn(api, "updateMCPConnection").mockResolvedValue(next.items[0].workspace);
  render(<MCPConnections />);
  await screen.findByText("story-service");
  fireEvent.click(screen.getByRole("radio", { name: "工作区" }));
  fireEvent.click(screen.getByRole("button", { name: "更新凭据" }));
  fireEvent.change(screen.getByLabelText("api-token"), { target: { value: "updated" } });
  fireEvent.click(screen.getByRole("button", { name: "保存凭据" }));
  await screen.findByText("已保存 · v3");
  expect(screen.getByRole("radio", { name: "工作区" })).toHaveProperty("checked", true);
  expect(screen.queryByLabelText("api-token")).toBeNull();
});

it.each(["version", "fields", "permission", "storage"])("clears only stale credential drafts after a %s refresh", async (change) => {
  const next = structuredClone(inventory);
  if (change === "version") next.items[0].personal.version = 1;
  if (change === "fields") next.items[0].fields = ["replacement-token"];
  if (change === "permission") next.can_manage_personal = false;
  if (change === "storage") next.storage_available = false;
  const load = vi.spyOn(api, "listMCPConnections").mockResolvedValueOnce(inventory).mockResolvedValue(next);
  render(<MCPConnections />);
  fireEvent.click(await screen.findByRole("button", { name: "配置凭据" }));
  fireEvent.change(screen.getByLabelText("api-token"), { target: { value: "unsubmitted-secret" } });
  fireEvent.click(screen.getByRole("button", { name: "刷新连接凭据" }));
  await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
  await waitFor(() => expect(screen.queryByLabelText("api-token")).toBeNull());
  expect(document.querySelector("input[type=password]")).toBeNull();
  if (change === "permission" || change === "storage") return;
  const edit = await screen.findByRole("button", { name: "配置凭据" });
  await waitFor(() => expect(edit).toHaveProperty("disabled", false));
  fireEvent.click(edit);
  expect(screen.getByLabelText(change === "fields" ? "replacement-token" : "api-token")).toHaveProperty("value", "");
});
