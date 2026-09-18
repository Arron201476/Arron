from typing import Any

from pydantic import BaseModel, ConfigDict, Field

from .backend import BackendError
from .managed_instructions import instruction_activity_key
from .native_files import _go_json_hash, portable_file_path
from .native_manifest import NativeMemorySource, RuntimeResourceFile


class MemorySnapshot(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True, hide_input_in_errors=True)
    activity_key: str = Field(min_length=1, max_length=270)
    workspace_id: str = Field(min_length=1, max_length=256)
    project_id: str = Field(min_length=1, max_length=256)
    user_id: str = Field(min_length=1, max_length=256)
    version: int = Field(ge=0)
    current_version: int = Field(ge=0)
    content_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    read_enabled: bool
    files: dict[str, str] = Field(max_length=256)


async def resolve_memory_snapshot(context: Any) -> MemorySnapshot:
    key = instruction_activity_key(context)
    raw = await context.backend.resolve_agent_memory_snapshot(context.project_id, key)
    try:
        snapshot = MemorySnapshot.model_validate(raw)
        if snapshot.project_id != context.project_id or snapshot.activity_key != key or snapshot.current_version < snapshot.version:
            raise ValueError("identity")
        instructions = getattr(context, "managed_instructions", None)
        if instructions is not None and any(snapshot.model_dump()[name] != instructions.get(name) for name in ("workspace_id", "user_id")):
            raise ValueError("owner")
        if not snapshot.read_enabled:
            if snapshot.files:
                raise ValueError("disabled body")
            return snapshot
        if (snapshot.version < 1 or not snapshot.files.get("memory_summary.md", "").strip()
                or any(not portable_file_path(path) or "\0" in content for path, content in snapshot.files.items())
                or _go_json_hash(dict(sorted(snapshot.files.items()))) != snapshot.content_hash):
            raise ValueError("content")
        return snapshot
    except (ValueError, TypeError, UnicodeError) as exc:
        raise BackendError("Agent memory snapshot identity or content is invalid") from exc


async def resolve_memory_source(context: Any) -> NativeMemorySource | None:
    snapshot = await resolve_memory_snapshot(context)
    if not getattr(context, "memory_generation_id", ""):
        context.memory_archive_snapshot = snapshot
    return NativeMemorySource(version=snapshot.version, content_hash=snapshot.content_hash) if snapshot.read_enabled else None


def validate_memory_manifest(manifest, source: NativeMemorySource | None) -> None:
    references = {entry.resource.memory.model_dump_json() for _, entry in manifest.iter_entries()
                  if type(entry) is RuntimeResourceFile and entry.resource.memory is not None}
    expected = {source.model_dump_json()} if source else set()
    if references != expected:
        raise BackendError("Native workspace memory differs from its current execution binding")
