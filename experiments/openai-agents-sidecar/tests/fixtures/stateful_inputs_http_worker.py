"""Exercise real SDK additional-input admission against an isolated Go server."""
import argparse
import asyncio
import json
from pathlib import Path
import sys
from urllib.parse import urlsplit
from uuid import uuid4

import httpx2 as httpx
from openai import APIConnectionError

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from content_agent_sidecar.config import Settings
from content_agent_sidecar.task_worker import SDKTaskWorker
from test_stateful_execution import StreamingSequence, final_item, tool_item
from input_attachment_assertions import assert_attached_materials, user_text


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--backend-url", required=True)
    parser.add_argument("--phase", choices=["start", "failure-start", "finish", "reject-output", "late"], required=True)
    args = parser.parse_args()
    url = urlsplit(args.backend_url)
    if url.scheme != "http" or url.hostname != "127.0.0.1" or not url.port or url.port in {8860, 8880}:
        raise ValueError("Only isolated loopback servers are allowed")
    settings = Settings(backend_base_url=args.backend_url, model_base_url=args.backend_url + "/no-model-provider",
        model_api_key="fixture-not-a-credential", model_name="sdk-http-fixture", model_timeout_seconds=15,
        model_max_output_tokens=1024, model_max_retries=0, run_timeout_seconds=60,
        tracing_enabled=False, internal_token="sdk-http-test-internal", task_worker_lease_seconds=90,
        guardrail_max_output_chars=1 if args.phase == "reject-output" else 1_000_000)
    worker = SDKTaskWorker(settings)
    worker._heartbeat_interval_seconds = 1
    claim_saved, pause_seen = {}, asyncio.Event()
    claim_original, heartbeat_original = worker._backend.claim_execution_task, worker._backend.heartbeat_execution_attempt
    async def claim(**kwargs):
        result = await claim_original(**kwargs)
        claim_saved.update(result)
        return result
    async def heartbeat(*values, **kwargs):
        result = await heartbeat_original(*values, **kwargs)
        if result.get("data", result).get("pause_requested"):
            pause_seen.set()
        return result
    worker._backend.claim_execution_task = claim
    worker._backend.heartbeat_execution_attempt = heartbeat

    class InputModel(StreamingSequence):
        async def stream_response(self, *values, **kwargs):
            project = claim_saved["context_pack"]["project_id"]
            attempt = claim_saved["attempt"]["attempt_id"]
            if args.phase == "failure-start":
                assert len(self.inputs) < 2
                if not self.inputs:
                    output = [tool_item("apply_workspace_patch", {"path": "input-evidence.txt", "expected_version": 0,
                        "operation": "create_file", "diff": "+original-write"}, "input-write")]
                else:
                    async with httpx.AsyncClient(timeout=10) as client:
                        response = await client.post(f"{args.backend_url}/api/v1/projects/{project}/execution-attempts/{attempt}/inputs",
                            json={"content": "HTTP_ADDITIONAL_REQUIREMENT"}, headers={"Idempotency-Key": str(uuid4())})
                        assert response.status_code == 202, response.text
                    output = APIConnectionError(request=httpx.Request("POST", "https://fixture.invalid/responses"))
                self.responses.append(output)
                async for event in super().stream_response(*values, **kwargs):
                    yield event
                return
            if args.phase in {"start", "late"}:
                assert not self.inputs
                async with httpx.AsyncClient(timeout=10) as client:
                    response = await client.post(f"{args.backend_url}/api/v1/projects/{project}/execution-attempts/{attempt}/inputs",
                        json={"content": "HTTP_ADDITIONAL_REQUIREMENT"}, headers={"Idempotency-Key": str(uuid4())})
                    assert response.status_code == 202, response.text
                    assert response.json()["data"]["status"] == "received"
                await asyncio.wait_for(pause_seen.wait(), 5)
                output = final_item({"title": "Late instruction", "content": "The request arrived after this model call started."}) if args.phase == "late" else tool_item(
                    "apply_workspace_patch", {"path": "input-evidence.txt", "expected_version": 0, "operation": "create_file", "diff": "+original-write"}, "input-write")
            else:
                texts = [user_text(item) for item in values[1] if item.get("role") == "user"]
                assert texts.count("HTTP_ADDITIONAL_REQUIREMENT") == 1, texts
                assert "REMOVED_BEFORE_MODEL" not in texts
                assert len(claim_saved["additional_inputs"]) == 1
                assert_attached_materials(values[1], required=bool(claim_saved["additional_inputs"][0].get("attachments")))
                assert len([item for item in values[1] if item.get("type") == "function_call_output" and item.get("call_id") == "input-write"]) == 1
                output = final_item({"title": "Input result", "content": "HTTP_ADDITIONAL_REQUIREMENT"})
            self.responses.append([output])
            async for event in super().stream_response(*values, **kwargs):
                yield event
                await asyncio.sleep(0)

    model = InputModel([])
    worker._model = model
    assert await worker.run_once()
    assert len(model.inputs) == (2 if args.phase == "failure-start" else 1)
    print(json.dumps({"phase": args.phase, "model_requests": len(model.inputs)}))


if __name__ == "__main__":
    asyncio.run(main())
