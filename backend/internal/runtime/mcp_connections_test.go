package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func mcpConnectionStore(t *testing.T) (*Store, string, *agenttool.Registry) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.db")
	store, err := Open(path, capability.NewEmptyRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	tools, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion, MCPServers: []agenttool.MCPServerConfig{{
		ID: "service", Description: "Configured MCP", Transport: "streamable_http", URL: "https://example.com/mcp", Enabled: true,
		CredentialHeaders: map[string]string{"Authorization": "api-token"},
		AllowedTools:      []agenttool.MCPToolConfig{{Name: "lookup", Description: "Lookup", Access: agenttool.AccessRead}, {Name: "save", Description: "Save", Access: agenttool.AccessWrite}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(tools)
	return store, path, tools
}

func mcpConnectionKey(t *testing.T, store *Store) {
	t.Helper()
	if err := store.ConfigureMCPCredentials(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x37}, 32))); err != nil {
		t.Fatal(err)
	}
}

func mcpOwnerContext() context.Context {
	return identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
}

func mcpUpdate(scope, value, requestID string, version int) UpdateMCPConnectionCommand {
	return UpdateMCPConnectionCommand{ServerID: "service", Scope: scope, ExpectedVersion: version, RequestID: requestID, Values: map[string]string{"api-token": value}}
}

func mcpBinding(t *testing.T, store *Store, ctx context.Context) string {
	t.Helper()
	catalog, err := store.AgentToolCatalog(ctx, true)
	if err != nil || len(catalog.MCPServers) != 1 {
		t.Fatalf("catalog: %+v %v", catalog, err)
	}
	return catalog.MCPServers[0].CredentialBinding
}

func mcpExecutionContext(t *testing.T, store *Store, ctx context.Context) context.Context {
	t.Helper()
	project, err := store.CreateProject(ctx, "Isolated MCP connection")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Inspect connection"}, CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	principal, _ := identity.UserFromContext(ctx)
	service := identity.WithDelegatedUser(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), principal)
	return WithAgentActivity(service, AgentActivityIdentity{ProjectID: project.ProjectID, AgentTurnID: turn.AgentTurnID})
}

func TestMCPConnectionsEncryptedScopedCASReopenAndTombstones(t *testing.T) {
	store, path, tools := mcpConnectionStore(t)
	ctx := mcpOwnerContext()
	command := mcpUpdate("workspace", "Bearer shared-secret-726", "shared-create", 0)
	_, err := store.UpdateMCPConnection(ctx, command)
	assertDomainCode(t, err, "MCP_CREDENTIAL_STORAGE_UNAVAILABLE")
	missing, err := store.AgentToolCatalog(ctx, true)
	if err != nil || missing.MCPServers[0].Enabled {
		t.Fatal("credential-less MCP must not block ordinary chat", err)
	}
	mcpConnectionKey(t, store)
	shared, err := store.UpdateMCPConnection(ctx, command)
	if err != nil || shared.Version != 1 || shared.Status != "active" {
		t.Fatalf("save: %+v %v", shared, err)
	}
	again, err := store.UpdateMCPConnection(ctx, command)
	if err != nil || again != shared {
		t.Fatalf("retry: %+v %v", again, err)
	}
	conflict := command
	conflict.Values = map[string]string{"api-token": "changed"}
	_, err = store.UpdateMCPConnection(ctx, conflict)
	assertDomainCode(t, err, "IDEMPOTENCY_CONFLICT")
	conflict.RequestID = "stale-version"
	_, err = store.UpdateMCPConnection(ctx, conflict)
	assertDomainCode(t, err, "MCP_CREDENTIAL_CONFLICT")
	initialBinding := mcpBinding(t, store, ctx)
	personal := mcpUpdate("user", "Bearer personal-secret-493", "personal-create", 0)
	if _, err := store.UpdateMCPConnection(ctx, personal); err != nil {
		t.Fatal(err)
	}
	personalBinding := mcpBinding(t, store, ctx)
	if initialBinding == personalBinding {
		t.Fatal("credential change did not bind approval hash")
	}
	inventory, err := store.ListMCPConnections(ctx)
	if err != nil || inventory.Items[0].EffectiveScope != "user" || !inventory.CanManageWorkspace {
		t.Fatalf("inventory: %+v %v", inventory, err)
	}
	encoded, _ := json.Marshal(inventory)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "Authorization") || strings.Contains(string(encoded), "example.com") {
		t.Fatal("inventory exposed secret or host configuration")
	}
	var encrypted []byte
	if err := store.db.QueryRow(`SELECT ciphertext FROM mcp_connections WHERE owner_user_id = ?`, identity.DefaultUserID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("personal-secret")) || len(encrypted) < 28 {
		t.Fatal("plaintext credential persisted")
	}
	execution := mcpExecutionContext(t, store, ctx)
	resolve := ResolveMCPCredentialsCommand{ServerID: "service", CredentialBinding: personalBinding}
	result, err := store.ResolveMCPCredentials(execution, resolve)
	if err != nil || result.Values["api-token"] != personal.Values["api-token"] {
		t.Fatal("personal credential not consumed", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, capability.NewEmptyRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.SetAgentToolRegistry(tools)
	mcpConnectionKey(t, reopened)
	result, err = reopened.ResolveMCPCredentials(execution, resolve)
	if err != nil || result.Values["api-token"] != personal.Values["api-token"] {
		t.Fatal("reopen lost encrypted credential", err)
	}
	deleted, err := reopened.UpdateMCPConnection(ctx, UpdateMCPConnectionCommand{ServerID: "service", Scope: "user", ExpectedVersion: 1, RequestID: "revoke", Delete: true})
	if err != nil || deleted.Status != "deleted" || deleted.Version != 2 {
		t.Fatal("revoke failed", err)
	}
	var remains int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM mcp_connections WHERE owner_user_id = ? AND ciphertext IS NOT NULL`, identity.DefaultUserID).Scan(&remains); err != nil || remains != 0 {
		t.Fatal("revoke retained ciphertext", err)
	}
	_, err = reopened.ResolveMCPCredentials(execution, resolve)
	assertDomainCode(t, err, "AGENT_TOOL_CONFIGURATION_CHANGED")
	resolve.CredentialBinding = mcpBinding(t, reopened, ctx)
	if resolve.CredentialBinding == initialBinding {
		t.Fatal("revoke resurrected old binding")
	}
	result, err = reopened.ResolveMCPCredentials(execution, resolve)
	if err != nil || result.Values["api-token"] != command.Values["api-token"] {
		t.Fatal("workspace fallback failed", err)
	}
}

func TestMCPConnectionsTenantRoleActivityAndCiphertextBoundaries(t *testing.T) {
	store, _, _ := mcpConnectionStore(t)
	mcpConnectionKey(t, store)
	owner := identity.DefaultLocalPrincipal()
	ctx := mcpOwnerContext()
	if _, err := store.UpdateMCPConnection(ctx, mcpUpdate("workspace", "Bearer workspace-A", "workspace", 0)); err != nil {
		t.Fatal(err)
	}
	for _, user := range []identity.Principal{
		{Kind: identity.KindUser, WorkspaceID: owner.WorkspaceID, UserID: "other-editor", Role: identity.RoleEditor},
		{Kind: identity.KindUser, WorkspaceID: "workspace-B", UserID: "foreign-owner", Role: identity.RoleOwner},
	} {
		if err := store.BootstrapPrincipal(ctx, user); err != nil {
			t.Fatal(err)
		}
		other := identity.WithPrincipal(context.Background(), user)
		if _, err := store.UpdateMCPConnection(other, mcpUpdate("user", "Bearer "+user.UserID, "personal", 0)); err != nil {
			t.Fatal(err)
		}
		execution := mcpExecutionContext(t, store, other)
		result, err := store.ResolveMCPCredentials(execution, ResolveMCPCredentialsCommand{ServerID: "service", CredentialBinding: mcpBinding(t, store, other)})
		if err != nil || result.Values["api-token"] != "Bearer "+user.UserID {
			t.Fatal("cross-user credential consumption", err)
		}
		_, err = store.ResolveMCPCredentials(execution, ResolveMCPCredentialsCommand{ServerID: "service", CredentialBinding: mcpBinding(t, store, ctx)})
		assertDomainCode(t, err, "AGENT_TOOL_CONFIGURATION_CHANGED")
		if user.Role == identity.RoleEditor {
			forged := user
			forged.Role = identity.RoleOwner
			_, err = store.UpdateMCPConnection(identity.WithPrincipal(ctx, forged), mcpUpdate("workspace", "forged", "forge", 1))
			assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
		}
		if _, err := store.db.Exec(`UPDATE workspace_memberships SET role = 'viewer' WHERE user_id = ?`, user.UserID); err != nil {
			t.Fatal(err)
		}
		_, err = store.UpdateMCPConnection(other, mcpUpdate("user", "changed", "viewer-write", 1))
		assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
		_, err = store.ResolveMCPCredentials(execution, ResolveMCPCredentialsCommand{ServerID: "service", CredentialBinding: mcpBinding(t, store, other)})
		assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
	}
	command := ResolveMCPCredentialsCommand{ServerID: "service", CredentialBinding: mcpBinding(t, store, ctx)}
	for _, invalid := range []context.Context{context.Background(), ctx, identity.WithPrincipal(ctx, identity.ServicePrincipal())} {
		_, err := store.ResolveMCPCredentials(invalid, command)
		assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
	}
	execution := mcpExecutionContext(t, store, ctx)
	activity, _ := AgentActivityFromContext(execution)
	activity.AllowTerminal = true
	_, err := store.ResolveMCPCredentials(WithAgentActivity(execution, activity), command)
	assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
	if _, err := store.BeginAgentTurnCommit(ctx, activity.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	_, err = store.ResolveMCPCredentials(execution, command)
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	personalRecord, err := mcpConnectionQuery(ctx, store.db, owner.WorkspaceID, "other-editor", "service")
	if err != nil {
		t.Fatal(err)
	}
	personalRecord.OwnerUserID = "forged-owner"
	_, err = store.decryptMCPConnection(personalRecord)
	assertDomainCode(t, err, "MCP_CREDENTIAL_UNAVAILABLE")
}

func TestMCPConnectionsInvalidateOldApprovalAndValidateInputs(t *testing.T) {
	store, _, _ := mcpConnectionStore(t)
	mcpConnectionKey(t, store)
	ctx := mcpOwnerContext()
	for index, command := range []UpdateMCPConnectionCommand{
		mcpUpdate("project", "value", "bad-scope", 0), mcpUpdate("user", "value\r\nInjected: yes", "bad-value", 0),
		mcpUpdate("user", strings.Repeat("a", 4097), "oversize", 0), mcpUpdate("user", "", "empty", 0),
		{Scope: "user", ServerID: "service", RequestID: "extra-field", Values: map[string]string{"api-token": "value", "PATH": "override"}},
	} {
		_, err := store.UpdateMCPConnection(ctx, command)
		if err == nil {
			t.Fatalf("invalid input %d accepted", index)
		}
	}
	if _, err := store.UpdateMCPConnection(ctx, mcpUpdate("user", "original", "create", 0)); err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, "Credential-bound approval")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := store.AgentToolCatalog(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	for _, tool := range catalog.Tools {
		if tool.ID == "mcp:service/save" {
			hash = tool.ConfigurationHash
		}
	}
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: "credential-write", ToolID: "mcp:service/save", ConfigurationHash: hash, Arguments: json.RawMessage(`{}`)})
	if err != nil || call.Approval == nil {
		t.Fatal("approval not created", err)
	}
	if _, err := store.ResolveAgentToolApproval(ctx, ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID,
		ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve", ActorRef: identity.DefaultUserID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateMCPConnection(ctx, mcpUpdate("user", "rotated", "rotate", 1)); err != nil {
		t.Fatal(err)
	}
	_, err = store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: hash})
	assertDomainCode(t, err, "AGENT_TOOL_CONFIGURATION_CHANGED")
}

func TestMCPConnectionMigrationFrom48PreservesLegacyReferences(t *testing.T) {
	store, path, tools := mcpConnectionStore(t)
	ctx := mcpOwnerContext()
	_, err := store.PutWorkspaceMCPCredentialCommand(ctx, PutWorkspaceMCPCredentialCommand{
		WorkspaceID: identity.DefaultWorkspaceID, UserID: identity.DefaultUserID, ServerID: "service",
		CredentialName: "legacy", SecretRef: "env://LEGACY_FIXTURE_REF",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE mcp_connections; ALTER TABLE agent_tool_config_snapshots DROP COLUMN credential_user_id; PRAGMA user_version=48;`); err != nil {
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
	reopened.SetAgentToolRegistry(tools)
	var backup string
	if err := reopened.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=48 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	refs, err := reopened.ListWorkspaceMCPCredentials(ctx, identity.DefaultWorkspaceID)
	if err != nil || len(refs) != 1 || refs[0].SecretRef != "env://LEGACY_FIXTURE_REF" {
		t.Fatal("migration changed legacy inventory", err)
	}
	items, err := reopened.ListMCPConnections(ctx)
	if err != nil || items.StorageAvailable || items.Items[0].EffectiveScope != "none" {
		t.Fatal("legacy reference was activated as a credential", err)
	}
}

func TestMCPConnectionWorkspaceDeletionClearsCiphertextAndKeyValidation(t *testing.T) {
	store, _, _ := mcpConnectionStore(t)
	for _, invalid := range []string{"not-base64", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if err := store.ConfigureMCPCredentials(invalid); err == nil {
			t.Fatal("invalid key accepted")
		}
	}
	mcpConnectionKey(t, store)
	ctx := mcpOwnerContext()
	for _, scope := range []string{"workspace", "user"} {
		command := mcpUpdate(scope, "fixture-only-value", scope+"-create", 0)
		if _, err := store.UpdateMCPConnection(ctx, command); err != nil {
			t.Fatal(err)
		}
		command.ExpectedVersion = 1
		_, err := store.UpdateMCPConnection(ctx, command)
		assertDomainCode(t, err, "IDEMPOTENCY_CONFLICT")
	}
	result, err := store.DeleteWorkspace(ctx, DeleteWorkspaceCommand{WorkspaceID: identity.DefaultWorkspaceID, UserID: identity.DefaultUserID, Confirmation: identity.DefaultWorkspaceID})
	if err != nil || result.MCPCredentialCount != 2 {
		t.Fatal("workspace deletion omitted connections", err)
	}
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM mcp_connections WHERE ciphertext IS NOT NULL OR status <> 'deleted'`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("deleted workspace retained active credential", err)
	}
}
