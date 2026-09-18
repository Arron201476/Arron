package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendOnlyProjectEventStream(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	fixedTime := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixedTime }

	project, err := store.CreateProject(ctx, "Event Stream")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	project, err = store.RenameProject(ctx, project.ProjectID, "Event Stream Renamed", project.Version)
	if err != nil {
		t.Fatalf("RenameProject() error = %v", err)
	}
	if _, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "继续"); err != nil {
		t.Fatalf("CreateUserMessage() error = %v", err)
	}

	first, err := store.ListProjectEvents(ctx, project.ProjectID, 0, 2)
	if err != nil {
		t.Fatalf("ListProjectEvents(first) error = %v", err)
	}
	if len(first.Items) != 2 || !first.HasMore || first.NextSeq != 2 || first.CurrentSeq != 3 {
		t.Fatalf("first batch = %+v", first)
	}
	second, err := store.ListProjectEvents(ctx, project.ProjectID, first.NextSeq, 2)
	if err != nil {
		t.Fatalf("ListProjectEvents(second) error = %v", err)
	}
	if len(second.Items) != 1 || second.HasMore || second.NextSeq != 3 || second.CurrentSeq != 3 {
		t.Fatalf("second batch = %+v", second)
	}
	all := append(first.Items, second.Items...)
	for index, event := range all {
		wantSeq := int64(index + 1)
		if event.ProjectEventSeq != wantSeq {
			t.Fatalf("event[%d] project seq = %d, want %d", index, event.ProjectEventSeq, wantSeq)
		}
		if !event.OccurredAt.Equal(fixedTime) {
			t.Fatalf("event[%d] time = %s, want fixed time", index, event.OccurredAt)
		}
	}

	if _, err := store.ListProjectEvents(ctx, project.ProjectID, 4, 10); err == nil {
		t.Fatal("cursor ahead was accepted")
	} else {
		var domain *DomainError
		if !errors.As(err, &domain) || domain.Code != "EVENT_CURSOR_AHEAD" {
			t.Fatalf("cursor ahead error = %v", err)
		}
	}

	if _, err := store.db.ExecContext(ctx, `
		UPDATE events SET event_type = 'rewritten'
		WHERE project_id = ? AND project_event_seq = 1`,
		project.ProjectID,
	); err == nil {
		t.Fatal("append-only event update was accepted")
	}
	if _, err := store.db.ExecContext(ctx, `
		DELETE FROM events
		WHERE project_id = ? AND project_event_seq = 1`,
		project.ProjectID,
	); err == nil {
		t.Fatal("append-only event delete was accepted")
	}
	var eventCount, outboxCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM events WHERE project_id = ?`,
		project.ProjectID,
	).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM event_outbox WHERE project_id = ?`,
		project.ProjectID,
	).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if eventCount != 3 || outboxCount != eventCount {
		t.Fatalf("events=%d outbox=%d", eventCount, outboxCount)
	}
}

func TestRunEventStreamUsesIndependentMonotonicSequence(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, _, snapshot := startNovelRunForLifecycle(t, store, "Run Event Stream")

	runBatch, err := store.ListRunEvents(ctx, snapshot.Run.RunID, 0, maxEventBatchLimit)
	if err != nil {
		t.Fatalf("ListRunEvents() error = %v", err)
	}
	if len(runBatch.Items) == 0 || runBatch.CurrentSeq != int64(len(runBatch.Items)) {
		t.Fatalf("run batch = %+v", runBatch)
	}
	previousProjectSeq := int64(0)
	for index, event := range runBatch.Items {
		wantRunSeq := int64(index + 1)
		if event.RunEventSeq == nil || *event.RunEventSeq != wantRunSeq {
			t.Fatalf("event[%d] run seq = %v, want %d", index, event.RunEventSeq, wantRunSeq)
		}
		if event.ProjectEventSeq <= previousProjectSeq {
			t.Fatalf("project seq did not increase: previous=%d current=%d", previousProjectSeq, event.ProjectEventSeq)
		}
		previousProjectSeq = event.ProjectEventSeq
	}
	projectCurrent, err := store.CurrentProjectEventSeq(ctx, project.ProjectID)
	if err != nil {
		t.Fatalf("CurrentProjectEventSeq() error = %v", err)
	}
	if projectCurrent < runBatch.CurrentSeq {
		t.Fatalf("project seq %d is behind run seq %d", projectCurrent, runBatch.CurrentSeq)
	}
}
