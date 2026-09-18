import type { AgentToolCall, Asset, AttachmentRef, ComposerRegistryEntry } from "../../types";

export function assetAvailable(asset: Asset) {
  return asset.status === "available" && !asset.deleted_at && (!asset.expires_at || Date.parse(asset.expires_at) > Date.now());
}

export function materialUnavailableReason(asset: Asset, capability: ComposerRegistryEntry | null = null) {
  if (!assetAvailable(asset)) return asset.status === "deleted" || asset.deleted_at ? "文件已删除" : "文件暂不可用或已过期";
  if (!asset.current_snapshot_id) return "缺少材料快照";
  if (!["text", "document", "image", "video"].includes(asset.kind)) return "该格式尚不能直接作为材料";
  if ((asset.kind === "document" || asset.kind === "text") && asset.parse_status !== "completed") return "文本尚未解析完成";
  if (capability && !capability.accepted_asset_kinds.includes(asset.kind)) return `${capability.label} 不接受这类材料`;
  return "";
}

export function materialAttachment(asset: Asset): AttachmentRef {
  return { asset_id: asset.asset_id, asset_snapshot_id: asset.current_snapshot_id, display_name: asset.display_name || asset.original_filename, kind: asset.kind };
}

export function toolOutputAssets(call: AgentToolCall, assets: Asset[]) {
  if (!["hosted", "mcp"].includes(call.tool_kind) || !call.sdk_tool_call_id) return [];
  return assets.filter((asset) => asset.project_id === call.project_id && asset.source_type === `${call.tool_kind}_tool` && asset.metadata?.agent_tool_call_id === call.agent_tool_call_id && asset.metadata?.sdk_tool_call_id === call.sdk_tool_call_id);
}

export function citationLink(value: string | undefined): string | null {
  if (!value || /[\s\\\u0000-\u001f\u007f]/.test(value)) return null;
  try {
    const url = new URL(value);
    if (!["https:", "http:"].includes(url.protocol) || !url.hostname || url.username || url.password) return null;
    for (const key of [...url.searchParams.keys(), ...new URLSearchParams(url.hash.slice(1)).keys()]) if (/authorization|password|passwd|token|secret|apikey|accesskey|privatekey|cookie|credential|sessionid/.test(key.toLowerCase().replace(/[_\-. ]/g, ""))) return null;
    return url.href;
  } catch { return null; }
}
