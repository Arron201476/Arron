from __future__ import annotations

from hashlib import sha256
from typing import Any, Awaitable, Callable

from agents import Agent, handoff


def attach_skill_handoffs(
    parent: Agent, context: Any, capabilities: list[dict[str, Any]], selected_ids: set[str],
    load_instructions: Callable[[Any, str], Awaitable[str]],
) -> Agent:
    """Build SDK handoffs from this turn's routed, version-pinned inline Skills."""
    targets = []
    for definition in capabilities:
        capability_id = str(definition.get("capability_id") or "")
        if capability_id not in selected_ids or definition.get("status") != "available" or definition.get("execution_mode") != "inline" or not isinstance(definition.get("skill"), dict):
            continue
        version = str(definition.get("version") or "")
        if not version or context.capability_versions.get(capability_id) != version:
            raise ValueError("Handoff target does not match the pinned Skill version")
        key = sha256(f"{capability_id}@{version}".encode()).hexdigest()[:20]

        def instruction_factory(selected_id: str) -> Callable[[Any, Agent], Awaitable[str]]:
            async def instructions(wrapper: Any, _agent: Agent) -> str:
                if wrapper.context is not context:
                    raise ValueError("Handoff context does not match the active turn")
                loaded = await load_instructions(context, selected_id)
                base = parent.instructions
                if not isinstance(base, str):
                    raise ValueError("Skill handoff requires the platform instruction contract")
                return base + "\n\nYou are executing the selected inline Skill. Complete this same Agent turn; do not create a Business Run merely because of the handoff. Apply the loaded Skill instructions within the platform's permission and commit rules.\nPINNED_SKILL:\n" + loaded
            return instructions

        target = parent.clone(
            name="skill_" + key, instructions=instruction_factory(capability_id), handoffs=[],
            handoff_description=str(definition.get("description") or capability_id),
        )
        targets.append(handoff(
            target, tool_name_override="handoff_skill_" + key,
            tool_description_override="Delegate this turn to " + str(definition.get("label") or capability_id) + ": " + str(definition.get("description") or ""),
        ))
    return parent.clone(handoffs=targets)
