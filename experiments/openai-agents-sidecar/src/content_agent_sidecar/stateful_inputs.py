from __future__ import annotations

from copy import deepcopy
from dataclasses import dataclass, field
from typing import Any

from agents import RunState
from agents.items import ItemHelpers

from .input_attachments import additional_input_message, input_content_hash
from .native_pause import initial_model_recovery, native_pending_input_matches


@dataclass
class StatefulInputs:
    ids: list[str] = field(default_factory=list)
    hashes: list[str] = field(default_factory=list)
    messages: list[dict[str, Any]] = field(default_factory=list)
    received_included: list[str] = field(default_factory=list)
    included: list[str] = field(default_factory=list)
    stage_ids: list[str] = field(default_factory=list)
    stage_included: list[str] = field(default_factory=list)
    # Native add_input admissions become generated InputItems; they do not
    # rewrite original_input used to verify a frozen repair candidate.
    original_ids: list[str] = field(default_factory=list)

    @classmethod
    def from_claim(cls, attempt_id: str, inputs: Any) -> StatefulInputs:
        if not isinstance(inputs, list) or len(inputs) > 128:
            raise ValueError("Stateful additional inputs exceed the durable limit")
        ledger, size = cls(), 0
        previous_sequence = 0
        for index, item in enumerate(inputs):
            if (not isinstance(item, dict) or item.get("attempt_id") != attempt_id
                or type(item.get("sequence")) is not int or not previous_sequence < item["sequence"] <= 128):
                raise ValueError("Stateful additional input identity or order is invalid")
            previous_sequence = item["sequence"]
            key, content, status = item.get("input_id"), item.get("content"), item.get("status")
            if (not isinstance(key, str) or not key.strip() or len(key) > 256 or key in ledger.ids
                or not isinstance(content, str) or status not in {"received", "included"}):
                raise ValueError("Stateful additional input payload is invalid")
            encoded = content.encode("utf-8")
            size += len(encoded)
            if len(encoded) > 32 << 10 or size > 512 << 10:
                raise ValueError("Stateful additional input exceeds the durable byte limit")
            digest = input_content_hash(item)
            if item.get("content_hash") != digest:
                raise ValueError("Stateful additional input content hash changed")
            ledger.ids.append(key)
            ledger.hashes.append(digest)
            ledger.messages.append(additional_input_message(item))
            if status == "included":
                if len(ledger.received_included) != index:
                    raise ValueError("Stateful input receipts are not an ordered prefix")
                ledger.received_included.append(key)
        return ledger

    def restore(self, saved: Any, *, result_repair: bool = False) -> None:
        if saved is None:
            if self.received_included and not result_repair:
                raise ValueError("Stateful included inputs have no native checkpoint")
            self.included = list(self.received_included)
            return
        keys = {"ids", "hashes", "included", "stage_ids", "stage_included", "original_ids"}
        if (not isinstance(saved, dict) or set(saved) != keys
            or any(not isinstance(saved[key], list) or len(saved[key]) > 128
                   or any(not isinstance(value, str) for value in saved[key]) for key in keys)):
            raise ValueError("Stateful input checkpoint is invalid")
        known, hashes = saved["ids"], saved["hashes"]
        included, staged, consumed = saved["included"], saved["stage_ids"], saved["stage_included"]
        if (known != self.ids[:len(known)] or hashes != self.hashes[:len(known)]
            or included != known[:len(included)] or staged != known[:len(staged)]
            or consumed != staged[:len(consumed)] or consumed != included[:len(consumed)]
            or saved["original_ids"] != staged[:len(saved["original_ids"])]
            or self.received_included != included[:len(self.received_included)]):
            raise ValueError("Stateful input checkpoint differs from the immutable input ledger")
        self.included, self.stage_ids, self.stage_included = deepcopy((included, staged, consumed))
        self.original_ids = list(saved["original_ids"])

    def snapshot(self) -> dict[str, Any]:
        return deepcopy({"ids": self.ids, "hashes": self.hashes, "included": self.included,
                         "stage_ids": self.stage_ids, "stage_included": self.stage_included, "original_ids": self.original_ids})

    def saved_original_input(self, base_input: Any) -> Any:
        if not self.original_ids:
            return base_input
        return [*ItemHelpers.input_to_new_input_list(base_input), *deepcopy(self.messages[:len(self.original_ids)])]

    def stage(self, runner_input: Any, *, resumed: bool = False) -> Any:
        if not resumed:
            self.stage_ids, self.stage_included = [], []
        elif isinstance(runner_input, RunState):
            expected = self.messages[len(self.stage_included):len(self.stage_ids)]
            if not native_pending_input_matches(runner_input, expected):
                raise ValueError("Stateful native pending input differs from its checkpoint ledger")
        elif self.stage_included:
            raise ValueError("Unstarted stateful checkpoint claims model-consumed input")
        new = deepcopy(self.messages[len(self.stage_ids):])
        if new:
            if isinstance(runner_input, RunState):
                if initial_model_recovery(runner_input):
                    return runner_input
                runner_input.add_input(new)
            else:
                runner_input = [*ItemHelpers.input_to_new_input_list(runner_input), *new]
        self.stage_ids = list(self.ids)
        if not isinstance(runner_input, RunState):
            self.original_ids = list(self.stage_ids)
        return runner_input

    def model_responded(self) -> None:
        # A response confirms admission, not obedience or completion of the task.
        self.stage_included = list(self.stage_ids)
        if len(self.stage_ids) > len(self.included):
            self.included = list(self.stage_ids)
