import asyncio
import base64
from types import SimpleNamespace

import pytest
from agents import Agent, Runner, RunConfig
from agents.computer import AsyncComputer
from agents.exceptions import UserError
from agents.items import ModelResponse
from agents.models.openai_responses import OpenAIResponsesModel
from agents.usage import Usage
from openai.types.responses import ResponseComputerToolCall

from content_agent_sidecar.native_computer import ComputerSessionBinding, ComputerSessionError, ManagedComputer, managed_computer_tool
from test_run_state_approval import _text_response


PNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="


class Transport:
    def __init__(self):
        self.calls = []
        self.closed = 0
        self.edit = {}

    async def execute(self, binding, sequence, action):
        self.calls.append((binding, sequence, action))
        return {"session_id": binding.session_id, "generation": binding.generation, "sequence": sequence,
                "authorized": True, "status": "completed", "png_base64": PNG, **self.edit}

    async def close(self, binding):
        self.closed += 1


def computer(transport, **kwargs):
    return ManagedComputer(ComputerSessionBinding("isolated-session", 1, 1, 1), transport, **kwargs)


def test_sdk_async_computer_forwards_every_action_with_monotonic_identity():
    async def run():
        transport = Transport()
        instance = computer(transport)
        assert isinstance(instance, AsyncComputer)
        assert instance.environment == "browser" and instance.dimensions == (1, 1)
        await instance.click(0, 0, keys=["CTRL"])
        await instance.double_click(0, 0)
        await instance.scroll(0, 0, 1, -2)
        await instance.type("fixture")
        await instance.wait()
        await instance.move(0, 0)
        await instance.keypress(["ENTER"])
        await instance.drag([(0, 0), (0, 0)])
        assert await instance.screenshot() == PNG
        assert [call[1] for call in transport.calls] == list(range(1, 10))
        assert [call[2]["type"] for call in transport.calls] == ["click", "double_click", "scroll", "type", "wait", "move", "keypress", "drag", "screenshot"]
        await instance.close()
        await instance.close()
        assert transport.closed == 1
        with pytest.raises(ComputerSessionError, match="closed"):
            await instance.wait()
        assert len(transport.calls) == 9

    asyncio.run(run())


@pytest.mark.parametrize("edit", [
    {"authorized": False}, {"authorized": 1}, {"status": "pending_approval"},
    {"session_id": "foreign"}, {"generation": 2}, {"sequence": 2}, {"sequence": True},
])
def test_unconfirmed_receipt_closes_session_and_cannot_replay(edit):
    async def run():
        transport = Transport()
        transport.edit = edit
        instance = computer(transport)
        with pytest.raises(ComputerSessionError, match="receipt"):
            await instance.wait()
        with pytest.raises(ComputerSessionError, match="closed"):
            await instance.wait()
        assert len(transport.calls) == transport.closed == 1

    asyncio.run(run())


@pytest.mark.parametrize("action", [
    lambda c: c.click(-1, 0), lambda c: c.move(1, 0), lambda c: c.click(True, 0),
    lambda c: c.click(0, 0, "unsupported"), lambda c: c.scroll(0, 0, 100001, 0),
    lambda c: c.type("x" * 65537), lambda c: c.keypress(["\n"]), lambda c: c.drag([(0, 0)]),
])
def test_invalid_actions_never_reach_transport(action):
    transport = Transport()
    with pytest.raises(ComputerSessionError):
        asyncio.run(action(computer(transport)))
    assert not transport.calls


@pytest.mark.parametrize("encoded", [None, "not base64", base64.b64encode(b"not png").decode()])
def test_invalid_screenshot_is_not_returned_to_model(encoded):
    async def run():
        transport = Transport()
        transport.edit = {"png_base64": encoded}
        with pytest.raises(ComputerSessionError, match="screenshot"):
            await computer(transport).screenshot()
        assert transport.closed == 1

    asyncio.run(run())


@pytest.mark.parametrize("cancel", [False, True])
def test_timeout_or_cancellation_stops_session_without_replay(cancel):
    async def run():
        entered = asyncio.Event()

        class WaitingTransport(Transport):
            async def execute(self, binding, sequence, action):
                self.calls.append(action)
                entered.set()
                await asyncio.Event().wait()

        transport = WaitingTransport()
        instance = computer(transport, timeout_seconds=0.05)
        task = asyncio.create_task(instance.wait())
        await asyncio.wait_for(entered.wait(), 1)
        if cancel:
            task.cancel()
        with pytest.raises(asyncio.CancelledError if cancel else TimeoutError):
            await task
        assert len(transport.calls) == transport.closed == 1
        with pytest.raises(ComputerSessionError, match="closed"):
            await instance.wait()

    asyncio.run(run())


@pytest.mark.parametrize("approved", [True, False, "approved", {"approved": True}, None])
def test_sdk_computer_safety_check_requires_explicit_true_before_actions(approved):
    class Model(OpenAIResponsesModel):
        def __init__(self):
            super().__init__(model="isolated-computer", openai_client=SimpleNamespace())
            self.calls = 0

        async def get_response(self, *args, **kwargs):
            self.calls += 1
            if self.calls > 1:
                return _text_response()
            return ModelResponse(output=[ResponseComputerToolCall(
                id="computer-item", call_id="computer-call", type="computer_call", status="completed",
                action={"type": "click", "x": 0, "y": 0, "button": "left"},
                pending_safety_checks=[{"id": "check-1", "code": "sensitive_domain", "message": "fixture"}],
            )], usage=Usage(requests=1), response_id="response-1")

    async def run():
        transport = Transport()
        opened = []
        checked = []
        context = SimpleNamespace(project_id="p", conversation_id="c", agent_turn_id="turn")

        async def open_session(owner):
            assert owner is context
            opened.append(True)
            return computer(transport)

        async def authorize(data):
            assert data.ctx_wrapper.context is context
            checked.append((data.tool_call.call_id, data.safety_check.id))
            return approved

        agent = Agent(name="Managed computer", model=Model(), tools=[managed_computer_tool(open_session, authorize)])
        if approved is True:
            result = await Runner.run(agent, "Click", context=context, run_config=RunConfig(tracing_disabled=True))
            assert result.final_output
            assert [entry[2]["type"] for entry in transport.calls] == ["click", "screenshot"]
            assert transport.closed == 1
        else:
            with pytest.raises(UserError, match="not acknowledged"):
                await Runner.run(agent, "Click", context=context, run_config=RunConfig(tracing_disabled=True))
            assert not transport.calls
        assert checked == [("computer-call", "check-1")]

    asyncio.run(run())


@pytest.mark.parametrize("suppress_cancel", [False, True])
def test_close_interrupts_transport_and_discards_late_success(suppress_cancel):
    async def run():
        entered = asyncio.Event()

        class WaitingTransport(Transport):
            async def execute(self, binding, sequence, action):
                entered.set()
                try:
                    await asyncio.Event().wait()
                except asyncio.CancelledError:
                    if not suppress_cancel:
                        raise
                return await super().execute(binding, sequence, action)

        transport = WaitingTransport()
        instance = computer(transport)
        active = asyncio.create_task(instance.wait())
        await asyncio.wait_for(entered.wait(), timeout=1)
        queued = asyncio.create_task(instance.wait())
        try:
            await asyncio.wait_for(instance.close(), timeout=1)
            results = await asyncio.gather(active, queued, return_exceptions=True)
            assert isinstance(results[0], ComputerSessionError if suppress_cancel else asyncio.CancelledError)
            assert isinstance(results[1], ComputerSessionError)
            assert transport.closed == 1
            assert len(transport.calls) == int(suppress_cancel)
        finally:
            for task in (active, queued):
                if not task.done():
                    task.cancel()
            await asyncio.gather(active, queued, return_exceptions=True)

    asyncio.run(run())
