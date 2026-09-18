"""Business-data checks for disposable rollback drill directories, never live data."""

from __future__ import annotations

import argparse
import base64
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import stat
from typing import Any
import urllib.error
import urllib.request


ROOT = Path(__file__).absolute().parent.parent
TIME = "2026-09-01T00:00:00Z"
MANIFEST_SCHEMA = "agent_rollback_fixture.v1"


def confined(root: Path, path: Path) -> Path:
    root = Path(os.path.abspath(root))
    path = Path(os.path.abspath(path if path.is_absolute() else root / path))
    if path == root or not path.is_relative_to(root):
        raise ValueError("Rollback path is outside the approved directory")
    for ancestor in (path, *path.parents):
        try:
            info = ancestor.lstat()
        except FileNotFoundError:
            continue
        if stat.S_ISLNK(info.st_mode) or getattr(info, "st_file_attributes", 0) & stat.FILE_ATTRIBUTE_REPARSE_POINT:
            raise ValueError("Rollback paths cannot traverse reparse points")
    return path


def encode(value: Any) -> bytes:
    return json.dumps(value, ensure_ascii=True, sort_keys=True, separators=(",", ":"), allow_nan=False).encode("ascii")


def digest(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def quote_identifier(value: str) -> str:
    return '"' + value.replace('"', '""') + '"'


def open_database(work_root: Path, database: Path, *, writable: bool = False) -> sqlite3.Connection:
    database = confined(work_root, database)
    connection = sqlite3.connect(database.as_uri() + ("?mode=rw" if writable else "?mode=ro"), uri=True)
    connection.execute("PRAGMA foreign_keys=ON")
    return connection


def database_snapshot(connection: sqlite3.Connection) -> dict[str, Any]:
    if connection.execute("PRAGMA integrity_check").fetchall() != [("ok",)]:
        raise ValueError("Database integrity check failed")
    if connection.execute("PRAGMA foreign_key_check").fetchall():
        raise ValueError("Database foreign-key check failed")
    schema = connection.execute(
        "SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name"
    ).fetchall()
    tables: dict[str, Any] = {}
    for kind, name, _, _ in schema:
        if kind != "table":
            continue
        columns = [row[1] for row in connection.execute(f"PRAGMA table_info({quote_identifier(name)})")]
        rows = []
        for row in connection.execute(f"SELECT * FROM {quote_identifier(name)}"):
            # Preserve SQLite value types and duplicate rows without depending on rowid or page layout.
            cells = [[type(cell).__name__, base64.b64encode(cell).decode("ascii") if isinstance(cell, bytes) else cell] for cell in row]
            rows.append(encode(cells).decode("ascii"))
        tables[name] = {"columns": columns, "rows": len(rows), "sha256": digest(encode(sorted(rows)))}
    result = {"schema_version": connection.execute("PRAGMA user_version").fetchone()[0],
              "schema_sha256": digest(encode(schema)), "tables": tables}
    return {**result, "sha256": digest(encode(result))}


def load_manifest(work_root: Path, manifest_path: Path) -> dict[str, Any]:
    manifest = json.loads(confined(work_root, manifest_path).read_text(encoding="utf-8"))
    if (manifest.get("schema_version") != MANIFEST_SCHEMA or not manifest.get("rows")
            or not manifest.get("blobs") or not manifest.get("http_checks")):
        raise ValueError("Invalid rollback fixture manifest")
    return manifest


def seed(work_root: Path, database: Path, manifest_path: Path) -> dict[str, Any]:
    database = confined(work_root, database)
    manifest_path = confined(work_root, manifest_path)
    if manifest_path.exists():
        raise ValueError("Fixture manifest already exists")
    connection = open_database(work_root, database, writable=True)
    rows: list[dict[str, Any]] = []
    blobs: list[dict[str, Any]] = []
    checks: list[dict[str, Any]] = []

    def insert(table: str, values: dict[str, Any]) -> None:
        columns = ",".join(map(quote_identifier, values))
        connection.execute(f"INSERT INTO {quote_identifier(table)} ({columns}) VALUES ({','.join('?' for _ in values)})", list(values.values()))
        key = next(iter(values))
        rows.append({"table": table, "key": key, "id": values[key], "values": values})

    try:
        connection.execute("BEGIN IMMEDIATE")
        if connection.execute("PRAGMA user_version").fetchone()[0] != 24:
            raise ValueError("The business fixture requires the pinned schema-24 baseline")
        for table in ("projects", "conversations", "messages", "runs", "artifacts", "assets", "asset_blobs"):
            if connection.execute(f"SELECT COUNT(*) FROM {quote_identifier(table)}").fetchone()[0]:
                raise ValueError("Refusing to seed a nonempty business database")
        for index in (1, 2):
            suffix = f"rollback_{index}"
            project, conversation, run, step = (f"{prefix}_{suffix}" for prefix in ("prj", "conv", "run", "step"))
            asset, snapshot, blob, artifact = (f"{prefix}_{suffix}" for prefix in ("ast", "ass", "blob", "art"))
            version_ids = [f"av_{suffix}_{version}" for version in (1, 2)]
            input_ids = [f"input_{suffix}_{version}" for version in (1, 2)]
            message_ids = [f"msg_{suffix}_{role}" for role in ("user", "assistant")]
            title = f"\u56de\u6eda\u9a8c\u8bc1 {index}"
            text = "\u4fdd\u7559\u539f\u6587\u4e0e\u5386\u53f2\u7248\u672c\u3002\n"
            insert("projects", dict(project_id=project, workspace_id=f"ws_{suffix}", title=title, version=2,
                status="active", primary_conversation_id=conversation, active_write_run_id=None,
                current_capability_id="novel_to_script", current_focus_artifact_version_id=version_ids[1],
                project_event_seq=1, created_at=TIME, updated_at=TIME, deleted_at=None))
            insert("conversations", dict(conversation_id=conversation, project_id=project, is_primary=1, created_at=TIME, updated_at=TIME))
            messages = []
            for role, message in zip(("user", "assistant"), message_ids):
                timestamp = TIME if role == "user" else "2026-09-01T00:00:01Z"
                values = dict(message_id=message, conversation_id=conversation, project_id=project, role=role, content=text, created_at=timestamp)
                insert("messages", values)
                messages.append(values)
                insert("message_contexts", dict(message_id=message, capability_ref_json=None, attachment_refs_json="[]", selection_snapshot_json=None, client_context_json="{}"))
                insert("message_routing_contexts", dict(message_id=message, scope="project", invocation_id=None, run_id=None, capability_id=None, artifact_id=None))
            content = text.encode("utf-8") if index == 1 else bytes(range(256)) + b"rollback-binary\x00\xff"
            relative = f"assets/{project}/{asset}/original"
            original = confined(work_root, database.parent / relative)
            saved_relative = f"fixture-backup-assets/{project}/{asset}/original"
            saved = confined(work_root, work_root / saved_relative)
            for destination in (original, saved):
                destination.parent.mkdir(parents=True, exist_ok=True)
                with destination.open("xb") as output:
                    output.write(content)
            mime = "text/plain" if index == 1 else "application/octet-stream"
            kind = "text" if index == 1 else "document"
            blob_record = dict(blob_id=blob, project_id=project, asset_id=asset, role="original", storage_ref=relative,
                size_bytes=len(content), checksum_algorithm="sha256", checksum=digest(content), status="available", created_at=TIME)
            insert("asset_blobs", blob_record)
            insert("assets", dict(asset_id=asset, project_id=project, current_snapshot_id=snapshot, kind=kind, source_type="upload",
                display_name="fixture.txt" if index == 1 else "fixture.bin", original_filename="fixture.txt" if index == 1 else "fixture.bin",
                extension=".txt" if index == 1 else ".bin", declared_mime_type=mime, detected_mime_type=mime,
                size_bytes=len(content), checksum_algorithm="sha256", checksum=digest(content), status="available",
                parse_status="parsed" if index == 1 else "unsupported", original_blob_id=blob, metadata_json="{}",
                retention_policy_id=None, uploaded_at=TIME, expires_at=None, created_at=TIME, updated_at=TIME, deleted_at=None, delete_reason=None))
            asset_payload = dict(asset_id=asset, project_id=project, kind=kind, status="available", original_blob_id=blob,
                storage_ref=relative, size_bytes=len(content), checksum_algorithm="sha256", checksum=digest(content), detected_mime_type=mime)
            insert("asset_snapshots", dict(asset_snapshot_id=snapshot, asset_id=asset, project_id=project, snapshot_version=1,
                status="available", payload_json=encode(asset_payload).decode(), created_at=TIME))
            blobs.append({"storage_ref": relative, "saved_ref": saved_relative, "size_bytes": len(content), "sha256": digest(content)})
            config = encode({"config_ref": "adaptation_strategy", "payload": {}}).decode()
            insert("runs", dict(run_id=run, project_id=project, conversation_id=conversation, capability_id="novel_to_script",
                capability_version="1.0.0", run_kind="pipeline", write_intent=1, status="completed", current_step_run_id=step,
                current_input_snapshot_version_id=input_ids[1], input_snapshot_status="sealed", config_snapshot_json=config,
                run_event_seq=0, started_at=TIME, ended_at=TIME, created_at=TIME, updated_at=TIME))
            insert("run_config_snapshots", dict(config_snapshot_id=f"cfg_{suffix}", run_id=run, config_ref="adaptation_strategy",
                version=1, status="sealed", payload_json=config, snapshot_hash=digest(config.encode()), created_at=TIME, sealed_at=TIME))
            insert("step_runs", dict(step_run_id=step, run_id=run, step_id="source_input", status="completed", attempt_count=1,
                approval_policy="manual", input_version_snapshot_json="{}", task_cursor_json="{}", started_at=TIME, ended_at=TIME))
            insert("artifacts", dict(artifact_id=artifact, project_id=project, run_id=run, step_run_id=step, capability_id="novel_to_script",
                artifact_type="source_input", scope_key="project", current_version_id=version_ids[1], created_at=TIME, updated_at=TIME))
            for version in (1, 2):
                payload = dict(input_snapshot_id=input_ids[version - 1], source_kind="novel",
                    assets=[dict(asset_id=asset, asset_snapshot_id=snapshot, role="primary_source", order=1)], user_request_message_id=message_ids[0])
                insert("run_input_snapshot_versions", dict(run_input_snapshot_version_id=input_ids[version - 1], run_id=run,
                    version=version, status="sealed", payload_json=encode(payload).decode(), created_at=TIME, sealed_at=TIME))
                status = "superseded" if version == 1 else "confirmed"
                insert("artifact_versions", dict(artifact_version_id=version_ids[version - 1], artifact_id=artifact,
                    version=version, status=status, payload_json=encode(payload).decode(), schema_id="common.sourceInput",
                    schema_version="1.0.0", created_by_kind="user", actor_ref="rollback-fixture", creation_reason="manual_revision",
                    base_version_id=None if version == 1 else version_ids[0], created_at=TIME, confirmed_at=TIME))
                checks.append({"path": f"/api/v1/artifact-versions/{version_ids[version - 1]}", "expected": {"data": {
                    "artifact_version_id": version_ids[version - 1], "version": version, "status": status, "payload": payload}}})
            insert("events", dict(event_id=f"evt_{suffix}", event_type="project.created", schema_version=1,
                project_id=project, run_id=None, step_run_id=None, project_event_seq=1, run_event_seq=None,
                actor_kind="user", actor_ref="rollback-fixture", subject_type="project", subject_id=project,
                payload_json=encode({"title": title}).decode(), occurred_at=TIME))
            insert("event_outbox", dict(event_id=f"evt_{suffix}", project_id=project, run_id=None, status="published",
                delivery_attempts=0, available_at=TIME, created_at=TIME, published_at=TIME))
            insert("idempotency_records", dict(idempotency_record_id=f"idem_{suffix}", scope=project, command_type="create_project",
                idempotency_key=f"request_{suffix}", request_hash=digest(title.encode()), status="completed",
                response_json=encode({"project_id": project}).decode(), error_code=None, error_message=None,
                created_at=TIME, completed_at=TIME, expires_at="2099-01-01T00:00:00Z"))
            checks.extend([
                {"path": f"/api/v1/projects/{project}", "expected": {"data": {"project_id": project, "title": title, "primary_conversation_id": conversation}}},
                {"path": f"/api/v1/conversations/{conversation}/messages", "expected": {"data": {"items": messages}}},
                {"path": f"/api/v1/artifacts/{artifact}", "expected": {"data": {"artifact_id": artifact, "current_version_id": version_ids[1]}}},
                {"path": f"/api/v1/assets/{asset}/content", "sha256": digest(content)},
            ])
        snapshot = database_snapshot(connection)
        manifest = {"schema_version": MANIFEST_SCHEMA, "database": snapshot, "rows": rows, "blobs": blobs, "http_checks": checks}
        connection.commit()
        manifest_path.parent.mkdir(parents=True, exist_ok=True)
        with manifest_path.open("xb") as output:
            output.write(encode(manifest) + b"\n")
        return {"status": "passed", "fixture_rows": len(rows), "blob_count": len(blobs), "database_sha256": snapshot["sha256"]}
    finally:
        connection.close()


def verify_database(work_root: Path, database: Path, manifest: dict[str, Any], *, exact: bool) -> dict[str, Any]:
    connection = open_database(work_root, database)
    try:
        connection.execute("BEGIN")
        observed = database_snapshot(connection)
        if exact:
            if observed != manifest["database"]:
                raise ValueError("Restored database differs from the complete pre-migration logical snapshot")
        else:
            for table in {record["table"] for record in manifest["rows"]}:
                if observed["tables"][table]["rows"] != manifest["database"]["tables"][table]["rows"]:
                    raise ValueError(f"Business row count changed during migration: {table}")
            for record in manifest["rows"]:
                columns = list(record["values"])
                result = connection.execute(
                    f"SELECT {','.join(map(quote_identifier, columns))} FROM {quote_identifier(record['table'])} WHERE {quote_identifier(record['key'])}=?",
                    (record["id"],),
                ).fetchall()
                if result != [tuple(record["values"][column] for column in columns)]:
                    raise ValueError(f"Business row changed during migration: {record['table']}")
        return observed
    finally:
        connection.close()


def verify_blobs(work_root: Path, database: Path, manifest: dict[str, Any], *, saved: bool = False) -> None:
    for blob in manifest["blobs"]:
        path = (confined(work_root / "fixture-backup-assets", work_root / blob["saved_ref"]) if saved
                else confined(database.parent, Path(blob["storage_ref"])))
        data = confined(work_root, path).read_bytes()
        if len(data) != blob["size_bytes"] or digest(data) != blob["sha256"]:
            raise ValueError("Rollback asset bytes do not match the pre-migration snapshot")


def select_backup(work_root: Path, database: Path, manifest: dict[str, Any], target_version: int) -> dict[str, Any]:
    database = confined(work_root, database)
    connection = open_database(work_root, database)
    baseline_version = manifest["database"]["schema_version"]
    try:
        connection.execute("BEGIN")
        if target_version <= baseline_version or connection.execute("PRAGMA user_version").fetchone()[0] != target_version:
            raise ValueError("Unexpected migrated database version")
        records = connection.execute(
            "SELECT migration_id,backup_ref,status,completed_at FROM migration_history WHERE from_version=? AND to_version=?",
            (baseline_version, target_version),
        ).fetchall()
    finally:
        connection.close()
    if len(records) != 1 or records[0][2] != "completed" or not records[0][3]:
        raise ValueError("Migration must have one unambiguous completed receipt")
    migration_id, reference, _, _ = records[0]
    if not re.fullmatch(rf"mig_{baseline_version}_to_{target_version}_[0-9]+", migration_id):
        raise ValueError("Invalid migration identity")
    backup = confined(work_root, Path(reference))
    expected = confined(work_root, database.parent / "backups" / migration_id / database.name)
    if backup != expected:
        raise ValueError("Migration backup does not match the exact recorded migration directory")
    observed = verify_database(work_root, backup, manifest, exact=True)
    return {"migration_id": migration_id, "backup_ref": str(backup), "database_sha256": observed["sha256"], "sha256": digest(backup.read_bytes())}


def restore(work_root: Path, database: Path, destination: Path, manifest: dict[str, Any], target_version: int) -> dict[str, Any]:
    destination = confined(work_root, destination)
    # A separate empty data directory proves that recovery does not depend on the live asset tree.
    if destination.parent.exists():
        raise ValueError("Rollback destination directory must not already exist")
    selected = select_backup(work_root, database, manifest, target_version)
    verify_blobs(work_root, database, manifest, saved=True)
    destination.parent.mkdir(parents=True)
    with destination.open("xb"):
        pass
    with closing(open_database(work_root, Path(selected["backup_ref"]))) as source:
        with closing(open_database(work_root, destination, writable=True)) as target:
            source.backup(target)
    for blob in manifest["blobs"]:
        original = confined(work_root / "fixture-backup-assets", work_root / blob["saved_ref"])
        restored = confined(destination.parent, Path(blob["storage_ref"]))
        restored.parent.mkdir(parents=True, exist_ok=True)
        with original.open("rb") as source_file, restored.open("xb") as target_file:
            shutil.copyfileobj(source_file, target_file)
    observed = verify_database(work_root, destination, manifest, exact=True)
    verify_blobs(work_root, destination, manifest)
    return {**selected, "status": "passed", "restored_database_sha256": observed["sha256"], "restored_blob_count": len(manifest["blobs"])}


def assert_subset(actual: Any, expected: Any) -> None:
    if isinstance(expected, dict):
        if not isinstance(actual, dict) or any(key not in actual for key in expected):
            raise ValueError("Missing restored API fields")
        for key, value in expected.items():
            assert_subset(actual[key], value)
    elif isinstance(expected, list):
        if not isinstance(actual, list) or len(actual) != len(expected):
            raise ValueError("Restored API list does not match")
        for item, value in zip(actual, expected):
            assert_subset(item, value)
    elif type(actual) is not type(expected) or actual != expected:
        raise ValueError("Restored API value does not match")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError("Rollback API redirects are forbidden")


def verify_http(manifest: dict[str, Any], port: int) -> dict[str, Any]:
    if not 1 <= port <= 65535 or port in (8860, 8880):
        raise ValueError("Rollback HTTP checks require an isolated loopback port")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    if not manifest.get("http_checks"):
        raise ValueError("Rollback HTTP checks must not be empty")
    for check in manifest["http_checks"]:
        if not re.fullmatch(r"/api/v1/[a-zA-Z0-9_/-]+", check["path"]):
            raise ValueError("Invalid rollback API check path")
        request = urllib.request.Request(f"http://127.0.0.1:{port}{check['path']}", method="GET")
        with opener.open(request, timeout=5) as response:
            if response.status != 200:
                raise ValueError("Rollback API check did not return 200")
            body = response.read(1024 * 1024 + 1)
        if len(body) > 1024 * 1024:
            raise ValueError("Rollback API response exceeds the fixture limit")
        if "sha256" in check:
            if digest(body) != check["sha256"]:
                raise ValueError("Restored asset download differs from original bytes")
        else:
            assert_subset(json.loads(body), check["expected"])
    return {"status": "passed", "http_checks": len(manifest["http_checks"])}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("seed", "verify-exact", "verify-migrated", "select-backup", "restore", "verify-http"))
    parser.add_argument("--work-root", type=Path, required=True)
    parser.add_argument("--database", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--target-version", type=int)
    parser.add_argument("--destination", type=Path)
    parser.add_argument("--port", type=int)
    args = parser.parse_args()
    if args.operation in ("select-backup", "restore") and args.target_version is None:
        parser.error("backup operations require --target-version")
    if args.operation == "verify-http" and args.port is None:
        parser.error("verify-http requires --port")
    work_root = confined(ROOT / ".tmp/agent-platform-rollback-drill", args.work_root)
    database = confined(work_root, args.database)
    if args.operation == "seed":
        result = seed(work_root, database, args.manifest)
    else:
        manifest = load_manifest(work_root, args.manifest)
        if args.operation in ("verify-exact", "verify-migrated"):
            observed = verify_database(work_root, database, manifest, exact=args.operation == "verify-exact")
            verify_blobs(work_root, database, manifest)
            result = {"status": "passed", "database_sha256": observed["sha256"]}
        elif args.operation == "select-backup":
            result = select_backup(work_root, database, manifest, args.target_version)
        elif args.operation == "restore":
            if args.destination is None:
                parser.error("restore requires --destination")
            result = restore(work_root, database, args.destination, manifest, args.target_version)
        else:
            result = verify_http(manifest, args.port)
    print(json.dumps(result, ensure_ascii=True, sort_keys=True))


if __name__ == "__main__":
    main()
