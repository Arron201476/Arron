import asyncio
from dataclasses import replace
import hashlib
import json

import pytest
from agents import Agent, RunState

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.background_worker import SDKBackgroundTaskWorker
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.managed_instructions import bind_managed_instructions
from content_agent_sidecar.native_runner import bind_native_agent
from content_agent_sidecar.native_live_state import restore_native_checkpoint
from content_agent_sidecar.native_snapshot import SnapshotReference
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport
from content_agent_sidecar.observability import TurnObservation
from content_agent_sidecar.runtime import AgentContext, OpenAIAgentsRuntime, _serialize_agent_context, commit_agent_action, stop_after_commit
from content_agent_sidecar.skill_agents import attach_skill_handoffs
from content_agent_sidecar.stateful_execution import ExecutionTaskPaused, StatefulExecution
from content_agent_sidecar.task_worker import SDKTaskWorker
from test_app import settings
from test_native_capabilities import native_backend
from test_native_manifest import BODIES, ManifestFixture, SKILL, sources
from test_stateful_execution import StreamingSequence, final_item, tool_item
from instruction_fixtures import EmptyInstructionsBackend


class RunnerWorkspace(ManifestFixture):
    """Real SDK/temp files; lease, policy and engine remain transport fixtures."""

    async def renew(self):
        return await self.validate()

    async def recovery(self):
        assert self.manifest_status == "completed" and self.versions
        version = max(version for _, version in self.versions)
        body = self.versions[(self.inventory.session_id, version)]
        return self.inventory, SnapshotReference(version=version, sha256=hashlib.sha256(body).hexdigest())


def setup(tmp_path, monkeypatch, mode, responses):
    disk = RunnerWorkspace(tmp_path)
    monkeypatch.setattr(NativeWorkspaceHTTPTransport, "new", lambda *_args: disk)
    backend = native_backend()

    async def files(_project):
        return [*[file.model_dump() for file in sources().files], {"path": "deleted.txt", "version": 2, "deleted": True}]

    backend.list_workspace_files = files
    backend.resolve_agent_instruction_snapshot = EmptyInstructionsBackend().resolve_agent_instruction_snapshot
    context = AgentContext("project", "conversation", backend,
                           agent_turn_id="turn" if mode == "main" else "",
                           dispatch_generation=1 if mode == "main" else 0,
                           agent_task_attempt_id="background" if mode == "background" else "",
                           execution_attempt_id="stateful" if mode == "stateful" else "",
                           capability_versions={SKILL.capability_id: SKILL.version})
    model = StreamingSequence(responses)
    config = replace(settings(), native_workspace_enabled=True)
    return disk, backend, context, model, config


async def tools_for(backend, context):
    await bind_managed_instructions(context)
    prepared = await AgentToolProvider(backend, [], guardrail_policy=SDKGuardrailPolicy()).prepare(context, [], set())
    prepared.capabilities = {SKILL.capability_id: {
        "capability_id": SKILL.capability_id, "version": SKILL.version, "execution_mode": "inline",
        "status": "available", "skill": {"content_hash": SKILL.content_hash},
    }}
    prepared.selected_ids = {SKILL.capability_id}
    return prepared


async def invoke(mode, agent, runner_input, context, config):
    if mode == "main":
        runtime = OpenAIAgentsRuntime.__new__(OpenAIAgentsRuntime)
        runtime._settings = config
        return await runtime._run_execution_agent(agent, runner_input, context, None, None, TurnObservation("fixture", "fixture"))
    if mode == "background":
        worker = SDKBackgroundTaskWorker.__new__(SDKBackgroundTaskWorker)
        worker._settings = config
        return await worker._run_streamed(agent, runner_input, context, None)
    worker = SDKTaskWorker.__new__(SDKTaskWorker)
    worker._settings = config
    execution = StatefulExecution(context, {})
    agent = agent.clone(instructions=agent.instructions or "Preserve original evidence.")
    return await worker._run_streamed(agent, runner_input, 20, execution=execution)


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_actual_stream_loops_preserve_workspace_across_runner_calls(tmp_path, monkeypatch, mode):
    async def scenario():
        disk, backend, context, model, config = setup(tmp_path, monkeypatch, mode, [[final_item({"title": "first"})], [final_item({"title": "second"})]])
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Native worker", model=model))
            first = await invoke(mode, agent, "First batch", context, config)
            assert first.final_output and context.native_workspace.state.snapshot.reference.version == 1
            second = await invoke(mode, agent, "Next batch", context, config)
            assert second.final_output and context.native_workspace.state.snapshot.reference.version == 2
        assert disk.creates == disk.prepares == 1 and disk.closes == 2 and len(disk.restores) == 1
        assert (disk.root / "docs/notes.txt").read_bytes() == BODIES["docs/notes.txt"]
        assert all({"apply_patch", "exec_command", "view_image"} <= set(names) for names in model.tools)
        assert "native_workspace" not in _serialize_agent_context(context)
        assert context.native_workspace.guard is None
    asyncio.run(scenario())


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
@pytest.mark.parametrize("failure", ["save", "close"])
def test_actual_stream_loops_reject_unconfirmed_workspace_delivery(tmp_path, monkeypatch, mode, failure):
    async def scenario():
        disk, backend, context, model, config = setup(tmp_path, monkeypatch, mode, [[final_item({"title": "done"})]])
        disk.fail_save = "after" if failure == "save" else ""
        disk.fail_close = failure == "close"
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Native worker", model=model))
            with pytest.raises(BackendError, match="durably saved"):
                await invoke(mode, agent, "Produce result", context, config)
        assert len(model.inputs) == 1 and len(disk.saves) == 1
        assert context.native_workspace.guard is None and context.native_workspace.state is None
        assert disk.closes == 0 and context.commit_result is None
    asyncio.run(scenario())


def test_main_commit_waits_for_save_and_close(tmp_path, monkeypatch):
    async def scenario():
        decision = {"decision": {"intent": "chat", "confidence": 1, "reply": "Completed"}}
        disk, backend, context, model, config = setup(tmp_path, monkeypatch, "main", [[tool_item("commit_agent_action", decision, "commit")]])
        commits = []

        async def commit(*args):
            assert disk.status == "closed" and len(disk.saves) == 1
            assert context.native_workspace.guard._task is None
            commits.append(args)
            return {"result": "committed"}

        backend.commit_agent_turn = commit
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Main", model=model, tools=[commit_agent_action], tool_use_behavior=stop_after_commit))
            result = await invoke("main", agent, "Answer", context, config)
        assert result.final_output and context.commit_result == {"result": "committed"}
        assert len(commits) == disk.closes == len(disk.saves) == 1
    asyncio.run(scenario())


def test_skill_handoff_shares_one_native_session(tmp_path, monkeypatch):
    async def scenario():
        key = hashlib.sha256(f"{SKILL.capability_id}@{SKILL.version}".encode()).hexdigest()[:20]
        disk, backend, context, model, config = setup(tmp_path, monkeypatch, "main", [
            [tool_item("handoff_skill_" + key, {}, "handoff")], [final_item({"title": "done"})],
        ])
        prepared = await tools_for(backend, context)

        async def instructions(_context, _key):
            return "Preserve original evidence."

        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Main", instructions="Keep source facts.", model=model))
            agent = attach_skill_handoffs(agent, context, list(prepared.capabilities.values()), prepared.selected_ids, instructions)
            result = await invoke("main", agent, "Diagnose the outline", context, config)
        assert result.last_agent.name == "skill_" + key
        assert disk.creates == disk.closes == disk.prepares == len(disk.saves) == 1
        assert len(model.inputs) == 2
    asyncio.run(scenario())


def test_fresh_owner_uses_authoritative_recovery_and_rejects_stale_state(tmp_path, monkeypatch):
    async def scenario():
        disk, backend, context, model, config = setup(tmp_path, monkeypatch, "main", [[final_item({"title": "one"})], [final_item({"title": "two"})]])
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Main", model=model))
            first = await invoke("main", agent, "First", context, config)
            old = first.to_state().to_json(context_serializer=_serialize_agent_context)
            await invoke("main", agent, "Second", context, config)
            context = replace(context, native_workspace=None, active_tool_calls={})
            prepared.context = context
            disk.lease = None
            restore_native_checkpoint(context, old["context"]["context"])
            with pytest.raises(BackendError):
                await bind_native_agent(context, config, Agent(name="Main", model=model))
        assert disk.creates == disk.prepares == 1 and len(disk.saves) == 2
        assert len(model.inputs) == 2 and disk.status == "closed"
    asyncio.run(scenario())


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_stream_cancellation_closes_once_and_preserves_cancellation(tmp_path, monkeypatch, mode):
    async def scenario():
        disk, backend, context, _, config = setup(tmp_path, monkeypatch, mode, [])
        started = asyncio.Event()
        stopped = asyncio.Event()

        class WaitingModel(StreamingSequence):
            async def stream_response(self, *args, **kwargs):
                started.set()
                try:
                    await asyncio.Event().wait()
                finally:
                    stopped.set()
                if False:
                    yield

        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Waiting", model=WaitingModel([])))
            task = asyncio.create_task(invoke(mode, agent, "Wait", context, config))
            await asyncio.wait_for(started.wait(), 5)
            task.cancel()
            with pytest.raises(asyncio.CancelledError):
                await asyncio.wait_for(task, 5)
        assert stopped.is_set() and disk.status == "closed" and context.commit_result is None
        assert disk.closes == len(disk.saves) == 1 and context.native_workspace.guard is None
    asyncio.run(scenario())


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_native_approval_resumes_through_stream_loop_in_fresh_owner(tmp_path, monkeypatch, mode):
    async def scenario():
        args = {"cmd": "printf sample", "login": False}
        disk, backend, context, model, config = setup(tmp_path, monkeypatch, mode, [
            [tool_item("exec_command", args, "native-command")], [final_item({"title": "done"})],
        ])
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Native worker", model=model))
            try:
                pending = await invoke(mode, agent, "Run command", context, config)
                saved = pending.to_state().to_json(context_serializer=_serialize_agent_context)
            except ExecutionTaskPaused as paused:
                saved = paused.run_state
            assert not disk.commands and disk.closes == 0
            assert saved["context"]["context"]["native_workspace_checkpoint"]["state"] == "ready" and "sandbox" not in saved
            context = replace(context, native_workspace=None, active_tool_calls={})
            prepared.context = context
            disk.lease = None
            agent = await bind_native_agent(context, config, Agent(name="Native worker", model=model))
            state = await RunState.from_json(agent, saved, context_override=context)
            state.approve(state.get_interruptions()[0])
            backend.calls["native-command"]["status"] = "approved"
            result = await invoke(mode, agent, state, context, config)
        assert result.final_output and len(disk.commands) == 1 and disk.closes == 1
        assert disk.creates == disk.prepares == 1 and not disk.restores
    asyncio.run(scenario())


@pytest.mark.parametrize("failure", ["save", "close"])
def test_main_commit_never_calls_backend_when_workspace_is_unconfirmed(tmp_path, monkeypatch, failure):
    async def scenario():
        disk, backend, context, model, config = setup(tmp_path, monkeypatch, "main", [
            [tool_item("commit_agent_action", {"decision": {"intent": "chat", "confidence": 1, "reply": "Done"}}, "commit")],
            [final_item({"title": "failed"})],
        ])
        disk.fail_save = "after" if failure == "save" else ""
        disk.fail_close = failure == "close"

        async def commit(*_args):
            pytest.fail("Unconfirmed workspace reached backend commit")

        backend.commit_agent_turn = commit
        prepared = await tools_for(backend, context)
        async with prepared:
            agent = await bind_native_agent(context, config, Agent(name="Main", model=model, tools=[commit_agent_action], tool_use_behavior=stop_after_commit))
            with pytest.raises(BackendError):
                await invoke("main", agent, "Answer", context, config)
        assert context.commit_result is None and len(disk.saves) == 1 and disk.closes == 0
    asyncio.run(scenario())
