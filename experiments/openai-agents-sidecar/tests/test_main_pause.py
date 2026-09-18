import asyncio
from dataclasses import replace
from copy import deepcopy
import json

import httpx2 as httpx
import pytest
from agents import Agent, RunConfig, RunContextWrapper, Runner, RunState, SQLiteSession, UserError, function_tool

from content_agent_sidecar.contracts import AgentExecutionRequest, AgentToolApprovalDecision
from content_agent_sidecar.app import create_app
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.runtime import AgentContext, OpenAIAgentsRuntime, RequiredCommitModelAdapter, RuntimeCompatibilityError, _serialize_agent_context, stop_after_commit
from test_app import settings
from test_main_dynamic_skill_tools import MainAuthorBackend
from test_project_skills import ARGS
from test_stateful_execution import StreamingSequence, final_item, tool_item


@pytest.mark.parametrize("decision", ["approve", "reject", "approved-terminal"])
def test_native_custom_commit_can_pause_after_old_approvals_before_appending(tmp_path, decision):
    writes = []

    @function_tool(needs_approval=True)
    async def old_write(ctx: RunContextWrapper[AgentContext]) -> str:
        writes.append("old")
        if decision == "approved-terminal":
            ctx.context.commit_result = {"reply": "Already complete"}
        return "written"

    @function_tool
    async def commit(ctx: RunContextWrapper[AgentContext]) -> str:
        ctx.context.commit_result = {"reply": "Complete with additional instruction"}
        return "committed"

    async def run():
        model = StreamingSequence([[tool_item("old_write", {}, "old")], [tool_item("commit", {}, "commit")]])
        agent = Agent(name="Main pause", model=model, tools=[old_write, commit], tool_use_behavior=stop_after_commit)
        config = RunConfig(tracing_disabled=True)
        session = SQLiteSession("main", tmp_path / "session.db")
        context = AgentContext("p", "c", None, agent_turn_id="t")
        try:
            first = Runner.run_streamed(agent, "Original", context=context, session=session, run_config=config)
            async for _ in first.stream_events():
                pass
            state = first.to_state()
            with pytest.raises(UserError, match="tool result may end"):
                state.add_input("Additional instruction")
            if decision == "reject":
                state.reject(state.get_interruptions()[0])
            else:
                state.approve(state.get_interruptions()[0])
            settling = Runner.run_streamed(agent, state, session=session, run_config=config)
            settling.cancel(mode="after_turn")
            async for _ in settling.stream_events():
                pass
            assert len(model.inputs) == 1 and not settling.interruptions
            assert writes == ([] if decision == "reject" else ["old"])
            if decision == "approved-terminal":
                assert settling.final_output == str(context.commit_result)
                assert context.commit_result == {"reply": "Already complete"}
                with pytest.raises(UserError, match="terminal"):
                    settling.to_state().add_input("Additional instruction")
                return
            assert settling.final_output is None
            state = settling.to_state()
            state.add_input("Additional instruction")
            saved = json.loads(json.dumps(state.to_json(context_serializer=_serialize_agent_context)))
            session.close()
            session = SQLiteSession("main", tmp_path / "session.db")
            restored = await RunState.from_json(agent, saved, context_override=context)
            final = Runner.run_streamed(agent, restored, session=session, run_config=config)
            async for _ in final.stream_events():
                pass
            assert final.final_output == str(context.commit_result)
            assert context.commit_result == {"reply": "Complete with additional instruction"}
            assert len(model.inputs) == 2 and final.to_state().pending_input == []
            assert [item["content"] for item in model.inputs[-1] if item.get("role") == "user"] == ["Original", "Additional instruction"]
            assert [item["content"] for item in await session.get_items() if item.get("role") == "user"] == ["Original", "Additional instruction"]
        finally:
            session.close()

    asyncio.run(run())


class PauseBackend(MainAuthorBackend):
    async def get_agent_tool_catalog(self):
        catalog = await super().get_agent_tool_catalog()
        template = next(tool for tool in catalog["tools"] if tool["name"] == "load_skill_instructions")
        catalog["tools"].append({**template, "id": "runtime:validate_workspace_skill", "name": "validate_workspace_skill"})
        return catalog

    async def begin_agent_tool_call(self, **payload):
        call = await super().begin_agent_tool_call(**payload)
        if payload["tool_id"] == "runtime:validate_workspace_skill":
            self.calls[payload["sdk_tool_call_id"]]["status"] = call["status"] = "approved"
        return call


class PausingModel(StreamingSequence):
    stream = None

    async def stream_response(self, *args, **kwargs):
        async for event in super().stream_response(*args, **kwargs):
            if self.stream is not None:
                assert self.stream.pause()
                self.stream = None
                await asyncio.sleep(0)
            yield event


def make_runtime(tmp_path, backend, primary, repair=None):
    config = replace(settings(), backend_base_url="http://127.0.0.1:1", model_base_url="http://127.0.0.1:1",
                     session_db_path=str(tmp_path / "main-session.db"))
    runtime = OpenAIAgentsRuntime(config, backend)
    runtime._execution_agent.model = primary
    runtime._commit_repair_agent.model = RequiredCommitModelAdapter(repair or StreamingSequence([]))
    return runtime


def request_fixture():
    return AgentExecutionRequest(project_id="p", conversation_id="c", agent_turn_id="t", idempotency_key="i", request={"content": "Read and report"})


def commit_item(sdk_id="commit"):
    return tool_item("commit_agent_action", {"decision": {"intent": "chat", "reply": "Done", "confidence": 1}}, sdk_id)


@pytest.mark.parametrize("before_model", [False, True])
def test_main_runtime_user_pause_rebuild_preserves_session_and_completed_tools(tmp_path, before_model):
    backend = PauseBackend()
    model = PausingModel([[tool_item("validate_workspace_skill", {"root_path": "skills/my-skill"}, "read")], [commit_item()]])

    async def run():
        request = request_fixture()
        first_runtime = make_runtime(tmp_path, backend, model)
        try:
            stream = first_runtime.start_execution_stream(request)
            if before_model:
                assert stream.pause() and stream.pause()
            else:
                model.stream = stream
            first = [event async for event in stream.events()]
            assert first[-1]["event"] == "agent.turn.paused"
            assert not stream.pause() and not stream.cancel()
            checkpoint = first[-1]["data"]
            assert checkpoint["pending_sdk_tool_call_ids"] == [] and not backend.commits
            assert len(model.inputs) == (0 if before_model else 1)
            assert checkpoint["run_state"]["context"]["context"]["main_execution_phase"] == "execution"
        finally:
            await first_runtime._model_client.close()
        restarted = make_runtime(tmp_path, backend, model)
        try:
            restored = request.model_copy(update={"run_state": checkpoint["run_state"]})
            final = [event async for event in restarted.start_execution_stream(restored).events()]
            assert final[-1]["event"] == "agent.turn.committed" and len(backend.commits) == 1
            assert len(model.inputs) == 2
            assert len([item for item in backend.begins if item["sdk_tool_call_id"] == "read"]) == 1
            session = restarted._session("p", "c", "")
            try:
                items = await session.get_items()
                assert len([item for item in items if item.get("role") == "user"]) == 1
            finally:
                session.close()
        finally:
            await restarted._model_client.close()

    asyncio.run(run())


@pytest.mark.parametrize("stage", ["before-repair", "during-repair", "second-repair"])
def test_main_runtime_pauses_and_restores_terminal_repair_without_repeating_primary(tmp_path, stage):
    backend = PauseBackend()
    primary = PausingModel([[final_item({"uncommitted": "Original candidate"})]])
    invalid_commit = tool_item("commit_agent_action", {"decision": {"intent": "revise", "reply": "Must not revise", "confidence": 1}}, "invalid")
    repair = PausingModel([[invalid_commit], [commit_item()]]) if stage == "during-repair" else PausingModel([[commit_item()]])
    if stage == "second-repair":
        repair.responses.insert(0, [final_item({"uncommitted": "First repair candidate"})])

    async def run():
        request = request_fixture()
        runtime = make_runtime(tmp_path, backend, primary, repair)
        try:
            stream = runtime.start_execution_stream(request)
            if stage == "before-repair":
                primary.stream = stream
            else:
                repair.stream = stream
            first = [event async for event in stream.events()]
            assert first[-1]["event"] == "agent.turn.paused", first
            checkpoint = first[-1]["data"]
            context = checkpoint["run_state"]["context"]["context"]
            assert context["main_execution_phase"] == "terminal_repair"
            assert context["main_repair_attempt"] == (1 if stage == "second-repair" else 0)
            assert len(primary.inputs) == 1 and len(repair.inputs) == (0 if stage == "before-repair" else 1)
            assert not backend.commits
        finally:
            await runtime._model_client.close()
        restarted = make_runtime(tmp_path, backend, StreamingSequence([]), repair)
        try:
            resumed = request.model_copy(update={"run_state": checkpoint["run_state"]})
            final = [event async for event in restarted.start_execution_stream(resumed).events()]
            assert final[-1]["event"] == "agent.turn.committed", final
            assert len(backend.commits) == 1 and len(primary.inputs) == 1
            assert len(repair.inputs) == (1 if stage == "before-repair" else 2)
            assert "Original candidate" in json.dumps(repair.inputs[-1])
        finally:
            await restarted._model_client.close()

    asyncio.run(run())


def test_streaming_terminal_repair_preserves_json_only_gateway_compatibility(tmp_path):
    backend = PauseBackend()
    primary = StreamingSequence([[final_item({"uncommitted": "candidate"})]])
    repair = StreamingSequence([[final_item({"intent": "chat", "reply": "Repaired JSON", "confidence": 1})]])

    async def run():
        runtime = make_runtime(tmp_path, backend, primary, repair)
        try:
            events = [event async for event in runtime.start_execution_stream(request_fixture()).events()]
            assert events[-1]["event"] == "agent.turn.committed"
            assert len(backend.commits) == 1 and backend.commits[0]["reply"] == "Repaired JSON"
            assert len(primary.inputs) == len(repair.inputs) == 1
        finally:
            await runtime._model_client.close()

    asyncio.run(run())


def test_repair_resume_retains_original_candidate_when_first_repair_is_empty(tmp_path):
    backend = PauseBackend()
    primary = StreamingSequence([[final_item({"uncommitted": "Original candidate"})]])
    invalid = tool_item("commit_agent_action", {"decision": {"intent": "revise", "reply": "Invalid", "confidence": 1}}, "invalid")
    empty = final_item({})
    empty.content[0].text = ""
    repair = PausingModel([[invalid], [empty], [commit_item()]])

    async def run():
        runtime = make_runtime(tmp_path, backend, primary, repair)
        try:
            stream = runtime.start_execution_stream(request_fixture())
            repair.stream = stream
            events = [event async for event in stream.events()]
            assert events[-1]["event"] == "agent.turn.paused"
            state = events[-1]["data"]["run_state"]
        finally:
            await runtime._model_client.close()
        restarted = make_runtime(tmp_path, backend, StreamingSequence([]), repair)
        try:
            request = request_fixture().model_copy(update={"run_state": state})
            events = [event async for event in restarted.start_execution_stream(request).events()]
            assert events[-1]["event"] == "agent.turn.committed" and len(backend.commits) == 1
            repair_request = json.loads(repair.inputs[-1][-1]["content"])
            assert repair_request["repair_attempt"] == 2
            assert repair_request["candidate"] == '{"uncommitted": "Original candidate"}'
        finally:
            await restarted._model_client.close()

    asyncio.run(run())


@pytest.mark.parametrize("kind", ["json", "tool", "invalid"])
def test_required_commit_stream_adapter_preserves_event_and_response_metadata(kind):
    from openai.types.responses import Response, ResponseCompletedEvent, ResponseTextDeltaEvent

    output = [commit_item()] if kind == "tool" else [final_item(
        {"intent": "chat", "reply": "Converted", "confidence": 1} if kind == "json" else {"not": "a decision"}
    )]
    delta = ResponseTextDeltaEvent(type="response.output_text.delta", sequence_number=1, item_id="final", output_index=0, content_index=0, delta="fixture", logprobs=[])
    response = Response.model_construct(id="response-original", output=output, usage={"total_tokens": 19}, status="completed")
    completed = ResponseCompletedEvent(type="response.completed", sequence_number=2, response=response)
    original_output = completed.response.output
    original_response = deepcopy(completed.response.model_dump())

    class Delegate:
        async def stream_response(self):
            yield delta
            yield completed

    async def run():
        events = [event async for event in RequiredCommitModelAdapter(Delegate()).stream_response()]
        assert events[0] is delta
        final = events[1]
        assert final.sequence_number == 2 and final.response.id == response.id
        assert final.response.usage == response.usage and final.response.status == response.status
        if kind == "json":
            assert final is not completed and completed.response.output is original_output
            assert completed.response.model_dump() == original_response
            call = final.response.output[0]
            assert call.name == "commit_agent_action" and call.call_id.startswith("compat_commit_")
            assert json.loads(call.arguments)["decision"]["reply"] == "Converted"
        else:
            assert final is completed and final.response.output is original_output

    asyncio.run(run())


@pytest.mark.parametrize("blocked", [False, True])
def test_repeated_zero_model_pause_preserves_history_and_runs_initial_guardrail(tmp_path, blocked):
    backend = PauseBackend()
    model = StreamingSequence([[commit_item()]])
    history = [{"role": "user", "content": "Historical request"}, {"role": "assistant", "content": "Historical reply"}]

    async def run():
        request = request_fixture()
        for attempt in range(3):
            runtime = make_runtime(tmp_path, backend, model)
            session = runtime._session("p", "c", "")
            try:
                if attempt == 0:
                    await session.add_items(history)
                if attempt == 2 and blocked:
                    runtime._guardrails = SDKGuardrailPolicy(max_input_chars=1)
                stream = runtime.start_execution_stream(request)
                if attempt < 2:
                    assert stream.pause()
                    events = [event async for event in stream.events()]
                    assert events[-1]["event"] == "agent.turn.paused" and not model.inputs
                    request = request.model_copy(update={"run_state": events[-1]["data"]["run_state"]})
                    assert await session.get_items() == history
                elif blocked:
                    with pytest.raises(RuntimeCompatibilityError, match="InputGuardrailTripwireTriggered"):
                        _ = [event async for event in stream.events()]
                    assert not model.inputs and not backend.commits
                    assert await session.get_items() == history
                else:
                    events = [event async for event in stream.events()]
                    assert events[-1]["event"] == "agent.turn.committed" and len(backend.commits) == 1
                    items = await session.get_items()
                    assert items[:2] == history
                    assert len([item for item in items if item.get("role") == "user"]) == 2
                    assert model.inputs[0][:2] == history
                    assert len([item for item in model.inputs[0] if item.get("role") == "user"]) == 2
            finally:
                session.close()
                await runtime._model_client.close()

    asyncio.run(run())


@pytest.mark.parametrize("decision", [None, "approve", "reject"])
def test_main_pause_keeps_approval_and_settles_it_before_next_model(tmp_path, decision):
    backend = PauseBackend()
    model = PausingModel([[tool_item("install_workspace_skill", ARGS, "install")], [commit_item()]])

    async def execute(request, *, pause_before=False, pause_on_response=False):
        runtime = make_runtime(tmp_path, backend, model)
        try:
            stream = runtime.start_execution_stream(request)
            if pause_before:
                assert stream.pause()
            if pause_on_response:
                model.stream = stream
            return [event async for event in stream.events()]
        finally:
            await runtime._model_client.close()

    async def run():
        request = request_fixture()
        events = await execute(request, pause_on_response=True)
        assert events[-1]["event"] == "agent.turn.paused" and not backend.installs
        checkpoint = events[-1]["data"]
        assert checkpoint["pending_sdk_tool_call_ids"] == ["install"] and len(model.inputs) == 1
        request = request.model_copy(update={"run_state": checkpoint["run_state"]})
        if decision is None:
            with pytest.raises(RuntimeCompatibilityError, match="decision"):
                await execute(request)
            assert not backend.installs and not backend.commits and len(model.inputs) == 1
            return
        backend.calls["install"]["status"] = "approved" if decision == "approve" else "rejected"
        request = request.model_copy(update={"approval_decisions": [AgentToolApprovalDecision(sdk_tool_call_id="install", action=decision)]})
        settled = await execute(request, pause_before=True)
        assert settled[-1]["event"] == "agent.turn.paused"
        assert settled[-1]["data"]["pending_sdk_tool_call_ids"] == []
        assert len(model.inputs) == 1 and len(backend.installs) == (1 if decision == "approve" else 0)
        request = request.model_copy(update={"run_state": settled[-1]["data"]["run_state"], "approval_decisions": []})
        final = await execute(request)
        assert final[-1]["event"] == "agent.turn.committed" and len(backend.commits) == 1
        assert len(model.inputs) == 2 and len(backend.installs) == (1 if decision == "approve" else 0)
        outputs = [item for item in model.inputs[-1] if item.get("type") == "function_call_output" and item.get("call_id") == "install"]
        assert len(outputs) == 1
        if decision == "reject":
            assert "rejected" in outputs[0]["output"]

    asyncio.run(run())


@pytest.mark.parametrize("corruption", ["phase", "attempt", "candidate", "initial", "missing-initial", "started-initial"])
def test_invalid_main_checkpoint_cannot_restart_executed_work(tmp_path, corruption):
    backend = PauseBackend()
    model = PausingModel([[tool_item("validate_workspace_skill", {"root_path": "skills/my-skill"}, "read")]])

    async def run():
        runtime = make_runtime(tmp_path, backend, model)
        try:
            stream = runtime.start_execution_stream(request_fixture())
            if corruption == "started-initial":
                model.stream = stream
            else:
                assert stream.pause()
            events = [event async for event in stream.events()]
            state = deepcopy(events[-1]["data"]["run_state"])
            payload = state["context"]["context"]
            if corruption == "phase":
                payload["main_execution_phase"] = "foreign"
            elif corruption == "attempt":
                payload["main_repair_attempt"] = True
            elif corruption == "candidate":
                payload["main_repair_candidate"] = {"invalid": "type"}
            elif corruption == "missing-initial":
                payload.pop("main_unstarted_input")
            else:
                payload["main_unstarted_input"] = "Do not resume the original work"
            count = len(model.inputs)
            restored = request_fixture().model_copy(update={"run_state": state})
            with pytest.raises(RuntimeCompatibilityError):
                _ = [event async for event in runtime.start_execution_stream(restored).events()]
            assert len(model.inputs) == count and not backend.commits
        finally:
            await runtime._model_client.close()

    asyncio.run(run())


@pytest.mark.parametrize("cancel", [False, True])
def test_authenticated_main_pause_http_controls_real_sdk_stream(tmp_path, cancel):
    backend = PauseBackend()

    async def run():
        entered, release = asyncio.Event(), asyncio.Event()

        class BlockedModel(StreamingSequence):
            async def stream_response(self, *args, **kwargs):
                entered.set()
                await release.wait()
                async for event in super().stream_response(*args, **kwargs):
                    yield event

        model = BlockedModel([[tool_item("validate_workspace_skill", {"root_path": "skills/my-skill"}, "read")]])
        runtime = make_runtime(tmp_path, backend, model)
        try:
            transport = httpx.ASGITransport(app=create_app(runtime._settings, runtime))
            async with httpx.AsyncClient(transport=transport, base_url="http://sidecar.test", headers={"Authorization": "Bearer internal-test-token"}) as client:
                pending = asyncio.create_task(client.post("/internal/v1/agent/execute-stream", json=request_fixture().model_dump(mode="json")))
                try:
                    await asyncio.wait_for(entered.wait(), 3)
                    path = "/internal/v1/agent/runs/t/pause"
                    denied = await client.post(path, headers={"Authorization": "Bearer invalid"})
                    assert denied.status_code == 401
                    missing = await client.post("/internal/v1/agent/runs/foreign/pause")
                    assert missing.json() == {"run_id": "foreign", "accepted": False}
                    duplicate = await client.post("/internal/v1/agent/execute-stream", json=request_fixture().model_dump(mode="json"))
                    assert duplicate.status_code == 409
                    for _ in range(2):
                        paused = await client.post(path)
                        assert paused.json() == {"run_id": "t", "accepted": True}
                    if cancel:
                        cancelled = await client.post("/internal/v1/agent/runs/t/cancel")
                        assert cancelled.json()["accepted"] is True
                        assert (await client.post(path)).json()["accepted"] is False
                    release.set()
                    response = await asyncio.wait_for(pending, 3)
                    assert response.status_code == 200
                    events = [json.loads(line[6:]) for line in response.text.splitlines() if line.startswith("data: ")]
                    last = events[-1]
                    assert last["event_type"] == ("agent.turn.cancelled" if cancel else "agent.turn.paused")
                    assert last["terminal"] is cancel
                    assert not backend.commits
                    if not cancel:
                        assert last["payload"]["run_state"]["model_responses"]
                    assert (await client.post(path)).json()["accepted"] is False
                finally:
                    release.set()
                    if not pending.done():
                        pending.cancel()
                    await asyncio.gather(pending, return_exceptions=True)
        finally:
            await runtime._model_client.close()

    asyncio.run(run())
