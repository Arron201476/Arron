import { afterEach, expect, it, vi } from "vitest";
import { api, ApiError } from "./api";
import type { AssetSetSnapshot } from "./types";
afterEach(() => vi.restoreAllMocks());
const key = "11111111-1111-4111-8111-111111111111";
it.each(["move", "assign", "bulk", "seal", "remove", "include", "exclude", "reopen"])("preserves %s batch version and key across retries", async (action) => {
  const fetcher = vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response(JSON.stringify({ data: {} }), { status: 201 }));
  const batch = { asset_set: { asset_set_id: "batch/id", current_version: 7 }, version: { completeness: { missing_episode_numbers: [2] } } } as AssetSetSnapshot;
  const invoke = () => action === "move" ? api.swapVideoEpisodes(batch, "a", 2, "b", 1, key) : action === "assign" ? api.setVideoEpisodeNo(batch, "a", 3, key) : action === "bulk" ? api.setVideoEpisodeNumbers(batch, [{ assetID: "a", episodeNo: 3 }], key) : action === "remove" ? api.removeVideoBatchAsset(batch, "a", key) : action === "reopen" ? api.reopenVideoBatch(batch, key) : action === "include" || action === "exclude" ? api.setVideoBatchAssetIncluded(batch, "a", action === "include", key) : api.sealVideoBatch(batch, true, key);
  await invoke(); await invoke(); expect(fetcher.mock.calls[0]).toEqual(fetcher.mock.calls[1]);
  const [url, options] = fetcher.mock.calls[0];
  expect(url).toBe(`/api/v1/asset-sets/batch%2Fid/${action === "seal" || action === "reopen" ? action : "versions"}`);
  expect(new Headers(options?.headers).get("Idempotency-Key")).toBe(key);
  expect(JSON.parse(String(options?.body)).expected_asset_set_version).toBe(7);
  if (["include", "exclude", "remove"].includes(action)) expect(JSON.parse(String(options?.body)).changes).toEqual([expect.objectContaining({ operation: `${action}_asset`, asset_id: "a" })]);
});

it("reuses separate creation and append keys after a lost append response", async () => {
  const base = { asset_set: { project_id: "project/id", asset_set_id: "batch/id", current_version: 1, current_version_id: "v1", status: "collecting" }, version: { asset_set_version_id: "v1", version: 1 }, members: [] };
  let unavailable = true;
  const fetcher = vi.spyOn(globalThis, "fetch").mockImplementation(async (url) => String(url).endsWith("/versions") && unavailable ? new Response(JSON.stringify({ error: { code: "UNAVAILABLE", message: "Unknown" } }), { status: 503 }) : new Response(JSON.stringify({ data: base }), { status: 201 }));
  const attachments = [{ asset_id: "video", asset_snapshot_id: "av", display_name: "video.mp4" }];
  await expect(api.appendVideoBatch("project/id", null, attachments, undefined, key)).rejects.toBeInstanceOf(ApiError);
  unavailable = false;
  await api.appendVideoBatch("project/id", null, attachments, undefined, key);
  for (const [url, options] of fetcher.mock.calls) {
    expect(String(url)).toContain(String(url).endsWith("/versions") ? "batch%2Fid" : "project%2Fid");
    expect(new Headers(options?.headers).get("Idempotency-Key")).toBe(`${key}:${String(url).endsWith("/versions") ? "append" : "create"}`);
  }
});
