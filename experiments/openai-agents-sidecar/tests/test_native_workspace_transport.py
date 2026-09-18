import asyncio
from collections import deque
from contextlib import contextmanager
from dataclasses import dataclass, field
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import json
import threading
from urllib.error import HTTPError

import pytest
from agents.sandbox.session.dependencies import Dependencies
from agents.sandbox.snapshot import SnapshotBase

from content_agent_sidecar.backend import BackendClient, BackendError, backend_activity, backend_memory_activity
from content_agent_sidecar.native_snapshot import (
    MAX_ARCHIVE_BYTES, SNAPSHOT_DEPENDENCY, NativeSnapshotError, PendingSnapshotSave,
    RuntimeSnapshotBinding, RuntimeWorkspaceSnapshot, SnapshotReference,
)
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport


SESSION = "c3e8589b-7b31-42f4-9a22-8e9cd6ad3099"
OTHER = "057d3ba5-9582-4bbb-b68a-ad738119f674"
KEY = "a" * 64
REQUEST_ID = "b" * 32
PREFIX = "/internal/v1/native-workspaces/"
BINARY = b"wire-only binary fixture\x00\xff\x80"
DIGEST = hashlib.sha256(BINARY).hexdigest()


def lease(**changes):
    return {"session_id": SESSION, "generation": 1, "lease_until": "2030-01-01T00:01:00Z",
            "snapshot_version": 0, **changes}


def receipt(**changes):
    return {"session_id": SESSION, "version": 1, "sha256": DIGEST, **changes}


@dataclass
class Reply:
    body: bytes
    status: int = 200
    content_type: str = "application/json"
    headers: list[tuple[str, str]] = field(default_factory=list)


def response(data):
    return Reply(json.dumps({"data": data}).encode())


def archive_reply(data=BINARY, version=1, **changes):
    headers = {"X-Workspace-Session-ID": SESSION, "X-Workspace-Snapshot-Version": str(version),
               "X-Workspace-Snapshot-Sha256": hashlib.sha256(data).hexdigest(), **changes}
    return Reply(data, content_type="application/x-tar", headers=list(headers.items()))


@contextmanager
def wire(*replies):
    """A real loopback HTTP transport fixture, not a substitute for Go Store tests."""
    pending, requests, errors = deque(replies), [], []

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def handle_request(self):
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if length > MAX_ARCHIVE_BYTES:
                    raise AssertionError("client sent an oversized request")
                requests.append((self.command, self.path, self.headers, self.rfile.read(length)))
                answer = pending.popleft()
                if callable(answer):
                    answer = answer(requests[-1])
                self.send_response(answer.status)
                self.send_header("Content-Type", answer.content_type)
                self.send_header("Content-Length", str(len(answer.body)))
                for name, value in answer.headers:
                    self.send_header(name, value)
                self.end_headers()
                try:
                    self.wfile.write(answer.body)
                except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
                    pass
            except Exception as exc:
                errors.append(exc)
                self.close_connection = True

        do_GET = do_POST = do_PUT = handle_request

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
    thread.start()
    client = BackendClient(f"http://127.0.0.1:{server.server_port}", timeout_seconds=2,
                           internal_token="isolated-service-credential")
    try:
        yield client, requests
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=3)
        assert not thread.is_alive()
        assert not errors


@pytest.mark.parametrize("mode", ["main", "background", "stateful", "memory"])
def test_workspace_real_http_all_execution_modes_and_binary_contract(mode):
    activity = {"agent_turn_id": "turn-1"} if mode == "main" else {
        "task_attempt_id" if mode == "background" else "execution_attempt_id": "attempt-1",
        "attempt_token": "isolated-attempt-credential",
    }
    with wire(response({"lease": None}), response(lease()), response(lease()),
              response({"session_id": SESSION, "state": "ready"}), archive_reply(version=0),
              response(receipt()), archive_reply(), response({"receipt": receipt()}),
              response({"session_id": SESSION, "state": "ready"})) as (backend, requests):
        async def scenario():
            scope = backend_memory_activity("project-1", "generation-1", 2, "isolated-attempt-credential") if mode == "memory" else backend_activity("project-1", **activity)
            with scope:
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1 if mode == "main" else 0)
                assert await transport.current() is None
                assert (await transport.reserve()).session_id == SESSION
                await transport.validate()
                await transport.ensure()
                data = await transport.export_workspace()
                assert data == BINARY
                ref = await transport.save_snapshot(SESSION, REQUEST_ID, 0, data)
                assert await transport.read_snapshot(SESSION, ref) == data
                pending = PendingSnapshotSave(request_id=REQUEST_ID, parent_version=0, sha256=DIGEST)
                assert await transport.read_snapshot_receipt(SESSION, pending) == ref
                await transport.restore_environment("c" * 32, ref)
        asyncio.run(scenario())
        assert len(requests) == 9
        for method, path, headers, body in requests:
            assert headers["Authorization"] == "Bearer isolated-service-credential"
            assert headers["X-Agent-Project-ID"] == "project-1"
            assert headers["X-Workspace-Lease-Key"] == KEY
            assert headers["X-Agent-Dispatch-Generation"] == ("1" if mode == "main" else "0")
            assert headers["Accept-Encoding"] == "identity"
            assert (headers["X-Agent-Turn-ID"] == "turn-1") == (mode == "main")
            if mode != "main":
                assert headers["X-Agent-Attempt-Token"] == "isolated-attempt-credential"
            if mode == "memory":
                assert headers["X-Agent-Memory-Generation-ID"] == "generation-1"
                assert headers["X-Agent-Memory-Generation-Attempt"] == "2"
                assert headers["X-Agent-Task-Attempt-ID"] is None
                assert headers["X-Agent-Execution-Attempt-ID"] is None
            assert KEY not in path and KEY.encode() not in body
        method, path, headers, body = requests[5]
        assert (method, path, body) == ("PUT", PREFIX + SESSION + "/snapshots/" + REQUEST_ID, BINARY)
        assert headers["Content-Type"] == "application/x-tar"
        assert headers["X-Workspace-Snapshot-Parent"] == "0"
        assert headers["X-Workspace-Snapshot-Sha256"] == DIGEST
        assert json.loads(requests[-1][3]) == {"request_id": "c" * 32, "version": 1, "sha256": DIGEST}


def test_workspace_transport_cannot_rebind_execution_or_inject_authority():
    with wire(response(lease())) as (backend, requests):
        async def scenario():
            with pytest.raises(BackendError):
                NativeWorkspaceHTTPTransport(backend, KEY, 1)
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                for headers in ({"Authorization": "replacement"}, {"X-Agent-Turn-ID": "turn-2"}):
                    with pytest.raises(BackendError):
                        await backend.native_workspace_request("GET", PREFIX + "current", headers)
                with pytest.raises(BackendError):
                    await transport.read_snapshot(OTHER, SnapshotReference(version=1, sha256=DIGEST))
            with backend_activity("project-1", agent_turn_id="turn-2"):
                with pytest.raises(BackendError, match="different execution"):
                    await transport.validate()
            with backend_activity("project-1", task_attempt_id="attempt-1", attempt_token="token-1"):
                background = NativeWorkspaceHTTPTransport(backend, KEY, 0)
            with backend_activity("project-1", task_attempt_id="attempt-1", attempt_token="token-2"):
                with pytest.raises(BackendError, match="different execution"):
                    await background.current()
        asyncio.run(scenario())
        assert len(requests) == 1


@pytest.mark.parametrize("redirect", [301, 302, 303, 307, 308])
def test_workspace_http_never_forwards_credentials_to_redirect_or_environment_proxy(monkeypatch, redirect):
    with wire(response({"unexpected": True})) as (target, target_requests):
        monkeypatch.setenv("HTTP_PROXY", target._base_url)
        monkeypatch.setenv("http_proxy", target._base_url)
        monkeypatch.setenv("NO_PROXY", "")
        monkeypatch.setenv("no_proxy", "")
        answer = Reply(b"private-redirect-body", redirect, headers=[("Location", target._base_url + "/capture")])
        with wire(answer) as (backend, requests):
            async def scenario():
                with backend_activity("project-1", agent_turn_id="turn-1"):
                    with pytest.raises(BackendError) as failed:
                        await NativeWorkspaceHTTPTransport(backend, KEY, 1).current()
                    assert failed.value.status_code == redirect
                    assert failed.value.__context__ is None and failed.value.__cause__ is None
                    assert "private-redirect-body" not in str(failed.value)
            asyncio.run(scenario())
            assert len(requests) == 1
        assert not target_requests


@pytest.mark.parametrize("answer", [
    Reply(b"[]"), Reply(b"not-json"), Reply(b"{}", content_type="text/plain"),
    Reply(b"{}", headers=[("Content-Encoding", "gzip")]),
    Reply(b"{}", headers=[("Content-Length", "2")]),
    Reply(b"{}", headers=[("Content-Type", "application/json")]),
    Reply(b"{}", status=201), Reply(b"x" * ((64 << 10) + 1)),
    response({"lease": lease(lease_until="2030-01-01T00:01:00")}),
    response({"lease": lease(generation=True)}), response({"lease": ["private-response"]}),
], ids=["array", "bad-json", "wrong-mime", "encoded", "duplicate-length", "duplicate-type",
        "wrong-status", "oversized", "naive-deadline", "bool-generation", "bad-lease"])
def test_workspace_malformed_http_or_lease_responses_are_unconfirmed(answer):
    with wire(answer) as (backend, _):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                with pytest.raises(BackendError) as failed:
                    await NativeWorkspaceHTTPTransport(backend, KEY, 1).current()
                assert failed.value.__context__ is None
                assert "private-response" not in str(failed.value)
        asyncio.run(scenario())


@pytest.mark.parametrize("change", ["owner", "version", "hash", "duplicate", "empty", "too-large"])
def test_workspace_binary_binding_integrity_and_bounds(change):
    answer = archive_reply()
    if change in {"owner", "version", "hash"}:
        index, value = {"owner": (0, OTHER), "version": (1, "2"), "hash": (2, "0" * 64)}[change]
        answer.headers[index] = (answer.headers[index][0], value)
    elif change == "duplicate":
        answer.headers.append(answer.headers[0])
    else:
        answer = archive_reply(b"" if change == "empty" else b"a" * (MAX_ARCHIVE_BYTES + 1))
    with wire(response(lease()), answer) as (backend, _):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                with pytest.raises(BackendError):
                    await transport.read_snapshot(SESSION, SnapshotReference(version=1, sha256=DIGEST))
        asyncio.run(scenario())


@pytest.mark.parametrize("invalid", [[], "bad", receipt(session_id=OTHER), receipt(version=2), receipt(sha256="0" * 64)])
def test_workspace_exact_save_receipt_is_checked(invalid):
    with wire(response(lease()), response({"receipt": invalid})) as (backend, _):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                with pytest.raises(BackendError):
                    await transport.read_snapshot_receipt(SESSION, PendingSnapshotSave(
                        request_id=REQUEST_ID, parent_version=0, sha256=DIGEST))
        asyncio.run(scenario())


def test_workspace_takeover_and_renewal_cannot_switch_logical_sessions():
    with wire(response({"lease": lease(generation=4)}), response(lease(generation=5)),
              response(lease(session_id=OTHER, generation=5))) as (backend, requests):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                current = await transport.current()
                with pytest.raises(BackendError, match="previously discovered"):
                    await transport.reserve(current.generation)
                acquired = await transport.reserve(current.generation, expected_session_id=current.session_id)
                assert acquired.generation == 5
                with pytest.raises(BackendError):
                    await transport.reserve()
                assert transport.lease == acquired
        asyncio.run(scenario())
        assert len(requests) == 3


def test_workspace_snapshot_sdk_rebuild_resolves_exact_lost_http_receipt_without_replay():
    with wire(response(lease()), Reply(b"lost-ack-private-details", status=502),
              response({"receipt": receipt()}), archive_reply()) as (backend, requests):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                dependencies = Dependencies.with_values({SNAPSHOT_DEPENDENCY: RuntimeSnapshotBinding(SESSION, transport)})
                snapshot = RuntimeWorkspaceSnapshot(id=SESSION)
                with pytest.raises(NativeSnapshotError):
                    await snapshot.persist(io.BytesIO(BINARY), dependencies=dependencies)
                serialized = snapshot.model_dump_json()
                assert KEY not in serialized and "credential" not in serialized and "lost-ack" not in serialized
                rebuilt = SnapshotBase.parse(json.loads(serialized))
                assert (await rebuilt.restore(dependencies=dependencies)).read() == BINARY
                assert rebuilt.reference.version == 1 and rebuilt.pending is None
                assert requests[2][1].startswith(PREFIX + SESSION + "/snapshot-receipts/" + snapshot.pending.request_id + "?")
        asyncio.run(scenario())
        assert [item[0] for item in requests] == ["POST", "PUT", "GET", "GET"]


def test_workspace_request_limits_reject_before_any_http_call():
    with wire() as (backend, requests):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                for kwargs in ({"archive": b""}, {"archive": b"x" * (MAX_ARCHIVE_BYTES + 1)},
                               {"payload": {"huge": "x" * 4096}}, {"archive": b"x", "payload": {}}):
                    with pytest.raises(BackendError):
                        await backend.native_workspace_request("PUT", PREFIX + "test", {}, **kwargs)
        asyncio.run(scenario())
        assert not requests


def test_workspace_error_body_read_failure_is_redacted_and_closed(monkeypatch):
    class BrokenBody(io.BytesIO):
        def read(self, *args):
            raise OSError("private-token-and-provider-body")
    body = BrokenBody()
    class BrokenOpener:
        def open(self, *args, **kwargs):
            raise HTTPError("http://127.0.0.1/private", 502, "private-provider-error", {}, body)
    monkeypatch.setattr("content_agent_sidecar.backend.build_opener", lambda *args: BrokenOpener())
    backend = BackendClient("http://127.0.0.1:1", internal_token="isolated-service-credential")
    async def scenario():
        with backend_activity("project-1", agent_turn_id="turn-1"):
            with pytest.raises(BackendError) as failed:
                await NativeWorkspaceHTTPTransport(backend, KEY, 1).current()
            assert failed.value.status_code == 502
            assert failed.value.__context__ is None and "private" not in str(failed.value)
    asyncio.run(scenario())
    assert body.closed
