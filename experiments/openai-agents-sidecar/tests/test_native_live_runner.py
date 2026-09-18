import asyncio
from copy import deepcopy
from dataclasses import replace

import pytest
from agents import Agent, RunState, Runner

from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.native_execution import require_native_shell_call
from content_agent_sidecar.native_live_state import NativePTYProcessState, restore_native_checkpoint
from content_agent_sidecar.native_pty import NativePTYReceipt
from content_agent_sidecar.native_runner import bind_native_agent
from content_agent_sidecar.native_session import RuntimeSandboxClient
from content_agent_sidecar.native_file_session import RuntimeSandboxFileSession
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport
from content_agent_sidecar.runtime import _serialize_agent_context
from content_agent_sidecar.stateful_execution import ExecutionTaskPaused
from test_native_runner import RunnerWorkspace, setup, tools_for, invoke
from test_native_capabilities import assemble, CapturingModel, tool_response
from test_native_manifest import sources
from test_stateful_execution import final_item, tool_item


class LiveRunnerWorkspace(RunnerWorkspace):
    def __init__(self, root):
        super().__init__(root)
        self.terminal_starts = []
        self.terminal_inputs = []
        self.terminal_stops = 0

    async def pty_start(self, call, sdk, arguments, argv, **kwargs):
        assert require_native_shell_call().sdk_tool_call_id == sdk
        self.terminal_starts.append((call, sdk, arguments, argv, kwargs))
        assert not self.pty_processes
        self.pty_processes[1000] = NativePTYProcessState(pty_session_id=1000, process_id="d" * 32, sequence=1, tty=True, status="running")
        return NativePTYReceipt(1000, 1, "d" * 32, b"ready for input", None, "running", 0)

    async def pty_input(self, call, sdk, arguments, **kwargs):
        assert require_native_shell_call().sdk_tool_call_id == sdk
        self.terminal_inputs.append((call, sdk, arguments, kwargs))
        item = self.pty_processes[kwargs["session_id"]]
        assert item.sequence == kwargs["expected_sequence"] == 1 and item.process_id == kwargs["expected_process_id"]
        self.pty_processes[1000] = item.model_copy(update={"sequence": 2, "status": "exited"})
        return NativePTYReceipt(1000, 2, item.process_id, b"accepted", 0, "exited", len(kwargs["chars"].encode()))

    async def terminate_pty(self):
        self.terminal_stops += 1
        await super().terminate_pty()


async def paused_json(mode, agent, runner_input, context, config):
    try:
        result = await invoke(mode, agent, runner_input, context, config)
        assert result.interruptions
        return result.to_state().to_json(context_serializer=_serialize_agent_context)
    except ExecutionTaskPaused as paused:
        return paused.run_state


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
@pytest.mark.parametrize("outcome", ["approve", "reject", "environment-lost"])
def test_real_entrypoints_rebind_original_terminal_across_two_owner_restarts(tmp_path, monkeypatch, mode, outcome):
    async def scenario():
        _, backend, context, model, config = setup(tmp_path, monkeypatch, mode, [
            [tool_item("exec_command", {"cmd": "read input", "tty": True, "login": False}, "start-terminal")],
            [tool_item("write_stdin", {"session_id": 1000, "chars": "hello\n"}, "input-terminal")],
            [final_item({"title": "Result checked"})],
        ])
        root = tmp_path / "live-owner"
        root.mkdir()
        disk = LiveRunnerWorkspace(root)
        monkeypatch.setattr(NativeWorkspaceHTTPTransport, "new", lambda *_args: disk)
        descriptor = deepcopy(next(item for item in backend.catalog["tools"] if item["name"] == "exec_command"))
        descriptor.update(id="runtime:write_stdin", name="write_stdin")
        backend.catalog["tools"].append(descriptor)

        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Interactive worker", model=model))
            saved = await paused_json(mode, agent, "Run an interactive task", context, config)
        first_environment = context.native_workspace_checkpoint.environment_id
        assert not disk.terminal_starts and not disk.closes

        context = replace(context, native_workspace=None, native_workspace_checkpoint=None, active_tool_calls={})
        restore_native_checkpoint(context, saved["context"]["context"])
        disk.lease = None
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Interactive worker", model=model))
            state = await RunState.from_json(agent, saved, context_override=context)
            state.approve(state.get_interruptions()[0])
            backend.calls["start-terminal"]["status"] = "approved"
            saved = await paused_json(mode, agent, state, context, config)
        assert len(disk.terminal_starts) == 1 and not disk.terminal_inputs and not disk.terminal_stops
        assert context.native_workspace_checkpoint.environment_id == first_environment
        assert context.native_workspace.state.active_pty and "sandbox" not in saved
        # A live process may write after the pause snapshot. Rebinding must not
        # hydrate the older archive over those bytes while the environment lives.
        (disk.root / "after-pause.txt").write_bytes(b"live process progress")
        if outcome == "environment-lost":
            await disk.shutdown()

        context = replace(context, native_workspace=None, native_workspace_checkpoint=None, active_tool_calls={})
        restore_native_checkpoint(context, saved["context"]["context"])
        disk.lease = None
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Interactive worker", model=model))
            state = await RunState.from_json(agent, saved, context_override=context)
            item = state.get_interruptions()[0]
            state.reject(item) if outcome == "reject" else state.approve(item)
            backend.calls["input-terminal"]["status"] = "rejected" if outcome == "reject" else "approved"
            result = await invoke(mode, agent, state, context, config)
        assert result.final_output and not result.interruptions
        assert len(disk.terminal_starts) == 1 and len(disk.terminal_inputs) == int(outcome == "approve")
        assert disk.terminal_stops == 1 and context.native_workspace_checkpoint.state == "closed"
        if outcome == "environment-lost":
            assert len(disk.restores) == 1 and not (disk.root / "after-pause.txt").exists()
            assert len(backend.failed) == 1 and len(backend.completed) == 1
        else:
            assert not disk.restores and (disk.root / "after-pause.txt").read_bytes() == b"live process progress"
            assert len(backend.completed) == (2 if outcome == "approve" else 1) and not backend.failed
        assert not disk.commands and not disk.helpers
    asyncio.run(scenario())


@pytest.mark.parametrize("fault", ["environment", "snapshot", "missing", "closed-to-live"])
def test_recovery_rejects_wrong_or_missing_checkpoint_without_model_or_restore(tmp_path, monkeypatch, fault):
    async def scenario():
        disk, backend, context, model, config = setup(tmp_path, monkeypatch, "main", [
            [tool_item("exec_command", {"cmd": "read input", "tty": True}, "pending")],
        ])
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Worker", model=model))
            saved = await paused_json("main", agent, "Run", context, config)
        checkpoint = saved["context"]["context"]["native_workspace_checkpoint"]
        if fault == "environment": checkpoint["environment_id"] = "00000000-0000-0000-0000-000000000099"
        elif fault == "snapshot": checkpoint["snapshot"]["sha256"] = "b" * 64
        elif fault == "closed-to-live": checkpoint["state"] = "closed"
        else: saved["context"]["context"].pop("native_workspace_checkpoint")
        context = replace(context, native_workspace=None, native_workspace_checkpoint=None, active_tool_calls={})
        restore_native_checkpoint(context, saved["context"]["context"])
        disk.lease = None
        prepared = await tools_for(backend, context)
        async with prepared:
            with pytest.raises(BackendError):
                agent = await bind_native_agent(context, config, Agent(name="Worker", model=model))
                state = await RunState.from_json(agent, saved, context_override=context)
                await invoke("main", agent, state, context, config)
        assert len(model.inputs) == 1 and not disk.restores and not disk.closes and not disk.commands
    asyncio.run(scenario())


@pytest.mark.parametrize("fault", ["", "snapshot", "agent-entry"])
def test_previous_sdk_owned_file_checkpoint_migrates_without_repeating_approved_command(tmp_path, monkeypatch, fault):
    async def scenario():
        disk, backend, context, model, config = setup(tmp_path, monkeypatch, "main", [[final_item({"title": "complete"})]])
        old_client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, sources=sources())
        old_agent, old_config = await assemble(backend, context, old_client,
            CapturingModel([tool_response("exec_command", {"cmd": "printf sample", "login": False})]))
        pending = await Runner.run(old_agent, "Run approved command", context=context, run_config=old_config)
        saved = pending.to_state().to_json(context_serializer=_serialize_agent_context)
        old_client.require_confirmed_cleanup()
        assert "sandbox" in saved and saved["context"]["context"]["native_workspace_checkpoint"] is None
        if fault:
            target = (saved["sandbox"]["session_state"] if fault == "snapshot" else
                      next(iter(saved["sandbox"]["sessions_by_agent"].values()))["session_state"])
            target["snapshot"]["reference"]["sha256"] = "e" * 64
        context = replace(context, native_workspace=None, active_tool_calls={})
        disk.lease = None
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Native assembly", model=model))
            state = await RunState.from_json(agent, saved, context_override=context)
            state.approve(state.get_interruptions()[0])
            backend.calls["native-call"]["status"] = "approved"
            if fault:
                with pytest.raises(BackendError):
                    await invoke("main", agent, state, context, config)
                assert not model.inputs and not disk.commands and not disk.restores
            else:
                result = await invoke("main", agent, state, context, config)
                assert result.final_output and len(disk.commands) == 1 and disk.closes == 2 and len(disk.restores) == 1
                assert context.native_workspace_checkpoint.state == "closed"
                assert "sandbox" not in result.to_state().to_json(context_serializer=_serialize_agent_context)
    asyncio.run(scenario())


def test_disabling_native_execution_does_not_downgrade_a_saved_workspace(tmp_path, monkeypatch):
    async def scenario():
        disk, backend, context, model, config = setup(tmp_path, monkeypatch, "main", [
            [tool_item("exec_command", {"cmd": "read input"}, "pending")],
        ])
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Worker", model=model))
            await paused_json("main", agent, "Run", context, config)
        with pytest.raises(BackendError, match="recovery is disabled"):
            await bind_native_agent(context, replace(config, native_workspace_enabled=False), Agent(name="Worker", model=model))
        assert not disk.commands and len(model.inputs) == 1
    asyncio.run(scenario())
