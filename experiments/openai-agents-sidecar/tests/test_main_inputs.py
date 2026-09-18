import asyncio
from copy import deepcopy
import json

import pytest
from agents import Agent, RunContextWrapper, SQLiteSession, function_tool

from content_agent_sidecar.contracts import AgentToolApprovalDecision
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.runtime import AgentContext, RuntimeCompatibilityError, stop_after_commit
from test_main_pause import PauseBackend, PausingModel, commit_item, make_runtime, request_fixture
from test_project_skills import ARGS
from test_stateful_execution import StreamingSequence, final_item, tool_item


def inputs(*contents):
    return [{"input_id": f"input-{index+1}", "agent_turn_id": "t", "sequence": index+1, "content": content, "status": "received"} for index, content in enumerate(contents)]


async def execute(tmp_path, backend, model, request, *, pause_before=False, pause_on_response=False, repair=None, guardrail=None):
    runtime = make_runtime(tmp_path, backend, model, repair)
    try:
        if guardrail is not None:
            runtime._guardrails = guardrail
        stream = runtime.start_execution_stream(request)
        if pause_before:
            assert stream.pause()
        if pause_on_response:
            model.stream = stream
        return [event async for event in stream.events()]
    finally:
        await runtime._model_client.close()


def test_main_append_rebuild_receipts_and_completed_tools_do_not_replay(tmp_path):
    backend = PauseBackend()
    model = PausingModel([[tool_item("validate_workspace_skill", {"root_path": "skills/my-skill"}, "read-1")],
                         [tool_item("validate_workspace_skill", {"root_path": "skills/my-skill"}, "read-2")], [commit_item()]])

    async def run():
        request = request_fixture()
        first = await execute(tmp_path, backend, model, request, pause_on_response=True)
        assert first[-1]["event"] == "agent.turn.paused" and first[-1]["data"].get("included_input_ids", []) == []
        request = request.model_copy(update={"run_state": first[-1]["data"]["run_state"], "additional_inputs": inputs("New requirement")})
        second = await execute(tmp_path, backend, model, request, pause_on_response=True)
        assert second[-1]["event"] == "agent.turn.paused" and second[-1]["data"]["included_input_ids"] == ["input-1"]
        request = request.model_copy(update={"run_state": second[-1]["data"]["run_state"], "additional_inputs": inputs("New requirement", "Another requirement")})
        final = await execute(tmp_path, backend, model, request)
        assert final[-1]["event"] == "agent.turn.committed" and final[-1]["data"]["included_input_ids"] == ["input-1", "input-2"]
        assert len(model.inputs) == 3 and len(backend.commits) == 1
        messages = [item["content"] for item in model.inputs[-1] if item.get("role") == "user"]
        assert messages[-2:] == ["New requirement", "Another requirement"] and messages.count("New requirement") == 1
        assert [item["sdk_tool_call_id"] for item in backend.begins].count("read-1") == 1
        assert [item["sdk_tool_call_id"] for item in backend.begins].count("read-2") == 1
        runtime = make_runtime(tmp_path, backend, StreamingSequence([]))
        session = runtime._session("p", "c", "")
        try:
            user_items = [item["content"] for item in await session.get_items() if item.get("role") == "user"]
            assert len(user_items) == 3 and user_items[-2:] == ["New requirement", "Another requirement"]
        finally:
            session.close()
            await runtime._model_client.close()

    asyncio.run(run())


@pytest.mark.parametrize("blocked", [False, True])
def test_main_append_repeated_unstarted_pause_preserves_inputs_and_initial_guardrail(tmp_path, blocked):
    backend = PauseBackend()
    model = StreamingSequence([[commit_item()]])

    async def run():
        request = request_fixture().model_copy(update={"additional_inputs": inputs("Queued extra")})
        for index in range(2):
            events = await execute(tmp_path, backend, model, request, pause_before=True)
            assert events[-1]["event"] == "agent.turn.paused" and events[-1]["data"].get("included_input_ids", []) == []
            assert model.inputs == [] and not backend.commits
            request = request.model_copy(update={"run_state": events[-1]["data"]["run_state"], "additional_inputs": inputs("Queued extra", "Added while paused")})
        if blocked:
            with pytest.raises(RuntimeCompatibilityError):
                await execute(tmp_path, backend, model, request, guardrail=SDKGuardrailPolicy(max_input_chars=1))
            assert not model.inputs and not backend.commits
        else:
            events = await execute(tmp_path, backend, model, request)
            assert events[-1]["data"]["included_input_ids"] == ["input-1", "input-2"]
            assert len(model.inputs) == 1
            messages = [item["content"] for item in model.inputs[0] if item.get("role") == "user"]
            assert messages[-2:] == ["Queued extra", "Added while paused"] and len(messages) == 3

    asyncio.run(run())


@pytest.mark.parametrize("decision", [None, "approve", "reject"])
@pytest.mark.parametrize("pause_after_settle", [False, True])
def test_main_append_settles_old_approval_before_new_model_and_can_pause_again(tmp_path, decision, pause_after_settle):
    backend = PauseBackend()
    model = PausingModel([[tool_item("install_workspace_skill", ARGS, "install")], [commit_item()]])

    async def run():
        request = request_fixture()
        first = await execute(tmp_path, backend, model, request)
        assert first[-1]["event"] == "agent.turn.waiting_approval" and not backend.installs
        request = request.model_copy(update={"run_state": first[-1]["data"]["run_state"], "additional_inputs": inputs("New requirements after old authorization")})
        if decision is None:
            with pytest.raises(RuntimeCompatibilityError, match="decision"):
                await execute(tmp_path, backend, model, request)
            assert not backend.installs and not backend.commits and len(model.inputs) == 1
            return
        backend.calls["install"]["status"] = "approved" if decision == "approve" else "rejected"
        request = request.model_copy(update={"approval_decisions": [AgentToolApprovalDecision(sdk_tool_call_id="install", action=decision)]})
        events = await execute(tmp_path, backend, model, request, pause_before=pause_after_settle)
        if pause_after_settle:
            assert events[-1]["event"] == "agent.turn.paused" and events[-1]["data"].get("included_input_ids", []) == []
            assert events[-1]["data"]["pending_sdk_tool_call_ids"] == [] and len(model.inputs) == 1
            request = request.model_copy(update={"run_state": events[-1]["data"]["run_state"], "approval_decisions": []})
            events = await execute(tmp_path, backend, model, request)
        assert events[-1]["event"] == "agent.turn.committed" and events[-1]["data"]["included_input_ids"] == ["input-1"]
        assert len(model.inputs) == 2 and len(backend.installs) == (1 if decision == "approve" else 0)
        outputs = [item for item in model.inputs[-1] if item.get("call_id") == "install" and item.get("type") == "function_call_output"]
        assert len(outputs) == 1
        messages = [item["content"] for item in model.inputs[-1] if item.get("role") == "user"]
        assert messages[-1] == "New requirements after old authorization"

    asyncio.run(run())


def test_main_new_input_does_not_survive_a_terminal_old_tool_as_false_receipt(tmp_path):
    writes = []

    @function_tool(needs_approval=True)
    async def old_commit(ctx: RunContextWrapper[AgentContext]) -> str:
        writes.append("old")
        ctx.context.commit_result = {"reply": "Old tool committed"}
        return "done"

    async def run():
        model = StreamingSequence([[tool_item("old_commit", {}, "old")]])
        runtime = make_runtime(tmp_path, PauseBackend(), model)
        context = AgentContext("p", "c", None, agent_turn_id="t")
        agent = Agent(name="Main old commit", model=model, tools=[old_commit], tool_use_behavior=stop_after_commit)
        session = SQLiteSession("main", tmp_path / "old-commit.db")
        async def emit(_):
            pass
        try:
            first = await runtime._run_execution_agent(agent, "Original", context, session, emit, runtime._new_turn_observation())
            state = first.to_state()
            state.approve(state.get_interruptions()[0])
            context.main_inputs = inputs("Too late for a new model turn")
            final = await runtime._run_execution_agent(agent, state, context, session, emit, runtime._new_turn_observation())
            assert final.final_output is not None and writes == ["old"] and len(model.inputs) == 1
            assert context.main_included_input_ids == [] and context.main_input_ids == []
            assert "Too late" not in json.dumps(await session.get_items())
        finally:
            session.close()
            await runtime._model_client.close()

    asyncio.run(run())


def test_main_append_at_terminal_repair_does_not_restart_primary_or_duplicate_input(tmp_path):
    backend = PauseBackend()
    primary = PausingModel([[final_item({"uncommitted": "Original candidate"})]])
    repair = StreamingSequence([[commit_item()]])

    async def run():
        request = request_fixture()
        first = await execute(tmp_path, backend, primary, request, pause_on_response=True, repair=repair)
        assert first[-1]["data"]["run_state"]["context"]["context"]["main_execution_phase"] == "terminal_repair"
        request = request.model_copy(update={"run_state": first[-1]["data"]["run_state"], "additional_inputs": inputs("Repair using this final requirement")})
        final = await execute(tmp_path, backend, StreamingSequence([]), request, repair=repair)
        assert final[-1]["event"] == "agent.turn.committed" and final[-1]["data"]["included_input_ids"] == ["input-1"]
        assert len(primary.inputs) == len(repair.inputs) == 1
        messages = [item["content"] for item in repair.inputs[0] if item.get("role") == "user"]
        assert messages[-1] == "Repair using this final requirement"

    asyncio.run(run())


@pytest.mark.parametrize("mutation", ["body", "identity", "order", "duplicate", "missing", "hash", "receipt", "durable-receipt", "native-pending"])
def test_main_append_rejects_incompatible_checkpoint_without_a_model_call(tmp_path, mutation):
    backend = PauseBackend()
    model = PausingModel([[tool_item("validate_workspace_skill", {"root_path": "skills/my-skill"}, "read")], [commit_item()]])

    async def run():
        request = request_fixture()
        first = await execute(tmp_path, backend, model, request, pause_on_response=True)
        request = request.model_copy(update={"run_state": first[-1]["data"]["run_state"], "additional_inputs": inputs("Extra")})
        pending = await execute(tmp_path, backend, model, request, pause_before=True)
        state, items = deepcopy(pending[-1]["data"]["run_state"]), inputs("Extra")
        payload = state["context"]["context"]
        if mutation == "body":
            items[0]["content"] = "Altered"
        elif mutation == "identity":
            items[0]["agent_turn_id"] = "foreign"
        elif mutation == "order":
            items[0]["sequence"] = True
        elif mutation == "duplicate":
            items.append({**items[0], "sequence": 2})
        elif mutation == "missing":
            items = []
        elif mutation == "hash":
            payload["main_input_hashes"] = []
        elif mutation == "receipt":
            payload["main_included_input_ids"] = ["input-1"]
        elif mutation == "durable-receipt":
            items[0]["status"] = "included"
        else:
            state["pending_input"] = []
        invalid = request.model_copy(update={"run_state": state, "additional_inputs": items})
        with pytest.raises(RuntimeCompatibilityError):
            await execute(tmp_path, backend, model, invalid)
        assert len(model.inputs) == 1 and not backend.commits

    asyncio.run(run())


@pytest.mark.parametrize("decision", [None, "approve", "reject"])
def test_main_resumed_additional_input_guardrail_rejects_before_model_and_receipt(tmp_path, decision):
    backend = PauseBackend()
    call = tool_item("validate_workspace_skill", {"root_path": "skills/my-skill"}, "read") if decision is None else tool_item("install_workspace_skill", ARGS, "install")
    model = PausingModel([[call]])

    async def run():
        request = request_fixture()
        first = await execute(tmp_path, backend, model, request, pause_on_response=decision is None)
        assert first[-1]["event"] == ("agent.turn.paused" if decision is None else "agent.turn.waiting_approval")
        changes = {"run_state": first[-1]["data"]["run_state"], "additional_inputs": inputs("Rejected additional requirement")}
        if decision is not None:
            backend.calls["install"]["status"] = "approved" if decision == "approve" else "rejected"
            changes["approval_decisions"] = [AgentToolApprovalDecision(sdk_tool_call_id="install", action=decision)]
        runtime = make_runtime(tmp_path, backend, model)
        runtime._guardrails = SDKGuardrailPolicy(max_input_chars=1)
        events = []
        session = runtime._session("p", "c", "")
        try:
            with pytest.raises(RuntimeCompatibilityError):
                async for event in runtime.start_execution_stream(request.model_copy(update=changes)).events():
                    events.append(event)
            assert len(model.inputs) == 1 and not backend.commits
            assert len(backend.installs) == (1 if decision == "approve" else 0)
            assert not any(event["data"].get("included_input_ids") for event in events)
            assert "Rejected additional requirement" not in json.dumps(await session.get_items())
        finally:
            session.close()
            await runtime._model_client.close()

    asyncio.run(run())
