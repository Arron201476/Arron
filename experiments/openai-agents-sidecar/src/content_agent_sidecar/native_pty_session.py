from __future__ import annotations

from dataclasses import dataclass
import math

from agents.sandbox.session.pty_types import (
    PtyExecUpdate, clamp_pty_yield_time_ms, resolve_pty_write_yield_time_ms, truncate_text_by_tokens,
)

from .backend import BackendError
from .native_execution import require_native_shell_call
from .native_file_session import RuntimeSandboxFileSession
from .native_live_state import NativePTYState
from .native_pty import NativePTYReceipt, pty_input_operation, pty_start_operation, validate_pty_identity


@dataclass(frozen=True)
class _Terminal:
    process_id: str
    sequence: int
    tty: bool
    exited: bool


def _yield_millis(seconds, *, default, empty_input=False):
    if seconds is None:
        seconds = default
    if type(seconds) not in {int, float} or not math.isfinite(seconds) or seconds < 0:
        raise BackendError("Terminal yield must be finite and non-negative")
    value = math.ceil(min(seconds, 30) * 1000)
    return (resolve_pty_write_yield_time_ms(yield_time_ms=value, input_empty=True) if empty_input
            else clamp_pty_yield_time_ms(value))


class RuntimeSandboxPTYSession(RuntimeSandboxFileSession):
    """SDK PTY provider; Runner ownership must preserve live sessions at approval."""

    def __init__(self, state, transport):
        super().__init__(state, transport)
        self._terminals: dict[int, _Terminal] = {}
        self._pty_lock = self._io_lock
        self._pty_stopped = False
        self._live_binding: NativePTYState | None = None
        self.environment_id = ""

    def bind_live_state(self, state):
        if self._started or self._live_binding is not None or type(state) is not NativePTYState:
            raise BackendError("Terminal recovery requires one authoritative binding before startup")
        state = NativePTYState.model_validate(state.model_dump())
        if state.session_id != str(self.state.session_id) or state.state != "ready":
            raise BackendError("A live terminal cannot move to another environment")
        self._live_binding = state.model_copy(deep=True)

    async def _ensure_backend_started(self):
        if self._live_binding is None:
            await super()._ensure_backend_started()
            current = await self.transport.pty_state()
            if current.state != "ready" or current.session_id != str(self.state.session_id) or current.processes:
                raise BackendError("A fresh or file-restored workspace cannot inherit unrelated processes")
        else:
            await self.transport.validate()
            current = await self.transport.pty_state()
            if current != self._live_binding or await self.transport.probe() != "ready":
                raise BackendError("The saved live environment changed; it was not replaced from a file snapshot")
            if await self.transport.pty_state() != current:
                raise BackendError("Terminal bindings changed during environment reconnection")
            self._restored = True
            self._set_start_state_preserved(True, system=True)
        self.environment_id = current.environment_id
        self._terminals = {item.pty_session_id: _Terminal(item.process_id, item.sequence, item.tty, item.status == "exited")
                           for item in current.processes}
        self.state.active_pty = any(not item.exited for item in self._terminals.values())

    async def confirmed_live_binding(self):
        async with self._io_lock:
            self._ready_for_pty()
            current = await self.transport.pty_state()
            expected = {key: (item.process_id, item.sequence, item.tty, item.exited) for key, item in self._terminals.items()}
            actual = {item.pty_session_id: (item.process_id, item.sequence, item.tty, item.status == "exited") for item in current.processes}
            if (current.state != "ready" or current.session_id != str(self.state.session_id)
                    or current.environment_id != self.environment_id or actual != expected):
                raise BackendError("Workspace checkpoint does not match its confirmed terminal bindings")
            return current

    def supports_pty(self):
        return True

    def _ready_for_pty(self):
        self._check_binding()
        if (not self._started or self._start_failed or self._cleanup_pending or self._persisted_reference is not None
                or self.state.pending_file is not None or self.state.pending_restore is not None
                or self.state.pending_pty or self._pty_stopped):
            raise BackendError("Terminal I/O requires a live confirmed workspace before cleanup")

    @staticmethod
    def _output_budget(max_output_tokens):
        if max_output_tokens is not None and (type(max_output_tokens) is not int or max_output_tokens < 1):
            raise BackendError("Terminal output token limit must be a positive integer")

    def _consume(self, receipt, *, tty, max_output_tokens):
        if type(receipt) is not NativePTYReceipt:
            raise BackendError("Terminal I/O has no verified Runtime receipt")
        self._terminals[receipt.session_id] = _Terminal(receipt.process_id, receipt.sequence, tty, receipt.exit_code is not None)
        self.state.active_pty = any(not terminal.exited for terminal in self._terminals.values())
        text, count = truncate_text_by_tokens(receipt.output.decode("utf-8", errors="replace"), max_output_tokens)
        self.state.pending_pty = False
        return PtyExecUpdate(process_id=receipt.session_id if receipt.exit_code is None else None,
                             output=text.encode("utf-8"), exit_code=receipt.exit_code, original_token_count=count)

    async def pty_exec_start(self, *command, timeout=None, shell=True, user=None, tty=False,
                             yield_time_s=None, max_output_tokens=None):
        self._output_budget(max_output_tokens)
        if timeout is not None and (type(timeout) not in {int, float} or not math.isfinite(timeout) or not 0 < timeout <= 30):
            raise BackendError("Terminal timeout exceeds the workspace policy")
        argv = self._prepare_exec_command(*command, shell=shell, user=user)
        yield_ms = _yield_millis(yield_time_s, default=10)
        timeout_ms = 0 if timeout is None else max(1, math.ceil(timeout * 1000))
        pty_start_operation(argv, cwd="/workspace", tty=tty, yield_ms=yield_ms, timeout_ms=timeout_ms)
        call = require_native_shell_call()
        arguments = call.arguments()
        validate_pty_identity(call.agent_tool_call_id, call.sdk_tool_call_id, arguments)
        if arguments.get("tty", False) is not tty:
            raise BackendError("Terminal mode differs from its original approval")
        async with self._pty_lock:
            self._ready_for_pty()
            self.state.pending_pty = True
            receipt = await self.transport.pty_start(call.agent_tool_call_id, call.sdk_tool_call_id, arguments, argv,
                                                     tty=tty, yield_ms=yield_ms, timeout_ms=timeout_ms)
            if type(receipt) is not NativePTYReceipt:
                raise BackendError("Terminal I/O has no verified Runtime receipt")
            if receipt.session_id in self._terminals:
                raise BackendError("A terminal startup receipt reused an existing process identity")
            return self._consume(receipt, tty=tty, max_output_tokens=max_output_tokens)

    async def pty_write_stdin(self, *, session_id, chars, yield_time_s=None, max_output_tokens=None):
        self._output_budget(max_output_tokens)
        call = require_native_shell_call()
        arguments = call.arguments()
        validate_pty_identity(call.agent_tool_call_id, call.sdk_tool_call_id, arguments)
        yield_ms = _yield_millis(yield_time_s, default=.25, empty_input=chars == "")
        async with self._pty_lock:
            self._ready_for_pty()
            terminal = self._terminals.get(session_id) if type(session_id) is int else None
            if terminal is None or terminal.exited:
                raise BackendError("The original terminal is unavailable; no process was restarted", code="NATIVE_WORKSPACE_PTY_SESSION_LOST")
            pty_input_operation(session_id, terminal.sequence, chars, yield_ms)
            if type(arguments.get("session_id")) is not int or arguments["session_id"] != session_id or arguments.get("chars", "") != chars:
                raise BackendError("Terminal input differs from its original approval")
            if chars and not terminal.tty:
                raise BackendError("This process has no interactive stdin", code="NATIVE_WORKSPACE_PTY_STDIN_UNAVAILABLE")
            self.state.pending_pty = True
            receipt = await self.transport.pty_input(call.agent_tool_call_id, call.sdk_tool_call_id, arguments,
                session_id=session_id, expected_sequence=terminal.sequence, expected_process_id=terminal.process_id,
                chars=chars, yield_ms=yield_ms)
            return self._consume(receipt, tty=terminal.tty, max_output_tokens=max_output_tokens)

    async def pty_terminate_all(self):
        async with self._pty_lock:
            if self._closed or self._pty_stopped:
                return
            self._check_binding()
            if self.state.pending_pty or self.state.pending_file is not None or self.state.pending_restore is not None or self._start_failed:
                raise BackendError("Unconfirmed workspace I/O forbids automatic terminal cleanup")
            self.state.pending_pty = True
            await self.transport.terminate_pty()
            self._pty_stopped = True
            self.state.pending_pty = False
            self.state.active_pty = False
            self._terminals.clear()
