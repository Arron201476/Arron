from __future__ import annotations

from .native_runner import bind_native_agent, native_run_config, settle_native_stream
from .memory_autocapture import capture_completed_memory
from .native_live_state import restore_native_checkpoint

import asyncio
from contextlib import suppress
from contextvars import ContextVar
from collections.abc import Awaitable, Callable
import json
import logging
import os
import traceback
from typing import Any, Literal

from agents import (
    Agent,
    AgentOutputSchema,
    AsyncOpenAI,
    InputGuardrailTripwireTriggered,
    OutputGuardrailTripwireTriggered,
    ModelBehaviorError,
    ModelSettings,
    OpenAIResponsesModel,
    Runner,
    RunState,
    UserError,
)
from openai import BadRequestError
from pydantic import BaseModel, ConfigDict, Field, ValidationError

from .backend import BackendClient, BackendError, backend_activity
from .agent_tools import AgentToolConfigurationError, AgentToolProvider, TOOL_APPROVAL_INSTRUCTIONS
from .config import Settings
from .execution_tools import skill_execution_tools
from .observability import privacy_safe_run_config
from .guardrails import SDKGuardrailPolicy
from .native_pause import initial_model_recovery, native_pending_input_matches, unstarted_checkpoint, validate_unstarted_input
from .model_failure_boundary import MODEL_RECOVERY_REASON, ModelFailureBoundary
from .tool_outcomes import EXTERNAL_TOOL_RECOVERY_REASON, require_external_tool_pause
from .input_attachments import additional_input_message, input_content_hash, prepare_input_attachments
from .managed_instructions import bind_managed_instructions, with_managed_instructions
from .model_compat import compatible_chat_completions_model, is_routerhub_gemini_model
from .contracts import AgentToolApprovalDecision
from .runtime import (
    AgentContext, RuntimeCompatibilityError, _load_skill_instructions, normalize_provider_error,
    _apply_approval_decisions, _serialize_paused_run, _checkpoint_string_list,
    normalize_execution_stream_event,
)


logger = logging.getLogger(__name__)

_BACKGROUND_PROGRAM_PROGRESS: ContextVar[Callable[[dict[str, Any]], Awaitable[None]] | None] = ContextVar(
    "background_program_progress", default=None,
)


class BackgroundArtifactDraft(BaseModel):
    model_config = ConfigDict(extra="forbid")

    artifact_type: Literal["generic_document", "generic_table"]
    title: str = Field(min_length=1, max_length=200)
    payload: dict[str, Any]


class BackgroundTaskOutput(BaseModel):
    model_config = ConfigDict(extra="forbid")

    summary: str = Field(min_length=1, max_length=2000)
    result: dict[str, Any] = Field(default_factory=dict)
    artifact_draft: BackgroundArtifactDraft | None = None


class BackgroundTaskPaused(Exception):
    def __init__(self, result: Any, *, user_requested: bool = False, recovery_reason: str = "") -> None:
        super().__init__("Background task paused" if user_requested else "Background task awaits tool approval")
        self.user_requested = user_requested
        self.recovery_reason = recovery_reason
        self.run_state, self.schema_version, self.pending_ids = _serialize_paused_run(result, allow_no_approvals=user_requested)


class SDKBackgroundTaskWorker:
    """Executes any background Skill from its immutable instruction snapshot."""

    def __init__(
        self,
        settings: Settings,
        backend: BackendClient | None = None,
        *,
        slot: int = 1,
        archive_queue=None,
    ) -> None:
        settings.validate_model()
        self._settings = settings
        self._archive_queue = archive_queue
        self._guardrails = SDKGuardrailPolicy.from_settings(settings)
        self._backend = backend or BackendClient(
            settings.backend_base_url,
            timeout_seconds=30,
            internal_token=settings.internal_token,
            ffmpeg_command=settings.ffmpeg_command,
        )
        base_url = settings.content_model_base_url or settings.model_base_url
        api_key = settings.content_model_api_key or settings.model_api_key
        self._model_name = settings.content_model_name or settings.model_name
        timeout = (
            settings.content_model_timeout_seconds
            if settings.content_model_base_url
            else settings.model_timeout_seconds
        )
        retries = (
            settings.content_model_max_retries
            if settings.content_model_base_url
            else settings.model_max_retries
        )
        client = AsyncOpenAI(
            api_key=api_key,
            base_url=base_url,
            timeout=timeout,
            max_retries=retries,
        )
        self._model = (
            compatible_chat_completions_model(
                model=self._model_name,
                openai_client=client,
            )
            if settings.content_model_base_url
            else OpenAIResponsesModel(model=self._model_name, openai_client=client)
        )
        self._max_tokens = min(
            settings.content_model_max_output_tokens
            if settings.content_model_base_url
            else settings.model_max_output_tokens,
            settings.orchestration_max_output_tokens,
        )
        self._worker_id = f"openai-agents-sdk-background-{os.getpid()}-slot-{slot}"
        self._task: asyncio.Task[None] | None = None
        self._heartbeat_interval_seconds = min(10.0, settings.task_worker_lease_seconds / 3)
        hosted_client = AsyncOpenAI(api_key=settings.model_api_key, base_url=settings.model_base_url,
                                    timeout=settings.model_timeout_seconds, max_retries=0)
        self._tool_provider = AgentToolProvider(self._backend, skill_execution_tools(),
            hosted_model=OpenAIResponsesModel(model=settings.model_name, openai_client=hosted_client), hosted_client=hosted_client,
            subtask_model=lambda: self._model, guardrail_policy=self._guardrails,
            execution_model=lambda: self._model)

    def start(self) -> None:
        if self._task is None:
            self._task = asyncio.create_task(self._run_loop())

    async def stop(self) -> None:
        if self._task is None:
            return
        self._task.cancel()
        with suppress(asyncio.CancelledError):
            await self._task
        self._task = None

    async def run_once(self) -> bool:
        claim = await self._backend.claim_agent_task(
            worker_id=self._worker_id,
            provider_id="openai_agents_sdk",
            model_id=self._model_name,
            lease_seconds=self._settings.task_worker_lease_seconds,
        )
        if claim is None:
            return False
        attempt = claim.get("attempt") or {}
        attempt_id = str(attempt.get("agent_task_attempt_id") or "")
        try:
            progress = await self._backend.update_agent_task_progress(
                claim, current=1, total=3, message="加载 Skill 指令"
            )
            pause_requested = asyncio.Event()
            self._observe_pause_request(progress, pause_requested)
            output, usage, trace_ref = await self._execute_with_liveness(claim, pause_requested)
            await self._backend.update_agent_task_progress(
                claim, current=2, total=3, message="提交任务结果"
            )
            artifact = output.get("artifact_draft")
            await self._backend.complete_agent_task(
                claim,
                result={
                    "summary": output["summary"],
                    "data": output.get("result") or {},
                },
                artifact_draft=artifact if isinstance(artifact, dict) else None,
                usage=usage,
                trace_ref=trace_ref,
                **({"included_input_ids": claim["_included_input_ids"]} if claim.get("_included_input_ids") else {}),
            )
            logger.info("SDK background task completed attempt_id=%s", attempt_id)
        except BackgroundTaskPaused as pause:
            checkpoint = self._backend.complete_agent_task_pause if pause.user_requested else self._backend.pause_agent_task_for_approval
            await checkpoint(claim, run_state=pause.run_state,
                schema_version=pause.schema_version, pending_sdk_tool_call_ids=pause.pending_ids,
                **({"recovery_reason": pause.recovery_reason} if pause.recovery_reason else {}),
                **({"included_input_ids": claim["_included_input_ids"]} if claim.get("_included_input_ids") else {}))
            logger.info("SDK background task checkpointed attempt_id=%s user_pause=%s", attempt_id, pause.user_requested)
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            error_code = self._error_code(exc)
            detail = self._safe_error_detail(exc)
            logger.warning(
                "SDK background task failed attempt_id=%s code=%s error=%s frames=%s",
                attempt_id,
                error_code,
                detail,
                [(frame.f_code.co_name, line) for frame, line in traceback.walk_tb(exc.__traceback__)][-8:],
            )
            with suppress(BackendError):
                await self._backend.fail_agent_task(
                    claim,
                    error_code=error_code,
                    error_message=detail,
                    retryable=error_code == "PROVIDER_TEMPORARY_FAILURE",
                    **({"included_input_ids": claim["_included_input_ids"]} if claim.get("_included_input_ids") else {}),
                )
        return True

    async def _run_loop(self) -> None:
        idle_seconds = self._settings.task_worker_poll_milliseconds / 1000
        while True:
            try:
                worked = await self.run_once()
                if not worked:
                    await asyncio.sleep(idle_seconds)
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                logger.error("SDK background task worker loop failed: %s", exc)
                await asyncio.sleep(max(idle_seconds, 2.0))

    async def _execute_claim(
        self, claim: dict[str, Any], pause_requested: asyncio.Event | None = None,
    ) -> tuple[dict[str, Any], dict[str, Any], str]:
        with backend_activity(
            str((claim.get("task") or {}).get("project_id") or ""),
            task_attempt_id=str((claim.get("attempt") or {}).get("agent_task_attempt_id") or ""),
            attempt_token=str(claim.get("attempt_token") or ""),
        ):
            return await self._execute_claim_scoped(claim, pause_requested)

    async def _execute_claim_scoped(
        self, claim: dict[str, Any], pause_requested: asyncio.Event | None = None,
    ) -> tuple[dict[str, Any], dict[str, Any], str]:
        instructions = str(claim.get("instructions") or "").strip()
        if not instructions:
            raise ValueError("background Skill instructions are missing")
        task = claim.get("task")
        if not isinstance(task, dict):
            raise ValueError("background task snapshot is missing")
        capability_id = str(task.get("capability_id") or "")
        version = str(task.get("capability_version") or "")
        if not all(task.get(key) for key in ("project_id", "conversation_id", "skill_invocation_id")) or not capability_id or not version:
            raise ValueError("background task identity or pinned Skill version is missing")
        try:
            additional = await prepare_input_attachments(str(task["project_id"]), claim.get("additional_inputs", []), self._backend)
        except (ValueError, BackendError) as exc:
            raise RuntimeCompatibilityError("Background additional material is invalid", failure_stage="run_state_restore") from exc
        context = AgentContext(
            project_id=str(task["project_id"]), conversation_id=str(task["conversation_id"]),
            backend=self._backend, skill_invocation_id=str(task["skill_invocation_id"]),
            routed_capabilities={capability_id}, capability_versions={capability_id: version},
            agent_task_id=str(task["agent_task_id"]), agent_task_attempt_id=str(claim["attempt"]["agent_task_attempt_id"]),
            attempt_token=str(claim["attempt_token"]),
            raw_request={"content": str(claim.get("request_content") or ""), "input": task.get("input") or {}, "config": task.get("config") or {}},
            background_inputs=additional,
            memory_archive_queue=self._archive_queue,
        )
        await _load_skill_instructions(context, capability_id)
        resume = claim.get("resume")
        if resume is not None:
            await self._restore_context(context, resume)
        await bind_managed_instructions(context, resume["run_state"] if resume is not None else None)
        prepared = await self._tool_provider.prepare(
            context, list(context.loaded_capabilities.values()), set(context.routed_capabilities),
        )
        prompt = (
            "完成下面的后台任务。严格遵守 Skill 指令。返回任务摘要、结构化结果；"
            "只有结果适合持久保存时才返回 artifact_draft。不得把隐藏推理写入结果。\n\n"
            "USER_REQUEST:\n"
            + str(claim.get("request_content") or "")
            + "\n\nTASK_INPUT:\n"
            + json.dumps(task.get("input") or {}, ensure_ascii=False)
            + "\n\nTASK_CONFIG:\n"
            + json.dumps(task.get("config") or {}, ensure_ascii=False)
        )
        agent = Agent(
            name=str(claim.get("skill_name") or "Background Skill"),
            instructions=instructions + "\nUse tools to read project materials and Skill references as needed. Do not invent unread resource contents. Save working files with apply_workspace_patch, using exact versions and SDK V4A patches; saved SKILL.md files are not automatically installed Skills. Use get_artifact_downloads for saved document/table versions, and only report download URLs returned by tools. Document payloads use a content_markdown or content string; table payloads use explicit columns (strings or key/label objects) and rows (rectangular arrays or objects keyed by column). Downloads do not save files on the user's computer. External writes require the configured tool approval; never bypass it. Return task output for the Runtime to persist.\n" + TOOL_APPROVAL_INSTRUCTIONS,
            model=self._model,
            model_settings=ModelSettings(max_tokens=self._max_tokens),
            output_type=AgentOutputSchema(BackgroundTaskOutput, strict_json_schema=False),
            tools=prepared.tools,
            mcp_servers=prepared.mcp_servers,
        )
        agent = agent.clone(instructions=with_managed_instructions(agent.instructions, context))
        async with prepared:
            try:
                return await self._run_agent(agent, prompt, context, resume=resume, pause_requested=pause_requested)
            finally:
                claim["_included_input_ids"] = list(context.background_included_input_ids)

    async def _run_agent(self, agent: Agent, prompt: str, context: AgentContext, *, resume: dict[str, Any] | None = None, pause_requested: asyncio.Event | None = None) -> tuple[dict[str, Any], dict[str, Any], str]:
        agent = self._guardrails.protect(agent)
        if is_routerhub_gemini_model(
            str(getattr(self, "_model_name", "") or "")
        ):
            context.background_output_mode = "json"
        if context.background_output_mode == "json":
            agent = self._json_agent(agent)
        agent = await bind_native_agent(context, self._settings, agent)
        runner_input: Any = prompt
        if resume is not None:
            runner_input = await RunState.from_json(agent, resume["run_state"], context_override=context)
            decisions = resume.get("approval_decisions")
            if not isinstance(decisions, list):
                raise RuntimeCompatibilityError("Background checkpoint approval decisions are invalid", failure_stage="run_state_restore")
            if decisions or runner_input.get_interruptions():
                _apply_approval_decisions(runner_input, [AgentToolApprovalDecision.model_validate(item) for item in decisions])
        initial_prompt = prompt if resume is not None and unstarted_checkpoint(resume["run_state"]) else None
        runner_input = self._stage_additional_inputs(runner_input, context, initial_prompt=initial_prompt)
        try:
            run = await self._run_streamed(agent, runner_input, context, pause_requested)
        except (BadRequestError, ModelBehaviorError):
            # Restarting after a tool ran can duplicate an external action.
            if resume is not None or context.active_tool_calls or (pause_requested is not None and pause_requested.is_set()):
                raise
            context.background_output_mode = "json"
            compatible_agent = self._json_agent(agent)
            run = await self._run_streamed(compatible_agent, runner_input, context, pause_requested)
        user_pause = pause_requested is not None and pause_requested.is_set()
        if getattr(run, "interruptions", None):
            raise BackgroundTaskPaused(run, user_requested=user_pause)
        if user_pause and run.final_output is None:
            raise BackgroundTaskPaused(run, user_requested=True)
        output = self._normalize_output(run.final_output)
        await capture_completed_memory(context, run)
        return output, self._usage(run), self._trace_ref(run)

    async def _run_streamed(self, agent: Agent, runner_input: Any, context: AgentContext, pause_requested: asyncio.Event | None) -> Any:
        async with native_run_config(context, self._sdk_run_config(), runner_input) as config:
            result = await self._run_streamed_once(agent, runner_input, context, pause_requested, config)
        deferred_inputs = len(context.background_input_ids) < len(context.background_inputs)
        if deferred_inputs and result.final_output is None and not result.interruptions and not (pause_requested is not None and pause_requested.is_set()):
            staged = self._stage_additional_inputs(result.to_state(), context)
            return await self._run_streamed(agent, staged, context, pause_requested)
        return result

    async def _run_streamed_once(self, agent, runner_input, context, pause_requested, config):
        resumed = isinstance(runner_input, RunState)
        deferred_inputs = len(context.background_input_ids) < len(context.background_inputs)
        boundary = ModelFailureBoundary(lambda: result.cancel(mode="after_turn") if deferred_inputs else None,
                                        on_external_review=lambda: result.cancel(mode="after_turn"))
        result = Runner.run_streamed(agent, runner_input, context=context, max_turns=16, hooks=boundary, run_config=config)
        model_responded = False
        model_started = asyncio.Event()

        async def wait_for_pause() -> None:
            assert pause_requested is not None
            await pause_requested.wait()
            if not resumed:
                await model_started.wait()
            result.cancel(mode="after_turn")

        watcher = None
        if pause_requested is not None:
            if pause_requested.is_set():
                result.cancel(mode="after_turn")
            else:
                watcher = asyncio.create_task(wait_for_pause())
        recovery_error = None
        cancelled = False
        try:
            async with asyncio.timeout(self._settings.run_timeout_seconds):
                async for event in result.stream_events():
                    report = _BACKGROUND_PROGRAM_PROGRESS.get()
                    if report is not None:
                        normalized = normalize_execution_stream_event(event)
                        if normalized is not None and normalized.get("data", {}).get("tool_name") == "programmatic_tool_calling":
                            await report(normalized)
                    if event.type == "raw_response_event":
                        model_started.set()
                    if event.type == "raw_response_event" and getattr(event.data, "type", "") == "response.completed":
                        model_responded = True
        except BaseException as exc:
            if boundary.permits_checkpoint(exc, result):
                recovery_error = exc
            else:
                result.cancel()
                cancelled = True
                raise
        finally:
            if watcher is not None:
                watcher.cancel()
                await asyncio.gather(watcher, return_exceptions=True)
            await settle_native_stream(context, result, cancelled=cancelled)
            # Output rejection does not undo earlier native input admission.
            if context.background_input_ids and model_responded and not result.to_state().pending_input:
                context.background_included_input_ids = list(context.background_input_ids)
        if boundary.external_review:
            require_external_tool_pause(result)
            raise BackgroundTaskPaused(result, user_requested=True, recovery_reason=EXTERNAL_TOOL_RECOVERY_REASON)
        if recovery_error is not None:
            raise BackgroundTaskPaused(result, user_requested=True, recovery_reason=MODEL_RECOVERY_REASON) from recovery_error
        if deferred_inputs and result.final_output is None and not result.interruptions and not (pause_requested is not None and pause_requested.is_set()):
            if not model_responded:
                raise RuntimeCompatibilityError("Native model recovery did not reach an input admission boundary")
        return result

    @staticmethod
    def _stage_additional_inputs(runner_input: Any, context: AgentContext, *, initial_prompt: str | None = None) -> Any:
        inputs = context.background_inputs
        if not isinstance(inputs, list) or len(inputs) > 128:
            raise RuntimeCompatibilityError("Background additional inputs are invalid", failure_stage="run_state_restore")
        ids, contents, hashes = [], [], []
        size = 0
        previous_sequence = 0
        for index, item in enumerate(inputs):
            if not isinstance(item, dict) or item.get("agent_task_id") != context.agent_task_id or type(item.get("sequence")) is not int or not previous_sequence < item["sequence"] <= 128:
                raise RuntimeCompatibilityError("Background input identity or order is invalid", failure_stage="run_state_restore")
            previous_sequence = item["sequence"]
            key, content = item.get("input_id"), item.get("content")
            if not isinstance(key, str) or not key or len(key) > 256 or key in ids or not isinstance(content, str) or len(content.encode("utf-8")) > 32 << 10:
                raise RuntimeCompatibilityError("Background input payload is invalid", failure_stage="run_state_restore")
            ids.append(key)
            size += len(content.encode("utf-8"))
            if size > 512 << 10:
                raise RuntimeCompatibilityError("Background input exceeds the durable limit", failure_stage="run_state_restore")
            hashes.append(input_content_hash(item))
            contents.append(additional_input_message(item))
        seen, included = context.background_input_ids, context.background_included_input_ids
        if seen != ids[:len(seen)] or included != seen[:len(included)] or context.background_input_hashes != hashes[:len(seen)]:
            raise RuntimeCompatibilityError("Background input checkpoint does not match its durable claim", failure_stage="run_state_restore")
        if seen and not isinstance(runner_input, RunState):
            raise RuntimeCompatibilityError("Background input checkpoint is missing", failure_stage="run_state_restore")
        if isinstance(runner_input, RunState) and initial_prompt is None and not native_pending_input_matches(runner_input, contents[len(included):len(seen)]):
            raise RuntimeCompatibilityError("Background native pending input differs from its checkpoint", failure_stage="run_state_restore")
        if initial_prompt is not None:
            if included:
                raise RuntimeCompatibilityError("Unstarted checkpoint claims admitted input", failure_stage="run_state_restore")
            original = [{"role": "user", "content": initial_prompt}, *contents[:len(seen)]]
            try:
                validate_unstarted_input(runner_input.to_json(context_serializer=lambda _: {}), original)
            except ValueError as exc:
                raise RuntimeCompatibilityError(str(exc), failure_stage="run_state_restore") from exc
            runner_input, seen = initial_prompt, []
        new = contents[len(seen):]
        if new:
            if isinstance(runner_input, RunState):
                if initial_model_recovery(runner_input):
                    return runner_input
                try:
                    runner_input.add_input(new)
                except UserError as exc:
                    raise RuntimeCompatibilityError("Native SDK checkpoint cannot accept additional input", failure_stage="run_state_restore") from exc
            else:
                runner_input = [{"role": "user", "content": runner_input}, *new]
        context.background_input_ids = ids
        context.background_input_hashes = hashes
        return runner_input

    @staticmethod
    def _json_agent(agent: Agent) -> Agent:
        schema = json.dumps(BackgroundTaskOutput.model_json_schema(), ensure_ascii=False)
        return agent.clone(instructions=str(agent.instructions) + "\nReturn exactly one JSON object matching OUTPUT_SCHEMA. Keep artifact content inside artifact_draft.payload, not at its top level. Do not omit required fields.\nOUTPUT_SCHEMA:\n" + schema, output_type=None)

    @staticmethod
    async def _restore_context(context: AgentContext, resume: dict[str, Any]) -> None:
        state = resume.get("run_state") if isinstance(resume, dict) else None
        payload = (state.get("context") or {}).get("context") if isinstance(state, dict) else None
        if not isinstance(payload, dict) or payload.get("schema_version") != "content_agent_context.v1":
            raise ValueError("Background RunState context is incompatible")
        if payload.get("agent_turn_id") or payload.get("execution_attempt_id"):
            raise ValueError("Background RunState contains another execution mode")
        for key in ("project_id", "conversation_id", "agent_task_id", "agent_task_attempt_id", "skill_invocation_id", "raw_request"):
            if payload.get(key) != getattr(context, key):
                raise ValueError("Background RunState identity or input does not match the durable task")
        versions = payload.get("capability_versions")
        loaded = payload.get("loaded_capabilities")
        if not isinstance(versions, dict) or len(versions) > 128 or not all(isinstance(key, str) and key and isinstance(value, str) and value for key, value in versions.items()):
            raise ValueError("Background RunState capability versions are invalid")
        if not isinstance(loaded, dict) or not all(isinstance(key, str) and isinstance(value, dict) and key in versions for key, value in loaded.items()):
            raise ValueError("Background RunState Skill metadata is invalid")
        # The durable task's primary Skill remains pinned. Additional inline
        # Skills must be revalidated against this attempt's authorized catalog.
        for key, version in context.capability_versions.items():
            if versions.get(key) != version or loaded.get(key) != context.loaded_capabilities.get(key):
                raise ValueError("Background Skill metadata changed since the approval checkpoint")
        routed = set(_checkpoint_string_list(payload, "routed_capabilities"))
        if not routed.issubset(versions):
            raise ValueError("Background RunState routed Skill versions are missing")
        primary_ids = set(context.capability_versions)
        context.routed_capabilities.update(routed)
        context.capability_versions.update(versions)
        for key in sorted(routed | set(loaded)):
            if key in primary_ids:
                continue
            await _load_skill_instructions(context, key)
            current = context.loaded_capabilities[key]
            skill = current.get("skill") or {}
            if current.get("execution_mode") != "inline":
                raise ValueError("Additional background Skills must remain inline")
            if (skill.get("dependencies") or skill.get("scripts")) and key not in loaded:
                raise ValueError("Additional Skill dependencies have no verified checkpoint metadata")
            if key in loaded and loaded[key] != current:
                raise ValueError("Background Skill metadata changed since the approval checkpoint")
        context.consulted_capabilities.update(_checkpoint_string_list(payload, "consulted_capabilities"))
        mode = payload.get("background_output_mode")
        if mode not in {"structured", "json"}:
            raise ValueError("Background RunState output mode is invalid")
        context.background_output_mode = mode
        restore_native_checkpoint(context, payload)
        for key in ("background_input_ids", "background_included_input_ids"):
            value = payload.get(key, [])
            if not isinstance(value, list) or len(value) > 128 or not all(isinstance(item, str) and item and len(item) <= 256 for item in value) or len(set(value)) != len(value):
                raise RuntimeCompatibilityError("Background input checkpoint is invalid", failure_stage="run_state_restore")
            setattr(context, key, list(value))
        hashes = payload.get("background_input_hashes")
        if hashes is None and "background_input_hashes" not in payload:
            old_inputs = context.background_inputs[:len(context.background_input_ids)]
            if any(item.get("attachments") for item in old_inputs):
                raise RuntimeCompatibilityError("Legacy background checkpoint cannot claim attachments", failure_stage="run_state_restore")
            hashes = [input_content_hash(item) for item in old_inputs]
        if not isinstance(hashes, list) or len(hashes) != len(context.background_input_ids) or any(not isinstance(value, str) or len(value) != 64 for value in hashes):
            raise RuntimeCompatibilityError("Background input hashes are invalid", failure_stage="run_state_restore")
        context.background_input_hashes = list(hashes)

    @staticmethod
    def _observe_pause_request(progress: dict[str, Any], signal: asyncio.Event) -> None:
        task = progress.get("data", progress)
        if isinstance(task, dict) and task.get("status") == "pausing":
            signal.set()

    async def _execute_with_liveness(self, claim: dict[str, Any], pause_requested: asyncio.Event | None = None):
        pause_requested = pause_requested or asyncio.Event()
        progress_lock = asyncio.Lock()
        message = "执行 Skill 任务"

        async def publish_program(event: dict[str, Any]) -> None:
            nonlocal message
            if event.get("event") == "agent.tool.started":
                next_message = "正在执行程序化工具调用"
            elif event.get("event") == "agent.tool.completed" and event.get("data", {}).get("status") in {"completed", "incomplete"}:
                next_message = "程序执行未完成" if event["data"]["status"] == "incomplete" else "正在整理程序结果"
            else:
                return
            async with progress_lock:
                message = next_message
                progress = await self._backend.update_agent_task_progress(claim, current=1, total=3, message=message)
                self._observe_pause_request(progress, pause_requested)

        async def heartbeat() -> None:
            while True:
                await asyncio.sleep(self._heartbeat_interval_seconds)
                async with progress_lock:
                    progress = await self._backend.update_agent_task_progress(
                        claim, current=1, total=3, message=message,
                        lease_seconds=self._settings.task_worker_lease_seconds,
                    )
                    self._observe_pause_request(progress, pause_requested)

        token = _BACKGROUND_PROGRAM_PROGRESS.set(publish_program)
        try:
            execution = asyncio.create_task(asyncio.wait_for(
                self._execute_claim(claim, pause_requested), timeout=self._settings.run_timeout_seconds,
            ))
        finally:
            _BACKGROUND_PROGRAM_PROGRESS.reset(token)
        monitor = asyncio.create_task(heartbeat())
        try:
            done, _ = await asyncio.wait({execution, monitor}, return_when=asyncio.FIRST_COMPLETED)
            if monitor in done:
                await monitor
            return await execution
        finally:
            for pending in (execution, monitor):
                if not pending.done():
                    pending.cancel()
            await asyncio.gather(execution, monitor, return_exceptions=True)

    @staticmethod
    def _normalize_output(value: Any) -> dict[str, Any]:
        if isinstance(value, BackgroundTaskOutput):
            return value.model_dump(mode="json", exclude_none=True)
        if isinstance(value, BaseModel):
            value = value.model_dump(mode="json", exclude_none=True)
        if isinstance(value, str):
            text = value.strip()
            if text.startswith("```"):
                lines = text.splitlines()
                if lines and lines[0].startswith("```"):
                    lines = lines[1:]
                if lines and lines[-1].strip() == "```":
                    lines = lines[:-1]
                text = "\n".join(lines).strip()
            value = json.loads(text)
        if not isinstance(value, dict):
            raise ValueError("background task output must be a JSON object")
        try:
            validated = BackgroundTaskOutput.model_validate(value)
        except ValidationError as exc:
            known_fields = {"summary", "result", "artifact_draft", "artifact_type", "title", "payload"}
            logger.warning("Background output schema validation failed: %s", [
                (tuple(part if part in known_fields else "field" for part in error["loc"]), error["type"])
                for error in exc.errors(include_input=False, include_url=False)[:12]
            ])
            raise
        return validated.model_dump(mode="json", exclude_none=True)

    @staticmethod
    def _usage(result: Any) -> dict[str, Any]:
        usage = getattr(getattr(result, "context_wrapper", None), "usage", None)
        if usage is None:
            return {}
        return {
            "requests": int(getattr(usage, "requests", 0) or 0),
            "input_tokens": int(getattr(usage, "input_tokens", 0) or 0),
            "output_tokens": int(getattr(usage, "output_tokens", 0) or 0),
            "total_tokens": int(getattr(usage, "total_tokens", 0) or 0),
        }

    def _sdk_run_config(self):
        return privacy_safe_run_config(
            workflow_name="content-agent-background-task",
            tracing_enabled=self._settings.tracing_enabled,
            release_id=self._settings.release_id,
        )

    @staticmethod
    def _trace_ref(result: Any) -> str:
        responses = getattr(result, "raw_responses", None) or []
        if responses:
            return str(getattr(responses[-1], "response_id", "") or "")
        return ""

    @staticmethod
    def _error_code(exc: Exception) -> str:
        if isinstance(exc, InputGuardrailTripwireTriggered):
            return "AGENT_INPUT_GUARDRAIL_REJECTED"
        if isinstance(exc, OutputGuardrailTripwireTriggered):
            return "AGENT_OUTPUT_GUARDRAIL_REJECTED"
        if isinstance(exc, RuntimeCompatibilityError):
            return "AGENT_RUN_STATE_INVALID"
        if isinstance(exc, (AgentToolConfigurationError, UserError)):
            return "AGENT_TOOL_CONFIGURATION_INVALID"
        if isinstance(exc, BackendError) and "AGENT_TASK_CANCELLED" in str(exc):
            return "AGENT_TASK_CANCELLED"
        if isinstance(exc, (ValueError, ModelBehaviorError, json.JSONDecodeError)):
            return "OUTPUT_REPAIR_FAILED"
        return "PROVIDER_TEMPORARY_FAILURE"

    @staticmethod
    def _safe_error_detail(exc: Exception) -> str:
        return normalize_provider_error(exc)[:500]
