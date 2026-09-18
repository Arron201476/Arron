// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api, ApiError } from "../../api";
import type { AgentMemoryToolProposal, AgentToolCall } from "../../types";
import { MemoryToolApproval } from "./MemoryToolApproval";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const call = { agent_tool_call_id: "call", project_id: "p", tool_id: "runtime:exec_command", tool_name: "exec_command", arguments_hash: "hash", status: "pending_approval",
  approval: { agent_tool_approval_id: "approval", status: "pending", version: 1, options: ["approve", "reject"], subject_snapshot_hash: "subject" } } as AgentToolCall;
const proposal: AgentMemoryToolProposal = { agent_tool_call_id: "call", generation_id: "generation", project_id: "p", user_id: "u", arguments_hash: "hash",
  approval_id: "approval", approval_version: 1, subject_snapshot_hash: "subject", arguments: { command: "PRIVATE_COMMAND" }, can_approve: true, can_reject: true };
const props = { call, userID: "u", interaction: { viewKey: "agent_tool_approval", commands: ["approve", "deny"] }, onResolve: vi.fn() };

it("shows confirmed private parameters before allowing the exact approval", async () => {
  vi.spyOn(api, "getAgentMemoryToolProposal").mockResolvedValue(proposal);
  const resolve = vi.fn().mockResolvedValue(undefined);
  render(<MemoryToolApproval {...props} onResolve={resolve} />);
  expect(screen.queryByRole("button", { name: "允许执行" })).toBeNull();
  await screen.findByText(/PRIVATE_COMMAND/);
  fireEvent.click(screen.getByRole("button", { name: "允许执行" }));
  await waitFor(() => expect(resolve).toHaveBeenCalledWith(call.approval, "approve"));
});

it.each([
  { user_id: "other" }, { project_id: "other" }, { agent_tool_call_id: "other" }, { arguments_hash: "other" },
  { approval_id: "other" }, { approval_version: 2 }, { subject_snapshot_hash: "other" }, { generation_id: "" },
  { arguments: undefined }, { can_reject: false },
])("rejects mismatched private proposal %j", async patch => {
  vi.spyOn(api, "getAgentMemoryToolProposal").mockResolvedValue({ ...proposal, ...patch });
  render(<MemoryToolApproval {...props} />);
  await screen.findByRole("alert");
  expect(screen.queryByText(/PRIVATE_COMMAND/)).toBeNull();
  expect(screen.queryByRole("button", { name: "允许执行" })).toBeNull();
});

it("permits rejecting an unavailable proposal without displaying private arguments", async () => {
  vi.spyOn(api, "getAgentMemoryToolProposal").mockResolvedValue({ ...proposal, arguments: undefined, can_approve: false });
  const resolve = vi.fn().mockResolvedValue(undefined);
  render(<MemoryToolApproval {...props} onResolve={resolve} />);
  expect(await screen.findByRole("button", { name: "允许执行" })).toHaveProperty("disabled", true);
  expect(screen.queryByText(/PRIVATE_COMMAND/)).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "拒绝执行" }));
  await waitFor(() => expect(resolve).toHaveBeenCalledWith(call.approval, "reject"));
});

it.each(["approve", "reject"] as const)("retains original %s after an unknown outcome and refresh", async action => {
  vi.spyOn(api, "getAgentMemoryToolProposal").mockResolvedValue(proposal);
  let reject!: (error: Error) => void;
  const resolve = vi.fn().mockImplementationOnce(() => new Promise<void>((_yes, no) => { reject = no; })).mockResolvedValue(undefined);
  render(<MemoryToolApproval {...props} onResolve={resolve} />);
  const label = action === "approve" ? "允许执行" : "拒绝执行", other = action === "approve" ? "拒绝执行" : "允许执行";
  const button = await screen.findByRole("button", { name: label });
  act(() => { button.click(); button.click(); });
  expect(resolve).toHaveBeenCalledOnce();
  await act(async () => reject(new Error("Unknown outcome")));
  expect(screen.getByRole("button", { name: other })).toHaveProperty("disabled", true);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "刷新私有工具提案" })));
  expect(screen.getByRole("button", { name: other })).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: label }));
  await waitFor(() => expect(resolve).toHaveBeenCalledTimes(2));
});

it("clears private arguments after a definitive conflict", async () => {
  vi.spyOn(api, "getAgentMemoryToolProposal").mockResolvedValue(proposal);
  render(<MemoryToolApproval {...props} onResolve={vi.fn().mockRejectedValue(new ApiError("AGENT_MEMORY_CONFLICT", "Conflict", 409))} />);
  fireEvent.click(await screen.findByRole("button", { name: "允许执行" }));
  await screen.findByRole("alert");
  expect(screen.queryByText(/PRIVATE_COMMAND/)).toBeNull();
});

it("drops late private responses after switching users", async () => {
  let finish!: (value: AgentMemoryToolProposal) => void;
  vi.spyOn(api, "getAgentMemoryToolProposal").mockImplementationOnce(() => new Promise(yes => { finish = yes; })).mockRejectedValue(new Error("No access"));
  const { rerender } = render(<MemoryToolApproval {...props} />);
  rerender(<MemoryToolApproval {...props} userID="other" />);
  await screen.findByRole("alert");
  await act(async () => finish(proposal));
  expect(screen.queryByText(/PRIVATE_COMMAND/)).toBeNull();
});

it("shows private parameters read-only without mutation controls", async () => {
  vi.spyOn(api, "getAgentMemoryToolProposal").mockResolvedValue({ ...proposal, can_approve: false, can_reject: false });
  render(<MemoryToolApproval {...props} readOnly />);
  await screen.findByText(/PRIVATE_COMMAND/);
  expect(screen.queryByRole("button", { name: "允许执行" })).toBeNull();
  expect(screen.queryByRole("button", { name: "拒绝执行" })).toBeNull();
});
