"""Single-command PTY bridge, embedded by Go and run only inside its OCI workspace."""

import base64
import errno
import json
import os
import posixpath
import selectors
import signal
import sys
import time


MAX_INPUT = 64 << 10
MAX_OUTPUT = 1 << 20
MAX_FRAME = 128 << 10
ROOT = "/workspace"


class ProtocolError(Exception):
    pass


def parse_frame(raw):
    def unique_pairs(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ProtocolError("duplicate field")
            result[key] = value
        return result

    if not isinstance(raw, bytes) or not raw.endswith(b"\n") or len(raw) > MAX_FRAME:
        raise ProtocolError("invalid frame")
    try:
        value = json.loads(raw, object_pairs_hook=unique_pairs)
    except (ValueError, UnicodeError) as exc:
        raise ProtocolError("invalid JSON") from exc
    if not isinstance(value, dict):
        raise ProtocolError("expected object")
    return value


def integer(value, low, high):
    return type(value) is int and low <= value <= high


def start_arguments(value):
    if set(value) != {"sequence", "operation", "argv", "cwd", "tty", "timeout_ms", "yield_ms"}:
        raise ProtocolError("invalid startup fields")
    argv, cwd = value["argv"], value["cwd"]
    if (value["operation"] != "start" or type(value["sequence"]) is not int or value["sequence"] != 1
            or not isinstance(argv, list) or not 1 <= len(argv) <= 256
            or any(not isinstance(arg, str) or "\x00" in arg for arg in argv)
            or not argv[0].strip() or argv[0].startswith("-") or "=" in argv[0]
            or sum(len(arg.encode("utf-8")) for arg in argv) > 64 << 10
            or not isinstance(cwd, str) or posixpath.normpath(cwd) != cwd
            or (cwd != ROOT and not cwd.startswith(ROOT + "/"))
            or any(char in cwd for char in "\\\x00\r\n") or type(value["tty"]) is not bool
            or not integer(value["timeout_ms"], 1, 300000) or not integer(value["yield_ms"], 1, 30000)):
        raise ProtocolError("invalid startup values")
    return value


def input_arguments(value, sequence, tty):
    if set(value) != {"sequence", "operation", "input", "yield_ms"}:
        raise ProtocolError("invalid input fields")
    if (type(value["sequence"]) is not int or value["sequence"] != sequence
            or value["operation"] not in {"input", "terminate"}
            or not isinstance(value["input"], str) or len(value["input"]) > (MAX_INPUT * 4 + 2) // 3
            or not integer(value["yield_ms"], 1, 30000)):
        raise ProtocolError("invalid input values")
    try:
        data = base64.b64decode(value["input"], validate=True)
    except ValueError as exc:
        raise ProtocolError("invalid input encoding") from exc
    if (len(data) > MAX_INPUT or base64.b64encode(data).decode("ascii") != value["input"]
            or data and (not tty or value["operation"] != "input")):
        raise ProtocolError("input is not available")
    return data


class Child:
    """Keep the child unreaped until its process group has been signalled."""

    def __init__(self, arguments):
        if os.name != "posix" or not all(hasattr(os, name) for name in ("fork", "waitid", "WNOWAIT")):
            raise ProtocolError("POSIX process ownership is unavailable")
        signal.signal(signal.SIGCHLD, signal.SIG_DFL)
        if arguments["tty"]:
            import pty

            pid, descriptor = pty.fork()
            if pid == 0:
                self._exec(arguments)
        else:
            descriptor, output = os.pipe()
            try:
                pid = os.fork()
            except BaseException:
                os.close(descriptor)
                os.close(output)
                raise
            if pid == 0:
                try:
                    os.close(descriptor)
                    os.setsid()
                    incoming = os.open(os.devnull, os.O_RDONLY)
                    os.dup2(incoming, 0)
                    os.dup2(output, 1)
                    os.dup2(output, 2)
                    os.close(incoming)
                    os.close(output)
                    self._exec(arguments)
                finally:
                    os._exit(127)
            os.close(output)
        self.pid, self.descriptor = pid, descriptor
        self.exit_code = None
        self.closed = False
        os.set_blocking(descriptor, False)

    @staticmethod
    def _exec(arguments):
        try:
            os.chdir(arguments["cwd"])
            environment = {"PATH": "/usr/local/bin:/usr/bin:/bin", "LANG": "C.UTF-8", "HOME": ROOT}
            if arguments["tty"]:
                import fcntl
                import struct
                import termios

                fcntl.ioctl(0, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
                environment["TERM"] = "xterm"
            os.execvpe(arguments["argv"][0], arguments["argv"], environment)
        except BaseException:
            try:
                os.write(2, b"Command startup failed.\n")
            finally:
                os._exit(127)

    def poll(self):
        if self.exit_code is not None:
            return self.exit_code
        status = os.waitid(os.P_PID, self.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT)
        if status is None:
            return None
        self.exit_code = status.si_status if status.si_code == os.CLD_EXITED else 128 + status.si_status
        return self.exit_code

    def read(self):
        try:
            return os.read(self.descriptor, 65536)
        except BlockingIOError:
            return None
        except OSError as exc:
            if exc.errno == errno.EIO:
                return b""
            raise

    def write(self, data):
        try:
            return os.write(self.descriptor, data)
        except BlockingIOError:
            return 0
        except OSError as exc:
            if exc.errno in {errno.EPIPE, errno.EIO} and self.poll() is not None:
                return 0
            raise

    def terminate(self):
        if self.closed:
            return
        # The unreaped leader reserves its pid; never signal a reused process group.
        try:
            os.killpg(self.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        # A just-forked child may not have entered its new session yet.
        try:
            os.kill(self.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        deadline = time.monotonic() + 2
        while self.poll() is None:
            if time.monotonic() >= deadline:
                raise ProtocolError("process termination is unconfirmed")
            time.sleep(0.01)
        os.waitpid(self.pid, 0)
        self.closed = True

    def close(self):
        try:
            self.terminate()
        finally:
            os.close(self.descriptor)


class Conversation:
    """Bound output and input while the agent is between poll requests."""

    def __init__(self, arguments, child, now=time.monotonic):
        self.arguments, self.child, self.now = arguments, child, now
        self.deadline = now() + arguments["timeout_ms"] / 1000
        self.yield_until = now() + arguments["yield_ms"] / 1000
        self.sequence = 1
        self.pending = True
        self.output = bytearray()
        self.input = b""
        self.input_bytes = 0
        self.reason = "running"
        self.exit_code = None

    def receive(self, value):
        if self.pending:
            raise ProtocolError("no input request is expected")
        data = input_arguments(value, self.sequence + 1, self.arguments["tty"])
        self.sequence += 1
        self.pending = True
        self.yield_until = self.now() + value["yield_ms"] / 1000
        self.input = data
        self.input_bytes = 0
        if value["operation"] == "terminate":
            self.child.terminate()
            self.reason, self.exit_code = "terminated", self.child.poll()

    def _read_output(self):
        # A bounded number of reads prevents an infinite producer starving cancellation.
        for _ in range(17):
            data = self.child.read()
            if not data:
                break
            if len(self.output) + len(data) > MAX_OUTPUT:
                self.child.terminate()
                self.reason, self.exit_code = "output_limit", self.child.poll()
                self.output.clear()
                break
            self.output.extend(data)

    def advance(self):
        if self.exit_code is None and self.child.poll() is not None:
            self.child.terminate()
            self.reason, self.exit_code = "exited", self.child.poll()
        if self.input and self.exit_code is None:
            written = self.child.write(self.input)
            self.input = self.input[written:]
            self.input_bytes += written
        self._read_output()
        if self.exit_code is None:
            self.exit_code = self.child.poll()
            if self.exit_code is not None:
                self.child.terminate()
                self.reason = "exited"
                self._read_output()
            elif self.now() >= self.deadline:
                self.child.terminate()
                self.reason, self.exit_code = "timeout", self.child.poll()
        if self.pending and (self.exit_code is not None or not self.input and self.now() >= self.yield_until):
            result = {"sequence": self.sequence, "output": base64.b64encode(self.output).decode("ascii"),
                      "exit_code": self.exit_code, "reason": self.reason, "input_bytes": self.input_bytes}
            self.output.clear()
            self.pending = False
            return result
        return None


def run(stdin, stdout, *, child_type=Child, now=time.monotonic, selector_type=selectors.DefaultSelector):
    arguments = start_arguments(parse_frame(stdin.readline(MAX_FRAME + 1)))
    child = child_type(arguments)
    try:
        conversation = Conversation(arguments, child, now)
        with selector_type() as selector:
            selector.register(stdin.fileno(), selectors.EVENT_READ)
            buffered = b""
            while True:
                result = conversation.advance()
                if result is not None:
                    stdout.write(json.dumps(result, separators=(",", ":")).encode("ascii") + b"\n")
                    stdout.flush()
                    if result["exit_code"] is not None:
                        return
                for _key, _events in selector.select(0.01):
                    data = os.read(stdin.fileno(), MAX_FRAME + 1)
                    if not data:
                        return
                    buffered += data
                    if len(buffered) > MAX_FRAME:
                        raise ProtocolError("oversized input frame")
                    if b"\n" in buffered:
                        line, buffered = buffered.split(b"\n", 1)
                        if buffered:
                            raise ProtocolError("pipelined input is forbidden")
                        conversation.receive(parse_frame(line + b"\n"))
    finally:
        child.close()


if __name__ == "__main__":
    try:
        # No buffered read-ahead across the initial frame and subsequent select loop.
        run(getattr(sys.stdin.buffer, "raw", sys.stdin.buffer), sys.stdout.buffer)
    except BaseException:
        os.write(2, b"Workspace terminal transport failed.\n")
        sys.exit(1)
