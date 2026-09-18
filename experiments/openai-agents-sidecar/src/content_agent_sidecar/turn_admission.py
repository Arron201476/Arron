"""In-process exclusion across Runtime instances, not a durable execution lease."""

import asyncio
from threading import Lock

_active = set()
_lock = Lock()


class TurnAlreadyRunningError(ValueError):
    pass


def reserve_turn(request):
    asyncio.get_running_loop()
    identity = (request.project_id, request.conversation_id, request.agent_turn_id or request.idempotency_key)
    with _lock:
        if identity in _active:
            raise TurnAlreadyRunningError("Agent turn is already running")
        _active.add(identity)
    released = False

    def release():
        nonlocal released
        with _lock:
            if not released:
                _active.remove(identity)
                released = True

    return release
