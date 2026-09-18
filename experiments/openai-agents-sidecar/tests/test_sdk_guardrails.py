from __future__ import annotations

import asyncio
import json
from typing import Any
from types import SimpleNamespace

import pytest
from agents import Agent, InputGuardrailTripwireTriggered, OutputGuardrailTripwireTriggered, RunConfig, RunContextWrapper, Runner, function_tool
from agents.items import ModelResponse
from agents.usage import Usage
from openai.types.responses import ResponseFunctionToolCall, ResponseOutputMessage

from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.runtime import stop_after_commit
from test_agent_tools import StaticModel


def message(text: str) -> ModelResponse:
    return ModelResponse(output=[ResponseOutputMessage(id="msg_guard", role="assistant", type="message", status="completed", content=[{"type": "output_text", "text": text, "annotations": []}])], usage=Usage(), response_id="resp_guard")


def test_sdk_input_guardrail_runs_before_model() -> None:
    class UncalledModel(StaticModel):
        async def get_response(self, *args: Any, **kwargs: Any) -> ModelResponse:
            raise AssertionError("model must not run")

    agent = SDKGuardrailPolicy(max_input_chars=10).protect(Agent(name="Input", model=UncalledModel(message("unused"))))
    with pytest.raises(InputGuardrailTripwireTriggered):
        asyncio.run(Runner.run(agent, "x" * 11, run_config=RunConfig(tracing_disabled=True)))


def test_sdk_output_guardrail_blocks_configured_credentials() -> None:
    secret = "private-credential-value"
    agent = SDKGuardrailPolicy(protected_values=(secret,)).protect(Agent(name="Output", model=StaticModel(message(secret))))
    with pytest.raises(OutputGuardrailTripwireTriggered):
        asyncio.run(Runner.run(agent, "Answer", run_config=RunConfig(tracing_disabled=True)))


def test_sdk_tool_guardrail_prevents_terminal_write_and_allows_repair() -> None:
    committed = []

    @function_tool
    async def commit_agent_action(ctx: RunContextWrapper[Any], payload: str) -> str:
        """Persist a terminal reply."""
        committed.append(payload)
        ctx.context.commit_result = "committed"
        return "committed"

    class RepairModel(StaticModel):
        calls = 0

        async def get_response(self, *args: Any, **kwargs: Any) -> ModelResponse:
            self.calls += 1
            value = "private-credential-value" if self.calls == 1 else "safe response"
            return ModelResponse(output=[ResponseFunctionToolCall(id=f"fc_{self.calls}", type="function_call", call_id=f"sdk_{self.calls}", name="commit_agent_action", arguments=json.dumps({"payload": value}))], usage=Usage(), response_id=f"resp_{self.calls}")

    agent = SDKGuardrailPolicy(protected_values=("private-credential-value",)).protect(Agent(name="Commit", model=RepairModel(message("unused")), tools=[commit_agent_action], tool_use_behavior=stop_after_commit))
    result = asyncio.run(Runner.run(agent, "Answer", context=SimpleNamespace(commit_result=None), max_turns=3, run_config=RunConfig(tracing_disabled=True)))
    assert committed == ["safe response"]
    assert result.final_output == "committed"


def test_output_shape_cannot_bypass_guardrail_with_attachment_type_or_secret_key() -> None:
    policy = SDKGuardrailPolicy(protected_values=("private-credential-value",))
    assert policy._output_problem({"type": "input_image", "image_url": "private-credential-value"}) == "PROTECTED_CREDENTIAL"
    assert policy._output_problem({"private-credential-value": "hidden in a key"}) == "PROTECTED_CREDENTIAL"
    assert asyncio.run(policy.check_input(None, None, [{"type": "input_image", "image_url": "x" * 2_000_000}])).tripwire_triggered is False
