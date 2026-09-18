"""Run one real Worker against an isolated Go HTTP server with a scripted SDK model."""
import argparse
import asyncio
import json
from pathlib import Path
import sys
from uuid import uuid4
from urllib.parse import urlsplit

import httpx2 as httpx

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from content_agent_sidecar.config import Settings
from content_agent_sidecar.task_worker import SDKTaskWorker
from test_stateful_execution import StreamingSequence, final_item, tool_item


ROOT = "skills/http-authored"
CONTENT = "---\nname: http-authored\ndescription: Review a supplied story.\n---\nReport HTTP_SDK_AUTHORED using the supplied story.\n"


def latest_tool_output(items):
    item = next(item for item in reversed(items) if item.get("type") == "function_call_output")
    return json.loads(item["output"])


class AuthorModel(StreamingSequence):
    def __init__(self, phase, backend_url="", run_id="", pause_seen=None):
        super().__init__([])
        self.phase = phase
        self.backend_url, self.run_id, self.pause_seen = backend_url, run_id, pause_seen

    async def stream_response(self, *args, **kwargs):
        index = len(self.inputs) + (1 if self.phase == "after-pause" else 0)
        if self.phase in {"author", "pause-author", "after-pause"}:
            if index == 0:
                output = tool_item("apply_workspace_patch", {"path": ROOT + "/SKILL.md", "expected_version": 0,
                    "operation": "create_file", "diff": "\n".join("+" + line for line in CONTENT.splitlines())}, "write-skill")
            elif index == 1:
                receipt = latest_tool_output(args[1])
                assert receipt["file"]["version"] == 1, receipt
                output = tool_item("validate_workspace_skill", {"root_path": ROOT}, "validate-skill")
            elif index == 2:
                preview = latest_tool_output(args[1])
                assert preview["status"] == "valid", preview
                output = tool_item("install_workspace_skill", {"root_path": ROOT, "snapshot_hash": preview["snapshot_hash"],
                    "scope": "project", "installation_id": "", "expected_active_version_id": ""}, "install-skill")
            else:
                raise AssertionError("Worker continued past the unapproved installation")
        elif self.phase == "approve" and index == 0:
            installed = latest_tool_output(args[1])
            assert installed["installed_as_skill"] and installed["ready_in_current_turn"], installed
            output = tool_item("load_skill_instructions", {"capability_id": "http_authored"}, "load-authored-skill")
        else:
            if self.phase == "approve":
                assert index == 1
                assert "HTTP_SDK_AUTHORED" in json.dumps(latest_tool_output(args[1]))
            else:
                assert self.phase == "reject" and index == 0
                assert "rejected" in json.dumps(args[1]).lower()
            output = final_item({"title": "HTTP SDK review", "content": "Verified isolated Skill author workflow."})
        self.responses.append([output])
        if self.phase == "pause-author":
            assert index == 0
            async with httpx.AsyncClient(timeout=10) as client:
                response = await client.post(f"{self.backend_url}/api/v1/runs/{self.run_id}/pause", json={},
                    headers={"Idempotency-Key": str(uuid4())})
                assert response.status_code == 202, response.text
                assert response.json()["data"]["run"]["status"] == "pausing"
            await asyncio.wait_for(self.pause_seen.wait(), 5)
        async for event in super().stream_response(*args, **kwargs):
            yield event
            if self.phase == "pause-author":
                await asyncio.sleep(0)


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--backend-url", required=True)
    parser.add_argument("--phase", choices=["author", "approve", "reject", "pause-author", "after-pause"], required=True)
    parser.add_argument("--run-id", default="")
    args = parser.parse_args()
    url = urlsplit(args.backend_url)
    if url.scheme != "http" or url.hostname != "127.0.0.1" or not url.port or url.port in {8860, 8880}:
        raise ValueError("Only an isolated loopback backend may be tested")
    settings = Settings(backend_base_url=args.backend_url, model_base_url=args.backend_url + "/no-model-provider",
        model_api_key="fixture-not-a-credential", model_name="sdk-http-fixture", model_timeout_seconds=15,
        model_max_output_tokens=1024, model_max_retries=0, run_timeout_seconds=60,
        tracing_enabled=False, internal_token="sdk-http-test-internal", task_worker_lease_seconds=90)
    worker = SDKTaskWorker(settings)
    pause_seen = asyncio.Event()
    heartbeat = worker._backend.heartbeat_execution_attempt
    async def observe_pause(*values, **kwargs):
        receipt = await heartbeat(*values, **kwargs)
        if receipt.get("data", receipt).get("pause_requested"):
            pause_seen.set()
        return receipt
    worker._backend.heartbeat_execution_attempt = observe_pause
    if args.phase == "pause-author":
        worker._heartbeat_interval_seconds = 1.0
    model = AuthorModel(args.phase, args.backend_url, args.run_id, pause_seen)
    worker._model = model
    assert await worker.run_once()
    expected = 3 if args.phase == "author" else 2 if args.phase in {"approve", "after-pause"} else 1
    assert len(model.inputs) == expected, (args.phase, len(model.inputs))
    print(json.dumps({"phase": args.phase, "model_requests": len(model.inputs)}))


if __name__ == "__main__":
    asyncio.run(main())
