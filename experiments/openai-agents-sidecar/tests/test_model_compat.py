from agents import AsyncOpenAI, OpenAIChatCompletionsModel
from agents.models.chatcmpl_converter import Converter

from content_agent_sidecar.model_compat import (
    RouterHubGeminiChatCompletionsModel,
    compatible_chat_completions_model,
    is_routerhub_gemini_model,
    normalize_routerhub_gemini_tool_input,
)


def function_call(call_id: str = "call_1") -> dict:
    return {
        "type": "function_call",
        "call_id": call_id,
        "name": "read_context_artifacts",
        "arguments": "{}",
    }


def test_routerhub_tool_call_gets_nonempty_assistant_content() -> None:
    items = [
        {"role": "user", "content": "read context"},
        function_call(),
        {
            "type": "function_call_output",
            "call_id": "call_1",
            "output": '{"ok":true}',
        },
    ]

    normalized = normalize_routerhub_gemini_tool_input(items)
    messages = Converter.items_to_messages(normalized, model="gemini-3p5-flash-routerhub")

    assert messages[1]["role"] == "assistant"
    assert messages[1]["content"] == "Calling the required tool."
    assert messages[1]["tool_calls"][0]["id"] == "call_1"
    assert messages[2]["role"] == "tool"


def test_routerhub_tool_call_preserves_existing_assistant_text() -> None:
    items = [
        {
            "id": "msg_1",
            "type": "message",
            "role": "assistant",
            "status": "completed",
            "content": [
                {"type": "output_text", "text": "working", "annotations": []}
            ],
        },
        function_call(),
    ]

    normalized = normalize_routerhub_gemini_tool_input(items)
    messages = Converter.items_to_messages(normalized, model="gemini-3p5-flash-routerhub")

    assert messages[0]["content"] == "working"
    assert messages[0]["tool_calls"][0]["id"] == "call_1"


def test_compatible_model_only_wraps_routerhub_gemini() -> None:
    client = AsyncOpenAI(api_key="test", base_url="https://example.invalid/v1")

    routerhub = compatible_chat_completions_model(
        model="gemini-3p5-flash-routerhub", openai_client=client
    )
    ordinary = compatible_chat_completions_model(
        model="another-model", openai_client=client
    )

    assert isinstance(routerhub, RouterHubGeminiChatCompletionsModel)
    assert type(ordinary) is OpenAIChatCompletionsModel
    assert is_routerhub_gemini_model("gemini-3p5-flash-routerhub")
    assert not is_routerhub_gemini_model("gemini-3p1-pro-apipro")
