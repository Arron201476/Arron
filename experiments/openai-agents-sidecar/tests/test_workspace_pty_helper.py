import base64
import importlib.util
import io
import json
from pathlib import Path
from types import SimpleNamespace

import pytest


PATH = Path(__file__).resolve().parents[3] / "backend/internal/scriptsandbox/workspace_pty.py"
spec = importlib.util.spec_from_file_location("workspace_pty_under_test", PATH)
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)


def start(**changes):
    return {"sequence": 1, "operation": "start", "argv": ["python", "-c", "print('hello')"],
            "cwd": "/workspace", "tty": True, "timeout_ms": 30000, "yield_ms": 1000, **changes}


def incoming(sequence=2, data=b"", **changes):
    return {"sequence": sequence, "operation": "input", "input": base64.b64encode(data).decode(),
            "yield_ms": 1000, **changes}


class ChildFixture:
    """No OS command, signal, PTY, or real engine is used by these protocol tests."""

    def __init__(self, _arguments=None):
        self.output = []
        self.written = b""
        self.max_write = 65536
        self.exit_code = None
        self.terminations = 0
        self.closed = False
        self.write_failure = None

    def read(self):
        return self.output.pop(0) if self.output else None

    def write(self, value):
        if self.write_failure:
            raise self.write_failure
        count = min(len(value), self.max_write)
        self.written += value[:count]
        return count

    def poll(self):
        return self.exit_code

    def terminate(self):
        self.terminations += 1
        if self.exit_code is None:
            self.exit_code = 137

    def close(self):
        self.terminate()
        self.closed = True


def conversation(**changes):
    clock, child = [0.0], ChildFixture()
    state = helper.Conversation(helper.start_arguments(start(**changes)), child, lambda: clock[0])
    return state, child, clock


@pytest.mark.parametrize("change", [
    {"sequence": True}, {"sequence": 2}, {"operation": "input"}, {"argv": []}, {"argv": [""]},
    {"argv": ["-cmd"]}, {"argv": ["SECRET=value"]}, {"argv": ["python", "x\x00y"]},
    {"argv": ["python", "x" * 65537]}, {"argv": ["python", 1]}, {"cwd": "/etc"},
    {"cwd": "/workspace/../etc"}, {"cwd": "/workspace-old"}, {"cwd": "/workspace\\host"},
    {"tty": 1}, {"timeout_ms": True}, {"timeout_ms": 0}, {"timeout_ms": 300001},
    {"yield_ms": 0}, {"yield_ms": 30001}, {"extra": "ignored?"},
])
def test_startup_invalid_before_any_process_creation(change):
    with pytest.raises(helper.ProtocolError):
        helper.start_arguments(start(**change))


@pytest.mark.parametrize("raw", [b'{}', b'[]\n', b'{"sequence":1,"sequence":2}\n', b'null\n',
                                  b'{"bad":"\xff"}\n', b' ' * (helper.MAX_FRAME + 1) + b'\n'],
                         ids=["missing-newline", "array", "duplicate-key", "null", "bad-utf8", "oversize"])
def test_invalid_frames_fail_closed(raw):
    with pytest.raises(helper.ProtocolError):
        helper.parse_frame(raw)


@pytest.mark.parametrize("change", [
    {"sequence": 1}, {"sequence": True}, {"input": None}, {"input": "!bad!"},
    {"input": "Zh=="}, {"input": base64.b64encode(b"x" * (helper.MAX_INPUT + 1)).decode()},
    {"operation": "start"}, {"operation": "terminate", "input": "YQ=="}, {"yield_ms": True},
    {"yield_ms": 0}, {"yield_ms": 30001}, {"extra": "ignored?"},
])
def test_input_requires_exact_sequence_and_bounded_canonical_bytes(change):
    with pytest.raises(helper.ProtocolError):
        helper.input_arguments(incoming(**change), 2, True)


def test_noninteractive_process_allows_poll_but_not_stdin():
    assert helper.input_arguments(incoming(), 2, False) == b""
    with pytest.raises(helper.ProtocolError, match="not available"):
        helper.input_arguments(incoming(data=b"write"), 2, False)


def test_binary_output_multiple_polls_input_and_exact_exit():
    state, child, clock = conversation()
    child.output = [b"\x00\xffhello\n"]
    assert state.advance() is None
    clock[0] = 1
    first = state.advance()
    assert first == {"sequence": 1, "output": base64.b64encode(b"\x00\xffhello\n").decode(),
                     "exit_code": None, "reason": "running", "input_bytes": 0}
    state.receive(incoming(data=b"answer\n"))
    assert state.advance() is None
    assert child.written == b"answer\n"
    clock[0] = 2
    second = state.advance()
    assert second["input_bytes"] == 7 and second["sequence"] == 2 and second["output"] == ""
    state.receive(incoming(sequence=3))
    child.output, child.exit_code = [b"done\n"], 23
    final = state.advance()
    assert final["exit_code"] == 23 and final["reason"] == "exited"
    assert base64.b64decode(final["output"]) == b"done\n"


def test_partial_input_is_not_reported_as_fully_written_or_repeated():
    state, child, clock = conversation()
    clock[0] = 1
    state.advance()
    child.max_write = 2
    state.receive(incoming(data=b"abcdef"))
    clock[0] = 2
    assert state.advance() is None
    assert state.advance() is None
    final = state.advance()
    assert final["input_bytes"] == 6 and child.written == b"abcdef"
    assert state.advance() is None and child.written == b"abcdef"


def test_early_exit_returns_final_output_and_reports_unwritten_input():
    state, child, clock = conversation()
    clock[0] = 1
    state.advance()
    child.max_write = 1
    state.receive(incoming(data=b"abcdef"))
    state.advance()
    child.exit_code, child.output = 9, [b"early exit"]
    result = state.advance()
    assert result["exit_code"] == 9 and result["input_bytes"] == 1 and child.written == b"a"
    assert base64.b64decode(result["output"]) == b"early exit"


def test_idle_exit_is_returned_on_the_next_poll_without_relaunch():
    state, child, clock = conversation()
    clock[0] = 1
    state.advance()
    child.exit_code, child.output = 0, [b"late final output"]
    assert state.advance() is None
    state.receive(incoming(data=b"too late"))
    result = state.advance()
    assert result["exit_code"] == 0 and result["input_bytes"] == 0 and child.written == b""
    assert base64.b64decode(result["output"]) == b"late final output"


def test_timeout_is_command_lifetime_not_reset_by_poll_or_input():
    state, child, clock = conversation(timeout_ms=3000)
    clock[0] = 1
    assert state.advance()["reason"] == "running"
    state.receive(incoming(data=b"keep going"))
    clock[0] = 2
    assert state.advance()["reason"] == "running"
    clock[0] = 3
    assert state.advance() is None
    assert child.exit_code == 137
    state.receive(incoming(sequence=3))
    assert state.advance()["reason"] == "timeout"


def test_output_limit_applies_while_idle_and_does_not_return_partial_success():
    state, child, clock = conversation()
    clock[0] = 1
    state.advance()
    child.output = [b"x" * 65536] * 17
    assert state.advance() is None
    assert child.exit_code == 137
    state.receive(incoming())
    result = state.advance()
    assert result["reason"] == "output_limit" and result["output"] == ""


def test_explicit_termination_preserves_collected_output():
    state, child, clock = conversation()
    clock[0] = 1
    state.advance()
    child.output = [b"last output"]
    state.receive(incoming(operation="terminate"))
    result = state.advance()
    assert result["reason"] == "terminated" and result["exit_code"] == 137
    assert base64.b64decode(result["output"]) == b"last output"


def test_second_outstanding_request_is_rejected_before_writing_input():
    state, child, _clock = conversation()
    with pytest.raises(helper.ProtocolError):
        state.receive(incoming(data=b"must not be sent"))
    assert child.written == b""


class InputFixture(io.BytesIO):
    def fileno(self):
        return 123456  # Never passed to the real OS in these tests.


class SelectorFixture:
    def __enter__(self):
        return self

    def __exit__(self, *_args):
        pass

    def register(self, *_args):
        pass

    def select(self, _timeout):
        return [(None, None)]


@pytest.mark.parametrize("failure", ["eof", "bad_frame", "write_failure", "output_failure"])
def test_loop_always_terminates_owned_child_on_transport_failure(monkeypatch, failure):
    child = ChildFixture()
    pipe = InputFixture(json.dumps(start()).encode() + b"\n")
    frames = [json.dumps(incoming(data=b"answer")).encode() + b"\n"]
    ticks = iter([0, 0, 1, 1, 2, 2, 3, 3, 4, 4])
    if failure == "bad_frame":
        frames = [b'{"invalid":true}\n']
    elif failure == "write_failure":
        child.write_failure = OSError("input pipe failed")
    elif failure == "output_failure":
        class BadOutput(io.BytesIO):
            def write(self, _data):
                raise OSError("output pipe failed")
        output = BadOutput()
    if failure != "output_failure":
        output = io.BytesIO()
    if failure == "eof":
        frames = []
    monkeypatch.setattr(helper, "os", SimpleNamespace(read=lambda fd, count: frames.pop(0) if frames else b""))
    if failure == "eof":
        helper.run(pipe, output, child_type=lambda _: child, now=lambda: next(ticks), selector_type=SelectorFixture)
    else:
        with pytest.raises((helper.ProtocolError, OSError)):
            helper.run(pipe, output, child_type=lambda _: child, now=lambda: next(ticks), selector_type=SelectorFixture)
    assert child.closed and child.terminations > 0


@pytest.mark.parametrize("missing_group", [False, True])
def test_child_termination_keeps_pid_reserved_until_signalling_then_reaps(monkeypatch, missing_group):
    calls = []
    child = helper.Child.__new__(helper.Child)
    child.pid, child.descriptor, child.exit_code, child.closed = 555, 444, None, False

    def killpg(pid, sig):
        calls.append(("group", pid, sig))
        if missing_group:
            raise ProcessLookupError()

    def waitid(kind, pid, flags):
        calls.append(("observe-unreaped", kind, pid, flags))
        return SimpleNamespace(si_status=9, si_code=2)

    monkeypatch.setattr(helper, "os", SimpleNamespace(
        P_PID=1, WEXITED=2, WNOHANG=4, WNOWAIT=8, CLD_EXITED=1,
        killpg=killpg, kill=lambda pid, sig: calls.append(("child", pid, sig)),
        waitid=waitid, waitpid=lambda pid, flags: calls.append(("reap", pid, flags)),
        close=lambda fd: calls.append(("close", fd)),
    ))
    monkeypatch.setattr(helper, "signal", SimpleNamespace(SIGKILL=9))
    child.terminate()
    child.terminate()
    assert calls == [("group", 555, 9), ("child", 555, 9), ("observe-unreaped", 1, 555, 14), ("reap", 555, 0)]
    assert child.closed and child.poll() == 137


def test_unconfirmed_child_termination_does_not_reap_or_claim_closed(monkeypatch):
    clock = [0]
    calls = []
    child = helper.Child.__new__(helper.Child)
    child.pid, child.descriptor, child.exit_code, child.closed = 555, 444, None, False
    monkeypatch.setattr(helper, "os", SimpleNamespace(
        P_PID=1, WEXITED=2, WNOHANG=4, WNOWAIT=8,
        killpg=lambda *_: None, kill=lambda *_: None, waitid=lambda *_: None,
        waitpid=lambda *_: calls.append("must not reap"),
    ))
    monkeypatch.setattr(helper, "signal", SimpleNamespace(SIGKILL=9))
    monkeypatch.setattr(helper, "time", SimpleNamespace(monotonic=lambda: clock[0],
                                                       sleep=lambda _: clock.__setitem__(0, clock[0] + 1)))
    with pytest.raises(helper.ProtocolError, match="termination is unconfirmed"):
        child.terminate()
    assert not child.closed and calls == []
