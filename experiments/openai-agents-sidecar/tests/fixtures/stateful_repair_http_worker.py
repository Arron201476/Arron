"""Exercise repair checkpoints against an isolated real Go backend."""
import argparse
import asyncio
from dataclasses import replace
import json
from pathlib import Path
import sys
from urllib.parse import urlsplit
from uuid import uuid4

import httpx2 as httpx

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from content_agent_sidecar.config import Settings
from content_agent_sidecar.task_worker import SDKTaskWorker
from test_stateful_execution import StreamingSequence, final_item, tool_item


class RepairModel(StreamingSequence):
    def __init__(self, phase, backend_url, run_id, pause_seen):
        super().__init__([])
        self.phase, self.backend_url, self.run_id, self.pause_seen = phase, backend_url, run_id, pause_seen

    async def stream_response(self, *args, **kwargs):
        index = len(self.inputs)
        if self.phase.startswith("start-"):
            if index == 0:
                output = [tool_item("apply_workspace_patch", {"path": "repair-evidence.txt", "expected_version": 0,
                    "operation": "create_file", "diff": "+original execution"}, "write-evidence")]
            elif index == 1:
                receipt = next(item for item in reversed(args[1]) if item.get("type") == "function_call_output")
                assert json.loads(receipt["output"])["file"]["version"] == 1
                output = [final_item({"wrong": "HTTP_REPAIR_CANDIDATE"})]
            else:
                assert self.phase == "start-native" and index == 2 and not args[3]
                output = []
            pause = index == (1 if self.phase == "start-zero" else 2)
        else:
            assert self.phase in {"resume", "output-guardrail"} and index == 0 and not args[3]
            assert json.loads(args[1][0]["content"])["candidate"] == '{"wrong": "HTTP_REPAIR_CANDIDATE"}'
            output = [final_item({"title": "Repaired review", "content": "HTTP_REPAIR_COMPLETED"})]
            pause = False
        self.responses.append(output)
        if pause:
            async with httpx.AsyncClient(timeout=10) as client:
                response = await client.post(f"{self.backend_url}/api/v1/runs/{self.run_id}/pause", json={},
                    headers={"Idempotency-Key": str(uuid4())})
                assert response.status_code == 202, response.text
            await asyncio.wait_for(self.pause_seen.wait(), 5)
        async for event in super().stream_response(*args, **kwargs):
            yield event
            await asyncio.sleep(0)


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--backend-url", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--phase", choices=["start-zero", "start-native", "resume", "guardrail", "output-guardrail"], required=True)
    args = parser.parse_args()
    url = urlsplit(args.backend_url)
    if url.scheme != "http" or url.hostname != "127.0.0.1" or not url.port or url.port in {8860, 8880}:
        raise ValueError("Only an isolated loopback backend may be tested")
    settings = Settings(backend_base_url=args.backend_url, model_base_url=args.backend_url + "/no-model-provider",
        model_api_key="fixture-not-a-credential", model_name="sdk-repair-fixture", model_timeout_seconds=15,
        model_max_output_tokens=1024, model_max_retries=0, run_timeout_seconds=60,
        tracing_enabled=False, internal_token="sdk-http-test-internal", task_worker_lease_seconds=90)
    if args.phase == "guardrail":
        settings = replace(settings, guardrail_max_input_chars=1)
    elif args.phase == "output-guardrail":
        settings = replace(settings, guardrail_max_output_chars=1)
    worker = SDKTaskWorker(settings)
    pause_seen = asyncio.Event()
    heartbeat = worker._backend.heartbeat_execution_attempt
    async def observe_pause(*values, **kwargs):
        receipt = await heartbeat(*values, **kwargs)
        if receipt.get("data", receipt).get("pause_requested"):
            pause_seen.set()
        return receipt
    worker._backend.heartbeat_execution_attempt = observe_pause
    if args.phase.startswith("start-"):
        worker._heartbeat_interval_seconds = 1.0
    model = RepairModel(args.phase, args.backend_url, args.run_id, pause_seen)
    worker._model = model
    assert await worker.run_once()
    expected = {"start-zero": 2, "start-native": 3, "resume": 1, "guardrail": 0, "output-guardrail": 1}[args.phase]
    assert len(model.inputs) == expected, (args.phase, len(model.inputs))
    print(json.dumps({"phase": args.phase, "model_requests": len(model.inputs)}))


if __name__ == "__main__":
    asyncio.run(main())
