"""Assemble private native stages only inside an admitted memory attempt."""

from contextlib import asynccontextmanager
from dataclasses import replace
from hashlib import sha256
import json

from .agent_tools import AgentToolProvider
from .backend import BackendError
from .guardrails import SDKGuardrailPolicy
from .native_runner import NativeWorkspaceExecution
from .native_manifest import NativeGenerationSource
from .memory_input_plan import memory_input_plan_payload
from .native_files import _go_json_hash
from .memory_publication import prepare_agent_memory_publication, publish_agent_memory


@asynccontextmanager
async def prepare_memory_stage(stage, claim, context, policy, *, input_plan=None, input_receipt=None):
    if policy is None:
        yield stage
        return
    if not isinstance(policy, SDKGuardrailPolicy) or context.native_workspace is not None:
        raise BackendError("Memory stage requires one private workspace preparation owner")
    context.native_workspace_checkpoint = claim.checkpoint.native_workspace if claim.checkpoint is not None else None
    context.native_generation_source = None
    context.native_generation_files = None
    if claim.job.phase == "consolidation":
        if claim.extraction is None:
            raise BackendError("Memory workspace requires its persisted extraction")
        raw = json.dumps(claim.extraction.model_dump(), ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()
        context.native_generation_source = NativeGenerationSource(generation_id=claim.job.generation_id,
            source_hash=claim.job.source_hash, extraction_hash=sha256(raw).hexdigest())
    if input_plan is not None or input_receipt is not None:
        if input_plan is None or input_receipt is None:
            raise BackendError("Memory initialization requires its complete input receipt")
        value = memory_input_plan_payload(claim, input_plan)
        expected = {"generation_id": claim.job.generation_id, "plan_hash": _go_json_hash(value), "content_hash": input_plan.content_hash}
        if input_receipt != expected:
            raise BackendError("Memory initialization differs from its persisted input receipt")
        context.native_generation_source = context.native_generation_source.model_copy(update={"plan_hash": expected["plan_hash"]})
        context.native_generation_files = {path: sha256(text.encode()).hexdigest() for path, text in input_plan.files.items()}
    tools = [prepare_agent_memory_publication, publish_agent_memory] if claim.job.phase == "consolidation" else []
    provider = AgentToolProvider(context.backend, tools, guardrail_policy=policy)
    prepared = await provider.prepare(context, [], set())
    async with prepared:
        owner = NativeWorkspaceExecution(context)
        context.native_workspace = owner
        bound = await owner.bind_agent(stage.agent, prepared)
        yield replace(stage, agent=bound)
