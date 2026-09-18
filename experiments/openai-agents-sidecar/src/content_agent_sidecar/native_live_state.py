from __future__ import annotations

from typing import Literal
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field, model_validator

from .backend import BackendError
from .native_snapshot import SnapshotReference


class NativePTYProcessState(BaseModel):
    model_config = ConfigDict(frozen=True, extra="forbid", hide_input_in_errors=True)
    pty_session_id: int = Field(strict=True, ge=1000, le=1127)
    process_id: str = Field(pattern=r"^[a-f0-9]{32}$")
    sequence: int = Field(strict=True, ge=1, le=512)
    tty: bool = Field(strict=True)
    status: Literal["running", "exited"]


def canonical_uuid(value):
    return isinstance(value, str) and str(UUID(value)) == value


class NativePTYState(BaseModel):
    model_config = ConfigDict(frozen=True, extra="forbid", hide_input_in_errors=True)
    session_id: str
    environment_id: str
    state: Literal["ready", "closed", "absent"]
    processes: list[NativePTYProcessState] = Field(max_length=128)

    @model_validator(mode="after")
    def validate_binding(self):
        if not canonical_uuid(self.session_id) or (self.state == "absent" and self.environment_id != ""
                or self.state != "absent" and not canonical_uuid(self.environment_id)):
            raise ValueError("Terminal inventory has no canonical environment identity")
        if self.state != "ready" and self.processes:
            raise ValueError("A missing environment cannot promise live processes")
        ids = [item.pty_session_id for item in self.processes]
        if ids != sorted(set(ids)) or len({item.process_id for item in self.processes}) != len(ids):
            raise ValueError("Terminal inventory contains ambiguous process identities")
        return self


class NativeWorkspaceCheckpoint(BaseModel):
    model_config = ConfigDict(frozen=True, extra="forbid", hide_input_in_errors=True)
    schema_version: Literal["content_agent_native_workspace.v1"] = "content_agent_native_workspace.v1"
    session_id: str
    environment_id: str
    state: Literal["ready", "closed"]
    snapshot: SnapshotReference

    @model_validator(mode="after")
    def validate_binding(self):
        if not canonical_uuid(self.session_id) or not canonical_uuid(self.environment_id):
            raise ValueError("Workspace checkpoint has no canonical environment identity")
        return self


def restore_native_checkpoint(context, payload):
    value = payload.get("native_workspace_checkpoint")
    if value is None:
        context.native_workspace_checkpoint = None
        return
    try:
        context.native_workspace_checkpoint = NativeWorkspaceCheckpoint.model_validate(value)
    except (ValueError, TypeError):
        raise BackendError("Native workspace checkpoint is not a verified recovery reference") from None
