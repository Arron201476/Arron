from __future__ import annotations

import asyncio
import base64
import json
from types import SimpleNamespace

import pytest
from agents.tool_context import ToolContext

from content_agent_sidecar.runtime import AgentContext, _skill_resources, inspect_text_asset
from content_agent_sidecar.skill_resources import skill_resource_output


def test_resources_require_loaded_skill_and_preserve_exact_version() -> None:
    calls = []

    async def get_resources(*args, **kwargs):
        calls.append((args, kwargs))
        return {"data": {"content": "reference", "next_offset": 9}}

    context = AgentContext("project", "conversation", SimpleNamespace(get_skill_resources=get_resources))
    with pytest.raises(ValueError):
        asyncio.run(_skill_resources(context, "review"))
    context.loaded_capabilities["review"] = {"version": "1.0.0", "skill": {}}
    context.capability_versions["review"] = "2.0.0"
    result = json.loads(asyncio.run(_skill_resources(context, "review", path="references/guide.md", offset=5, limit=10)))
    assert result["data"]["content"] == "reference"
    assert calls == [(('project', 'review', '1.0.0'), {"path": "references/guide.md", "offset": 5, "limit": 10})]
    with pytest.raises(ValueError):
        asyncio.run(_skill_resources(context, "review", offset=-1))
    assert len(calls) == 1


def test_uploaded_text_can_be_read_past_the_first_48000_characters() -> None:
    async def metadata(asset_id):
        return {"data": {"project_id": "project", "kind": "text", "current_snapshot_id": "snapshot"}}

    async def parsed(asset_id, snapshot_id):
        return {"data": {"content": "甲" * 48000 + "结尾证据"}}

    backend = SimpleNamespace(get_asset=metadata, get_parsed_asset_text=parsed, _data=lambda payload: payload["data"])
    context = AgentContext("project", "conversation", backend)

    async def execute():
        tool_context = ToolContext(context, tool_name="inspect_text_asset", tool_call_id="read-tail", tool_arguments="{}")
        arguments = {"asset_id": "asset", "asset_snapshot_id": "snapshot", "offset": 48000, "limit": 10}
        return json.loads(await inspect_text_asset.on_invoke_tool(tool_context, json.dumps(arguments)))

    result = asyncio.run(execute())
    assert result["content"] == "结尾证据"
    assert result["next_offset"] == 48004
    assert result["truncated"] is False


@pytest.mark.parametrize(("kind", "media_type", "filename", "input_type"), [
    ("image", "image/png", "picture.png", "input_image"),
    ("file", "application/pdf", "guide.pdf", "input_file"),
    ("file", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "guide.docx", "input_file"),
])
def test_resource_flows_through_native_sdk_guardrails_and_audit(kind, media_type, filename, input_type) -> None:
    from agents import Agent, RunConfig, Runner, function_tool
    from agents.items import ModelResponse
    from agents.usage import Usage
    from openai.types.responses import ResponseFunctionToolCall
    from content_agent_sidecar.agent_tools import AgentToolProvider
    from content_agent_sidecar.guardrails import SDKGuardrailPolicy
    from test_agent_tools import AuditBackend, StaticModel, _context, _runtime_catalog
    from test_sdk_guardrails import message

    encoded = base64.b64encode(b"bounded-binary-fixture" * 60000).decode()
    payload = {"data": {"kind": kind, "media_type": media_type, "filename": filename, "data_base64": encoded}}

    @function_tool
    async def read_skill_resource() -> object:
        """Read a fixed binary resource."""
        return skill_resource_output(payload)

    class ResourceModel(StaticModel):
        calls = 0

        async def get_response(self, *args, **kwargs):
            self.calls += 1
            if self.calls == 1:
                return ModelResponse(output=[ResponseFunctionToolCall(id="fc_resource", type="function_call", call_id="sdk_resource", name="read_skill_resource", arguments="{}")], usage=Usage(), response_id="resp_resource")
            tool_output = next(item for item in kwargs["input"] if isinstance(item, dict) and item.get("type") == "function_call_output")
            binary = next(item for item in tool_output["output"] if item["type"] == input_type)
            assert encoded in binary.get("image_url", binary.get("file_data", ""))
            return message("Read the resource")

    catalog = _runtime_catalog()
    catalog["tools"][0].update(id="runtime:read_skill_resource", name="read_skill_resource", max_result_bytes=16 * 1024 * 1024)
    backend = AuditBackend(catalog)

    async def execute():
        context = _context()
        prepared = await AgentToolProvider(backend, [read_skill_resource]).prepare(context, [], set())
        agent = SDKGuardrailPolicy().protect(Agent(name="Resource", tools=prepared.tools, model=ResourceModel(message("unused"))))
        return await Runner.run(agent, "Read", context=context, max_turns=3, run_config=RunConfig(tracing_disabled=True))

    result = asyncio.run(execute())
    assert result.final_output == "Read the resource"
    assert len(backend.completed) == 1 and not backend.failed
    assert backend.completed[0]["result_size_bytes"] > 1_000_000
    assert encoded not in json.dumps(backend.completed)
    assert "binary_resource" in json.dumps(backend.completed)


@pytest.mark.parametrize("change", [
    {"data_base64": "%%%"},
    {"data_base64": "A" * (14 * 1024 * 1024)},
    {"media_type": "application/x-executable"},
    {"filename": "../guide.pdf"},
])
def test_resource_output_rejects_invalid_binary_payload(change) -> None:
    data = {"kind": "file", "media_type": "application/pdf", "filename": "guide.pdf", "data_base64": base64.b64encode(b"fixture").decode(), **change}
    with pytest.raises(ValueError):
        skill_resource_output({"data": data})


def test_maximum_binary_resource_survives_native_approval_checkpoint() -> None:
    from agents import Agent, RunConfig, Runner, RunState, function_tool
    from agents.items import ModelResponse
    from agents.usage import Usage
    from openai.types.responses import ResponseFunctionToolCall
    from content_agent_sidecar.runtime import _serialize_paused_run
    from test_agent_tools import StaticModel
    from test_sdk_guardrails import message

    encoded = base64.b64encode(b"x" * (10 << 20)).decode()
    writes = []

    @function_tool
    async def read_resource() -> object:
        """Read the binary Skill reference."""
        return skill_resource_output({"data": {"kind": "image", "media_type": "image/png", "filename": "reference.png", "data_base64": encoded}})

    @function_tool(needs_approval=True)
    async def save_result() -> str:
        """Save the result after reading the reference."""
        writes.append("saved")
        return "saved"

    class MediaModel(StaticModel):
        async def get_response(self, *args, **kwargs):
            inputs = kwargs.get("input", [])
            results = {item.get("call_id"): item for item in inputs if isinstance(item, dict) and item.get("type") == "function_call_output"}
            if "read-reference" not in results:
                return ModelResponse(output=[ResponseFunctionToolCall(type="function_call", call_id="read-reference", name="read_resource", arguments="{}")], usage=Usage(), response_id=None)
            assert results["read-reference"]["output"][1]["image_url"] == "data:image/png;base64," + encoded
            if "save-result" in results:
                return message("done")
            return ModelResponse(output=[ResponseFunctionToolCall(type="function_call", call_id="save-result", name="save_result", arguments="{}")], usage=Usage(), response_id=None)

    async def execute():
        context = AgentContext("project", "conversation", SimpleNamespace())
        agent = Agent(name="Media", model=MediaModel(message("unused")), tools=[read_resource, save_result])
        pending = await Runner.run(agent, "Read then save", context=context, run_config=RunConfig(tracing_disabled=True))
        checkpoint, _, _ = _serialize_paused_run(pending)
        assert len(json.dumps(checkpoint)) > 8 << 20
        assert not writes
        fresh = AgentContext("project", "conversation", SimpleNamespace())
        rebuilt = agent.clone(model=MediaModel(message("unused")))
        state = await RunState.from_json(rebuilt, json.loads(json.dumps(checkpoint)), context_override=fresh, strict_context=True)
        state.approve(state.get_interruptions()[0])
        result = await Runner.run(rebuilt, state, context=fresh, run_config=RunConfig(tracing_disabled=True))
        assert result.final_output == "done" and writes == ["saved"]

    asyncio.run(execute())
