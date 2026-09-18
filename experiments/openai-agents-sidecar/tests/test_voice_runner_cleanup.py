import asyncio
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest

from content_agent_sidecar import native_voice
from content_agent_sidecar.runtime import OpenAIAgentsRuntime


def fixture():
    request = SimpleNamespace(project_id="p", conversation_id="c", audio={}, session_id="s", generation=1)
    bridge = SimpleNamespace(prepare_turn=AsyncMock(), persist_event=AsyncMock())
    terminal = {"session_id": "s", "generation": 1, "sequence": 1, "payload": {"type": "session_ended"}}
    return request, bridge, terminal


@pytest.mark.parametrize("owned", [True, False])
def test_terminal_waits_for_pipeline_and_owned_runtime_cleanup(monkeypatch, owned):
    async def run():
        request, bridge, terminal = fixture()
        order = []

        async def pipeline(*args, **kwargs):
            await args[6](terminal)
            order.append("pipeline_closed")

        async def close():
            order.append("runtime_closed")

        async def send(frame):
            assert frame == terminal
            order.append("terminal_sent")

        monkeypatch.setattr(native_voice, "run_configured_voice_turn", pipeline)
        runtime = SimpleNamespace(close=AsyncMock(side_effect=close))
        await native_voice.configured_voice_runner(lambda: runtime, owns_runtime=owned)(request, send, bridge)
        assert order == (["pipeline_closed", "runtime_closed", "terminal_sent"] if owned else ["pipeline_closed", "terminal_sent"])
        assert runtime.close.await_count == int(owned)
    asyncio.run(run())


@pytest.mark.parametrize("failure", ["pipeline", "close", "missing_terminal", "late_output"])
def test_cleanup_failures_never_emit_success(monkeypatch, failure):
    async def run():
        request, bridge, terminal = fixture()

        async def pipeline(*args, **kwargs):
            if failure == "missing_terminal":
                return
            await args[6](terminal)
            if failure == "pipeline":
                raise RuntimeError("pipeline cleanup failed")
            if failure == "late_output":
                await args[6](terminal)

        monkeypatch.setattr(native_voice, "run_configured_voice_turn", pipeline)
        runtime = SimpleNamespace(close=AsyncMock(side_effect=RuntimeError("close failed") if failure == "close" else None))
        send = AsyncMock()
        with pytest.raises((RuntimeError, native_voice.VoiceInputError)):
            await native_voice.configured_voice_runner(lambda: runtime, owns_runtime=True)(request, send, bridge)
        runtime.close.assert_awaited_once()
        send.assert_not_awaited()
    asyncio.run(run())


def test_cancellation_closes_owned_runtime_without_terminal(monkeypatch):
    async def run():
        request, bridge, _ = fixture()
        started = asyncio.Event()

        async def pipeline(*args, **kwargs):
            started.set()
            await asyncio.Future()

        monkeypatch.setattr(native_voice, "run_configured_voice_turn", pipeline)
        runtime = SimpleNamespace(close=AsyncMock())
        send = AsyncMock()
        task = asyncio.create_task(native_voice.configured_voice_runner(lambda: runtime, owns_runtime=True)(request, send, bridge))
        await started.wait()
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task
        runtime.close.assert_awaited_once()
        send.assert_not_awaited()
    asyncio.run(run())


def test_runtime_close_releases_its_model_client():
    async def run():
        runtime = object.__new__(OpenAIAgentsRuntime)
        runtime._model_client = SimpleNamespace(close=AsyncMock())
        await runtime.close()
        await runtime.close()
        runtime._model_client.close.assert_awaited_once()
    asyncio.run(run())


def test_repeated_cancellation_does_not_abandon_model_client_cleanup():
    async def run():
        started = asyncio.Event()
        release = asyncio.Event()
        cleaned = asyncio.Event()

        async def close():
            started.set()
            await release.wait()
            cleaned.set()

        runtime = object.__new__(OpenAIAgentsRuntime)
        runtime._model_client = SimpleNamespace(close=AsyncMock(side_effect=close))
        task = asyncio.create_task(runtime.close())
        await started.wait()
        for _ in range(2):
            task.cancel()
            await asyncio.sleep(0)
            assert not task.done() and not cleaned.is_set()
        release.set()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert cleaned.is_set()
        await runtime.close()
        runtime._model_client.close.assert_awaited_once()
    asyncio.run(run())
