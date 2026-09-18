package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/scriptsandbox"
)

func TestScriptApprovalPinsSelectedVersionAcrossUpgradeAndRestart(t *testing.T) {
	for _, scope := range []capability.SkillScope{capability.SkillScopeWorkspace, capability.SkillScopeUser, capability.SkillScopeProject} {
		t.Run(string(scope), func(t *testing.T) { testScriptApprovalScopeSnapshot(t, scope) })
	}
}

func TestSameNameScriptApprovalsRequireExactOrUnambiguousIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, capabilityID, scriptID, otherScriptID, errorCode string
		mismatchedSnapshot                                     bool
	}{
		{name: "exact first", capabilityID: "scope_a", scriptID: "render", otherScriptID: "render"},
		{name: "exact second", capabilityID: "scope_b", scriptID: "render", otherScriptID: "render"},
		{name: "ambiguous legacy", scriptID: "render", otherScriptID: "render", errorCode: "SKILL_SCRIPT_IDENTITY_AMBIGUOUS"},
		{name: "unique legacy first", scriptID: "render", otherScriptID: "review"},
		{name: "unique legacy second", scriptID: "review", otherScriptID: "review"},
		{name: "unknown identity", capabilityID: "unknown", scriptID: "render", otherScriptID: "render", errorCode: "SKILL_SCRIPT_SKILL_UNAVAILABLE"},
		{name: "wrong script", capabilityID: "scope_a", scriptID: "review", otherScriptID: "review", errorCode: "SKILL_SCRIPT_NOT_DECLARED"},
		{name: "mismatched snapshot", capabilityID: "scope_b", scriptID: "render", otherScriptID: "render", mismatchedSnapshot: true, errorCode: "SKILL_SCRIPT_SNAPSHOT_MISMATCH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(filepath.Join(t.TempDir(), "identity.db"), loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			project, err := store.CreateProject(ctx, "script identity")
			if err != nil {
				t.Fatal(err)
			}
			first, err := store.InstallSkillZIP(ctx, "first.zip", bytesReader(buildScriptIdentitySkillZIP(t, "scope_a", "render")), "admin")
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.InstallSkillZIP(ctx, "second.zip", bytesReader(buildScriptIdentitySkillZIP(t, "scope_b", tc.otherScriptID)), "admin", SkillInstallTarget{Scope: capability.SkillScopeProject, ProjectID: project.ProjectID})
			if err != nil {
				t.Fatal(err)
			}
			arguments := map[string]string{"skill_name": "same-name", "script_id": tc.scriptID, "input_json": "{}"}
			if tc.capabilityID != "" {
				arguments["capability_id"] = tc.capabilityID
			}
			raw, err := json.Marshal(arguments)
			if err != nil {
				t.Fatal(err)
			}
			command := BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, SDKToolCallID: "same-name-script", ToolID: "runtime:execute_skill_script", Arguments: raw}
			if tc.mismatchedSnapshot {
				version := activeSkillVersionForTest(t, first)
				command.SkillSnapshot = &AgentToolSkillSnapshot{CapabilityID: first.CapabilityID, Version: version.Version, ContentHash: version.ContentHash}
			}
			call, err := store.BeginAgentToolCall(ctx, command)
			if tc.errorCode != "" {
				assertDomainCode(t, err, tc.errorCode)
				var count int
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_tool_calls WHERE conversation_id = ?`, project.PrimaryConversationID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("invalid call registered: %d, %v", count, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			expected := tc.capabilityID
			if expected == "" {
				expected = "scope_a"
				if tc.scriptID == "review" {
					expected = "scope_b"
				}
			}
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, _, _, err := scriptSnapshotForCallTx(ctx, tx, call.AgentToolCallID)
			_ = tx.Rollback()
			if err != nil || snapshot.CapabilityID != expected {
				t.Fatalf("wrong package pin: %+v, %v", snapshot, err)
			}
			reused, err := store.BeginAgentToolCall(ctx, command)
			if err != nil || reused.AgentToolCallID != call.AgentToolCallID {
				t.Fatalf("original approval not reused: %+v, %v", reused, err)
			}
		})
	}
}

func buildScriptIdentitySkillZIP(t *testing.T, capabilityID, scriptID string) []byte {
	t.Helper()
	manifest, err := json.Marshal(map[string]any{
		"schema_version": "1.0.0", "id": capabilityID, "version": "1.0.0", "execution_mode": "inline", "ui": map[string]any{},
		"scripts": []map[string]string{{"id": scriptID, "path": "scripts/render.py", "runtime": "python", "description": "Identity fixture."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return buildSkillZIP(t, []skillZIPTestEntry{
		{name: "SKILL.md", data: []byte("---\nname: same-name\ndescription: Script identity fixture.\n---\n\nUse the selected script.\n")},
		{name: "content-agent/manifest.json", data: manifest},
		{name: "scripts/render.py", data: []byte("print('" + capabilityID + "')")},
	})
}

func testScriptApprovalScopeSnapshot(t *testing.T, scope capability.SkillScope) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "content_agent.db")
	store, err := Open(databasePath, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	sandbox := &runtimeScriptSandbox{status: scriptsandbox.Status{Available: true, Adapter: "fixture", Engine: "fixture", Runtimes: []string{"python"}}}
	store.SetScriptSandbox(sandbox)
	project, err := store.CreateProject(ctx, "pinned script")
	if err != nil {
		t.Fatal(err)
	}
	target := SkillInstallTarget{Scope: scope}
	if scope == capability.SkillScopeProject {
		target.ProjectID = project.ProjectID
	}
	if scope != capability.SkillScopeWorkspace {
		if _, err := store.InstallSkillZIP(ctx, "workspace-script.zip", bytesReader(buildScriptSkillZIP(t)), "admin"); err != nil {
			t.Fatal(err)
		}
	}
	installed, err := store.InstallSkillZIP(ctx, "script.zip", bytesReader(buildScriptSkillZIP(t)), "admin", target)
	if err != nil {
		t.Fatal(err)
	}
	first := activeSkillVersionForTest(t, installed)
	if _, err := store.UpdateScriptSandboxPolicy(ctx, UpdateScriptSandboxPolicyCommand{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	command := BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: "snapshot-script", ToolID: "runtime:execute_skill_script",
		Arguments:     json.RawMessage(`{"skill_name":"sandbox-skill","script_id":"render","input_json":"{}"}`),
		SkillSnapshot: &AgentToolSkillSnapshot{CapabilityID: installed.CapabilityID, Version: first.Version, ContentHash: first.ContentHash},
	}
	call, err := store.BeginAgentToolCall(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(call.Approval.Reason, first.ContentHash) || !strings.Contains(call.Approval.Reason, "@1.0.0") {
		t.Fatalf("approval omits version identity: %+v", call.Approval)
	}
	upgraded, err := store.InstallSkillZIP(ctx, "script-v2.zip", bytesReader(buildScriptSkillVersionZIP(t, "2.0.0")), "admin", target)
	if err != nil {
		t.Fatal(err)
	}
	second := activeSkillVersionForTest(t, upgraded)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	store.SetScriptSandbox(sandbox)
	if _, err := store.ResolveAgentToolApproval(ctx, ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve", ActorRef: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAgentToolCall(ctx, command); err != nil {
		t.Fatal(err)
	}
	forged := command
	forged.SkillSnapshot = &AgentToolSkillSnapshot{CapabilityID: installed.CapabilityID, Version: second.Version, ContentHash: second.ContentHash}
	_, err = store.BeginAgentToolCall(ctx, forged)
	assertDomainCode(t, err, "SKILL_SCRIPT_SNAPSHOT_MISMATCH")
	if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID}); err != nil {
		t.Fatal(err)
	}
	execution, err := store.ExecuteSkillScript(ctx, ExecuteSkillScriptCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, Arguments: command.Arguments})
	if err != nil || execution.Status != "completed" || sandbox.calls != 1 {
		t.Fatalf("execution = %+v, %v", execution, err)
	}
	script, err := os.ReadFile(filepath.Join(sandbox.request.SkillRoot, sandbox.request.ScriptPath))
	if err != nil || !strings.Contains(string(script), "render 1.0.0") {
		t.Fatalf("executed wrong version: %q, %v", script, err)
	}
	// Even a new call after an upgrade must respect the SDK's selected snapshot.
	command.SDKToolCallID = "snapshot-script-after-upgrade"
	newCall, err := store.BeginAgentToolCall(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	var boundVersion string
	if err := store.db.QueryRow(`SELECT skill_version_id FROM agent_tool_skill_snapshots WHERE agent_tool_call_id = ?`, newCall.AgentToolCallID).Scan(&boundVersion); err != nil || boundVersion != first.SkillVersionID {
		t.Fatalf("new call ignored selected version: %s / %v", boundVersion, err)
	}
	if _, err := store.SetSkillInstallationEnabled(ctx, installed.SkillInstallationID, false, "admin"); err != nil {
		t.Fatal(err)
	}
	command.SDKToolCallID = "snapshot-script-disabled"
	_, err = store.BeginAgentToolCall(ctx, command)
	assertDomainCode(t, err, "SKILL_SCRIPT_SKILL_UNAVAILABLE")
}

func TestScriptSnapshotMigrationRejectsUnboundLegacyApprovals(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "content_agent.db")
	store, err := Open(databasePath, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	if _, err := store.InstallSkillZIP(ctx, "script.zip", bytesReader(buildScriptSkillZIP(t)), "admin"); err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, "legacy script approval")
	if err != nil {
		t.Fatal(err)
	}
	command := BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, SDKToolCallID: "legacy-script", ToolID: "runtime:execute_skill_script", Arguments: json.RawMessage(`{"skill_name":"sandbox-skill","script_id":"render","input_json":"{}"}`)}
	if _, err := store.BeginAgentToolCall(ctx, command); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE agent_tool_skill_snapshots; PRAGMA user_version = 33;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	_, err = store.BeginAgentToolCall(ctx, command)
	assertDomainCode(t, err, "SKILL_SCRIPT_SNAPSHOT_UNAVAILABLE")
	var backup string
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version = 33 AND to_version = ? AND status = 'completed'`, schemaVersion).Scan(&backup); err != nil || backup == "" {
		t.Fatalf("migration backup: %q / %v", backup, err)
	}
}

func TestDirectoryScriptApprovalExecutesOriginalSnapshotWithoutManagedInstallation(t *testing.T) {
	for _, mode := range []string{"platform-manifest", "native-skill"} {
		t.Run(mode, func(t *testing.T) { testDirectoryScriptSnapshot(t, mode == "native-skill") })
	}
}

func testDirectoryScriptSnapshot(t *testing.T, native bool) {
	ctx := context.Background()
	registry := loadTestRegistry(t)
	source, err := prepareSkillZIP(registry.ProjectRoot(), t.TempDir(), "directory.zip", bytesReader(buildScriptSkillZIP(t)))
	if err != nil {
		t.Fatal(err)
	}
	if native {
		if err := os.Remove(filepath.Join(source.Directory, "content-agent", "manifest.json")); err != nil {
			t.Fatal(err)
		}
		source, err = capability.InspectSkillPackage(registry.ProjectRoot(), capability.SkillRoot{Scope: capability.SkillScopeWorkspace, Path: filepath.Dir(source.Directory)}, source.Directory)
		if err != nil || len(source.Scripts) != 1 {
			t.Fatalf("native script discovery: %+v err=%v", source, err)
		}
	}
	if err := registry.AddSkillRoot(capability.SkillRoot{Scope: capability.SkillScopeWorkspace, Path: filepath.Dir(source.Directory)}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "directory-script.db")
	store, err := Open(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	sandbox := &runtimeScriptSandbox{status: scriptsandbox.Status{Available: true, Adapter: "fixture", Engine: "fixture", Runtimes: []string{"python"}}}
	store.SetScriptSandbox(sandbox)
	project, _ := store.CreateProject(ctx, "Directory script")
	if _, err := store.UpdateScriptSandboxPolicy(ctx, UpdateScriptSandboxPolicyCommand{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	arguments, err := json.Marshal(map[string]string{"skill_name": source.Name, "script_id": source.Scripts[0].ID, "input_json": "{}"})
	if err != nil {
		t.Fatal(err)
	}
	command := BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: "directory-script", ToolID: "runtime:execute_skill_script",
		Arguments:     arguments,
		SkillSnapshot: &AgentToolSkillSnapshot{CapabilityID: source.CapabilityID, Version: source.Version, ContentHash: source.ContentHash},
	}
	call, err := store.BeginAgentToolCall(ctx, command)
	if err != nil || call.Approval == nil {
		t.Fatalf("directory script must still need approval: %+v err=%v", call, err)
	}
	if err := os.WriteFile(filepath.Join(source.Directory, "scripts", "render.py"), []byte("print('UNAPPROVED_CHANGE')"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	store.SetScriptSandbox(sandbox)
	if _, err := store.ResolveAgentToolApproval(ctx, ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve", ActorRef: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAgentToolCall(ctx, command); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID}); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	active := scriptSnapshotStillEnabledTx(ctx, tx, call.AgentToolCallID)
	tx.Rollback()
	if !active {
		t.Fatal("directory snapshot monitor incorrectly required installation")
	}
	execution, err := store.ExecuteSkillScript(ctx, ExecuteSkillScriptCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, Arguments: command.Arguments})
	if err != nil || execution.Status != "completed" || execution.SkillSnapshotID == "" || execution.SkillInstallationID != "" || execution.SkillVersionID != "" {
		t.Fatalf("directory execution: %+v err=%v", execution, err)
	}
	script, err := os.ReadFile(filepath.Join(sandbox.request.SkillRoot, sandbox.request.ScriptPath))
	if err != nil || string(script) != "print('render 1.0.0')" {
		t.Fatalf("executed unapproved directory mutation: %q err=%v", script, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM skill_installations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("directory scripts created fake installations: %d err=%v", count, err)
	}
}

func TestV36ScriptSnapshotMigrationPreservesApprovalsAndExecutionHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v36-scripts.db")
	store, err := Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	installed, err := store.InstallSkillZIP(ctx, "script.zip", bytesReader(buildScriptSkillZIP(t)), "admin")
	if err != nil {
		t.Fatal(err)
	}
	project, _ := store.CreateProject(ctx, "Migration history")
	command := BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: "v36-script", ToolID: "runtime:execute_skill_script", Arguments: json.RawMessage(`{"skill_name":"sandbox-skill","script_id":"render","input_json":"{}"}`)}
	call, err := store.BeginAgentToolCall(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := strings.NewReplacer(
		"\tskill_snapshot_id TEXT REFERENCES skill_execution_snapshots(skill_snapshot_id),\n", "",
		"\tCHECK(skill_version_id IS NOT NULL OR skill_snapshot_id IS NOT NULL)\n", "",
		"\tCHECK(skill_version_id IS NOT NULL OR skill_snapshot_id IS NOT NULL),\n", "",
		"\tCHECK((skill_installation_id IS NULL) = (skill_version_id IS NULL))\n", "",
	).Replace(scriptSnapshotSchema)
	legacySchema = strings.NewReplacer(
		"skill_version_id TEXT REFERENCES", "skill_version_id TEXT NOT NULL REFERENCES",
		"skill_installation_id TEXT REFERENCES", "skill_installation_id TEXT NOT NULL REFERENCES",
		"skill_version_id TEXT NOT NULL REFERENCES skill_versions(skill_version_id),\n);", "skill_version_id TEXT NOT NULL REFERENCES skill_versions(skill_version_id)\n);",
		"updated_at TEXT NOT NULL,\n);", "updated_at TEXT NOT NULL\n);",
	).Replace(legacySchema)
	// NewReplacer does not reprocess replaced text; remove the final legacy
	// binding comma after restoring NOT NULL.
	legacySchema = strings.ReplaceAll(legacySchema, "skill_version_id TEXT NOT NULL REFERENCES skill_versions(skill_version_id),\n);", "skill_version_id TEXT NOT NULL REFERENCES skill_versions(skill_version_id)\n);")
	if _, err := store.db.Exec(`DROP TABLE agent_tool_skill_snapshots; DROP TABLE skill_script_executions;` + legacySchema); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO agent_tool_skill_snapshots(agent_tool_call_id, skill_version_id) VALUES(?, ?)`, call.AgentToolCallID, *installed.ActiveVersionID); err != nil {
		t.Fatal(err)
	}
	now := formatTime(store.now())
	if _, err := store.db.Exec(`INSERT INTO skill_script_executions(
		skill_script_execution_id, workspace_id, project_id, conversation_id, agent_tool_call_id,
		skill_installation_id, skill_version_id, script_id, script_path, runtime, adapter, engine, image,
		status, input_hash, limits_json, environment_names_json, output_storage_ref, requested_by, started_at, completed_at, updated_at)
		VALUES('legacy_execution', ?, ?, ?, ?, ?, ?, 'render', 'scripts/render.py', 'python', 'fixture', 'fixture', 'fixture', 'completed', 'hash', '{}', '[]', '', 'admin', ?, ?, ?)`,
		installed.WorkspaceID, project.ProjectID, project.PrimaryConversationID, call.AgentToolCallID, installed.SkillInstallationID, *installed.ActiveVersionID, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 36`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	if _, err := store.BeginAgentToolCall(ctx, command); err != nil {
		t.Fatalf("migrated approval lost its binding: %v", err)
	}
	execution, err := store.GetSkillScriptExecution(ctx, "legacy_execution")
	if err != nil || execution.Status != "completed" || execution.SkillVersionID != *installed.ActiveVersionID || execution.SkillSnapshotID != "" {
		t.Fatalf("migrated execution: %+v err=%v", execution, err)
	}
	var backup string
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version = 36 AND to_version = ? AND status = 'completed'`, schemaVersion).Scan(&backup); err != nil || backup == "" {
		t.Fatalf("v36 backup missing: %q err=%v", backup, err)
	}
	rows, err := store.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("migration broke foreign keys")
	}
}
