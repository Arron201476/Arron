import asyncio
import json
from types import SimpleNamespace

import pytest
from agents.models.openai_responses import OpenAIResponsesModel
from openai.types.responses import ResponseFunctionToolCall
from openai.types.responses.response_output_item import Program, ProgramOutput

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.background_worker import SDKBackgroundTaskWorker, _BACKGROUND_PROGRAM_PROGRESS
from test_agent_tools import AuditBackend, echo_value
from test_app import settings
from test_background_worker import FakeBackend
from test_programmatic_tools import catalog
from test_stateful_execution import StreamingSequence, final_item


@pytest.mark.parametrize("status", ["completed", "incomplete"])
def test_background_sdk_program_progress_survives_heartbeat_without_raw_output(status):
    class Backend(FakeBackend):
        def __init__(self):
            super().__init__()
            self.audit = AuditBackend(catalog())
            self.heartbeat_seen = asyncio.Event()

        def __getattr__(self, name):
            return getattr(self.audit, name)

        async def update_agent_task_progress(self, claim, **kwargs):
            result = await super().update_agent_task_progress(claim, **kwargs)
            if kwargs.get("lease_seconds") and kwargs["message"] == "正在执行程序化工具调用":
                self.heartbeat_seen.set()
            return result

    class Model(OpenAIResponsesModel):
        def __init__(self, backend):
            super().__init__(model="isolated-background-program", openai_client=SimpleNamespace())
            self.backend = backend
            self.calls = 0
            self.sequence = StreamingSequence([[
                Program(type="program", id="program-item", call_id="program-1", code="PRIVATE_CODE", fingerprint="PRIVATE_FINGERPRINT"),
                ResponseFunctionToolCall(type="function_call", name="echo_value", call_id="read-1",
                                         arguments='{"value":"one"}', caller={"type": "program", "caller_id": "program-1"}),
            ], [
                ProgramOutput(type="program_output", id="program-result", call_id="program-1", result="PRIVATE_RESULT", status=status),
                final_item({"summary": "Done", "result": {}, "artifact_draft": None}),
            ]])

        async def stream_response(self, *args, **kwargs):
            self.calls += 1
            if self.calls == 2:
                await asyncio.wait_for(self.backend.heartbeat_seen.wait(), timeout=2)
            async for event in self.sequence.stream_response(*args, **kwargs):
                yield event

    async def run():
        backend = Backend()
        worker = SDKBackgroundTaskWorker(settings(), backend)
        worker._model = Model(backend)
        worker._tool_provider = AgentToolProvider(backend, [echo_value], execution_model=lambda: worker._model)
        worker._heartbeat_interval_seconds = 0.01
        assert await worker.run_once()
        assert backend.failed is None
        assert backend.completed is not None
        messages = [entry[2] for entry in backend.progress]
        assert messages.count("正在执行程序化工具调用") >= 2
        expected = "程序执行未完成" if status == "incomplete" else "正在整理程序结果"
        assert expected in messages
        assert messages[-1] == "提交任务结果"
        assert "执行 Skill 任务" not in messages[messages.index("正在执行程序化工具调用"):]
        assert "PRIVATE_" not in json.dumps(backend.progress)
        assert backend.audit.begun[0]["program_call_id"] == "program-1"
        assert backend.audit.begun[0]["agent_task_attempt_id"] == "agatm_1"
        assert _BACKGROUND_PROGRAM_PROGRESS.get() is None

    asyncio.run(run())


def test_program_progress_receipt_propagates_pause_and_cleans_context():
    class Backend(FakeBackend):
        async def update_agent_task_progress(self, claim, **kwargs):
            await super().update_agent_task_progress(claim, **kwargs)
            return {"data": {"status": "pausing"}}

    async def run():
        worker = SDKBackgroundTaskWorker(settings(), Backend())

        async def execute(claim, signal):
            callback = _BACKGROUND_PROGRAM_PROGRESS.get()
            assert callback is not None
            await callback({"event": "agent.tool.started", "data": {"tool_name": "programmatic_tool_calling"}})
            assert signal.is_set()
            return "paused-fixture"

        worker._execute_claim = execute
        assert await worker._execute_with_liveness({}) == "paused-fixture"
        assert _BACKGROUND_PROGRAM_PROGRESS.get() is None

    asyncio.run(run())


def test_concurrent_background_progress_is_bound_to_each_claim():
    class Backend(FakeBackend):
        def __init__(self):
            super().__init__()
            self.claim_messages = []

        async def update_agent_task_progress(self, claim, **kwargs):
            self.claim_messages.append((claim["id"], kwargs["message"]))
            return {}

    async def run():
        backend = Backend()
        worker = SDKBackgroundTaskWorker(settings(), backend)
        entered = 0
        both_entered = asyncio.Event()

        async def execute(claim, signal):
            nonlocal entered
            entered += 1
            if entered == 2:
                both_entered.set()
            await asyncio.wait_for(both_entered.wait(), timeout=2)
            callback = _BACKGROUND_PROGRAM_PROGRESS.get()
            await callback({"event": "agent.tool.completed", "data": {"status": claim["status"]}})
            return claim["id"]

        worker._execute_claim = execute
        assert await asyncio.gather(
            worker._execute_with_liveness({"id": "a", "status": "completed"}),
            worker._execute_with_liveness({"id": "b", "status": "incomplete"}),
        ) == ["a", "b"]
        assert sorted(backend.claim_messages) == [("a", "正在整理程序结果"), ("b", "程序执行未完成")]
        assert _BACKGROUND_PROGRAM_PROGRESS.get() is None

    asyncio.run(run())


def test_failed_progress_publication_does_not_leak_callback_to_next_execution():
    class Backend(FakeBackend):
        async def update_agent_task_progress(self, claim, **kwargs):
            raise RuntimeError("isolated progress outage")

    async def run():
        worker = SDKBackgroundTaskWorker(settings(), Backend())

        async def execute(claim, signal):
            callback = _BACKGROUND_PROGRAM_PROGRESS.get()
            await callback({"event": "agent.tool.started", "data": {}})
            raise AssertionError("failed publication was ignored")

        worker._execute_claim = execute
        with pytest.raises(RuntimeError, match="progress outage"):
            await worker._execute_with_liveness({})
        assert _BACKGROUND_PROGRAM_PROGRESS.get() is None

    asyncio.run(run())
