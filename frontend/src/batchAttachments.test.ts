import { afterEach, expect, it, vi } from "vitest";
import { api } from "./api";
import { nonBatchAttachmentRefs, reconcileVideoBatchAttachments, removeAttachmentReferences, videoBatchAttachmentRefs } from "./batchAttachments";
import type { Asset, AssetSetSnapshot, AttachmentRef } from "./types";

afterEach(() => vi.restoreAllMocks());
const refs: AttachmentRef[] = [
  { asset_id: "zip", asset_snapshot_id: "zip-v1", display_name: "episodes.zip", kind: "archive" },
  ...["one", "two"].map((id) => ({ asset_id: id, asset_snapshot_id: `${id}-v1`, display_name: `${id}.mp4`, kind: "video", container_asset_id: "zip", hidden: true })),
];
function batch(): AssetSetSnapshot {
  return { asset_set: { asset_set_id: "batch", project_id: "project" }, members: [
    { asset_id: "one", included: true, episode_order: 2 }, { asset_id: "two", included: true, episode_order: 1 },
  ] } as AssetSetSnapshot;
}
it("removes a ZIP and its children together", () => {
  expect(removeAttachmentReferences(refs, ["zip"])).toEqual([]);
});
it("removes a ZIP parent when a child is removed but retains visible standalone siblings", () => {
  expect(removeAttachmentReferences(refs, ["one"])).toEqual([{ asset_id: "two", asset_snapshot_id: "two-v1", display_name: "two.mp4", kind: "video" }]);
  expect(refs[2].hidden).toBe(true);
});
it("sends only included members in batch order without the ZIP or hidden metadata", () => {
  const selected = batch();
  expect(videoBatchAttachmentRefs(selected, refs).map((ref) => ref.asset_id)).toEqual(["two", "one"]);
  selected.members[0].included = false;
  expect(videoBatchAttachmentRefs(selected, refs)).toEqual([{ asset_id: "two", asset_snapshot_id: "two-v1", display_name: "two.mp4", kind: "video" }]);
});
it("rejects an empty batch or missing immutable attachment reference", () => {
  expect(() => videoBatchAttachmentRefs(batch(), [])).toThrow("批次素材引用不完整");
  const empty = batch(); empty.members.forEach((member) => { member.included = false; });
  expect(() => videoBatchAttachmentRefs(empty, refs)).toThrow("批次没有纳入任何素材");
});
it("retains non-video children of a mixed ZIP without bringing the full container back", () => {
  const doc = { asset_id: "doc", asset_snapshot_id: "doc-v1", display_name: "notes.txt", kind: "text", container_asset_id: "zip", hidden: true };
  expect(nonBatchAttachmentRefs(batch(), [...refs, doc])).toEqual([{ asset_id: "doc", asset_snapshot_id: "doc-v1", display_name: "notes.txt", kind: "text" }]);
});
it("retains excluded references for later inclusion but drops removed batch members", async () => {
  const read = vi.spyOn(api, "listAssets");
  const excluded = batch(); excluded.members[0].included = false;
  expect(await reconcileVideoBatchAttachments("project", batch(), excluded, refs)).toEqual(refs);
  const removed = batch(); removed.members.shift();
  expect((await reconcileVideoBatchAttachments("project", batch(), removed, refs)).map((ref) => ref.asset_id)).toEqual(["two"]);
  expect(read).not.toHaveBeenCalled();
});
it.each(["available", "foreign", "deleted", "no-snapshot", "text"])("resolves a newly included remote member only when %s", async (scenario) => {
  const next = batch(); next.members = [next.members[0]];
  const asset = { asset_id: "one", project_id: scenario === "foreign" ? "foreign" : "project", status: scenario === "deleted" ? "deleted" : "available", current_snapshot_id: scenario === "no-snapshot" ? "" : "one-v1", kind: scenario === "text" ? "text" : "video", original_filename: "one.mp4" } as Asset;
  vi.spyOn(api, "listAssets").mockResolvedValue([asset]);
  const result = reconcileVideoBatchAttachments("project", null, next, []);
  if (scenario === "available") expect(await result).toEqual([{ asset_id: "one", asset_snapshot_id: "one-v1", display_name: "one.mp4", kind: "video" }]);
  else await expect(result).rejects.toThrow("批次素材已变化或不可用");
});
it("rejects a different batch or project before fetching assets", async () => {
  const read = vi.spyOn(api, "listAssets");
  const next = batch(); next.asset_set.asset_set_id = "other";
  await expect(reconcileVideoBatchAttachments("project", batch(), next, [])).rejects.toThrow("不属于当前作品");
  await expect(reconcileVideoBatchAttachments("foreign", null, batch(), [])).rejects.toThrow("不属于当前作品");
  expect(read).not.toHaveBeenCalled();
});
