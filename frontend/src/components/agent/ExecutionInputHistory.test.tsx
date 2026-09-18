// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../../api";
import type { ExecutionInputAttemptPage } from "../../types";
import { ExecutionInputHistory } from "./ExecutionInputHistory";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const item = { attempt_id: "one", task_item_id: "task", item_key: "episode:1", attempt_no: 1, status: "failed", input_count: 2, included_count: 1, current: false };
it("paginates, preserves rows on failure and reuses the cursor", async () => {
  const apiCall = vi.spyOn(api, "listExecutionInputAttempts").mockResolvedValueOnce({ items: [item], next_cursor: "one" }).mockRejectedValueOnce(new Error("Offline")).mockResolvedValueOnce({ items: [item, { ...item, attempt_id: "two", attempt_no: 2 }], next_cursor: "" });
  const select = vi.fn();
  render(<ExecutionInputHistory projectID="p" runID="r" disabled={false} onSelect={select} />);
  fireEvent.click(screen.getByRole("button", { name: "历史追加记录", exact: true }));
  await screen.findByText("episode:1 · 第 1 次尝试");
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "更多历史追加记录" })));
  expect(screen.getByRole("alert").textContent).toBe("Offline");
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "更多历史追加记录" })));
  expect(apiCall.mock.calls[1].slice(0,3)).toEqual(["p","r","one"]);
  expect(apiCall.mock.calls[2].slice(0,3)).toEqual(["p","r","one"]);
  expect(screen.getAllByText("episode:1 · 第 1 次尝试")).toHaveLength(1);
  fireEvent.click(screen.getByRole("button", { name: /第 2 次尝试/ }));
  expect(select).toHaveBeenCalledWith(expect.objectContaining({ attempt_id: "two" }));
});
it("aborts and ignores a delayed page after unmount", async () => {
  let finish: (page: ExecutionInputAttemptPage) => void = () => {};
  const apiCall = vi.spyOn(api, "listExecutionInputAttempts").mockImplementation(() => new Promise((resolve) => { finish=resolve; }));
  const ui = render(<ExecutionInputHistory projectID="p" runID="r" disabled={false} onSelect={vi.fn()} />);
  fireEvent.click(screen.getByRole("button", { name: "历史追加记录", exact: true }));
  ui.unmount();
  expect(apiCall.mock.calls[0][3]?.aborted).toBe(true);
  await act(async () => finish({ items:[item], next_cursor:"" }));
});
