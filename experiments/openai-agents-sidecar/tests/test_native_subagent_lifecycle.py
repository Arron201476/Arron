import asyncio
from copy import deepcopy
import json

import pytest
from agents import Agent, RunContextWrapper, RunState, function_tool

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.managed_instructions import bind_managed_instructions
from content_agent_sidecar.native_runner import bind_native_agent
from content_agent_sidecar.runtime import AgentContext
from test_native_manifest import SKILL
from test_native_runner import setup
from test_native_workflow_boundaries import rebuilt_context, run_boundary
from test_stateful_execution import StreamingSequence, final_item, tool_item
from test_subagents import BranchModel, CatalogBackend, parent_model


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
@pytest.mark.parametrize("control", ["pause", "cancel"])
def test_actual_native_parent_controls_join_children_before_checkpoint_or_close(tmp_path, monkeypatch, mode, control):
    async def scenario():
        disk, backend, context, _, config = setup(tmp_path, monkeypatch, mode, [])
        backend.catalog["tools"].extend(deepcopy(CatalogBackend().catalog["tools"]))
        entered, exited = [], []
        both, release, pause = asyncio.Event(), asyncio.Event(), asyncio.Event()

        @function_tool
        async def inspect_project(ctx: RunContextWrapper[AgentContext], label: str) -> str:
            assert ctx.context is not context and ctx.context.native_workspace is None
            entered.append(label)
            if len(entered) == 2:
                both.set()
            try:
                await release.wait()
                ctx.context.inspected_artifacts.append({"data": {
                    "artifact_id": label, "artifact_version_id": "version-" + label}})
                return "Read " + label
            finally:
                exited.append(label)

        models = {name: StreamingSequence([
            [tool_item("inspect_project", {"label": name}, "same-child-read-id")],
            [final_item({"finding": name})]]) for name in ("left", "right")}

        async def prepare(current):
            await bind_managed_instructions(current)
            prepared = await AgentToolProvider(backend, [inspect_project],
                subtask_model=lambda: BranchModel(models), guardrail_policy=SDKGuardrailPolicy()).prepare(current, [], set())
            prepared.capabilities = {SKILL.capability_id: {"capability_id": SKILL.capability_id,
                "version": SKILL.version, "execution_mode": "inline", "status": "available", "skill": {"content_hash": SKILL.content_hash}}}
            prepared.selected_ids = {SKILL.capability_id}
            return prepared

        prepared = await prepare(context)
        async with prepared:
            primary = parent_model()
            agent = await bind_native_agent(context, config, Agent(name="Coordinator", model=primary, tools=prepared.tools))
            running = asyncio.create_task(run_boundary(mode, agent, "Original", context, config, pause=pause))
            try:
                await asyncio.wait_for(both.wait(), 8)
                if control == "cancel":
                    running.cancel()
                    with pytest.raises(asyncio.CancelledError):
                        await asyncio.wait_for(running, 8)
                else:
                    pause.set()
                    await asyncio.sleep(0)
                    assert not running.done() and not disk.saves and not disk.closes
                    release.set()
                    result, saved = await asyncio.wait_for(running, 8)
                    assert result is None or result.final_output is None
                    assert saved["context"]["context"]["native_workspace_checkpoint"]["state"] == "ready"
            finally:
                release.set()
                if not running.done():
                    running.cancel()
                await asyncio.gather(running, return_exceptions=True)
        assert sorted(entered) == sorted(exited) == ["left", "right"]
        assert not any(not task.done() for task in prepared.subtasks.tasks)
        assert not context.inspected_artifacts and len(primary.inputs) == 1
        if control == "cancel":
            assert not backend.completed
            assert disk.closes == 1 and context.native_workspace_checkpoint.state == "closed"
            assert all(len(model.inputs) == 1 for model in models.values())
            return
        assert not disk.closes and len(backend.completed) == 4
        assert all(len(model.inputs) == 2 and model.tools == [["inspect_project"], ["inspect_project"]]
                   for model in models.values())
        completed = deepcopy(backend.completed)
        context = rebuilt_context(context, saved)
        disk.lease = None
        final = StreamingSequence([[final_item({"title": "Joined both findings"})]])
        prepared = await prepare(context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Coordinator", model=final, tools=prepared.tools))
            state = await RunState.from_json(agent, saved, context_override=context)
            result, _ = await run_boundary(mode, agent, state, context, config)
        assert result.final_output and len(final.inputs) == 1 and result.context_wrapper.usage.requests == 6
        assert backend.completed == completed and sorted(entered) == ["left", "right"]
        outputs = [json.loads(item["output"]) for item in final.inputs[0]
                   if item.get("type") == "function_call_output" and item.get("call_id") in {"left", "right"}]
        assert {item["inspected_artifacts"][0]["artifact_id"] for item in outputs} == {"left", "right"}
        assert all(item["schema_version"] == "agent_subtask.v1" and len(item["read_call_ids"]) == 1 for item in outputs)
        assert disk.creates == disk.closes == 1 and not disk.restores and not disk.commands
        assert context.native_workspace_checkpoint.state == "closed"
    asyncio.run(scenario())
