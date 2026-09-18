// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { api, ApiError } from "../../api";
import type { AgentMemoryDocument, Principal } from "../../types";
import { ProjectMemory } from "./ProjectMemory";

const principal: Principal = { kind: "user", user_id: "u", workspace_id: "w", workspace_name: "W", display_name: "User", role: "editor", auth_method: "fixture" };
const initial: AgentMemoryDocument = { project_id: "p", user_id: "u", version: 1, files: { "memory_summary.md": "Original" }, content_hash: "hash", enabled: true, forgotten: false };
const methods = ["showModal", "close"] as const;
const descriptors = methods.map((name) => Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, name));
beforeAll(() => {
  methods.forEach((name) => Object.defineProperty(HTMLDialogElement.prototype, name, { configurable: true, value(this: HTMLDialogElement) { this.open = name === "showModal"; } }));
});
afterAll(() => methods.forEach((name, index) => {
  const descriptor = descriptors[index];
  if (descriptor) Object.defineProperty(HTMLDialogElement.prototype, name, descriptor);
  else Reflect.deleteProperty(HTMLDialogElement.prototype, name);
}));
beforeEach(() => {
  vi.spyOn(window, "confirm").mockReturnValue(true);
  vi.spyOn(api, "getAgentMemory").mockResolvedValue(initial);
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
async function open(role: Principal["role"] = "editor") {
  render(<ProjectMemory projectID="p" principal={{ ...principal, role }} />);
  fireEvent.click(screen.getByRole("button", { name: "项目记忆" }));
  await screen.findByRole("textbox", { name: "记忆正文" });
}

it("locks an unknown save and retries the exact original command", async () => {
  const update = vi.spyOn(api, "updateAgentMemory").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Response lost", 503)).mockImplementation(async (command) => {
    const result = { ...initial, files: command.files, version: 2, request_id: command.request_id };
    vi.mocked(api.getAgentMemory).mockResolvedValue(result);
    return result;
  });
  await open();
  fireEvent.change(screen.getByRole("textbox", { name: "记忆正文" }), { target: { value: "Revised" } });
  fireEvent.click(screen.getByRole("button", { name: "保存记忆" }));
  await screen.findByText("Response lost");
  expect(screen.getByRole("textbox", { name: "记忆正文" })).toHaveProperty("readOnly", true);
  expect(screen.getByRole("button", { name: "遗忘全部" })).toHaveProperty("disabled", true);
  expect(screen.getByRole("button", { name: "关闭记忆" })).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "重试原请求" }));
  await waitFor(() => expect(update).toHaveBeenCalledTimes(2));
  expect(update.mock.calls[1][0]).toEqual(update.mock.calls[0][0]);
  await waitFor(() => expect(screen.getByRole("textbox", { name: "记忆正文" })).toHaveProperty("readOnly", false));
});

it.each(["user_id", "project_id", "request_id", "version", "files"])("rejects a mismatched %s receipt", async (field) => {
  vi.spyOn(api, "updateAgentMemory").mockImplementation(async (command) => ({ ...initial, version: 2, files: command.files, request_id: command.request_id, [field]: field === "files" ? { "memory_summary.md": "Wrong body" } : field === "version" ? 99 : "foreign" }));
  await open();
  fireEvent.change(screen.getByRole("textbox", { name: "记忆正文" }), { target: { value: "Revised" } });
  fireEvent.click(screen.getByRole("button", { name: "保存记忆" }));
  await screen.findByRole("alert");
  expect(screen.getByRole("textbox", { name: "记忆正文" })).toHaveProperty("value", "Revised");
  expect(screen.getByRole("button", { name: "重试原请求" })).toBeTruthy();
});

it("requires confirmation and clears all memory files on forgetting", async () => {
  const update = vi.spyOn(api, "updateAgentMemory").mockImplementation(async (command) => {
    const result = { ...initial, files: {}, enabled: false, forgotten: true, version: 2, request_id: command.request_id };
    vi.mocked(api.getAgentMemory).mockResolvedValue(result);
    return result;
  });
  await open();
  vi.mocked(window.confirm).mockReturnValueOnce(false);
  fireEvent.click(screen.getByRole("button", { name: "遗忘全部" }));
  expect(update).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "遗忘全部" }));
  await waitFor(() => expect(update).toHaveBeenCalledOnce());
  expect(update.mock.calls[0][0]).toMatchObject({ expected_version: 1, files: {}, enabled: false, forget: true });
  await waitFor(() => expect(screen.queryByRole("textbox", { name: "记忆正文" })).toBeNull());
});

it("does not let a viewer submit changes", async () => {
  const update = vi.spyOn(api, "updateAgentMemory");
  await open("viewer");
  expect(screen.getByRole("textbox", { name: "记忆正文" })).toHaveProperty("readOnly", true);
  fireEvent.click(screen.getByRole("button", { name: "遗忘全部" }));
  fireEvent.click(screen.getByRole("button", { name: "保存记忆" }));
  expect(update).not.toHaveBeenCalled();
});

it.each(["user", "workspace", "role"])("isolates a late save after the %s changes", async (change) => {
  let resolveSave!: (value: AgentMemoryDocument) => void;
  const update = vi.spyOn(api, "updateAgentMemory").mockImplementation(() => new Promise((resolve) => { resolveSave = resolve; }));
  const view = render(<ProjectMemory projectID="p" principal={principal} />);
  fireEvent.click(screen.getByRole("button", { name: "项目记忆" }));
  fireEvent.change(await screen.findByRole("textbox", { name: "记忆正文" }), { target: { value: "Old private edit" } });
  fireEvent.click(screen.getByRole("button", { name: "保存记忆" }));
  await waitFor(() => expect(update).toHaveBeenCalledOnce());
  const nextPrincipal: Principal = { ...principal,
    ...(change === "user" ? { user_id: "next-user" } : change === "workspace" ? { workspace_id: "next-workspace" } : { role: "viewer" as const }),
  };
  vi.mocked(api.getAgentMemory).mockResolvedValue({ ...initial, user_id: nextPrincipal.user_id, files: { "memory_summary.md": "Current identity memory" } });
  view.rerender(<ProjectMemory projectID="p" principal={nextPrincipal} />);
  await waitFor(() => expect(screen.getByRole("textbox", { name: "记忆正文" })).toHaveProperty("value", "Current identity memory"));
  const reads = vi.mocked(api.getAgentMemory).mock.calls.length;
  const command = update.mock.calls[0][0];
  await act(async () => { resolveSave({ ...initial, version: 2, files: command.files, request_id: command.request_id }); });
  expect(screen.getByRole("textbox", { name: "记忆正文" })).toHaveProperty("value", "Current identity memory");
  expect(screen.getByRole("textbox", { name: "记忆正文" })).toHaveProperty("readOnly", change === "role");
  expect(api.getAgentMemory).toHaveBeenCalledTimes(reads);
  expect(update).toHaveBeenCalledOnce();
  expect(screen.queryByRole("alert")).toBeNull();
});

it("aborts the old identity read and ignores a response that still arrives", async () => {
  let resolveRead!: (value: AgentMemoryDocument) => void;
  vi.mocked(api.getAgentMemory).mockImplementationOnce(() => new Promise((resolve) => { resolveRead = resolve; }));
  const view = render(<ProjectMemory projectID="p" principal={principal} />);
  fireEvent.click(screen.getByRole("button", { name: "项目记忆" }));
  await waitFor(() => expect(api.getAgentMemory).toHaveBeenCalledOnce());
  const oldSignal = vi.mocked(api.getAgentMemory).mock.calls[0][1];
  vi.mocked(api.getAgentMemory).mockResolvedValue({ ...initial, user_id: "next-user", files: { "memory_summary.md": "New private memory" } });
  view.rerender(<ProjectMemory projectID="p" principal={{ ...principal, user_id: "next-user" }} />);
  await waitFor(() => expect(screen.getByRole("textbox", { name: "记忆正文" })).toHaveProperty("value", "New private memory"));
  expect(oldSignal?.aborted).toBe(true);
  await act(async () => { resolveRead(initial); });
  expect(screen.getByRole("textbox", { name: "记忆正文" })).toHaveProperty("value", "New private memory");
  expect(screen.queryByRole("alert")).toBeNull();
});
