from __future__ import annotations

import json
from typing import Any

from mcp.types import CallToolResult, TextContent


EXTERNAL_TOOL_RECOVERY_REASON = "external_tool_outcome_unknown"
EXTERNAL_TOOL_RECOVERY_CODE = "SDK_TOOL_OUTCOME_UNRESOLVED"
MCP_OUTCOME_UNKNOWN_CODE = "MCP_TOOL_OUTCOME_UNKNOWN"


class ToolOutcomeReviewRequired(Exception):
    def __init__(self, result: Any):
        super().__init__("Issued external tool outcome requires user reconciliation")
        self.result = result


def require_external_tool_pause(result: Any) -> None:
    if (not result.is_complete or result.interruptions or result.final_output is not None
            or type(result.current_turn) is not int or type(result.max_turns) is not int
            or not 0 < result.current_turn < result.max_turns):
        raise ValueError("Unknown tool outcome did not reach a resumable native boundary")


def unknown_mcp_result(agent_tool_call_id: str, sdk_tool_call_id: str, *, tool_id: str = "", arguments_hash: str = "") -> CallToolResult:
    for value in (agent_tool_call_id, sdk_tool_call_id):
        if not isinstance(value, str) or not value.strip() or "\0" in value or len(value.encode("utf-8")) > 256:
            raise ValueError("Unknown tool outcomes require exact audited call identities")
    payload = {"schema_version": "agent_tool_outcome.v1", "status": "outcome_unknown",
               "agent_tool_call_id": agent_tool_call_id, "sdk_tool_call_id": sdk_tool_call_id,
               "requires_user_reconciliation": True}
    if tool_id or arguments_hash:
        if (not isinstance(tool_id, str) or not tool_id.startswith("mcp:") or "\0" in tool_id or len(tool_id.encode("utf-8")) > 512
                or not isinstance(arguments_hash, str) or len(arguments_hash) != 64 or any(char not in "0123456789abcdef" for char in arguments_hash)):
            raise ValueError("Unknown tool outcome requires its original tool and arguments hash")
        payload.update(tool_id=tool_id, arguments_hash=arguments_hash)
    return CallToolResult(content=[TextContent(text=json.dumps(payload, ensure_ascii=False, separators=(",", ":")))], is_error=True)


def mark_external_tool_review(context: Any, agent_tool_call_id: str) -> None:
    if not agent_tool_call_id or len(agent_tool_call_id.encode("utf-8")) > 256 or "\0" in agent_tool_call_id:
        raise ValueError("Unknown tool outcome identity is invalid")
    ids = getattr(context, "external_tool_outcome_ids", None)
    if ids is None:
        ids = []
        context.external_tool_outcome_ids = ids
    if not isinstance(ids, list) or any(not isinstance(value, str) for value in ids):
        raise ValueError("Unknown tool outcome set is invalid")
    if agent_tool_call_id not in ids:
        ids.append(agent_tool_call_id)


def external_tool_review_requested(context: Any) -> bool:
    return bool(getattr(context, "external_tool_outcome_ids", []))
