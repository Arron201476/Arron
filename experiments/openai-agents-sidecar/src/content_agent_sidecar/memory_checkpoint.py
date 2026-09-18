"""Private, identity-bound checkpoints for paused native SDK memory stages."""

from hashlib import sha256
import json
from typing import Any, Literal

from agents import RunState
from agents.agent_output import AgentOutputSchema, AgentOutputSchemaBase
from agents.result import RunResultBase, RunResultStreaming
from pydantic import BaseModel, ConfigDict, Field

from .native_memory import MemoryStage
from .native_live_state import NativeWorkspaceCheckpoint


MAX_MEMORY_CHECKPOINT_BYTES = 4 << 20


class MemoryGenerationBinding(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True, hide_input_in_errors=True)

    generation_id: str = Field(min_length=1, max_length=256)
    phase: Literal["extraction", "consolidation"]
    project_id: str = Field(min_length=1, max_length=256)
    user_id: str = Field(min_length=1, max_length=256)
    source_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    base_version: int = Field(ge=0)
    base_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    model_id: str = Field(min_length=1, max_length=256)
    # Supplied by trusted worker configuration, never inferred from saved state.
    policy_hash: str = Field(pattern=r"^[a-f0-9]{64}$")


class MemoryStageCheckpoint(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True, hide_input_in_errors=True)

    schema_version: Literal["agent_memory_checkpoint.v1"] = "agent_memory_checkpoint.v1"
    binding: MemoryGenerationBinding
    stage_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    state_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    state_json: str = Field(min_length=1, repr=False)
    native_workspace: NativeWorkspaceCheckpoint | None = Field(default=None, repr=False)
    pause_kind: Literal["approval", "user"] = "approval"


def _encode(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True, allow_nan=False)


def _digest(value: str) -> str:
    return sha256(value.encode("utf-8")).hexdigest()


def _stage_hash(stage: MemoryStage, binding: MemoryGenerationBinding) -> str:
    expected_name = {"extraction": "sandbox-memory-phase-one", "consolidation": "sandbox-memory-phase-two"}[binding.phase]
    if stage.agent.name != expected_name or stage.agent.handoffs or not isinstance(stage.agent.instructions, str):
        raise ValueError("Memory checkpoint requires the original frozen stage")
    if stage.agent.model is None or isinstance(stage.agent.model, str) and stage.agent.model != binding.model_id:
        raise ValueError("Memory checkpoint model does not match the generation")
    if stage.agent.tool_use_behavior != "run_llm_again":
        raise ValueError("Memory checkpoint requires the original tool execution policy")
    output = stage.agent.output_type
    schema = output if isinstance(output, AgentOutputSchemaBase) else AgentOutputSchema(output or str)
    tools = [{"type": type(tool).__name__, "name": getattr(tool, "name", None),
              "parameters": getattr(tool, "params_json_schema", None),
              "approval": "callback" if callable(getattr(tool, "needs_approval", False)) else getattr(tool, "needs_approval", False)}
             for tool in stage.agent.tools]
    return _digest(_encode({"name": stage.agent.name, "instructions": stage.agent.instructions,
                           "input": stage.input, "policy_hash": binding.policy_hash,
                           "model_settings": stage.agent.model_settings.to_json_dict(), "tools": tools,
                           "output": None if schema.is_plain_text() else schema.json_schema(),
                           "output_strict": schema.is_strict_json_schema(),
                           "reset_tool_choice": stage.agent.reset_tool_choice}))


def checkpoint_memory_stage(result: RunResultBase, stage: MemoryStage,
                            binding: MemoryGenerationBinding, *, user_requested: bool = False) -> MemoryStageCheckpoint:
    binding = MemoryGenerationBinding.model_validate(binding.model_dump())
    stage_hash = _stage_hash(stage, binding)
    if not isinstance(result, RunResultBase) or result.last_agent is not stage.agent:
        raise ValueError("Memory checkpoint requires its original SDK run result")
    if isinstance(result, RunResultStreaming) and (not result.is_complete or result.run_loop_task is not None and not result.run_loop_task.done()):
        raise ValueError("Memory checkpoint cannot freeze a running stream")
    state = result.to_state()
    if type(user_requested) is not bool:
        raise ValueError("Memory pause intent must be explicit")
    graceful = (user_requested and isinstance(result, RunResultStreaming)
                and result._cancel_mode == "after_turn")
    if user_requested and not graceful:
        raise ValueError("Memory user pause requires a settled SDK turn boundary")
    if (not state.get_interruptions() and not graceful) or result.final_output is not None:
        raise ValueError("Memory checkpoint requires a paused native SDK stage")
    payload = state.to_json(context_serializer=lambda _: {}, strict_context=True)
    if payload.get("original_input") != stage.input:
        raise ValueError("Memory checkpoint input does not match the original stage")
    # SDK mapping contexts bypass context_serializer. Runtime credentials must
    # always come from a newly admitted worker, while SDK approvals stay intact.
    payload["context"]["context"] = {}
    encoded = _encode(payload)
    context = result.context_wrapper.context
    workspace = getattr(context, "native_workspace_checkpoint", None)
    owner = getattr(context, "native_workspace", None)
    if owner is not None and (workspace is None or getattr(owner, "_settled", False) is not True):
        raise ValueError("Memory workspace has no settled recovery checkpoint")
    if workspace is not None:
        if type(workspace) is not NativeWorkspaceCheckpoint:
            raise ValueError("Memory workspace recovery reference is invalid")
        workspace = NativeWorkspaceCheckpoint.model_validate(workspace.model_dump())
    checkpoint = MemoryStageCheckpoint(binding=binding, stage_hash=stage_hash,
                                       state_hash=_digest(encoded), state_json=encoded, native_workspace=workspace,
                                       pause_kind="user" if user_requested else "approval")
    if len(checkpoint.model_dump_json().encode("utf-8")) > MAX_MEMORY_CHECKPOINT_BYTES:
        raise ValueError("Memory checkpoint exceeds the durable storage limit")
    return checkpoint


async def restore_memory_stage(checkpoint: MemoryStageCheckpoint, stage: MemoryStage,
                               binding: MemoryGenerationBinding, *, context: Any) -> RunState:
    checked = MemoryStageCheckpoint.model_validate(checkpoint.model_dump())
    expected = MemoryGenerationBinding.model_validate(binding.model_dump())
    if context is None or checked.binding != expected or checked.stage_hash != _stage_hash(stage, expected):
        raise ValueError("Memory checkpoint does not match the admitted generation")
    if getattr(context, "native_workspace_checkpoint", None) != checked.native_workspace:
        raise ValueError("Memory checkpoint does not match the admitted native workspace")
    if len(checked.model_dump_json().encode("utf-8")) > MAX_MEMORY_CHECKPOINT_BYTES or _digest(checked.state_json) != checked.state_hash:
        raise ValueError("Memory checkpoint failed integrity validation")
    try:
        payload = json.loads(checked.state_json)
        if payload["original_input"] != stage.input or payload["context"]["context"] != {}:
            raise ValueError("binding")
        restored = await RunState.from_json(stage.agent, payload, context_override=context, strict_context=True)
        if checked.pause_kind == "approval" and not restored.get_interruptions():
            raise ValueError("not paused")
    except Exception:
        # SDK parse failures may include private state. Do not expose diagnostics.
        raise ValueError("Memory SDK checkpoint could not be restored") from None
    return restored
