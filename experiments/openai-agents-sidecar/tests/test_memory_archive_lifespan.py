import asyncio
from dataclasses import replace
import importlib
from pathlib import Path
import sqlite3

import pytest

from content_agent_sidecar.backend import BackendError
from content_agent_sidecar.memory_archive_queue import open_memory_archive_queue
from test_app import settings


app_module = importlib.import_module("content_agent_sidecar.app")


@pytest.mark.parametrize("fail_start", [False, True])
def test_archive_queue_is_shared_and_closed_after_workers(monkeypatch, fail_start):
    events = []
    class Queue:
        def close(self): events.append("close")
    queue = Queue()
    def open_queue(path, session):
        assert path == "configured-private-path"
        events.append("open")
        return queue
    class Worker:
        def __init__(self, config, *, slot, archive_queue):
            assert archive_queue is queue
            events.append("create")
        def start(self):
            events.append("start")
            if fail_start: raise ValueError("startup failed")
        async def stop(self): events.append("stop")
    class ArchiveWorker:
        @classmethod
        def from_settings(cls, config, supplied):
            assert supplied is queue
            return cls()
        def start(self): events.append("archive-start")
        async def stop(self): events.append("archive-stop")
    monkeypatch.setattr(app_module, "open_memory_archive_queue", open_queue)
    monkeypatch.setattr(app_module, "SDKTaskWorker", Worker)
    monkeypatch.setattr(app_module, "SDKBackgroundTaskWorker", Worker)
    monkeypatch.setattr(app_module, "MemoryArchiveWorker", ArchiveWorker)
    async def run():
        app = app_module.create_app(replace(settings(), memory_archive_db_path="configured-private-path", task_worker_enabled=True))
        if fail_start:
            with pytest.raises(ValueError, match="startup failed"):
                async with app.router.lifespan_context(app): pytest.fail("served after failed startup")
        else:
            async with app.router.lifespan_context(app):
                assert "close" not in events
        assert events[-1] == "close" and events.count("stop") == (1 if fail_start else 2)
        assert events.count("archive-stop") == 1
    asyncio.run(run())


def test_archive_factory_rejects_session_alias_and_nonpersistent_paths():
    target = str(Path.cwd() / "dedicated-archive.sqlite")
    assert open_memory_archive_queue("", ":memory:") is None
    for path, session in [(target, target), ("relative.sqlite", ":memory:"), (":memory:", ":memory:")]:
        with pytest.raises(ValueError): open_memory_archive_queue(path, session)


@pytest.mark.parametrize("stat_error", [False, True])
def test_archive_factory_rejects_same_file_identity_or_unverifiable_alias(monkeypatch, stat_error):
    target = str(Path.cwd() / "archive-alias.sqlite")
    session = str(Path.cwd() / "session-origin.sqlite")
    monkeypatch.setattr(Path, "exists", lambda self: True)
    def samefile(self, other):
        if stat_error:
            raise PermissionError("PRIVATE_PATH")
        return True
    monkeypatch.setattr(Path, "samefile", samefile)
    def forbidden_connect(*args, **kwargs):
        pytest.fail("Opened a database before validating file identity")
    monkeypatch.setattr(sqlite3, "connect", forbidden_connect)
    with pytest.raises(ValueError) as failure:
        open_memory_archive_queue(target, session)
    assert "PRIVATE_PATH" not in str(failure.value)


@pytest.mark.parametrize("foreign", [False, True])
def test_archive_factory_owns_connection_and_rejects_foreign_tables(monkeypatch, foreign):
    db = sqlite3.connect(":memory:")
    if foreign:
        db.execute("CREATE TABLE sdk_sessions (private_body TEXT)")
        db.commit()
    monkeypatch.setattr(sqlite3, "connect", lambda *args, **kwargs: db)
    path = str(Path.cwd() / "unused-factory-test.sqlite")
    if foreign:
        with pytest.raises(BackendError, match="could not be opened"):
            open_memory_archive_queue(path, ":memory:")
        with pytest.raises(sqlite3.ProgrammingError): db.execute("SELECT 1")
    else:
        queue = open_memory_archive_queue(path, ":memory:")
        assert queue.pending() == []
        queue.close()
        with pytest.raises(sqlite3.ProgrammingError): db.execute("SELECT 1")


@pytest.mark.parametrize("schema", ["missing-column", "wrong-type", "nullable", "trigger"])
def test_archive_factory_rejects_incompatible_schema_and_closes_connection(monkeypatch, schema):
    from content_agent_sidecar.memory_archive_queue import MemoryArchiveQueue
    db = sqlite3.connect(":memory:")
    if schema == "trigger":
        MemoryArchiveQueue(db)
        db.execute("CREATE TRIGGER ignore_archive_delete BEFORE DELETE ON pending_memory_archives BEGIN SELECT RAISE(IGNORE); END")
    else:
        fields = "entry_id TEXT PRIMARY KEY,payload_hash TEXT NOT NULL,payload TEXT NOT NULL,size_bytes INTEGER NOT NULL,expires_at REAL NOT NULL"
        if schema == "missing-column": fields = fields.replace(",expires_at REAL NOT NULL", "")
        if schema == "wrong-type": fields = fields.replace("payload TEXT", "payload BLOB")
        if schema == "nullable": fields = fields.replace("payload TEXT NOT NULL", "payload TEXT")
        db.execute("CREATE TABLE pending_memory_archives (" + fields + ")")
    db.commit()
    monkeypatch.setattr(sqlite3, "connect", lambda *args, **kwargs: db)
    with pytest.raises(BackendError, match="could not be opened"):
        open_memory_archive_queue(str(Path.cwd() / "unused-schema-test.sqlite"), ":memory:")
    with pytest.raises(sqlite3.ProgrammingError): db.execute("SELECT 1")
