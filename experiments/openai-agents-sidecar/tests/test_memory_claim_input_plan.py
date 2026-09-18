from hashlib import sha256
import json

import pytest

from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.native_files import _go_json_hash
from test_memory_generation_claim import decode, raw_claim


@pytest.mark.parametrize("fault", [None, "missing-plan", "missing-hash", "phase", "hash", "files",
                                  "baseline_version", "baseline_hash", "source_hash", "extraction_hash", "path"])
def test_claim_recovers_bound_private_input_plan(raw_claim, fault):
    raw_claim["job"]["phase"] = "consolidation"
    extraction = {"rollout_slug": "fixture", "rollout_summary": "summary", "raw_memory": "private"}
    raw_claim["extraction"] = extraction
    files = {f".agent-memory/file-{index}.md": "PRIVATE_INPUT" for index in range(4)}
    plan = {"baseline_version": raw_claim["job"]["base_version"], "baseline_hash": raw_claim["job"]["base_hash"],
            "source_hash": raw_claim["job"]["source_hash"],
            "extraction_hash": sha256(json.dumps(extraction, ensure_ascii=False, separators=(",", ":")).encode()).hexdigest(),
            "files": files, "content_hash": _go_json_hash(files)}
    if fault in {"baseline_version", "baseline_hash", "source_hash", "extraction_hash"}:
        plan[fault] = 99 if fault == "baseline_version" else "f" * 64
    if fault == "path":
        files["public/leak.md"] = files.pop(".agent-memory/file-0.md")
        plan["content_hash"] = _go_json_hash(files)
    raw_claim.update(input_plan=plan, input_plan_hash=_go_json_hash(dict(sorted(plan.items()))))
    if fault == "missing-plan": del raw_claim["input_plan"]
    if fault == "missing-hash": del raw_claim["input_plan_hash"]
    if fault == "phase": raw_claim["job"]["phase"] = "extraction"
    if fault == "hash": raw_claim["input_plan_hash"] = "f" * 64
    if fault == "files": files[".agent-memory/file-0.md"] = "changed"
    if fault:
        with pytest.raises(BackendError) as error:
            decode(raw_claim)
        assert "PRIVATE_INPUT" not in str(error.value)
    else:
        claim = decode(raw_claim)
        assert claim.input_plan.model_dump() == plan
        assert claim.input_plan_hash == raw_claim["input_plan_hash"]
        assert "PRIVATE_INPUT" not in repr(claim)
