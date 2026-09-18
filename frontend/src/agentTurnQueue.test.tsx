// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { AgentTurnStatus, Composer } from "./App";
import { api, ApiError } from "./api";
import type { AgentTurn } from "./types";

const turn: AgentTurn = { agent_turn_id: "queued-turn", user_id: "owner", workspace_id: "w", project_id: "p", conversation_id: "c", status: "accepted", request: { content: "Original", attachment_refs: [{ asset_id: "a", asset_snapshot_id: "s" }] }, created_at: "2026-09-06T00:00:00Z", updated_at: "2026-09-06T00:00:00Z" };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("freezes an uncertain queue edit and prevents competing cancellation or same-tick duplicate saves", async () => {
  let reject!: (error: Error) => void;
  const onEdit = vi.fn().mockImplementationOnce(() => new Promise<void>((_yes, no) => { reject = no; })).mockResolvedValue(undefined);
  const cancel = vi.fn();
  render(<AgentTurnStatus turn={turn} onCancel={cancel} onEdit={onEdit} />);
  fireEvent.click(screen.getByRole("button", { name: "编辑排队消息", exact: true }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Revised" } });
  const save = screen.getByRole("button", { name: "保存排队消息" });
  act(() => { save.click(); save.click(); });
  expect(onEdit).toHaveBeenCalledOnce();
  await act(async () => reject(new Error("Response lost")));
  expect(screen.getByRole("textbox")).toHaveProperty("value", "Revised");
  expect(screen.getByRole("textbox")).toHaveProperty("disabled", true);
  expect(screen.getByRole("button", { name: "取消排队消息" })).toHaveProperty("disabled", true);
  expect(screen.getByRole("button", { name: "放弃修改" })).toHaveProperty("disabled", true);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "保存排队消息" })));
  expect(onEdit.mock.calls[1]).toEqual(onEdit.mock.calls[0]);
  expect(cancel).not.toHaveBeenCalled();
});

it("retains an editing draft when execution terminates and isolates late errors", async () => {
  let reject!: (error: Error) => void;
  const onEdit = vi.fn(() => new Promise<void>((_yes, no) => { reject = no; }));
  const { rerender } = render(<AgentTurnStatus turn={turn} onCancel={vi.fn()} onEdit={onEdit} />);
  fireEvent.click(screen.getByRole("button", { name: "编辑排队消息", exact: true }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Unsaved draft" } });
  fireEvent.click(screen.getByRole("button", { name: "保存排队消息" }));
  rerender(<AgentTurnStatus turn={{ ...turn, status: "cancelled" }} onCancel={vi.fn()} onEdit={onEdit} />);
  await act(async () => reject(new ApiError("LATE", "Old failure", 502)));
  expect(screen.getByRole("textbox")).toHaveProperty("value", "Unsaved draft");
  expect(screen.queryByText("Old failure")).toBeNull();
  expect(screen.getByRole("button", { name: "保存排队消息" })).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "放弃修改" }));
  expect(screen.queryByRole("textbox")).toBeNull();
});

it("preserves queue edit payload and the supplied key across manual transport retries", async () => {
  const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response(JSON.stringify({ data: turn }), { status: 200 }));
  await api.updateQueuedAgentTurn({ ...turn, agent_turn_id: "turn/with space" }, "Changed", "manual-key");
  await api.updateQueuedAgentTurn({ ...turn, agent_turn_id: "turn/with space" }, "Changed", "manual-key");
  for (const [url, init] of fetch.mock.calls) {
    expect(url).toBe("/api/v1/agent-turns/turn%2Fwith%20space/queued-message");
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBe("manual-key");
    expect(JSON.parse(init?.body as string)).toEqual({ content: "Changed", expected_content: "Original" });
  }
});

it("edits the exact queued request and blocks duplicate submissions", async () => {
  let finish!: () => void;
  const onEdit = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
  render(<AgentTurnStatus turn={turn} onCancel={vi.fn()} onEdit={onEdit} />);
  fireEvent.click(screen.getByRole("button", { name: "编辑排队消息", exact: true }));
  const draft = screen.getByRole("textbox", { name: "编辑排队消息内容" });
  expect(document.activeElement).toBe(draft);
  fireEvent.change(draft, { target: { value: "Revised" } });
  const save = screen.getByRole("button", { name: "保存排队消息" });
  fireEvent.click(save); fireEvent.click(save);
  expect(onEdit).toHaveBeenCalledExactlyOnceWith(turn, "Revised");
  expect((screen.getByRole("button", { name: "取消排队消息" }) as HTMLButtonElement).disabled).toBe(true);
  await act(async () => finish());
  expect(screen.queryByRole("textbox")).toBeNull();
});

it.each(["running", "accepted"] as const)("retains the local draft when the %s request changes while editing", (status) => {
  const onEdit = vi.fn();
  const view = render(<AgentTurnStatus turn={turn} onCancel={vi.fn()} onEdit={onEdit} />);
  fireEvent.click(screen.getByRole("button", { name: "编辑排队消息", exact: true }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Local draft" } });
  view.rerender(<AgentTurnStatus turn={{ ...turn, status, request: { ...turn.request, content: "Updated elsewhere" } }} onCancel={vi.fn()} onEdit={onEdit} />);
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("Local draft");
  expect(screen.getByRole("alert").textContent).toContain("未覆盖你的草稿");
  expect((screen.getByRole("button", { name: "保存排队消息" }) as HTMLButtonElement).disabled).toBe(true);
  expect(onEdit).not.toHaveBeenCalled();
});

it("keeps failed edits available for correction without claiming they were saved", async () => {
  const onEdit = vi.fn().mockRejectedValue(new ApiError("AGENT_TURN_STATE_CONFLICT", "消息已开始处理", 409));
  render(<AgentTurnStatus turn={turn} onCancel={vi.fn()} onEdit={onEdit} />);
  fireEvent.click(screen.getByRole("button", { name: "编辑排队消息", exact: true }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Unsaved" } });
  fireEvent.click(screen.getByRole("button", { name: "保存排队消息" }));
  await screen.findByRole("alert");
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("Unsaved");
  expect(screen.getByRole("alert").textContent).toBe("消息已开始处理");
});

it.each([undefined, "2026-09-06T00:00:01Z"])("hides unauthorized or already-started queue edit controls", (started_at) => {
  render(<AgentTurnStatus turn={{ ...turn, started_at }} onCancel={vi.fn()} onEdit={started_at ? vi.fn() : undefined} />);
  expect(screen.queryByRole("button", { name: "编辑排队消息", exact: true })).toBeNull();
  if (started_at) expect(screen.getByText("等待恢复执行")).toBeTruthy();
});

it("sends an idempotent compare-and-set PATCH without changing attachments or Skill selection", async () => {
  const response = () => new Response(JSON.stringify({ data: turn }), { status: 200 });
  const fetchMock = vi.spyOn(globalThis, "fetch").mockRejectedValueOnce(new TypeError("lost receipt")).mockResolvedValueOnce(response());
  expect(await api.updateQueuedAgentTurn(turn, "Revised")).toEqual(turn);
  expect(fetchMock).toHaveBeenCalledTimes(2);
  const [url, options] = fetchMock.mock.calls[0];
  expect(url).toBe("/api/v1/agent-turns/queued-turn/queued-message");
  expect(options?.method).toBe("PATCH");
  expect(JSON.parse(options?.body as string)).toEqual({ content: "Revised", expected_content: "Original" });
  expect(new Headers(options?.headers).get("Idempotency-Key")).toBe(new Headers(fetchMock.mock.calls[1][1]?.headers).get("Idempotency-Key"));
});

it("keeps next-turn sends available during execution and names their queue semantics", async () => {
  const onSend = vi.fn().mockResolvedValue(undefined);
  render(<Composer projectID="p" projectAssets={[]} capabilities={[]} runSnapshot={null} context={{ view: {} }} onClearSelection={vi.fn()} onSend={onSend} queueMode />);
  fireEvent.change(screen.getByRole("textbox", { name: "给 Agent 的消息" }), { target: { value: "Next turn" } });
  fireEvent.click(screen.getByRole("button", { name: "加入下一轮队列" }));
  await waitFor(() => expect(onSend).toHaveBeenCalledOnce());
  await screen.findByRole("button", { name: "加入下一轮队列" });
  fireEvent.change(screen.getByRole("textbox", { name: "给 Agent 的消息" }), { target: { value: "Another turn" } });
  fireEvent.click(screen.getByRole("button", { name: "加入下一轮队列" }));
  await waitFor(() => expect(onSend).toHaveBeenCalledTimes(2));
});
