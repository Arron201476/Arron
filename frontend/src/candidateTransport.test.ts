import { afterEach, expect, it, vi } from "vitest";
import { api } from "./api";
import type { ScriptCandidate } from "./types";

afterEach(() => vi.restoreAllMocks());
const key = "11111111-1111-4111-8111-111111111111";
it.each(["preview", "export"])("preserves %s request identity and exact version across retries", async (operation) => {
  const fetcher = vi.spyOn(globalThis, "fetch").mockRejectedValueOnce(new Error("Response lost")).mockImplementation(async () => new Response(JSON.stringify({ data: {} }), { status: 201 }));
  const candidate = { candidate_id: "candidate/id", scripts_artifact_version_id: "version" } as ScriptCandidate;
  const invoke = () => operation === "preview" ? api.previewFinalSelection("project/id", candidate.candidate_id, "selection", key, "version") : api.createScriptExport(candidate, "docx", key);
  await invoke(); await invoke();
  expect(fetcher).toHaveBeenCalledTimes(3);
  for (const [url, options] of fetcher.mock.calls) {
    expect(url).toBe(operation === "preview" ? "/api/v1/projects/project%2Fid/final-script-selection-previews" : "/api/v1/script-candidates/candidate%2Fid/exports");
    expect(new Headers(options?.headers).get("Idempotency-Key")).toBe(key);
    expect(JSON.parse(String(options?.body))).toEqual(operation === "preview" ? { candidate_id: "candidate/id", expected_current_selection_id: "selection", expected_artifact_version_id: "version" } : { format: "docx", artifact_version_id: "version" });
  }
});

it("downloads bytes from the encoded authenticated export route", async () => {
  const fetcher = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("exact bytes"));
  const controller = new AbortController();
  const result = await api.downloadScriptExport("export/id", controller.signal);
  expect(await result.text()).toBe("exact bytes");
  expect(fetcher).toHaveBeenCalledWith("/api/v1/exports/export%2Fid/content", { signal: controller.signal });
});

it("propagates export expiry and permission failures instead of downloading JSON", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: { code: "EXPORT_EXPIRED", message: "Expired export" } }), { status: 410 }));
  await expect(api.downloadScriptExport("export")).rejects.toMatchObject({ code: "EXPORT_EXPIRED", message: "Expired export", status: 410 });
});
