// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../../api";
import type { AgentSubtaskResult as Result } from "../../types";
import { AgentSubtaskResult } from "./AgentSubtaskResult";

vi.mock("../../api", () => ({ api: { getAgentSubtaskResult: vi.fn() } }));
afterEach(() => { cleanup(); vi.resetAllMocks(); });
const result: Result = { schema_version: "agent_subtask.v1", agent_tool_call_id: "call", project_id: "p", text: "<script>not executable</script>\nComplete findings", read_call_ids: ["read"], inspected_artifacts: [{ artifact_id: "artifact", artifact_version_id: "version" }] };
function toggle(open: boolean) { const details = screen.getByText("子任务结果").closest("details")!; details.open = open; fireEvent(details, new Event("toggle")); }

it("loads the full durable result only when opened and renders untrusted text safely", async () => {
  vi.mocked(api.getAgentSubtaskResult).mockResolvedValue(result);
  const { container } = render(<AgentSubtaskResult callID="call" projectID="p" />);
  expect(api.getAgentSubtaskResult).not.toHaveBeenCalled();
  toggle(true);
  expect(await screen.findByText(/Complete findings/)).toBeTruthy();
  expect(screen.getByText("version")).toBeTruthy();
  expect(container.querySelector("script")).toBeNull();
  expect(api.getAgentSubtaskResult).toHaveBeenCalledWith("call", expect.any(AbortSignal));
});

it("reports unavailable results and supports retry without re-running a child", async () => {
  vi.mocked(api.getAgentSubtaskResult).mockRejectedValueOnce(new Error("Result unavailable")).mockResolvedValueOnce(result);
  render(<AgentSubtaskResult callID="call" projectID="p" />);
  toggle(true);
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", "Result unavailable");
  fireEvent.click(screen.getByRole("button", { name: "重试读取子任务结果" }));
  expect(await screen.findByText(/Complete findings/)).toBeTruthy();
  expect(api.getAgentSubtaskResult).toHaveBeenCalledTimes(2);
});

it("rejects a mismatched result identity", async () => {
  vi.mocked(api.getAgentSubtaskResult).mockResolvedValue({ ...result, project_id: "other" });
  render(<AgentSubtaskResult callID="call" projectID="p" />);
  toggle(true);
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", "子任务结果与当前执行不一致。");
  expect(screen.queryByText(/Complete findings/)).toBeNull();
});

it("ignores a late response after changing the selected execution", async () => {
  let resolve!: (value: Result) => void;
  vi.mocked(api.getAgentSubtaskResult).mockImplementationOnce(() => new Promise((done) => { resolve = done; })).mockResolvedValueOnce({ ...result, agent_tool_call_id: "next", text: "Next result" });
  const { rerender } = render(<AgentSubtaskResult callID="call" projectID="p" />);
  toggle(true);
  await waitFor(() => expect(api.getAgentSubtaskResult).toHaveBeenCalledTimes(1));
  const signal = vi.mocked(api.getAgentSubtaskResult).mock.calls[0][1]!;
  rerender(<AgentSubtaskResult callID="next" projectID="p" />);
  expect(await screen.findByText("Next result")).toBeTruthy();
  await act(async () => resolve(result));
  expect(signal.aborted).toBe(true);
  expect(screen.queryByText(/Complete findings/)).toBeNull();
});
