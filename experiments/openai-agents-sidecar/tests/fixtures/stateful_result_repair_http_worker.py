"""Actual SDK generation and formal Go business rejection on an isolated backend."""
import argparse
import asyncio
from copy import deepcopy
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


def source_analysis(pack, invalid=False):
    sources = pack["task_cursor"]["batch"]["source_units"]
    units = []
    for source in sources:
        reference = {key: source[key] for key in ("asset_id", "asset_snapshot_id", "source_unit_id")}
        reference.update(source_type="asset_text_range", range_label=source["source_unit_id"])
        units.append({"source_unit_id": source["source_unit_id"], "summary": "The reporter discovers evidence.",
            "key_events": ["Evidence discovered"], "character_changes": [], "conflict_stage": "discovery",
            "hook_or_suspense_potential": "medium", "source_refs": [reference], "claims": []})
    return {"source_kind": "novel", "units": units,
        "coverage_check": {"covered_source_unit_ids": ["FOREIGN_REPAIR_UNIT"] if invalid else [item["source_unit_id"] for item in sources],
            "missing_source_unit_ids": [], "order_issues": []},
        "source_trace": {"grounded": [], "inferred": [], "claims": []}}


class FormalRepairModel(StreamingSequence):
    async def stream_response(self, *args, **kwargs):
        index = len(self.inputs)
        if self.phase == "start":
            assert not self.claim.get("repair") and index < 2
            output = [tool_item("apply_workspace_patch", {"path": "formal-repair-evidence.txt", "operation": "create_file",
                "expected_version": 0, "diff": "+original execution"}, "formal-repair-write")] if index == 0 else [final_item(source_analysis(self.claim["context_pack"], invalid=True))]
        else:
            assert index == 0 and not args[3]
            repair = self.claim["repair"]
            assert repair["error_code"] == "BATCH_COVERAGE_INVALID"
            assert json.loads(args[1][0]["content"])["candidate"] == repair["candidate"]
            assert "FOREIGN_REPAIR_UNIT" in repair["candidate"]
            output = [] if self.phase == "pause" else [final_item(source_analysis(self.claim["context_pack"], invalid=self.phase == "reject"))]
        self.responses.append(output)
        if self.phase == "pause":
            async with httpx.AsyncClient(timeout=10) as client:
                response = await client.post(f"{self.backend_url}/api/v1/runs/{self.run_id}/pause", json={}, headers={"Idempotency-Key": str(uuid4())})
                assert response.status_code == 202, response.text
            await asyncio.wait_for(self.pause_seen.wait(), 5)
        async for event in super().stream_response(*args, **kwargs):
            yield event
            await asyncio.sleep(0)


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--backend-url", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--phase", required=True, choices=["start", "pause", "finish", "reject"])
    args = parser.parse_args()
    url = urlsplit(args.backend_url)
    if url.scheme != "http" or url.hostname != "127.0.0.1" or not url.port or url.port in {8860, 8880}:
        raise ValueError("Only an isolated loopback backend may be tested")
    settings = Settings(backend_base_url=args.backend_url, model_base_url=args.backend_url + "/no-model-provider",
        model_api_key="fixture-not-a-credential", model_name="sdk-formal-repair-fixture", model_timeout_seconds=15,
        model_max_output_tokens=4096, model_max_retries=0, run_timeout_seconds=60, tracing_enabled=False,
        internal_token="sdk-http-test-internal", task_worker_lease_seconds=90)
    worker = SDKTaskWorker(settings)
    model = FormalRepairModel([])
    model.phase, model.backend_url, model.run_id = args.phase, args.backend_url, args.run_id
    model.pause_seen = asyncio.Event()
    claim_task = worker._backend.claim_execution_task
    async def observe_claim(**kwargs):
        claim = await claim_task(**kwargs)
        assert claim and claim["attempt"]["run_id"] == args.run_id
        model.claim = deepcopy(claim)
        return claim
    worker._backend.claim_execution_task = observe_claim
    heartbeat = worker._backend.heartbeat_execution_attempt
    async def observe_pause(*values, **kwargs):
        result = await heartbeat(*values, **kwargs)
        if result.get("data", result).get("pause_requested"):
            model.pause_seen.set()
        return result
    worker._backend.heartbeat_execution_attempt = observe_pause
    commit = worker._backend.commit_execution_result
    statuses = []
    async def observe_commit(*values):
        result = await commit(*values)
        status = result.get("data", result)["commit_status"]
        statuses.append(status)
        if status in {"output_repair_queued", "output_repair_failed"}:
            duplicate = await commit(*values)
            assert duplicate.get("data", duplicate)["commit_status"] == status
        return result
    worker._backend.commit_execution_result = observe_commit
    if args.phase == "pause":
        worker._heartbeat_interval_seconds = 1.0
    worker._model = model
    assert await worker.run_once()
    assert len(model.inputs) == (2 if args.phase == "start" else 1), (args.phase, len(model.inputs))
    expected = {"start": ["output_repair_queued"], "pause": [], "finish": ["task_checkpointed"], "reject": ["output_repair_failed"]}
    assert statuses == expected[args.phase], statuses
    print(json.dumps({"phase": args.phase, "model_requests": len(model.inputs), "commit_statuses": statuses}))


if __name__ == "__main__":
    asyncio.run(main())
