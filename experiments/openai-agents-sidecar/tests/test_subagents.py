import asyncio
from copy import deepcopy
import json

import pytest
from agents import Agent, RunConfig, RunContextWrapper, Runner, RunState, function_tool
from agents.items import ModelResponse
from agents.usage import Usage

from content_agent_sidecar.agent_tools import AgentToolConfigurationError, AgentToolProvider
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.managed_instructions import bind_managed_instructions
from content_agent_sidecar.runtime import AgentContext, _serialize_agent_context
from content_agent_sidecar.subagents import SUBTASK_TOOL_NAME, SubtaskRequest, SubtaskScope, _artifact_references, build_subtask_tool
from test_stateful_execution import StreamingSequence, final_item, tool_item
from instruction_fixtures import EmptyInstructionsBackend


class ReadBackend(EmptyInstructionsBackend):
    def __init__(self):
        self.calls, self.completed, self.cancelled = {}, [], []

    async def begin_agent_tool_call(self, **payload):
        sdk_id = payload["sdk_tool_call_id"]
        assert sdk_id not in self.calls
        self.calls[sdk_id] = deepcopy(payload)
        return {"agent_tool_call_id": "call-" + sdk_id, "status": "running"}

    async def start_agent_tool_call(self, call_id, sdk_id, **_kwargs):
        assert call_id == "call-" + sdk_id and sdk_id in self.calls
        return {"agent_tool_call_id": call_id, "status": "running"}

    async def complete_agent_tool_call(self, call_id, **_kwargs):
        self.completed.append(call_id)

    async def cancel_agent_tool_call(self, call_id, _reason):
        self.cancelled.append(call_id)


@pytest.mark.parametrize("field,value", [("title", " "), ("task", "\n"), ("materials", "a\x00b"),
                                        ("title", "x" * 121), ("task", "x" * 16001), ("materials", "x" * 64001)],
                         ids=["blank-title", "blank-task", "null-byte", "long-title", "long-task", "long-materials"])
def test_subtask_request_rejects_invalid_text(field, value):
    payload = {"title": "Review", "task": "Review the supplied text", "materials": ""}
    payload[field] = value
    with pytest.raises(ValueError):
        SubtaskRequest.model_validate(payload)


def test_subtask_artifact_references_use_both_backend_envelopes_without_bodies():
    version = {"artifact_id": "artifact", "artifact_version_id": "version", "payload": {"content": "large" * 100000}}
    assert _artifact_references([{"data": version}, {"artifact": {"artifact_id": "artifact"}, "version": version}]) == [
        {"artifact_id": "artifact", "artifact_version_id": "version"}]
    with pytest.raises(ValueError):
        _artifact_references([{"data": {"artifact_id": "missing-version"}}])


def test_subtask_requires_durable_completion_acknowledgement():
    class UnavailableBackend(ReadBackend):
        async def complete_agent_tool_call(self, *_args, **_kwargs):
            raise RuntimeError("receipt unavailable")

    async def run():
        provider = AgentToolProvider(UnavailableBackend(), [])
        with pytest.raises(RuntimeError, match="receipt unavailable"):
            await provider.complete_call("subtask", {"text": "result"}, 20, required=True)

    asyncio.run(run())


class CatalogBackend(ReadBackend):
    def __init__(self, *, delegate_access="read", delegate_approval="never", read_access="read"):
        super().__init__()
        self.catalog = {"schema_version": "1.0.0", "tools": [
            {"id": "runtime:" + name, "kind": "runtime_function", "name": name, "description": name,
             "access": access, "approval": approval, "enabled": True, "timeout_seconds": 30,
             "max_retries": 0, "max_result_bytes": 256 * 1024}
            for name, access, approval in [("inspect_project", read_access, "never"),
                                            (SUBTASK_TOOL_NAME, delegate_access, delegate_approval)]
        ]}

    async def get_agent_tool_catalog(self):
        return deepcopy(self.catalog)


class BranchModel(StreamingSequence):
    def __init__(self, branches):
        super().__init__([])
        self.branches = branches

    async def stream_response(self, *args, **kwargs):
        encoded = json.dumps(args[1])
        branch = next(name for name in self.branches if name in encoded)
        async for event in self.branches[branch].stream_response(*args, **kwargs):
            yield event

    async def get_response(self, *args, **kwargs):
        encoded = json.dumps(kwargs["input"])
        branch = next(name for name in self.branches if name in encoded)
        model = self.branches[branch]
        model.inputs.append(deepcopy(kwargs["input"]))
        model.tools.append([tool.name for tool in kwargs["tools"]])
        output = await model.get_response(*args, **kwargs)
        return ModelResponse(output=output, usage=Usage(requests=1, input_tokens=10, output_tokens=2, total_tokens=12),
                             response_id=f"{branch}-{len(model.inputs)}")


def parent_model():
    return StreamingSequence([[tool_item(SUBTASK_TOOL_NAME, {"title": name, "task": name, "materials": "Fixture material"}, name)
                               for name in ("left", "right")]])


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_native_parallel_subtasks_are_isolated_joined_and_not_replayed(mode):
    async def run():
        backend = ReadBackend()
        context = AgentContext("p", "c", backend, agent_turn_id="turn")
        if mode == "background":
            context.agent_turn_id, context.agent_task_id, context.agent_task_attempt_id = "", "task", "attempt"
        if mode == "stateful":
            context.agent_turn_id, context.execution_attempt_id = "", "attempt"
        context.attempt_token = "private-token-not-for-model"
        await bind_managed_instructions(context)
        arrived, both = [], asyncio.Event()

        @function_tool
        async def inspect_project(ctx: RunContextWrapper[AgentContext], label: str) -> str:
            assert ctx.context is not context
            arrived.append(label)
            if len(arrived) == 2:
                both.set()
            await asyncio.wait_for(both.wait(), 3)
            ctx.context.inspected_artifacts.append({"data": {"artifact_id": label, "artifact_version_id": "version-" + label, "payload": {"content": "Large body " * 50000}}})
            return "Read " + label

        @function_tool
        async def apply_workspace_patch() -> str:
            raise AssertionError("A child cannot write")

        provider = AgentToolProvider(backend, [inspect_project, apply_workspace_patch])
        reads = provider.runtime_tools()
        models = {name: StreamingSequence([[tool_item("inspect_project", {"label": name}, "same-native-id")],
                                           [final_item({"finding": name})]]) for name in ("left", "right")}
        scope = SubtaskScope()
        tool = build_subtask_tool(BranchModel(models), reads, SDKGuardrailPolicy(), scope)
        parent = Agent(name="Coordinator", model=parent_model(), tools=[tool])
        config = RunConfig(tracing_disabled=True)
        first = Runner.run_streamed(parent, "Run independent subtasks", context=context, max_turns=4, run_config=config)
        async for event in first.stream_events():
            if event.type == "raw_response_event":
                first.cancel(mode="after_turn")
        await asyncio.wait_for(scope.drain(), 3)
        assert set(arrived) == {"left", "right"} and len(backend.calls) == len(backend.completed) == 2
        assert not context.inspected_artifacts and not context.active_tool_calls and context.commit_result is None
        assert all(len(model.inputs) == 2 and all(names == ["inspect_project"] for names in model.tools) for model in models.values())
        assert len({call["sdk_tool_call_id"] for call in backend.calls.values()}) == 2
        for call in backend.calls.values():
            assert call["project_id"] == "p" and call["conversation_id"] == "c"
            assert call.get("execution_attempt_id", "") == context.execution_attempt_id
            assert call["agent_task_attempt_id"] == context.agent_task_attempt_id
            assert call["agent_turn_id"] == context.agent_turn_id
        snapshot = json.loads(json.dumps(first.to_state().to_json(context_serializer=_serialize_agent_context)))
        assert "private-token-not-for-model" not in json.dumps(snapshot)
        final_model = StreamingSequence([[final_item({"summary": "Joined both results"})]])
        resumed_agent = parent.clone(model=final_model)
        restored = await RunState.from_json(resumed_agent, snapshot, context_override=context, strict_context=True)
        result = Runner.run_streamed(resumed_agent, restored, run_config=config)
        async for _ in result.stream_events():
            pass
        assert len(backend.calls) == 2 and result.context_wrapper.usage.requests == 6
        outputs = [json.loads(item["output"]) for item in final_model.inputs[0] if item.get("type") == "function_call_output"]
        assert len(outputs) == 2 and all(item["schema_version"] == "agent_subtask.v1" for item in outputs)
        assert {item["inspected_artifacts"][0]["artifact_id"] for item in outputs} == {"left", "right"}
        assert all(len(json.dumps(item)) < 1024 for item in outputs)
        assert {item["read_call_ids"][0] for item in outputs} == set(backend.completed)
        assert not (asyncio.all_tasks() - {asyncio.current_task()})

    asyncio.run(run())


def test_parent_cancellation_stops_every_native_child():
    async def run():
        backend = ReadBackend()
        context = AgentContext("p", "c", backend, agent_turn_id="turn")
        await bind_managed_instructions(context)
        started, stopped = [], []
        both = asyncio.Event()

        @function_tool
        async def inspect_project(label: str) -> str:
            started.append(label)
            if len(started) == 2:
                both.set()
            try:
                await asyncio.Event().wait()
            finally:
                stopped.append(label)

        models = {name: StreamingSequence([[tool_item("inspect_project", {"label": name}, "same-id")]]) for name in ("left", "right")}
        scope = SubtaskScope()
        tool = build_subtask_tool(BranchModel(models), AgentToolProvider(backend, [inspect_project]).runtime_tools(), SDKGuardrailPolicy(), scope)
        parent = Agent(name="Coordinator", model=parent_model(), tools=[tool])
        result = Runner.run_streamed(parent, "Run independent subtasks", context=context, run_config=RunConfig(tracing_disabled=True))

        async def consume():
            async for _ in result.stream_events():
                pass

        consumer = asyncio.create_task(consume())
        try:
            await asyncio.wait_for(both.wait(), 3)
            result.cancel()
            await asyncio.wait_for(consumer, 3)
            if result.run_loop_task is not None:
                await asyncio.wait_for(asyncio.gather(result.run_loop_task, return_exceptions=True), 3)
            await asyncio.wait_for(scope.drain(), 3)
        finally:
            if not consumer.done():
                result.cancel()
                consumer.cancel()
                await asyncio.gather(consumer, return_exceptions=True)
        assert sorted(stopped) == ["left", "right"] and not backend.completed
        assert len(backend.cancelled) == 2 and context.commit_result is None
        # The isolated loop may still contain cancelled SDK batch supervisors;
        # require their natural completion, without cancelling them again.
        supervisors = asyncio.all_tasks() - {asyncio.current_task()}
        assert all(task.cancelling() for task in supervisors)
        await asyncio.wait_for(asyncio.gather(*supervisors, return_exceptions=True), 3)
        assert not (asyncio.all_tasks() - {asyncio.current_task()})

    asyncio.run(run())


@pytest.mark.parametrize("read_access", ["read", "write", "sensitive"])
def test_shared_provider_audits_subtasks_and_filters_child_permissions(read_access):
    async def run():
        backend = CatalogBackend(read_access=read_access)
        context = AgentContext("p", "c", backend, agent_turn_id="turn")
        await bind_managed_instructions(context)

        @function_tool
        async def inspect_project() -> str:
            raise AssertionError("This model fixture must not perform a read")

        branches = {name: StreamingSequence([[final_item({"finding": name})]]) for name in ("left", "right")}
        provider = AgentToolProvider(backend, [inspect_project], subtask_model=lambda: BranchModel(branches),
                                     guardrail_policy=SDKGuardrailPolicy())
        prepared = await provider.prepare(context, [], set())
        async with prepared:
            parent = Agent(name="Coordinator", model=parent_model(), tools=prepared.tools)
            result = Runner.run_streamed(parent, "Analyze", context=context, run_config=RunConfig(tracing_disabled=True))
            async for event in result.stream_events():
                if event.type == "raw_response_event":
                    result.cancel(mode="after_turn")
        assert context.skill_tool_scope is None and len(backend.completed) == 2
        assert set(backend.calls) == {"left", "right"}
        assert all(call["tool_id"] == "runtime:" + SUBTASK_TOOL_NAME for call in backend.calls.values())
        assert all(model.tools == [["inspect_project"] if read_access == "read" else []] for model in branches.values())
        assert not (asyncio.all_tasks() - {asyncio.current_task()})

    asyncio.run(run())


@pytest.mark.parametrize("access,approval,policy", [("write", "never", True), ("read", "always", True), ("read", "never", False)])
def test_subtask_provider_requires_trusted_read_policy_and_guardrails(access, approval, policy):
    async def run():
        backend = CatalogBackend(delegate_access=access, delegate_approval=approval)
        provider = AgentToolProvider(backend, [], subtask_model=lambda: StreamingSequence([]),
                                     guardrail_policy=SDKGuardrailPolicy() if policy else None)
        with pytest.raises(AgentToolConfigurationError, match="read-only descriptor and platform guardrails"):
            await provider.prepare(AgentContext("p", "c", backend, agent_turn_id="turn"), [], set())
        assert not backend.calls

    asyncio.run(run())
