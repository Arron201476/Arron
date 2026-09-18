from __future__ import annotations

from datetime import datetime, timezone
from typing import Any, Literal
from uuid import uuid4

from pydantic import BaseModel, Field


AgentEventType = Literal[
    "agent.turn.started",
    "agent.updated",
    "agent.tool.started",
    "agent.tool.completed",
    "agent.output.delta",
    "agent.approval.requested",
    "agent.artifact.created",
    "agent.turn.waiting_approval",
    "agent.turn.paused",
    "agent.turn.inputs_included",
    "agent.turn.committed",
    "agent.turn.failed",
    "agent.turn.cancelled",
]


class AgentEventEnvelope(BaseModel):
    schema_version: Literal["1.0.0"] = "1.0.0"
    event_id: str = Field(default_factory=lambda: "agevt_" + uuid4().hex)
    event_type: AgentEventType
    project_id: str
    conversation_id: str
    turn_id: str
    terminal: bool = False
    payload: dict[str, Any] = Field(default_factory=dict)
    occurred_at: datetime = Field(
        default_factory=lambda: datetime.now(timezone.utc)
    )


def agent_event(
    event_type: AgentEventType,
    *,
    project_id: str,
    conversation_id: str,
    turn_id: str,
    payload: dict[str, Any] | None = None,
    terminal: bool = False,
) -> AgentEventEnvelope:
    return AgentEventEnvelope(
        event_type=event_type,
        project_id=project_id,
        conversation_id=conversation_id,
        turn_id=turn_id,
        terminal=terminal,
        payload=payload or {},
    )
