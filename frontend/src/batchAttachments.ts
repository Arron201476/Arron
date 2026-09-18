import { api, ApiError } from "./api";
import type { AssetSetSnapshot, AttachmentRef } from "./types";

export function removeAttachmentReferences(attachments: AttachmentRef[], assetIDs: string[]): AttachmentRef[] {
  const removed = new Set(assetIDs);
  const containers = new Set(attachments.filter((item) => removed.has(item.asset_id) && item.container_asset_id).map((item) => item.container_asset_id!));
  return attachments.filter((item) => !removed.has(item.asset_id) && !containers.has(item.asset_id) && !removed.has(item.container_asset_id ?? "")).map((item) => {
    if (!containers.has(item.container_asset_id ?? "")) return item;
    const { container_asset_id: _container, hidden: _hidden, ...reference } = item;
    return reference;
  });
}

export function videoBatchAttachmentRefs(batch: AssetSetSnapshot, attachments: AttachmentRef[]): AttachmentRef[] {
  const included = batch.members.filter((member) => member.included).sort((a, b) => a.episode_order - b.episode_order);
  if (!included.length) throw new ApiError("ASSET_SET_EMPTY", "批次没有纳入任何素材。", 409);
  return included.map((member) => {
    const reference = attachments.find((item) => item.asset_id === member.asset_id);
    if (!reference?.asset_snapshot_id) throw new ApiError("ASSET_SET_ATTACHMENTS_MISSING", "批次素材引用不完整，请重新读取批次。", 409);
    const { container_asset_id: _container, hidden: _hidden, ...attached } = reference;
    return attached;
  });
}

export function nonBatchAttachmentRefs(batch: AssetSetSnapshot, attachments: AttachmentRef[]): AttachmentRef[] {
  const members = new Set(batch.members.map((member) => member.asset_id));
  const containers = new Set(attachments.filter((item) => members.has(item.asset_id) && item.container_asset_id).map((item) => item.container_asset_id!));
  return attachments.filter((item) => !members.has(item.asset_id) && !containers.has(item.asset_id)).map((item) => {
    if (!containers.has(item.container_asset_id ?? "")) return item;
    const { container_asset_id: _container, hidden: _hidden, ...reference } = item;
    return reference;
  });
}

export async function reconcileVideoBatchAttachments(projectID: string, previous: AssetSetSnapshot | null, next: AssetSetSnapshot, attachments: AttachmentRef[]): Promise<AttachmentRef[]> {
  if (next.asset_set.project_id !== projectID || previous && previous.asset_set.asset_set_id !== next.asset_set.asset_set_id) throw new ApiError("ASSET_SET_VERSION_CONFLICT", "素材批次不属于当前作品或操作对象。", 409);
  const retained = new Set(next.members.map((member) => member.asset_id));
  const removed = (previous?.members ?? []).filter((member) => !retained.has(member.asset_id)).map((member) => member.asset_id);
  const result = removeAttachmentReferences(attachments, removed);
  const missing = next.members.filter((member) => member.included && !result.some((item) => item.asset_id === member.asset_id));
  if (missing.length) {
    const assets = await api.listAssets(projectID);
    for (const member of missing) {
      const asset = assets.find((item) => item.asset_id === member.asset_id && item.project_id === projectID && item.status === "available" && item.kind === "video");
      if (!asset?.current_snapshot_id) throw new ApiError("ASSET_SET_ATTACHMENTS_MISSING", "批次素材已变化或不可用，请重新选择。", 409);
      result.push({ asset_id: asset.asset_id, asset_snapshot_id: asset.current_snapshot_id, display_name: asset.display_name || asset.original_filename, kind: asset.kind });
    }
  }
  return result;
}
