"""Restore private SDK approvals from the Runtime's durable tool authority."""

import json

from .backend import BackendError


async def restore_memory_approvals(state, backend, context, *, phase="extraction"):
    if phase not in {"extraction", "consolidation"}:
        raise BackendError("Memory approval phase is invalid")
    allowed = {"exec_command", "write_stdin", "apply_patch"}
    if phase == "consolidation":
        allowed.add("publish_agent_memory")
    items = state.get_interruptions()
    calls = []
    seen = set()
    for item in items:
        if (not item.call_id or item.call_id in seen or item.tool_namespace
                or item.name not in allowed):
            raise BackendError("Memory checkpoint contains an unadmitted approval")
        seen.add(item.call_id)
        raw = item.raw_item
        kind = raw.get("type") if isinstance(raw, dict) else getattr(raw, "type", None)
        try:
            if item.name == "apply_patch":
                if kind != "custom_tool_call" or not isinstance(item.arguments, str):
                    raise ValueError("patch")
                arguments = {"input": item.arguments}
            else:
                if kind != "function_call":
                    raise ValueError("function")
                arguments = json.loads(item.arguments)
                if not isinstance(arguments, dict):
                    raise ValueError("arguments")
        except (ValueError, TypeError):
            raise BackendError("Memory checkpoint approval arguments are invalid") from None
        calls.append((item, arguments))
    catalog = await backend.get_agent_tool_catalog()
    descriptors = {tool["id"]: tool for tool in catalog["tools"]}
    decisions = []
    for item, arguments in calls:
        tool_id = "runtime:" + item.name
        descriptor = descriptors.get(tool_id)
        if descriptor is None or descriptor.get("enabled") is not True or descriptor.get("approval") != "always":
            raise BackendError("Memory approval tool is no longer admitted")
        # Re-registering the same SDK ID reads its durable receipt. Runtime checks
        # the original arguments, generation and phase before returning a decision.
        receipt = await backend.begin_agent_tool_call(project_id=context.project_id,
            conversation_id=context.conversation_id, agent_turn_id="", sdk_tool_call_id=item.call_id,
            tool_id=tool_id, arguments=arguments, memory_generation_id=context.memory_generation_id,
            memory_generation_attempt=context.memory_generation_attempt, attempt_token=context.attempt_token,
            configuration_hash=descriptor.get("configuration_hash", ""))
        expected = {"project_id": context.project_id, "conversation_id": context.conversation_id,
                    "sdk_tool_call_id": item.call_id, "tool_id": tool_id}
        status = receipt.get("status") if isinstance(receipt, dict) else None
        approval = {"approved": "approved", "rejected": "rejected", "pending_approval": "pending"}.get(status)
        if (not isinstance(receipt, dict) or not receipt.get("agent_tool_call_id") or approval is None
                or receipt.get("approval_status") != approval
                or any(receipt.get(key) != value for key, value in expected.items())):
            raise BackendError("Memory approval receipt is inconsistent or cannot be replayed")
        decisions.append((item, status))
    # Do not partially modify SDK state if any receipt failed validation.
    for item, status in decisions:
        if status == "approved":
            state.approve(item)
        elif status == "rejected":
            state.reject(item, rejection_message="The user rejected this tool call. Continue without executing it.")
