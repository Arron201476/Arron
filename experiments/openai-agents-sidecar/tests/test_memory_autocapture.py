import asyncio
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest

from content_agent_sidecar.backend import BackendError, backend_activity
from content_agent_sidecar.memory_autocapture import prepare_memory_archive, capture_completed_memory
from content_agent_sidecar.native_files import _go_json_hash
from test_memory_rollout import completed, source
from test_memory_rollout_transport import identity


def context(mode="turn", enabled=True):
    binding = source(mode)
    backend = SimpleNamespace(native_workspace_activity=lambda: {
        "X-Agent-Project-ID": "project",
        {"turn": "X-Agent-Turn-ID", "background": "X-Agent-Task-Attempt-ID", "execution": "X-Agent-Execution-Attempt-ID"}[mode]: "original",
    })
    async def request(operation, payload):
        if operation == "archive-policy":
            return {"project_id": binding.project_id, "user_id": binding.user_id,
                    "archive_enabled": enabled, "generate_enabled": enabled, "revision": 1}
        assert operation == "archive"
        assert payload["consent_revision"] == 1
        return {key: payload["rollout"][key] for key in ("activity_key", "segment_id", "project_id", "user_id", "content_hash")}
    backend.memory_rollout_request = AsyncMock(side_effect=request)
    return SimpleNamespace(backend=backend, memory_archive_snapshot=binding,
                           agent_turn_id="original" if mode == "turn" else "", commit_result={"confirmed": True})


@pytest.mark.parametrize("mode", ["turn", "background", "execution"])
def test_completed_sdk_result_archives_once_under_original_consent(mode):
    async def run():
        ctx = context(mode)
        with backend_activity("project", **identity(mode)):
            await prepare_memory_archive(ctx)
            await prepare_memory_archive(ctx)
            result = await completed()
            await capture_completed_memory(ctx, result)
            await capture_completed_memory(ctx, result)
        assert ctx.memory_archive_state.status == "archived"
        assert ctx.backend.memory_rollout_request.call_count == 2
        assert ctx.memory_archive_state.frozen is None
    asyncio.run(run())


def test_disabled_or_uncommitted_execution_does_not_archive():
    async def run():
        for enabled in (False, True):
            ctx = context(enabled=enabled)
            ctx.commit_result = None
            await prepare_memory_archive(ctx)
            await capture_completed_memory(ctx, await completed())
            assert ctx.backend.memory_rollout_request.call_count == 1
    asyncio.run(run())


def test_failed_archive_retains_exact_bytes_without_business_retry_or_private_log(caplog):
    async def run():
        ctx = context()
        await prepare_memory_archive(ctx)
        ctx.backend.memory_rollout_request.side_effect = BackendError("PRIVATE_SOURCE_AND_TOKEN")
        result = await completed()
        await capture_completed_memory(ctx, result)
        frozen = ctx.memory_archive_state.frozen
        await capture_completed_memory(ctx, result)
        assert frozen is ctx.memory_archive_state.frozen
        assert ctx.memory_archive_state.status == "unconfirmed"
        assert ctx.backend.memory_rollout_request.call_count == 3
        assert "PRIVATE_SOURCE_AND_TOKEN" not in caplog.text
    asyncio.run(run())


@pytest.mark.parametrize("mode", ["turn", "background", "execution"])
def test_non_native_execution_resolves_source_before_authorization(mode):
    async def run():
        ctx = context(mode)
        binding = ctx.memory_archive_snapshot
        binding = binding.model_copy(update={"content_hash": _go_json_hash(binding.files)})
        ctx.memory_archive_snapshot = None
        ctx.project_id = binding.project_id
        ctx.agent_task_attempt_id = "original" if mode == "background" else ""
        ctx.execution_attempt_id = "original" if mode == "execution" else ""
        ctx.backend.resolve_agent_memory_snapshot = AsyncMock(return_value=binding.model_dump())
        await prepare_memory_archive(ctx)
        ctx.backend.resolve_agent_memory_snapshot.assert_awaited_once_with(binding.project_id, binding.activity_key)
        await capture_completed_memory(ctx, await completed())
        assert ctx.memory_archive_state.status == "archived"
    asyncio.run(run())


def test_lost_write_response_requires_transaction_receipt_without_write_retry():
    async def run():
        ctx = context()
        await prepare_memory_archive(ctx)
        stored = None
        operations = []
        async def request(operation, payload):
            nonlocal stored
            operations.append(operation)
            if operation == "archive":
                stored = payload["rollout"]
                raise BackendError("lost response", status_code=503)
            assert operation == "archive-receipt" and payload == {"segment_id": stored["segment_id"], "content_hash": stored["content_hash"], "consent_revision": 1}
            return {**{key: stored[key] for key in ("activity_key", "segment_id", "project_id", "user_id", "content_hash")},
                    "consent_revision": 1, "generate_enabled": True, "generation_id": "amg-confirmed"}
        ctx.backend.memory_rollout_request.side_effect = request
        result = await completed()
        await capture_completed_memory(ctx, result)
        await capture_completed_memory(ctx, result)
        assert ctx.memory_archive_state.status == "archived"
        assert ctx.memory_archive_state.frozen is None
        assert operations == ["archive", "archive-receipt"]
    asyncio.run(run())


@pytest.mark.parametrize("status", [401, 403, 409, 410])
def test_revoked_consent_does_not_attempt_recovery_or_write_again(status):
    async def run():
        ctx = context()
        await prepare_memory_archive(ctx)
        ctx.backend.memory_rollout_request.side_effect = BackendError("revoked", status_code=status)
        result = await completed()
        await capture_completed_memory(ctx, result)
        await capture_completed_memory(ctx, result)
        assert ctx.backend.memory_rollout_request.call_count == 2
        assert ctx.memory_archive_state.status == "rejected"
        assert ctx.memory_archive_state.frozen is None and ctx.memory_archive_state.result is None
        assert ctx.memory_archive_state.source is None and ctx.memory_archive_state.policy is None
        assert ctx.memory_archive_snapshot is None
    asyncio.run(run())


@pytest.mark.parametrize("phase", ["prepare", "readback"])
def test_archive_revocation_discards_private_recovery_references(phase):
    async def run():
        ctx = context()
        if phase == "readback":
            await prepare_memory_archive(ctx)
            ctx.backend.memory_rollout_request.side_effect = [BackendError("unknown", status_code=503), BackendError("revoked", status_code=403)]
            await capture_completed_memory(ctx, await completed())
        else:
            ctx.backend.memory_rollout_request.side_effect = BackendError("revoked", status_code=403)
            await prepare_memory_archive(ctx)
        count = ctx.backend.memory_rollout_request.call_count
        await prepare_memory_archive(ctx)
        await capture_completed_memory(ctx, await completed())
        assert ctx.backend.memory_rollout_request.call_count == count
        state = ctx.memory_archive_state
        assert state.status == "rejected" and ctx.memory_archive_snapshot is None
        assert all(getattr(state, name) is None for name in ("frozen", "source", "policy", "result"))
    asyncio.run(run())


def test_generation_and_unfinished_results_are_not_recursive_sources():
    async def run():
        ctx = context()
        ctx.memory_generation_id = "generation"
        await prepare_memory_archive(ctx)
        ctx.backend.memory_rollout_request.assert_not_called()
        ctx.memory_generation_id = ""
        await prepare_memory_archive(ctx)
        for result in (SimpleNamespace(final_output=None, interruptions=[]),
                       SimpleNamespace(final_output="pending", interruptions=[object()])):
            await capture_completed_memory(ctx, result)
        assert ctx.backend.memory_rollout_request.call_count == 1
    asyncio.run(run())
