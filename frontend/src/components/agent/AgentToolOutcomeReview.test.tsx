// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../../api";
import type { AgentToolOutcomeReview as Review } from "../../types";
import { AgentToolOutcomeReview } from "./AgentToolOutcomeReview";

vi.mock("../../api", () => ({ api: { getAgentToolOutcomeReview: vi.fn(), resolveAgentToolOutcome: vi.fn() } }));
afterEach(() => { cleanup(); vi.resetAllMocks(); });
const review: Review = { agent_tool_call_id: "call", project_id: "p", sdk_tool_call_id: "native", tool_id: "mcp:service/write", arguments_hash: "args", configuration_hash: "config", user_id: "owner", execution_mode: "conversation", execution_id: "turn", execution_status: "paused", subject_snapshot_hash: "snapshot", can_resolve: true };
function open() { const details = screen.getByText("外部操作结果核对").closest("details")!; details.open = true; fireEvent(details, new Event("toggle")); }
async function form() { vi.mocked(api.getAgentToolOutcomeReview).mockResolvedValue(review); const result = render(<AgentToolOutcomeReview callID="call" projectID="p" />); open(); await screen.findByLabelText("操作已发生"); return result; }
function fill(evidence = "Checked remote record") { fireEvent.click(screen.getByLabelText("操作已发生")); fireEvent.change(screen.getByLabelText("核对依据"), { target: { value: evidence } }); fireEvent.click(screen.getByLabelText("已核对外部服务中的原操作")); }

it("requires an explicit outcome, evidence and acknowledgement before saving", async () => {
  await form();
  expect((screen.getByRole("button", { name: "保存核对结果" }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByLabelText("操作已发生") as HTMLInputElement).checked).toBe(false);
  vi.mocked(api.resolveAgentToolOutcome).mockImplementation(async (_id, command) => ({ ...review, can_resolve: false, resolution: { ...command, actor_user_id: "owner", created_at: "2026-09-08T00:00:00Z" } }));
  fill(); fireEvent.click(screen.getByRole("button", { name: "保存核对结果" }));
  expect(await screen.findByText("用户确认：操作已发生")).toBeTruthy();
  expect(api.resolveAgentToolOutcome).toHaveBeenCalledWith("call", expect.objectContaining({ subject_snapshot_hash: "snapshot", outcome: "applied", evidence: "Checked remote record" }), expect.any(AbortSignal));
  expect(screen.queryByLabelText("核对依据")).toBeNull();
});

it("preserves the same idempotency request after a lost acknowledgement", async () => {
  await form(); fill();
  vi.mocked(api.resolveAgentToolOutcome).mockRejectedValue(new Error("Connection lost"));
  fireEvent.click(screen.getByRole("button", { name: "保存核对结果" }));
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", "Connection lost");
  const first = vi.mocked(api.resolveAgentToolOutcome).mock.calls[0][1];
  fireEvent.click(screen.getByRole("button", { name: "保存核对结果" }));
  await waitFor(() => expect(api.resolveAgentToolOutcome).toHaveBeenCalledTimes(2));
  expect(vi.mocked(api.resolveAgentToolOutcome).mock.calls[1][1]).toEqual(first);
  await screen.findByRole("alert");
  fireEvent.change(screen.getByLabelText("核对依据"), { target: { value: "New evidence" } });
  fireEvent.click(screen.getByRole("button", { name: "保存核对结果" }));
  await waitFor(() => expect(api.resolveAgentToolOutcome).toHaveBeenCalledTimes(3));
  expect(vi.mocked(api.resolveAgentToolOutcome).mock.calls[2][1].request_id).not.toBe(first.request_id);
});

it.each(["outcome", "evidence", "actor", "tool", "execution", "arguments"])("rejects a mismatched %s in the saved resolution and retries the original request", async (field) => {
  await form(); fill();
  vi.mocked(api.resolveAgentToolOutcome).mockImplementation(async (_id, command) => {
    const result: Review = { ...review, can_resolve: false, resolution: { ...command, actor_user_id: "owner", created_at: "2026-09-08T00:00:00Z" } };
    if (field === "outcome") result.resolution!.outcome = "not_applied";
    if (field === "evidence") result.resolution!.evidence = "Different evidence";
    if (field === "actor") result.resolution!.actor_user_id = "another-user";
    if (field === "tool") result.tool_id = "mcp:another/write";
    if (field === "execution") result.execution_id = "another-turn";
    if (field === "arguments") result.arguments_hash = "other-arguments";
    return result;
  });
  fireEvent.click(screen.getByRole("button", { name: "保存核对结果" }));
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", "核对回执与本次提交不一致。");
  expect(screen.queryByText("用户确认：操作未发生")).toBeNull();
  expect((screen.getByLabelText("核对依据") as HTMLTextAreaElement).value).toBe("Checked remote record");
  const original = vi.mocked(api.resolveAgentToolOutcome).mock.calls[0][1];
  vi.mocked(api.resolveAgentToolOutcome).mockImplementation(async (_id, command) => ({ ...review, can_resolve: false, resolution: { ...command, actor_user_id: "owner", created_at: "2026-09-08T00:00:00Z" } }));
  fireEvent.click(screen.getByRole("button", { name: "保存核对结果" }));
  expect(await screen.findByText("用户确认：操作已发生")).toBeTruthy();
  expect(vi.mocked(api.resolveAgentToolOutcome).mock.calls[1][1]).toEqual(original);
});

it("renders recorded evidence as text and does not allow another resolution", async () => {
  vi.mocked(api.getAgentToolOutcomeReview).mockResolvedValue({ ...review, can_resolve: false, resolution: { request_id: "saved", outcome: "not_applied", evidence: "<script>user evidence</script>", actor_user_id: "owner", created_at: "2026-09-08T00:00:00Z" } });
  const { container } = render(<AgentToolOutcomeReview callID="call" projectID="p" />); open();
  expect(await screen.findByText("用户确认：操作未发生")).toBeTruthy();
  expect(container.querySelector("script")).toBeNull();
  expect(screen.queryByLabelText("核对依据")).toBeNull();
});

it("does not expose the form when the server denies reconciliation", async () => {
  vi.mocked(api.getAgentToolOutcomeReview).mockResolvedValue({ ...review, can_resolve: false });
  render(<AgentToolOutcomeReview callID="call" projectID="p" />); open();
  expect(await screen.findByText("等待执行暂停或结束后，由原发起人核对。")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "保存核对结果" })).toBeNull();
});

it("discards an in-flight save and its draft when the selected call changes", async () => {
  const { rerender } = await form(); fill();
  let done!: (value: Review) => void;
  vi.mocked(api.resolveAgentToolOutcome).mockImplementation(() => new Promise((resolve) => { done = resolve; }));
  fireEvent.click(screen.getByRole("button", { name: "保存核对结果" }));
  await waitFor(() => expect(api.resolveAgentToolOutcome).toHaveBeenCalledTimes(1));
  const [_id, command, signal] = vi.mocked(api.resolveAgentToolOutcome).mock.calls[0];
  vi.mocked(api.getAgentToolOutcomeReview).mockResolvedValue({ ...review, agent_tool_call_id: "next" });
  rerender(<AgentToolOutcomeReview callID="next" projectID="p" />);
  await screen.findByLabelText("核对依据");
  await act(async () => done({ ...review, can_resolve: false, resolution: { ...command, actor_user_id: "owner", created_at: "2026-09-08T00:00:00Z" } }));
  expect(signal?.aborted).toBe(true);
  expect((screen.getByLabelText("核对依据") as HTMLTextAreaElement).value).toBe("");
  expect(screen.queryByText("用户确认：操作已发生")).toBeNull();
});

it("rejects a mismatched read identity and a changed save snapshot", async () => {
  await form(); fill();
  vi.mocked(api.resolveAgentToolOutcome).mockResolvedValue({ ...review, subject_snapshot_hash: "other" });
  fireEvent.click(screen.getByRole("button", { name: "保存核对结果" }));
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", "核对回执与本次提交不一致。");
  vi.mocked(api.getAgentToolOutcomeReview).mockResolvedValue({ ...review, project_id: "other" });
  fireEvent.click(screen.getByRole("button", { name: "刷新核对记录" }));
  expect(await screen.findByRole("alert")).toHaveProperty("textContent", "核对记录与当前操作不一致。");
});
