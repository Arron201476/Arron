import asyncio
from contextlib import asynccontextmanager
from hashlib import sha256
import json
from types import SimpleNamespace

import pytest
from agents import RunConfig

from content_agent_sidecar.backend import BackendError, _activity_headers
from content_agent_sidecar.memory_checkpoint import checkpoint_memory_stage, restore_memory_stage
from content_agent_sidecar.memory_execution import execute_memory_extraction
from content_agent_sidecar.native_live_state import NativeWorkspaceCheckpoint
from test_memory_checkpoint import paused, binding
from test_memory_execution import setup, output
from test_memory_generation_claim import raw_claim
from test_run_state_approval import SequenceModel


def workspace():
    return NativeWorkspaceCheckpoint(session_id="11111111-1111-1111-1111-111111111111",
        environment_id="22222222-2222-2222-2222-222222222222", state="ready",
        snapshot={"version": 1, "sha256": "a" * 64})


def test_memory_checkpoint_retains_only_verified_native_reference():
    async def run():
        stage, result, _, _ = await paused("consolidation")
        context = SimpleNamespace(native_workspace=SimpleNamespace(_settled=False), native_workspace_checkpoint=None, attempt_token="PRIVATE_TOKEN")
        result.context_wrapper.context = context
        with pytest.raises(ValueError, match="settled recovery"):
            checkpoint_memory_stage(result, stage, binding("consolidation"))
        context.native_workspace_checkpoint = workspace()
        with pytest.raises(ValueError, match="settled recovery"):
            checkpoint_memory_stage(result, stage, binding("consolidation"))
        context.native_workspace._settled = True
        checkpoint = checkpoint_memory_stage(result, stage, binding("consolidation"))
        assert checkpoint.native_workspace == workspace()
        assert "PRIVATE_TOKEN" not in checkpoint.model_dump_json()
        assert json.loads(checkpoint.state_json)["context"]["context"] == {}
        fresh = SimpleNamespace(native_workspace_checkpoint=workspace(), attempt_token="NEW_TOKEN")
        restored = await restore_memory_stage(checkpoint, stage, binding("consolidation"), context=fresh)
        assert restored.get_interruptions()
        for value in (None, workspace().model_copy(update={"snapshot": workspace().snapshot.model_copy(update={"version": 2})})):
            fresh.native_workspace_checkpoint = value
            with pytest.raises(ValueError, match="native workspace"):
                await restore_memory_stage(checkpoint, stage, binding("consolidation"), context=fresh)
    asyncio.run(run())


@pytest.mark.parametrize("settle_failure", [False, True])
def test_native_owner_settles_before_memory_phase_commit(raw_claim, settle_failure):
    async def run():
        backend, claim, stage, generation_binding, context = setup(raw_claim, SequenceModel([output()]))
        events = []
        class Owner:
            @asynccontextmanager
            async def run_config(self, config, runner_input):
                assert _activity_headers.get()["X-Agent-Memory-Generation-ID"] == claim.job.generation_id
                events.append("enter")
                try:
                    yield config
                finally:
                    events.append("exit")

            async def settle(self, result, *, cancelled=False):
                assert result.final_output.raw_memory == "memory"
                events.append("settle")
                if settle_failure:
                    raise BackendError("workspace close unconfirmed")
                context.native_workspace_checkpoint = workspace().model_copy(update={"state": "closed"})

        context.native_workspace = Owner()
        async def persist(operation, payload):
            assert events == ["enter", "settle", "exit"]
            assert _activity_headers.get() is None and operation == "extraction"
            events.append("commit")
            digest = sha256(json.dumps(payload["output"], ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()
            return {"receipt": {"generation_id": claim.job.generation_id, "content_hash": digest, "has_memory": True}}
        backend.memory_generation_request = persist
        call = execute_memory_extraction(backend, claim, "worker", stage, generation_binding,
            context=context, run_config=RunConfig(tracing_disabled=True))
        if settle_failure:
            with pytest.raises(BackendError, match="close unconfirmed"):
                await call
            assert events == ["enter", "settle", "exit"]
        else:
            assert (await call).extraction["has_memory"]
            assert events == ["enter", "settle", "exit", "commit"]
        assert _activity_headers.get() is None
    asyncio.run(run())
