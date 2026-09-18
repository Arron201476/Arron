from __future__ import annotations

import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
PROJECT_ROOT = ROOT.parents[1]


def test_migration_gates_record_approved_cutover() -> None:
    gates = json.loads((ROOT / "migration-gates.json").read_text(encoding="utf-8"))

    assert gates["cutover_requires_user_approval"] is False
    assert [step["id"] for step in gates["steps"]] == list(range(1, 9))
    assert gates["steps"][-1]["status"] == "completed"


def test_migration_gates_separate_sdk_and_runtime_ownership() -> None:
    gates = json.loads((ROOT / "migration-gates.json").read_text(encoding="utf-8"))
    runtime = set(gates["runtime_ownership"])
    sdk = set(gates["sdk_ownership"])

    assert not runtime & sdk
    assert {"artifacts", "runs", "approvals", "idempotency"} <= runtime
    assert {"session", "intent_decision", "target_discovery", "tool_selection", "artifact_revision"} <= sdk


def test_migration_gates_cover_generic_skills_and_failures() -> None:
    gates = json.loads((ROOT / "migration-gates.json").read_text(encoding="utf-8"))

    assert set(gates["regression_cases"]) == {
        "G1", "G2", "G3", "G4", "G5", "G6",
        "S1", "S2", "S3", "R1", "R2", "R3", "R4", "U1",
    }


def test_server_cutover_path_has_no_eino_or_go_model_execution() -> None:
    main = (PROJECT_ROOT / "backend" / "cmd" / "server" / "main.go").read_text(
        encoding="utf-8"
    )
    assert "NewRequiredSidecarAgentService" in main
    assert "revision.NewSidecar" in main
    assert "NewEinoAgentService" not in main
    assert "modelprovider.NewOpenAICompatible" not in main
    assert "workerdeploy.NewDemoExecution" not in main
