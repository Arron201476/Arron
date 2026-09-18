from __future__ import annotations

from typing import Any
from urllib.parse import quote


async def persist_tool_output(
    backend: Any, call_id: str, sdk_call_id: str, filename: str, data: bytes,
    *, project_id: str = "", source_type: str = "",
) -> dict[str, Any]:
    if not call_id or not sdk_call_id or not data:
        raise ValueError("Tool output is not associated with an audited tool call")
    result = await backend.store_agent_tool_output(call_id, sdk_call_id, filename, data)
    asset = result.get("asset") or {}
    asset_id = str(asset.get("asset_id") or "")
    snapshot = result.get("asset_snapshot") or {}
    snapshot_id = str(snapshot.get("asset_snapshot_id") or "")
    if not asset_id or not snapshot_id or snapshot.get("asset_id") != asset_id:
        raise ValueError("Tool output was not persisted as a project asset")
    metadata = asset.get("metadata") or {}
    if ((project_id and asset.get("project_id") != project_id)
            or (source_type and (asset.get("source_type") != source_type
                or metadata.get("agent_tool_call_id") != call_id
                or metadata.get("sdk_tool_call_id") != sdk_call_id))):
        raise ValueError("Tool output receipt does not match the execution")
    asset_path = quote(asset_id, safe="")
    return {"asset_id": asset_id, "asset_snapshot_id": snapshot_id, "kind": asset.get("kind"),
            "filename": asset.get("original_filename"), "content_url": f"/api/v1/assets/{asset_path}/content",
            "download_url": f"/api/v1/assets/{asset_path}/download", "size_bytes": asset.get("size_bytes")}
