package runtime

import (
	"context"
	"path/filepath"
	"testing"
)

func TestListProjectActivitiesReturnsStableLifecycleProjection(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, _, snapshot := startNovelRunForLifecycle(t, store, "Project activity")
	items, err := store.ListProjectActivities(context.Background(), project.ProjectID, 0)
	if err != nil {
		t.Fatalf("ListProjectActivities() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("activities = %d, want run.started and approval.requested", len(items))
	}
	if items[0].EventType != "run.started" || items[1].EventType != "approval.requested" {
		t.Fatalf("event types = %q, %q", items[0].EventType, items[1].EventType)
	}
	if items[0].RunID == nil || *items[0].RunID != snapshot.Run.RunID {
		t.Fatalf("run id = %v, want %s", items[0].RunID, snapshot.Run.RunID)
	}
	if items[0].CapabilityID == nil || *items[0].CapabilityID != "novel_to_script" {
		t.Fatalf("capability id = %v", items[0].CapabilityID)
	}
	if items[0].StepID == nil || *items[0].StepID != "ingest_source" {
		t.Fatalf("step id = %v", items[0].StepID)
	}
}
