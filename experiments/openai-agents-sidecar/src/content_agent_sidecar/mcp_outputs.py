from __future__ import annotations

import base64
import hashlib
import json
import re
from typing import Any
from urllib.parse import unquote, urlsplit

from mcp.types import TextContent

from .tool_outputs import persist_tool_output


MAX_OUTPUTS = 16
MAX_OUTPUT_BYTES = 20 * 1024 * 1024
_EXTENSIONS = {
    "image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif",
    "audio/wav": ".wav", "audio/x-wav": ".wav", "audio/mpeg": ".mp3", "audio/ogg": ".ogg",
    "text/plain": ".txt", "text/markdown": ".md", "text/csv": ".csv", "application/json": ".json",
    "application/pdf": ".pdf", "application/zip": ".zip",
    "application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": ".xlsx",
}


def _filename(index: int, name: str, uri: str, mime_type: str) -> str:
    candidate = name or unquote(urlsplit(uri).path.rsplit("/", 1)[-1])
    candidate = re.sub(r'[/\\:\x00-\x1f\x7f<>"|?*]', "_", candidate).strip(" .")
    if not candidate or len(candidate.encode("utf-8")) > 140:
        candidate = "output" + _EXTENSIONS.get(mime_type, ".bin")
    elif "." not in candidate:
        candidate += _EXTENSIONS.get(mime_type, ".bin")
    return f"mcp-{index:02d}-{candidate}"


async def persist_mcp_outputs(server: Any, result: Any, audit: Any) -> tuple[Any, dict[str, Any]]:
    """Keep native SDK content; add verified asset receipts without fetching arbitrary URLs."""
    if getattr(result, "result_type", "complete") != "complete":
        raise ValueError("MCP output is not complete")
    payload = result.model_dump(mode="json", by_alias=True, exclude_none=True)
    pending: list[tuple[str, bytes]] = []
    budget = min(MAX_OUTPUT_BYTES, audit.max_result_bytes)
    size = 0
    seen_links: set[str] = set()

    def collect(block: dict[str, Any], name: str = "") -> dict[str, Any]:
        nonlocal size
        kind = block.get("type")
        resource = block.get("resource") if kind == "resource" else block
        if not isinstance(resource, dict):
            raise ValueError("MCP output resource is invalid")
        binary = resource.get("data") if kind in {"image", "audio"} else resource.get("blob")
        text = resource.get("text") if kind == "resource" else None
        if binary is None and text is None:
            return block
        if len(pending) >= MAX_OUTPUTS:
            raise ValueError("MCP output contains too many files")
        if binary is not None:
            if not isinstance(binary, str) or len(binary) > ((budget - size + 2) // 3) * 4:
                raise ValueError("MCP output file exceeds the byte limit")
            data = base64.b64decode(binary, validate=True)
        else:
            if not isinstance(text, str):
                raise ValueError("MCP text resource is invalid")
            data = text.encode("utf-8")
        if not data or size + len(data) > budget:
            raise ValueError("MCP output file is empty or exceeds the byte limit")
        size += len(data)
        filename = _filename(len(pending) + 1, name, str(resource.get("uri") or ""),
                             str(resource.get("mime_type") or resource.get("mimeType") or ("text/plain" if text is not None else "")))
        pending.append((filename, data))
        return {"type": kind, "filename": filename, "size_bytes": len(data),
                "sha256": hashlib.sha256(data).hexdigest()}

    summary = []
    for block in payload.get("content", []):
        if block.get("type") != "resource_link":
            summary.append(collect(block))
            continue
        uri = str(block.get("uri") or "")
        if not uri:
            raise ValueError("MCP linked resource has no identity")
        if uri in seen_links:
            continue
        if len(seen_links) >= MAX_OUTPUTS or block.get("size", 0) > budget - size:
            raise ValueError("MCP linked output exceeds the delivery limit")
        seen_links.add(uri)
        # Resolve only through the already-authorized MCP session. Never open a
        # resource URI as a host path or make an independent HTTP request.
        linked = await server.read_resource(uri)
        if getattr(linked, "result_type", "complete") != "complete":
            raise ValueError("MCP linked resource is not complete")
        contents = linked.model_dump(mode="json", by_alias=True, exclude_none=True).get("contents", [])
        if not contents:
            raise ValueError("MCP linked resource returned no content")
        for resource in contents:
            if resource.get("uri") != uri:
                raise ValueError("MCP linked resource identity changed")
            summary.append(collect({"type": "resource", "resource": resource}, str(block.get("name") or "")))

    # Validate all inline and linked files before starting durable uploads.
    outputs = [await persist_tool_output(audit.provider._backend, audit.agent_tool_call_id,
        audit.sdk_tool_call_id, filename, data, project_id=audit.project_id, source_type="mcp_tool")
        for filename, data in pending]
    payload["content"] = summary
    if outputs:
        payload["outputs"] = outputs
        receipt = TextContent(type="text", text=json.dumps({"platform_tool_outputs": outputs}, ensure_ascii=False))
        result = result.model_copy(update={"content": [*result.content, receipt]})
    return result, payload
