import asyncio
import base64
import json
from pathlib import Path
from types import SimpleNamespace

import pytest
from agents import Agent, RunContextWrapper
from agents.tool_context import ToolContext
from mcp.types import AudioContent, BlobResourceContents, CallToolResult, EmbeddedResource, ImageContent, ReadResourceResult, ResourceLink, TextContent, TextResourceContents

from content_agent_sidecar.agent_tools import AgentToolProvider, _json_safe
from content_agent_sidecar.mcp_outputs import persist_mcp_outputs
from test_agent_tools import AuditBackend, _context, _mcp_catalog, _selected_skill


PNG = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9Zl1sAAAAASUVORK5CYII=")


class OutputBackend(AuditBackend):
    def __init__(self):
        super().__init__(_mcp_catalog(Path(__file__).parent / "fixtures" / "mcp_output_server.py"))
        self.saved = []

    async def store_agent_tool_output(self, call_id, sdk_id, filename, data):
        self.saved.append((call_id, sdk_id, filename, data))
        asset_id = f"asset-{len(self.saved)}"
        return {"asset": {"asset_id": asset_id, "project_id": "prj-tools", "kind": "text", "original_filename": filename,
                          "size_bytes": len(data), "source_type": "mcp_tool", "metadata": {"agent_tool_call_id": call_id, "sdk_tool_call_id": sdk_id}},
                "asset_snapshot": {"asset_id": asset_id, "asset_snapshot_id": f"snapshot-{len(self.saved)}"}}


def audit_for(backend, limit=65536):
    return SimpleNamespace(provider=SimpleNamespace(_backend=backend), agent_tool_call_id="call", sdk_tool_call_id="sdk",
                           project_id="prj-tools", max_result_bytes=limit)


def test_all_inline_media_are_persisted_and_native_content_is_preserved():
    backend = OutputBackend()
    contents = [TextContent(text="Generated files"), ImageContent(data=base64.b64encode(PNG).decode(), mime_type="image/png"),
                AudioContent(data=base64.b64encode(b"audio").decode(), mime_type="audio/wav"),
                EmbeddedResource(resource=TextResourceContents(uri="memo:///notes.md", mime_type="text/markdown", text="# Notes")),
                EmbeddedResource(resource=BlobResourceContents(uri="memo:///result.bin", blob=base64.b64encode(b"binary").decode()))]
    result = CallToolResult(content=contents, structured_content={"count": 4})
    returned, summary = asyncio.run(persist_mcp_outputs(None, result, audit_for(backend)))
    assert result.content == contents and returned.content[:-1] == contents
    assert returned.structured_content == {"count": 4}
    assert [item[2:] for item in backend.saved] == [("mcp-01-output.png", PNG), ("mcp-02-output.wav", b"audio"),
                                                 ("mcp-03-notes.md", b"# Notes"), ("mcp-04-result.bin", b"binary")]
    assert len(summary["outputs"]) == 4 and "blob" not in json.dumps(summary)
    assert base64.b64encode(PNG).decode() not in json.dumps(summary)
    receipts = json.loads(returned.content[-1].text)["platform_tool_outputs"]
    assert receipts == summary["outputs"] and receipts[0]["asset_snapshot_id"] == "snapshot-1"
    assert receipts[0]["download_url"] == "/api/v1/assets/asset-1/download"


def test_linked_resource_uses_only_existing_mcp_server_and_deduplicates_uri():
    backend, reads = OutputBackend(), []
    uri = "file:///not-a-local-file/report.csv"

    async def read_resource(requested):
        reads.append(requested)
        return ReadResourceResult(contents=[TextResourceContents(uri=uri, mime_type="text/csv", text="value\n42\n")])

    result = CallToolResult(content=[ResourceLink(uri=uri, name="../CON:report.csv", size=9)] * 2)
    returned, summary = asyncio.run(persist_mcp_outputs(SimpleNamespace(read_resource=read_resource), result, audit_for(backend)))
    assert reads == [uri] and len(backend.saved) == 1
    assert backend.saved[0][2:] == ("mcp-01-_CON_report.csv", b"value\n42\n")
    assert len(summary["outputs"]) == 1 and len(returned.content) == 3


@pytest.mark.parametrize("case", ["invalid-base64", "empty", "oversize", "combined", "count", "link-size", "link-empty", "link-identity", "link-partial"])
def test_invalid_outputs_fail_before_any_asset_is_written(case):
    backend = OutputBackend()
    valid = ImageContent(data=base64.b64encode(b"valid").decode(), mime_type="image/png")
    contents = [valid]
    limit = 1024
    if case == "invalid-base64": contents.append(ImageContent(data="%%%%", mime_type="image/png"))
    if case == "empty": contents.append(EmbeddedResource(resource=TextResourceContents(uri="memo:///empty.txt", text="")))
    if case == "oversize": contents.append(ImageContent(data=base64.b64encode(b"x" * 1025).decode(), mime_type="image/png"))
    if case == "combined": contents *= 2; limit = 9
    if case == "count": contents *= 17
    if case.startswith("link-"): contents.append(ResourceLink(uri="memo:///report.csv", name="report.csv", size=1025 if case == "link-size" else 5))

    async def read_resource(uri):
        assert case != "link-size"
        return ReadResourceResult(contents=[] if case == "link-empty" else [TextResourceContents(uri="memo:///wrong" if case == "link-identity" else uri, text="value")],
                                  result_type="partial" if case == "link-partial" else "complete")

    with pytest.raises(ValueError):
        asyncio.run(persist_mcp_outputs(SimpleNamespace(read_resource=read_resource), CallToolResult(content=contents), audit_for(backend, limit)))
    assert not backend.saved


@pytest.mark.parametrize("case", ["project", "source", "call", "sdk", "snapshot", "offline"])
def test_persistence_mismatch_is_not_published_as_success(case):
    backend = OutputBackend()
    save = backend.store_agent_tool_output

    async def invalid(*args):
        if case == "offline": raise RuntimeError("store unavailable")
        result = await save(*args)
        if case == "project": result["asset"]["project_id"] = "foreign"
        if case == "source": result["asset"]["source_type"] = "hosted_tool"
        if case == "call": result["asset"]["metadata"]["agent_tool_call_id"] = "wrong"
        if case == "sdk": result["asset"]["metadata"]["sdk_tool_call_id"] = "wrong"
        if case == "snapshot": result["asset_snapshot"]["asset_id"] = "wrong"
        return result

    backend.store_agent_tool_output = invalid
    result = CallToolResult(content=[EmbeddedResource(resource=TextResourceContents(uri="memo:///fact.txt", text="42"))])
    with pytest.raises((ValueError, RuntimeError)):
        asyncio.run(persist_mcp_outputs(None, result, audit_for(backend)))
    assert len(result.content) == 1


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
@pytest.mark.parametrize("kind", ["inline", "linked", "image", "error"])
def test_native_stdio_output_is_persisted_under_exact_execution_identity(tmp_path, monkeypatch, mode, kind):
    monkeypatch.setenv("TEST_STORY_MCP_STATE_FILE", str(tmp_path / "unused.jsonl"))
    backend, context = OutputBackend(), _context()
    if mode != "main":
        context.agent_turn_id = ""
        setattr(context, "agent_task_attempt_id" if mode == "background" else "execution_attempt_id", "attempt")
        context.attempt_token = "attempt-token"

    async def execute():
        prepared = await AgentToolProvider(backend, []).prepare(context, _selected_skill(), {"story_fact_check"})
        async with prepared:
            agent = Agent(name="MCP output fixture", model="unused", mcp_servers=prepared.mcp_servers)
            read = next(tool for tool in await agent.get_all_tools(RunContextWrapper(context)) if tool.name.endswith("lookup_story_fact"))
            wrapper = ToolContext(context, tool_name=read.name, tool_call_id="sdk-file", tool_arguments=json.dumps({"key": kind}))
            return await read.on_invoke_tool(wrapper, wrapper.tool_arguments)

    result = asyncio.run(execute())
    if kind == "error":
        assert not backend.saved and not backend.completed and len(backend.failed) == 1
        assert "platform_tool_outputs" not in json.dumps(_json_safe(result))
    else:
        assert len(backend.saved) == len(backend.completed) == 1 and not backend.failed
        assert backend.saved[0][1:] == (("sdk-file", "mcp-01-output.png", PNG) if kind == "image" else ("sdk-file", "mcp-01-fact.csv", b"value\n42\n"))
        assert "platform_tool_outputs" in json.dumps(_json_safe(result)) and "snapshot-1" in json.dumps(_json_safe(result))
    expected = {"main": "agent_turn_id", "background": "agent_task_attempt_id", "stateful": "execution_attempt_id"}[mode]
    assert backend.begun[0][expected] == ("turn-tools" if mode == "main" else "attempt")


def test_incomplete_tool_output_is_not_a_delivery_receipt():
    backend = OutputBackend()
    result = CallToolResult(content=[TextContent(text="Still working")], result_type="partial")
    with pytest.raises(ValueError, match="not complete"):
        asyncio.run(persist_mcp_outputs(None, result, audit_for(backend)))
    assert not backend.saved
