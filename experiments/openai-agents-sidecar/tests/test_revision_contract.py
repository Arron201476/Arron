import asyncio
from copy import deepcopy
import json
from pathlib import Path
from types import SimpleNamespace

import pytest
from agents.items import ModelResponse
from agents.usage import Usage

from content_agent_sidecar import revision
from content_agent_sidecar.contracts import ArtifactRevisionRequest, ArtifactValidationContract
from content_agent_sidecar.runtime import RuntimeCompatibilityError
from test_run_state_approval import SequenceModel
from test_stateful_execution import final_item


FIXTURES = Path(__file__).resolve().parents[3] / "fixtures/skills/response-contracts"
DIRECT = json.loads((FIXTURES / "direct-result.json").read_text(encoding="utf-8"))
MULTI = json.loads((FIXTURES / "multi-result.json").read_text(encoding="utf-8"))


def contract_for(mode):
    schema = {"allOf": [DIRECT["output_schema"], DIRECT["provider_schema"]]}
    bundle = {}
    key = ""
    if mode == "multi":
        schema = {"allOf": [{"type": "object", "additionalProperties": False,
            "properties": {item["artifact_type"]: item["schema"] for item in MULTI["output_contracts"]},
            "required": [item["artifact_type"] for item in MULTI["output_contracts"]]}, MULTI["provider_schema"]]}
        bundle = deepcopy(MULTI["valid"])
        key = "review_notes"
    elif mode == "batch":
        output = deepcopy(DIRECT["output_schema"])
        output["properties"]["episode_no"] = {"type": "integer"}
        output["required"].append("episode_no")
        schema = {"allOf": [output, DIRECT["provider_schema"], {"properties": {"episode_no": {"const": 2}}}]}
    return ArtifactValidationContract(response_schema=schema, context_hash="frozen-context",
        output_key=key, output_bundle=bundle)


class RevisionModel(SequenceModel):
    def __init__(self, plan):
        super().__init__([ModelResponse(output=[final_item(plan)], usage=Usage(requests=1), response_id="revision-plan")])
        self.inputs = []

    async def get_response(self, *args, **kwargs):
        self.inputs.append(deepcopy(args[1] if len(args) > 1 else kwargs["input"]))
        return await super().get_response(*args, **kwargs)


def execute_plan(monkeypatch, contract, plan, base=None):
    monkeypatch.setattr(revision, "_native_apply_patch_supported", False)
    payload = deepcopy(base or DIRECT["valid"])
    if contract and "episode_no" in json.dumps(contract.response_schema):
        payload["episode_no"] = 2
    model = RevisionModel(plan)
    runtime = SimpleNamespace(model=model, orchestration_max_tokens=2048,
        settings=SimpleNamespace(tracing_enabled=False, release_id="revision-contract-test",
            run_timeout_seconds=10, model_name="deterministic-revision"))
    request = ArtifactRevisionRequest(revision_request_id="revision", revision_attempt_id="attempt",
        instruction="Revise the requested fields.", target={"field_path": "/"},
        artifact_schema={"schema_id": "generic_document", "schema_version": "1.0.0"},
        artifact_payload=payload, artifact_validation=contract)
    return runtime, request, model


@pytest.mark.parametrize("mode", ["single", "batch", "multi"])
@pytest.mark.parametrize("new_title", ["Short", "123", '"Updated review"'])
def test_real_sdk_revision_runner_enforces_frozen_output_contract(monkeypatch, mode, new_title):
    contract = contract_for(mode)
    saved_bundle = deepcopy(contract.output_bundle)
    plan = {"action": "patch", "old_text": "", "new_text": "", "summary": "Title revised",
        "replacements": [{"path": "/title", "old_text": '"Review"', "new_text": new_title}]}
    runtime, request, model = execute_plan(monkeypatch, contract, plan)

    async def scenario():
        if new_title == '"Updated review"':
            result = await revision.execute_artifact_revision(runtime, request)
            assert result.outcome == "proposed" and result.proposal_payload["title"] == "Updated review"
            assert result.changed_paths == ["/title"]
        else:
            with pytest.raises(RuntimeCompatibilityError, match="frozen output contract"):
                await revision.execute_artifact_revision(runtime, request)
        assert len(model.inputs) == 1 and not model.responses
        prompt = json.loads(model.inputs[0][0]["content"])
        assert prompt["artifact_validation"] == contract.model_dump()
        assert request.artifact_payload["title"] == "Review" and contract.output_bundle == saved_bundle
        assert not (asyncio.all_tasks() - {asyncio.current_task()})
    asyncio.run(scenario())


@pytest.mark.parametrize("mode", ["single", "batch", "multi"])
def test_revision_final_validation_rejects_missing_content(mode):
    contract = contract_for(mode)
    validator = revision.revision_output_validator(contract)
    payload = {"title": "Review"}
    if mode == "batch":
        payload["episode_no"] = 2
    with pytest.raises(RuntimeCompatibilityError, match="frozen output contract"):
        revision.validate_revision_proposal(validator, contract, payload)


def test_sdk_revision_cannot_change_batch_identity(monkeypatch):
    plan = {"action": "patch", "summary": "Change episode", "replacements": [
        {"path": "/episode_no", "old_text": "2", "new_text": "3"}]}
    runtime, request, _ = execute_plan(monkeypatch, contract_for("batch"), plan)
    with pytest.raises(RuntimeCompatibilityError, match="frozen output contract"):
        asyncio.run(revision.execute_artifact_revision(runtime, request))


def test_multi_replacement_validates_final_state_not_intermediate_patches(monkeypatch):
    contract = contract_for("single")
    contract.response_schema["allOf"].append({"oneOf": [
        {"properties": {"title": {"const": "Review"}, "content": {"const": DIRECT["valid"]["content"]}}},
        {"properties": {"title": {"const": "Updated review"}, "content": {"const": "Updated content"}}},
    ]})
    plan = {"action": "patch", "summary": "Revise related fields", "replacements": [
        {"path": "/title", "old_text": "Review", "new_text": "Updated review"},
        {"path": "/content", "old_text": DIRECT["valid"]["content"], "new_text": "Updated content"},
    ]}
    runtime, request, model = execute_plan(monkeypatch, contract, plan)
    result = asyncio.run(revision.execute_artifact_revision(runtime, request))
    assert result.proposal_payload == {"title": "Updated review", "content": "Updated content"}
    assert not model.responses and len(model.inputs) == 1


@pytest.mark.parametrize("mode", ["single", "multi"])
def test_no_change_preserves_base_without_synthesizing_a_repair(monkeypatch, mode):
    runtime, request, model = execute_plan(monkeypatch, contract_for(mode), {"action": "no_change", "summary": "Keep the original"})
    result = asyncio.run(revision.execute_artifact_revision(runtime, request))
    assert result.outcome == "no_change" and result.proposal_payload == request.artifact_payload
    assert not result.changed_paths and not model.responses


def test_legacy_revision_without_managed_contract_keeps_original_behavior(monkeypatch):
    runtime, request, _ = execute_plan(monkeypatch, None, {"action": "patch", "summary": "Rename", "replacements": [
        {"path": "/title", "old_text": "Review", "new_text": "Short"}]})
    assert asyncio.run(revision.execute_artifact_revision(runtime, request)).proposal_payload["title"] == "Short"


@pytest.mark.parametrize("broken", ["missing_key", "missing_bundle", "invalid_schema"])
def test_invalid_edit_contract_is_rejected_before_model_call(monkeypatch, broken):
    contract = contract_for("multi")
    if broken == "missing_key":
        contract.output_key = "not-an-output"
    elif broken == "missing_bundle":
        contract.output_bundle = {}
    else:
        contract.response_schema = {"type": "invalid"}
    runtime, request, model = execute_plan(monkeypatch, contract, {"action": "no_change", "summary": "Keep"})
    with pytest.raises(RuntimeCompatibilityError):
        asyncio.run(revision.execute_artifact_revision(runtime, request))
    assert len(model.responses) == 1 and not model.inputs


def test_revision_schema_does_not_fetch_external_resources():
    contract = ArtifactValidationContract(response_schema={"$ref": "https://example.invalid/private-schema"}, context_hash="frozen")
    with pytest.raises(RuntimeCompatibilityError, match="frozen output contract"):
        revision.validate_revision_proposal(revision.revision_output_validator(contract), contract, {"title": "Review"})
