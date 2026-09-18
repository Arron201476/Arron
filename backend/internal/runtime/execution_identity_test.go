package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func TestAgentActivityRestoresUserWithoutReplacingServiceIdentity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "identity.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	user := identity.Principal{Kind: identity.KindUser, UserID: "editor", WorkspaceID: "workspace", Role: identity.RoleEditor}
	if err := store.BootstrapPrincipal(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithPrincipal(context.Background(), user)
	project, err := store.CreateProject(ctx, "Scoped execution")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Inspect"}, CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	activity := AgentActivityIdentity{ProjectID: project.ProjectID, AgentTurnID: turn.AgentTurnID}
	_, err = store.ResolveAgentActivityPrincipal(ctx, activity)
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveAgentActivityPrincipal(ctx, activity)
	if err != nil || resolved.UserID != user.UserID || resolved.WorkspaceID != user.WorkspaceID || resolved.Role != identity.RoleEditor {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	service := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	delegated := identity.WithDelegatedUser(service, resolved)
	transport, _ := identity.FromContext(delegated)
	if transport.Kind != identity.KindService || identity.UserIDFromContext(delegated) != user.UserID || identity.WorkspaceIDFromContext(delegated) != user.WorkspaceID {
		t.Fatalf("delegation lost service/user distinction: %+v", transport)
	}
	other := activity
	other.ProjectID = "another-project"
	_, err = store.ResolveAgentActivityPrincipal(ctx, other)
	assertDomainCode(t, err, "AGENT_ACTIVITY_SCOPE_MISMATCH")
	if _, err := store.BeginAgentTurnCommit(ctx, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	_, err = store.ResolveAgentActivityPrincipal(ctx, activity)
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	activity.AllowTerminal = true
	if _, err := store.ResolveAgentActivityPrincipal(ctx, activity); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET role = 'viewer' WHERE user_id = ?`, user.UserID); err != nil {
		t.Fatal(err)
	}
	resolved, err = store.ResolveAgentActivityPrincipal(ctx, activity)
	if err != nil || resolved.Role != identity.RoleViewer {
		t.Fatalf("membership change not reflected: %+v %v", resolved, err)
	}
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET status = 'disabled' WHERE user_id = ?`, user.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveAgentActivityPrincipal(ctx, activity); err == nil {
		t.Fatal("disabled membership accepted")
	}
}

func TestBackgroundActivityValidatesDurableOwnerTokenAndLease(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "background.db"), loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := createBackgroundTaskForTest(t, store)
	ctx := context.Background()
	claim, err := store.ClaimAgentTask(ctx, ClaimAgentTaskCommand{WorkerID: "worker", ProviderID: "sdk", ModelID: "test", LeaseSeconds: 60})
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	activity := AgentActivityIdentity{ProjectID: task.ProjectID, AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken}
	principal, err := store.ResolveAgentActivityPrincipal(ctx, activity)
	if err != nil || principal.UserID != identity.DefaultUserID {
		t.Fatalf("owner=%+v err=%v", principal, err)
	}
	activity.AttemptToken = "invalid"
	_, err = store.ResolveAgentActivityPrincipal(ctx, activity)
	assertDomainCode(t, err, "ATTEMPT_TOKEN_INVALID")
	activity.AttemptToken = claim.AttemptToken
	store.now = func() time.Time { return time.Now().UTC().Add(time.Hour) }
	_, err = store.ResolveAgentActivityPrincipal(ctx, activity)
	assertDomainCode(t, err, "ATTEMPT_LEASE_EXPIRED")
	activity.AllowTerminal = true
	if _, err := store.ResolveAgentActivityPrincipal(ctx, activity); err != nil {
		t.Fatal(err)
	}
	activity.AttemptToken = "invalid"
	if _, err := store.ResolveAgentActivityPrincipal(ctx, activity); err == nil {
		t.Fatal("cleanup bypassed attempt token")
	}
}

func TestExecutionOwnerMigrationPreservesStoredCreator(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "migration.db"), loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := createBackgroundTaskForTest(t, store)
	if _, err := store.db.Exec(`UPDATE skill_invocations SET user_id = ''`); err != nil {
		t.Fatal(err)
	}
	if err := migrateExecutionOwners(store.db); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := store.db.QueryRow(`SELECT user_id FROM skill_invocations WHERE skill_invocation_id = ?`, task.SkillInvocationID).Scan(&owner); err != nil || owner != identity.DefaultUserID {
		t.Fatalf("legacy owner=%q err=%v", owner, err)
	}
	if _, err := store.db.Exec(`UPDATE skill_invocations SET user_id = 'stored-creator'`); err != nil {
		t.Fatal(err)
	}
	if err := migrateExecutionOwners(store.db); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT user_id FROM skill_invocations WHERE skill_invocation_id = ?`, task.SkillInvocationID).Scan(&owner); err != nil || owner != "stored-creator" {
		t.Fatalf("creator overwritten=%q err=%v", owner, err)
	}
}
