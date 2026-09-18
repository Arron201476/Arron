import hashlib
import io
import json
from pathlib import PurePosixPath
import tarfile
from typing import Any

from agents import RunContextWrapper, function_tool
from agents.tool_context import ToolContext
from pydantic import BaseModel, ConfigDict, Field, field_validator

from .backend import BackendError
from .managed_memory import resolve_memory_snapshot
from .native_files import _go_json_hash, portable_file_path
from .native_manifest import MEMORY_DIRECTORY
from .native_publication import _enabled, _owner
from .native_snapshot import MAX_ARCHIVE_BYTES, SnapshotReference


class MemorySelection(BaseModel):
    model_config = ConfigDict(strict=True, frozen=True, extra="forbid", hide_input_in_errors=True)
    files: list[str] = Field(min_length=1, max_length=256)

    @field_validator("files")
    @classmethod
    def validate_paths(cls, files):
        seen = {}
        for value in files:
            if not portable_file_path(value) or not value.startswith(MEMORY_DIRECTORY+"/"):
                raise ValueError("Memory publication requires private workspace-relative paths")
            path = PurePosixPath(value)
            for index, part in enumerate((path, *path.parents)):
                if str(part) == ".":
                    continue
                key, kind = str(part).lower(), index > 0
                if key in seen and (seen[key] != (str(part), kind) or not kind):
                    raise ValueError("Memory publication paths overlap or alias")
                seen[key] = (str(part), kind)
        if MEMORY_DIRECTORY+"/memory_summary.md" not in files:
            raise ValueError("Memory publication requires its exact summary file")
        return files


class MemoryPublicationArguments(MemorySelection):
    snapshot: SnapshotReference
    expected_version: int = Field(ge=0)


def memory_snapshot_contents(body: bytes, reference: SnapshotReference, files: list[str]) -> dict[str, str]:
    MemorySelection(files=files)
    if not isinstance(body, bytes) or not 0 < len(body) <= MAX_ARCHIVE_BYTES or hashlib.sha256(body).hexdigest() != reference.sha256:
        raise BackendError("Memory snapshot bytes do not match their reference")
    wanted, found = set(files), {}
    with tarfile.open(fileobj=io.BytesIO(body), mode="r:") as archive:
        for index, entry in enumerate(archive, 1):
            if index > 1024:
                raise BackendError("Memory snapshot has too many entries")
            if entry.name not in wanted:
                continue
            key = entry.name[len(MEMORY_DIRECTORY)+1:]
            if not entry.isfile() or key in found or not 0 <= entry.size <= 1024*1024:
                raise BackendError("Memory publication requires bounded regular UTF-8 files")
            with archive.extractfile(entry) as stream:
                value = stream.read(1024*1024+1)
            if len(value) != entry.size or b"\0" in value:
                raise BackendError("Memory file content is invalid")
            found[key] = value.decode("utf-8", errors="strict")
    if len(found) != len(wanted) or not found.get("memory_summary.md", "").strip():
        raise BackendError("Memory snapshot is missing its selected files or summary")
    if len(json.dumps(found, ensure_ascii=False).encode()) > 1024*1024:
        raise BackendError("Memory bundle exceeds its storage bound")
    return dict(sorted(found.items()))


@function_tool(is_enabled=_enabled)
async def prepare_agent_memory_publication(ctx: RunContextWrapper[Any], files: list[str]) -> str:
    """Prepare an explicit complete private memory replacement from .agent-memory files. Include memory_summary.md and every file to retain. Returns exact publish_agent_memory arguments and hashes, not file contents. Does not save or enable memory."""
    MemorySelection(files=files)
    owner = _owner(ctx.context)
    memory = await resolve_memory_snapshot(ctx.context)
    reference, body = await owner.client.publication_snapshot()
    contents = memory_snapshot_contents(body, reference, files)
    args = MemoryPublicationArguments(files=files, snapshot=reference, expected_version=memory.current_version)
    return json.dumps({"saved": False, "publish_arguments": args.model_dump(), "content_hash": _go_json_hash(contents), "file_count": len(contents)})


@function_tool(is_enabled=_enabled)
async def publish_agent_memory(ctx: ToolContext[Any], snapshot: SnapshotReference, expected_version: int, files: list[str]) -> str:
    """After the memory owner approves, replace and enable private project memory with the exact prepared snapshot selection. Never saves shared project files or installs Skills. Check the receipt; conflicts require preparing a new request, unknown results retain the original call."""
    context = ctx.context
    owner = _owner(context)
    args = MemoryPublicationArguments(snapshot=snapshot, expected_version=expected_version, files=files)
    call = context.active_tool_calls.get(ctx.tool_call_id)
    if not isinstance(call, dict) or not call.get("agent_tool_call_id"):
        raise BackendError("Memory publication requires an audited SDK call")
    memory = await resolve_memory_snapshot(context)
    session_id = owner.transport.lease.session_id
    body = await owner.transport.read_snapshot(session_id, snapshot)
    contents = memory_snapshot_contents(body, snapshot, files)
    receipt = await owner.transport.publish_memory(str(call["agent_tool_call_id"]), ctx.tool_call_id, args.model_dump())
    if (not isinstance(receipt, dict) or set(receipt) != {"agent_tool_call_id", "session_id", "project_id", "user_id", "version", "content_hash", "snapshot"}
            or receipt["agent_tool_call_id"] != call["agent_tool_call_id"] or receipt["session_id"] != session_id
            or receipt["project_id"] != context.project_id or receipt["user_id"] != memory.user_id
            or type(receipt["version"]) is not int or receipt["version"] != expected_version+1
            or receipt["content_hash"] != _go_json_hash(contents) or receipt["snapshot"] != snapshot.model_dump()):
        raise BackendError("Memory publication receipt differs from its approved bytes or owner")
    return json.dumps({**receipt, "saved": True, "saved_to": "private_project_memory"})
