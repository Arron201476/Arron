package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/identity"
)

func statefulToolClaimForTest(t *testing.T, store *Store) (Project, *TaskClaim) {
	t.Helper()
	project, _, initial := startNovelRunForLifecycle(t, store, "Stateful SDK tools")
	approveAndResumeLifecycleRun(t, store, project, initial)
	return project, claimStructuredTaskOnce(t, store, 60)
}

func statefulToolCommand(project Project, claim *TaskClaim, tool, sdkID string, args json.RawMessage) BeginAgentToolCallCommand {
	return BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		ExecutionAttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
		ToolID: tool, SDKToolCallID: sdkID, Arguments: args}
}

func TestStatefulToolIdentityOwnerTokenLeaseAndIsolation(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "stateful.db"))
	defer store.Close()
	project, claim := statefulToolClaimForTest(t, store)
	ctx := context.Background()
	activity := AgentActivityIdentity{ProjectID: project.ProjectID, ExecutionAttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken}
	owner := identity.Principal{Kind: identity.KindUser, UserID: "workflow-author", WorkspaceID: project.WorkspaceID, Role: identity.RoleEditor}
	if err := store.BootstrapPrincipal(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE runs SET user_id = ? WHERE run_id = ?`, owner.UserID, claim.Attempt.RunID); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveAgentActivityPrincipal(ctx, activity)
	if err != nil || resolved.UserID != owner.UserID || resolved.Role != identity.RoleEditor {
		t.Fatalf("stateful owner: %+v %v", resolved, err)
	}
	for _, item := range []struct {
		name, code string
		edit       func(*AgentActivityIdentity)
	}{
		{"token", "ATTEMPT_TOKEN_INVALID", func(a *AgentActivityIdentity) { a.AttemptToken = "bad" }},
		{"missing-token", "ATTEMPT_TOKEN_INVALID", func(a *AgentActivityIdentity) { a.AttemptToken = "" }},
		{"project", "AGENT_ACTIVITY_SCOPE_MISMATCH", func(a *AgentActivityIdentity) { a.ProjectID = "foreign" }},
		{"mixed-turn", "AGENT_ACTIVITY_INVALID", func(a *AgentActivityIdentity) { a.AgentTurnID = "turn" }},
		{"mixed-background", "AGENT_ACTIVITY_INVALID", func(a *AgentActivityIdentity) { a.AgentTaskAttemptID = "attempt" }},
	} {
		t.Run(item.name, func(t *testing.T) {
			other := activity
			item.edit(&other)
			_, err := store.ResolveAgentActivityPrincipal(ctx, other)
			assertDomainCode(t, err, item.code)
		})
	}
	store.now = func() time.Time { return claim.Attempt.LeaseUntil.Add(time.Second) }
	_, err = store.ResolveAgentActivityPrincipal(ctx, activity)
	assertDomainCode(t, err, "ATTEMPT_LEASE_EXPIRED")
	activity.AllowTerminal = true
	if _, err := store.ResolveAgentActivityPrincipal(ctx, activity); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET status = 'disabled' WHERE user_id = ?`, owner.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveAgentActivityPrincipal(ctx, activity); err == nil {
		t.Fatal("revoked owner accepted")
	}
}

func TestStatefulToolBindingWriteIdempotencyRestartAndCancellation(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "stateful.db")
	store := openProjectFilesTestStore(t, database)
	defer func() { store.Close() }()
	project, claim := statefulToolClaimForTest(t, store)
	patch := ProjectFilePatch{Path: "stateful/result.txt", Operation: "create_file", Diff: "+Done"}
	args, _ := json.Marshal(patch)
	command := statefulToolCommand(project, claim, "runtime:apply_workspace_patch", "sdk-stateful", args)
	call, err := store.BeginAgentToolCall(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.BeginAgentToolCall(ctx, command)
	if err != nil || duplicate.AgentToolCallID != call.AgentToolCallID {
		t.Fatalf("duplicate: %+v %v", duplicate, err)
	}
	activity := AgentActivityIdentity{ProjectID: project.ProjectID, ExecutionAttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken}
	if err := store.ValidateAgentActivityToolCall(ctx, activity, call.AgentToolCallID); err != nil {
		t.Fatal(err)
	}
	wrong := activity
	wrong.ExecutionAttemptID = "foreign"
	assertDomainCode(t, store.ValidateAgentActivityToolCall(ctx, wrong, call.AgentToolCallID), "AGENT_ACTIVITY_SCOPE_MISMATCH")
	unbound := command
	unbound.ExecutionAttemptID = ""
	unbound.AttemptToken = ""
	_, err = store.BeginAgentToolCall(ctx, unbound)
	assertDomainCode(t, err, "AGENT_ACTIVITY_SCOPE_MISMATCH")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	duplicate, err = store.BeginAgentToolCall(ctx, command)
	if err != nil || duplicate.AgentToolCallID != call.AgentToolCallID {
		t.Fatalf("restart binding: %+v %v", duplicate, err)
	}
	file, err := store.ApplyProjectFilePatch(WithAgentActivity(ctx, activity), call.AgentToolCallID, call.SDKToolCallID, patch, "Done")
	if err != nil || file.Version != 1 {
		t.Fatalf("stateful file: %+v %v", file, err)
	}
	latePatch := ProjectFilePatch{Path: "stateful/late.txt", Operation: "create_file", Diff: "+Late"}
	lateArgs, _ := json.Marshal(latePatch)
	late, err := store.BeginAgentToolCall(ctx, statefulToolCommand(project, claim, command.ToolID, "sdk-late", lateArgs))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelRun(ctx, CancelRunCommand{RunID: claim.Attempt.RunID, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyProjectFilePatch(ctx, late.AgentToolCallID, late.SDKToolCallID, latePatch, "Late")
	if err == nil {
		t.Fatal("cancelled workflow wrote a file")
	}
	loaded, err := store.GetAgentToolCall(ctx, late.AgentToolCallID)
	if err != nil || loaded.Status != "cancelled" {
		t.Fatalf("cancelled tool: %+v %v", loaded, err)
	}
	_, err = store.ResolveAgentActivityPrincipal(ctx, activity)
	assertDomainCode(t, err, "ATTEMPT_STALE")
}

func TestStatefulToolExpiredAttemptCancelsApprovalAndCannotReplayBinding(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "stateful.db"))
	defer store.Close()
	registry, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion,
		HostedTools: []agenttool.HostedToolConfig{{ID: "code", Type: "code_interpreter", Description: "Compute", Enabled: true, Access: agenttool.AccessSensitive}}})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(registry)
	project, claim := statefulToolClaimForTest(t, store)
	command := statefulToolCommand(project, claim, "hosted:code", "sdk-pending", json.RawMessage(`{"instruction":"compute"}`))
	command.ConfigurationHash = toolConfigurationHashForTest(t, store, command.ToolID)
	call, err := store.BeginAgentToolCall(ctx, command)
	if err != nil || call.Approval == nil {
		t.Fatalf("approval: %+v %v", call, err)
	}
	store.now = func() time.Time { return claim.Attempt.LeaseUntil.Add(time.Second) }
	_, err = store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: command.ConfigurationHash})
	assertDomainCode(t, err, "ATTEMPT_LEASE_EXPIRED")
	fresh := claimStructuredTaskOnce(t, store, 60)
	if fresh.Attempt.AttemptID == claim.Attempt.AttemptID {
		t.Fatal("expired execution was reused")
	}
	loaded, err := store.GetAgentToolCall(ctx, call.AgentToolCallID)
	if err != nil || loaded.Status != "cancelled" || loaded.Approval.Status != "cancelled" {
		t.Fatalf("expired approval: %+v %v", loaded, err)
	}
	command.ExecutionAttemptID, command.AttemptToken = fresh.Attempt.AttemptID, fresh.AttemptToken
	_, err = store.BeginAgentToolCall(ctx, command)
	assertDomainCode(t, err, "AGENT_ACTIVITY_SCOPE_MISMATCH")
}

func TestStatefulToolSideEffectsPreventAutomaticReplay(t *testing.T) {
	for _, failure := range []string{"expired", "provider_failure"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "stateful.db"))
			defer store.Close()
			project, claim := statefulToolClaimForTest(t, store)
			patch := ProjectFilePatch{Path: "result.txt", Operation: "create_file", Diff: "+Saved"}
			args, _ := json.Marshal(patch)
			command := statefulToolCommand(project, claim, "runtime:apply_workspace_patch", "sdk-write", args)
			call, err := store.BeginAgentToolCall(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "Saved"); err != nil {
				t.Fatal(err)
			}
			if failure == "expired" {
				store.now = func() time.Time { return claim.Attempt.LeaseUntil.Add(time.Second) }
			} else {
				command := FailExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
					InputSnapshotHash: claim.Attempt.InputSnapshotHash, ErrorCode: "PROVIDER_TEMPORARY_FAILURE"}
				result, err := store.FailExecutionAttempt(ctx, command)
				if err != nil || result.Retryable || result.Task.Status != "failed" {
					t.Fatalf("unsafe retry: %+v %v", result, err)
				}
				if _, err := store.FailExecutionAttempt(ctx, command); err != nil {
					t.Fatalf("failure receipt not idempotent: %v", err)
				}
			}
			fresh, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{WorkerID: "replacement", ExecutorIDs: []string{"worker.structured_content"}, ProviderID: "test", LeaseSeconds: 60})
			if err != nil || fresh != nil {
				t.Fatalf("write was automatically replayed: %+v %v", fresh, err)
			}
			task, err := store.GetExecutionAttempt(ctx, claim.Attempt.AttemptID)
			if err != nil || (task.Status != "expired" && task.Status != "failed") {
				t.Fatalf("attempt did not settle: %+v %v", task, err)
			}
			file, err := store.ReadProjectFile(ctx, project.ProjectID, patch.Path, 0, 0, 100)
			if err != nil || file.Content != "Saved" || file.File.Version != 1 {
				t.Fatalf("output lost/repeated: %+v %v", file, err)
			}
		})
	}
}

func TestStatefulToolMigrateV40PreservesExecutionAndFiles(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "migration.db")
	store := openProjectFilesTestStore(t, database)
	defer func() { store.Close() }()
	project, claim := statefulToolClaimForTest(t, store)
	patch := ProjectFilePatch{Path: "before.txt", Operation: "create_file", Diff: "+Preserved"}
	call := beginProjectFileCall(t, store, project, patch)
	if _, err := store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "Preserved"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE execution_tool_calls; PRAGMA user_version=40;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	attempt, err := store.GetExecutionAttempt(ctx, claim.Attempt.AttemptID)
	if err != nil || attempt.InputSnapshotHash != claim.Attempt.InputSnapshotHash || attempt.Status != "running" {
		t.Fatalf("migrated attempt: %+v %v", attempt, err)
	}
	file, err := store.ReadProjectFile(ctx, project.ProjectID, patch.Path, 0, 0, 100)
	if err != nil || file.Content != "Preserved" {
		t.Fatalf("migrated file: %+v %v", file, err)
	}
	var backup string
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=40 AND to_version=?`, schemaVersion).Scan(&backup); err != nil || backup == "" {
		t.Fatalf("migration backup: %q %v", backup, err)
	}
	_, err = store.BeginAgentToolCall(ctx, statefulToolCommand(project, claim, "runtime:inspect_project", "sdk-after-migration", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
}
