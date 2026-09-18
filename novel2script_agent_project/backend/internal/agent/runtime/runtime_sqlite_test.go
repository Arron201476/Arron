package runtime

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novel2script-agent/backend/internal/agent"
)

func TestRuntimeStoreRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version=999`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if db, err := openRuntimeDatabase(path); err == nil {
		db.Close()
		t.Fatal("expected newer runtime schema to be rejected")
	}
}

func TestSQLiteRuntimeStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	runtime := NewRuntimeWithState(nil, path)
	run := agent.Run{RunID: "run_sqlite", ProjectID: "project_sqlite", SourceMode: agent.SourceModeNovel, Status: agent.RunCompleted, StartedAt: time.Now().UTC(), Metadata: map[string]any{"generation_config": map[string]any{"target_episode_count": 2}, "generation_config_version": 1}}
	artifact := runtime.artifact(run, "story_bible", agent.ArtifactConfirmed, nil, map[string]any{"title": "test", "generation_config_ref": configReference(run.RunID, 1)})
	event := runtime.event(run.RunID, "step_story_bible", agent.EventArtifactCreated, "created", []string{artifact.ArtifactID}, nil, nil)
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{artifact}
	runtime.events[run.RunID] = []agent.RunEvent{event}
	runtime.saveLocked()
	if err := runtime.PersistenceError(); err != nil {
		t.Fatal(err)
	}

	restored := NewRuntimeWithState(nil, path)
	loaded, ok := restored.GetRun(run.RunID)
	if !ok || loaded.ProjectID != run.ProjectID {
		t.Fatalf("run was not restored: %#v", loaded)
	}
	if values := restored.Artifacts(run.RunID); len(values) != 1 || values[0].Payload["title"] != "test" {
		t.Fatalf("artifacts were not restored: %#v", values)
	}
	if values := restored.Events(run.RunID); len(values) != 1 || values[0].EventID != event.EventID {
		t.Fatalf("events were not restored: %#v", values)
	}
}

func TestSQLiteRuntimeMigratesLegacyJSONAndRemovesConfigCopies(t *testing.T) {
	dir := t.TempDir()
	run := agent.Run{RunID: "run_legacy", ProjectID: "project_legacy", SourceMode: agent.SourceModeNonNovel, Status: agent.RunCompleted, Metadata: map[string]any{"generation_config": map[string]any{"target_episode_count": float64(3)}}}
	artifact := agent.Artifact{ArtifactID: "artifact_legacy", RunID: run.RunID, ProjectID: run.ProjectID, ArtifactType: "source_input", SourceMode: run.SourceMode, Status: agent.ArtifactConfirmed, Version: 1, Payload: map[string]any{"generation_config": map[string]any{"target_episode_count": float64(3)}, "text": "legacy"}}
	legacy := runtimeState{Runs: map[string]agent.Run{run.RunID: run}, Events: map[string][]agent.RunEvent{}, Artifacts: map[string][]agent.Artifact{run.RunID: {artifact}}, Approvals: map[string]agent.ApprovalRequest{}, Counter: 8}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mock_runtime_state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	restored := NewRuntimeWithState(nil, filepath.Join(dir, "runtime.db"))
	values := restored.Artifacts(run.RunID)
	if len(values) != 1 {
		t.Fatalf("legacy artifact not migrated: %#v", values)
	}
	if _, copied := values[0].Payload["generation_config"]; copied {
		t.Fatalf("generation config copy survived migration: %#v", values[0].Payload)
	}
	if _, referenced := values[0].Payload["generation_config_ref"]; !referenced {
		t.Fatalf("generation config reference missing: %#v", values[0].Payload)
	}
	if _, err := os.Stat(filepath.Join(dir, "runtime.db")); err != nil {
		t.Fatalf("runtime database was not created: %v", err)
	}
}

func TestArtifactConfigEditUpdatesRunAuthorityAndReference(t *testing.T) {
	runtime := NewRuntime(nil)
	run := agent.Run{RunID: "run_config", ProjectID: "project_config", SourceMode: agent.SourceModeNovel, Status: agent.RunCompleted, Metadata: map[string]any{"generation_config": map[string]any{"target_episode_count": 2}, "generation_config_version": 1}}
	artifact := runtime.artifact(run, "episode_split", agent.ArtifactConfirmed, nil, map[string]any{"generation_config_ref": configReference(run.RunID, 1), "episodes": []any{}})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{artifact}
	updated, _, _, ok := runtime.UpdateArtifact(artifact.ArtifactID, map[string]any{"generation_config": map[string]any{"target_episode_count": 4}, "episodes": []any{}})
	if !ok {
		t.Fatal("config edit was not applied")
	}
	if _, copied := updated.Payload["generation_config"]; copied {
		t.Fatalf("updated artifact persisted a config copy: %#v", updated.Payload)
	}
	ref, _ := updated.Payload["generation_config_ref"].(map[string]any)
	if ref["version"] != 2 {
		t.Fatalf("expected config reference v2, got %#v", ref)
	}
	loaded, _ := runtime.GetRun(run.RunID)
	if generationConfigVersion(loaded) != 2 || authoritativeGenerationConfigLocked(loaded)["target_episode_count"] != 4 {
		t.Fatalf("run authority was not updated: %#v", loaded.Metadata)
	}
}
