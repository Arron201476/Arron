// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { AgentTurnInputs } from "./AgentTaskInputs";
import { AgentTurnStatus } from "../../App";
import { AgentExecutionHistory } from "./AgentExecutionHistory";
import { api } from "../../api";
import type { AgentTurn } from "../../types";

const turn: AgentTurn = { agent_turn_id: "turn", workspace_id: "w", project_id: "p", conversation_id: "c", user_id: "owner", status: "running", request: { content: "Original" }, created_at: "2026-09-06T00:00:00Z", updated_at: "2026-09-06T00:00:00Z" };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("submits the exact turn once and retains a submission key after lost receipts", async () => {
  const append = vi.fn().mockRejectedValueOnce(new Error("Lost receipt")).mockResolvedValue(undefined);
  render(<AgentTurnInputs turn={turn} onAppend={append} />);
  fireEvent.click(screen.getByRole("button", { name: "追加本轮要求" }));
  fireEvent.change(screen.getByRole("textbox", { name: "本轮追加要求" }), { target: { value: "Additional requirement" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "提交追加要求" })); });
  expect(screen.getByRole("alert").textContent).toBe("Lost receipt");
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "提交追加要求" })); });
  expect(append.mock.calls[0]).toEqual([turn, "Additional requirement", expect.any(String)]);
  expect(append.mock.calls[1]).toEqual(append.mock.calls[0]);
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(screen.queryByText("已送入模型")).toBeNull();
});

it.each(["committed", "failed", "cancelled"] as const)("retains an open draft when the main turn becomes %s", async (status) => {
  const append = vi.fn();
  const view = render(<AgentTurnStatus turn={turn} onCancel={vi.fn()} onAppend={append} />);
  fireEvent.click(screen.getByRole("button", { name: "追加本轮要求" }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Unsubmitted requirement" } });
  view.rerender(<AgentTurnStatus turn={{ ...turn, status }} onCancel={vi.fn()} onAppend={append} />);
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("Unsubmitted requirement");
  expect((screen.getByRole("button", { name: "提交追加要求" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "收起追加输入" }));
  fireEvent.click(screen.getByRole("button", { name: "查看未提交草稿" }));
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("Unsubmitted requirement");
  expect(append).not.toHaveBeenCalled();
});

it("blocks submission after author permission is revoked without losing the draft", () => {
  const view = render(<AgentTurnInputs turn={turn} onAppend={vi.fn()} />);
  fireEvent.click(screen.getByRole("button", { name: "追加本轮要求" }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Retain this draft" } });
  view.rerender(<AgentTurnInputs turn={turn} />);
  expect(screen.getByRole("alert").textContent).toContain("当前已无追加权限");
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("Retain this draft");
  expect((screen.getByRole("button", { name: "提交追加要求" }) as HTMLButtonElement).disabled).toBe(true);
});

it("keeps both confirmed and late receipts visible in execution history without offering writes", () => {
  render(<AgentExecutionHistory turns={[{ ...turn, status: "committed", additional_inputs: [
    { input_id: "first", agent_turn_id: "turn", sequence: 1, user_id: "owner", content: "Sent", status: "included", created_at: turn.created_at },
    { input_id: "last", agent_turn_id: "turn", sequence: 2, user_id: "owner", content: "Too late", status: "received", created_at: turn.created_at },
  ] }]} calls={[]} />);
  fireEvent.click(screen.getByText("已完成 · 0 次工具调用"));
  fireEvent.click(screen.getByText("追加要求 · 2"));
  expect(screen.getByText("已送入模型")).toBeTruthy();
  expect(screen.getByText("未确认送入模型")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "追加本轮要求" })).toBeNull();
});

it("lets the author override automatic continuation while waiting for the native pause", async () => {
  const control = vi.fn().mockResolvedValue(undefined);
  const pausing = { ...turn, status: "pausing" as const };
  render(<AgentTurnStatus turn={pausing} onCancel={vi.fn()} onControl={control} />);
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "保持暂停" })); });
  expect(control).toHaveBeenCalledExactlyOnceWith(pausing, "pause", expect.any(String));
  expect(screen.queryByRole("button", { name: "继续本轮" })).toBeNull();
});

it("retries main input transport with the same caller key and escaped turn ID", async () => {
  const mock = vi.spyOn(globalThis, "fetch").mockRejectedValueOnce(new TypeError("Lost response")).mockResolvedValueOnce(new Response(JSON.stringify({ data: { input_id: "first" } }), { status: 202 }));
  await api.appendAgentTurnInput("turn/id", "Extra", "durable-key");
  expect(mock).toHaveBeenCalledTimes(2);
  for (const [url, options] of mock.mock.calls) {
    expect(url).toBe("/api/v1/agent-turns/turn%2Fid/inputs");
    expect(options?.body).toBe('{"content":"Extra"}');
    expect(new Headers(options?.headers).get("Idempotency-Key")).toBe("durable-key");
  }
});
