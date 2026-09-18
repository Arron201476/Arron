// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { AgentTaskInputs } from "./AgentTaskInputs";
import type { AgentTask } from "../../types";
import { api } from "../../api";

const task = { agent_task_id: "task", status: "running", cancel_requested: false, additional_inputs: [] } as unknown as AgentTask;
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("submits the exact task once without pretending it has already reached the model", async () => {
  let finish!: () => void;
  const onAppend = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
  const view = render(<AgentTaskInputs task={task} onAppend={onAppend} />);
  fireEvent.click(screen.getByRole("button", { name: "追加后台任务要求" }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Do not rewrite" } });
  const submit = screen.getByRole("button", { name: "提交追加要求" });
  fireEvent.click(submit); fireEvent.click(submit);
  expect(onAppend).toHaveBeenCalledExactlyOnceWith(task, "Do not rewrite", expect.any(String));
  await act(async () => finish());
  expect(screen.queryByRole("textbox")).toBeNull();
  view.rerender(<AgentTaskInputs task={{ ...task, status: "paused", additional_inputs: [{ input_id: "input", agent_task_id: "task", sequence: 1, user_id: "u", content: "Do not rewrite", status: "received", created_at: "2026-09-06T00:00:00Z" }] }} onAppend={onAppend} />);
  fireEvent.click(screen.getByText("追加要求 · 1"));
  expect(screen.getByText("已接收，待送入模型")).toBeTruthy();
  expect(screen.queryByText("已送入模型")).toBeNull();
  expect(screen.getByRole("note").textContent).toContain("先处理已批准的工具");
});

it("keeps the draft on failure and disables submission when the task finishes", async () => {
  const onAppend = vi.fn().mockRejectedValue(new Error("Task finished"));
  const view = render(<AgentTaskInputs task={task} onAppend={onAppend} />);
  fireEvent.click(screen.getByRole("button", { name: "追加后台任务要求" }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Unsent draft" } });
  fireEvent.click(screen.getByRole("button", { name: "提交追加要求" }));
  await screen.findByText("Task finished");
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("Unsent draft");
  view.rerender(<AgentTaskInputs task={{ ...task, status: "completed" }} onAppend={onAppend} />);
  expect((screen.getByRole("button", { name: "提交追加要求" }) as HTMLButtonElement).disabled).toBe(true);
});

it("does not offer writes without a callback and labels unconfirmed terminal input accurately", () => {
  render(<AgentTaskInputs task={{ ...task, status: "completed", additional_inputs: [{ input_id: "late", agent_task_id: "task", sequence: 1, user_id: "u", content: "Too late", status: "received", created_at: "2026-09-06T00:00:00Z" }] }} />);
  fireEvent.click(screen.getByText("追加要求 · 1"));
  expect(screen.getByText("未确认送入模型")).toBeTruthy();
  expect(screen.queryByRole("button")).toBeNull();
});

it("preserves an open draft but disables submission when write permission is revoked", () => {
  const onAppend = vi.fn();
  const view = render(<AgentTaskInputs task={task} onAppend={onAppend} />);
  fireEvent.click(screen.getByRole("button", { name: "追加后台任务要求" }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Unsent requirement" } });
  view.rerender(<AgentTaskInputs task={task} />);
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("Unsent requirement");
  expect(screen.getByRole("alert").textContent).toContain("当前已无追加权限");
  const submit = screen.getByRole("button", { name: "提交追加要求" });
  expect((submit as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(submit);
  expect(onAppend).not.toHaveBeenCalled();
});

it("retries a lost input receipt with the same idempotency key", async () => {
  const mock = vi.spyOn(globalThis, "fetch").mockRejectedValueOnce(new Error("Lost response")).mockResolvedValueOnce(new Response(JSON.stringify({ data: { input_id: "input" } }), { status: 202 }));
  await api.appendAgentTaskInput("task", "New requirement");
  expect(mock).toHaveBeenCalledTimes(2);
  const [url, init] = mock.mock.calls[0];
  expect(url).toBe("/api/v1/agent-tasks/task/inputs");
  expect(JSON.parse(init?.body as string)).toEqual({ content: "New requirement" });
  expect(new Headers(init?.headers).get("Idempotency-Key")).toBe(new Headers(mock.mock.calls[1][1]?.headers).get("Idempotency-Key"));
});

it("keeps the submission key across manual retries after both response attempts are lost", async () => {
  const mock = vi.spyOn(globalThis, "fetch").mockRejectedValueOnce(new Error("Lost response")).mockRejectedValueOnce(new Error("Lost response again"))
    .mockImplementation(async () => new Response(JSON.stringify({ data: { input_id: "input" } }), { status: 202 }));
  const onAppend = async (value: AgentTask, content: string, key: string) => { await api.appendAgentTaskInput(value.agent_task_id, content, key); };
  render(<AgentTaskInputs task={task} onAppend={onAppend} />);
  fireEvent.click(screen.getByRole("button", { name: "追加后台任务要求" }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Same requirement" } });
  fireEvent.click(screen.getByRole("button", { name: "提交追加要求" }));
  await screen.findByText("Lost response");
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "提交追加要求" })); });
  expect(screen.queryByRole("textbox")).toBeNull();
  const keys = mock.mock.calls.map(([, init]) => new Headers(init?.headers).get("Idempotency-Key"));
  expect(keys).toHaveLength(3);
  expect(new Set(keys).size).toBe(1);
  expect(keys[0]).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "追加后台任务要求" }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Same requirement" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "提交追加要求" })); });
  expect(new Headers(mock.mock.calls[3][1]?.headers).get("Idempotency-Key")).not.toBe(keys[0]);
});

it("uses a new submission key when the user changes an unconfirmed requirement", async () => {
  const onAppend = vi.fn().mockRejectedValue(new Error("Unconfirmed"));
  render(<AgentTaskInputs task={task} onAppend={onAppend} />);
  fireEvent.click(screen.getByRole("button", { name: "追加后台任务要求" }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "First requirement" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "提交追加要求" })); });
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Another requirement" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "提交追加要求" })); });
  expect(onAppend).toHaveBeenCalledTimes(2);
  expect(onAppend.mock.calls[1][2]).not.toBe(onAppend.mock.calls[0][2]);
});
