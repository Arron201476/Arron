package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func instructionUpdate(scope, projectID, content, id string, version int) UpdateAgentInstructionsCommand {
	return UpdateAgentInstructionsCommand{Scope: scope, ProjectID: projectID, Content: content, Enabled: content != "", RequestID: id, ExpectedVersion: version}
}

func TestAgentInstructionsScopesVersionsAndPrivateOwnership(t *testing.T) {
	store, _, _ := mcpConnectionStore(t)
	ctx := mcpOwnerContext()
	project, err := store.CreateProject(ctx, "Instructions")
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"workspace", "user", "project"} {
		command := instructionUpdate(scope, project.ProjectID, scope+" first", "first", 0)
		first, err := store.UpdateAgentInstructions(ctx, command)
		if err != nil || first.Version != 1 || !first.CanEdit {
			t.Fatalf("first: %+v %v", first, err)
		}
		second, err := store.UpdateAgentInstructions(ctx, instructionUpdate(scope, project.ProjectID, scope+" second", "second", 1))
		if err != nil || second.Version != 2 {
			t.Fatalf("second: %+v %v", second, err)
		}
		again, err := store.UpdateAgentInstructions(ctx, command)
		if err != nil || again != first {
			t.Fatalf("idempotent original receipt: %+v %v", again, err)
		}
		command.Content = "different"
		_, err = store.UpdateAgentInstructions(ctx, command)
		assertDomainCode(t, err, "IDEMPOTENCY_CONFLICT")
		command.RequestID = "stale"
		_, err = store.UpdateAgentInstructions(ctx, command)
		assertDomainCode(t, err, "AGENT_INSTRUCTIONS_CONFLICT")
	}
	view, err := store.GetAgentInstructions(ctx, project.ProjectID)
	if err != nil || len(view.Documents) != 3 || view.AppliesTo != "new_executions" {
		t.Fatalf("view: %+v %v", view, err)
	}
	for _, role := range []identity.Role{identity.RoleEditor, identity.RoleViewer, identity.RoleOwner} {
		user := identity.Principal{Kind: identity.KindUser, UserID: "instructions-" + string(role), WorkspaceID: identity.DefaultWorkspaceID, Role: role}
		if role == identity.RoleOwner {
			user.WorkspaceID = "foreign-instructions"
		}
		if err := store.BootstrapPrincipal(ctx, user); err != nil {
			t.Fatal(err)
		}
		other := identity.WithPrincipal(context.Background(), user)
		otherView, err := store.GetAgentInstructions(other, "")
		if err != nil || otherView.Documents[1].Content != "" || otherView.Documents[1].ScopeRef != user.UserID {
			t.Fatalf("private rule leak: %+v %v", otherView, err)
		}
		if role == identity.RoleOwner {
			if otherView.Documents[0].Content != "" {
				t.Fatal("workspace rule leak")
			}
			_, err = store.GetAgentInstructions(other, project.ProjectID)
			assertDomainCode(t, err, "PROJECT_NOT_FOUND")
			continue
		}
		if otherView.Documents[0].Content != "workspace second" || otherView.Documents[0].CanEdit {
			t.Fatalf("shared read: %+v", otherView)
		}
		_, err = store.UpdateAgentInstructions(other, instructionUpdate("user", "", "own", "own", 0))
		if role == identity.RoleViewer {
			assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
		} else if err != nil {
			t.Fatal(err)
		}
		forged := user
		forged.Role = identity.RoleOwner
		_, err = store.UpdateAgentInstructions(identity.WithPrincipal(ctx, forged), instructionUpdate("workspace", "", "forged", "forged", 2))
		assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
	}
	for _, command := range []UpdateAgentInstructionsCommand{
		instructionUpdate("unknown", "", "text", "bad", 0), instructionUpdate("project", "", "text", "bad", 0),
		instructionUpdate("user", "", strings.Repeat("x", 8193), "bad", 0), instructionUpdate("user", "", "nul\x00", "bad", 0),
		instructionUpdate("user", "", "text", "bad\n", 0), instructionUpdate("user", "", string([]byte{0xff}), "bad", 0),
		instructionUpdate("user", "", "   ", "bad", 0), instructionUpdate("user", "", "text", "bad", -1),
	} {
		_, err := store.UpdateAgentInstructions(ctx, command)
		assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	}
	_, err = store.UpdateAgentInstructions(identity.WithDelegatedUser(identity.WithPrincipal(ctx, identity.ServicePrincipal()), identity.DefaultLocalPrincipal()), instructionUpdate("user", "", "internal", "internal", 2))
	assertDomainCode(t, err, "AUTHENTICATION_REQUIRED")
}

func TestAgentInstructionsSnapshotPinsVersionsAcrossRestartAndRevocation(t *testing.T) {
	store, path, _ := mcpConnectionStore(t)
	ctx := mcpOwnerContext()
	execution := mcpExecutionContext(t, store, ctx)
	activity, _ := AgentActivityFromContext(execution)
	for _, scope := range []string{"workspace", "user", "project"} {
		if _, err := store.UpdateAgentInstructions(ctx, instructionUpdate(scope, activity.ProjectID, scope+" old", "save", 0)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ResolveAgentInstructionSnapshot(execution)
	if err != nil || len(first.Documents) != 3 || first.ActivityKey != "turn:"+activity.AgentTurnID {
		t.Fatalf("snapshot: %+v %v", first, err)
	}
	for _, scope := range []string{"workspace", "user", "project"} {
		if _, err := store.UpdateAgentInstructions(ctx, instructionUpdate(scope, activity.ProjectID, "", "forget", 1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, capability.NewEmptyRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.ResolveAgentInstructionSnapshot(execution)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("resume changed: %+v %v", again, err)
	}
	next := mcpExecutionContext(t, reopened, ctx)
	latest, err := reopened.ResolveAgentInstructionSnapshot(next)
	if err != nil || len(latest.Documents) != 0 {
		t.Fatalf("revoked rule applied to new execution: %+v %v", latest, err)
	}
	if _, err := reopened.db.Exec(`UPDATE agent_instruction_versions SET content='tampered' WHERE scope='user' AND version=1`); err != nil {
		t.Fatal(err)
	}
	_, err = reopened.ResolveAgentInstructionSnapshot(execution)
	if err == nil {
		t.Fatal("corrupt pinned document accepted")
	}
	if _, err := reopened.db.Exec(`UPDATE agent_instruction_snapshots SET content_hash='' WHERE activity_key=?`, latest.ActivityKey); err != nil {
		t.Fatal(err)
	}
	_, err = reopened.ResolveAgentInstructionSnapshot(next)
	assertDomainCode(t, err, "AGENT_INSTRUCTIONS_INVALID")
}

func TestAgentInstructionsSnapshotExecutionIdentitiesAndLegacyResume(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "rules.db"))
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
				claim, err := store.ClaimAgentTask(context.Background(), ClaimAgentTaskCommand{WorkerID: "rules", ProviderID: "sdk", ModelID: "test", LeaseSeconds: 60})
				if err != nil || claim == nil {
					t.Fatalf("claim: %+v %v", claim, err)
				}
				activity = AgentActivityIdentity{ProjectID: task.ProjectID, AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken}
			case "stateful":
				project, claim := statefulToolClaimForTest(t, store)
				activity = AgentActivityIdentity{ProjectID: project.ProjectID, ExecutionAttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken}
			}
			if _, err := store.UpdateAgentInstructions(ctx, instructionUpdate("user", "", "private user rule", "save", 0)); err != nil {
				t.Fatal(err)
			}
			service := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
			execution := WithAgentActivity(service, activity)
			first, err := store.ResolveAgentInstructionSnapshot(execution)
			if err != nil || len(first.Documents) != 1 || first.UserID != identity.DefaultUserID {
				t.Fatalf("identity: %+v %v", first, err)
			}
			invalid := activity
			invalid.ProjectID = "foreign"
			if _, err := store.ResolveAgentInstructionSnapshot(WithAgentActivity(service, invalid)); err == nil {
				t.Fatal("foreign project accepted")
			}
			invalid = activity
			invalid.AllowTerminal = true
			_, err = store.ResolveAgentInstructionSnapshot(WithAgentActivity(service, invalid))
			assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
			if mode != "conversation" {
				invalid = activity
				invalid.AttemptToken = "wrong"
				_, err = store.ResolveAgentInstructionSnapshot(WithAgentActivity(service, invalid))
				assertDomainCode(t, err, "ATTEMPT_TOKEN_INVALID")
			} else {
				legacy := mcpExecutionContext(t, store, ctx)
				legacyActivity, _ := AgentActivityFromContext(legacy)
				if _, err := store.db.Exec(`INSERT INTO agent_turn_run_states VALUES(?, 'legacy', '{}', 'hash', '[]', 1, '', '')`, legacyActivity.AgentTurnID); err != nil {
					t.Fatal(err)
				}
				snapshot, err := store.ResolveAgentInstructionSnapshot(legacy)
				if err != nil || len(snapshot.Documents) != 0 {
					t.Fatalf("new rules applied to legacy checkpoint: %+v %v", snapshot, err)
				}
			}
		})
	}
}

func TestAgentInstructionsQuotaAndProjectDeletePreview(t *testing.T) {
	store, _, _ := mcpConnectionStore(t)
	ctx := mcpOwnerContext()
	project, err := store.CreateProject(ctx, "Delete instructions")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAgentInstructions(ctx, instructionUpdate("project", project.ProjectID, "old", "first", 0)); err != nil {
		t.Fatal(err)
	}
	preview, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
	if err != nil || preview.Impact.InstructionVersionCount != 1 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if _, err := store.UpdateAgentInstructions(ctx, instructionUpdate("project", project.ProjectID, "new", "second", 1)); err != nil {
		t.Fatal(err)
	}
	updated, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
	if err != nil || updated.SnapshotHash == preview.SnapshotHash || updated.Impact.InstructionVersionCount != 2 {
		t.Fatalf("preview not invalidated: %+v %v", updated, err)
	}
	if _, err := store.db.Exec(`UPDATE workspace_quotas SET max_storage_bytes=6 WHERE workspace_id=?`, identity.DefaultWorkspaceID); err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateAgentInstructions(ctx, instructionUpdate("user", "", "x", "quota", 0))
	assertDomainCode(t, err, "WORKSPACE_QUOTA_EXCEEDED")
	view, err := store.GetAgentInstructions(ctx, project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "quota") {
		t.Fatal("failed write persisted")
	}
}

func TestAgentInstructionsMigration49AndDeletedWorkspace(t *testing.T) {
	store, path, _ := mcpConnectionStore(t)
	ctx := mcpOwnerContext()
	execution := mcpExecutionContext(t, store, ctx)
	activity, _ := AgentActivityFromContext(execution)
	if _, err := store.db.Exec(`INSERT INTO agent_turn_run_states VALUES(?, 'legacy', '{}', 'hash', '[]', 1, '', '')`, activity.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE agent_instruction_proposals; DROP TABLE agent_instruction_snapshots; DROP TABLE agent_instruction_versions; PRAGMA user_version=49;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, capability.NewEmptyRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var backup string
	if err := reopened.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=49 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"workspace", "user", "project"} {
		if _, err := reopened.UpdateAgentInstructions(ctx, instructionUpdate(scope, activity.ProjectID, scope+" new", "save", 0)); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := reopened.ResolveAgentInstructionSnapshot(execution)
	if err != nil || len(snapshot.Documents) != 0 {
		t.Fatalf("migration changed legacy execution: %+v %v", snapshot, err)
	}
	if _, err := reopened.DeleteWorkspace(ctx, DeleteWorkspaceCommand{WorkspaceID: identity.DefaultWorkspaceID, UserID: identity.DefaultUserID, Confirmation: identity.DefaultWorkspaceID}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"agent_instruction_versions", "agent_instruction_snapshots", "agent_instruction_proposals"} {
		var count int
		if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("retained %s: %d %v", table, count, err)
		}
	}
}
