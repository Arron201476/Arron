import asyncio
from contextlib import asynccontextmanager
from hashlib import sha256
import json

import pytest
from agents import Agent, RunConfig, function_tool

from content_agent_sidecar.backend import BackendClient, BackendError, _activity_headers
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.memory_execution import run_memory_consolidation
from content_agent_sidecar.native_files import _go_json_hash
import content_agent_sidecar.memory_workspace as module
from test_memory_execution import setup
from test_memory_generation_claim import raw_claim
from test_memory_native_lifecycle import workspace
from test_run_state_approval import SequenceModel, _text_response, _tool_call_response
from test_stateful_execution import StreamingSequence


@pytest.mark.parametrize("fault", [None, "start", "baseline", "newer-baseline", "inputs", "timeout", "pause", "complete", "complete-timeout", "complete-recovered", "complete-receipt"])
@pytest.mark.parametrize("streamed", [False, True])
def test_consolidation_prepares_sdk_inputs_then_runs_native_stage(raw_claim, monkeypatch, fault, streamed):
    async def run():
        baseline_files = {"memory_summary.md": "Existing private baseline"}
        raw_claim["job"].update(phase="consolidation", base_version=2, base_hash=_go_json_hash(baseline_files))
        raw_claim["extraction"] = {"rollout_slug": "fixture", "rollout_summary": "summary", "raw_memory": "private raw memory"}
        message = _tool_call_response() if fault == "pause" else _text_response()
        if fault == "pause": message.output[0].name = "exec_command"
        events = []
        class Model(StreamingSequence if streamed else SequenceModel):
            async def get_response(self, *args, **kwargs):
                assert events == ["baseline", "inputs", "bind", "workspace"]
                events.append("model")
                return await super().get_response(*args, **kwargs)
            async def stream_response(self, *args, **kwargs):
                assert events == ["baseline", "inputs", "bind", "workspace"]
                events.append("model")
                async for event in super().stream_response(*args, **kwargs):
                    yield event
        model = Model([message.output] if streamed else [message])
        backend, claim, _, binding, context = setup(raw_claim, model, fail_start=fault == "start")
        async def snapshot(project, activity_key):
            assert backend.starts == 1 and _activity_headers.get()["X-Agent-Memory-Generation-ID"] == claim.job.generation_id
            events.append("baseline")
            return {"activity_key": activity_key, "workspace_id": claim.source.workspace_id, "project_id": project,
                "user_id": claim.job.user_id, "version": 3 if fault == "baseline" else 2, "current_version": 3 if fault in {"baseline", "newer-baseline"} else 2,
                "content_hash": claim.job.base_hash, "read_enabled": True, "files": baseline_files}
        backend.resolve_agent_memory_snapshot = snapshot
        completion_payloads = []
        async def request(operation, payload):
            assert _activity_headers.get() is None
            events.append(operation)
            if operation == "inputs":
                if fault == "inputs": raise BackendError("input preparation unconfirmed")
                if fault == "timeout": await asyncio.sleep(1)
                plan = payload["input_plan"]
                assert "private raw memory" in plan["files"][".agent-memory/raw_memories.md"]
                return {"receipt": {"generation_id": claim.job.generation_id, "plan_hash": _go_json_hash(plan), "content_hash": plan["content_hash"]}}
            if operation == "complete":
                assert events[-2] == ("complete" if completion_payloads else "settle")
                completion_payloads.append(payload)
                assert payload == completion_payloads[0]
                if fault == "complete": raise BackendError("publication was not confirmed")
                if fault == "complete-timeout" or fault == "complete-recovered" and len(completion_payloads) == 1:
                    await asyncio.sleep(1)
                return {"job": {**claim.job.model_dump(), "status": "completed", "started": True, "lease_until": "",
                    "checkpoint_hash": sha256(b"").hexdigest(), "revision": claim.job.revision + 3,
                    "generation_id": "other" if fault == "complete-receipt" else claim.job.generation_id}}
            assert operation == "pause"
            digest = sha256(json.dumps(payload["checkpoint"], ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()
            return {"job": {**claim.job.model_dump(), "status": "paused", "started": True, "lease_until": "",
                "checkpoint_hash": digest, "revision": claim.job.revision + 3, "error_code": "MEMORY_GENERATION_APPROVAL_PENDING"}}
        backend.memory_generation_request = request
        backend.complete_memory_generation = BackendClient.complete_memory_generation.__get__(backend)
        @asynccontextmanager
        async def prepared(): yield object()
        class Provider:
            def __init__(self, backend, tools, **kwargs):
                assert {tool.name for tool in tools} == {"prepare_agent_memory_publication", "publish_agent_memory"}
            async def prepare(self, *args): return prepared()
        @function_tool(needs_approval=True)
        async def exec_command(value: str) -> str:
            """Pending fixture must not execute."""
            pytest.fail("unapproved tool executed")
        class Owner:
            def __init__(self, actual_context): self._settled = False
            async def bind_agent(self, agent, prepared):
                assert context.native_generation_source.plan_hash and len(context.native_generation_files) >= 4
                events.append("bind")
                return agent.clone(tools=[exec_command] if fault == "pause" else [])
            @asynccontextmanager
            async def run_config(self, config, runner_input):
                events.append("workspace")
                yield config
            async def settle(self, result, **kwargs):
                self._settled = True
                context.native_workspace_checkpoint = workspace()
                events.append("settle")
        monkeypatch.setattr(module, "AgentToolProvider", Provider)
        monkeypatch.setattr(module, "NativeWorkspaceExecution", Owner)
        call = run_memory_consolidation(backend, claim, "worker", Agent(name="template", model=model), binding,
            native_policy=SDKGuardrailPolicy(), context=context, run_config=RunConfig(tracing_disabled=True),
            cooperative_pause=streamed,
            request_timeout_seconds=.01 if fault in {"timeout", "complete-timeout", "complete-recovered"} else 10)
        if fault in {"start", "baseline", "newer-baseline", "inputs", "timeout", "complete", "complete-timeout", "complete-receipt"}:
            with pytest.raises(BackendError) as error: await call
            if fault == "timeout": assert error.value.code == "MEMORY_GENERATION_INPUTS_UNCONFIRMED"
            if fault == "complete-timeout": assert error.value.code == "MEMORY_GENERATION_COMMIT_UNCONFIRMED"
            assert ("model" in events) == fault.startswith("complete")
        else:
            result = await call
            assert bool(result.result.interruptions) == (fault == "pause")
            assert (result.pause is not None) == (fault == "pause")
            assert (result.completion is not None) == (fault != "pause")
            assert result.input_plan.prepared.selection.selected[0].rollout_id in result.stage.input
            assert events == ["baseline", "inputs", "bind", "workspace", "model", "settle"] + (["pause"] if fault == "pause" else ["complete"] * (2 if fault == "complete-recovered" else 1))
        assert len(completion_payloads) == (2 if fault in {"complete-timeout", "complete-recovered"} else 1 if fault is None or fault.startswith("complete") else 0)
        assert _activity_headers.get() is None
    asyncio.run(run())
