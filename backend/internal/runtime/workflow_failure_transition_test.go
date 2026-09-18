package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
)

func workflowFailureRegistry(t *testing.T, capabilityID, sourceID string) *capability.Registry {
	t.Helper()
	registry := loadTestRegistry(t)
	entry, _ := registry.Get(capabilityID)
	for index := range entry.Definition.Steps {
		step := &entry.Definition.Steps[index]
		if step.ID == sourceID {
			recovery := *step
			recovery.ID = "recover_" + sourceID
			step.Next = append([]capability.TransitionRef{{When: "failure", To: recovery.ID}}, step.Next...)
			entry.Definition.Steps = append(entry.Definition.Steps, recovery)
			return registry
		}
	}
	t.Fatalf("missing source fixture step %s", sourceID)
	return nil
}

func failWorkflowClaim(t *testing.T, store *Store, claim *TaskClaim, code string) (FailExecutionAttemptCommand, AttemptFailureResult) {
	t.Helper()
	command := FailExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash, ErrorCode: code}
	result, err := store.FailExecutionAttempt(context.Background(), command)
	if err != nil {
		t.Fatalf("failure report: %v", err)
	}
	return command, result
}

func readWorkflowFailureFacts(t *testing.T, store *Store, runID string) workflowFailureTransition {
	t.Helper()
	var raw, hash string
	if err := store.db.QueryRow(`SELECT payload_json,snapshot_hash FROM run_decision_snapshots WHERE run_id=? AND decision_type='workflow_failure_transition'`, runID).Scan(&raw, &hash); err != nil {
		t.Fatal(err)
	}
	var facts workflowFailureTransition
	if json.Unmarshal([]byte(raw), &facts) != nil || hash != sha256Hex([]byte(raw)) {
		t.Fatalf("invalid failure snapshot: %s", raw)
	}
	return facts
}

func TestWorkflowFailureWaitsForRetryExhaustionAndExplicitResume(t *testing.T) {
	ctx := context.Background()
	registry := workflowFailureRegistry(t, "novel_to_script", "build_story_bible")
	database := filepath.Join(t.TempDir(), "failure.db")
	store, err := Open(database, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	project, first := statefulToolClaimForTest(t, store)
	_, result := failWorkflowClaim(t, store, first, "MODEL_RATE_LIMIT")
	if !result.Retryable {
		t.Fatal("declared failure edge consumed an automatic retry")
	}
	var decisions int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM run_decision_snapshots WHERE run_id=? AND decision_type='workflow_failure_transition'`, first.Attempt.RunID).Scan(&decisions); err != nil || decisions != 0 {
		t.Fatalf("retry created a successor: %d %v", decisions, err)
	}
	second := claimStructuredTask(t, store)
	command, result := failWorkflowClaim(t, store, second, "MODEL_RATE_LIMIT")
	if result.Retryable || result.Task.Status != "failed" || result.Attempt.Status != "failed" {
		t.Fatalf("original failure was hidden: %+v", result)
	}
	facts := readWorkflowFailureFacts(t, store, first.Attempt.RunID)
	paused, err := store.GetRunSnapshot(ctx, first.Attempt.RunID)
	if err != nil || paused.Run.Status != "paused" || currentStepID(paused) != "recover_build_story_bible" ||
		!containsAvailableAction(paused.AvailableActions, "resume_run") || facts.FromStepRunID != first.Attempt.StepRunID || facts.ToStepRunID != *paused.Run.CurrentStepRunID {
		t.Fatalf("failure successor not offered: %+v %+v %v", paused, facts, err)
	}
	if tasks, err := store.ListTaskItems(ctx, facts.ToStepRunID); err != nil || len(tasks) != 0 {
		t.Fatalf("failure successor executed without resume: %+v %v", tasks, err)
	}
	activities, err := store.ListProjectActivities(ctx, project.ProjectID, 0)
	visible := false
	for _, activity := range activities {
		visible = visible || activity.EventType == "workflow.failure_transition_prepared"
	}
	if err != nil || !visible {
		t.Fatalf("failure branch omitted from user activity projection: %+v %v", activities, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(database, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FailExecutionAttempt(ctx, command); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM run_decision_snapshots WHERE run_id=? AND decision_type='workflow_failure_transition'`, first.Attempt.RunID).Scan(&decisions); err != nil || decisions != 1 {
		t.Fatalf("duplicate failure created another successor: %d %v", decisions, err)
	}
	if _, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: first.Attempt.RunID}); err != nil {
		t.Fatal(err)
	}
	claim := claimStructuredTask(t, store)
	if claim.Attempt.StepRunID != facts.ToStepRunID || claim.Resume != nil || claim.Attempt.AttemptID == second.Attempt.AttemptID {
		t.Fatalf("successor replayed the original attempt: %+v", claim.Attempt)
	}
	found := false
	for _, decision := range claim.ContextPack.DecisionSnapshots {
		found = found || decision.DecisionType == "workflow_failure_transition" && strings.Contains(string(decision.Payload), second.Task.TaskItemID)
	}
	if !found {
		t.Fatal("successor context omitted the actual failure facts")
	}
}

func TestWorkflowFailureWaitsForWholeBatchIncludingLastSuccessfulItem(t *testing.T) {
	for _, execution := range []string{"sequential", "parallel"} {
		t.Run(execution, func(t *testing.T) {
			ctx := context.Background()
			registry := workflowFailureRegistry(t, "video_reference_creation", "extract_video_scripts")
			entry, _ := registry.Get("video_reference_creation")
			source := compiledStep(entry.Definition.Steps, "extract_video_scripts")
			source.Batch.Execution = execution
			if execution == "parallel" {
				source.StateTransition = nil
			}
			store, err := Open(filepath.Join(t.TempDir(), "batch.db"), registry)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, running := prepareRunningPartialVideoBatch(t, store)
			first := claimTaskForExecutor(t, store, "workflow.video_script_extract")
			failWorkflowClaim(t, store, first, "PROVIDER_RESPONSE_INVALID")
			if execution == "parallel" {
				run, err := store.GetRun(ctx, running.Run.RunID)
				if err != nil || run.Status != "running" || *run.CurrentStepRunID != first.Attempt.StepRunID {
					t.Fatalf("first failure skipped an unsettled batch item: %+v %v", run, err)
				}
				second := claimTaskForExecutor(t, store, "workflow.video_script_extract")
				commitVideoClaim(t, store, second)
			}
			snapshot, err := store.GetRunSnapshot(ctx, running.Run.RunID)
			if err != nil || snapshot.Run.Status != "paused" || currentStepID(snapshot) != "recover_extract_video_scripts" {
				t.Fatalf("settled batch did not select failure target: %+v %v", snapshot, err)
			}
			facts := readWorkflowFailureFacts(t, store, running.Run.RunID)
			failed, succeeded := 2, 0
			if execution == "parallel" {
				failed, succeeded = 1, 1
			}
			if len(facts.FailedItems) != failed || facts.SucceededItems != succeeded {
				t.Fatalf("batch facts omitted settled items: %+v", facts)
			}
			if tasks, err := store.ListTaskItems(ctx, facts.ToStepRunID); err != nil || len(tasks) != 0 {
				t.Fatalf("batch successor started automatically: %+v %v", tasks, err)
			}
		})
	}
}

func TestWorkflowInvalidFailureEdgeStillCommitsTheFailure(t *testing.T) {
	for _, scenario := range []string{"ambiguous", "missing-target", "self-retry", "missing-input", "changed-source", "write-lock", "guardrail"} {
		t.Run(scenario, func(t *testing.T) {
			registry := workflowFailureRegistry(t, "novel_to_script", "build_story_bible")
			entry, _ := registry.Get("novel_to_script")
			source := compiledStep(entry.Definition.Steps, "build_story_bible")
			target := compiledStep(entry.Definition.Steps, "recover_build_story_bible")
			switch scenario {
			case "ambiguous":
				source.Next = append(source.Next, capability.TransitionRef{When: "failure", To: "ingest_source"})
			case "missing-target":
				source.Next[0].To = "missing"
			case "self-retry":
				source.Next[0].To = source.ID
			case "missing-input":
				target.InputRefs = []capability.ArtifactRef{{ArtifactType: "scripts", Cardinality: "one"}}
			}
			store, err := Open(filepath.Join(t.TempDir(), "invalid.db"), registry)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, claim := statefulToolClaimForTest(t, store)
			if scenario == "changed-source" {
				if _, err := store.db.Exec(`UPDATE artifact_versions SET status='pending_approval' WHERE artifact_version_id IN
					(SELECT json_extract(value,'$.artifact_version_id') FROM step_runs,json_each(input_version_snapshot_json) WHERE step_run_id=?)`, claim.Attempt.StepRunID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "write-lock" {
				if _, err := store.db.Exec(`UPDATE projects SET active_write_run_id=NULL WHERE project_id=(SELECT project_id FROM runs WHERE run_id=?)`, claim.Attempt.RunID); err != nil {
					t.Fatal(err)
				}
			}
			code := "PROVIDER_RESPONSE_INVALID"
			if scenario == "guardrail" {
				code = "AGENT_OUTPUT_GUARDRAIL_REJECTED"
			}
			_, failed := failWorkflowClaim(t, store, claim, code)
			run, err := store.GetRun(context.Background(), claim.Attempt.RunID)
			if err != nil || run.Status != "failed" || *run.CurrentStepRunID != claim.Attempt.StepRunID || failed.Task.Status != "failed" {
				t.Fatalf("invalid branch swallowed failure: %+v %+v %v", run, failed, err)
			}
			var blocked, prepared int
			if err := store.db.QueryRow(`SELECT COUNT(CASE WHEN event_type='workflow.failure_transition_blocked' THEN 1 END),
				COUNT(CASE WHEN event_type='workflow.failure_transition_prepared' THEN 1 END) FROM events WHERE run_id=?`, run.RunID).Scan(&blocked, &prepared); err != nil || blocked != 1 || prepared != 0 {
				t.Fatalf("failure branch audit: %d %d %v", blocked, prepared, err)
			}
		})
	}
}

func TestWorkflowFailureKeepsDelegatedSkillActiveUntilTheRunEnds(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "invocation.db"), workflowFailureRegistry(t, "novel_to_script", "build_story_bible"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, claim := statefulToolClaimForTest(t, store)
	user, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "Invocation fixture request")
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "Invocation fixture response")
	if err != nil {
		t.Fatal(err)
	}
	// Seed only the invocation association; task execution above uses the public lifecycle.
	if _, err := store.db.Exec(`UPDATE messages SET role='assistant' WHERE message_id=?`, assistant.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO skill_invocations(skill_invocation_id,project_id,conversation_id,user_message_id,agent_message_id,
		capability_id,capability_version,execution_mode,status,run_id,created_at,updated_at)
		SELECT 'failure-invocation',project_id,conversation_id,?,?,capability_id,capability_version,'stateful_workflow','delegated_to_run',run_id,created_at,updated_at FROM runs WHERE run_id=?`,
		user.MessageID, assistant.MessageID, claim.Attempt.RunID); err != nil {
		t.Fatal(err)
	}
	failWorkflowClaim(t, store, claim, "PROVIDER_RESPONSE_INVALID")
	var status string
	if err := store.db.QueryRow(`SELECT status FROM skill_invocations WHERE skill_invocation_id='failure-invocation'`).Scan(&status); err != nil || status != "delegated_to_run" {
		t.Fatalf("failure branch prematurely finalized the Skill invocation: %q %v", status, err)
	}
	if _, err := store.CancelRun(ctx, CancelRunCommand{RunID: claim.Attempt.RunID, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT status FROM skill_invocations WHERE skill_invocation_id='failure-invocation'`).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("cancel did not finalize the delegated invocation: %q %v", status, err)
	}
}

func TestWorkflowFailurePreservesCheckpointsAndCompletedEffectsWithoutReplay(t *testing.T) {
	for _, mode := range []string{"pending", "approved", "running", "completed", "failed", "checkpoint-approved", "checkpoint-rejected"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(filepath.Join(t.TempDir(), "tools.db"), workflowFailureRegistry(t, "novel_to_script", "build_story_bible"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			project, claim := statefulToolClaimForTest(t, store)
			call := statefulApprovalForTest(t, store, project, claim, "failure-write")
			checkpoint := strings.HasPrefix(mode, "checkpoint-")
			if checkpoint {
				if _, err := store.PauseExecutionForApproval(ctx, statefulPauseForTest(claim, call.SDKToolCallID)); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "pending" {
				action := "approve"
				if mode == "checkpoint-rejected" {
					action = "reject"
				}
				resolveBackgroundApprovalForTest(t, store, call, action)
			}
			if checkpoint {
				claim, err = store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
				if err != nil || claim == nil {
					t.Fatalf("resume checkpoint fixture: %+v %v", claim, err)
				}
			} else if mode == "running" || mode == "completed" || mode == "failed" {
				if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID,
					ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: toolConfigurationHashForTest(t, store, call.ToolID)}); err != nil {
					t.Fatal(err)
				}
				if mode == "completed" {
					if _, err := store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: json.RawMessage(`{"saved":true}`)}); err != nil {
						t.Fatal(err)
					}
				} else if mode == "failed" {
					if _, err := store.FailAgentToolCall(ctx, FailAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ErrorCode: "ACK_LOST", ErrorMessage: "Unknown external outcome"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			failWorkflowClaim(t, store, claim, "PROVIDER_RESPONSE_INVALID")
			run, err := store.GetRun(ctx, claim.Attempt.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "running" || mode == "failed" {
				if run.Status != "failed" {
					t.Fatalf("unknown write outcome advanced: %+v", run)
				}
				return
			}
			if run.Status != "paused" {
				t.Fatalf("settled failure did not offer a successor: %+v", run)
			}
			facts := readWorkflowFailureFacts(t, store, run.RunID)
			if mode == "completed" && (len(facts.CompletedEffects) != 1 || facts.CompletedEffects[0].CallID != call.AgentToolCallID ||
				facts.CompletedEffects[0].ResultHash == "" || facts.CompletedEffects[0].ArgumentsHash != call.ArgumentsHash || facts.CompletedEffects[0].SDKToolCallID != call.SDKToolCallID) {
				t.Fatalf("completed effect lost: %+v", facts)
			}
			if checkpoint {
				var raw string
				if err := store.db.QueryRow(`SELECT state_json FROM execution_run_states WHERE attempt_id=?`, claim.Attempt.AttemptID).Scan(&raw); err != nil || !strings.Contains(raw, "sdk-private-marker") || len(facts.RetainedCheckpoints) != 1 {
					t.Fatalf("original checkpoint was discarded: %s %+v %v", raw, facts, err)
				}
			}
			if _, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: run.RunID}); err != nil {
				t.Fatal(err)
			}
			next := claimStructuredTask(t, store)
			if next.Resume != nil || next.Attempt.AttemptID == claim.Attempt.AttemptID {
				t.Fatal("successor reused the failed attempt checkpoint")
			}
		})
	}
}

func TestWorkflowFailureResumeRevalidatesFrozenTransition(t *testing.T) {
	for _, scenario := range []string{"decision-hash", "decision-missing", "cursor-missing", "step-input", "source-changed", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(filepath.Join(t.TempDir(), "resume.db"), workflowFailureRegistry(t, "novel_to_script", "build_story_bible"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, claim := statefulToolClaimForTest(t, store)
			failWorkflowClaim(t, store, claim, "PROVIDER_RESPONSE_INVALID")
			facts := readWorkflowFailureFacts(t, store, claim.Attempt.RunID)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := store.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "decision-hash":
				exec(`UPDATE run_decision_snapshots SET snapshot_hash='bad' WHERE run_id=? AND decision_type='workflow_failure_transition'`, claim.Attempt.RunID)
			case "decision-missing":
				exec(`DELETE FROM run_decision_snapshots WHERE run_id=? AND decision_type='workflow_failure_transition'`, claim.Attempt.RunID)
			case "cursor-missing":
				exec(`UPDATE step_runs SET task_cursor_json='{}' WHERE step_run_id=?`, facts.ToStepRunID)
			case "step-input":
				exec(`UPDATE step_runs SET input_version_snapshot_json='[]' WHERE step_run_id=?`, facts.ToStepRunID)
			case "source-changed":
				exec(`UPDATE task_items SET failure='WORKER_INTERNAL_ERROR' WHERE task_item_id=?`, claim.Task.TaskItemID)
			case "cancelled":
				if _, err := store.CancelRun(ctx, CancelRunCommand{RunID: claim.Attempt.RunID, Confirmed: true}); err != nil {
					t.Fatal(err)
				}
			}
			_, err = store.ResumeRun(ctx, ResumeRunCommand{RunID: claim.Attempt.RunID})
			code := "WORKFLOW_FAILURE_TRANSITION_CHANGED"
			if scenario == "cancelled" {
				code = "RUN_STATE_CONFLICT"
			}
			assertDomainCode(t, err, code)
			if tasks, err := store.ListTaskItems(ctx, facts.ToStepRunID); err != nil || len(tasks) != 0 {
				t.Fatalf("rejected continuation queued work: %+v %v", tasks, err)
			}
		})
	}
}

func TestWorkflowFailureSnapshotWriteFailureRollsBackSettlement(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "atomic.db"), workflowFailureRegistry(t, "novel_to_script", "build_story_bible"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, claim := statefulToolClaimForTest(t, store)
	if _, err := store.db.Exec(`CREATE TRIGGER fail_transition_snapshot BEFORE INSERT ON run_decision_snapshots
		WHEN NEW.decision_type='workflow_failure_transition' BEGIN SELECT RAISE(ABORT,'failure snapshot unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	command := FailExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash, ErrorCode: "PROVIDER_RESPONSE_INVALID"}
	_, err = store.FailExecutionAttempt(ctx, command)
	if err == nil || !strings.Contains(err.Error(), "failure snapshot unavailable") {
		t.Fatalf("expected actual injected failure: %v", err)
	}
	var status string
	var created int
	if err := store.db.QueryRow(`SELECT status FROM execution_attempts WHERE attempt_id=?`, claim.Attempt.AttemptID).Scan(&status); err != nil || status != "running" {
		t.Fatalf("partial failure receipt committed: %q %v", status, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM step_runs WHERE run_id=? AND step_id='recover_build_story_bible'`, claim.Attempt.RunID).Scan(&created); err != nil || created != 0 {
		t.Fatalf("orphan successor committed: %d %v", created, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_transition_snapshot`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FailExecutionAttempt(ctx, command); err != nil {
		t.Fatal(err)
	}
}
