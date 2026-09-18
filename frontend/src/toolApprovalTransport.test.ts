import { afterEach, expect, it, vi } from "vitest";
import { api } from "./api";
import type { AgentToolApproval } from "./types";

afterEach(() => vi.restoreAllMocks());
it.each(["approve", "reject"] as const)("preserves the %s approval identity for transport and manual retry", async (action) => {
  const approval = { agent_tool_approval_id: "approval/with space", version: 4, subject_snapshot_hash: "subject" } as AgentToolApproval;
  const key = "11111111-1111-4111-8111-111111111111";
  const fetch = vi.spyOn(globalThis, "fetch").mockRejectedValueOnce(new Error("Lost response")).mockImplementation(async () => new Response(JSON.stringify({ data: {} }), { status: 200 }));
  await api.resolveAgentToolApproval(approval, action, key);
  await api.resolveAgentToolApproval(approval, action, key);
  expect(fetch).toHaveBeenCalledTimes(3);
  for (const call of fetch.mock.calls) {
    expect(call[0]).toBe("/api/v1/agent-tool-approvals/approval%2Fwith%20space/resolutions");
    expect(new Headers(call[1]?.headers).get("Idempotency-Key")).toBe(key);
    expect(call[1]?.method).toBe("POST");
    expect(JSON.parse(call[1]?.body as string)).toEqual({ expected_version: 4, subject_snapshot_hash: "subject", action });
  }
});
