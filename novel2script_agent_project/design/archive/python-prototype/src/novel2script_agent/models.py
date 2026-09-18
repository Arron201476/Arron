from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Literal

SourceMode = Literal["novel", "non_novel"]
RunStatus = Literal["pending", "running", "needs_approval", "completed", "failed", "cancelled"]


@dataclass(slots=True)
class AgentIntent:
    source_mode: SourceMode | None
    action: Literal["generate", "revise", "chat", "unknown"]
    confidence: float
    reason: str


@dataclass(slots=True)
class AgentStep:
    step_id: str
    prompt_path: str
    output_artifact: str
    status: RunStatus = "pending"
    needs_approval: bool = False


@dataclass(slots=True)
class AgentPlan:
    run_id: str
    source_mode: SourceMode
    steps: list[AgentStep] = field(default_factory=list)


@dataclass(slots=True)
class ArtifactRef:
    artifact_type: str
    path: str
    version: int = 1
    metadata: dict[str, Any] = field(default_factory=dict)
