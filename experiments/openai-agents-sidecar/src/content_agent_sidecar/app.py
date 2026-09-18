from __future__ import annotations

import asyncio
from collections.abc import AsyncGenerator, Awaitable, Callable
from contextlib import asynccontextmanager
import json
import logging
from typing import Any

import anyio
from fastapi import FastAPI, HTTPException
from fastapi.responses import StreamingResponse
from starlette.types import Receive, Scope, Send

from .backend import BackendError
from .background_worker import SDKBackgroundTaskWorker
from .memory_worker import SDKMemoryWorker
from .memory_archive_queue import open_memory_archive_queue
from .memory_archive_worker import MemoryArchiveWorker
from .config import Settings
from .internal_auth import InternalAuthentication
from .realtime_routes import register_realtime_routes
from .voice_routes import register_voice_routes
from .turn_admission import TurnAlreadyRunningError
from .native_voice import configured_voice_runner
from .contracts import (
    CancelResponse,
    AgentExecutionRequest,
    HealthResponse,
    ArtifactRevisionRequest,
    ArtifactRevisionResponse,
)
from .events import agent_event
from .runtime import OpenAIAgentsRuntime, RuntimeCompatibilityError
from .task_worker import SDKTaskWorker
from .revision import execute_artifact_revision


logger = logging.getLogger("content_agent_sidecar")


class _ClosingAgentResponse(StreamingResponse):
    def __init__(self, content: AsyncGenerator[str, None], close: Callable[[], Awaitable[None]], *, headers: dict[str, str]) -> None:
        super().__init__(content, media_type="text/event-stream", headers=headers)
        self._events = content
        self._close = close

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        try:
            await super().__call__(scope, receive, send)
        finally:
            # StreamingResponse does not close a generator suspended at yield
            # when sending headers/body fails, including before the first event.
            with anyio.CancelScope(shield=True):
                try:
                    await self._events.aclose()
                finally:
                    await self._close()


def _failure_diagnostics(exc: BaseException) -> tuple[str, int | None]:
    types: list[str] = []
    seen: set[int] = set()
    status: int | None = None
    current: BaseException | None = exc
    while current is not None and id(current) not in seen and len(types) < 8:
        seen.add(id(current))
        types.append(type(current).__name__)
        candidate = getattr(current, "status_code", None)
        if isinstance(candidate, int) and 100 <= candidate <= 599:
            status = candidate
        current = current.__cause__ or current.__context__
    return ">".join(types), status


class ActiveRunRegistry:
    def __init__(self) -> None:
        self._runs: dict[str, Any] = {}

    def add(self, run_id: str, stream: Any) -> None:
        self._runs[run_id] = stream

    def contains(self, run_id: str) -> bool:
        return run_id in self._runs

    def remove(self, run_id: str) -> None:
        self._runs.pop(run_id, None)

    def cancel(self, run_id: str) -> bool:
        stream = self._runs.get(run_id)
        return stream.cancel() if stream is not None else False

    def pause(self, run_id: str) -> bool:
        stream = self._runs.get(run_id)
        return stream.pause() if stream is not None else False


def encode_sse(envelope: dict[str, Any]) -> str:
    return (
        f"event: {envelope['event_type']}\n"
        f"data: {json.dumps(envelope, ensure_ascii=False)}\n\n"
    )


def create_app(
    settings: Settings | None = None,
    runtime: OpenAIAgentsRuntime | None = None,
    realtime_opener: Any = None,
    voice_runner: Any = None,
) -> FastAPI:
    resolved = settings or Settings.from_env()
    if runtime is not None and resolved.memory_archive_db_path:
        raise ValueError("Configured memory archival requires an application-owned runtime")
    archive_queue = None
    def archive_options():
        return {"archive_queue": archive_queue} if archive_queue is not None else {}
    task_workers: list[SDKTaskWorker] = []
    background_task_workers: list[SDKBackgroundTaskWorker] = []
    memory_workers: list[SDKMemoryWorker] = []
    archive_workers: list[MemoryArchiveWorker] = []

    @asynccontextmanager
    async def lifespan(_app: FastAPI):  # type: ignore[no-untyped-def]
        nonlocal archive_queue
        try:
            archive_queue = open_memory_archive_queue(resolved.memory_archive_db_path, resolved.session_db_path)
            if archive_queue is not None:
                archive_worker = MemoryArchiveWorker.from_settings(resolved, archive_queue)
                archive_workers.append(archive_worker)
                archive_worker.start()
            if resolved.memory_worker_enabled:
                memory_worker = SDKMemoryWorker.from_settings(resolved)
                memory_workers.append(memory_worker)
                memory_worker.start()
            if resolved.task_worker_enabled:
                for slot in range(1, resolved.task_worker_concurrency + 1):
                    task_worker = SDKTaskWorker(resolved, slot=slot, **archive_options())
                    task_workers.append(task_worker)
                    task_worker.start()
                    background_worker = SDKBackgroundTaskWorker(resolved, slot=slot, **archive_options())
                    background_task_workers.append(background_worker)
                    background_worker.start()
                logger.info(
                    "OpenAI Agents SDK task workers started model=%s max_output_tokens=%s concurrency=%s",
                    resolved.model_name,
                    min(
                        resolved.model_max_output_tokens,
                        resolved.orchestration_max_output_tokens,
                    ),
                    resolved.task_worker_concurrency,
                )
            yield
        finally:
            with anyio.CancelScope(shield=True):
                results = await asyncio.gather(
                    *(worker.stop() for worker in task_workers),
                    *(worker.stop() for worker in background_task_workers),
                    *(worker.stop() for worker in memory_workers),
                    *(worker.stop() for worker in archive_workers),
                    return_exceptions=True,
                )
                task_workers.clear()
                background_task_workers.clear()
                memory_workers.clear()
                archive_workers.clear()
                failures = [type(result).__name__ for result in results if isinstance(result, BaseException)]
                if archive_queue is not None:
                    try:
                        archive_queue.close()
                    except Exception as error:
                        failures.append(type(error).__name__)
                    finally:
                        archive_queue = None
                if failures:
                    logger.error("Agent worker shutdown failed cause_types=%s", ",".join(failures))
                    raise RuntimeError("Agent worker shutdown failed") from None

    app = FastAPI(
        title="Content Agent OpenAI SDK Sidecar",
        version="0.1.0",
        lifespan=lifespan,
    )
    registry = ActiveRunRegistry()

    app.add_middleware(InternalAuthentication, token=resolved.internal_token)
    register_realtime_routes(app, realtime_opener)
    if voice_runner is None and resolved.voice_enabled:
        voice_runner = configured_voice_runner(lambda: runtime or OpenAIAgentsRuntime(resolved, **archive_options()),
                                               owns_runtime=runtime is None)
    register_voice_routes(app, voice_runner)

    @app.get("/healthz", response_model=HealthResponse)
    async def health() -> HealthResponse:
        configured = bool(
            resolved.model_base_url and resolved.model_api_key and resolved.model_name
        )
        return HealthResponse(
            status="ok" if configured else "degraded",
            backend_base_url=resolved.backend_base_url,
            model_configured=configured,
            write_tools_enabled=bool(resolved.internal_token),
        )

    @app.post("/internal/v1/agent/execute-stream")
    async def stream_agent_turn(payload: AgentExecutionRequest) -> StreamingResponse:
        turn_id = payload.agent_turn_id or payload.idempotency_key
        if registry.contains(turn_id):
            raise HTTPException(status_code=409, detail="Agent turn is already running")
        try:
            active_runtime = runtime or OpenAIAgentsRuntime(resolved, **archive_options())
            stream = active_runtime.start_execution_stream(payload)
        except TurnAlreadyRunningError as exc:
            raise HTTPException(status_code=409, detail="Agent turn is already running") from exc
        except ValueError as exc:
            raise HTTPException(status_code=503, detail=str(exc)) from exc

        registry.add(turn_id, stream)

        async def event_source():  # type: ignore[no-untyped-def]
            try:
                async for item in stream.events():
                    terminal = item["event"] in {
                        "agent.turn.committed",
                        "agent.turn.cancelled",
                    }
                    envelope = agent_event(
                        item["event"],
                        project_id=payload.project_id,
                        conversation_id=payload.conversation_id,
                        turn_id=turn_id,
                        payload=item["data"],
                        terminal=terminal,
                    )
                    yield encode_sse(envelope.model_dump(mode="json"))
            except (ValueError, BackendError, RuntimeCompatibilityError) as exc:
                cause_types, http_status = _failure_diagnostics(exc)
                logger.warning(
                    "streamed agent execution failed project_id=%s conversation_id=%s cause_types=%s http_status=%s",
                    payload.project_id,
                    payload.conversation_id,
                    cause_types,
                    http_status,
                )
                observation = (
                    exc.observation
                    if isinstance(exc, RuntimeCompatibilityError)
                    else {}
                )
                failure_stage = (
                    exc.failure_stage
                    if isinstance(exc, RuntimeCompatibilityError)
                    else "sidecar"
                )
                failed = agent_event(
                    "agent.turn.failed",
                    project_id=payload.project_id,
                    conversation_id=payload.conversation_id,
                    turn_id=turn_id,
                    payload={
                        "code": "AGENT_EXECUTION_FAILED",
                        "message": "Agent execution failed.",
                        "failure_stage": failure_stage or "sidecar",
                        "observation": observation,
                    },
                    terminal=True,
                )
                yield encode_sse(failed.model_dump(mode="json"))
            finally:
                stream.cancel("go_disconnected")

        async def close_stream() -> None:
            await stream.aclose("go_disconnected")
            registry.remove(turn_id)

        return _ClosingAgentResponse(
            event_source(),
            close_stream,
            headers={
                "Cache-Control": "no-cache",
                "X-Accel-Buffering": "no",
                "X-Sidecar-Run-ID": turn_id,
            },
        )

    @app.post(
        "/internal/v1/agent/runs/{run_id}/cancel",
        response_model=CancelResponse,
    )
    async def cancel_internal_agent(run_id: str) -> CancelResponse:
        return CancelResponse(run_id=run_id, accepted=registry.cancel(run_id))

    @app.post("/internal/v1/agent/runs/{run_id}/pause", response_model=CancelResponse)
    async def pause_internal_agent(run_id: str) -> CancelResponse:
        return CancelResponse(run_id=run_id, accepted=registry.pause(run_id))

    @app.post(
        "/internal/v1/revisions/execute",
        response_model=ArtifactRevisionResponse,
    )
    async def execute_revision(
        request: ArtifactRevisionRequest,
    ) -> ArtifactRevisionResponse:
        try:
            active_runtime = runtime or OpenAIAgentsRuntime(resolved, **archive_options())
            return await execute_artifact_revision(active_runtime, request)
        except ValueError as exc:
            raise HTTPException(status_code=503, detail=str(exc)) from exc
        except RuntimeCompatibilityError as exc:
            logger.warning(
                "SDK revision failed revision_request_id=%s: %s",
                request.revision_request_id,
                exc,
            )
            raise HTTPException(status_code=502, detail=str(exc)) from exc

    return app


app = create_app()
