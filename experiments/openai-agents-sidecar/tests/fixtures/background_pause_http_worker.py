"""Exercise the real background Worker and Go HTTP boundaries with a scripted SDK model."""
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

from content_agent_sidecar.backend import BackendClient
from content_agent_sidecar.background_worker import SDKBackgroundTaskWorker
from content_agent_sidecar.config import Settings
from test_stateful_execution import StreamingSequence, final_item, tool_item
from input_attachment_assertions import assert_attached_materials, user_text


ROOT = "skills/background-pause"
CONTENT = "---\nname: background-pause\ndescription: Review supplied notes.\n---\nReport BACKGROUND_PAUSE_RESTORED.\n"


class ObservedBackend(BackendClient):
    def __init__(self, url):
        super().__init__(url, internal_token="pause-service")
        self.observed = asyncio.Event()

    async def update_agent_task_progress(self, *args, **kwargs):
        response = await super().update_agent_task_progress(*args, **kwargs)
        if response.get("data", response).get("status") == "pausing":
            self.observed.set()
        return response


class PauseModel(StreamingSequence):
    def __init__(self, backend, user, task_id, phase):
        super().__init__([])
        self.backend, self.user, self.task_id, self.phase = backend, user, task_id, phase

    async def stream_response(self, *args, **kwargs):
        index = len(self.inputs)
        if self.phase == "failure-write" and index == 1:
            self.responses.append(APIConnectionError(request=httpx.Request("POST", "https://fixture.invalid/responses")))
            async for event in super().stream_response(*args, **kwargs):
                yield event
            return
        if self.phase in {"write", "append", "failure-write"}:
            assert index == 0
            output = tool_item("apply_workspace_patch", {"path": ROOT + "/SKILL.md", "expected_version": 0,
                "operation": "create_file", "diff": "\n".join("+" + line for line in CONTENT.splitlines())}, "write-file")
        elif self.phase == "install":
            if index == 0:
                output = tool_item("validate_workspace_skill", {"root_path": ROOT}, "validate-skill")
            else:
                assert index == 1
                preview = json.loads(next(item["output"] for item in reversed(args[1]) if item.get("type") == "function_call_output"))
                assert preview["status"] == "valid", preview
                output = tool_item("install_workspace_skill", {"root_path": ROOT, "snapshot_hash": preview["snapshot_hash"],
                    "scope": "project", "installation_id": "", "expected_active_version_id": ""}, "install-skill")
        else:
            assert index == 0 and self.phase in {"finish", "approve", "reject", "append_finish", "revise_finish", "attachment_finish"}
            if self.phase in {"append_finish", "revise_finish", "attachment_finish", "approve", "reject"}:
                assert [user_text(item) for item in args[1] if item.get("role") == "user"].count("APPENDED_REQUIREMENT_ONCE") == 1
                assert "REMOVED_BEFORE_MODEL" not in json.dumps(args[1])
            assert_attached_materials(args[1], required=self.phase == "attachment_finish")
            outputs = [item for item in args[1] if item.get("type") == "function_call_output"]
            assert len([item for item in outputs if item.get("call_id") == "write-file"]) == 1
            assert json.loads(next(item["output"] for item in outputs if item.get("call_id") == "write-file"))["file"]["version"] == 1
            if self.phase == "approve":
                installed = json.loads(next(item["output"] for item in outputs if item.get("call_id") == "install-skill"))
                assert installed["installed_as_skill"], installed
            elif self.phase == "reject":
                assert "rejected" in next(item["output"] for item in outputs if item.get("call_id") == "install-skill")
            output = final_item({"summary": "Background resumed", "result": {"phase": self.phase}, "artifact_draft": {
                "artifact_type": "generic_document", "title": "Pause receipt", "payload": {"content": "BACKGROUND_PAUSE_RESTORED"}}})

        if self.phase in {"write", "append"} or (self.phase == "install" and index == 1):
            if self.phase == "append":
                response = await self.user.post(f"/api/v1/agent-tasks/{self.task_id}/inputs", json={"content": "APPENDED_REQUIREMENT_ONCE"}, headers={"Idempotency-Key": str(uuid4())})
                assert response.status_code == 202 and response.json()["data"]["status"] == "received", response.text
            else:
                response = await self.user.post(f"/api/v1/agent-tasks/{self.task_id}/pause", headers={"Idempotency-Key": str(uuid4())})
                assert response.status_code == 202 and response.json()["data"]["status"] == "pausing", response.text
            await asyncio.wait_for(self.backend.observed.wait(), 5)
            await asyncio.sleep(0)
        self.responses.append([output])
        async for event in super().stream_response(*args, **kwargs):
            yield event


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--backend-url", required=True)
    parser.add_argument("--task-id", required=True)
    parser.add_argument("--phase", choices=["write", "failure-write", "install", "finish", "approve", "reject", "append", "append_finish", "revise_finish", "attachment_finish"], required=True)
    args = parser.parse_args()
    url = urlsplit(args.backend_url)
    if url.scheme != "http" or url.hostname != "127.0.0.1" or not url.port or url.port in {8860, 8880}:
        raise ValueError("Only an isolated loopback backend may be tested")
    config = Settings(backend_base_url=args.backend_url, model_base_url=args.backend_url + "/no-model-provider",
        model_api_key="fixture-not-a-credential", model_name="sdk-pause-fixture", model_timeout_seconds=15,
        model_max_output_tokens=1024, model_max_retries=0, run_timeout_seconds=45,
        tracing_enabled=False, internal_token="pause-service", task_worker_lease_seconds=90)
    backend = ObservedBackend(args.backend_url)
    worker = SDKBackgroundTaskWorker(config, backend)
    worker._heartbeat_interval_seconds = 0.01
    async with httpx.AsyncClient(base_url=args.backend_url, headers={"Authorization": "Bearer pause-owner"}, timeout=15) as user:
        model = PauseModel(backend, user, args.task_id, args.phase)
        worker._model = model
        assert await worker.run_once()
        task = (await user.get(f"/api/v1/agent-tasks/{args.task_id}")).json()["data"]
        expected = "paused" if args.phase in {"write", "install", "failure-write"} else "queued" if args.phase == "append" else "completed"
        if args.phase in {"append", "append_finish", "approve", "reject"}:
            assert len(task["additional_inputs"]) == 1 and task["additional_inputs"][0]["status"] == ("received" if args.phase == "append" else "included")
        elif args.phase in {"revise_finish", "attachment_finish"}:
            inputs = task["additional_inputs"]
            assert [item["status"] for item in inputs] == ["withdrawn", "superseded", "included"]
            assert inputs[1]["replacement_input_id"] == inputs[2]["input_id"]
            assert inputs[1]["content"] == "REMOVED_BEFORE_MODEL"
            assert inputs[2]["content"] == "APPENDED_REQUIREMENT_ONCE"
        assert task["status"] == expected, {key: task.get(key) for key in ["status", "failure_code", "failure_message"]}
        assert len(model.inputs) == (2 if args.phase in {"install", "failure-write"} else 1)
        if args.phase == "failure-write":
            assert task["failure_code"] == "SDK_MODEL_RECOVERY_REQUIRED"
        print(json.dumps({"phase": args.phase, "status": task["status"], "model_requests": len(model.inputs)}))


if __name__ == "__main__":
    asyncio.run(main())
