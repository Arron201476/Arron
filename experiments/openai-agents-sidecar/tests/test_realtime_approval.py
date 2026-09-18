import asyncio
from unittest.mock import AsyncMock

import pytest
from agents import FunctionTool
from agents.realtime import RealtimeAgent
from agents.realtime.model_events import RealtimeModelToolCallEvent

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.native_realtime import RealtimeSessionError, managed_realtime_session, realtime_approval_refresher
from test_native_realtime import Model, config, owner
from test_tool_approval_refresh import Backend


@pytest.mark.parametrize("decision", ["approved", "rejected", "pending_approval"])
@pytest.mark.parametrize("invalidation", [None, "project_id", "dispatch_generation", "execution_attempt_id", "stop"])
def test_sdk_realtime_approval_uses_refreshed_durable_receipt(decision, invalidation):
    async def run():
        context = owner()
        context.active_tool_calls = {}
        backend = Backend()
        provider = AgentToolProvider(backend, [])
        invoked = []
        invocation_started = asyncio.Event()

        async def needs_approval(wrapper, arguments, call_id):
            call = await provider.ensure_call(wrapper, "runtime:action", arguments, call_id)
            return call["status"] == "pending_approval"

        async def invoke(wrapper, arguments):
            assert context.active_tool_calls[wrapper.tool_call_id]["status"] == "approved"
            invoked.append(arguments)
            invocation_started.set()
            return "done"

        tool = FunctionTool(name="action", description="fixture", params_json_schema={"type": "object",
            "properties": {}, "additionalProperties": False}, on_invoke_tool=invoke, needs_approval=needs_approval)

        class ConditionalModel(Model):
            async def send_event_if(self, event, send_if):
                if not send_if():
                    return False
                await self.send_event(event)
                return True

        model = ConditionalModel()
        refresh_receipt = realtime_approval_refresher(provider, context)

        async def refresh(event):
            status = await refresh_receipt(event)
            if invalidation == "stop":
                await connection.close()
            elif invalidation is not None:
                setattr(context, invalidation, 1 if invalidation == "dispatch_generation" else "changed")
            return status

        async with managed_realtime_session(RealtimeAgent(name="fixture", tools=[tool]), model, context, config(),
                approval_refresher=refresh) as connection:
            approve = AsyncMock(wraps=connection._session.approve_tool_call)
            reject = AsyncMock(wraps=connection._session.reject_tool_call)
            connection._session.approve_tool_call = approve
            connection._session.reject_tool_call = reject
            await model.listeners[0].on_event(RealtimeModelToolCallEvent(name="action", call_id="sdk-1", arguments="{}"))
            events = connection.__aiter__()
            try:
                async with asyncio.timeout(2):
                    while (await anext(events)).type != "tool_approval_required":
                        pass
                assert not invoked
                backend.status = decision
                if invalidation is not None:
                    with pytest.raises(RealtimeSessionError):
                        await connection.resolve_approval("sdk-1")
                    await asyncio.sleep(0)
                    assert not invoked and model.closed
                    approve.assert_not_awaited()
                    reject.assert_not_awaited()
                    return
                assert await connection.resolve_approval("sdk-1") == decision
                if decision == "approved":
                    await asyncio.wait_for(invocation_started.wait(), timeout=2)
                assert bool(invoked) == (decision == "approved")
                assert backend.calls == 2
            finally:
                await events.aclose()
        assert model.closed
    asyncio.run(run())
