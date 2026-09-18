import asyncio
from copy import deepcopy
import json
from pathlib import Path

from jsonschema import Draft202012Validator
import pytest

from test_stateful_execution import StreamingSequence, WorkerBackend, final_item, worker_for


ROOT = Path(__file__).resolve().parents[3]
PACKAGE = ROOT / "fixtures/skills/batched/episode-review-workflow"


def package_json(path):
    return json.loads((PACKAGE / path).read_text(encoding="utf-8"))


def test_batch_workflow_fixture_matches_manifest_schema_and_source_contract():
    schema = json.loads((ROOT / "capabilities/v1/manifest.schema.json").read_text(encoding="utf-8"))
    Draft202012Validator.check_schema(schema)
    Draft202012Validator(schema).validate(package_json("content-agent/workflow.json"))
    for name in ("input", "config", "result"):
        Draft202012Validator.check_schema(package_json(f"schemas/{name}.json"))
    validator = Draft202012Validator(package_json("schemas/input.json"))
    common = {"project_id": "p", "source_type": "story", "user_request_message_id": "m"}
    validator.validate({**common, "assets": [{"asset_id": "a", "asset_snapshot_id": "as1", "role": "primary_source", "order": 1}]})
    validator.validate({**common, "artifact_versions": [{"artifact_version_id": "av1", "role": "primary_source", "order": 1}]})
    assert not validator.is_valid(common)


@pytest.mark.parametrize("episode_no", [1, 2, 3])
def test_actual_sdk_worker_uses_package_batch_schema_and_authoritative_scope(episode_no):
    async def run():
        backend = WorkerBackend()
        pack = backend.claim["context_pack"]
        schema = package_json("schemas/result.json")
        contract = {"artifact_type": "generic_document", "schema": schema}
        pack.update({
            "skill_instructions": {"content": (PACKAGE / "SKILL.md").read_text(encoding="utf-8")},
            "prompt": {"content": (PACKAGE / "prompts/review.md").read_text(encoding="utf-8")},
            "target": {"scope_key": f"episode:{episode_no}"},
            "task_cursor": {"batch": {"episode_start": episode_no, "episode_end": episode_no, "target_episode_count": 3}},
            "asset_context": [{"asset_id": "outline", "asset_snapshot_id": "outline-v1", "content": "Three episode premises."}],
            "output_contract": contract,
            "output_contracts": [deepcopy(contract)],
            "provider_result_contract": {**deepcopy(contract), "artifact_type": "provider_result"},
        })
        backend.primary["data"]["skill"]["instructions"] = pack["skill_instructions"]["content"]
        payload = {"episode_no": episode_no, "title": f"Episode {episode_no}", "content": "Raise the stakes before the closing hook."}
        model = StreamingSequence([[final_item(payload)]])
        assert await worker_for(backend, model).run_once()
        assert backend.commits == ["response-hash"] and not backend.failures
        assert backend.submissions[0][0] == payload
        assert len(model.inputs) == 1 and not model.responses
        assert model.schemas[0].json_schema() == schema
        prompt = json.dumps(model.inputs[0])
        assert f"episode:{episode_no}" in prompt and "outline-v1" in prompt
        assert not (asyncio.all_tasks() - {asyncio.current_task()})
    asyncio.run(run())
