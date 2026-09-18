import asyncio
import json
from io import BytesIO

import pytest
from agents import Agent

from content_agent_sidecar.memory_input_plan import _PreparationSession, plan_memory_consolidation
from content_agent_sidecar.native_files import _go_json_hash
from test_memory_consolidation import fixture


def test_input_plan_uses_sdk_selection_without_changing_baseline_or_workspace():
    async def run():
        session, _, rollout, original, options = await fixture()
        baseline = original.model_copy(update={"content_hash": _go_json_hash(original.files)})
        before = baseline.model_dump()
        plan = await plan_memory_consolidation(Agent(name="template", model="configured"), rollout, original, baseline, **options)
        assert baseline.model_dump() == before and session.writes == []
        assert plan.baseline_version == baseline.version and plan.baseline_hash == baseline.content_hash
        assert plan.content_hash == _go_json_hash(plan.files)
        assert ".agent-memory/memory_summary.md" not in plan.files
        assert ".agent-memory/phase_two_selection.json" not in plan.files
        assert "Persisted raw memory" in plan.files[".agent-memory/raw_memories.md"]
        selected = plan.prepared.selection.selected
        record = json.loads(plan.files[".agent-memory-input/phase_two_selection.json"])
        assert record["version"] == 1
        assert record["selected"] == [item.to_dict() for item in selected]
        assert record["updated_at"] == json.loads(rollout.rollout_jsonl)["updated_at"]
        assert len(selected) == 1
        assert plan.files[selected[0].rollout_path] == rollout.rollout_jsonl
        assert plan.files[".agent-memory/" + selected[0].rollout_summary_file].endswith("Persisted summary\n")
        again = await plan_memory_consolidation(Agent(name="template", model="configured"), rollout, original, baseline, **options)
        assert again.files == plan.files and again.content_hash == plan.content_hash
    asyncio.run(run())


@pytest.mark.parametrize("fault", ["owner", "hash", "disabled", "path"])
def test_plan_rejects_unconfirmed_private_baseline(fault):
    async def run():
        _, _, rollout, source, options = await fixture()
        baseline = source.model_copy(update={"content_hash": _go_json_hash(source.files)})
        if fault == "owner": baseline = baseline.model_copy(update={"user_id": "other"})
        if fault == "hash": baseline = baseline.model_copy(update={"content_hash": "0" * 64})
        if fault == "disabled": baseline = baseline.model_copy(update={"read_enabled": False})
        if fault == "path":
            files = {"../escape.txt": "private"}
            baseline = baseline.model_copy(update={"files": files, "content_hash": _go_json_hash(files)})
        with pytest.raises(ValueError):
            await plan_memory_consolidation(Agent(name="template", model="configured"), rollout, source, baseline, **options)
    asyncio.run(run())


def test_planning_session_is_not_a_shell_or_unbounded_filesystem():
    async def run():
        session = _PreparationSession({})
        for path in ("/tmp/private", "../escape", "project.txt", ".agent-memory/../outside"):
            with pytest.raises(ValueError):
                await session.write(path, BytesIO(b"private"))
        with pytest.raises(ValueError, match="commands"):
            await session.exec("rm", "-rf", ".agent-memory", shell=False)
        with pytest.raises(ValueError, match="bounded"):
            await session.write(".agent-memory/large", BytesIO(b"x" * ((16 << 20) + 1)))
        assert not session.files
    asyncio.run(run())
