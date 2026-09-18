// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, ApiError } from "../../api";
import type { AgentInstructionView, Project } from "../../types";
import { AgentInstructions } from "./AgentInstructions";

const view: AgentInstructionView = { workspace_id: "w", documents: [
  { scope: "workspace", scope_ref: "w", version: 2, content: "Shared rule", content_hash: "hash", enabled: true, can_edit: false },
  { scope: "user", scope_ref: "u", version: 0, content: "", content_hash: "empty", enabled: false, can_edit: true },
], applies_to: "new_executions" };
beforeEach(() => { vi.spyOn(api, "getAgentInstructions").mockResolvedValue(structuredClone(view)); vi.spyOn(api, "listProjects").mockResolvedValue([{ project_id: "p", title: "作品 P" } as Project]); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("saves scoped CAS instructions and renders the durable receipt", async () => {
  const save = vi.spyOn(api, "updateAgentInstructions").mockResolvedValue({ ...view.documents[1], version: 1, content: "只写中文", enabled: true });
  render(<AgentInstructions workspaceName="Workspace" />);
  fireEvent.change(await screen.findByLabelText("规则内容"), { target: { value: "只写中文" } });
  fireEvent.click(screen.getByLabelText("启用"));
  fireEvent.click(screen.getByRole("button", { name: "保存规则" }));
  await waitFor(() => expect(save).toHaveBeenCalledWith({ scope: "user", expected_version: 0, request_id: expect.any(String), content: "只写中文", enabled: true }));
  expect(await screen.findByText("v1 · 已启用")).toBeTruthy();
  expect(screen.getByRole("button", { name: "保存规则" })).toHaveProperty("disabled", true);
});

it("keeps the user draft on conflict without allowing an overwrite", async () => {
  vi.spyOn(api, "updateAgentInstructions").mockRejectedValue(new ApiError("AGENT_INSTRUCTIONS_CONFLICT", "版本已变化", 409));
  render(<AgentInstructions workspaceName="Workspace" />);
  fireEvent.change(await screen.findByLabelText("规则内容"), { target: { value: "Unsaved draft" } });
  fireEvent.click(screen.getByRole("button", { name: "保存规则" }));
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", expect.stringContaining("版本已变化"));
  expect(screen.getByLabelText("规则内容")).toHaveProperty("value", "Unsaved draft");
  expect(screen.getByRole("button", { name: "保存规则" })).toHaveProperty("disabled", true);
});

it("checks UTF8 byte limits and empty enabled rules", async () => {
  render(<AgentInstructions workspaceName="Workspace" />);
  fireEvent.click(await screen.findByLabelText("启用"));
  expect(screen.getByRole("button", { name: "保存规则" })).toHaveProperty("disabled", true);
  fireEvent.change(screen.getByLabelText("规则内容"), { target: { value: "中".repeat(2731) } });
  expect(screen.getByText("8193 / 8192 字节")).toBeTruthy();
  expect(screen.getByRole("button", { name: "保存规则" })).toHaveProperty("disabled", true);
});

it("confirms dirty navigation and enforces workspace read-only", async () => {
  const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValue(true);
  render(<AgentInstructions workspaceName="Workspace" />);
  fireEvent.change(await screen.findByLabelText("规则内容"), { target: { value: "draft" } });
  fireEvent.click(screen.getByRole("tab", { name: "工作区" }));
  expect(screen.getByLabelText("规则内容")).toHaveProperty("value", "draft");
  fireEvent.click(screen.getByRole("tab", { name: "工作区" }));
  expect(screen.getByLabelText("规则内容")).toHaveProperty("value", "Shared rule");
  expect(screen.getByLabelText("规则内容")).toHaveProperty("readOnly", true);
  expect(screen.queryByRole("button", { name: "保存规则" })).toBeNull();
  expect(confirm).toHaveBeenCalledTimes(2);
});

it("loads the project scope and confirms clearing for future executions", async () => {
  const projectDoc = { ...view.documents[0], scope: "project" as const, scope_ref: "p", can_edit: true };
  vi.mocked(api.getAgentInstructions).mockResolvedValue({ ...view, project_id: "p", documents: [...view.documents, projectDoc] });
  const save = vi.spyOn(api, "updateAgentInstructions").mockResolvedValue({ ...projectDoc, version: 3, content: "", enabled: false });
  const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValue(true);
  render(<AgentInstructions workspaceName="Workspace" initialProjectID="p" />);
  fireEvent.click(await screen.findByRole("button", { name: "清空并停用" }));
  expect(save).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "清空并停用" }));
  await waitFor(() => expect(save).toHaveBeenCalledWith(expect.objectContaining({ scope: "project", project_id: "p", expected_version: 2, content: "", enabled: false })));
  expect(await screen.findByText("v3 · 已停用")).toBeTruthy();
  expect(confirm.mock.calls[1][0]).toContain("历史执行");
});

it("does not expose stale project instructions when loading fails", async () => {
  vi.mocked(api.getAgentInstructions).mockRejectedValue(new Error("无权访问此作品"));
  render(<AgentInstructions workspaceName="Workspace" initialProjectID="foreign" />);
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", "无权访问此作品");
  expect(screen.queryByLabelText("规则内容")).toBeNull();
});

it.each([false, true])("reuses the exact request after a committed save loses its response (clear=%s)", async (clear) => {
  const document = { ...view.documents[1], version: 4, content: "Existing rule", enabled: true };
  vi.mocked(api.getAgentInstructions).mockResolvedValue({ ...view, documents: [document] });
  vi.spyOn(window, "confirm").mockReturnValue(true);
  let committed: Parameters<typeof api.updateAgentInstructions>[0] | undefined;
  const save = vi.spyOn(api, "updateAgentInstructions").mockImplementation(async (command) => {
    if (!committed) { committed = command; throw new ApiError("UNAVAILABLE", "Response lost after save", 503); }
    if (command.request_id !== committed.request_id) throw new ApiError("AGENT_INSTRUCTIONS_CONFLICT", "Version advanced", 409);
    return { ...document, version: 5, content: command.content, enabled: command.enabled };
  });
  render(<AgentInstructions workspaceName="Workspace" />);
  await screen.findByLabelText("规则内容");
  if (!clear) fireEvent.change(screen.getByLabelText("规则内容"), { target: { value: "Updated rule" } });
  const name = clear ? "清空并停用" : "保存规则";
  fireEvent.click(screen.getByRole("button", { name }));
  await screen.findByText("Response lost after save");
  fireEvent.click(screen.getByRole("button", { name }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
  expect(save.mock.calls[1][0]).toEqual(save.mock.calls[0][0]);
  expect(await screen.findByText(`v5 · ${clear ? "已停用" : "已启用"}`)).toBeTruthy();
});

it("uses a new request identity when the user changes a failed save", async () => {
  const save = vi.spyOn(api, "updateAgentInstructions").mockRejectedValue(new ApiError("UNAVAILABLE", "Retry later", 503));
  render(<AgentInstructions workspaceName="Workspace" />);
  fireEvent.change(await screen.findByLabelText("规则内容"), { target: { value: "First draft" } });
  fireEvent.click(screen.getByRole("button", { name: "保存规则" }));
  await screen.findByText("Retry later");
  fireEvent.change(screen.getByLabelText("规则内容"), { target: { value: "Changed draft" } });
  fireEvent.click(screen.getByRole("button", { name: "保存规则" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
  expect(save.mock.calls[1][0].request_id).not.toBe(save.mock.calls[0][0].request_id);
  expect(save.mock.calls[1][0].content).toBe("Changed draft");
});
