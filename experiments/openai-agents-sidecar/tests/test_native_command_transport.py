import asyncio
import base64
import hashlib
import json

import pytest

from content_agent_sidecar.backend import BackendError, backend_activity
from content_agent_sidecar.native_execution import native_shell_call_scope, require_native_shell_call
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport
from test_native_workspace_transport import KEY, SESSION, Reply, lease, response, wire


COMMAND = b'{"argv":["sh","-c","printf \\u003cnative\\u003e\\u0026"],"cwd":"/workspace","timeout_ms":1000}'
RESULT = b'{"Stdout":"AP+A","Stderr":"","ExitCode":7}'


def command_receipt(**changes):
    return {"session_id": SESSION, "agent_tool_call_id": "tool-1", "command_hash": hashlib.sha256(COMMAND).hexdigest(),
            "result_hash": hashlib.sha256(RESULT).hexdigest(), "stdout": "AP+A", "stderr": "", "exit_code": 7, **changes}


async def invoke(transport, **options):
    return await transport.execute_command("tool-1", "sdk-1", {"cmd": "printf <native>&"},
                                           ["sh", "-c", "printf <native>&"], timeout_ms=1000, **options)


def test_command_wire_binds_original_sdk_call_prepared_command_and_binary_receipt():
    with wire(response(lease()), response(command_receipt())) as (backend, requests):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                result = await invoke(transport)
                assert result.stdout == bytes([0, 255, 128]) and result.stderr == b"" and result.exit_code == 7
        asyncio.run(scenario())
        method, path, headers, body = requests[-1]
        assert method == "POST" and path.endswith("/" + SESSION + "/commands")
        payload = json.loads(body)
        assert payload == {"agent_tool_call_id": "tool-1", "sdk_tool_call_id": "sdk-1", "arguments": {"cmd": "printf <native>&"},
                           "configuration_hash": "", "command": {"argv": ["sh", "-c", "printf <native>&"], "cwd": "/workspace", "timeout_ms": 1000}}
        assert headers["X-Workspace-Lease-Key"] == KEY and KEY.encode() not in body


@pytest.mark.parametrize("change", ["session", "call", "command", "result", "stdout", "encoding", "exit-code", "extra"])
def test_command_rejects_mismatched_or_corrupt_receipt_without_retry(change):
    changes = {"session": {"session_id": "foreign"}, "call": {"agent_tool_call_id": "foreign"},
               "command": {"command_hash": "0" * 64}, "result": {"result_hash": "0" * 64},
               "stdout": {"stdout": "AQ=="}, "encoding": {"stdout": "@@"},
               "exit-code": {"exit_code": True}, "extra": {"unexpected": "private"}}
    with wire(response(lease()), response(command_receipt(**changes[change]))) as (backend, requests):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                with pytest.raises(BackendError):
                    await invoke(transport)
        asyncio.run(scenario())
        assert len(requests) == 2


def test_command_binary_output_transport_bound_is_separate_from_control_response_limit():
    stdout = base64.b64encode(b"a" * (1 << 20)).decode()
    encoded = json.dumps({"Stdout": stdout, "Stderr": "", "ExitCode": 0}, separators=(",", ":"))
    answer = command_receipt(stdout=stdout, exit_code=0, result_hash=hashlib.sha256(encoded.encode()).hexdigest())
    with wire(response(lease()), response(answer)) as (backend, _):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                result = await invoke(transport)
                assert result.stdout == b"a" * (1 << 20)
        asyncio.run(scenario())


def test_command_authority_errors_never_implicitly_retry():
    answer = Reply(json.dumps({"error": {"code": "NATIVE_WORKSPACE_COMMAND_UNCONFIRMED", "message": "private-provider-message"}}).encode(), status=409)
    with wire(response(lease()), answer) as (backend, requests):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                with pytest.raises(BackendError) as failed:
                    await invoke(transport)
                assert failed.value.code == "NATIVE_WORKSPACE_COMMAND_UNCONFIRMED"
                assert failed.value.__context__ is None and "private" not in str(failed.value)
        asyncio.run(scenario())
        assert len(requests) == 2


@pytest.mark.parametrize("case", ["cwd", "parent", "stdin", "timeout", "argv", "unaudited-config"])
def test_command_invalid_request_rejected_before_http(case):
    with wire(response(lease())) as (backend, requests):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                values = {"cwd": "/workspace", "stdin": b"", "timeout_ms": 0, "argv": ["sh", "-c", "true"], "configuration_hash": ""}
                changes = {"cwd": {"cwd": "/tmp"}, "parent": {"cwd": "/workspace/../tmp"},
                           "stdin": {"stdin": b"x" * ((1 << 20) + 1)}, "timeout": {"timeout_ms": 30_001},
                           "argv": {"argv": ["-unsafe"]}, "unaudited-config": {"configuration_hash": "foreign"}}
                values.update(changes[case])
                with pytest.raises(BackendError):
                    await transport.execute_command("tool-1", "sdk-1", {"cmd": "true"}, **values)
        asyncio.run(scenario())
        assert len(requests) == 1


def test_native_shell_call_scope_is_task_local_immutable_and_not_reused_after_cancel():
    async def scenario():
        with pytest.raises(BackendError):
            require_native_shell_call()
        first, second = asyncio.Event(), asyncio.Event()
        async def scoped(call_id, entered, peer):
            with native_shell_call_scope(call_id, "sdk-" + call_id, '{"cmd":"true"}'):
                entered.set()
                await peer.wait()
                current = require_native_shell_call()
                assert current.agent_tool_call_id == call_id
                mutable = current.arguments()
                mutable["cmd"] = "changed"
                assert current.arguments() == {"cmd": "true"}
                if call_id == "first":
                    raise asyncio.CancelledError()
        a = asyncio.create_task(scoped("first", first, second))
        b = asyncio.create_task(scoped("second", second, first))
        results = await asyncio.gather(a, b, return_exceptions=True)
        assert isinstance(results[0], asyncio.CancelledError) and results[1] is None
        with pytest.raises(BackendError):
            require_native_shell_call()
    asyncio.run(scenario())
