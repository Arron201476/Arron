// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../../api";
import { MemoryGenerations, type MemoryGenerationSummary } from "./MemoryGenerations";

const item: MemoryGenerationSummary = { generation_id: "g", project_id: "p", user_id: "u", status: "paused", phase: "consolidation", revision: 2, attempt: 1, model_id: "model", created_at: "2026-09-14T00:00:00Z", resume_available: true };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it("shows a cooperative pause request without claiming it is already paused", async () => {
  const running = { ...item, status: "running" as const, resume_available: false };
  const requested = { ...running, revision: 3, error_code: "MEMORY_GENERATION_PAUSE_REQUESTED" };
  vi.spyOn(api, "listMemoryGenerations").mockResolvedValueOnce({ items: [running] }).mockResolvedValue({ items: [requested] });
  vi.spyOn(api, "controlMemoryGeneration").mockResolvedValue(requested);
  render(<MemoryGenerations projectID="p" userID="u" editable />);
  fireEvent.click(await screen.findByRole("button", { name: "暂停生成任务" }));
  await screen.findByText("整合 · 正在暂停");
  expect(screen.getByRole("button", { name: "暂停生成任务" })).toHaveProperty("disabled", true);
  expect(screen.getByRole("button", { name: "取消生成任务" })).toHaveProperty("disabled", false);
  expect(screen.queryByRole("alert")).toBeNull();
});
it.each([false, undefined])("does not offer an unconfirmed resume when availability is %s", async (available) => {
  vi.spyOn(api, "listMemoryGenerations").mockResolvedValue({ items: [{ ...item, resume_available: available }] });
  const control = vi.spyOn(api, "controlMemoryGeneration");
  render(<MemoryGenerations projectID="p" userID="u" editable />);
  const button = await screen.findByRole("button", { name: "恢复生成任务" });
  expect(button).toHaveProperty("disabled", true);
  fireEvent.click(button);
  expect(control).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "取消生成任务" })).toHaveProperty("disabled", false);
  expect(screen.getByText(available === false ? "缺少恢复检查点" : "恢复条件未确认")).toBeTruthy();
});
it("loads private task states and advances pages", async () => {
  const load = vi.spyOn(api, "listMemoryGenerations").mockResolvedValueOnce({ items: [item], next_cursor: "g" })
    .mockResolvedValueOnce({ items: [{ ...item, generation_id: "older", status: "completed" }] });
  render(<MemoryGenerations projectID="p" userID="u" />);
  await screen.findByText("整合 · 已暂停");
  fireEvent.click(screen.getByRole("button", { name: "加载更多" }));
  await screen.findByText("整合 · 已完成");
  expect(load.mock.calls[1][1]).toBe("g");
  expect(screen.queryByRole("button", { name: "加载更多" })).toBeNull();
});
it("rejects another owner's response without displaying its model", async () => {
  vi.spyOn(api, "listMemoryGenerations").mockResolvedValue({ items: [{ ...item, user_id: "other", model_id: "PRIVATE_MODEL" }] });
  render(<MemoryGenerations projectID="p" userID="u" />);
  await screen.findByRole("alert");
  expect(screen.queryByText(/PRIVATE_MODEL/)).toBeNull();
});
it("refreshes after an error and aborts on unmount", async () => {
  const load = vi.spyOn(api, "listMemoryGenerations").mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce({ items: [] });
  const view = render(<MemoryGenerations projectID="p" userID="u" />);
  await screen.findByText("offline");
  await waitFor(() => expect(screen.getByRole("button", { name: "刷新生成任务" })).toHaveProperty("disabled", false));
  fireEvent.click(screen.getByRole("button", { name: "刷新生成任务" }));
  await screen.findByText("暂无生成任务");
  const signal = load.mock.calls[1][2];
  view.unmount();
  expect(signal?.aborted).toBe(true);
});

it("resumes with the observed revision and reloads confirmed state", async () => {
  const load = vi.spyOn(api, "listMemoryGenerations").mockResolvedValueOnce({ items: [item] })
    .mockResolvedValue({ items: [{ ...item, status: "queued", revision: 3 }] });
  const control = vi.spyOn(api, "controlMemoryGeneration").mockResolvedValue({ ...item, status: "queued", revision: 3 });
  render(<MemoryGenerations projectID="p" userID="u" editable />);
  fireEvent.click(await screen.findByRole("button", { name: "恢复生成任务" }));
  await screen.findByText("整合 · 排队中");
  expect(control).toHaveBeenCalledWith("p", "g", "resume", 2);
  expect(load).toHaveBeenCalledTimes(2);
});

it("locks controls on an unknown outcome until an authoritative refresh", async () => {
  vi.spyOn(api, "listMemoryGenerations").mockResolvedValue({ items: [item] });
  const control = vi.spyOn(api, "controlMemoryGeneration").mockRejectedValue(new Error("outcome unknown"));
  render(<MemoryGenerations projectID="p" userID="u" editable />);
  fireEvent.click(await screen.findByRole("button", { name: "恢复生成任务" }));
  await screen.findByText("outcome unknown");
  expect(screen.getByRole("button", { name: "恢复生成任务" })).toHaveProperty("disabled", true);
  expect(screen.getByRole("button", { name: "取消生成任务" })).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "刷新生成任务" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "恢复生成任务" })).toHaveProperty("disabled", false));
  expect(control).toHaveBeenCalledTimes(1);
});

it("does not expose mutations to viewers", async () => {
  vi.spyOn(api, "listMemoryGenerations").mockResolvedValue({ items: [item] });
  render(<MemoryGenerations projectID="p" userID="u" />);
  await screen.findByText("整合 · 已暂停");
  expect(screen.queryByRole("button", { name: "恢复生成任务" })).toBeNull();
  expect(screen.queryByRole("button", { name: "取消生成任务" })).toBeNull();
});
