from __future__ import annotations

import asyncio
import hashlib
import json
from pathlib import Path
import sys
from types import SimpleNamespace
from typing import Any
from uuid import uuid4

import pytest
from agents import Agent, FunctionTool, Runner, ToolSearchTool, function_tool
from agents.items import ModelResponse
from agents.models.interface import Model
from agents.run_context import RunContextWrapper
from agents.tool_context import ToolContext
from agents.tool import validate_responses_tool_search_configuration
from agents.usage import Usage
from openai.types.responses import ResponseFunctionToolCall

from content_agent_sidecar.agent_tools import (
    AgentToolCatalog,
    AgentToolConfigurationError,
    AgentToolProvider,
)


@function_tool
async def echo_value(value: str) -> dict[str, str]:
    """Echo a value."""

    return {"value": value}


@function_tool
async def execute_skill_script(
    skill_name: str, script_id: str, input_json: str
) -> dict[str, Any]:
    """Execute a declared Skill script."""

    return {"skill_name": skill_name, "script_id": script_id, "input_json": input_json}


class AuditBackend:
    def __init__(self, catalog: dict[str, Any]) -> None:
        self.catalog = catalog
        self.begun: list[dict[str, Any]] = []
        self.started: list[tuple[str, str]] = []
        self.started_hashes: list[str] = []
        self.completed: list[dict[str, Any]] = []
        self.failed: list[dict[str, Any]] = []
        self.cancelled: list[dict[str, Any]] = []

    async def get_agent_tool_catalog(self) -> dict[str, Any]:
        return self.catalog

    async def begin_agent_tool_call(self, **payload: Any) -> dict[str, Any]:
        self.begun.append(payload)
        descriptor = next(
            item for item in self.catalog["tools"] if item["id"] == payload["tool_id"]
        )
        status = (
            "pending_approval"
            if descriptor["approval"] == "always"
            else "running"
        )
        return {
            "agent_tool_call_id": f"tcall-{len(self.begun)}",
            "sdk_tool_call_id": payload["sdk_tool_call_id"],
            "tool_id": payload["tool_id"],
            "arguments_hash": hashlib.sha256(json.dumps(payload["arguments"], ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()).hexdigest(),
            "status": status,
        }

    async def start_agent_tool_call(
        self, agent_tool_call_id: str, sdk_tool_call_id: str, *, configuration_hash: str = ""
    ) -> dict[str, Any]:
        self.started.append((agent_tool_call_id, sdk_tool_call_id))
        self.started_hashes.append(configuration_hash)
        return {"agent_tool_call_id": agent_tool_call_id, "status": "running"}

    async def complete_agent_tool_call(self, agent_tool_call_id: str, **payload: Any) -> dict[str, Any]:
        self.completed.append({"agent_tool_call_id": agent_tool_call_id, **payload})
        return {"agent_tool_call_id": agent_tool_call_id, "status": "completed"}

    async def fail_agent_tool_call(self, agent_tool_call_id: str, **payload: Any) -> dict[str, Any]:
        self.failed.append({"agent_tool_call_id": agent_tool_call_id, **payload})
        return {"agent_tool_call_id": agent_tool_call_id, "status": "failed"}

    async def cancel_agent_tool_call(self, agent_tool_call_id: str, reason: str) -> dict[str, Any]:
        self.cancelled.append({"agent_tool_call_id": agent_tool_call_id, "reason": reason})
        return {"agent_tool_call_id": agent_tool_call_id, "status": "cancelled"}


class StaticModel(Model):
    def __init__(self, response: ModelResponse) -> None:
        self.response = response

    async def get_response(self, *_args: Any, **_kwargs: Any) -> ModelResponse:
        return self.response

    def stream_response(self, *_args: Any, **_kwargs: Any) -> Any:
        raise AssertionError("streaming is not used by this fixture")

    async def close(self) -> None:
        return None


def _context() -> SimpleNamespace:
    return SimpleNamespace(
        project_id="prj-tools",
        conversation_id="conv-tools",
        agent_turn_id="turn-tools",
        skill_invocation_id="sinv-tools",
        active_tool_calls={},
    )


def test_stateful_provider_binds_real_execution_attempt_without_background_alias() -> None:
    backend = AuditBackend(_runtime_catalog())
    context = _context()
    context.agent_turn_id = ""
    context.agent_task_attempt_id = ""
    context.execution_attempt_id = "stateful-attempt"
    context.attempt_token = "stateful-private-token"
    wrapper = ToolContext(context, tool_name="echo_value", tool_call_id="sdk-stateful", tool_arguments='{"value":"one"}')
    provider = AgentToolProvider(backend, [])
    asyncio.run(provider.ensure_call(wrapper, "runtime:echo_value", {"value": "one"}, "sdk-stateful"))
    assert backend.begun[0]["execution_attempt_id"] == "stateful-attempt"
    assert backend.begun[0]["attempt_token"] == "stateful-private-token"
    assert backend.begun[0]["agent_turn_id"] == ""
    assert backend.begun[0]["agent_task_attempt_id"] == ""


def _runtime_catalog(*, approval: str = "never") -> dict[str, Any]:
    return {
        "schema_version": "1.0.0",
        "tools": [
            {
                "id": "runtime:echo_value",
                "kind": "runtime_function",
                "name": "echo_value",
                "description": "Echo a value",
                "access": "read" if approval == "never" else "write",
                "approval": approval,
                "enabled": True,
                "defer_loading": False,
                "timeout_seconds": 5,
                "max_retries": 0,
                "max_result_bytes": 4096,
            }
        ],
        "mcp_servers": [],
        "hosted_tools": [],
    }


def test_recoverable_sdk_function_error_is_audited_as_failure() -> None:
    @function_tool(name_override="echo_value")
    async def fail_read(value: str) -> str:
        """Reject unavailable content."""
        raise ValueError("resource unavailable")

    backend = AuditBackend(_runtime_catalog())
    provider = AgentToolProvider(backend, [fail_read])

    async def execute():
        prepared = await provider.prepare(_context(), [], set())
        context = ToolContext(_context(), tool_name="echo_value", tool_call_id="failed-read", tool_arguments='{"value":"test"}')
        return await prepared.tools[0].on_invoke_tool(context, '{"value":"test"}')

    output = asyncio.run(execute())
    assert "resource unavailable" in output
    assert len(backend.failed) == 1
    assert backend.completed == []


@pytest.mark.parametrize("hosted", [False, True])
@pytest.mark.parametrize("deferred", [False, True])
def test_prepared_deferred_tools_include_native_sdk_search(hosted: bool, deferred: bool) -> None:
    catalog = _runtime_catalog()
    descriptor = catalog["tools"][0]
    descriptor["defer_loading"] = deferred
    if hosted:
        descriptor.update(id="hosted:search", kind="hosted", name="web_search")
        catalog["hosted_tools"] = [{"id": "search", "type": "web_search", "enabled": True}]
    backend = AuditBackend(catalog)
    provider = AgentToolProvider(backend, [] if hosted else [echo_value], hosted_model="offline-test")
    prepared = asyncio.run(provider.prepare(_context(), [], set()))

    validate_responses_tool_search_configuration(prepared.tools)
    searches = [tool for tool in prepared.tools if isinstance(tool, ToolSearchTool)]
    assert len(searches) == int(deferred)
    wrapped = [tool for tool in prepared.tools if isinstance(tool, FunctionTool)]
    assert len(wrapped) == 1
    assert wrapped[0].defer_loading is deferred
    assert wrapped[0].needs_approval is not False
    assert backend.begun == []


@pytest.mark.parametrize("disabled", [False, True])
def test_search_does_not_restore_tools_excluded_by_policy(disabled: bool) -> None:
    catalog = _runtime_catalog(approval="always")
    catalog["tools"][0].update(defer_loading=True, enabled=not disabled)
    provider = AgentToolProvider(AuditBackend(catalog), [echo_value])
    prepared = asyncio.run(provider.prepare(_context(), [], set(), read_only=not disabled))
    assert prepared.tools == []
    validate_responses_tool_search_configuration(prepared.tools)


def test_tool_configuration_hash_reaches_begin_start_and_rejects_cached_replacement() -> None:
    catalog = _runtime_catalog(approval="always")
    catalog["tools"][0]["configuration_hash"] = "sha256:original"
    backend = AuditBackend(catalog)
    provider = AgentToolProvider(backend, [echo_value])
    context = _context()

    async def execute() -> None:
        prepared = await provider.prepare(context, [], set())
        tool_context = ToolContext(context, tool_name="echo_value", tool_call_id="bound-call", tool_arguments='{"value":"test"}')
        assert await prepared.tools[0].needs_approval(tool_context, {"value": "test"}, "bound-call")
        assert backend.begun[0]["configuration_hash"] == "sha256:original"
        context.active_tool_calls["bound-call"]["status"] = "approved"
        await prepared.tools[0].on_invoke_tool(tool_context, '{"value":"test"}')
        assert backend.started_hashes == ["sha256:original"]
        catalog["tools"][0]["configuration_hash"] = "sha256:replacement"
        replacement = await provider.prepare(context, [], set())
        with pytest.raises(AgentToolConfigurationError, match="configuration changed"):
            await replacement.tools[0].needs_approval(tool_context, {"value": "test"}, "bound-call")
        assert len(backend.begun) == 1

    asyncio.run(execute())


def test_read_only_background_tools_cannot_acquire_writes() -> None:
    backend = AuditBackend(_runtime_catalog(approval="always"))
    provider = AgentToolProvider(backend, [echo_value])
    prepared = asyncio.run(provider.prepare(_context(), [], set(), read_only=True))
    assert prepared.tools == []

    catalog = _mcp_catalog(Path("unused"))
    provider = AgentToolProvider(AuditBackend(catalog), [])
    with pytest.raises(AgentToolConfigurationError, match="background Skills"):
        asyncio.run(provider.prepare(_context(), _selected_skill(), {"story_fact_check"}, read_only=True))


def _mcp_catalog(script: Path) -> dict[str, Any]:
    descriptors = []
    allowed_tools = []
    for name, access, approval in (
        ("lookup_story_fact", "read", "never"),
        ("save_story_fact", "write", "always"),
    ):
        descriptors.append(
            {
                "id": f"mcp:story-fixture/{name}",
                "configuration_hash": "sha256:fixture-service-configuration",
                "kind": "mcp",
                "server_id": "story-fixture",
                "name": name,
                "description": name.replace("_", " "),
                "transport": "stdio",
                "access": access,
                "approval": approval,
                "enabled": True,
                "defer_loading": True,
                "timeout_seconds": 10,
                "max_retries": 0,
                "max_result_bytes": 8192,
            }
        )
        allowed_tools.append(
            {
                "name": name,
                "description": name.replace("_", " "),
                "access": access,
                "approval": approval,
            }
        )
    return {
        "schema_version": "1.0.0",
        "tools": descriptors,
        "mcp_servers": [
            {
                "id": "story-fixture",
                "description": "Story protocol fixture",
                "transport": "stdio",
                "command": sys.executable,
                "args": [str(script)],
                "cwd": str(script.parent),
                "environment": {"STORY_MCP_STATE_FILE": "TEST_STORY_MCP_STATE_FILE"},
                "header_environment": {},
                "enabled": True,
                "allowed_tools": allowed_tools,
                "defer_loading": True,
                "timeout_seconds": 10,
                "max_retries": 0,
                "max_result_bytes": 8192,
            }
        ],
        "hosted_tools": [],
    }


def _selected_skill() -> list[dict[str, Any]]:
    return [
        {
            "capability_id": "story_fact_check",
            "skill": {
                "dependencies": [
                    {
                        "type": "mcp",
                        "value": "story-fixture/lookup_story_fact",
                        "status": "available",
                    },
                    {
                        "type": "mcp",
                        "value": "story-fixture/save_story_fact",
                        "status": "available",
                    },
                ]
            },
        }
    ]


def test_catalog_rejects_missing_descriptor_and_retrying_approval_server() -> None:
    catalog = _mcp_catalog(Path("fixture.py"))
    catalog["tools"].pop()
    with pytest.raises(AgentToolConfigurationError, match="descriptor missing"):
        AgentToolCatalog.parse(catalog)

    catalog = _mcp_catalog(Path("fixture.py"))
    catalog["mcp_servers"][0]["max_retries"] = 1
    backend = AuditBackend(catalog)
    provider = AgentToolProvider(backend, [])
    with pytest.raises(AgentToolConfigurationError, match="cannot retry"):
        asyncio.run(provider.prepare(_context(), _selected_skill(), {"story_fact_check"}))


def test_runtime_function_tool_uses_sdk_call_id_and_audit_lifecycle() -> None:
    backend = AuditBackend(_runtime_catalog())
    provider = AgentToolProvider(backend, [echo_value])
    context = _context()

    async def execute() -> Any:
        prepared = await provider.prepare(context, [], set())
        tool = prepared.tools[0]
        wrapper = RunContextWrapper(context)
        assert callable(tool.needs_approval)
        assert await tool.needs_approval(wrapper, {"value": "alpha"}, "sdk-read-1") is False
        tool_context = ToolContext(
            context,
            tool_name=tool.name,
            tool_call_id="sdk-read-1",
            tool_arguments='{"value":"alpha"}',
        )
        return await tool.on_invoke_tool(tool_context, '{"value":"alpha"}')

    output = asyncio.run(execute())

    assert output == {"value": "alpha"}
    assert backend.begun[0]["sdk_tool_call_id"] == "sdk-read-1"
    assert backend.started == [("tcall-1", "sdk-read-1")]
    assert backend.completed[0]["agent_tool_call_id"] == "tcall-1"


def test_runtime_function_write_is_not_invoked_before_approval() -> None:
    backend = AuditBackend(_runtime_catalog(approval="always"))
    provider = AgentToolProvider(backend, [echo_value])

    async def check() -> None:
        prepared = await provider.prepare(_context(), [], set())
        tool = prepared.tools[0]
        assert callable(tool.needs_approval)
        assert await tool.needs_approval(
            RunContextWrapper(_context()), {"value": "blocked"}, "sdk-write-1"
        ) is True

    asyncio.run(check())
    assert backend.started == []
    assert backend.completed == []


def test_skill_script_tool_is_exposed_only_for_selected_script_skill() -> None:
    catalog = _runtime_catalog(approval="always")
    catalog["tools"][0].update(
        {
            "id": "runtime:execute_skill_script",
            "name": "execute_skill_script",
            "description": "Execute a declared Skill script in the OCI sandbox",
            "access": "sensitive",
            "timeout_seconds": 300,
        }
    )
    backend = AuditBackend(catalog)
    provider = AgentToolProvider(backend, [execute_skill_script])

    async def check() -> None:
        context = _context()
        without_scripts = await provider.prepare(
            context,
            [{"capability_id": "plain", "skill": {"name": "plain", "scripts": []}}],
            {"plain"},
        )
        from agents import RunContextWrapper
        assert await Agent(name="No scripts", tools=without_scripts.tools).get_all_tools(RunContextWrapper(context)) == []
        assert context.allowed_skill_scripts == set()
        context = _context()
        with_scripts = await provider.prepare(
            context,
            [
                {
                    "capability_id": "scripted",
                    "version": "1.2.0",
                    "skill": {
                        "name": "sandbox-skill",
                        "content_hash": "sha256:selected-script",
                        "scripts": [
                            {
                                "id": "render",
                                "path": "scripts/render.py",
                                "runtime": "python",
                            }
                        ]
                    },
                }
            ],
            {"scripted"},
        )
        assert [tool.name for tool in with_scripts.tools] == ["execute_skill_script"]
        assert callable(with_scripts.tools[0].needs_approval)
        assert context.allowed_skill_scripts == {("scripted", "render")}
        arguments = {"skill_name": "sandbox-skill", "script_id": "render", "input_json": "{}"}
        agent = Agent(name="Script approval", tools=with_scripts.tools, model=StaticModel(ModelResponse(output=[ResponseFunctionToolCall(
            call_id="sdk-script-pin", name="execute_skill_script", arguments=json.dumps(arguments), type="function_call",
        )], usage=Usage(requests=1), response_id=None)))
        pending = await Runner.run(agent, "run script", context=context, max_turns=1)
        assert len(pending.interruptions) == 1
        assert backend.started == []
        assert backend.begun[-1]["arguments"] == arguments
        assert backend.begun[-1]["skill_snapshot"] == {"capability_id": "scripted", "version": "1.2.0", "content_hash": "sha256:selected-script"}
        context.skill_script_snapshots = {}
        with pytest.raises(AgentToolConfigurationError, match="verified version snapshot"):
            await provider.ensure_call(RunContextWrapper(context), "runtime:execute_skill_script", arguments, "sdk-script-missing-pin")

    asyncio.run(check())


def test_stdio_mcp_read_executes_and_sdk_write_interrupts_before_transport(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    script = Path(__file__).parent / "fixtures" / "story_mcp_server.py"
    project_root = Path(__file__).resolve().parents[3]
    state_file = project_root / ".tmp" / f"mcp-writes-{uuid4().hex}.jsonl"
    monkeypatch.setenv("TEST_STORY_MCP_STATE_FILE", str(state_file))
    backend = AuditBackend(_mcp_catalog(script))
    provider = AgentToolProvider(backend, [])
    context = _context()

    async def execute() -> tuple[Any, Any]:
        prepared = await provider.prepare(
            context, _selected_skill(), {"story_fact_check"}
        )
        async with prepared:
            agent = Agent(
                name="MCP fixture Agent",
                model="unused",
                mcp_servers=prepared.mcp_servers,
                mcp_config={
                    "convert_schemas_to_strict": True,
                    "include_server_in_tool_names": True,
                },
            )
            tools = await agent.get_all_tools(RunContextWrapper(context))
            read_tool = next(tool for tool in tools if tool.name.endswith("lookup_story_fact"))
            write_tool = next(tool for tool in tools if tool.name.endswith("save_story_fact"))
            read_context = ToolContext(
                context,
                tool_name=read_tool.name,
                tool_call_id="sdk-mcp-read",
                tool_arguments='{"key":"hero"}',
            )
            read_output = await read_tool.on_invoke_tool(
                read_context, '{"key":"hero"}'
            )

            write_call = ResponseFunctionToolCall(
                id=None,
                call_id="sdk-mcp-write",
                arguments=json.dumps({"key": "hero", "value": "沈砚"}, ensure_ascii=False),
                name=write_tool.name,
                type="function_call",
                status="completed",
            )
            agent.model = StaticModel(
                ModelResponse(output=[write_call], usage=Usage(requests=1), response_id=None)
            )
            pending = await Runner.run(agent, "save the fact", context=context, max_turns=1)
            return read_output, pending

    read_output, pending = asyncio.run(execute())

    assert "沈砚" in json.dumps(read_output, ensure_ascii=False)
    assert len(pending.interruptions) == 1
    assert not state_file.exists()
    assert backend.begun[-1]["sdk_tool_call_id"] == "sdk-mcp-write"
    assert backend.begun[-1]["tool_id"] == "mcp:story-fixture/save_story_fact"
    assert backend.begun[-1]["configuration_hash"] == "sha256:fixture-service-configuration"
    assert backend.started_hashes == ["sha256:fixture-service-configuration"]
    assert all(item[1] != "sdk-mcp-write" for item in backend.started)
