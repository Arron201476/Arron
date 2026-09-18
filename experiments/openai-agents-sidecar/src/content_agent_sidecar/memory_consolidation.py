"""Prepare persisted extraction artifacts using the pinned SDK memory layout."""

from dataclasses import dataclass
from hashlib import sha256
import json
from pathlib import PurePosixPath

from agents import Agent
from agents.sandbox.memory.interface import RolloutExtractionArtifacts
from agents.sandbox.memory.manager import _format_raw_memory, _format_rollout_summary
from agents.sandbox.memory.phase_one import normalize_rollout_slug
from agents.sandbox.memory.storage import PhaseTwoInputSelection, SandboxMemoryStorage

from .managed_memory import MemorySnapshot
from .memory_rollout import MemoryRollout
from .native_files import portable_file_path
from .native_memory import MemoryStage, extraction_has_memory, memory_consolidation_stage


@dataclass(frozen=True)
class PreparedMemoryConsolidation:
    stage: MemoryStage
    selection: PhaseTwoInputSelection


async def prepare_memory_consolidation(template: Agent, storage: SandboxMemoryStorage,
                                       rollout: MemoryRollout, source: MemorySnapshot, *,
                                       segment_id: str, extraction: RolloutExtractionArtifacts,
                                       extraction_hash: str, session_id: str,
                                       max_raw_memories: int, extra_prompt: str | None = None) -> PreparedMemoryConsolidation:
    original = rollout.verified_input(source, segment_id=segment_id)
    values = RolloutExtractionArtifacts.model_validate(extraction.model_dump(), strict=True)
    encoded = json.dumps(values.model_dump(), ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode("utf-8")
    if len(encoded) > 4 << 20 or sha256(encoded).hexdigest() != extraction_hash or not extraction_has_memory(values):
        raise ValueError("Memory consolidation requires the confirmed nonempty extraction")
    if (type(max_raw_memories) is not int or not 1 <= max_raw_memories <= 256
            or not session_id or len(session_id) > 256 or any(ord(char) < 32 for char in session_id)):
        raise ValueError("Memory consolidation requires a bounded selection and session identity")
    memory_root = str(storage.memories_dir).replace("\\", "/")
    sessions_root = str(storage.sessions_dir).replace("\\", "/")
    if not portable_file_path(memory_root) or not portable_file_path(sessions_root):
        raise ValueError("Memory consolidation storage roots are invalid")
    # Validate the Agent before any writes. The storage must be in the admitted
    # generation's private workspace; this adapter does not grant workspace access.
    memory_consolidation_stage(template, memory_root, PhaseTwoInputSelection([], set(), []), extra_prompt=extra_prompt)
    payload = json.loads(original)
    rollout_id = payload["rollout_id"]
    summary_file = f"rollout_summaries/{rollout_id}_{normalize_rollout_slug(values.rollout_slug)}.md"
    rollout_path = str(PurePosixPath(sessions_root) / f"{rollout_id}.jsonl")
    updated_at = payload["updated_at"]
    terminal = payload["terminal_metadata"]["terminal_state"]
    files = {
        storage.sessions_dir / f"{rollout_id}.jsonl": original,
        storage.raw_memories_dir / f"{rollout_id}.md": _format_raw_memory(
            updated_at=updated_at, rollout_id=rollout_id, rollout_path=rollout_path,
            rollout_summary_file=summary_file, terminal_state=terminal, raw_memory=values.raw_memory),
        storage.memories_dir / summary_file: _format_rollout_summary(
            updated_at=updated_at, rollout_path=rollout_path, session_id=session_id,
            terminal_state=terminal, rollout_summary=values.rollout_summary),
    }
    await storage.ensure_layout()
    for path, contents in files.items():
        await storage.write_text(path, contents)
        if await storage.read_text(path) != contents:
            raise ValueError("Memory consolidation input write was not confirmed")
    selection = await storage.build_phase_two_input_selection(max_raw_memories_for_consolidation=max_raw_memories)
    ids = [item.rollout_id for item in selection.selected]
    if rollout_id not in ids or len(ids) != len(set(ids)):
        raise ValueError("Memory consolidation selection omitted or duplicated its input")
    for item in [*selection.selected, *selection.removed]:
        if (not portable_file_path(item.rollout_summary_file)
                or not item.rollout_summary_file.startswith("rollout_summaries/")
                or not portable_file_path(item.rollout_id) or "/" in item.rollout_id
                or item.rollout_path != str(PurePosixPath(sessions_root) / f"{item.rollout_id}.jsonl")):
            raise ValueError("Memory consolidation selection contains invalid resource paths")
    chunks = []
    total_bytes = 0
    for item in selection.selected:
        chunk = (await storage.read_text(storage.raw_memories_dir / f"{item.rollout_id}.md")).rstrip("\n")
        await storage.read_text(storage.memories_dir / item.rollout_summary_file)
        total_bytes += len(chunk.encode("utf-8"))
        if total_bytes > 16 << 20:
            raise ValueError("Memory consolidation selected inputs exceed their bound")
        chunks.append(chunk)
    if not await storage.rebuild_raw_memories(selected_items=selection.selected):
        raise ValueError("Memory consolidation has no confirmed raw memory inputs")
    if await storage.read_text(storage.memories_dir / "raw_memories.md") != "\n\n".join(chunks):
        raise ValueError("Memory consolidation aggregate differs from its selected inputs")
    stage = memory_consolidation_stage(template, memory_root, selection, extra_prompt=extra_prompt)
    # Do not update phase_two_selection.json until the caller confirms completion.
    return PreparedMemoryConsolidation(stage=stage, selection=selection)
