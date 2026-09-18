from __future__ import annotations

from dataclasses import dataclass, field
import asyncio
from contextvars import ContextVar
import json
import logging
from pathlib import Path
import re
from uuid import uuid4
from contextlib import suppress
from typing import Any, AsyncIterator, Awaitable, Callable

import anyio
from openai.types.responses import ResponseFunctionToolCall

from agents import (
    Agent,
    AsyncOpenAI,
    ModelSettings,
    OpenAIResponsesModel,
    RunContextWrapper,
    RunState,
    Runner,
    ToolsToFinalOutputResult,
    UserError,
    function_tool,
    set_tracing_disabled,
)
from agents.items import ItemHelpers, ModelResponse
from agents.models.interface import Model, ModelTracing

from .backend import BackendClient, BackendError, backend_activity
from .agent_tools import AgentToolConfigurationError, AgentToolProvider, TOOL_APPROVAL_INSTRUCTIONS, selected_skill_script_snapshot
from .config import Settings
from .contracts import (
    AgentExecutionRequest,
    AgentToolApprovalDecision,
    ArtifactSourceRoutingDecision,
    ControlDecision,
    SkillRoutingDecision,
)
from .observability import TurnObservation
from .session import NativeCompactionProtocolError, create_sdk_session, prepare_sdk_session, sdk_session_turn
from .turn_admission import reserve_turn
from .execution_control import list_execution_targets, inspect_execution_controls, control_execution
from .structured_output import parse_control_decision
from .guardrails import SDKGuardrailPolicy
from .skill_agents import attach_skill_handoffs
from .skill_metadata import allows_implicit_skill_invocation
from .workspace_files import apply_workspace_patch, list_workspace_files, read_workspace_file
from .native_publication import prepare_workspace_publication, publish_workspace_files
from .memory_publication import prepare_agent_memory_publication, publish_agent_memory
from .project_skills import install_workspace_skill, validate_workspace_skill
from .native_pause import unstarted_checkpoint, validate_unstarted_input
from .model_failure_boundary import MODEL_RECOVERY_REASON, ModelFailureBoundary, ModelRecoveryRequired
from .tool_outcomes import EXTERNAL_TOOL_RECOVERY_REASON, ToolOutcomeReviewRequired, require_external_tool_pause
from .main_inputs import main_input_batch, stage_main_inputs, validate_main_input_state
from .input_attachments import prepare_input_attachments
from .managed_instructions import bind_managed_instructions, with_managed_instructions
from .instruction_tools import get_saved_instructions, update_saved_instructions
from .native_runner import bind_native_agent, native_run_config, settle_native_stream
from .memory_autocapture import capture_completed_memory
from .native_live_state import restore_native_checkpoint


_ACTIVE_ROUTING_OBSERVATION: ContextVar[
    tuple[TurnObservation, str, str] | None
] = ContextVar("active_routing_observation", default=None)

_ACTIVE_TURN_PAUSE: ContextVar[asyncio.Event | None] = ContextVar("active_turn_pause", default=None)


logger = logging.getLogger(__name__)


def stop_after_commit(context: RunContextWrapper[Any], _results: Any) -> ToolsToFinalOutputResult:
    committed = getattr(context.context, "commit_result", None)
    return ToolsToFinalOutputResult(is_final_output=committed is not None, final_output=committed)


@dataclass
class AgentContext:
    project_id: str
    conversation_id: str
    backend: BackendClient
    raw_request: dict[str, Any] = field(default_factory=dict)
    idempotency_key: str = ""
    agent_turn_id: str = ""
    dispatch_generation: int = 0
    native_workspace: Any = field(default=None, repr=False, compare=False)
    native_workspace_checkpoint: Any = field(default=None, repr=False, compare=False)
    memory_archive_snapshot: Any = field(default=None, repr=False, compare=False)
    memory_archive_state: Any = field(default=None, repr=False, compare=False)
    memory_archive_queue: Any = field(default=None, repr=False, compare=False)
    skill_invocation_id: str = ""
    agent_task_id: str = ""
    agent_task_attempt_id: str = ""
    execution_attempt_id: str = ""
    memory_generation_id: str = ""
    memory_generation_attempt: int = 0
    attempt_token: str = field(default="", repr=False)
    background_output_mode: str = "structured"
    background_inputs: list[dict[str, Any]] = field(default_factory=list, repr=False)
    background_input_ids: list[str] = field(default_factory=list)
    background_input_hashes: list[str] = field(default_factory=list)
    background_included_input_ids: list[str] = field(default_factory=list)
    main_execution_phase: str = "execution"
    main_repair_attempt: int = 0
    main_repair_candidate: str = field(default="", repr=False)
    main_unstarted_input: Any = field(default=None, repr=False)
    main_inputs: list[dict[str, Any]] = field(default_factory=list, repr=False)
    main_input_ids: list[str] = field(default_factory=list)
    main_input_hashes: list[str] = field(default_factory=list)
    main_included_input_ids: list[str] = field(default_factory=list)
    commit_result: dict[str, Any] | None = None
    consulted_capabilities: set[str] = field(default_factory=set)
    routed_capabilities: set[str] = field(default_factory=set)
    capability_versions: dict[str, str] = field(default_factory=dict)
    loaded_capabilities: dict[str, dict[str, Any]] = field(default_factory=dict)
    previous_turn_consulted_capabilities: set[str] = field(default_factory=set)
    inspected_artifacts: list[dict[str, Any]] = field(default_factory=list)
    rejected_decisions: list[dict[str, Any]] = field(default_factory=list)
    resolved_source_artifact_version_ids: list[str] = field(default_factory=list)
    source_resolution_attempted: bool = False
    source_resolution_requires_binding: bool = True
    active_tool_calls: dict[str, dict[str, Any]] = field(default_factory=dict)
    # Only this live Runner needs the pause signal; durable facts are SDK items.
    external_tool_outcome_ids: list[str] = field(default_factory=list, repr=False)
    allowed_skill_scripts: set[tuple[str, str]] = field(default_factory=set)
    skill_script_snapshots: dict[str, dict[str, str]] = field(default_factory=dict)
    skill_tool_scope: Any = field(default=None, repr=False, compare=False)
    managed_instructions: dict[str, Any] | None = field(default=None, repr=False)
    managed_instruction_hash: str | None = None
    observation: TurnObservation | None = None


_AGENT_CONTEXT_CHECKPOINT_SCHEMA = "content_agent_context.v1"
_MAX_SERIALIZED_RUN_STATE_BYTES = 64 << 20


def _serialize_agent_context(value: Any) -> dict[str, Any]:
    if not isinstance(value, AgentContext):
        raise TypeError("RunState context is not an AgentContext")
    if value.memory_generation_id or value.memory_generation_attempt:
        raise TypeError("Memory generation requires its private SDK checkpoint adapter")
    return {
        "schema_version": _AGENT_CONTEXT_CHECKPOINT_SCHEMA,
        "project_id": value.project_id,
        "conversation_id": value.conversation_id,
        "raw_request": value.raw_request,
        "idempotency_key": value.idempotency_key,
        "agent_turn_id": value.agent_turn_id,
        "skill_invocation_id": value.skill_invocation_id,
        "agent_task_id": value.agent_task_id,
        "agent_task_attempt_id": value.agent_task_attempt_id,
        "execution_attempt_id": value.execution_attempt_id,
        "native_workspace_checkpoint": (value.native_workspace_checkpoint.model_dump() if value.native_workspace_checkpoint is not None else None),
        "managed_instruction_hash": value.managed_instruction_hash,
        "background_output_mode": value.background_output_mode,
        "background_input_ids": value.background_input_ids,
        "background_input_hashes": value.background_input_hashes,
        "background_included_input_ids": value.background_included_input_ids,
        "main_execution_phase": value.main_execution_phase,
        "main_repair_attempt": value.main_repair_attempt,
        "main_repair_candidate": value.main_repair_candidate,
        "main_unstarted_input": value.main_unstarted_input,
        "main_input_ids": value.main_input_ids,
        "main_input_hashes": value.main_input_hashes,
        "main_included_input_ids": value.main_included_input_ids,
        "consulted_capabilities": sorted(value.consulted_capabilities),
        "routed_capabilities": sorted(value.routed_capabilities),
        "capability_versions": value.capability_versions,
        "loaded_capabilities": value.loaded_capabilities,
        "previous_turn_consulted_capabilities": sorted(
            value.previous_turn_consulted_capabilities
        ),
        "inspected_artifacts": value.inspected_artifacts,
        "rejected_decisions": value.rejected_decisions,
        "resolved_source_artifact_version_ids": (
            value.resolved_source_artifact_version_ids
        ),
        "source_resolution_attempted": value.source_resolution_attempted,
        "source_resolution_requires_binding": value.source_resolution_requires_binding,
        "observation": (
            value.observation.checkpoint() if value.observation is not None else None
        ),
    }


def _restore_agent_context(
    state_json: dict[str, Any],
    request: AgentExecutionRequest,
    backend: BackendClient,
    observation: TurnObservation,
) -> AgentContext:
    context_entry = state_json.get("context")
    payload = context_entry.get("context") if isinstance(context_entry, dict) else None
    if not isinstance(payload, dict) or payload.get("schema_version") != _AGENT_CONTEXT_CHECKPOINT_SCHEMA:
        raise RuntimeCompatibilityError(
            "Agents SDK RunState has no compatible application context",
            failure_stage="run_state_restore",
        )
    expected_turn_id = request.agent_turn_id or request.idempotency_key
    expected = {
        "project_id": request.project_id,
        "conversation_id": request.conversation_id,
        "idempotency_key": request.idempotency_key,
        "agent_turn_id": expected_turn_id,
        "agent_task_id": "",
        "agent_task_attempt_id": "",
        "execution_attempt_id": "",
    }
    if any(str(payload.get(key) or "") != value for key, value in expected.items()):
        raise RuntimeCompatibilityError(
            "Agents SDK RunState identity does not match the requested turn",
            failure_stage="run_state_restore",
        )
    raw_request = payload.get("raw_request")
    if not isinstance(raw_request, dict) or raw_request != request.request:
        raise RuntimeCompatibilityError(
            "Agents SDK RunState request does not match the durable turn",
            failure_stage="run_state_restore",
        )
    try:
        observation.resume_from_checkpoint(payload.get("observation"))
    except ValueError as exc:
        raise RuntimeCompatibilityError(
            "Agents SDK RunState observation checkpoint is invalid",
            failure_stage="run_state_restore",
        ) from exc
    context = AgentContext(
        project_id=request.project_id,
        conversation_id=request.conversation_id,
        backend=backend,
        raw_request=raw_request,
        idempotency_key=request.idempotency_key,
        agent_turn_id=expected_turn_id,
        observation=observation,
    )
    context.skill_invocation_id = str(payload.get("skill_invocation_id") or "")
    restore_native_checkpoint(context, payload)
    phase, repair_attempt = payload.get("main_execution_phase", "execution"), payload.get("main_repair_attempt", 0)
    if not isinstance(phase, str) or phase not in {"execution", "terminal_repair"} or type(repair_attempt) is not int or repair_attempt not in {0, 1} or (phase == "execution" and repair_attempt != 0):
        raise RuntimeCompatibilityError("Main execution checkpoint phase is invalid", failure_stage="run_state_restore")
    context.main_execution_phase, context.main_repair_attempt = phase, repair_attempt
    candidate = payload.get("main_repair_candidate", "")
    if not isinstance(candidate, str):
        raise RuntimeCompatibilityError("Main repair checkpoint candidate is invalid", failure_stage="run_state_restore")
    context.main_repair_candidate = candidate
    initial_input = payload.get("main_unstarted_input")
    if initial_input is not None:
        try:
            validate_unstarted_input(state_json, initial_input)
        except ValueError as exc:
            raise RuntimeCompatibilityError(str(exc), failure_stage="run_state_restore") from exc
    context.main_unstarted_input = initial_input
    context.main_inputs = request.additional_inputs
    context.main_input_ids = payload.get("main_input_ids", [])
    context.main_input_hashes = payload.get("main_input_hashes", [])
    context.main_included_input_ids = payload.get("main_included_input_ids", [])
    try:
        main_input_batch(context)
    except ValueError as exc:
        raise RuntimeCompatibilityError(str(exc), failure_stage="run_state_restore") from exc
    context.consulted_capabilities.update(
        _checkpoint_string_list(payload, "consulted_capabilities")
    )
    context.routed_capabilities.update(
        _checkpoint_string_list(payload, "routed_capabilities")
    )
    context.previous_turn_consulted_capabilities.update(
        _checkpoint_string_list(payload, "previous_turn_consulted_capabilities")
    )
    context.resolved_source_artifact_version_ids = _checkpoint_string_list(
        payload, "resolved_source_artifact_version_ids"
    )
    context.source_resolution_attempted = bool(
        payload.get("source_resolution_attempted")
    )
    context.source_resolution_requires_binding = payload.get("source_resolution_requires_binding", True) is not False
    versions = payload.get("capability_versions")
    if not isinstance(versions, dict) or not all(
        isinstance(key, str) and isinstance(value, str)
        for key, value in versions.items()
    ):
        raise RuntimeCompatibilityError(
            "Agents SDK RunState capability versions are invalid",
            failure_stage="run_state_restore",
        )
    context.capability_versions.update(versions)
    loaded = payload.get("loaded_capabilities")
    if isinstance(loaded, dict) and all(
        isinstance(key, str) and isinstance(value, dict)
        for key, value in loaded.items()
    ):
        context.loaded_capabilities.update(loaded)
    context.inspected_artifacts = _checkpoint_object_list(
        payload, "inspected_artifacts"
    )
    context.rejected_decisions = _checkpoint_object_list(
        payload, "rejected_decisions"
    )
    return context


def _checkpoint_string_list(payload: dict[str, Any], key: str) -> list[str]:
    value = payload.get(key, [])
    if not isinstance(value, list) or len(value) > 128 or not all(
        isinstance(item, str) and item.strip() and len(item) <= 256 for item in value
    ):
        raise RuntimeCompatibilityError(
            f"Agents SDK RunState field {key!r} is invalid",
            failure_stage="run_state_restore",
        )
    return list(dict.fromkeys(item.strip() for item in value))


def _checkpoint_object_list(payload: dict[str, Any], key: str) -> list[dict[str, Any]]:
    value = payload.get(key, [])
    if not isinstance(value, list) or len(value) > 128 or not all(
        isinstance(item, dict) for item in value
    ):
        raise RuntimeCompatibilityError(
            f"Agents SDK RunState field {key!r} is invalid",
            failure_stage="run_state_restore",
        )
    return value


def _apply_approval_decisions(
    state: RunState[Any, Agent[Any]],
    decisions: list[AgentToolApprovalDecision],
) -> None:
    interruptions = state.get_interruptions()
    by_call_id: dict[str, Any] = {}
    for item in interruptions:
        call_id = str(item.call_id or "").strip()
        if not call_id or call_id in by_call_id:
            raise RuntimeCompatibilityError(
                "Agents SDK RunState contains ambiguous approval identities",
                failure_stage="run_state_restore",
            )
        by_call_id[call_id] = item
    if not decisions:
        raise RuntimeCompatibilityError(
            "Agents SDK RunState was resumed without an approval decision",
            failure_stage="run_state_restore",
        )
    seen: set[str] = set()
    for decision in decisions:
        if decision.sdk_tool_call_id in seen:
            raise RuntimeCompatibilityError(
                "Agent approval decisions contain a duplicate tool call",
                failure_stage="run_state_restore",
            )
        seen.add(decision.sdk_tool_call_id)
        item = by_call_id.get(decision.sdk_tool_call_id)
        if item is None:
            raise RuntimeCompatibilityError(
                "Agent approval decision does not match the SDK RunState",
                failure_stage="run_state_restore",
            )
        if decision.action == "approve":
            state.approve(item)
        else:
            state.reject(
                item,
                rejection_message="The user rejected this tool call. Continue without executing it.",
            )


def _serialize_paused_run(result: Any, *, allow_no_approvals: bool = False) -> tuple[dict[str, Any], str, list[str]]:
    context_wrapper = getattr(result, "context_wrapper", None)
    context = getattr(context_wrapper, "context", None)
    if isinstance(context, AgentContext) and context.observation is not None:
        context.observation.ingest_result(result)
        context.observation.mark_resume_usage_baseline(result)
    state = result.to_state()
    state_json = state.to_json(
        context_serializer=_serialize_agent_context,
        strict_context=True,
    )
    encoded_size = len(
        json.dumps(state_json, ensure_ascii=False, separators=(",", ":")).encode(
            "utf-8"
        )
    )
    if encoded_size > _MAX_SERIALIZED_RUN_STATE_BYTES:
        raise RuntimeCompatibilityError(
            "Agents SDK RunState exceeds the durable checkpoint limit",
            failure_stage="run_state_checkpoint",
        )
    schema_version = str(state_json.get("$schemaVersion") or "").strip()
    if not schema_version or len(schema_version) > 64:
        raise RuntimeCompatibilityError(
            "Agents SDK RunState schema version is missing",
            failure_stage="run_state_checkpoint",
        )
    pending: list[str] = []
    for item in state.get_interruptions():
        call_id = str(item.call_id or "").strip()
        if not call_id or call_id in pending:
            raise RuntimeCompatibilityError(
                "Agents SDK approval item has no unique call ID",
                failure_stage="run_state_checkpoint",
            )
        pending.append(call_id)
    if (not pending and not allow_no_approvals) or len(pending) > 32:
        raise RuntimeCompatibilityError(
            "Agents SDK returned an invalid approval batch",
            failure_stage="run_state_checkpoint",
        )
    return state_json, schema_version, pending


class RuntimeCompatibilityError(RuntimeError):
    """The compatible provider did not satisfy the Agents SDK contract."""

    def __init__(
        self,
        message: str,
        *,
        failure_stage: str = "",
        observation: dict[str, Any] | None = None,
    ) -> None:
        super().__init__(message)
        self.failure_stage = failure_stage
        self.observation = observation or {}

    def bind_observation(self, observation: TurnObservation) -> None:
        observation.mark_failure(self.failure_stage)
        self.failure_stage = observation.failure_stage
        self.observation = observation.public()


def normalize_provider_error(exc: Exception) -> str:
    if isinstance(exc, NativeCompactionProtocolError):
        return str(exc)
    status_code = getattr(exc, "status_code", None)
    if isinstance(status_code, int):
        detail = _provider_error_detail(exc)
        message = (
            "model provider request failed "
            f"(HTTP {status_code}, {type(exc).__name__})"
        )
        return f"{message}: {detail}" if detail else message
    return f"Agents SDK run failed ({type(exc).__name__})"


def _provider_error_detail(exc: Exception) -> str:
    body = getattr(exc, "body", None)
    if isinstance(body, dict):
        error = body.get("error", body)
        if isinstance(error, dict):
            code = str(error.get("code", "")).strip()
            message = str(error.get("message", "")).strip()
            detail = ": ".join(part for part in (code, message) if part)
            if detail:
                return detail[:500]
    message = str(getattr(exc, "message", "")).strip()
    return message[:500]


@dataclass(frozen=True)
class AgentExecutionOutcome:
    exchange: dict[str, Any]
    observation: dict[str, Any]
    included_input_ids: list[str] = field(default_factory=list)


@dataclass(frozen=True)
class AgentExecutionPaused:
    run_state: dict[str, Any]
    run_state_schema: str
    pending_sdk_tool_call_ids: list[str]
    observation: dict[str, Any]
    user_requested: bool = False
    included_input_ids: list[str] = field(default_factory=list)
    recovery_reason: str = ""


def _require_streamed_checkpoint(pause: AgentExecutionPaused, emit: Any) -> None:
    if emit is None:
        reason = "model recovery" if pause.recovery_reason else "pause" if pause.user_requested else "approval"
        raise RuntimeCompatibilityError(
            f"Agent {reason} requires the streamed execution protocol",
            failure_stage="run_state_checkpoint",
        )


@dataclass(frozen=True)
class ArtifactSourceResolution:
    version_ids: list[str]
    requires_binding: bool


class AgentExecutionStream:
    """Streams safe lifecycle events while a full controlled turn executes."""

    def __init__(
        self,
        operation: Callable[
            [Callable[[dict[str, Any]], Awaitable[None]]],
            Awaitable[AgentExecutionOutcome | AgentExecutionPaused | dict[str, Any]],
        ],
        observation: TurnObservation | None = None,
        pause_requested: asyncio.Event | None = None,
        on_close: Callable[[], None] | None = None,
    ) -> None:
        self._queue: asyncio.Queue[dict[str, Any]] = asyncio.Queue()
        self._cancel_reason: str | None = None
        self._observation = observation or TurnObservation("unknown", "unknown")
        self._pause_requested = pause_requested
        self._on_close = on_close
        self._task = asyncio.create_task(operation(self._queue.put))

    def pause(self) -> bool:
        if self._cancel_reason is not None or self._task.done() or self._pause_requested is None:
            return False
        self._pause_requested.set()
        return True

    def cancel(self, reason: str = "client_request") -> bool:
        if self._cancel_reason is not None or self._task.done():
            return False
        self._cancel_reason = reason
        self._observation.mark_cancelled(reason)
        self._task.cancel()
        return True

    async def aclose(self, reason: str = "go_disconnected") -> None:
        self.cancel(reason)
        # HTTP disconnect cancellation must not interrupt the operation's own
        # bounded tool/workspace cleanup or release its turn reservation early.
        try:
            with anyio.CancelScope(shield=True):
                with suppress(asyncio.CancelledError, Exception):
                    await asyncio.shield(self._task)
        finally:
            if self._on_close is not None:
                release, self._on_close = self._on_close, None
                if self._task.done():
                    release()
                else:
                    # A caller cancelled during shielded cleanup must not free
                    # the execution identity while the operation still runs.
                    self._task.add_done_callback(lambda _task: release())

    async def events(self) -> AsyncIterator[dict[str, Any]]:
        try:
            while True:
                if self._task.done() and self._queue.empty():
                    break
                queue_item = asyncio.create_task(self._queue.get())
                try:
                    done, _ = await asyncio.wait(
                        {queue_item, self._task}, return_when=asyncio.FIRST_COMPLETED
                    )
                finally:
                    if not queue_item.done():
                        queue_item.cancel()
                    with suppress(asyncio.CancelledError):
                        await queue_item
                if queue_item in done:
                    yield queue_item.result()
                    continue
            if self._cancel_reason is not None:
                with suppress(asyncio.CancelledError):
                    await self._task
                yield {
                    "event": "agent.turn.cancelled",
                    "data": {
                        "reason": self._cancel_reason,
                        "observation": self._observation.public(),
                    },
                }
                return
            outcome = await self._task
            if isinstance(outcome, AgentExecutionPaused):
                yield {
                    "event": "agent.turn.paused" if outcome.user_requested else "agent.turn.waiting_approval",
                    "data": {
                        "run_state": outcome.run_state,
                        "run_state_schema": outcome.run_state_schema,
                        "pending_sdk_tool_call_ids": (
                            outcome.pending_sdk_tool_call_ids
                        ),
                        "observation": outcome.observation,
                        **({"recovery_reason": outcome.recovery_reason} if outcome.recovery_reason else {}),
                        **({"included_input_ids": outcome.included_input_ids} if outcome.included_input_ids else {}),
                    },
                }
                return
            if isinstance(outcome, AgentExecutionOutcome):
                exchange = outcome.exchange
                observation = outcome.observation
            else:
                exchange = outcome
                observation = self._observation.public()
            yield {
                "event": "agent.turn.committed",
                "data": {"exchange": exchange, "observation": observation,
                         **({"included_input_ids": outcome.included_input_ids} if isinstance(outcome, AgentExecutionOutcome) and outcome.included_input_ids else {})},
            }
        except (asyncio.CancelledError, GeneratorExit):
            await self.aclose()
            raise
        except RuntimeCompatibilityError as exc:
            if not exc.observation:
                exc.bind_observation(self._observation)
            raise
        except Exception as exc:
            wrapped = RuntimeCompatibilityError(normalize_provider_error(exc), failure_stage=getattr(exc, "failure_stage", ""))
            wrapped.bind_observation(self._observation)
            raise wrapped from exc
        finally:
            if self._on_close is not None:
                await self.aclose()


def normalize_stream_event(event: Any) -> dict[str, Any] | None:
    event_type = getattr(event, "type", "")
    if event_type == "agent_updated_stream_event":
        agent = getattr(event, "new_agent", None)
        return {
            "event": "agent.updated",
            "data": {"name": getattr(agent, "name", "")},
        }
    if event_type == "run_item_stream_event":
        name = getattr(event, "name", "")
        item = getattr(event, "item", None)
        raw_item = getattr(item, "raw_item", None)
        raw_type = raw_item.get("type") if isinstance(raw_item, dict) else getattr(raw_item, "type", "")
        if raw_type == "program":
            return {"event": "agent.tool.started", "data": {"name": "programmatic_tool_calling"}}
        if raw_type == "program_output":
            status = raw_item.get("status") if isinstance(raw_item, dict) else getattr(raw_item, "status", None)
            if status not in {"completed", "incomplete"}:
                return None
            return {"event": "agent.tool.completed", "data": {"tool_name": "programmatic_tool_calling", "status": status}}
        if name in {"tool_called", "handoff_requested"}:
            item = getattr(event, "item", None)
            raw_item = getattr(item, "raw_item", None)
            return {
                "event": "agent.tool.started",
                "data": {"name": getattr(raw_item, "name", "")},
            }
        if name in {"tool_output", "handoff_occured", "handoff_occurred"}:
            return {"event": "agent.tool.completed", "data": {}}
        return None
    if event_type == "raw_response_event":
        data = getattr(event, "data", None)
        delta = getattr(data, "delta", None)
        if isinstance(delta, str) and delta:
            return {"event": "agent.output.delta", "data": {"text": delta}}
    return None


def normalize_execution_stream_event(event: Any) -> dict[str, Any] | None:
    """Expose tool lifecycle only; raw model deltas contain control JSON."""

    normalized = normalize_stream_event(event)
    if normalized is None or normalized["event"] == "agent.output.delta":
        return None
    data = normalized.get("data", {})
    if normalized["event"] == "agent.tool.started":
        tool_name = str(data.get("name") or "").strip()
        if not tool_name:
            return None
        return {
            "event": "agent.tool.started",
            "data": {"tool_name": tool_name},
        }
    return normalized


@function_tool
async def list_capabilities(ctx: RunContextWrapper[AgentContext]) -> str:
    """List capability plug-ins currently registered by the Go runtime."""
    payload = await ctx.context.backend.get_capabilities(ctx.context.project_id)
    return json.dumps(payload, ensure_ascii=False)


@function_tool
async def load_skill_instructions(
    ctx: RunContextWrapper[AgentContext], capability_id: str
) -> str:
    """Load a selected Skill or discover an available inline Skill that allows implicit invocation in this execution's frozen catalog. Do not use a Skill the user declined or only asked about. Its dependencies retain their approval policy. Use read_skill_resource if truncated."""
    return await _load_skill_instructions(ctx.context, capability_id)


async def _load_skill_instructions(
    context: AgentContext, capability_id: str
) -> str:
    capability_id = capability_id.strip()
    explicit = context.raw_request.get("capability_ref")
    explicitly_selected = (
        isinstance(explicit, dict)
        and str(explicit.get("capability_id") or "").strip() == capability_id
    )
    discovered = None
    tool_scope = context.skill_tool_scope
    pending_install = tool_scope is not None and capability_id in tool_scope.pending_installed_ids
    if pending_install or capability_id not in context.routed_capabilities and not explicitly_selected:
        discovered = tool_scope.discoverable_capabilities.get(capability_id) if tool_scope is not None else None
        if not isinstance(discovered, dict) or discovered.get("status") != "available" or discovered.get("execution_mode") != "inline" or not (discovered.get("skill") or {}).get("content_hash"):
            raise ValueError("该 Skill 不在本轮已验证的可用 inline 目录中。")
        if not pending_install and not allows_implicit_skill_invocation(discovered):
            raise ValueError("该 Skill 仅允许明确选中后调用。")
    version = context.capability_versions.get(capability_id, "")
    if discovered is not None:
        version = str(discovered.get("version") or "")
        if not version:
            raise ValueError("目录 Skill 缺少锁定版本。")
    if explicitly_selected and not version:
        version = str(explicit.get("version") or "").strip()
    payload = await context.backend.get_capability(
        capability_id, context.project_id, version
    )
    data = payload.get("data") if isinstance(payload, dict) else None
    if not isinstance(data, dict) or data.get("status") != "available":
        raise ValueError("该 Skill 当前不可用。")
    if data.get("capability_id") != capability_id:
        raise ValueError("Skill 详情与请求不一致。")
    if version and data.get("version") != version:
        raise ValueError("Skill 详情版本与本轮锁定版本不一致。")
    skill = data.get("skill")
    if discovered is not None:
        frozen_skill = discovered["skill"]
        if data.get("execution_mode") != "inline" or not isinstance(skill, dict) or any(skill.get(key) != frozen_skill.get(key) for key in ("content_hash", "scope", "scope_ref")):
            raise ValueError("Skill 详情与冻结目录不一致。")
        await tool_scope.activate_skill(data)
    instructions = skill.get("instructions") if isinstance(skill, dict) else None
    if isinstance(instructions, str) and len(instructions) > 48000:
        skill = dict(skill)
        skill["instructions"] = instructions[:48000]
        skill["instructions_truncated"] = True
        skill["full_instructions_resource"] = "SKILL.md"
    projection = {
        key: data.get(key)
        for key in (
            "capability_id",
            "version",
            "label",
            "description",
            "kind",
            "execution_mode",
            "input_binding",
            "accepted_asset_kinds",
            "default_config_ref",
            "config_options",
            "input_schema",
            "config_schemas",
            "commands",
            "entry_policy",
            "routing",
        )
    }
    projection["skill"] = skill
    context.consulted_capabilities.add(capability_id)
    context.loaded_capabilities[capability_id] = data
    return json.dumps(projection, ensure_ascii=False)


async def _skill_resources(
    context: AgentContext, capability_id: str, *, path: str = "",
    offset: int = 0, limit: int = 16000,
) -> Any:
    definition = context.loaded_capabilities.get(capability_id)
    if not isinstance(definition, dict) or not isinstance(definition.get("skill"), dict):
        raise ValueError("请先加载本轮选中 Skill 的指令，再读取其资源。")
    version = str(definition.get("version") or "")
    if not version:
        raise ValueError("Skill 缺少已锁定版本。")
    if offset < 0 or not 1 <= limit <= 16000:
        raise ValueError("offset 必须非负，limit 必须为 1..16000。")
    payload = await context.backend.get_skill_resources(
        context.project_id, capability_id, version, path=path, offset=offset, limit=limit,
    )
    from .skill_resources import skill_resource_output

    return skill_resource_output(payload)


@function_tool
async def list_skill_resources(ctx: RunContextWrapper[AgentContext], capability_id: str) -> str:
    """List reference, asset, script and SKILL.md paths in a loaded Skill version."""
    return await _skill_resources(ctx.context, capability_id)


@function_tool
async def read_skill_resource(
    ctx: RunContextWrapper[AgentContext], capability_id: str, path: str,
    offset: int = 0, limit: int = 16000,
) -> Any:
    """Read Skill text, images or PDF/DOCX files. Page text via next_offset; binary files require offset=0."""
    if not path.strip():
        raise ValueError("资源路径不能为空。")
    return await _skill_resources(ctx.context, capability_id, path=path, offset=offset, limit=limit)


@function_tool
async def inspect_project(ctx: RunContextWrapper[AgentContext]) -> str:
    """Read a compact authoritative project overview and pending work."""
    payload = await ctx.context.backend.get_project_overview(ctx.context.project_id)
    return json.dumps(payload, ensure_ascii=False)


@function_tool
async def inspect_project_goal(ctx: RunContextWrapper[AgentContext]) -> str:
    """Read the project's current authoritative Goal, if one is active."""
    payload = await ctx.context.backend.get_project_goal(ctx.context.project_id)
    return json.dumps(payload, ensure_ascii=False)


@function_tool
async def list_project_assets(ctx: RunContextWrapper[AgentContext]) -> str:
    """List uploaded assets and exact current snapshot IDs for this project."""
    payload = await ctx.context.backend.get_project_assets(ctx.context.project_id)
    return json.dumps(payload, ensure_ascii=False)


@function_tool
async def inspect_text_asset(
    ctx: RunContextWrapper[AgentContext],
    asset_id: str,
    asset_snapshot_id: str,
    offset: int = 0,
    limit: int = 48000,
) -> str:
    """Read a text/document snapshot page. Follow next_offset when truncated; offsets count Unicode characters."""
    if offset < 0 or not 1 <= limit <= 48000:
        raise ValueError("offset 必须非负，limit 必须为 1..48000。")
    metadata_result = await ctx.context.backend.get_asset(asset_id)
    metadata = ctx.context.backend._data(metadata_result)
    if not isinstance(metadata, dict):
        raise ValueError("材料信息无效。")
    if metadata.get("project_id") != ctx.context.project_id:
        raise ValueError("材料不属于当前项目。")
    if metadata.get("current_snapshot_id") != asset_snapshot_id:
        raise ValueError("材料快照已过期。")
    if metadata.get("kind") not in {"text", "document"}:
        raise ValueError("该材料不是可读取的文本或文档。")
    payload = await ctx.context.backend.get_parsed_asset_text(
        asset_id, asset_snapshot_id
    )
    data = ctx.context.backend._data(payload)
    if not isinstance(data, dict) or not isinstance(data.get("content"), str):
        raise ValueError("材料解析文本无效。")
    content = data["content"]
    if offset > len(content):
        raise ValueError("offset 超出材料文本长度。")
    end = min(offset + limit, len(content))
    data = dict(data)
    data.update(content=content[offset:end], offset=offset, next_offset=end,
                total_chars=len(content), truncated=end < len(content))
    return json.dumps(data, ensure_ascii=False)


@function_tool
async def search_artifacts(
    ctx: RunContextWrapper[AgentContext],
    query: str = "",
    artifact_type: str = "",
    limit: int = 20,
) -> str:
    """Search current project artifacts by title, type, scope, or capability."""
    payload = await ctx.context.backend.search_artifacts(
        ctx.context.project_id,
        query=query,
        artifact_type=artifact_type,
        limit=limit,
    )
    return json.dumps(payload, ensure_ascii=False)


@function_tool
async def inspect_current_artifact(
    ctx: RunContextWrapper[AgentContext], artifact_id: str
) -> str:
    """Read an artifact and its exact current version after finding its ID."""
    payload = await ctx.context.backend.get_current_artifact_version(artifact_id)
    ctx.context.inspected_artifacts.append(payload)
    return json.dumps(payload, ensure_ascii=False)


@function_tool
async def inspect_artifact_version(
    ctx: RunContextWrapper[AgentContext], artifact_version_id: str
) -> str:
    """Read one exact artifact version when the user targets existing content."""
    payload = await ctx.context.backend.get_artifact_version(artifact_version_id)
    ctx.context.inspected_artifacts.append(payload)
    return json.dumps(payload, ensure_ascii=False)


@function_tool
async def get_artifact_downloads(
    ctx: RunContextWrapper[AgentContext], artifact_version_id: str
) -> str:
    """Get verified download formats and URLs for one saved document/table version.

    Find the exact version with the artifact inspection tools first. Return only
    receipt URLs, including its version/status and format warnings. This does not
    save a file to the user's computer or export an unsaved proposal.
    """
    payload = await ctx.context.backend.get_artifact_delivery(artifact_version_id)
    if payload.get("project_id") != ctx.context.project_id or payload.get("artifact_version_id") != artifact_version_id:
        raise ValueError("Artifact download receipt does not match the project/version")
    return json.dumps(payload, ensure_ascii=False)


@function_tool
async def inspect_run(ctx: RunContextWrapper[AgentContext], run_id: str) -> str:
    """Read one exact business Run snapshot, including steps and approval state."""
    payload = await ctx.context.backend.get_run_snapshot(run_id)
    return json.dumps(payload, ensure_ascii=False)


@function_tool
async def set_episode_execution_mode(
    ctx: RunContextWrapper[AgentContext], run_id: str, mode: str
) -> str:
    """Switch a text-to-script Run between continuous and review_each at a safe episode boundary."""
    if mode not in {"continuous", "review_each"}:
        raise ValueError("mode 必须是 continuous 或 review_each。")
    payload = await ctx.context.backend.set_episode_execution_mode(run_id, mode)
    return json.dumps(payload, ensure_ascii=False)


@function_tool
async def inspect_recent_conversation(
    ctx: RunContextWrapper[AgentContext], limit: int = 20
) -> str:
    """Read recent authoritative messages for this conversation when needed."""
    payload = await ctx.context.backend.get_conversation_messages(
        ctx.context.conversation_id, limit=limit
    )
    return json.dumps(payload, ensure_ascii=False)


@function_tool
async def search_conversation_history(
    ctx: RunContextWrapper[AgentContext], query: str, limit: int = 20
) -> str:
    """Search older authoritative messages when recent Session history is insufficient."""
    payload = await ctx.context.backend.search_conversation_messages(
        ctx.context.conversation_id, query=query, limit=limit
    )
    return json.dumps(payload, ensure_ascii=False)


@function_tool(failure_error_function=None)
async def execute_skill_script(
    ctx: RunContextWrapper[AgentContext],
    skill_name: str,
    script_id: str,
    input_json: str,
    capability_id: str = "",
) -> str:
    """Execute one declared script from the selected Skill in the isolated OCI sandbox.

    Args:
        skill_name: Exact Skill name from the selected Skill metadata.
        script_id: Exact script ID declared by that Skill.
        input_json: JSON-encoded input value; use ``{}`` when no input is needed.
        capability_id: Exact selected capability ID, required when names and script IDs collide.
            Prefer always supplying it; an empty value only supports unambiguous legacy calls.
    """
    sdk_tool_call_id = str(getattr(ctx, "tool_call_id", ""))
    active = ctx.context.active_tool_calls.get(sdk_tool_call_id)
    if not isinstance(active, dict):
        raise RuntimeError("audited Skill script call context is missing")
    agent_tool_call_id = str(active.get("agent_tool_call_id") or "")
    if not agent_tool_call_id:
        raise RuntimeError("audited Skill script call ID is missing")
    try:
        json.loads(input_json)
    except json.JSONDecodeError as exc:
        raise ValueError("input_json must contain exactly one valid JSON value") from exc
    arguments = {
        "skill_name": skill_name,
        "script_id": script_id,
        "input_json": input_json,
    }
    raw_arguments = json.loads(getattr(ctx, "tool_arguments", "{}"))
    if capability_id or "capability_id" in raw_arguments:
        arguments["capability_id"] = capability_id
    registered = active.get("_skill_script_arguments")
    if registered is not None:
        if registered != arguments:
            raise AgentToolConfigurationError("Skill script arguments changed after approval registration")
    else:
        selected_skill_script_snapshot(ctx.context, arguments)
    result = await ctx.context.backend.execute_skill_script(
        agent_tool_call_id, sdk_tool_call_id, arguments
    )
    return json.dumps(result, ensure_ascii=False)


async def _build_commit_payload(
    context: AgentContext, decision: ControlDecision
) -> dict[str, Any]:
    if decision.intent == "revise" and not _explicit_revision_request(
        context.raw_request
    ):
        raise ValueError(
            "本轮没有明确的修改动作或选区；执行既定方案应继续生成，不得因界面焦点改写现有产物。"
        )
    if (
        decision.intent == "clarify"
        and decision.clarification is not None
        and any("skill" in option.casefold() for option in decision.clarification.options)
        and not context.consulted_capabilities
        and not context.previous_turn_consulted_capabilities
    ):
        raise ValueError(
            "未咨询 Skill 专家时不得提供 Skill 选项；已定位的局部修改应直接提交 revise。"
        )
    clarification = (
        decision.clarification.model_dump(mode="json")
        if decision.clarification is not None
        else None
    )
    reply = decision.reply.strip()
    if clarification is not None and not reply:
        reply = str(clarification["question"])

    capability_ref: dict[str, Any] | None = None
    proposed_action: dict[str, Any] | None = None
    if (
        decision.intent in {"chat", "create_artifact"}
        and context.consulted_capabilities
        and not _explicit_generic_agent_choice(
            str(context.raw_request.get("content") or "")
        )
    ):
        suggestion = await _skill_suggestion_payload(context, decision.confidence)
        if suggestion is not None:
            return suggestion
        selected_inline_id = ""
        if (
            decision.capability_id
            and decision.capability_id in context.consulted_capabilities
        ):
            selected_inline_id = decision.capability_id
        elif len(context.consulted_capabilities) == 1:
            selected_inline_id = next(iter(context.consulted_capabilities))
        if selected_inline_id:
            loaded = context.loaded_capabilities.get(selected_inline_id)
            if not isinstance(loaded, dict):
                registry = await context.backend.get_capabilities(context.project_id)
                loaded = next(
                    (
                        item
                        for item in _capability_items(registry)
                        if item.get("capability_id") == selected_inline_id
                        and item.get("status") == "available"
                    ),
                    None,
                )
            if isinstance(loaded, dict) and loaded.get("execution_mode") == "inline":
                explicit = context.raw_request.get("capability_ref")
                capability_ref = {
                    "capability_id": selected_inline_id,
                    "version": loaded["version"],
                    "selection_mode": (
                        "explicit"
                        if isinstance(explicit, dict)
                        and explicit.get("capability_id") == selected_inline_id
                        else "inferred"
                    ),
                }
    if decision.intent == "propose_capability":
        if (
            context.source_resolution_attempted
            and context.source_resolution_requires_binding
            and not context.resolved_source_artifact_version_ids
        ):
            question = "没有唯一定位到本次指定的来源产物，请明确选择一个现有产物后再启动。"
            return {
                "reply": question,
                "intent": "clarify",
                "confidence": 0.0,
                "capability_ref": None,
                "target_ref": None,
                "clarification": {"question": question, "options": []},
                "proposed_action": None,
                "artifact_draft": None,
                "goal_update": None,
            }
        explicit = context.raw_request.get("capability_ref")
        explicitly_selected = (
            isinstance(explicit, dict)
            and explicit.get("capability_id") == decision.capability_id
        )
        consultation_is_current = (
            decision.capability_id in context.consulted_capabilities
        )
        consultation_is_confirmed_previous = (
            decision.skill_confirmed
            and _explicit_skill_confirmation(
                str(context.raw_request.get("content") or "")
            )
            and decision.capability_id
            in context.previous_turn_consulted_capabilities
        )
        if (
            not explicitly_selected
            and not consultation_is_current
            and not consultation_is_confirmed_previous
        ):
            question = "请再确认素材类型和希望得到的结果，以便选择正确的 Skill。"
            return {
                "reply": question,
                "intent": "clarify",
                "confidence": 0.0,
                "capability_ref": None,
                "target_ref": None,
                "clarification": {"question": question, "options": []},
                "proposed_action": None,
                "artifact_draft": None,
                "goal_update": None,
            }
        registry = await context.backend.get_capabilities(context.project_id)
        data = registry.get("data", {})
        items = data.get("items", []) if isinstance(data, dict) else []
        capability = next(
            (
                item
                for item in items
                if isinstance(item, dict)
                and item.get("capability_id") == decision.capability_id
                and item.get("status") == "available"
            ),
            None,
        )
        if capability is None:
            question = "请重新选择一个当前可用的 Skill，或说明希望处理的素材类型。"
            return {
                "reply": question,
                "intent": "clarify",
                "confidence": 0.0,
                "capability_ref": None,
                "target_ref": None,
                "clarification": {"question": question, "options": []},
                "proposed_action": None,
                "artifact_draft": None,
                "goal_update": None,
            }
        if not explicitly_selected and not decision.skill_confirmed:
            suggestion = await _skill_suggestion_payload(context, decision.confidence)
            if suggestion is not None:
                return suggestion
        capability_ref = {
            "capability_id": decision.capability_id,
            "version": capability["version"],
            "selection_mode": (
                "explicit"
                if isinstance(explicit, dict)
                and explicit.get("capability_id") == decision.capability_id
                else "inferred"
            ),
        }
        execution_mode = str(capability.get("execution_mode") or "")
        if execution_mode == "inline":
            raise ValueError(
                "inline Skill 必须在当前 Agent turn 内完成，不能创建 Task 或 Run。"
            )
        requires_confirmation = bool(
            (capability.get("entry_policy") or {}).get(
                "requires_user_confirmation", False
            )
        )
        default_config_ref = str(capability.get("default_config_ref") or "creation")
        source_artifact_version_ids = (
            context.resolved_source_artifact_version_ids
            if context.source_resolution_attempted
            else decision.source_artifact_version_ids
        )
        proposed_input = {}
        if source_artifact_version_ids:
            proposed_input["artifact_versions"] = [
                {
                    "artifact_version_id": version_id,
                    "role": "primary_source",
                    "order": index + 1,
                }
                for index, version_id in enumerate(
                    source_artifact_version_ids
                )
            ]
        proposed_action = {
            "action_type": (
                "start_background_task"
                if execution_mode == "background_task"
                else "collect_run_configuration"
            ),
            "capability_ref": capability_ref,
            "input": proposed_input,
            "config": (
                decision.capability_config
                if execution_mode == "background_task"
                else (
                    {"config_ref": default_config_ref, "payload": decision.capability_config}
                    if decision.capability_config
                    else {}
                )
            ),
            "requires_confirmation": (
                requires_confirmation
                if execution_mode == "background_task"
                else False
            ),
        }
        label = str(capability.get("label") or decision.capability_id)
        if execution_mode == "background_task" and not requires_confirmation:
            reply = f"已将「{label}」加入后台任务。"
        elif execution_mode == "background_task":
            reply = f"已为「{label}」准备后台任务，请确认后启动。"
        else:
            reply = f"已为「{label}」Skill 准备配置卡，请确认配置后再启动任务。"

    artifact_draft = (
        decision.artifact_draft.model_dump(mode="json")
        if decision.artifact_draft is not None
        else None
    )
    artifact_drafts = [
        draft.model_dump(mode="json") for draft in decision.artifact_drafts
    ]
    target_ref = (
        decision.target_ref.model_dump(mode="json")
        if decision.target_ref is not None
        else None
    )
    return {
        "reply": reply,
        "intent": decision.intent,
        "confidence": decision.confidence,
        "source_artifact_version_ids": decision.source_artifact_version_ids,
        "capability_ref": capability_ref,
        "target_ref": target_ref,
        "clarification": clarification,
        "proposed_action": proposed_action,
        "artifact_draft": artifact_draft,
        "artifact_drafts": artifact_drafts,
        "goal_update": (
            decision.goal_update.model_dump(mode="json")
            if decision.goal_update is not None
            else None
        ),
    }


async def _skill_suggestion_payload(
    context: AgentContext, confidence: float
) -> dict[str, Any] | None:
    if not context.consulted_capabilities:
        return None
    registry = await context.backend.get_capabilities(context.project_id)
    data = registry.get("data", {})
    items = data.get("items", []) if isinstance(data, dict) else []
    matched = next(
        (
            item
            for item in items
            if isinstance(item, dict)
            and item.get("capability_id") in context.consulted_capabilities
            and item.get("status") == "available"
        ),
        None,
    )
    if matched is None:
        return None
    entry_policy = matched.get("entry_policy")
    if isinstance(entry_policy, dict) and not entry_policy.get(
        "requires_user_confirmation", False
    ):
        return None
    label = str(matched.get("label") or matched.get("capability_id"))
    question = (
        f"这个需求适合使用「{label}」Skill。"
        "你希望使用该 Skill 的完整生产流程，还是由通用 Agent 直接生成普通产物？"
    )
    return {
        "reply": question,
        "intent": "clarify",
        "confidence": confidence,
        "capability_ref": None,
        "target_ref": None,
        "clarification": {
            "question": question,
            "options": ["使用 Skill", "由通用 Agent 直接生成"],
        },
        "proposed_action": None,
        "artifact_draft": None,
        "artifact_drafts": [],
        "goal_update": None,
    }


def _explicit_generic_agent_choice(content: str) -> bool:
    normalized = content.strip().casefold().replace(" ", "")
    return any(
        cue in normalized
        for cue in (
            "通用agent",
            "通用代理",
            "不用skill",
            "不使用skill",
            "不要skill",
            "直接生成普通产物",
        )
    )


def _explicit_revision_request(raw_request: dict[str, Any]) -> bool:
    selection = raw_request.get("selection_snapshot")
    if isinstance(selection, dict) and isinstance(selection.get("selection"), dict):
        return True
    normalized = re.sub(
        r"\s+", "", str(raw_request.get("content") or "").strip().casefold()
    )
    return any(
        cue in normalized
        for cue in (
            "修改",
            "改一下",
            "改成",
            "调整",
            "替换",
            "删除",
            "删掉",
            "补充到",
            "追加到",
            "加到",
            "续写",
            "继续写",
            "扩写",
            "润色",
            "重写",
            "完善这个",
            "降低",
            "提高",
            "缩短",
            "延长",
        )
    )


def _has_attachment_sources(raw_request: dict[str, Any]) -> bool:
    references = raw_request.get("attachment_refs")
    return isinstance(references, list) and any(
        isinstance(reference, dict)
        and bool(str(reference.get("asset_id") or "").strip())
        and bool(str(reference.get("asset_snapshot_id") or "").strip())
        for reference in references
    )


def _explicit_skill_confirmation(content: str) -> bool:
    normalized = content.strip().casefold().replace(" ", "")
    if _explicit_generic_agent_choice(content):
        return False
    return "skill" in normalized and any(
        cue in normalized for cue in ("使用", "采用", "选择", "就用", "确认")
    )


def _inherit_recent_material_context(
    raw_request: dict[str, Any], payload: dict[str, Any]
) -> None:
    """Carry material only across an adjacent, explicit Skill confirmation."""

    if not _explicit_skill_confirmation(str(raw_request.get("content") or "")):
        return
    references = raw_request.get("attachment_refs")
    client_context = raw_request.get("client_context")
    if not isinstance(client_context, dict):
        client_context = {}
        raw_request["client_context"] = client_context
    if (
        isinstance(references, list)
        and references
        or client_context.get("current_asset_set_version_id")
    ):
        return

    items = payload.get("items")
    if not isinstance(items, list) or not items:
        return
    latest = items[-1]
    if (
        not isinstance(latest, dict)
        or latest.get("role") != "assistant"
        or "skill" not in str(latest.get("content") or "").casefold()
        or "你希望使用" not in str(latest.get("content") or "")
    ):
        return
    for item in reversed(items[:-1]):
        if not isinstance(item, dict) or item.get("role") != "user":
            continue
        message_context = item.get("message_context")
        if not isinstance(message_context, dict):
            return
        inherited = message_context.get("attachment_refs")
        if isinstance(inherited, list) and inherited:
            raw_request["attachment_refs"] = inherited
        previous_client = message_context.get("client_context")
        if isinstance(previous_client, dict):
            asset_set_version_id = previous_client.get(
                "current_asset_set_version_id"
            )
            if asset_set_version_id:
                client_context["current_asset_set_version_id"] = asset_set_version_id
        return


def _previous_turn_consulted_capabilities(
    session_items: list[dict[str, Any]],
) -> set[str]:
    last_user_index = max(
        (
            index
            for index, item in enumerate(session_items)
            if item.get("role") == "user"
        ),
        default=-1,
    )
    result: set[str] = set()
    for item in session_items[last_user_index + 1 :]:
        if item.get("type") != "function_call":
            continue
        name = str(item.get("name") or "")
        if name == "load_skill_instructions":
            arguments = item.get("arguments")
            if isinstance(arguments, str):
                try:
                    arguments = json.loads(arguments)
                except json.JSONDecodeError:
                    arguments = None
            if isinstance(arguments, dict):
                capability_id = str(arguments.get("capability_id") or "").strip()
                if capability_id:
                    result.add(capability_id)
            continue
        legacy = re.fullmatch(r"consult_([a-z][a-z0-9_]*)_skill", name)
        if legacy is not None:
            result.add(legacy.group(1))
    return result


def _capability_items(payload: dict[str, Any]) -> list[dict[str, Any]]:
    data = payload.get("data", {})
    items = data.get("items", []) if isinstance(data, dict) else []
    return [item for item in items if isinstance(item, dict)]


def _routing_skill_catalog(
    capability_items: list[dict[str, Any]],
    *,
    auto_route_only: bool,
    character_budget: int = 8000,
    mentioned_ids: set[str] | None = None,
) -> list[dict[str, Any]]:
    mentioned_ids = mentioned_ids or set()
    candidates: list[dict[str, Any]] = []
    ordered = sorted(
        capability_items,
        key=lambda item: (
            str(item.get("capability_id") or "") not in mentioned_ids,
            int((item.get("ui_entry") or {}).get("menu_order") or 100000),
            str(item.get("capability_id") or ""),
        ),
    )
    used = 2
    for item in ordered:
        if item.get("status") != "available":
            continue
        capability_id = str(item.get("capability_id") or "").strip()
        if not capability_id:
            continue
        entry_policy = item.get("entry_policy")
        if not isinstance(entry_policy, dict):
            entry_policy = {}
        implicit = allows_implicit_skill_invocation(item)
        if auto_route_only and not implicit and capability_id not in mentioned_ids:
            continue
        routing = item.get("routing")
        if not isinstance(routing, dict):
            routing = {}
        skill = item.get("skill")
        if not isinstance(skill, dict):
            skill = {}
        candidate = {
            "capability_id": capability_id,
            "name": str(skill.get("name") or item.get("label") or capability_id)[:128],
            "path": str(skill.get("path") or "")[:1024],
            "description": str(item.get("description") or "")[:1024],
            "allow_implicit_invocation": implicit,
            "execution_mode": str(item.get("execution_mode") or ""),
            "accepted_asset_kinds": [
                str(kind)
                for kind in item.get("accepted_asset_kinds", [])
                if isinstance(kind, str)
            ][:12],
            "explicit_aliases": [
                str(alias)
                for alias in routing.get("explicit_aliases", [])
                if isinstance(alias, str)
            ][:12],
            "intent_examples": [
                str(example)
                for example in routing.get("intent_examples", [])
                if isinstance(example, str)
            ][:6],
            "requires_user_confirmation": bool(
                entry_policy.get("requires_user_confirmation", False)
            ),
        }
        encoded = json.dumps(candidate, ensure_ascii=False, separators=(",", ":"))
        if candidates and used + len(encoded) + 1 > max(512, character_budget):
            break
        candidates.append(candidate)
        used += len(encoded) + 1
    return candidates


def _mentioned_skill_ids(
    raw_request: dict[str, Any], capability_items: list[dict[str, Any]]
) -> set[str]:
    content = str(raw_request.get("content") or "").strip().casefold()
    if not content:
        return set()
    matches: set[str] = set()
    for item in capability_items:
        if item.get("status") != "available":
            continue
        capability_id = str(item.get("capability_id") or "")
        skill = item.get("skill") if isinstance(item.get("skill"), dict) else {}
        routing = item.get("routing") if isinstance(item.get("routing"), dict) else {}
        aliases = [capability_id, skill.get("name", ""), item.get("label", ""), *routing.get("explicit_aliases", [])]
        if any(
            len(str(alias).strip()) >= 3
            and str(alias).strip().casefold() in content
            for alias in aliases
        ):
            matches.add(capability_id)
    return matches


def _explicit_artifact_versions(
    raw_request: dict[str, Any],
    artifact_catalog: dict[str, Any],
    accepted_asset_kinds: set[str] | None = None,
) -> list[str]:
    def compatible(item: dict[str, Any]) -> bool:
        if accepted_asset_kinds is None:
            return True
        source_kind = {
            "generic_document": "document",
            "generic_table": "document",
        }.get(str(item.get("artifact_type") or ""))
        return source_kind in accepted_asset_kinds

    items = [
        item
        for item in artifact_catalog.get("items", [])
        if isinstance(item, dict) and compatible(item)
    ]
    client_context = raw_request.get("client_context")
    focused_version_id = (
        str(client_context.get("current_artifact_version_id") or "").strip()
        if isinstance(client_context, dict)
        else ""
    )
    if focused_version_id and any(
        item.get("current_version_id") == focused_version_id for item in items
    ):
        return [focused_version_id]

    request_text = re.sub(
        r"[\W_]+", "", json.dumps(raw_request, ensure_ascii=False).casefold()
    )
    matches: list[tuple[int, str]] = []
    for item in items:
        title = str(item.get("title", "")).strip()
        version_id = str(item.get("current_version_id", "")).strip()
        normalized_title = re.sub(r"[\W_]+", "", title.casefold())
        if normalized_title and version_id and normalized_title in request_text:
            matches.append((len(normalized_title), version_id))
    if not matches:
        return []
    longest = max(length for length, _version_id in matches)
    return list(
        dict.fromkeys(
            version_id
            for length, version_id in matches
            if length == longest
        )
    )


@function_tool(strict_mode=False)
async def commit_agent_action(
    ctx: RunContextWrapper[AgentContext], decision: ControlDecision
) -> str:
    """Commit exactly one validated terminal Agent decision through the Go Runtime."""
    if ctx.context.commit_result is not None:
        return json.dumps({"committed": True, "duplicate": True})
    task = asyncio.current_task()
    if task is not None and task.cancelling():
        raise asyncio.CancelledError
    try:
        if any(call.get("status") == "pending_approval" for call in ctx.context.active_tool_calls.values()):
            raise ValueError("A tool is awaiting approval. Preserve the SDK interruption; do not commit a terminal decision.")
        payload = await _build_commit_payload(ctx.context, decision)
    except ValueError as exc:
        ctx.context.rejected_decisions.append(decision.model_dump(mode="json"))
        return json.dumps(
            {"committed": False, "validation_error": str(exc)},
            ensure_ascii=False,
        )
    if task is not None and task.cancelling():
        raise asyncio.CancelledError
    native = ctx.context.native_workspace
    if native is not None:
        await native.before_commit()
    try:
        if ctx.context.observation is None:
            result = await ctx.context.backend.commit_agent_turn(
                ctx.context.project_id,
                ctx.context.conversation_id,
                ctx.context.raw_request,
                payload,
                ctx.context.idempotency_key,
                ctx.context.agent_turn_id,
            )
        else:
            with ctx.context.observation.measure("runtime_commit"):
                result = await ctx.context.backend.commit_agent_turn(
                    ctx.context.project_id,
                    ctx.context.conversation_id,
                    ctx.context.raw_request,
                    payload,
                    ctx.context.idempotency_key,
                    ctx.context.agent_turn_id,
                )
    except BaseException:
        if native is not None:
            native.commit_failed()
        raise
    ctx.context.commit_result = result
    return json.dumps({"committed": True}, ensure_ascii=False)


class RequiredCommitModelAdapter(Model):
    """Normalize a compatible gateway's JSON-only required-tool response for the SDK Runner."""

    def __init__(self, delegate: Model) -> None:
        self._delegate = delegate

    async def get_response(
        self,
        system_instructions: str | None,
        input: str | list[Any],
        model_settings: ModelSettings,
        tools: list[Any],
        output_schema: Any,
        handoffs: list[Any],
        tracing: ModelTracing,
        *,
        previous_response_id: str | None,
        conversation_id: str | None,
        prompt: Any,
    ) -> ModelResponse:
        response = await self._delegate.get_response(
            system_instructions,
            input,
            model_settings,
            tools,
            output_schema,
            handoffs,
            tracing,
            previous_response_id=previous_response_id,
            conversation_id=conversation_id,
            prompt=prompt,
        )
        output = self._control_output(response.output)
        if output is response.output:
            return response
        return ModelResponse(
            output=output, usage=response.usage, response_id=response.response_id,
            request_id=response.request_id, raw_usage=response.raw_usage,
        )

    @staticmethod
    def _control_output(output: list[Any]) -> list[Any]:
        if any(isinstance(item, ResponseFunctionToolCall) for item in output):
            return output
        text = "\n".join(
            part
            for part in (ItemHelpers.extract_text(item) for item in output)
            if part
        ).strip()
        try:
            decision = parse_control_decision(text)
        except ValueError:
            return output
        call = ResponseFunctionToolCall(
            id=None,
            call_id=f"compat_commit_{uuid4().hex}",
            arguments=json.dumps(
                {"decision": decision.model_dump(mode="json")},
                ensure_ascii=False,
            ),
            name="commit_agent_action",
            type="function_call",
            status="completed",
        )
        return [call]

    async def stream_response(self, *args: Any, **kwargs: Any) -> AsyncIterator[Any]:
        async for event in self._delegate.stream_response(*args, **kwargs):
            if event.type == "response.completed":
                output = self._control_output(event.response.output)
                if output is not event.response.output:
                    event = event.model_copy(update={"response": event.response.model_copy(update={"output": output})})
            yield event

    async def close(self) -> None:
        await self._delegate.close()


class OpenAIAgentsRuntime:
    def __init__(self, settings: Settings, backend: BackendClient | None = None, *, archive_queue=None) -> None:
        settings.validate_model()
        self._archive_queue = archive_queue
        set_tracing_disabled(not settings.tracing_enabled)
        orchestration_tokens = min(
            settings.model_max_output_tokens,
            settings.orchestration_max_output_tokens,
        )
        client = AsyncOpenAI(
            api_key=settings.model_api_key,
            base_url=settings.model_base_url,
            timeout=settings.model_timeout_seconds,
            max_retries=settings.model_max_retries,
        )
        model = OpenAIResponsesModel(
            model=settings.model_name,
            openai_client=client,
        )
        self._settings = settings
        self._guardrails = SDKGuardrailPolicy.from_settings(settings)
        self._model = model
        self._model_client = client
        self._backend = backend or BackendClient(
            settings.backend_base_url,
            internal_token=settings.internal_token,
            ffmpeg_command=settings.ffmpeg_command,
            temp_dir=str(
                Path(settings.session_db_path).expanduser().resolve().parent
            ),
        )
        runtime_tools = [
            get_saved_instructions,
            update_saved_instructions,
            list_capabilities,
            load_skill_instructions,
            list_skill_resources,
            read_skill_resource,
            list_workspace_files,
            read_workspace_file,
            apply_workspace_patch,
            prepare_workspace_publication,
            publish_workspace_files,
            prepare_agent_memory_publication,
            publish_agent_memory,
            validate_workspace_skill,
            install_workspace_skill,
            inspect_project,
            inspect_project_goal,
            list_project_assets,
            inspect_text_asset,
            search_artifacts,
            inspect_current_artifact,
            inspect_artifact_version,
            get_artifact_downloads,
            inspect_run,
            list_execution_targets,
            inspect_execution_controls,
            control_execution,
            set_episode_execution_mode,
            inspect_recent_conversation,
            search_conversation_history,
            execute_skill_script,
            commit_agent_action,
        ]
        self._tool_provider = AgentToolProvider(
            self._backend, runtime_tools,
            hosted_model=OpenAIResponsesModel(model=settings.model_name, openai_client=client.with_options(max_retries=0)),
            hosted_client=client,
            subtask_model=lambda: self._execution_agent.model, guardrail_policy=self._guardrails,
            execution_model=lambda: self._execution_agent.model,
        )
        self._skill_router_agent = Agent(
            name="内容生产 Skill Router",
            instructions=(
                "你只根据输入中的 request 和 skills 目录判断当前请求适合哪些 Skill，"
                "不执行请求，也不能输出目录之外的 capability_id。"
                "只使用每项的 description、routing 和 accepted_asset_kinds 判断；"
                "description 中的适用范围与不适用边界都必须遵守。"
                "提及名称只是候选线索，不代表调用：拒绝使用、引用他人指令、解释或比较 Skill 时不选。"
                "allow_implicit_invocation=false 的 Skill 仅在用户本轮明确要求使用该 Skill 时可选，"
                "不能仅因描述匹配或名称出现而选中；其他 Skill 也不得违背用户本轮的拒绝指令。"
                "普通聊天、查看、局部修改，以及与任何目录描述不匹配的请求返回空数组。"
                "本轮仅上传、标记或补充素材，例如‘这是第一本’‘这是第二本小说’或"
                "‘再发一份’，但没有在本轮明确要求执行任务时，不适用任何 Skill。"
                "只依据本轮请求判断，不继承上一轮用户选择的通用 Agent 或 Skill 模式。"
                "最多返回三个最匹配的 capability_id；存在歧义时宁可不选。"
            ),
            model=model,
            output_type=SkillRoutingDecision,
            model_settings=ModelSettings(max_tokens=min(orchestration_tokens, 4096)),
        )
        self._artifact_source_router_agent = Agent(
            name="内容生产来源产物定位器",
            instructions=(
                "你只为当前请求定位被用户明确点名或明确指代为输入来源的已有产物，"
                "不执行创作，不按列表顺序猜测，也不得使用界面焦点。"
                "严格比较用户描述与产物标题、类型：故事大纲不能选成完整剧本，"
                "单集剧本不能选成多集剧本。只返回目录中真实存在的产物标题。"
                "artifact_titles 必须逐字复制目录里的完整 title，不要输出任何 ID。"
                "用户直接在本轮消息提供正文、资料或任务主题，且没有指定已有产物时，"
                "返回空 artifact_titles 和 needs_clarification=false，不要求先创建或上传产物。"
                "用户指定了已有产物但目录中不存在、类型不匹配，或多个同名无法区分时，"
                "返回空 artifact_titles 和 needs_clarification=true。"
            ),
            model=model,
            output_type=ArtifactSourceRoutingDecision,
            model_settings=ModelSettings(max_tokens=min(orchestration_tokens, 4096)),
        )
        execution_tools = self._tool_provider.runtime_tools()
        self._execution_agent = Agent[AgentContext](
            name="内容生产主 Agent SDK 执行器",
            instructions=(
                "你是内容生产平台的通用主 Agent，负责完整的一轮判断、工具编排和提交。"
                "Skill 只是可选插件；Go Runtime 是项目、Run、Artifact 的唯一事实源。"
                "你可以自主调用读取工具定位事实和上下文。普通聊天、澄清、创建通用产物、"
                "调用 Skill、查看或修改产物的普通终态，都必须通过 commit_agent_action 提交一次成功结果。"
                "工具审批 interruption 例外：等待审批时不得提交普通终态；校验失败的提交可修正后重试。"
                "不得用普通文本冒充提交，不得声称未通过工具完成的写操作已经成功。"
                "用户要求保存或修改工作文件时，使用 list_workspace_files、read_workspace_file 和 apply_workspace_patch；"
                "修改前读取确切版本，补丁使用 SDK V4A 格式。只提供工具回执返回的下载地址，不编造导出按钮。"
                "普通文档/表格保存为 generic_document/generic_table；文档正文使用 content_markdown 或 content 字符串，"
                "表格使用 columns（字符串列名或 key/label 对象）与 rows（等宽数组或按列 key 的对象）。"
                "已有普通产物下载先 inspect_current_artifact/inspect_artifact_version，再 get_artifact_downloads；"
                "只转述返回的格式、版本状态和 URL，不得声称已保存到用户电脑。新产物提交成功后前端提供下载入口。"
                "工作文件不是已安装的 Skill，保存 SKILL.md 不代表已经注册、可调用或执行过脚本；必须如实区分。"
                "用户要求创建 Skill 时，写入独立目录下的标准 SKILL.md（YAML name、description 与 Markdown 指令），"
                "按需写 references/scripts/assets/agents 文件；普通 inline Skill 不需要业务工作流代码。"
                "用 validate_workspace_skill 校验完整目录，再以该 snapshot_hash 调用 install_workspace_skill 请求安装审批。"
                "不要自行宣称等待授权；实际调用安装工具。新安装的两个安装版本 ID 参数使用空字符串；升级必须核对既有安装 ID 和 active_version_id。"
                "安装回执 ready_in_current_turn=true 时可 load_skill_instructions 后使用；否则必须按实际注册/依赖状态说明后续调用条件。"
                "输入中的 skill_router_candidates 来自 Go 动态注册表。使用任何候选前必须"
                "调用 load_skill_instructions，读取其当前版本、执行模式、确认策略和指令；"
                "需要引用文件时先用 list_skill_resources 列举，再用 read_skill_resource 按需读取。"
                "instructions_truncated 为 true 时必须分页读取 SKILL.md 全文；不得猜测未读取的引用资料。"
                "不得凭 capability_id 猜测 Skill 内容。inline Skill 在当前 turn 直接按完整指令"
                "完成，以 chat 或 create_artifact 提交，不得伪装成 Business Run。"
                "stateful_workflow 在用户主动选择时可以直接 propose_capability；隐式匹配且"
                "requires_user_confirmation=true 时首次必须 clarify，并给出“使用 Skill”和"
                "“由通用 Agent 直接生成”两个选项。只有用户随后明确同意才可"
                "propose_capability，并将 skill_confirmed 设为 true。"
                "用户选择通用 Agent 时应 create_artifact，不得继续推销 Skill。"
                "propose_capability 时，把用户已明确提供的运行配置写入 capability_config；"
				"字段、类型、枚举和默认值只能来自 load_skill_instructions 返回的 "
				"config_schemas；不得在主 Agent 中猜测某个 Skill 的专用字段。"
				"用户已明确给出的合法配置必须保留；缺少 Schema 必填值时应澄清。"
                "Skill 使用已有产物作为来源时，必须先 search_artifacts 和"
                "inspect_current_artifact，随后把确切当前版本 ID 写入"
                "source_artifact_version_ids；不得依赖界面当前焦点代替用户点名的来源。"
                "用户点名已有产物时，必须调用 search_artifacts 检索 Go 权威库目录，"
                "命中后用其 artifact_id 调用 inspect_current_artifact；不得声称用户尚未"
                "提供已能从目录检索到的产物。禁止要求每轮预灌完整产物目录。"
                "创建通用产物时，如果内容延续、汇总或改写了已有产物，必须先搜索并读取"
                "实际使用的当前版本，再把这些版本的确切 ID 写入"
                "source_artifact_version_ids；不得只依赖 Session 内容而丢失产物血缘。"
                "只读理解请求，例如询问图片或视频讲了什么、查看项目或读取产物，应直接"
                "调用读取工具并以 chat 或 inspect 回答，不得建议或追问是否使用 Skill。"
                "对已有产物的局部修改、框选修改或明确字段修改不属于 Skill 路由；定位到"
                "当前版本和 field_path 后必须直接 revise，不得再询问使用 Skill 还是通用 Agent，"
                "也不得把局部修改表述为重新运行 Skill。"
                "‘按照刚才的方案继续’‘继续下一步’等是在要求执行此前方案，不是修改当前"
                "焦点产物；除非用户同时明确点名修改对象、选区，或使用修改、调整、续写、"
                "追加等编辑动作，否则不得 revise，应继续生成下一项普通产物或按需澄清。"
                "skill_router_candidates 只是前置候选，不代表完整指令已加载。"
                "候选不合适或任务需要其他 inline Skill 时，可 list_capabilities 后调用 load_skill_instructions；"
                "该工具只接纳本次执行已验证目录中的版本，并按原审批策略装载依赖，不授予新的外部权限。"
                "必须通过通用 load_skill_instructions 工具渐进披露；该工具返回的 description"
                "边界和 instructions 是当前版本真源，最终选择与提交仍由你负责。"
                "仅上传、标记或补充第几份素材时应接收并延续既定任务，不得仅因附件类型"
                "咨询或推荐 Skill。"
                "任务依赖已上传文本或文档时，必须先调用 list_project_assets 获取当前快照，"
                "再调用 inspect_text_asset 读取解析正文；不得因附件不在本轮消息中就声称"
                "无法读取，也不得只根据文件名、会话摘要或占位符生成内容。"
                "同一 Skill 的待处理配置卡由 commit_agent_action 在提交阶段原子协调，"
                "不要在普通聊天中主动提及与当前请求无关的历史配置卡。"
                "用户要求暂停、继续、取消或重试时，用 list_execution_targets 定位确切 Run 或后台任务；"
                "不能按列表顺序或界面焦点猜目标，歧义时先澄清。调用 inspect_execution_controls 获取当前状态、"
                "available_actions 和 snapshot_hash，再以相同参数调用 control_execution 请求 SDK 审批。"
                "控制命令是平台生命周期操作，不需要新建 Skill 或提出新的生产流程。"
                "后台任务的暂停保留 SDK 状态，只有 paused 后才可继续；等待授权时暂停后还须明确继续。不能用取消/重试冒充暂停/继续。"
                "只以控制工具的回执回复：pausing 表示已请求安全暂停，queued 表示已排队；"
                "不得声称已停止或已完成。状态变化、无权限或重放风险时先说明失败，不能绕开校验。"
                "Runner 注入的 SDK Session 是本会话的模型记忆；其中用户已经明确说过的"
                "事实可以直接作为聊天依据，必须优先回答，不得声称没有记忆。"
                "用户询问‘最早’‘之前’‘还记得吗’‘会话记忆’等历史事实时，"
                "只有 SDK Session 确实没有明确答案时，才调用 "
                "search_conversation_history 回查 Go 权威会话；禁止未搜索就回答无法确认。"
                "项目 Goal 是跨回合的明确目标，不是一次性聊天主题；需要了解当前 Goal 时"
                "调用 inspect_project_goal。只有用户明确建立、改变、完成或取消持续目标时，"
                "才在提交中设置 goal_update；普通问答和单次创作请求不得自动创建 Goal。"
                "修改或重新生成既有产物前必须先搜索并读取当前版本。始终使用简体中文。"
                + TOOL_APPROVAL_INSTRUCTIONS
            ),
            model=model,
            tools=execution_tools,
            model_settings=ModelSettings(
                max_tokens=orchestration_tokens,
                tool_choice="required",
            ),
            reset_tool_choice=False,
            tool_use_behavior=stop_after_commit,
        )
        self._commit_repair_agent = Agent[AgentContext](
            name="内容生产主 Agent SDK 提交修复器",
            instructions=(
                "你只负责把上一执行器的候选结果整理为一个可提交的终态。"
                "必须调用 commit_agent_action，不能调用其他工具。"
                "如果兼容网关无法返回原生工具调用，则只输出 ControlDecision JSON 对象，"
                "不得添加 Markdown 或解释；协议适配层会把通过严格校验的 JSON 转为工具调用。"
                "不要机械重复固定澄清。结合原始请求、候选结果和近期对话决定 chat、clarify、"
                "propose_capability、create_artifact、inspect、revise、regenerate 或 unsupported。"
                "仅在用户明确管理持续项目目标时保留 goal_update，普通请求必须为 null。"
                "未显式选择 Skill 时，只有用户确认了上一轮的 Skill 建议，才把 "
                "skill_confirmed 设为 true；否则必须为 false。"
                "propose_capability 时必须从当前请求和候选结果保留用户已明确提供的"
				"合法配置，并严格按已加载 Skill 的 config_schemas 写入 capability_config。"
                "若原始请求点名已有来源产物，必须保留主执行器候选中的确切"
                "source_artifact_version_ids，不得改用当前焦点产物。"
                "create_artifact 若延续、汇总或改写已有产物，也必须保留候选中实际读取并"
                "使用的 source_artifact_version_ids，不得清空血缘。"
                "若候选结果声称旧会话事实无法确认，且工具轨迹没有调用过 "
                "search_conversation_history，不得把该说法当作可靠结论。"
                "已定位的局部修改必须整理为 revise，禁止改成 Skill 选择澄清。"
                "修复输入中的 inspected_artifacts 是主执行器已读取的权威当前版本；"
                "局部修改时从中保留 artifact_id、artifact_version_id、artifact_type、"
                "scope_key，并根据用户指定内容填写精确 field_path，禁止再次澄清。"
            ),
            model=RequiredCommitModelAdapter(model),
            tools=self._tool_provider.runtime_tools(["commit_agent_action"]),
            model_settings=ModelSettings(
                max_tokens=orchestration_tokens,
                tool_choice="required",
            ),
            reset_tool_choice=False,
            tool_use_behavior=stop_after_commit,
        )
        self._skill_router_agent = self._guardrails.protect(self._skill_router_agent)
        self._artifact_source_router_agent = self._guardrails.protect(self._artifact_source_router_agent)
    @property
    def model(self) -> OpenAIResponsesModel:
        return self._model

    @property
    def settings(self) -> Settings:
        return self._settings

    @property
    def orchestration_max_tokens(self) -> int:
        return min(
            self._settings.model_max_output_tokens,
            self._settings.orchestration_max_output_tokens,
        )

    def _new_turn_observation(self) -> TurnObservation:
        return TurnObservation(
            provider_id="openai-compatible-responses",
            model_id=self._settings.model_name,
            release_id=self._settings.release_id,
        )

    async def close(self) -> None:
        """Release this runtime's model client after its executions have drained."""
        task = getattr(self, "_close_task", None)
        if task is None:
            task = self._close_task = asyncio.create_task(self._model_client.close())
        interrupted = False
        with anyio.CancelScope(shield=True):
            while not task.done():
                try:
                    await asyncio.shield(task)
                except asyncio.CancelledError:
                    interrupted = True
            await task
        if interrupted:
            raise asyncio.CancelledError

    async def execute_turn(self, request: AgentExecutionRequest) -> dict[str, Any]:
        request = request.model_copy(deep=True)
        observation = self._new_turn_observation()
        session_id = f"{request.project_id}:{request.conversation_id}"
        release = reserve_turn(request)
        try:
            async with sdk_session_turn(self._settings.session_db_path, session_id):
                outcome = await self._execute_turn_locked(request, observation=observation)
                if isinstance(outcome, AgentExecutionPaused):
                    _require_streamed_checkpoint(outcome, None)
                return outcome.exchange
        finally:
            release()

    def create_voice_pipeline(self, project_id, conversation_id, prepare_turn, persist_event, *, stt_model, tts_model):
        from .native_voice import managed_voice_pipeline
        from .voice_runtime import voice_turn_workflow

        return managed_voice_pipeline(
            voice_turn_workflow(self, project_id, conversation_id, prepare_turn, persist_event),
            stt_model=stt_model, tts_model=tts_model,
        )

    def start_execution_stream(
        self, request: AgentExecutionRequest
    ) -> AgentExecutionStream:
        request = request.model_copy(deep=True)
        observation = self._new_turn_observation()
        pause_requested = asyncio.Event()

        async def operation(
            emit: Callable[[dict[str, Any]], Awaitable[None]],
        ) -> AgentExecutionOutcome | AgentExecutionPaused:
            token = _ACTIVE_TURN_PAUSE.set(pause_requested)
            try:
                session_id = f"{request.project_id}:{request.conversation_id}"
                async with sdk_session_turn(self._settings.session_db_path, session_id):
                    return await self._execute_turn_locked(
                        request, emit, observation=observation
                    )
            finally:
                _ACTIVE_TURN_PAUSE.reset(token)

        release = reserve_turn(request)
        try:
            return AgentExecutionStream(operation, observation, pause_requested, on_close=release)
        except BaseException:
            release()
            raise

    async def _execute_turn_locked(
        self,
        request: AgentExecutionRequest,
        emit: Callable[[dict[str, Any]], Awaitable[None]] | None = None,
        *,
        observation: TurnObservation | None = None,
    ) -> AgentExecutionOutcome | AgentExecutionPaused:
        if not request.agent_turn_id:
            return await self._execute_turn_scoped(request, emit, observation=observation)
        with backend_activity(request.project_id, agent_turn_id=request.agent_turn_id):
            return await self._execute_turn_scoped(request, emit, observation=observation)

    async def _execute_turn_scoped(
        self,
        request: AgentExecutionRequest,
        emit: Callable[[dict[str, Any]], Awaitable[None]] | None = None,
        *,
        observation: TurnObservation | None = None,
    ) -> AgentExecutionOutcome | AgentExecutionPaused:
        session = self._session(request.project_id, request.conversation_id, str(request.request.get("content", "")))
        try:
            return await self._execute_turn_with_session(request, session, emit, observation=observation)
        finally:
            session.close()

    async def _execute_turn_with_session(
        self,
        request: AgentExecutionRequest,
        session: Any,
        emit: Callable[[dict[str, Any]], Awaitable[None]] | None = None,
        *,
        observation: TurnObservation | None = None,
    ) -> AgentExecutionOutcome | AgentExecutionPaused:
        observation = observation or self._new_turn_observation()
        try:
            request = request.model_copy(update={"additional_inputs": await prepare_input_attachments(request.project_id, request.additional_inputs, self._backend)})
        except (ValueError, BackendError) as exc:
            raise RuntimeCompatibilityError("Additional material no longer matches its execution snapshot", failure_stage="run_state_restore") from exc
        is_resume = request.run_state is not None
        if not is_resume and _explicit_skill_confirmation(
            str(request.request.get("content") or "")
        ):
            with observation.measure("recent_context"):
                conversation = await self._backend.get_conversation_messages(
                    request.conversation_id, limit=8
                )
                _inherit_recent_material_context(request.request, conversation)
        if is_resume:
            context = _restore_agent_context(
                request.run_state or {}, request, self._backend, observation
            )
        else:
            context = AgentContext(
                project_id=request.project_id,
                conversation_id=request.conversation_id,
                backend=self._backend,
                raw_request=request.request,
                idempotency_key=request.idempotency_key,
                agent_turn_id=request.agent_turn_id or request.idempotency_key,
                observation=observation,
                main_inputs=request.additional_inputs,
            )
        context.dispatch_generation = request.dispatch_generation
        context.memory_archive_queue = self._archive_queue
        with observation.measure("instructions_prepare"):
            await bind_managed_instructions(context, request.run_state)
        with observation.measure("session_prepare"):
            if is_resume:
                session_items = await session.get_items()
                session_checkpoint = len(session_items)
            else:
                session_items, session_checkpoint = await prepare_sdk_session(session)
        if not is_resume:
            context.previous_turn_consulted_capabilities.update(
                _previous_turn_consulted_capabilities(session_items)
            )
        with observation.measure("workspace_context"):
            artifact_catalog = (
                {}
                if is_resume
                else await self._backend.search_artifacts(request.project_id, limit=50)
            )
            registry = await self._backend.get_capabilities(request.project_id)
        capability_items = _capability_items(registry)
        if not is_resume:
            context.capability_versions.update({
                str(item.get("capability_id")): str(item.get("version"))
                for item in capability_items
                if isinstance(item, dict)
                and str(item.get("capability_id") or "").strip()
                and str(item.get("version") or "").strip()
            })
        explicit_capability_ref = request.request.get("capability_ref")
        if not is_resume and isinstance(explicit_capability_ref, dict):
            explicit_id = str(
                explicit_capability_ref.get("capability_id") or ""
            ).strip()
            explicit_version = str(
                explicit_capability_ref.get("version") or ""
            ).strip()
            if explicit_id and explicit_version:
                context.capability_versions[explicit_id] = explicit_version
        if is_resume:
            routed_capabilities = set(context.routed_capabilities)
        else:
            with observation.measure("skill_routing"):
                routing_token = _ACTIVE_ROUTING_OBSERVATION.set(
                    (observation, request.conversation_id, context.agent_turn_id)
                )
                try:
                    routed_capabilities = await self._route_skills(
                        request.request, capability_items
                    )
                finally:
                    _ACTIVE_ROUTING_OBSERVATION.reset(routing_token)
            context.routed_capabilities.update(routed_capabilities)
        selected_capability_ids = set(routed_capabilities)
        capability_ref = request.request.get("capability_ref")
        if isinstance(capability_ref, dict):
            capability_id = str(capability_ref.get("capability_id") or "").strip()
            if capability_id:
                selected_capability_ids.add(capability_id)
        capability_items = await self._resolve_selected_capability_versions(
            context, capability_items, selected_capability_ids
        )
        if not is_resume and (
            routed_capabilities
            or isinstance(request.request.get("capability_ref"), dict)
        ) and not _has_attachment_sources(request.request):
            accepted_asset_kinds = {
                str(kind)
                for item in capability_items
                if isinstance(item, dict)
                and item.get("capability_id") in selected_capability_ids
                for kind in item.get("accepted_asset_kinds", [])
                if isinstance(kind, str)
            }
            context.source_resolution_attempted = True
            with observation.measure("source_routing"):
                routing_token = _ACTIVE_ROUTING_OBSERVATION.set(
                    (observation, request.conversation_id, context.agent_turn_id)
                )
                try:
                    source_resolution = await self._resolve_artifact_sources(
                        request.request,
                        artifact_catalog,
                        accepted_asset_kinds,
                    )
                    context.resolved_source_artifact_version_ids = source_resolution.version_ids
                    context.source_resolution_requires_binding = source_resolution.requires_binding
                finally:
                    _ACTIVE_ROUTING_OBSERVATION.reset(routing_token)
        prompt = json.dumps(
            {
                "request": request.request,
                "resolved_source_artifact_version_ids": (
                    context.resolved_source_artifact_version_ids
                ),
                "skill_router_candidates": [
                    item
                    for item in _routing_skill_catalog(
                        capability_items, auto_route_only=False, mentioned_ids=selected_capability_ids
                    )
                    if item["capability_id"] in context.routed_capabilities
                    or (
                        isinstance(request.request.get("capability_ref"), dict)
                        and item["capability_id"]
                        == request.request["capability_ref"].get("capability_id")
                    )
                ],
                "instruction": (
                    "由 SDK Runner 自主判断动作。产物目录只用于定位候选；"
                    "使用已有产物前仍须调用 inspect_current_artifact 核对正文和当前版本，"
                    "并通过 commit_agent_action 提交唯一终态。"
                ),
            },
            ensure_ascii=False,
        )
        runner_input: Any = None
        if not is_resume:
            runner_input = await self._runner_input(
                request.request,
                prompt,
                capability_items,
                selected_capability_ids,
            )
        paused = False
        try:
            candidate = ""
            repair_resume = request if context.main_execution_phase == "terminal_repair" else None
            if repair_resume is None:
                with observation.measure("tool_prepare"):
                    prepared_tools = await self._tool_provider.prepare(
                        context, capability_items, selected_capability_ids
                    )
                async with prepared_tools:
                    execution_agent = self._execution_agent.clone(
                        instructions=with_managed_instructions(self._execution_agent.instructions, context),
                        tools=prepared_tools.tools,
                        mcp_servers=prepared_tools.mcp_servers,
                        mcp_config={
                            "convert_schemas_to_strict": True,
                            "include_server_in_tool_names": True,
                        },
                    )
                    execution_agent = await bind_native_agent(context, self._settings, self._guardrails.protect(execution_agent))
                    execution_agent = attach_skill_handoffs(
                        execution_agent, context, capability_items,
                        selected_capability_ids, _load_skill_instructions,
                    )
                    if is_resume:
                        with observation.measure("run_state_restore"):
                            runner_input = await self._restore_execution_state(execution_agent, request, context)
                    with observation.measure("agent_loop"):
                        result = await self._run_execution_agent(
                            execution_agent, runner_input, context, session, emit, observation,
                        )
                observation.ingest_result(result)
                pause = self._execution_pause(result, context, observation)
                if pause is not None:
                    _require_streamed_checkpoint(pause, emit)
                    paused = True
                    return pause
                if context.commit_result is not None:
                    return AgentExecutionOutcome(exchange=context.commit_result, observation=observation.public(), included_input_ids=list(context.main_included_input_ids))
                final_output = getattr(result, "final_output", "")
                candidate = final_output if isinstance(final_output, str) else ""
            repair_pause = await self._run_terminal_repair(
                request, context, candidate, session, emit, observation, resume=repair_resume,
            )
            if repair_pause is not None:
                _require_streamed_checkpoint(repair_pause, emit)
                paused = True
                return repair_pause
        except (ModelRecoveryRequired, ToolOutcomeReviewRequired) as recovery:
            observation.ingest_result(recovery.result)
            state_json, state_schema, pending_ids = _serialize_paused_run(recovery.result, allow_no_approvals=True)
            pause = AgentExecutionPaused(state_json, state_schema, pending_ids, observation.public(),
                                         True, list(context.main_included_input_ids),
                                         EXTERNAL_TOOL_RECOVERY_REASON if isinstance(recovery, ToolOutcomeReviewRequired) else MODEL_RECOVERY_REASON)
            _require_streamed_checkpoint(pause, emit)
            paused = True
            return pause
        except asyncio.CancelledError:
            raise
        except TimeoutError as exc:
            wrapped = RuntimeCompatibilityError("Agents SDK run timed out")
            wrapped.bind_observation(observation)
            raise wrapped from exc
        except AgentToolConfigurationError as exc:
            wrapped = RuntimeCompatibilityError(str(exc))
            wrapped.bind_observation(observation)
            raise wrapped from exc
        except RuntimeCompatibilityError as exc:
            exc.bind_observation(observation)
            raise
        except Exception as exc:
            wrapped = RuntimeCompatibilityError(normalize_provider_error(exc), failure_stage=getattr(exc, "failure_stage", ""))
            wrapped.bind_observation(observation)
            raise wrapped from exc
        finally:
            if context.commit_result is None and not paused:
                await self._rollback_session(session, session_checkpoint)
        if context.commit_result is None:
            logger.error(
                "SDK Runner completed without terminal commit project_id=%s conversation_id=%s",
                request.project_id,
                request.conversation_id,
            )
            raise RuntimeCompatibilityError(
                "Agents SDK run completed without commit_agent_action"
            )
        return AgentExecutionOutcome(
            exchange=context.commit_result,
            observation=observation.public(),
            included_input_ids=list(context.main_included_input_ids),
        )

    @staticmethod
    async def _restore_execution_state(agent: Agent, request: AgentExecutionRequest, context: AgentContext) -> Any:
        state = await RunState.from_json(agent, request.run_state or {}, context_override=context, strict_context=True)
        if request.approval_decisions or state.get_interruptions():
            _apply_approval_decisions(state, request.approval_decisions)
        if unstarted_checkpoint(request.run_state or {}):
            try:
                validate_unstarted_input(request.run_state or {}, context.main_unstarted_input)
            except ValueError as exc:
                raise RuntimeCompatibilityError(str(exc), failure_stage="run_state_restore") from exc
            return context.main_unstarted_input
        return state

    @staticmethod
    def _execution_pause(result: Any, context: AgentContext, observation: TurnObservation) -> AgentExecutionPaused | None:
        signal = _ACTIVE_TURN_PAUSE.get()
        user_requested = signal is not None and signal.is_set()
        if context.commit_result is not None:
            return None
        if not getattr(result, "interruptions", None) and not (user_requested and result.final_output is None):
            return None
        state_json, state_schema, pending_ids = _serialize_paused_run(result, allow_no_approvals=user_requested)
        return AgentExecutionPaused(state_json, state_schema, pending_ids, observation.public(), user_requested, list(context.main_included_input_ids))

    async def _run_terminal_repair(
        self, request: AgentExecutionRequest, context: AgentContext, candidate: str,
        session: Any, emit: Callable[[dict[str, Any]], Awaitable[None]] | None,
        observation: TurnObservation, *, resume: AgentExecutionRequest | None = None,
    ) -> AgentExecutionPaused | None:
        if resume is not None:
            candidate = context.main_repair_candidate
        context.main_execution_phase = "terminal_repair"
        for repair_attempt in range(context.main_repair_attempt, 2):
            context.main_repair_attempt = repair_attempt
            context.main_repair_candidate = candidate
            agent = self._guardrails.protect(self._commit_repair_agent.clone(
                instructions=with_managed_instructions(self._commit_repair_agent.instructions, context),
            ))
            agent = await bind_native_agent(context, self._settings, agent)
            if resume is not None:
                with observation.measure("run_state_restore"):
                    repair_input = await self._restore_execution_state(agent, resume, context)
                resume = None
            else:
                repair_input = json.dumps({
                    "request": request.request, "candidate": candidate,
                    "rejected_decisions": context.rejected_decisions[-3:],
                    "inspected_artifacts": context.inspected_artifacts[-3:],
                    "repair_attempt": repair_attempt + 1,
                    "instruction": "整理并提交本轮唯一终态，不得重复向用户索取已提供的信息。",
                }, ensure_ascii=False)
            with observation.measure("terminal_repair"):
                result = await self._run_execution_agent(
                    agent, repair_input, context, session, emit, observation,
                    max_turns=2, workflow_name="content-agent-terminal-repair",
                )
            observation.ingest_result(result)
            pause = self._execution_pause(result, context, observation)
            if pause is not None:
                return pause
            if context.commit_result is not None:
                break
            repaired_output = getattr(result, "final_output", "")
            if isinstance(repaired_output, str) and repaired_output.strip():
                candidate = repaired_output
        return None

    async def _run_execution_agent(
        self,
        execution_agent: Agent[AgentContext],
        runner_input: Any,
        context: AgentContext,
        session: Any,
        emit: Callable[[dict[str, Any]], Awaitable[None]] | None,
        observation: TurnObservation,
        *,
        max_turns: int = 8,
        workflow_name: str = "content-agent-turn",
        stage_inputs: bool = True,
        stop_after_turn: bool = False,
    ) -> Any:
        if stage_inputs:
            try:
                validate_main_input_state(context, runner_input)
                ids, _, _ = main_input_batch(context)
                if len(ids) > len(context.main_input_ids) and isinstance(runner_input, RunState) and runner_input.get_interruptions():
                    # Custom commit behavior may finish while processing old decisions.
                    # Let the native Runner settle them before it can admit new input.
                    settled = await self._run_execution_agent(
                        execution_agent, runner_input, context, session, emit, observation,
                        max_turns=max_turns, workflow_name=workflow_name,
                        stage_inputs=False, stop_after_turn=True,
                    )
                    if context.commit_result is not None or settled.interruptions or settled.final_output is not None:
                        return settled
                    runner_input = settled.to_state()
                runner_input = stage_main_inputs(context, runner_input)
            except (ValueError, UserError) as exc:
                raise RuntimeCompatibilityError("Main additional input cannot be restored or admitted by the native SDK", failure_stage="run_state_restore") from exc
        run_config = observation.run_config(
            workflow_name=workflow_name,
            conversation_id=context.conversation_id,
            agent_turn_id=context.agent_turn_id,
            tracing_enabled=self._settings.tracing_enabled,
        )
        async with native_run_config(context, run_config, runner_input) as active_config:
            result = await self._run_execution_agent_once(
                execution_agent, runner_input, context, session, emit, active_config,
                max_turns=max_turns, stage_inputs=stage_inputs, stop_after_turn=stop_after_turn,
            )
        signal = _ACTIVE_TURN_PAUSE.get()
        deferred_inputs = stage_inputs and len(context.main_input_ids) < len(context.main_inputs)
        if deferred_inputs and result.final_output is None and not result.interruptions and not (signal is not None and signal.is_set()):
            return await self._run_execution_agent(execution_agent, result.to_state(), context, session, emit, observation,
                                                   max_turns=max_turns, workflow_name=workflow_name)
        await capture_completed_memory(context, result)
        return result

    async def _run_execution_agent_once(
        self, execution_agent, runner_input, context, session, emit, run_config,
        *, max_turns, stage_inputs, stop_after_turn,
    ):
        runner_context = None if isinstance(runner_input, RunState) else context
        if emit is None and not context.main_inputs and not stop_after_turn and not context.agent_turn_id:
            result = await asyncio.wait_for(
                Runner.run(
                    execution_agent,
                    runner_input,
                    context=runner_context,
                    session=session,
                    max_turns=max_turns,
                    run_config=run_config,
                ),
                timeout=self._settings.run_timeout_seconds,
            )
            await settle_native_stream(context, result)
            return result

        signal = _ACTIVE_TURN_PAUSE.get()
        resumed = isinstance(runner_input, RunState)
        deferred_inputs = stage_inputs and len(context.main_input_ids) < len(context.main_inputs)
        context.main_unstarted_input = None if resumed else runner_input
        def on_model_start() -> None:
            context.main_unstarted_input = None
            if deferred_inputs:
                result.cancel(mode="after_turn")
        boundary = ModelFailureBoundary(on_model_start, on_external_review=lambda: result.cancel(mode="after_turn"))
        result = Runner.run_streamed(
            execution_agent,
            runner_input,
            context=runner_context,
            session=session,
            max_turns=max_turns,
            run_config=run_config,
            hooks=boundary,
        )

        model_started = asyncio.Event()
        model_responded = False
        async def wait_for_pause() -> None:
            assert signal is not None
            await signal.wait()
            if not resumed:
                await model_started.wait()
            result.cancel(mode="after_turn")

        watcher = None
        if stop_after_turn:
            result.cancel(mode="after_turn")
        elif signal is not None:
            if signal.is_set():
                result.cancel(mode="after_turn")
            else:
                watcher = asyncio.create_task(wait_for_pause())

        async def consume() -> None:
            nonlocal model_responded
            async for sdk_event in result.stream_events():
                if sdk_event.type == "raw_response_event":
                    context.main_unstarted_input = None
                    model_started.set()
                    if getattr(sdk_event.data, "type", "") == "response.completed":
                        model_responded = True
                normalized = normalize_execution_stream_event(sdk_event)
                if normalized is not None and emit is not None:
                    await emit(normalized)

        recovery_error = None
        cancelled = False
        try:
            await asyncio.wait_for(
                consume(), timeout=self._settings.run_timeout_seconds
            )
        except BaseException as exc:
            if context.commit_result is None and boundary.permits_checkpoint(exc, result):
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
            if context.main_input_ids and model_responded and not result.to_state().pending_input:
                changed = context.main_included_input_ids != context.main_input_ids
                context.main_included_input_ids = list(context.main_input_ids)
                if changed and emit is not None:
                    await emit({"event": "agent.turn.inputs_included", "data": {"included_input_ids": list(context.main_included_input_ids)}})
        if boundary.external_review:
            require_external_tool_pause(result)
            if context.commit_result is not None:
                raise RuntimeCompatibilityError("External outcome review cannot follow a terminal commit")
            raise ToolOutcomeReviewRequired(result)
        if recovery_error is not None:
            raise ModelRecoveryRequired(result) from recovery_error
        if deferred_inputs and result.final_output is None and not result.interruptions and not (signal is not None and signal.is_set()):
            if not model_responded:
                raise RuntimeCompatibilityError("Native model recovery did not reach an input admission boundary")
        return result

    async def _route_skills(
        self,
        raw_request: dict[str, Any],
        capability_items: list[dict[str, Any]],
    ) -> set[str]:
        routing_context = _ACTIVE_ROUTING_OBSERVATION.get()
        observation = routing_context[0] if routing_context is not None else None
        if isinstance(raw_request.get("capability_ref"), dict):
            return set()
        candidates = _routing_skill_catalog(
            capability_items, auto_route_only=True,
            mentioned_ids=_mentioned_skill_ids(raw_request, capability_items),
        )
        if not candidates:
            return set()
        try:
            result = await asyncio.wait_for(
                Runner.run(
                    self._skill_router_agent,
                    json.dumps(
                        {"request": raw_request, "skills": candidates},
                        ensure_ascii=False,
                    ),
                    max_turns=1,
                    run_config=(
                        observation.run_config(
                            workflow_name="content-agent-skill-router",
                            conversation_id=routing_context[1],
                            agent_turn_id=routing_context[2],
                            tracing_enabled=self._settings.tracing_enabled,
                        )
                        if observation is not None
                        else None
                    ),
                ),
                timeout=self._settings.run_timeout_seconds,
            )
        except Exception as exc:
            logger.warning("SDK Skill Router unavailable: %s", normalize_provider_error(exc))
            return set()
        if observation is not None:
            observation.ingest_result(result)
        decision = getattr(result, "final_output", None)
        if not isinstance(decision, SkillRoutingDecision):
            logger.warning("SDK Skill Router returned unsupported output")
            return set()
        allowed_ids = {item["capability_id"] for item in candidates}
        return {
            capability_id
            for capability_id in decision.applicable_capability_ids[:3]
            if capability_id in allowed_ids
        }

    async def _resolve_selected_capability_versions(
        self,
        context: AgentContext,
        capability_items: list[dict[str, Any]],
        selected_capability_ids: set[str],
    ) -> list[dict[str, Any]]:
        resolved = list(capability_items)
        indexes = {
            str(item.get("capability_id") or "").strip(): index
            for index, item in enumerate(resolved)
            if isinstance(item, dict)
            and str(item.get("capability_id") or "").strip()
        }
        for capability_id in sorted(selected_capability_ids):
            expected_version = str(
                context.capability_versions.get(capability_id) or ""
            ).strip()
            current_index = indexes.get(capability_id)
            current = resolved[current_index] if current_index is not None else None
            if (
                isinstance(current, dict)
                and current.get("status") == "available"
                and (
                    not expected_version
                    or str(current.get("version") or "").strip()
                    == expected_version
                )
            ):
                continue
            if not expected_version:
                raise RuntimeCompatibilityError(
                    "selected Skill version is unavailable"
                )
            payload = await context.backend.get_capability(
                capability_id, context.project_id, expected_version
            )
            detail = payload.get("data") if isinstance(payload, dict) else None
            if (
                not isinstance(detail, dict)
                or detail.get("status") != "available"
                or str(detail.get("capability_id") or "").strip()
                != capability_id
                or str(detail.get("version") or "").strip()
                != expected_version
            ):
                raise RuntimeCompatibilityError(
                    "selected Skill version is unavailable"
                )
            if current_index is None:
                indexes[capability_id] = len(resolved)
                resolved.append(detail)
            else:
                resolved[current_index] = detail
        return resolved

    async def _resolve_artifact_sources(
        self,
        raw_request: dict[str, Any],
        artifact_catalog: dict[str, Any],
        accepted_asset_kinds: set[str] | None = None,
    ) -> ArtifactSourceResolution:
        routing_context = _ACTIVE_ROUTING_OBSERVATION.get()
        observation = routing_context[0] if routing_context is not None else None
        explicit_versions = _explicit_artifact_versions(
            raw_request, artifact_catalog, accepted_asset_kinds
        )
        if explicit_versions:
            return ArtifactSourceResolution(explicit_versions, True)
        try:
            result = await asyncio.wait_for(
                Runner.run(
                    self._artifact_source_router_agent,
                    json.dumps(
                        {"request": raw_request, "artifact_catalog": artifact_catalog},
                        ensure_ascii=False,
                    ),
                    max_turns=1,
                    run_config=(
                        observation.run_config(
                            workflow_name="content-agent-source-router",
                            conversation_id=routing_context[1],
                            agent_turn_id=routing_context[2],
                            tracing_enabled=self._settings.tracing_enabled,
                        )
                        if observation is not None
                        else None
                    ),
                ),
                timeout=self._settings.run_timeout_seconds,
            )
        except Exception as exc:
            logger.warning(
                "SDK artifact source router unavailable: %s",
                normalize_provider_error(exc),
            )
            return ArtifactSourceResolution([], True)
        if observation is not None:
            observation.ingest_result(result)
        decision = getattr(result, "final_output", None)
        if not isinstance(decision, ArtifactSourceRoutingDecision):
            logger.warning("SDK artifact source router returned unsupported output")
            return ArtifactSourceResolution([], True)
        if decision.needs_clarification:
            return ArtifactSourceResolution([], True)
        if not decision.artifact_titles:
            return ArtifactSourceResolution([], False)
        items = artifact_catalog.get("items", [])
        versions_by_title: dict[str, list[str]] = {}
        for item in items:
            if not isinstance(item, dict):
                continue
            if accepted_asset_kinds is not None:
                source_kind = {
                    "generic_document": "document",
                    "generic_table": "document",
                }.get(str(item.get("artifact_type") or ""))
                if source_kind not in accepted_asset_kinds:
                    continue
            title = str(item.get("title", "")).strip()
            version_id = str(item.get("current_version_id", "")).strip()
            if not title or not version_id:
                continue
            normalized_title = re.sub(r"[\W_]+", "", title.casefold())
            versions_by_title.setdefault(normalized_title, []).append(version_id)
        resolved: list[str] = []
        for title in decision.artifact_titles:
            normalized_title = re.sub(r"[\W_]+", "", title.casefold())
            matches = versions_by_title.get(normalized_title, [])
            if not matches and len(normalized_title) >= 4:
                matches = [
                    version_id
                    for catalog_title, version_ids in versions_by_title.items()
                    if normalized_title in catalog_title
                    or catalog_title in normalized_title
                    for version_id in version_ids
                ]
            if len(matches) == 1 and matches[0] not in resolved:
                resolved.append(matches[0])
        if len(resolved) != len(decision.artifact_titles):
            return ArtifactSourceResolution([], True)
        return ArtifactSourceResolution(resolved, True)

    @staticmethod
    async def _rollback_session(session: Any, checkpoint: int) -> None:
        items = await session.get_items()
        for _ in range(max(0, len(items) - checkpoint)):
            await session.pop_item()

    async def _runner_input(
        self,
        raw_request: dict[str, Any],
        prompt: str,
        capability_items: list[dict[str, Any]] | None = None,
        selected_capability_ids: set[str] | None = None,
    ) -> str | list[dict[str, Any]]:
        references = raw_request.get("attachment_refs")
        if not isinstance(references, list) or not references:
            return prompt
        content: list[dict[str, Any]] = [{"type": "input_text", "text": prompt}]
        if selected_capability_ids is None:
            selected_capability_ids = set()
            capability_ref = raw_request.get("capability_ref")
            if isinstance(capability_ref, dict):
                capability_id = str(capability_ref.get("capability_id", "")).strip()
                if capability_id:
                    selected_capability_ids.add(capability_id)
        defers_attachment_preview = any(
            isinstance(item, dict)
            and item.get("capability_id") in selected_capability_ids
            and isinstance(item.get("input_binding"), dict)
            and bool(str(item["input_binding"].get("asset_set_purpose") or "").strip())
            for item in capability_items or []
        )
        include_video_frames = not defers_attachment_preview
        remaining_image_slots = 48
        for reference in references:
            if not isinstance(reference, dict):
                continue
            asset_id = str(reference.get("asset_id", "")).strip()
            snapshot_id = str(reference.get("asset_snapshot_id", "")).strip()
            if not asset_id or not snapshot_id:
                continue
            image_url = await self._backend.get_image_data_url(asset_id, snapshot_id)
            if image_url is not None:
                if remaining_image_slots <= 0:
                    continue
                content.append(
                    {"type": "input_image", "image_url": image_url, "detail": "auto"}
                )
                remaining_image_slots -= 1
                continue
            if not include_video_frames or remaining_image_slots <= 0:
                continue
            video_frames = await self._backend.get_video_frame_data_urls(
                asset_id, snapshot_id
            )
            if video_frames:
                content.append(
                    {
                        "type": "input_text",
                        "text": (
                            "以下图片是本轮视频附件中均匀抽取的代表帧，"
                            "可用于通用视觉理解，但不包含完整音频、字幕或逐镜分析。"
                        ),
                    }
                )
                content.extend(
                    {
                        "type": "input_image",
                        "image_url": frame,
                        "detail": "auto",
                    }
                    for frame in video_frames[:remaining_image_slots]
                )
                remaining_image_slots -= min(len(video_frames), remaining_image_slots)
        if len(content) == 1:
            return prompt
        return [{"role": "user", "content": content}]

    def _session(
        self,
        project_id: str,
        conversation_id: str,
        current_user_content: str,
    ) -> Any:
        return create_sdk_session(
            self._backend,
            project_id,
            conversation_id,
            client=self._model_client,
            model=self._settings.model_name,
            db_path=self._settings.session_db_path,
            current_user_content=current_user_content,
        )
