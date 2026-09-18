import asyncio

import pytest

from content_agent_sidecar.backend import BackendError, backend_activity
from content_agent_sidecar.config import _native_workspace_enabled
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport, NativeWorkspaceLease
from test_native_manifest import inventory
from test_native_workspace_transport import DIGEST, KEY, OTHER, SESSION, lease, response, wire


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
@pytest.mark.parametrize("mutation", ["", "manifest_identity", "manifest_hash", "snapshot_version", "snapshot_hash", "extra"])
def test_recovery_http_receipt_requires_authoritative_session_manifest_and_head(mode, mutation):
    async def scenario():
        data = {"manifest": inventory().model_dump(exclude_none=True), "snapshot": {"version": 2, "sha256": DIGEST}}
        if mutation == "manifest_identity":
            data["manifest"]["session_id"] = OTHER
        elif mutation == "manifest_hash":
            data["manifest"]["manifest_hash"] = "f" * 64
        elif mutation == "snapshot_version":
            data["snapshot"]["version"] = 1
        elif mutation == "snapshot_hash":
            data["snapshot"]["sha256"] = "untrusted"
        elif mutation == "extra":
            data["host_path"] = "not-allowed"
        identity = ({"agent_turn_id": "turn"} if mode == "main" else
                    {"task_attempt_id": "task", "attempt_token": "fixture"} if mode == "background" else
                    {"execution_attempt_id": "attempt", "attempt_token": "fixture"})
        with wire(response(data)) as (backend, requests), backend_activity("project", **identity):
            transport = NativeWorkspaceHTTPTransport(backend, KEY, 2 if mode == "main" else 0)
            transport.lease = NativeWorkspaceLease.model_validate(lease(snapshot_version=2))
            if mutation:
                with pytest.raises(BackendError):
                    await transport.recovery()
            else:
                manifest, snapshot = await transport.recovery()
                assert manifest == inventory() and snapshot.version == 2 and snapshot.sha256 == DIGEST
            assert len(requests) == 1 and requests[0][0:2] == ("GET", f"/internal/v1/native-workspaces/{SESSION}/recovery")
            assert requests[0][2]["X-Workspace-Lease-Key"] == KEY and requests[0][3] == b""
    asyncio.run(scenario())


@pytest.mark.parametrize("value,expected", [(None, False), ("", False), ("false", False), ("F", False), ("1", True), ("T", True), ("TRUE", True), ("yes", None)])
def test_native_enablement_is_explicit_and_matches_go_boolean_values(monkeypatch, value, expected):
    monkeypatch.delenv("CONTENT_AGENT_NATIVE_WORKSPACE_ENABLED", raising=False)
    if value is not None:
        monkeypatch.setenv("CONTENT_AGENT_NATIVE_WORKSPACE_ENABLED", value)
    if expected is None:
        with pytest.raises(ValueError):
            _native_workspace_enabled()
    else:
        assert _native_workspace_enabled() is expected
