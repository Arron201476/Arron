import asyncio
from contextlib import asynccontextmanager

import pytest
from agents import RunConfig

from content_agent_sidecar.backend import BackendError, _activity_headers
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.memory_execution import run_memory_stage
import content_agent_sidecar.memory_workspace as module
from test_memory_execution import setup, output
from test_memory_generation_claim import raw_claim
from test_run_state_approval import SequenceModel
from test_memory_native_lifecycle import workspace


@pytest.mark.parametrize("failure", [None, "start", "prepare", "bind"])
def test_native_stage_preparation_is_inside_started_memory_identity(raw_claim, monkeypatch, failure):
    async def run():
        events = []
        class Model(SequenceModel):
            async def get_response(self, *args, **kwargs):
                assert events == ["catalog", "tools-enter", "bind", "workspace-enter"]
                events.append("model")
                return await super().get_response(*args, **kwargs)
        backend, claim, stage, binding, context = setup(raw_claim, Model([output()]), fail_start=failure == "start")
        @asynccontextmanager
        async def prepared():
            events.append("tools-enter")
            try:
                yield object()
            finally:
                events.append("tools-exit")
        class Provider:
            def __init__(self, actual_backend, tools, *, guardrail_policy):
                assert actual_backend is backend and tools == []
                assert backend.starts == 1
                assert _activity_headers.get()["X-Agent-Memory-Generation-ID"] == claim.job.generation_id
            async def prepare(self, actual_context, capabilities, selected):
                assert actual_context is context and capabilities == [] and selected == set()
                events.append("catalog")
                if failure == "prepare":
                    raise BackendError("preparation unconfirmed")
                return prepared()
        class Owner:
            def __init__(self, actual_context):
                assert actual_context is context
            async def bind_agent(self, agent, prepared):
                events.append("bind")
                if failure == "bind":
                    raise BackendError("binding unconfirmed")
                return agent.clone(instructions=agent.instructions + "\nprivate native stage")
            @asynccontextmanager
            async def run_config(self, config, runner_input):
                events.append("workspace-enter")
                yield config
                events.append("workspace-exit")
            async def settle(self, result, *, cancelled=False):
                events.append("settle")
        monkeypatch.setattr(module, "AgentToolProvider", Provider)
        monkeypatch.setattr(module, "NativeWorkspaceExecution", Owner)
        call = run_memory_stage(backend, claim, "worker", stage, binding, context=context,
            run_config=RunConfig(tracing_disabled=True), native_policy=SDKGuardrailPolicy())
        if failure:
            with pytest.raises(BackendError):
                await call
            assert "model" not in events
            if failure == "start":
                assert not events
            if failure == "bind":
                assert events[-1] == "tools-exit"
        else:
            assert (await call).final_output.raw_memory == "memory"
            assert events == ["catalog", "tools-enter", "bind", "workspace-enter", "model", "settle", "workspace-exit", "tools-exit"]
        assert _activity_headers.get() is None
    asyncio.run(run())


@pytest.mark.parametrize("fault", ["policy", "owner", "checkpoint"])
def test_preparation_rejects_conflicting_configuration_before_start(raw_claim, fault):
    backend, claim, stage, binding, context = setup(raw_claim, SequenceModel([output()]))
    policy = SDKGuardrailPolicy()
    if fault == "policy":
        policy = object()
    elif fault == "owner":
        context.native_workspace = object()
    else:
        context.native_workspace_checkpoint = workspace()
    with pytest.raises(BackendError):
        asyncio.run(run_memory_stage(backend, claim, "worker", stage, binding, context=context,
            run_config=RunConfig(tracing_disabled=True), native_policy=policy))
    assert backend.starts == 0
