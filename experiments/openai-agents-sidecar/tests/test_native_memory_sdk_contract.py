"""Pinned SDK behavior probes, not platform Memory acceptance tests."""

import asyncio
from io import BytesIO
from types import SimpleNamespace
from unittest.mock import AsyncMock

from agents import Runner
from agents.run_config import RunConfig
from agents.sandbox.capabilities import Memory
from agents.sandbox.config import MemoryGenerateConfig
from agents.sandbox.manifest import Manifest
from agents.sandbox.memory.manager import SandboxMemoryGenerationManager
from agents.sandbox.memory.phase_two import run_phase_two


def test_native_memory_reads_the_session_summary_without_generating():
    async def run():
        handle = BytesIO(b"Prefer concise scene descriptions.")
        session = SimpleNamespace(read=AsyncMock(return_value=handle))
        memory = Memory(generate=None)
        memory.session = session
        instructions = await memory.instructions(Manifest())
        assert "Prefer concise scene descriptions." in instructions
        assert "memories" in instructions
        assert handle.closed
        assert session.read.await_args.args[0].as_posix() == "memories/memory_summary.md"
        assert memory.required_capability_types() == {"filesystem", "shell"}

    asyncio.run(run())


def test_native_memory_close_does_not_propagate_extraction_failure():
    async def run():
        hooks = []
        class Session:
            def register_pre_stop_hook(self, hook):
                hooks.append(hook)

        manager = SandboxMemoryGenerationManager(
            session=Session(), memory=Memory()
        )
        manager._storage = SimpleNamespace(ensure_layout=AsyncMock())
        manager._rollout_files_by_rollout_id["rollout-1"] = "rollout-1.jsonl"
        manager._process_rollout_file = AsyncMock(side_effect=RuntimeError("extraction failed"))
        # The SDK calls this during close. A normal return is not proof of generation.
        await hooks[0]()
        manager._process_rollout_file.assert_awaited_once_with("rollout-1.jsonl")
        assert manager._pending_phase_two_rollout_ids == []
        await manager.flush()
        assert manager._process_rollout_file.await_count == 1

    asyncio.run(run())


def test_native_memory_consolidation_creates_its_own_unwrapped_agent(monkeypatch):
    async def run():
        captured = AsyncMock()
        monkeypatch.setattr(Runner, "run", captured)
        config = MemoryGenerateConfig(phase_two_model="approved-model-fixture")
        # The prompt renderer only needs the selection's rendered fields.
        monkeypatch.setattr(
            "agents.sandbox.memory.phase_two.render_memory_consolidation_prompt",
            lambda **_kwargs: "consolidate fixture",
        )
        run_config = RunConfig()
        await run_phase_two(config=config, memory_root="memories", selection=None, run_config=run_config)
        agent = captured.await_args.args[0]
        assert agent.model == "approved-model-fixture"
        assert agent.hooks is None
        assert captured.await_args.kwargs["run_config"] is run_config
        assert captured.await_args.kwargs["max_turns"] == 500
        assert {capability.type for capability in agent.capabilities} >= {"filesystem", "shell"}
        assert all(getattr(capability, "configure_tools", None) is None for capability in agent.capabilities)

    asyncio.run(run())
