// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api, ApiError } from "../../api";
import type { AgentMemoryProposal, AgentToolCall } from "../../types";
import { MemoryChangeApproval } from "./MemoryChangeApproval";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const call = { agent_tool_call_id: "call", project_id: "p", tool_id: "runtime:publish_agent_memory", arguments_hash: "hash", status: "pending_approval",
  approval: { agent_tool_approval_id: "approval", status: "pending", version: 1, title: "Save", reason: "Private", options: ["approve", "reject"], subject_snapshot_hash: "subject" } } as AgentToolCall;
const proposal: AgentMemoryProposal = { agent_tool_call_id: "call", project_id: "p", user_id: "u", arguments_hash: "hash", approval_id: "approval", approval_version: 1, subject_snapshot_hash: "subject",
  expected_version: 1, can_approve: true, can_reject: true, available: true, files: { "memory_summary.md": "PRIVATE_NEW", "new.md": "ADDED" },
  current: { project_id: "p", user_id: "u", version: 1, files: { "memory_summary.md": "PRIVATE_OLD", "remove.md": "REMOVED" }, enabled: true, forgotten: false, content_hash: "old" } };
const props = { call, userID: "u", interaction: { viewKey: "agent_tool_approval", commands: ["approve", "deny"] }, onResolve: vi.fn() };

it("shows replacements, additions and deletions before saving the exact approval", async () => {
  vi.spyOn(api, "getAgentMemoryProposal").mockResolvedValue(proposal);
  const resolve = vi.fn().mockResolvedValue(undefined);
  render(<MemoryChangeApproval {...props} onResolve={resolve} />);
  expect(screen.queryByRole("button", { name: "确认保存记忆" })).toBeNull();
  await screen.findByText("memory_summary.md · 替换");
  expect(screen.getByText("new.md · 新增")).toBeTruthy();
  expect(screen.getByText("remove.md · 删除")).toBeTruthy();
  expect(screen.getByText("PRIVATE_NEW")).toBeTruthy();
  expect(screen.getByText("PRIVATE_OLD")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "确认保存记忆" }));
  await waitFor(() => expect(resolve).toHaveBeenCalledWith(call.approval, "approve"));
});

it.each([
  { user_id: "other" }, { project_id: "other" }, { agent_tool_call_id: "other" }, { arguments_hash: "other" },
  { approval_id: "other" }, { approval_version: 2 }, { subject_snapshot_hash: "other" },
  { current: { ...proposal.current, user_id: "other" } }, { current: { ...proposal.current, project_id: "other" } },
  { current: { ...proposal.current, version: 2 } }, { available: false },
])("rejects a mismatched private response %j", async patch => {
  vi.spyOn(api, "getAgentMemoryProposal").mockResolvedValue({ ...proposal, ...patch });
  render(<MemoryChangeApproval {...props} />);
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", "私有记忆提案与当前审批不一致。");
  expect(screen.queryByText("PRIVATE_NEW")).toBeNull();
  expect(screen.queryByRole("button", { name: "确认保存记忆" })).toBeNull();
});

it("does not expose private content or mutation controls after owner access is denied", async () => {
  vi.spyOn(api, "getAgentMemoryProposal").mockRejectedValue(new ApiError("WORKSPACE_ACCESS_DENIED", "仅本人可查看", 403));
  render(<MemoryChangeApproval {...props} />);
  await screen.findByRole("alert");
  expect(screen.queryByText("PRIVATE_NEW")).toBeNull();
  expect(screen.queryByRole("button", { name: "拒绝保存" })).toBeNull();
});

it("allows rejecting a stale proposal without displaying its old candidate", async () => {
  vi.spyOn(api, "getAgentMemoryProposal").mockResolvedValue({ ...proposal, current: { ...proposal.current, version: 2, files: {} }, files: {}, available: false, can_approve: false });
  const resolve = vi.fn().mockResolvedValue(undefined);
  render(<MemoryChangeApproval {...props} onResolve={resolve} />);
  expect(await screen.findByRole("button", { name: "确认保存记忆" })).toHaveProperty("disabled", true);
  expect(screen.queryByText("PRIVATE_NEW")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "拒绝保存" }));
  await waitFor(() => expect(resolve).toHaveBeenCalledWith(call.approval, "reject"));
});

it.each(["approve", "reject"] as const)("retains the original %s action after a lost response and refresh", async action => {
  vi.spyOn(api, "getAgentMemoryProposal").mockResolvedValue(proposal);
  let reject!: (error: Error) => void;
  const resolve = vi.fn().mockImplementationOnce(() => new Promise<void>((_yes, no) => { reject = no; })).mockResolvedValue(undefined);
  render(<MemoryChangeApproval {...props} onResolve={resolve} />);
  const label = action === "approve" ? "确认保存记忆" : "拒绝保存";
  const opposite = action === "approve" ? "拒绝保存" : "确认保存记忆";
  const button = await screen.findByRole("button", { name: label });
  act(() => { button.click(); button.click(); });
  expect(resolve).toHaveBeenCalledOnce();
  await act(async () => reject(new Error("Lost response")));
  expect(screen.getByRole("button", { name: opposite })).toHaveProperty("disabled", true);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "刷新记忆提案" })));
  expect(screen.getByRole("button", { name: opposite })).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: label }));
  await waitFor(() => expect(resolve).toHaveBeenCalledTimes(2));
});

it("clears candidate after a definitive conflict", async () => {
  vi.spyOn(api, "getAgentMemoryProposal").mockResolvedValue(proposal);
  const resolve = vi.fn().mockRejectedValue(new ApiError("AGENT_MEMORY_CONFLICT", "版本冲突", 409));
  render(<MemoryChangeApproval {...props} onResolve={resolve} />);
  fireEvent.click(await screen.findByRole("button", { name: "确认保存记忆" }));
  await screen.findByRole("alert");
  expect(screen.queryByText("PRIVATE_NEW")).toBeNull();
});

it("does not display a late private response after switching user", async () => {
  let finish!: (value: AgentMemoryProposal) => void;
  vi.spyOn(api, "getAgentMemoryProposal").mockImplementationOnce(() => new Promise(yes => { finish = yes; })).mockRejectedValue(new Error("No access"));
  const { rerender } = render(<MemoryChangeApproval {...props} />);
  rerender(<MemoryChangeApproval {...props} userID="other" />);
  await screen.findByRole("alert");
  await act(async () => finish(proposal));
  expect(screen.queryByText("PRIVATE_NEW")).toBeNull();
});

it("makes the owner's read-only view non-actionable", async () => {
  vi.spyOn(api, "getAgentMemoryProposal").mockResolvedValue({ ...proposal, can_approve: false, can_reject: false });
  render(<MemoryChangeApproval {...props} readOnly />);
  await screen.findByText("PRIVATE_NEW");
  expect(screen.queryByRole("button", { name: "确认保存记忆" })).toBeNull();
  expect(screen.queryByRole("button", { name: "拒绝保存" })).toBeNull();
});
