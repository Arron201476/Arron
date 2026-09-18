package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func TestWorkspaceToolConfigurationPersistsAndSeparatesAuthorization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tools.db")
	store, err := Open(path, capability.NewEmptyRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	tools := agentToolRegistryForTest(t)
	store.SetAgentToolRegistry(tools)
	owner := identity.DefaultLocalPrincipal()
	ctx := identity.WithPrincipal(context.Background(), owner)
	other := owner
	other.WorkspaceID, other.UserID = "workspace_other", "user_other"
	if err := store.BootstrapPrincipal(ctx, other); err != nil {
		t.Fatal(err)
	}
	otherCtx := identity.WithPrincipal(context.Background(), other)
	current, err := store.GetAgentToolConfiguration(ctx)
	if err != nil || current.Version != 0 {
		t.Fatalf("initial: %+v, %v", current, err)
	}
	updated, err := store.UpdateAgentToolConfiguration(ctx, UpdateAgentToolConfigurationCommand{Enabled: map[string]bool{"mcp:fixture": false, "hosted:native-web-search": true}})
	if err != nil || updated.Version != 1 {
		t.Fatalf("update: %+v, %v", updated, err)
	}
	getDescriptor := func(ctx context.Context, id string) agenttool.Descriptor {
		t.Helper()
		catalog, err := store.AgentToolCatalog(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, descriptor := range catalog.Tools {
			if descriptor.ID == id {
				return descriptor
			}
		}
		t.Fatalf("missing descriptor %s", id)
		return agenttool.Descriptor{}
	}
	if getDescriptor(ctx, "mcp:fixture/read_fact").Enabled || !getDescriptor(otherCtx, "mcp:fixture/read_fact").Enabled {
		t.Fatal("MCP configuration crossed workspace")
	}
	if !getDescriptor(ctx, "hosted:native-web-search").Enabled || getDescriptor(otherCtx, "hosted:native-web-search").Enabled {
		t.Fatal("Hosted configuration crossed workspace")
	}
	_, err = store.UpdateAgentToolConfiguration(ctx, UpdateAgentToolConfigurationCommand{Enabled: map[string]bool{"mcp:fixture": true}})
	assertDomainCode(t, err, "AGENT_TOOL_CONFIGURATION_CONFLICT")
	for _, enabled := range []map[string]bool{{"mcp:unknown": true}, {"runtime:commit_agent_action": false}, {"hosted:native-file-search": true}} {
		_, err := store.UpdateAgentToolConfiguration(ctx, UpdateAgentToolConfigurationCommand{ExpectedVersion: 1, Enabled: enabled})
		assertDomainCode(t, err, "AGENT_TOOL_CONFIGURATION_INVALID")
	}
	editor := owner
	editor.UserID, editor.Role = "user_editor", identity.RoleEditor
	if err := store.BootstrapPrincipal(ctx, editor); err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateAgentToolConfiguration(identity.WithPrincipal(ctx, editor), UpdateAgentToolConfigurationCommand{ExpectedVersion: 1, Enabled: map[string]bool{"mcp:fixture": true}})
	assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
	forged := editor
	forged.Role = identity.RoleOwner
	_, err = store.UpdateAgentToolConfiguration(identity.WithPrincipal(ctx, forged), UpdateAgentToolConfigurationCommand{ExpectedVersion: 1, Enabled: map[string]bool{"mcp:fixture": true}})
	assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, capability.NewEmptyRegistry())
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(tools)
	if getDescriptor(ctx, "mcp:fixture/read_fact").Enabled || !getDescriptor(ctx, "hosted:native-web-search").Enabled {
		t.Fatal("restart lost workspace settings")
	}
}

func TestWorkspaceToolSettingsReevaluateSkillDependencies(t *testing.T) {
	root := t.TempDir()
	directory := writeSkillTestPackage(t, root, "tool-dependent", "tool_dependent", "1.0.0", "inline", "Use the approved lookup tool.")
	if err := os.MkdirAll(filepath.Join(directory, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "agents", "openai.yaml"), []byte("dependencies:\n  tools:\n    - type: mcp\n      value: fixture/read_fact\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := loadTestRegistry(t)
	if err := registry.AddSkillRoot(capability.SkillRoot{Scope: capability.SkillScopeSystem, Path: root}); err != nil {
		t.Fatal(err)
	}
	config := agentToolRegistryForTest(t).PrivateCatalog()
	config.MCPServers[0].Enabled = false
	tools, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion, MCPServers: config.MCPServers})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.SetSkillDependencyResolver(tools); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(t.TempDir(), "dependencies.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(tools)
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	assertStatus := func(want capability.Availability) {
		t.Helper()
		selected, err := store.CapabilityRegistryForSelection(ctx, identity.DefaultWorkspaceID)
		if err != nil {
			t.Fatal(err)
		}
		entry, ok := selected.Get("tool_dependent")
		if !ok || entry.Status != want {
			t.Fatalf("dependency status: %+v want %s", entry, want)
		}
	}
	assertStatus(capability.Unavailable)
	if _, err := store.UpdateAgentToolConfiguration(ctx, UpdateAgentToolConfigurationCommand{Enabled: map[string]bool{"mcp:fixture": true}}); err != nil {
		t.Fatal(err)
	}
	assertStatus(capability.Available)
	project, err := store.CreateProject(ctx, "Pinned dependency scope")
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []string
	for _, managed := range []bool{false, true} {
		selected, err := store.CapabilityRegistryForSelection(ctx, identity.DefaultWorkspaceID)
		if err != nil {
			t.Fatal(err)
		}
		entry, _ := selected.Get("tool_dependent")
		if managed {
			if _, err := store.InstallDiscoveredSkill(ctx, entry.Skill.CapabilityID, entry.Skill.Version, entry.Skill.ContentHash, "fixture"); err != nil {
				t.Fatal(err)
			}
			selected, err = store.CapabilityRegistryForSelection(ctx, identity.DefaultWorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			entry, _ = selected.Get("tool_dependent")
		}
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, snapshot, err := store.pinExecutionSkillTx(ctx, tx, project.ProjectID, entry)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if snapshot == nil {
			t.Fatal("missing pinned execution snapshot")
		}
		snapshots = append(snapshots, *snapshot)
		bound, ok, err := store.capabilityEntryForExecutionSnapshotQuery(ctx, store.db, project.ProjectID, "tool_dependent", *snapshot)
		if err != nil || !ok || bound.Status != capability.Available {
			t.Fatalf("managed=%t pinned dependency: %+v err=%v", managed, bound, err)
		}
	}
	if _, err := store.UpdateAgentToolConfiguration(ctx, UpdateAgentToolConfigurationCommand{ExpectedVersion: 1, Enabled: map[string]bool{"mcp:fixture": false}}); err != nil {
		t.Fatal(err)
	}
	assertStatus(capability.Unavailable)
	for _, snapshot := range snapshots {
		bound, ok, err := store.capabilityEntryForExecutionSnapshotQuery(ctx, store.db, project.ProjectID, "tool_dependent", snapshot)
		if err != nil || !ok || bound.Status != capability.Unavailable {
			t.Fatalf("disabled pinned dependency: %+v err=%v", bound, err)
		}
	}
}

func TestToolApprovalBindsConfigurationAcrossRestartAndReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshots.db")
	store, err := Open(path, capability.NewEmptyRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	tools := agentToolRegistryForTest(t)
	store.SetAgentToolRegistry(tools)
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	project, err := store.CreateProject(ctx, "Configuration snapshot")
	if err != nil {
		t.Fatal(err)
	}
	command := BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, SDKToolCallID: "approved-service-call", ToolID: "mcp:fixture/save_fact", Arguments: json.RawMessage(`{"value":"original"}`)}
	_, err = store.BeginAgentToolCall(ctx, command)
	assertDomainCode(t, err, "AGENT_TOOL_CONFIGURATION_CHANGED")
	command.ConfigurationHash = toolConfigurationHashForTest(t, store, command.ToolID)
	call, err := store.BeginAgentToolCall(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ResolveAgentToolApproval(ctx, ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, capability.NewEmptyRegistry())
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(tools)
	start := StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: command.ConfigurationHash}
	// Reloading the exact operator definition keeps the confirmed identity.
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = store.validateAgentToolConfigurationTx(ctx, tx, call, start.ConfigurationHash)
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	private := tools.PrivateCatalog()
	private.MCPServers[0].URL = "http://127.0.0.1:9322/replacement"
	replacement, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion, MCPServers: private.MCPServers})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(replacement)
	_, err = store.StartAgentToolCall(ctx, start)
	assertDomainCode(t, err, "AGENT_TOOL_CONFIGURATION_CHANGED")
	command.ConfigurationHash = toolConfigurationHashForTest(t, store, command.ToolID)
	_, err = store.BeginAgentToolCall(ctx, command)
	assertDomainCode(t, err, "AGENT_TOOL_CONFIGURATION_CHANGED")
	store.SetAgentToolRegistry(tools)
	_, err = store.UpdateAgentToolConfiguration(ctx, UpdateAgentToolConfigurationCommand{Enabled: map[string]bool{"mcp:fixture": false}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.StartAgentToolCall(ctx, start)
	assertDomainCode(t, err, "AGENT_TOOL_CONFIGURATION_CHANGED")
	_, err = store.UpdateAgentToolConfiguration(ctx, UpdateAgentToolConfigurationCommand{ExpectedVersion: 1, Enabled: map[string]bool{"mcp:fixture": true}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.StartAgentToolCall(ctx, start)
	assertDomainCode(t, err, "AGENT_TOOL_CONFIGURATION_CHANGED")
	if _, err := store.db.Exec(`DELETE FROM agent_tool_config_snapshots WHERE agent_tool_call_id = ?`, call.AgentToolCallID); err != nil {
		t.Fatal(err)
	}
	_, err = store.StartAgentToolCall(ctx, start)
	assertDomainCode(t, err, "AGENT_TOOL_CONFIGURATION_CHANGED")
}

func TestWorkspaceToolConfigurationMigrationPreservesLegacyApproval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	store, err := Open(path, capability.NewEmptyRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	tools := agentToolRegistryForTest(t)
	store.SetAgentToolRegistry(tools)
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Legacy approval")
	if err != nil {
		t.Fatal(err)
	}
	hash := toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact")
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: "legacy-mcp-call", ToolID: "mcp:fixture/save_fact", ConfigurationHash: hash, Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE agent_tool_config_snapshots; DROP TABLE workspace_agent_tool_settings; PRAGMA user_version = 37;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, capability.NewEmptyRegistry())
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(tools)
	restored, err := store.GetAgentToolCall(ctx, call.AgentToolCallID)
	if err != nil || restored.Status != "pending_approval" || restored.Approval == nil || restored.Approval.SubjectSnapshotHash != call.Approval.SubjectSnapshotHash {
		t.Fatalf("migration changed legacy approval: %+v err=%v", restored, err)
	}
	_, err = store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: hash})
	assertDomainCode(t, err, "AGENT_TOOL_CONFIGURATION_CHANGED")
	var version, snapshots int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("schema=%d err=%v", version, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_tool_config_snapshots`).Scan(&snapshots); err != nil || snapshots != 0 {
		t.Fatal("migration silently rebound legacy tool calls")
	}
	rows, err := store.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		rows.Close()
		t.Fatal("migration broke foreign keys")
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "*", "migration.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("migration backup count=%d err=%v", len(backups), err)
	}
}
