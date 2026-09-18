import asyncio
from types import SimpleNamespace

import pytest

from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.memory_execution import _confirm_memory_completion


@pytest.mark.parametrize("after_timeout", [False, True])
@pytest.mark.parametrize("cancelled", [False, True])
def test_completion_confirmation_preserves_cancel_and_rejection(after_timeout, cancelled):
    async def run():
        claim = object()
        calls = []
        error = asyncio.CancelledError() if cancelled else BackendError("invalid completion receipt")

        async def complete(actual_claim, worker_id):
            assert actual_claim is claim and worker_id == "worker"
            calls.append(actual_claim)
            if after_timeout and len(calls) == 1:
                await asyncio.sleep(1)
            raise error

        backend = SimpleNamespace(complete_memory_generation=complete)
        with pytest.raises(type(error)) as caught:
            await _confirm_memory_completion(backend, claim, "worker", .01)
        assert caught.value is error
        assert len(calls) == (2 if after_timeout else 1)

    asyncio.run(run())
