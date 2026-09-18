"""SDK memory stage assembly; execution and persistence remain caller-owned."""

from dataclasses import dataclass
from hashlib import sha256
import json
from pathlib import PurePosixPath, PureWindowsPath

from agents import Agent
from agents.sandbox.memory.interface import RolloutExtractionArtifacts
from agents.sandbox.memory.phase_one import normalize_rollout_slug, render_phase_one_prompt, validate_rollout_artifacts
from agents.sandbox.memory.prompts import (
    render_memory_consolidation_prompt,
    render_rollout_extraction_prompt,
)
from agents.sandbox.memory.storage import PhaseTwoInputSelection

from .managed_memory import MemorySnapshot
from .memory_rollout import MemoryRollout
from .backend import BackendError


@dataclass(frozen=True)
class MemoryStage:
    agent: Agent
    input: str


def _stage_agent(template: Agent, *, name: str, instructions: str, output_type=None) -> Agent:
    if type(template) is not Agent:
        raise ValueError("Memory stages require an unbound platform Agent template")
    if template.model is None:
        raise ValueError("Memory requires the caller's explicitly configured model")
    if template.handoffs:
        raise ValueError("Memory stages cannot inherit business handoffs")
    if callable(template.instructions):
        raise ValueError("Memory stage policy instructions must be frozen before assembly")
    return template.clone(
        name=name,
        instructions="\n\n".join(part for part in (template.instructions, instructions) if part),
        output_type=output_type,
        tool_use_behavior="run_llm_again",
    )


def memory_extraction_stage(template: Agent, rollout_contents: str, *, extra_prompt: str | None = None) -> MemoryStage:
    return MemoryStage(
        agent=_stage_agent(template, name="sandbox-memory-phase-one",
                           instructions=render_rollout_extraction_prompt(extra_prompt=extra_prompt),
                           output_type=RolloutExtractionArtifacts),
        input=render_phase_one_prompt(rollout_contents=rollout_contents),
    )


def memory_extraction_from_source(template: Agent, rollout: MemoryRollout, source: MemorySnapshot,
                                  *, segment_id: str, extra_prompt: str | None = None) -> MemoryStage:
    return memory_extraction_stage(template, rollout.verified_input(source, segment_id=segment_id),
                                   extra_prompt=extra_prompt)


def memory_consolidation_stage(template: Agent, memory_root: str, selection: PhaseTwoInputSelection,
                               *, extra_prompt: str | None = None) -> MemoryStage:
    # Match the SDK Memory layout validation on both Windows and POSIX hosts.
    path = PurePosixPath(memory_root)
    if (not memory_root or not path.parts or path.is_absolute() or ".." in path.parts
            or "\\" in memory_root or PureWindowsPath(memory_root).drive):
        raise ValueError("Memory consolidation requires a relative workspace directory")
    return MemoryStage(
        agent=_stage_agent(template, name="sandbox-memory-phase-two", instructions=""),
        input=render_memory_consolidation_prompt(memory_root=memory_root, selection=selection,
                                                extra_prompt=extra_prompt),
    )


def extraction_has_memory(value: object) -> bool:
    if not isinstance(value, RolloutExtractionArtifacts):
        raise ValueError("Memory extraction did not return the SDK output contract")
    has_memory = validate_rollout_artifacts(value)
    if has_memory:
        try:
            normalize_rollout_slug(value.rollout_slug)
        except ValueError:
            raise ValueError("Memory extraction rollout slug is invalid for SDK storage") from None
    return has_memory


async def persist_memory_extraction(backend, *, generation_id: str, worker_id: str,
                                    attempt_token: str, attempt: int,
                                    output: RolloutExtractionArtifacts) -> dict:
    has_memory = extraction_has_memory(output)
    values = RolloutExtractionArtifacts.model_validate(output.model_dump(), strict=True).model_dump()
    raw = json.dumps(values, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode("utf-8")
    if len(raw) > 4 << 20:
        raise ValueError("Memory extraction exceeds the durable output limit")
    expected = {"generation_id": generation_id, "content_hash": sha256(raw).hexdigest(), "has_memory": has_memory}
    result = await backend.memory_generation_request("extraction", {
        "generation_id": generation_id, "worker_id": worker_id,
        "attempt_token": attempt_token, "attempt": attempt, "output": values,
    })
    receipt = result.get("receipt")
    if not isinstance(receipt, dict) or receipt != expected or type(receipt.get("has_memory")) is not bool:
        raise BackendError("Memory extraction persistence receipt does not match its original output")
    return receipt
