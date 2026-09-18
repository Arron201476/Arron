from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import dataclass
import json
import re

from .backend import BackendError


@dataclass(frozen=True)
class NativeShellCall:
    agent_tool_call_id: str
    sdk_tool_call_id: str
    arguments_json: str

    def arguments(self) -> dict:
        return json.loads(self.arguments_json)


_native_shell_call: ContextVar[NativeShellCall | None] = ContextVar("native_shell_call", default=None)


@contextmanager
def native_shell_call_scope(call_id: str, sdk_id: str, raw: str):
    if not call_id or not sdk_id or not isinstance(json.loads(raw), dict):
        raise BackendError("Native shell execution requires its approved original SDK call")
    token = _native_shell_call.set(NativeShellCall(call_id, sdk_id, raw))
    try:
        yield
    finally:
        _native_shell_call.reset(token)


def require_native_shell_call() -> NativeShellCall:
    value = _native_shell_call.get()
    if value is None:
        raise BackendError("No approved SDK shell invocation is active")
    return value


def current_native_shell_call() -> NativeShellCall | None:
    return _native_shell_call.get()


@dataclass(frozen=True)
class NativePatchCall:
    agent_tool_call_id: str
    sdk_tool_call_id: str
    arguments_hash: str


_native_patch_call: ContextVar[NativePatchCall | None] = ContextVar("native_patch_call", default=None)


@contextmanager
def native_patch_call_scope(call_id: str, sdk_id: str, arguments_hash: str):
    if (not isinstance(call_id, str) or not call_id or len(call_id) > 128
            or not isinstance(sdk_id, str) or not sdk_id or len(sdk_id) > 512
            or not isinstance(arguments_hash, str) or not re.fullmatch(r"[a-f0-9]{64}", arguments_hash)):
        raise BackendError("Native file writes require their approved original SDK patch call")
    token = _native_patch_call.set(NativePatchCall(call_id, sdk_id, arguments_hash))
    try:
        yield
    finally:
        _native_patch_call.reset(token)


def current_native_patch_call() -> NativePatchCall | None:
    return _native_patch_call.get()
