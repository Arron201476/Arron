from __future__ import annotations

from dataclasses import dataclass, field
from copy import copy
import json
from typing import Any

from agents import Agent, FunctionTool, GuardrailFunctionOutput, InputGuardrail, OutputGuardrail
from agents.tool_guardrails import ToolGuardrailFunctionOutput, ToolInputGuardrail, ToolOutputGuardrail
from agents.tool import ToolOutputFileContent, ToolOutputImage


@dataclass(frozen=True)
class SDKGuardrailPolicy:
    max_input_chars: int = 1_000_000
    max_output_chars: int = 1_000_000
    protected_values: tuple[str, ...] = field(default=(), repr=False)

    @classmethod
    def from_settings(cls, settings: Any) -> "SDKGuardrailPolicy":
        return cls(
            max_input_chars=getattr(settings, "guardrail_max_input_chars", 1_000_000),
            max_output_chars=getattr(settings, "guardrail_max_output_chars", 1_000_000),
            protected_values=tuple({value for name in (
                "model_api_key", "content_model_api_key", "video_model_api_key", "mediakit_api_key", "internal_token",
            ) if isinstance(value := getattr(settings, name, None), str) and len(value) >= 8}),
        )

    def _output_problem(self, value: Any) -> str:
        text = _content_text(value)
        if len(text) > self.max_output_chars:
            return "OUTPUT_SIZE_LIMIT"
        if any(secret in text for secret in self.protected_values):
            return "PROTECTED_CREDENTIAL"
        return ""

    def tool_content_problem(self, value: Any) -> str:
        return self._output_problem(value)

    async def check_input(self, _context: Any, _agent: Agent, value: Any) -> GuardrailFunctionOutput:
        reason = "INPUT_SIZE_LIMIT" if len(_content_text(value, skip_attachments=True)) > self.max_input_chars else ""
        return GuardrailFunctionOutput(output_info={"code": reason}, tripwire_triggered=bool(reason))

    async def check_output(self, _context: Any, _agent: Agent, value: Any) -> GuardrailFunctionOutput:
        reason = self._output_problem(value)
        return GuardrailFunctionOutput(output_info={"code": reason}, tripwire_triggered=bool(reason))

    async def check_tool_input(self, data: Any) -> ToolGuardrailFunctionOutput:
        arguments = data.context.tool_arguments
        try:
            value = json.loads(arguments) if isinstance(arguments, str) else arguments
        except (TypeError, ValueError):
            return ToolGuardrailFunctionOutput.reject_content("Tool arguments must be valid JSON.", {"code": "TOOL_ARGUMENTS_INVALID"})
        reason = self._output_problem(value)
        if reason:
            # This also protects commit_agent_action before it persists a reply
            # or artifact; an output-only guardrail would run after that write.
            return ToolGuardrailFunctionOutput.reject_content("Tool call blocked by platform data policy. Remove protected credentials and keep the result within the output limit.", {"code": reason})
        return ToolGuardrailFunctionOutput.allow()

    async def check_tool_output(self, data: Any) -> ToolGuardrailFunctionOutput:
        reason = self._output_problem(data.output)
        if reason:
            return ToolGuardrailFunctionOutput.reject_content("Tool output blocked by platform data policy.", {"code": reason})
        return ToolGuardrailFunctionOutput.allow()

    def protect(self, agent: Agent) -> Agent:
        return agent.clone(
            input_guardrails=[*agent.input_guardrails, InputGuardrail(self.check_input, name="platform_input_size", run_in_parallel=False)],
            output_guardrails=[*agent.output_guardrails, OutputGuardrail(self.check_output, name="platform_output_policy")],
            tools=[self.protect_tool(tool) if isinstance(tool, FunctionTool) else tool for tool in agent.tools],
        )

    def protect_tool(self, tool: FunctionTool) -> FunctionTool:
        protected = copy(tool)
        protected.tool_input_guardrails = [*(tool.tool_input_guardrails or []), ToolInputGuardrail(self.check_tool_input, name="platform_tool_input_policy")]
        protected.tool_output_guardrails = [*(tool.tool_output_guardrails or []), ToolOutputGuardrail(self.check_tool_output, name="platform_tool_output_policy")]
        return protected


def _content_text(value: Any, *, skip_attachments: bool = False) -> str:
    if isinstance(value, str):
        return value
    if isinstance(value, (ToolOutputImage, ToolOutputFileContent)):
        fields = value.model_dump(mode="json", exclude_none=True)
        for key in ("image_url", "file_data"):
            if isinstance(fields.get(key), str) and fields[key].startswith("data:"):
                fields.pop(key)
        return _content_text(fields, skip_attachments=skip_attachments)
    if hasattr(value, "model_dump"):
        return _content_text(value.model_dump(mode="json"), skip_attachments=skip_attachments)
    if isinstance(value, dict):
        # Binary attachments are bounded by the attachment pipeline, not a text
        # length guardrail; do not count a base64 image as conversation prose.
        if skip_attachments and value.get("type") in {"input_image", "input_file", "input_audio", "input_video", "image_url", "video_url"}:
            return ""
        return "\n".join(str(key) + ":" + _content_text(item, skip_attachments=skip_attachments) for key, item in value.items())
    if isinstance(value, (list, tuple)):
        return "\n".join(_content_text(item, skip_attachments=skip_attachments) for item in value)
    return str(value) if value is not None else ""
