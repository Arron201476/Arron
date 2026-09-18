from __future__ import annotations

from typing import Any, Mapping


UNIFIED_SKILL_SCHEMA = "unified_skill_metadata.v1"


def allows_implicit_skill_invocation(capability: Mapping[str, Any]) -> bool:
    policy = capability.get("entry_policy")
    skill = capability.get("skill")
    return (
        (not isinstance(policy, Mapping) or policy.get("auto_route", True) is True)
        and (not isinstance(skill, Mapping) or skill.get("allow_implicit_invocation", True) is True)
    )


def map_local_skill(capability: Mapping[str, Any]) -> dict[str, Any]:
    skill = capability.get("skill")
    if not isinstance(skill, Mapping):
        raise ValueError("local capability does not contain Skill metadata")
    capability_id = _safe_identifier(capability.get("capability_id"), 128)
    version = _safe_identifier(capability.get("version"), 128)
    name = _required_text(skill.get("name"), 128)
    description = _required_text(capability.get("description"), 2048)
    path = _required_text(skill.get("path"), 1024)
    return {
        "schema_version": UNIFIED_SKILL_SCHEMA,
        "source": "local_package",
        "source_id": capability_id,
        "capability_id": capability_id,
        "name": name,
        "description": description,
        "default_version": version,
        "latest_version": version,
        "execution_mode": _safe_identifier(
            capability.get("execution_mode") or "inline", 64
        ),
        "allow_implicit_invocation": allows_implicit_skill_invocation(capability),
        "content_state": (
            "loaded" if str(skill.get("instructions") or "").strip() else "deferred"
        ),
        "content_ref": path,
    }


def map_openai_hosted_skill(skill: Any) -> dict[str, Any]:
    skill_id = _safe_identifier(_field(skill, "id"), 160)
    default_version = _safe_identifier(_field(skill, "default_version"), 128)
    latest_version = _safe_identifier(_field(skill, "latest_version"), 128)
    if _field(skill, "object") != "skill":
        raise ValueError("OpenAI hosted object is not a Skill")
    return {
        "schema_version": UNIFIED_SKILL_SCHEMA,
        "source": "openai_hosted",
        "source_id": skill_id,
        "capability_id": f"hosted:{skill_id}",
        "name": _required_text(_field(skill, "name"), 128),
        "description": _required_text(_field(skill, "description"), 2048),
        "default_version": default_version,
        "latest_version": latest_version,
        "execution_mode": "inline",
        "allow_implicit_invocation": False,
        "content_state": "deferred",
        "content_ref": f"skills/{skill_id}/versions/{default_version}/content",
    }


def _field(value: Any, name: str) -> Any:
    if isinstance(value, Mapping):
        return value.get(name)
    return getattr(value, name, None)


def _required_text(value: Any, limit: int) -> str:
    text = str(value or "").strip()
    if not text or len(text) > limit or any(ord(character) < 32 for character in text):
        raise ValueError("Skill metadata contains invalid text")
    return text


def _safe_identifier(value: Any, limit: int) -> str:
    text = str(value or "").strip()
    if (
        not text
        or len(text) > limit
        or any(
            not (character.isalnum() or character in {"_", "-", ".", ":"})
            for character in text
        )
    ):
        raise ValueError("Skill metadata contains an invalid identifier")
    return text
