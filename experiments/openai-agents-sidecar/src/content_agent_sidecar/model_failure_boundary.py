from __future__ import annotations

from collections.abc import Callable
from typing import Any

from agents import RunHooks
from openai import APIConnectionError, APIStatusError

from .tool_outcomes import external_tool_review_requested


MODEL_RECOVERY_REASON = "model_temporary_failure"


class ModelRecoveryRequired(Exception):
    def __init__(self, result: Any):
        super().__init__("Model transport failed at a resumable native boundary")
        self.result = result


class ModelFailureBoundary(RunHooks):
    """Identify a native model-call failure, never an unfinished local tool."""

    def __init__(self, on_model_start: Callable[[], None] | None = None, *, on_external_review: Callable[[], None] | None = None):
        self.model_active = False
        self.active_tools = 0
        self.on_model_start = on_model_start
        self.on_external_review = on_external_review
        self.external_review = False

    async def on_llm_start(self, context: Any, agent: Any, system_prompt: Any, input_items: Any) -> None:
        self.model_active = True
        if self.on_model_start is not None:
            self.on_model_start()

    async def on_llm_end(self, context: Any, agent: Any, response: Any) -> None:
        self.model_active = False

    async def on_tool_start(self, context: Any, agent: Any, tool: Any) -> None:
        self.active_tools += 1

    async def on_tool_end(self, context: Any, agent: Any, tool: Any, result: Any) -> None:
        self.active_tools -= 1
        if external_tool_review_requested(context.context):
            self.external_review = True
            if self.on_external_review is not None:
                self.on_external_review()

    def permits_checkpoint(self, error: BaseException, result: Any) -> bool:
        transient = isinstance(error, APIConnectionError) or (
            isinstance(error, APIStatusError) and (error.status_code in {408, 429} or 500 <= error.status_code <= 599)
        )
        return bool(transient and not self.external_review and self.model_active and self.active_tools == 0
                    and result.is_complete and not result.interruptions and result.final_output is None
                    and type(result.current_turn) is int and type(result.max_turns) is int
                    and 0 < result.current_turn < result.max_turns)
