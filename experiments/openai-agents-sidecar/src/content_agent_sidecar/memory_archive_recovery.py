"""Reauthorize frozen archives without restoring obsolete execution credentials."""

import json

from .backend import BackendError
from .memory_rollout import MemoryArchivePolicy, MemoryRollout


async def recover_memory_archive(backend, payload: dict) -> dict[str, str]:
    try:
        if not isinstance(payload, dict) or set(payload) != {"rollout", "consent_revision", "generate_enabled"}:
            raise ValueError("shape")
        rollout = MemoryRollout.model_validate(payload["rollout"])
        policy = MemoryArchivePolicy(project_id=rollout.project_id, user_id=rollout.user_id,
                                     archive_enabled=True, generate_enabled=payload["generate_enabled"],
                                     revision=payload["consent_revision"])
        result = json.loads(rollout.rollout_jsonl)
        if result["terminal_metadata"]["terminal_state"] != "completed" or result.get("interruptions"):
            raise ValueError("unfinished")
    except (ValueError, TypeError, KeyError):
        raise BackendError("Memory archive recovery requires a valid completed private source") from None
    receipt = await backend.recover_memory_archive({"rollout": rollout.model_dump(), "consent_revision": policy.revision})
    expected = {key: getattr(rollout, key) for key in ("activity_key", "segment_id", "project_id", "user_id", "content_hash")}
    if receipt != expected:
        raise BackendError("Memory archive recovery receipt differs from its frozen source")
    return expected
