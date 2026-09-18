from __future__ import annotations

import asyncio
import json
import pytest
from test_stateful_execution import StreamingSequence, final_item

from content_agent_sidecar.background_worker import (
    BackgroundTaskOutput,
    SDKBackgroundTaskWorker,
)
from content_agent_sidecar.backend import BackendError
import content_agent_sidecar.background_worker as background_worker_module


def test_json_compatibility_agent_keeps_the_complete_output_schema():
    from agents import Agent, AgentOutputSchema

    original = Agent(name="Fixture", instructions="Preserve user facts.", output_type=AgentOutputSchema(BackgroundTaskOutput, strict_json_schema=False))
    compatible = SDKBackgroundTaskWorker._json_agent(original)
    assert compatible.output_type is None
    assert compatible.instructions.startswith("Preserve user facts.")
    schema = json.loads(compatible.instructions.split("OUTPUT_SCHEMA:\n", 1)[1])
    assert schema == BackgroundTaskOutput.model_json_schema()
    assert "payload" in schema["$defs"]["BackgroundArtifactDraft"]["required"]


from instruction_fixtures import EmptyInstructionsBackend


class FakeBackend(EmptyInstructionsBackend):
    def __init__(self) -> None:
        self.claimed = False
        self.progress: list[tuple[int, int, str]] = []
        self.completed: dict[str, object] | None = None
        self.failed: dict[str, object] | None = None

    async def claim_agent_task(self, **_kwargs):  # type: ignore[no-untyped-def]
        if self.claimed:
            return None
        self.claimed = True
        return {
            "task": {
                "agent_task_id": "agt_1",
                "project_id": "project", "conversation_id": "conversation",
                "skill_invocation_id": "invocation", "capability_id": "story_research_digest",
                "capability_version": "1.0.0",
                "input": {"topic": "悬疑"},
                "config": {"depth": "focused"},
            },
            "attempt": {"agent_task_attempt_id": "agatm_1"},
            "attempt_token": "token",
            "skill_name": "story-research-digest",
            "instructions": "区分事实、推断和建议。",
            "request_content": "整理这些材料。",
        }

    async def update_agent_task_progress(
        self, _claim, *, current: int, total: int, message: str, lease_seconds: int = 0
    ) -> dict[str, object]:
        self.progress.append((current, total, message))
        return {}

    async def get_capability(self, capability_id, project_id, version):
        return {"data": {"capability_id": capability_id, "version": version, "status": "available",
                         "skill": {"instructions": "区分事实、推断和建议。", "dependencies": []}}}

    async def complete_agent_task(self, _claim, **kwargs):  # type: ignore[no-untyped-def]
        self.completed = kwargs
        return {}

    async def fail_agent_task(self, _claim, **kwargs):  # type: ignore[no-untyped-def]
        self.failed = kwargs
        return {}


@pytest.mark.parametrize("plain_json", [False, True])
@pytest.mark.parametrize("misplaced", [
    {"summary": "Completed", "content_markdown": "Do not discard this body"},
    {"summary": "Completed", "artifact_draft": {
        "artifact_type": "generic_document", "title": "Draft", "payload": {},
        "content_markdown": "Do not discard this body"}},
])
def test_background_worker_rejects_misplaced_output_without_reporting_success(plain_json, misplaced):
    from test_app import settings

    backend = FakeBackend()
    worker = SDKBackgroundTaskWorker(settings(), backend=backend)
    if plain_json:
        worker._model_name = "gemini-fixture-routerhub"
    worker._model = StreamingSequence([[final_item(misplaced)], [final_item(misplaced)]])
    assert asyncio.run(worker.run_once()) is True
    assert backend.completed is None
    assert backend.failed is not None
    assert backend.failed["error_code"] == "OUTPUT_REPAIR_FAILED"
    assert backend.failed["retryable"] is False


def test_background_output_preserves_open_result_and_payload_fields():
    value = {"summary": "Completed", "result": {"custom_fact": {"source": "original"}},
             "artifact_draft": {"artifact_type": "generic_document", "title": "Draft",
                                "payload": {"content_markdown": "Body", "custom_metadata": [1, 2]}}}
    assert SDKBackgroundTaskWorker._normalize_output(json.dumps(value)) == value


def test_background_worker_executes_frozen_skill_with_agents_sdk(monkeypatch) -> None:
    from test_app import settings

    backend = FakeBackend()
    archive_queue = object()
    worker = SDKBackgroundTaskWorker(settings(), backend=backend, archive_queue=archive_queue)  # type: ignore[arg-type]
    captured_run_config = None

    native_run = background_worker_module.Runner.run_streamed
    worker._model = StreamingSequence([[final_item({"summary": "调研完成", "result": {"facts": ["事实"]}, "artifact_draft": {
        "artifact_type": "generic_document", "title": "创作调研摘要", "payload": {"content": "事实与建议"}}})]])

    def run(_agent, prompt, **kwargs):  # type: ignore[no-untyped-def]
        nonlocal captured_run_config
        captured_run_config = kwargs["run_config"]
        assert "区分事实、推断和建议" not in prompt
        assert '"topic": "悬疑"' in prompt
        assert {"read_skill_resource", "list_skill_resources", "inspect_text_asset", "inspect_current_artifact"}.issubset({tool.name for tool in _agent.tools})
        assert "commit_agent_action" not in {tool.name for tool in _agent.tools}
        assert kwargs["context"].skill_invocation_id == "invocation"
        assert kwargs["context"].memory_archive_queue is archive_queue
        return native_run(_agent, prompt, **kwargs)

    monkeypatch.setattr(background_worker_module.Runner, "run_streamed", run)

    assert asyncio.run(worker.run_once()) is True
    assert backend.progress == [
        (1, 3, "加载 Skill 指令"),
        (2, 3, "提交任务结果"),
    ]
    assert backend.completed == {
        "result": {"summary": "调研完成", "data": {"facts": ["事实"]}},
        "artifact_draft": {
            "artifact_type": "generic_document",
            "title": "创作调研摘要",
            "payload": {"content": "事实与建议"},
        },
        "usage": backend.completed["usage"],
        "trace_ref": "response-1",
    }
    assert backend.completed["usage"]["total_tokens"] == 12
    assert backend.failed is None
    assert captured_run_config.tracing_disabled is True
    assert captured_run_config.trace_include_sensitive_data is False


def test_background_worker_reports_retryable_provider_failure(monkeypatch) -> None:
    from test_app import settings

    backend = FakeBackend()
    worker = SDKBackgroundTaskWorker(settings(), backend=backend)  # type: ignore[arg-type]

    worker._model = StreamingSequence([RuntimeError("temporary upstream failure")])

    assert asyncio.run(worker.run_once()) is True
    assert backend.completed is None
    assert backend.failed is not None
    assert backend.failed["error_code"] == "PROVIDER_TEMPORARY_FAILURE"
    assert backend.failed["retryable"] is True


def test_background_worker_cancels_inflight_model_when_runtime_cancels_task(monkeypatch) -> None:
    from test_app import settings
    backend = FakeBackend()
    worker = SDKBackgroundTaskWorker(settings(), backend=backend)
    worker._heartbeat_interval_seconds = 0.01
    cancelled = []
    started = asyncio.Event()

    class WaitingModel(StreamingSequence):
        async def stream_response(self, *args, **kwargs):
            started.set()
            try:
                await asyncio.sleep(20)
            except asyncio.CancelledError:
                cancelled.append(True)
                raise
            if False:
                yield

    original_progress = backend.update_agent_task_progress

    async def progress(claim, **kwargs):
        if kwargs.get("lease_seconds"):
            await started.wait()
            raise BackendError("AGENT_TASK_CANCELLED")
        return await original_progress(claim, **kwargs)

    worker._model = WaitingModel([])
    monkeypatch.setattr(backend, "update_agent_task_progress", progress)
    async def run():
        assert await worker.run_once() is True
        # Verify cancellation before asyncio.run performs its shutdown cleanup.
        assert cancelled == [True]

    asyncio.run(run())
    assert backend.completed is None
    assert backend.failed["error_code"] == "AGENT_TASK_CANCELLED"
    assert backend.failed["retryable"] is False
