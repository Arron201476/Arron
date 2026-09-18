import asyncio
import json

import pytest
from agents import Agent, RunConfig, Runner, RunState, function_tool

from test_stateful_execution import StreamingSequence, final_item, tool_item


@pytest.mark.parametrize("approval", [False, True])
def test_native_after_turn_pause_preserves_completed_tools_and_pending_approval(tmp_path, approval):
    writes = []

    @function_tool(needs_approval=approval)
    async def save_once(value: str) -> str:
        writes.append(value)
        return "saved"

    model = StreamingSequence([
        [tool_item("save_once", {"value": "one write"}, "write")],
        [final_item({"summary": "done"})],
    ])
    config = RunConfig(tracing_disabled=True)

    def agent():
        return Agent(name="Pause probe", model=model, tools=[save_once])

    async def run():
        result = Runner.run_streamed(agent(), "Save one value", run_config=config)
        stopped = False
        async for event in result.stream_events():
            if event.type == "raw_response_event" and not stopped:
                result.cancel(mode="after_turn")
                stopped = True
        assert stopped and result.is_complete
        assert result.final_output is None and len(model.inputs) == 1
        assert writes == ([] if approval else ["one write"])
        assert bool(result.interruptions) == approval
        checkpoint = tmp_path / "native-run-state.json"
        checkpoint.write_text(json.dumps(result.to_state().to_json()), encoding="utf-8")
        rebuilt = agent()
        state = await RunState.from_json(rebuilt, json.loads(checkpoint.read_text(encoding="utf-8")))
        pending = state.get_interruptions()
        assert len(pending) == int(approval)
        if approval:
            state.approve(pending[0])
        resumed = Runner.run_streamed(rebuilt, state, run_config=config)
        async for _ in resumed.stream_events():
            pass
        assert json.loads(resumed.final_output) == {"summary": "done"}
        assert writes == ["one write"]
        assert len(model.inputs) == 2 and not model.responses
        outputs = [item for item in model.inputs[-1] if item.get("type") == "function_call_output" and item.get("call_id") == "write"]
        assert len(outputs) == 1 and outputs[0]["output"] == "saved"
        assert resumed.context_wrapper.usage.total_tokens == 24

    asyncio.run(run())
