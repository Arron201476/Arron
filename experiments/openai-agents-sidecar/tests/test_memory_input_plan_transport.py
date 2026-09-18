import asyncio
from dataclasses import replace
from hashlib import sha256
import json
from types import SimpleNamespace

import pytest
from agents import Agent

from content_agent_sidecar.backend import BackendError, backend_memory_activity
from content_agent_sidecar.memory_input_plan import plan_memory_consolidation, persist_memory_input_plan
from content_agent_sidecar.native_files import _go_json_hash
from test_memory_consolidation import fixture
from test_native_workspace_transport import response, wire


@pytest.mark.parametrize("fault", [None, "receipt", "baseline", "files", "delegated"])
def test_input_plan_requires_exact_independent_worker_receipt(fault):
    async def run():
        _, _, rollout, source, options = await fixture()
        baseline = source.model_copy(update={"content_hash": _go_json_hash(source.files)})
        plan = await plan_memory_consolidation(Agent(name="template", model="configured"), rollout, source, baseline, **options)
        claim = SimpleNamespace(job=SimpleNamespace(generation_id="generation", phase="consolidation",
            base_version=baseline.version, base_hash=baseline.content_hash, source_hash=rollout.content_hash, attempt=2),
            attempt_token="private-token", extraction=options["extraction"])
        if fault == "baseline": plan = replace(plan, baseline_hash="0" * 64)
        if fault == "files": plan = replace(plan, files={})
        def reply(request):
            method, path, headers, body = request
            assert method == "POST" and path.endswith("/generations/inputs")
            assert not headers["X-Agent-Memory-Generation-ID"]
            payload = json.loads(body)
            assert payload["attempt_token"] == claim.attempt_token and payload["attempt"] == 2
            assert payload["input_plan"]["files"] == plan.files
            assert list(payload["input_plan"]) == sorted(payload["input_plan"])
            encoded = json.dumps(payload["input_plan"], ensure_ascii=False, sort_keys=True, separators=(",", ":"))
            for plain, escaped in (("<", "\\u003c"), (">", "\\u003e"), ("&", "\\u0026"), ("\u2028", "\\u2028"), ("\u2029", "\\u2029")):
                encoded = encoded.replace(plain, escaped)
            digest = sha256(encoded.encode()).hexdigest()
            return response({"receipt": {"generation_id": "generation", "content_hash": plan.content_hash,
                "plan_hash": "0" * 64 if fault == "receipt" else digest}})
        with wire(reply) as (backend, requests):
            if fault == "delegated":
                with backend_memory_activity("project", "generation", 2, "private-token"), pytest.raises(BackendError):
                    await persist_memory_input_plan(backend, claim, "worker", plan)
            elif fault:
                with pytest.raises(BackendError):
                    await persist_memory_input_plan(backend, claim, "worker", plan)
            else:
                receipt = await persist_memory_input_plan(backend, claim, "worker", plan)
                assert receipt["content_hash"] == plan.content_hash
            assert len(requests) == (1 if fault in (None, "receipt") else 0)
    asyncio.run(run())
