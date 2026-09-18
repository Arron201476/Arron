from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

from content_agent_sidecar.app import create_app, _failure_diagnostics
from content_agent_sidecar.config import Settings


def test_failure_diagnostics_preserve_cause_type_without_sensitive_messages():
    inner = TimeoutError("private token and request content")
    inner.status_code = 504
    outer = RuntimeError("private gateway response")
    outer.__cause__ = inner
    assert _failure_diagnostics(outer) == ("RuntimeError>TimeoutError", 504)
    inner.__cause__ = outer
    assert _failure_diagnostics(outer) == ("RuntimeError>TimeoutError", 504)


class FakeRuntime:
    def start_execution_stream(self, _request):  # type: ignore[no-untyped-def]
        return FakeExecutionStream()


class FakeExecutionStream:
    cancelled = False

    async def events(self):  # type: ignore[no-untyped-def]
        yield {
            "event": "agent.tool.started",
            "data": {"tool_name": "inspect_project"},
        }
        yield {
            "event": "agent.turn.committed",
            "data": {
                "exchange": {
                    "data": {"agent_message": {"message_id": "msg_agent"}}
                }
            },
        }

    def cancel(self, _reason: str = "client_request") -> bool:
        self.cancelled = True
        return True

    async def aclose(self, reason: str = "go_disconnected") -> None:
        self.cancel(reason)


class FailingExecutionStream(FakeExecutionStream):
    async def events(self):  # type: ignore[no-untyped-def]
        if False:
            yield {}
        raise ValueError("sensitive configuration detail")


class PausedExecutionStream(FakeExecutionStream):
    async def events(self):  # type: ignore[no-untyped-def]
        yield {
            "event": "agent.turn.waiting_approval",
            "data": {
                "run_state": {"$schemaVersion": "1.16", "private": "checkpoint"},
                "run_state_schema": "1.16",
                "pending_sdk_tool_call_ids": ["sdk-write-1"],
                "observation": {},
            },
        }


def settings() -> Settings:
    return Settings(
        backend_base_url="http://127.0.0.1:8860",
        model_base_url="https://model.example/v1",
        model_api_key="test-key",
        model_name="gpt-test-model",
        model_timeout_seconds=10,
        model_max_output_tokens=1024,
        model_max_retries=0,
        run_timeout_seconds=30,
        tracing_enabled=False,
        internal_token="internal-test-token",
    )


def execution_payload() -> dict[str, object]:
    return {
        "project_id": "prj_test",
        "conversation_id": "conv_test",
        "request": {"content": "生成一份大纲"},
        "idempotency_key": "11111111-1111-4111-8111-111111111111",
        "agent_turn_id": "turn_test",
    }


def test_health_does_not_expose_credentials() -> None:
    client = TestClient(create_app(settings(), FakeRuntime()))
    response = client.get("/healthz")
    assert response.status_code == 200
    assert response.json() == {
        "status": "ok",
        "backend_base_url": "http://127.0.0.1:8860",
        "model_configured": True,
        "write_tools_enabled": True,
    }
    assert "test-key" not in response.text


@pytest.mark.parametrize(
    "path",
    [
        "/v1/agent/runs",
        "/v1/agent/runs/stream",
        "/internal/v1/agent/decide",
        "/internal/v1/agent/execute",
        "/internal/v1/control/interpret",
    ],
)
def test_obsolete_agent_routes_are_removed(path: str) -> None:
    client = TestClient(create_app(settings(), FakeRuntime()))
    response = client.post(
        path,
        headers={"Authorization": "Bearer internal-test-token"},
        json=execution_payload(),
    )
    assert response.status_code == 404


def test_internal_stream_requires_bearer_token() -> None:
    client = TestClient(create_app(settings(), FakeRuntime()))
    response = client.post(
        "/internal/v1/agent/execute-stream",
        json=execution_payload(),
    )
    assert response.status_code == 401


def test_internal_auth_runs_before_request_validation() -> None:
    client = TestClient(create_app(settings(), FakeRuntime()))
    response = client.post(
        "/internal/v1/agent/execute-stream",
        headers={"Authorization": "Bearer wrong-token"},
        json={"not": "the execution contract"},
    )
    assert response.status_code == 401


def test_internal_agent_execute_stream_uses_caller_turn_id() -> None:
    client = TestClient(create_app(settings(), FakeRuntime()))
    with client.stream(
        "POST",
        "/internal/v1/agent/execute-stream",
        headers={"Authorization": "Bearer internal-test-token"},
        json=execution_payload(),
    ) as response:
        body = "".join(response.iter_text())
    assert response.status_code == 200
    assert response.headers["x-sidecar-run-id"] == "turn_test"
    assert "event: agent.tool.started" in body
    assert "event: agent.turn.committed" in body
    assert '"turn_id": "turn_test"' in body


def test_internal_agent_execute_stream_marks_approval_checkpoint_non_terminal() -> None:
    class PausedRuntime(FakeRuntime):
        def start_execution_stream(self, _request):  # type: ignore[no-untyped-def]
            return PausedExecutionStream()

    client = TestClient(create_app(settings(), PausedRuntime()))
    with client.stream(
        "POST",
        "/internal/v1/agent/execute-stream",
        headers={"Authorization": "Bearer internal-test-token"},
        json=execution_payload(),
    ) as response:
        body = "".join(response.iter_text())
    assert response.status_code == 200
    assert body.count("event: agent.turn.waiting_approval") == 1
    assert '"terminal": false' in body
    assert '"private": "checkpoint"' in body
    assert "agent.turn.committed" not in body


def test_internal_agent_execute_stream_converts_runtime_value_error_to_terminal() -> None:
    class FailingRuntime(FakeRuntime):
        def start_execution_stream(self, _request):  # type: ignore[no-untyped-def]
            return FailingExecutionStream()

    client = TestClient(create_app(settings(), FailingRuntime()))
    payload = execution_payload()
    payload["agent_turn_id"] = "turn_failed"
    with client.stream(
        "POST",
        "/internal/v1/agent/execute-stream",
        headers={"Authorization": "Bearer internal-test-token"},
        json=payload,
    ) as response:
        body = "".join(response.iter_text())
    assert response.status_code == 200
    assert body.count("event: agent.turn.failed") == 1
    assert '"terminal": true' in body


def test_internal_cancel_unknown_turn_is_not_accepted() -> None:
    client = TestClient(create_app(settings(), FakeRuntime()))
    response = client.post(
        "/internal/v1/agent/runs/turn_missing/cancel",
        headers={"Authorization": "Bearer internal-test-token"},
    )
    assert response.status_code == 200
    assert response.json() == {"run_id": "turn_missing", "accepted": False}
