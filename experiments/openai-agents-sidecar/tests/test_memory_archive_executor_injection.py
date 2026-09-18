import asyncio
from types import SimpleNamespace

from content_agent_sidecar.task_worker import SDKTaskWorker, StatefulExecution
from test_app import settings


def test_stateful_preparation_injects_current_queue_not_checkpoint_object(monkeypatch):
    queue = object()
    backend = object()
    worker = SDKTaskWorker(settings(), backend=backend, archive_queue=queue)
    execution = SimpleNamespace(context=SimpleNamespace(memory_archive_queue=None), resume=None,
        completed_batches=[], phase="generation", repair_origin=None, context_reads=set())
    async def create(claim, actual_backend):
        assert actual_backend is backend
        return execution
    monkeypatch.setattr(StatefulExecution, "create", create)
    result = asyncio.run(worker._prepare_execution({"context_pack": {}}))
    assert result is execution and execution.context.memory_archive_queue is queue
