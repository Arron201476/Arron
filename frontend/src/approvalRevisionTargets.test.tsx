// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ApprovalCard } from "./App";
import { api, ApiError } from "./api";
import type { Approval, ApprovalRevisionTargets, RevisionRequest } from "./types";

const approval: Approval = { approval_request_id: "approval", project_id: "project", run_id: "run", scope: "artifact", status: "pending", version: 2,
  title: "Confirm reports", reason: "Review both outputs", options: ["approve", "request_ai_revision"],
  subject_kind: "artifact_version_set", subject_ref_id: "step", subject_version: 2, subject_snapshot_hash: "bound-versions", requested_at: "2026-09-10T00:00:00Z" };
const targets = (): ApprovalRevisionTargets => ({ approval: { ...approval }, targets: [
  { artifact_id: "summary", artifact_version_id: "summary-v1", artifact_type: "generic_document", scope_key: "singleton", version: 1, label: "Overview - v1" },
  { artifact_id: "notes", artifact_version_id: "notes-v2", artifact_type: "review_notes", scope_key: "singleton", version: 2, label: "Review notes - v2" },
] });
const props = () => ({ approval, viewKey: "action_list" as const, blockingRevision: null, onResolve: vi.fn(async () => {}) });
beforeEach(() => { vi.spyOn(api, "getApprovalRevisionTargets").mockResolvedValue(targets()); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
async function chooseTarget() {
  fireEvent.click(screen.getByRole("button", { name: "让 Agent 修改" }));
  const select = await screen.findByRole("combobox", { name: "修改对象" });
  fireEvent.change(select, { target: { value: "notes-v2" } });
  fireEvent.change(screen.getByRole("textbox", { name: "修改要求" }), { target: { value: "Strengthen the final hook" } });
  return select;
}

it("requires explicit selection from the bound set and submits the exact output version", async () => {
  const input = props(); render(<ApprovalCard {...input} />);
  expect(api.getApprovalRevisionTargets).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "让 Agent 修改" }));
  const select = await screen.findByRole("combobox", { name: "修改对象" });
  expect(select).toHaveProperty("value", "");
  expect(screen.getByRole("option", { name: "Review notes - v2" })).toBeTruthy();
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Improve the ending" } });
  expect(screen.getByRole("button", { name: "提交修改要求" })).toHaveProperty("disabled", true);
  fireEvent.change(select, { target: { value: "notes-v2" } });
  fireEvent.click(screen.getByRole("button", { name: "提交修改要求" }));
  await waitFor(() => expect(input.onResolve).toHaveBeenCalledExactlyOnceWith(approval, "request_ai_revision", "Improve the ending", { target_artifact_version_id: "notes-v2" }));
});

it("automatically binds the sole editable output in a version set", async () => {
  vi.mocked(api.getApprovalRevisionTargets).mockResolvedValue({ ...targets(), targets: targets().targets.slice(0, 1) });
  render(<ApprovalCard {...props()} />);
  fireEvent.click(screen.getByRole("button", { name: "让 Agent 修改" }));
  expect(await screen.findByRole("combobox", { name: "修改对象" })).toHaveProperty("value", "summary-v1");
});

it.each(["wrong_approval", "wrong_project", "wrong_version", "wrong_hash", "empty", "duplicate", "invalid_version", "invalid_label", "invalid_id", "invalid_type", "invalid_scope"])("blocks malformed or stale target response: %s", async (fault) => {
  const response = targets();
  if (fault === "wrong_approval") response.approval.approval_request_id = "foreign";
  if (fault === "wrong_project") response.approval.project_id = "foreign";
  if (fault === "wrong_version") response.approval.version++;
  if (fault === "wrong_hash") response.approval.subject_snapshot_hash = "new";
  if (fault === "empty") response.targets = [];
  if (fault === "duplicate") response.targets.push(response.targets[0]);
  if (fault === "invalid_version") response.targets[0].version = 0;
  if (fault === "invalid_label") response.targets[0].label = "";
  if (fault === "invalid_id") Object.assign(response.targets[0], { artifact_version_id: 42 });
  if (fault === "invalid_type") Object.assign(response.targets[0], { artifact_type: {} });
  if (fault === "invalid_scope") Object.assign(response.targets[0], { scope_key: null });
  vi.mocked(api.getApprovalRevisionTargets).mockResolvedValue(response);
  const input = props(); render(<ApprovalCard {...input} />);
  fireEvent.click(screen.getByRole("button", { name: "让 Agent 修改" }));
  await screen.findByRole("alert");
  expect(screen.queryByRole("combobox")).toBeNull();
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Revision instruction" } });
  expect(screen.getByRole("button", { name: "提交修改要求" })).toHaveProperty("disabled", true);
  expect(input.onResolve).not.toHaveBeenCalled();
});

it("retries target reading without mutation and preserves the typed instruction", async () => {
  vi.mocked(api.getApprovalRevisionTargets).mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Target read failed", 503));
  const input = props(); render(<ApprovalCard {...input} />);
  fireEvent.click(screen.getByRole("button", { name: "让 Agent 修改" }));
  await screen.findByText("Target read failed");
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Keep this draft" } });
  fireEvent.click(screen.getByRole("button", { name: "重试读取修改对象" }));
  await screen.findByRole("combobox");
  expect(screen.getByRole("textbox")).toHaveProperty("value", "Keep this draft");
  expect(input.onResolve).not.toHaveBeenCalled();
});

it.each([503, 408])("keeps the exact target and instruction locked after uncertain HTTP %d", async (status) => {
  const input = props();
  input.onResolve.mockRejectedValueOnce(new ApiError("UNKNOWN", "Outcome unknown", status)).mockResolvedValue(undefined);
  render(<ApprovalCard {...input} />); await chooseTarget();
  fireEvent.click(screen.getByRole("button", { name: "提交修改要求" }));
  await screen.findByText("Outcome unknown");
  expect(screen.getByRole("combobox")).toHaveProperty("disabled", true);
  expect(screen.getByRole("textbox")).toHaveProperty("disabled", true);
  expect(screen.getByRole("button", { name: "取消" })).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "提交修改要求" }));
  await waitFor(() => expect(input.onResolve).toHaveBeenCalledTimes(2));
  expect(input.onResolve.mock.calls[1]).toEqual(input.onResolve.mock.calls[0]);
});

it("keeps selection across same-version refresh but rejects late reads for a replaced approval", async () => {
  const input = props(); const view = render(<ApprovalCard {...input} />);
  await chooseTarget();
  view.rerender(<ApprovalCard {...input} approval={{ ...approval }} />);
  await act(async () => {});
  expect(api.getApprovalRevisionTargets).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("combobox")).toHaveProperty("value", "notes-v2");
  let complete!: (value: ApprovalRevisionTargets) => void;
  vi.mocked(api.getApprovalRevisionTargets).mockImplementationOnce(() => new Promise((resolve) => { complete = resolve; }));
  fireEvent.click(screen.getByRole("button", { name: "取消" }));
  fireEvent.click(screen.getByRole("button", { name: "让 Agent 修改" }));
  view.rerender(<ApprovalCard {...input} approval={{ ...approval, version: 3, subject_snapshot_hash: "new" }} />);
  await act(async () => complete(targets()));
  expect(screen.queryByRole("combobox")).toBeNull();
  expect(screen.queryByRole("textbox")).toBeNull();
});

it("preserves an unknown request across temporary loss of write access", async () => {
  const input = props();
  input.onResolve.mockRejectedValueOnce(new ApiError("UNKNOWN", "Outcome unknown", 503)).mockResolvedValue(undefined);
  const view = render(<ApprovalCard {...input} />); await chooseTarget();
  fireEvent.click(screen.getByRole("button", { name: "提交修改要求" }));
  await screen.findByText("Outcome unknown");
  view.rerender(<ApprovalCard {...input} readOnly />);
  view.rerender(<ApprovalCard {...input} />);
  expect(api.getApprovalRevisionTargets).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("combobox")).toHaveProperty("value", "notes-v2");
  expect(screen.getByRole("combobox")).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "提交修改要求" }));
  await waitFor(() => expect(input.onResolve).toHaveBeenCalledTimes(2));
  expect(input.onResolve.mock.calls[1]).toEqual(input.onResolve.mock.calls[0]);
});

it("honors revoked write access and an active revision before submission", async () => {
  const input = props(); const view = render(<ApprovalCard {...input} />); await chooseTarget();
  view.rerender(<ApprovalCard {...input} blockingRevision={{ instruction: "Other edit", status: "queued" } as RevisionRequest} />);
  expect(screen.getByRole("button", { name: "提交修改要求" })).toHaveProperty("disabled", true);
  view.rerender(<ApprovalCard {...input} readOnly />);
  expect(screen.queryByRole("combobox")).toBeNull();
  expect(input.onResolve).not.toHaveBeenCalled();
});
