import asyncio
from dataclasses import replace
import json

import httpx2 as httpx
import pytest
from agents import Agent, RunState
from openai import APIConnectionError

from content_agent_sidecar.background_worker import BackgroundTaskPaused, SDKBackgroundTaskWorker
from content_agent_sidecar.model_failure_boundary import ModelRecoveryRequired
from content_agent_sidecar.native_live_state import restore_native_checkpoint
from content_agent_sidecar.native_runner import bind_native_agent
from content_agent_sidecar.observability import TurnObservation
from content_agent_sidecar.runtime import OpenAIAgentsRuntime, _ACTIVE_TURN_PAUSE, _serialize_agent_context
from content_agent_sidecar.stateful_execution import ExecutionTaskPaused, StatefulExecution
from content_agent_sidecar.stateful_inputs import StatefulInputs
from content_agent_sidecar.task_worker import SDKTaskWorker, _STATEFUL_PAUSE
from test_native_patch_tool import response as patch_response
from test_native_runner import setup, tools_for
from test_stateful_execution import StreamingSequence, claim_fixture, final_item
from test_stateful_inputs import additional


async def run_boundary(mode, agent, runner_input, context, config, *, pause=None, inputs=()):
    token = None
    try:
        if mode == "main":
            context.main_inputs = list(inputs)
            runtime = OpenAIAgentsRuntime.__new__(OpenAIAgentsRuntime)
            runtime._settings = config
            token = _ACTIVE_TURN_PAUSE.set(pause)
            result = await runtime._run_execution_agent(
                agent, runner_input, context, None, None, TurnObservation("fixture", "fixture"))
        elif mode == "background":
            context.background_inputs = list(inputs)
            worker = SDKBackgroundTaskWorker.__new__(SDKBackgroundTaskWorker)
            worker._settings = config
            staged = worker._stage_additional_inputs(runner_input, context)
            result = await worker._run_streamed(agent, staged, context, pause)
        else:
            worker = SDKTaskWorker.__new__(SDKTaskWorker)
            worker._settings = config
            execution = StatefulExecution(context, {
                "attempt_id": context.execution_attempt_id, "input_snapshot_hash": "input-hash"})
            execution.inputs = StatefulInputs.from_claim(context.execution_attempt_id, list(inputs))
            execution.inputs.restore(None)
            token = _STATEFUL_PAUSE.set(pause)
            result = await worker._run_streamed(agent, "Original", 20, execution=execution,
                resumed_input=runner_input if isinstance(runner_input, RunState) else None)
        return result, result.to_state().to_json(context_serializer=_serialize_agent_context)
    except ModelRecoveryRequired as stopped:
        return None, stopped.result.to_state().to_json(context_serializer=_serialize_agent_context)
    except (BackgroundTaskPaused, ExecutionTaskPaused) as stopped:
        return None, stopped.run_state
    finally:
        if token is not None:
            (_ACTIVE_TURN_PAUSE if mode == "main" else _STATEFUL_PAUSE).reset(token)


def rebuilt_context(context, saved):
    context = replace(context, native_workspace=None, native_workspace_checkpoint=None, active_tool_calls={})
    restore_native_checkpoint(context, json.loads(json.dumps(saved))["context"]["context"])
    return context


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
@pytest.mark.parametrize("boundary", ["user-pause", "model-failure"])
def test_native_patch_survives_pause_rebuild_and_additional_input_once(tmp_path, monkeypatch, mode, boundary):
    async def scenario():
        error = APIConnectionError(request=httpx.Request("POST", "https://fixture.invalid/responses"))
        responses = [[patch_response().output[0]]]
        if boundary == "model-failure":
            responses.append(error)
        disk, backend, context, model, config = setup(tmp_path, monkeypatch, mode, responses)
        context.agent_task_id = "task" if mode == "background" else ""
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Boundary worker", model=model))
            _, saved = await run_boundary(mode, agent, "Original", context, config)
        assert not (disk.root / "note.txt").exists() and not disk.closes

        context = rebuilt_context(context, saved)
        disk.lease = None
        pause = asyncio.Event() if boundary == "user-pause" else None
        if pause is not None:
            pause.set()
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Boundary worker", model=model))
            state = await RunState.from_json(agent, saved, context_override=context)
            state.approve(state.get_interruptions()[0])
            backend.calls["patch-call"]["status"] = "approved"
            result, saved = await run_boundary(mode, agent, state, context, config, pause=pause)
        assert result is None or result.final_output is None
        assert not saved.get("interruptions") and context.native_workspace_checkpoint.state == "ready"
        assert (disk.root / "note.txt").read_bytes() == b"native patch"
        assert not disk.closes and len(backend.completed) == 1 and not backend.failed

        context = rebuilt_context(context, saved)
        disk.lease = None
        final = StreamingSequence([[final_item({"title": "Completed with added requirement"})]])
        entry = additional("Keep the existing file and add a concise conclusion.")
        if mode == "main":
            entry["agent_turn_id"] = context.agent_turn_id
        elif mode == "background":
            entry["agent_task_id"] = context.agent_task_id
        else:
            entry["attempt_id"] = context.execution_attempt_id
        input_receipts = []

        async def record_inputs(attempt, token, snapshot, ids):
            assert attempt == context.execution_attempt_id and snapshot == "input-hash"
            input_receipts.append(list(ids))

        backend.record_execution_inputs_included = record_inputs
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Boundary worker", model=final))
            state = await RunState.from_json(agent, saved, context_override=context)
            result, _ = await run_boundary(mode, agent, state, context, config, inputs=[entry])
        assert result.final_output and len(final.inputs) == 1
        messages = [item.get("content") for item in final.inputs[0] if item.get("role") == "user"]
        assert messages.count("Original") == messages.count(entry["content"]) == 1
        assert (disk.root / "note.txt").read_bytes() == b"native patch"
        writes = [op for _, op, _ in disk.file_calls if op.operation == "write" and op.path == "note.txt"]
        assert len(writes) == len(backend.completed) == 1 and not backend.failed
        assert disk.creates == disk.closes == 1 and not disk.restores and not disk.commands
        assert context.native_workspace_checkpoint.state == "closed"
        if mode == "main":
            assert context.main_included_input_ids == [entry["input_id"]]
        elif mode == "background":
            assert context.background_included_input_ids == [entry["input_id"]]
        else:
            assert input_receipts == [[entry["input_id"]]]
    asyncio.run(scenario())


@pytest.mark.parametrize("restart", [False, True])
def test_stateful_output_repair_keeps_workspace_but_has_no_mutating_tools(tmp_path, monkeypatch, restart):
    async def scenario():
        disk, backend, context, model, config = setup(tmp_path, monkeypatch, "stateful", [[final_item({"wrong": True})]])
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Generator", model=model))
            result, saved = await run_boundary("stateful", agent, "Original", context, config)
        assert result.final_output and context.skill_tool_scope is None and disk.closes == 1
        if restart:
            context = rebuilt_context(context, saved)
            disk.lease = None
        worker = SDKTaskWorker.__new__(SDKTaskWorker)
        worker._settings = config
        worker._max_tokens = 1000
        worker._model = StreamingSequence([[final_item({"title": "Repaired"})]])
        execution = StatefulExecution(context, {})
        payload, usage, _ = await worker._repair_output(claim_fixture()["context_pack"], result.final_output,
            ValueError("Missing title"), {"requests": 1, "total_tokens": 12}, "", phase="repair_validate", execution=execution)
        assert payload == {"title": "Repaired"} and usage["requests"] == 2
        assert worker._model.tools == [[]]
        assert json.loads(worker._model.inputs[0][0]["content"])["candidate"] == result.final_output
        assert disk.creates == 1 and disk.closes == 2 and len(disk.restores) == 1
        assert context.native_workspace_checkpoint.state == "closed" and not disk.commands and not backend.begun
    asyncio.run(scenario())
