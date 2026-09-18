import asyncio
from contextlib import asynccontextmanager
from hashlib import sha256
import json

from agents import Agent, RunConfig, Runner, function_tool

from content_agent_sidecar.backend import BackendClient, _activity_headers
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.memory_checkpoint import checkpoint_memory_stage
from content_agent_sidecar.memory_execution import run_memory_consolidation
from content_agent_sidecar.memory_input_plan import plan_memory_consolidation, memory_input_plan_payload
from content_agent_sidecar.native_files import _go_json_hash
import content_agent_sidecar.memory_workspace as workspace_module
from test_memory_consolidation import fixture
from test_memory_execution import setup
from test_memory_generation_claim import raw_claim
from test_memory_native_lifecycle import workspace
from test_run_state_approval import _text_response, _tool_call_response
from test_stateful_execution import StreamingSequence


def test_production_stream_resumes_frozen_user_checkpoint_once(raw_claim, monkeypatch):
    async def run():
        _, _, _, source, options = await fixture()
        baseline = source.model_copy(update={"content_hash": _go_json_hash(source.files)})
        raw_claim["job"].update(phase="consolidation", base_version=baseline.version, base_hash=baseline.content_hash)
        raw_claim["extraction"] = options["extraction"].model_dump()
        writes = []
        @function_tool
        async def write_value(value: str) -> str:
            """Complete the original in-flight operation."""
            writes.append(value)
            initial.cancel(mode="after_turn")
            return "written"
        model = StreamingSequence([_tool_call_response().output, _text_response().output])
        template = Agent(name="template", model=model, tools=[write_value])
        _, original, _, binding, first_context = setup(raw_claim, model)
        plan = await plan_memory_consolidation(template, original.source, source, baseline, **options)
        first_context.native_workspace_checkpoint = workspace()
        initial = Runner.run_streamed(plan.prepared.stage.agent, plan.prepared.stage.input, context=first_context,
                                      run_config=RunConfig(tracing_disabled=True))
        async for _ in initial.stream_events():
            pass
        await initial.run_loop_task
        checkpoint = checkpoint_memory_stage(initial, plan.prepared.stage, binding, user_requested=True)
        frozen = memory_input_plan_payload(original, plan)
        raw_claim.update(checkpoint=checkpoint.model_dump(), input_plan=frozen, input_plan_hash=_go_json_hash(frozen),
                         attempt_token="NEW_PRIVATE_TOKEN")
        raw_claim["job"].update(attempt=2, revision=6,
            checkpoint_hash=sha256(json.dumps(raw_claim["checkpoint"], ensure_ascii=False, separators=(",", ":")).encode()).hexdigest())
        backend, claim, _, binding, context = setup(raw_claim, model)
        events = []
        async def forbidden_baseline(*args, **kwargs):
            raise AssertionError("Recovery re-read the changed baseline")
        backend.resolve_agent_memory_snapshot = forbidden_baseline
        async def catalog():
            events.append("catalog")
            return {"tools": []}
        backend.get_agent_tool_catalog = catalog
        async def request(operation, payload):
            assert _activity_headers.get() is None
            assert payload["attempt"] == 2 and payload["attempt_token"] == "NEW_PRIVATE_TOKEN"
            events.append(operation)
            if operation == "inputs":
                assert payload["input_plan"] == frozen
                return {"receipt": {"generation_id": claim.job.generation_id, "plan_hash": claim.input_plan_hash,
                                    "content_hash": plan.content_hash}}
            assert operation == "complete"
            return {"job": {**claim.job.model_dump(), "status": "completed", "started": True, "lease_until": "",
                            "revision": 9, "checkpoint_hash": sha256(b"").hexdigest()}}
        backend.memory_generation_request = request
        backend.complete_memory_generation = BackendClient.complete_memory_generation.__get__(backend)
        @asynccontextmanager
        async def prepared():
            yield object()
        class Provider:
            def __init__(self, *args, **kwargs): pass
            async def prepare(self, *args): return prepared()
        class Owner:
            def __init__(self, actual):
                assert actual is context
                self._settled = False
            async def bind_agent(self, agent, prepared): return agent
            @asynccontextmanager
            async def run_config(self, config, runner_input): yield config
            async def settle(self, result, **kwargs):
                self._settled = True
                events.append("settle")
        monkeypatch.setattr(workspace_module, "AgentToolProvider", Provider)
        monkeypatch.setattr(workspace_module, "NativeWorkspaceExecution", Owner)
        result = await run_memory_consolidation(backend, claim, "worker", template, binding, context=context,
            native_policy=SDKGuardrailPolicy(), run_config=RunConfig(tracing_disabled=True), cooperative_pause=True)
        assert result.completion.status == "completed" and result.pause is None
        assert events == ["inputs", "catalog", "settle", "complete"]
        assert writes == ["approved payload"] and not model.responses
        assert _activity_headers.get() is None
    asyncio.run(run())
