import pytest

from content_agent_sidecar.backend import BackendClient, BackendError, backend_activity, backend_memory_activity
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport


def test_memory_transport_is_bound_to_attempt_and_cleans_up_context():
    backend = BackendClient("http://127.0.0.1:1", internal_token="test")
    with backend_memory_activity("project", "generation", 2, "token"):
        transport = NativeWorkspaceHTTPTransport.new(backend)
        assert backend.native_workspace_activity() == {
            "X-Agent-Project-ID": "project", "X-Agent-Memory-Generation-ID": "generation",
            "X-Agent-Memory-Generation-Attempt": "2", "X-Agent-Attempt-Token": "token",
        }
        assert transport._headers(require_lease=False)["X-Agent-Dispatch-Generation"] == "0"
        with pytest.raises(BackendError):
            NativeWorkspaceHTTPTransport.new(backend, dispatch_generation=1)
    with pytest.raises(BackendError):
        backend.native_workspace_activity()
    with backend_memory_activity("project", "generation", 3, "new-token"):
        with pytest.raises(BackendError):
            transport._headers(require_lease=False)
    with pytest.raises(RuntimeError):
        with backend_memory_activity("project", "generation", 2, "token"):
            raise RuntimeError("worker failed")
    with pytest.raises(BackendError):
        backend.native_workspace_activity()


@pytest.mark.parametrize("attempt", [0, -1, True, 1.0, "1", 2**31])
def test_memory_activity_rejects_invalid_attempt(attempt):
    with pytest.raises(ValueError):
        with backend_memory_activity("project", "generation", attempt, "token"):
            pytest.fail("admitted invalid attempt")


def test_memory_activity_cannot_borrow_parent_context():
    with backend_activity("project", agent_turn_id="turn"):
        with pytest.raises(ValueError):
            with backend_memory_activity("project", "generation", 1, "token"):
                pytest.fail("admitted nested memory worker")
