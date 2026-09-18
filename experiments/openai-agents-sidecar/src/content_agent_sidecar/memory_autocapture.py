"""Opt-in archival of settled SDK results, independent of business retries."""

import asyncio
from dataclasses import dataclass, field
import logging
from uuid import uuid4

from .backend import BackendError
from .managed_memory import resolve_memory_snapshot
from .memory_rollout import archive_memory_rollout, capture_memory_rollout, resolve_memory_archive_policy, confirm_memory_archive


logger = logging.getLogger(__name__)


@dataclass
class MemoryArchiveState:
    source: object = field(repr=False)
    policy: object = field(default=None, repr=False)
    status: str = "unavailable"
    result: object = field(default=None, repr=False)
    frozen: object = field(default=None, repr=False)
    queue_entry: object = field(default=None, repr=False)


def _acknowledge_archive(context, state):
    if state.queue_entry is not None:
        context.memory_archive_queue.acknowledge(state.queue_entry)
        state.queue_entry = None


def _discard_rejected_archive(context, state, error):
    if isinstance(error, BackendError) and error.status_code in {401, 403, 409, 410}:
        try:
            _acknowledge_archive(context, state)
        except Exception as cleanup_error:
            logger.warning("Rejected memory archive queue cleanup unconfirmed: %s", type(cleanup_error).__name__)
        state.status = "rejected"
        state.frozen = state.result = state.source = state.policy = state.queue_entry = None
        context.memory_archive_snapshot = None
        return True
    return False


async def prepare_memory_archive(context):
    if context is None or getattr(context, "memory_generation_id", ""):
        return
    source = getattr(context, "memory_archive_snapshot", None)
    if getattr(context, "memory_archive_state", None) is not None:
        return
    state = MemoryArchiveState(source)
    context.memory_archive_state = state
    try:
        async with asyncio.timeout(5):
            if source is None:
                source = await resolve_memory_snapshot(context)
                context.memory_archive_snapshot = source
                state.source = source
            state.policy = await resolve_memory_archive_policy(context.backend, source)
        state.status = "ready" if state.policy.archive_enabled else "disabled"
    except Exception as exc:
        logger.warning("Memory archive consent unavailable: %s", type(exc).__name__)
        _discard_rejected_archive(context, state, exc)


async def capture_completed_memory(context, result):
    state = getattr(context, "memory_archive_state", None)
    if state is None or state.status not in {"ready", "archived"}:
        return
    if result is state.result or getattr(result, "final_output", None) is None or getattr(result, "interruptions", None):
        return
    # Main execution output is not a committed user turn until the commit tool
    # has succeeded. Background/stateful results remain SDK source, not receipts.
    if getattr(context, "agent_turn_id", "") and getattr(context, "commit_result", None) is None:
        return
    state.result = result
    state.status = "unconfirmed"
    archive_attempted = False
    try:
        state.frozen = capture_memory_rollout(result, state.source, segment_id="sdk-" + uuid4().hex)
        queue = getattr(context, "memory_archive_queue", None)
        if queue is not None:
            state.queue_entry = queue.put(state.frozen, state.policy)
        async with asyncio.timeout(10):
            archive_attempted = True
            await archive_memory_rollout(context.backend, state.frozen, state.source, state.policy)
        _acknowledge_archive(context, state)
        state.status = "archived"
        state.frozen = None
    except Exception as exc:
        # Never turn a post-run archive failure into another model/tool attempt.
        # Retain exact bytes in this execution for future recovery integration.
        logger.warning("Memory archive unconfirmed: %s", type(exc).__name__)
        if _discard_rejected_archive(context, state, exc):
            return
        unknown = isinstance(exc, TimeoutError) or isinstance(exc, BackendError) and (
            not exc.status_code or exc.status_code == 408 or exc.status_code >= 500)
        if archive_attempted and unknown and state.frozen is not None:
            try:
                async with asyncio.timeout(5):
                    await confirm_memory_archive(context.backend, state.frozen, state.source, state.policy)
                _acknowledge_archive(context, state)
                state.status = "archived"
                state.frozen = None
            except Exception as read_error:
                logger.warning("Memory archive readback unconfirmed: %s", type(read_error).__name__)
                _discard_rejected_archive(context, state, read_error)
