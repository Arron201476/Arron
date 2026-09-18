package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
)

func TestV32MigrationAddsPrivateBackgroundCheckpointsWithoutChangingTasks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "content_agent.db")
	store, err := Open(path, loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	task := createBackgroundTaskForTest(t, store)
	if _, err := store.db.Exec(`DROP TABLE agent_task_run_states; DROP TABLE agent_task_tool_calls; PRAGMA user_version=32;`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fresh, err := store.GetAgentTask(ctx, task.AgentTaskID)
	if err != nil || fresh.Status != "queued" || fresh.CapabilityVersion != task.CapabilityVersion {
		t.Fatalf("task changed during migration: %+v, %v", fresh, err)
	}
	for _, name := range []string{"agent_task_run_states", "agent_task_tool_calls"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing %s: %d, %v", name, count, err)
		}
	}
	var backup string
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version = 32 AND to_version = ? AND status = 'completed'`, schemaVersion).Scan(&backup); err != nil || backup == "" {
		t.Fatalf("migration has no rollback backup: %s, %v", backup, err)
	}
}

func TestOpenBackfillsLegacyRunConfigSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "content_agent.db")
	registry := loadTestRegistry(t)
	store, err := Open(path, registry)
	if err != nil {
		t.Fatalf("Open(seed) error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	config := json.RawMessage(`{"config_ref":"creation","payload":{"target_episode_count":12,"episode_duration_minutes":2,"preserve_existing_episode_marks":true,"expansion_policy":"confirm_if_needed","user_requirements":[]}}`)
	if _, err := db.Exec(`
		INSERT INTO projects(
			project_id, workspace_id, title, version, status,
			primary_conversation_id, project_event_seq, created_at, updated_at
		) VALUES('project_legacy', 'shared', 'legacy', 1, 'running', 'conversation_legacy', 0, '2026-08-01T00:00:00Z', '2026-08-01T00:00:00Z');
		INSERT INTO conversations(
			conversation_id, project_id, is_primary, created_at, updated_at
		) VALUES('conversation_legacy', 'project_legacy', 1, '2026-08-01T00:00:00Z', '2026-08-01T00:00:00Z');
	`); err != nil {
		db.Close()
		t.Fatalf("seed legacy project: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO runs(
			run_id, project_id, conversation_id, capability_id, capability_version,
			run_kind, write_intent, status, current_input_snapshot_version_id,
			input_snapshot_status, config_snapshot_json, run_event_seq, created_at, updated_at
		) VALUES(
			'run_legacy', 'project_legacy', 'conversation_legacy', 'novel_to_script', '1.0.0',
			'generate', 1, 'running', 'input_legacy', 'sealed', ?, 0, ?, ?
		);
		DELETE FROM run_config_snapshots WHERE run_id = 'run_legacy';
		PRAGMA user_version=17;`,
		string(config), "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z",
	); err != nil {
		db.Close()
		t.Fatalf("seed legacy run: %v", err)
	}
	var legacyPayload string
	if err := db.QueryRow(`SELECT config_snapshot_json FROM runs WHERE run_id = 'run_legacy'`).Scan(&legacyPayload); err != nil || legacyPayload != string(config) {
		db.Close()
		t.Fatalf("legacy config payload = %q, error = %v", legacyPayload, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}

	store, err = Open(path, registry)
	if err != nil {
		t.Fatalf("Open(migrate) error = %v", err)
	}
	defer store.Close()
	var configRef, payload, hash, status string
	var version int
	if err := store.db.QueryRow(`
		SELECT config_ref, version, status, payload_json, snapshot_hash
		FROM run_config_snapshots WHERE run_id = 'run_legacy'`).Scan(
		&configRef, &version, &status, &payload, &hash,
	); err != nil {
		t.Fatalf("query backfilled snapshot: %v", err)
	}
	if configRef != "creation" || version != 1 || status != "sealed" || payload != string(config) {
		t.Fatalf("unexpected snapshot ref=%q version=%d status=%q payload=%s", configRef, version, status, payload)
	}
	if hash != sha256Hex(config) {
		t.Fatalf("snapshot hash = %q, want %q", hash, sha256Hex(config))
	}
	var databaseVersion int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&databaseVersion); err != nil {
		t.Fatalf("read migrated schema version: %v", err)
	}
	if databaseVersion != schemaVersion {
		t.Fatalf("schema version = %d, want %d", databaseVersion, schemaVersion)
	}
}

func TestOpenBackfillsLegacyAgentShellArtifactSessionConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "content_agent.db")
	store, err := Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open(seed) error = %v", err)
	}
	project, err := store.CreateProject(context.Background(), "legacy agent shell")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO runs(
			run_id, project_id, conversation_id, capability_id, capability_version,
			run_kind, write_intent, status, current_input_snapshot_version_id,
			input_snapshot_status, config_snapshot_json,
			run_event_seq, created_at, updated_at
		) VALUES('run_legacy_shell', ?, ?, 'agent_shell', '1.0.0', 'artifact_session', 0,
			'completed', 'input_legacy_shell', 'sealed', '{}', 0,
			'2026-08-01T00:00:00Z', '2026-08-01T00:00:00Z');
		PRAGMA user_version=17;`, project.ProjectID, project.PrimaryConversationID); err != nil {
		db.Close()
		t.Fatalf("seed legacy agent shell run: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}

	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open(migrate) error = %v", err)
	}
	defer store.Close()
	var configRef, payload string
	if err := store.db.QueryRow(`SELECT config_ref, payload_json FROM run_config_snapshots WHERE run_id = 'run_legacy_shell'`).Scan(&configRef, &payload); err != nil {
		t.Fatalf("query backfilled agent shell config: %v", err)
	}
	if configRef != "agent_shell" || payload != `{"config_ref":"agent_shell","payload":{}}` {
		t.Fatalf("backfilled agent shell config = %q %s", configRef, payload)
	}
}

func TestOpenBacksUpExistingDatabaseBeforeMigration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "content_agent.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE legacy_probe(value TEXT NOT NULL); INSERT INTO legacy_probe(value) VALUES('keep'); PRAGMA user_version=15;`); err != nil {
		t.Fatalf("seed fixture db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}
	store, err := Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open(migrate) error = %v", err)
	}
	defer store.Close()
	var value, migrationID, backupRef, status string
	if err := store.db.QueryRowContext(context.Background(), `SELECT value FROM legacy_probe`).Scan(&value); err != nil || value != "keep" {
		t.Fatalf("legacy value = %q, error = %v", value, err)
	}
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT migration_id, backup_ref, status FROM migration_history
		WHERE from_version = 15 AND to_version = ?`, schemaVersion).Scan(&migrationID, &backupRef, &status); err != nil {
		t.Fatalf("migration history: %v", err)
	}
	if status != "completed" || migrationID == "" {
		t.Fatalf("migration = %q status=%q", migrationID, status)
	}
	if info, err := os.Stat(backupRef); err != nil || info.Size() == 0 {
		t.Fatalf("backup %q info=%v error=%v", backupRef, info, err)
	}
	if info, err := os.Stat(filepath.Join(root, "migration-reports", migrationID+".json")); err != nil || info.Size() == 0 {
		t.Fatalf("migration report info=%v error=%v", info, err)
	}
}

func TestMigrationCompletionFailurePreservesVersionAndOriginalBackup(t *testing.T) {
	for _, failure := range []string{"history-insert", "report-directory"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "migration.db")
			store, err := Open(path, loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`CREATE TABLE preserved_probe(value TEXT); INSERT INTO preserved_probe VALUES('keep'); PRAGMA user_version=62;`); err != nil {
				store.Close()
				t.Fatal(err)
			}
			if failure == "history-insert" {
				_, err = store.db.Exec(`CREATE TRIGGER reject_migration_history BEFORE INSERT ON migration_history BEGIN SELECT RAISE(ABORT, 'injected completion failure'); END`)
			} else {
				err = os.WriteFile(filepath.Join(root, "migration-reports"), []byte("not a directory"), 0o600)
			}
			if closeErr := store.Close(); err != nil || closeErr != nil {
				t.Fatalf("prepare failure: %v close=%v", err, closeErr)
			}
			unexpected, err := Open(path, loadTestRegistry(t))
			if err == nil {
				unexpected.Close()
				t.Fatal("migration should fail at the injected completion boundary")
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			var version, completed int
			if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 62 {
				t.Fatalf("failed migration advanced schema marker: %d %v", version, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM migration_history WHERE from_version=62 AND status='completed'`).Scan(&completed); err != nil || completed != 0 {
				t.Fatalf("failed migration has a success receipt: %d %v", completed, err)
			}
			backups, err := filepath.Glob(filepath.Join(root, "backups", "*", "migration.db"))
			if err != nil || len(backups) != 1 {
				t.Fatalf("original backup missing: %v %v", backups, err)
			}
			original, err := os.ReadFile(backups[0])
			if err != nil || len(original) == 0 {
				t.Fatalf("original backup unreadable: %v", err)
			}
			if failure == "history-insert" {
				_, err = db.Exec(`DROP TRIGGER reject_migration_history`)
			} else {
				err = os.Remove(filepath.Join(root, "migration-reports"))
			}
			if closeErr := db.Close(); err != nil || closeErr != nil {
				t.Fatalf("remove fixture failure: %v close=%v", err, closeErr)
			}
			store, err = Open(path, loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
				t.Fatalf("recovery schema marker: %d %v", version, err)
			}
			var migrationID, backupRef, value string
			if err := store.db.QueryRow(`SELECT migration_id, backup_ref FROM migration_history WHERE from_version=62 AND to_version=? AND status='completed'`, schemaVersion).Scan(&migrationID, &backupRef); err != nil {
				t.Fatal(err)
			}
			if backupRef == backups[0] {
				t.Fatal("retry reused the original backup path")
			}
			if _, err := os.Stat(filepath.Join(root, "migration-reports", migrationID+".json")); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRow(`SELECT value FROM preserved_probe`).Scan(&value); err != nil || value != "keep" {
				t.Fatalf("recovery lost existing data: %q %v", value, err)
			}
			retained, err := os.ReadFile(backups[0])
			if err != nil || !bytes.Equal(original, retained) {
				t.Fatalf("retry replaced the original rollback backup: %v", err)
			}
		})
	}
}

func TestConfigSnapshotBackfillDoesNotAdvanceWholeSchema(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "partial.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`PRAGMA user_version=62`); err != nil {
		t.Fatal(err)
	}
	if err := migrateRunConfigSnapshots(store.db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 62 {
		t.Fatalf("component migration advanced the global schema marker: %d %v", version, err)
	}
}

func TestMigrateAgentTurnObservabilityAddsV31ColumnsToLegacyTable(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE agent_turns(agent_turn_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateAgentTurnObservability(db); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{
		"provider_id", "model_id", "release_id", "trace_refs_json",
		"response_ids_json", "request_ids_json", "usage_json", "latency_json",
		"failure_stage", "cancel_reason", "skill_invocation_id", "agent_task_id",
		"run_id", "agent_tool_call_ids_json",
	} {
		present, err := tableHasColumn(db, "agent_turns", column)
		if err != nil || !present {
			t.Fatalf("column %s present = %v, error = %v", column, present, err)
		}
	}
	if err := migrateAgentTurnObservability(db); err != nil {
		t.Fatalf("idempotent migration failed: %v", err)
	}
}

func TestOpenMigratesV31AgentTurnToDurableApprovalSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "content_agent.db")
	registry := loadTestRegistry(t)
	store, err := Open(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, "v31 approval migration")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "preserve this turn"}, CommandMeta{
			Scope: project.ProjectID, CommandType: "create_message",
			IdempotencyKey: "31111111-1111-4111-8111-111111111111", RequestHash: "v31-turn",
		})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE agent_turns SET provider_id = 'provider-v31', model_id = 'model-v31',
			trace_refs_json = '["trace-v31"]', agent_tool_call_ids_json = '["tool-v31"]'
		WHERE agent_turn_id = ?`, turn.AgentTurnID); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		PRAGMA foreign_keys=OFF;
		DROP TABLE agent_turn_run_states;
		CREATE TABLE agent_turns_v31 (
			agent_turn_id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT 'user_local_default',
			project_id TEXT NOT NULL REFERENCES projects(project_id),
			conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
			idempotency_key TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			request_json TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN (
				'accepted','running','cancel_requested','committing','committed','failed','cancelled'
			)),
			error_code TEXT,
			error_message TEXT,
			exchange_json TEXT,
			user_message_id TEXT REFERENCES messages(message_id),
			agent_message_id TEXT REFERENCES messages(message_id),
			terminal_event_id TEXT UNIQUE,
			created_at TEXT NOT NULL,
			started_at TEXT,
			completed_at TEXT,
			provider_id TEXT,
			model_id TEXT,
			release_id TEXT,
			trace_refs_json TEXT NOT NULL DEFAULT '[]',
			response_ids_json TEXT NOT NULL DEFAULT '[]',
			request_ids_json TEXT NOT NULL DEFAULT '[]',
			usage_json TEXT NOT NULL DEFAULT '{}',
			latency_json TEXT NOT NULL DEFAULT '{}',
			failure_stage TEXT,
			cancel_reason TEXT,
			skill_invocation_id TEXT,
			agent_task_id TEXT,
			run_id TEXT,
			agent_tool_call_ids_json TEXT NOT NULL DEFAULT '[]',
			updated_at TEXT NOT NULL,
			UNIQUE(project_id, idempotency_key)
		);
		INSERT INTO agent_turns_v31 SELECT
			agent_turn_id, workspace_id, user_id, project_id, conversation_id,
			idempotency_key, request_hash, request_json, status, error_code, error_message,
			exchange_json, user_message_id, agent_message_id, terminal_event_id, created_at,
			started_at, completed_at, provider_id, model_id, release_id, trace_refs_json,
			response_ids_json, request_ids_json, usage_json, latency_json, failure_stage,
			cancel_reason, skill_invocation_id, agent_task_id, run_id, agent_tool_call_ids_json,
			updated_at FROM agent_turns;
		DROP TABLE agent_turn_skills;
		DROP TABLE agent_turns;
		ALTER TABLE agent_turns_v31 RENAME TO agent_turns;
		PRAGMA user_version=31;
		PRAGMA foreign_keys=ON;
	`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path, registry)
	if err != nil {
		t.Fatalf("Open(v31 migrate) error = %v", err)
	}
	defer store.Close()
	got, err := store.GetAgentTurn(ctx, turn.AgentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "accepted" || got.Request.Content != "preserve this turn" ||
		got.Observation.ProviderID != "provider-v31" || got.Observation.ModelID != "model-v31" ||
		len(got.Observation.TraceRefs) != 1 || got.Observation.TraceRefs[0] != "trace-v31" ||
		len(got.Correlation.AgentToolCallIDs) != 1 || got.Correlation.AgentToolCallIDs[0] != "tool-v31" {
		t.Fatalf("migrated turn = %+v", got)
	}
	if _, err := store.db.Exec(`UPDATE agent_turns SET status = 'waiting_approval' WHERE agent_turn_id = ?`, turn.AgentTurnID); err != nil {
		t.Fatalf("v32 status constraint rejected waiting_approval: %v", err)
	}
	var checkpointTable string
	if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'agent_turn_run_states'`).Scan(&checkpointTable); err != nil {
		t.Fatalf("approval checkpoint table missing: %v", err)
	}
	var databaseVersion int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&databaseVersion); err != nil || databaseVersion != schemaVersion {
		t.Fatalf("schema version = %d, error = %v", databaseVersion, err)
	}
}
