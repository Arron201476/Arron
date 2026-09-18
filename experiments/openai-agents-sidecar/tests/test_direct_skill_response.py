import asyncio
from copy import deepcopy
import json
from pathlib import Path

import pytest
import httpx2
from openai import BadRequestError

from content_agent_sidecar.task_worker import SDKTaskWorker
from test_stateful_execution import StreamingSequence, WorkerBackend, final_item, worker_for


FIXTURE = json.loads((Path(__file__).resolve().parents[3] / "fixtures/skills/response-contracts/direct-result.json").read_text(encoding="utf-8"))


def response_pack():
    return {
        "output_contract": {"artifact_type": "generic_document", "schema": deepcopy(FIXTURE["output_schema"])},
        "provider_result_contract": {"artifact_type": "provider_result", "schema": {
            "type": "object", "allOf": [deepcopy(FIXTURE["output_schema"]), deepcopy(FIXTURE["provider_schema"])],
        }},
    }


@pytest.mark.parametrize("payload", FIXTURE["invalid"])
def test_direct_response_contract_rejects_outputs_that_do_not_satisfy_both_schemas(payload):
    with pytest.raises(ValueError):
        SDKTaskWorker._parse_and_validate(json.dumps(payload), response_pack())


@pytest.mark.parametrize("repair", [False, True])
@pytest.mark.parametrize("transport_rejected", [False, True])
def test_actual_sdk_runner_submits_raw_direct_result_and_repairs_missing_artifact_fields(repair, transport_rejected):
    async def run():
        backend = WorkerBackend()
        backend.claim["context_pack"].update(response_pack())
        responses = [[final_item(FIXTURE["valid"])]]
        if repair:
            responses.insert(0, [final_item(FIXTURE["invalid"][0])])
        if transport_rejected:
            responses.insert(0, BadRequestError(
                "provider does not support allOf in its output schema",
                response=httpx2.Response(400, request=httpx2.Request("POST", "https://fixture.invalid/responses")),
                body={"code": "invalid_json_schema"},
            ))
        model = StreamingSequence(responses)
        assert await worker_for(backend, model).run_once()
        assert not backend.failures and backend.commits == ["response-hash"]
        assert backend.submissions[0][0] == FIXTURE["valid"]
        assert len(model.inputs) == 1 + int(repair) + int(transport_rejected) and not model.responses
        assert model.schemas[0].json_schema() == response_pack()["provider_result_contract"]["schema"]
        if transport_rejected:
            assert model.schemas[1:] == [None] * (1 + int(repair))
        assert not (asyncio.all_tasks() - {asyncio.current_task()})
    asyncio.run(run())
