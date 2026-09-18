from __future__ import annotations

import asyncio
import base64
import json
from contextlib import asynccontextmanager
from types import SimpleNamespace
from typing import Any

import pytest
from agents import Agent, RunConfig, Runner
from agents.items import ModelResponse
from agents.tool_context import ToolContext
from agents.usage import Usage
from openai.types.responses import ResponseCodeInterpreterToolCall, ResponseFunctionToolCall, ResponseFunctionWebSearch, ResponseOutputMessage
from openai.types.responses.response_output_item import ImageGenerationCall

from content_agent_sidecar.agent_tools import AgentToolConfigurationError, AgentToolProvider
from test_agent_tools import AuditBackend, StaticModel, _context


def catalog(kind: str, *, approval: str = "never", access: str = "read") -> dict[str, Any]:
    return {
        "schema_version": "1.0.0",
        "tools": [{"id": "hosted:fixture", "kind": "hosted", "name": kind, "description": "Hosted fixture", "enabled": True, "access": access, "approval": approval, "timeout_seconds": 5, "max_retries": 0, "max_result_bytes": 8192}],
        "hosted_tools": [{"id": "fixture", "type": kind, "enabled": True, "vector_store_ids": ["vs_fixture"] if kind == "file_search" else []}],
    }


class HostedModel(StaticModel):
    def __init__(self, kind: str = "web_search", *, status: str = "completed") -> None:
        self.seen_tools: list[Any] = []
        self.seen_inputs: list[Any] = []
        if kind == "image_generation":
            item = ImageGenerationCall(id="ig_fixture", type="image_generation_call", status=status, result=base64.b64encode(b"image-output").decode())
        elif kind == "code_interpreter":
            item = ResponseCodeInterpreterToolCall(id="ci_fixture", type="code_interpreter_call", status=status, container_id="cntr_fixture", outputs=[])
        else:
            item = ResponseFunctionWebSearch(id="ws_fixture", type="web_search_call", status=status, action={"type": "search", "query": "fixture"})
        super().__init__(ModelResponse(output=[item, ResponseOutputMessage(id="msg_hosted", type="message", role="assistant", status="completed", content=[{"type": "output_text", "text": "Hosted result", "annotations": []}])], usage=Usage(requests=1, input_tokens=5, output_tokens=7, total_tokens=12), response_id="resp_hosted"))

    async def get_response(self, *args: Any, **kwargs: Any) -> ModelResponse:
        self.seen_tools.extend(kwargs.get("tools") or [])
        self.seen_inputs.append(kwargs.get("input"))
        return self.response


def test_hosted_search_uses_native_sdk_tool_and_persists_audit() -> None:
    backend, model, context = AuditBackend(catalog("web_search")), HostedModel(), _context()
    provider = AgentToolProvider(backend, [], hosted_model=model)

    async def execute() -> str:
        prepared = await provider.prepare(context, [], set())
        tool = prepared.tools[0]
        arguments = '{"instruction":"Find the fixture"}'
        wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="sdk_hosted", tool_arguments=arguments, run_config=RunConfig(tracing_disabled=True, trace_include_sensitive_data=False))
        return await tool.on_invoke_tool(wrapper, arguments)

    output = json.loads(asyncio.run(execute()))
    assert output["text"] == "Hosted result"
    assert output["hosted_calls"][0]["id"] == "ws_fixture"
    assert model.seen_tools[0].name == "web_search"
    assert backend.begun[0]["tool_id"] == "hosted:fixture"
    assert len(backend.completed) == 1 and not backend.failed


def test_hosted_approval_pauses_sdk_before_any_provider_action_and_resumes() -> None:
    backend, model, context = AuditBackend(catalog("web_search", approval="always")), HostedModel(), _context()
    provider = AgentToolProvider(backend, [], hosted_model=model)

    async def execute() -> None:
        prepared = await provider.prepare(context, [], set())
        tool = prepared.tools[0]
        parent = Agent(name="Parent", tools=[tool], model=StaticModel(ModelResponse(output=[ResponseFunctionToolCall(id="fc_hosted", type="function_call", call_id="sdk_hosted", name=tool.name, arguments='{"instruction":"Search"}')], usage=Usage(), response_id="resp_parent")), tool_use_behavior="stop_on_first_tool")
        run = await Runner.run(parent, "Search", context=context, run_config=RunConfig(tracing_disabled=True, trace_include_sensitive_data=False))
        assert len(run.interruptions) == 1
        assert model.seen_tools == [] and backend.started == []
        context.active_tool_calls["sdk_hosted"]["status"] = "approved"
        state = run.to_state()
        state.approve(run.interruptions[0])
        resumed = await Runner.run(parent, state, run_config=RunConfig(tracing_disabled=True, trace_include_sensitive_data=False))
        assert not resumed.interruptions
        assert "Hosted result" in resumed.final_output
        assert len(backend.completed) == 1

    asyncio.run(execute())


def test_failed_hosted_result_is_not_audited_as_success() -> None:
    backend, model, context = AuditBackend(catalog("web_search")), HostedModel(status="failed"), _context()

    async def execute() -> None:
        prepared = await AgentToolProvider(backend, [], hosted_model=model).prepare(context, [], set())
        tool = prepared.tools[0]
        wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="sdk_failed", tool_arguments='{"instruction":"Search"}', run_config=RunConfig(tracing_disabled=True))
        with pytest.raises(RuntimeError, match="did not complete"):
            await tool.on_invoke_tool(wrapper, wrapper.tool_arguments)

    asyncio.run(execute())
    assert len(backend.failed) == 1 and backend.completed == []


def test_image_output_is_persisted_not_returned_as_base64() -> None:
    backend, model, context = AuditBackend(catalog("image_generation", access="write", approval="always")), HostedModel("image_generation"), _context()
    saved = []

    async def save(call_id: str, sdk_id: str, filename: str, content: bytes) -> dict[str, Any]:
        saved.append((call_id, sdk_id, filename, content))
        return {"asset": {"asset_id": "ast_generated", "project_id": context.project_id, "source_type": "hosted_tool", "metadata": {"agent_tool_call_id": call_id, "sdk_tool_call_id": sdk_id}, "kind": "image", "original_filename": filename, "size_bytes": len(content)}, "asset_snapshot": {"asset_id": "ast_generated", "asset_snapshot_id": "ass_generated"}}

    backend.store_agent_tool_output = save  # type: ignore[attr-defined]

    async def execute() -> str:
        prepared = await AgentToolProvider(backend, [], hosted_model=model).prepare(context, [], set())
        tool = prepared.tools[0]
        arguments = '{"instruction":"Generate an image"}'
        wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="sdk_image", tool_arguments=arguments, run_config=RunConfig(tracing_disabled=True))
        assert await tool.needs_approval(wrapper, json.loads(arguments), "sdk_image")
        context.active_tool_calls["sdk_image"]["status"] = "approved"
        return await tool.on_invoke_tool(wrapper, arguments)

    output = json.loads(asyncio.run(execute()))
    assert saved == [("tcall-1", "sdk_image", "generated-image.png", b"image-output")]
    assert output["outputs"][0]["content_url"] == "/api/v1/assets/ast_generated/content"
    assert output["outputs"][0]["download_url"] == "/api/v1/assets/ast_generated/download"
    assert output["outputs"][0]["asset_snapshot_id"] == "ass_generated"
    assert "aW1hZ2Utb3V0cHV0" not in json.dumps(backend.completed)


@pytest.mark.parametrize("kind", ["image_generation", "code_interpreter"])
@pytest.mark.parametrize("mismatch", [None, "project", "source", "call", "sdk_call"])
def test_hosted_delivery_checks_execution_binding(kind, mismatch):
    backend = AuditBackend(catalog(kind, access="write", approval="always"))
    model, context = HostedModel(kind), _context()
    if kind == "code_interpreter":
        model.response.output[-1] = ResponseOutputMessage(
            id="msg_file", type="message", role="assistant", status="completed",
            content=[{"type": "output_text", "text": "File", "annotations": [{
                "type": "container_file_citation", "container_id": "cntr_fixture",
                "file_id": "cfile_fixture", "filename": "report.txt", "start_index": 0, "end_index": 4}]}])

    class Content:
        async def iter_bytes(self, **kwargs):
            yield b"report body"

    @asynccontextmanager
    async def retrieve(file_id, *, container_id):
        assert (container_id, file_id) == ("cntr_fixture", "cfile_fixture")
        yield Content()

    client = SimpleNamespace(containers=SimpleNamespace(files=SimpleNamespace(
        content=SimpleNamespace(with_streaming_response=SimpleNamespace(retrieve=retrieve)))))

    async def save(call_id, sdk_id, filename, content):
        asset = {"asset_id": "ast_result", "project_id": context.project_id,
                 "source_type": "hosted_tool", "original_filename": filename, "size_bytes": len(content),
                 "metadata": {"agent_tool_call_id": call_id, "sdk_tool_call_id": sdk_id}}
        if mismatch == "project": asset["project_id"] = "another-project"
        if mismatch == "source": asset["source_type"] = "mcp_tool"
        if mismatch == "call": asset["metadata"]["agent_tool_call_id"] = "another-call"
        if mismatch == "sdk_call": asset["metadata"]["sdk_tool_call_id"] = "another-sdk-call"
        return {"asset": asset, "asset_snapshot": {"asset_id": "ast_result", "asset_snapshot_id": "ass_result"}}

    backend.store_agent_tool_output = save

    async def execute():
        prepared = await AgentToolProvider(backend, [], hosted_model=model, hosted_client=client).prepare(context, [], set())
        tool = prepared.tools[0]
        arguments = '{"instruction":"Create the file"}'
        wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="sdk_delivery", tool_arguments=arguments,
                              run_config=RunConfig(tracing_disabled=True))
        assert await tool.needs_approval(wrapper, json.loads(arguments), "sdk_delivery")
        context.active_tool_calls["sdk_delivery"]["status"] = "approved"
        if mismatch is not None:
            with pytest.raises(ValueError, match="receipt does not match"):
                await tool.on_invoke_tool(wrapper, arguments)
        else:
            result = json.loads(await tool.on_invoke_tool(wrapper, arguments))
            assert result["outputs"][0]["asset_snapshot_id"] == "ass_result"

    asyncio.run(execute())
    if mismatch is not None:
        assert backend.failed and not backend.completed
    else:
        assert backend.completed and not backend.failed


@pytest.mark.parametrize("snapshot", [{}, {"asset_snapshot_id": "ass_wrong", "asset_id": "ast_other"}])
def test_hosted_output_without_matching_snapshot_is_failed(snapshot: dict[str, Any]) -> None:
    backend = AuditBackend(catalog("image_generation", access="write", approval="always"))
    model, context = HostedModel("image_generation"), _context()

    async def save(*args: Any) -> dict[str, Any]:
        return {"asset": {"asset_id": "ast_generated"}, "asset_snapshot": snapshot}

    backend.store_agent_tool_output = save  # type: ignore[attr-defined]

    async def execute() -> None:
        prepared = await AgentToolProvider(backend, [], hosted_model=model).prepare(context, [], set())
        tool = prepared.tools[0]
        arguments = '{"instruction":"Generate an image"}'
        wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="sdk_bad_receipt", tool_arguments=arguments, run_config=RunConfig(tracing_disabled=True))
        assert await tool.needs_approval(wrapper, json.loads(arguments), "sdk_bad_receipt")
        context.active_tool_calls["sdk_bad_receipt"]["status"] = "approved"
        with pytest.raises(ValueError, match="not persisted as a project asset"):
            await tool.on_invoke_tool(wrapper, arguments)

    asyncio.run(execute())
    assert len(backend.failed) == 1 and not backend.completed


@pytest.mark.parametrize("kind", ["web_search", "file_search", "code_interpreter", "image_generation"])
def test_all_hosted_adapters_can_be_prepared(kind: str) -> None:
    protected = kind in {"code_interpreter", "image_generation"}
    backend = AuditBackend(catalog(kind, access="sensitive" if protected else "read", approval="always" if protected else "never"))
    prepared = asyncio.run(AgentToolProvider(backend, [], hosted_model=HostedModel()).prepare(_context(), [], set()))
    assert len(prepared.tools) == 1


def test_read_only_worker_cannot_acquire_hosted_writes() -> None:
    backend = AuditBackend(catalog("image_generation", access="sensitive", approval="always"))
    prepared = asyncio.run(AgentToolProvider(backend, [], hosted_model=HostedModel()).prepare(_context(), [], set(), read_only=True))
    assert not prepared.tools
    backend = AuditBackend(catalog("file_search"))
    backend.catalog["hosted_tools"][0]["vector_store_ids"] = []
    with pytest.raises(AgentToolConfigurationError, match="vector_store_ids"):
        asyncio.run(AgentToolProvider(backend, [], hosted_model=HostedModel()).prepare(_context(), [], set()))
