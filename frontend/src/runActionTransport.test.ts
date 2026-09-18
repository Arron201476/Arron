import { afterEach, expect, it, vi } from "vitest";
import { api } from "./api";
import type { AvailableAction } from "./types";

afterEach(() => vi.restoreAllMocks());
const key = "11111111-1111-4111-8111-111111111111";
const actions = ["pause_run", "resume_run", "cancel_run", "retry_failed_step", "continue_with_partial_results"];
function actionFor(actionID: string): AvailableAction {
  const step = actionID === "retry_failed_step" || actionID === "continue_with_partial_results";
  return { action_id: actionID, target_type: step ? "step_run" : "run", target_id: step ? "step" : "run", enabled: true, disabled_reason: null };
}

it.each(actions)("preserves %s target, confirmation and identity through automatic and manual retry", async (actionID) => {
  const fetch = vi.spyOn(globalThis, "fetch").mockRejectedValueOnce(new Error("Response lost")).mockImplementation(async () => new Response(JSON.stringify({ data: {} }), { status: 200 }));
  await api.executeRunAction("run", actionFor(actionID), key);
  await api.executeRunAction("run", actionFor(actionID), key);
  expect(fetch).toHaveBeenCalledTimes(3);
  const path = ({ pause_run: "runs/run/pause", resume_run: "runs/run/resume", cancel_run: "runs/run/cancel", retry_failed_step: "steps/step/retries", continue_with_partial_results: "steps/step/partial-continuations" } as Record<string, string>)[actionID];
  for (const call of fetch.mock.calls) {
    expect(call[0]).toBe(`/api/v1/${path}`);
    expect(new Headers(call[1]?.headers).get("Idempotency-Key")).toBe(key);
    expect(call[1]?.body).toBe(["cancel_run", "continue_with_partial_results"].includes(actionID) ? JSON.stringify({ confirmation: { confirmed: true } }) : undefined);
  }
});

it.each(actions)("rejects disabled or mismatched %s targets before any network request", async (actionID) => {
  const fetch = vi.spyOn(globalThis, "fetch");
  const action = actionFor(actionID);
  for (const invalid of [{ ...action, enabled: false }, { ...action, target_type: "artifact" }, { ...action, target_id: "" }]) {
    await expect(api.executeRunAction("run", invalid, key)).rejects.toMatchObject({ code: "RUN_STATE_CONFLICT" });
  }
  if (action.target_type === "run") await expect(api.executeRunAction("another-run", action, key)).rejects.toMatchObject({ code: "RUN_STATE_CONFLICT" });
  expect(fetch).not.toHaveBeenCalled();
});
