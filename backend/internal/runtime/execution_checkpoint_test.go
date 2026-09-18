package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func statefulApprovalForTest(t *testing.T, store *Store, project Project, claim *TaskClaim, sdkID string) AgentToolCall {
	t.Helper()
	command := statefulToolCommand(project, claim, "mcp:fixture/save_fact", sdkID, json.RawMessage(`{"value":"safe"}`))
	command.ConfigurationHash = toolConfigurationHashForTest(t, store, command.ToolID)
	call, err := store.BeginAgentToolCall(context.Background(), command)
	if err != nil || call.Approval == nil {
		t.Fatalf("stateful approval: %+v %v", call, err)
	}
	return call
}

func statefulPauseForTest(claim *TaskClaim, sdkIDs ...string) PauseExecutionForApprovalCommand {
	return PauseExecutionForApprovalCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash, WorkerState: json.RawMessage(`{"phase":"generate","batch_cursor":40,"private":"worker-private-marker"}`),
		PauseAgentTurnForApprovalCommand: PauseAgentTurnForApprovalCommand{SchemaVersion: "1.16",
			RunState: json.RawMessage(`{"$schemaVersion":"1.16","private":"sdk-private-marker"}`), PendingSDKToolCallIDs: sdkIDs}}
}

func statefulResumeCommand(claim *TaskClaim) ClaimExecutionTaskCommand {
	command := ClaimExecutionTaskCommand{WorkerID: "resumed-worker", ProviderID: claim.Attempt.ProviderID, ExecutorIDs: []string{claim.ExecutorID}, LeaseSeconds: 60}
	if claim.ContextPack.Budget.ModelID != nil {
		command.ModelID = *claim.ContextPack.Budget.ModelID
	}
	return command
}

func TestStatefulApprovalRepeatedResumePreservesSnapshotAndRotatesToken(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "approval.db")
	store := openProjectFilesTestStore(t, database)
	defer func() { store.Close() }()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, claim := statefulToolClaimForTest(t, store)
	originalID, originalHash := claim.Attempt.AttemptID, claim.Attempt.InputSnapshotHash
	originalPack, _ := json.Marshal(claim.ContextPack)
	for i := 0; i < 4; i++ {
		call := statefulApprovalForTest(t, store, project, claim, fmt.Sprintf("stateful-%d", i))
		pause := statefulPauseForTest(claim, call.SDKToolCallID)
		waiting, err := store.PauseExecutionForApproval(ctx, pause)
		if err != nil || waiting.Status != "waiting_approval" {
			t.Fatalf("pause: %+v %v", waiting, err)
		}
		if _, err := store.PauseExecutionForApproval(ctx, pause); err != nil {
			t.Fatalf("pause retry: %v", err)
		}
		if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim)); err != nil || next != nil {
			t.Fatalf("unapproved task resumed: %+v %v", next, err)
		}
		public, _ := json.Marshal(waiting)
		var leaked int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE project_id = ? AND payload_json LIKE '%private-marker%'`, project.ProjectID).Scan(&leaked); err != nil || leaked != 0 || strings.Contains(string(public), "private-marker") {
			t.Fatalf("checkpoint leaked: %d %v", leaked, err)
		}
		if i == 0 {
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store = openProjectFilesTestStore(t, database)
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
		}
		// Waiting for a human is not a running lease and may outlast it.
		store.now = func() time.Time { return claim.Attempt.LeaseUntil.Add(time.Hour) }
		action := "approve"
		if i == 1 {
			action = "reject"
		}
		resolveBackgroundApprovalForTest(t, store, call, action)
		oldToken := claim.AttemptToken
		next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
		if err != nil || next == nil || next.Resume == nil || next.Attempt.AttemptID != originalID || next.Attempt.InputSnapshotHash != originalHash ||
			next.Attempt.AttemptNo != 1 || next.Task.AttemptCount != 1 || next.AttemptToken == oldToken || next.Resume.CheckpointVersion != i+1 {
			t.Fatalf("resume lost identity or checkpoint: %+v %v", next, err)
		}
		nextPack, _ := json.Marshal(next.ContextPack)
		if string(nextPack) != string(originalPack) || string(next.Resume.RunState) != string(pause.RunState) || string(next.Resume.WorkerState) != string(pause.WorkerState) ||
			len(next.Resume.ApprovalDecisions) != 1 || next.Resume.ApprovalDecisions[0].Action != action {
			t.Fatalf("resume changed frozen state: %+v", next.Resume)
		}
		wire, err := json.Marshal(next.Resume)
		if err != nil {
			t.Fatal(err)
		}
		var resumeEnvelope map[string]json.RawMessage
		if err := json.Unmarshal(wire, &resumeEnvelope); err != nil {
			t.Fatal(err)
		}
		if string(resumeEnvelope["schema_version"]) != `"1.16"` {
			t.Fatalf("Worker resume schema missing or changed: %s", wire)
		}
		_, err = store.PauseExecutionForApproval(ctx, pause)
		assertDomainCode(t, err, "ATTEMPT_TOKEN_INVALID")
		claim = next
		fixedNow := next.Attempt.LeaseUntil.Add(-30 * time.Second)
		store.now = func() time.Time { return fixedNow }
		if action == "approve" {
			if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID,
				ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: toolConfigurationHashForTest(t, store, call.ToolID)}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: json.RawMessage(`{"saved":true}`)}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestStatefulApprovalEarlyDecisionMultipleApprovalsAndCheckpointValidation(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "approval.db"))
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, claim := statefulToolClaimForTest(t, store)
	a := statefulApprovalForTest(t, store, project, claim, "a")
	b := statefulApprovalForTest(t, store, project, claim, "b")
	pause := statefulPauseForTest(claim, a.SDKToolCallID, b.SDKToolCallID)
	for _, item := range []struct {
		name string
		edit func(*PauseExecutionForApprovalCommand)
	}{
		{"schema", func(c *PauseExecutionForApprovalCommand) { c.SchemaVersion = "wrong" }},
		{"token", func(c *PauseExecutionForApprovalCommand) { c.AttemptToken = "wrong" }},
		{"input", func(c *PauseExecutionForApprovalCommand) { c.InputSnapshotHash = "wrong" }},
		{"missing-approval", func(c *PauseExecutionForApprovalCommand) { c.PendingSDKToolCallIDs = []string{"a"} }},
		{"foreign-approval", func(c *PauseExecutionForApprovalCommand) { c.PendingSDKToolCallIDs = []string{"foreign"} }},
		{"duplicate-approval", func(c *PauseExecutionForApprovalCommand) { c.PendingSDKToolCallIDs = []string{"a", "a"} }},
		{"worker-state", func(c *PauseExecutionForApprovalCommand) { c.WorkerState = json.RawMessage(`[]`) }},
	} {
		t.Run(item.name, func(t *testing.T) {
			bad := pause
			item.edit(&bad)
			if _, err := store.PauseExecutionForApproval(ctx, bad); err == nil {
				t.Fatal("invalid checkpoint accepted")
			}
		})
	}
	resolveBackgroundApprovalForTest(t, store, a, "approve")
	if _, err := store.PauseExecutionForApproval(ctx, pause); err != nil {
		t.Fatalf("decision-before-checkpoint race: %v", err)
	}
	bad := pause
	bad.WorkerState = json.RawMessage(`{"phase":"changed"}`)
	_, err := store.PauseExecutionForApproval(ctx, bad)
	assertDomainCode(t, err, "AGENT_RUN_STATE_CHECKPOINT_CONFLICT")
	if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim)); err != nil || next != nil {
		t.Fatalf("partly approved task resumed: %+v %v", next, err)
	}
	resolveBackgroundApprovalForTest(t, store, b, "reject")
	wrongModel := statefulResumeCommand(claim)
	wrongModel.ModelID = "not-the-frozen-model"
	if next, err := store.ClaimExecutionTask(ctx, wrongModel); err != nil || next != nil {
		t.Fatalf("model changed across approval: %+v %v", next, err)
	}
	resumed, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
	if err != nil || resumed == nil || len(resumed.Resume.ApprovalDecisions) != 2 {
		t.Fatalf("resolved task failed to resume: %+v %v", resumed, err)
	}
}

func TestStatefulApprovalPauseCancelExpiryAndCorruption(t *testing.T) {
	for _, action := range []string{"pause", "cancel", "delete", "expired-before-checkpoint", "expired-after-resume", "expired-while-pausing", "state-corrupt", "worker-corrupt", "context-corrupt", "checkpoint-missing", "revoked"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "approval.db"))
			defer store.Close()
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			project, claim := statefulToolClaimForTest(t, store)
			call := statefulApprovalForTest(t, store, project, claim, "sdk-wait")
			pause := statefulPauseForTest(claim, call.SDKToolCallID)
			if action == "expired-before-checkpoint" {
				store.now = func() time.Time { return claim.Attempt.LeaseUntil }
				_, err := store.PauseExecutionForApproval(ctx, pause)
				assertDomainCode(t, err, "ATTEMPT_LEASE_EXPIRED")
				return
			}
			if _, err := store.PauseExecutionForApproval(ctx, pause); err != nil {
				t.Fatal(err)
			}
			if action == "cancel" || action == "delete" {
				if action == "cancel" {
					if _, err := store.CancelRun(ctx, CancelRunCommand{RunID: claim.Attempt.RunID, Confirmed: true}); err != nil {
						t.Fatal(err)
					}
				} else {
					preview, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := store.ConfirmProjectDelete(ctx, ConfirmProjectDeleteCommand{ProjectID: project.ProjectID, PreviewHash: preview.SnapshotHash, Confirmed: true, ActorRef: "test"}); err != nil {
						t.Fatal(err)
					}
				}
				cancelled, _ := store.GetExecutionAttempt(ctx, claim.Attempt.AttemptID)
				tool, _ := store.GetAgentToolCall(ctx, call.AgentToolCallID)
				if cancelled.Status != "cancelled" || tool.Status != "cancelled" || tool.Approval.Status != "cancelled" {
					t.Fatalf("waiting execution not cancelled: %+v %+v", cancelled, tool)
				}
			} else {
				resolveBackgroundApprovalForTest(t, store, call, "approve")
			}
			switch action {
			case "pause":
				if _, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: claim.Attempt.RunID}); err != nil {
					t.Fatal(err)
				}
			case "state-corrupt", "worker-corrupt", "context-corrupt", "checkpoint-missing", "revoked":
				query := map[string]string{
					"state-corrupt":      `UPDATE execution_run_states SET state_json = '{}'`,
					"worker-corrupt":     `UPDATE execution_run_states SET worker_state_json = '{}'`,
					"context-corrupt":    `UPDATE context_packs SET payload_json = '{}'`,
					"checkpoint-missing": `DELETE FROM execution_run_states`,
					"revoked":            `UPDATE workspace_memberships SET status = 'disabled'`,
				}[action]
				if _, err := store.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			case "expired-after-resume", "expired-while-pausing":
				next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
				if err != nil || next == nil {
					t.Fatalf("resume: %+v %v", next, err)
				}
				if action == "expired-while-pausing" {
					if _, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: claim.Attempt.RunID}); err != nil {
						t.Fatal(err)
					}
				}
				store.now = func() time.Time { return next.Attempt.LeaseUntil }
			}
			if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim)); err != nil || next != nil {
				t.Fatalf("unsafe task resumed: %+v %v", next, err)
			}
			if action == "pause" {
				if _, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: claim.Attempt.RunID}); err != nil {
					t.Fatal(err)
				}
				resumed, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
				if err != nil || resumed == nil || resumed.Attempt.AttemptID != claim.Attempt.AttemptID || resumed.Resume == nil {
					t.Fatalf("user resume lost checkpoint: %+v %v", resumed, err)
				}
			}
			if action == "expired-while-pausing" {
				if _, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: claim.Attempt.RunID}); err == nil {
					t.Fatal("resuming an abandoned checkpoint silently restarts execution")
				}
			}
		})
	}
}

func TestStatefulApprovalMigrationFromV41PreservesExistingToolAndClaim(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "approval.db")
	store := openProjectFilesTestStore(t, database)
	defer func() { store.Close() }()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, claim := statefulToolClaimForTest(t, store)
	call := statefulApprovalForTest(t, store, project, claim, "before-migration")
	if _, err := store.db.Exec(`DROP TABLE execution_run_states`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version=41`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	var backup string
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=41 AND to_version=?`, schemaVersion).Scan(&backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("migration backup missing: %v", err)
	}
	if _, err := store.PauseExecutionForApproval(ctx, statefulPauseForTest(claim, call.SDKToolCallID)); err != nil {
		t.Fatal(err)
	}
	resolveBackgroundApprovalForTest(t, store, call, "approve")
	resumed, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
	if err != nil || resumed == nil || resumed.Attempt.AttemptID != claim.Attempt.AttemptID || resumed.Attempt.InputSnapshotHash != claim.Attempt.InputSnapshotHash {
		t.Fatalf("migration lost existing execution: %+v %v", resumed, err)
	}
}

func TestStatefulApprovalOnlyOneWorkerCanClaimResume(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "approval.db"))
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, claim := statefulToolClaimForTest(t, store)
	call := statefulApprovalForTest(t, store, project, claim, "concurrent-claim")
	if _, err := store.PauseExecutionForApproval(ctx, statefulPauseForTest(claim, call.SDKToolCallID)); err != nil {
		t.Fatal(err)
	}
	resolveBackgroundApprovalForTest(t, store, call, "approve")
	var wg sync.WaitGroup
	results := make(chan *TaskClaim, 8)
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			command := statefulResumeCommand(claim)
			command.WorkerID = fmt.Sprintf("worker-%d", i)
			result, err := store.ClaimExecutionTask(ctx, command)
			results <- result
			errors <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errors)
	count := 0
	for result := range results {
		if result != nil {
			count++
		}
	}
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("resume claimed %d times", count)
	}
}
