from __future__ import annotations

from .native_runner import bind_native_agent, native_run_config, settle_native_stream
from .memory_autocapture import capture_completed_memory

import asyncio
from copy import deepcopy
from contextlib import suppress
from contextvars import ContextVar
from collections.abc import Awaitable, Callable
import json
import logging
import os
from pathlib import Path
import re
from typing import Any

from agents import (
    Agent,
    AgentOutputSchemaBase,
    AsyncOpenAI,
    function_tool,
    InputGuardrailTripwireTriggered,
    ModelBehaviorError,
    ModelSettings,
    OpenAIResponsesModel,
    OutputGuardrailTripwireTriggered,
    Runner,
    RunState,
    UserError,
)
from jsonschema import Draft202012Validator
from openai import BadRequestError

from .backend import BackendClient, BackendError, backend_activity
from .agent_tools import AgentToolConfigurationError, AgentToolProvider, PreparedAgentTools, TOOL_APPROVAL_INSTRUCTIONS
from .config import Settings
from .execution_tools import skill_execution_tools
from .guardrails import SDKGuardrailPolicy
from .model_compat import compatible_chat_completions_model, is_routerhub_gemini_model
from .native_pause import unstarted_checkpoint
from .model_failure_boundary import MODEL_RECOVERY_REASON, ModelFailureBoundary
from .tool_outcomes import EXTERNAL_TOOL_RECOVERY_REASON, require_external_tool_pause
from .managed_instructions import with_managed_instructions
from .observability import privacy_safe_run_config
from .runtime import RuntimeCompatibilityError, normalize_provider_error, normalize_execution_stream_event
from .stateful_execution import ExecutionCheckpointFailed, ExecutionStateInvalid, ExecutionTaskPaused, StatefulExecution
from .video_tool import VideoAnalysisTool


logger = logging.getLogger(__name__)
_STATEFUL_PAUSE: ContextVar[asyncio.Event | None] = ContextVar("stateful_pause", default=None)
_STATEFUL_PROGRAM_PROGRESS: ContextVar[Callable[[str], Awaitable[None]] | None] = ContextVar("stateful_program_progress", default=None)

EXECUTOR_IDS = [
    "worker.structured_content",
    "workflow.novel_episode_split",
    "workflow.episode_cards",
    "workflow.shared_script_generation",
    "workflow.shared_script_quality_review",
    "workflow.non_novel_story_seed",
    "workflow.non_novel_series_blueprint",
    "workflow.video_script_extract",
]


class RuntimeJSONOutputSchema(AgentOutputSchemaBase):
    def __init__(self, schema: dict[str, Any]) -> None:
        self._schema = schema
        self._validator = Draft202012Validator(schema)

    def is_plain_text(self) -> bool:
        return False

    def name(self) -> str:
        return "runtime_contract"

    def json_schema(self) -> dict[str, Any]:
        return self._schema

    def is_strict_json_schema(self) -> bool:
        return False

    def validate_json(self, json_str: str) -> dict[str, Any]:
        try:
            payload = json.loads(json_str)
        except json.JSONDecodeError as exc:
            raise ModelBehaviorError("model output is not valid JSON") from exc
        if not isinstance(payload, dict):
            raise ModelBehaviorError("model output is not a JSON object")
        # The SDK schema is transport guidance. Runtime validation below remains
        # authoritative and can repair a partial provider result without losing it.
        return payload


class ExecutionLeaseLost(Exception):
    """The Worker cannot confirm ownership of the claimed execution."""


class ExecutionToolReplayRisk(Exception):
    """A fresh SDK run could duplicate a previously issued tool operation."""


class SDKTaskWorker:
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
        self._backend = backend or BackendClient(
            settings.backend_base_url,
            timeout_seconds=30,
            internal_token=settings.internal_token,
            ffmpeg_command=settings.ffmpeg_command,
            temp_dir=str(
                Path(settings.session_db_path).expanduser().resolve().parent
            ),
        )
        content_model_base_url = (
            settings.content_model_base_url or settings.model_base_url
        )
        content_model_api_key = settings.content_model_api_key or settings.model_api_key
        self._content_model_name = settings.content_model_name or settings.model_name
        content_model_timeout = (
            settings.content_model_timeout_seconds
            if settings.content_model_base_url
            else settings.model_timeout_seconds
        )
        content_model_retries = (
            settings.content_model_max_retries
            if settings.content_model_base_url
            else settings.model_max_retries
        )
        client = AsyncOpenAI(
            api_key=content_model_api_key,
            base_url=content_model_base_url,
            timeout=content_model_timeout,
            max_retries=content_model_retries,
        )
        self._model = (
            compatible_chat_completions_model(
                model=self._content_model_name,
                openai_client=client,
            )
            if settings.content_model_base_url
            else OpenAIResponsesModel(
                model=self._content_model_name,
                openai_client=client,
            )
        )
        self._max_tokens = min(
            (
                settings.content_model_max_output_tokens
                if settings.content_model_base_url
                else settings.model_max_output_tokens
            ),
            settings.orchestration_max_output_tokens,
        )
        self._worker_id = f"openai-agents-sdk-{os.getpid()}-slot-{slot}"
        self._task: asyncio.Task[None] | None = None
        self._video_tool: VideoAnalysisTool | None = None
        self._heartbeat_interval_seconds = min(10.0, settings.task_worker_lease_seconds / 3)
        hosted_client = AsyncOpenAI(api_key=settings.model_api_key, base_url=settings.model_base_url,
                                    timeout=settings.model_timeout_seconds, max_retries=0)
        self._tool_provider = AgentToolProvider(self._backend, skill_execution_tools(),
            hosted_model=OpenAIResponsesModel(model=settings.model_name, openai_client=hosted_client), hosted_client=hosted_client,
            subtask_model=lambda: self._model, guardrail_policy=SDKGuardrailPolicy.from_settings(settings),
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
        claim = await self._backend.claim_execution_task(
            worker_id=self._worker_id,
            executor_ids=EXECUTOR_IDS,
            provider_id="openai_agents_sdk",
            model_id=self._content_model_name,
            lease_seconds=self._settings.task_worker_lease_seconds,
        )
        if claim is None:
            return False
        attempt = claim.get("attempt", {})
        attempt_id = str(attempt.get("attempt_id", ""))
        try:
            received = await self._execute_with_liveness(claim)
            attempt_result = received.get("data", received)
            if attempt_result.get("status") in {"waiting_approval", "paused", "repair_pending"}:
                logger.info("SDK task checkpoint saved attempt_id=%s status=%s", attempt_id, attempt_result["status"])
                return True
            response_hash = str(attempt_result.get("response_hash") or "")
            if not response_hash:
                raise BackendError("runtime accepted result without response hash")
            try:
                committed = await self._backend.commit_execution_result(claim, response_hash)
            except BackendError as exc:
                if self._is_output_rejection(exc):
                    raise
                # The uploaded receipt survives an uncertain commit acknowledgement.
                # Its lease recovery must recommit/repair it, never regenerate it.
                raise ExecutionLeaseLost("Final commit acknowledgement is uncertain; retain the durable result") from exc
            commit_data = committed.get("data", committed)
            logger.info(
                "SDK task result handled attempt_id=%s status=%s",
                attempt_id,
                commit_data.get("commit_status", ""),
            )
        except ExecutionLeaseLost:
            logger.warning("SDK task stopped after losing execution lease attempt_id=%s", attempt_id)
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            error_code = self._error_code(exc)
            logger.warning(
                "SDK task failed attempt_id=%s code=%s error=%s",
                attempt_id,
                error_code,
                self._safe_error_detail(exc),
            )
            with suppress(BackendError):
                await self._backend.fail_execution_attempt(
                    claim,
                    error_code,
                    {
                        "error_code": error_code,
                        "stage": self._failure_stage(error_code),
                        "summary": self._user_summary(error_code),
                        "retryable": error_code == "PROVIDER_TEMPORARY_FAILURE",
                        "technical_detail": self._safe_error_detail(exc),
                    },
                )
        return True

    async def _execute_with_liveness(self, claim: dict[str, Any]) -> dict[str, Any]:
        pause_signal = asyncio.Event()
        async def renew(program_status: str = "") -> dict[str, Any]:
            try:
                response = await asyncio.wait_for(
                    self._backend.heartbeat_execution_attempt(
                        claim, lease_seconds=self._settings.task_worker_lease_seconds,
                        **({"program_status": program_status} if program_status else {}),
                    ),
                    timeout=self._heartbeat_interval_seconds,
                )
                receipt = response.get("data", response)
                attempt = claim["attempt"]
                if (not isinstance(receipt, dict) or receipt.get("attempt_id") != attempt["attempt_id"]
                    or receipt.get("input_snapshot_hash") != attempt["input_snapshot_hash"]
                    or receipt.get("status") not in {"running", "result_received", "waiting_approval", "paused", "repair_pending"}
                    or type(receipt.get("pause_requested", False)) is not bool):
                    raise ValueError("Execution lease acknowledgement does not match claim")
                if receipt.get("pause_requested"):
                    pause_signal.set()
                if program_status and receipt["status"] == "running" and receipt.get("program_status") != program_status:
                    raise ValueError("Program progress acknowledgement does not match publication")
                return receipt
            except Exception as exc:
                raise ExecutionLeaseLost("Cannot renew execution lease") from exc

        async def heartbeat() -> None:
            while True:
                await asyncio.sleep(self._heartbeat_interval_seconds)
                await renew()

        async def publish_program(status: str) -> None:
            await renew(status)

        receipt = await renew()
        if receipt["status"] in {"result_received", "waiting_approval", "paused", "repair_pending"}:
            return {"data": receipt}
        token = _STATEFUL_PAUSE.set(pause_signal)
        progress_token = _STATEFUL_PROGRAM_PROGRESS.set(publish_program)
        try:
            execution = asyncio.create_task(self._execute_and_submit(claim))
        finally:
            _STATEFUL_PAUSE.reset(token)
            _STATEFUL_PROGRAM_PROGRESS.reset(progress_token)
        monitor = asyncio.create_task(heartbeat())
        try:
            done, _ = await asyncio.wait({execution, monitor}, return_when=asyncio.FIRST_COMPLETED)
            if monitor in done:
                await monitor
            return await execution
        except BackendError:
            receipt = await renew()
            if receipt["status"] in {"result_received", "repair_pending"}:
                return {"data": receipt}
            raise
        finally:
            for pending in (execution, monitor):
                if not pending.done():
                    pending.cancel()
            await asyncio.gather(execution, monitor, return_exceptions=True)

    async def _execute_and_submit(self, claim: dict[str, Any]) -> dict[str, Any]:
        try:
            return await self._execute_claim(claim)
        except ExecutionTaskPaused as pause:
            checkpoint_statuses = {"paused", "waiting_approval"} if pause.user_pause else {"waiting_approval"}
            try:
                save = self._backend.pause_execution_at_native_boundary if pause.user_pause else self._backend.pause_execution_for_approval
                receipt = await save(claim, run_state=pause.run_state,
                    schema_version=pause.schema_version, pending_sdk_tool_call_ids=pause.pending_ids, worker_state=pause.worker_state,
                    **({"recovery_reason": pause.recovery_reason} if pause.recovery_reason else {}))
            except BackendError:
                # A lost HTTP acknowledgement must not turn a durable pause into
                # provider failure. The rotated token also protects a fast resume.
                try:
                    receipt = await self._backend.heartbeat_execution_attempt(claim, lease_seconds=self._settings.task_worker_lease_seconds)
                except BackendError as exc:
                    raise ExecutionLeaseLost("Cannot confirm checkpoint ownership") from exc
                data = receipt.get("data", receipt) if isinstance(receipt, dict) else None
                if not isinstance(data, dict):
                    raise ExecutionLeaseLost("Checkpoint acknowledgement is invalid")
                if data.get("status") not in checkpoint_statuses:
                    raise
            data = receipt.get("data", receipt) if isinstance(receipt, dict) else None
            if (not isinstance(data, dict) or data.get("status") not in checkpoint_statuses or data.get("attempt_id") != claim["attempt"]["attempt_id"]
                or data.get("input_snapshot_hash") != claim["attempt"]["input_snapshot_hash"]):
                raise ExecutionLeaseLost("Checkpoint acknowledgement does not match execution")
            return receipt

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
                logger.error("SDK task worker loop failed: %s", exc)
                await asyncio.sleep(max(idle_seconds, 2.0))

    async def _execute_claim(
        self, claim: dict[str, Any]
    ) -> dict[str, Any]:
        pack, attempt = claim.get("context_pack") or {}, claim.get("attempt") or {}
        with backend_activity(str(pack.get("project_id") or ""), execution_attempt_id=str(attempt.get("attempt_id") or ""),
                              attempt_token=str(claim.get("attempt_token") or "")):
            execution = await self._prepare_execution(claim)
            if execution.phase == "repair_backend":
                payload, usage, trace_ref = await self._resume_repair(pack, execution)
            else:
                payload, usage, trace_ref = await self._execute_generation(claim, execution=execution)
                try:
                    return await self._backend.submit_execution_result(claim, payload, usage, trace_ref)
                except BackendError as exc:
                    if not self._is_output_rejection(exc):
                        raise
                    payload, usage, trace_ref = await self._repair_output(pack, json.dumps(payload, ensure_ascii=False),
                        ValueError(self._safe_error_detail(exc)), usage, trace_ref, phase="repair_backend",
                        execution=None if claim.get("executor_id") == "workflow.video_script_extract" else execution)
            payload = self._normalize_story_bible_coverage(payload, pack)
            payload = self._parse_and_validate(json.dumps(payload, ensure_ascii=False), pack)
            payload = self._provider_envelope(payload, pack)
            return await self._backend.submit_execution_result(claim, payload, usage, trace_ref)

    async def _prepare_execution(self, claim: dict[str, Any]) -> StatefulExecution:
        pack = claim.get("context_pack") or {}
        try:
            execution = await StatefulExecution.create(claim, self._backend)
            execution.context.memory_archive_queue = self._archive_queue
            if claim.get("executor_id") == "workflow.video_script_extract" and execution.resume is not None:
                raise ValueError("A video-provider task cannot resume a text SDK checkpoint")
            source_units = self._source_analysis_units(pack)
            completed = execution.completed_batches
            batch_count = (len(source_units) + 39) // 40
            if completed and (len(source_units) <= 40 or len(completed) > batch_count
                              or (len(completed) == batch_count) != (execution.phase == "repair_backend")):
                raise ValueError("Stateful checkpoint source batch cursor is invalid")
            if execution.phase == "repair_backend" and execution.repair_origin is None and len(source_units) > 40 and len(completed) != batch_count:
                raise ValueError("Backend repair is missing completed source batches")
            for index, batch in enumerate(completed):
                self._normalize_source_analysis_chunk(batch["payload"], source_units[index * 40:(index + 1) * 40])
            allowed = {str(item.get("artifact_version_id") or "") for item in self._context_catalog(pack)}
            if not execution.context_reads.issubset(allowed):
                raise ValueError("Stateful checkpoint context reads are not in the frozen catalog")
        except (ValueError, UserError, KeyError, TypeError, RuntimeCompatibilityError) as exc:
            raise ExecutionStateInvalid("Stateful execution identity, frozen inputs or checkpoint are invalid") from exc
        return execution

    async def _execute_generation(
        self, claim: dict[str, Any], *, execution: StatefulExecution | None = None,
    ) -> tuple[dict[str, Any], dict[str, Any], str]:
        pack = claim.get("context_pack")
        if not isinstance(pack, dict):
            raise ValueError("context pack is missing")
        source_units = self._source_analysis_units(pack)
        if (
            not pack.get("_sdk_source_analysis_chunk")
            and len(source_units) > 40
        ):
            return await self._execute_source_analysis_batches(
                claim, pack, source_units, execution=execution,
            )
        instructions, input_items = "", []
        if execution is None or execution.phase == "generate":
            instructions, input_items = await self._render_input(pack)
        context_reads: set[str] = execution.context_reads if execution is not None else set()
        has_upstream_artifacts = bool(self._context_catalog(pack))
        context_tools = (
            self._context_tools(pack, context_reads) if has_upstream_artifacts else []
        )
        is_video_task = str(claim.get("executor_id") or "") == "workflow.video_script_extract"
        payload: dict[str, Any] | None = None
        result: Any | None = None
        attribution_result: Any | None = None
        usage: dict[str, Any] = {}
        trace_ref = ""
        validation_repaired = execution is not None and execution.phase == "repair_validate"
        if execution is not None and execution.phase in {"repair_parse", "repair_validate"}:
            payload, usage, trace_ref = await self._resume_repair(pack, execution)
        if is_video_task:
            if self._video_tool is None:
                self._video_tool = VideoAnalysisTool(self._settings, self._backend)
            video_tool = self._video_tool
            provider_contract = pack.get("provider_result_contract")
            contracts = (
                [provider_contract]
                if isinstance(provider_contract, dict)
                else pack.get("output_contracts") or [pack.get("output_contract")]
            )
            video_analysis_instructions = (
                "When SUBTITLE_TIMELINE_EVIDENCE is present, return compact visual evidence as "
                "{\"video_evidence\":{\"title\":\"\",\"plot_summary\":\"\","
                "\"continuity_delta\":{\"new_facts\":[],\"character_state_changes\":[],"
                "\"relationship_changes\":[],\"hooks_opened\":[],\"hooks_resolved\":[]},"
                "\"scenes\":[{\"scene_id\":\"scene_1_1\",\"heading\":\"场1-1 地点 日 外\","
                "\"location\":\"\",\"interior_exterior\":\"外\",\"time_of_day\":\"日\","
                "\"time_range\":{\"start_ms\":0,\"end_ms\":10000},\"characters\":[],"
                "\"actions\":[{\"text\":\"人物执行可见动作。\",\"time_range\":{\"start_ms\":0,"
                "\"end_ms\":1000}}]}],\"dialogue_annotations\":[{\"subtitle_id\":"
                "\"subtitle_0001\",\"speaker\":\"角色名\",\"delivery\":\"冷声\"}],"
                "\"uncertainty_flags\":[],\"extraction_completeness\":{\"status\":"
                "\"complete_first_pass\",\"known_gaps\":[],\"review_notes\":[]}}}. "
                "Do not repeat subtitle text. Return exactly one dialogue annotation for every "
                "subtitle_id. Every scene must contain at least one visible, evidence-grounded action; "
                "add actions at meaningful position, expression, prop, relationship, or event-result "
                "changes. Use delivery only for audible or visible key performance changes, never as "
                "a quota or on every line. Scene ranges must be chronological, contiguous, and cover "
                "the complete source duration. When subtitle evidence is unavailable, return the final "
                "video_script_unit JSON object required by the Runtime Output Contract below. Cover "
                "the complete video duration in chronological scenes with visible actions and every "
                "readable or audible dialogue line as a separate block. "
                "All human-readable content and every character or speaker label must use Simplified "
                "Chinese. Preserve names shown or spoken in Chinese exactly; never translate or "
                "transliterate them to English. Use Chinese role labels such as 工人甲 for unnamed "
                "people. When a social role is visible or clear from dialogue, use a contextual "
                "Chinese label such as 学校负责人、家长甲 or 顾客己 instead of 未知人物. "
                "Narrative exposition must use 旁白 or 画外音. Mark a line as 未知人物 only when "
                "even the speaker's role cannot be supported by audio, visuals, or context. Do not merge "
                "or omit dialogue to keep the output compact. Return JSON only.\n\n"
                "RUNTIME_OUTPUT_CONTRACT:\n"
                + json.dumps([item for item in contracts if isinstance(item, dict)], ensure_ascii=False)
            )
            validation_errors: list[str] = []
            for provider_attempt in range(2):
                authoritative_video_result = await video_tool.analyze(
                    pack, video_analysis_instructions
                )
                try:
                    direct = self._parse_json_object(authoritative_video_result)
                    direct = self._unwrap_provider_result_envelope(direct, pack)
                    if getattr(video_tool, "subtitle_timeline", None):
                        direct = self._assemble_video_evidence_payload(
                            direct,
                            pack,
                            video_tool.subtitle_timeline,
                            int(getattr(video_tool, "source_duration_ms", 0) or 0),
                        )
                    direct = self._normalize_video_script_payload(direct, pack)
                    self._assert_video_output_quality(
                        direct,
                        int(getattr(video_tool, "source_duration_ms", 0) or 0),
                    )
                    direct = self._parse_and_validate(
                        json.dumps(direct, ensure_ascii=False), pack
                    )
                    try:
                        direct, attribution_result = await self._attribute_video_dialogue_speakers(
                            direct, pack
                        )
                    except Exception as exc:
                        logger.warning(
                            "optional video speaker attribution failed; preserving provider labels: %s",
                            self._safe_error_detail(exc),
                        )
                        attribution_result = None
                    direct = self._normalize_video_script_payload(direct, pack)
                    self._assert_video_output_quality(
                        direct,
                        int(getattr(video_tool, "source_duration_ms", 0) or 0),
                        require_speaker_resolution=True,
                    )
                    payload = self._parse_and_validate(
                        json.dumps(direct, ensure_ascii=False), pack
                    )
                    break
                except ValueError as exc:
                    validation_errors.append(self._safe_error_detail(exc))
                    video_tool.discard_cached(str(pack.get("context_hash") or ""))
                    if provider_attempt == 0:
                        logger.warning(
                            "video provider result failed validation; requesting one fresh result: %s",
                            validation_errors[-1],
                        )
                        continue
                    raise ValueError(
                        "video provider result failed the final artifact contract after retry: "
                        + validation_errors[-1]
                    ) from exc
        if payload is None:
            prepared = (await self._tool_provider.prepare(execution.context, list(execution.context.loaded_capabilities.values()),
                        set(execution.context.routed_capabilities)) if execution is not None else PreparedAgentTools(tools=[], mcp_servers=[]))
            output_type = self._runtime_output_schema(pack)
            agent = Agent(
                name="内容生产 Skill SDK 执行器",
                instructions=(
                    instructions
                    + (
                        "\n\n# SDK Context Tools\n"
                        "输入只包含 Go Runtime 锁定的上游版本目录，不包含上游正文。"
                        "生成前必须调用 read_context_artifacts 读取完成当前任务所需的权威版本；"
                        "剧本逐集生成时，sdk_context_candidate 是 Go 冻结的可选权威版本目录；"
                        "你必须读取紧邻上一集的 script_handoff 和 script_unit，并可按当前任务需要"
                        "自主读取更早版本，不得凭会话记忆猜测前集实际内容。"
                        if has_upstream_artifacts
                        else ""
                    )
                    + "\nUse tools for project materials, Skill resources and working files. Only report saved files or downloads returned by tools. Keep the workflow's frozen inputs and output contract authoritative.\n"
                    + TOOL_APPROVAL_INSTRUCTIONS
                ),
                model=self._model,
                tools=[*context_tools, *prepared.tools],
                mcp_servers=prepared.mcp_servers,
                model_settings=ModelSettings(
                    max_tokens=self._max_tokens,
                    tool_choice="required" if has_upstream_artifacts else None,
                ),
                reset_tool_choice=True,
                output_type=output_type,
            )
            def json_agent() -> Agent:
                return agent.clone(name="内容生产 Skill SDK 兼容执行器", output_type=None,
                    instructions=str(agent.instructions) + "\n\n只输出一个满足 Runtime Output Contract 的 JSON 对象。")

            async with prepared:
                routerhub_json = is_routerhub_gemini_model(
                    str(getattr(self, "_content_model_name", "") or "")
                )
                if execution is not None and routerhub_json:
                    execution.output_mode = "json"
                active_agent = (
                    json_agent()
                    if routerhub_json
                    or (execution is not None and execution.output_mode == "json")
                    else agent
                )
                try:
                    result = await self._run_streamed(active_agent, input_items, self._settings.run_timeout_seconds, execution=execution)
                except (ModelBehaviorError, BadRequestError) as exc:
                    if active_agent.output_type is None:
                        raise
                    if execution is not None and (execution.restored or execution.context.active_tool_calls):
                        raise ExecutionToolReplayRisk("SDK output compatibility retry would replay issued tools") from exc
                    logger.warning("SDK structured output transport rejected; retrying with plain JSON text: %s", exc)
                    if execution is not None:
                        execution.output_mode = "json"
                    result = await self._run_streamed(json_agent(), input_items, self._settings.run_timeout_seconds, execution=execution)
            usage, trace_ref = self._usage(result), self._trace_ref(result)
            output = result.final_output
            if isinstance(output, dict):
                payload = self._unwrap_provider_result_envelope(output, pack)
            elif isinstance(output, str):
                try:
                    payload = self._parse_and_validate(output, pack)
                except ValueError as primary_error:
                    payload, usage, trace_ref = await self._repair_output(pack, output, primary_error,
                        usage, trace_ref, phase="repair_parse", execution=execution)
            else:
                raise ValueError("Agents SDK task returned unsupported output")
        payload = self._normalize_story_bible_coverage(payload, pack)
        try:
            payload = self._normalize_video_script_payload(payload, pack)
            if is_video_task:
                self._assert_video_output_quality(
                    payload,
                    int(getattr(self._video_tool, "source_duration_ms", 0) or 0),
                    require_speaker_resolution=True,
                )
            payload = self._parse_and_validate(
                json.dumps(payload, ensure_ascii=False), pack
            )
        except ValueError as validation_error:
            if is_video_task:
                self._video_tool.discard_cached(str(pack.get("context_hash") or ""))
                raise ValueError(
                    "final video artifact failed validation: "
                    + self._safe_error_detail(validation_error)
                ) from validation_error
            if validation_repaired:
                raise
            payload, usage, trace_ref = await self._repair_output(pack, json.dumps(payload, ensure_ascii=False),
                validation_error, usage, trace_ref, phase="repair_validate", execution=execution)
            payload = self._normalize_story_bible_coverage(payload, pack)
            payload = self._normalize_video_script_payload(payload, pack)
            payload = self._parse_and_validate(
                json.dumps(payload, ensure_ascii=False), pack
            )
        payload = self._provider_envelope(payload, pack)
        if attribution_result is not None:
            attribution_results = (
                attribution_result
                if isinstance(attribution_result, list)
                else [attribution_result]
            )
            for item in attribution_results:
                usage = self._merge_usage(usage, self._usage(item))
        if is_video_task and self._video_tool is not None:
            usage["video_provider"] = self._video_tool.usage
        usage["context_artifact_version_ids"] = sorted(context_reads)
        trace_ref = (
            self._video_tool.trace_ref
            if is_video_task and self._video_tool is not None and self._video_tool.trace_ref
            else trace_ref
        )
        return payload, usage, trace_ref

    @staticmethod
    def _source_analysis_units(pack: dict[str, Any]) -> list[dict[str, Any]]:
        cursor = pack.get("task_cursor")
        batch = cursor.get("batch") if isinstance(cursor, dict) else None
        if not isinstance(batch, dict) or batch.get("phase") != "source_analysis":
            return []
        return [item for item in batch.get("source_units") or [] if isinstance(item, dict)]

    async def _execute_source_analysis_batches(
        self,
        claim: dict[str, Any],
        pack: dict[str, Any],
        source_units: list[dict[str, Any]],
        *, execution: StatefulExecution | None = None,
    ) -> tuple[dict[str, Any], dict[str, Any], str]:
        merged_units: list[dict[str, Any]] = []
        merged_trace = {"grounded": [], "inferred": [], "claims": []}
        merged_usage: dict[str, Any] = {}
        trace_ref = ""
        for start in range(0, len(source_units), 40):
            expected = source_units[start : start + 40]
            if execution is not None and start // 40 < len(execution.completed_batches):
                completed = execution.completed_batches[start // 40]
                payload, usage, chunk_trace_ref = completed["payload"], completed["usage"], completed["trace_ref"]
            else:
                chunk_pack = deepcopy(pack)
                chunk_pack["_sdk_source_analysis_chunk"] = True
                chunk_pack["task_cursor"]["batch"]["source_units"] = expected
                chunk_claim = dict(claim)
                chunk_claim["context_pack"] = chunk_pack
                payload, usage, chunk_trace_ref = await self._execute_generation(chunk_claim, execution=execution)
                payload = self._normalize_source_analysis_chunk(payload, expected)
                if execution is not None:
                    execution.complete_batch(payload, usage, chunk_trace_ref)
            payload = self._normalize_source_analysis_chunk(payload, expected)
            merged_units.extend(payload["units"])
            source_trace = payload.get("source_trace") or {}
            for key in merged_trace:
                values = source_trace.get(key) if isinstance(source_trace, dict) else None
                if isinstance(values, list):
                    merged_trace[key].extend(values)
            merged_usage = self._merge_usage(merged_usage, usage)
            if chunk_trace_ref:
                trace_ref = chunk_trace_ref

        merged = {
            "source_kind": "novel",
            "units": merged_units,
            "coverage_check": {
                "covered_source_unit_ids": [
                    str(item.get("source_unit_id") or "") for item in source_units
                ],
                "missing_source_unit_ids": [],
                "order_issues": [],
            },
            "source_trace": merged_trace,
        }
        merged = self._parse_and_validate(
            json.dumps(merged, ensure_ascii=False), pack
        )
        return self._provider_envelope(merged, pack), merged_usage, trace_ref

    @staticmethod
    def _normalize_source_analysis_chunk(
        payload: dict[str, Any], expected: list[dict[str, Any]]
    ) -> dict[str, Any]:
        units = payload.get("units")
        if not isinstance(units, list):
            raise ValueError("source analysis chunk units are missing")
        by_id = {
            str(item.get("source_unit_id") or ""): item
            for item in units
            if isinstance(item, dict) and item.get("source_unit_id")
        }
        expected_ids = [str(item.get("source_unit_id") or "") for item in expected]
        if len(units) != len(expected) or set(by_id) != set(expected_ids):
            raise ValueError(
                "source analysis chunk coverage mismatch: "
                f"expected={len(expected)}, actual={len(units)}"
            )
        normalized: list[dict[str, Any]] = []
        for source in expected:
            source_unit_id = str(source.get("source_unit_id") or "")
            unit = dict(by_id[source_unit_id])
            existing_refs = unit.get("source_refs") or []
            evidence_excerpt = next(
                (
                    str(ref.get("evidence_excerpt") or "")
                    for ref in existing_refs
                    if isinstance(ref, dict) and ref.get("evidence_excerpt")
                ),
                "",
            )
            reference = {
                "source_type": "asset_text_range",
                "asset_id": str(source.get("asset_id") or ""),
                "asset_snapshot_id": str(source.get("asset_snapshot_id") or ""),
                "source_unit_id": source_unit_id,
                "range_label": source_unit_id,
            }
            if evidence_excerpt:
                reference["evidence_excerpt"] = evidence_excerpt
            unit["source_refs"] = [reference]
            normalized.append(unit)
        result = dict(payload)
        result["source_kind"] = "novel"
        result["units"] = normalized
        result["coverage_check"] = {
            "covered_source_unit_ids": expected_ids,
            "missing_source_unit_ids": [],
            "order_issues": [],
        }
        return result

    @staticmethod
    def _context_catalog(pack: dict[str, Any]) -> list[dict[str, Any]]:
        catalog: list[dict[str, Any]] = []
        for item in pack.get("upstream_context") or []:
            if not isinstance(item, dict):
                continue
            catalog.append(
                {
                    key: value
                    for key, value in item.items()
                    if key != "content"
                }
            )
        return catalog

    @staticmethod
    def _context_tools(
        pack: dict[str, Any], context_reads: set[str] | None = None
    ) -> list[Any]:
        pinned = {
            str(item.get("artifact_version_id") or ""): item
            for item in pack.get("upstream_context") or []
            if isinstance(item, dict) and item.get("artifact_version_id")
        }

        @function_tool
        async def read_context_artifacts(
            artifact_version_ids: list[str],
        ) -> str:
            """Read exact Go-pinned upstream artifact versions needed for this Skill task."""
            if not artifact_version_ids:
                return json.dumps(
                    {"error": "artifact_version_ids is required"},
                    ensure_ascii=False,
                )
            unknown = [item for item in artifact_version_ids if item not in pinned]
            if unknown:
                return json.dumps(
                    {"error": "unavailable_context_version", "version_ids": unknown},
                    ensure_ascii=False,
                )
            if context_reads is not None:
                context_reads.update(artifact_version_ids)
            return json.dumps(
                {"items": [pinned[item] for item in artifact_version_ids]},
                ensure_ascii=False,
            )

        return [read_context_artifacts]

    async def _run_streamed(
        self,
        agent: Agent[Any],
        input_items: Any,
        timeout_seconds: int,
        *, execution: StatefulExecution | None = None,
        max_turns: int = 16,
        workflow_name: str = "content-agent-stateful-task",
        resumed_input: RunState | None = None,
    ) -> Any:
        if execution is not None and resumed_input is None:
            agent = agent.clone(instructions=with_managed_instructions(agent.instructions, execution.context))
        protected = SDKGuardrailPolicy.from_settings(self._settings).protect(agent)
        if execution is not None and execution.repair_origin is None:
            protected = await bind_native_agent(execution.context, self._settings, protected)
        runner_input = execution.inputs.stage(resumed_input, resumed=True) if resumed_input is not None else await execution.runner_input(protected, input_items) if execution is not None else input_items
        context = execution.context if execution is not None else None
        async with native_run_config(context, self._sdk_run_config(workflow_name), runner_input) as config:
            result = await self._run_streamed_once(protected, runner_input, timeout_seconds, execution=execution,
                                                   max_turns=max_turns, run_config=config)
        signal = _STATEFUL_PAUSE.get() if execution is not None else None
        deferred_inputs = execution is not None and len(execution.inputs.stage_ids) < len(execution.inputs.ids)
        if deferred_inputs and result.final_output is None and not result.interruptions and not (signal is not None and signal.is_set()):
            return await self._run_streamed(agent, input_items, timeout_seconds, execution=execution,
                                           max_turns=max_turns, workflow_name=workflow_name, resumed_input=result.to_state())
        if execution is not None:
            execution.finish_generation(result, user_pause=signal is not None and signal.is_set())
        await capture_completed_memory(context, result)
        return result

    async def _run_streamed_once(self, protected, runner_input, timeout_seconds, *, execution, max_turns, run_config):
        deferred_inputs = execution is not None and len(execution.inputs.stage_ids) < len(execution.inputs.ids)
        if execution is not None:
            execution.unstarted_input = None if isinstance(runner_input, RunState) else deepcopy(runner_input)
        def on_model_start() -> None:
            if execution is not None:
                execution.unstarted_input = None
            if deferred_inputs:
                result.cancel(mode="after_turn")
        boundary = ModelFailureBoundary(on_model_start, on_external_review=lambda: result.cancel(mode="after_turn"))
        result = Runner.run_streamed(
            protected,
            runner_input,
            context=execution.context if execution is not None else None,
            max_turns=max_turns,
            run_config=run_config,
            hooks=boundary,
        )
        signal = _STATEFUL_PAUSE.get() if execution is not None else None
        model_started = asyncio.Event()
        model_responded = False
        async def pause_after_turn() -> None:
            await signal.wait()
            if not isinstance(runner_input, RunState):
                await model_started.wait()
            result.cancel(mode="after_turn")
        watcher = None
        if signal is not None:
            if signal.is_set():
                result.cancel(mode="after_turn")
            else:
                watcher = asyncio.create_task(pause_after_turn())
        recovery_error = None
        cancelled = False
        try:
            async with asyncio.timeout(timeout_seconds):
                async for event in result.stream_events():
                    report = _STATEFUL_PROGRAM_PROGRESS.get() if execution is not None else None
                    if report is not None:
                        normalized = normalize_execution_stream_event(event)
                        if normalized is not None and normalized.get("data", {}).get("tool_name") == "programmatic_tool_calling":
                            phase = "running" if normalized["event"] == "agent.tool.started" else normalized["data"].get("status")
                            if phase in {"running", "completed", "incomplete"}:
                                await report(phase)
                    if event.type == "raw_response_event":
                        model_started.set()
                        if execution is not None:
                            execution.unstarted_input = None
                        if getattr(event.data, "type", "") == "response.completed":
                            model_responded = True
        except TimeoutError as exc:
            result.cancel()
            cancelled = True
            raise RuntimeError("Agents SDK task run timed out") from exc
        except BaseException as exc:
            if execution is not None and boundary.permits_checkpoint(exc, result):
                recovery_error = exc
            else:
                result.cancel()
                cancelled = True
                raise
        finally:
            if watcher is not None:
                watcher.cancel()
                await asyncio.gather(watcher, return_exceptions=True)
            await settle_native_stream(execution.context if execution is not None else None, result, cancelled=cancelled)
            if execution is not None and model_responded and not result.to_state().pending_input:
                execution.inputs.model_responded()
                if execution.inputs.included:
                    try:
                        await execution.context.backend.record_execution_inputs_included(
                            execution.identity["attempt_id"], execution.context.attempt_token,
                            execution.identity["input_snapshot_hash"], execution.inputs.included)
                    except BackendError as exc:
                        raise ExecutionCheckpointFailed("Cannot persist stateful model input receipt") from exc
        if boundary.external_review:
            require_external_tool_pause(result)
            if execution is None:
                raise ExecutionCheckpointFailed("External outcome review has no stateful execution identity")
            raise ExecutionTaskPaused(result, execution.checkpoint(), user_pause=True,
                                      recovery_reason=EXTERNAL_TOOL_RECOVERY_REASON)
        if recovery_error is not None:
            raise ExecutionTaskPaused(result, execution.checkpoint(), user_pause=True,
                                      recovery_reason=MODEL_RECOVERY_REASON) from recovery_error
        if deferred_inputs and result.final_output is None and not result.interruptions and not (signal is not None and signal.is_set()):
            if not model_responded:
                raise ExecutionCheckpointFailed("Native model recovery did not reach an input admission boundary")
        return result

    async def _render_input(
        self, pack: dict[str, Any]
    ) -> tuple[str, list[dict[str, Any]]]:
        prompt = pack.get("prompt") or {}
        rules = pack.get("rules") or []
        contracts = pack.get("output_contracts") or []
        provider_contract = pack.get("provider_result_contract")
        if isinstance(provider_contract, dict):
            contracts = [provider_contract]
        elif not contracts and isinstance(pack.get("output_contract"), dict):
            contracts = [pack["output_contract"]]
        instructions = str(prompt.get("content") or "")
        skill = pack.get("skill_instructions")
        if isinstance(skill, dict) and str(skill.get("content") or "").strip():
            instructions = (
                str(skill.get("content") or "")
                + "\n\n# Workflow Prompt\n"
                + instructions
            )
        for rule in rules:
            if isinstance(rule, dict):
                instructions += (
                    "\n\n# Rule Module: "
                    + str(rule.get("id") or "")
                    + "\n"
                    + str(rule.get("content") or "")
                )
        instructions += (
            "\n\n# Runtime Output Contract\n"
            "只输出一个 JSON 对象，不输出 Markdown、解释或下一步决策。\n"
            + json.dumps(contracts, ensure_ascii=False)
        )
        user_payload = {
            "project_id": pack.get("project_id"),
            "operation": (pack.get("intent") or {}).get("operation"),
            "target": pack.get("target"),
            "run_input": pack.get("run_input_snapshot"),
            "source_assets": pack.get("asset_context") or [],
            "upstream_context_catalog": self._context_catalog(pack),
            "structured_run_state": pack.get("structured_run_state"),
            "state_adjacent_context": [
                item
                for item in pack.get("upstream_context") or []
                if isinstance(item, dict)
                and item.get("selection_policy") == "state_adjacent"
            ],
            "decision_snapshots": pack.get("decision_snapshots") or [],
            "config_snapshot": pack.get("config_snapshot"),
            "task_cursor": pack.get("task_cursor"),
            "context_hash": pack.get("context_hash"),
        }
        content: list[dict[str, Any]] = [
            {
                "type": "input_text",
                "text": json.dumps(user_payload, ensure_ascii=False),
            }
        ]
        for asset in pack.get("asset_context") or []:
            if not isinstance(asset, dict):
                continue
            kind = str(asset.get("kind") or "")
            asset_id = str(asset.get("asset_id") or "")
            snapshot_id = str(asset.get("asset_snapshot_id") or "")
            if kind == "image":
                data_url = await self._backend.get_image_data_url(asset_id, snapshot_id)
                if data_url:
                    content.append(
                        {"type": "input_image", "image_url": data_url, "detail": "auto"}
                    )
        return instructions, [{"role": "user", "content": content}]

    async def _repair_output(self, pack: dict[str, Any], output: str, error: ValueError,
                             usage: dict[str, Any], trace_ref: str, *, phase: str,
                             execution: StatefulExecution | None) -> tuple[dict[str, Any], dict[str, Any], str]:
        if execution is not None:
            execution.begin_repair(phase, pack, output, error, usage, trace_ref)
            return await self._resume_repair(pack, execution)
        payload, result = await self._repair(pack, output, error)
        return payload, self._merge_usage(usage, self._usage(result)), self._trace_ref(result) or trace_ref

    async def _resume_repair(self, pack: dict[str, Any], execution: StatefulExecution) -> tuple[dict[str, Any], dict[str, Any], str]:
        saved = execution.repair_candidate(pack)
        payload, result = await self._repair(pack, saved["candidate"], ValueError(saved["error"]), execution=execution)
        saved = execution.repair_candidate(pack)
        usage = self._merge_usage(saved["usage"], self._usage(result))
        trace_ref = self._trace_ref(result) or saved["trace_ref"]
        execution.finish_repair()
        return payload, usage, trace_ref

    async def _repair(
        self, pack: dict[str, Any], output: str, error: ValueError,
        *, execution: StatefulExecution | None = None,
    ) -> tuple[dict[str, Any], Any]:
        contracts = pack.get("output_contracts") or []
        provider_contract = pack.get("provider_result_contract")
        if isinstance(provider_contract, dict):
            contracts = [provider_contract]
        elif not contracts and isinstance(pack.get("output_contract"), dict):
            contracts = [pack["output_contract"]]
        repair_instructions = (
            "修复候选输出，使其成为满足给定 JSON Schema 的一个 JSON 对象。"
            "只输出 JSON，不得解释。"
            + (
                "最终使用 {\"artifact\": {...}} 外层包装，artifact 的值必须满足 Schema。"
                if self._requires_artifact_envelope(pack)
                else "直接输出满足 Schema 的裸 JSON 对象，不得添加 artifact 外层。"
            )
            + "Schema："
            + json.dumps(contracts, ensure_ascii=False)
        )
        agent = Agent(
            name="内容生产 Skill SDK 输出修复器",
            instructions=repair_instructions,
            model=self._model,
            model_settings=ModelSettings(max_tokens=self._max_tokens),
            output_type=self._runtime_output_schema(pack),
        )
        repair_input = json.dumps(
            {"validation_error": str(error), "candidate": output},
            ensure_ascii=False,
        )
        def compatible_agent() -> Agent:
            return Agent(
                name="内容生产 Skill SDK 输出兼容修复器",
                instructions=(
                    repair_instructions
                    + "\n兼容重试：不要复制 Runtime 契约元数据，只输出产物 JSON。"
                ),
                model=self._model,
                model_settings=ModelSettings(max_tokens=self._max_tokens),
            )
        active_agent = compatible_agent() if execution is not None and execution.output_mode == "json" else agent
        try:
            result = await self._run_streamed(active_agent, repair_input, self._settings.run_timeout_seconds,
                execution=execution, max_turns=2, workflow_name="content-agent-task-output-repair")
        except (ModelBehaviorError, BadRequestError) as exc:
            if active_agent.output_type is None:
                raise
            if execution is not None and execution.resume is not None and not unstarted_checkpoint(execution.resume["run_state"]):
                raise ExecutionToolReplayRisk("SDK repair compatibility retry would discard an executed checkpoint") from exc
            logger.warning("SDK repair schema rejected; retrying with JSON object transport: %s", exc)
            if execution is not None:
                details = getattr(exc, "run_data", None)
                execution.repair["usage"] = self._merge_usage(execution.repair["usage"], self._usage(details))
                execution.repair["trace_ref"] = self._trace_ref(details) or execution.repair["trace_ref"]
                execution.resume = None
                execution.output_mode = "json"
            result = await self._run_streamed(compatible_agent(), repair_input, self._settings.run_timeout_seconds,
                execution=execution, max_turns=2, workflow_name="content-agent-task-output-repair")
        if isinstance(result.final_output, dict):
            return self._parse_and_validate(
                json.dumps(result.final_output, ensure_ascii=False), pack
            ), result
        if isinstance(result.final_output, str):
            return self._parse_and_validate(result.final_output, pack), result
        raise ValueError("Agents SDK repair returned unsupported output")

    @staticmethod
    def _runtime_output_schema(pack: dict[str, Any]) -> RuntimeJSONOutputSchema:
        provider_contract = pack.get("provider_result_contract")
        if isinstance(provider_contract, dict) and isinstance(
            provider_contract.get("schema"), dict
        ):
            return RuntimeJSONOutputSchema(
                SDKTaskWorker._responses_transport_schema(provider_contract["schema"])
            )
        contracts = pack.get("output_contracts") or []
        if not contracts and isinstance(pack.get("output_contract"), dict):
            contracts = [pack["output_contract"]]
        valid_contracts = [
            contract
            for contract in contracts
            if isinstance(contract, dict) and isinstance(contract.get("schema"), dict)
        ]
        if len(valid_contracts) == 1:
            return RuntimeJSONOutputSchema(
                SDKTaskWorker._artifact_transport_schema(valid_contracts[0])
            )
        return RuntimeJSONOutputSchema({"type": "object"})

    @staticmethod
    def _responses_transport_schema(schema: dict[str, Any]) -> dict[str, Any]:
        if schema.get("type") is not None:
            return schema
        branches = schema.get("allOf")
        if not isinstance(branches, list):
            return schema
        object_branches = [
            branch
            for branch in branches
            if isinstance(branch, dict) and branch.get("type") == "object"
        ]
        metadata_keys = {"$defs", "$id", "$schema", "title", "description"}
        metadata_branches = [
            branch
            for branch in branches
            if isinstance(branch, dict)
            and branch.get("type") is None
            and set(branch).issubset(metadata_keys)
        ]
        if len(object_branches) != 1 or len(metadata_branches) != len(branches) - 1:
            return schema
        transport_schema = deepcopy(object_branches[0])
        for key in metadata_keys:
            if key in schema:
                transport_schema[key] = deepcopy(schema[key])
        for branch in metadata_branches:
            for key, value in branch.items():
                if key == "$defs" and isinstance(value, dict):
                    transport_schema.setdefault("$defs", {}).update(deepcopy(value))
                elif key not in transport_schema:
                    transport_schema[key] = deepcopy(value)
        return transport_schema

    @staticmethod
    def _artifact_transport_schema(contract: dict[str, Any]) -> dict[str, Any]:
        schema = contract["schema"]
        artifact_type = str(contract.get("artifact_type") or "").strip()
        if not artifact_type or artifact_type.startswith("task_checkpoint:"):
            return schema
        properties = schema.get("properties")
        if isinstance(properties, dict) and artifact_type in properties:
            return schema
        return {
            "type": "object",
            "required": [artifact_type],
            "properties": {artifact_type: schema},
            "additionalProperties": False,
        }

    @staticmethod
    def _parse_json_object(output: str) -> dict[str, Any]:
        text = output.strip()
        if text.startswith("```"):
            first_newline = text.find("\n")
            if first_newline >= 0:
                text = text[first_newline + 1 :]
            if "```" in text:
                text = text.split("```", 1)[0].strip()
        try:
            payload = json.loads(text)
        except json.JSONDecodeError as exc:
            raise ValueError("provider output is not valid JSON") from exc
        if (
            isinstance(payload, list)
            and len(payload) == 1
            and isinstance(payload[0], dict)
        ):
            payload = payload[0]
        if not isinstance(payload, dict):
            raise ValueError("provider output is not a JSON object")
        return payload

    @staticmethod
    def _parse_and_validate(output: str, pack: dict[str, Any]) -> dict[str, Any]:
        payload = SDKTaskWorker._parse_json_object(output)
        payload = SDKTaskWorker._unwrap_provider_result_envelope(payload, pack)
        contracts = pack.get("output_contracts") or []
        provider_contract = pack.get("provider_result_contract")
        if isinstance(provider_contract, dict):
            contracts = [provider_contract]
            if not isinstance(pack.get("skill_instructions"), dict):
                payload = SDKTaskWorker._normalize_provider_transport(payload, provider_contract)
        elif not contracts and isinstance(pack.get("output_contract"), dict):
            contracts = [pack["output_contract"]]
        for contract in contracts:
            schema = contract.get("schema") if isinstance(contract, dict) else None
            if isinstance(schema, dict):
                artifact_type = str(contract.get("artifact_type") or "")
                candidate = payload
                if not isinstance(provider_contract, dict):
                    candidate = payload.get("artifact")
                    if candidate is None and artifact_type:
                        candidate = payload.get(artifact_type)
                    if candidate is None:
                        candidate = payload
                errors = list(Draft202012Validator(schema).iter_errors(candidate))
                if errors:
                    raise ValueError(errors[0].message)
        return payload

    @staticmethod
    def _normalize_provider_transport(
        payload: dict[str, Any], contract: dict[str, Any]
    ) -> dict[str, Any]:
        schema = contract.get("schema")
        if not isinstance(schema, dict) or schema.get("additionalProperties") is not False:
            return payload
        properties = schema.get("properties")
        required = schema.get("required")
        if not isinstance(properties, dict) or not isinstance(required, list):
            return payload
        required_keys = [str(key) for key in required]
        if not required_keys or not all(key in payload for key in required_keys):
            return payload
        allowed = set(properties)
        return {key: value for key, value in payload.items() if key in allowed}

    @staticmethod
    def _provider_envelope(
        payload: dict[str, Any], pack: dict[str, Any]
    ) -> dict[str, Any]:
        if not SDKTaskWorker._requires_artifact_envelope(pack):
            return payload
        contracts = pack.get("output_contracts") or []
        provider_contract = pack.get("provider_result_contract")
        if isinstance(provider_contract, dict):
            contracts = [provider_contract]
        elif not contracts and isinstance(pack.get("output_contract"), dict):
            contracts = [pack["output_contract"]]
        if len(contracts) != 1 or not isinstance(contracts[0], dict):
            return payload
        artifact_type = str(contracts[0].get("artifact_type") or "")
        if "artifact" in payload or (artifact_type and artifact_type in payload):
            return payload
        return {"artifact": payload}

    @staticmethod
    def _unwrap_provider_result_envelope(
        payload: dict[str, Any], pack: dict[str, Any]
    ) -> dict[str, Any]:
        content = payload.get("content")
        contract = pack.get("provider_result_contract")
        if not isinstance(contract, dict):
            contracts = pack.get("output_contracts") or []
            if not contracts and isinstance(pack.get("output_contract"), dict):
                contracts = [pack["output_contract"]]
            contract = contracts[0] if len(contracts) == 1 else None
        if not isinstance(contract, dict):
            return payload
        expected_type = str(contract.get("artifact_type") or "")
        if expected_type and str(payload.get("artifact_type") or "") == expected_type:
            if isinstance(content, dict):
                return content
            return {
                key: value
                for key, value in payload.items()
                if key not in {"artifact_type", "schema_ref", "schema_version"}
            }
        return payload

    @staticmethod
    def _requires_artifact_envelope(pack: dict[str, Any]) -> bool:
        intent = pack.get("intent")
        if isinstance(intent, dict) and intent.get("operation") == "generate_task_checkpoint":
            return False
        contracts = pack.get("output_contracts") or []
        provider_contract = pack.get("provider_result_contract")
        if isinstance(provider_contract, dict):
            # The provider schema already defines the exact transport wrapper.
            # Adding an artifact layer here changes what Go's response adapter sees.
            return False
        elif not contracts and isinstance(pack.get("output_contract"), dict):
            contracts = [pack["output_contract"]]
        if len(contracts) != 1:
            return False
        return not any(
            isinstance(contract, dict)
            and str(contract.get("artifact_type") or "").startswith("task_checkpoint:")
            for contract in contracts
        )

    @staticmethod
    def _normalize_story_bible_coverage(
        payload: dict[str, Any], pack: dict[str, Any]
    ) -> dict[str, Any]:
        cursor = pack.get("task_cursor")
        if not isinstance(cursor, dict):
            return payload
        batch = cursor.get("batch")
        if not isinstance(batch, dict) or batch.get("phase") != "story_bible_aggregate":
            return payload
        source_analysis = batch.get("source_analysis")
        if not isinstance(source_analysis, dict):
            return payload
        units = source_analysis.get("units")
        if not isinstance(units, list) or not units:
            return payload
        story = payload.get("artifact")
        if not isinstance(story, dict):
            story = payload.get("story_bible")
        if not isinstance(story, dict):
            story = payload
        source_structure = story.get("source_structure")
        existing = {
            str(item.get("source_unit_id")): item
            for item in source_structure or []
            if isinstance(item, dict) and item.get("source_unit_id")
        }
        normalized: list[dict[str, Any]] = []
        for unit in units:
            if not isinstance(unit, dict):
                continue
            source_unit_id = str(unit.get("source_unit_id") or "")
            if not source_unit_id:
                continue
            item = existing.get(source_unit_id)
            if item is None:
                item = {
                    "source_unit_id": source_unit_id,
                    "source_range": source_unit_id,
                    "summary": unit.get("summary") or "",
                    "key_events": unit.get("key_events") or [],
                    "character_changes": unit.get("character_changes") or [],
                    "conflict_stage": unit.get("conflict_stage") or "",
                    "hook_or_suspense_potential": unit.get(
                        "hook_or_suspense_potential"
                    )
                    or "medium",
                    "source_refs": unit.get("source_refs") or [],
                }
            normalized.append(item)
        if len(normalized) == len(units):
            story["source_structure"] = normalized
        return payload

    @staticmethod
    def _video_script_unit(payload: dict[str, Any]) -> dict[str, Any] | None:
        if isinstance(payload.get("scenes"), list):
            return payload

        for key in ("artifact", "video_script_unit"):
            unit = SDKTaskWorker._single_json_object(payload.get(key))
            if unit is None:
                continue
            nested = SDKTaskWorker._video_script_unit(unit)
            return nested if nested is not None else unit
        return None

    @staticmethod
    def _single_json_object(value: Any) -> dict[str, Any] | None:
        if isinstance(value, str):
            try:
                value = json.loads(value)
            except json.JSONDecodeError:
                return None
        if (
            isinstance(value, list)
            and len(value) == 1
            and isinstance(value[0], dict)
        ):
            value = value[0]
        return value if isinstance(value, dict) else None

    @staticmethod
    def _payload_shape(payload: dict[str, Any]) -> str:
        shape: dict[str, str] = {}
        for key, value in payload.items():
            if isinstance(value, list):
                shape[key] = f"list[{len(value)}]"
            elif isinstance(value, dict):
                shape[key] = "object"
            else:
                shape[key] = type(value).__name__
        return json.dumps(shape, ensure_ascii=False, sort_keys=True)

    @staticmethod
    def _assemble_video_evidence_payload(
        payload: dict[str, Any],
        pack: dict[str, Any],
        subtitles: list[dict[str, Any]],
        source_duration_ms: int,
    ) -> dict[str, Any]:
        if "video_evidence" not in payload and set(payload) == {"schema"}:
            wrapped = SDKTaskWorker._single_json_object(payload.get("schema"))
            if isinstance(wrapped, dict) and isinstance(
                wrapped.get("video_evidence"), dict
            ):
                payload = wrapped
        evidence = payload.get("video_evidence")
        if evidence is None:
            return payload
        if not isinstance(evidence, dict):
            raise ValueError("video_evidence is not an object")
        raw_scenes = evidence.get("scenes")
        if not isinstance(raw_scenes, list) or not raw_scenes:
            raise ValueError("video_evidence scenes are missing")

        cursor = pack.get("task_cursor") or {}
        video_cursor = cursor.get("video") if isinstance(cursor, dict) else {}
        asset_id = str((video_cursor or {}).get("asset_id") or "")
        snapshot_id = str((video_cursor or {}).get("asset_snapshot_id") or "")
        episode_order = int((video_cursor or {}).get("episode_order") or 1)
        episode_no = int((video_cursor or {}).get("episode_no") or episode_order)

        def bounded_range(value: Any, fallback_start: int, fallback_end: int) -> tuple[int, int]:
            item = value if isinstance(value, dict) else {}
            try:
                start = int(item.get("start_ms", fallback_start))
                end = int(item.get("end_ms", fallback_end))
            except (TypeError, ValueError):
                start, end = fallback_start, fallback_end
            maximum = max(source_duration_ms, 1)
            start = min(max(0, start), maximum - 1)
            end = min(max(start + 1, end), maximum)
            return start, end

        def source_ref(start: int, end: int) -> dict[str, Any]:
            return {
                "source_type": "video_time_range",
                "asset_id": asset_id,
                "asset_snapshot_id": snapshot_id,
                "time_range": {"start_ms": start, "end_ms": end},
            }

        scenes: list[dict[str, Any]] = []
        for index, raw_scene in enumerate(raw_scenes, start=1):
            if not isinstance(raw_scene, dict):
                raise ValueError("video_evidence scene is invalid")
            start, end = bounded_range(
                raw_scene.get("time_range"),
                0 if index == 1 else scenes[-1]["time_range"]["end_ms"],
                source_duration_ms,
            )
            actions: list[dict[str, Any]] = []
            for action_index, raw_action in enumerate(raw_scene.get("actions") or [], start=1):
                if not isinstance(raw_action, dict):
                    continue
                text = str(raw_action.get("text") or "").strip().lstrip("△").strip()
                if not text:
                    continue
                action_start, action_end = bounded_range(
                    raw_action.get("time_range"), start, min(end, start + 1000)
                )
                action_start = min(max(action_start, start), end - 1)
                action_end = min(max(action_start + 1, action_end), end)
                actions.append({
                    "line_id": f"line_{episode_no}_{index}_action_{action_index}",
                    "block_type": "action",
                    "text": text,
                    "source_refs": [source_ref(action_start, action_end)],
                    "uncertainty": "none",
                    "_timeline_start_ms": action_start,
                })
            scenes.append({
                "scene_id": str(raw_scene.get("scene_id") or f"scene_{episode_no}_{index}"),
                "heading": str(raw_scene.get("heading") or f"场{episode_no}-{index}"),
                "location": str(raw_scene.get("location") or "未知地点"),
                "interior_exterior": str(raw_scene.get("interior_exterior") or "内外"),
                "time_of_day": str(raw_scene.get("time_of_day") or "时间不明"),
                "time_range": {"start_ms": start, "end_ms": end},
                "characters": [str(item).strip() for item in raw_scene.get("characters") or [] if str(item).strip()],
                "blocks": actions,
                "source_refs": [source_ref(start, end)],
            })
        scenes.sort(key=lambda item: item["time_range"]["start_ms"])

        raw_annotations = evidence.get("dialogue_annotations") or []
        if not isinstance(raw_annotations, list):
            raise ValueError("video_evidence dialogue_annotations is not a list")
        annotation_ids = [
            str(item.get("subtitle_id") or "")
            for item in raw_annotations
            if isinstance(item, dict) and item.get("subtitle_id")
        ]
        if len(annotation_ids) != len(set(annotation_ids)):
            raise ValueError("video_evidence contains duplicate subtitle annotations")
        expected_subtitle_ids = {
            str(item.get("subtitle_id") or f"subtitle_{index + 1:04d}")
            for index, item in enumerate(subtitles)
        }
        actual_subtitle_ids = set(annotation_ids)
        if actual_subtitle_ids != expected_subtitle_ids:
            missing = sorted(expected_subtitle_ids - actual_subtitle_ids)
            unexpected = sorted(actual_subtitle_ids - expected_subtitle_ids)
            raise ValueError(
                "video_evidence subtitle annotations do not match the authoritative timeline: "
                f"missing={missing[:8]}, unexpected={unexpected[:8]}"
            )
        annotations = {
            str(item["subtitle_id"]): item
            for item in raw_annotations
            if isinstance(item, dict) and item.get("subtitle_id")
        }
        for subtitle_index, subtitle in enumerate(subtitles, start=1):
            subtitle_id = str(subtitle.get("subtitle_id") or f"subtitle_{subtitle_index:04d}")
            start, end = bounded_range(
                subtitle,
                int(subtitle.get("start_ms") or 0),
                int(subtitle.get("end_ms") or 0),
            )
            midpoint = (start + end) // 2
            scene = min(
                scenes,
                key=lambda item: 0
                if item["time_range"]["start_ms"] <= midpoint < item["time_range"]["end_ms"]
                else min(
                    abs(midpoint - item["time_range"]["start_ms"]),
                    abs(midpoint - item["time_range"]["end_ms"]),
                ),
            )
            annotation = annotations.get(subtitle_id) or {}
            speaker = str(annotation.get("speaker") or "未知人物").strip() or "未知人物"
            block: dict[str, Any] = {
                "line_id": subtitle_id,
                "block_type": "dialogue",
                "speaker": speaker,
                "text": str(subtitle.get("text") or "").strip(),
                "source_refs": [source_ref(start, end)],
                "_timeline_start_ms": start,
            }
            delivery = str(annotation.get("delivery") or "").strip()
            if delivery:
                block["delivery"] = delivery
            if speaker == "未知人物":
                block["uncertainty"] = "speaker_unknown"
            scene["blocks"].append(block)
            if speaker != "未知人物" and speaker not in scene["characters"]:
                scene["characters"].append(speaker)

        for scene in scenes:
            scene["blocks"].sort(key=lambda item: int(item.pop("_timeline_start_ms", 0)))

        continuity = evidence.get("continuity_delta")
        if not isinstance(continuity, dict):
            continuity = {}
        normalized_continuity = {
            key: list(continuity.get(key) or [])
            for key in (
                "new_facts",
                "character_state_changes",
                "relationship_changes",
                "hooks_opened",
                "hooks_resolved",
            )
        }
        completeness = evidence.get("extraction_completeness")
        if not isinstance(completeness, dict):
            completeness = {}
        return {
            "video_script_unit": {
                "episode_no": episode_no,
                "episode_order": episode_order,
                "source_file_name": str((video_cursor or {}).get("file_name") or "video.mp4"),
                "plot_summary": str(evidence.get("plot_summary") or "视频剧情已按时间线还原。"),
                "title": str(evidence.get("title") or ""),
                "script_text": f"第{episode_no}集",
                "scenes": scenes,
                "source_refs": [source_ref(0, max(source_duration_ms, 1))],
                "continuity_delta": normalized_continuity,
                "uncertainty_flags": [],
                "extraction_completeness": {
                    "status": str(completeness.get("status") or "complete_first_pass"),
                    "known_gaps": list(completeness.get("known_gaps") or []),
                    "review_notes": list(completeness.get("review_notes") or []),
                },
                "source_availability": "active",
            }
        }

    @staticmethod
    def _assert_video_output_quality(
        payload: dict[str, Any], source_duration_ms: int, *, require_speaker_resolution: bool = False
    ) -> None:
        unit = SDKTaskWorker._video_script_unit(payload)
        if unit is None:
            raise ValueError("video_script_unit is missing")
        scenes = unit.get("scenes")
        if not isinstance(scenes, list) or not scenes:
            raise ValueError("video_script_unit scenes are missing")
        latin_labels: list[str] = []
        dialogue_count = 0
        unknown_dialogue_count = 0
        named_scene_character_count = 0
        ranges: list[tuple[int, int]] = []
        for scene in scenes:
            if not isinstance(scene, dict):
                continue
            labels = [str(item or "").strip() for item in scene.get("characters") or []]
            named_scene_character_count += sum(
                label not in {"", "未知人物", "旁白", "画外音", "系统"}
                for label in labels
            )
            labels.extend(
                str(block.get("speaker") or "").strip()
                for block in scene.get("blocks") or []
                if isinstance(block, dict) and block.get("block_type") == "dialogue"
            )
            for block in scene.get("blocks") or []:
                if not isinstance(block, dict) or block.get("block_type") != "dialogue":
                    continue
                dialogue_count += 1
                if str(block.get("speaker") or "").strip() == "未知人物":
                    unknown_dialogue_count += 1
            latin_labels.extend(label for label in labels if re.search(r"[A-Za-z]", label))
        def collect_ranges(value: Any) -> None:
            if isinstance(value, dict):
                if value.get("source_type") == "video_time_range":
                    time_range = value.get("time_range")
                    if isinstance(time_range, dict):
                        try:
                            start = max(0, int(time_range.get("start_ms") or 0))
                            end = int(time_range.get("end_ms") or 0)
                        except (TypeError, ValueError):
                            pass
                        else:
                            if end > start:
                                ranges.append((start, end))
                for item in value.values():
                    collect_ranges(item)
            elif isinstance(value, list):
                for item in value:
                    collect_ranges(item)

        collect_ranges(unit)
        if latin_labels:
            raise ValueError(
                "character and speaker labels must be Chinese: "
                + ", ".join(sorted(set(latin_labels))[:8])
            )
        if (
            require_speaker_resolution
            and dialogue_count >= 3
            and named_scene_character_count > 0
            and unknown_dialogue_count / dialogue_count > 0.8
        ):
            raise ValueError(
                "video dialogue speaker attribution is mostly unresolved: "
                f"unknown={unknown_dialogue_count}, total={dialogue_count}"
            )
        if source_duration_ms <= 0:
            return
        maximum_evidence_end = max((end for _, end in ranges), default=0)
        if maximum_evidence_end > source_duration_ms + 2000:
            raise ValueError(
                "video timeline evidence exceeds source duration: "
                f"maximum_end_ms={maximum_evidence_end}, "
                f"source_duration_ms={source_duration_ms}"
            )
        clipped = sorted(
            (min(start, source_duration_ms), min(end, source_duration_ms))
            for start, end in ranges
            if start < source_duration_ms and end > 0
        )
        merged: list[list[int]] = []
        for start, end in clipped:
            if not merged or start > merged[-1][1] + 1000:
                merged.append([start, end])
            else:
                merged[-1][1] = max(merged[-1][1], end)
        covered_ms = sum(end - start for start, end in merged)
        covered_end_ms = max((end for _, end in clipped), default=0)
        if covered_ms < source_duration_ms * 0.85 or covered_end_ms < source_duration_ms * 0.95:
            raise ValueError(
                "video timeline coverage incomplete: "
                f"covered_ms={covered_ms}, covered_end_ms={covered_end_ms}, "
                f"source_duration_ms={source_duration_ms}"
            )
        scenes_without_actions = [
            str(scene.get("scene_id") or index + 1)
            for index, scene in enumerate(scenes)
            if isinstance(scene, dict)
            and not any(
                isinstance(block, dict) and block.get("block_type") == "action"
                for block in scene.get("blocks") or []
            )
        ]
        if scenes_without_actions:
            raise ValueError(
                "video scenes require evidence-grounded action blocks: "
                + ", ".join(scenes_without_actions[:8])
            )

    async def _attribute_video_dialogue_speakers(
        self, payload: dict[str, Any], pack: dict[str, Any]
    ) -> tuple[dict[str, Any], Any | None]:
        unit = self._video_script_unit(payload)
        if unit is None:
            return payload, None
        self._split_video_dialogue_lines(unit)
        candidates = self._video_speaker_candidates(unit)
        if not candidates:
            return payload, None

        assignment_schema = RuntimeJSONOutputSchema(
            {
                "type": "object",
                "required": ["assignments"],
                "properties": {
                    "assignments": {
                        "type": "array",
                        "items": {
                            "type": "object",
                            "required": ["key", "speaker"],
                            "properties": {
                                "key": {"type": "string"},
                                "speaker": {"type": "string"},
                            },
                            "additionalProperties": False,
                        },
                    }
                },
                "additionalProperties": False,
            }
        )
        agent = Agent(
            name="视频对白说话人归属 Agent",
            instructions=(
                "你只做逐行说话人归属，不改写台词，不生成剧本。"
                "根据每场的动作、在场人物、称呼、语义承接和对话轮次，为每个 key 选择 speaker。"
                "叙述剧情、交代时间地点或总结事件的画外叙事应选择旁白或画外音，不能标为未知人物。"
                "speaker 必须严格来自该行 allowed_speakers；证据不足才使用未知人物。"
                "必须对每个输入 key 恰好返回一次，不得遗漏、重复或新增 key。"
            ),
            model=self._model,
            model_settings=ModelSettings(max_tokens=self._max_tokens),
            output_type=assignment_schema,
        )
        batches = self._video_speaker_batches(candidates)
        assignments: list[dict[str, Any]] = []
        results: list[Any] = []
        for batch_index, batch in enumerate(batches, start=1):
            attribution_input = json.dumps(
                {
                    "task": "attribute_dialogue_speakers",
                    "episode_no": unit.get("episode_no"),
                    "batch_index": batch_index,
                    "batch_count": len(batches),
                    "lines": batch,
                },
                ensure_ascii=False,
            )
            result = await asyncio.wait_for(
                Runner.run(
                    SDKGuardrailPolicy.from_settings(self._settings).protect(agent),
                    attribution_input,
                    max_turns=2,
                    run_config=self._sdk_run_config(
                        "content-agent-video-speaker-attribution"
                    ),
                ),
                timeout=self._settings.run_timeout_seconds,
            )
            output = result.final_output
            if not isinstance(output, dict):
                raise ValueError("speaker attribution did not return a JSON object")
            batch_assignments = output.get("assignments")
            if not isinstance(batch_assignments, list):
                raise ValueError("speaker attribution assignments are missing")
            assignments.extend(batch_assignments)
            results.append(result)
        return self._apply_video_speaker_assignments(payload, candidates, assignments), results

    @staticmethod
    def _split_video_dialogue_lines(unit: dict[str, Any]) -> None:
        """Keep every provider dialogue line independently attributable and editable."""
        for scene_index, scene in enumerate(unit.get("scenes") or []):
            if not isinstance(scene, dict):
                continue
            rewritten: list[dict[str, Any]] = []
            for block_index, block in enumerate(scene.get("blocks") or []):
                if not isinstance(block, dict) or block.get("block_type") != "dialogue":
                    rewritten.append(block)
                    continue
                lines = [
                    line.strip()
                    for line in str(block.get("text") or "").splitlines()
                    if line.strip()
                ]
                if len(lines) <= 1:
                    rewritten.append(block)
                    continue
                base_line_id = str(
                    block.get("line_id")
                    or f"line_{scene_index + 1}_{block_index + 1}"
                )
                for line_index, line in enumerate(lines, start=1):
                    replacement = dict(block)
                    replacement["line_id"] = f"{base_line_id}_{line_index}"
                    replacement["text"] = line
                    rewritten.append(replacement)
            scene["blocks"] = rewritten

    @staticmethod
    def _video_speaker_candidates(unit: dict[str, Any]) -> list[dict[str, Any]]:
        candidates: list[dict[str, Any]] = []
        for scene_index, scene in enumerate(unit.get("scenes") or []):
            if not isinstance(scene, dict):
                continue
            scene_id = str(scene.get("scene_id") or f"scene_{scene_index + 1}")
            characters = [
                str(name).strip()
                for name in scene.get("characters") or []
                if str(name).strip()
            ]
            for block_index, block in enumerate(scene.get("blocks") or []):
                if not isinstance(block, dict) or block.get("block_type") != "dialogue":
                    continue
                speaker = str(block.get("speaker") or "").strip()
                lines = [line.strip() for line in str(block.get("text") or "").splitlines() if line.strip()]
                if speaker != "未知人物" or not lines:
                    continue
                allowed_speakers = [
                    name
                    for name in characters
                    if name not in {"未知人物", "旁白", "画外音"}
                ] + ["旁白", "画外音", "未知人物"]
                for line_index, line in enumerate(lines):
                    candidates.append(
                        {
                            "key": f"{scene_id}:{block_index}:{line_index}",
                            "scene_id": scene_id,
                            "heading": scene.get("heading"),
                            "action_context": "\n".join(
                                str(item.get("text") or "")
                                for item in scene.get("blocks") or []
                                if isinstance(item, dict) and item.get("block_type") == "action"
                            ),
                            "allowed_speakers": allowed_speakers,
                            "line": line,
                        }
                    )
        return candidates

    @staticmethod
    def _video_speaker_batches(
        candidates: list[dict[str, Any]],
        *,
        max_scenes: int = 5,
        max_lines: int = 60,
    ) -> list[list[dict[str, Any]]]:
        batches: list[list[dict[str, Any]]] = []
        current: list[dict[str, Any]] = []
        current_scenes: set[str] = set()
        for candidate in candidates:
            scene_id = str(candidate.get("scene_id") or "")
            adds_scene = scene_id not in current_scenes
            if current and (
                len(current) >= max_lines
                or (adds_scene and len(current_scenes) >= max_scenes)
            ):
                batches.append(current)
                current = []
                current_scenes = set()
            current.append(candidate)
            current_scenes.add(scene_id)
        if current:
            batches.append(current)
        return batches

    @staticmethod
    def _apply_video_speaker_assignments(
        payload: dict[str, Any],
        candidates: list[dict[str, Any]],
        assignments: list[dict[str, Any]],
    ) -> dict[str, Any]:
        expected = {str(item["key"]): item for item in candidates}
        resolved: dict[str, str] = {}
        for item in assignments:
            if not isinstance(item, dict):
                raise ValueError("speaker attribution contains an invalid assignment")
            key = str(item.get("key") or "")
            speaker = str(item.get("speaker") or "").strip()
            if key not in expected or key in resolved:
                raise ValueError(f"speaker attribution returned an invalid key: {key}")
            allowed = expected[key].get("allowed_speakers") or []
            if speaker not in allowed:
                logger.warning(
                    "speaker attribution returned out-of-scene speaker key=%s speaker=%s; "
                    "falling back to unknown",
                    key,
                    speaker,
                )
                speaker = "未知人物"
            resolved[key] = speaker
        if set(resolved) != set(expected):
            raise ValueError("speaker attribution did not cover every dialogue line")

        unit = SDKTaskWorker._video_script_unit(payload)
        if unit is None:
            raise ValueError("video script payload is missing")
        for scene_index, scene in enumerate(unit.get("scenes") or []):
            if not isinstance(scene, dict):
                continue
            scene_id = str(scene.get("scene_id") or f"scene_{scene_index + 1}")
            rewritten: list[dict[str, Any]] = []
            for block_index, block in enumerate(scene.get("blocks") or []):
                if not isinstance(block, dict) or block.get("block_type") != "dialogue":
                    rewritten.append(block)
                    continue
                lines = [line.strip() for line in str(block.get("text") or "").splitlines() if line.strip()]
                keys = [f"{scene_id}:{block_index}:{line_index}" for line_index in range(len(lines))]
                if not keys or keys[0] not in expected:
                    rewritten.append(block)
                    continue
                base_line_id = str(block.get("line_id") or f"{scene_id}_{block_index}")
                for line_index, (key, line) in enumerate(
                    zip(keys, lines, strict=True), start=1
                ):
                    speaker = resolved[key]
                    replacement = dict(block)
                    replacement["line_id"] = f"{base_line_id}_{line_index}"
                    replacement["speaker"] = speaker
                    replacement["text"] = line
                    if speaker == "未知人物":
                        replacement["uncertainty"] = "speaker_unknown"
                    else:
                        replacement.pop("uncertainty", None)
                    rewritten.append(replacement)
            scene["blocks"] = rewritten
        return payload

    @staticmethod
    def _normalize_video_script_payload(
        payload: dict[str, Any], pack: dict[str, Any]
    ) -> dict[str, Any]:
        contracts = pack.get("output_contracts") or []
        is_video = any(
            isinstance(contract, dict)
            and contract.get("artifact_type") == "video_script_unit"
            for contract in contracts
        )
        if not is_video:
            return payload
        unit: Any = SDKTaskWorker._video_script_unit(payload)
        if not isinstance(unit, dict):
            raise ValueError(
                "video script payload is not an object; shape="
                + SDKTaskWorker._payload_shape(payload)
            )

        cursor = pack.get("task_cursor") or {}
        video_cursor = cursor.get("video") if isinstance(cursor, dict) else {}
        registry = [
            dict(entry)
            for entry in (video_cursor or {}).get("character_registry", [])
            if isinstance(entry, dict) and entry.get("canonical_name")
        ]
        expected_episode_no = int((video_cursor or {}).get("episode_no") or 0)
        expected_episode_order = int((video_cursor or {}).get("episode_order") or 0)
        if expected_episode_no <= 0:
            expected_episode_no = expected_episode_order
        if expected_episode_order <= 0:
            expected_episode_order = expected_episode_no
        if expected_episode_no <= 0 or expected_episode_order <= 0:
            raise ValueError("video task cursor is missing the episode identity")
        unit["episode_no"] = expected_episode_no
        unit["episode_order"] = expected_episode_order
        source_file_name = str((video_cursor or {}).get("file_name") or "").strip()
        if source_file_name:
            unit["source_file_name"] = source_file_name
        source_asset_id = str((video_cursor or {}).get("asset_id") or "").strip()
        source_snapshot_id = str(
            (video_cursor or {}).get("asset_snapshot_id") or ""
        ).strip()
        if source_asset_id and source_snapshot_id:
            SDKTaskWorker._bind_video_source_refs(
                unit, source_asset_id, source_snapshot_id
            )
        episode_order = expected_episode_order
        names: list[str] = []
        scenes = unit.get("scenes")
        if not isinstance(scenes, list) or not scenes:
            raise ValueError("video script requires at least one scene")
        for scene in scenes:
            if not isinstance(scene, dict):
                raise ValueError("video script scene is invalid")
            for name in scene.get("characters") or []:
                SDKTaskWorker._append_unique_name(names, str(name).strip())
            for block in scene.get("blocks") or []:
                if isinstance(block, dict) and block.get("block_type") == "dialogue":
                    SDKTaskWorker._append_unique_name(names, str(block.get("speaker") or "").strip())
        for name in names:
            if SDKTaskWorker._ignored_video_character(name):
                continue
            SDKTaskWorker._register_video_character(registry, name, episode_order)

        def resolve(raw: str) -> str:
            name = raw.strip()
            if SDKTaskWorker._ignored_video_character(name):
                return name
            matches: list[str] = []
            for entry in registry:
                canonical = str(entry.get("canonical_name") or "")
                aliases = [str(value) for value in entry.get("aliases") or []]
                if name == canonical or name in aliases or SDKTaskWorker._safe_video_character_alias(canonical, name):
                    SDKTaskWorker._append_unique_name(matches, canonical)
            if len(matches) == 1:
                return matches[0]
            if len(matches) > 1:
                raise ValueError(
                    f'character name "{name}" matches multiple canonical characters: {", ".join(matches)}'
                )
            return name

        episode = expected_episode_no
        rendered = [f"第{episode}集"]
        block_count = 0
        for scene_index, scene in enumerate(scenes, start=1):
            heading = str(scene.get("heading") or "").strip()
            if not heading:
                raise ValueError(f"video script scene {scene_index} heading is empty")
            characters: list[str] = []
            for raw in scene.get("characters") or []:
                resolved = resolve(str(raw))
                SDKTaskWorker._append_unique_name(characters, resolved)
            scene["characters"] = characters
            rendered.append(heading)
            if characters:
                rendered.append("人物：" + "、".join(characters))
            blocks = scene.get("blocks")
            if not isinstance(blocks, list) or not blocks:
                raise ValueError(f"video script scene {scene_index} has no content blocks")
            for block_index, block in enumerate(blocks, start=1):
                if not isinstance(block, dict):
                    raise ValueError(f"video script scene {scene_index} block {block_index} is invalid")
                text = str(block.get("text") or "").strip()
                if not text:
                    raise ValueError(f"video script scene {scene_index} block {block_index} text is empty")
                block_count += 1
                block_type = str(block.get("block_type") or "")
                if block_type == "dialogue":
                    speaker = resolve(str(block.get("speaker") or ""))
                    if not speaker:
                        raise ValueError(
                            f"video script scene {scene_index} dialogue {block_index} speaker is empty"
                        )
                    block["speaker"] = speaker
                    delivery = SDKTaskWorker._render_video_delivery(
                        str(block.get("delivery") or "")
                    )
                    rendered.append(
                        speaker + (f"（{delivery}）" if delivery else "") + "：" + text
                    )
                elif block_type == "action":
                    rendered.append(text if text.startswith("△") else "△" + text)
                else:
                    rendered.append(text)
        if block_count == 0:
            raise ValueError("video script contains no renderable content")
        if str(unit.get("source_file_name") or "").strip() and not SDKTaskWorker._has_video_timeline_evidence(unit):
            raise ValueError("video script contains no video timeline evidence")
        unit["script_text"] = "\n".join(rendered)
        return payload

    @staticmethod
    def _render_video_delivery(value: str) -> str:
        delivery = value.strip()
        normalized = delivery.lower().replace("-", "_").replace(" ", "_")
        translated = {
            "voiceover": "画外音",
            "voice_over": "画外音",
            "vo": "画外音",
            "offscreen": "画外",
            "off_screen": "画外",
            "inner_voice": "内心独白",
            "internal_monologue": "内心独白",
            "whisper": "低声",
            "whispering": "低声",
            "shout": "高声",
            "shouting": "高声",
        }.get(normalized)
        if translated:
            return translated
        return "" if re.search(r"[A-Za-z]", delivery) else delivery

    @staticmethod
    def _bind_video_source_refs(
        value: Any, asset_id: str, asset_snapshot_id: str
    ) -> None:
        if isinstance(value, dict):
            if value.get("source_type") == "video_time_range":
                value["asset_id"] = asset_id
                value["asset_snapshot_id"] = asset_snapshot_id
            for item in value.values():
                SDKTaskWorker._bind_video_source_refs(
                    item, asset_id, asset_snapshot_id
                )
        elif isinstance(value, list):
            for item in value:
                SDKTaskWorker._bind_video_source_refs(
                    item, asset_id, asset_snapshot_id
                )

    @staticmethod
    def _has_video_timeline_evidence(value: Any) -> bool:
        if isinstance(value, dict):
            if value.get("source_type") == "video_time_range":
                time_range = value.get("time_range")
                if isinstance(time_range, dict):
                    try:
                        if (
                            int(time_range.get("end_ms") or 0)
                            - int(time_range.get("start_ms") or 0)
                            >= 1000
                        ):
                            return True
                    except (TypeError, ValueError):
                        pass
            return any(SDKTaskWorker._has_video_timeline_evidence(item) for item in value.values())
        if isinstance(value, list):
            return any(SDKTaskWorker._has_video_timeline_evidence(item) for item in value)
        return False

    @staticmethod
    def _append_unique_name(values: list[str], value: str) -> None:
        if value and value not in values:
            values.append(value)

    @staticmethod
    def _normalized_video_character_name(name: str) -> str:
        result = name.strip()
        for suffix in ("V.O.", "O.S.", "VO", "OS"):
            if result.endswith(suffix):
                result = result[: -len(suffix)].strip()
                break
        return result

    @staticmethod
    def _ignored_video_character(name: str) -> bool:
        return SDKTaskWorker._normalized_video_character_name(name) in {
            "", "未知人物", "旁白", "系统", "画外音",
        }

    @staticmethod
    def _safe_video_character_alias(left: str, right: str) -> bool:
        left = SDKTaskWorker._normalized_video_character_name(left)
        right = SDKTaskWorker._normalized_video_character_name(right)
        if not left or not right:
            return False
        if left == right:
            return True
        shorter, longer = sorted((left, right), key=len)
        generic = {
            "师傅", "老板", "书记", "主任", "村民", "男人", "女人", "男子", "女子",
            "先生", "小姐", "妈妈", "爸爸", "爷爷", "奶奶", "老人", "年轻人", "顾客",
        }
        return (
            len(shorter) >= 2
            and len(longer) - len(shorter) == 1
            and shorter not in generic
            and (longer.startswith(shorter) or longer.endswith(shorter))
        )

    @staticmethod
    def _likely_video_character_typo(left: str, right: str) -> bool:
        left = SDKTaskWorker._normalized_video_character_name(left)
        right = SDKTaskWorker._normalized_video_character_name(right)
        enumerators = set("甲乙丙丁戊己庚辛壬癸一二三四五六七八九十")
        if (
            len(left) >= 2
            and len(left) == len(right)
            and left[:-1] == right[:-1]
            and left[-1:] in enumerators
            and right[-1:] in enumerators
        ):
            return False
        return (
            left != right
            and len(left) >= 3
            and len(left) == len(right)
            and sum(a != b for a, b in zip(left, right)) == 1
        )

    @staticmethod
    def _video_character_matches_registry(registry: list[dict[str, Any]], name: str) -> bool:
        for entry in registry:
            canonical = str(entry.get("canonical_name") or "")
            aliases = [str(value) for value in entry.get("aliases") or []]
            if name == canonical or name in aliases or SDKTaskWorker._safe_video_character_alias(canonical, name):
                return True
        return False

    @staticmethod
    def _register_video_character(
        registry: list[dict[str, Any]], name: str, episode: int
    ) -> None:
        if SDKTaskWorker._ignored_video_character(name):
            return
        for entry in registry:
            canonical = str(entry.get("canonical_name") or "")
            aliases = [str(value) for value in entry.get("aliases") or []]
            if name == canonical or name in aliases or SDKTaskWorker._safe_video_character_alias(canonical, name):
                SDKTaskWorker._append_unique_name(aliases, name)
                if len(SDKTaskWorker._normalized_video_character_name(name)) > len(
                    SDKTaskWorker._normalized_video_character_name(canonical)
                ):
                    SDKTaskWorker._append_unique_name(aliases, canonical)
                    entry["canonical_name"] = name
                entry["aliases"] = aliases
                entry["last_seen_episode"] = max(int(entry.get("last_seen_episode") or 0), episode)
                entry["mention_count"] = int(entry.get("mention_count") or 0) + 1
                return
        registry.append(
            {
                "canonical_name": name,
                "aliases": [name],
                "first_seen_episode": episode,
                "last_seen_episode": episode,
                "mention_count": 1,
            }
        )

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

    def _sdk_run_config(self, workflow_name: str):
        return privacy_safe_run_config(
            workflow_name=workflow_name,
            tracing_enabled=bool(
                getattr(self._settings, "tracing_enabled", False)
            ),
            release_id=str(getattr(self._settings, "release_id", "dev") or "dev"),
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
        if isinstance(exc, ExecutionCheckpointFailed):
            return "SDK_CHECKPOINT_FAILED"
        if isinstance(exc, ExecutionStateInvalid):
            return "SDK_EXECUTION_STATE_INVALID"
        if isinstance(exc, ExecutionToolReplayRisk):
            return "SDK_TOOL_REPLAY_RISK"
        if isinstance(exc, (AgentToolConfigurationError, UserError)):
            return "AGENT_TOOL_CONFIGURATION_INVALID"
        text = str(exc).lower()
        if "output_schema_validation_failed" in text or "batch_coverage_invalid" in text or "output_length_insufficient" in text:
            return "OUTPUT_REPAIR_FAILED"
        if "content policy" in text or "safety" in text or "sensitive" in text:
            return "PROVIDER_CONTENT_POLICY_BLOCKED"
        if isinstance(exc, ValueError):
            return "OUTPUT_REPAIR_FAILED"
        return "PROVIDER_TEMPORARY_FAILURE"

    @staticmethod
    def _is_output_rejection(exc: BackendError) -> bool:
        text = str(exc).upper()
        return (
            "OUTPUT_SCHEMA_VALIDATION_FAILED" in text
            or "BATCH_COVERAGE_INVALID" in text
            or "OUTPUT_LENGTH_INSUFFICIENT" in text
        )

    @staticmethod
    def _safe_error_detail(exc: Exception) -> str:
        parts: list[str] = []
        current: BaseException | None = exc
        seen: set[int] = set()
        while current is not None and id(current) not in seen and len(parts) < 4:
            seen.add(id(current))
            message = str(current).strip()
            if message:
                parts.append(f"{type(current).__name__}: {message}")
            current = current.__cause__ or current.__context__
        detail = " <- ".join(parts) or normalize_provider_error(exc)
        detail = re.sub(r"(?i)bearer\s+[a-z0-9._~+/=-]{8,}", "Bearer [REDACTED]", detail)
        detail = re.sub(r"\bsk-[A-Za-z0-9_-]{8,}\b", "[REDACTED]", detail)
        return detail[:500]

    @staticmethod
    def _merge_usage(left: dict[str, Any], right: dict[str, Any]) -> dict[str, Any]:
        keys = {"requests", "input_tokens", "output_tokens", "total_tokens"}
        merged = {**left, **{key: int(left.get(key, 0)) + int(right.get(key, 0)) for key in keys}}
        if "context_artifact_version_ids" in left or "context_artifact_version_ids" in right:
            merged["context_artifact_version_ids"] = sorted(set(left.get("context_artifact_version_ids", [])) | set(right.get("context_artifact_version_ids", [])))
        return merged

    @staticmethod
    def _user_summary(error_code: str) -> str:
        if error_code == "AGENT_INPUT_GUARDRAIL_REJECTED":
            return "本次输入未通过平台安全校验，已停止执行，未继续发起模型请求。"
        if error_code == "AGENT_OUTPUT_GUARDRAIL_REJECTED":
            return "输出未通过平台安全校验，已停止执行，未提交被拒绝的内容。"
        if error_code == "SDK_CHECKPOINT_FAILED":
            return "SDK 执行状态无法保存，已停止任务，没有从头重跑之前的操作。"
        if error_code == "SDK_EXECUTION_STATE_INVALID":
            return "任务快照或 SDK 恢复状态不一致，已停止执行，未从头重新生成。"
        if error_code == "SDK_TOOL_REPLAY_RISK":
            return "任务已发起工具操作，不能从头自动重试，请先检查已保存的结果。"
        if error_code == "AGENT_TOOL_CONFIGURATION_INVALID":
            return "当前任务需要的工具配置不可用，请检查工具及 Skill 依赖。"
        if error_code == "PROVIDER_CONTENT_POLICY_BLOCKED":
            return "模型服务因内容安全策略拒绝了该输入。"
        if error_code == "OUTPUT_REPAIR_FAILED":
            return "模型输出未满足当前产物格式要求。"
        return "模型服务暂时未能完成该任务，可以稍后重试。"

    @staticmethod
    def _failure_stage(error_code: str) -> str:
        if error_code == "AGENT_INPUT_GUARDRAIL_REJECTED":
            return "input_validation"
        if error_code == "SDK_CHECKPOINT_FAILED":
            return "run_state_checkpoint"
        if error_code == "SDK_EXECUTION_STATE_INVALID":
            return "run_state_restore"
        if error_code in {"SDK_TOOL_REPLAY_RISK", "AGENT_TOOL_CONFIGURATION_INVALID"}:
            return "tool_execution"
        if error_code in {"OUTPUT_REPAIR_FAILED", "AGENT_OUTPUT_GUARDRAIL_REJECTED"}:
            return "output_validation"
        return "provider_call"
