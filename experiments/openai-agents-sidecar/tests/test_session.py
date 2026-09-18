from __future__ import annotations

import asyncio
from pathlib import Path
from types import SimpleNamespace
from typing import Any
from uuid import uuid4

import pytest
from agents import AsyncOpenAI, Session

from content_agent_sidecar.session import (
    NativeCompactionProtocolError,
    PersistentResponsesCompactionSession,
    SDKMemorySession,
    create_sdk_session,
    prepare_sdk_session,
    sdk_session_turn,
)


@pytest.mark.parametrize("output", [
    [], [{"role": "user", "content": "plaintext gateway summary"}],
    [{"type": "compaction", "id": "cmp_1"}],
    [{"type": "compaction", "id": "cmp_1", "encrypted_content": ""}],
])
def test_non_native_compaction_cannot_replace_history(session_db_path, output):
    original = [{"role": "user", "content": "important input"}, {"role": "assistant", "content": "original answer"}]

    async def compact(**kwargs):
        assert kwargs["input"] == original
        return SimpleNamespace(output=output, usage=None)

    session = create_sdk_session(FakeBackend(original), "p", "c", client=SimpleNamespace(responses=SimpleNamespace(compact=compact)), model="gpt-test", db_path=session_db_path, current_user_content="")

    async def exercise():
        with pytest.raises(NativeCompactionProtocolError):
            await session.run_compaction({"force": True})
        assert await session.get_items() == original

    try:
        asyncio.run(exercise())
    finally:
        session.close()
    restored = SDKMemorySession(FakeBackend([]), "p", "c", db_path=session_db_path)
    try:
        assert asyncio.run(restored.get_items()) == original
    finally:
        restored.close()


def test_native_compaction_items_are_persisted_and_used_without_rewriting(session_db_path):
    encrypted = {"type": "compaction", "id": "cmp_fixture", "encrypted_content": "opaque-test-fixture"}
    output = [{"role": "user", "content": "keep this input"}, encrypted]

    async def compact(**kwargs):
        return SimpleNamespace(output=output, usage=None)

    client = SimpleNamespace(responses=SimpleNamespace(compact=compact))
    session = create_sdk_session(FakeBackend([{ "role": "assistant", "content": "old"}]), "p", "c", client=client, model="gpt-test", db_path=session_db_path, current_user_content="")
    try:
        asyncio.run(session.run_compaction({"force": True}))
        assert asyncio.run(session.get_items()) == output
    finally:
        session.close()
    restored = create_sdk_session(FakeBackend([]), "p", "c", client=client, model="gpt-test", db_path=session_db_path, current_user_content="")
    try:
        assert asyncio.run(restored.get_items()) == output
    finally:
        restored.close()


from instruction_fixtures import EmptyInstructionsBackend


class FakeBackend(EmptyInstructionsBackend):
    def __init__(self, messages: list[dict[str, Any]]) -> None:
        self.messages = messages
        self.calls: list[tuple[str, int]] = []

    async def get_conversation_messages(
        self, conversation_id: str, limit: int = 100
    ) -> dict[str, Any]:
        self.calls.append((conversation_id, limit))
        items = self.messages[-limit:] if limit > 0 else self.messages
        return {"items": items, "count": len(items)}


def test_independent_in_memory_sessions_bootstrap_independently() -> None:
    backend = FakeBackend([{"role": "user", "content": "history"}])
    for _ in range(2):
        session = SDKMemorySession(backend, "same-project", "same-conversation")
        try:
            assert asyncio.run(session.get_items()) == [{"role": "user", "content": "history"}]
        finally:
            session.close()
    assert len(backend.calls) == 2


@pytest.fixture
def session_db_path() -> str:
    path = Path(__file__).parent / ".test-data" / f"{uuid4().hex}.db"
    path.parent.mkdir(exist_ok=True)
    yield str(path)
    for candidate in path.parent.glob(f"{path.name}*"):
        candidate.unlink(missing_ok=True)


def test_sdk_memory_session_implements_sdk_protocol(session_db_path: str) -> None:
    session = SDKMemorySession(
        FakeBackend([]),  # type: ignore[arg-type]
        "project",
        "conversation",
        db_path=session_db_path,
    )

    assert isinstance(session, Session)
    assert session.session_id == "project:conversation"
    session.close()


def test_sdk_memory_session_bootstraps_history_once_and_excludes_current_user(
    session_db_path: str,
) -> None:
    backend = FakeBackend([
        {"role": "user", "content": "第一轮"},
        {"role": "assistant", "content": "第一轮回复"},
        {"role": "user", "content": "当前问题"},
    ])
    session = SDKMemorySession(
        backend,  # type: ignore[arg-type]
        "project",
        "conversation",
        db_path=session_db_path,
        current_user_content="当前问题",
    )

    items = asyncio.run(session.get_items())
    items_again = asyncio.run(session.get_items())

    assert items == [
        {"role": "user", "content": "第一轮"},
        {"role": "assistant", "content": "第一轮回复"},
    ]
    assert items_again == items
    assert backend.calls == [("conversation", 0)]
    session.close()


def test_sdk_memory_session_persists_sdk_items_across_instances(
    session_db_path: str,
) -> None:
    backend = FakeBackend([])
    first = SDKMemorySession(
        backend,  # type: ignore[arg-type]
        "project",
        "conversation",
        db_path=session_db_path,
    )
    asyncio.run(first.add_items([{"role": "assistant", "content": "SDK 持久回复"}]))
    first.close()

    second = SDKMemorySession(
        backend,  # type: ignore[arg-type]
        "project",
        "conversation",
        db_path=session_db_path,
    )

    assert asyncio.run(second.get_items()) == [
        {"role": "assistant", "content": "SDK 持久回复"}
    ]
    assert backend.calls == [("conversation", 0)]
    second.close()


def test_sdk_memory_session_persists_empty_bootstrap_across_instances(
    session_db_path: str,
) -> None:
    backend = FakeBackend([])
    first = SDKMemorySession(
        backend,  # type: ignore[arg-type]
        "project",
        "conversation",
        db_path=session_db_path,
    )
    assert asyncio.run(first.get_items()) == []
    first.close()

    second = SDKMemorySession(
        backend,  # type: ignore[arg-type]
        "project",
        "conversation",
        db_path=session_db_path,
    )
    assert asyncio.run(second.get_items()) == []
    assert backend.calls == [("conversation", 0)]
    second.close()


def test_sdk_memory_session_serializes_concurrent_bootstrap(
    session_db_path: str,
) -> None:
    backend = FakeBackend([{"role": "user", "content": "历史消息"}])

    async def exercise() -> list[list[dict[str, Any]]]:
        first = SDKMemorySession(
            backend,  # type: ignore[arg-type]
            "project",
            "conversation",
            db_path=session_db_path,
        )
        second = SDKMemorySession(
            backend,  # type: ignore[arg-type]
            "project",
            "conversation",
            db_path=session_db_path,
        )
        try:
            return await asyncio.gather(first.get_items(), second.get_items())
        finally:
            first.close()
            second.close()

    results = asyncio.run(exercise())
    assert results == [
        [{"role": "user", "content": "历史消息"}],
        [{"role": "user", "content": "历史消息"}],
    ]
    assert backend.calls == [("conversation", 0)]


def test_sdk_session_turn_serializes_same_session() -> None:
    active = 0
    maximum_active = 0

    async def worker() -> None:
        nonlocal active, maximum_active
        async with sdk_session_turn(":memory:", "project:conversation"):
            active += 1
            maximum_active = max(maximum_active, active)
            await asyncio.sleep(0.01)
            active -= 1

    async def exercise() -> None:
        await asyncio.gather(worker(), worker())

    asyncio.run(exercise())
    assert maximum_active == 1


def test_sdk_memory_session_isolated_by_project_and_conversation(
    session_db_path: str,
) -> None:
    backend = FakeBackend([])
    first = SDKMemorySession(
        backend,  # type: ignore[arg-type]
        "project-a",
        "conversation-a",
        db_path=session_db_path,
    )
    second = SDKMemorySession(
        backend,  # type: ignore[arg-type]
        "project-b",
        "conversation-b",
        db_path=session_db_path,
    )

    assert first.session_id != second.session_id
    first.close()
    second.close()


def test_factory_wraps_persistent_session_with_input_compaction(
    session_db_path: str,
) -> None:
    session = create_sdk_session(
        FakeBackend([]),  # type: ignore[arg-type]
        "project",
        "conversation",
        client=AsyncOpenAI(api_key="test", base_url="http://127.0.0.1:9/v1"),
        model="gpt-test-model",
        db_path=session_db_path,
        current_user_content="",
    )

    assert isinstance(session, PersistentResponsesCompactionSession)
    assert session.compaction_mode == "input"
    assert isinstance(session.underlying_session, SDKMemorySession)
    session.close()


def test_factory_uses_sdk_native_compaction_without_summary_fallback(
    session_db_path: str,
) -> None:
    session = create_sdk_session(
        FakeBackend([]),  # type: ignore[arg-type]
        "project",
        "conversation",
        client=AsyncOpenAI(api_key="test", base_url="http://127.0.0.1:9/v1"),
        model="gpt-test-model",
        db_path=session_db_path,
        current_user_content="",
    )

    assert not hasattr(session, "_fallback_summarizer")
    assert session.should_trigger_compaction is not None
    session.close()


def test_prepare_sdk_session_compacts_before_checkpoint() -> None:
    class FakeCompactionSession:
        def __init__(self) -> None:
            self.items = [
                {"role": "user", "content": "旧问题"},
                {"type": "reasoning", "id": "reasoning_1"},
            ]
            self.calls: list[str] = []

        async def get_items(self) -> list[dict[str, Any]]:
            self.calls.append("get_items")
            return list(self.items)

        async def run_compaction(self) -> None:
            self.calls.append("run_compaction")
            self.items = [{"type": "compaction", "encrypted_content": "opaque"}]

    session = FakeCompactionSession()
    previous_items, checkpoint = asyncio.run(
        prepare_sdk_session(session)  # type: ignore[arg-type]
    )

    assert previous_items[0]["content"] == "旧问题"
    assert checkpoint == 1
    assert session.calls == ["get_items", "run_compaction", "get_items"]
