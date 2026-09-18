import asyncio
from hashlib import sha256

import pytest
from agents import Agent, Runner, RunConfig

from content_agent_sidecar.background_worker import SDKBackgroundTaskWorker
from content_agent_sidecar.main_inputs import stage_main_inputs
from content_agent_sidecar.runtime import AgentContext
from content_agent_sidecar.stateful_inputs import StatefulInputs
from test_stateful_execution import StreamingSequence, final_item


def stage(mode, sequences):
    inputs = [{"input_id": f"input-{index}", "agent_turn_id": "t", "agent_task_id": "b", "attempt_id": "e", "sequence": sequence,
               "content": f"Revision {index}", "content_hash": sha256(f"Revision {index}".encode()).hexdigest(), "status": "received"} for index, sequence in enumerate(sequences)]
    context = AgentContext("p", "c", None, agent_turn_id="t" if mode == "conversation" else "", agent_task_id="b" if mode == "background" else "",
                           main_inputs=inputs if mode == "conversation" else [], background_inputs=inputs if mode == "background" else [])
    if mode == "conversation": return stage_main_inputs(context, "Original")
    if mode == "background": return SDKBackgroundTaskWorker._stage_additional_inputs("Original", context)
    ledger = StatefulInputs.from_claim("e", inputs)
    ledger.restore(None)
    return ledger.stage("Original")


@pytest.mark.parametrize("mode", ["conversation", "background", "stateful"])
def test_native_runner_accepts_immutable_sequence_gaps_after_unclaimed_revisions(mode):
    async def run():
        model = StreamingSequence([[final_item({"title": "Done"})]])
        result = Runner.run_streamed(Agent(name="Revision", model=model), stage(mode, [3, 5]), run_config=RunConfig(tracing_disabled=True))
        async for _ in result.stream_events():
            pass
        assert result.final_output
        texts = [item["content"] for item in model.inputs[0] if item.get("role") == "user"]
        assert texts == ["Original", "Revision 0", "Revision 1"]
    asyncio.run(run())


@pytest.mark.parametrize("mode", ["conversation", "background", "stateful"])
@pytest.mark.parametrize("sequences", [[0], [True], [129], [3, 3], [5, 3]])
def test_revisions_still_reject_invalid_sequence_order(mode, sequences):
    with pytest.raises(Exception, match="identity or order"):
        stage(mode, sequences)
