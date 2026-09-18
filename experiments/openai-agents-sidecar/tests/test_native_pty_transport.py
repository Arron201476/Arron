import asyncio
import base64
from copy import deepcopy
import json

import pytest

from content_agent_sidecar.backend import BackendError, backend_activity
from content_agent_sidecar.native_files import _go_json_hash
from content_agent_sidecar.native_pty import pty_input_operation, pty_start_operation
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport
from test_native_workspace_transport import KEY, OTHER, PREFIX, SESSION, Reply, lease, response, wire


PROCESS = "d" * 32
START_ARGS = {"cmd": "read input", "tty": True}
ARGV = ["python", "-c", "input()"]


def start_operation():
    return pty_start_operation(ARGV, cwd="/workspace", tty=True, yield_ms=1000, timeout_ms=0)


def terminal_receipt(operation, *, call_id="start", output=b"ready", exit_code=None, reason="running", input_bytes=0):
    result = {"process_id": PROCESS, "output": base64.b64encode(output).decode(),
              "exit_code": exit_code, "reason": reason, "input_bytes": input_bytes}
    return {"session_id": SESSION, "agent_tool_call_id": call_id, "pty_session_id": 1000,
            "sequence": 1 if "start" in operation else operation["input"]["expected_sequence"] + 1,
            "request_hash": _go_json_hash(operation), "result_hash": _go_json_hash(result), "result": result}


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_pty_real_http_binds_each_io_to_execution_and_approved_arguments(mode):
    chars = "\u4f60\u597d<&\u2028\n"
    arguments = {"session_id": 1000, "chars": chars}
    operation = pty_input_operation(1000, 1, chars, 1000)
    activity = {"agent_turn_id": "turn"} if mode == "main" else {
        "task_attempt_id" if mode == "background" else "execution_attempt_id": "attempt", "attempt_token": "fixture-attempt",
    }
    with wire(response(lease()), response(terminal_receipt(start_operation())),
              response(terminal_receipt(operation, call_id="input", output=b"\x00\xff\x80", exit_code=23,
                                        reason="exited", input_bytes=len(chars.encode())))) as (backend, requests):
        async def scenario():
            with backend_activity("project", **activity):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1 if mode == "main" else 0)
                await transport.reserve()
                started = await transport.pty_start("start", "sdk-start", START_ARGS, ARGV, tty=True)
                assert started.session_id == 1000 and started.sequence == 1 and started.process_id == PROCESS
                assert started.output == b"ready" and started.exit_code is None
                finished = await transport.pty_input("input", "sdk-input", arguments, session_id=started.session_id,
                    expected_sequence=started.sequence, expected_process_id=started.process_id, chars=chars)
                assert finished.output == b"\x00\xff\x80" and finished.exit_code == 23 and finished.sequence == 2
                assert finished.input_bytes == len(chars.encode())
        asyncio.run(scenario())
        assert len(requests) == 3
        for method, path, headers, body in requests[1:]:
            assert (method, path) == ("POST", PREFIX + SESSION + "/pty")
            assert headers["Authorization"] == "Bearer isolated-service-credential"
            assert headers["X-Agent-Project-ID"] == "project" and headers["X-Workspace-Lease-Key"] == KEY
            assert headers["X-Agent-Dispatch-Generation"] == ("1" if mode == "main" else "0")
            assert (headers["X-Agent-Turn-ID"] == "turn") == (mode == "main")
            if mode != "main":
                assert headers["X-Agent-Attempt-Token"] == "fixture-attempt"
            assert KEY.encode() not in body and PROCESS.encode() not in body
        assert json.loads(requests[1][3]) == {"agent_tool_call_id": "start", "sdk_tool_call_id": "sdk-start", "arguments": START_ARGS, **start_operation()}
        assert json.loads(requests[2][3]) == {"agent_tool_call_id": "input", "sdk_tool_call_id": "sdk-input", "arguments": arguments, **operation}


@pytest.mark.parametrize("fault", ["workspace", "call", "request-hash", "result-hash", "session", "sequence", "extra",
    "process", "output-type", "base64", "base64-padding", "too-much-output", "negative-input", "excess-input", "bool-input",
    "partial-running-input", "running-with-exit", "exit-without-code", "exit-bool", "exit-range", "reason-type", "extra-result"])
def test_pty_corrupt_receipt_never_succeeds_or_retries(fault):
    operation = pty_input_operation(1000, 1, "x", 1000)
    receipt = terminal_receipt(operation, call_id="input", input_bytes=1)
    top = {"workspace": ("session_id", OTHER), "call": ("agent_tool_call_id", "different"),
           "request-hash": ("request_hash", "a" * 64), "result-hash": ("result_hash", "b" * 64),
           "session": ("pty_session_id", 1001), "sequence": ("sequence", True), "extra": ("unbound", True)}
    result_changes = {"process": ("process_id", "e" * 32), "output-type": ("output", None), "base64": ("output", "%%%%"),
        "base64-padding": ("output", "YQ==="), "too-much-output": ("output", base64.b64encode(b"x" * ((1 << 20) + 1)).decode()),
        "negative-input": ("input_bytes", -1), "excess-input": ("input_bytes", 2), "bool-input": ("input_bytes", True),
        "partial-running-input": ("input_bytes", 0), "running-with-exit": ("exit_code", 0), "exit-without-code": ("reason", "exited"),
        "exit-bool": ("exit_code", True), "exit-range": ("exit_code", 256), "reason-type": ("reason", []), "extra-result": ("other", 1)}
    if fault in top:
        key, value = top[fault]
        receipt[key] = value
    else:
        key, value = result_changes[fault]
        receipt["result"][key] = value
        receipt["result_hash"] = _go_json_hash(receipt["result"])
    with wire(response(lease()), response(receipt)) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                with pytest.raises(BackendError):
                    await transport.pty_input("input", "sdk-input", {"session_id": 1000, "chars": "x"},
                        session_id=1000, expected_sequence=1, chars="x", expected_process_id=PROCESS)
        asyncio.run(scenario())
        assert len(requests) == 2


@pytest.mark.parametrize("fault", ["empty-argv", "bad-argv", "surrogate", "argv-limit", "cwd", "tty", "yield", "timeout", "changed-tty", "argument-limit"])
def test_invalid_pty_start_fails_before_http(fault):
    argv, arguments, kwargs = list(ARGV), deepcopy(START_ARGS), {"tty": True}
    if fault == "empty-argv":
        argv = []
    elif fault == "bad-argv":
        argv = [None]
    elif fault == "surrogate":
        argv = ["python", "\ud800"]
    elif fault == "argv-limit":
        argv = ["python", "x" * (64 << 10)]
    elif fault == "changed-tty":
        arguments["tty"] = False
    elif fault == "argument-limit":
        arguments["cmd"] = "x" * (64 << 10)
    else:
        key, value = {"cwd": ("cwd", "/workspace/../private"), "tty": ("tty", 1),
                      "yield": ("yield_ms", True), "timeout": ("timeout_ms", 30001)}[fault]
        kwargs[key] = value
    with wire(response(lease())) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                with pytest.raises(BackendError):
                    await transport.pty_start("start", "sdk-start", arguments, argv, **kwargs)
        asyncio.run(scenario())
        assert len(requests) == 1


@pytest.mark.parametrize("fault", ["session", "sequence", "chars", "surrogate", "limit", "yield", "process", "approval"])
def test_invalid_pty_input_fails_before_http(fault):
    kwargs = {"session_id": 1000, "expected_sequence": 1, "chars": "x", "yield_ms": 1000, "expected_process_id": PROCESS}
    arguments = {"session_id": 1000, "chars": "x"}
    if fault == "approval":
        arguments["chars"] = "different"
    else:
        key, value = {"session": ("session_id", True), "sequence": ("expected_sequence", 512), "chars": ("chars", None),
            "surrogate": ("chars", "\ud800"), "limit": ("chars", "x" * ((64 << 10) + 1)),
            "yield": ("yield_ms", 0), "process": ("expected_process_id", "other")}[fault]
        kwargs[key] = value
    with wire(response(lease())) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                with pytest.raises(BackendError):
                    await transport.pty_input("input", "sdk-input", arguments, **kwargs)
        asyncio.run(scenario())
        assert len(requests) == 1


def test_pty_supports_full_bounded_binary_output_above_control_limit():
    output = bytes(range(256)) * 4096
    with wire(response(lease()), response(terminal_receipt(start_operation(), output=output))) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                result = await transport.pty_start("start", "sdk-start", START_ARGS, ARGV, tty=True)
                assert result.output == output
        asyncio.run(scenario())
        assert len(requests) == 2


@pytest.mark.parametrize("answer", [Reply(b'{"error":{"code":"NATIVE_WORKSPACE_PTY_UNCONFIRMED"}}', status=409),
    Reply(b'{"data":'), Reply(b"x" * ((2 << 20) + 1)), Reply(b"", status=302, headers=[("Location", "http://127.0.0.1:1/")])],
    ids=["unknown", "truncated", "oversize", "redirect"])
def test_uncertain_terminal_transport_does_not_retry_or_follow_redirect(answer):
    with wire(response(lease()), answer) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                with pytest.raises(BackendError):
                    await transport.pty_start("start", "sdk-start", START_ARGS, ARGV, tty=True)
        asyncio.run(scenario())
        assert len(requests) == 2


@pytest.mark.parametrize("suffix,limit", [("pty", 256 << 10), ("publications", 256 << 10),
    ("ensure", 4096), ("pty?unbound=1", 4096), ("publications/extra", 4096)])
def test_large_request_allowance_is_bounded_and_scoped_to_exact_endpoint(suffix, limit):
    with wire() as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                with pytest.raises(BackendError, match="control request exceeds"):
                    await backend.native_workspace_request("POST", PREFIX + SESSION + "/" + suffix, {}, payload={"data": "x" * limit})
        asyncio.run(scenario())
        assert not requests
