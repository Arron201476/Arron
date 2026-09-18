// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../../api";
import { MemorySourcePicker } from "./MemorySourcePicker";

const source = { activity_key: "turn:a", segment_id: "s", source_hash: "a".repeat(64) };
const page = { project_id: "p", user_id: "u", items: [source] };
const receipt = { ...source, project_id: "p", user_id: "u", generation_id: "g", revision: 1, status: "queued" };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
async function select() {
  await screen.findByRole("option", { name: "turn:a · s" });
  fireEvent.change(screen.getByRole("combobox", { name: "记忆生成来源" }), { target: { value: JSON.stringify(["turn:a", "s"]) } });
}
it("queues only the selected server reference and refreshes after confirmation", async () => {
  vi.spyOn(api, "listMemorySources").mockResolvedValueOnce(page).mockResolvedValue({ ...page, items: [{ ...source, generation_id: "g" }] });
  const queue = vi.spyOn(api, "queueMemoryGeneration").mockResolvedValue(receipt);
  const onQueued = vi.fn();
  render(<MemorySourcePicker projectID="p" userID="u" onQueued={onQueued} />);
  await select(); fireEvent.click(screen.getByRole("button", { name: "生成记忆" }));
  await waitFor(() => expect(onQueued).toHaveBeenCalledTimes(1));
  expect(queue).toHaveBeenCalledWith("p", source);
  expect(await screen.findByRole("option", { name: "turn:a · s · 已创建任务" })).toHaveProperty("disabled", true);
});
it("retains the exact source after a lost receipt", async () => {
  vi.spyOn(api, "listMemorySources").mockResolvedValue(page);
  const queue = vi.spyOn(api, "queueMemoryGeneration").mockRejectedValueOnce(new Error("lost reply")).mockResolvedValue(receipt);
  const done = vi.fn();
  render(<MemorySourcePicker projectID="p" userID="u" onQueued={done} />);
  await select(); fireEvent.click(screen.getByRole("button", { name: "生成记忆" }));
  await screen.findByText("lost reply");
  expect(screen.getByRole("combobox", { name: "记忆生成来源" })).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "重试原排队请求" }));
  await waitFor(() => expect(done).toHaveBeenCalledTimes(1));
  expect(queue.mock.calls[1]).toEqual(queue.mock.calls[0]);
});
it("rejects another user's source inventory", async () => {
  vi.spyOn(api, "listMemorySources").mockResolvedValue({ ...page, user_id: "other" });
  render(<MemorySourcePicker projectID="p" userID="u" onQueued={vi.fn()} />);
  await screen.findByRole("alert");
  expect(screen.queryByRole("option", { name: "turn:a · s" })).toBeNull();
  expect(screen.getByRole("button", { name: "生成记忆" })).toHaveProperty("disabled", true);
});
