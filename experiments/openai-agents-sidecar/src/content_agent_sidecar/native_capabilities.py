from __future__ import annotations

from dataclasses import dataclass, fields, replace
from typing import Any

from agents import Agent, FunctionTool, RunConfig, ToolSearchTool
from agents.run_config import SandboxRunConfig
from agents.sandbox import SandboxAgent
from agents.sandbox.capabilities import Filesystem, Memory, Shell, Skills
from agents.sandbox.config import MemoryLayoutConfig, MemoryReadConfig
from agents.sandbox.entries import Dir
from agents.sandbox.manifest import Manifest
from pydantic import Field

from .agent_tools import AgentToolConfigurationError, PreparedAgentTools
from .native_session import RuntimeSandboxClient, RuntimeSandboxOptions
from .native_snapshot import RuntimeWorkspaceSnapshotSpec
from .native_manifest import MEMORY_DIRECTORY, RuntimeResourceFile


class _RuntimeFilesystem(Filesystem):
    allowed_names: frozenset[str] = Field(exclude=True)

    def tools(self):
        return [tool for tool in super().tools() if tool.name in self.allowed_names]


class _RuntimeShell(Shell):
    allowed_names: frozenset[str] = Field(exclude=True)

    def tools(self):
        return [tool for tool in super().tools() if tool.name in self.allowed_names]


def _allowed_native_names(prepared: PreparedAgentTools) -> frozenset[str]:
    names = set()
    for name in ("view_image", "apply_patch", "exec_command", "write_stdin"):
        descriptor = prepared.catalog.descriptors.get("runtime:"+name)
        if descriptor is None or not descriptor.enabled:
            continue
        if prepared.read_only and (descriptor.access != "read" or descriptor.approval != "never"):
            continue
        if descriptor.kind != "runtime_function" or descriptor.name != name:
            raise AgentToolConfigurationError("Native tools require their exact Runtime descriptor")
        if name == "view_image" and (descriptor.access != "read" or descriptor.approval != "never"):
            raise AgentToolConfigurationError("Native image reads require the read-only Runtime descriptor")
        if name != "view_image" and (descriptor.access not in {"write", "sensitive"} or descriptor.approval != "always" or descriptor.max_retries != 0):
            raise AgentToolConfigurationError("Native workspace mutations require explicit approval and no automatic retries")
        names.add(name)
    return frozenset(names)


def _wrap_bound_tool(prepared: PreparedAgentTools, tool: Any):
    tool_id = "runtime:"+tool.name
    if tool.name == "exec_command":
        invoke = tool.on_invoke_tool
        needs_approval = tool.needs_approval

        async def validate_approval(context, arguments, call_id):
            # The SDK's non-PTY fallback otherwise silently ignores tty=true.
            # Do not advertise an interactive execution as a successful one-shot.
            if tool.args_model.model_validate(arguments).tty and not tool.session.supports_pty():
                raise AgentToolConfigurationError("Interactive native execution is unavailable on this provider")
            if callable(needs_approval):
                return await needs_approval(context, arguments, call_id)
            return needs_approval

        async def checked_invoke(context, raw):
            args = tool.args_model.model_validate_json(raw)
            if args.tty and not tool.session.supports_pty():
                raise AgentToolConfigurationError("Interactive native execution is unavailable on this provider")
            return await invoke(context, raw)

        tool.on_invoke_tool = checked_invoke
        # Approval is replaced by the Runtime wrapper below; provider preflight
        # must also run before that wrapper registers a durable user decision.
        original = prepared.provider._wrap_runtime_tools(prepared.catalog, {tool_id}, tools_by_id={tool_id: tool})
        if len(original) != 1:
            raise AgentToolConfigurationError("Native tool was not wrapped with its current policy")
        wrapped = original[0]
        approval = wrapped.needs_approval

        async def checked_approval(context, arguments, call_id):
            await validate_approval(context, arguments, call_id)
            return await approval(context, arguments, call_id)

        wrapped.needs_approval = checked_approval
    else:
        result = prepared.provider._wrap_runtime_tools(prepared.catalog, {tool_id}, tools_by_id={tool_id: tool})
        if len(result) != 1:
            raise AgentToolConfigurationError("Native tool was not wrapped with its current policy")
        wrapped = result[0]
    return prepared.provider._guardrail_policy.protect_tool(wrapped) if isinstance(wrapped, FunctionTool) else wrapped


@dataclass(frozen=True)
class NativeWorkspaceBinding:
    client: RuntimeSandboxClient
    manifest: Manifest
    capabilities: tuple[Any, ...]
    deferred_tools: bool

    def bind_agent(self, agent: Agent) -> SandboxAgent:
        if type(agent) is not Agent:
            raise AgentToolConfigurationError("Native workspace assembly requires an unbound SDK Agent")
        native_names = {"view_image", "apply_patch", "exec_command", "write_stdin"}
        if any(getattr(tool, "name", "") in native_names for tool in agent.tools):
            raise AgentToolConfigurationError("Native tools must be supplied after sandbox binding, not twice")
        arguments = {field.name: getattr(agent, field.name) for field in fields(Agent) if field.init}
        arguments["tools"] = list(agent.tools)
        if self.deferred_tools and not any(isinstance(tool, ToolSearchTool) for tool in agent.tools):
            arguments["tools"].append(ToolSearchTool())
        # Existing Session compaction stays with the caller. Adding the default
        # sandbox Compaction here would create a second context-compaction owner.
        return SandboxAgent(**arguments, default_manifest=self.manifest.model_copy(deep=True), capabilities=self.capabilities)

    def run_config(self, config: RunConfig, *, session_state=None) -> RunConfig:
        if config.sandbox is not None:
            raise AgentToolConfigurationError("Native workspace assembly cannot replace an existing sandbox owner")
        return replace(config, sandbox=SandboxRunConfig(client=self.client, options=RuntimeSandboxOptions(),
                       snapshot=RuntimeWorkspaceSnapshotSpec(), manifest=self.manifest.model_copy(deep=True),
                       session_state=session_state))


async def prepare_native_workspace(prepared: PreparedAgentTools, client: RuntimeSandboxClient) -> NativeWorkspaceBinding:
    """Attach audited SDK capabilities without changing the caller's Agent loop."""
    if prepared.provider is None or prepared.catalog is None or not prepared.provider.audit_enabled or prepared.provider._guardrail_policy is None:
        raise AgentToolConfigurationError("Native workspace tools require Runtime auditing and platform data policy")
    names = _allowed_native_names(prepared)

    def configure_filesystem(toolset):
        for name in ("view_image", "apply_patch"):
            if name in names:
                setattr(toolset, name, _wrap_bound_tool(prepared, getattr(toolset, name)))

    def configure_shell(toolset):
        for name in ("exec_command", "write_stdin"):
            if name in names and getattr(toolset, name) is not None:
                setattr(toolset, name, _wrap_bound_tool(prepared, getattr(toolset, name)))

    capabilities = []
    if names.intersection({"view_image", "apply_patch"}):
        capabilities.append(_RuntimeFilesystem(allowed_names=names, configure_tools=configure_filesystem))
    if "exec_command" in names:
        capabilities.append(_RuntimeShell(allowed_names=names, configure_tools=configure_shell))
    manifest = await client.prepare_manifest()
    memory_files = [entry.resource for _, entry in manifest.iter_entries()
                    if type(entry) is RuntimeResourceFile and entry.resource.memory is not None]
    if memory_files:
        if not any(file.path == f"{MEMORY_DIRECTORY}/memory_summary.md" for file in memory_files):
            raise AgentToolConfigurationError("Native memory requires its versioned summary")
        if "exec_command" not in names:
            # Satisfy SDK read dependency without granting tools excluded by policy.
            capabilities.append(_RuntimeShell(allowed_names=frozenset(), configure_tools=configure_shell))
        capabilities.append(Memory(layout=MemoryLayoutConfig(memories_dir=MEMORY_DIRECTORY),
                                   read=MemoryReadConfig(live_update=False), generate=None))
    if ".skills" in manifest.entries:
        if type(manifest.entries[".skills"]) is not Dir:
            raise AgentToolConfigurationError("Installed Skill resources require their directory namespace")
        capabilities.append(Skills(from_=Dir(), skills_path=".skills"))
    deferred = any(prepared.catalog.descriptors["runtime:"+name].defer_loading for name in names)
    return NativeWorkspaceBinding(client, manifest, tuple(capabilities), deferred)
