import asyncio
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Thread
from urllib.parse import parse_qs, urlsplit

import pytest

from content_agent_sidecar.backend import BackendClient, backend_activity


def test_workspace_file_transport_keeps_project_version_and_activity_identity():
    received = []

    class Handler(BaseHTTPRequestHandler):
        def handle_call(self):
            body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
            received.append((self.path, dict(self.headers), json.loads(body) if body else None))
            data = [] if self.path.endswith("/files") else {"path": "draft/a & b.txt"}
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"data": data}).encode())

        do_GET = handle_call
        do_POST = handle_call

        def log_message(self, *_args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        backend = BackendClient(f"http://127.0.0.1:{server.server_port}", internal_token="file-service-fixture")
        patch = {"path": "draft/a & b.txt", "expected_version": 2, "operation": "update_file", "diff": "@@\n-before\n+after"}
        skill_args = {"root_path": "draft/a & b", "snapshot_hash": "snapshot", "scope": "project", "installation_id": "", "expected_active_version_id": ""}

        async def run():
            with backend_activity("project-files", agent_turn_id="turn-files"):
                assert await backend.list_workspace_files("project-files") == []
                await backend.read_workspace_file("project-files", patch["path"], 2, 4, 20)
                await backend.apply_workspace_patch("call-files", "sdk-files", patch, "after")
                await backend.preview_workspace_skill("project-files", skill_args["root_path"])
                await backend.install_workspace_skill("call-skill", "sdk-skill", skill_args)
                await backend.get_artifact_delivery("av/exact & version")
                await backend.list_execution_targets("project-files", "run", "run & 1")
                await backend.inspect_execution_controls("project-files", "run", "run & 1")
                await backend.control_execution("call-control", "sdk-control", {"target_type": "run", "target_id": "run & 1", "action_id": "pause_run", "snapshot_hash": "exact"})

        asyncio.run(run())
        assert len(received) == 9
        for _, headers, _ in received:
            headers = {key.lower(): value for key, value in headers.items()}
            assert headers["authorization"] == "Bearer file-service-fixture"
            assert headers["x-agent-project-id"] == "project-files"
            assert headers["x-agent-turn-id"] == "turn-files"
        assert parse_qs(urlsplit(received[1][0]).query) == {"path": [patch["path"]], "version": ["2"], "offset": ["4"], "limit": ["20"]}
        assert received[2][0] == "/internal/v1/agent-tool-calls/call-files/project-file-patches"
        assert received[2][2] == {"sdk_tool_call_id": "sdk-files", "patch": patch, "content": "after"}
        assert urlsplit(received[3][0]).path == "/api/v1/projects/project-files/skill-drafts/preview"
        assert parse_qs(urlsplit(received[3][0]).query) == {"root_path": [skill_args["root_path"]]}
        assert received[4][0] == "/internal/v1/agent-tool-calls/call-skill/skill-installations"
        assert received[4][2] == {"sdk_tool_call_id": "sdk-skill", "arguments": skill_args}
        assert received[5][0] == "/api/v1/artifact-versions/av%2Fexact%20%26%20version/delivery"
        assert parse_qs(urlsplit(received[6][0]).query) == {"target_type": ["run"], "after_id": ["run & 1"]}
        assert parse_qs(urlsplit(received[7][0]).query) == {"target_type": ["run"], "target_id": ["run & 1"]}
        assert received[8][0] == "/internal/v1/agent-tool-calls/call-control/execution-control"
        assert received[8][2] == {"sdk_tool_call_id": "sdk-control", "arguments": {"target_type": "run", "target_id": "run & 1", "action_id": "pause_run", "snapshot_hash": "exact"}}
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


@pytest.mark.parametrize("kind", ["execution", "background"])
def test_task_claim_sends_service_authentication_over_http(kind):
    received = []

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            received.append((self.path, self.headers.get("Authorization")))
            self.rfile.read(int(self.headers.get("Content-Length", 0)))
            self.send_response(204 if self.headers.get("Authorization") == "Bearer service-fixture" else 401)
            self.end_headers()

        def log_message(self, *_args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        backend = BackendClient(f"http://127.0.0.1:{server.server_port}", internal_token="service-fixture")
        args = dict(worker_id="fixture", provider_id="provider", model_id="model", lease_seconds=60)
        if kind == "execution":
            result = asyncio.run(backend.claim_execution_task(**args, executor_ids=["worker.structured_content"]))
            path = "/internal/v1/executor/task-claims"
        else:
            result = asyncio.run(backend.claim_agent_task(**args))
            path = "/internal/v1/agent-tasks/claims"
        assert result is None
        assert received == [(path, "Bearer service-fixture")]
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def test_stateful_heartbeat_http_uses_service_token_and_frozen_attempt():
    received = []

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            received.append((self.path, self.headers.get("Authorization"), self.headers.get("X-Attempt-Token"),
                json.loads(self.rfile.read(int(self.headers["Content-Length"])))))
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"data":{"attempt_id":"stateful","status":"running"}}')

        def log_message(self, *_args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        backend = BackendClient(f"http://127.0.0.1:{server.server_port}", internal_token="service-fixture")
        claim = {"attempt": {"attempt_id": "stateful/attempt", "input_snapshot_hash": "frozen-input"}, "attempt_token": "attempt-fixture"}
        result = asyncio.run(backend.heartbeat_execution_attempt(claim, lease_seconds=60))
        assert result["data"]["status"] == "running"
        assert received == [("/internal/v1/executor/attempts/stateful%2Fattempt/heartbeat", "Bearer service-fixture", "attempt-fixture",
            {"input_snapshot_hash": "frozen-input", "lease_seconds": 60})]
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def test_backend_activity_is_per_run_and_propagates_to_threads():
    backend = BackendClient("http://unused", internal_token="service-fixture")

    async def run(project, turn):
        with backend_activity(project, agent_turn_id=turn):
            await asyncio.sleep(0)
            return await asyncio.to_thread(backend._internal_headers)

    async def concurrent_runs():
        return await asyncio.gather(run("project-a", "turn-a"), run("project-b", "turn-b"))

    a, b = asyncio.run(concurrent_runs())
    assert a["X-Agent-Project-ID"] == "project-a"
    assert a["X-Agent-Turn-ID"] == "turn-a"
    assert b["X-Agent-Project-ID"] == "project-b"
    assert b["X-Agent-Turn-ID"] == "turn-b"
    assert backend._internal_headers() == {"Authorization": "Bearer service-fixture"}


def test_stateful_approval_checkpoint_http_preserves_sdk_and_worker_state():
    received = []

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            received.append((self.path, self.headers.get("Authorization"), self.headers.get("X-Attempt-Token"),
                json.loads(self.rfile.read(int(self.headers["Content-Length"])))))
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"data":{"attempt_id":"stateful","status":"waiting_approval"}}')

        def log_message(self, *_args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        backend = BackendClient(f"http://127.0.0.1:{server.server_port}", internal_token="service-fixture")
        claim = {"attempt": {"attempt_id": "stateful/attempt", "input_snapshot_hash": "frozen-input"}, "attempt_token": "attempt-fixture"}
        state = {"$schemaVersion": "1.16", "context": {"context": {"execution_attempt_id": "stateful/attempt"}}}
        worker = {"schema_version": "fixture.v1", "phase": "generate", "source_cursor": 40}
        result = asyncio.run(backend.pause_execution_for_approval(claim, run_state=state, schema_version="1.16",
            pending_sdk_tool_call_ids=["sdk-call"], worker_state=worker))
        assert result["data"]["status"] == "waiting_approval"
        assert received == [("/internal/v1/executor/attempts/stateful%2Fattempt/approval-checkpoint", "Bearer service-fixture", "attempt-fixture",
            {"input_snapshot_hash": "frozen-input", "schema_version": "1.16", "run_state": state,
             "pending_sdk_tool_call_ids": ["sdk-call"], "worker_state": worker})]
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def test_nested_activity_resets_on_error_and_keeps_attempt_token_private():
    backend = BackendClient("http://unused", internal_token="service-fixture")
    with backend_activity("project", agent_turn_id="turn"):
        with pytest.raises(RuntimeError):
            with backend_activity("project", task_attempt_id="attempt", attempt_token="private"):
                assert backend._internal_headers()["X-Agent-Attempt-Token"] == "private"
                assert "X-Agent-Turn-ID" not in backend._internal_headers()
                raise RuntimeError("abort")
        assert backend._internal_headers()["X-Agent-Turn-ID"] == "turn"
        assert "X-Agent-Attempt-Token" not in backend._internal_headers()
    assert backend._internal_headers() == {"Authorization": "Bearer service-fixture"}


@pytest.mark.parametrize("fields", [{}, {"agent_turn_id": "turn", "task_attempt_id": "attempt", "attempt_token": "token"}, {"task_attempt_id": "attempt"}, {"agent_turn_id": "turn", "attempt_token": "token"}, {"execution_attempt_id": "attempt"}, {"execution_attempt_id": "stateful", "task_attempt_id": "background", "attempt_token": "token"}, {"execution_attempt_id": "stateful", "agent_turn_id": "turn", "attempt_token": "token"}])
def test_invalid_activity_is_rejected(fields):
    with pytest.raises(ValueError):
        with backend_activity("project", **fields):
            pytest.fail("invalid activity accepted")


def test_stateful_activity_transport_binds_call_without_background_identity():
    received = []

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            received.append((self.path, dict(self.headers), json.loads(self.rfile.read(int(self.headers["Content-Length"])))))
            self.send_response(201)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"data":{"agent_tool_call_id":"stateful-call","status":"running"}}')

        def log_message(self, *_args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        backend = BackendClient(f"http://127.0.0.1:{server.server_port}", internal_token="stateful-service")

        async def run():
            with backend_activity("project", execution_attempt_id="stateful", attempt_token="private-token"):
                result = await backend.begin_agent_tool_call(project_id="project", conversation_id="conversation", agent_turn_id="",
                    execution_attempt_id="stateful", attempt_token="private-token", sdk_tool_call_id="sdk-call",
                    tool_id="runtime:inspect_project", arguments={})
                assert result["agent_tool_call_id"] == "stateful-call"
            assert backend._internal_headers() == {"Authorization": "Bearer stateful-service"}

        asyncio.run(run())
        path, headers, body = received[0]
        headers = {key.lower(): value for key, value in headers.items()}
        assert path == "/internal/v1/agent-tool-calls"
        assert headers["x-agent-execution-attempt-id"] == body["execution_attempt_id"] == "stateful"
        assert headers["x-agent-attempt-token"] == body["attempt_token"] == "private-token"
        assert "x-agent-task-attempt-id" not in headers and not body["agent_task_attempt_id"]
        assert "x-agent-turn-id" not in headers and not body["agent_turn_id"]
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)
