import asyncio
from unittest.mock import AsyncMock

import pytest

from content_agent_sidecar.backend import BackendClient, backend_activity


@pytest.mark.parametrize("mode", ["turn", "background", "execution"])
def test_memory_snapshot_uses_original_activity_credentials(mode):
    client = BackendClient("http://127.0.0.1:9", internal_token="test-only")
    response = {"activity_key": f"{mode}:attempt", "project_id": "project"}
    client._post_json = AsyncMock(return_value={"data": response})
    kwargs = {"agent_turn_id": "attempt"} if mode == "turn" else {
        ("task_attempt_id" if mode == "background" else "execution_attempt_id"): "attempt",
        "attempt_token": "original-lease",
    }
    with backend_activity("project", **kwargs):
        assert asyncio.run(client.resolve_agent_memory_snapshot("project", f"{mode}:attempt")) == response
    call = client._post_json.call_args
    assert call.args == ("/internal/v1/agent-memory/snapshot", {
        "project_id": "project", "activity_key": f"{mode}:attempt",
    })
    headers = call.kwargs["headers"]
    assert headers["Authorization"] == "Bearer test-only"
    assert headers["X-Agent-Project-ID"] == "project"
    expected = {"turn": "X-Agent-Turn-ID", "background": "X-Agent-Task-Attempt-ID", "execution": "X-Agent-Execution-Attempt-ID"}
    assert headers[expected[mode]] == "attempt"
    if mode != "turn":
        assert headers["X-Agent-Attempt-Token"] == "original-lease"


@pytest.mark.parametrize("response", [None, {}, {"data": None}, {"data": []}])
def test_memory_snapshot_rejects_missing_receipt(response):
    client = BackendClient("http://127.0.0.1:9", internal_token="test-only")
    client._post_json = AsyncMock(return_value=response)
    with pytest.raises(RuntimeError, match="Agent memory snapshot"):
        asyncio.run(client.resolve_agent_memory_snapshot("project", "turn:attempt"))
