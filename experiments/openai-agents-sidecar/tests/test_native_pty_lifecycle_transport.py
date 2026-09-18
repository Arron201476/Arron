import asyncio
from email.message import Message
import io
import json

import pytest

from content_agent_sidecar.backend import BackendError, backend_activity
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport
from test_native_workspace_transport import KEY, OTHER, PREFIX, SESSION, Reply, lease, response, wire


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_pty_cleanup_real_http_has_execution_identity_and_no_process_input(mode):
    activity = {"agent_turn_id": "turn"} if mode == "main" else {
        "task_attempt_id" if mode == "background" else "execution_attempt_id": "attempt", "attempt_token": "fixture-attempt"}
    with wire(response(lease()), response({"session_id": SESSION, "state": "stopped"})) as (backend, requests):
        async def scenario():
            with backend_activity("project", **activity):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1 if mode == "main" else 0)
                await transport.reserve()
                await transport.terminate_pty()
        asyncio.run(scenario())
        assert len(requests) == 2
        method, path, headers, body = requests[-1]
        assert (method, path, json.loads(body)) == ("POST", PREFIX + SESSION + "/pty/terminate", {})
        assert headers["Authorization"] == "Bearer isolated-service-credential"
        assert headers["X-Agent-Project-ID"] == "project" and headers["X-Workspace-Lease-Key"] == KEY
        assert headers["X-Agent-Dispatch-Generation"] == ("1" if mode == "main" else "0")
        if mode != "main":
            assert headers["X-Agent-Attempt-Token"] == "fixture-attempt"


@pytest.mark.parametrize("answer", [response({"session_id": OTHER, "state": "stopped"}),
    response({"session_id": SESSION, "state": "running"}), response({"session_id": SESSION, "state": "stopped", "extra": True}),
    Reply(b'{"data":'), Reply(b"x" * ((64 << 10) + 1)),
    Reply(b'{"error":{"code":"NATIVE_WORKSPACE_PTY_UNCONFIRMED"}}', status=409),
    Reply(b"", status=302, headers=[("Location", "http://127.0.0.1:1/")])],
    ids=["workspace", "running", "extra", "truncated", "oversize", "unknown", "redirect"])
def test_unknown_cleanup_never_claims_stopped_or_retries(answer):
    with wire(response(lease()), answer) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                with pytest.raises(BackendError):
                    await transport.terminate_pty()
        asyncio.run(scenario())
        assert len(requests) == 2


@pytest.mark.parametrize("suffix", ["pty/terminate", "pty/terminate/extra", "pty/terminate?x=1"])
def test_cleanup_keeps_small_control_request_limit(suffix):
    with wire() as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                with pytest.raises(BackendError, match="control request exceeds"):
                    await backend.native_workspace_request("POST", PREFIX + SESSION + "/" + suffix, {}, payload={"data": "x" * 4096})
        asyncio.run(scenario())
        assert not requests


@pytest.mark.parametrize("suffix,extended", [("pty/terminate", True), ("pty/terminate/extra", False), ("pty/terminate?x=1", False)])
def test_cleanup_timeout_margin_is_limited_to_exact_lifecycle_endpoint(monkeypatch, suffix, extended):
    from content_agent_sidecar import backend as module

    class FakeResponse(io.BytesIO):
        status = 200

        def __init__(self, url):
            super().__init__(b'{"data":{}}')
            self.url = url
            self.headers = Message()
            self.headers["Content-Type"] = "application/json"

        def geturl(self):
            return self.url

    class Opener:
        def open(self, request, *, timeout):
            seen.append(timeout)
            return FakeResponse(request.full_url)

    seen = []
    with wire() as (backend, requests):
        monkeypatch.setattr(module, "build_opener", lambda *handlers: Opener())
        backend._native_workspace_request_sync("POST", PREFIX + SESSION + "/" + suffix, {}, b"{}", False)
        assert seen == [max(backend._timeout_seconds, 145) if extended else backend._timeout_seconds]
        assert not requests
