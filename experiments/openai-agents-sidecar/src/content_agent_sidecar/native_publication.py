from __future__ import annotations

import hashlib
import io
import json
import tarfile
from typing import Any

from agents import RunContextWrapper, function_tool
from agents.tool_context import ToolContext
from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from .backend import BackendError
from .native_files import MAX_FILE_BYTES, portable_file_path
from .native_manifest import GENERATION_DIRECTORY, MEMORY_DIRECTORY
from .native_snapshot import MAX_ARCHIVE_BYTES, SnapshotReference
from .workspace_files import file_content_url


class PublicationSelection(BaseModel):
    model_config = ConfigDict(frozen=True, extra="forbid", strict=True, hide_input_in_errors=True)
    source_path: str
    path: str

    @field_validator("source_path", "path")
    @classmethod
    def check_path(cls, value):
        if not portable_file_path(value):
            raise ValueError("Publication paths must be portable workspace-relative file paths")
        if value.split("/")[0].lower() in {MEMORY_DIRECTORY, GENERATION_DIRECTORY}:
            raise ValueError("Private memory cannot be published as shared project files")
        return value

    @model_validator(mode="after")
    def check_destination(self):
        if self.path.split("/")[0].lower() == ".skills":
            raise ValueError("Publish installed Skill copies to a project draft, not the reserved .skills directory")
        return self


class PublicationFile(PublicationSelection):
    expected_version: int = Field(ge=0)


class PublicationArguments(BaseModel):
    model_config = ConfigDict(frozen=True, extra="forbid", strict=True, hide_input_in_errors=True)
    snapshot: SnapshotReference
    files: list[PublicationFile] = Field(min_length=1, max_length=256)

    @model_validator(mode="after")
    def unique_destinations(self):
        validate_selection(self.files)
        return self


def validate_selection(files):
    if not 1 <= len(files) <= 256:
        raise ValueError("Publication requires 1 to 256 explicit file destinations")
    targets = set()
    for file in files:
        key = file.path.lower()
        if any(key == old or key.startswith(old + "/") or old.startswith(key + "/") for old in targets):
            raise ValueError("Publication destinations overlap or collide")
        targets.add(key)


def snapshot_files(body: bytes, reference: SnapshotReference, files: list[PublicationSelection]) -> dict[str, dict]:
    if not isinstance(body, bytes) or not 0 < len(body) <= MAX_ARCHIVE_BYTES or hashlib.sha256(body).hexdigest() != reference.sha256:
        raise BackendError("Publication snapshot bytes do not match the exact reference")
    wanted = {file.source_path for file in files}
    found = {}
    # Inspect only, never extract an archive into the host filesystem.
    with tarfile.open(fileobj=io.BytesIO(body), mode="r:") as archive:
        for index, entry in enumerate(archive, start=1):
            if index > 1024:
                raise BackendError("Publication snapshot has too many entries")
            if entry.name not in wanted:
                continue
            if not entry.isfile() or entry.name in found or not 0 <= entry.size <= MAX_FILE_BYTES:
                raise BackendError("Publication source is not a bounded regular file")
            with archive.extractfile(entry) as stream:
                content = stream.read(MAX_FILE_BYTES + 1)
            if len(content) != entry.size:
                raise BackendError("Publication source size differs from its archive")
            try:
                binary = "\x00" in content.decode("utf-8")
            except UnicodeDecodeError:
                binary = True
            found[entry.name] = {"size_bytes": len(content), "content_hash": hashlib.sha256(content).hexdigest(), "binary": binary}
    if set(found) != wanted:
        raise BackendError("Publication snapshot does not contain every selected file")
    return found


def _enabled(ctx, _agent):
    return getattr(ctx.context, "native_workspace", None) is not None


def _owner(context):
    owner = getattr(context, "native_workspace", None)
    if owner is None or owner.client is None or owner.guard is None:
        raise BackendError("Publication requires this execution's active SDK workspace")
    owner.guard.require_confirmed()
    return owner


@function_tool(is_enabled=_enabled)
async def prepare_workspace_publication(ctx: RunContextWrapper[Any], files: list[PublicationSelection]) -> str:
    """Freeze selected native files for publication to project Working Files. Paths are relative to /workspace. Return exact publish_workspace_files arguments; nothing is installed or published yet. Re-prepare after conflicts or edits."""
    validate_selection(files)
    owner = _owner(ctx.context)
    current = await ctx.context.backend.list_workspace_files(ctx.context.project_id)
    versions = {}
    for file in current:
        if (file.get("project_id") != ctx.context.project_id or type(file.get("version")) is not int
                or file["version"] < 1 or not isinstance(file.get("path"), str)):
            raise BackendError("Publication destination inventory is invalid")
        key = file["path"].lower()
        if key in versions:
            raise BackendError("Publication destination inventory has duplicate paths")
        versions[key] = file
    selected = []
    for file in files:
        old = versions.get(file.path.lower())
        if old and old["path"] != file.path:
            raise ValueError("Use the exact case of the existing destination path")
        selected.append(PublicationFile(**file.model_dump(), expected_version=old["version"] if old else 0))
    reference, body = await owner.client.publication_snapshot()
    inventory = snapshot_files(body, reference, files)
    arguments = PublicationArguments(snapshot=reference, files=selected)
    return json.dumps({"published": False, "publish_arguments": arguments.model_dump(), "files": [
        {**file.model_dump(), **inventory[file.source_path]} for file in selected
    ]}, ensure_ascii=False)


@function_tool(is_enabled=_enabled)
async def publish_workspace_files(ctx: ToolContext[Any], snapshot: SnapshotReference, files: list[PublicationFile]) -> str:
    """Publish the exact prepared snapshot selection after approval, atomically and with version conflict checks. It creates project Working Files, not installed Skills. Then validate_workspace_skill and install_workspace_skill for a Skill draft. Never claims files were saved on the user's computer."""
    context = ctx.context
    owner = _owner(context)
    arguments = PublicationArguments(snapshot=snapshot, files=files)
    call = context.active_tool_calls.get(ctx.tool_call_id)
    if not isinstance(call, dict) or not call.get("agent_tool_call_id"):
        raise BackendError("Publication requires an audited SDK tool call")
    session_id = owner.transport.lease.session_id
    body = await owner.transport.read_snapshot(session_id, snapshot)
    inventory = snapshot_files(body, snapshot, files)
    receipt = await owner.transport.publish_files(str(call["agent_tool_call_id"]), ctx.tool_call_id, arguments.model_dump())
    if (not isinstance(receipt, dict) or receipt.get("session_id") != session_id
            or receipt.get("agent_tool_call_id") != call["agent_tool_call_id"]
            or receipt.get("snapshot") != snapshot.model_dump() or not isinstance(receipt.get("files"), list)
            or len(receipt["files"]) != len(files)):
        raise BackendError("Publication receipt does not match its snapshot or audited call")
    for requested, saved in zip(files, receipt["files"], strict=True):
        expected = inventory[requested.source_path]
        if (not isinstance(saved, dict) or saved.get("project_id") != context.project_id
                or saved.get("path") != requested.path or type(saved.get("version")) is not int
                or saved["version"] != requested.expected_version + 1 or saved.get("deleted") is not False
                or saved.get("agent_tool_call_id") != call["agent_tool_call_id"]
                or type(saved.get("size_bytes")) is not int or saved["size_bytes"] != expected["size_bytes"]
                or saved.get("content_hash") != expected["content_hash"]
                or saved.get("binary", False) is not expected["binary"]):
            raise BackendError("Published file receipt differs from its approved bytes or destination")
        saved["content_url"] = file_content_url(context.project_id, saved["path"], saved["version"])
    return json.dumps({**receipt, "saved_to": "project_workspace", "installed_as_skill": False}, ensure_ascii=False)
