from __future__ import annotations

from typing import Any

from agents import AsyncOpenAI, OpenAIChatCompletionsModel


_TOOL_CALL_TYPES = frozenset({"function_call", "file_search_call"})
_ROUTERHUB_GEMINI_TOOL_MESSAGE = "Calling the required tool."


def is_routerhub_gemini_model(model: str) -> bool:
    normalized = model.lower()
    return "gemini" in normalized and "routerhub" in normalized


def _assistant_message_with_content(item: dict[str, Any], fallback_id: str) -> dict[str, Any]:
    normalized = dict(item)
    content = normalized.get("content")
    if isinstance(content, str):
        parts: list[dict[str, Any]] = []
        if content:
            parts.append({"type": "output_text", "text": content, "annotations": []})
    elif isinstance(content, list):
        parts = list(content)
    else:
        parts = []

    has_text = any(
        isinstance(part, dict)
        and part.get("type") == "output_text"
        and isinstance(part.get("text"), str)
        and bool(part["text"])
        for part in parts
    )
    if not has_text:
        parts.append(
            {
                "type": "output_text",
                "text": _ROUTERHUB_GEMINI_TOOL_MESSAGE,
                "annotations": [],
            }
        )

    normalized.update(
        {
            "id": str(normalized.get("id") or fallback_id),
            "type": "message",
            "role": "assistant",
            "status": str(normalized.get("status") or "completed"),
            "content": parts,
        }
    )
    return normalized


def normalize_routerhub_gemini_tool_input(input_items: Any) -> Any:
    """Keep tool-call assistant messages valid for RouterHub's Gemini adapter."""
    if isinstance(input_items, str) or not isinstance(input_items, list):
        return input_items

    normalized: list[Any] = []
    assistant_index: int | None = None
    for item in input_items:
        item_type = item.get("type") if isinstance(item, dict) else None
        role = item.get("role") if isinstance(item, dict) else None

        if item_type == "message" and role == "assistant":
            normalized.append(item)
            assistant_index = len(normalized) - 1
            continue
        if item_type == "reasoning":
            normalized.append(item)
            continue
        if item_type in _TOOL_CALL_TYPES:
            call_id = str(item.get("call_id") or item.get("id") or len(normalized))
            fallback_id = f"routerhub_tool_message_{call_id}"
            if assistant_index is None:
                normalized.append(
                    _assistant_message_with_content(
                        {"role": "assistant", "content": []}, fallback_id
                    )
                )
                assistant_index = len(normalized) - 1
            else:
                assistant = normalized[assistant_index]
                if isinstance(assistant, dict):
                    normalized[assistant_index] = _assistant_message_with_content(
                        assistant, fallback_id
                    )
            normalized.append(item)
            continue

        normalized.append(item)
        assistant_index = None

    return normalized


class RouterHubGeminiChatCompletionsModel(OpenAIChatCompletionsModel):
    async def _fetch_response(
        self,
        system_instructions: Any,
        input: Any,
        *args: Any,
        **kwargs: Any,
    ) -> Any:
        return await super()._fetch_response(
            system_instructions,
            normalize_routerhub_gemini_tool_input(input),
            *args,
            **kwargs,
        )


def compatible_chat_completions_model(
    *, model: str, openai_client: AsyncOpenAI
) -> OpenAIChatCompletionsModel:
    model_type = (
        RouterHubGeminiChatCompletionsModel
        if is_routerhub_gemini_model(model)
        else OpenAIChatCompletionsModel
    )
    return model_type(model=model, openai_client=openai_client)
