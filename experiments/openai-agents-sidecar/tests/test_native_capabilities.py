import asyncio
from copy import deepcopy
import hashlib
import json
import struct
import zlib

import pytest
from agents import Agent, RunConfig, Runner, RunState, ToolSearchTool
from agents.exceptions import ToolInputGuardrailTripwireTriggered, UserError
from agents.items import ModelResponse
from agents.sandbox import SandboxAgent
from agents.sandbox.manifest import Manifest
from agents.usage import Usage
from openai.types.responses import ResponseFunctionToolCall

from content_agent_sidecar.agent_tools import AgentToolConfigurationError, AgentToolProvider
from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.native_capabilities import prepare_native_workspace
from content_agent_sidecar.native_file_session import RuntimeSandboxFileSession
from content_agent_sidecar.native_execution import current_native_shell_call
from content_agent_sidecar.native_session import RuntimeSandboxClient
from test_agent_tools import AuditBackend, _context
from test_native_manifest import BODIES, ManifestFixture, sources
from test_native_patch_tool import PatchBackend, response
from test_run_state_approval import SequenceModel, _text_response


class CapturingModel(SequenceModel):
    def __init__(self, responses):
        super().__init__(responses)
        self.requests = []

    async def get_response(self, *args, **kwargs):
        self.requests.append(kwargs)
        return await super().get_response(*args, **kwargs)


class NativeBackend(PatchBackend):
    async def resolve_agent_memory_snapshot(self, project_id, activity_key):
        return {"activity_key": activity_key, "project_id": project_id, "workspace_id": "test-workspace",
                "user_id": "test-user", "version": 0, "current_version": 0, "content_hash": hashlib.sha256(b"{}").hexdigest(),
                "read_enabled": False, "files": {}}

    async def start_agent_tool_call(self, call_id, sdk_id, **kwargs):
        assert self.calls[sdk_id]["status"] in {"approved", "running"}
        self.calls[sdk_id]["status"] = "running"
        return await AuditBackend.start_agent_tool_call(self, call_id, sdk_id, **kwargs)


def native_backend(*, disabled=(), deferred=False):
    backend = NativeBackend()
    patch = backend.catalog["tools"][0]
    patch.update(enabled="apply_patch" not in disabled, defer_loading=deferred)
    for name in ("view_image", "exec_command"):
        item = deepcopy(patch)
        item.update(id="runtime:"+name, name=name, enabled=name not in disabled)
        if name == "view_image":
            item.update(access="read", approval="never", max_result_bytes=16*1024*1024)
        backend.catalog["tools"].append(item)
    return backend


async def assemble(backend, context, client, model, *, read_only=False, policy=None):
    prepared = await AgentToolProvider(backend, [], guardrail_policy=policy or SDKGuardrailPolicy()).prepare(context, [], set(), read_only=read_only)
    binding = await prepare_native_workspace(prepared, client)
    agent = binding.bind_agent(Agent(name="Native assembly", instructions="Keep the supplied source intact.", model=model))
    return agent, binding.run_config(RunConfig(tracing_disabled=True))


@pytest.mark.parametrize("read_only,disabled,expected", [
    (False, (), {"view_image", "exec_command", "apply_patch"}),
    (True, (), {"view_image"}),
    (False, ("apply_patch", "view_image", "exec_command"), set()),
])
def test_runner_binds_filtered_native_tools_and_discovers_skill_from_real_files(tmp_path, read_only, disabled, expected):
    async def scenario():
        disk = ManifestFixture(tmp_path)
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, sources=sources())
        model = CapturingModel([_text_response()])
        context = _context()
        agent, config = await assemble(native_backend(disabled=disabled), context, client, model, read_only=read_only)
        assert isinstance(agent, SandboxAgent) and disk.creates == 0 and disk.prepares == 1
        assert await client.prepare_manifest() == agent.default_manifest and disk.prepares == 1
        result = await Runner.run(agent, "Inspect the installed skills", context=context, run_config=config)
        assert result.final_output and not result.interruptions
        names = {tool.name for tool in model.requests[0]["tools"]}
        assert names == expected
        instructions = model.requests[0]["system_instructions"]
        assert "Keep the supplied source intact." in instructions
        assert "Inspect a supplied outline" in instructions and ".skills/fixture/SKILL.md" in instructions
        client.require_confirmed_cleanup()
        assert disk.prepares == disk.creates == disk.closes == 1 and not disk.commands and not disk.helpers
    asyncio.run(scenario())


def tool_response(name, arguments):
    return ModelResponse(output=[ResponseFunctionToolCall(type="function_call", call_id="native-call", name=name,
                         arguments=json.dumps(arguments), id="native-item")], usage=Usage(requests=1), response_id="native-model")


@pytest.mark.parametrize("decision", ["approve", "reject"])
def test_runner_native_shell_uses_original_approved_arguments_without_helper_exec(tmp_path, decision):
    async def scenario():
        disk = ManifestFixture(tmp_path)
        backend, context = native_backend(), _context()
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, sources=sources())
        arguments = {"cmd": "printf sample", "workdir": "docs", "login": False, "max_output_tokens": 100}
        agent, config = await assemble(backend, context, client, CapturingModel([tool_response("exec_command", arguments)]))
        pending = await Runner.run(agent, "Run the command", context=context, run_config=config)
        assert len(pending.interruptions) == 1 and not disk.commands
        client.require_confirmed_cleanup()
        run_json = pending.to_state().to_json(context_serializer=lambda value: {"project_id": value.project_id})
        saved = client.serialize_session_state(client._session.state)
        disk.lease = None
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, manifest=Manifest.model_validate(saved["manifest"]))
        context = _context()
        agent, config = await assemble(backend, context, client, CapturingModel([_text_response()]))
        state = await RunState.from_json(agent, run_json, context_override=context)
        backend.calls["native-call"]["status"] = "approved" if decision == "approve" else "rejected"
        item = state.get_interruptions()[0]
        state.approve(item) if decision == "approve" else state.reject(item)
        await Runner.run(agent, state, context=context, run_config=config)
        client.require_confirmed_cleanup()
        assert len(disk.commands) == len(backend.completed) == (1 if decision == "approve" else 0)
        if decision == "approve":
            positional, keyword = disk.commands[0]
            assert "cd /workspace/docs && printf sample" in repr((positional, keyword))
            assert arguments == backend.begun[0]["arguments"]
            assert "native-call" in repr((positional, keyword))
        assert current_native_shell_call() is None and not disk.helpers
    asyncio.run(scenario())


def test_runner_native_image_keeps_binary_model_output_out_of_audit_log(tmp_path, monkeypatch):
    def chunk(kind, data):
        return struct.pack("!I", len(data)) + kind + data + struct.pack("!I", zlib.crc32(kind+data))
    png = (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack("!2I5B", 400, 256, 8, 2, 0, 0, 0))
           + chunk(b"IDAT", zlib.compress((b"\0"+b"\x44\x88\xff"*400)*256, level=0)) + chunk(b"IEND", b""))
    monkeypatch.setitem(BODIES, ".skills/fixture/assets/view.png", png)

    async def scenario():
        disk = ManifestFixture(tmp_path)
        backend, context = native_backend(), _context()
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, sources=sources())
        model = CapturingModel([tool_response("view_image", {"path": ".skills/fixture/assets/view.png"}), _text_response()])
        agent, config = await assemble(backend, context, client, model)
        result = await Runner.run(agent, "Inspect the image", context=context, run_config=config)
        assert result.final_output and not result.interruptions and len(backend.completed) == 1
        outputs = [item for item in model.requests[1]["input"] if isinstance(item, dict) and item.get("type") == "function_call_output"]
        url = outputs[0]["output"][0]["image_url"]
        assert url.startswith("data:image/png;base64,") and len(url) > 256*1024
        audit = backend.completed[0]["result"]["image_url"]
        assert audit == {"omitted": "binary_resource", "encoded_bytes": len(url), "sha256": hashlib.sha256(url.encode()).hexdigest()}
        assert "data:image" not in json.dumps(backend.completed) and not backend.failed
        client.require_confirmed_cleanup()
        assert not disk.helpers and not disk.commands
    asyncio.run(scenario())


@pytest.mark.parametrize("failure", ["tty", "patch_policy", "shell_policy"])
def test_bound_native_tools_reject_unsupported_or_protected_input_before_registration(tmp_path, failure):
    async def scenario():
        disk = ManifestFixture(tmp_path)
        backend, context = native_backend(), _context()
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, sources=sources())
        if failure == "patch_policy":
            call = response().output[0]
            call.input = call.input.replace("native patch", "PRIVATE_NATIVE_VALUE")
            reply = ModelResponse(output=[call], usage=Usage(requests=1), response_id="guarded-patch")
        else:
            arguments = {"cmd": "printf PRIVATE_NATIVE_VALUE"} if failure == "shell_policy" else {"cmd": "printf sample", "tty": True}
            reply = tool_response("exec_command", arguments)
        agent, config = await assemble(backend, context, client, CapturingModel([reply]), policy=SDKGuardrailPolicy(protected_values=("PRIVATE_NATIVE_VALUE",)))
        with pytest.raises((AgentToolConfigurationError, ToolInputGuardrailTripwireTriggered, UserError)) as error:
            await Runner.run(agent, "Check guarded input", context=context, run_config=config)
        assert ("Interactive native execution" if failure == "tty" else "policy") in str(error.value)
        assert not backend.begun and not backend.started and not disk.commands
        assert not (disk.root / "note.txt").exists()
        client.require_confirmed_cleanup()
    asyncio.run(scenario())


@pytest.mark.parametrize("read_only,disabled,expected", [(False, (), True), (True, (), True), (False, ("apply_patch", "exec_command", "view_image"), False)])
def test_deferred_native_tools_supply_sdk_search_only_when_allowed(tmp_path, read_only, disabled, expected):
    async def scenario():
        disk = ManifestFixture(tmp_path)
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, sources=sources())
        backend, context = native_backend(disabled=disabled, deferred=True), _context()
        model = CapturingModel([_text_response()])
        agent, config = await assemble(backend, context, client, model, read_only=read_only)
        await Runner.run(agent, "Discover tools", context=context, run_config=config)
        tools = model.requests[0]["tools"]
        assert any(isinstance(tool, ToolSearchTool) for tool in tools) == expected
        for tool in tools:
            if getattr(tool, "name", None) in {"apply_patch", "view_image", "exec_command"}:
                assert tool.defer_loading
                assert not read_only or tool.name == "view_image"
        client.require_confirmed_cleanup()
    asyncio.run(scenario())


@pytest.mark.parametrize("failure", ["lost", "cancel"])
def test_resource_preparation_failure_does_not_create_environment_or_replay(tmp_path, failure):
    async def scenario():
        disk = ManifestFixture(tmp_path)
        original = disk.prepare_manifest
        async def fail_after_preparation(selected):
            await original(selected)
            if failure == "cancel":
                raise asyncio.CancelledError()
            raise BackendError("Preparation receipt lost")
        disk.prepare_manifest = fail_after_preparation
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, sources=sources())
        with pytest.raises((BackendError, asyncio.CancelledError)):
            await client.prepare_manifest()
        with pytest.raises(BackendError, match="cannot be replayed"):
            await client.prepare_manifest()
        with pytest.raises(BackendError, match="cannot be replayed"):
            await client.create()
        assert disk.prepares == 1 and disk.creates == disk.closes == 0
    asyncio.run(scenario())


@pytest.mark.parametrize("failure", ["no_policy", "patch_unapproved", "patch_retry", "image_write", "wrong_name"])
def test_native_assembly_rejects_unsafe_policy_before_reserving_resources(tmp_path, failure):
    async def scenario():
        disk = ManifestFixture(tmp_path)
        backend, context = native_backend(), _context()
        if failure == "patch_unapproved":
            backend.catalog["tools"][0]["approval"] = "never"
        elif failure == "patch_retry":
            backend.catalog["tools"][0]["max_retries"] = 1
        elif failure == "image_write":
            backend.catalog["tools"][1]["access"] = "write"
        elif failure == "wrong_name":
            backend.catalog["tools"][0]["name"] = "some_other_tool"
        prepared = await AgentToolProvider(backend, [], guardrail_policy=None if failure == "no_policy" else SDKGuardrailPolicy()).prepare(context, [], set())
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, sources=sources())
        with pytest.raises(AgentToolConfigurationError):
            await prepare_native_workspace(prepared, client)
        assert not disk.current_lease and disk.prepares == disk.creates == 0
    asyncio.run(scenario())


def test_runner_final_text_is_not_deliverable_after_unconfirmed_native_cleanup(tmp_path):
    async def scenario():
        disk = ManifestFixture(tmp_path)
        disk.fail_close = True
        backend, context = native_backend(), _context()
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, sources=sources())
        agent, config = await assemble(backend, context, client, CapturingModel([_text_response()]))
        result = await Runner.run(agent, "Finish", context=context, run_config=config)
        assert result.final_output
        with pytest.raises(BackendError):
            client.require_confirmed_cleanup()
        assert disk.closes == 0 and disk.status == "ready"
    asyncio.run(scenario())


@pytest.mark.parametrize("decision", ["approve", "reject"])
def test_runner_native_patch_approval_serialization_rebuild_and_cleanup(tmp_path, decision):
    async def scenario():
        disk = ManifestFixture(tmp_path)
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, sources=sources())
        backend, context = native_backend(), _context()
        agent, config = await assemble(backend, context, client, CapturingModel([response()]))
        pending = await Runner.run(agent, "Write the note", context=context, run_config=config)
        assert len(pending.interruptions) == 1 and not (disk.root / "note.txt").exists()
        first = client.require_confirmed_cleanup()
        run_json = pending.to_state().to_json(context_serializer=lambda value: {"project_id": value.project_id})
        saved = client.serialize_session_state(client._session.state)
        disk.lease = None
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, manifest=Manifest.model_validate(saved["manifest"]))
        context = _context()
        agent, config = await assemble(backend, context, client, CapturingModel([_text_response()]))
        state = await RunState.from_json(agent, run_json, context_override=context)
        backend.calls["patch-call"]["status"] = "approved" if decision == "approve" else "rejected"
        interruption = state.get_interruptions()[0]
        state.approve(interruption) if decision == "approve" else state.reject(interruption)
        result = await Runner.run(agent, state, context=context, run_config=config)
        assert result.final_output and not result.interruptions
        assert (disk.root / "note.txt").exists() == (decision == "approve")
        assert len(backend.started) == len(backend.completed) == (1 if decision == "approve" else 0)
        if decision == "approve":
            assert (disk.root / "note.txt").read_bytes() == b"native patch"
        assert client.require_confirmed_cleanup().version > first.version
        assert disk.prepares == 1 and not disk.commands and not disk.helpers
    asyncio.run(scenario())
