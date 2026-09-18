from __future__ import annotations

import asyncio
from types import TracebackType
from typing import Self

from .backend import BackendError
from .native_workspace_transport import NativeWorkspaceHTTPTransport


class NativeWorkspaceLeaseGuard:
    """Keep one Runner owner's lease live without retrying failed operations.

    The owner must include resource preparation, Runner cleanup and delivery
    validation in this scope. The guard is transient and never part of RunState.
    """

    def __init__(self, transport: NativeWorkspaceHTTPTransport, *, interval_seconds: float = 15,
                 request_timeout_seconds: float = 10):
        if (type(interval_seconds) not in {int, float} or not 0 < interval_seconds <= 15
                or type(request_timeout_seconds) not in {int, float} or not 0 < request_timeout_seconds <= 10):
            raise BackendError("Workspace renewal timing exceeds its bounded lease window")
        self.transport = transport
        self._interval = interval_seconds
        self._timeout = request_timeout_seconds
        self._task: asyncio.Task | None = None
        self._owner: asyncio.Task | None = None
        self._failure: BackendError | None = None
        self._closed = False
        self._cancel_requested = False

    async def __aenter__(self) -> Self:
        if self._closed or self._owner is not None:
            raise BackendError("Workspace lease guard cannot be reentered or reused")
        self._owner = asyncio.current_task()
        if self._owner is None:
            raise BackendError("Workspace renewal requires a live Runner owner")
        self._task = asyncio.create_task(self._renew(), name="native-workspace-lease")
        return self

    async def _renew(self) -> None:
        try:
            while True:
                await asyncio.sleep(self._interval)
                if self.transport.lease is None:
                    continue
                async with asyncio.timeout(self._timeout):
                    await self.transport.renew()
        except asyncio.CancelledError:
            raise
        except Exception:
            # Do not leak transport details or retry an unknown receipt. The
            # Runner owner must stop before it can publish a successful result.
            self._failure = BackendError("Native workspace lease renewal was not confirmed", code="NATIVE_WORKSPACE_LEASE_UNCONFIRMED")
            if self._owner is not None and not self._owner.done():
                self._cancel_requested = self._owner.cancel()

    def require_confirmed(self) -> None:
        if self._failure is not None:
            raise self._failure

    async def pause(self) -> None:
        if self._closed or self._owner is None:
            raise BackendError("Workspace renewal has no active owner")
        if self._task is not None:
            self._task.cancel()
            await asyncio.gather(self._task, return_exceptions=True)
            self._task = None
        self.require_confirmed()

    def resume(self) -> None:
        if self._closed or self._owner is None or self._owner.done():
            raise BackendError("Workspace renewal has no active owner")
        self.require_confirmed()
        if self._task is None:
            self._task = asyncio.create_task(self._renew(), name="native-workspace-lease")

    async def __aexit__(self, exc_type: type[BaseException] | None, exc: BaseException | None,
                        traceback: TracebackType | None) -> None:
        self._closed = True
        task = self._task
        if task is not None:
            task.cancel()
            await asyncio.gather(task, return_exceptions=True)
        external_cancel = self._owner is not None and self._owner.cancelling() > int(self._cancel_requested)
        if self._cancel_requested and self._owner is not None:
            self._owner.uncancel()
            self._cancel_requested = False
        if external_cancel and isinstance(exc, asyncio.CancelledError):
            return
        self.require_confirmed()
