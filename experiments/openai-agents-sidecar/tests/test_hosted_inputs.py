from __future__ import annotations

import asyncio
import base64
import hashlib
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Thread
from typing import Any
from urllib.parse import parse_qs, urlsplit

import pytest
import httpx2
from agents import Agent, RunConfig, Runner, RunState
from agents.items import ModelResponse
from agents.models.openai_responses import OpenAIResponsesModel
from agents.tool_context import ToolContext
from agents.usage import Usage
from openai.types.responses import ResponseFunctionToolCall, ResponseOutputMessage
from openai import AsyncOpenAI
from pydantic import ValidationError

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.backend import BackendClient, BackendError, backend_activity
from content_agent_sidecar.hosted_inputs import HostedMaterialRequest, hosted_input_builder
from test_agent_tools import AuditBackend, StaticModel, _context
from test_hosted_tools import HostedModel, catalog


def input_metadata(data: bytes, *, image: bool = False) -> dict[str, Any]:
    return {
        "asset_id": "ast_input", "project_id": "prj-tools", "current_snapshot_id": "ass_input",
        "status": "available", "kind": "image" if image else "text",
        "original_filename": "image.png" if image else "table.csv",
        "detected_mime_type": "image/png" if image else "text/plain", "size_bytes": len(data),
        "checksum_algorithm": "sha256", "checksum": hashlib.sha256(data).hexdigest(),
    }


def request_params() -> dict[str, Any]:
    return {"instruction": "Process the supplied material", "input_assets": [{"asset_id": "ast_input", "asset_snapshot_id": "ass_input"}]}


@pytest.mark.parametrize("kind", ["code_interpreter", "image_generation"])
@pytest.mark.parametrize("approve", [False, True])
def test_native_hosted_material_approval_and_rebuilt_state(kind: str, approve: bool) -> None:
    backend = AuditBackend(catalog(kind, access="write", approval="always"))
    model, context, loaded = HostedModel(kind), _context(), []
    content = b"fixture-image" if kind == "image_generation" else b"name,value\nsource,42\n"

    async def load(project: str, asset: str, snapshot: str, *, max_bytes: int) -> tuple[dict[str, Any], bytes]:
        assert backend.started == [("tcall-1", "sdk_material")]
        loaded.append((project, asset, snapshot))
        return input_metadata(content, image=kind == "image_generation"), content

    async def save(*args: Any) -> dict[str, Any]:
        return {"asset": {"asset_id": "ast_output", "project_id": context.project_id, "source_type": "hosted_tool", "metadata": {"agent_tool_call_id": args[0], "sdk_tool_call_id": args[1]}, "original_filename": "generated.png"}, "asset_snapshot": {"asset_id": "ast_output", "asset_snapshot_id": "ass_output"}}

    backend.get_hosted_input_content = load  # type: ignore[attr-defined]
    backend.store_agent_tool_output = save  # type: ignore[attr-defined]

    async def execute() -> None:
        prepared = await AgentToolProvider(backend, [], hosted_model=model).prepare(context, [], set())
        tool = prepared.tools[0]
        args = json.dumps(request_params())
        parent = Agent(name="MaterialParent", tools=[tool], model=StaticModel(ModelResponse(
            output=[ResponseFunctionToolCall(id="fc_material", type="function_call", call_id="sdk_material", name=tool.name, arguments=args)],
            usage=Usage(), response_id="resp_material",
        )), tool_use_behavior="stop_on_first_tool")
        config = RunConfig(tracing_disabled=True, trace_include_sensitive_data=False)
        run = await Runner.run(parent, "Use this material", context=context, run_config=config)
        assert len(run.interruptions) == 1
        assert not loaded and not model.seen_inputs and not backend.started
        serialized = run.to_state().to_json(context_serializer=lambda _: {})
        fresh_context = _context()
        fresh_context.active_tool_calls = context.active_tool_calls
        fresh_context.active_tool_calls["sdk_material"]["status"] = "approved" if approve else "rejected"
        fresh = await AgentToolProvider(backend, [], hosted_model=model).prepare(fresh_context, [], set())
        fresh_parent = parent.clone(tools=fresh.tools)
        if not approve:
            fresh_parent = fresh_parent.clone(model=StaticModel(ModelResponse(output=[ResponseOutputMessage(
                id="msg_rejected", type="message", role="assistant", status="completed",
                content=[{"type": "output_text", "text": "The requested action was not executed.", "annotations": []}],
            )], usage=Usage(), response_id="resp_rejected")))
        state = await RunState.from_json(fresh_parent, serialized, context_override=fresh_context)
        if approve:
            state.approve(run.interruptions[0])
        else:
            state.reject(run.interruptions[0])
        resumed = await Runner.run(fresh_parent, state, run_config=config)
        assert not resumed.interruptions

    asyncio.run(execute())
    if not approve:
        assert not loaded and not model.seen_inputs and not backend.started
        return
    assert loaded == [("prj-tools", "ast_input", "ass_input")]
    parts = model.seen_inputs[0][0]["content"]
    native = next(part for part in parts if part["type"] == ("input_image" if kind == "image_generation" else "input_file"))
    encoded = native["image_url" if kind == "image_generation" else "file_data"]
    assert base64.b64decode(encoded.split(",", 1)[1]) == content
    assert model.seen_tools[0].name == kind
    if kind == "code_interpreter":
        assert native["filename"] == "table.csv"
        assert model.seen_tools[0].tool_config["container"] == {"type": "auto"}
    assert len(backend.completed) == 1 and not backend.failed
    assert backend.begun[0]["arguments"] == request_params()
    assert base64.b64encode(content).decode() not in json.dumps(backend.completed)


@pytest.mark.parametrize("params", [
    {"instruction": "Test", "input_assets": [{"asset_id": "ast_input"}]},
    {"instruction": "Test", "input_assets": [{"asset_id": "https://untrusted", "asset_snapshot_id": "ass_input"}]},
    {"instruction": "Test", "input_assets": request_params()["input_assets"] * 5},
    {"instruction": "Test", "file_url": "https://untrusted"},
])
def test_material_schema_rejects_unbound_or_arbitrary_inputs(params: dict[str, Any]) -> None:
    with pytest.raises(ValidationError):
        HostedMaterialRequest.model_validate(params)


@pytest.mark.parametrize("kind", ["code_interpreter", "image_generation"])
def test_native_responses_transport_carries_material_bytes(kind: str) -> None:
    seen = []
    data = b"wire fixture bytes"
    backend = AuditBackend(catalog(kind, access="write", approval="always"))
    fixture = HostedModel(kind).response

    async def load(*args: Any, **kwargs: Any) -> tuple[dict[str, Any], bytes]:
        return input_metadata(data, image=kind == "image_generation"), data

    async def save(*args: Any) -> dict[str, Any]:
        return {"asset": {"asset_id": "ast_output", "project_id": _context().project_id, "source_type": "hosted_tool", "metadata": {"agent_tool_call_id": args[0], "sdk_tool_call_id": args[1]}}, "asset_snapshot": {"asset_id": "ast_output", "asset_snapshot_id": "ass_output"}}

    backend.get_hosted_input_content = load  # type: ignore[attr-defined]
    backend.store_agent_tool_output = save  # type: ignore[attr-defined]

    def respond(request: httpx2.Request) -> httpx2.Response:
        assert request.url == "https://hosted-fixture.invalid/v1/responses"
        payload = json.loads(request.content)
        seen.append(payload)
        return httpx2.Response(200, json={
            "id": "resp_wire", "object": "response", "created_at": 0, "status": "completed",
            "model": "fixture-model", "parallel_tool_calls": True,
            "output": [item.model_dump(mode="json") for item in fixture.output],
            "usage": {"input_tokens": 2, "output_tokens": 3, "total_tokens": 5},
            "tool_choice": "required", "tools": payload["tools"],
        })

    async def run() -> None:
        async with AsyncOpenAI(api_key="local-fixture", base_url="https://hosted-fixture.invalid/v1", http_client=httpx2.AsyncClient(transport=httpx2.MockTransport(respond))) as client:
            model = OpenAIResponsesModel(model="fixture-model", openai_client=client)
            context = _context()
            tool = (await AgentToolProvider(backend, [], hosted_model=model, hosted_client=client).prepare(context, [], set())).tools[0]
            args = json.dumps(request_params())
            wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="sdk_wire", tool_arguments=args, run_config=RunConfig(tracing_disabled=True, trace_include_sensitive_data=False))
            assert await tool.needs_approval(wrapper, request_params(), "sdk_wire")
            assert not seen
            context.active_tool_calls["sdk_wire"]["status"] = "approved"
            await tool.on_invoke_tool(wrapper, args)

    asyncio.run(run())
    assert len(seen) == 1
    assert seen[0]["tools"][0]["type"] == kind
    parts = seen[0]["input"][0]["content"]
    native = next(part for part in parts if part["type"] == ("input_image" if kind == "image_generation" else "input_file"))
    encoded = native["image_url" if kind == "image_generation" else "file_data"]
    assert base64.b64decode(encoded.split(",", 1)[1]) == data
    assert len(backend.completed) == 1 and not backend.failed


@pytest.mark.parametrize("failure", ["success", "foreign_project", "stale_snapshot", "deleted", "oversize", "checksum", "changed_bytes", "expired_content", "conflict_content", "stream_overlimit"])
def test_hosted_backend_reads_bound_bytes_and_rejects_invalid_material(failure: str) -> None:
    original = b"exact fixture bytes"
    metadata = input_metadata(original)
    payload = original
    if failure == "foreign_project":
        metadata["project_id"] = "prj_foreign"
    elif failure == "stale_snapshot":
        metadata["current_snapshot_id"] = "ass_other"
    elif failure == "deleted":
        metadata["status"] = "deleted"
    elif failure == "oversize":
        metadata["size_bytes"] = 1000
    elif failure == "checksum":
        metadata["checksum"] = "missing"
    elif failure == "changed_bytes":
        payload = b"wrong fixture bytes"
    elif failure == "stream_overlimit":
        payload = b"x" * 101
    seen = []

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            seen.append((self.path, dict(self.headers)))
            if "/content?" in self.path:
                self.send_response(410 if failure == "expired_content" else 409 if failure == "conflict_content" else 200)
                self.send_header("Content-Type", "text/plain")
                self.end_headers()
                self.wfile.write(payload)
            else:
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps({"data": metadata}).encode())

        def log_message(self, *_args: Any) -> None:
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        backend = BackendClient(f"http://127.0.0.1:{server.server_port}", internal_token="input-fixture")

        async def run() -> None:
            with backend_activity("prj-tools", agent_turn_id="turn-tools"):
                if failure == "success":
                    result, data = await backend.get_hosted_input_content("prj-tools", "ast_input", "ass_input", max_bytes=100)
                    assert result == metadata and data == original
                else:
                    with pytest.raises(BackendError):
                        await backend.get_hosted_input_content("prj-tools", "ast_input", "ass_input", max_bytes=100)

        asyncio.run(run())
        assert len(seen) == (2 if failure in {"success", "changed_bytes", "expired_content", "conflict_content", "stream_overlimit"} else 1)
        for path, headers in seen:
            headers = {key.lower(): value for key, value in headers.items()}
            assert headers["authorization"] == "Bearer input-fixture"
            assert headers["x-agent-project-id"] == "prj-tools"
            assert headers["x-agent-turn-id"] == "turn-tools"
            if "/content?" in path:
                assert parse_qs(urlsplit(path).query) == {"asset_snapshot_id": ["ass_input"]}
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


@pytest.mark.parametrize("case", ["duplicate", "conflict", "unsupported", "bad_name", "bad_mime", "total_limit"])
def test_native_input_builder_dedup_limits_and_format_rejection(case: str, monkeypatch: pytest.MonkeyPatch) -> None:
    from content_agent_sidecar import hosted_inputs

    params = request_params()
    data = b"fixture"
    metadata = input_metadata(data, image=True)
    calls = []
    if case in {"duplicate", "conflict", "total_limit"}:
        params["input_assets"].append({"asset_id": "ast_second" if case == "total_limit" else "ast_input", "asset_snapshot_id": "ass_other" if case == "conflict" else "ass_input"})
    if case == "unsupported":
        metadata["kind"] = "text"
    if case == "bad_name":
        metadata["original_filename"] = "../image.png"
    if case == "bad_mime":
        metadata["detected_mime_type"] = "image/jpeg"
    if case == "total_limit":
        monkeypatch.setattr(hosted_inputs, "MAX_INPUT_TOTAL_BYTES", len(data))

    class Backend:
        async def get_hosted_input_content(self, *args: Any, **kwargs: Any) -> tuple[dict[str, Any], bytes]:
            calls.append((args, kwargs))
            return metadata, data

    async def run() -> None:
        builder = hosted_input_builder("image_generation", _context(), Backend())
        if case == "duplicate":
            result = await builder({"params": params})
            assert sum(part["type"] == "input_image" for part in result[0]["content"]) == 1
            assert len(calls) == 1
        else:
            with pytest.raises(ValueError):
                await builder({"params": params})

    asyncio.run(run())


@pytest.mark.parametrize("cancelled", [False, True])
def test_hosted_input_failure_or_cancellation_never_reaches_provider(cancelled: bool) -> None:
    backend = AuditBackend(catalog("code_interpreter", access="write", approval="always"))
    model, context = HostedModel("code_interpreter"), _context()

    async def load(*args: Any, **kwargs: Any) -> Any:
        if cancelled:
            raise asyncio.CancelledError()
        raise BackendError("stale input snapshot")

    backend.get_hosted_input_content = load  # type: ignore[attr-defined]

    async def run() -> None:
        tool = (await AgentToolProvider(backend, [], hosted_model=model).prepare(context, [], set())).tools[0]
        args = json.dumps(request_params())
        wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="sdk_failure", tool_arguments=args, run_config=RunConfig(tracing_disabled=True))
        assert await tool.needs_approval(wrapper, request_params(), "sdk_failure")
        context.active_tool_calls["sdk_failure"]["status"] = "approved"
        with pytest.raises(asyncio.CancelledError if cancelled else BackendError):
            await tool.on_invoke_tool(wrapper, args)

    asyncio.run(run())
    assert not model.seen_inputs and not backend.completed
    assert len(backend.cancelled if cancelled else backend.failed) == 1
