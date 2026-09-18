from __future__ import annotations

import json

from pydantic import ValidationError

from .contracts import ControlDecision


def parse_control_decision(raw: str) -> ControlDecision:
    for value in _json_objects(raw):
        try:
            return ControlDecision.model_validate(value)
        except ValidationError:
            continue

    excerpt = raw.strip()[:240].replace("\r", " ").replace("\n", " ")
    raise ValueError(f"provider did not return a valid ControlDecision JSON object: {excerpt}")


def _json_objects(raw: str):  # type: ignore[no-untyped-def]
    text = raw.strip()
    # The internal compatible gateway may escape JSON's structural quotes as
    # literal unicode sequences while leaving braces and colons untouched.
    # Replacing only quote escapes preserves value-level unicode escapes for
    # the standard JSON decoder.
    text = text.replace("\\u0022", '"').replace("\\u0022".upper(), '"')
    decoder = json.JSONDecoder()
    candidates = [0]
    candidates.extend(index for index, char in enumerate(text) if char == "{")

    for index in dict.fromkeys(candidates):
        try:
            value, _ = decoder.raw_decode(text[index:])
        except json.JSONDecodeError:
            continue
        if not isinstance(value, dict):
            continue
        yield value
