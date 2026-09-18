from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Mapping


EVAL_SCHEMA = "agent_eval_suite.v1"
EXECUTION_MODES = {"inline", "background_task", "stateful_workflow"}
LEGACY_CAPABILITIES = {
    "novel_to_script", "non_novel_to_script",
    "video_reference_creation", "script_continuation",
}


def load_eval_suite(path: Path) -> dict[str, Any]:
    data = json.loads(path.read_text(encoding="utf-8"))
    validate_eval_suite(data)
    return data


def validate_eval_suite(suite: Mapping[str, Any]) -> None:
    if not isinstance(suite, Mapping) or suite.get("schema_version") != EVAL_SCHEMA:
        raise ValueError("unsupported Agent eval suite version")
    capabilities = suite.get("capabilities")
    cases = suite.get("cases")
    required_tags = suite.get("required_tags")
    if not isinstance(capabilities, list) or not capabilities:
        raise ValueError("Agent eval suite requires capabilities")
    if not isinstance(cases, list) or not cases:
        raise ValueError("Agent eval suite requires cases")
    _string_list(required_tags, "coverage tags", nonempty=True)
    catalog: dict[tuple[str, str], Mapping[str, Any]] = {}
    for item in capabilities:
        if not isinstance(item, Mapping):
            raise ValueError("Agent eval capability must be an object")
        key = (_identifier(item.get("capability_id")), _string(item.get("version"), "version"))
        if key in catalog:
            raise ValueError("Agent eval capability references must be unique")
        mode = _string(item.get("execution_mode"), "execution mode")
        if mode not in EXECUTION_MODES:
            raise ValueError("Agent eval capability has an unknown execution mode")
        if item.get("creates_run") is not (mode == "stateful_workflow"):
            raise ValueError("Agent eval capability has an inconsistent creates_run flag")
        catalog[key] = item
    capability_ids = {key[0] for key in catalog}
    seen_ids: set[str] = set()
    covered_tags: set[str] = set()
    positive_references: set[str] = set()
    covered_skill_modes: set[str] = set()
    for case in cases:
        if not isinstance(case, Mapping):
            raise ValueError("Agent eval case must be an object")
        case_id = _identifier(case.get("id"))
        if case_id in seen_ids:
            raise ValueError("Agent eval case IDs must be unique")
        seen_ids.add(case_id)
        _string(case.get("prompt"), "prompt")
        covered_tags.update(_string_list(case.get("tags"), "case tags", nonempty=True))
        expected = case.get("expected")
        if not isinstance(expected, Mapping):
            raise ValueError(f"Agent eval case {case_id} has no expected intents")
        intents = _string_list(expected.get("intents"), "expected intents", nonempty=True)
        expected_capability = expected.get("capability_id")
        if expected_capability is not None:
            if _identifier(expected_capability) not in capability_ids:
                raise ValueError("Agent eval case expects an unknown capability")
        forbidden = _string_list(expected.get("forbidden_capability_ids", []), "forbidden capabilities")
        if not set(forbidden) <= capability_ids:
            raise ValueError("Agent eval case forbids an unknown capability")
        if expected_capability in forbidden:
            raise ValueError("Agent eval case both expects and forbids a capability")
        reference = case.get("capability_ref")
        if reference is not None:
            if not isinstance(reference, Mapping):
                raise ValueError("Agent eval capability reference must be an object")
            key = (_identifier(reference.get("capability_id")), _string(reference.get("version"), "version"))
            if key not in catalog:
                raise ValueError(f"Agent eval case {case_id} references an unknown capability version")
            # Only a positive routing assertion proves the selected fixture is covered.
            if expected_capability == key[0] and set(intents) == {"propose_capability"}:
                positive_references.add(key[0])
                if key[0] not in LEGACY_CAPABILITIES:
                    covered_skill_modes.add(catalog[key]["execution_mode"])
    missing_tags = set(required_tags) - covered_tags
    if missing_tags:
        raise ValueError(f"Agent eval suite is missing coverage: {sorted(missing_tags)}")
    if not LEGACY_CAPABILITIES <= positive_references:
        raise ValueError("Agent eval suite does not cover all legacy workflows")
    if not EXECUTION_MODES <= covered_skill_modes:
        raise ValueError("Agent eval suite does not cover all Skill execution modes")


def build_execution_request(
    suite: Mapping[str, Any], case: Mapping[str, Any]
) -> dict[str, Any]:
    case_id = _identifier(case.get("id"))
    request: dict[str, Any] = {
        "content": str(case["prompt"]),
        "attachment_refs": list(case.get("attachment_refs") or []),
        "selection_snapshot": case.get("selection_snapshot"),
        "client_context": {},
    }
    if isinstance(case.get("capability_ref"), Mapping):
        request["capability_ref"] = dict(case["capability_ref"])
    return {
        "project_id": "prj_sidecar_eval_" + case_id,
        "conversation_id": "conv_sidecar_eval_" + case_id,
        "request": request,
        "idempotency_key": "eval_" + case_id,
        "agent_turn_id": "eval_turn_" + case_id,
    }


def evaluate_decision(case: Mapping[str, Any], decision: Mapping[str, Any]) -> list[str]:
    expected = case["expected"]
    failures: list[str] = []
    intent = str(decision.get("intent") or "")
    if intent not in expected["intents"]:
        failures.append("unexpected_intent")
    expected_capability = expected.get("capability_id")
    actual_capability = decision.get("capability_id")
    if expected_capability is not None and actual_capability != expected_capability:
        failures.append("unexpected_capability")
    if expected_capability is None and actual_capability:
        failures.append("unexpected_capability")
    if actual_capability in set(expected.get("forbidden_capability_ids") or []):
        failures.append("forbidden_capability")
    return failures


def _identifier(value: Any) -> str:
    text = _string(value, "ID")
    if not text or len(text) > 96 or any(
        not (character.islower() or character.isdigit() or character == "_")
        for character in text
    ):
        raise ValueError("Agent eval case has an invalid ID")
    return text


def _string(value: Any, field: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"Agent eval {field} must be a nonempty string")
    return value


def _string_list(value: Any, field: str, *, nonempty: bool = False) -> list[str]:
    if not isinstance(value, list) or (nonempty and not value):
        raise ValueError(f"Agent eval {field} must be a list" + (" with entries" if nonempty else ""))
    for item in value:
        _string(item, field)
    return value
