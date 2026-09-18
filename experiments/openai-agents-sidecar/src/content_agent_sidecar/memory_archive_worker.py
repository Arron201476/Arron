"""Bounded recovery of private archives using current service authorization."""

import asyncio
import logging

from .backend import BackendClient, BackendError
from .memory_archive_recovery import recover_memory_archive


logger = logging.getLogger(__name__)
_REVOKED = frozenset({"AGENT_MEMORY_ARCHIVE_SOURCE_REVOKED", "AGENT_MEMORY_CONFLICT",
                      "AGENT_ACTIVITY_SCOPE_MISMATCH", "WORKSPACE_ACCESS_DENIED"})


class MemoryArchiveWorker:
    @classmethod
    def from_settings(cls, settings, queue):
        if not isinstance(settings.internal_token, str) or not settings.internal_token.strip():
            raise ValueError("Memory archive recovery requires internal service credentials")
        backend = BackendClient(settings.backend_base_url, timeout_seconds=10,
                                internal_token=settings.internal_token, ffmpeg_command=settings.ffmpeg_command)
        return cls(backend, queue)

    def __init__(self, backend, queue, *, poll_seconds=5, request_seconds=12):
        if (type(poll_seconds) not in (int, float) or not .1 <= poll_seconds <= 60
                or type(request_seconds) not in (int, float) or not 1 <= request_seconds <= 30):
            raise ValueError("Invalid archive worker timing")
        self.backend, self.queue = backend, queue
        self.poll_seconds, self.request_seconds = poll_seconds, request_seconds
        self._task = self._active_task = self._shutdown_task = None
        self._closed = False
        self._lock = asyncio.Lock()
        self._cursor = 0

    def start(self):
        if self._closed:
            raise RuntimeError("Stopped archive worker cannot restart")
        if self._task is None:
            self._task = asyncio.create_task(self._run_loop())

    async def stop(self):
        if asyncio.current_task() is self._active_task:
            raise RuntimeError("Archive worker cannot stop its own execution")
        self._closed = True
        if self._shutdown_task is None:
            self._shutdown_task = asyncio.create_task(self._shutdown())
        await asyncio.shield(self._shutdown_task)

    async def _shutdown(self):
        tasks = {task for task in (self._task, self._active_task) if task is not None}
        for task in tasks:
            task.cancel()
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)
        self._task = None

    async def run_once(self):
        async with self._lock:
            if self._closed:
                raise RuntimeError("Stopped archive worker cannot recover sources")
            self._active_task = asyncio.current_task()
            try:
                # The queue is capped at 256. Rotate bounded batches so a pending
                # source cannot indefinitely starve later completed sources.
                pending = self.queue.pending(limit=256, skip_invalid=True)
                if not pending:
                    return 0
                start = self._cursor % len(pending)
                batch = (pending[start:] + pending[:start])[:16]
                self._cursor = (start + len(batch)) % len(pending)
                confirmed = 0
                for entry in batch:
                    if not self.queue.is_pending(entry):
                        continue
                    try:
                        async with asyncio.timeout(self.request_seconds):
                            await recover_memory_archive(self.backend, entry.payload)
                    except BackendError as error:
                        # Service authentication failures and unknown conflicts are
                        # not evidence that the user's original consent was revoked.
                        if error.code in _REVOKED and error.status_code in {403, 409, 410}:
                            self.queue.acknowledge(entry)
                        else:
                            logger.warning("Archive recovery unconfirmed cause_type=%s", type(error).__name__)
                    except Exception as error:
                        logger.warning("Archive recovery unconfirmed cause_type=%s", type(error).__name__)
                    else:
                        self.queue.acknowledge(entry)
                        confirmed += 1
                return confirmed
            finally:
                self._active_task = None

    async def _run_loop(self):
        while True:
            try:
                await self.run_once()
            except asyncio.CancelledError:
                raise
            except Exception as error:
                logger.warning("Archive recovery batch unconfirmed cause_type=%s", type(error).__name__)
            await asyncio.sleep(self.poll_seconds)
