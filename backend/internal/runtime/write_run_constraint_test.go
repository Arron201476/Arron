package runtime

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDatabaseEnforcesOneActiveWriteRunPerProject(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	firstProject, err := store.CreateProject(ctx, "Constraint A")
	if err != nil {
		t.Fatalf("CreateProject(first) error = %v", err)
	}
	secondProject, err := store.CreateProject(ctx, "Constraint B")
	if err != nil {
		t.Fatalf("CreateProject(second) error = %v", err)
	}

	insertRawRun(t, store.db, firstProject, "run_constraint_a1", "running")
	if err := insertRawRunError(store.db, firstProject, "run_constraint_a2", "paused"); err == nil {
		t.Fatal("database accepted a second active write run for one project")
	} else if !isProjectWriteRunConstraint(err) {
		t.Fatalf("second active run error = %v", err)
	}
	if err := insertRawRunError(store.db, secondProject, "run_constraint_b1", "waiting_approval"); err != nil {
		t.Fatalf("different project active run was rejected: %v", err)
	}

	if _, err := store.db.ExecContext(ctx, `
		UPDATE runs SET status = 'completed', ended_at = ?, updated_at = ?
		WHERE run_id = ?`,
		formatTime(store.now()), formatTime(store.now()), "run_constraint_a1",
	); err != nil {
		t.Fatalf("complete first raw run: %v", err)
	}
	insertRawRun(t, store.db, firstProject, "run_constraint_a3", "running")
	if _, err := store.db.ExecContext(ctx, `
		UPDATE runs SET status = 'failed', updated_at = ?
		WHERE run_id = ?`,
		formatTime(store.now()), "run_constraint_a3",
	); err != nil {
		t.Fatalf("fail raw run: %v", err)
	}
	if err := insertRawRunError(store.db, firstProject, "run_constraint_a4", "pending"); err == nil {
		t.Fatal("database released a failed write run before retry or cancellation")
	} else if !isProjectWriteRunConstraint(err) {
		t.Fatalf("failed run constraint error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE runs SET status = 'cancelled', ended_at = ?, updated_at = ?
		WHERE run_id = ?`,
		formatTime(store.now()), formatTime(store.now()), "run_constraint_a3",
	); err != nil {
		t.Fatalf("cancel raw run: %v", err)
	}
	insertRawRun(t, store.db, firstProject, "run_constraint_a4", "pending")
}

func TestConcurrentStartRunAllowsExactlyOneWriterPerProject(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "Concurrent Same Project")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	message, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "开始")
	if err != nil {
		t.Fatalf("CreateUserMessage() error = %v", err)
	}
	source := createTextAsset(t, store, project.ProjectID, "constraint-source.txt", "第一章。")
	command := bindStartRunProposal(t, store, novelStartCommand(project, message.MessageID, source))

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := store.StartRun(ctx, command)
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		var domain *DomainError
		if errors.As(err, &domain) &&
			(domain.Code == "PROJECT_WRITE_RUN_CONFLICT" || domain.Code == "CONFIRMATION_STALE") {
			conflicts++
			continue
		}
		t.Fatalf("unexpected concurrent StartRun error = %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent starts successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestDifferentProjectsMayHoldActiveWriteRuns(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	first, _, firstRun := startNovelRunForLifecycle(t, store, "Parallel Project A")
	second, _, secondRun := startNovelRunForLifecycle(t, store, "Parallel Project B")
	if first.ProjectID == second.ProjectID || firstRun.Run.RunID == secondRun.Run.RunID {
		t.Fatalf("projects or runs were not independent: first=%+v second=%+v", firstRun.Run, secondRun.Run)
	}
	if firstRun.Run.Status != "waiting_approval" || secondRun.Run.Status != "waiting_approval" {
		t.Fatalf("different project runs are not both active: first=%s second=%s", firstRun.Run.Status, secondRun.Run.Status)
	}
}

func insertRawRun(t *testing.T, db *sql.DB, project Project, runID, status string) {
	t.Helper()
	if err := insertRawRunError(db, project, runID, status); err != nil {
		t.Fatalf("insert raw run %s: %v", runID, err)
	}
}

func insertRawRunError(db *sql.DB, project Project, runID, status string) error {
	now := formatTime(time.Now().UTC())
	_, err := db.Exec(`
		INSERT INTO runs(
			run_id, project_id, conversation_id, capability_id, capability_version,
			run_kind, write_intent, status, current_input_snapshot_version_id,
			input_snapshot_status, config_snapshot_json, run_event_seq,
			started_at, created_at, updated_at
		) VALUES(?, ?, ?, 'novel_to_script', '1.4.0', 'generation', 1, ?, ?,
			'sealed', '{}', 0, ?, ?, ?)`,
		runID,
		project.ProjectID,
		project.PrimaryConversationID,
		status,
		"risv_"+runID,
		now,
		now,
		now,
	)
	return err
}
