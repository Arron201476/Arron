import asyncio
from copy import deepcopy
import json

import pytest
import httpx2 as httpx
from agents import Agent, Runner, RunConfig, RunState, UserError, function_tool
from openai import APIConnectionError, APIStatusError

from content_agent_sidecar.model_failure_boundary import ModelFailureBoundary

from test_stateful_execution import StreamingSequence, final_item, tool_item


@pytest.mark.parametrize("after_write", [False, True])
def test_fixed_sdk_can_restore_a_failed_model_boundary_without_replaying_completed_tools(after_write):
    async def run():
        writes = []

        @function_tool
        async def write_once(value: str) -> str:
            writes.append(value)
            return "Durable receipt version 1"

        responses = [[tool_item("write_once", {"value": "original"}, "write-1")]] if after_write else []
        error = APIConnectionError(message="Model transport failed", request=httpx.Request("POST", "https://fixture.invalid/responses"))
        model = StreamingSequence([*responses, error])
        agent = Agent(name="FailureBoundary", instructions="Continue the same task.", model=model, tools=[write_once])
        boundary = ModelFailureBoundary()
        result = Runner.run_streamed(agent, "Original request", max_turns=8, hooks=boundary, run_config=RunConfig(tracing_disabled=True))
        with pytest.raises(APIConnectionError, match="Model transport failed"):
            async for _ in result.stream_events():
                pass
        assert boundary.permits_checkpoint(error, result)
        snapshot = result.to_state().to_json(context_serializer=lambda _: {})
        frozen = json.loads(json.dumps(snapshot))
        assert writes == (["original"] if after_write else [])
        replacement = StreamingSequence([[final_item({"done": True})]])
        restored_agent = agent.clone(model=replacement)
        state = await RunState.from_json(restored_agent, deepcopy(frozen), context_override={})
        resumed = Runner.run_streamed(restored_agent, state, run_config=RunConfig(tracing_disabled=True))
        async for _ in resumed.stream_events():
            pass
        assert resumed.final_output and len(replacement.inputs) == 1
        assert writes == (["original"] if after_write else [])
        assert resumed.max_turns == 8
        if after_write:
            receipts = [item for item in replacement.inputs[0] if item.get("type") == "function_call_output"]
            assert len(receipts) == 1 and "Durable receipt version 1" in receipts[0]["output"]
            assert resumed.context_wrapper.usage.requests == 2
        assert snapshot == frozen

    asyncio.run(run())


@pytest.mark.parametrize("status", [400, 401, 403, 404, 408, 409, 422, 429, 500, 503])
def test_model_failure_classification_does_not_recover_permanent_errors(status):
    from types import SimpleNamespace
    request = httpx.Request("POST", "https://fixture.invalid/responses")
    response = httpx.Response(status, request=request)
    error = APIStatusError("Fixture status", response=response, body={})
    boundary = ModelFailureBoundary()
    boundary.model_active = True
    result = SimpleNamespace(is_complete=True, interruptions=[], final_output=None, current_turn=2, max_turns=8)
    assert boundary.permits_checkpoint(error, result) == (status in {408, 429, 500, 503})
    boundary.active_tools = 1
    assert not boundary.permits_checkpoint(error, result)
    boundary.active_tools = 0
    result.current_turn = result.max_turns
    assert not boundary.permits_checkpoint(error, result)


def test_sdk_tool_connection_failure_is_not_a_recoverable_model_boundary():
    async def run():
        error = APIConnectionError(request=httpx.Request("POST", "https://fixture.invalid/write"))

        @function_tool(failure_error_function=None)
        async def uncertain_write() -> str:
            raise error

        model = StreamingSequence([[tool_item("uncertain_write", {}, "write")]])
        boundary = ModelFailureBoundary()
        result = Runner.run_streamed(Agent(name="Unsafe write", model=model, tools=[uncertain_write]), "Write",
                                     hooks=boundary, run_config=RunConfig(tracing_disabled=True))
        with pytest.raises(UserError) as raised:
            async for _ in result.stream_events():
                pass
        assert raised.value.__cause__ is error
        assert not boundary.permits_checkpoint(raised.value, result)
        assert not boundary.permits_checkpoint(error, result)
        assert boundary.active_tools == 1 and not boundary.model_active

    asyncio.run(run())
