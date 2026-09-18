import asyncio
from dataclasses import replace
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest
from agents import Agent

from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.memory_worker import SDKMemoryWorker
import content_agent_sidecar.memory_worker as module
from test_memory_generation_claim import decode, raw_claim, POLICY
from test_app import settings


@pytest.mark.parametrize("token", ["", "   ", None])
def test_worker_factory_requires_credentials_before_creating_clients(monkeypatch, token):
    def forbidden(*args, **kwargs):
        pytest.fail("Created a client for a worker without credentials")
    monkeypatch.setattr(module, "BackendClient", forbidden)
    monkeypatch.setattr(module, "AsyncOpenAI", forbidden)
    with pytest.raises(ValueError, match="requires internal service credentials"):
        SDKMemoryWorker.from_settings(replace(settings(), internal_token=token))


@pytest.mark.parametrize("change", [{"model_name": "x" * 257},
                                   {"task_worker_lease_seconds": 60.0},
                                   {"task_worker_poll_milliseconds": 500.0},
                                   {"task_worker_lease_seconds": True},
                                   {"task_worker_poll_milliseconds": 60001}])
def test_worker_factory_validates_contract_before_creating_clients(monkeypatch, change):
    def forbidden(*args, **kwargs):
        pytest.fail("Created a client before validating worker configuration")
    monkeypatch.setattr(module, "BackendClient", forbidden)
    monkeypatch.setattr(module, "AsyncOpenAI", forbidden)
    with pytest.raises(ValueError):
        SDKMemoryWorker.from_settings(replace(settings(), **change))


@pytest.mark.parametrize("phase", ["empty", "extraction", "consolidation"])
def test_worker_dispatches_existing_executors_with_original_identity(raw_claim, monkeypatch, phase):
    async def run():
        claim = decode(raw_claim)
        claim = claim.model_copy(update={"job": claim.job.model_copy(update={"phase": phase if phase != "empty" else "extraction"})})
        calls = []
        async def take(worker, model, seconds, **options):
            assert worker == "worker" and model == claim.job.model_id and options["policy_hash"] == POLICY
            return None if phase == "empty" else claim
        async def execute(backend, actual, worker, stage, binding, **options):
            assert actual is claim and binding.phase == phase
            assert options["context"].conversation_id == claim.conversation_id
            assert options["context"].attempt_token == claim.attempt_token
            assert options["run_config"].tracing_disabled
            calls.append(phase)
            return "confirmed"
        monkeypatch.setattr(module, "execute_memory_extraction", execute)
        monkeypatch.setattr(module, "run_memory_consolidation", execute)
        worker = SDKMemoryWorker(SimpleNamespace(claim_memory_generation=take), Agent(name="private", model=claim.job.model_id),
            SDKGuardrailPolicy(), worker_id="worker", model_id=claim.job.model_id, policy_hash=POLICY)
        assert await worker.run_once() == (None if phase == "empty" else "confirmed")
        assert calls == ([] if phase == "empty" else [phase])
    asyncio.run(run())


def test_worker_stop_cancels_and_awaits_live_claim():
    async def run():
        entered, exited = asyncio.Event(), asyncio.Event()
        async def claim(*args, **kwargs):
            entered.set()
            try:
                await asyncio.Event().wait()
            finally:
                exited.set()
        worker = SDKMemoryWorker(SimpleNamespace(claim_memory_generation=claim), Agent(name="private", model="configured"),
            SDKGuardrailPolicy(), worker_id="worker", model_id="configured", policy_hash=POLICY)
        worker.start()
        original = worker._task
        worker.start()
        assert worker._task is original
        await asyncio.wait_for(entered.wait(), 1)
        await worker.stop()
        assert exited.is_set() and original.done() and worker._task is None
        await worker.stop()
    asyncio.run(run())


def test_factory_reuses_control_model_and_closes_owned_client(monkeypatch):
    config = settings()
    clients = []
    class Client:
        def __init__(self, **options):
            assert options["base_url"] == config.model_base_url
            assert options["api_key"] == config.model_api_key
            self.closed = False
            clients.append(self)
        async def close(self): self.closed = True
    monkeypatch.setattr(module, "AsyncOpenAI", Client)
    monkeypatch.setattr(module, "OpenAIResponsesModel", lambda model, openai_client: model)
    async def run():
        worker = SDKMemoryWorker.from_settings(config)
        assert worker.template.model == config.model_name and worker.model_id == config.model_name
        assert worker.template.input_guardrails and worker.template.output_guardrails
        await worker.stop()
        assert clients[0].closed
        await worker.stop()
    asyncio.run(run())


def test_stopped_worker_cannot_restart_or_claim_with_closed_client():
    async def run():
        backend = SimpleNamespace(claim_memory_generation=AsyncMock(return_value=None))
        worker = SDKMemoryWorker(backend, Agent(name="private", model="configured"), SDKGuardrailPolicy(),
                                 worker_id="worker", model_id="configured", policy_hash=POLICY)
        client = SimpleNamespace(close=AsyncMock())
        worker._client = client
        await asyncio.gather(worker.stop(), worker.stop())
        client.close.assert_awaited_once()
        with pytest.raises(RuntimeError, match="cannot be restarted"):
            worker.start()
        with pytest.raises(RuntimeError, match="cannot claim"):
            await worker.run_once()
        backend.claim_memory_generation.assert_not_called()
    asyncio.run(run())


def test_stop_drains_direct_execution_before_closing_client_and_rejects_waiting_claim():
    async def run():
        entered = asyncio.Event()
        events = []
        async def claim(*args, **kwargs):
            entered.set()
            try:
                await asyncio.Event().wait()
            finally:
                events.append("execution-ended")
        async def close():
            events.append("client-closed")
        worker = SDKMemoryWorker(SimpleNamespace(claim_memory_generation=claim),
                                 Agent(name="private", model="configured"), SDKGuardrailPolicy(),
                                 worker_id="worker", model_id="configured", policy_hash=POLICY)
        worker._client = SimpleNamespace(close=close)
        active = asyncio.create_task(worker.run_once())
        await asyncio.wait_for(entered.wait(), 1)
        waiting = asyncio.create_task(worker.run_once())
        await worker.stop()
        results = await asyncio.gather(active, waiting, return_exceptions=True)
        assert isinstance(results[0], asyncio.CancelledError)
        assert isinstance(results[1], RuntimeError)
        assert events == ["execution-ended", "client-closed"]
    asyncio.run(run())
