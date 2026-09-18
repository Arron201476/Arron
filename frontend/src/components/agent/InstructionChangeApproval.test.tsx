// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api, ApiError } from "../../api";
import type { AgentInstructionProposal, AgentToolCall } from "../../types";
import { InstructionChangeApproval } from "./InstructionChangeApproval";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const call = { agent_tool_call_id: "call", project_id: "p", tool_id: "runtime:update_saved_instructions", arguments_hash: "hash", status: "pending_approval",
  approval: { agent_tool_approval_id: "approval", status: "pending", version: 1, title: "确认保存 Agent 规则", reason: "后续新执行生效", options: ["approve", "reject"], subject_snapshot_hash: "subject" } } as AgentToolCall;
const proposal: AgentInstructionProposal = { agent_tool_call_id: "call", project_id: "p", user_id: "u", arguments_hash: "hash", can_approve: true, can_reject: true,
  arguments: { scope: "user", expected_version: 1, content: "PRIVATE_NEW_RULE", enabled: true },
  current: { scope: "user", scope_ref: "u", version: 1, content: "OLD_RULE", enabled: true, content_hash: "old", can_edit: true } };
const interaction = { viewKey: "agent_tool_approval", commands: ["approve", "deny"] };

it.each(["approve", "reject"] as const)("retains only the original rule %s command after an unknown response", async (action) => {
  vi.spyOn(api, "getAgentInstructionProposal").mockResolvedValue(proposal);
  let reject!: (error: Error) => void;
  const resolve = vi.fn().mockImplementationOnce(() => new Promise<void>((_yes, no) => { reject = no; })).mockResolvedValue(undefined);
  render(<InstructionChangeApproval call={call} interaction={interaction} onResolve={resolve} />);
  await screen.findByText("PRIVATE_NEW_RULE");
  const label = action === "approve" ? "确认保存规则" : "拒绝变更";
  const other = action === "approve" ? "拒绝变更" : "确认保存规则";
  const button = screen.getByRole("button", { name: label });
  act(() => { button.click(); button.click(); });
  expect(resolve).toHaveBeenCalledOnce();
  await act(async () => reject(new Error("Response lost")));
  expect(screen.getByText("PRIVATE_NEW_RULE")).toBeTruthy();
  expect(screen.getByRole("button", { name: other })).toHaveProperty("disabled", true);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "刷新规则提案" })));
  expect(screen.getByRole("button", { name: other })).toHaveProperty("disabled", true);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: label })));
  expect(resolve).toHaveBeenCalledTimes(2);
});

it("does not transfer late submission failures to a new rule approval", async () => {
  vi.spyOn(api, "getAgentInstructionProposal").mockResolvedValue(proposal);
  let reject!: (error: Error) => void;
  const resolve = vi.fn(() => new Promise<void>((_yes, no) => { reject = no; }));
  const { rerender } = render(<InstructionChangeApproval call={call} interaction={interaction} onResolve={resolve} />);
  await screen.findByText("PRIVATE_NEW_RULE");
  fireEvent.click(screen.getByRole("button", { name: "确认保存规则" }));
  rerender(<InstructionChangeApproval call={{ ...call, approval: { ...call.approval!, version: 2 } }} interaction={interaction} onResolve={resolve} />);
  await screen.findByText("PRIVATE_NEW_RULE");
  await act(async () => reject(new Error("Late old error")));
  expect(screen.queryByText("Late old error")).toBeNull();
  expect(screen.getByRole("button", { name: "拒绝变更" })).toHaveProperty("disabled", false);
});

it("does not expose rule mutation controls in a read-only view", async () => {
  vi.spyOn(api, "getAgentInstructionProposal").mockResolvedValue(proposal);
  render(<InstructionChangeApproval call={call} readOnly interaction={interaction} onResolve={vi.fn()} />);
  await screen.findByText("PRIVATE_NEW_RULE");
  expect(screen.queryByRole("button", { name: "确认保存规则" })).toBeNull();
  expect(screen.queryByRole("button", { name: "拒绝变更" })).toBeNull();
});

it("requires the matching private proposal before author approval", async () => {
  vi.spyOn(api, "getAgentInstructionProposal").mockResolvedValue(proposal);
  const resolve = vi.fn().mockResolvedValue(undefined);
  render(<InstructionChangeApproval call={call} interaction={interaction} onResolve={resolve} />);
  expect(screen.queryByRole("button", { name: "确认保存规则" })).toBeNull();
  expect(await screen.findByText("PRIVATE_NEW_RULE")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "确认保存规则" }));
  await waitFor(() => expect(resolve).toHaveBeenCalledWith(call.approval, "approve"));
});

it("does not offer approval or rejection to other users", async () => {
  vi.spyOn(api, "getAgentInstructionProposal").mockRejectedValue(new ApiError("WORKSPACE_ACCESS_DENIED", "只有发起用户可以查看或确认", 403));
  const resolve = vi.fn();
  render(<InstructionChangeApproval call={call} interaction={interaction} onResolve={resolve} />);
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", "只有发起用户可以查看或确认");
  expect(screen.queryByText("PRIVATE_NEW_RULE")).toBeNull();
  expect(screen.queryByRole("button", { name: "确认保存规则" })).toBeNull();
  expect(screen.queryByRole("button", { name: "拒绝变更" })).toBeNull();
  expect(resolve).not.toHaveBeenCalled();
});

it("rejects a receipt belonging to another call", async () => {
  vi.spyOn(api, "getAgentInstructionProposal").mockResolvedValue({ ...proposal, arguments_hash: "foreign" });
  render(<InstructionChangeApproval call={call} interaction={interaction} onResolve={vi.fn()} />);
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", "规则提案与当前审批不一致。");
  expect(screen.queryByText("PRIVATE_NEW_RULE")).toBeNull();
});

it("keeps rejection available for a stale version but does not overwrite it", async () => {
  vi.spyOn(api, "getAgentInstructionProposal").mockResolvedValue({ ...proposal, can_approve: false });
  const resolve = vi.fn().mockResolvedValue(undefined);
  render(<InstructionChangeApproval call={call} interaction={interaction} onResolve={resolve} />);
  expect(await screen.findByRole("button", { name: "确认保存规则" })).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "拒绝变更" }));
  await waitFor(() => expect(resolve).toHaveBeenCalledWith(call.approval, "reject"));
});

it("hides the private text after the approval is resolved", async () => {
  const load = vi.spyOn(api, "getAgentInstructionProposal").mockResolvedValue(proposal);
  const { rerender } = render(<InstructionChangeApproval call={call} interaction={interaction} onResolve={vi.fn()} />);
  await screen.findByText("PRIVATE_NEW_RULE");
  rerender(<InstructionChangeApproval call={{ ...call, status: "completed", approval: { ...call.approval!, status: "approved" } }} interaction={interaction} onResolve={vi.fn()} />);
  await waitFor(() => expect(screen.queryByText("PRIVATE_NEW_RULE")).toBeNull());
  expect(load).toHaveBeenCalledOnce();
});
