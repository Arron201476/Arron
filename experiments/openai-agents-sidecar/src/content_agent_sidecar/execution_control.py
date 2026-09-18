from __future__ import annotations

import json
from typing import Any, Literal

from agents import RunContextWrapper, function_tool
from agents.tool_context import ToolContext


@function_tool
async def list_execution_targets(ctx: RunContextWrapper[Any], target_type: Literal["run", "agent_task"], after_id: str = "") -> str:
    """List this project's Runs or background tasks in bounded pages. Follow next_after_id when needed; never choose an ambiguous target by list order or UI focus. Inspect the exact target before controlling it."""
    result = await ctx.context.backend.list_execution_targets(ctx.context.project_id, target_type, after_id)
    if not isinstance(result.get("items"), list) or any(not isinstance(item, dict) or item.get("project_id") != ctx.context.project_id or item.get("target_type") != target_type for item in result["items"]):
        raise ValueError("Execution targets do not match this project")
    return json.dumps(result, ensure_ascii=False)


@function_tool
async def inspect_execution_controls(ctx: RunContextWrapper[Any], target_type: Literal["run", "agent_task"], target_id: str) -> str:
    """Read an exact task's status, available control actions, and authorization snapshot. Only these available_actions can be requested. A pausing task must reach paused before it can be resumed. This read does not mutate a task."""
    result = await ctx.context.backend.inspect_execution_controls(ctx.context.project_id, target_type, target_id)
    if (result.get("project_id"), result.get("target_type"), result.get("target_id")) != (ctx.context.project_id, target_type, target_id) or not result.get("snapshot_hash"):
        raise ValueError("Execution control snapshot does not match the requested target")
    return json.dumps(result, ensure_ascii=False)


@function_tool
async def control_execution(
    ctx: ToolContext[Any], target_type: Literal["run", "agent_task"], target_id: str,
    action_id: Literal["pause_run", "resume_run", "cancel_run", "retry_failed_step", "cancel_agent_task", "retry_agent_task", "pause_agent_task", "resume_agent_task"],
    snapshot_hash: str,
) -> str:
    """Request SDK approval for one control action returned by inspect_execution_controls, using its exact target and snapshot_hash. This is the same command as the UI button. Approval must occur before execution; stale snapshots require a fresh inspection and approval. pausing means pause requested, not already paused; queued means scheduled, not completed. Never claim execution from approval alone."""
    context = ctx.context
    call = context.active_tool_calls.get(ctx.tool_call_id)
    if not context.agent_turn_id or not isinstance(call, dict) or not call.get("agent_tool_call_id"):
        raise ValueError("Task control requires an audited main conversation tool call")
    arguments = {"target_type": target_type, "target_id": target_id, "action_id": action_id, "snapshot_hash": snapshot_hash}
    result = await context.backend.control_execution(str(call["agent_tool_call_id"]), ctx.tool_call_id, arguments)
    if (result.get("project_id"), result.get("target_type"), result.get("target_id"), result.get("action_id")) != (context.project_id, target_type, target_id, action_id) or not result.get("status"):
        raise ValueError("Execution control receipt does not match the approved request")
    return json.dumps(result, ensure_ascii=False)
