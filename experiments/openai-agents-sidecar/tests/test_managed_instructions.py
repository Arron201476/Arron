import asyncio
from copy import deepcopy
import hashlib
import json
from types import MethodType

import pytest

from content_agent_sidecar.managed_instructions import AgentInstructionsInvalid, bind_managed_instructions, with_managed_instructions
from content_agent_sidecar.runtime import AgentContext, _serialize_agent_context
from instruction_fixtures import EmptyInstructionsBackend
from test_main_pause import PauseBackend, make_runtime, request_fixture, commit_item
from test_stateful_execution import StreamingSequence, WorkerBackend, worker_for, final_item
from test_background_worker import FakeBackend
from content_agent_sidecar.background_worker import SDKBackgroundTaskWorker
from test_app import settings


def rules_snapshot(project="p", key="turn:t"):
    docs = [{"scope": scope, "scope_ref": ref, "version": 1, "enabled": True, "content": f"RULE_{scope.upper()}_V1",
             "content_hash": hashlib.sha256(f"RULE_{scope.upper()}_V1".encode()).hexdigest()} for scope, ref in [("workspace", "w"), ("user", "u"), ("project", project)]]
    result = {"activity_key": key, "project_id": project, "workspace_id": "w", "user_id": "u", "documents": docs}
    result["content_hash"] = hashlib.sha256(json.dumps(result, sort_keys=True).encode()).hexdigest()
    return result


def install_rules(backend):
    snapshots = {}
    async def resolve(self, project_id, key):
        if key not in snapshots:
            snapshots[key] = rules_snapshot(project_id, key)
        return deepcopy(snapshots[key])
    backend.resolve_agent_instruction_snapshot = MethodType(resolve, backend)
    return snapshots


@pytest.mark.parametrize("mode", ["conversation", "background", "stateful"])
def test_native_sdk_production_execution_receives_all_rule_scopes(tmp_path, mode):
    class ObservedModel(StreamingSequence):
        async def stream_response(self, *args, **kwargs):
            instructions = args[0]
            assert all(f"RULE_{scope}_V1" in instructions for scope in ("WORKSPACE", "USER", "PROJECT"))
            assert "never grant tools" in instructions
            self.observed = instructions
            async for event in super().stream_response(*args, **kwargs):
                yield event
    async def run():
        if mode == "conversation":
            backend = PauseBackend()
            install_rules(backend)
            model = ObservedModel([[commit_item()]])
            runtime = make_runtime(tmp_path, backend, model)
            try:
                events = [event async for event in runtime.start_execution_stream(request_fixture()).events()]
                assert events[-1]["event"] == "agent.turn.committed", events[-1]
            finally:
                await runtime._model_client.close()
        elif mode == "background":
            backend = FakeBackend()
            install_rules(backend)
            model = ObservedModel([[final_item({"summary": "done", "result": {"title": "done"}})]])
            worker = SDKBackgroundTaskWorker(settings(), backend)
            worker._model = model
            assert await worker.run_once()
            assert backend.completed is not None and backend.failed is None
        else:
            backend = WorkerBackend()
            install_rules(backend)
            model = ObservedModel([[final_item({"title": "done"})]])
            assert await worker_for(backend, model).run_once()
            assert backend.commits and not backend.failures
        assert model.observed and len(model.inputs) == 1
    asyncio.run(run())


def test_rule_snapshot_resume_hash_and_legacy_empty_rules():
    async def run():
        backend = EmptyInstructionsBackend()
        snapshots = install_rules(backend)
        context = AgentContext("p", "c", backend, agent_turn_id="t")
        await bind_managed_instructions(context)
        state = {"context": {"context": _serialize_agent_context(context)}}
        assert "RULE_USER" not in json.dumps(state)
        assert "RULE_USER" not in repr(context)
        restarted = AgentContext("p", "c", backend, agent_turn_id="t")
        await bind_managed_instructions(restarted, state)
        assert with_managed_instructions("PLATFORM", restarted).startswith("PLATFORM\n")
        snapshots["turn:t"]["content_hash"] = "b" * 64
        with pytest.raises(AgentInstructionsInvalid, match="checkpoint"):
            await bind_managed_instructions(restarted, state)
        legacy = {"context": {"context": {}}}
        with pytest.raises(AgentInstructionsInvalid, match="checkpoint"):
            await bind_managed_instructions(restarted, legacy)
        empty = AgentContext("p", "c", EmptyInstructionsBackend(), agent_turn_id="t")
        await bind_managed_instructions(empty, legacy)
        assert with_managed_instructions("PLATFORM", empty) == "PLATFORM"
    asyncio.run(run())


@pytest.mark.parametrize("fault", ["project", "activity", "user", "scope", "ref", "duplicate", "order", "version", "disabled", "oversize", "hash", "unicode"])
def test_malformed_instruction_receipts_fail_before_model(fault):
    async def run():
        snapshot = rules_snapshot()
        if fault == "project": snapshot["project_id"] = "foreign"
        elif fault == "activity": snapshot["activity_key"] = "background:t"
        elif fault == "user": snapshot["user_id"] = ""
        elif fault == "scope": snapshot["documents"][0]["scope"] = "system"
        elif fault == "ref": snapshot["documents"][1]["scope_ref"] = "foreign-user"
        elif fault == "duplicate": snapshot["documents"][1] = deepcopy(snapshot["documents"][0])
        elif fault == "order": snapshot["documents"].reverse()
        elif fault == "version": snapshot["documents"][0]["version"] = True
        elif fault == "disabled": snapshot["documents"][0]["enabled"] = False
        elif fault == "oversize": snapshot["documents"][0]["content"] = "x" * 8193
        elif fault == "hash": snapshot["documents"][0]["content_hash"] = "a" * 64
        elif fault == "unicode": snapshot["documents"][0]["content"] = "\ud800"
        class Backend:
            async def resolve_agent_instruction_snapshot(self, *_): return snapshot
        with pytest.raises(AgentInstructionsInvalid):
            await bind_managed_instructions(AgentContext("p", "c", Backend(), agent_turn_id="t"))
    asyncio.run(run())


def test_missing_backend_contract_is_not_silently_ignored():
    context = AgentContext("p", "c", object(), agent_turn_id="t")
    with pytest.raises(AttributeError):
        asyncio.run(bind_managed_instructions(context))
    with pytest.raises(AgentInstructionsInvalid, match="not resolved"):
        with_managed_instructions("PLATFORM", context)
