from __future__ import annotations

import asyncio
from contextlib import asynccontextmanager
from pathlib import Path
import sqlite3
import threading
from typing import Any
from weakref import WeakValueDictionary

from agents import AsyncOpenAI, SessionSettings, SQLiteSession
from agents.memory import OpenAIResponsesCompactionSession

from .backend import BackendClient


_lock_registry_guard = threading.Lock()
_bootstrap_locks: WeakValueDictionary[tuple[int, str, str], asyncio.Lock] = (
    WeakValueDictionary()
)
_turn_locks: WeakValueDictionary[tuple[int, str, str], asyncio.Lock] = (
    WeakValueDictionary()
)


def _shared_lock(
    registry: WeakValueDictionary[tuple[int, str, str], asyncio.Lock],
    db_path: str,
    session_id: str,
) -> asyncio.Lock:
    key = (id(asyncio.get_running_loop()), db_path, session_id)
    with _lock_registry_guard:
        lock = registry.get(key)
        if lock is None:
            lock = asyncio.Lock()
            registry[key] = lock
        return lock


@asynccontextmanager
async def sdk_session_turn(db_path: str, session_id: str):
    """Serialize complete Runner turns that share one SDK Session."""

    lock = _shared_lock(_turn_locks, db_path, session_id)
    async with lock:
        yield


class SDKMemorySession:
    """Persistent SDK Session bootstrapped once from the Go conversation log.

    Go remains the authoritative user-visible conversation and business store.
    The SDK database owns the model-facing item history, including tool items and
    compacted state, after the first bootstrap.
    """

    session_settings: SessionSettings | None = None

    def __init__(
        self,
        backend: BackendClient,
        project_id: str,
        conversation_id: str,
        *,
        db_path: str = ":memory:",
        current_user_content: str = "",
    ) -> None:
        self.session_id = f"{project_id}:{conversation_id}"
        self._backend = backend
        self._conversation_id = conversation_id
        self._current_user_content = current_user_content.strip()
        self._db_path = db_path
        self._bootstrapped = False
        if db_path != ":memory:":
            Path(db_path).expanduser().resolve().parent.mkdir(parents=True, exist_ok=True)
        self._delegate = SQLiteSession(self.session_id, db_path)

    async def get_items(self, limit: int | None = None) -> list[dict[str, Any]]:
        await self._ensure_bootstrapped()
        return await self._delegate.get_items(limit)

    async def add_items(self, items: list[dict[str, Any]]) -> None:
        await self._ensure_bootstrapped()
        await self._delegate.add_items(items)

    async def pop_item(self) -> dict[str, Any] | None:
        await self._ensure_bootstrapped()
        return await self._delegate.pop_item()

    async def clear_session(self) -> None:
        self._bootstrapped = True
        await self._delegate.clear_session()

    def close(self) -> None:
        self._delegate.close()

    async def _ensure_bootstrapped(self) -> None:
        if self._bootstrapped:
            return
        async with _shared_lock(
            _bootstrap_locks, self._db_path, self.session_id
        ):
            if self._bootstrapped:
                return
            if await asyncio.to_thread(self._has_bootstrap_marker):
                self._bootstrapped = True
                return
            if await self._delegate.get_items(limit=1):
                await asyncio.to_thread(self._write_bootstrap_marker)
                self._bootstrapped = True
                return

            payload = await self._backend.get_conversation_messages(
                self._conversation_id,
                limit=0,
            )
            items = [
                {"role": message["role"], "content": message["content"]}
                for message in payload.get("items", [])
                if isinstance(message, dict)
                and message.get("role") in {"user", "assistant"}
                and isinstance(message.get("content"), str)
                and message["content"].strip()
            ]
            if (
                items
                and items[-1]["role"] == "user"
                and items[-1]["content"].strip() == self._current_user_content
            ):
                items.pop()
            if items:
                await self._delegate.add_items(items)
            await asyncio.to_thread(self._write_bootstrap_marker)
            self._bootstrapped = True

    def _has_bootstrap_marker(self) -> bool:
        if self._db_path == ":memory:":
            return False
        connection = sqlite3.connect(self._db_path)
        try:
            self._ensure_bootstrap_table(connection)
            row = connection.execute(
                "SELECT 1 FROM content_agent_session_bootstrap WHERE session_id = ?",
                (self.session_id,),
            ).fetchone()
        finally:
            connection.close()
        return row is not None

    def _write_bootstrap_marker(self) -> None:
        if self._db_path == ":memory:":
            return
        connection = sqlite3.connect(self._db_path)
        try:
            self._ensure_bootstrap_table(connection)
            connection.execute(
                "INSERT OR IGNORE INTO content_agent_session_bootstrap(session_id) VALUES (?)",
                (self.session_id,),
            )
            connection.commit()
        finally:
            connection.close()

    @staticmethod
    def _ensure_bootstrap_table(connection: sqlite3.Connection) -> None:
        connection.execute(
            "CREATE TABLE IF NOT EXISTS content_agent_session_bootstrap ("
            "session_id TEXT PRIMARY KEY, bootstrapped_at TEXT NOT NULL "
            "DEFAULT CURRENT_TIMESTAMP)"
        )


class NativeCompactionProtocolError(RuntimeError):
    failure_stage = "session_compaction"
    code = "NATIVE_COMPACTION_PROTOCOL_UNSUPPORTED"

    def __init__(self) -> None:
        super().__init__("模型网关未返回有效的原生 compaction 项；已保留原会话，请修复网关的 Responses compact 协议后重试。")


def native_compaction_output_valid(output_items: list[dict[str, Any]]) -> bool:
    native_items = [item for item in output_items if isinstance(item, dict) and item.get("type") == "compaction"]
    return bool(native_items) and all(
        isinstance(item.get("encrypted_content"), str) and bool(item["encrypted_content"].strip())
        and isinstance(item.get("id"), str) and bool(item["id"].strip())
        for item in native_items
    )


class PersistentResponsesCompactionSession(OpenAIResponsesCompactionSession):
    """SDK-native Responses compaction with explicit SQLite cleanup."""

    async def _replace_underlying_session_items(self, *, output_items, previous_items) -> None:
        # The pinned SDK 0.21.1 replacement hook is reached before any clear/add.
        # Some compatible gateways return a plaintext user summary with HTTP 200.
        if not native_compaction_output_valid(output_items):
            raise NativeCompactionProtocolError()
        await super()._replace_underlying_session_items(output_items=output_items, previous_items=previous_items)

    def close(self) -> None:
        close = getattr(self.underlying_session, "close", None)
        if callable(close):
            close()


def create_sdk_session(
    backend: BackendClient,
    project_id: str,
    conversation_id: str,
    *,
    client: AsyncOpenAI,
    model: str,
    db_path: str,
    current_user_content: str,
) -> PersistentResponsesCompactionSession:
    underlying = SDKMemorySession(
        backend,
        project_id,
        conversation_id,
        db_path=db_path,
        current_user_content=current_user_content,
    )
    return PersistentResponsesCompactionSession(
        session_id=underlying.session_id,
        underlying_session=underlying,
        client=client,
        model=model,
        compaction_mode="input",
    )


async def prepare_sdk_session(
    session: PersistentResponsesCompactionSession,
) -> tuple[list[dict[str, Any]], int]:
    """Bootstrap and compact SDK history before it is rendered for a Runner turn."""

    previous_items = await session.get_items()
    await session.run_compaction()
    current_items = await session.get_items()
    return previous_items, len(current_items)
