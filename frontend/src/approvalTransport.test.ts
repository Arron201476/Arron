import { afterEach, expect, it, vi } from "vitest";
import { api } from "./api";
import type { Approval, AvailableAction } from "./types";

afterEach(() => vi.restoreAllMocks());
const approval = { approval_request_id: "approval", subject_ref_id: "subject", subject_snapshot_hash: "hash", version: 3 } as Approval;
const key = "11111111-1111-4111-8111-111111111111";
const commands: [string, () => Promise<unknown>][] = [
  ["resolution", () => api.resolveApproval(approval, "select_single_option", { selected_option_id: "option_2", options_artifact_version_id: "subject" }, key)],
  ["regeneration", () => api.requestApprovalRegeneration(approval, "request_ai_revision", "Instruction", key)],
  ["targeted revision", () => api.requestApprovalRegeneration(approval, "request_ai_revision", "Instruction", key, "selected-version")],
  ["risk", () => api.acceptQualityReviewRisk(approval, key)],
  ["quality", () => api.resolveQualityReviewAction(approval, "ai_revise", "Instruction", key)],
  ["final", () => api.confirmFinalSelection("project", approval, "previous", key)],
  ["episode mode", () => api.setEpisodeExecutionMode("run", "continuous", key)],
  ...["pause_run", "resume_run", "cancel_run", "retry_failed_step", "continue_with_partial_results"].map((action_id): [string, () => Promise<unknown>] => [action_id, () => api.executeRunAction("run", { action_id, target_type: ["retry_failed_step", "continue_with_partial_results"].includes(action_id) ? "step_run" : "run", target_id: ["retry_failed_step", "continue_with_partial_results"].includes(action_id) ? "step" : "run", enabled: true, disabled_reason: null } as AvailableAction, key)]),
];
it.each(commands)("preserves the caller's %s request identity on retries", async (_name, invoke) => {
  const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response(JSON.stringify({ data: {} }), { status: 200, headers: { "Content-Type": "application/json" } }));
  await invoke(); await invoke();
  for (const call of fetch.mock.calls) expect(new Headers(call[1]?.headers).get("Idempotency-Key")).toBe(key);
  expect(fetch.mock.calls[0][0]).toBe(fetch.mock.calls[1][0]);
  expect(fetch.mock.calls[0][1]?.body).toBe(fetch.mock.calls[1][1]?.body);
});

it("reads approval-bound revision targets without a mutation and preserves the selected version in the request", async () => {
  const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response(JSON.stringify({ data: {} }), { status: 200, headers: { "Content-Type": "application/json" } }));
  await api.getApprovalRevisionTargets("approval");
  expect(String(fetch.mock.calls[0][0])).toBe("/api/v1/approvals/approval/revision-targets");
  expect(fetch.mock.calls[0][1]?.method ?? "GET").toBe("GET");
  await api.requestApprovalRegeneration(approval, "request_ai_revision", "Instruction", key, "selected-version");
  expect(JSON.parse(String(fetch.mock.calls[1][1]?.body))).toEqual({ action: "request_ai_revision", instruction: "Instruction", expected_approval_version: 3, subject_snapshot_hash: "hash", target_artifact_version_id: "selected-version" });
  await api.requestApprovalRegeneration(approval, "regenerate_artifact", "", key);
  expect(JSON.parse(String(fetch.mock.calls[2][1]?.body))).not.toHaveProperty("target_artifact_version_id");
});

it("sends the remaining episode mode inside the version-bound approval payload", async () => {
  const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response(JSON.stringify({ data: {} }), { status: 200, headers: { "Content-Type": "application/json" } }));
  await api.resolveApproval(approval, "approve", { episode_execution_mode: "continuous" }, key);
  expect(fetch).toHaveBeenCalledOnce();
  expect(String(fetch.mock.calls[0][0])).toBe("/api/v1/approvals/approval/resolutions");
  expect(JSON.parse(String(fetch.mock.calls[0][1]?.body))).toMatchObject({
    action: "approve", expected_approval_version: approval.version, subject_snapshot_hash: approval.subject_snapshot_hash,
    resolution_payload: { episode_execution_mode: "continuous" },
  });
  expect(new Headers(fetch.mock.calls[0][1]?.headers).get("Idempotency-Key")).toBe(key);
});
