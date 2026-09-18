from typing import Any

from agents.items import ItemHelpers


def initial_model_recovery(state: Any) -> bool:
    snapshot = state.to_json(context_serializer=lambda _: {})
    return (type(snapshot.get("current_turn")) is int and snapshot["current_turn"] > 0
            and snapshot.get("current_step") is None and snapshot.get("model_responses") == []
            and snapshot.get("generated_items") == [] and snapshot.get("last_model_response") is None)


def native_pending_input_matches(state: Any, expected: list[Any]) -> bool:
    if state.pending_input == expected:
        return True
    if state.pending_input or not expected:
        return False
    snapshot = state.to_json(context_serializer=lambda _: {})
    generated = snapshot.get("generated_items", [])
    if len(generated) >= len(expected):
        tail = generated[-len(expected):]
        if all(isinstance(item, dict) and item.get("type") == "input_item" for item in tail):
            return [item.get("raw_item") for item in tail] == expected
    # A failed first model request already holds its additional inputs in
    # original_input, without a response confirming receipt. Do not append them
    # twice or falsely mark them included merely to make restoration succeed.
    if not initial_model_recovery(state):
        return False
    original = ItemHelpers.input_to_new_input_list(snapshot["original_input"])
    return original[-len(expected):] == expected


def unstarted_checkpoint(state: dict[str, Any]) -> bool:
    # A zero-turn SDK checkpoint has not persisted or guarded its initial input.
    # Only that boundary may enter a fresh Runner; any executed state must resume.
    return (
        type(state.get("current_turn")) is int and state["current_turn"] == 0
        and state.get("current_step") is None
        and state.get("last_processed_response") is None
        and state.get("last_model_response") is None
        and all(state.get(key) == [] for key in ("model_responses", "generated_items", "session_items", "pending_input"))
    )


def validate_unstarted_input(state: dict[str, Any], initial_input: Any) -> None:
    if not unstarted_checkpoint(state) or not isinstance(initial_input, (str, list)):
        raise ValueError("Unstarted checkpoint has no frozen initial input")
    items = ItemHelpers.input_to_new_input_list(initial_input)
    original = ItemHelpers.input_to_new_input_list(state["original_input"])
    if not items or original[-len(items):] != items:
        raise ValueError("Unstarted checkpoint does not match its frozen initial input")
