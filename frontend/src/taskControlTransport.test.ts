import { afterEach, expect, it, vi } from "vitest";
import { api } from "./api";

afterEach(() => vi.restoreAllMocks());
const key = "11111111-1111-4111-8111-111111111111";
it.each(["cancel", "pause", "resume", "retry"] as const)("keeps the %s task and request identity across network and manual retries", async (operation) => {
  const fetch = vi.spyOn(globalThis, "fetch").mockRejectedValueOnce(new Error("Lost response")).mockImplementation(async () => new Response(JSON.stringify({ data: {} }), { status: 200 }));
  const methods = { cancel: api.cancelAgentTask, pause: api.pauseAgentTask, resume: api.resumeAgentTask, retry: api.retryAgentTask };
  await methods[operation]("task/with space", key); await methods[operation]("task/with space", key);
  expect(fetch).toHaveBeenCalledTimes(3);
  for (const call of fetch.mock.calls) {
    expect(call[0]).toBe(`/api/v1/agent-tasks/task%2Fwith%20space/${operation}`);
    expect(new Headers(call[1]?.headers).get("Idempotency-Key")).toBe(key);
    expect(call[1]?.method).toBe("POST");
    expect(call[1]?.body).toBeUndefined();
  }
});
