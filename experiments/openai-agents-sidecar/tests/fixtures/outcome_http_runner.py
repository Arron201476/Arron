"""Disposable real Go RPC and native SDK recovery; only the model/MCP transport are simulated."""
import asyncio
import json
from pathlib import Path
import sys
from urllib.parse import urlsplit

from agents.mcp import MCPServerStreamableHttp
from mcp.types import CallToolResult, TextContent, Tool

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from content_agent_sidecar.backend import BackendClient
from content_agent_sidecar.background_worker import SDKBackgroundTaskWorker
from content_agent_sidecar.config import Settings
from content_agent_sidecar.contracts import AgentExecutionRequest
from content_agent_sidecar.runtime import OpenAIAgentsRuntime
from content_agent_sidecar.task_worker import SDKTaskWorker
from test_stateful_execution import StreamingSequence, final_item, tool_item


async def main(config):
    url = urlsplit(config["backend_url"])
    if url.scheme != "http" or url.hostname != "127.0.0.1" or not url.port or url.port in {8860, 8880}:
        raise ValueError("Only disposable loopback servers are allowed")
    effects_path = Path(config["effects_file"]).resolve()
    if effects_path.parent != Path(config["session_db"]).resolve().parent:
        raise ValueError("Effects must stay in the isolated test directory")
    effects = json.loads(effects_path.read_text(encoding="utf-8")) if effects_path.exists() else []
    barrier = asyncio.Event()

    async def no_transport(self): pass

    async def list_tools(self, *_args, **_kwargs):
        return [Tool(name="write", description="A fixture external write", input_schema={"type": "object", "properties": {"value": {"type": "string"}},
                     "required": ["value"], "additionalProperties": False})]

    async def write(self, name, arguments, meta=None):
        assert config["phase"] == "issued", "The original operation was replayed"
        effects.append(arguments["value"])
        effects_path.write_text(json.dumps(effects), encoding="utf-8")
        if len(effects) == 2: barrier.set()
        await asyncio.wait_for(barrier.wait(), 10)
        if arguments["value"] == "uncertain": raise OSError("Fixture lost provider response after issuing write")
        return CallToolResult(content=[TextContent(text="Provider confirmed known write")])

    MCPServerStreamableHttp.connect = no_transport
    MCPServerStreamableHttp.cleanup = no_transport
    MCPServerStreamableHttp.list_tools = list_tools
    MCPServerStreamableHttp.call_tool = write

    class Model(StreamingSequence):
        async def stream_response(self, *args, **kwargs):
            assert not self.inputs and config["phase"] != "issued", "Unexpected model request"
            if config["phase"] == "approve":
                name = next(tool.name for tool in args[3] if tool.name == "write" or tool.name.endswith("_write"))
                output = [tool_item(name, {"value": value}, value) for value in ("known", "uncertain")]
            else:
                assert "agent_tool_reconciliation.v1" in json.dumps(args[1])
                assert "CHECKED_REMOTE_ORIGINAL" in json.dumps(args[1])
                original_outputs = [item for item in args[1] if item.get("type") == "function_call_output" and item.get("call_id") in {"known", "uncertain"}]
                assert len(original_outputs) == 2
                if config["mode"] == "conversation":
                    output = [tool_item("commit_agent_action", {"decision": {"intent": "chat", "reply": "CHECKED_AND_CONTINUED", "confidence": 1}}, "commit")]
                elif config["mode"] == "background_task":
                    output = [final_item({"summary": "CHECKED_AND_CONTINUED", "result": {"checked": True}, "artifact_draft": None})]
                else:
                    output = [final_item({"title": "Checked result", "content": "CHECKED_AND_CONTINUED"})]
            self.responses.append(output)
            async for event in super().stream_response(*args, **kwargs): yield event

    settings = Settings(backend_base_url=config["backend_url"], model_base_url=config["backend_url"]+"/no-model-provider",
                        model_api_key="fixture-not-a-credential", model_name="gpt-outcome-http-fixture", model_timeout_seconds=15,
                        model_max_output_tokens=1024, model_max_retries=0, run_timeout_seconds=60, tracing_enabled=False,
                        internal_token="outcome-service", task_worker_lease_seconds=120, session_db_path=config["session_db"])
    model, events = Model([]), []
    if config["mode"] == "conversation":
        runtime = OpenAIAgentsRuntime(settings, BackendClient(config["backend_url"], internal_token="outcome-service"))
        runtime._execution_agent.model = model
        async def no_route(_request, _items): return set()
        runtime._route_skills = no_route
        try:
            stream = runtime.start_execution_stream(AgentExecutionRequest(project_id=config["project_id"], conversation_id=config["conversation_id"],
                agent_turn_id=config["agent_turn_id"], idempotency_key=config["idempotency_key"], request=config["request"],
                run_state=config.get("run_state"), approval_decisions=config.get("approval_decisions") or [], additional_inputs=config.get("additional_inputs") or []))
            events = [event async for event in stream.events()]
            expected = {"approve":"agent.turn.waiting_approval", "issued":"agent.turn.paused", "resume":"agent.turn.committed"}[config["phase"]]
            assert events[-1]["event"] == expected, events
        finally:
            await runtime._model_client.close()
    else:
        worker = SDKBackgroundTaskWorker(settings) if config["mode"] == "background_task" else SDKTaskWorker(settings)
        worker._model = model
        assert await worker.run_once()
    assert len(model.inputs) == (0 if config["phase"] == "issued" else 1)
    assert sorted(effects) == ([] if config["phase"] == "approve" else ["known", "uncertain"])
    print(json.dumps({"events": events, "model_requests":len(model.inputs), "effects":effects}))


if __name__ == "__main__":
    asyncio.run(main(json.load(sys.stdin)))
