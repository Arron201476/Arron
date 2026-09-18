package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
)

func prepareWorkflowContinueApproval(t *testing.T, store *Store) (RunSnapshot, Approval) {
	t.Helper()
	ctx := context.Background()
	_, initial, _, _ := prepareStoryBibleForImpact(t, store, "User continuation")
	approval, err := store.pendingApprovalForRun(ctx, initial.Run.RunID)
	if err != nil || approval == nil {
		t.Fatalf("artifact approval: %+v %v", approval, err)
	}
	waiting, err := store.ResolveApproval(ctx, ResolveApprovalCommand{ApprovalRequestID: approval.ApprovalRequestID,
		Action: "approve", ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash})
	if err != nil || waiting.Run.Status != "waiting_approval" || waiting.CurrentApproval == nil || waiting.CurrentApproval.Scope != "workflow_transition" {
		t.Fatalf("manual continuation gate: %+v %v", waiting, err)
	}
	if currentStepID(waiting) != "build_story_bible" || waiting.CurrentApproval.ApprovalRequestID == approval.ApprovalRequestID {
		t.Fatalf("continuation advanced or reused the artifact approval: %+v", waiting)
	}
	return waiting, *waiting.CurrentApproval
}

func workflowContinueRegistry(t *testing.T) *capability.Registry {
	t.Helper()
	registry := loadTestRegistry(t)
	entry, _ := registry.Get("novel_to_script")
	for index := range entry.Definition.Steps {
		if step := &entry.Definition.Steps[index]; step.ID == "build_story_bible" {
			step.Next = []capability.TransitionRef{{When: "failure", To: "ingest_source"}, {When: "user_continue", To: "review_volume_fit"}}
		}
	}
	return registry
}

func TestWorkflowContinueApprovalSurvivesReopenAndDoesNotReplaySuccessor(t *testing.T) {
	ctx := context.Background()
	registry := workflowContinueRegistry(t)
	path := filepath.Join(t.TempDir(), "continue.db")
	store, err := Open(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	waiting, approval := prepareWorkflowContinueApproval(t, store)
	before := episodeApprovalState(t, store, approval)
	_, err = store.ResumeRun(ctx, ResumeRunCommand{RunID: waiting.Run.RunID})
	assertDomainCode(t, err, "RUN_STATE_CONFLICT")
	if episodeApprovalState(t, store, approval) != before {
		t.Fatal("resume bypassed or changed the pending user gate")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	command := ResolveApprovalCommand{CommandMeta: CommandMeta{Scope: approval.ProjectID, CommandType: "resolve_approval",
		IdempotencyKey: "workflow-continue", RequestHash: "workflow-continue-v1"}, ApprovalRequestID: approval.ApprovalRequestID,
		Action: "approve", ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash}
	result, err := store.ResolveApproval(ctx, command)
	if err != nil || result.Run.Status != "paused" || currentStepID(result) != "review_volume_fit" || result.CurrentApproval != nil {
		t.Fatalf("explicit continuation: %+v %v", result, err)
	}
	before = episodeApprovalState(t, store, approval)
	replayed, err := store.ResolveApproval(ctx, command)
	if err != nil || replayed.Run.CurrentStepRunID == nil || *replayed.Run.CurrentStepRunID != *result.Run.CurrentStepRunID || episodeApprovalState(t, store, approval) != before {
		t.Fatalf("continuation replay changed the successor: %+v %v", replayed, err)
	}
	var volumeDecisions int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM run_decision_snapshots WHERE run_id=? AND decision_type='volume_fit'`, result.Run.RunID).Scan(&volumeDecisions); err != nil || volumeDecisions != 0 {
		t.Fatalf("manual gate was interpreted as an expansion strategy: %d %v", volumeDecisions, err)
	}
}

func TestWorkflowContinueRejectsChangedSubjectsWithoutCommitting(t *testing.T) {
	for _, scenario := range []string{"client-target", "decision-hash", "bound-version", "unconfirmed-output", "missing-binding", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(filepath.Join(t.TempDir(), "reject.db"), workflowContinueRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			waiting, approval := prepareWorkflowContinueApproval(t, store)
			command := ResolveApprovalCommand{ApprovalRequestID: approval.ApprovalRequestID, Action: "approve",
				ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash}
			code := "APPROVAL_SUBJECT_CHANGED"
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "client-target":
				command.ResolutionPayload = json.RawMessage(`{"to_step_id":"ingest_source"}`)
				code = "REQUEST_VALIDATION_FAILED"
			case "decision-hash":
				exec(`UPDATE run_decision_snapshots SET snapshot_hash='invalid' WHERE decision_snapshot_id=?`, approval.SubjectRefID)
			case "bound-version":
				exec(`UPDATE artifacts SET current_version_id='' WHERE current_version_id IN (SELECT artifact_version_id FROM approval_subject_versions WHERE approval_request_id=?)`, approval.ApprovalRequestID)
			case "unconfirmed-output":
				exec(`UPDATE artifact_versions SET status='pending_approval' WHERE artifact_version_id IN (SELECT artifact_version_id FROM approval_subject_versions WHERE approval_request_id=?)`, approval.ApprovalRequestID)
			case "missing-binding":
				exec(`DELETE FROM approval_subject_versions WHERE approval_request_id=?`, approval.ApprovalRequestID)
			case "cancelled":
				cancelled, err := store.CancelRun(ctx, CancelRunCommand{RunID: waiting.Run.RunID, Confirmed: true})
				if err != nil || cancelled.Run.Status != "cancelled" {
					t.Fatalf("cancel at continuation gate: %+v %v", cancelled, err)
				}
				code = "APPROVAL_ALREADY_RESOLVED"
			}
			before := episodeApprovalState(t, store, approval)
			_, err = store.ResolveApproval(ctx, command)
			assertDomainCode(t, err, code)
			if episodeApprovalState(t, store, approval) != before {
				t.Fatal("rejected continuation changed durable state")
			}
		})
	}
}

func TestRuntimeStepCanWaitForUserContinueWithoutRerunningIt(t *testing.T) {
	ctx := context.Background()
	registry := loadTestRegistry(t)
	entry, _ := registry.Get("novel_to_script")
	for index := range entry.Definition.Steps {
		if step := &entry.Definition.Steps[index]; step.ID == "review_volume_fit" {
			step.Next = []capability.TransitionRef{{When: "user_continue", To: "split_episodes"}}
		}
	}
	store, err := Open(filepath.Join(t.TempDir(), "runtime-continue.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, initial, _, _ := prepareStoryBibleForImpactWithContent(t, store, "Runtime user gate", strings.Repeat("Source material. ", 4000))
	approval, err := store.pendingApprovalForRun(ctx, initial.Run.RunID)
	if err != nil || approval == nil {
		t.Fatalf("story approval: %+v %v", approval, err)
	}
	if _, err := store.ResolveApproval(ctx, ResolveApprovalCommand{ApprovalRequestID: approval.ApprovalRequestID, Action: "approve",
		ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash}); err != nil {
		t.Fatal(err)
	}
	waiting, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: initial.Run.RunID})
	if err != nil || waiting.Run.Status != "waiting_approval" || waiting.CurrentApproval == nil || waiting.CurrentApproval.Scope != "workflow_transition" || currentStepID(waiting) != "review_volume_fit" {
		t.Fatalf("runtime-created user gate: %+v %v", waiting, err)
	}
	approval = waiting.CurrentApproval
	continued, err := store.ResolveApproval(ctx, ResolveApprovalCommand{ApprovalRequestID: approval.ApprovalRequestID, Action: "approve",
		ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash})
	if err != nil || currentStepID(continued) != "split_episodes" || continued.Run.Status != "paused" {
		t.Fatalf("runtime continuation target: %+v %v", continued, err)
	}
}

func TestQualityReviewUserContinueGateBindsReviewInputs(t *testing.T) {
	ctx := context.Background()
	registry := loadTestRegistry(t)
	entry, _ := registry.Get("novel_to_script")
	var reviewStep capability.CompiledStep
	for index := range entry.Definition.Steps {
		if step := &entry.Definition.Steps[index]; step.ID == "review_script_set" {
			step.Next = []capability.TransitionRef{{When: "user_continue", To: "aggregate_scripts"}}
			reviewStep = *step
		}
	}
	store, err := Open(filepath.Join(t.TempDir(), "review-continue.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// This existing state fixture exercises transition persistence, not model review quality.
	runID, stepID, projectID := seedQualityReviewRun(t, store)
	seedQualityReviewScriptArtifacts(t, store, runID, projectID)
	if _, err := store.db.Exec(`UPDATE projects SET status='running', active_write_run_id=? WHERE project_id=?`, runID, projectID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT artifact_id, current_version_id, scope_key FROM artifacts
		WHERE run_id=? AND artifact_type IN ('script_unit','script_handoff') ORDER BY artifact_type`, runID)
	if err != nil {
		t.Fatal(err)
	}
	var inputs []map[string]any
	for rows.Next() {
		var artifact, version, scope string
		if err := rows.Scan(&artifact, &version, &scope); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		inputs = append(inputs, map[string]any{"artifact_id": artifact, "artifact_version_id": version, "scope_key": scope, "status": "confirmed"})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	payload, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE step_runs SET input_version_snapshot_json=? WHERE step_run_id=?`, string(payload), stepID); err != nil {
		t.Fatal(err)
	}
	if err := store.advancePassedQualityReviewTx(ctx, tx, runID, stepID, reviewStep, store.now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	waiting, err := store.GetRunSnapshot(ctx, runID)
	if err != nil || waiting.Run.Status != "waiting_approval" || waiting.CurrentApproval == nil || waiting.CurrentApproval.Scope != "workflow_transition" {
		t.Fatalf("quality review continuation: %+v %v", waiting, err)
	}
	approval := waiting.CurrentApproval
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM approval_subject_versions WHERE approval_request_id=?`, approval.ApprovalRequestID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("review must bind body and handoff: %d %v", count, err)
	}
	result, err := store.ResolveApproval(ctx, ResolveApprovalCommand{ApprovalRequestID: approval.ApprovalRequestID, Action: "approve",
		ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash})
	if err != nil || result.Run.Status != "paused" || currentStepID(result) != "aggregate_scripts" {
		t.Fatalf("quality review continuation target: %+v %v", result, err)
	}
}
