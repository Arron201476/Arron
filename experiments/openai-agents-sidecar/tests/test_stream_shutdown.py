import asyncio
from contextlib import suppress

from fastapi import HTTPException
import pytest
from starlette.requests import ClientDisconnect

from content_agent_sidecar.app import create_app
from content_agent_sidecar.contracts import AgentExecutionRequest
from content_agent_sidecar.runtime import AgentExecutionStream
from test_app import execution_payload, settings


def test_stream_cancellation_joins_its_waiting_event_reader():
    async def scenario():
        reading = asyncio.Event()
        readers = []

        class ObservedQueue(asyncio.Queue):
            async def get(self):
                readers.append(asyncio.current_task())
                reading.set()
                return await super().get()

        async def operation(_emit):
            await asyncio.Event().wait()

        stream = AgentExecutionStream(operation)
        stream._queue = ObservedQueue()
        events = stream.events()
        pending = asyncio.create_task(anext(events))
        try:
            await asyncio.wait_for(reading.wait(), 2)
            pending.cancel()
            with pytest.raises(asyncio.CancelledError):
                await asyncio.wait_for(pending, 2)
            assert stream._task.done()
            assert len(readers) == 1 and readers[0].done(), "event queue reader survived stream cancellation"
        finally:
            pending.cancel()
            stream.cancel()
            for reader in readers:
                reader.cancel()
            await asyncio.gather(pending, stream._task, *readers, return_exceptions=True)
            await events.aclose()
    asyncio.run(scenario())


@pytest.mark.parametrize("spec", ["2.0", "2.4"])
@pytest.mark.parametrize("disconnect_at", ["headers", "event"])
def test_http_disconnect_keeps_turn_reserved_until_operation_cleanup_finishes(spec, disconnect_at):
    async def scenario():
        operation_started, cleanup_started, release_cleanup = asyncio.Event(), asyncio.Event(), asyncio.Event()
        disconnected = asyncio.Event()
        streams = []

        async def operation(emit):
            operation_started.set()
            try:
                await emit({"event": "agent.tool.started", "data": {"tool_name": "inspect_project"}})
                await asyncio.Event().wait()
            finally:
                cleanup_started.set()
                await release_cleanup.wait()

        class Runtime:
            def start_execution_stream(self, _request):
                stream = AgentExecutionStream(operation)
                streams.append(stream)
                return stream

        app = create_app(settings(), Runtime())
        endpoint = next(route.endpoint for route in app.routes if route.path == "/internal/v1/agent/execute-stream")
        payload = AgentExecutionRequest.model_validate(execution_payload())
        response = await endpoint(payload)
        await asyncio.wait_for(operation_started.wait(), 2)

        async def receive():
            await disconnected.wait()
            return {"type": "http.disconnect"}

        async def send(message):
            target = "http.response.start" if disconnect_at == "headers" else "http.response.body"
            if message["type"] == target:
                disconnected.set()
                if spec == "2.4":
                    raise OSError("client disconnected")
                await asyncio.Event().wait()

        scope = {"type": "http", "asgi": {"spec_version": spec}}
        delivering = asyncio.create_task(response(scope, receive, send))
        try:
            await asyncio.wait_for(disconnected.wait(), 2)
            await asyncio.wait_for(cleanup_started.wait(), 2)
            assert not delivering.done(), "HTTP cleanup returned while the Agent operation was still alive"
            with pytest.raises(HTTPException) as conflict:
                await endpoint(payload)
            assert conflict.value.status_code == 409 and len(streams) == 1
            release_cleanup.set()
            if spec == "2.4":
                with pytest.raises(ClientDisconnect):
                    await asyncio.wait_for(delivering, 2)
            else:
                await asyncio.wait_for(delivering, 2)
            assert streams[0]._task.done()
            next_response = await endpoint(payload)
            assert next_response.status_code == 200 and len(streams) == 2
            with suppress(ClientDisconnect):
                await next_response(scope, receive, send)
        finally:
            release_cleanup.set()
            for stream in streams:
                stream.cancel()
            await asyncio.gather(*(stream._task for stream in streams), return_exceptions=True)
            await response.body_iterator.aclose()
            if not delivering.done():
                delivering.cancel()
            await asyncio.gather(delivering, return_exceptions=True)
    asyncio.run(scenario())
