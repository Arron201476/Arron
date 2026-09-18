package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func TestWorkspaceQuotasAndAgentTurnIdentity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := identity.Principal{
		Kind: identity.KindUser, UserID: "quota_user", DisplayName: "Quota User",
		WorkspaceID: "quota_workspace", WorkspaceName: "Quota Workspace", Role: identity.RoleOwner,
	}
	if err := store.BootstrapPrincipal(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithPrincipal(context.Background(), principal)
	if _, err := store.db.Exec(`
		UPDATE workspace_quotas
		SET max_projects = 1, max_storage_bytes = 10,
		    max_active_agent_turns = 1, max_installed_skills = 0
		WHERE workspace_id = ?`, principal.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, "Quota Project")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateProject(ctx, "Over Quota")
	assertDomainCode(t, err, "WORKSPACE_QUOTA_EXCEEDED")
	_, err = store.CreateUploadSession(ctx, project.ProjectID, []UploadItemSpec{{
		ClientItemKey: "too-large", Kind: "text", OriginalFilename: "large.txt",
		DeclaredMIMEType: "text/plain", DeclaredSizeBytes: 11,
	}})
	assertDomainCode(t, err, "WORKSPACE_QUOTA_EXCEEDED")

	skillSource := writeSkillTestPackage(
		t, filepath.Join(t.TempDir(), "source"), "quota-skill", "quota_skill",
		"1.0.0", "inline", "Quota fixture.",
	)
	_, err = store.InstallSkillDirectory(ctx, skillSource, "")
	assertDomainCode(t, err, "WORKSPACE_QUOTA_EXCEEDED")

	turn, err := store.AcceptAgentTurn(
		ctx, project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "第一轮"}, CommandMeta{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if turn.UserID != principal.UserID || turn.WorkspaceID != principal.WorkspaceID {
		t.Fatalf("turn identity = %+v", turn)
	}
	_, err = store.AcceptAgentTurn(
		ctx, project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "第二轮"}, CommandMeta{},
	)
	assertDomainCode(t, err, "WORKSPACE_QUOTA_EXCEEDED")
}

func TestWorkspaceMCPCredentialsAreTenantScopedAndReferenceOnly(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principalA := identity.Principal{
		Kind: identity.KindUser, UserID: "mcp_user_a", DisplayName: "A",
		WorkspaceID: "mcp_workspace_a", WorkspaceName: "A", Role: identity.RoleAdmin,
	}
	principalB := identity.Principal{
		Kind: identity.KindUser, UserID: "mcp_user_b", DisplayName: "B",
		WorkspaceID: "mcp_workspace_b", WorkspaceName: "B", Role: identity.RoleAdmin,
	}
	for _, principal := range []identity.Principal{principalA, principalB} {
		if err := store.BootstrapPrincipal(context.Background(), principal); err != nil {
			t.Fatal(err)
		}
	}
	_, err = store.PutWorkspaceMCPCredential(
		context.Background(), principalA.WorkspaceID, principalA.UserID,
		"research", "default", "literal-secret",
	)
	assertDomainCode(t, err, "MCP_SECRET_REF_REQUIRED")
	credential, err := store.PutWorkspaceMCPCredential(
		context.Background(), principalA.WorkspaceID, principalA.UserID,
		"research", "default", "env://MCP_RESEARCH_TOKEN",
	)
	if err != nil {
		t.Fatal(err)
	}
	itemsA, err := store.ListWorkspaceMCPCredentials(context.Background(), principalA.WorkspaceID)
	if err != nil || len(itemsA) != 1 {
		t.Fatalf("workspace A credentials = %+v, err=%v", itemsA, err)
	}
	itemsB, err := store.ListWorkspaceMCPCredentials(context.Background(), principalB.WorkspaceID)
	if err != nil || len(itemsB) != 0 {
		t.Fatalf("workspace B credentials = %+v, err=%v", itemsB, err)
	}
	err = store.DeleteWorkspaceMCPCredential(
		context.Background(), principalB.WorkspaceID, credential.CredentialID,
	)
	assertDomainCode(t, err, "MCP_CREDENTIAL_NOT_FOUND")
}

func TestSecurityAuditRetentionUsesWorkspacePolicy(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := identity.Principal{
		Kind: identity.KindUser, UserID: "audit_user", DisplayName: "Audit User",
		WorkspaceID: "audit_workspace", WorkspaceName: "Audit Workspace", Role: identity.RoleOwner,
	}
	if err := store.BootstrapPrincipal(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE workspace_quotas SET audit_retention_days = 1 WHERE workspace_id = ?`, principal.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSecurityAudit(context.Background(), SecurityAuditEvent{
		WorkspaceID: principal.WorkspaceID, UserID: principal.UserID,
		PrincipalKind: identity.KindUser, Action: "GET /api/v1/projects", Outcome: "succeeded", StatusCode: 200,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO security_audit_events(
			audit_event_id, workspace_id, user_id, principal_kind, action,
			outcome, status_code, request_id, created_at
		) VALUES('audit_old', ?, ?, 'user', 'old', 'succeeded', 200, 'request_old', ?)`,
		principal.WorkspaceID, principal.UserID, formatTime(time.Now().UTC().Add(-48*time.Hour)),
	); err != nil {
		t.Fatal(err)
	}
	pruned, err := store.PruneSecurityAuditEvents(context.Background())
	if err != nil || pruned != 1 {
		t.Fatalf("pruned = %d, err=%v", pruned, err)
	}
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM security_audit_events WHERE workspace_id = ?`, principal.WorkspaceID).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("remaining audit events = %d, err=%v", remaining, err)
	}
}

func TestWorkspaceDeletionPropagatesAndSchedulesSourceCleanup(t *testing.T) {
	root := t.TempDir()
	store, err := Open(filepath.Join(root, "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := identity.Principal{
		Kind: identity.KindUser, UserID: "delete_owner", DisplayName: "Delete Owner",
		WorkspaceID: "delete_workspace", WorkspaceName: "Delete Workspace", Role: identity.RoleOwner,
	}
	if err := store.BootstrapPrincipal(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithPrincipal(context.Background(), principal)
	project, err := store.CreateProject(ctx, "Delete Project")
	if err != nil {
		t.Fatal(err)
	}
	asset := createTextAsset(t, store, project.ProjectID, "source.txt", "workspace source")
	skillSource := writeSkillTestPackage(
		t, filepath.Join(root, "source"), "delete-skill", "delete_skill",
		"1.0.0", "inline", "Delete fixture.",
	)
	if _, err := store.InstallSkillDirectory(ctx, skillSource, principal.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutWorkspaceMCPCredential(
		context.Background(), principal.WorkspaceID, principal.UserID,
		"research", "default", "vault://workspaces/delete/research",
	); err != nil {
		t.Fatal(err)
	}
	_, err = store.DeleteWorkspace(ctx, DeleteWorkspaceCommand{
		WorkspaceID: principal.WorkspaceID, UserID: principal.UserID, Confirmation: "wrong",
	})
	assertDomainCode(t, err, "REQUIRED_CONFIRMATION_MISSING")
	result, err := store.DeleteWorkspace(ctx, DeleteWorkspaceCommand{
		WorkspaceID: principal.WorkspaceID, UserID: principal.UserID,
		Confirmation: principal.WorkspaceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectCount != 1 || result.AssetCount != 1 || result.SkillCount != 1 ||
		result.MCPCredentialCount != 1 || result.SourceDeletionJobCount != 1 {
		t.Fatalf("delete result = %+v", result)
	}
	if _, err := store.ResolvePrincipal(context.Background(), principal); err == nil {
		t.Fatal("deleted workspace principal still resolves")
	}
	var workspaceStatus, projectStatus, assetStatus string
	if err := store.db.QueryRow(`SELECT status FROM workspaces WHERE workspace_id = ?`, principal.WorkspaceID).Scan(&workspaceStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT status FROM projects WHERE project_id = ?`, project.ProjectID).Scan(&projectStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT status FROM assets WHERE asset_id = ?`, asset.Asset.AssetID).Scan(&assetStatus); err != nil {
		t.Fatal(err)
	}
	if workspaceStatus != "deleted" || projectStatus != "deleted" || assetStatus != "deleted" {
		t.Fatalf("delete statuses = workspace:%s project:%s asset:%s", workspaceStatus, projectStatus, assetStatus)
	}
	jobs, err := store.ListRetentionJobs(context.Background(), asset.Asset.AssetID)
	if err != nil || len(jobs) == 0 || jobs[0].Status != "scheduled" {
		t.Fatalf("retention jobs = %+v, err=%v", jobs, err)
	}
	if _, err := os.Stat(store.managedSkillActiveRoot(principal.WorkspaceID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace active Skill directory still exists: %v", err)
	}
}
