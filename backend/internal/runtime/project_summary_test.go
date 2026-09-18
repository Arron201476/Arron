package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestProjectSummaryRetainsLatestCompletedRun(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, err := store.CreateProject(ctx, "历史作品")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO runs (
			run_id, project_id, conversation_id, capability_id, capability_version,
			run_kind, write_intent, status, current_input_snapshot_version_id,
			input_snapshot_status, config_snapshot_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"run_completed", project.ProjectID, project.PrimaryConversationID,
		"novel_to_script", "1.4.0", "generation", 1, "completed",
		"risv_completed", "sealed", `{}`, now, now,
	); err != nil {
		t.Fatalf("insert completed run error = %v", err)
	}

	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("ListProjects() count = %d, want 1", len(projects))
	}
	got := projects[0]
	if got.LatestCapabilityID == nil || *got.LatestCapabilityID != "novel_to_script" {
		t.Fatalf("LatestCapabilityID = %v", got.LatestCapabilityID)
	}
	if got.LatestRunStatus == nil || *got.LatestRunStatus != "completed" {
		t.Fatalf("LatestRunStatus = %v", got.LatestRunStatus)
	}
	if got.ActiveWriteRunID != nil || got.CurrentCapabilityID != nil {
		t.Fatalf("completed project unexpectedly has active fields: %+v", got)
	}
}
