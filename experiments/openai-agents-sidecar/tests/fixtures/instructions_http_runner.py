import asyncio
import json
import sys
from uuid import uuid4

import httpx2 as httpx
from agents import Agent, Runner, RunConfig, RunState
from agents.items import ModelResponse
from agents.models.interface import Model
from agents.usage import Usage
from openai.types.responses import ResponseFunctionToolCall, ResponseOutputMessage, ResponseOutputText

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.backend import BackendClient, backend_activity
from content_agent_sidecar.contracts import AgentToolApprovalDecision
from content_agent_sidecar.instruction_tools import get_saved_instructions, update_saved_instructions
from content_agent_sidecar.managed_instructions import bind_managed_instructions, with_managed_instructions
from content_agent_sidecar.runtime import AgentContext, _serialize_paused_run, _apply_approval_decisions


class RuleModel(Model):
    def __init__(self): self.calls = 0

    async def get_response(self, system_instructions, *_args, **_kwargs):
        assert "PINNED_RULE_V1" in system_instructions
        assert "AUTHOR_RULE_V2" not in system_instructions
        self.calls += 1
        if self.calls in (1, 3):
            item = ResponseFunctionToolCall(type="function_call", call_id=f"read-{self.calls}", name="get_saved_instructions", arguments="{}")
        elif self.calls == 2:
            item = ResponseFunctionToolCall(type="function_call", call_id="author-write", name="update_saved_instructions",
                arguments=json.dumps({"scope": "user", "expected_version": 1, "content": "AUTHOR_RULE_V2", "enabled": True}))
        else:
            assert self.calls == 4
            item = ResponseOutputMessage(id="done", type="message", role="assistant", status="completed",
                content=[ResponseOutputText(type="output_text", text="Done", annotations=[])])
        return ModelResponse(output=[item], usage=Usage(requests=1), response_id=f"rule-response-{self.calls}")

    def stream_response(self, *_args, **_kwargs): raise AssertionError("Non-streamed SDK fixture")


async def main(config):
    backend = BackendClient(config["backend_url"], internal_token="instruction-service")
    identity = {key: config.get(key, "") for key in ("agent_turn_id", "execution_attempt_id", "attempt_token")}
    identity["task_attempt_id"] = config.get("agent_task_attempt_id", "")
    def context():
        return AgentContext(config["project_id"], config["conversation_id"], backend,
            agent_turn_id=identity["agent_turn_id"], agent_task_attempt_id=identity["task_attempt_id"],
            execution_attempt_id=identity["execution_attempt_id"], attempt_token=identity["attempt_token"],
            agent_task_id=config.get("agent_task_id", ""), skill_invocation_id=config.get("skill_invocation_id", ""))
    model = RuleModel()
    provider = AgentToolProvider(backend, [get_saved_instructions, update_saved_instructions])
    with backend_activity(config["project_id"], **identity):
        first_context = context()
        await bind_managed_instructions(first_context)
        prepared = await provider.prepare(first_context, [], set())
        async with prepared:
            agent = Agent(name="Rule author", model=model, instructions=with_managed_instructions("Platform", first_context), tools=prepared.tools)
            result = await Runner.run(agent, "Save this explicit preference", context=first_context, run_config=RunConfig(tracing_disabled=True))
            state, _, pending = _serialize_paused_run(result)
            assert pending == ["author-write"] and model.calls == 2
        async with httpx.AsyncClient(base_url=config["backend_url"], trust_env=False, timeout=20) as client:
            auth = {"Authorization": "Bearer "+config["user_token"]}
            calls = (await client.get(f'/api/v1/projects/{config["project_id"]}/agent-tool-calls', headers=auth)).json()["data"]["items"]
            call = next(item for item in calls if item["sdk_tool_call_id"] == "author-write")
            assert "AUTHOR_RULE_V2" not in json.dumps(calls)
            proposal_path = f'/api/v1/agent-tool-calls/{call["agent_tool_call_id"]}/instruction-proposal'
            denied = await client.get(proposal_path, headers={"Authorization": "Bearer "+config["other_token"]})
            assert denied.status_code == 403 and "AUTHOR_RULE_V2" not in denied.text
            proposal = await client.get(proposal_path, headers=auth)
            assert proposal.status_code == 200
            assert proposal.json()["data"]["arguments"]["content"] == "AUTHOR_RULE_V2"
            assert proposal.json()["data"]["current"]["content"] == "PINNED_RULE_V1"
            approval = call["approval"]
            approval_path = f'/api/v1/agent-tool-approvals/{approval["agent_tool_approval_id"]}/resolutions'
            body = {"expected_version": approval["version"], "subject_snapshot_hash": approval["subject_snapshot_hash"], "action": "approve"}
            forbidden = await client.post(approval_path, json=body, headers={"Authorization": "Bearer "+config["other_token"], "Idempotency-Key": str(uuid4())})
            assert forbidden.status_code == 403
            authorized = await client.post(approval_path, json=body, headers={**auth, "Idempotency-Key": str(uuid4())})
            assert authorized.status_code == 200, authorized.text
        resumed_context = context()
        await bind_managed_instructions(resumed_context, state)
        prepared = await provider.prepare(resumed_context, [], set())
        async with prepared:
            agent = Agent(name="Rule author", model=model, instructions=with_managed_instructions("Platform", resumed_context), tools=prepared.tools)
            restored = await RunState.from_json(agent, json.loads(json.dumps(state)), context_override=resumed_context, strict_context=True)
            _apply_approval_decisions(restored, [AgentToolApprovalDecision(sdk_tool_call_id="author-write", action="approve")])
            result = await Runner.run(agent, restored, context=resumed_context, run_config=RunConfig(tracing_disabled=True))
            assert not result.interruptions and model.calls == 4
            outputs = json.dumps([item.raw_item for item in result.new_items if item.type == "tool_call_output_item"])
            assert "AUTHOR_RULE_V2" in outputs
            assert first_context.managed_instruction_hash == resumed_context.managed_instruction_hash
    print(json.dumps({"native_sdk": True, "model": "deterministic", "requests": model.calls, "mode": config["mode"], "author_approval": True}))


if __name__ == "__main__": asyncio.run(main(json.load(sys.stdin)))
