from __future__ import annotations

import json
from typing import Any, Literal
from urllib.parse import quote, urlencode

from agents import RunContextWrapper, function_tool
from agents.apply_diff import apply_diff
from agents.tool_context import ToolContext


MAX_FILE_BYTES = 1 << 20


def file_content_url(project_id: str, path: str, version: int) -> str:
    return f"/api/v1/projects/{quote(project_id, safe='')}/files/content?{urlencode({'path': path, 'version': version})}"


@function_tool
async def list_workspace_files(ctx: RunContextWrapper[Any]) -> str:
    """List this project's saved working files and current versions, including tombstones."""
    files = await ctx.context.backend.list_workspace_files(ctx.context.project_id)
    return json.dumps({"files": files}, ensure_ascii=False)


@function_tool
async def read_workspace_file(
    ctx: RunContextWrapper[Any], path: str, version: int = 0, offset: int = 0, limit: int = 16000,
) -> str:
    """Read saved UTF-8 text by character range, or binary file metadata and download URL. Version 0 selects latest."""
    if offset < 0 or not 1 <= limit <= 48000 or version < 0:
        raise ValueError("Invalid file version or character range")
    result = await ctx.context.backend.read_workspace_file(ctx.context.project_id, path, version, offset, limit)
    file = result.get("file") or {}
    if file.get("binary"):
        if file.get("project_id") != ctx.context.project_id or file.get("path") != path or not isinstance(file.get("version"), int) or file["version"] < 1 or version and file["version"] != version:
            raise ValueError("Binary file receipt does not match the requested project and version")
        result["content_url"] = file_content_url(ctx.context.project_id, path, file["version"])
    return json.dumps(result, ensure_ascii=False)


@function_tool
async def apply_workspace_patch(
    ctx: ToolContext[Any], path: str, expected_version: int,
    operation: Literal["create_file", "update_file", "delete_file"], diff: str,
) -> str:
    """Apply an SDK V4A patch to a project file, never a host path. Create: +prefixed lines. Update: @@ context hunks. Delete: empty diff. Use version 0 for new files, otherwise the exact version read/listed. Files persist but are not installed Skills or executed scripts."""
    context = ctx.context
    call = context.active_tool_calls.get(ctx.tool_call_id)
    if not isinstance(call, dict) or not call.get("agent_tool_call_id"):
        raise ValueError("Workspace writes require an audited SDK tool call")
    if expected_version < 0 or len(diff.encode("utf-8")) > MAX_FILE_BYTES:
        raise ValueError("Invalid file version or patch size")
    if operation == "delete_file":
        if diff or expected_version == 0:
            raise ValueError("Delete requires an existing file version and an empty diff")
        content = ""
    elif operation == "create_file":
        content = apply_diff("", diff, mode="create")
    else:
        if expected_version == 0:
            raise ValueError("Read the file before updating it")
        source = await context.backend.read_workspace_file(context.project_id, path, expected_version, 0, MAX_FILE_BYTES)
        file = source.get("file") or {}
        if file.get("binary"):
            raise ValueError("Binary files cannot be edited with a UTF-8 patch; publish native workspace bytes instead")
        if source.get("truncated") or file.get("version") != expected_version or file.get("path") != path or file.get("project_id") != context.project_id:
            raise ValueError("File content does not match the requested project and version")
        original = source.get("content")
        if not isinstance(original, str) or len(original.encode("utf-8")) > MAX_FILE_BYTES:
            raise ValueError("File content is missing or exceeds the limit")
        content = apply_diff(original, diff)
    if len(content.encode("utf-8")) > MAX_FILE_BYTES or "\x00" in content:
        raise ValueError("Patched file exceeds the UTF-8 file limit")
    patch = {"path": path, "expected_version": expected_version, "operation": operation, "diff": diff}
    file = await context.backend.apply_workspace_patch(str(call["agent_tool_call_id"]), ctx.tool_call_id, patch, content)
    if file.get("project_id") != context.project_id or file.get("path") != path or file.get("version") != expected_version + 1:
        raise ValueError("Saved file receipt does not match this operation")
    result = {"file": file, "saved_to": "project_workspace", "installed_as_skill": False}
    if not file.get("deleted"):
        result["content_url"] = file_content_url(context.project_id, path, int(file["version"]))
    return json.dumps(result, ensure_ascii=False)
