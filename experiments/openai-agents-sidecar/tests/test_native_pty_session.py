import asyncio
from copy import deepcopy
from dataclasses import replace
import json
from pathlib import Path

import pytest
from agents import RunState, Runner
from agents.run_config import SandboxRunConfig
from agents.sandbox.session.pty_types import truncate_text_by_tokens

from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.native_execution import current_native_shell_call, native_shell_call_scope
from content_agent_sidecar.native_pty import NativePTYReceipt
from content_agent_sidecar.native_pty_session import RuntimeSandboxPTYSession
from content_agent_sidecar.native_live_state import NativePTYProcessState
from content_agent_sidecar.native_session import RuntimeSandboxClient
from test_agent_tools import _context
from test_native_capabilities import CapturingModel, assemble, native_backend, tool_response
from test_native_file_session import DiskWorkspaceFixture
from test_run_state_approval import _text_response


class PTYFixture(DiskWorkspaceFixture):
    """Real SDK/files, simulated terminal receipts; no host process execution."""

    def __init__(self, root):
        super().__init__(root)
        self.events = []
        self.pty_calls = []
        self.failure = None
        self.output = b"ready\x00\xff"
        self.exit_on_input = False

    def exchange(self, kind, arguments, kwargs):
        assert current_native_shell_call() is not None
        self.events.append(kind)
        self.pty_calls.append((kind, arguments, kwargs))
        if self.failure == "cancel":
            raise asyncio.CancelledError()
        if self.failure == "lost":
            raise BackendError("Receipt lost")
        if self.failure == "malformed":
            return None
        sequence = kwargs.get("expected_sequence", 0) + 1
        code = 23 if kind == "input" and self.exit_on_input else None
        self.pty_processes[1000] = NativePTYProcessState(pty_session_id=1000, process_id="d" * 32, sequence=sequence,
            tty=kwargs.get("tty", self.pty_processes[1000].tty if 1000 in self.pty_processes else False),
            status="exited" if code is not None else "running")
        return NativePTYReceipt(session_id=1000, sequence=sequence, process_id="d" * 32,
            output=self.output, exit_code=code, reason="exited" if code is not None else "running",
            input_bytes=len(kwargs.get("chars", "").encode()))

    async def pty_start(self, *args, **kwargs):
        return self.exchange("start", args, kwargs)

    async def pty_input(self, *args, **kwargs):
        return self.exchange("input", args, kwargs)

    async def terminate_pty(self):
        self.events.append("terminate")
        if self.failure:
            raise BackendError("Termination unconfirmed")
        await super().terminate_pty()

    async def export_workspace(self):
        self.events.append("export")
        return await super().export_workspace()

    async def shutdown(self):
        self.events.append("shutdown")
        return await super().shutdown()


async def create_session(tmp_path):
    disk = PTYFixture(tmp_path)
    client = RuntimeSandboxClient(disk, RuntimeSandboxPTYSession)
    session = await client.create()
    await session.start()
    return disk, client, session


def scope(kind, **arguments):
    return native_shell_call_scope(kind, "sdk-" + kind, json.dumps(arguments))


def test_public_sdk_pty_start_poll_input_exit_and_stop_before_snapshot(tmp_path):
    async def scenario():
        disk, client, session = await create_session(tmp_path)
        assert session.supports_pty()
        with scope("start", cmd="read input", tty=True):
            started = await session.pty_exec_start("read input", tty=True)
        assert started.process_id == 1000 and started.exit_code is None
        assert session.state.active_pty
        assert started.output == b"ready\x00\xef\xbf\xbd"
        assert disk.pty_calls[0][1][3] == ["sh", "-lc", "read input"]
        assert disk.pty_calls[0][2]["yield_ms"] == 10000
        with scope("poll", session_id=1000, chars=""):
            polled = await session.pty_write_stdin(session_id=1000, chars="", yield_time_s=.001)
        assert polled.process_id == 1000
        assert disk.pty_calls[-1][2]["yield_ms"] == 5000
        assert disk.pty_calls[-1][2]["expected_sequence"] == 1
        disk.exit_on_input = True
        disk.output = b"response " * 1000
        expected, tokens = truncate_text_by_tokens(disk.output.decode(), 25)
        with scope("input", session_id=1000, chars="hello\n"):
            finished = await session.pty_write_stdin(session_id=1000, chars="hello\n", max_output_tokens=25)
        assert finished.process_id is None and finished.exit_code == 23
        assert not session.state.active_pty
        assert finished.output == expected.encode() and finished.original_token_count == tokens
        assert disk.pty_calls[-1][2]["expected_sequence"] == 2
        assert disk.pty_calls[-1][2]["yield_ms"] == 250
        with scope("after-exit", session_id=1000, chars=""):
            with pytest.raises(BackendError, match="unavailable"):
                await session.pty_write_stdin(session_id=1000, chars="")
        # Bytes are written by the fixture, not an unaudited SDK model command.
        (disk.root / "result.bin").write_bytes(b"\x00\xffresult")
        await session.aclose()
        reference = client.require_confirmed_cleanup()
        assert reference.version == 1
        assert disk.events == ["start", "input", "input", "terminate", "export", "shutdown"]
        await client.close_owned_session()
        assert disk.events.count("terminate") == 1 and disk.closes == 1
        assert not disk.commands and not disk.helpers and current_native_shell_call() is None
    asyncio.run(scenario())


@pytest.mark.parametrize("fault", ["unaudited", "changed-mode", "user", "timeout", "yield", "budget"])
def test_pty_preflight_rejects_invalid_execution_without_effects(tmp_path, fault):
    async def scenario():
        disk, client, session = await create_session(tmp_path)
        kwargs = {"tty": True}
        if fault != "unaudited":
            key, value = {"changed-mode": ("tty", False), "user": ("user", "root"), "timeout": ("timeout", 31),
                          "yield": ("yield_time_s", float("nan")), "budget": ("max_output_tokens", True)}[fault]
            kwargs[key] = value
            with scope("start", cmd="read input", tty=True), pytest.raises(BackendError):
                await session.pty_exec_start("read input", **kwargs)
        else:
            with pytest.raises(BackendError):
                await session.pty_exec_start("read input", **kwargs)
        assert not disk.events and not session.state.pending_pty
        await client.close_owned_session()
        client.require_confirmed_cleanup()
    asyncio.run(scenario())


@pytest.mark.parametrize("fault", ["lost", "cancel", "malformed"])
@pytest.mark.parametrize("operation", ["start", "input"])
def test_unknown_pty_outcome_survives_state_and_blocks_io_save_cleanup_reopen(tmp_path, fault, operation):
    async def scenario():
        disk, client, session = await create_session(tmp_path)
        if operation == "input":
            with scope("start", cmd="read input", tty=True):
                await session.pty_exec_start("read input", tty=True)
        disk.failure = fault
        with pytest.raises(asyncio.CancelledError if fault == "cancel" else BackendError):
            if operation == "start":
                with scope("start", cmd="read input", tty=True):
                    await session.pty_exec_start("read input", tty=True)
            else:
                with scope("input", session_id=1000, chars="x"):
                    await session.pty_write_stdin(session_id=1000, chars="x")
        assert session.state.pending_pty
        saved = client.serialize_session_state(session.state)
        assert saved["pending_pty"] is True
        disk.failure = None
        for action in (lambda: session.read(Path("note.txt")), client.publication_snapshot,
                       session.pty_terminate_all, client.close_owned_session):
            with pytest.raises(BackendError):
                await action()
        with pytest.raises(BackendError):
            client.require_confirmed_cleanup()
        disk.lease = None
        rebuilt = RuntimeSandboxClient(disk, RuntimeSandboxPTYSession)
        restored = await rebuilt.resume(rebuilt.deserialize_session_state(saved))
        with pytest.raises(BackendError):
            await restored.start()
        assert disk.events == (["start", "input"] if operation == "input" else ["start"])
        assert not disk.restores and not disk.closes
    asyncio.run(scenario())


def test_uncertain_terminal_termination_is_not_retried_or_saved_by_sdk_cleanup(tmp_path):
    async def scenario():
        disk, client, session = await create_session(tmp_path)
        with scope("start", cmd="read input", tty=True):
            await session.pty_exec_start("read input", tty=True)
        disk.failure = "lost"
        with pytest.raises(Exception):
            await session.aclose()
        assert session.state.pending_pty
        disk.failure = None
        for action in (client.close_owned_session, session.pty_terminate_all, client.publication_snapshot):
            with pytest.raises(BackendError):
                await action()
        assert disk.events == ["start", "terminate"] and not disk.closes
        with pytest.raises(BackendError):
            client.require_confirmed_cleanup()
    asyncio.run(scenario())


def pty_backend():
    backend = native_backend()
    descriptor = deepcopy(next(item for item in backend.catalog["tools"] if item["name"] == "exec_command"))
    descriptor.update(id="runtime:write_stdin", name="write_stdin")
    backend.catalog["tools"].append(descriptor)
    return backend


def sdk_reply(name, arguments):
    reply = tool_response(name, arguments)
    reply.output[0].call_id = name + "-call"
    return reply


@pytest.mark.parametrize("decision", ["approve", "reject"])
def test_actual_sdk_shell_pauses_twice_with_public_live_session_and_exact_audit(tmp_path, decision):
    async def scenario():
        disk = PTYFixture(tmp_path)
        backend, context = pty_backend(), _context()
        model = CapturingModel([sdk_reply("exec_command", {"cmd": "read input", "tty": True, "login": False}),
                                sdk_reply("write_stdin", {"session_id": 1000, "chars": "hello\n"}), _text_response()])
        client = RuntimeSandboxClient(disk, RuntimeSandboxPTYSession)
        agent, config = await assemble(backend, context, client, model)
        session = await client.create()
        await session.start()
        config = replace(config, sandbox=SandboxRunConfig(session=session))
        result = await Runner.run(agent, "Run and reply", context=context, run_config=config)
        assert len(result.interruptions) == 1 and not disk.events
        assert {"exec_command", "write_stdin"} <= {tool.name for tool in model.requests[0]["tools"]}
        for name, approved in (("exec_command", True), ("write_stdin", decision == "approve")):
            raw = result.to_state().to_json(context_serializer=lambda value: {"project_id": value.project_id})
            state = await RunState.from_json(agent, raw, context_override=context)
            backend.calls[name + "-call"]["status"] = "approved" if approved else "rejected"
            context.active_tool_calls[name + "-call"]["status"] = "approved" if approved else "rejected"
            item = state.get_interruptions()[0]
            state.approve(item) if approved else state.reject(item)
            result = await Runner.run(agent, state, context=context, run_config=config)
            assert "terminate" not in disk.events and not disk.closes and not disk.restores
            if name == "exec_command":
                assert len(result.interruptions) == 1 and disk.events == ["start"]
        assert result.final_output and not result.interruptions
        assert len(backend.completed) == (2 if decision == "approve" else 1) and not backend.failed
        assert disk.events == (["start", "input"] if decision == "approve" else ["start"])
        if decision == "approve":
            assert disk.pty_calls[-1][1][1] == "write_stdin-call"
            assert disk.pty_calls[-1][1][2] == {"session_id": 1000, "chars": "hello\n"}
        await client.close_owned_session()
        client.require_confirmed_cleanup()
        assert disk.events[-3:] == ["terminate", "export", "shutdown"]
        assert not disk.commands and not disk.helpers
    asyncio.run(scenario())


def test_published_file_snapshot_does_not_represent_a_live_process(tmp_path):
    async def scenario():
        disk, client, session = await create_session(tmp_path)
        with scope("start", cmd="read input", tty=True):
            await session.pty_exec_start("read input", tty=True)
        reference, _ = await client.publication_snapshot()
        saved = client.serialize_session_state(session.state)
        assert saved["active_pty"] is True and reference.version == 1
        assert disk.events == ["start", "export"]
        rebuilt = RuntimeSandboxClient(disk, RuntimeSandboxPTYSession, expected_snapshot=reference)
        with pytest.raises(BackendError):
            rebuilt.deserialize_session_state(saved)
        rebuilt = RuntimeSandboxClient(disk, RuntimeSandboxPTYSession)
        restored = await rebuilt.resume(rebuilt.deserialize_session_state(saved))
        with pytest.raises(BackendError, match="snapshot cannot restore"):
            await restored.start()
        assert not disk.restores
        # The original live owner is still usable and can clean up exactly once.
        with scope("poll", session_id=1000, chars=""):
            await session.pty_write_stdin(session_id=1000, chars="")
        await client.close_owned_session()
        assert not session.state.active_pty and client.require_confirmed_cleanup().version == 2
        assert disk.events == ["start", "export", "input", "terminate", "export", "shutdown"]
    asyncio.run(scenario())


def test_noninteractive_process_can_be_polled_but_cannot_receive_stdin(tmp_path):
    async def scenario():
        disk, client, session = await create_session(tmp_path)
        with scope("start", cmd="long command"):
            await session.pty_exec_start("long command", shell=False)
        assert disk.pty_calls[0][1][3] == ["long command"]
        with scope("poll", session_id=1000, chars=""):
            assert (await session.pty_write_stdin(session_id=1000, chars="")).process_id == 1000
        with scope("input", session_id=1000, chars="x"):
            with pytest.raises(BackendError, match="no interactive stdin"):
                await session.pty_write_stdin(session_id=1000, chars="x")
        assert not session.state.pending_pty and disk.events == ["start", "input"]
        await client.close_owned_session()
        client.require_confirmed_cleanup()
    asyncio.run(scenario())


@pytest.mark.parametrize("first", ["terminal", "file", "snapshot"])
def test_sdk_concurrent_file_terminal_and_snapshot_use_one_environment_queue(tmp_path, first):
    async def scenario():
        disk, client, session = await create_session(tmp_path)
        initial_file_calls = len(disk.file_calls)
        (disk.root / "note.txt").write_bytes(b"saved bytes")
        entered, release = asyncio.Event(), asyncio.Event()
        attribute = {"terminal": "pty_start", "file": "file_operation", "snapshot": "export_workspace"}[first]
        original = getattr(disk, attribute)

        async def held(*args, **kwargs):
            entered.set()
            await release.wait()
            return await original(*args, **kwargs)

        setattr(disk, attribute, held)

        async def execute(kind):
            if kind == "terminal":
                with scope("start", cmd="read input", tty=True):
                    return await session.pty_exec_start("read input", tty=True)
            if kind == "file":
                with await session.read(Path("note.txt")) as stream:
                    return stream.read()
            return await client.publication_snapshot()

        async with asyncio.timeout(5):
            task = asyncio.create_task(execute(first))
            await entered.wait()
            other = [asyncio.create_task(execute(kind)) for kind in ("terminal", "file", "snapshot") if kind != first]
            await asyncio.sleep(0)
            assert not any(item.done() for item in other)
            release.set()
            values = await asyncio.gather(task, *other)
        assert b"saved bytes" in values
        assert len(disk.pty_calls) == 1 and len(disk.file_calls) == initial_file_calls + 1
        assert not session.state.pending_pty and session.state.pending_file is None
        assert disk.events.count("export") == 1 and len(disk.saves) == 1
        await client.close_owned_session()
        client.require_confirmed_cleanup()
        assert disk.events.count("terminate") == 1
    asyncio.run(scenario())


def test_native_stdin_guardrail_blocks_protected_input_before_registration(tmp_path):
    async def scenario():
        disk = PTYFixture(tmp_path)
        backend, context = pty_backend(), _context()
        model = CapturingModel([sdk_reply("write_stdin", {"session_id": 1000, "chars": "PRIVATE_NATIVE_VALUE"})])
        client = RuntimeSandboxClient(disk, RuntimeSandboxPTYSession)
        agent, config = await assemble(backend, context, client, model, policy=SDKGuardrailPolicy(protected_values=("PRIVATE_NATIVE_VALUE",)))
        with pytest.raises(Exception, match="policy"):
            await Runner.run(agent, "Write input", context=context, run_config=config)
        client.require_confirmed_cleanup()
        assert not backend.begun and not disk.pty_calls
    asyncio.run(scenario())
