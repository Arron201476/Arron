import asyncio
import sqlite3

import pytest

from content_agent_sidecar.backend import BackendError, backend_activity
from content_agent_sidecar.memory_archive_queue import MemoryArchiveQueue
from content_agent_sidecar.memory_autocapture import prepare_memory_archive, capture_completed_memory
from test_memory_autocapture import context
from test_memory_rollout import completed
from test_memory_rollout_transport import identity


@pytest.mark.parametrize("mode", ["turn", "background", "execution"])
@pytest.mark.parametrize("outcome", ["success", "unknown", "rejected", "capacity"])
def test_production_capture_queues_before_transport_and_only_discards_confirmed_outcomes(mode, outcome):
    async def run():
        ctx = context(mode)
        queue = MemoryArchiveQueue(sqlite3.connect(":memory:"), max_bytes=1 if outcome == "capacity" else 64 << 20)
        ctx.memory_archive_queue = queue
        operations = []
        with backend_activity("project", **identity(mode)):
            await prepare_memory_archive(ctx)
            async def request(operation, payload):
                operations.append(operation)
                pending = queue.pending()
                assert len(pending) == 1
                assert pending[0].payload["rollout"] == ctx.memory_archive_state.frozen.model_dump()
                if outcome == "unknown": raise BackendError("unknown", status_code=503)
                if outcome == "rejected": raise BackendError("revoked", status_code=409)
                assert operation == "archive"
                return {key: payload["rollout"][key] for key in ("activity_key", "segment_id", "project_id", "user_id", "content_hash")}
            ctx.backend.memory_rollout_request.side_effect = request
            result = await completed()
            await capture_completed_memory(ctx, result)
            await capture_completed_memory(ctx, result)
        assert operations == ({"success": ["archive"], "unknown": ["archive", "archive-receipt"], "rejected": ["archive"], "capacity": []}[outcome])
        assert len(queue.pending()) == (1 if outcome == "unknown" else 0)
        assert ctx.memory_archive_state.status == {"success": "archived", "unknown": "unconfirmed", "rejected": "rejected", "capacity": "unconfirmed"}[outcome]
        queue.close()
    asyncio.run(run())


def test_confirmed_archive_with_failed_queue_cleanup_is_not_reported_as_removed():
    async def run():
        ctx = context()
        queue = MemoryArchiveQueue(sqlite3.connect(":memory:"))
        ctx.memory_archive_queue = queue
        await prepare_memory_archive(ctx)
        def failed_cleanup(entry): raise OSError("PRIVATE_DATABASE_PATH")
        queue.acknowledge = failed_cleanup
        await capture_completed_memory(ctx, await completed())
        assert ctx.memory_archive_state.status == "unconfirmed"
        assert ctx.memory_archive_state.queue_entry is not None
        assert len(queue.pending()) == 1
        queue.close()
    asyncio.run(run())
