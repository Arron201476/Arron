from __future__ import annotations

import os
import sqlite3
import sys

if sqlite3.sqlite_version_info < (3, 35, 0):
    try:
        import pysqlite3
    except ImportError as exc:
        raise RuntimeError(
            "SQLite 3.35 or newer is required for the SDK Session store"
        ) from exc
    sys.modules["sqlite3"] = pysqlite3

import uvicorn


def main() -> None:
    port = int(os.getenv("CONTENT_AGENT_SIDECAR_PORT", "8871"))
    uvicorn.run(
        "content_agent_sidecar.app:app",
        host="127.0.0.1",
        port=port,
        log_level="info",
    )


if __name__ == "__main__":
    main()
