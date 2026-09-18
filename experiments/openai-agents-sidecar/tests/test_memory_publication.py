import asyncio
from copy import deepcopy
from dataclasses import replace
import hashlib
import io
import json
import tarfile
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest
from agents import Agent, RunState
from agents.tool import set_function_tool_failure_error_function
from agents.tool_context import ToolContext

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.backend import BackendError, backend_activity
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.managed_instructions import bind_managed_instructions
from content_agent_sidecar.memory_publication import (
    MemoryPublicationArguments, MemorySelection, memory_snapshot_contents,
    prepare_agent_memory_publication, publish_agent_memory,
)
from content_agent_sidecar.native_files import _go_json_hash
from content_agent_sidecar.native_runner import bind_native_agent
from content_agent_sidecar.native_snapshot import SnapshotReference
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport
from content_agent_sidecar.runtime import _serialize_agent_context
from content_agent_sidecar.stateful_execution import ExecutionTaskPaused
from test_managed_memory import BODY, MemoryWorkspace, snapshot
from test_native_manifest import SKILL
from test_native_runner import setup, invoke
from test_native_workspace_transport import KEY, SESSION, lease, response, wire
from test_stateful_execution import StreamingSequence, final_item, tool_item


PATH = ".agent-memory/memory_summary.md"


async def prepared_for(backend, context):
    await bind_managed_instructions(context)
    prepared = await AgentToolProvider(backend, [prepare_agent_memory_publication, publish_agent_memory],
                                       guardrail_policy=SDKGuardrailPolicy()).prepare(context, [], set())
    prepared.capabilities = {SKILL.capability_id: {"capability_id": SKILL.capability_id, "version": SKILL.version,
        "status": "available", "execution_mode": "inline", "skill": {"content_hash": SKILL.content_hash}}}
    prepared.selected_ids = {SKILL.capability_id}
    return prepared


class PublishingModel(StreamingSequence):
    def __init__(self):
        super().__init__([])

    async def stream_response(self, *args, **kwargs):
        if not self.inputs:
            self.responses = [[tool_item("prepare_agent_memory_publication", {"files": [PATH]}, "prepare-memory")]]
        elif len(self.inputs) == 1:
            output = next(item for item in reversed(args[1]) if isinstance(item, dict) and item.get("type") == "function_call_output")
            prepared = json.loads(output["output"])
            assert not prepared["saved"] and BODY not in output["output"]
            self.responses = [[tool_item("publish_agent_memory", prepared["publish_arguments"], "publish-memory")]]
        else:
            self.responses = [[final_item({"title": "finished"})]]
        async for event in super().stream_response(*args, **kwargs):
            yield event


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
@pytest.mark.parametrize("approve", [False, True])
def test_memory_save_uses_sdk_approval_and_original_snapshot_across_rebuild(tmp_path, monkeypatch, mode, approve):
    async def run():
        monkeypatch.setattr("test_native_runner.RunnerWorkspace", MemoryWorkspace)
        disk, backend, context, _, config = setup(tmp_path, monkeypatch, mode, [])
        published = []
        async def resolve(project, activity):
            return {**snapshot(project, activity), "current_version": 2 if published else 1}
        backend.resolve_agent_memory_snapshot = resolve
        for name, access, approval in [("prepare_agent_memory_publication", "read", "never"), ("publish_agent_memory", "write", "always")]:
            descriptor = deepcopy(backend.catalog["tools"][0])
            descriptor.update(id="runtime:"+name, name=name, access=access, approval=approval, max_result_bytes=64*1024)
            backend.catalog["tools"].append(descriptor)
        async def publish(call_id, sdk_id, arguments):
            assert backend.calls[sdk_id]["status"] == "running" and not published
            args = MemoryPublicationArguments.model_validate(arguments)
            contents = memory_snapshot_contents(await disk.read_snapshot(disk.lease.session_id, args.snapshot), args.snapshot, args.files)
            receipt = {"agent_tool_call_id":call_id, "session_id":disk.lease.session_id, "project_id":"project", "user_id":"test-user",
                       "snapshot":args.snapshot.model_dump(), "version":args.expected_version+1, "content_hash":_go_json_hash(contents)}
            published.append(receipt)
            return receipt
        disk.publish_memory = publish
        model = PublishingModel()
        prepared = await prepared_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Memory publisher", model=model, tools=prepared.tools))
            try:
                result = await invoke(mode, agent, "Save the requested private memory", context, config)
                saved = result.to_state().to_json(context_serializer=_serialize_agent_context)
            except ExecutionTaskPaused as paused:
                saved = paused.run_state
            assert not published
        context = replace(context, native_workspace=None, active_tool_calls={})
        disk.lease = None
        prepared = await prepared_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Memory publisher", model=model, tools=prepared.tools))
            state = await RunState.from_json(agent, saved, context_override=context)
            item = state.get_interruptions()[0]
            state.approve(item) if approve else state.reject(item)
            backend.calls["publish-memory"]["status"] = "approved" if approve else "rejected"
            result = await invoke(mode, agent, state, context, config)
        assert result.final_output and len(published) == int(approve)
        if approve:
            assert published[0]["version"] == 2
            assert published[0]["content_hash"] == _go_json_hash({"memory_summary.md": BODY})
        assert disk.closes == 1
    asyncio.run(run())


def archive_fixture():
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w") as archive:
        entry = tarfile.TarInfo(PATH)
        entry.size = len(BODY.encode())
        archive.addfile(entry, io.BytesIO(BODY.encode()))
    body = output.getvalue()
    return body, SnapshotReference(version=1, sha256=hashlib.sha256(body).hexdigest())


@pytest.mark.parametrize("field,value", [("session_id","other"), ("agent_tool_call_id","other"), ("project_id","other"),
    ("user_id","other"), ("version",True), ("version",3), ("content_hash","a"*64), ("snapshot",{"version":2,"sha256":"a"*64})])
def test_memory_mismatched_receipt_cannot_report_success(field, value):
    async def run():
        body, reference = archive_fixture()
        receipt = {"session_id":SESSION, "agent_tool_call_id":"call", "project_id":"project", "user_id":"test-user",
                   "version":2, "content_hash":_go_json_hash({"memory_summary.md":BODY}), "snapshot":reference.model_dump()}
        receipt[field] = value
        transport = SimpleNamespace(lease=SimpleNamespace(session_id=SESSION), read_snapshot=AsyncMock(return_value=body), publish_memory=AsyncMock(return_value=receipt))
        owner = SimpleNamespace(client=object(), guard=SimpleNamespace(require_confirmed=lambda:None), transport=transport)
        backend = SimpleNamespace(resolve_agent_memory_snapshot=AsyncMock(return_value=snapshot()))
        context = SimpleNamespace(native_workspace=owner, active_tool_calls={"sdk":{"agent_tool_call_id":"call"}}, backend=backend,
            project_id="project", agent_turn_id="turn", agent_task_attempt_id="", execution_attempt_id="", managed_instructions=None)
        args = {"snapshot":reference.model_dump(), "expected_version":1, "files":[PATH]}
        tool = deepcopy(publish_agent_memory)
        set_function_tool_failure_error_function(tool, None)
        ctx = ToolContext(context=context, tool_name=tool.name, tool_call_id="sdk", tool_arguments=json.dumps(args))
        with pytest.raises(BackendError, match="receipt"):
            await tool.on_invoke_tool(ctx, json.dumps(args))
    asyncio.run(run())


@pytest.mark.parametrize("paths", [["shared.md"], [".agent-memory/MEMORY_SUMMARY.md"], [PATH, PATH], [PATH,".agent-memory/A/x",".agent-memory/a/y"]])
def test_memory_publication_rejects_unsafe_selection(paths):
    with pytest.raises(ValueError):
        MemorySelection(files=paths)


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_memory_publication_transport_keeps_execution_credentials(mode):
    args = {"snapshot":{"version":1,"sha256":"a"*64},"expected_version":0,"files":[PATH]}
    activity = {"agent_turn_id":"turn"} if mode=="main" else {"task_attempt_id" if mode=="background" else "execution_attempt_id":"attempt", "attempt_token":"original-token"}
    with wire(response(lease()), response({"session_id":SESSION})) as (backend, requests):
        async def run():
            with backend_activity("project", **activity):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1 if mode=="main" else 0)
                await transport.reserve()
                assert await transport.publish_memory("call","sdk",args)=={"session_id":SESSION}
        asyncio.run(run())
        assert requests[-1][1].endswith("/memory-publications")
        assert json.loads(requests[-1][3])["arguments"]==args
        assert requests[-1][2]["X-Agent-Project-ID"]=="project"
