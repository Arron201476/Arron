import asyncio
from hashlib import sha256
from io import BytesIO
import json
from pathlib import Path
from types import SimpleNamespace

import pytest
from agents import Agent
from agents.sandbox.config import MemoryLayoutConfig
from agents.sandbox.memory.interface import RolloutExtractionArtifacts
from agents.sandbox.memory.storage import SandboxMemoryStorage

from content_agent_sidecar.memory_consolidation import prepare_memory_consolidation
from content_agent_sidecar.memory_rollout import capture_memory_rollout
from test_memory_rollout import completed, source


class MemorySession:
    def __init__(self):
        self.files = {}
        self.writes = []
        self.corrupt = False
        self.list_failure = False
        self.corrupt_aggregate = False

    def normalize_path(self, path):
        return Path(path).as_posix()

    async def mkdir(self, path, *, parents):
        pass

    async def exec(self, *args, **kwargs):
        return SimpleNamespace(ok=lambda: str(args[-1]) in self.files)

    async def write(self, path, handle):
        key = self.normalize_path(path)
        self.files[key] = handle.read()
        self.writes.append(key)

    async def read(self, path):
        key = self.normalize_path(path)
        if key not in self.files:
            raise FileNotFoundError(key)
        if self.corrupt and key.endswith(".jsonl"):
            return BytesIO(b"corrupt")
        if self.corrupt_aggregate and key.endswith("/raw_memories.md"):
            return BytesIO(b"incomplete aggregate")
        return BytesIO(self.files[key])

    async def ls(self, path):
        if self.list_failure:
            raise OSError("private workspace unavailable")
        prefix = self.normalize_path(path) + "/"
        return [SimpleNamespace(path=key, is_dir=lambda: False) for key in self.files
                if key.startswith(prefix) and "/" not in key[len(prefix):]]


async def fixture():
    original = source()
    rollout = capture_memory_rollout(await completed(), original, segment_id="segment-1")
    output = RolloutExtractionArtifacts(rollout_slug="fixture.md", rollout_summary="Persisted summary", raw_memory="Persisted raw memory")
    digest = sha256(json.dumps(output.model_dump(), ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()
    session = MemorySession()
    storage = SandboxMemoryStorage(session=session, layout=MemoryLayoutConfig())
    args = dict(segment_id="segment-1", extraction=output, extraction_hash=digest, session_id="private-session", max_raw_memories=10)
    return session, storage, rollout, original, args


def test_consolidation_uses_sdk_storage_and_original_persisted_input():
    async def run():
        session, storage, rollout, original, args = await fixture()
        template = Agent(name="platform", model="configured", instructions="Frozen policy")
        prepared = await prepare_memory_consolidation(template, storage, rollout, original, **args)
        assert len(prepared.selection.selected) == 1
        selected = prepared.selection.selected[0]
        assert selected.rollout_id == json.loads(rollout.rollout_jsonl)["rollout_id"]
        assert selected.rollout_summary_file.endswith("_fixture.md")
        assert "Persisted raw memory" in (await storage.read_text(storage.memories_dir / "raw_memories.md"))
        assert await storage.read_text(storage.sessions_dir / f"{selected.rollout_id}.jsonl") == rollout.rollout_jsonl
        assert selected.rollout_id in prepared.stage.input
        assert prepared.stage.agent.model == "configured"
        assert "Frozen policy" in prepared.stage.agent.instructions
        assert storage.phase_two_selection_path.as_posix() not in session.files
        again = await prepare_memory_consolidation(template, storage, rollout, original, **args)
        assert again.selection == prepared.selection
    asyncio.run(run())


@pytest.mark.parametrize("failure", ["source", "hash", "count", "session", "agent", "write", "list", "aggregate"])
def test_consolidation_does_not_start_from_unconfirmed_inputs(failure):
    async def run():
        session, storage, rollout, original, args = await fixture()
        template = Agent(name="platform", model="configured")
        if failure == "source": original = original.model_copy(update={"user_id": "other-user"})
        if failure == "hash": args["extraction_hash"] = "f" * 64
        if failure == "count": args["max_raw_memories"] = 0
        if failure == "session": args["session_id"] = "bad\nmetadata"
        if failure == "agent": template = Agent(name="unconfigured")
        if failure == "write": session.corrupt = True
        if failure == "list": session.list_failure = True
        if failure == "aggregate": session.corrupt_aggregate = True
        with pytest.raises(ValueError):
            await prepare_memory_consolidation(template, storage, rollout, original, **args)
        if failure not in {"write", "list", "aggregate"}: assert session.writes == []
        assert storage.phase_two_selection_path.as_posix() not in session.files
    asyncio.run(run())
