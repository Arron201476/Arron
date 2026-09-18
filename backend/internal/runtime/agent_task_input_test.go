package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestBackgroundFailureInputReceiptRequiresClaimedPrefixAndIsAtomic(t *testing.T) {
	store, task, claimCommand := pausedBackgroundStore(t)
	defer store.Close()
	ctx := context.Background()
	ids := []string{}
	for _, content := range []string{"First requirement", "Second requirement"} {
		input, err := store.AppendAgentTaskInput(ctx, AppendAgentTaskInputCommand{AgentTaskID: task.AgentTaskID, Content: content})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, input.InputID)
	}
	claim, err := store.ClaimAgentTask(ctx, claimCommand)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	late, err := store.AppendAgentTaskInput(ctx, AppendAgentTaskInputCommand{AgentTaskID: task.AgentTaskID, Content: "Unclaimed late input"})
	if err != nil {
		t.Fatal(err)
	}
	command := FailAgentTaskCommand{AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken, ErrorCode: "AGENT_OUTPUT_GUARDRAIL_REJECTED", ErrorMessage: "Output was rejected"}
	for _, invalid := range [][]string{{ids[1]}, {ids[1], ids[0]}, {ids[0], ids[0]}, {ids[0], late.InputID}, {"foreign"}} {
		command.IncludedInputIDs = invalid
		_, err := store.FailAgentTask(ctx, command)
		assertDomainCode(t, err, "AGENT_TASK_INPUT_CONFLICT")
		current, err := store.GetAgentTask(ctx, task.AgentTaskID)
		if err != nil || current.Status != "pausing" || current.AdditionalInputs[0].Status != "received" {
			t.Fatal("invalid receipt partially committed", err)
		}
	}
	command.IncludedInputIDs = ids
	command.AttemptToken = "wrong-token"
	_, err = store.FailAgentTask(ctx, command)
	assertDomainCode(t, err, "ATTEMPT_TOKEN_INVALID")
	command.AttemptToken = claim.AttemptToken
	failed, err := store.FailAgentTask(ctx, command)
	if err != nil || failed.Status != "failed" {
		t.Fatal(err)
	}
	current, err := store.GetAgentTask(ctx, task.AgentTaskID)
	if err != nil || len(current.AdditionalInputs) != 3 || current.AdditionalInputs[0].Status != "included" || current.AdditionalInputs[1].Status != "included" || current.AdditionalInputs[2].Status != "received" {
		t.Fatal("failure lost model receipt or included late input", err)
	}
}

func TestBackgroundInputsAutoPauseResumeReceiptsAndFinalRace(t *testing.T) {
	store, task, claimCommand := pausedBackgroundStore(t)
	ctx := context.Background()
	claim, err := store.ClaimAgentTask(ctx, claimCommand)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	appendCommand := AppendAgentTaskInputCommand{CommandMeta: CommandMeta{IdempotencyKey: "append-1", CommandType: "append_agent_task_input", RequestHash: "first"}, AgentTaskID: task.AgentTaskID, Content: "New requirement"}
	input, err := store.AppendAgentTaskInput(ctx, appendCommand)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.AppendAgentTaskInput(ctx, appendCommand)
	if err != nil || repeated.InputID != input.InputID {
		t.Fatalf("duplicate input: %+v %v", repeated, err)
	}
	fresh, err := store.GetAgentTask(ctx, task.AgentTaskID)
	if err != nil || fresh.Status != "pausing" || len(fresh.AdditionalInputs) != 1 || fresh.AdditionalInputs[0].Status != "received" {
		t.Fatalf("received: %+v %v", fresh, err)
	}
	paused, err := store.CompleteAgentTaskPause(ctx, pauseCheckpoint(claim))
	if err != nil || paused.Status != "queued" {
		t.Fatalf("auto pause: %+v %v", paused, err)
	}
	resumed, err := store.ClaimAgentTask(ctx, claimCommand)
	if err != nil || resumed == nil || resumed.Resume == nil || resumed.Attempt.AgentTaskAttemptID != claim.Attempt.AgentTaskAttemptID || len(resumed.AdditionalInputs) != 1 {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	late, err := store.AppendAgentTaskInput(ctx, AppendAgentTaskInputCommand{AgentTaskID: task.AgentTaskID, Content: "Arrived after the model finished"})
	if err != nil {
		t.Fatal(err)
	}
	complete := CompleteAgentTaskCommand{AgentTaskAttemptID: resumed.Attempt.AgentTaskAttemptID, AttemptToken: resumed.AttemptToken, Result: json.RawMessage(`{"summary":"Done"}`), IncludedInputIDs: []string{late.InputID}}
	_, err = store.CompleteAgentTask(ctx, complete)
	assertDomainCode(t, err, "AGENT_TASK_INPUT_CONFLICT")
	complete.IncludedInputIDs = []string{input.InputID}
	if _, err := store.CompleteAgentTask(ctx, complete); err != nil {
		t.Fatal(err)
	}
	fresh, err = store.GetAgentTask(ctx, task.AgentTaskID)
	if err != nil || fresh.Status != "completed" || fresh.AdditionalInputs[0].Status != "included" || fresh.AdditionalInputs[1].Status != "received" || fresh.AdditionalInputs[1].IncludedAt != nil {
		t.Fatalf("late input falsely included: %+v %v", fresh, err)
	}
	repeated, err = store.AppendAgentTaskInput(ctx, appendCommand)
	if err != nil || repeated.InputID != input.InputID || repeated.Status != "included" || repeated.IncludedAt == nil {
		t.Fatalf("idempotent retry returned stale model receipt: %+v %v", repeated, err)
	}
	_, err = store.AppendAgentTaskInput(ctx, AppendAgentTaskInputCommand{AgentTaskID: task.AgentTaskID, Content: "Too late"})
	assertDomainCode(t, err, "AGENT_TASK_STATE_CONFLICT")
}

func TestBackgroundInputsManualPauseOverridesAutomaticResume(t *testing.T) {
	store, task, command := pausedBackgroundStore(t)
	ctx := context.Background()
	claim, err := store.ClaimAgentTask(ctx, command)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if _, err := store.AppendAgentTaskInput(ctx, AppendAgentTaskInputCommand{AgentTaskID: task.AgentTaskID, Content: "Append"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequestAgentTaskPause(ctx, PauseAgentTaskCommand{AgentTaskID: task.AgentTaskID}); err != nil {
		t.Fatal(err)
	}
	paused, err := store.CompleteAgentTaskPause(ctx, pauseCheckpoint(claim))
	if err != nil || paused.Status != "paused" {
		t.Fatalf("manual pause lost: %+v %v", paused, err)
	}
	if claimed, err := store.ClaimAgentTask(ctx, command); err != nil || claimed != nil {
		t.Fatalf("manual pause claimed: %+v %v", claimed, err)
	}
	if _, err := store.AppendAgentTaskInput(ctx, AppendAgentTaskInputCommand{AgentTaskID: task.AgentTaskID, Content: "Keep paused"}); err != nil {
		t.Fatal(err)
	}
	fresh, _ := store.GetAgentTask(ctx, task.AgentTaskID)
	if fresh.Status != "paused" {
		t.Fatal("append resumed manually paused task")
	}
}

func TestBackgroundInputsRejectCollaboratorsAndRevokedWritePermission(t *testing.T) {
	store, task, _ := pausedBackgroundStore(t)
	for _, user := range []identity.Principal{
		{Kind: identity.KindUser, WorkspaceID: task.WorkspaceID, UserID: "other-author", Role: identity.RoleOwner},
		{Kind: identity.KindUser, WorkspaceID: task.WorkspaceID, UserID: task.UserID, Role: identity.RoleViewer},
	} {
		ctx := identity.WithPrincipal(context.Background(), user)
		_, err := store.AppendAgentTaskInput(ctx, AppendAgentTaskInputCommand{AgentTaskID: task.AgentTaskID, Content: "Cannot inject into another author's execution"})
		assertDomainCode(t, err, "ROLE_FORBIDDEN")
	}
	stale := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, WorkspaceID: task.WorkspaceID, UserID: task.UserID, Role: identity.RoleOwner})
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, task.WorkspaceID, task.UserID); err != nil {
		t.Fatal(err)
	}
	_, err := store.AppendAgentTaskInput(stale, AppendAgentTaskInputCommand{AgentTaskID: task.AgentTaskID, Content: "Stale owner principal"})
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
}

func TestBackgroundInputsApprovalBoundariesRequireExplicitResume(t *testing.T) {
	for _, stage := range []string{"running", "waiting_approval", "approved_queued"} {
		t.Run(stage, func(t *testing.T) {
			store, task, command := pausedBackgroundStore(t)
			ctx := context.Background()
			claim, err := store.ClaimAgentTask(ctx, command)
			if err != nil || claim == nil {
				t.Fatal(err)
			}
			call := backgroundApprovalForTest(t, store, *claim, "pending")
			if stage != "running" {
				if _, err := store.PauseAgentTaskForApproval(ctx, pauseCheckpoint(claim, "pending")); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "approved_queued" {
				resolveBackgroundApprovalForTest(t, store, call, "approve")
			}
			if _, err := store.AppendAgentTaskInput(ctx, AppendAgentTaskInputCommand{AgentTaskID: task.AgentTaskID, Content: "New instruction does not approve old tools"}); err != nil {
				t.Fatal(err)
			}
			if stage == "running" {
				if _, err := store.CompleteAgentTaskPause(ctx, pauseCheckpoint(claim, "pending")); err != nil {
					t.Fatal(err)
				}
			}
			if stage != "approved_queued" {
				resolveBackgroundApprovalForTest(t, store, call, "reject")
			}
			fresh, _ := store.GetAgentTask(ctx, task.AgentTaskID)
			if fresh.Status != "paused" {
				t.Fatalf("approval auto resumed despite new input: %s", fresh.Status)
			}
			if claimed, err := store.ClaimAgentTask(ctx, command); err != nil || claimed != nil {
				t.Fatalf("claimed pending input approval boundary: %+v %v", claimed, err)
			}
			if _, err := store.ResumeAgentTask(ctx, ResumeAgentTaskCommand{AgentTaskID: task.AgentTaskID}); err != nil {
				t.Fatal(err)
			}
			resumed, err := store.ClaimAgentTask(ctx, command)
			if err != nil || resumed == nil || len(resumed.AdditionalInputs) != 1 || len(resumed.Resume.ApprovalDecisions) != 1 {
				t.Fatalf("resume: %+v %v", resumed, err)
			}
		})
	}
}

func TestV43MigrationAddsBackgroundInputsAndKeepsNativeCheckpoint(t *testing.T) {
	database := filepath.Join(t.TempDir(), "v43.db")
	store, err := Open(database, loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	task := createBackgroundTaskForTest(t, store)
	ctx := context.Background()
	claim, err := store.ClaimAgentTask(ctx, ClaimAgentTaskCommand{WorkerID: "worker", ProviderID: "sdk", ModelID: "fixture", LeaseSeconds: 60})
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if _, err := store.RequestAgentTaskPause(ctx, PauseAgentTaskCommand{AgentTaskID: task.AgentTaskID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAgentTaskPause(ctx, pauseCheckpoint(claim)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE agent_task_inputs; ALTER TABLE agent_tasks DROP COLUMN input_pause_requested; PRAGMA user_version=43;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(database, loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.GetAgentTask(ctx, task.AgentTaskID)
	if err != nil || fresh.Status != "paused" || len(fresh.AdditionalInputs) != 0 {
		t.Fatalf("migrated task changed: %+v %v", fresh, err)
	}
	var state, backup string
	if err := store.db.QueryRow(`SELECT state_json FROM agent_task_run_states WHERE agent_task_id=?`, task.AgentTaskID).Scan(&state); err != nil || state != string(pauseCheckpoint(claim).RunState) {
		t.Fatalf("native checkpoint changed: %s %v", state, err)
	}
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=43 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil || backup == "" {
		t.Fatalf("missing migration backup: %s %v", backup, err)
	}
}
