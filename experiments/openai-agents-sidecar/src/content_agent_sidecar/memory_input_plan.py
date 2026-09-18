"""Bounded SDK preparation without granting unapproved workspace writes."""

from dataclasses import dataclass, replace
from io import BytesIO
from pathlib import Path
from hashlib import sha256
import json

from agents.sandbox.config import MemoryLayoutConfig
from agents.sandbox.files import FileEntry
from agents.sandbox.memory.storage import SandboxMemoryStorage
from agents.sandbox.types import ExecResult, Permissions

from .memory_consolidation import PreparedMemoryConsolidation, prepare_memory_consolidation
from .native_files import _go_json_hash, portable_file_path
from .native_manifest import GENERATION_DIRECTORY, MEMORY_DIRECTORY
from .backend import BackendError
from .native_memory import MemoryStage, memory_consolidation_stage
from agents.sandbox.memory.storage import PhaseTwoInputSelection


class _PreparationSession:
    """Only SDK storage's data IO subset; no host filesystem or shell access."""

    def __init__(self, files):
        self.files = dict(files)
        self.original = dict(files)
        self._check_size()

    def _check_size(self):
        if len(self.files) > 256 or sum(len(value) for value in self.files.values()) > 16 << 20:
            raise ValueError("Memory preparation exceeds its file budget")

    def normalize_path(self, path):
        value = Path(path).as_posix()
        if not portable_file_path(value) or value.split("/")[0] not in {MEMORY_DIRECTORY, GENERATION_DIRECTORY}:
            raise ValueError("Memory preparation left its private namespace")
        return Path(value)

    async def mkdir(self, path, *, parents):
        self.normalize_path(path)

    async def exec(self, *args, **kwargs):
        if len(args) != 3 or args[:2] != ("test", "-f") or kwargs != {"shell": False}:
            raise ValueError("Memory preparation cannot execute commands")
        key = self.normalize_path(args[2]).as_posix()
        return ExecResult(stdout=b"", stderr=b"", exit_code=0 if key in self.files else 1)

    async def read(self, path):
        key = self.normalize_path(path).as_posix()
        if key not in self.files:
            raise FileNotFoundError(key)
        return BytesIO(self.files[key])

    async def write(self, path, handle):
        key = self.normalize_path(path).as_posix()
        data = handle.read((16 << 20) + 1)
        if not isinstance(data, bytes) or len(data) > 16 << 20:
            raise ValueError("Memory preparation requires bounded bytes")
        self.files[key] = data
        self._check_size()

    async def ls(self, path):
        prefix = self.normalize_path(path).as_posix() + "/"
        return [FileEntry(path=key, permissions=Permissions.from_mode(0o600), owner="private", group="private", size=len(value))
                for key, value in sorted(self.files.items()) if key.startswith(prefix) and "/" not in key[len(prefix):]]


@dataclass(frozen=True)
class RecoveredMemoryConsolidation:
    # Selection history is not reconstructed from a partial publication record.
    stage: MemoryStage


@dataclass(frozen=True)
class MemoryInputPlan:
    prepared: PreparedMemoryConsolidation | RecoveredMemoryConsolidation
    files: dict[str, str]
    content_hash: str
    baseline_version: int
    baseline_hash: str


def recover_memory_input_plan(template, claim):
    """Reuse frozen files and the original SDK input, never reselect a baseline."""
    try:
        frozen, checkpoint = claim.input_plan, claim.checkpoint
        if frozen is None or checkpoint is None or checkpoint.native_workspace is None:
            raise ValueError("missing recovery inputs")
        if sha256(checkpoint.state_json.encode()).hexdigest() != checkpoint.state_hash:
            raise ValueError("state hash")
        state = json.loads(checkpoint.state_json)
        original_input = state["original_input"]
        if not isinstance(original_input, str) or not original_input or state["context"]["context"] != {}:
            raise ValueError("original input")
        stage = memory_consolidation_stage(template, MEMORY_DIRECTORY, PhaseTwoInputSelection([], set(), []))
        prepared = RecoveredMemoryConsolidation(replace(stage, input=original_input))
        plan = MemoryInputPlan(prepared, dict(frozen.files), frozen.content_hash, frozen.baseline_version, frozen.baseline_hash)
        value = memory_input_plan_payload(claim, plan)
        if value != frozen.model_dump() or _go_json_hash(value) != claim.input_plan_hash:
            raise ValueError("plan hash")
        return plan
    except (ValueError, TypeError, KeyError, AttributeError):
        raise BackendError("Memory recovery requires its confirmed frozen input plan and SDK checkpoint") from None


async def plan_memory_consolidation(template, rollout, source, baseline, **options):
    """Compute exact SDK-derived writes; this is not a write authorization."""
    if any(getattr(baseline, key) != getattr(source, key) for key in ("project_id", "user_id", "workspace_id")):
        raise ValueError("Memory preparation baseline belongs to another owner")
    if baseline.read_enabled:
        if _go_json_hash(dict(sorted(baseline.files.items()))) != baseline.content_hash:
            raise ValueError("Memory preparation baseline content is unconfirmed")
    elif baseline.files:
        raise ValueError("Disabled memory baseline cannot supply private files")
    initial = {}
    for name, text in baseline.files.items():
        if not portable_file_path(name) or "\0" in text:
            raise ValueError("Memory preparation baseline path or content is invalid")
        initial[f"{MEMORY_DIRECTORY}/{name}"] = text.encode("utf-8")
    session = _PreparationSession(initial)
    storage = SandboxMemoryStorage(session=session, layout=MemoryLayoutConfig(
        sessions_dir=GENERATION_DIRECTORY, memories_dir=MEMORY_DIRECTORY))
    prepared = await prepare_memory_consolidation(template, storage, rollout, source, **options)
    # Keep SDK selection metadata unpublished and stable across approval retries.
    selection_path = storage.phase_two_selection_path.as_posix()
    prior = session.files.get(selection_path)
    await storage.write_phase_two_selection(selected_items=prepared.selection.selected)
    selection = json.loads(session.files.pop(selection_path))
    if prior is not None:
        session.files[selection_path] = prior
    selection["updated_at"] = json.loads(rollout.rollout_jsonl)["updated_at"]
    session.files[f"{GENERATION_DIRECTORY}/phase_two_selection.json"] = (json.dumps(selection, indent=2) + "\n").encode()
    files = {path: body.decode("utf-8") for path, body in sorted(session.files.items()) if session.original.get(path) != body}
    return MemoryInputPlan(prepared=prepared, files=files, content_hash=_go_json_hash(files),
                           baseline_version=baseline.version, baseline_hash=baseline.content_hash)


def memory_input_plan_payload(claim, plan: MemoryInputPlan):
    if (claim.job.phase != "consolidation" or claim.extraction is None
            or plan.baseline_version != claim.job.base_version or plan.baseline_hash != claim.job.base_hash
            or plan.content_hash != _go_json_hash(plan.files)):
        raise BackendError("Memory input plan differs from its admitted consolidation")
    extraction = json.dumps(claim.extraction.model_dump(), ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()
    value = {"baseline_version": plan.baseline_version, "baseline_hash": plan.baseline_hash,
             "source_hash": claim.job.source_hash, "extraction_hash": sha256(extraction).hexdigest(),
             "content_hash": plan.content_hash, "files": dict(sorted(plan.files.items()))}
    # Go persists this as a map, unlike native file/PTY struct receipts whose
    # field order must be preserved by the shared JSON hash helper.
    return dict(sorted(value.items()))


async def persist_memory_input_plan(backend, claim, worker_id, plan: MemoryInputPlan):
    value = memory_input_plan_payload(claim, plan)
    expected = {"generation_id": claim.job.generation_id, "plan_hash": _go_json_hash(value), "content_hash": plan.content_hash}
    response = await backend.memory_generation_request("inputs", {"generation_id": claim.job.generation_id,
        "worker_id": worker_id, "attempt": claim.job.attempt, "attempt_token": claim.attempt_token, "input_plan": value})
    if response.get("receipt") != expected:
        raise BackendError("Memory input plan persistence was not confirmed")
    return expected
