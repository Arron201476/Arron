import asyncio
import base64
from copy import deepcopy
import hashlib
import json

import pytest

from content_agent_sidecar.backend import BackendError, backend_activity
from content_agent_sidecar.native_execution import NativePatchCall
from content_agent_sidecar.native_files import MAX_FILE_BYTES, NativeFileOperation, NativeFileResult
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport
from test_native_workspace_transport import KEY, OTHER, REQUEST_ID, SESSION, Reply, lease, response, wire


CALL = NativePatchCall("patch-1", "sdk-1", "d" * 64)


def file_wire(operation, body=b"", **changes):
    sha = hashlib.sha256(body).hexdigest()
    entry = {"path": operation.path, "directory": False, "mode": 0o600, "size_bytes": len(body), "sha256": sha}
    return {"operation": operation.operation, "path": operation.path,
            "data": base64.b64encode(body).decode() if operation.operation == "read" else "",
            "entries": [entry], "sha256": sha, **changes}


def file_receipt(operation, result, **changes):
    return response({"session_id": SESSION, "request_id": REQUEST_ID,
                     "request_hash": operation.request_hash, "file": result, **changes})


@pytest.mark.parametrize("kind", ["read", "write"])
def test_native_file_binary_http_exceeds_old_control_limit(kind):
    body = (b"\x00\xff\x80native" * 10000)
    operation = NativeFileOperation(kind, "nested/payload.bin", data=body if kind == "write" else b"")
    with wire(response(lease()), file_receipt(operation, file_wire(operation, body))) as (backend, requests):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                result = await transport.file_operation(REQUEST_ID, operation, CALL if kind == "write" else None)
                assert result.data == (body if kind == "read" else b"")
                assert result.sha256 == hashlib.sha256(body).hexdigest()
        asyncio.run(scenario())
        method, path, headers, payload = requests[-1]
        assert method == "POST" and path.endswith("/files")
        assert headers["Authorization"] == "Bearer isolated-service-credential"
        decoded = json.loads(payload)
        assert decoded["file"] == operation.payload()
        assert decoded["arguments_hash"] == (CALL.arguments_hash if kind == "write" else "")
        assert KEY not in payload.decode()


@pytest.mark.parametrize("change", ["session", "request", "hash", "missing", "wrong_type"])
def test_native_file_http_requires_exact_operation_receipt(change):
    operation = NativeFileOperation("write", "file", data=b"a")
    result = {"session_id": SESSION, "request_id": REQUEST_ID, "request_hash": operation.request_hash, "file": file_wire(operation, b"a")}
    if change == "session":
        result["session_id"] = OTHER
    elif change == "request":
        result["request_id"] = "e" * 32
    elif change == "hash":
        result["request_hash"] = "f" * 64
    elif change == "missing":
        result.pop("file")
    else:
        result["file"] = []
    with wire(response(lease()), response(result)) as (backend, requests):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                with pytest.raises(BackendError):
                    await transport.file_operation(REQUEST_ID, operation, CALL)
        asyncio.run(scenario())
        assert len(requests) == 2


@pytest.mark.parametrize("fault", [
    "data_hash", "entry_hash", "entry_size", "directory", "path", "mode", "bool_mode", "bool_size",
    "entry_extra", "entry_hash_type", "duplicate", "body_base64", "body_type", "body_padding", "missing_entries", "extra",
])
def test_native_file_response_validates_binary_and_metadata(fault):
    operation = NativeFileOperation("read", "file")
    value = file_wire(operation, b"a")
    entry = value["entries"][0]
    if fault == "data_hash":
        value["sha256"] = "f" * 64
    elif fault == "entry_hash":
        entry["sha256"] = "f" * 64
    elif fault == "entry_size":
        entry["size_bytes"] = 2
    elif fault == "directory":
        entry["directory"] = True
    elif fault == "path":
        entry["path"] = "../escape"
    elif fault == "mode":
        entry["mode"] = 0o1600
    elif fault == "bool_mode":
        entry["mode"] = True
    elif fault == "bool_size":
        entry["size_bytes"] = True
    elif fault == "entry_extra":
        entry["secret"] = "untrusted"
    elif fault == "entry_hash_type":
        entry["sha256"] = []
    elif fault == "duplicate":
        value["entries"].append(deepcopy(entry))
    elif fault == "body_base64":
        value["data"] = "?"
    elif fault == "body_type":
        value["data"] = None
    elif fault == "body_padding":
        value["data"] = "YR=="
    elif fault == "missing_entries":
        value.pop("entries")
    else:
        value["extra"] = True
    with pytest.raises(BackendError):
        NativeFileResult.from_wire(operation, value)


@pytest.mark.parametrize("operation", [
    NativeFileOperation("exec", "file"), NativeFileOperation("read", "../escape"),
    NativeFileOperation("read", "C:/host"), NativeFileOperation("read", "\u4e2d" * 81),
    NativeFileOperation("write", "."), NativeFileOperation("remove", ".", recursive=True),
    NativeFileOperation("write", "file", parents=True), NativeFileOperation("read", "file", data=b"a"),
    NativeFileOperation("write", "file", data="text"), NativeFileOperation("mkdir", "file", parents=1),
    NativeFileOperation("chmod", "file"), NativeFileOperation("chmod", "file", mode=True),
    NativeFileOperation("chmod", "file", mode=-1), NativeFileOperation("write", "file", data=b"x" * (MAX_FILE_BYTES + 1)),
])
def test_native_file_invalid_request_never_reaches_http(operation):
    with wire(response(lease())) as (backend, requests):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                with pytest.raises(BackendError):
                    await transport.file_operation(REQUEST_ID, operation, CALL)
        asyncio.run(scenario())
        assert len(requests) == 1


def test_native_file_authority_cannot_be_omitted_or_forged_locally():
    with wire(response(lease())) as (backend, requests):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                for call in (None, {}, NativePatchCall("", "sdk", "a" * 64), NativePatchCall("call", "sdk", "bad")):
                    with pytest.raises(BackendError):
                        await transport.file_operation(REQUEST_ID, NativeFileOperation("write", "file", data=b"a"), call)
        asyncio.run(scenario())
        assert len(requests) == 1


def test_native_file_list_only_accepts_immediate_unique_children():
    value = {"operation": "list", "path": ".", "data": "", "sha256": "", "entries": [
        {"path": "dir", "directory": True, "mode": 0o700, "size_bytes": 0},
        {"path": "file", "directory": False, "mode": 0o600, "size_bytes": 3},
    ]}
    operation = NativeFileOperation("list", ".")
    assert len(NativeFileResult.from_wire(operation, value).entries) == 2
    for changed in ([{**value["entries"][1], "path": "dir/child"}],
                    [value["entries"][1], {**value["entries"][1], "path": "FILE"}],
                    [{**value["entries"][1], "sha256": []}]):
        with pytest.raises(BackendError):
            NativeFileResult.from_wire(operation, {**value, "entries": changed})


def test_native_file_receipt_hash_matches_go_encoding_and_omitted_defaults():
    operation = NativeFileOperation("write", "\u4e2d\u6587/file&.bin", data=b"\x00\xff")
    canonical = '{"operation":"write","path":"\u4e2d\u6587/file\\u0026.bin","data":"AP8="}'.encode()
    assert operation.request_hash == hashlib.sha256(canonical).hexdigest()
    assert NativeFileOperation("write", "empty").payload() == {"operation": "write", "path": "empty"}
    assert NativeFileOperation("chmod", "file", mode=0).payload()["mode"] == 0


def test_native_file_http_safe_error_keeps_static_code_without_private_body():
    error = Reply(b'{"error":{"code":"WORKSPACE_FILE_NOT_FOUND","message":"private details"}}', status=409)
    with wire(response(lease()), error) as (backend, requests):
        async def scenario():
            with backend_activity("project-1", agent_turn_id="turn-1"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                await transport.reserve()
                with pytest.raises(BackendError) as failed:
                    await transport.file_operation(REQUEST_ID, NativeFileOperation("read", "missing"))
                assert failed.value.code == "WORKSPACE_FILE_NOT_FOUND" and failed.value.status_code == 409
                assert "private details" not in str(failed.value) and failed.value.__context__ is None
        asyncio.run(scenario())
        assert len(requests) == 2
