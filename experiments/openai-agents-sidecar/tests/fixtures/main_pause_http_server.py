"""Real Sidecar/SDK main-turn pause against an isolated Go backend, without an external model."""
import argparse
import asyncio
import json
from pathlib import Path
import socket
import sys
import time
from urllib.parse import urlsplit
from uuid import uuid4

import httpx2 as httpx
import uvicorn
from agents import SQLiteSession
from openai import APIConnectionError

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from content_agent_sidecar.app import create_app
from content_agent_sidecar.backend import BackendClient
from content_agent_sidecar.config import Settings
from content_agent_sidecar.runtime import OpenAIAgentsRuntime, RequiredCommitModelAdapter
from test_stateful_execution import StreamingSequence, tool_item
from input_attachment_assertions import assert_attached_materials, user_text

print(f"fixture imports ready at {time.monotonic():.3f}", file=sys.stderr, flush=True)


CONTENT = "---\nname: main-pause\ndescription: Inspect supplied notes.\n---\nMAIN_PAUSE_RESTORED\n"
ADDITIONAL = "Preserve the original file and apply this additional requirement."
LATE = "This arrived after the final model request."


class MainPauseModel(StreamingSequence):
    def __init__(self, args, observed):
        super().__init__([])
        self.args, self.observed = args, observed

    async def stream_response(self, *args, **kwargs):
        if self.args.phase == "failure-write":
            assert len(self.inputs) < 2
            output = APIConnectionError(request=httpx.Request("POST", "https://fixture.invalid/responses")) if self.inputs else [tool_item(
                "apply_workspace_patch", {"path": "skills/main-pause/SKILL.md", "expected_version": 0, "operation": "create_file",
                "diff": "\n".join("+" + line for line in CONTENT.splitlines())}, "write-file")]
            self.responses.append(output)
            async for event in super().stream_response(*args, **kwargs):
                yield event
            return
        assert not self.inputs or (self.args.phase == "input-auto" and len(self.inputs) == 1), "original execution or a completed tool was replayed"
        if self.args.phase in {"write", "input-write"} or (self.args.phase == "input-auto" and not self.inputs):
            async with httpx.AsyncClient(base_url=self.args.backend_url, headers={"Authorization": "Bearer pause-owner"}, timeout=10) as user:
                if self.args.phase.startswith("input-"):
                    response = await user.post(f"/api/v1/agent-turns/{self.args.turn_id}/inputs", json={"content": ADDITIONAL}, headers={"Idempotency-Key": str(uuid4())})
                    assert response.status_code == 202 and response.json()["data"]["status"] == "received", response.text
                if self.args.phase != "input-auto":
                    response = await user.post(f"/api/v1/agent-turns/{self.args.turn_id}/pause", json={}, headers={"Idempotency-Key": str(uuid4())})
                    assert response.status_code == 200 and response.json()["data"]["status"] == "pausing", response.text
            await asyncio.wait_for(self.observed.wait(), 10)
            await asyncio.sleep(0)
            output = tool_item("apply_workspace_patch", {"path": "skills/main-pause/SKILL.md", "expected_version": 0,
                "operation": "create_file", "diff": "\n".join("+" + line for line in CONTENT.splitlines())}, "write-file")
        else:
            outputs = [item for item in args[1] if item.get("type") == "function_call_output" and item.get("call_id") == "write-file"]
            assert len(outputs) == 1 and json.loads(outputs[0]["output"])["file"]["version"] == 1, outputs
            if self.args.phase.startswith("input-"):
                messages = [user_text(item) for item in args[1] if item.get("role") == "user"]
                assert messages.count(ADDITIONAL) == 1 and LATE not in messages, messages
                assert "REMOVED_BEFORE_MODEL" not in json.dumps(args[1])
                assert_attached_materials(args[1])
            if self.args.phase == "input-auto":
                async with httpx.AsyncClient(base_url=self.args.backend_url, headers={"Authorization": "Bearer pause-owner"}, timeout=10) as user:
                    response = await user.post(f"/api/v1/agent-turns/{self.args.turn_id}/inputs", json={"content": LATE}, headers={"Idempotency-Key": str(uuid4())})
                    assert response.status_code == 202 and response.json()["data"]["status"] == "received", response.text
            output = tool_item("commit_agent_action", {"decision": {"intent": "chat", "reply": "MAIN_PAUSE_RESTORED", "confidence": 1}}, "commit")
        self.responses.append([output])
        async for event in super().stream_response(*args, **kwargs):
            yield event


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--backend-url", required=True)
    parser.add_argument("--turn-id", required=True)
    parser.add_argument("--project-id", required=True)
    parser.add_argument("--conversation-id", required=True)
    parser.add_argument("--session-db", required=True)
    parser.add_argument("--phase", choices=["write", "resume", "input-write", "input-resume", "input-auto", "failure-write", "failure-resume"], required=True)
    args = parser.parse_args()
    url = urlsplit(args.backend_url)
    if url.scheme != "http" or url.hostname != "127.0.0.1" or not url.port or url.port in {8860, 8880}:
        raise ValueError("Only an isolated loopback backend may be tested")
    config = Settings(backend_base_url=args.backend_url, model_base_url=args.backend_url + "/no-model-provider",
        model_api_key="fixture-not-a-credential", model_name="gpt-main-pause-fixture", model_timeout_seconds=10,
        model_max_output_tokens=1024, model_max_retries=0, run_timeout_seconds=40,
        tracing_enabled=False, internal_token="pause-service", session_db_path=args.session_db)
    runtime = OpenAIAgentsRuntime(config, BackendClient(args.backend_url, internal_token="pause-service"))
    print(f"fixture runtime ready at {time.monotonic():.3f}", file=sys.stderr, flush=True)
    observed = asyncio.Event()
    model = MainPauseModel(args, observed)
    runtime._execution_agent.model = model
    runtime._commit_repair_agent.model = RequiredCommitModelAdapter(StreamingSequence([]))
    app = create_app(config, runtime)
    print(f"fixture app ready at {time.monotonic():.3f}", file=sys.stderr, flush=True)

    @app.middleware("http")
    async def observe_pause(request, call_next):
        response = await call_next(request)
        if request.url.path == f"/internal/v1/agent/runs/{args.turn_id}/pause" and response.status_code == 200:
            observed.set()
        return response

    @app.get("/internal/v1/fixture/state")
    async def state():
        session = SQLiteSession(f"{args.project_id}:{args.conversation_id}", args.session_db)
        try:
            items = await session.get_items()
        finally:
            session.close()
        return {"model_requests": len(model.inputs), "user_items": len([item for item in items if item.get("role") == "user"]),
                "additional_user_items": len([item for item in items if item.get("role") == "user" and user_text(item) == ADDITIONAL]),
                "native_attachment_count": assert_attached_materials(model.inputs[-1]) if model.inputs else 0,
                "late_user_items": len([item for item in items if item.get("role") == "user" and item.get("content") == LATE]),
                "file_outputs": len([item for item in items if item.get("type") == "function_call_output" and item.get("call_id") == "write-file"])}

    @app.post("/internal/v1/fixture/stop")
    async def stop():
        server.should_exit = True
        return {"stopping": True}

    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
        assert port not in {8860, 8880}
        server = uvicorn.Server(uvicorn.Config(app, log_level="warning", access_log=False, timeout_graceful_shutdown=5))
        print(json.dumps({"url": f"http://127.0.0.1:{port}"}), flush=True)
        try:
            await server.serve(sockets=[listener])
        finally:
            await runtime._model_client.close()


if __name__ == "__main__":
    asyncio.run(main())
