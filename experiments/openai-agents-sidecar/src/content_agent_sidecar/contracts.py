from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, Field


class HealthResponse(BaseModel):
    status: Literal["ok", "degraded"]
    backend_base_url: str
    model_configured: bool
    write_tools_enabled: bool = False


class CancelResponse(BaseModel):
    run_id: str
    accepted: bool


class ControlClarification(BaseModel):
    question: str
    options: list[str] = Field(default_factory=list)


class ControlArtifactDraft(BaseModel):
    artifact_type: Literal["generic_document", "generic_table"]
    title: str
    payload: dict[str, Any]


class ControlTargetRef(BaseModel):
    target_type: Literal["artifact"] = "artifact"
    target_id: str
    artifact_version_id: str | None = None
    artifact_type: str | None = None
    scope_key: str | None = None
    field_path: str | None = None


class ControlGoalUpdate(BaseModel):
    action: Literal["set", "complete", "cancel"]
    title: str = ""
    success_criteria: list[str] = Field(default_factory=list)


class ControlDecision(BaseModel):
    reply: str = ""
    intent: Literal[
        "propose_capability",
        "clarify",
        "chat",
        "create_artifact",
        "inspect",
        "revise",
        "regenerate",
        "unsupported",
    ]
    confidence: float = Field(ge=0, le=1)
    capability_id: str | None = None
    clarification: ControlClarification | None = None
    artifact_draft: ControlArtifactDraft | None = None
    artifact_drafts: list[ControlArtifactDraft] = Field(default_factory=list, max_length=12)
    target_ref: ControlTargetRef | None = None
    goal_update: ControlGoalUpdate | None = None
    capability_config: dict[str, int | float | bool | str | list[str]] = Field(
        default_factory=dict
    )
    source_artifact_version_ids: list[str] = Field(default_factory=list, max_length=12)
    skill_confirmed: bool = Field(default=False, exclude=True)


class AgentExecutionRequest(BaseModel):
    project_id: str = Field(min_length=1)
    conversation_id: str = Field(min_length=1)
    request: dict[str, Any]
    idempotency_key: str = Field(min_length=1)
    agent_turn_id: str = Field(default="", min_length=0)
    dispatch_generation: int = Field(default=0, ge=0, strict=True)
    run_state: dict[str, Any] | None = None
    additional_inputs: list[dict[str, Any]] = Field(default_factory=list, max_length=128)
    approval_decisions: list["AgentToolApprovalDecision"] = Field(
        default_factory=list, max_length=32
    )


class AgentToolApprovalDecision(BaseModel):
    sdk_tool_call_id: str = Field(min_length=1, max_length=256)
    action: Literal["approve", "reject"]


class ArtifactValidationContract(BaseModel):
    response_schema: dict[str, Any]
    context_hash: str = Field(min_length=1)
    output_key: str = ""
    output_bundle: dict[str, dict[str, Any]] = Field(default_factory=dict)


class ArtifactRevisionRequest(BaseModel):
    revision_request_id: str = Field(min_length=1)
    revision_attempt_id: str = Field(min_length=1)
    instruction: str = Field(min_length=1)
    target: dict[str, Any]
    artifact_schema: dict[str, Any]
    artifact_payload: dict[str, Any]
    artifact_validation: ArtifactValidationContract | None = None


class ArtifactRevisionResponse(BaseModel):
    outcome: Literal["proposed", "no_change"] = "proposed"
    proposal_payload: dict[str, Any]
    proposal_summary: str
    provider_id: str
    trace_ref: str
    changed_paths: list[str] = Field(default_factory=list)


class SkillRoutingDecision(BaseModel):
    applicable_capability_ids: list[str] = Field(default_factory=list, max_length=8)
    reason: str = ""


class ArtifactSourceRoutingDecision(BaseModel):
    artifact_titles: list[str] = Field(default_factory=list, max_length=12)
    needs_clarification: bool = False
    reason: str = ""
