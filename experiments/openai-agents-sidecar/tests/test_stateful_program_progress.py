import asyncio
from types import SimpleNamespace

import pytest
from agents import Agent, ProgrammaticToolCallingTool, RunConfig
from agents.models.openai_responses import OpenAIResponsesModel
from openai.types.responses.response_output_item import Program, ProgramOutput

from content_agent_sidecar.runtime import AgentContext
from content_agent_sidecar.stateful_execution import StatefulExecution
from content_agent_sidecar.task_worker import _STATEFUL_PROGRAM_PROGRESS
from test_stateful_execution import StreamingSequence, final_item
from test_task_worker_liveness import LeaseBackend, lease_worker


class ProgramLeaseBackend(LeaseBackend):
    def __init__(self, *, wrong_receipt=False):
        super().__init__()
        self.program_updates = []
        self.wrong_receipt = wrong_receipt

    async def heartbeat_execution_attempt(self, claim, *, lease_seconds, program_status=""):
        result = await super().heartbeat_execution_attempt(claim, lease_seconds=lease_seconds)
        if program_status:
            self.program_updates.append(program_status)
            result["data"]["program_status"] = "wrong" if self.wrong_receipt else program_status
        return result


@pytest.mark.parametrize("phase", ["completed", "incomplete"])
def test_stateful_streamed_program_publishes_status_through_owned_lease(phase):
    class Model(OpenAIResponsesModel):
        def __init__(self):
            super().__init__(model="isolated-stateful-program", openai_client=SimpleNamespace())
            self.sequence = StreamingSequence([[
                Program(type="program", id="p-item", call_id="p-call", code="private-code", fingerprint="private-fingerprint"),
                ProgramOutput(type="program_output", id="p-output", call_id="p-call", status=phase, result="private-result"),
                final_item({"title": "completed"}),
            ]])

        async def stream_response(self, *args, **kwargs):
            async for event in self.sequence.stream_response(*args, **kwargs):
                yield event

    async def run():
        backend = ProgramLeaseBackend()
        worker = lease_worker(backend)

        async def execute(claim, **kwargs):
            execution = StatefulExecution(AgentContext("p", "c", backend), {})
            result = await worker._run_streamed_once(
                Agent(name="Program progress", model=Model(), tools=[ProgrammaticToolCallingTool()]),
                "Run", 5, execution=execution, max_turns=4, run_config=RunConfig(tracing_disabled=True),
            )
            assert result.final_output
            return {"title": "completed"}, {}, "trace"

        worker._execute_generation = execute
        assert await asyncio.wait_for(worker.run_once(), timeout=8)
        assert backend.program_updates == ["running", phase]
        assert backend.commits == ["receipt"] and not backend.failures
        assert _STATEFUL_PROGRAM_PROGRESS.get() is None

    asyncio.run(run())


def test_wrong_program_progress_receipt_stops_without_result_commit():
    async def run():
        backend = ProgramLeaseBackend(wrong_receipt=True)
        worker = lease_worker(backend)

        async def execute(claim, **kwargs):
            await _STATEFUL_PROGRAM_PROGRESS.get()("running")
            raise AssertionError("invalid progress acknowledgement was ignored")

        worker._execute_generation = execute
        assert await worker.run_once()
        assert backend.program_updates == ["running"]
        assert not backend.commits and not backend.submissions
        assert _STATEFUL_PROGRAM_PROGRESS.get() is None

    asyncio.run(run())
