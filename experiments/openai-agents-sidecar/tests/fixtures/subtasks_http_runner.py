"""Run the three production SDK entrypoints against a disposable Go HTTP server."""
import asyncio
from collections import Counter
import json
from pathlib import Path
import sys
from urllib.parse import urlsplit
from uuid import uuid4

import httpx2 as httpx
from agents.items import ModelResponse
from agents.usage import Usage
from openai import APIConnectionError

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from content_agent_sidecar.backend import BackendClient
from content_agent_sidecar.background_worker import SDKBackgroundTaskWorker
from content_agent_sidecar.config import Settings
from content_agent_sidecar.contracts import AgentExecutionRequest
from content_agent_sidecar.runtime import OpenAIAgentsRuntime
from content_agent_sidecar.task_worker import SDKTaskWorker
from test_stateful_execution import StreamingSequence, final_item, tool_item


class SubtaskModel(StreamingSequence):
    def __init__(self, config):
        super().__init__([])
        self.config, self.child_calls = config, Counter()
        self.both_children = asyncio.Event()
        self.pause = None

    async def get_response(self, *args, **kwargs):
        assert self.config.get("phase") != "resume", "completed child was replayed after process rebuild"
        text = json.dumps(kwargs["input"])
        label = next(name for name in ("left", "right") if f"SUBTASK_{name}" in text)
        self.child_calls[label] += 1
        names = {tool.name for tool in kwargs["tools"]}
        assert "inspect_artifact_version" in names and not names.intersection({"delegate_subtask", "apply_workspace_patch", "commit_agent_action", "install_workspace_skill"})
        if self.child_calls[label] == 1:
            if sum(self.child_calls.values()) == 2:
                self.both_children.set()
            await asyncio.wait_for(self.both_children.wait(), 10)
            output = tool_item("inspect_artifact_version", {"artifact_version_id": self.config["artifact_version_id"]}, "same-native-read-id")
        else:
            assert self.child_calls[label] == 2
            assert "SUBTASK_SOURCE_EVIDENCE" in text
            output = final_item({"finding": label + " inspected the source"})
        return ModelResponse(output=[output], usage=Usage(requests=1, input_tokens=10, output_tokens=2, total_tokens=12), response_id=f"{label}-{self.child_calls[label]}")

    async def stream_response(self, *args, **kwargs):
        phase = self.config.get("phase", "complete")
        if not self.inputs and phase != "resume":
            assert "delegate_subtask" in {tool.name for tool in args[3]}
            if phase == "pause":
                await self.pause()
            output = [tool_item("delegate_subtask", {"title": label, "task": "SUBTASK_" + label, "materials": "Inspect the indicated version"}, label) for label in ("left", "right")]
        else:
            assert len(self.inputs) == (0 if phase == "resume" else 1)
            results = [json.loads(item["output"]) for item in args[1] if item.get("type") == "function_call_output" and item.get("call_id") in {"left", "right"}]
            assert len(results) == 2
            assert len({result["read_call_ids"][0] for result in results}) == 2
            for result in results:
                assert result["inspected_artifacts"] == [{"artifact_id": self.config["artifact_id"], "artifact_version_id": self.config["artifact_version_id"]}]
            if phase == "fail":
                output = APIConnectionError(request=httpx.Request("POST", "https://fixture.invalid/responses"))
            elif self.config["mode"] == "conversation":
                output = [tool_item("commit_agent_action", {"decision": {"intent": "chat", "reply": "JOINED_SUBTASKS", "confidence": 1}}, "commit")]
            elif self.config["mode"] == "background_task":
                output = [final_item({"summary": "JOINED_SUBTASKS", "result": {"joined": 2}, "artifact_draft": {"artifact_type": "generic_document", "title": "Combined findings", "payload": {"content": "JOINED_SUBTASKS"}}})]
            else:
                output = [final_item({"title": "Combined findings", "content": "JOINED_SUBTASKS"})]
        self.responses.append(output)
        async for event in super().stream_response(*args, **kwargs):
            yield event


async def main(config):
    url = urlsplit(config["backend_url"])
    if url.scheme != "http" or url.hostname != "127.0.0.1" or not url.port or url.port in {8860, 8880}:
        raise ValueError("Only disposable loopback servers are allowed")
    settings = Settings(backend_base_url=config["backend_url"], model_base_url=config["backend_url"] + "/no-model-provider",
        model_api_key="fixture-not-a-credential", model_name="gpt-subtask-http-fixture", model_timeout_seconds=15,
        model_max_output_tokens=1024, model_max_retries=0, run_timeout_seconds=60,
        tracing_enabled=False, internal_token="subtask-service", task_worker_lease_seconds=120,
        session_db_path=config["session_db"])
    model = SubtaskModel(config)
    events = []
    observed_pause = asyncio.Event()

    class ObservedBackend(BackendClient):
        async def update_agent_task_progress(self, *args, **kwargs):
            response = await super().update_agent_task_progress(*args, **kwargs)
            if response.get("data", response).get("status") == "pausing":
                observed_pause.set()
            return response

        async def heartbeat_execution_attempt(self, *args, **kwargs):
            response = await super().heartbeat_execution_attempt(*args, **kwargs)
            if response.get("data", response).get("pause_requested"):
                observed_pause.set()
            return response

    backend = ObservedBackend(config["backend_url"], internal_token="subtask-service")

    async def request_pause():
        mode = config["mode"]
        path = f'/api/v1/agent-turns/{config["agent_turn_id"]}/pause' if mode == "conversation" else (
            f'/api/v1/agent-tasks/{config["agent_task_id"]}/pause' if mode == "background_task" else f'/api/v1/runs/{config["run_id"]}/pause')
        async with httpx.AsyncClient(base_url=config["backend_url"], headers={"Authorization": "Bearer " + config["owner"]}, timeout=10) as user:
            response = await user.post(path, json={}, headers={"Idempotency-Key": str(uuid4())})
            assert response.status_code == (200 if mode == "conversation" else 202), response.text
        if mode == "conversation":
            assert stream.pause()
        else:
            await asyncio.wait_for(observed_pause.wait(), 25)
        await asyncio.sleep(0)

    model.pause = request_pause
    if config["mode"] == "conversation":
        runtime = OpenAIAgentsRuntime(settings, backend)
        runtime._execution_agent.model = model
        async def no_route(_request, _items): return set()
        runtime._route_skills = no_route
        try:
            stream = runtime.start_execution_stream(AgentExecutionRequest(project_id=config["project_id"], conversation_id=config["conversation_id"],
                agent_turn_id=config["agent_turn_id"], idempotency_key=config["idempotency_key"], request=config["request"],
                run_state=config.get("run_state")))
            events = [event async for event in stream.events()]
            terminal = "agent.turn.paused" if config.get("phase") in {"fail", "pause"} else "agent.turn.committed"
            assert any(event.get("event") == terminal for event in events), [event.get("event") for event in events]
        finally:
            await runtime._model_client.close()
    else:
        worker = SDKBackgroundTaskWorker(settings, backend) if config["mode"] == "background_task" else SDKTaskWorker(settings, backend)
        worker._model = model
        assert await worker.run_once()
    if config.get("phase") == "resume":
        assert len(model.inputs) == 1 and not model.child_calls
    else:
        assert len(model.inputs) == (1 if config.get("phase") == "pause" else 2) and dict(model.child_calls) == {"left": 2, "right": 2}
    print(json.dumps({"mode": config["mode"], "events": events, "parent_requests": len(model.inputs), "child_requests": dict(model.child_calls), "native_sdk": True, "external_model": False}))


if __name__ == "__main__":
    asyncio.run(main(json.load(sys.stdin)))
