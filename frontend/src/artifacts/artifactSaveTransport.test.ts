import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { Artifact, ArtifactVersion } from "../types";

afterEach(() => vi.restoreAllMocks());
const key = "11111111-1111-4111-8111-111111111111";
const artifact = { artifact_id: "artifact" } as Artifact;
const base = { artifact_version_id: "base", version: 1 } as ArtifactVersion;
it.each([
  ["version", () => api.createArtifactVersion(artifact, base, { content: "Saved body" }, key)],
  ["completion", () => api.completeScriptEdit("run", ["saved-version"], key)],
] as const)("preserves the %s command body and identity across retries", async (_name, invoke) => {
  const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response(JSON.stringify({ data: {} }), { status: 200 }));
  await invoke(); await invoke();
  expect(fetch).toHaveBeenCalledTimes(2);
  for (const call of fetch.mock.calls) expect(new Headers(call[1]?.headers).get("Idempotency-Key")).toBe(key);
  expect(fetch.mock.calls[1][0]).toBe(fetch.mock.calls[0][0]);
  expect(fetch.mock.calls[1][1]?.body).toBe(fetch.mock.calls[0][1]?.body);
});
