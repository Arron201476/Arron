from __future__ import annotations

import asyncio
from copy import deepcopy
import hashlib
import json
from typing import Any, TYPE_CHECKING

from agents.sandbox.capabilities.tools.apply_patch_tool import SandboxApplyPatchTool
from agents.tool import CustomTool

from .native_execution import native_patch_call_scope

if TYPE_CHECKING:
    from .agent_tools import AgentToolProvider, ToolDescriptor
    from .guardrails import SDKGuardrailPolicy


def _arguments_hash(raw: str) -> str:
    # The Go audit authority hashes encoding/json's canonical string object.
    canonical = json.dumps({"input": raw}, ensure_ascii=False, separators=(",", ":"))
    for value, escaped in (("<", "\\u003c"), (">", "\\u003e"), ("&", "\\u0026"), ("\u2028", "\\u2028"), ("\u2029", "\\u2029")):
        canonical = canonical.replace(value, escaped)
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


def audited_native_patch_tool(
    provider: AgentToolProvider,
    native: SandboxApplyPatchTool,
    descriptor: ToolDescriptor,
    policy: SDKGuardrailPolicy | None,
) -> CustomTool:
    from .agent_tools import AgentToolApprovalRejected, AgentToolApprovalRequired, AgentToolConfigurationError, AgentToolResultTooLarge

    if (not provider.audit_enabled or policy is None or descriptor.id != "runtime:apply_patch"
            or descriptor.name != native.name or descriptor.kind != "runtime_function"
            or descriptor.access not in {"write", "sensitive"} or descriptor.approval != "always"
            or descriptor.max_retries != 0):
        raise AgentToolConfigurationError("Native patch execution requires an audited, approval-only write descriptor and data policy")

    async def registered(context: Any, raw: str, call_id: str) -> dict[str, Any]:
        if not isinstance(raw, str) or policy.tool_content_problem(raw):
            raise AgentToolConfigurationError("Native patch input blocked by platform data policy")
        try:
            call = await provider.ensure_call(context, descriptor.id, {"input": raw}, call_id,
                                              configuration_hash=descriptor.configuration_hash)
        except Exception as exc:
            raise AgentToolConfigurationError("Native patch approval registration failed") from exc
        if (not isinstance(call, dict) or not call.get("agent_tool_call_id")
                or call.get("sdk_tool_call_id") != call_id or call.get("tool_id") != descriptor.id
                or call.get("arguments_hash") != _arguments_hash(raw)):
            raise AgentToolConfigurationError("Native patch input does not match its durable approval")
        status = call.get("status")
        if status == "rejected":
            raise AgentToolApprovalRejected("Native patch approval was rejected")
        if status not in {"pending_approval", "approved"}:
            raise AgentToolConfigurationError("Native patch call is not pending or approved; automatic replay is forbidden")
        return call

    async def needs_approval(context: Any, raw: str, call_id: str) -> bool:
        return (await registered(context, raw, call_id))["status"] == "pending_approval"

    async def record_failure(call_id: str, code: str) -> None:
        try:
            await provider.fail_call(call_id, code, "Native patch execution did not produce a confirmed result; do not replay automatically", required=True)
        except Exception as exc:
            raise AgentToolConfigurationError("Native patch failure receipt could not be persisted") from exc

    async def invoke(context: Any, raw: str) -> Any:
        sdk_call_id = str(getattr(context, "tool_call_id", ""))
        call = await registered(context, raw, sdk_call_id)
        if call["status"] == "pending_approval":
            raise AgentToolApprovalRequired("Native patch approval is required")
        call_id = call["agent_tool_call_id"]
        call["status"] = "starting"
        try:
            started = await provider.start_call(call_id, sdk_call_id, descriptor.configuration_hash)
            if not isinstance(started, dict) or started.get("agent_tool_call_id") != call_id or started.get("status") != "running":
                raise AgentToolConfigurationError("Native patch start receipt is invalid")
        except asyncio.CancelledError:
            call["status"] = "failed"
            raise
        except Exception as exc:
            call["status"] = "failed"
            raise AgentToolConfigurationError("Native patch execution authority could not be acquired") from exc
        call["status"] = "running"
        try:
            async with asyncio.timeout(descriptor.timeout_seconds):
                # Keep the SDK's grammar, path validation and multi-file editor intact.
                with native_patch_call_scope(call_id, sdk_call_id, _arguments_hash(raw)):
                    output = await native.on_invoke_tool(context, raw)
            if policy.tool_content_problem(output):
                raise AgentToolConfigurationError("Native patch output blocked by platform data policy")
            payload = json.dumps(output, ensure_ascii=False, separators=(",", ":"))
            size = len(payload.encode("utf-8"))
            if size > descriptor.max_result_bytes:
                raise AgentToolResultTooLarge("Native patch result exceeds the configured output limit")
            await provider.complete_call(call_id, output, size, required=True)
        except asyncio.CancelledError:
            call["status"] = "failed"
            await record_failure(call_id, "CUSTOM_TOOL_OUTCOME_UNKNOWN")
            raise
        except Exception as exc:
            call["status"] = "failed"
            await record_failure(call_id, "CUSTOM_TOOL_OUTCOME_UNKNOWN")
            raise AgentToolConfigurationError("Native patch did not produce a confirmed result; automatic replay is forbidden") from exc
        call["status"] = "completed"
        return output

    # Operation-typed native callbacks cannot replace the platform's durable user decision.
    return CustomTool(name=native.name, description=native.description, format=deepcopy(native.format),
                      on_invoke_tool=invoke, needs_approval=needs_approval, on_approval=None,
                      defer_loading=descriptor.defer_loading, allowed_callers=deepcopy(native.allowed_callers))
