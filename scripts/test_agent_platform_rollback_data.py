"""Exercise rollback helpers with schema-derived SQLite fixtures, not Go migrations."""

import copy
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import re
import sqlite3
import subprocess
import sys
import threading
import unittest
from unittest.mock import patch
import uuid

import agent_platform_rollback_data as rollback


class RollbackDataTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = subprocess.run(
            ["git", "show", "pre-agent-convergence-20260904:backend/internal/runtime/store.go"],
            cwd=rollback.ROOT, capture_output=True, check=True, encoding="utf-8",
        ).stdout
        definitions = re.findall(r"(?ms)^const schema = `\n(.*?)\n`", source)
        if len(definitions) != 1:
            raise AssertionError("Pinned baseline SQL schema was not uniquely identified")
        cls.schema = definitions[0]
        cls.fixture_root = rollback.confined(rollback.ROOT, rollback.ROOT / ".tmp/agent-platform-rollback-drill" / ("tests-" + uuid.uuid4().hex))
        cls.fixture_root.mkdir(parents=True)

    def setUp(self):
        self.root = rollback.confined(self.fixture_root, self.fixture_root / self._testMethodName)
        self.database = self.root / "data/content_agent.db"
        self.database.parent.mkdir(parents=True)
        with sqlite3.connect(self.database) as connection:
            connection.executescript("PRAGMA foreign_keys=ON;" + self.schema)
            connection.execute("PRAGMA user_version=24")
        self.manifest_path = self.root / "business-manifest.json"

    def seed(self):
        result = rollback.seed(self.root, self.database, self.manifest_path)
        self.assertEqual(result["status"], "passed")
        return rollback.load_manifest(self.root, self.manifest_path)

    def migrate_fixture(self, manifest):
        # This is only a VACUUM backup + synthetic migration receipt, never the Runtime migrator.
        migration_id = "mig_24_to_63_123456789"
        backup = self.database.parent / "backups" / migration_id / self.database.name
        backup.parent.mkdir(parents=True)
        with sqlite3.connect(self.database) as connection:
            connection.execute("VACUUM INTO ?", (str(backup),))
            connection.execute("PRAGMA user_version=63")
            connection.execute(
                "INSERT INTO migration_history(migration_id,from_version,to_version,backup_ref,status,started_at,completed_at) VALUES(?,24,63,?,'completed',?,?)",
                (migration_id, str(backup), rollback.TIME, rollback.TIME),
            )
        return backup

    def change_receipt(self, field, value):
        with sqlite3.connect(self.database) as connection:
            connection.execute(f"UPDATE migration_history SET {rollback.quote_identifier(field)}=?", (value,))

    def test_seed_has_business_history_and_distinct_asset_bytes(self):
        manifest = self.seed()
        snapshot = rollback.verify_database(self.root, self.database, manifest, exact=True)
        for table, count in {"projects": 2, "conversations": 2, "messages": 4, "artifacts": 2,
                             "artifact_versions": 4, "assets": 2, "asset_blobs": 2, "events": 2, "idempotency_records": 2}.items():
            self.assertEqual(snapshot["tables"][table]["rows"], count)
        rollback.verify_blobs(self.root, self.database, manifest)
        rollback.verify_blobs(self.root, self.database, manifest, saved=True)
        self.assertEqual(len(manifest["http_checks"]), 12)

    def test_seed_does_not_overwrite_existing_manifest_or_database(self):
        manifest = self.seed()
        before = self.database.read_bytes()
        with self.assertRaises(ValueError):
            rollback.seed(self.root, self.database, self.manifest_path)
        with self.assertRaises(ValueError):
            rollback.seed(self.root, self.database, self.root / "second-manifest.json")
        self.assertEqual(before, self.database.read_bytes())
        rollback.verify_database(self.root, self.database, manifest, exact=True)

    def test_seed_rejects_wrong_schema_before_writing_business_rows(self):
        with sqlite3.connect(self.database) as connection:
            connection.execute("PRAGMA user_version=63")
        with self.assertRaises(ValueError):
            rollback.seed(self.root, self.database, self.manifest_path)
        with sqlite3.connect(self.database) as connection:
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM projects").fetchone()[0], 0)

    def test_vacuum_layout_change_preserves_complete_logical_snapshot(self):
        with sqlite3.connect(self.database) as connection:
            connection.execute("CREATE TABLE padding(value BLOB)")
            connection.execute("INSERT INTO padding VALUES(zeroblob(262144))")
            connection.execute("DROP TABLE padding")
        manifest = self.seed()
        before_size = self.database.stat().st_size
        backup = self.migrate_fixture(manifest)
        self.assertLess(backup.stat().st_size, before_size)
        selected = rollback.select_backup(self.root, self.database, manifest, 63)
        self.assertEqual(Path(selected["backup_ref"]), backup)
        self.assertEqual(selected["database_sha256"], manifest["database"]["sha256"])

    def test_exact_backup_ignores_newer_unrelated_backup_file(self):
        manifest = self.seed()
        backup = self.migrate_fixture(manifest)
        unrelated = self.database.parent / "backups/newer/content_agent.db"
        unrelated.parent.mkdir()
        unrelated.write_bytes(b"newer but invalid")
        self.assertEqual(Path(rollback.select_backup(self.root, self.database, manifest, 63)["backup_ref"]), backup)

    def test_missing_migration_receipt_is_rejected(self):
        manifest = self.seed()
        self.migrate_fixture(manifest)
        with sqlite3.connect(self.database) as connection:
            connection.execute("DELETE FROM migration_history")
        with self.assertRaises(ValueError):
            rollback.select_backup(self.root, self.database, manifest, 63)

    def test_ambiguous_receipts_are_rejected(self):
        manifest = self.seed()
        backup = self.migrate_fixture(manifest)
        with sqlite3.connect(self.database) as connection:
            connection.execute(
                "INSERT INTO migration_history(migration_id,from_version,to_version,backup_ref,status,started_at,completed_at) VALUES('mig_24_to_63_987',24,63,?,'completed',?,?)",
                (str(backup), rollback.TIME, rollback.TIME),
            )
        with self.assertRaises(ValueError):
            rollback.select_backup(self.root, self.database, manifest, 63)

    def test_incomplete_receipt_is_rejected(self):
        manifest = self.seed()
        self.migrate_fixture(manifest)
        for field, value in (("status", "pending"), ("completed_at", None)):
            with self.subTest(field=field):
                self.change_receipt("status", "completed")
                self.change_receipt(field, value)
                with self.assertRaises(ValueError):
                    rollback.select_backup(self.root, self.database, manifest, 63)

    def test_backup_reference_must_match_migration_directory_and_database(self):
        manifest = self.seed()
        self.migrate_fixture(manifest)
        for reference in (str(self.root.parent / "outside.db"), str(self.database), str(self.database.parent / "backups/other/content_agent.db")):
            with self.subTest(reference=reference):
                self.change_receipt("backup_ref", reference)
                with self.assertRaises(ValueError):
                    rollback.select_backup(self.root, self.database, manifest, 63)

    def test_changed_backup_business_content_is_rejected(self):
        manifest = self.seed()
        backup = self.migrate_fixture(manifest)
        with sqlite3.connect(backup) as connection:
            connection.execute("UPDATE messages SET content='lost original content'")
        with self.assertRaises(ValueError):
            rollback.select_backup(self.root, self.database, manifest, 63)

    def test_missing_history_version_is_rejected(self):
        manifest = self.seed()
        with sqlite3.connect(self.database) as connection:
            connection.execute("DELETE FROM artifact_versions WHERE version=1")
        with self.assertRaises(ValueError):
            rollback.verify_database(self.root, self.database, manifest, exact=False)

    def test_changed_migrated_business_content_is_rejected(self):
        manifest = self.seed()
        self.migrate_fixture(manifest)
        with sqlite3.connect(self.database) as connection:
            connection.execute("UPDATE projects SET title='wrong project'")
        with self.assertRaises(ValueError):
            rollback.verify_database(self.root, self.database, manifest, exact=False)

    def test_migration_duplicate_message_is_rejected_even_when_original_rows_survive(self):
        manifest = self.seed()
        self.migrate_fixture(manifest)
        with sqlite3.connect(self.database) as connection:
            connection.execute("INSERT INTO messages SELECT 'duplicate-message',conversation_id,project_id,role,content,created_at FROM messages LIMIT 1")
        with self.assertRaisesRegex(ValueError, 'row count'):
            rollback.verify_database(self.root, self.database, manifest, exact=False)

    def test_new_migration_columns_do_not_hide_old_business_content(self):
        manifest = self.seed()
        self.migrate_fixture(manifest)
        with sqlite3.connect(self.database) as connection:
            connection.execute("ALTER TABLE projects ADD COLUMN owner_user_id TEXT")
            connection.execute("UPDATE projects SET owner_user_id='new-owner'")
        rollback.verify_database(self.root, self.database, manifest, exact=False)

    def test_wrong_version_is_rejected(self):
        manifest = self.seed()
        self.migrate_fixture(manifest)
        for version in (24, 62, 64):
            with self.subTest(version=version), self.assertRaises(ValueError):
                rollback.select_backup(self.root, self.database, manifest, version)

    def test_restore_uses_fresh_directory_and_backed_up_asset_bytes(self):
        manifest = self.seed()
        backup = self.migrate_fixture(manifest)
        backup_before = backup.read_bytes()
        for blob in manifest["blobs"]:
            rollback.confined(self.database.parent, Path(blob["storage_ref"])).unlink()
        destination = self.root / "restored-data/content_agent.db"
        result = rollback.restore(self.root, self.database, destination, manifest, 63)
        self.assertEqual(result["status"], "passed")
        rollback.verify_database(self.root, destination, manifest, exact=True)
        rollback.verify_blobs(self.root, destination, manifest)
        self.assertEqual(backup_before, backup.read_bytes())
        with sqlite3.connect(self.database) as connection:
            self.assertEqual(connection.execute("PRAGMA user_version").fetchone()[0], 63)

    def test_restore_refuses_existing_destination_without_touching_it(self):
        manifest = self.seed()
        self.migrate_fixture(manifest)
        destination = self.root / "existing-data/content_agent.db"
        destination.parent.mkdir()
        destination.write_bytes(b"do not overwrite")
        with self.assertRaises(ValueError):
            rollback.restore(self.root, self.database, destination, manifest, 63)
        self.assertEqual(destination.read_bytes(), b"do not overwrite")

    def test_corrupt_saved_asset_is_rejected_before_creating_destination(self):
        manifest = self.seed()
        self.migrate_fixture(manifest)
        (self.root / manifest["blobs"][0]["saved_ref"]).write_bytes(b"corrupt")
        destination = self.root / "restored-data/content_agent.db"
        with self.assertRaises(ValueError):
            rollback.restore(self.root, self.database, destination, manifest, 63)
        self.assertFalse(destination.parent.exists())

    def test_asset_reference_cannot_escape_its_data_directory(self):
        manifest = self.seed()
        for field, value, saved in (("storage_ref", "../business-manifest.json", False), ("saved_ref", "data/content_agent.db", True)):
            candidate = copy.deepcopy(manifest)
            candidate["blobs"][0][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                rollback.verify_blobs(self.root, self.database, candidate, saved=saved)

    def test_read_only_verification_preserves_database_bytes(self):
        manifest = self.seed()
        before = self.database.read_bytes()
        rollback.verify_database(self.root, self.database, manifest, exact=True)
        self.assertEqual(before, self.database.read_bytes())

    def test_sqlite_wal_content_is_included_in_logical_snapshot(self):
        manifest = self.seed()
        with sqlite3.connect(self.database) as connection:
            connection.execute("PRAGMA journal_mode=WAL")
            connection.execute("UPDATE messages SET content='committed WAL content'")
            connection.commit()
            with self.assertRaises(ValueError):
                rollback.verify_database(self.root, self.database, manifest, exact=True)

    def test_missing_read_only_database_is_not_created(self):
        absent = self.root / "missing.db"
        with self.assertRaises(sqlite3.OperationalError):
            rollback.open_database(self.root, absent)
        self.assertFalse(absent.exists())

    def test_work_root_reparse_point_is_rejected(self):
        actual_lstat = Path.lstat

        def reparse(path):
            if path == self.root:
                return type("Stat", (), {"st_mode": 0, "st_file_attributes": 1024})()
            return actual_lstat(path)

        with patch.object(Path, "lstat", reparse), self.assertRaises(ValueError):
            rollback.confined(self.root, self.database)

    def test_cli_rejects_live_data_root_before_database_access(self):
        with patch("sys.argv", ["rollback-data", "seed", "--work-root", str(rollback.ROOT / "data"),
                              "--database", str(rollback.ROOT / "data/content_agent.db"), "--manifest", str(self.manifest_path)]):
            with patch.object(rollback, "open_database") as database, self.assertRaises(ValueError):
                rollback.main()
            database.assert_not_called()

    def test_actual_cli_round_trip_in_temporary_directory(self):
        def invoke(operation, database=None, *extra):
            output = subprocess.run(
                [sys.executable, str(Path(rollback.__file__)), operation, '--work-root', str(self.root),
                 '--database', str(database or self.database), '--manifest', str(self.manifest_path), *extra],
                cwd=rollback.ROOT, capture_output=True, check=True, encoding='utf-8',
            )
            return json.loads(output.stdout)

        self.assertEqual(invoke('seed')['status'], 'passed')
        manifest = rollback.load_manifest(self.root, self.manifest_path)
        backup = self.migrate_fixture(manifest)
        self.assertEqual(Path(invoke('select-backup', None, '--target-version', '63')['backup_ref']), backup)
        destination = self.root / 'restored-cli/content_agent.db'
        self.assertEqual(invoke('restore', None, '--target-version', '63', '--destination', str(destination))['status'], 'passed')
        self.assertEqual(invoke('verify-exact', destination)['status'], 'passed')

    def test_foreign_key_corruption_is_rejected(self):
        manifest = self.seed()
        with sqlite3.connect(self.database) as connection:
            connection.execute('DELETE FROM projects')
        with self.assertRaisesRegex(ValueError, 'foreign-key'):
            rollback.verify_database(self.root, self.database, manifest, exact=False)

    def test_http_verifier_accepts_exact_data_and_rejects_wrong_data_and_redirects(self):
        manifest = self.seed()
        state = {"mode": "correct", "requests": []}
        checks = {check["path"]: check for check in manifest["http_checks"]}
        blob_by_path = {f"/api/v1/assets/ast_rollback_{index}/content": self.root / blob["saved_ref"] for index, blob in enumerate(manifest["blobs"], 1)}

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_GET(self):
                state["requests"].append(self.path)
                if state["mode"] == "redirect":
                    self.send_response(302)
                    self.send_header("Location", "/unexpected-redirect")
                    self.end_headers()
                    return
                check = checks[self.path]
                if state["mode"] == "wrong":
                    body = b"{}"
                elif "sha256" in check:
                    body = blob_by_path[self.path].read_bytes()
                else:
                    body = rollback.encode(check["expected"])
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            port = server.server_address[1]
            self.assertEqual(rollback.verify_http(manifest, port)["http_checks"], 12)
            for mode in ("wrong", "redirect"):
                state["mode"] = mode
                with self.subTest(mode=mode), self.assertRaises(ValueError):
                    rollback.verify_http(manifest, port)
            self.assertNotIn("/unexpected-redirect", state["requests"])
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)
        self.assertFalse(thread.is_alive())

    def test_http_verifier_refuses_reserved_ports_and_empty_checks(self):
        manifest = self.seed()
        with patch.object(rollback.urllib.request, "build_opener") as opener:
            for port in (0, 8860, 8880, 65536):
                with self.subTest(port=port), self.assertRaises(ValueError):
                    rollback.verify_http(manifest, port)
            opener.assert_not_called()
        with self.assertRaises(ValueError):
            rollback.verify_http({"http_checks": []}, 12345)


if __name__ == "__main__":
    unittest.main(verbosity=2)
