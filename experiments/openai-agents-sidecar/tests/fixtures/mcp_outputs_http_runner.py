import asyncio
import json
import sys

from agents import Agent, Runner, RunConfig
from agents.items import ModelResponse
from agents.models.interface import Model
from agents.usage import Usage
from openai.types.responses import ResponseFunctionToolCall

from content_agent_sidecar.agent_tools import AgentToolProvider, _json_safe
from content_agent_sidecar.backend import BackendClient, backend_activity
from content_agent_sidecar.runtime import AgentContext, _serialize_paused_run


class FileModel(Model):
    def __init__(self, kind):
        self.kind = kind
        self.calls = 0

    async def get_response(self, *_args, **_kwargs):
        self.calls += 1
        assert self.calls == 1, "File fixture must not retry the model"
        return ModelResponse(output=[ResponseFunctionToolCall(type="function_call", call_id="sdk-output-" + self.kind,
            name="mcp_story-fixture__lookup_story_fact", arguments=json.dumps({"key": self.kind}))], usage=Usage(requests=1), response_id="response-" + self.kind)

    def stream_response(self, *_args, **_kwargs):
        raise AssertionError("Fixture uses the public non-streamed Runner")


class RetryingUploadClient(BackendClient):
    async def store_agent_tool_output(self, *args):
        first = await super().store_agent_tool_output(*args)
        second = await super().store_agent_tool_output(*args)
        assert first["asset"]["asset_id"] == second["asset"]["asset_id"]
        assert first["asset_snapshot"]["asset_snapshot_id"] == second["asset_snapshot"]["asset_snapshot_id"]
        return second


async def main(config):
    backend = RetryingUploadClient(config["backend_url"], internal_token="mcp-output-service")
    identity = {key: config.get(key, "") for key in ("agent_turn_id", "execution_attempt_id", "attempt_token")}
    identity["task_attempt_id"] = config.get("agent_task_attempt_id", "")
    context = AgentContext(config["project_id"], config["conversation_id"], backend,
        skill_invocation_id=config.get("skill_invocation_id", ""),
        agent_turn_id=identity["agent_turn_id"], agent_task_id=config.get("agent_task_id", ""),
        agent_task_attempt_id=identity["task_attempt_id"], execution_attempt_id=identity["execution_attempt_id"], attempt_token=identity["attempt_token"])
    with backend_activity(context.project_id, **identity):
        prepared = await AgentToolProvider(backend, []).prepare(context, [], set())
        async with prepared:
            for kind in ("inline", "linked", "image", "error"):
                model = FileModel(kind)
                agent = Agent(name="Isolated MCP output", model=model, mcp_servers=prepared.mcp_servers,
                              mcp_config={"include_server_in_tool_names": True}, tool_use_behavior="stop_on_first_tool")
                result = await Runner.run(agent, "Return the fixture file", context=context, max_turns=1,
                                          run_config=RunConfig(tracing_disabled=True))
                assert not result.interruptions and model.calls == 1
                state, _, _ = _serialize_paused_run(result, allow_no_approvals=True)
                assert "isolated-mcp-connection" not in json.dumps(state)
                observed = json.dumps(_json_safe([item.raw_item for item in result.new_items]))
                assert ("platform_tool_outputs" in observed) == (kind != "error")
    print(json.dumps({"native_sdk": True, "model": "deterministic", "requests": 4, "mode": config["mode"]}))


if __name__ == "__main__":
    asyncio.run(main(json.load(sys.stdin)))
