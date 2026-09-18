import asyncio
import json
from types import SimpleNamespace

import pytest
from agents import Agent, RunConfig, RunHooks, Runner, RunState
from agents.mcp import MCPServer
from mcp.types import CallToolResult, TextContent, Tool

from content_agent_sidecar.tool_outcomes import external_tool_review_requested, mark_external_tool_review, unknown_mcp_result
from test_stateful_execution import StreamingSequence, final_item, tool_item


@pytest.mark.parametrize("value", ["", " ", "\0", "x" * 257], ids=["empty", "blank", "nul", "long"])
def test_unknown_mcp_outcome_requires_exact_ids(value):
    with pytest.raises(ValueError):
        unknown_mcp_result("call", value)
    with pytest.raises(ValueError):
        unknown_mcp_result(value, "native")


def test_unknown_mcp_outcome_is_an_error_and_does_not_claim_success():
    result = unknown_mcp_result("call", "native")
    assert result.is_error is True
    payload = json.loads(result.content[0].text)
    assert payload == {"schema_version": "agent_tool_outcome.v1", "status": "outcome_unknown", "agent_tool_call_id": "call",
                       "sdk_tool_call_id": "native", "requires_user_reconciliation": True}
    context = SimpleNamespace()
    assert not external_tool_review_requested(context)
    mark_external_tool_review(context, "call")
    mark_external_tool_review(context, "call")
    assert external_tool_review_requested(context) and context.external_tool_outcome_ids == ["call"]


def test_native_mcp_error_output_pause_and_user_fact_preserve_both_issued_operations():
    async def scenario():
        context = SimpleNamespace()
        effects = []
        started = asyncio.Event()

        class Server(MCPServer):
            @property
            def name(self): return "outcome-fixture"
            async def connect(self): pass
            async def cleanup(self): pass
            async def list_prompts(self): return []
            async def get_prompt(self, *_args, **_kwargs): raise AssertionError("No prompts")
            async def list_tools(self, *_args, **_kwargs):
                return [Tool(name="issued_write", description="An already authorized fixture write",
                             input_schema={"type": "object", "properties": {"label": {"type": "string"}}, "required": ["label"], "additionalProperties": False})]
            async def call_tool(self, name, arguments, meta=None):
                label = arguments["label"]
                effects.append(label)
                if len(effects) == 2: started.set()
                await asyncio.wait_for(started.wait(), 2)
                if label == "uncertain":
                    mark_external_tool_review(context, "tcall-uncertain")
                    return unknown_mcp_result("tcall-uncertain", "uncertain")
                return CallToolResult(content=[TextContent(text="Provider confirmed original write")])

        class Boundary(RunHooks):
            async def on_tool_end(self, wrapper, _agent, _tool, _result):
                if external_tool_review_requested(wrapper.context): first.cancel(mode="after_turn")

        server = Server(failure_error_function=None)
        model = StreamingSequence([[tool_item("issued_write", {"label": label}, label) for label in ("known", "uncertain")]])
        agent = Agent(name="MCP outcome fixture", model=model, mcp_servers=[server])
        config = RunConfig(tracing_disabled=True)
        first = Runner.run_streamed(agent, "Perform the two approved writes", context=context, max_turns=6, hooks=Boundary(), run_config=config)
        async for _ in first.stream_events(): pass
        snapshot = json.loads(json.dumps(first.to_state().to_json(context_serializer=lambda _: {})))
        assert snapshot["current_step"]["type"] == "next_step_run_again"
        assert len(model.inputs) == 1 and sorted(effects) == ["known", "uncertain"]
        outputs = [item["raw_item"] for item in snapshot["generated_items"] if item["type"] == "tool_call_output_item"]
        assert {item["call_id"] for item in outputs} == {"known", "uncertain"}
        print("NATIVE_MCP_OUTCOME_ITEMS=" + json.dumps(snapshot["generated_items"]))
        next_model = StreamingSequence([[final_item({"summary": "User reconciliation received"})]])
        restored_agent = agent.clone(model=next_model)
        state = await RunState.from_json(restored_agent, snapshot, context_override=SimpleNamespace(), strict_context=True)
        state.add_input([{"role": "user", "content": "USER_RECONCILIATION: tcall-uncertain was applied; retain original call and do not replay it."}])
        resumed = Runner.run_streamed(restored_agent, state, run_config=config)
        async for _ in resumed.stream_events(): pass
        assert sorted(effects) == ["known", "uncertain"] and len(next_model.inputs) == 1
        assert "USER_RECONCILIATION" in json.dumps(next_model.inputs[0])
        assert resumed.max_turns == 6 and resumed.context_wrapper.usage.requests == 2
    asyncio.run(scenario())
