from __future__ import annotations

import json
from copy import deepcopy
from typing import Any, Literal
from urllib.parse import quote, urlencode

from agents import RunContextWrapper, function_tool
from agents.tool_context import ToolContext


@function_tool
async def validate_workspace_skill(ctx: RunContextWrapper[Any], root_path: str) -> str:
    """Validate a saved Skill folder without installing or executing it. Use an empty root_path for project root. A minimal SKILL.md has YAML name (lowercase kebab-case), description, and Markdown instructions. Optional references/, scripts/, assets/ and agents/ files are included. Returns the exact snapshot hash required for export/installation."""
    result = await ctx.context.backend.preview_workspace_skill(ctx.context.project_id, root_path)
    if result.get("project_id") != ctx.context.project_id or result.get("root_path") != root_path:
        raise ValueError("Skill draft preview does not match this project and folder")
    result["installed_as_skill"] = False
    if result.get("status") == "valid":
        query = urlencode({"root_path": root_path, "snapshot_hash": result["snapshot_hash"]})
        result["archive_url"] = f"/api/v1/projects/{quote(ctx.context.project_id, safe='')}/skill-drafts/archive?{query}"
    return json.dumps(result, ensure_ascii=False)


@function_tool
async def install_workspace_skill(
    ctx: ToolContext[Any], root_path: str, snapshot_hash: str,
    scope: Literal["user", "project", "workspace"], installation_id: str, expected_active_version_id: str,
) -> str:
    """Request approval to install a validated Skill draft. Use the exact validation snapshot; choose user/project/workspace scope as requested. New installs require empty installation_id and expected_active_version_id. Upgrades require both IDs from the existing installation. This tool requests SDK approval before changing the registry; do not replace it with a chat confirmation."""
    context = ctx.context
    call = context.active_tool_calls.get(ctx.tool_call_id)
    if not isinstance(call, dict) or not call.get("agent_tool_call_id"):
        raise ValueError("Skill installation requires an audited approved SDK call")
    arguments = {"root_path": root_path, "snapshot_hash": snapshot_hash, "scope": scope,
                 "installation_id": installation_id, "expected_active_version_id": expected_active_version_id}
    saved = await context.backend.install_workspace_skill(str(call["agent_tool_call_id"]), ctx.tool_call_id, arguments)
    receipt, installation = saved.get("receipt") or {}, saved.get("installation") or {}
    if not receipt.get("receipt_id") or not receipt.get("skill_version_id") or not installation.get("skill_installation_id") or receipt.get("project_id") != context.project_id or receipt.get("skill_installation_id") != installation.get("skill_installation_id") or installation.get("scope") != scope or (scope == "project" and installation.get("scope_ref") != context.project_id) or (installation_id and installation.get("skill_installation_id") != installation_id):
        raise ValueError("Skill installation receipt does not match the requested scope")
    version = next((item for item in installation.get("versions", []) if item.get("skill_version_id") == receipt.get("skill_version_id")), None)
    if not version:
        raise ValueError("Installed Skill version is missing from its receipt")
    result = {
        "receipt": receipt, "installed_as_skill": True,
        "skill_installation_id": installation["skill_installation_id"],
        "capability_id": installation["capability_id"], "name": installation["skill_name"],
        "version": version["version"], "content_hash": version["content_hash"],
        "execution_mode": version["execution_mode"], "scope": scope, "scope_ref": installation["scope_ref"],
        "enabled": installation["enabled"], "registry_status": installation["registry_status"],
        "active_version_id": installation.get("active_version_id"),
        "ready_in_current_turn": False,
    }
    previous = context.loaded_capabilities.get(installation["capability_id"]) or {}
    if (context.agent_task_id or context.execution_attempt_id) and previous and previous.get("execution_mode") != "inline":
        result["readiness_reason"] = "EXECUTION_VERSION_PINNED"
        return json.dumps(result, ensure_ascii=False)
    if installation.get("enabled") and installation.get("registry_status") == "available" and installation.get("active_version_id") == version["skill_version_id"] and version["execution_mode"] == "inline":
        manifest = version.get("manifest") or {}
        tool_scope = context.skill_tool_scope
        if tool_scope is not None or (not manifest.get("dependencies") and not manifest.get("scripts")):
            try:
                selected = (await context.backend.get_capability(installation["capability_id"], context.project_id, version["version"])).get("data") or {}
                selected_skill = selected.get("skill") or {}
                if (selected.get("status") == "available" and selected.get("capability_id") == installation["capability_id"]
                        and selected.get("version") == version["version"] and selected.get("execution_mode") == "inline"
                        and selected_skill.get("content_hash") == version["content_hash"] and selected_skill.get("scope") == result["scope"]):
                    if tool_scope is not None:
                        # The install is committed, but dependency connection may
                        # fail. A later explicit load may retry this exact version
                        # without repeating installation or granting its tools early.
                        tool_scope.discoverable_capabilities[installation["capability_id"]] = deepcopy(selected)
                        tool_scope.pending_installed_ids.add(installation["capability_id"])
                        await tool_scope.activate_skill(selected)
                    else:
                        context.routed_capabilities.add(installation["capability_id"])
                        context.capability_versions[installation["capability_id"]] = version["version"]
                        context.loaded_capabilities.pop(installation["capability_id"], None)
                    result["ready_in_current_turn"] = True
                else:
                    result["readiness_reason"] = "CAPABILITY_VERSION_NOT_VISIBLE"
            except Exception:
                # Installation is already committed; a catalog read failure must
                # not turn its durable receipt into a claimed installation failure.
                result["readiness_reason"] = "CAPABILITY_REFRESH_FAILED"
        else:
            result["readiness_reason"] = "TOOL_PREPARATION_UNAVAILABLE"
    return json.dumps(result, ensure_ascii=False)
