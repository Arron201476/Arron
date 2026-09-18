package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
)

func runtimeDispatchStep(t *testing.T, registry *capability.Registry, id string) *capability.CompiledStep {
	t.Helper()
	entry, ok := registry.Get("novel_to_script")
	if !ok || entry.Definition == nil {
		t.Fatal("missing novel definition")
	}
	for index := range entry.Definition.Steps {
		if entry.Definition.Steps[index].ID == id {
			return &entry.Definition.Steps[index]
		}
	}
	t.Fatalf("missing step %s", id)
	return nil
}

func runtimeDispatchRegistry(t *testing.T) *capability.Registry {
	t.Helper()
	registry := loadTestRegistry(t)
	source := runtimeDispatchStep(t, registry, "build_source_manifest")
	volume := runtimeDispatchStep(t, registry, "review_volume_fit")
	source.Next = []capability.TransitionRef{{When: "completed", To: volume.ID}}
	volume.InputRefs = slices.Clone(source.InputRefs)
	volume.Next = []capability.TransitionRef{{When: "completed", To: "build_story_bible"}}
	// A single model successor isolates dispatch from the novel analysis batch planner.
	worker := runtimeDispatchStep(t, registry, "build_story_bible")
	worker.Kind, worker.Batch = "model", nil
	worker.InputRefs = slices.Clone(source.InputRefs)
	return registry
}

func prepareRuntimeDispatch(t *testing.T, store *Store, content string) RunSnapshot {
	t.Helper()
	_, _, initial := startNovelRunWithContent(t, store, "Runtime dispatch", content)
	approval := initial.CurrentApproval
	if approval == nil {
		t.Fatal("missing source approval")
	}
	paused, err := store.ResolveApproval(context.Background(), ResolveApprovalCommand{
		ApprovalRequestID: approval.ApprovalRequestID, Action: "approve",
		ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash,
	})
	if err != nil || paused.Run.Status != "paused" || currentStepID(paused) != "build_source_manifest" {
		t.Fatalf("source boundary: %+v %v", paused, err)
	}
	return paused
}

func runtimeDispatchResumeCommand(run Run) ResumeRunCommand {
	return ResumeRunCommand{CommandMeta: CommandMeta{
		Scope: run.ProjectID, CommandType: "resume_run", IdempotencyKey: "resume-runtime-chain", RequestHash: "resume-runtime-chain-v1",
	}, RunID: run.RunID}
}

func TestResumeDispatchesConsecutiveRuntimeStepsBeforeWorker(t *testing.T) {
	ctx := context.Background()
	registry := runtimeDispatchRegistry(t)
	path := filepath.Join(t.TempDir(), "dispatch.db")
	store, err := Open(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	paused := prepareRuntimeDispatch(t, store, strings.Repeat("Source material. ", 4000))
	command := runtimeDispatchResumeCommand(paused.Run)
	running, err := store.ResumeRun(ctx, command)
	if err != nil || running.Run.Status != "running" || currentStepID(running) != "build_story_bible" {
		t.Fatalf("Runtime chain to Worker: %+v %v", running, err)
	}
	for _, step := range running.Steps {
		tasks, err := store.ListTaskItems(ctx, step.StepRunID)
		if err != nil {
			t.Fatal(err)
		}
		if step.StepID == "build_story_bible" {
			if len(tasks) != 1 || tasks[0].Status != "pending" {
				t.Fatalf("Worker task: %+v", tasks)
			}
		} else if len(tasks) != 0 || step.Status != "completed" {
			t.Fatalf("Runtime step enqueued or incomplete: %+v tasks=%+v", step, tasks)
		}
	}
	claim := claimStructuredTask(t, store)
	if claim.ExecutorID != "worker.structured_content" {
		t.Fatalf("claimed non-Worker executor: %+v", claim)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.ResumeRun(ctx, command)
	if err != nil || replayed.EventCursor != running.EventCursor || *replayed.Run.CurrentStepRunID != *running.Run.CurrentStepRunID {
		t.Fatalf("reopened original resume receipt: %+v %v", replayed, err)
	}
	var attempts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_attempts WHERE run_id=?`, running.Run.RunID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("resume receipt created another attempt: %d %v", attempts, err)
	}
}

func TestRuntimeDispatchStopsAtDeclaredTerminalAndEmitsCompletionOnce(t *testing.T) {
	for _, terminal := range []string{"build_source_manifest", "review_volume_fit"} {
		t.Run(terminal, func(t *testing.T) {
			registry := runtimeDispatchRegistry(t)
			runtimeDispatchStep(t, registry, terminal).Next = nil
			entry, _ := registry.Get("novel_to_script")
			entry.Definition.Completion = capability.Completion{TerminalStepIDs: []string{terminal}, RequiredArtifacts: []capability.CompletionArtifact{
				{ArtifactType: "source_manifest", RequiredStatus: "confirmed", Coverage: "one"},
			}}
			store, err := Open(filepath.Join(t.TempDir(), "terminal.db"), registry)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			paused := prepareRuntimeDispatch(t, store, strings.Repeat("Source material. ", 4000))
			command := runtimeDispatchResumeCommand(paused.Run)
			completed, err := store.ResumeRun(context.Background(), command)
			if err != nil || completed.Run.Status != "completed" || completed.Run.EndedAt == nil || currentStepID(completed) != terminal {
				t.Fatalf("Runtime terminal: %+v %v", completed, err)
			}
			project, err := store.GetProject(context.Background(), paused.Run.ProjectID)
			if err != nil || project.ActiveWriteRunID != nil || project.Status != "ready" {
				t.Fatalf("terminal write lock: %+v %v", project, err)
			}
			if _, err := store.ResumeRun(context.Background(), command); err != nil {
				t.Fatal(err)
			}
			var events, tasks int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE run_id=? AND event_type='run.completed'`, paused.Run.RunID).Scan(&events); err != nil || events != 1 {
				t.Fatalf("completion events: %d %v", events, err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_items WHERE run_id=?`, paused.Run.RunID).Scan(&tasks); err != nil || tasks != 0 {
				t.Fatalf("Runtime terminal Worker tasks: %d %v", tasks, err)
			}
		})
	}
}

func TestRuntimeDispatchStopsForApprovalAndYieldsCycles(t *testing.T) {
	for _, scenario := range []string{"user-confirmation", "expansion-confirmation", "cycle"} {
		t.Run(scenario, func(t *testing.T) {
			registry := runtimeDispatchRegistry(t)
			volume := runtimeDispatchStep(t, registry, "review_volume_fit")
			content := strings.Repeat("Source material. ", 4000)
			if scenario == "user-confirmation" {
				volume.Next[0].When = "user_continue"
			} else if scenario == "expansion-confirmation" {
				content = "Short source."
			} else {
				volume.Next[0].To = volume.ID
			}
			store, err := Open(filepath.Join(t.TempDir(), "boundary.db"), registry)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			paused := prepareRuntimeDispatch(t, store, content)
			command := runtimeDispatchResumeCommand(paused.Run)
			result, err := store.ResumeRun(context.Background(), command)
			if err != nil || currentStepID(result) != "review_volume_fit" {
				t.Fatalf("boundary %s: %+v %v", scenario, result, err)
			}
			if scenario == "cycle" {
				if result.Run.Status != "paused" || result.CurrentApproval != nil {
					t.Fatalf("cycle did not yield: %+v", result)
				}
				var completed, pending int
				if err := store.db.QueryRow(`SELECT COUNT(CASE WHEN status='completed' THEN 1 END), COUNT(CASE WHEN status='pending' THEN 1 END)
					FROM step_runs WHERE run_id=? AND step_id='review_volume_fit'`, result.Run.RunID).Scan(&completed, &pending); err != nil || completed != 1 || pending != 1 {
					t.Fatalf("cycle replayed or lost next iteration: completed=%d pending=%d err=%v", completed, pending, err)
				}
			} else if result.Run.Status != "waiting_approval" || result.CurrentApproval == nil {
				t.Fatalf("approval bypassed: %+v", result)
			}
			var tasks int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_items WHERE run_id=?`, paused.Run.RunID).Scan(&tasks); err != nil || tasks != 0 {
				t.Fatalf("boundary enqueued a Worker: %d %v", tasks, err)
			}
			replayed, err := store.ResumeRun(context.Background(), command)
			if err != nil || replayed.EventCursor != result.EventCursor {
				t.Fatalf("boundary receipt replay: %+v %v", replayed, err)
			}
		})
	}
}

func TestRuntimeDispatchFailureRollsBackEarlierSteps(t *testing.T) {
	registry := runtimeDispatchRegistry(t)
	store, err := Open(filepath.Join(t.TempDir(), "rollback.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	paused := prepareRuntimeDispatch(t, store, strings.Repeat("Source material. ", 4000))
	if _, err := store.db.Exec(`CREATE TRIGGER reject_dispatch_step BEFORE UPDATE OF status ON step_runs
		WHEN NEW.step_id='review_volume_fit' AND NEW.status='completed'
		BEGIN SELECT RAISE(ABORT, 'dispatch fault'); END`); err != nil {
		t.Fatal(err)
	}
	command := runtimeDispatchResumeCommand(paused.Run)
	_, err = store.ResumeRun(context.Background(), command)
	if err == nil || !strings.Contains(err.Error(), "dispatch fault") {
		t.Fatalf("fault injection not reached: %v", err)
	}
	fresh, err := store.GetRunSnapshot(context.Background(), paused.Run.RunID)
	if err != nil || fresh.EventCursor != paused.EventCursor || len(fresh.Steps) != len(paused.Steps) || len(fresh.Artifacts) != len(paused.Artifacts) || fresh.Run.Status != "paused" {
		t.Fatalf("Runtime chain partially committed: %+v %v", fresh, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER reject_dispatch_step`); err != nil {
		t.Fatal(err)
	}
	if retried, err := store.ResumeRun(context.Background(), command); err != nil || retried.Run.Status != "running" {
		t.Fatalf("rolled-back command could not retry: %+v %v", retried, err)
	}
}

func TestRuntimeAggregateDispatchUsesActualSuccessorState(t *testing.T) {
	for _, target := range []string{"terminal", "build_story_bible", "review_script_set"} {
		t.Run(target, func(t *testing.T) {
			ctx := context.Background()
			registry := loadTestRegistry(t)
			aggregate := runtimeDispatchStep(t, registry, "aggregate_scripts")
			if target != "terminal" {
				aggregate.Next = []capability.TransitionRef{{When: "completed", To: target}}
			}
			if target == "build_story_bible" {
				worker := runtimeDispatchStep(t, registry, target)
				worker.Kind, worker.Batch = "model", nil
				worker.InputRefs = []capability.ArtifactRef{{ArtifactType: "scripts", Cardinality: "one", RequiredStatus: "confirmed", VersionPolicy: "approval_snapshot"}}
			}
			store, err := Open(filepath.Join(t.TempDir(), "aggregate.db"), registry)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			// Explicitly seeded confirmed units test Runtime dispatch, not model generation.
			runID, stepRunID, projectID := seedQualityReviewRunWithStepID(t, store, "aggregate_scripts")
			seedQualityReviewScriptArtifacts(t, store, runID, projectID)
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			inputs, err := currentRunInputsForStepTx(ctx, tx, projectID, runID, aggregate.InputRefs)
			if err != nil {
				t.Fatal(err)
			}
			input, err := json.Marshal(inputs)
			if err != nil {
				t.Fatal(err)
			}
			for _, query := range []struct {
				sql  string
				args []any
			}{
				{`UPDATE step_runs SET status='pending', input_version_snapshot_json=? WHERE step_run_id=?`, []any{string(input), stepRunID}},
				{`UPDATE runs SET status='paused' WHERE run_id=?`, []any{runID}},
				{`UPDATE projects SET status='paused', active_write_run_id=? WHERE project_id=?`, []any{runID, projectID}},
			} {
				if _, err := tx.ExecContext(ctx, query.sql, query.args...); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			command := runtimeDispatchResumeCommand(Run{RunID: runID, ProjectID: projectID})
			result, err := store.ResumeRun(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus, wantStep, wantCompleted := "running", target, 0
			if target == "terminal" {
				wantStatus, wantStep, wantCompleted = "completed", "aggregate_scripts", 1
			}
			if result.Run.Status != wantStatus || currentStepID(result) != wantStep {
				t.Fatalf("aggregate successor %s: %+v", target, result)
			}
			var completionEvents, aggregateTasks int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE run_id=? AND event_type='run.completed'`, runID).Scan(&completionEvents); err != nil || completionEvents != wantCompleted {
				t.Fatalf("premature/missing completion: %d %v", completionEvents, err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_items WHERE step_run_id=?`, stepRunID).Scan(&aggregateTasks); err != nil || aggregateTasks != 0 {
				t.Fatalf("aggregate was sent to Worker: %d %v", aggregateTasks, err)
			}
			beforeTasks, err := store.ListTaskItems(ctx, *result.Run.CurrentStepRunID)
			if err != nil || target != "terminal" && len(beforeTasks) == 0 {
				t.Fatalf("successor tasks: %+v %v", beforeTasks, err)
			}
			replayed, err := store.ResumeRun(ctx, command)
			if err != nil || replayed.EventCursor != result.EventCursor {
				t.Fatalf("aggregate receipt: %+v %v", replayed, err)
			}
			afterTasks, err := store.ListTaskItems(ctx, *result.Run.CurrentStepRunID)
			if err != nil || len(afterTasks) != len(beforeTasks) {
				t.Fatalf("aggregate receipt repeated task planning: %+v %v", afterTasks, err)
			}
		})
	}
}
