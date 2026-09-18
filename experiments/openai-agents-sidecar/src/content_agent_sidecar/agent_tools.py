from __future__ import annotations

import asyncio
from contextlib import suppress
from contextvars import ContextVar
from dataclasses import dataclass, field, replace
from copy import copy, deepcopy
import json
import hashlib
import inspect
import logging
import os
from typing import Any, Callable, Iterable, Mapping

from agents import FunctionTool, ProgrammaticToolCallingTool, ToolSearchTool
from agents.tool import CustomTool
from agents.sandbox.capabilities.tools.apply_patch_tool import SandboxApplyPatchTool
from agents.tool import ToolOutputFileContent, ToolOutputImage, default_tool_error_function, maybe_invoke_function_tool_failure_error_function, set_function_tool_failure_error_function
from agents.mcp import (
    MCPServerManager,
    MCPServerSse,
    MCPServerStdio,
    MCPServerStreamableHttp,
    create_static_tool_filter,
)
from .subagents import SUBTASK_READ_TOOLS, SUBTASK_TOOL_NAME, SubtaskScope, build_subtask_tool
from .skill_metadata import allows_implicit_skill_invocation


logger = logging.getLogger(__name__)


class AgentToolConfigurationError(RuntimeError):
    pass


def _mcp_failure_error(context: Any, error: Exception) -> str:
    # SDK wraps transport/meta errors. Broken audit authority must not become a
    # model-visible tool error that lets the agent issue another operation.
    cause: BaseException | None = error
    seen: set[int] = set()
    while cause is not None and id(cause) not in seen:
        seen.add(id(cause))
        if isinstance(cause, AgentToolConfigurationError):
            raise cause
        cause = cause.__cause__
    return default_tool_error_function(context, error)


def _copy_function_tool(tool: FunctionTool, **changes: Any) -> FunctionTool:
    if type(tool) is FunctionTool:
        return replace(tool, **changes)
    # Native sandbox tools have custom constructors and live session bindings.
    # Copy the bound instance instead of invoking its constructor as a dataclass.
    cloned = copy(tool)
    for name, value in changes.items():
        setattr(cloned, name, value)
    return cloned


TOOL_APPROVAL_INSTRUCTIONS = (
    "用户已经请求执行一项操作时，若目标工具需要审批，必须实际调用目标工具来申请审批。"
    "SDK 会先登记调用并产生 interruption，在用户批准前不会执行该工具的外部操作。"
    "不要在调用工具之前自行回复‘已暂停、等待批准’，也不要以聊天澄清代替平台审批卡。"
    "审批暂停不是任务完成，不应调用终态提交工具或返回已完成结果；由 SDK 保存状态并暂停。"
    "获批后继续原调用，被拒绝后如实说明未执行；只能根据实际工具结果声称执行成功。"
)


class AgentToolApprovalRequired(RuntimeError):
    pass


class AgentToolApprovalRejected(RuntimeError):
    pass


class AgentToolResultTooLarge(RuntimeError):
    pass


@dataclass(frozen=True)
class ToolDescriptor:
    id: str
    kind: str
    name: str
    description: str
    access: str
    approval: str
    enabled: bool
    server_id: str = ""
    transport: str = ""
    defer_loading: bool = False
    timeout_seconds: int = 30
    max_retries: int = 0
    max_result_bytes: int = 262144
    configuration_hash: str = ""

    @classmethod
    def parse(cls, source: Mapping[str, Any]) -> "ToolDescriptor":
        result_limit = 16 * 1024 * 1024 if source.get("kind") == "runtime_function" and source.get("id") in {"runtime:read_skill_resource", "runtime:view_image"} else 1024 * 1024
        descriptor = cls(
            id=_required_string(source, "id"),
            kind=_required_string(source, "kind"),
            name=_required_string(source, "name"),
            description=_required_string(source, "description"),
            access=_required_string(source, "access"),
            approval=_required_string(source, "approval"),
            enabled=bool(source.get("enabled")),
            server_id=str(source.get("server_id") or "").strip(),
            transport=str(source.get("transport") or "").strip(),
            defer_loading=bool(source.get("defer_loading")),
            configuration_hash=str(source.get("configuration_hash") or ""),
            timeout_seconds=_bounded_int(source, "timeout_seconds", 1, 600),
            max_retries=_bounded_int(source, "max_retries", 0, 5),
            max_result_bytes=_bounded_int(
                source, "max_result_bytes", 1024, result_limit
            ),
        )
        if descriptor.kind not in {"runtime_function", "mcp", "hosted"}:
            raise AgentToolConfigurationError(
                f"unsupported Agent tool kind {descriptor.kind!r}"
            )
        if descriptor.access not in {"read", "write", "sensitive"}:
            raise AgentToolConfigurationError(
                f"unsupported Agent tool access {descriptor.access!r}"
            )
        if descriptor.approval not in {"never", "always"}:
            raise AgentToolConfigurationError(
                f"unsupported Agent tool approval {descriptor.approval!r}"
            )
        return descriptor


@dataclass(frozen=True)
class MCPServerDefinition:
    id: str
    description: str
    transport: str
    url: str
    command: str
    args: tuple[str, ...]
    cwd: str
    environment: Mapping[str, str]
    header_environment: Mapping[str, str]
    enabled: bool
    allowed_tools: tuple[str, ...]
    defer_loading: bool
    timeout_seconds: int
    max_retries: int
    max_result_bytes: int
    credential_environment: Mapping[str, str] = field(default_factory=dict)
    credential_headers: Mapping[str, str] = field(default_factory=dict)
    credential_binding: str = ""

    @classmethod
    def parse(cls, source: Mapping[str, Any]) -> "MCPServerDefinition":
        tools = source.get("allowed_tools")
        if not isinstance(tools, list) or not tools:
            raise AgentToolConfigurationError("MCP allowed_tools must be non-empty")
        names = tuple(_required_string(item, "name") for item in tools if isinstance(item, dict))
        if len(names) != len(tools) or len(set(names)) != len(names):
            raise AgentToolConfigurationError("MCP allowed_tools are invalid or duplicated")
        definition = cls(
            id=_required_string(source, "id"),
            description=_required_string(source, "description"),
            transport=_required_string(source, "transport"),
            url=str(source.get("url") or "").strip(),
            command=str(source.get("command") or "").strip(),
            args=tuple(str(item) for item in source.get("args") or []),
            cwd=str(source.get("cwd") or "").strip(),
            environment=_string_map(source.get("environment")),
            header_environment=_string_map(source.get("header_environment")),
            credential_environment=_string_map(source.get("credential_environment")),
            credential_headers=_string_map(source.get("credential_headers")),
            credential_binding=str(source.get("credential_binding") or ""),
            enabled=bool(source.get("enabled")),
            allowed_tools=names,
            defer_loading=bool(source.get("defer_loading")),
            timeout_seconds=_bounded_int(source, "timeout_seconds", 1, 300),
            max_retries=_bounded_int(source, "max_retries", 0, 5),
            max_result_bytes=_bounded_int(
                source, "max_result_bytes", 1024, 1024 * 1024
            ),
        )
        if definition.transport not in {"streamable_http", "sse", "stdio"}:
            raise AgentToolConfigurationError(
                f"unsupported MCP transport {definition.transport!r}"
            )
        return definition


@dataclass(frozen=True)
class HostedToolDefinition:
    id: str
    type: str
    enabled: bool
    vector_store_ids: tuple[str, ...] = ()
    max_num_results: int = 10

    @classmethod
    def parse(cls, source: Mapping[str, Any]) -> "HostedToolDefinition":
        return cls(
            id=_required_string(source, "id"),
            type=_required_string(source, "type"),
            enabled=bool(source.get("enabled")),
            vector_store_ids=tuple(source.get("vector_store_ids") or ()),
            max_num_results=int(source.get("max_num_results") or 10),
        )


@dataclass(frozen=True)
class AgentToolCatalog:
    descriptors: Mapping[str, ToolDescriptor]
    mcp_servers: Mapping[str, MCPServerDefinition]
    hosted_tools: tuple[HostedToolDefinition, ...]
    programmatic_tool_ids: tuple[str, ...] = ()

    @classmethod
    def parse(cls, source: Mapping[str, Any]) -> "AgentToolCatalog":
        if source.get("schema_version") != "1.0.0":
            raise AgentToolConfigurationError("unsupported Agent tool catalog version")
        raw_tools = source.get("tools")
        if not isinstance(raw_tools, list):
            raise AgentToolConfigurationError("Agent tool catalog tools must be an array")
        descriptors: dict[str, ToolDescriptor] = {}
        for item in raw_tools:
            if not isinstance(item, dict):
                raise AgentToolConfigurationError("Agent tool descriptor must be an object")
            descriptor = ToolDescriptor.parse(item)
            if descriptor.id in descriptors:
                raise AgentToolConfigurationError(
                    f"duplicated Agent tool descriptor {descriptor.id!r}"
                )
            descriptors[descriptor.id] = descriptor

        servers: dict[str, MCPServerDefinition] = {}
        for item in source.get("mcp_servers") or []:
            if not isinstance(item, dict):
                raise AgentToolConfigurationError("MCP server definition must be an object")
            server = MCPServerDefinition.parse(item)
            if server.id in servers:
                raise AgentToolConfigurationError(
                    f"duplicated MCP server definition {server.id!r}"
                )
            for tool_name in server.allowed_tools:
                descriptor = descriptors.get(f"mcp:{server.id}/{tool_name}")
                if descriptor is None or descriptor.server_id != server.id:
                    raise AgentToolConfigurationError(
                        f"MCP tool descriptor missing for {server.id}/{tool_name}"
                    )
            servers[server.id] = server

        hosted: list[HostedToolDefinition] = []
        for item in source.get("hosted_tools") or []:
            if not isinstance(item, dict):
                raise AgentToolConfigurationError("hosted tool definition must be an object")
            definition = HostedToolDefinition.parse(item)
            if f"hosted:{definition.id}" not in descriptors:
                raise AgentToolConfigurationError(
                    f"hosted tool descriptor missing for {definition.id!r}"
                )
            hosted.append(definition)
        programmatic_ids = source.get("programmatic_tool_ids", [])
        if (not isinstance(programmatic_ids, list)
                or any(not isinstance(item, str) or not item for item in programmatic_ids)
                or len(set(programmatic_ids)) != len(programmatic_ids)):
            raise AgentToolConfigurationError("Programmatic tool IDs must be a unique string array")
        for tool_id in programmatic_ids:
            descriptor = descriptors.get(tool_id)
            if (descriptor is None or descriptor.kind != "runtime_function"
                    or descriptor.access != "read" or descriptor.approval != "never"):
                raise AgentToolConfigurationError("Programmatic tools require explicit read-only runtime descriptors without approval")
        return cls(descriptors=descriptors, mcp_servers=servers, hosted_tools=tuple(hosted),
                   programmatic_tool_ids=tuple(programmatic_ids))


@dataclass
class PreparedAgentTools:
    tools: list[Any]
    mcp_servers: list[Any]
    provider: AgentToolProvider | None = field(default=None, repr=False)
    context: Any = field(default=None, repr=False)
    catalog: AgentToolCatalog | None = field(default=None, repr=False)
    capabilities: dict[str, dict[str, Any]] = field(default_factory=dict, repr=False)
    discoverable_capabilities: dict[str, dict[str, Any]] = field(default_factory=dict, repr=False)
    selected_ids: set[str] = field(default_factory=set, repr=False)
    pending_installed_ids: set[str] = field(default_factory=set, init=False, repr=False)
    read_only: bool = False
    subtasks: SubtaskScope | None = field(default=None, repr=False)
    _managers: list[MCPServerManager] = field(default_factory=list, init=False, repr=False)
    _lock: asyncio.Lock = field(default_factory=asyncio.Lock, init=False, repr=False)
    _active: bool = field(default=False, init=False)

    @staticmethod
    def _manager(servers: list[Any]) -> MCPServerManager:
        timeout = max([10.0, *(float(server._agent_tool_server.timeout_seconds) for server in servers)])
        return MCPServerManager(servers, strict=True, connect_in_parallel=True,
                                connect_timeout_seconds=timeout, suppress_cancelled_error=False)

    async def __aenter__(self) -> "PreparedAgentTools":
        if self._active:
            raise AgentToolConfigurationError("Tool preparation is already active")
        manager = self._manager(self.mcp_servers)
        try:
            await manager.connect_all()
        except BaseException:
            await manager.cleanup_all()
            raise
        self._managers.append(manager)
        self._active = True
        if self.context is not None:
            self.context.skill_tool_scope = self
        return self

    async def __aexit__(self, *_args: Any) -> None:
        await self._cleanup()

    async def _cleanup(self) -> None:
        async with self._lock:
            self._active = False
            if self.subtasks is not None:
                await self.subtasks.drain()
            if self.context is not None and self.context.skill_tool_scope is self:
                self.context.skill_tool_scope = None
            managers, self._managers = list(reversed(self._managers)), []
            for manager in managers:
                with suppress(Exception):
                    await manager.cleanup_all()

    async def activate_skill(self, definition: dict[str, Any]) -> None:
        async with self._lock:
            if not self._active or self.provider is None or self.catalog is None or self.context is None:
                raise AgentToolConfigurationError("This execution has no active tool preparation")
            capability_id = str(definition.get("capability_id") or "")
            version = str(definition.get("version") or "")
            if not capability_id or not version or definition.get("status") != "available" or definition.get("execution_mode") != "inline":
                raise AgentToolConfigurationError("Only a verified inline Skill can join the active execution")
            if capability_id not in self.capabilities and len(self.capabilities) >= 128:
                raise AgentToolConfigurationError("The execution Skill catalog is full")
            capabilities = {**self.capabilities, capability_id: deepcopy(definition)}
            selected = self.selected_ids | {capability_id}
            items = list(capabilities.values())
            scripts, snapshots = _selected_skill_scripts(items, selected)
            if scripts and not any(tool.name == "execute_skill_script" for tool in self.tools if isinstance(tool, FunctionTool)):
                raise AgentToolConfigurationError("The script execution tool is unavailable in this execution")
            desired = await self.provider._mcp_servers(self.catalog, _selected_mcp_dependencies(items, selected), read_only=self.read_only)
            previous = {server._agent_tool_server.id: server for server in self.mcp_servers}
            next_servers, new_servers = [], []
            for server in desired:
                old = previous.get(server._agent_tool_server.id)
                if old is not None and old._agent_tool_server == server._agent_tool_server and old._agent_tool_descriptors == server._agent_tool_descriptors:
                    next_servers.append(old)
                else:
                    next_servers.append(server)
                    new_servers.append(server)
            manager = self._manager(new_servers)
            try:
                await manager.connect_all()
                for server in next_servers:
                    names = {tool.name for tool in await server.list_tools()}
                    if not set(server._agent_tool_descriptors).issubset(names):
                        raise AgentToolConfigurationError("A declared MCP tool is missing from its connected server")
            except BaseException:
                await manager.cleanup_all()
                raise
            # SDK Agent clones share this list. Existing in-flight tool objects keep
            # their old connection until the execution exits, including parallel calls.
            self._managers.append(manager)
            self.mcp_servers[:] = next_servers
            self.capabilities, self.selected_ids = capabilities, selected
            self.context.allowed_skill_scripts = scripts
            self.context.skill_script_snapshots = snapshots
            self.context.routed_capabilities.add(capability_id)
            self.context.capability_versions[capability_id] = version
            self.context.loaded_capabilities[capability_id] = deepcopy(definition)
            self.pending_installed_ids.discard(capability_id)


@dataclass(frozen=True)
class _MCPAuditRecord:
    provider: "AgentToolProvider"
    server_id: str
    tool_name: str
    sdk_tool_call_id: str
    agent_tool_call_id: str
    max_result_bytes: int
    project_id: str
    context: Any = None
    access: str = "read"
    tool_id: str = ""
    arguments_hash: str = ""


_mcp_audit_record: ContextVar[_MCPAuditRecord | None] = ContextVar(
    "content_agent_mcp_audit_record", default=None
)


class _AuditedMCPServerMixin:
    _agent_tool_provider: "AgentToolProvider"
    _agent_tool_server: MCPServerDefinition
    _agent_tool_descriptors: Mapping[str, ToolDescriptor]

    def _get_needs_approval_for_tool(self, tool: Any, _agent: Any) -> Any:
        async def needs_approval(
            run_context: Any, arguments: dict[str, Any], call_id: str
        ) -> bool:
            call = await self._agent_tool_provider.ensure_call(
                run_context,
                f"mcp:{self._agent_tool_server.id}/{tool.name}",
                arguments,
                call_id,
                configuration_hash=self._agent_tool_descriptors[tool.name].configuration_hash,
            )
            status = str(call.get("status") or "") if call is not None else "running"
            if status == "rejected":
                raise AgentToolApprovalRejected("Agent tool approval was rejected")
            return status == "pending_approval"

        return needs_approval

    async def call_tool(
        self,
        tool_name: str,
        arguments: dict[str, Any] | None,
        meta: dict[str, Any] | None = None,
    ) -> Any:
        audit = _mcp_audit_record.get()
        if audit is None or audit.server_id != self._agent_tool_server.id or audit.tool_name != tool_name:
            raise AgentToolConfigurationError(
                "MCP invocation reached the transport without an audited tool context"
            )
        async def unknown_outcome() -> Any:
            from .tool_outcomes import MCP_OUTCOME_UNKNOWN_CODE, mark_external_tool_review, unknown_mcp_result

            try:
                result = unknown_mcp_result(audit.agent_tool_call_id, audit.sdk_tool_call_id,
                                            tool_id=audit.tool_id, arguments_hash=audit.arguments_hash)
                await audit.provider.fail_call(audit.agent_tool_call_id, MCP_OUTCOME_UNKNOWN_CODE,
                                              "External operation was issued but its outcome requires user reconciliation", required=True)
            except Exception as exc:
                raise AgentToolConfigurationError("MCP outcome could not be durably recorded") from exc
            mark_external_tool_review(audit.context, audit.agent_tool_call_id)
            return result

        try:
            result = await super().call_tool(tool_name, arguments, meta)  # type: ignore[misc]
            payload = _json_safe(result)
            result_size = len(
                json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode(
                    "utf-8"
                )
            )
            if result_size > audit.max_result_bytes:
                raise AgentToolResultTooLarge(
                    f"MCP tool result exceeds {audit.max_result_bytes} bytes"
                )
            if _mcp_result_is_error(result):
                if audit.access != "read":
                    return await unknown_outcome()
                await audit.provider.fail_call(
                    audit.agent_tool_call_id,
                    "MCP_TOOL_ERROR",
                    "MCP tool returned an error result",
                )
            else:
                from .mcp_outputs import persist_mcp_outputs

                result, payload = await persist_mcp_outputs(self, result, audit)
                # Retry only the identical durable receipt, never the MCP action.
                for attempt in range(2 if audit.access != "read" else 1):
                    try:
                        await audit.provider.complete_call(audit.agent_tool_call_id, payload, result_size, required=audit.access != "read")
                        break
                    except Exception:
                        if attempt == 1:
                            raise
            return result
        except asyncio.CancelledError:
            await audit.provider.cancel_call(
                audit.agent_tool_call_id, "SDK MCP tool call cancelled"
            )
            raise
        except AgentToolConfigurationError:
            raise
        except Exception as exc:
            if audit.access != "read":
                return await unknown_outcome()
            await audit.provider.fail_call(
                audit.agent_tool_call_id,
                "MCP_TOOL_CALL_FAILED",
                f"MCP tool call failed ({type(exc).__name__})",
            )
            raise
        finally:
            _mcp_audit_record.set(None)


class AuditedMCPServerStreamableHttp(
    _AuditedMCPServerMixin, MCPServerStreamableHttp
):
    pass


class AuditedMCPServerSse(_AuditedMCPServerMixin, MCPServerSse):
    pass


class AuditedMCPServerStdio(_AuditedMCPServerMixin, MCPServerStdio):
    pass


class AgentToolProvider:
    def __init__(self, backend: Any, runtime_tools: Iterable[FunctionTool | CustomTool], *, hosted_model: Any = None, hosted_client: Any = None,
                 subtask_model: Callable[[], Any] | None = None, guardrail_policy: Any = None,
                 execution_model: Callable[[], Any] | None = None) -> None:
        self._backend = backend
        self._runtime_tools = {tool.name: tool for tool in runtime_tools}
        self._fallback_catalog = _fallback_catalog(self._runtime_tools.values())
        self._hosted_model = hosted_model
        self._hosted_client = hosted_client
        self._subtask_model = subtask_model
        self._guardrail_policy = guardrail_policy
        self._execution_model = execution_model

    @property
    def audit_enabled(self) -> bool:
        return callable(getattr(self._backend, "begin_agent_tool_call", None))

    def runtime_tools(self, names: Iterable[str] | None = None) -> list[FunctionTool | CustomTool]:
        selected = set(names) if names is not None else set(self._runtime_tools)
        return self._wrap_runtime_tools(self._fallback_catalog, selected)

    async def prepare(
        self,
        context: Any,
        capability_items: list[dict[str, Any]],
        selected_capability_ids: set[str],
        *,
        read_only: bool = False,
    ) -> PreparedAgentTools:
        catalog = await self._load_catalog()
        runtime_names = set(self._runtime_tools)
        if read_only:
            runtime_names = {name for name in runtime_names if (
                (descriptor := catalog.descriptors.get(f"runtime:{name}")) is not None
                and descriptor.access == "read" and descriptor.approval == "never"
            )}
        selected_scripts, script_snapshots = _selected_skill_scripts(capability_items, selected_capability_ids)
        setattr(context, "allowed_skill_scripts", selected_scripts)
        setattr(context, "skill_script_snapshots", script_snapshots)
        tools: list[Any] = self._wrap_runtime_tools(catalog, runtime_names)
        subtasks = None
        subtask_id = f"runtime:{SUBTASK_TOOL_NAME}"
        descriptor = catalog.descriptors.get(subtask_id)
        if self._subtask_model is not None and descriptor is not None and descriptor.enabled:
            if descriptor.access != "read" or descriptor.approval != "never" or self._guardrail_policy is None:
                raise AgentToolConfigurationError("Subtask execution requires a read-only descriptor and platform guardrails")
            subtasks = SubtaskScope()
            reads = [tool for tool in tools if isinstance(tool, FunctionTool) and tool.name in SUBTASK_READ_TOOLS
                     and (read := catalog.descriptors.get(f"runtime:{tool.name}")) is not None
                     and read.access == "read" and read.approval == "never"]
            delegated = build_subtask_tool(self._subtask_model(), reads, self._guardrail_policy, subtasks)
            tools.extend(self._wrap_runtime_tools(catalog, {subtask_id}, tools_by_id={subtask_id: delegated}))
        tools.extend(self._hosted_tools(catalog, context, read_only=read_only))
        tools = self._programmatic_tools(catalog, tools)
        if any(isinstance(tool, (FunctionTool, CustomTool)) and tool.defer_loading for tool in tools):
            tools.append(ToolSearchTool())
        selected_dependencies = _selected_mcp_dependencies(
            capability_items, selected_capability_ids
        )
        mcp_servers = await self._mcp_servers(catalog, selected_dependencies, read_only=read_only)
        return PreparedAgentTools(tools=tools, mcp_servers=mcp_servers, provider=self, context=context, catalog=catalog,
                                  capabilities={str(item["capability_id"]): deepcopy(item) for item in capability_items if item.get("capability_id") in selected_capability_ids},
                                  discoverable_capabilities={str(item["capability_id"]): deepcopy(item) for item in capability_items if item.get("capability_id") and item.get("status") == "available" and item.get("execution_mode") == "inline" and allows_implicit_skill_invocation(item)},
                                  selected_ids=set(selected_capability_ids), read_only=read_only, subtasks=subtasks)

    async def _load_catalog(self) -> AgentToolCatalog:
        loader = getattr(self._backend, "get_agent_tool_catalog", None)
        if not callable(loader):
            return self._fallback_catalog
        payload = await loader()
        if isinstance(payload, dict) and isinstance(payload.get("data"), dict):
            payload = payload["data"]
        if not isinstance(payload, dict):
            raise AgentToolConfigurationError(
                "backend Agent tool catalog is not a JSON object"
            )
        return AgentToolCatalog.parse(payload)

    def _programmatic_tools(self, catalog: AgentToolCatalog, tools: list[Any]) -> list[Any]:
        if not catalog.programmatic_tool_ids:
            return tools
        from agents.models.openai_responses import OpenAIResponsesModel

        model = self._execution_model() if self._execution_model is not None else None
        if not self.audit_enabled or not isinstance(model, OpenAIResponsesModel):
            raise AgentToolConfigurationError("Programmatic tools require durable audit and a Responses execution model")
        selected = set(catalog.programmatic_tool_ids)
        result: list[Any] = []
        eligible = False
        for tool in tools:
            if isinstance(tool, FunctionTool) and f"runtime:{tool.name}" in selected:
                # Clone only the already audited wrapper; never mutate the shared
                # tool or leak programmatic permissions into delegated agents.
                tool = _copy_function_tool(tool, allowed_callers=["direct", "programmatic"])
                eligible = True
            result.append(tool)
        if eligible:
            result.append(ProgrammaticToolCallingTool())
        return result

    def _wrap_runtime_tools(
        self, catalog: AgentToolCatalog, selected: set[str],
        *, tools_by_id: Mapping[str, FunctionTool | CustomTool] | None = None,
    ) -> list[FunctionTool | CustomTool]:
        result: list[FunctionTool | CustomTool] = []
        candidates = tools_by_id if tools_by_id is not None else {
            f"runtime:{name}": tool for name, tool in self._runtime_tools.items() if name in selected
        }
        for tool_id, tool in candidates.items():
            if tools_by_id is not None and tool_id not in selected:
                continue
            descriptor = catalog.descriptors.get(tool_id)
            if descriptor is None or not descriptor.enabled:
                continue
            if tool_id in {"runtime:prepare_workspace_publication", "runtime:publish_workspace_files", "runtime:prepare_agent_memory_publication", "runtime:publish_agent_memory"} and (
                    not self.audit_enabled or descriptor.max_retries != 0
                    or tool_id in {"runtime:publish_workspace_files", "runtime:publish_agent_memory"} and (descriptor.approval != "always" or descriptor.access != "write")):
                raise AgentToolConfigurationError("Native publication requires durable audit, explicit write approval and no automatic retries")
            if tool_id in {"runtime:exec_command", "runtime:write_stdin"} and (
                    not self.audit_enabled or descriptor.approval != "always"
                    or descriptor.access not in {"write", "sensitive"} or descriptor.max_retries != 0
                    or self._guardrail_policy is None):
                raise AgentToolConfigurationError("Native shell requires explicit durable approval, platform guardrails and no automatic retries")
            if isinstance(tool, CustomTool):
                if not isinstance(tool, SandboxApplyPatchTool):
                    raise AgentToolConfigurationError("Custom tool has no audited platform adapter")
                from .native_patch_tool import audited_native_patch_tool

                result.append(audited_native_patch_tool(self, tool, descriptor, self._guardrail_policy))
                continue
            unhandled_tool = set_function_tool_failure_error_function(_copy_function_tool(tool), None)

            async def script_enabled(run_context: Any, agent: Any, *, _tool=tool) -> bool:
                if not getattr(run_context.context, "allowed_skill_scripts", None):
                    return False
                enabled = _tool.is_enabled
                if isinstance(enabled, bool):
                    return enabled
                value = enabled(run_context, agent)
                return bool(await value if inspect.isawaitable(value) else value)

            async def needs_approval(
                run_context: Any,
                arguments: dict[str, Any],
                sdk_tool_call_id: str,
                *,
                _descriptor=descriptor,
            ) -> bool:
                if _descriptor.id in catalog.programmatic_tool_ids:
                    # SDK approval callbacks omit caller metadata. These grants
                    # are strictly approval=never; register at invocation, where
                    # ToolContext carries the validated program caller.
                    return False
                if _descriptor.id in {"runtime:exec_command", "runtime:write_stdin"} and self._guardrail_policy.tool_content_problem(arguments):
                    raise AgentToolConfigurationError("Native shell input blocked by platform data policy")
                call = await self.ensure_call(
                    run_context,
                    _descriptor.id,
                    arguments,
                    sdk_tool_call_id,
                    configuration_hash=_descriptor.configuration_hash,
                )
                if call is None:
                    return _descriptor.approval == "always"
                status = str(call.get("status") or "")
                if status == "rejected":
                    raise AgentToolApprovalRejected(
                        "Agent tool approval was rejected"
                    )
                return status == "pending_approval"

            async def invoke(tool_context: Any, input_json: str, *, _tool=tool, _unhandled_tool=unhandled_tool, _descriptor=descriptor) -> Any:
                arguments = json.loads(input_json) if input_json else {}
                if not isinstance(arguments, dict):
                    raise ValueError("Function tool arguments must be a JSON object")
                if _descriptor.id in {"runtime:exec_command", "runtime:write_stdin"} and self._guardrail_policy.tool_content_problem(arguments):
                    raise AgentToolConfigurationError("Native shell input blocked by platform data policy")
                call = await self.ensure_call(
                    tool_context,
                    _descriptor.id,
                    arguments,
                    str(getattr(tool_context, "tool_call_id", "")),
                    configuration_hash=_descriptor.configuration_hash,
                )
                programmatic_grant = _descriptor.id in catalog.programmatic_tool_ids
                call_id = str(call.get("agent_tool_call_id") or "") if call else ""
                if programmatic_grant and not call_id:
                    raise AgentToolConfigurationError("Programmatic tool registration receipt is unconfirmed")
                if call is not None and call.get("status") == "pending_approval":
                    raise AgentToolApprovalRequired("Agent tool approval is required")
                if call_id:
                    started = await self.start_call(
                        call_id, str(getattr(tool_context, "tool_call_id", "")), _descriptor.configuration_hash
                    )
                    if (programmatic_grant or _descriptor.id in {"runtime:exec_command", "runtime:write_stdin", "runtime:publish_workspace_files", "runtime:publish_agent_memory"}) and (
                            not isinstance(started, dict) or started.get("agent_tool_call_id") != call_id or started.get("status") != "running"):
                        raise AgentToolConfigurationError("Native write start receipt is unconfirmed")
                failure_recorded = False
                try:
                    try:
                        if _descriptor.id in {"runtime:exec_command", "runtime:write_stdin"}:
                            from .native_execution import native_shell_call_scope

                            with native_shell_call_scope(call_id, str(getattr(tool_context, "tool_call_id", "")), input_json):
                                output = await _unhandled_tool.on_invoke_tool(tool_context, input_json)
                        else:
                            output = await _unhandled_tool.on_invoke_tool(tool_context, input_json)
                    except Exception as tool_error:
                        if call_id:
                            from .backend import BackendError

                            commit_deferred = (_descriptor.id == "runtime:commit_agent_action" and isinstance(tool_error, BackendError)
                                               and tool_error.status_code == 409 and tool_error.code in {"SDK_TOOL_OUTCOME_UNRESOLVED", "AGENT_TOOL_APPROVAL_REQUIRED"})
                            await self.fail_call(call_id, "RUNTIME_COMMIT_DEFERRED" if commit_deferred else "FUNCTION_TOOL_CALL_FAILED",
                                                 f"Function tool call failed ({type(tool_error).__name__})", required=commit_deferred or _descriptor.id in {"runtime:exec_command", "runtime:write_stdin", "runtime:publish_workspace_files", "runtime:publish_agent_memory"})
                            failure_recorded = True
                        # Keep SDK recoverable tool errors, but record their real
                        # outcome before the SDK converts them to model-facing text.
                        handled = await maybe_invoke_function_tool_failure_error_function(
                            function_tool=_tool, context=tool_context, error=tool_error,
                        )
                        if handled is None:
                            raise
                        return handled
                    payload = _json_safe(output)
                    result_size = len(
                        json.dumps(
                            payload, ensure_ascii=False, separators=(",", ":")
                        ).encode("utf-8")
                    )
                    if result_size > _descriptor.max_result_bytes:
                        raise AgentToolResultTooLarge(
                            f"Function tool result exceeds {_descriptor.max_result_bytes} bytes"
                        )
                    if call_id:
                        await self.complete_call(call_id, _tool_output_audit(output), result_size,
                                                 required=programmatic_grant or _descriptor.id in {f"runtime:{SUBTASK_TOOL_NAME}", "runtime:exec_command", "runtime:write_stdin", "runtime:publish_workspace_files", "runtime:publish_agent_memory"},
                                                 confirm_receipt=programmatic_grant)
                    return output
                except asyncio.CancelledError:
                    if call_id:
                        await self.cancel_call(call_id, "SDK function tool cancelled")
                    raise
                except Exception as exc:
                    if call_id and not failure_recorded:
                        await self.fail_call(
                            call_id,
                            "FUNCTION_TOOL_CALL_FAILED",
                            f"Function tool call failed ({type(exc).__name__})",
                        )
                    raise

            result.append(
                _copy_function_tool(
                    tool,
                    on_invoke_tool=invoke,
                    needs_approval=needs_approval,
                    timeout_seconds=float(descriptor.timeout_seconds),
                    defer_loading=descriptor.defer_loading,
                    is_enabled=script_enabled if tool_id == "runtime:execute_skill_script" else tool.is_enabled,
                )
            )
        return result

    async def _mcp_servers(
        self,
        catalog: AgentToolCatalog,
        selected_dependencies: set[str],
        *,
        read_only: bool = False,
    ) -> list[Any]:
        selected_by_server: dict[str, set[str]] = {}
        for reference in selected_dependencies:
            server_id, separator, tool_name = reference.partition("/")
            if not separator:
                raise AgentToolConfigurationError(
                    f"invalid MCP dependency reference {reference!r}"
                )
            selected_by_server.setdefault(server_id, set()).add(tool_name)

        result: list[Any] = []
        for server in catalog.mcp_servers.values():
            if not server.enabled:
                if selected_by_server.get(server.id):
                    raise AgentToolConfigurationError("A selected Skill MCP server is disabled")
                continue
            selected_names = selected_by_server.get(server.id, set())
            if server.defer_loading and not selected_names:
                continue
            allowed_names = selected_names or set(server.allowed_tools)
            if read_only:
                readable = {name for name in server.allowed_tools if (
                    catalog.descriptors[f"mcp:{server.id}/{name}"].access == "read"
                    and catalog.descriptors[f"mcp:{server.id}/{name}"].approval == "never"
                )}
                if selected_names.difference(readable):
                    raise AgentToolConfigurationError("background Skills cannot execute approval-gated or mutating MCP dependencies")
                allowed_names = allowed_names.intersection(readable)
                if not allowed_names:
                    continue
            if not allowed_names.issubset(set(server.allowed_tools)):
                missing = sorted(allowed_names.difference(server.allowed_tools))
                raise AgentToolConfigurationError(
                    f"MCP dependency is not allowlisted on {server.id}: {missing}"
                )
            descriptors = [
                catalog.descriptors[f"mcp:{server.id}/{name}"]
                for name in sorted(allowed_names)
            ]
            if any(not item.enabled for item in descriptors):
                raise AgentToolConfigurationError("A selected MCP tool is disabled")
            if any(item.approval == "always" for item in descriptors) and server.max_retries:
                raise AgentToolConfigurationError(
                    f"MCP server {server.id!r} cannot retry approval-gated writes"
                )
            credentials = await self._mcp_credentials(server)
            result.append(self._create_mcp_server(server, descriptors, credentials))

        unresolved = set(selected_by_server).difference(catalog.mcp_servers)
        if unresolved:
            raise AgentToolConfigurationError(
                f"MCP dependency servers are missing: {sorted(unresolved)}"
            )
        return result

    async def _mcp_credentials(self, server: MCPServerDefinition) -> dict[str, str]:
        fields = set(server.credential_environment.values()) | set(server.credential_headers.values())
        if not fields:
            return {}
        resolver = getattr(self._backend, "resolve_mcp_credentials", None)
        if not callable(resolver) or not server.credential_binding or not self.audit_enabled:
            raise AgentToolConfigurationError("MCP credentials require an authenticated execution binding")
        response = await resolver(server.id, server.credential_binding)
        if not isinstance(response, dict) or response.get("server_id") != server.id or response.get("credential_binding") != server.credential_binding:
            raise AgentToolConfigurationError("MCP credential receipt does not match the connection")
        values = response.get("values")
        if not isinstance(values, dict) or set(values) != fields or any(
            not isinstance(value, str) or not value.strip() or len(value.encode("utf-8")) > 4096
            or any(ord(char) < 32 or 127 <= ord(char) <= 159 for char in value)
            for value in values.values()
        ):
            raise AgentToolConfigurationError("MCP credential fields are invalid")
        return values

    def _create_mcp_server(
        self,
        server: MCPServerDefinition,
        descriptors: list[ToolDescriptor],
        credentials: Mapping[str, str],
    ) -> Any:
        allowed_names = [descriptor.name for descriptor in descriptors]
        tool_filter = create_static_tool_filter(allowed_tool_names=allowed_names)

        async def resolve_audited_meta(meta_context: Any) -> None:
            run_context = meta_context.run_context
            sdk_call_id = str(getattr(run_context, "tool_call_id", ""))
            if not sdk_call_id:
                raise AgentToolConfigurationError("SDK MCP ToolContext has no tool_call_id")
            arguments = dict(meta_context.arguments or {})
            tool_id = f"mcp:{server.id}/{meta_context.tool_name}"
            descriptor = next(item for item in descriptors if item.name == meta_context.tool_name)
            call = await self.ensure_call(
                run_context, tool_id, arguments, sdk_call_id, configuration_hash=descriptor.configuration_hash
            )
            if call is None:
                return None
            call_id = str(call.get("agent_tool_call_id") or "")
            arguments_hash = str(call.get("arguments_hash") or "")
            if descriptor.access != "read":
                from .tool_outcomes import unknown_mcp_result

                # Validate the authoritative binding before issuing a remote write.
                unknown_mcp_result(call_id, sdk_call_id, tool_id=tool_id, arguments_hash=arguments_hash)
            started = await self.start_call(call_id, sdk_call_id, descriptor.configuration_hash)
            _mcp_audit_record.set(
                _MCPAuditRecord(
                    provider=self,
                    server_id=server.id,
                    tool_name=meta_context.tool_name,
                    sdk_tool_call_id=sdk_call_id,
                    agent_tool_call_id=str(started.get("agent_tool_call_id") or call_id),
                    max_result_bytes=descriptor.max_result_bytes,
                    project_id=str(run_context.context.project_id),
                    context=run_context.context,
                    access=descriptor.access,
                    tool_id=tool_id,
                    arguments_hash=arguments_hash,
                )
            )
            return None

        async def resolve_meta(meta_context: Any) -> None:
            try:
                return await resolve_audited_meta(meta_context)
            except Exception as exc:
                raise AgentToolConfigurationError("MCP audit binding could not be established") from exc

        common = {
            "cache_tools_list": True,
            "name": server.id,
            "client_session_timeout_seconds": float(server.timeout_seconds),
            "tool_filter": tool_filter,
            "max_retry_attempts": server.max_retries,
            "require_approval": False,
            "tool_meta_resolver": resolve_meta,
            "failure_error_function": _mcp_failure_error,
        }
        if server.transport in {"streamable_http", "sse"}:
            headers = _resolve_environment_map(server.header_environment)
            headers.update({target: credentials[field] for target, field in server.credential_headers.items()})
            params: dict[str, Any] = {
                "url": server.url,
                "headers": headers,
                "timeout": float(server.timeout_seconds),
                "sse_read_timeout": float(server.timeout_seconds),
            }
            cls: Any = (
                AuditedMCPServerStreamableHttp
                if server.transport == "streamable_http"
                else AuditedMCPServerSse
            )
        else:
            params = {
                "command": server.command,
                "args": list(server.args),
                "env": {**_resolve_environment_map(server.environment),
                        **{target: credentials[field] for target, field in server.credential_environment.items()}},
                "cwd": server.cwd,
                "encoding": "utf-8",
                "encoding_error_handler": "strict",
            }
            cls = AuditedMCPServerStdio
        instance = cls(params, **common)
        instance._agent_tool_provider = self
        instance._agent_tool_server = server
        instance._agent_tool_descriptors = {item.name: item for item in descriptors}
        return instance

    def _hosted_tools(self, catalog: AgentToolCatalog, context: Any, *, read_only: bool = False) -> list[Any]:
        from .hosted_tools import build_hosted_agent_tool

        result: dict[str, FunctionTool] = {}
        for definition in catalog.hosted_tools:
            descriptor = catalog.descriptors[f"hosted:{definition.id}"]
            if not definition.enabled or not descriptor.enabled:
                continue
            if read_only and (descriptor.approval != "never" or descriptor.access != "read"):
                continue
            if self._hosted_model is None:
                raise AgentToolConfigurationError("Hosted tools require the configured Responses model")
            try:
                result[descriptor.id] = build_hosted_agent_tool(
                    definition, descriptor, context, self._hosted_model,
                    self._hosted_client, self._backend,
                )
            except ValueError as exc:
                raise AgentToolConfigurationError(str(exc)) from exc
        return self._wrap_runtime_tools(catalog, set(result), tools_by_id=result)

    async def ensure_call(
        self,
        run_context: Any,
        tool_id: str,
        arguments: dict[str, Any],
        sdk_tool_call_id: str,
        *, configuration_hash: str = "", refresh: bool = False,
    ) -> dict[str, Any] | None:
        if not self.audit_enabled:
            if refresh:
                raise AgentToolConfigurationError("Approval refresh requires durable audit")
            return None
        if not sdk_tool_call_id:
            raise AgentToolConfigurationError("SDK ToolContext has no tool_call_id")
        context = getattr(run_context, "context", None)
        if context is None:
            raise AgentToolConfigurationError("SDK ToolContext has no AgentContext")
        caller = getattr(getattr(run_context, "tool_call", None), "caller", None)
        program_call_id = ""
        if caller is not None:
            caller = caller if isinstance(caller, dict) else caller.model_dump()
            if caller.get("type") == "program":
                program_call_id = caller.get("caller_id")
                if (not isinstance(program_call_id, str) or not program_call_id
                        or len(program_call_id.encode("utf-8")) > 256
                        or program_call_id.strip() != program_call_id
                        or any(ord(char) < 32 or 127 <= ord(char) <= 159 for char in program_call_id)):
                    raise AgentToolConfigurationError("Invalid program call identity")
            elif caller.get("type") != "direct":
                raise AgentToolConfigurationError("Unsupported SDK tool caller")
        memory_id = getattr(context, "memory_generation_id", "")
        memory_attempt = getattr(context, "memory_generation_attempt", 0)
        memory_binding = []
        if memory_id or memory_attempt:
            allowed_memory_tools = {"runtime:exec_command", "runtime:apply_patch", "runtime:write_stdin"}
            if getattr(context, "native_generation_source", None) is not None:
                allowed_memory_tools.update({"runtime:prepare_agent_memory_publication", "runtime:publish_agent_memory"})
            if (not isinstance(memory_id, str) or not memory_id or type(memory_attempt) is not int or memory_attempt < 1
                    or any(getattr(context, field, "") for field in ("agent_turn_id", "agent_task_attempt_id", "execution_attempt_id", "skill_invocation_id"))
                    or not getattr(context, "attempt_token", "")
                    or tool_id not in allowed_memory_tools):
                raise AgentToolConfigurationError("Memory tool requires its independent native execution")
            memory_binding = [context.project_id, context.conversation_id, memory_id, memory_attempt,
                              hashlib.sha256(context.attempt_token.encode()).hexdigest()]
        request_binding = hashlib.sha256(json.dumps(
            [tool_id, arguments, *memory_binding, *([program_call_id] if program_call_id else [])], ensure_ascii=False, sort_keys=True,
            separators=(",", ":"), allow_nan=False,
        ).encode("utf-8")).hexdigest()
        active = getattr(context, "active_tool_calls", None)
        previous = active.get(sdk_tool_call_id) if isinstance(active, dict) else None
        if refresh and (not isinstance(previous, dict) or not previous.get("agent_tool_call_id")):
            raise AgentToolConfigurationError("Approval refresh requires a registered tool call")
        if isinstance(active, dict) and sdk_tool_call_id in active:
            if active[sdk_tool_call_id].get("_configuration_hash", "") != configuration_hash:
                raise AgentToolConfigurationError("tool configuration changed after approval registration")
            if tool_id == "runtime:execute_skill_script":
                registered = active[sdk_tool_call_id].get("_skill_script_arguments")
                if registered is not None and registered != arguments:
                    raise AgentToolConfigurationError("Skill script arguments changed after approval registration")
            binding = active[sdk_tool_call_id].get("_request_binding")
            if binding is not None:
                if binding != request_binding:
                    raise AgentToolConfigurationError("tool or arguments changed after approval registration")
                if not refresh:
                    return active[sdk_tool_call_id]
            # Legacy caches have no local binding. Re-read the durable receipt;
            # the backend rejects reuse of the ID with different tool arguments.
        snapshot_kwargs: dict[str, Any] = {}
        if program_call_id:
            snapshot_kwargs["program_call_id"] = program_call_id
        if memory_id:
            snapshot_kwargs.update(memory_generation_id=memory_id, memory_generation_attempt=memory_attempt)
        execution_attempt_id = str(getattr(context, "execution_attempt_id", ""))
        if execution_attempt_id:
            snapshot_kwargs["execution_attempt_id"] = execution_attempt_id
        if configuration_hash:
            snapshot_kwargs["configuration_hash"] = configuration_hash
        if tool_id == "runtime:execute_skill_script":
            snapshot_kwargs["skill_snapshot"] = selected_skill_script_snapshot(context, arguments)
        call = await self._backend.begin_agent_tool_call(
            project_id=str(context.project_id),
            conversation_id=str(context.conversation_id),
            agent_turn_id=str(getattr(context, "agent_turn_id", "")),
            sdk_tool_call_id=sdk_tool_call_id,
            tool_id=tool_id,
            arguments=arguments,
            skill_invocation_id=str(getattr(context, "skill_invocation_id", "")),
            agent_task_attempt_id=str(getattr(context, "agent_task_attempt_id", "")),
            attempt_token=str(getattr(context, "attempt_token", "")),
            **snapshot_kwargs,
        )
        if not isinstance(call, dict):
            raise AgentToolConfigurationError("backend returned an invalid Agent tool call")
        if refresh and (call.get("agent_tool_call_id") != previous["agent_tool_call_id"]
                        or call.get("sdk_tool_call_id") != sdk_tool_call_id or call.get("tool_id") != tool_id
                        or call.get("arguments_hash") != previous.get("arguments_hash")):
            raise AgentToolConfigurationError("Approval refresh returned a different tool call binding")
        call["_configuration_hash"] = configuration_hash
        call["_request_binding"] = request_binding
        if tool_id == "runtime:execute_skill_script":
            # Catalog activation may change after registration; the backend owns
            # the immutable pin and execution must keep these exact arguments.
            call["_skill_script_arguments"] = deepcopy(arguments)
        if isinstance(active, dict):
            active[sdk_tool_call_id] = call
        return call

    async def start_call(
        self, agent_tool_call_id: str, sdk_tool_call_id: str, configuration_hash: str = ""
    ) -> dict[str, Any]:
        if not self.audit_enabled:
            return {"agent_tool_call_id": agent_tool_call_id, "status": "running"}
        return await self._backend.start_agent_tool_call(
            agent_tool_call_id, sdk_tool_call_id,
            **({"configuration_hash": configuration_hash} if configuration_hash else {}),
        )

    async def complete_call(
        self, agent_tool_call_id: str, result: Any, result_size_bytes: int, *, required: bool = False,
        confirm_receipt: bool = False,
    ) -> None:
        if not self.audit_enabled:
            return
        try:
            receipt = await self._backend.complete_agent_tool_call(
                agent_tool_call_id,
                result=result,
                result_size_bytes=result_size_bytes,
            )
            if confirm_receipt and (not isinstance(receipt, dict)
                                    or receipt.get("agent_tool_call_id") != agent_tool_call_id
                                    or receipt.get("status") != "completed"):
                raise AgentToolConfigurationError("Programmatic tool completion receipt is unconfirmed")
        except Exception:
            if required or confirm_receipt:
                raise
            # The external action has already completed. Do not raise into the SDK,
            # because an MCP retry could repeat a non-idempotent write.
            logger.exception("failed to persist completed Agent tool call")

    async def fail_call(
        self, agent_tool_call_id: str, error_code: str, error_message: str, *, required: bool = False
    ) -> None:
        if not self.audit_enabled:
            return
        try:
            await self._backend.fail_agent_tool_call(
                agent_tool_call_id,
                error_code=error_code,
                error_message=error_message,
            )
        except Exception:
            if required:
                raise

    async def cancel_call(self, agent_tool_call_id: str, reason: str) -> None:
        if not self.audit_enabled:
            return
        with suppress(Exception):
            await self._backend.cancel_agent_tool_call(agent_tool_call_id, reason)


def _selected_mcp_dependencies(
    capability_items: list[dict[str, Any]], selected_ids: set[str]
) -> set[str]:
    result: set[str] = set()
    for item in capability_items:
        if item.get("capability_id") not in selected_ids:
            continue
        skill = item.get("skill")
        if not isinstance(skill, dict):
            continue
        dependencies = skill.get("dependencies")
        if not isinstance(dependencies, list):
            continue
        for dependency in dependencies:
            if not isinstance(dependency, dict) or dependency.get("type") != "mcp":
                continue
            if dependency.get("status") not in {None, "available"}:
                raise AgentToolConfigurationError(
                    str(dependency.get("user_message") or "Skill MCP dependency is unavailable")
                )
            value = str(dependency.get("value") or "").strip()
            if value:
                result.add(value)
    return result


def _selected_skill_scripts(
    capability_items: list[dict[str, Any]], selected_ids: set[str]
) -> tuple[set[tuple[str, str]], dict[str, dict[str, str]]]:
    result: set[tuple[str, str]] = set()
    snapshots: dict[str, dict[str, str]] = {}
    for item in capability_items:
        capability_id = str(item.get("capability_id") or "")
        if capability_id not in selected_ids:
            continue
        skill = item.get("skill")
        if not isinstance(skill, dict):
            continue
        skill_name = str(skill.get("name") or "").strip()
        scripts = skill.get("scripts")
        if not skill_name or not isinstance(scripts, list):
            continue
        snapshots[capability_id] = {
            "capability_id": capability_id,
            "skill_name": skill_name,
            "version": str(item.get("version") or ""),
            "content_hash": str(skill.get("content_hash") or ""),
        }
        for script in scripts:
            if not isinstance(script, dict):
                continue
            script_id = str(script.get("id") or "").strip()
            if script_id:
                result.add((capability_id, script_id))
    return result, snapshots


def selected_skill_script_snapshot(context: Any, arguments: dict[str, Any]) -> dict[str, str]:
    skill_name = str(arguments.get("skill_name") or "").strip()
    script_id = str(arguments.get("script_id") or "").strip()
    capability_id = str(arguments.get("capability_id") or "").strip()
    allowed = getattr(context, "allowed_skill_scripts", set())
    matches = [snapshot for selected_id, snapshot in getattr(context, "skill_script_snapshots", {}).items()
               if (not capability_id or capability_id == selected_id)
               and snapshot.get("skill_name") == skill_name and (selected_id, script_id) in allowed]
    if len(matches) > 1:
        raise AgentToolConfigurationError("Skill script name is ambiguous; supply the exact capability_id from the selected Skill metadata")
    if not matches or not all(matches[0].get(key) for key in ("capability_id", "version", "content_hash")):
        raise AgentToolConfigurationError("selected Skill script has no verified version snapshot")
    return {key: matches[0][key] for key in ("capability_id", "version", "content_hash")}


def _fallback_catalog(runtime_tools: Iterable[FunctionTool | CustomTool]) -> AgentToolCatalog:
    descriptors = {
        f"runtime:{tool.name}": ToolDescriptor(
            id=f"runtime:{tool.name}",
            kind="runtime_function",
            name=tool.name,
            description=tool.description,
            access="write"
            if tool.name in {"set_episode_execution_mode", "commit_agent_action", "apply_workspace_patch", "install_workspace_skill", "control_execution", "update_saved_instructions", "publish_workspace_files", "publish_agent_memory"}
            else "read",
            approval="always" if tool.name in {"install_workspace_skill", "control_execution", "update_saved_instructions", "publish_workspace_files", "publish_agent_memory"} else "never",
            enabled=not isinstance(tool, CustomTool) and tool.name not in {"exec_command", "write_stdin", "prepare_workspace_publication", "publish_workspace_files", "prepare_agent_memory_publication", "publish_agent_memory"},
            timeout_seconds=60,
            max_result_bytes=16 * 1024 * 1024 if tool.name in {"read_skill_resource", "view_image"} else 256 * 1024,
        )
        for tool in runtime_tools
    }
    return AgentToolCatalog(descriptors=descriptors, mcp_servers={}, hosted_tools=())


def _required_string(source: Mapping[str, Any], key: str) -> str:
    value = str(source.get(key) or "").strip()
    if not value:
        raise AgentToolConfigurationError(f"{key} is required")
    return value


def _bounded_int(
    source: Mapping[str, Any], key: str, minimum: int, maximum: int
) -> int:
    try:
        value = int(source.get(key))
    except (TypeError, ValueError) as exc:
        raise AgentToolConfigurationError(f"{key} must be an integer") from exc
    if value < minimum or value > maximum:
        raise AgentToolConfigurationError(
            f"{key} must be between {minimum} and {maximum}"
        )
    return value


def _string_map(value: Any) -> Mapping[str, str]:
    if value is None:
        return {}
    if not isinstance(value, dict):
        raise AgentToolConfigurationError("environment mapping must be an object")
    result: dict[str, str] = {}
    for key, source_name in value.items():
        normalized_key = str(key).strip()
        normalized_source = str(source_name).strip()
        if not normalized_key or not normalized_source:
            raise AgentToolConfigurationError("environment mapping contains an empty entry")
        result[normalized_key] = normalized_source
    return result


def _resolve_environment_map(mapping: Mapping[str, str]) -> dict[str, str]:
    result: dict[str, str] = {}
    for target_name, source_name in mapping.items():
        value = os.getenv(source_name)
        if value is None:
            raise AgentToolConfigurationError(
                f"required MCP environment variable {source_name!r} is missing"
            )
        result[target_name] = value
    return result


def _json_safe(value: Any) -> Any:
    if hasattr(value, "model_dump"):
        return _json_safe(value.model_dump(mode="json", by_alias=True))
    if isinstance(value, (list, tuple)):
        return [_json_safe(item) for item in value]
    if isinstance(value, dict):
        return {str(key): _json_safe(item) for key, item in value.items()}
    try:
        json.dumps(value, ensure_ascii=False)
        return value
    except (TypeError, ValueError):
        return str(value)


def _tool_output_audit(value: Any) -> Any:
    if isinstance(value, (ToolOutputImage, ToolOutputFileContent)):
        result = value.model_dump(mode="json", exclude_none=True)
        for key in ("image_url", "file_data"):
            raw = result.get(key)
            if isinstance(raw, str) and raw.startswith("data:"):
                result[key] = {"omitted": "binary_resource", "encoded_bytes": len(raw), "sha256": hashlib.sha256(raw.encode()).hexdigest()}
        return result
    if isinstance(value, (list, tuple)):
        return [_tool_output_audit(item) for item in value]
    if isinstance(value, dict):
        return {str(key): _tool_output_audit(item) for key, item in value.items()}
    return _json_safe(value)


def _mcp_result_is_error(result: Any) -> bool:
    for name in ("isError", "is_error"):
        value = getattr(result, name, None)
        if isinstance(value, bool):
            return value
    return False
