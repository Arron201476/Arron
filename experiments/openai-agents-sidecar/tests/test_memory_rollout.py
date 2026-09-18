import asyncio
from hashlib import sha256
import json

import pytest
from agents import Agent, RunConfig, Runner, function_tool
from agents.sandbox.memory.interface import RolloutExtractionArtifacts
from pydantic import ValidationError

from content_agent_sidecar.managed_memory import MemorySnapshot
from content_agent_sidecar.memory_rollout import MemoryRollout, capture_memory_rollout
from content_agent_sidecar.native_memory import memory_extraction_from_source
from test_run_state_approval import SequenceModel, _text_response, _tool_call_response


def source(mode="turn"):
    return MemorySnapshot(activity_key=f"{mode}:original", workspace_id="workspace", project_id="project", user_id="owner",
                          version=1, current_version=1, content_hash="a" * 64, read_enabled=True,
                          files={"memory_summary.md": "Private source"})


async def completed():
    return await Runner.run(Agent(name="source", model=SequenceModel([_text_response()])),
                            [{"role": "system", "content": "SYSTEM_PRIVATE"},
                             {"role": "developer", "content": "DEVELOPER_PRIVATE"},
                             {"role": "user", "content": "USER_FACT"}], run_config=RunConfig(tracing_disabled=True))


@pytest.mark.parametrize("mode", ["turn", "background", "execution"])
def test_memory_source_uses_native_rollout_and_original_execution_for_extraction(mode):
    async def run():
        result = await completed()
        binding = source(mode)
        capture = capture_memory_rollout(result, binding, segment_id="segment-1")
        assert "SYSTEM_PRIVATE" not in capture.rollout_jsonl and "DEVELOPER_PRIVATE" not in capture.rollout_jsonl
        assert "USER_FACT" in capture.rollout_jsonl and "continued after the decision" in capture.rollout_jsonl
        assert "USER_FACT" not in repr(capture)
        expected = capture.rollout_jsonl
        result.input.append({"role": "user", "content": "LATE_MUTATION"})
        result.final_output = "CHANGED_AFTER_FREEZE"
        restored = MemoryRollout.model_validate_json(capture.model_dump_json())
        assert restored.verified_input(binding, segment_id="segment-1") == expected
        assert "LATE_MUTATION" not in expected and "CHANGED_AFTER_FREEZE" not in expected
        output = _text_response()
        output.output[0].content[0].text = json.dumps({"rollout_slug": "confirmed", "rollout_summary": "USER_FACT confirmed", "raw_memory": "USER_FACT"})
        stage = memory_extraction_from_source(Agent(name="memory", model=SequenceModel([output])), restored, binding, segment_id="segment-1")
        extracted = await Runner.run(stage.agent, stage.input, run_config=RunConfig(tracing_disabled=True))
        assert isinstance(extracted.final_output, RolloutExtractionArtifacts)
        assert extracted.final_output.raw_memory == "USER_FACT"
        assert "USER_FACT" in stage.input
    asyncio.run(run())


@pytest.mark.parametrize("field,value", [
    ("workspace_id", "foreign"), ("project_id", "foreign"), ("user_id", "foreign"), ("activity_key", "turn:foreign"),
    ("version", 2), ("content_hash", "b" * 64), ("read_enabled", False),
])
def test_memory_source_cannot_be_rebound_by_a_persisted_envelope(field, value):
    async def run():
        binding = source()
        captured = capture_memory_rollout(await completed(), binding, segment_id="segment-1")
        changed = binding.model_copy(update={field: value})
        with pytest.raises(ValueError, match="authorized execution"):
            captured.verified_input(changed, segment_id="segment-1")
    asyncio.run(run())


def test_memory_input_version_stays_pinned_after_its_own_confirmed_publication():
    async def run():
        binding = source()
        captured = capture_memory_rollout(await completed(), binding, segment_id="segment-1")
        assert captured.verified_input(binding.model_copy(update={"current_version": 2}), segment_id="segment-1") == captured.rollout_jsonl
        with pytest.raises(ValueError, match="authorized execution"):
            captured.verified_input(binding, segment_id="segment-2")
    asyncio.run(run())


@pytest.mark.parametrize("change", ["bytes", "multiple", "terminal", "rollout_id", "control"])
def test_restored_memory_source_rejects_corrupt_or_mismatched_content(change):
    async def run():
        captured = capture_memory_rollout(await completed(), source(), segment_id="segment-1")
        raw = captured.model_dump()
        payload = json.loads(raw["rollout_jsonl"])
        if change == "bytes":
            raw["rollout_jsonl"] += " "
        elif change == "multiple":
            raw["rollout_jsonl"] *= 2
            raw["content_hash"] = sha256(raw["rollout_jsonl"].encode()).hexdigest()
        else:
            if change == "terminal": payload["terminal_metadata"]["terminal_state"] = "failed"
            if change == "rollout_id": payload["rollout_id"] = "other"
            if change == "control": payload["unexpected_control"] = "override"
            raw["rollout_jsonl"] = json.dumps(payload) + "\n"
            raw["content_hash"] = sha256(raw["rollout_jsonl"].encode()).hexdigest()
        with pytest.raises(ValidationError, match="immutable source contract"):
            MemoryRollout.model_validate(raw)
    asyncio.run(run())


def test_memory_source_keeps_approval_interruption_and_exact_tool_id_not_success():
    async def run():
        @function_tool(needs_approval=True)
        async def write_value(value: str) -> str:
            raise AssertionError("Unapproved tool must not execute")
        result = await Runner.run(Agent(name="source", model=SequenceModel([_tool_call_response()]), tools=[write_value]), "write", run_config=RunConfig(tracing_disabled=True))
        captured = capture_memory_rollout(result, source(), segment_id="paused")
        payload = json.loads(captured.rollout_jsonl)
        assert payload["terminal_metadata"]["terminal_state"] == "interrupted"
        assert not payload["terminal_metadata"]["has_final_output"]
        assert "sdk-write-1" in captured.rollout_jsonl
        assert payload["interruptions"]
    asyncio.run(run())


def test_conflicting_failure_evidence_is_never_recorded_as_completed_memory():
    async def run():
        result = await completed()
        with pytest.raises(ValueError, match="conflicting success"):
            capture_memory_rollout(result, source(), segment_id="bad", exception=RuntimeError("rejected"))
        result.final_output = None
        captured = capture_memory_rollout(result, source(), segment_id="failed", exception=RuntimeError("SECRET_AUTHORIZATION"))
        payload = json.loads(captured.rollout_jsonl)
        assert payload["terminal_metadata"]["terminal_state"] == "failed"
        assert payload["terminal_metadata"]["exception_type"] == "RuntimeError"
        assert "SECRET_AUTHORIZATION" not in captured.rollout_jsonl
    asyncio.run(run())


def test_running_native_stream_is_not_a_freezable_source():
    async def run():
        class WaitingModel(SequenceModel):
            async def stream_response(self, *args, **kwargs):
                await asyncio.Event().wait()
                if False:
                    yield None
        result = Runner.run_streamed(Agent(name="running", model=WaitingModel([])), "input", run_config=RunConfig(tracing_disabled=True))
        with pytest.raises(ValueError, match="running SDK stream"):
            capture_memory_rollout(result, source(), segment_id="running")
        result.cancel()
        task = result.run_loop_task
        if task is not None:
            await asyncio.gather(task, return_exceptions=True)
    asyncio.run(run())
