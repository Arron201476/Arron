import asyncio
from dataclasses import replace
import importlib

import anyio
import pytest

from test_app import settings


app_module = importlib.import_module("content_agent_sidecar.app")


@pytest.mark.parametrize("enabled", [False, True])
@pytest.mark.parametrize("fail_start", [False, True])
def test_memory_worker_lifecycle_is_opt_in_and_drained(monkeypatch, enabled, fail_start):
    events = []
    class MemoryWorker:
        @classmethod
        def from_settings(cls, config):
            assert config.memory_worker_enabled
            events.append("create")
            return cls()
        def start(self):
            events.append("start")
            if fail_start:
                raise ValueError("memory startup failed")
        async def stop(self):
            events.append("stop")
    monkeypatch.setattr(app_module, "SDKMemoryWorker", MemoryWorker)
    async def run():
        app = app_module.create_app(replace(settings(), task_worker_enabled=False, memory_worker_enabled=enabled))
        if enabled and fail_start:
            with pytest.raises(ValueError, match="memory startup failed"):
                async with app.router.lifespan_context(app):
                    pytest.fail("failed startup served requests")
        else:
            async with app.router.lifespan_context(app):
                assert events == (["create", "start"] if enabled else [])
        assert events == (["create", "start", "stop"] if enabled else [])
    asyncio.run(run())


def test_partial_worker_startup_is_drained_and_lifespan_can_restart(monkeypatch):
    async def scenario():
        workers = []
        fail_startup = True

        class Worker:
            def __init__(self, _settings, *, slot):
                self.slot, self.task, self.stops = slot, None, 0
                workers.append(self)

            def start(self):
                self.task = asyncio.create_task(asyncio.Event().wait())

            async def stop(self):
                self.stops += 1
                if self.task is not None:
                    self.task.cancel()
                    await asyncio.gather(self.task, return_exceptions=True)

        class BackgroundWorker(Worker):
            def __init__(self, config, *, slot):
                if fail_startup and slot == 2:
                    raise ValueError("injected startup failure")
                super().__init__(config, slot=slot)

        monkeypatch.setattr(app_module, "SDKTaskWorker", Worker)
        monkeypatch.setattr(app_module, "SDKBackgroundTaskWorker", BackgroundWorker)
        app = app_module.create_app(replace(settings(), task_worker_enabled=True, task_worker_concurrency=2))
        try:
            with pytest.raises(ValueError, match="injected startup failure"):
                async with app.router.lifespan_context(app):
                    pytest.fail("partial startup should not serve requests")
            assert len(workers) == 3
            assert all(worker.stops == 1 and worker.task.done() for worker in workers)
            fail_startup = False
            async with app.router.lifespan_context(app):
                assert len(workers) == 7
                assert all(worker.stops == 0 for worker in workers[3:])
            assert all(worker.stops == 1 and worker.task.done() for worker in workers)
        finally:
            for worker in workers:
                if worker.task is not None:
                    worker.task.cancel()
            await asyncio.gather(*(worker.task for worker in workers if worker.task is not None), return_exceptions=True)
    asyncio.run(scenario())


@pytest.mark.parametrize("fail_stop", [False, True])
def test_worker_shutdown_joins_all_workers_even_when_one_stop_fails(monkeypatch, caplog, fail_stop):
    async def scenario():
        stops_started, release_second, second_stopped = asyncio.Event(), asyncio.Event(), asyncio.Event()
        entered = []

        class Worker:
            def __init__(self, _settings, *, slot):
                pass

            def start(self):
                pass

            async def stop(self):
                entered.append("task")
                if len(entered) == 2:
                    stops_started.set()
                if fail_stop:
                    raise ValueError("private provider or credential detail")

        class BackgroundWorker(Worker):
            async def stop(self):
                entered.append("background")
                if len(entered) == 2:
                    stops_started.set()
                await release_second.wait()
                second_stopped.set()

        monkeypatch.setattr(app_module, "SDKTaskWorker", Worker)
        monkeypatch.setattr(app_module, "SDKBackgroundTaskWorker", BackgroundWorker)
        app = app_module.create_app(replace(settings(), task_worker_enabled=True))

        async def lifespan():
            with anyio.CancelScope() as scope:
                async with app.router.lifespan_context(app):
                    scope.cancel()

        closing = asyncio.create_task(lifespan())
        try:
            await asyncio.wait_for(stops_started.wait(), 2)
            await asyncio.sleep(0)
            assert not closing.done() and not second_stopped.is_set()
            release_second.set()
            if fail_stop:
                with pytest.raises(RuntimeError, match="Agent worker shutdown failed"):
                    await asyncio.wait_for(closing, 2)
                assert "ValueError" in caplog.text and "private provider" not in caplog.text
            else:
                await asyncio.wait_for(closing, 2)
            assert second_stopped.is_set() and sorted(entered) == ["background", "task"]
        finally:
            release_second.set()
            await asyncio.gather(closing, return_exceptions=True)
    asyncio.run(scenario())
