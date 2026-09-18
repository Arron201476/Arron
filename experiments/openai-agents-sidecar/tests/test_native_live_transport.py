import asyncio
from copy import deepcopy

import pytest
from pydantic import ValidationError

from content_agent_sidecar.backend import BackendError, backend_activity
from content_agent_sidecar.native_live_state import NativeWorkspaceCheckpoint, restore_native_checkpoint
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport, NativeWorkspaceLease
from test_agent_tools import _context
from test_native_workspace_transport import DIGEST, KEY, OTHER, PREFIX, SESSION, Reply, lease, response, wire


def live_state():
    return {"session_id": SESSION, "environment_id": OTHER, "state": "ready", "processes": [
        {"pty_session_id": 1000, "process_id": "d" * 32, "sequence": 2, "tty": True, "status": "running"}]}


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_live_inventory_http_carries_current_execution_without_process_effects(mode):
    identity = {"agent_turn_id": "turn"} if mode == "main" else {
        "task_attempt_id" if mode == "background" else "execution_attempt_id": "attempt", "attempt_token": "fixture"}
    with wire(response(live_state())) as (backend, requests):
        async def scenario():
            with backend_activity("project", **identity):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1 if mode == "main" else 0)
                transport.lease = NativeWorkspaceLease.model_validate(lease())
                state = await transport.pty_state()
                assert state.model_dump() == live_state()
        asyncio.run(scenario())
        assert len(requests) == 1
        method, path, headers, body = requests[0]
        assert (method, path, body) == ("GET", PREFIX + SESSION + "/pty/state", b"")
        assert headers["X-Workspace-Lease-Key"] == KEY and headers["X-Agent-Project-ID"] == "project"
        assert headers["X-Agent-Dispatch-Generation"] == ("1" if mode == "main" else "0")
        if mode != "main": assert headers["X-Agent-Attempt-Token"] == "fixture"


@pytest.mark.parametrize("fault", ["workspace", "environment", "empty-environment", "extra", "unknown-state", "closed-process",
    "starting-process", "bool-id", "zero-sequence", "bool-tty", "extra-process", "duplicate", "null-processes", "limit"])
def test_unconfirmed_or_ambiguous_live_inventory_is_never_rebound(fault):
    data = live_state()
    item = data["processes"][0]
    if fault == "workspace": data["session_id"] = OTHER
    elif fault == "environment": data["environment_id"] = "host-process"
    elif fault == "empty-environment": data["environment_id"] = ""
    elif fault == "extra": data["host_pid"] = 12
    elif fault == "unknown-state": data["state"] = "blocked"
    elif fault == "closed-process": data["state"] = "closed"
    elif fault == "starting-process": item["status"] = "starting"
    elif fault == "bool-id": item["pty_session_id"] = True
    elif fault == "zero-sequence": item["sequence"] = 0
    elif fault == "bool-tty": item["tty"] = 1
    elif fault == "extra-process": item["host_pid"] = 12
    elif fault == "duplicate": data["processes"].append(deepcopy(item))
    elif fault == "null-processes": data["processes"] = None
    else: data["processes"] *= 129
    with wire(response(data)) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                transport.lease = NativeWorkspaceLease.model_validate(lease())
                with pytest.raises(BackendError): await transport.pty_state()
        asyncio.run(scenario())
        assert len(requests) == 1


@pytest.mark.parametrize("answer", [Reply(b'{"data":'), Reply(b"", status=302, headers=[("Location", "http://127.0.0.1:1/")]),
    Reply(b'{"error":{"code":"NATIVE_WORKSPACE_OPERATION_IN_PROGRESS"}}', status=409)], ids=["truncated", "redirect", "pending"])
def test_live_inventory_unknown_transport_is_not_retried(answer):
    with wire(answer) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                transport.lease = NativeWorkspaceLease.model_validate(lease())
                with pytest.raises(BackendError): await transport.pty_state()
        asyncio.run(scenario())
        assert len(requests) == 1


def test_application_checkpoint_contains_references_not_holder_keys_or_process_authority():
    checkpoint = NativeWorkspaceCheckpoint(session_id=SESSION, environment_id=OTHER, state="ready",
        snapshot={"version": 1, "sha256": DIGEST})
    context = _context()
    restore_native_checkpoint(context, {"native_workspace_checkpoint": checkpoint.model_dump()})
    assert context.native_workspace_checkpoint == checkpoint
    for key, value in (("holder_key", KEY), ("processes", live_state()["processes"]), ("host_path", "C:/private")):
        with pytest.raises(BackendError):
            restore_native_checkpoint(context, {"native_workspace_checkpoint": {**checkpoint.model_dump(), key: value}})
    with pytest.raises(ValidationError):
        NativeWorkspaceCheckpoint.model_validate({**checkpoint.model_dump(), "state": "absent"})
