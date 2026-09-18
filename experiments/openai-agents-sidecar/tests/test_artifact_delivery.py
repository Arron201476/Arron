import asyncio
import json
from dataclasses import replace
from types import SimpleNamespace

import pytest
from agents import Agent, Runner
from agents.items import ModelResponse
from agents.tool import set_function_tool_failure_error_function
from agents.tool_context import ToolContext
from agents.usage import Usage
from openai.types.responses import ResponseFunctionToolCall

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.runtime import get_artifact_downloads
from test_run_state_approval import SequenceModel, _text_response


class DeliveryBackend:
    def __init__(self):
        self.receipt = {"project_id": "p", "artifact_id": "art", "artifact_version_id": "av-1", "version": 1, "status": "confirmed", "downloads": [{"format": "txt", "download_url": "/api/v1/artifact-versions/av-1/download?format=txt"}], "warnings": []}
        self.requested, self.completed, self.failed = [], [], []

    async def get_artifact_delivery(self, version_id):
        self.requested.append(version_id)
        return self.receipt

    async def get_agent_tool_catalog(self):
        return {"schema_version": "1.0.0", "tools": [{"id": "runtime:get_artifact_downloads", "name": "get_artifact_downloads", "description": "Download saved versions", "kind": "runtime_function", "enabled": True, "access": "read", "approval": "never", "timeout_seconds": 5, "max_retries": 0, "max_result_bytes": 4096}], "mcp_servers": [], "hosted_tools": []}

    async def begin_agent_tool_call(self, **payload):
        assert payload["tool_id"] == "runtime:get_artifact_downloads"
        return {"agent_tool_call_id": "call", "status": "running", "sdk_tool_call_id": "sdk-delivery"}

    async def start_agent_tool_call(self, *_args, **_kwargs):
        return {"agent_tool_call_id": "call", "status": "running"}

    async def complete_agent_tool_call(self, *args, **kwargs):
        self.completed.append((args, kwargs))
        return {"status": "completed"}

    async def fail_agent_tool_call(self, *args, **kwargs):
        self.failed.append((args, kwargs))
        return {"status": "failed"}


def context_for(backend):
    return SimpleNamespace(project_id="p", conversation_id="c", agent_turn_id="t", backend=backend, active_tool_calls={})


@pytest.mark.parametrize("field,value", [("project_id", "foreign"), ("artifact_version_id", "av-2")])
def test_delivery_rejects_mismatched_receipt(field, value):
    backend = DeliveryBackend()
    backend.receipt[field] = value
    tool = set_function_tool_failure_error_function(replace(get_artifact_downloads), None)
    raw = json.dumps({"artifact_version_id": "av-1"})
    wrapper = ToolContext(context_for(backend), tool_name=tool.name, tool_call_id="sdk-delivery", tool_arguments=raw)
    with pytest.raises(ValueError, match="receipt"):
        asyncio.run(tool.on_invoke_tool(wrapper, raw))


def test_native_sdk_read_only_delivery_tool_returns_receipt_and_audits_completion():
    backend = DeliveryBackend()
    context = context_for(backend)

    async def run():
        provider = AgentToolProvider(backend, [get_artifact_downloads])
        prepared = await provider.prepare(context, [], set(), read_only=True)
        assert [tool.name for tool in prepared.tools] == ["get_artifact_downloads"]
        response = ModelResponse(output=[ResponseFunctionToolCall(id=None, call_id="sdk-delivery", arguments=json.dumps({"artifact_version_id": "av-1"}), name="get_artifact_downloads", type="function_call", status="completed")], usage=Usage(requests=1), response_id="delivery")
        agent = Agent(name="Delivery fixture", tools=prepared.tools, model=SequenceModel([response, _text_response()]))
        result = await Runner.run(agent, "Download saved version one", context=context)
        assert not result.interruptions
        outputs = [item.output for item in result.new_items if item.type == "tool_call_output_item"]
        assert json.loads(outputs[0]) == backend.receipt

    asyncio.run(run())
    assert backend.requested == ["av-1"]
    assert len(backend.completed) == 1 and not backend.failed
