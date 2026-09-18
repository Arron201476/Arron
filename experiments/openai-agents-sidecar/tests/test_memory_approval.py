import asyncio

import pytest
from agents import Runner, RunConfig, function_tool
from agents.tool import CustomTool

from content_agent_sidecar.backend import BackendError, _activity_headers
from content_agent_sidecar.memory_checkpoint import checkpoint_memory_stage
from content_agent_sidecar.memory_execution import run_memory_stage
from content_agent_sidecar.memory_approval import restore_memory_approvals
from test_memory_execution import setup, output
from test_memory_generation_claim import raw_claim
from test_run_state_approval import SequenceModel, _tool_call_response
from test_native_patch_tool import PATCH, response as patch_response


@pytest.mark.parametrize("phase", ["extraction", "consolidation"])
@pytest.mark.parametrize("status", ["approved", "rejected", "pending_approval", "running", "completed"])
def test_private_publication_approval_restores_only_consolidation(raw_claim, phase, status):
    async def run():
        response = _tool_call_response()
        response.output[0].name = "publish_agent_memory"
        backend, _, stage, _, context = setup(raw_claim, SequenceModel([response, output()]))
        writes, receipts = [], []

        @function_tool(needs_approval=True)
        async def publish_agent_memory(value: str) -> str:
            """Fixture publication; the backend is replaced in this test."""
            writes.append(value)
            return "saved"

        stage.agent.tools.append(publish_agent_memory)
        config = RunConfig(tracing_disabled=True)
        paused = await Runner.run(stage.agent, stage.input, context=context, run_config=config)
        state = paused.to_state()

        async def catalog():
            return {"tools": [{"id": "runtime:publish_agent_memory", "enabled": True, "approval": "always"}]}

        async def begin(**payload):
            receipts.append(payload)
            result = {key: payload[key] for key in ("project_id", "conversation_id", "sdk_tool_call_id", "tool_id")}
            result.update(agent_tool_call_id="publication", status=status,
                          approval_status="pending" if status == "pending_approval" else status)
            return result

        backend.get_agent_tool_catalog, backend.begin_agent_tool_call = catalog, begin
        if phase == "extraction" or status in {"running", "completed"}:
            with pytest.raises(BackendError):
                await restore_memory_approvals(state, backend, context, phase=phase)
            assert not writes
            assert len(receipts) == (0 if phase == "extraction" else 1)
            return
        await restore_memory_approvals(state, backend, context, phase=phase)
        result = await Runner.run(stage.agent, state, context=context, run_config=config)
        assert writes == (["approved payload"] if status == "approved" else [])
        assert bool(result.interruptions) == (status == "pending_approval")
        assert receipts[0]["sdk_tool_call_id"] == paused.interruptions[0].call_id
        assert receipts[0]["arguments"] == {"value": "approved payload"}
    asyncio.run(run())

@pytest.mark.parametrize("status", ["approved", "rejected", "pending_approval", "running", "completed", "failed"])
@pytest.mark.parametrize("tool_name", ["exec_command", "write_stdin", "apply_patch"])
def test_memory_resume_reads_durable_decision_before_sdk_execution(raw_claim, status, tool_name):
    asyncio.run(resume(raw_claim, status, tool_name=tool_name))


@pytest.mark.parametrize("field", ["project_id", "conversation_id", "sdk_tool_call_id", "tool_id", "approval_status", "agent_tool_call_id"])
def test_memory_resume_rejects_inconsistent_receipt(raw_claim, field):
    asyncio.run(resume(raw_claim, "approved", wrong=field))


async def resume(raw, status, wrong=None, tool_name="exec_command"):
    response = patch_response() if tool_name == "apply_patch" else _tool_call_response()
    response.output[0].name = tool_name
    model = SequenceModel([response, output()])
    backend, claim, stage, binding, context = setup(raw, model)
    writes = []
    reads = []

    @function_tool(needs_approval=True)
    async def exec_command(value: str) -> str:
        """Execute a test operation after approval."""
        assert reads
        writes.append(value)
        return "written"

    if tool_name == "apply_patch":
        async def invoke(context, raw):
            assert reads
            writes.append(raw)
            return "patched"
        tool = CustomTool(name=tool_name, description="Apply the approved patch", format={"type": "text"},
                          on_invoke_tool=invoke, needs_approval=True)
    else:
        tool = exec_command
        tool.name = tool_name
    stage.agent.tools.append(tool)
    config = RunConfig(tracing_disabled=True)
    paused = await Runner.run(stage.agent, stage.input, context=context, run_config=config)
    checkpoint = checkpoint_memory_stage(paused, stage, binding)
    recovered = claim.model_copy(update={"checkpoint": checkpoint})

    async def catalog():
        assert _activity_headers.get()["X-Agent-Memory-Generation-ID"] == claim.job.generation_id
        return {"tools": [{"id": "runtime:" + tool_name, "enabled": True, "approval": "always"}]}

    async def begin(**payload):
        assert backend.starts == 1
        assert payload["memory_generation_id"] == claim.job.generation_id
        assert payload["memory_generation_attempt"] == claim.job.attempt
        assert payload["attempt_token"] == claim.attempt_token
        assert not payload["agent_turn_id"]
        assert payload["arguments"] == ({"input": PATCH} if tool_name == "apply_patch" else {"value": "approved payload"})
        reads.append(payload)
        receipt = {key: payload[key] for key in ("project_id", "conversation_id", "sdk_tool_call_id", "tool_id")}
        receipt.update(agent_tool_call_id="durable-call", status=status,
                       approval_status={"pending_approval": "pending"}.get(status, status))
        if wrong:
            receipt[wrong] = "" if wrong == "agent_tool_call_id" else "other"
        return receipt

    backend.get_agent_tool_catalog = catalog
    backend.begin_agent_tool_call = begin
    if wrong or status in {"running", "completed", "failed"}:
        with pytest.raises(BackendError, match="cannot be replayed"):
            await run_memory_stage(backend, recovered, "worker", stage, binding, context=context, run_config=config)
        assert writes == [] and len(model.responses) == 1
    else:
        result = await run_memory_stage(backend, recovered, "worker", stage, binding, context=context, run_config=config)
        assert writes == ([PATCH if tool_name == "apply_patch" else "approved payload"] if status == "approved" else [])
        assert bool(result.interruptions) == (status == "pending_approval")
        assert (result.final_output is None) == (status == "pending_approval")
    assert len(reads) == 1 and _activity_headers.get() is None
