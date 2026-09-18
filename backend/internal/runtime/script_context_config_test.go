package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"content-agent/backend/internal/capability"
)

func TestScriptGenerationUsesStepConfigSnapshot(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, err = database.Exec(`
		CREATE TABLE run_config_snapshots (
			config_snapshot_id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			config_ref TEXT NOT NULL,
			version INTEGER NOT NULL,
			status TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			snapshot_hash TEXT NOT NULL,
			created_at TEXT NOT NULL,
			sealed_at TEXT NOT NULL
		);
		INSERT INTO run_config_snapshots VALUES (
			'cfg_creation', 'run_video', 'creation', 1, 'sealed',
			'{"config_ref":"creation","payload":{"target_episode_count":1,"episode_duration_minutes":1}}',
			'hash', '2026-08-06T00:00:00Z', '2026-08-06T00:00:00Z'
		);`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	configRef := "creation"
	payload, err := stepConfigPayloadTx(
		context.Background(),
		tx,
		Run{
			RunID:          "run_video",
			ConfigSnapshot: json.RawMessage(`{"config_ref":"extraction","payload":{}}`),
		},
		capability.CompiledStep{ConfigRef: &configRef},
	)
	if err != nil {
		t.Fatalf("stepConfigPayloadTx() error = %v", err)
	}
	targetCount, err := targetEpisodeCount(payload)
	if err != nil || targetCount != 1 {
		t.Fatalf("targetEpisodeCount() = %d, error = %v", targetCount, err)
	}
	config, _, err := normalizedScriptGenerationConfig(payload)
	if err != nil {
		t.Fatalf("normalizedScriptGenerationConfig() error = %v", err)
	}
	if target, ok := config["target_episode_count"].(int); !ok || target != 1 {
		t.Fatalf("target_episode_count = %v", config["target_episode_count"])
	}
}
