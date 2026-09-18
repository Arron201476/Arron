import asyncio
from copy import deepcopy
import json
from pathlib import Path

import pytest
from jsonschema import Draft202012Validator

from content_agent_sidecar.task_worker import SDKTaskWorker
from test_stateful_execution import StreamingSequence, WorkerBackend, final_item, worker_for


FIXTURE = json.loads((Path(__file__).resolve().parents[3] / "fixtures/skills/response-contracts/multi-result.json").read_text(encoding="utf-8"))


def bundle_pack(provider=True):
    contracts = deepcopy(FIXTURE["output_contracts"])
    schema = {"type": "object", "additionalProperties": False,
              "properties": {item["artifact_type"]: item["schema"] for item in contracts},
              "required": [item["artifact_type"] for item in contracts]}
    if provider:
        schema = {"type": "object", "allOf": [schema, deepcopy(FIXTURE["provider_schema"])]}
    return {"output_contract": contracts[0], "output_contracts": contracts,
            "provider_result_contract": {"artifact_type": "provider_result", "schema": schema},
            "skill_instructions": {"content": "Execute the original workflow."}}


@pytest.mark.parametrize("payload", FIXTURE["invalid"])
def test_multi_output_contract_rejects_incomplete_or_inconsistent_bundles(payload):
    with pytest.raises(ValueError):
        SDKTaskWorker._parse_and_validate(json.dumps(payload), bundle_pack())


@pytest.mark.parametrize("provider", [False, True])
def test_managed_bundle_never_discards_undeclared_output_fields(provider):
    extra = {**deepcopy(FIXTURE["valid"]), "undeclared": {"content": "Must not silently disappear."}}
    with pytest.raises(ValueError):
        SDKTaskWorker._parse_and_validate(json.dumps(extra), bundle_pack(provider))


@pytest.mark.parametrize("provider", [False, True])
@pytest.mark.parametrize("repair", [False, True])
def test_sdk_runner_submits_all_named_outputs_together(provider, repair):
    async def run():
        backend = WorkerBackend()
        backend.claim["context_pack"].update(bundle_pack(provider))
        responses = [[final_item(FIXTURE["valid"])]]
        if repair:
            responses.insert(0, [final_item(FIXTURE["invalid"][0])])
        model = StreamingSequence(responses)
        assert await worker_for(backend, model).run_once()
        assert not backend.failures and backend.commits == ["response-hash"]
        assert len(backend.submissions) == 1 and backend.submissions[0][0] == FIXTURE["valid"]
        assert len(model.inputs) == 1 + int(repair) and not model.responses
        schema = model.schemas[0].json_schema()
        Draft202012Validator.check_schema(schema)
        assert not list(Draft202012Validator(schema).iter_errors(FIXTURE["valid"]))
        for payload in FIXTURE["invalid"][:4]:
            assert list(Draft202012Validator(schema).iter_errors(payload))
        assert not (asyncio.all_tasks() - {asyncio.current_task()})
    asyncio.run(run())
