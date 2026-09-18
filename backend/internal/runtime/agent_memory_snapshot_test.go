package runtime

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestAgentMemorySnapshotRevokesReadersInAllExecutionModes(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "memory.db"))
			defer store.Close()
			ctx := mcpOwnerContext()
			var activity AgentActivityIdentity
			switch mode {
			case "conversation":
				activity, _ = AgentActivityFromContext(mcpExecutionContext(t, store, ctx))
			case "background":
				store.Close()
				var err error
				store, err = Open(filepath.Join(t.TempDir(), "background.db"), loadBackgroundTaskRegistry(t))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				task := createBackgroundTaskForTest(t, store)
				claim, err := store.ClaimAgentTask(context.Background(), ClaimAgentTaskCommand{WorkerID: "memory", ProviderID: "sdk", ModelID: "test", LeaseSeconds: 60})
				if err != nil || claim == nil {
					t.Fatalf("claim: %+v %v", claim, err)
				}
				activity = AgentActivityIdentity{ProjectID: task.ProjectID, AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken}
			case "stateful":
				project, claim := statefulToolClaimForTest(t, store)
				activity = AgentActivityIdentity{ProjectID: project.ProjectID, ExecutionAttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken}
			}
			command := UpdateAgentMemoryCommand{ProjectID: activity.ProjectID, Files: map[string]string{"memory_summary.md": "private"}, Enabled: true, RequestID: "save"}
			if _, err := store.UpdateAgentMemory(ctx, command); err != nil {
				t.Fatal(err)
			}
			service := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
			execution := WithAgentActivity(service, activity)
			first, err := store.ResolveAgentMemorySnapshot(execution)
			if err != nil || !first.ReadEnabled || first.Version != 1 || first.UserID != identity.DefaultUserID || first.Files["memory_summary.md"] != "private" {
				t.Fatalf("snapshot: %+v %v", first, err)
			}
			again, err := store.ResolveAgentMemorySnapshot(execution)
			if err != nil || !reflect.DeepEqual(first, again) {
				t.Fatalf("retry: %+v %v", again, err)
			}
			invalid := activity
			invalid.ProjectID = "foreign"
			if _, err := store.ResolveAgentMemorySnapshot(WithAgentActivity(service, invalid)); err == nil {
				t.Fatal("foreign identity accepted")
			}
			_, err = store.ResolveAgentMemorySnapshot(WithAgentActivity(ctx, activity))
			assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
			if _, err := store.UpdateAgentMemory(ctx, UpdateAgentMemoryCommand{ProjectID: activity.ProjectID, ExpectedVersion: 1, Forget: true, RequestID: "forget"}); err != nil {
				t.Fatal(err)
			}
			_, err = store.ResolveAgentMemorySnapshot(execution)
			assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
			_, err = store.ResolveAgentActivityPrincipal(service, activity)
			assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
			commitMode, commitID := "conversation", activity.AgentTurnID
			if mode == "background" {
				commitMode, commitID = "background_task", activity.AgentTaskAttemptID
			}
			if mode == "stateful" {
				commitMode, commitID = "stateful_workflow", activity.ExecutionAttemptID
			}
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertDomainCode(t, validateAgentMemoryCommitTx(ctx, tx, commitMode, commitID), "AGENT_MEMORY_CONFLICT")
			assertDomainCode(t, validateModelRecoveryToolsTx(ctx, tx, commitMode, commitID), "AGENT_MEMORY_CONFLICT")
			tx.Rollback()
			activity.AllowTerminal = true
			if _, err := store.ResolveAgentActivityPrincipal(service, activity); err != nil {
				t.Fatalf("cleanup blocked: %v", err)
			}
			_, err = store.ResolveAgentMemorySnapshot(WithAgentActivity(service, activity))
			assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
		})
	}
}

func TestAgentMemoryForgetBetweenBeginAndCompleteCommit(t *testing.T) {
	store, _, _ := mcpConnectionStore(t)
	ctx := mcpOwnerContext()
	execution := mcpExecutionContext(t, store, ctx)
	activity, _ := AgentActivityFromContext(execution)
	if _, err := store.UpdateAgentMemory(ctx, UpdateAgentMemoryCommand{ProjectID: activity.ProjectID, Files: map[string]string{"memory_summary.md": "private"}, Enabled: true, RequestID: "save"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveAgentMemorySnapshot(execution); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAgentTurnCommit(ctx, activity.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAgentMemory(ctx, UpdateAgentMemoryCommand{ProjectID: activity.ProjectID, ExpectedVersion: 1, Forget: true, RequestID: "forget"}); err != nil {
		t.Fatal(err)
	}
	_, err := store.BeginAgentTurnCommit(ctx, activity.AgentTurnID)
	assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
	_, err = store.CompleteAgentTurnCommit(ctx, activity.AgentTurnID, MessageExchange{})
	assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
}

func TestAgentMemorySnapshotDoesNotInjectIntoLegacyOrDisabledExecution(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		store, _, _ := mcpConnectionStore(t)
		ctx := mcpOwnerContext()
		execution := mcpExecutionContext(t, store, ctx)
		activity, _ := AgentActivityFromContext(execution)
		command := UpdateAgentMemoryCommand{ProjectID: activity.ProjectID, Files: map[string]string{"memory_summary.md": "private"}, Enabled: legacy, RequestID: "save"}
		if _, err := store.UpdateAgentMemory(ctx, command); err != nil {
			t.Fatal(err)
		}
		if legacy {
			if _, err := store.db.Exec(`INSERT INTO agent_turn_run_states VALUES(?, 'legacy', '{}', 'hash', '[]', 1, '', '')`, activity.AgentTurnID); err != nil {
				t.Fatal(err)
			}
		}
		first, err := store.ResolveAgentMemorySnapshot(execution)
		if err != nil || first.ReadEnabled || len(first.Files) != 0 {
			t.Fatalf("unexpected memory: %+v %v", first, err)
		}
		command.ExpectedVersion, command.RequestID, command.Enabled = 1, "enable", true
		if _, err := store.UpdateAgentMemory(ctx, command); err != nil {
			t.Fatal(err)
		}
		again, err := store.ResolveAgentMemorySnapshot(execution)
		if again.CurrentVersion != 2 {
			t.Fatalf("current memory version: %+v", again)
		}
		again.CurrentVersion = first.CurrentVersion
		if err != nil || !reflect.DeepEqual(first, again) {
			t.Fatalf("memory injected midway: %+v %v", again, err)
		}
	}
}
