import asyncio

import pytest
from agents import Runner

from content_agent_sidecar.backend import BackendError, backend_activity
from content_agent_sidecar.native_file_session import RuntimeSandboxFileSession
from content_agent_sidecar.native_lease import NativeWorkspaceLeaseGuard
from content_agent_sidecar.native_session import RuntimeSandboxClient
from content_agent_sidecar.native_workspace_transport import NativeWorkspaceHTTPTransport
from test_agent_tools import _context
from test_native_capabilities import CapturingModel, assemble, native_backend
from test_native_manifest import ManifestFixture, sources
from test_native_workspace_transport import KEY, OTHER, SESSION, lease, response, wire
from test_run_state_approval import _text_response


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_native_lease_renewal_http_preserves_owner_headers_and_snapshot_version(mode):
    first = lease()
    renewed = lease()
    activity = {"agent_turn_id": "turn"} if mode == "main" else {
        "task_attempt_id" if mode == "background" else "execution_attempt_id": "attempt", "attempt_token": "fixture-token",
    }
    with wire(response(first), response(renewed)) as (backend, requests):
        async def scenario():
            with backend_activity("project", **activity):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1 if mode == "main" else 0)
                await transport.reserve()
                transport.lease = transport.lease.model_copy(update={"snapshot_version": 3})
                result = await transport.renew()
                assert result.session_id == SESSION and result.snapshot_version == 3 and result.generation == first["generation"]
        asyncio.run(scenario())
        assert len(requests) == 2 and requests[1][1] == f"/internal/v1/native-workspaces/{SESSION}/lease/renew"
        assert requests[1][2]["X-Workspace-Lease-Key"] == KEY and requests[1][3] == b"{}"


@pytest.mark.parametrize("failure", ["session", "generation", "extra"])
def test_renewal_rejects_unbound_or_malformed_receipt_without_retry(failure):
    bad = lease()
    if failure == "session": bad["session_id"] = OTHER
    elif failure == "generation": bad["generation"] += 1
    else: bad["holder_key"] = KEY
    with wire(response(lease()), response(bad)) as (backend, requests):
        async def scenario():
            with backend_activity("project", agent_turn_id="turn"):
                transport = NativeWorkspaceHTTPTransport(backend, KEY, 1)
                before = await transport.reserve()
                with pytest.raises(BackendError):
                    await transport.renew()
                assert transport.lease == before
        asyncio.run(scenario())
        assert len(requests) == 2


class RenewingWorkspace(ManifestFixture):
    def __init__(self, root, failure=None):
        super().__init__(root)
        self.renewals = 0
        self.renewed = asyncio.Event()
        self.failure = failure

    async def renew(self):
        await self.validate()
        self.renewals += 1
        self.renewed.set()
        if self.failure == "lost":
            raise BackendError("PRIVATE_RENEWAL_DETAIL")
        if self.failure == "timeout":
            await asyncio.Event().wait()
        return self.lease


def test_native_guard_renews_while_actual_runner_waits_for_model_then_closes(tmp_path):
    async def scenario():
        disk = RenewingWorkspace(tmp_path)
        class WaitingModel(CapturingModel):
            async def get_response(self, *args, **kwargs):
                await asyncio.wait_for(disk.renewed.wait(), 2)
                return await super().get_response(*args, **kwargs)
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, sources=sources())
        context = _context()
        guard = NativeWorkspaceLeaseGuard(disk, interval_seconds=0.01)
        async with guard:
            agent, config = await assemble(native_backend(), context, client, WaitingModel([_text_response()]))
            result = await Runner.run(agent, "Wait for a model", context=context, run_config=config)
            assert result.final_output
            client.require_confirmed_cleanup()
        assert guard._task.done() and disk.renewals >= 1 and disk.prepares == disk.closes == 1
        assert not asyncio.current_task().cancelling()
        with pytest.raises(BackendError):
            async with guard:
                pass
    asyncio.run(scenario())


@pytest.mark.parametrize("failure", ["lost", "timeout"])
def test_native_guard_aborts_runner_once_and_does_not_publish_success(tmp_path, failure):
    async def scenario():
        disk = RenewingWorkspace(tmp_path, failure)
        class WaitingModel(CapturingModel):
            async def get_response(self, *args, **kwargs):
                await asyncio.Event().wait()
        client = RuntimeSandboxClient(disk, RuntimeSandboxFileSession, sources=sources())
        context = _context()
        guard = NativeWorkspaceLeaseGuard(disk, interval_seconds=0.01, request_timeout_seconds=0.01)
        with pytest.raises(BackendError, match="renewal was not confirmed") as error:
            async with guard:
                agent, config = await assemble(native_backend(), context, client, WaitingModel([]))
                await asyncio.wait_for(Runner.run(agent, "Wait for a model", context=context, run_config=config), 2)
                pytest.fail("unconfirmed lease returned a deliverable result")
        assert "PRIVATE_RENEWAL_DETAIL" not in str(error.value)
        assert disk.renewals == 1 and disk.prepares == 1 and guard._task.done()
        assert not asyncio.current_task().cancelling()
    asyncio.run(scenario())


def test_guard_without_workspace_does_not_reserve_and_preserves_user_cancellation(tmp_path):
    async def scenario():
        disk = RenewingWorkspace(tmp_path)
        guard = NativeWorkspaceLeaseGuard(disk, interval_seconds=0.001)
        entered = asyncio.Event()
        async def run():
            async with guard:
                entered.set()
                await asyncio.Event().wait()
        task = asyncio.create_task(run())
        await entered.wait()
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert not disk.lease and not disk.renewals and guard._task.done()
    asyncio.run(scenario())


def test_guard_preserves_external_cancellation_when_renewal_fails_at_same_time(tmp_path):
    async def scenario():
        disk = RenewingWorkspace(tmp_path)
        await disk.reserve()
        guard = NativeWorkspaceLeaseGuard(disk, interval_seconds=0.001)
        async def fail_with_external_cancel():
            guard._owner.cancel()
            raise BackendError("PRIVATE_RENEWAL_DETAIL")
        disk.renew = fail_with_external_cancel
        async def run():
            async with guard:
                await asyncio.Event().wait()
        task = asyncio.create_task(run())
        with pytest.raises(asyncio.CancelledError):
            await asyncio.wait_for(task, 2)
        assert task.cancelled() and task.cancelling() == 1 and guard._task.done()
        assert not asyncio.current_task().cancelling()
    asyncio.run(scenario())


@pytest.mark.parametrize("seconds", [0, -1, 16, True, "1", float("nan"), float("inf")])
def test_guard_rejects_unbounded_intervals(seconds):
    with pytest.raises(BackendError):
        NativeWorkspaceLeaseGuard(None, interval_seconds=seconds)


def test_pause_and_resume_from_commit_tool_preserves_original_owner(tmp_path):
    async def scenario():
        disk = RenewingWorkspace(tmp_path)
        await disk.reserve()
        guard = NativeWorkspaceLeaseGuard(disk, interval_seconds=0.001)
        original = asyncio.current_task()
        async with guard:
            async def commit_tool():
                await guard.pause()
                assert guard._task is None
                with pytest.raises(BackendError):
                    await guard.__aenter__()
                guard.resume()
                assert guard._owner is original
            await asyncio.create_task(commit_tool())
            await asyncio.wait_for(disk.renewed.wait(), 2)
        assert guard._task.done() and not original.cancelling()
    asyncio.run(scenario())
