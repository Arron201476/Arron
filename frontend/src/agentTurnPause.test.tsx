// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { AgentTurnStatus, nextAgentTurnLiveState } from "./App";
import { api, ApiError } from "./api";
import { agentTurnMessage } from "./agentTurns";
import { agentEventIsTerminal, parseAgentEvent } from "./agentEventProtocol";
import type { AgentTurn } from "./types";

const turn: AgentTurn = { agent_turn_id: "turn", user_id: "owner", workspace_id: "w", project_id: "p", conversation_id: "c", status: "running", request: { content: "Original" }, created_at: "2026-09-06T00:00:00Z", started_at: "2026-09-06T00:00:01Z", updated_at: "2026-09-06T00:00:01Z" };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("blocks same-tick duplicate cancellation and retains only that action after an unknown response", async () => {
  let reject!: (error: Error) => void;
  const cancel = vi.fn().mockImplementationOnce(() => new Promise<void>((_yes, no) => { reject = no; })).mockResolvedValue(undefined);
  render(<AgentTurnStatus turn={turn} onCancel={cancel} onControl={vi.fn()} />);
  const button = screen.getByRole("button", { name: "停止本轮" });
  act(() => { button.click(); button.click(); });
  expect(cancel).toHaveBeenCalledOnce();
  await act(async () => reject(new ApiError("COMMAND_IN_PROGRESS", "Pending cancellation", 409)));
  expect(screen.getByRole("button", { name: "暂停本轮" })).toHaveProperty("disabled", true);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "停止本轮" })));
  expect(cancel).toHaveBeenCalledTimes(2);
});

it("hides every write control for a read-only main turn", () => {
  render(<AgentTurnStatus turn={turn} readOnly onCancel={vi.fn()} onControl={vi.fn()} onAppend={vi.fn()} onEdit={vi.fn()} />);
  expect(screen.queryByRole("button", { name: "停止本轮" })).toBeNull();
  expect(screen.queryByRole("button", { name: "暂停本轮" })).toBeNull();
  expect(screen.queryByRole("button", { name: "编辑排队消息", exact: true })).toBeNull();
});

it("preserves cancellation identity across automatic and manual retries", async () => {
  const fetch = vi.spyOn(globalThis, "fetch").mockRejectedValueOnce(new Error("Response lost")).mockImplementation(async () => new Response(JSON.stringify({ data: { turn, accepted: true } }), { status: 200 }));
  await api.cancelAgentTurn("turn/with space", "cancel-key");
  await api.cancelAgentTurn("turn/with space", "cancel-key");
  expect(fetch).toHaveBeenCalledTimes(3);
  for (const [url, init] of fetch.mock.calls) {
    expect(url).toBe("/api/v1/agent-turns/turn%2Fwith%20space/cancel");
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBe("cancel-key");
    expect(init?.body).toBe("{}");
  }
});

it("pauses the exact turn and blocks duplicate controls while pending", async () => {
  let finish!: () => void;
  const onControl = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
  render(<AgentTurnStatus turn={turn} onCancel={vi.fn()} onControl={onControl} />);
  const pause = screen.getByRole("button", { name: "暂停本轮" });
  fireEvent.click(pause); fireEvent.click(pause);
  expect(onControl).toHaveBeenCalledExactlyOnceWith(turn, "pause", expect.any(String));
  expect((screen.getByRole("button", { name: "停止本轮" }) as HTMLButtonElement).disabled).toBe(true);
  await act(async () => finish());
});

it.each(["pausing", "committing", "committed", "cancel_requested", "cancelled", "failed"] as const)("does not offer premature resume for %s", (status) => {
  render(<AgentTurnStatus turn={{ ...turn, status }} onCancel={vi.fn()} onControl={vi.fn()} />);
  expect(screen.queryByRole("button", { name: "继续本轮" })).toBeNull();
  expect(screen.queryByRole("button", { name: "暂停本轮" })).toBeNull();
});

it("retains control idempotency after a lost receipt and changes keys for another action", async () => {
  const onControl = vi.fn().mockRejectedValueOnce(new Error("Lost receipt")).mockResolvedValue(undefined);
  const view = render(<AgentTurnStatus turn={turn} onCancel={vi.fn()} onControl={onControl} />);
  fireEvent.click(screen.getByRole("button", { name: "暂停本轮" }));
  await screen.findByRole("alert");
  fireEvent.click(screen.getByRole("button", { name: "暂停本轮" }));
  await waitFor(() => expect(onControl).toHaveBeenCalledTimes(2));
  expect(onControl.mock.calls[0][2]).toBe(onControl.mock.calls[1][2]);
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  view.rerender(<AgentTurnStatus turn={{ ...turn, status: "paused" }} live={{ status: "Old running status", output: "" }} onCancel={vi.fn()} onControl={onControl} />);
  expect(screen.getByText("本轮已暂停")).toBeTruthy();
  expect(screen.queryByText("Old running status")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "继续本轮" }));
  await waitFor(() => expect(onControl).toHaveBeenCalledTimes(3));
  expect(onControl.mock.calls[2][1]).toBe("resume");
  expect(onControl.mock.calls[2][2]).not.toBe(onControl.mock.calls[0][2]);
});

it("hides pause and resume when the caller has no author permission", () => {
  render(<AgentTurnStatus turn={{ ...turn, status: "paused" }} onCancel={vi.fn()} />);
  expect(screen.queryByRole("button", { name: "继续本轮" })).toBeNull();
});

it("shows a model recovery checkpoint as resumable without offering a fresh run", async () => {
  const onControl = vi.fn(async () => {});
  const recovery = { ...turn, status: "paused" as const, error_code: "SDK_MODEL_RECOVERY_REQUIRED" };
  const view = render(<AgentTurnStatus turn={recovery} onCancel={vi.fn()} onControl={onControl} />);
  expect(screen.getByText("等待恢复连接")).toBeTruthy();
  expect(screen.getByRole("status").textContent).toContain("原执行状态已保存");
  fireEvent.click(screen.getByRole("button", { name: "继续本轮" }));
  await waitFor(() => expect(onControl).toHaveBeenCalledWith(recovery, "resume", expect.any(String)));
  view.rerender(<AgentTurnStatus turn={turn} onCancel={vi.fn()} onControl={onControl} />);
  expect(screen.queryByText("等待恢复连接")).toBeNull();
});

it("distinguishes an unknown external operation from a model connection failure", async () => {
  const onControl = vi.fn(async () => { throw new ApiError("SDK_TOOL_OUTCOME_UNRESOLVED", "请先核对原操作", 409); });
  const recovery = { ...turn, status: "paused" as const, error_code: "SDK_TOOL_OUTCOME_UNRESOLVED" };
  render(<AgentTurnStatus turn={recovery} onCancel={vi.fn()} onControl={onControl} />);
  expect(screen.getByText("等待核对外部操作")).toBeTruthy();
  expect(screen.getByRole("status").textContent).toBe("外部操作结果未确认，原执行已暂停。");
  expect(screen.queryByText("等待恢复连接")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "继续本轮" }));
  await screen.findByText("请先核对原操作");
  expect(onControl).toHaveBeenCalledExactlyOnceWith(recovery, "resume", expect.any(String));
});

it.each(["pause", "resume"] as const)("uses the supplied durable key for %s transport retries", async (action) => {
  const fetchMock = vi.spyOn(globalThis, "fetch").mockRejectedValueOnce(new TypeError("Lost receipt")).mockResolvedValueOnce(new Response(JSON.stringify({ data: turn }), { status: 200 }));
  await api.controlAgentTurnPause("turn/id", action, "durable-control-key");
  expect(fetchMock).toHaveBeenCalledTimes(2);
  for (const [url, options] of fetchMock.mock.calls) {
    expect(url).toBe(`/api/v1/agent-turns/turn%2Fid/${action}`);
    expect(options?.body).toBe("{}");
    expect(new Headers(options?.headers).get("Idempotency-Key")).toBe("durable-control-key");
  }
});

it.each([
  ["agent.tool.started", "", "正在执行程序化工具调用"],
  ["agent.tool.completed", "incomplete", "程序执行未完成"],
  ["agent.tool.completed", "completed", "正在整理结果"],
] as const)("shows program lifecycle %s/%s without marking the turn terminal", (event_type, status, label) => {
  const event = parseAgentEvent({ schema_version: "1.0.0", event_id: "program-event", event_type, project_id: "p", conversation_id: "c", turn_id: "turn", terminal: false, payload: { tool_name: "programmatic_tool_calling", status }, occurred_at: turn.updated_at });
  expect(event).not.toBeNull();
  expect(agentEventIsTerminal(event!)).toBe(false);
  expect(nextAgentTurnLiveState({ status: "previous", output: "retained" }, event!)).toEqual({ status: label, output: "retained" });
});

it.each([["agent.turn.pause_requested", "pausing"], ["agent.turn.paused", "paused"]] as const)("keeps %s as a non-terminal lifecycle event", (event_type, status) => {
  const event = parseAgentEvent({ schema_version: "1.0.0", event_id: "event", event_type, project_id: "p", conversation_id: "c", turn_id: "turn", terminal: false, payload: { status }, occurred_at: turn.updated_at });
  expect(event).not.toBeNull();
  expect(agentEventIsTerminal(event!)).toBe(false);
  expect(nextAgentTurnLiveState(undefined, event!).status).toContain("暂停");
  expect(agentTurnMessage({ ...turn, status }).delivery_status).toBe(status);
});
