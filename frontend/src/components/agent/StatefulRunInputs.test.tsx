// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../../api";
import type { ExecutionInput, ExecutionInputsView, RunSnapshot } from "../../types";
import { StatefulRunInputs } from "./StatefulRunInputs";

const snapshot: RunSnapshot = {
  run: { run_id: "run", capability_id: "skill", status: "running", current_step_run_id: "step" },
  task_items: ["first", "second"].map((id, index) => ({ task_item_id: id, step_run_id: "step", item_key: id, item_order: index, status: "running", attempt_count: 1, current_attempt_id: id, failure: null })),
  available_actions: [], current_approval: null,
};
const input: ExecutionInput = { input_id: "input", attempt_id: "first", user_id: "user", sequence: 1, content: "Keep this requirement", content_hash: "hash", status: "received", created_at: "2026-09-07T00:00:00Z" };
const receipt = (attempt = "first", overrides: Partial<ExecutionInputsView> = {}): ExecutionInputsView => ({ attempt_id: attempt, run_id: "run", task_item_id: attempt, status: "running", can_append: true, inputs: [], ...overrides });
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

async function openEditor() {
  fireEvent.click(screen.getByRole("button", { name: "任务追加要求" }));
  fireEvent.click(await screen.findByRole("button", { name: "追加当前处理项要求" }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: input.content } });
}

it("loads only the selected attempt on demand and preserves its draft and retry key", async () => {
  const get = vi.spyOn(api, "getExecutionInputs").mockImplementation(async (_project, attempt) => receipt(attempt));
  const append = vi.spyOn(api, "appendExecutionInput").mockRejectedValueOnce(new Error("Lost receipt")).mockResolvedValue(input);
  render(<StatefulRunInputs projectID="project" snapshot={snapshot} />);
  expect(get).not.toHaveBeenCalled();
  await openEditor();
  expect(get.mock.calls.every((call) => call[1] === "first")).toBe(true);
  expect((screen.getByRole("combobox") as HTMLSelectElement).disabled).toBe(true);
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "提交追加要求" })); });
  expect(screen.getByRole("alert").textContent).toBe("Lost receipt");
  fireEvent.click(screen.getByRole("button", { name: "收起任务追加要求" }));
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "任务追加要求" })); });
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe(input.content);
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "提交追加要求" })); });
  expect(append.mock.calls[0]).toEqual(["project", "first", input.content, expect.any(String)]);
  expect(append.mock.calls[1]).toEqual(append.mock.calls[0]);
  expect((screen.getByRole("combobox") as HTMLSelectElement).disabled).toBe(false);
});

it("does not retarget a draft when a new attempt replaces the original", async () => {
  vi.spyOn(api, "getExecutionInputs").mockResolvedValue(receipt());
  const append = vi.spyOn(api, "appendExecutionInput").mockResolvedValue(input);
  const ui = render(<StatefulRunInputs projectID="project" snapshot={snapshot} />);
  await openEditor();
  ui.rerender(<StatefulRunInputs projectID="project" snapshot={{ ...snapshot, task_items: snapshot.task_items!.map((task) => ({ ...task, current_attempt_id: `${task.task_item_id}-retry` })) }} />);
  await waitFor(() => expect((screen.getByRole("button", { name: "提交追加要求" }) as HTMLButtonElement).disabled).toBe(false));
  expect((screen.getByRole("combobox") as HTMLSelectElement).value).toBe("first");
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "提交追加要求" })); });
  expect(append.mock.calls[0][1]).toBe("first");
});

it.each(["completed", "revoked", "unavailable"])("preserves the draft but prevents writes when %s", async (state) => {
  const get = vi.spyOn(api, "getExecutionInputs").mockResolvedValue(receipt());
  const append = vi.spyOn(api, "appendExecutionInput");
  const ui = render(<StatefulRunInputs projectID="project" snapshot={snapshot} />);
  await openEditor();
  if (state === "unavailable") get.mockRejectedValue(new Error("Permission lookup failed"));
  else get.mockResolvedValue(receipt("first", { status: state === "completed" ? "succeeded" : "running", can_append: false }));
  await act(async () => { ui.rerender(<StatefulRunInputs projectID="project" snapshot={{ ...snapshot }} />); });
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe(input.content);
  expect((screen.getByRole("button", { name: "提交追加要求" }) as HTMLButtonElement).disabled).toBe(true);
  expect(append).not.toHaveBeenCalled();
});

it("shows consumed and late input receipts without inventing model consumption", async () => {
  vi.spyOn(api, "getExecutionInputs").mockResolvedValue(receipt("first", { status: "succeeded", can_append: false, inputs: [input, { ...input, input_id: "consumed", sequence: 2, status: "included" }] }));
  render(<StatefulRunInputs projectID="project" snapshot={snapshot} />);
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "任务追加要求" })); });
  fireEvent.click(screen.getByText("追加要求 · 2"));
  expect(screen.getByText("未确认送入模型")).toBeTruthy();
  expect(screen.getByText("已送入模型")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "追加当前处理项要求" })).toBeNull();
});

it("ignores a late response for the previous selection", async () => {
  let finish: (view: ExecutionInputsView) => void = () => {};
  const get = vi.spyOn(api, "getExecutionInputs").mockImplementation((_project, attempt) => attempt === "first" ? new Promise((resolve) => { finish = resolve; }) : Promise.resolve(receipt("second")));
  render(<StatefulRunInputs projectID="project" snapshot={snapshot} />);
  fireEvent.click(screen.getByRole("button", { name: "任务追加要求" }));
  await act(async () => { fireEvent.change(screen.getByRole("combobox"), { target: { value: "second" } }); });
  await act(async () => { finish(receipt()); });
  expect((screen.getByRole("combobox") as HTMLSelectElement).value).toBe("second");
  expect(get.mock.calls[0][2]?.aborted).toBe(true);
});

it("escapes scope identifiers and retains the same idempotency key in HTTP retries", async () => {
  const fetch = vi.spyOn(globalThis, "fetch").mockRejectedValueOnce(new TypeError("Lost response")).mockResolvedValueOnce(new Response(JSON.stringify({ data: input }), { status: 202 }));
  await api.appendExecutionInput("project/id", "attempt/id", input.content, "same-key");
  expect(fetch).toHaveBeenCalledTimes(2);
  for (const [url, options] of fetch.mock.calls) {
    expect(url).toBe("/api/v1/projects/project%2Fid/execution-attempts/attempt%2Fid/inputs");
    expect(options?.body).toBe(JSON.stringify({ content: input.content }));
    expect(new Headers(options?.headers).get("Idempotency-Key")).toBe("same-key");
  }
});

it("finds previous-attempt inputs after a clean page mount and does not retarget a dirty draft", async () => {
  const get = vi.spyOn(api, "getExecutionInputs").mockImplementation(async (_project, attempt) => receipt(attempt, attempt === "old" ? { status: "failed", can_append: false, inputs: [{ ...input, attempt_id: "old", status: "included" }] } : {}));
  const list = vi.spyOn(api, "listExecutionInputAttempts").mockResolvedValue({ items: [{ attempt_id: "old", task_item_id: "first", item_key: "episode:1", attempt_no: 1, status: "failed", input_count: 1, included_count: 1, current: false }], next_cursor: "" });
  render(<StatefulRunInputs projectID="project" snapshot={snapshot} />);
  expect(list).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "历史追加记录", exact: true }));
  fireEvent.click(await screen.findByRole("button", { name: /episode:1 · 第 1 次尝试/ }));
  await screen.findByText("追加要求 · 1");
  fireEvent.click(screen.getByText("追加要求 · 1"));
  expect(screen.getByText("已送入模型")).toBeTruthy();
  expect(screen.getByText(input.content)).toBeTruthy();
  expect((screen.getByRole("combobox") as HTMLSelectElement).value).toBe("old");
  expect(screen.queryByRole("button", { name: "追加当前处理项要求" })).toBeNull();
  await act(async () => fireEvent.change(screen.getByRole("combobox"), { target: { value: "first" } }));
  fireEvent.click(await screen.findByRole("button", { name: "追加当前处理项要求" }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Pending draft" } });
  fireEvent.click(screen.getByRole("button", { name: "历史追加记录", exact: true }));
  expect((await screen.findByRole("button", { name: /episode:1 · 第 1 次尝试/ }) as HTMLButtonElement).disabled).toBe(true);
  expect(get.mock.calls.some((call) => call[1] === "old")).toBe(true);
});
