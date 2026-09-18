import asyncio
import base64
import json
from types import SimpleNamespace

import pytest
from agents import Agent, Runner, RunConfig
from agents.items import ModelResponse
from agents.models.openai_responses import OpenAIResponsesModel
from agents.usage import Usage
from openai.types.responses import ResponseComputerToolCall

from content_agent_sidecar.native_computer import ComputerSessionBinding, ManagedComputer, managed_computer_tool
from test_computer_browser_executor import browser, provisioning, startup_frame
from test_native_computer import PNG
from test_run_state_approval import _text_response


@pytest.mark.parametrize("mode", ["conversation", "background_task", "stateful_workflow"])
@pytest.mark.parametrize("batched", [False, True])
def test_sdk_actions_cross_adapter_and_frame_service_with_owned_identity(mode, batched):
    class Model(OpenAIResponsesModel):
        def __init__(self):
            super().__init__(model="isolated-computer-service", openai_client=SimpleNamespace())
            self.inputs = []

        async def get_response(self, *args, **kwargs):
            self.inputs.append(kwargs.get("input"))
            if len(self.inputs) > 1:
                return _text_response()
            click = {"type": "click", "x": 0, "y": 0, "button": "left"}
            actions = {"actions": [click, {"type": "keypress", "keys": ["CTRL", "a"]}]} if batched else {"action": click}
            return ModelResponse(output=[ResponseComputerToolCall(
                id="computer-item", call_id="computer-call", type="computer_call", status="completed",
                pending_safety_checks=[{"id": "check-1", "code": "sensitive_domain", "message": "fixture"}], **actions,
            )], usage=Usage(requests=1), response_id="computer-response")

    async def run():
        engine, owned, context, target = provisioning()
        target.viewport_size = {"width": 1, "height": 1}
        target.screenshot.return_value = base64.b64decode(PNG)
        requests, responses = asyncio.Queue(), asyncio.Queue()
        service = asyncio.create_task(browser.serve_browser(requests.get, responses.put, engine))
        owner = SimpleNamespace(project_id="project", conversation_id="conversation")
        key = {"conversation": "agent_turn_id", "background_task": "agent_task_attempt_id", "stateful_workflow": "execution_attempt_id"}[mode]
        setattr(owner, key, "owned-execution")
        if mode != "conversation":
            owner.attempt_token = "isolated-owner-token"
        authorized = []

        class Bridge:
            sequence = 0
            closed = False

            async def execute(self, binding, sequence, action):
                # This fixture stands in for Runtime authorization, not its implementation.
                assert authorized == ["computer-call"]
                assert binding == ComputerSessionBinding("browser-1", 1, 1, 1)
                assert sequence == self.sequence + 1
                self.sequence = sequence
                await requests.put((json.dumps({"session_id": binding.session_id, "generation": binding.generation,
                                                "sequence": sequence, "action": action}) + "\n").encode())
                result = await asyncio.wait_for(responses.get(), timeout=2)
                assert result["status"] == "completed"
                return {**result, "authorized": True}

            async def close(self, binding):
                if self.closed:
                    return
                self.closed = True
                await requests.put((json.dumps({"session_id": binding.session_id, "generation": binding.generation,
                                                "sequence": self.sequence + 1, "action": {"type": "close"}}) + "\n").encode())
                receipt = await asyncio.wait_for(responses.get(), timeout=2)
                assert receipt["status"] == "closed"
                assert await asyncio.wait_for(service, timeout=2)

        bridge = Bridge()

        async def open_session(value):
            assert value is owner
            await requests.put(startup_frame(width=1, height=1))
            ready = await asyncio.wait_for(responses.get(), timeout=2)
            assert ready == {"session_id": "browser-1", "generation": 1, "sequence": 0, "status": "ready", "width": 1, "height": 1}
            return ManagedComputer(ComputerSessionBinding("browser-1", 1, 1, 1), bridge)

        async def authorize(data):
            assert data.ctx_wrapper.context is owner
            authorized.append(data.tool_call.call_id)
            return True

        model = Model()
        try:
            result = await asyncio.wait_for(Runner.run(Agent(name="Computer service fixture", model=model,
                tools=[managed_computer_tool(open_session, authorize)]), "Perform action", context=owner,
                run_config=RunConfig(tracing_disabled=True)), timeout=8)
            assert result.final_output
            target.mouse.click.assert_awaited_once_with(0, 0, button="left")
            target.screenshot.assert_awaited_once()
            if batched:
                assert [item.args[0] for item in target.keyboard.down.await_args_list] == ["Control", "a"]
            assert bridge.sequence == (3 if batched else 2)
            assert bridge.closed and service.done()
            owned.close.assert_awaited_once()
            context.close.assert_awaited_once()
            assert "computer_call_output" in json.dumps(model.inputs[-1])
        finally:
            if not service.done():
                service.cancel()
            await asyncio.gather(service, return_exceptions=True)

    asyncio.run(run())
