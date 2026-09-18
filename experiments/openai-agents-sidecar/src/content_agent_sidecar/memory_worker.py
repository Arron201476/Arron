"""Sequential private memory scheduling over the existing SDK stage executors."""

import asyncio
import logging
import re
import json
import os
from hashlib import sha256

from agents import Agent, RunConfig, AsyncOpenAI, OpenAIResponsesModel, ModelSettings

from .backend import BackendClient, BackendError
from .guardrails import SDKGuardrailPolicy
from .memory_checkpoint import MemoryGenerationBinding
from .memory_execution import execute_memory_extraction, run_memory_consolidation
from .native_memory import memory_extraction_stage
from .runtime import AgentContext


logger = logging.getLogger(__name__)


class SDKMemoryWorker:
    @classmethod
    def from_settings(cls, settings):
        if not isinstance(settings.internal_token, str) or not settings.internal_token.strip():
            raise ValueError("Memory worker requires internal service credentials")
        settings.validate_model()
        if (type(settings.task_worker_lease_seconds) is not int
                or not 30 <= settings.task_worker_lease_seconds <= 1800
                or type(settings.task_worker_poll_milliseconds) is not int
                or not 100 <= settings.task_worker_poll_milliseconds <= 60000):
            raise ValueError("Memory worker polling or lease configuration is invalid")
        if not isinstance(settings.model_name, str) or not settings.model_name.strip() or len(settings.model_name) > 256:
            raise ValueError("Memory worker model identifier is invalid")
        policy = SDKGuardrailPolicy.from_settings(settings)
        policy_hash = sha256(json.dumps({"max_input_chars": policy.max_input_chars,
            "max_output_chars": policy.max_output_chars, "protected_values": sorted(policy.protected_values),
            "model_id": settings.model_name, "endpoint": settings.model_base_url}, sort_keys=True).encode()).hexdigest()
        backend = BackendClient(settings.backend_base_url, timeout_seconds=30, internal_token=settings.internal_token,
                                ffmpeg_command=settings.ffmpeg_command)
        client = AsyncOpenAI(api_key=settings.model_api_key, base_url=settings.model_base_url,
                             timeout=settings.model_timeout_seconds, max_retries=settings.model_max_retries)
        template = Agent(name="private-memory", model=OpenAIResponsesModel(model=settings.model_name, openai_client=client),
            model_settings=ModelSettings(max_tokens=min(settings.model_max_output_tokens, settings.orchestration_max_output_tokens)))
        worker = cls(backend, template, policy, worker_id=f"openai-agents-sdk-memory-{os.getpid()}",
            model_id=settings.model_name, policy_hash=policy_hash, lease_seconds=settings.task_worker_lease_seconds,
            poll_seconds=settings.task_worker_poll_milliseconds / 1000)
        worker._client = client
        return worker

    def __init__(self, backend, template: Agent, policy: SDKGuardrailPolicy, *, worker_id: str,
                 model_id: str, policy_hash: str, lease_seconds=60, poll_seconds=1.0):
        if (type(template) is not Agent or template.model is None or template.tools or template.handoffs
                or template.mcp_servers or callable(template.instructions) or not isinstance(policy, SDKGuardrailPolicy)
                or not isinstance(worker_id, str) or not worker_id.strip() or len(worker_id) > 256
                or not isinstance(model_id, str) or not model_id.strip() or len(model_id) > 256
                or isinstance(template.model, str) and template.model != model_id
                or not isinstance(policy_hash, str) or not re.fullmatch(r"[a-f0-9]{64}", policy_hash)
                or type(lease_seconds) is not int or not 30 <= lease_seconds <= 1800
                or type(poll_seconds) not in (int, float) or not .1 <= poll_seconds <= 60):
            raise ValueError("Memory worker configuration is invalid")
        self.backend, self.template, self.policy = backend, policy.protect(template), policy
        self.worker_id, self.model_id, self.policy_hash = worker_id, model_id, policy_hash
        self.lease_seconds, self.poll_seconds = lease_seconds, poll_seconds
        self._task = None
        self._client = None
        self._lock = asyncio.Lock()
        self._closed = False
        self._active_task = None
        self._shutdown_task = None

    def start(self):
        if self._closed:
            raise RuntimeError("Stopped memory worker cannot be restarted; create a new instance")
        if self._task is None:
            self._task = asyncio.create_task(self._run_loop())

    async def stop(self):
        if asyncio.current_task() is self._active_task:
            raise RuntimeError("Memory worker cannot stop from inside its own execution")
        self._closed = True
        if self._shutdown_task is None:
            self._shutdown_task = asyncio.create_task(self._shutdown())
        await asyncio.shield(self._shutdown_task)

    async def _shutdown(self):
        try:
            tasks = {task for task in (self._task, self._active_task) if task is not None}
            for task in tasks:
                task.cancel()
            if tasks:
                await asyncio.gather(*tasks, return_exceptions=True)
        finally:
            self._task = None
            if self._client is not None:
                client = self._client
                self._client = None
                await client.close()

    async def run_once(self):
        async with self._lock:
            if self._closed:
                raise RuntimeError("Stopped memory worker cannot claim work")
            self._active_task = asyncio.current_task()
            try:
                return await self._run_claim()
            finally:
                self._active_task = None

    async def _run_claim(self):
        claim = await self.backend.claim_memory_generation(self.worker_id, self.model_id,
            self.lease_seconds, policy_hash=self.policy_hash)
        if claim is None:
            return None
        if claim.job.model_id != self.model_id or claim.job.status != "running":
            raise BackendError("Memory worker received a mismatched claim")
        binding = MemoryGenerationBinding(**{name: getattr(claim.job, name)
            for name in MemoryGenerationBinding.model_fields if name != "policy_hash"}, policy_hash=self.policy_hash)
        context = AgentContext(claim.job.project_id, claim.conversation_id, self.backend,
            memory_generation_id=claim.job.generation_id, memory_generation_attempt=claim.job.attempt,
            attempt_token=claim.attempt_token)
        options = {"context": context, "native_policy": self.policy, "lease_seconds": self.lease_seconds,
                   "run_config": RunConfig(tracing_disabled=True), "cooperative_pause": True}
        if claim.job.phase == "extraction":
            stage = memory_extraction_stage(self.template, claim.source.rollout_jsonl)
            return await execute_memory_extraction(self.backend, claim, self.worker_id, stage, binding, **options)
        if claim.job.phase == "consolidation":
            return await run_memory_consolidation(self.backend, claim, self.worker_id, self.template, binding, **options)
        raise BackendError("Memory worker received an unsupported phase")

    async def _run_loop(self):
        while True:
            try:
                await self.run_once()
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                # Private source text, tool arguments and credentials never enter logs.
                logger.warning("Memory worker attempt unconfirmed cause_type=%s", type(exc).__name__)
            await asyncio.sleep(self.poll_seconds)
