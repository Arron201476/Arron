from __future__ import annotations

from pathlib import Path

import pytest

from content_agent_sidecar.evals import (
    build_execution_request,
    evaluate_decision,
    load_eval_suite,
    validate_eval_suite,
)


PROJECT_ROOT = Path(__file__).resolve().parents[3]


def test_agent_eval_suite_covers_legacy_and_generic_paths() -> None:
    suite = load_eval_suite(PROJECT_ROOT / "acceptance" / "agent-eval-cases.json")
    tags = {tag for case in suite["cases"] for tag in case["tags"]}
    assert set(suite["required_tags"]) <= tags
    assert len(suite["cases"]) >= 11


def test_eval_request_and_decision_checks_do_not_emit_prompt_content() -> None:
    suite = load_eval_suite(PROJECT_ROOT / "acceptance" / "agent-eval-cases.json")
    case = next(item for item in suite["cases"] if item["id"] == "legacy_novel")
    request = build_execution_request(suite, case)
    assert request["request"]["capability_ref"]["capability_id"] == "novel_to_script"
    assert request["agent_turn_id"] == "eval_turn_legacy_novel"
    assert evaluate_decision(
        case, {"intent": "propose_capability", "capability_id": "novel_to_script"}
    ) == []
    assert evaluate_decision(case, {"intent": "chat", "capability_id": None}) == [
        "unexpected_intent", "unexpected_capability",
    ]


def test_false_trigger_eval_rejects_skill_activation() -> None:
    suite = load_eval_suite(PROJECT_ROOT / "acceptance" / "agent-eval-cases.json")
    case = next(item for item in suite["cases"] if item["id"] == "inline_false_trigger")
    assert evaluate_decision(case, {"intent": "chat", "capability_id": None}) == []
    assert "unexpected_capability" in evaluate_decision(
        case, {"intent": "propose_capability", "capability_id": "outline_critic"}
    )


@pytest.mark.parametrize("change", [
    lambda s: s["cases"][0]["expected"].update(intents="chat"),
    lambda s: s["cases"][0]["expected"].update(intents=[None]),
    lambda s: s["cases"][0]["expected"].update(capability_id="missing_skill"),
    lambda s: s["cases"][0]["expected"].update(forbidden_capability_ids="outline_critic"),
    lambda s: s["cases"][0]["expected"].update(forbidden_capability_ids=["missing_skill"]),
    lambda s: s["cases"][0].update(prompt={"content": "Not a string"}),
    lambda s: s["cases"][0]["tags"].append(None),
    lambda s: s["capabilities"].append(dict(s["capabilities"][0])),
    lambda s: s["capabilities"].append(None),
    lambda s: s["capabilities"][0].update(creates_run="true"),
    lambda s: s["capabilities"][0].update(creates_run=False),
    lambda s: s["capabilities"][0].update(execution_mode="unknown_mode"),
    lambda s: s["cases"][1]["capability_ref"].update(version="missing_version"),
    lambda s: s["cases"][1]["capability_ref"].pop("version"),
    lambda s: s["cases"][0].update(capability_ref=[]),
], ids=[
    "string-intents", "non-string-intent", "unknown-expected-capability",
    "string-forbidden-capabilities", "unknown-forbidden-capability", "object-prompt",
    "non-string-tag", "duplicate-capability", "non-object-capability", "string-run-flag",
    "wrong-mode-run-relation", "unknown-execution-mode", "wrong-reference-version",
    "missing-reference-version", "non-object-reference",
])
def test_eval_suite_rejects_invalid_fixture_contracts(change) -> None:
    suite = load_eval_suite(PROJECT_ROOT / "acceptance" / "agent-eval-cases.json")
    change(suite)
    with pytest.raises(ValueError):
        validate_eval_suite(suite)


@pytest.mark.parametrize("mode", ["inline", "background_task", "stateful_workflow"])
@pytest.mark.parametrize("disguise", ["remove_required_tag", "retag_chat"])
def test_eval_mode_coverage_comes_from_expected_references_not_labels(mode, disguise) -> None:
    suite = load_eval_suite(PROJECT_ROOT / "acceptance" / "agent-eval-cases.json")
    ids = {
        item["capability_id"] for item in suite["capabilities"]
        if item.get("skill") and item["execution_mode"] == mode
    }
    suite["cases"] = [case for case in suite["cases"] if case.get("capability_ref", {}).get("capability_id") not in ids]
    if disguise == "remove_required_tag":
        suite["required_tags"].remove(mode)
    else:
        suite["cases"][0]["tags"].append(mode)
    with pytest.raises(ValueError):
        validate_eval_suite(suite)


def test_negative_routing_cases_do_not_replace_positive_workflow_coverage() -> None:
    suite = load_eval_suite(PROJECT_ROOT / "acceptance" / "agent-eval-cases.json")
    case = next(item for item in suite["cases"] if item["id"] == "legacy_continuation")
    case["expected"] = {"intents": ["chat"], "capability_id": None}
    with pytest.raises(ValueError):
        validate_eval_suite(suite)


@pytest.mark.parametrize("suite", [None, [], "agent_eval_suite.v1"])
def test_eval_suite_rejects_non_object_roots(suite) -> None:
    with pytest.raises(ValueError):
        validate_eval_suite(suite)


def test_eval_suite_keeps_valid_version_pins_and_negative_cases() -> None:
    suite = load_eval_suite(PROJECT_ROOT / "acceptance" / "agent-eval-cases.json")
    capability = next(item for item in suite["capabilities"] if item["capability_id"] == "outline_critic")
    suite["capabilities"].append({**capability, "version": "2.0.0"})
    positive = next(item for item in suite["cases"] if item["id"] == "fixture_inline_explicit")
    positive["capability_ref"]["version"] = "2.0.0"
    negative = next(item for item in suite["cases"] if item["id"] == "inline_false_trigger")
    negative["capability_ref"] = {"capability_id": "outline_critic", "version": "1.0.0"}
    suite["cases"][0]["prompt"] = "\n Hello.\n"
    validate_eval_suite(suite)
    assert build_execution_request(suite, positive)["request"]["capability_ref"]["version"] == "2.0.0"
    assert evaluate_decision(negative, {"intent": "chat", "capability_id": None}) == []


def test_allowing_chat_does_not_prove_successful_skill_routing() -> None:
    suite = load_eval_suite(PROJECT_ROOT / "acceptance" / "agent-eval-cases.json")
    case = next(item for item in suite["cases"] if item["id"] == "fixture_inline_explicit")
    case["expected"]["intents"].append("chat")
    with pytest.raises(ValueError, match="Skill execution modes"):
        validate_eval_suite(suite)
