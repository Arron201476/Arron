from __future__ import annotations

import base64
import binascii
from dataclasses import dataclass
import json
import posixpath
import re

from .backend import BackendError
from .native_files import _go_json_hash


def validate_pty_identity(call_id, sdk_id, arguments):
    if (not isinstance(call_id, str) or not 1 <= len(call_id) <= 128
            or not isinstance(sdk_id, str) or not 1 <= len(sdk_id) <= 512
            or not isinstance(arguments, dict)):
        raise BackendError("Terminal I/O requires bounded audited SDK identities")
    try:
        encoded = json.dumps(arguments, allow_nan=False).encode("utf-8")
        if len(encoded) <= 64 << 10:
            return
    except (TypeError, ValueError, UnicodeError):
        pass
    raise BackendError("Original terminal arguments exceed their wire contract")


def pty_start_operation(argv, *, cwd, tty, yield_ms, timeout_ms):
    failed = False
    try:
        failed = (not isinstance(argv, list) or not 1 <= len(argv) <= 256
                  or any(not isinstance(arg, str) or "\x00" in arg for arg in argv)
                  or sum(len(arg.encode("utf-8")) for arg in argv) > 64 << 10
                  or not argv[0].strip() or argv[0].startswith("-") or "=" in argv[0]
                  or not isinstance(cwd, str) or len(cwd.encode("utf-8")) > 4096
                  or posixpath.normpath(cwd) != cwd or cwd != "/workspace" and not cwd.startswith("/workspace/")
                  or any(char in cwd for char in "\\\x00\r\n") or type(tty) is not bool
                  or type(yield_ms) is not int or not 1 <= yield_ms <= 30000
                  or type(timeout_ms) is not int or not 0 <= timeout_ms <= 30000)
    except (TypeError, UnicodeError):
        failed = True
    if failed:
        raise BackendError("Invalid bounded terminal startup")
    command = {"argv": list(argv), "cwd": cwd}
    if timeout_ms:
        command["timeout_ms"] = timeout_ms
    return {"start": {"command": command, "tty": tty, "yield_ms": yield_ms}}


def pty_input_operation(session_id, expected_sequence, chars, yield_ms):
    if (type(session_id) is not int or not 1000 <= session_id <= 1127
            or type(expected_sequence) is not int or not 1 <= expected_sequence < 512
            or not isinstance(chars, str) or type(yield_ms) is not int or not 1 <= yield_ms <= 30000):
        raise BackendError("Invalid bounded terminal input")
    try:
        if len(chars.encode("utf-8")) <= 64 << 10:
            return {"input": {"pty_session_id": session_id, "expected_sequence": expected_sequence,
                              "chars": chars, "yield_ms": yield_ms}}
    except UnicodeError:
        pass
    raise BackendError("Terminal input exceeds its UTF-8 byte contract")


@dataclass(frozen=True)
class NativePTYReceipt:
    session_id: int
    sequence: int
    process_id: str
    output: bytes
    exit_code: int | None
    reason: str
    input_bytes: int


def parse_pty_receipt(data, *, workspace_id, call_id, operation, expected_process_id=None):
    fields = {"session_id", "agent_tool_call_id", "pty_session_id", "sequence", "request_hash", "result_hash", "result"}
    expected_sequence = 1 if "start" in operation else operation["input"]["expected_sequence"] + 1
    expected_session = None if "start" in operation else operation["input"]["pty_session_id"]
    input_bytes = 0 if "start" in operation else len(operation["input"]["chars"].encode("utf-8"))
    if (not isinstance(data, dict) or set(data) != fields or data["session_id"] != workspace_id
            or data["agent_tool_call_id"] != call_id or data["request_hash"] != _go_json_hash(operation)
            or type(data["pty_session_id"]) is not int or not 1000 <= data["pty_session_id"] <= 1127
            or expected_session is not None and data["pty_session_id"] != expected_session
            or type(data["sequence"]) is not int or data["sequence"] != expected_sequence):
        raise BackendError("Terminal receipt differs from its execution, request or sequence")
    result = data["result"]
    if (not isinstance(result, dict) or set(result) != {"process_id", "output", "exit_code", "reason", "input_bytes"}
            or not isinstance(result["process_id"], str) or not re.fullmatch(r"[a-f0-9]{32}", result["process_id"])
            or expected_process_id is not None and result["process_id"] != expected_process_id
            or not isinstance(result["reason"], str)
            or type(result["input_bytes"]) is not int or not 0 <= result["input_bytes"] <= input_bytes
            or not isinstance(result["output"], str) or len(result["output"]) > ((1 << 20) + 2) // 3 * 4):
        raise BackendError("Terminal result has an invalid process or byte binding")
    exit_code = result["exit_code"]
    if (exit_code is None and (result["reason"] != "running" or result["input_bytes"] != input_bytes)
            or exit_code is not None and (type(exit_code) is not int or not 0 <= exit_code <= 255
                                         or result["reason"] not in {"exited", "terminated", "timeout", "output_limit"})):
        raise BackendError("Terminal result has inconsistent process state")
    body = None
    try:
        body = base64.b64decode(result["output"], validate=True)
    except (ValueError, binascii.Error):
        pass
    if body is None or len(body) > 1 << 20 or base64.b64encode(body).decode("ascii") != result["output"]:
        raise BackendError("Terminal output is not canonical bounded binary")
    ordered = {name: result[name] for name in ("process_id", "output", "exit_code", "reason", "input_bytes")}
    if _go_json_hash(ordered) != data["result_hash"]:
        raise BackendError("Terminal result integrity is unconfirmed")
    return NativePTYReceipt(data["pty_session_id"], data["sequence"], result["process_id"], body,
                            exit_code, result["reason"], result["input_bytes"])
