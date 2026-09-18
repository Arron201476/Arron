"""Private write-ahead archive storage; never an SDK Session or tool authority."""

from dataclasses import dataclass, field
from hashlib import sha256
import json
import logging
import math
from pathlib import Path
import sqlite3
import time

from .backend import BackendError
from .memory_rollout import MemoryArchivePolicy, MemoryRollout


logger = logging.getLogger(__name__)


def open_memory_archive_queue(path: str, session_path: str):
    if not path:
        return None
    try:
        target = Path(path).expanduser()
        session = Path(session_path).expanduser()
        session_alias = session_path != ":memory:" and (
            target.resolve() == session.resolve()
            or target.exists() and session.exists() and target.samefile(session))
        invalid = path == ":memory:" or not target.is_absolute() or target.is_symlink() or session_alias
    except OSError:
        raise ValueError("Memory archive database identity could not be verified") from None
    if invalid:
        raise ValueError("Memory archive requires its own absolute database path")
    connection = None
    try:
        connection = sqlite3.connect(str(target), timeout=5)
        tables = {row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE type='table'")}
        if tables - {"pending_memory_archives"}:
            raise ValueError("Memory archive database contains unrelated tables")
        return MemoryArchiveQueue(connection)
    except Exception:
        if connection is not None:
            connection.close()
        raise BackendError("Private memory archive database could not be opened") from None


@dataclass(frozen=True)
class PendingMemoryArchive:
    entry_id: str
    payload_hash: str
    payload: dict = field(repr=False)


class MemoryArchiveQueue:
    """Own one dedicated connection; callers serialize access to this queue."""

    def __init__(self, connection: sqlite3.Connection, *, max_bytes=64 << 20, retention_seconds=86400, clock=time.time):
        if (connection.in_transaction or type(max_bytes) is not int or not 1 <= max_bytes <= 256 << 20
                or type(retention_seconds) is not int or not 60 <= retention_seconds <= 86400):
            raise ValueError("Invalid private archive queue configuration")
        self._db, self._max_bytes, self._retention, self._clock = connection, max_bytes, retention_seconds, clock
        # DELETE must also clear private payloads from reusable database pages.
        # This does not erase external backups or historical filesystem copies.
        if self._db.execute("PRAGMA secure_delete=ON").fetchone() != (1,):
            raise BackendError("Private archive storage cannot guarantee page cleanup")
        self._db.execute("""CREATE TABLE IF NOT EXISTS pending_memory_archives (
            entry_id TEXT PRIMARY KEY, payload_hash TEXT NOT NULL, payload TEXT NOT NULL,
            size_bytes INTEGER NOT NULL CHECK(size_bytes>0), expires_at REAL NOT NULL)""")
        columns = [(row[1], row[2].upper(), row[3], row[5])
                   for row in self._db.execute("PRAGMA table_info(pending_memory_archives)")]
        expected = [("entry_id", "TEXT", 0, 1), ("payload_hash", "TEXT", 1, 0),
                    ("payload", "TEXT", 1, 0), ("size_bytes", "INTEGER", 1, 0),
                    ("expires_at", "REAL", 1, 0)]
        if columns != expected or self._db.execute(
                "SELECT 1 FROM sqlite_master WHERE type='trigger' AND tbl_name='pending_memory_archives' LIMIT 1"
        ).fetchone() is not None:
            raise BackendError("Private archive database schema is incompatible")
        self._db.commit()

    def _now(self):
        now = self._clock()
        if type(now) not in (int, float) or not math.isfinite(now):
            raise ValueError("Invalid archive queue clock")
        return now

    def put(self, rollout: MemoryRollout, policy: MemoryArchivePolicy) -> PendingMemoryArchive:
        rollout = MemoryRollout.model_validate(rollout.model_dump())
        policy = MemoryArchivePolicy.model_validate(policy.model_dump())
        if not policy.archive_enabled or policy.project_id != rollout.project_id or policy.user_id != rollout.user_id:
            raise BackendError("Private archive queue requires original owner consent")
        payload = {"rollout": rollout.model_dump(), "consent_revision": policy.revision, "generate_enabled": policy.generate_enabled}
        encoded = json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False)
        key = json.dumps([rollout.workspace_id, rollout.user_id, rollout.project_id, rollout.activity_key, rollout.segment_id], separators=(",", ":"))
        entry = PendingMemoryArchive(sha256(key.encode()).hexdigest(), sha256(encoded.encode()).hexdigest(), payload)
        now, size = self._now(), len(encoded.encode())
        with self._db:
            self._db.execute("DELETE FROM pending_memory_archives WHERE expires_at<=?", (now,))
            old = self._db.execute("SELECT payload_hash,payload FROM pending_memory_archives WHERE entry_id=?", (entry.entry_id,)).fetchone()
            if old is not None:
                if old != (entry.payload_hash, encoded):
                    raise BackendError("Private archive queue entry already has different frozen content")
                return entry
            count, total = self._db.execute("SELECT COUNT(*),COALESCE(SUM(size_bytes),0) FROM pending_memory_archives").fetchone()
            if count >= 256 or total + size > self._max_bytes:
                raise BackendError("Private archive queue capacity reached")
            self._db.execute("INSERT INTO pending_memory_archives VALUES(?,?,?,?,?)",
                             (entry.entry_id, entry.payload_hash, encoded, size, now + self._retention))
        return entry

    def pending(self, *, limit=16, skip_invalid=False) -> list[PendingMemoryArchive]:
        if type(limit) is not int or not 1 <= limit <= 256 or type(skip_invalid) is not bool:
            raise ValueError("Invalid archive queue page size")
        with self._db:
            self._db.execute("DELETE FROM pending_memory_archives WHERE expires_at<=?", (self._now(),))
            rows = self._db.execute("SELECT entry_id,payload_hash,payload,size_bytes FROM pending_memory_archives ORDER BY expires_at,entry_id LIMIT ?", (limit,)).fetchall()
        result = []
        invalid_count = 0
        for key, digest, encoded, size in rows:
            try:
                if (not all(isinstance(value, str) for value in (key, digest, encoded))
                        or type(size) is not int or size < 1):
                    raise ValueError("storage types")
                if len(encoded.encode()) != size or sha256(encoded.encode()).hexdigest() != digest:
                    raise ValueError("hash")
                value = json.loads(encoded)
                if set(value) != {"rollout", "consent_revision", "generate_enabled"}:
                    raise ValueError("fields")
                rollout = MemoryRollout.model_validate(value["rollout"])
                MemoryArchivePolicy(project_id=rollout.project_id, user_id=rollout.user_id, archive_enabled=True,
                    generate_enabled=value["generate_enabled"], revision=value["consent_revision"])
                identity = json.dumps([rollout.workspace_id, rollout.user_id, rollout.project_id, rollout.activity_key, rollout.segment_id], separators=(",", ":"))
                if sha256(identity.encode()).hexdigest() != key:
                    raise ValueError("identity")
            except (ValueError, TypeError, KeyError, UnicodeError):
                if not skip_invalid:
                    raise BackendError("Private archive queue failed integrity validation") from None
                invalid_count += 1
                continue
            result.append(PendingMemoryArchive(key, digest, value))
        if invalid_count:
            logger.warning("Private archive queue integrity failures count=%d; invalid entries retained", invalid_count)
        return result

    def is_pending(self, entry: PendingMemoryArchive) -> bool:
        with self._db:
            self._db.execute("DELETE FROM pending_memory_archives WHERE expires_at<=?", (self._now(),))
            return self._db.execute(
                "SELECT 1 FROM pending_memory_archives WHERE entry_id=? AND payload_hash=?",
                (entry.entry_id, entry.payload_hash),
            ).fetchone() is not None

    def acknowledge(self, entry: PendingMemoryArchive):
        with self._db:
            self._db.execute("DELETE FROM pending_memory_archives WHERE entry_id=? AND payload_hash=?", (entry.entry_id, entry.payload_hash))

    def close(self):
        self._db.close()
