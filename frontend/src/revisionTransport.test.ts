import { afterEach, expect, it, vi } from "vitest";
import { api } from "./api";
import type { RevisionRequest } from "./types";

afterEach(() => vi.restoreAllMocks());
const key = "11111111-1111-4111-8111-111111111111";
const revision = { revision_request_id: "revision", version: 3 } as RevisionRequest;
it.each(["acceptRevision", "rejectRevision", "cancelRevision"] as const)("preserves %s identity across transport and manual retries", async (method) => {
  const fetch = vi.spyOn(globalThis, "fetch").mockRejectedValueOnce(new Error("Response lost")).mockImplementation(async () => new Response(JSON.stringify({ data: {} }), { status: 200 }));
  await api[method](revision, key); await api[method](revision, key);
  expect(fetch).toHaveBeenCalledTimes(3);
  for (const call of fetch.mock.calls) {
    expect(call[0]).toBe(fetch.mock.calls[0][0]);
    expect(new Headers(call[1]?.headers).get("Idempotency-Key")).toBe(key);
    expect(call[1]?.body).toBe(JSON.stringify({ expected_revision_version: 3 }));
  }
});

it.each(["execute", "target"] as const)("preserves %s admission identity and expected version across retries", async (action) => {
  const fetch = vi.spyOn(globalThis, "fetch").mockRejectedValueOnce(new Error("Response lost")).mockImplementation(async () => new Response(JSON.stringify({ data: {} }), { status: 202 }));
  const invoke = () => action === "execute" ? api.executeRevision("revision/id", key, 3) : api.resolveRevisionTarget("target/id", "candidate", key, 3);
  await invoke(); await invoke();
  expect(fetch).toHaveBeenCalledTimes(3);
  for (const [url, options] of fetch.mock.calls) {
    expect(url).toBe(action === "execute" ? "/api/v1/revision-requests/revision%2Fid/execute" : "/api/v1/target-resolutions/target%2Fid/resolve");
    expect(new Headers(options?.headers).get("Idempotency-Key")).toBe(key);
    expect(JSON.parse(String(options?.body))).toEqual(action === "execute" ? { expected_revision_version: 3 } : { candidate_id: "candidate", expected_revision_version: 3 });
  }
});
