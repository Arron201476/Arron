from __future__ import annotations

import asyncio
import json
from dataclasses import replace
from types import SimpleNamespace
from urllib.parse import parse_qs, urlsplit

import pytest
from agents.tool_context import ToolContext
from agents.tool import set_function_tool_failure_error_function

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.workspace_files import (
    MAX_FILE_BYTES, apply_workspace_patch, file_content_url,
    list_workspace_files, read_workspace_file,
)


class FileBackend:
    def __init__(self):
        self.reads = []
        self.writes = []
        self.source = {
            "file": {"project_id": "prj-files", "path": "draft/SKILL.md", "version": 1},
            "content": "Hello\nWorld\n", "truncated": False,
        }
        self.receipt_override = {}

    async def list_workspace_files(self, project_id):
        assert project_id == "prj-files"
        return [self.source["file"]]

    async def read_workspace_file(self, *args):
        self.reads.append(args)
        return self.source

    async def apply_workspace_patch(self, call_id, sdk_id, patch, content):
        self.writes.append((call_id, sdk_id, patch, content))
        return {
            "project_id": "prj-files", "path": patch["path"],
            "version": patch["expected_version"] + 1,
            "deleted": patch["operation"] == "delete_file",
            **self.receipt_override,
        }


def invoke(tool, backend, arguments, *, audited=True):
    context = SimpleNamespace(
        project_id="prj-files", backend=backend,
        active_tool_calls={"sdk-file": {"agent_tool_call_id": "call-file"}} if audited else {},
    )
    raw = json.dumps(arguments)
    wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="sdk-file", tool_arguments=raw)
    unhandled = set_function_tool_failure_error_function(replace(tool), None)
    return json.loads(asyncio.run(unhandled.on_invoke_tool(wrapper, raw)))


def patch_arguments(operation="create_file", version=0, diff="+Hello\n+World"):
    return {"path": "draft/SKILL.md", "expected_version": version, "operation": operation, "diff": diff}


def test_sdk_patch_create_update_delete_and_download_receipt():
    backend = FileBackend()
    created = invoke(apply_workspace_patch, backend, patch_arguments())
    assert backend.writes[0][-1] == "Hello\nWorld"
    assert backend.writes[0][:2] == ("call-file", "sdk-file")
    assert not created["installed_as_skill"]
    assert created["saved_to"] == "project_workspace"
    assert parse_qs(urlsplit(created["content_url"]).query) == {"path": ["draft/SKILL.md"], "version": ["1"]}
    updated = invoke(apply_workspace_patch, backend, patch_arguments("update_file", 1, "@@\n Hello\n-World\n+Updated"))
    assert backend.reads == [("prj-files", "draft/SKILL.md", 1, 0, MAX_FILE_BYTES)]
    assert backend.writes[-1][-1] == "Hello\nUpdated\n"
    assert updated["file"]["version"] == 2
    deleted = invoke(apply_workspace_patch, backend, patch_arguments("delete_file", 2, ""))
    assert backend.writes[-1][-1] == ""
    assert deleted["file"]["deleted"] is True
    assert "content_url" not in deleted


@pytest.mark.parametrize("arguments", [
    patch_arguments("delete_file", 0, ""), patch_arguments("delete_file", 1, "not-empty"),
    patch_arguments("update_file", 0, "@@\n-a\n+b"), patch_arguments(version=-1),
    patch_arguments(diff="missing-plus-prefix"), patch_arguments(diff="+nul\x00"),
    patch_arguments(diff="+" + "x" * MAX_FILE_BYTES),
    patch_arguments("update_file", 1, "@@\n-no-such-line\n+replacement"),
])
def test_invalid_patch_never_writes(arguments):
    backend = FileBackend()
    with pytest.raises(Exception):
        invoke(apply_workspace_patch, backend, arguments)
    assert not backend.writes


def test_write_requires_audited_call():
    backend = FileBackend()
    with pytest.raises(ValueError, match="audited SDK"):
        invoke(apply_workspace_patch, backend, patch_arguments(), audited=False)
    assert not backend.writes


def test_binary_read_offers_exact_download_and_never_becomes_text_patch():
    backend = FileBackend()
    backend.source["file"]["binary"] = True
    backend.source["content"] = ""
    result = invoke(read_workspace_file, backend, {"path": "draft/SKILL.md", "version": 1})
    assert result["file"]["binary"] and "version=1" in result["content_url"]
    with pytest.raises(ValueError, match="Binary files"):
        invoke(apply_workspace_patch, backend, patch_arguments("update_file", 1, "@@\n+text"))
    assert not backend.writes


@pytest.mark.parametrize("field,value", [
    ("truncated", True), ("version", 2), ("path", "other.txt"),
    ("project_id", "prj-other"), ("content", None), ("content", "x" * (MAX_FILE_BYTES + 1)),
], ids=["truncated", "wrong-version", "wrong-path", "wrong-project", "missing-content", "oversized-content"])
def test_update_rejects_incomplete_or_mismatched_base(field, value):
    backend = FileBackend()
    target = backend.source["file"] if field in {"version", "path", "project_id"} else backend.source
    target[field] = value
    with pytest.raises(ValueError):
        invoke(apply_workspace_patch, backend, patch_arguments("update_file", 1, "@@\n-Hello\n+Updated"))
    assert not backend.writes


@pytest.mark.parametrize("receipt", [{"version": 9}, {"project_id": "other"}, {"path": "other.txt"}])
def test_save_does_not_claim_success_for_wrong_receipt(receipt):
    backend = FileBackend()
    backend.receipt_override = receipt
    with pytest.raises(ValueError, match="receipt"):
        invoke(apply_workspace_patch, backend, patch_arguments())


def test_list_and_paged_read_and_encoded_url():
    backend = FileBackend()
    assert invoke(list_workspace_files, backend, {})["files"] == [backend.source["file"]]
    invoke(read_workspace_file, backend, {"path": "draft/SKILL.md", "version": 1, "offset": 5, "limit": 2})
    assert backend.reads == [("prj-files", "draft/SKILL.md", 1, 5, 2)]
    for limit in [0, 48001]:
        with pytest.raises(ValueError):
            invoke(read_workspace_file, backend, {"path": "draft/SKILL.md", "limit": limit})
    url = file_content_url("prj/files", "a & #/notes.txt", 3)
    assert urlsplit(url).path == "/api/v1/projects/prj%2Ffiles/files/content"
    assert parse_qs(urlsplit(url).query) == {"path": ["a & #/notes.txt"], "version": ["3"]}


def test_read_only_provider_cannot_offer_file_writes_even_in_fallback_catalog():
    backend = FileBackend()
    provider = AgentToolProvider(backend, [list_workspace_files, read_workspace_file, apply_workspace_patch])
    context = SimpleNamespace(project_id="prj-files", backend=backend, active_tool_calls={})
    prepared = asyncio.run(provider.prepare(context, [], set(), read_only=True))
    assert {tool.name for tool in prepared.tools} == {"list_workspace_files", "read_workspace_file"}
    writable = asyncio.run(provider.prepare(context, [], set()))
    assert "apply_workspace_patch" in {tool.name for tool in writable.tools}


@pytest.mark.parametrize("invalid", [False, True])
def test_provider_audits_patch_success_and_returns_recoverable_failure(invalid):
    class AuditedBackend(FileBackend):
        def __init__(self):
            super().__init__()
            self.begun, self.started, self.completed, self.failed = [], [], [], []

        async def begin_agent_tool_call(self, **payload):
            self.begun.append(payload)
            return {"agent_tool_call_id": "call-file", "status": "running", "sdk_tool_call_id": "sdk-file"}

        async def start_agent_tool_call(self, *args, **kwargs):
            self.started.append((args, kwargs))
            return {"agent_tool_call_id": "call-file", "status": "running"}

        async def complete_agent_tool_call(self, *args, **kwargs):
            self.completed.append((args, kwargs))
            return {"status": "completed"}

        async def fail_agent_tool_call(self, *args, **kwargs):
            self.failed.append((args, kwargs))
            return {"status": "failed"}

    backend = AuditedBackend()
    context = SimpleNamespace(project_id="prj-files", conversation_id="conv-files", agent_turn_id="turn-files", backend=backend, active_tool_calls={})
    provider = AgentToolProvider(backend, [apply_workspace_patch])

    async def run():
        prepared = await provider.prepare(context, [], set())
        tool = prepared.tools[0]
        raw = json.dumps(patch_arguments(diff="bad patch" if invalid else "+Created"))
        wrapper = ToolContext(context, tool_name=tool.name, tool_call_id="sdk-file", tool_arguments=raw)
        return await tool.on_invoke_tool(wrapper, raw)

    result = asyncio.run(run())
    assert len(backend.begun) == len(backend.started) == 1
    assert backend.begun[0]["tool_id"] == "runtime:apply_workspace_patch"
    if invalid:
        assert len(backend.failed) == 1
        assert not backend.writes and not backend.completed
        assert "error" in result.lower()
    else:
        assert json.loads(result)["file"]["version"] == 1
        assert len(backend.writes) == len(backend.completed) == 1
        assert not backend.failed
