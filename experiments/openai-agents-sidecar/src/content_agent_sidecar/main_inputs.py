from __future__ import annotations

from typing import Any

from agents import RunState
from agents.items import ItemHelpers

from .input_attachments import additional_input_message, input_content_hash
from .native_pause import initial_model_recovery, native_pending_input_matches


def main_input_batch(context: Any) -> tuple[list[str], list[str], list[dict[str, Any]]]:
    inputs = context.main_inputs
    if not isinstance(inputs, list) or len(inputs) > 128:
        raise ValueError("Main additional inputs are invalid")
    ids, hashes, messages = [], [], []
    size = 0
    previous_sequence = 0
    for index, item in enumerate(inputs):
        if not isinstance(item, dict) or item.get("agent_turn_id") != context.agent_turn_id or type(item.get("sequence")) is not int or not previous_sequence < item["sequence"] <= 128:
            raise ValueError("Main additional input identity or order is invalid")
        previous_sequence = item["sequence"]
        key, content = item.get("input_id"), item.get("content")
        if not isinstance(key, str) or not key.strip() or len(key) > 256 or key in ids or not isinstance(content, str):
            raise ValueError("Main additional input payload is invalid")
        encoded = content.encode("utf-8")
        size += len(encoded)
        if len(encoded) > 32 << 10 or size > 512 << 10:
            raise ValueError("Main additional input exceeds the durable limit")
        ids.append(key)
        hashes.append(input_content_hash(item))
        messages.append(additional_input_message(item))
    seen, saved_hashes, included = context.main_input_ids, context.main_input_hashes, context.main_included_input_ids
    if not all(isinstance(values, list) for values in (seen, saved_hashes, included)) or len(seen) > 128 or len(included) > 128:
        raise ValueError("Main input checkpoint ledger is invalid")
    if seen != ids[:len(seen)] or saved_hashes != hashes[:len(seen)] or included != seen[:len(included)]:
        raise ValueError("Main input checkpoint does not match its durable claim")
    for index, item in enumerate(inputs):
        if item.get("status", "received") not in {"received", "included"} or (item.get("status") == "included" and index >= len(included)):
            raise ValueError("Main input model receipt is missing from the native checkpoint")
    return ids, hashes, messages


def validate_main_input_state(context: Any, runner_input: Any) -> None:
    _, _, messages = main_input_batch(context)
    seen, included = context.main_input_ids, context.main_included_input_ids
    if isinstance(runner_input, RunState):
        if not native_pending_input_matches(runner_input, messages[len(included):len(seen)]):
            raise ValueError("Main native pending input does not match its checkpoint ledger")
    elif context.main_unstarted_input is not None and seen:
        original = ItemHelpers.input_to_new_input_list(runner_input)
        if included or original[-len(seen):] != messages[:len(seen)]:
            raise ValueError("Main unstarted input does not match its checkpoint ledger")
    elif seen != included:
        raise ValueError("Main input checkpoint is missing unconsumed native input")


def stage_main_inputs(context: Any, runner_input: Any) -> Any:
    validate_main_input_state(context, runner_input)
    ids, hashes, messages = main_input_batch(context)
    new = messages[len(context.main_input_ids):]
    if new:
        if isinstance(runner_input, RunState):
            if initial_model_recovery(runner_input):
                return runner_input
            runner_input.add_input(new)
        else:
            runner_input = [*ItemHelpers.input_to_new_input_list(runner_input), *new]
        context.main_input_ids, context.main_input_hashes = ids, hashes
    return runner_input
