import asyncio
import json

import pytest
from agents import Agent, GuardrailFunctionOutput, InputGuardrailTripwireTriggered, RunConfig, Runner, RunState, SQLiteSession, UserError, function_tool, input_guardrail

from test_stateful_execution import StreamingSequence, final_item, tool_item


async def drain(result, *, pause=False):
    async for event in result.stream_events():
        if pause and event.type == "raw_response_event":
            result.cancel(mode="after_turn")


@pytest.mark.parametrize("with_session", [False, True])
def test_native_pending_input_is_ordered_durable_and_does_not_replay_tools(tmp_path, with_session):
    writes = []

    @function_tool
    async def save_once(value: str) -> str:
        writes.append(value)
        return "saved"

    config = RunConfig(tracing_disabled=True)
    database = tmp_path / "native-session.db"

    async def run():
        session = SQLiteSession("pending-input", database) if with_session else None
        try:
            first_model = StreamingSequence([[tool_item("save_once", {"value": "one"}, "save")]])
            first_agent = Agent(name="Pending input", model=first_model, tools=[save_once])
            first = Runner.run_streamed(first_agent, "Original request", session=session, run_config=config)
            await drain(first, pause=True)
            state = first.to_state()
            extra = [{"role": "user", "content": "Second instruction"}]
            state.add_input("First instruction")
            state.add_input(extra)
            extra[0]["content"] = "Mutated caller value"
            detached = state.pending_input
            detached.clear()
            assert [item["content"] for item in state.pending_input] == ["First instruction", "Second instruction"]
            checkpoint = tmp_path / "state.json"
            checkpoint.write_text(json.dumps(state.to_json()), encoding="utf-8")
            if session:
                assert "First instruction" not in json.dumps(await session.get_items())
                session.close()
                session = SQLiteSession("pending-input", database)
            next_model = StreamingSequence([[final_item({"summary": "Complete"})]])
            agent = Agent(name="Pending input", model=next_model, tools=[save_once])
            restored = await RunState.from_json(agent, json.loads(checkpoint.read_text(encoding="utf-8")))
            assert len(restored.pending_input) == 2
            result = Runner.run_streamed(agent, restored, session=session, run_config=config)
            await drain(result)
            assert writes == ["one"] and len(first_model.inputs) == len(next_model.inputs) == 1
            assert [item["content"] for item in next_model.inputs[0] if item.get("role") == "user"] == ["Original request", "First instruction", "Second instruction"]
            assert result.to_state().pending_input == []
            if session:
                assert [item["content"] for item in await session.get_items() if item.get("role") == "user"] == ["Original request", "First instruction", "Second instruction"]
            terminal = result.to_state()
            with pytest.raises(UserError, match="terminal"):
                terminal.add_input("Too late")
            assert terminal.pending_input == []
        finally:
            if session:
                session.close()

    asyncio.run(run())


@pytest.mark.parametrize("decision", ["approve", "reject", "unresolved"])
def test_native_append_does_not_override_approvals_and_approved_tools_precede_input(decision):
    order = []

    @function_tool(needs_approval=True)
    async def save_once(value: str) -> str:
        order.append("write")
        return "saved"

    @input_guardrail(run_in_parallel=False)
    async def record_input(ctx, agent, input):
        if "Do not write" in json.dumps(input):
            order.append("admit-new-instruction")
        return GuardrailFunctionOutput(output_info=None, tripwire_triggered=False)

    async def run():
        model = StreamingSequence([[tool_item("save_once", {"value": "original"}, "save")], [final_item({"summary": "Done"})]])
        agent = Agent(name="Approval input", model=model, tools=[save_once], input_guardrails=[record_input])
        first = Runner.run_streamed(agent, "Save original", run_config=RunConfig(tracing_disabled=True))
        await drain(first)
        state = first.to_state()
        state.add_input("Do not write")
        state = await RunState.from_json(agent, json.loads(json.dumps(state.to_json())))
        pending = state.get_interruptions()
        assert len(pending) == 1 and order == []
        if decision != "unresolved":
            getattr(state, decision)(pending[0])
        resumed = Runner.run_streamed(agent, state, run_config=RunConfig(tracing_disabled=True))
        await drain(resumed)
        if decision == "unresolved":
            assert len(resumed.interruptions) == 1 and len(model.inputs) == 1 and order == []
            assert resumed.to_state().pending_input == [{"role": "user", "content": "Do not write"}]
        else:
            assert order == (["write", "admit-new-instruction"] if decision == "approve" else ["admit-new-instruction"])
            assert len(model.inputs) == 2 and resumed.to_state().pending_input == []

    asyncio.run(run())


def test_native_pending_input_guardrail_rejection_retains_pending_input_before_model():
    @function_tool
    async def inspect() -> str:
        return "read"

    @input_guardrail(run_in_parallel=False)
    async def reject_new_input(ctx, agent, input):
        return GuardrailFunctionOutput(output_info=None, tripwire_triggered="Forbidden" in json.dumps(input))

    async def run():
        model = StreamingSequence([[tool_item("inspect", {}, "read")]])
        agent = Agent(name="Input boundary", model=model, tools=[inspect], input_guardrails=[reject_new_input])
        first = Runner.run_streamed(agent, "Read", run_config=RunConfig(tracing_disabled=True))
        await drain(first, pause=True)
        state = first.to_state()
        state.add_input("Forbidden")
        resumed = Runner.run_streamed(agent, state, run_config=RunConfig(tracing_disabled=True))
        with pytest.raises(InputGuardrailTripwireTriggered):
            await drain(resumed)
        assert len(model.inputs) == 1
        assert resumed.to_state().pending_input == [{"role": "user", "content": "Forbidden"}]

    asyncio.run(run())


@pytest.mark.parametrize("behavior", ["stop_on_first_tool", {"stop_at_tool_names": ["save_once"]}, "custom", "max-turns"])
def test_native_append_rejects_approval_states_that_may_finish_without_another_model_call(behavior):
    @function_tool(needs_approval=True)
    async def save_once() -> str:
        raise AssertionError("no approval was granted")

    async def custom_behavior(ctx, results):
        raise AssertionError("no approval was granted")

    async def run():
        model = StreamingSequence([[tool_item("save_once", {}, "save")]])
        agent = Agent(name="Append boundary", model=model, tools=[save_once], tool_use_behavior=custom_behavior if behavior == "custom" else "run_llm_again" if behavior == "max-turns" else behavior)
        first = Runner.run_streamed(agent, "Save", max_turns=1 if behavior == "max-turns" else 4, run_config=RunConfig(tracing_disabled=True))
        await drain(first)
        state = first.to_state()
        with pytest.raises(UserError, match="remaining model turns|tool result may end"):
            state.add_input("New requirement")
        assert state.pending_input == [] and len(state.get_interruptions()) == 1

    asyncio.run(run())
