package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

const authoredSkillText = "---\nname: authored-story\ndescription: Review story outlines when the user asks for narrative diagnostics.\n---\n# Story review\nRead references/checklist.md and report causal gaps.\n"

func writeDraftFile(t *testing.T, store *Store, project Project, name, content string, version int) ProjectFile {
	t.Helper()
	operation := "create_file"
	if version > 0 {
		operation = "update_file"
	}
	patch := ProjectFilePatch{Path: name, ExpectedVersion: version, Operation: operation, Diff: "+" + strings.ReplaceAll(content, "\n", "\n+")}
	call := beginProjectFileCall(t, store, project, patch)
	file, err := store.ApplyProjectFilePatch(context.Background(), call.AgentToolCallID, call.SDKToolCallID, patch, content)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func createSkillDraftForTest(t *testing.T, store *Store) (context.Context, Project, ProjectSkillDraft) {
	t.Helper()
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	project, err := store.CreateProject(ctx, "Authored Skill")
	if err != nil {
		t.Fatal(err)
	}
	writeDraftFile(t, store, project, "skills/authored-story/SKILL.md", authoredSkillText, 0)
	writeDraftFile(t, store, project, "skills/authored-story/references/checklist.md", "Check narrative causality.", 0)
	preview, err := store.PreviewProjectSkillDraft(ctx, project.ProjectID, "skills/authored-story")
	if err != nil || preview.Status != "valid" {
		t.Fatalf("preview: %+v, %v", preview, err)
	}
	return ctx, project, preview
}

func TestProjectSkillDraftValidationExportInstallUpgradeAndReplay(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()
	ctx, project, preview := createSkillDraftForTest(t, store)
	if preview.CapabilityID != "authored_story" || preview.ExecutionMode != "inline" || len(preview.Files) != 2 || preview.Manifest.Path != "skills/authored-story/SKILL.md" {
		t.Fatalf("public preview: %+v", preview)
	}
	_, archive, err := store.ExportProjectSkillDraft(ctx, project.ProjectID, preview.RootPath, preview.SnapshotHash)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil || len(reader.File) != 2 {
		t.Fatalf("archive: %+v, %v", reader, err)
	}
	for _, file := range reader.File {
		if file.Name == "SKILL.md" {
			stream, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(stream)
			stream.Close()
			if err != nil || string(data) != authoredSkillText {
				t.Fatalf("export bytes: %s, %v", data, err)
			}
		}
	}
	args := ProjectSkillInstallArguments{RootPath: preview.RootPath, SnapshotHash: preview.SnapshotHash, Scope: capability.SkillScopeProject}
	_, err = store.InstallProjectSkillDraft(ctx, project.ProjectID, args, "first-install-key", false)
	assertDomainCode(t, err, "REQUIRED_CONFIRMATION_MISSING")
	first, err := store.InstallProjectSkillDraft(ctx, project.ProjectID, args, "first-install-key", true)
	if err != nil || !first.Installation.Enabled || first.Installation.RegistryStatus != "available" {
		t.Fatalf("install: %+v, %v", first, err)
	}
	resource, err := store.ReadSkillResource(ctx, project.ProjectID, preview.CapabilityID, preview.Version, "references/checklist.md", 0, 100)
	if err != nil || resource.Content != "Check narrative causality." {
		t.Fatalf("registered resource: %+v, %v", resource, err)
	}
	currentPreview, err := store.PreviewProjectSkillDraft(ctx, project.ProjectID, preview.RootPath)
	if err != nil || len(currentPreview.Installations) != 1 || currentPreview.Installations[0].InstallationID != first.Receipt.InstallationID {
		t.Fatalf("upgrade target: %+v, %v", currentPreview, err)
	}
	_, err = store.InstallProjectSkillDraft(ctx, project.ProjectID, args, "other-install-key", true)
	assertDomainCode(t, err, "SKILL_INSTALLATION_STATE_CONFLICT")
	writeDraftFile(t, store, project, "skills/authored-story/SKILL.md", authoredSkillText+"\nAlso inspect escalation.\n", 1)
	_, _, err = store.ExportProjectSkillDraft(ctx, project.ProjectID, preview.RootPath, preview.SnapshotHash)
	assertDomainCode(t, err, "SKILL_DRAFT_CHANGED")
	_, err = store.InstallProjectSkillDraft(ctx, project.ProjectID, args, "stale-install-key", true)
	assertDomainCode(t, err, "SKILL_DRAFT_CHANGED")
	updated, err := store.PreviewProjectSkillDraft(ctx, project.ProjectID, preview.RootPath)
	if err != nil {
		t.Fatal(err)
	}
	upgrade := args
	upgrade.SnapshotHash = updated.SnapshotHash
	upgrade.InstallationID = first.Receipt.InstallationID
	upgrade.ExpectedActiveVersionID = first.Receipt.VersionID
	second, err := store.InstallProjectSkillDraft(ctx, project.ProjectID, upgrade, "upgrade-install-key", true)
	if err != nil || second.Receipt.VersionID == first.Receipt.VersionID || len(second.Installation.Versions) != 2 {
		t.Fatalf("upgrade: %+v, %v", second, err)
	}
	replayed, err := store.InstallProjectSkillDraft(ctx, project.ProjectID, args, "first-install-key", true)
	if err != nil || replayed.Receipt != first.Receipt || *replayed.Installation.ActiveVersionID != second.Receipt.VersionID {
		t.Fatalf("replay reactivated an old version: %+v, %v", replayed, err)
	}
	_, err = store.InstallProjectSkillDraft(ctx, project.ProjectID, upgrade, "first-install-key", true)
	assertDomainCode(t, err, "IDEMPOTENCY_CONFLICT")
	_, err = store.InstallProjectSkillDraft(ctx, project.ProjectID, upgrade, "new-upgrade-key-2", true)
	assertDomainCode(t, err, "SKILL_DISCOVERY_CHANGED")
	wrongScope := upgrade
	wrongScope.Scope = capability.SkillScopeUser
	wrongScope.ExpectedActiveVersionID = second.Receipt.VersionID
	_, err = store.InstallProjectSkillDraft(ctx, project.ProjectID, wrongScope, "wrong-scope-key-1", true)
	assertDomainCode(t, err, "SKILL_INSTALLATION_STATE_CONFLICT")
}

func TestProjectSkillDraftInvalidPackagesAndScope(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()
	ctx, project, preview := createSkillDraftForTest(t, store)
	other := identity.DefaultLocalPrincipal()
	other.WorkspaceID = "workspace_other"
	_, err := store.PreviewProjectSkillDraft(identity.WithPrincipal(context.Background(), other), project.ProjectID, preview.RootPath)
	assertDomainCode(t, err, "PROJECT_NOT_FOUND")
	_, err = store.PreviewProjectSkillDraft(ctx, project.ProjectID, "../skills")
	assertDomainCode(t, err, "PROJECT_FILE_PATH_INVALID")
	viewer := identity.DefaultLocalPrincipal()
	viewer.Role = identity.RoleViewer
	_, err = store.InstallProjectSkillDraft(identity.WithPrincipal(context.Background(), viewer), project.ProjectID, ProjectSkillInstallArguments{RootPath: preview.RootPath, SnapshotHash: preview.SnapshotHash, Scope: capability.SkillScopeProject}, "viewer-install-key", true)
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
	writeDraftFile(t, store, project, "bad/SKILL.md", "not a Skill document", 0)
	invalid, err := store.PreviewProjectSkillDraft(ctx, project.ProjectID, "bad")
	if err != nil || invalid.Status != "invalid" || len(invalid.Diagnostics) == 0 {
		t.Fatalf("invalid preview: %+v, %v", invalid, err)
	}
	_, _, err = store.ExportProjectSkillDraft(ctx, project.ProjectID, "bad", invalid.SnapshotHash)
	assertDomainCode(t, err, "SKILL_PACKAGE_INVALID")
	installations, err := store.ListSkillInstallations(ctx, false)
	if err != nil || len(installations) != 0 {
		t.Fatalf("validation installed a Skill: %+v, %v", installations, err)
	}
}

func TestProjectSkillDraftSDKApprovalSnapshotAndCancellation(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()
	ctx, project, preview := createSkillDraftForTest(t, store)
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Install my Skill"}, CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Keep my original catalog"}, CommandMeta{IdempotencyKey: "sibling-turn", RequestHash: "sibling-request", CommandType: "agent_turn"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_turns SET status='running' WHERE agent_turn_id=?`, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	args := ProjectSkillInstallArguments{RootPath: preview.RootPath, SnapshotHash: preview.SnapshotHash, Scope: capability.SkillScopeProject}
	raw, _ := json.Marshal(args)
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, AgentTurnID: turn.AgentTurnID, SDKToolCallID: "sdk-install", ToolID: "runtime:install_workspace_skill", Arguments: raw})
	if err != nil || call.Status != "pending_approval" || call.Approval == nil {
		t.Fatalf("SDK approval registration: %+v, %v", call, err)
	}
	service := identity.WithDelegatedUser(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), identity.DefaultLocalPrincipal())
	service = WithAgentActivity(service, AgentActivityIdentity{ProjectID: project.ProjectID, AgentTurnID: turn.AgentTurnID})
	_, err = store.InstallProjectSkillDraftForTool(service, call.AgentToolCallID, call.SDKToolCallID, args)
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
	if _, err := store.ResolveAgentToolApproval(ctx, ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve", ActorRef: identity.DefaultUserID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartAgentToolCall(service, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID}); err != nil {
		t.Fatal(err)
	}
	result, err := store.InstallProjectSkillDraftForTool(service, call.AgentToolCallID, call.SDKToolCallID, args)
	if err != nil || result.Installation.RegistryStatus != "available" {
		t.Fatalf("approved install: %+v, %v", result, err)
	}
	resource, err := store.ReadSkillResource(service, project.ProjectID, preview.CapabilityID, preview.Version, "references/checklist.md", 0, 100)
	if err != nil || resource.Content != "Check narrative causality." {
		t.Fatalf("current turn cannot load installed resources: %+v, %v", resource, err)
	}
	siblingContext := WithAgentActivity(identity.WithDelegatedUser(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), identity.DefaultLocalPrincipal()), AgentActivityIdentity{ProjectID: project.ProjectID, AgentTurnID: sibling.AgentTurnID})
	siblingRegistry, err := store.CapabilityRegistryForProject(siblingContext, project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := siblingRegistry.Get(preview.CapabilityID); found {
		t.Fatal("another turn's frozen catalog changed after this installation")
	}
	replay, err := store.InstallProjectSkillDraftForTool(service, call.AgentToolCallID, call.SDKToolCallID, args)
	if err != nil || replay.Receipt != result.Receipt {
		t.Fatalf("SDK replay: %+v, %v", replay, err)
	}
	if result.Installation.CreatedBy != identity.DefaultUserID {
		t.Fatalf("installation lost delegated actor: %s", result.Installation.CreatedBy)
	}
	// Receipt application follows transaction insertion order even if the host
	// clock moves backwards; old-call retries cannot replace the newer binding.
	store.now = func() time.Time { return result.Receipt.CreatedAt.Add(-time.Hour) }
	writeDraftFile(t, store, project, "skills/authored-story/SKILL.md", authoredSkillText+"\nReview upgraded instructions.\n", 1)
	updated, err := store.PreviewProjectSkillDraft(ctx, project.ProjectID, preview.RootPath)
	if err != nil {
		t.Fatal(err)
	}
	upgrade := ProjectSkillInstallArguments{RootPath: preview.RootPath, SnapshotHash: updated.SnapshotHash, Scope: capability.SkillScopeProject, InstallationID: result.Receipt.InstallationID, ExpectedActiveVersionID: result.Receipt.VersionID}
	upgradeRaw, _ := json.Marshal(upgrade)
	upgradeCall, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, AgentTurnID: turn.AgentTurnID, SDKToolCallID: "sdk-upgrade", ToolID: "runtime:install_workspace_skill", Arguments: upgradeRaw})
	if err != nil || upgradeCall.Approval == nil {
		t.Fatalf("upgrade call: %+v, %v", upgradeCall, err)
	}
	if _, err := store.ResolveAgentToolApproval(ctx, ResolveAgentToolApprovalCommand{AgentToolApprovalID: upgradeCall.Approval.AgentToolApprovalID, ExpectedVersion: upgradeCall.Approval.Version, SubjectSnapshotHash: upgradeCall.Approval.SubjectSnapshotHash, Action: "approve", ActorRef: identity.DefaultUserID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartAgentToolCall(service, StartAgentToolCallCommand{AgentToolCallID: upgradeCall.AgentToolCallID, ExpectedSDKToolCallID: upgradeCall.SDKToolCallID}); err != nil {
		t.Fatal(err)
	}
	upgraded, err := store.InstallProjectSkillDraftForTool(service, upgradeCall.AgentToolCallID, upgradeCall.SDKToolCallID, upgrade)
	if err != nil {
		t.Fatal(err)
	}
	currentRegistry, err := store.CapabilityRegistryForProject(service, project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	entry, found := currentRegistry.Get(preview.CapabilityID)
	if !found || entry.Skill.Version != updated.Version {
		t.Fatalf("wrong upgrade binding: %+v", entry)
	}
	replay, err = store.InstallProjectSkillDraftForTool(service, call.AgentToolCallID, call.SDKToolCallID, args)
	if err != nil || replay.Receipt != result.Receipt || *replay.Installation.ActiveVersionID != upgraded.Receipt.VersionID {
		t.Fatalf("upgrade replay: %+v, %v", replay, err)
	}
	if _, err := store.CancelAgentToolCall(ctx, CancelAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Reason: "stop"}); err != nil {
		t.Fatal(err)
	}
	_, err = store.InstallProjectSkillDraftForTool(service, call.AgentToolCallID, call.SDKToolCallID, args)
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM project_skill_install_receipts WHERE agent_tool_call_id=?`, call.AgentToolCallID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("receipts: %d, %v", count, err)
	}
}

func TestProjectSkillDraftReceiptSurvivesReopenAndUninstalledCanBeReinstalled(t *testing.T) {
	database := filepath.Join(t.TempDir(), "runtime.db")
	store := openProjectFilesTestStore(t, database)
	ctx, project, preview := createSkillDraftForTest(t, store)
	args := ProjectSkillInstallArguments{RootPath: preview.RootPath, SnapshotHash: preview.SnapshotHash, Scope: capability.SkillScopeUser}
	first, err := store.InstallProjectSkillDraft(ctx, project.ProjectID, args, "durable-install-key", true)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()
	store = openProjectFilesTestStore(t, database)
	defer store.Close()
	replay, err := store.InstallProjectSkillDraft(ctx, project.ProjectID, args, "durable-install-key", true)
	if err != nil || replay.Receipt != first.Receipt || len(replay.Installation.Versions) != 1 || replay.Installation.RegistryStatus != "available" {
		t.Fatalf("reopened receipt: %+v, %v", replay, err)
	}
	if _, err := store.UninstallSkill(ctx, first.Receipt.InstallationID, ""); err != nil {
		t.Fatal(err)
	}
	preview, err = store.PreviewProjectSkillDraft(ctx, project.ProjectID, args.RootPath)
	if err != nil || len(preview.Installations) != 0 {
		t.Fatalf("uninstalled upgrade targets: %+v, %v", preview, err)
	}
	replay, err = store.InstallProjectSkillDraft(ctx, project.ProjectID, args, "durable-install-key", true)
	if err != nil || replay.Installation.Status != "uninstalled" {
		t.Fatalf("old receipt reactivated uninstalled Skill: %+v, %v", replay, err)
	}
	reinstalled, err := store.InstallProjectSkillDraft(ctx, project.ProjectID, args, "reinstall-new-key", true)
	if err != nil || reinstalled.Installation.Status != "installed" || reinstalled.Installation.RegistryStatus != "available" || reinstalled.Receipt.InstallationID != first.Receipt.InstallationID {
		t.Fatalf("reinstall: %+v, %v", reinstalled, err)
	}
}

func TestProjectSkillDraftMigrateVersion39PreservesFilesAndBackup(t *testing.T) {
	database := filepath.Join(t.TempDir(), "runtime.db")
	store := openProjectFilesTestStore(t, database)
	ctx, project, before := createSkillDraftForTest(t, store)
	if _, err := store.db.Exec(`DROP TABLE project_skill_install_receipts; PRAGMA user_version=39;`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	defer store.Close()
	after, err := store.PreviewProjectSkillDraft(ctx, project.ProjectID, before.RootPath)
	if err != nil || after.SnapshotHash != before.SnapshotHash || len(after.Files) != 2 {
		t.Fatalf("migrated files: %+v, %v", after, err)
	}
	var backup string
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=39 AND to_version=?`, schemaVersion).Scan(&backup); err != nil || backup == "" {
		t.Fatalf("migration backup: %q, %v", backup, err)
	}
	installed, err := store.InstallProjectSkillDraft(ctx, project.ProjectID, ProjectSkillInstallArguments{RootPath: after.RootPath, SnapshotHash: after.SnapshotHash, Scope: capability.SkillScopeProject}, "migrated-install-key", true)
	if err != nil || installed.Receipt.VersionID == "" {
		t.Fatalf("migrated installation: %+v, %v", installed, err)
	}
}

func TestProjectSkillDraftRejectsChangeDuringInstallation(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()
	ctx, project, preview := createSkillDraftForTest(t, store)
	snapshot, err := store.projectSkillDraftSnapshot(ctx, project.ProjectID, preview.RootPath)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := projectSkillDraftArchive(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	args := ProjectSkillInstallArguments{RootPath: preview.RootPath, SnapshotHash: preview.SnapshotHash, Scope: capability.SkillScopeProject}
	raw, _ := json.Marshal(args)
	_, hash, _, _ := summarizeAgentToolPayload(raw, true)
	guard := projectSkillInstallGuard{projectID: project.ProjectID, receiptID: "race-fixture", argumentHash: hash, arguments: args}
	_, err = store.installSkillCheckedWithDraft(ctx, "zip", "draft.zip", "", "", "", func(quarantine string) (*capability.SkillPackage, error) {
		prepared, err := prepareSkillZIP(store.registry.ProjectRoot(), quarantine, "draft.zip", bytes.NewReader(archive))
		if err != nil {
			return nil, err
		}
		writeDraftFile(t, store, project, "skills/authored-story/SKILL.md", authoredSkillText+"Changed after preparation.\n", 1)
		return prepared, nil
	}, &guard, SkillInstallTarget{Scope: capability.SkillScopeProject, ProjectID: project.ProjectID})
	assertDomainCode(t, err, "SKILL_DRAFT_CHANGED")
	installed, err := store.ListSkillInstallations(ctx, false)
	if err != nil || len(installed) != 0 {
		t.Fatalf("race published a Skill: %+v, %v", installed, err)
	}
}

func TestWorkspaceSkillToolsHaveCorrectApprovalPolicy(t *testing.T) {
	registry, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := registry.Get("runtime:install_workspace_skill")
	if !ok || tool.Approval != agenttool.ApprovalAlways || tool.Access != agenttool.AccessWrite {
		t.Fatalf("install policy: %+v", tool)
	}
	tool, ok = registry.Get("runtime:validate_workspace_skill")
	if !ok || tool.Approval != agenttool.ApprovalNever || tool.Access != agenttool.AccessRead {
		t.Fatalf("validation policy: %+v", tool)
	}
}

func TestProjectSkillDraftConcurrentUpgradeReturnsSameReceipt(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()
	ctx, project, preview := createSkillDraftForTest(t, store)
	args := ProjectSkillInstallArguments{RootPath: preview.RootPath, SnapshotHash: preview.SnapshotHash, Scope: capability.SkillScopeProject}
	first, err := store.InstallProjectSkillDraft(ctx, project.ProjectID, args, "first-install-key", true)
	if err != nil {
		t.Fatal(err)
	}
	writeDraftFile(t, store, project, "skills/authored-story/SKILL.md", authoredSkillText+"\nUse the upgraded rubric.\n", 1)
	preview, err = store.PreviewProjectSkillDraft(ctx, project.ProjectID, args.RootPath)
	if err != nil {
		t.Fatal(err)
	}
	args.SnapshotHash, args.InstallationID, args.ExpectedActiveVersionID = preview.SnapshotHash, first.Receipt.InstallationID, first.Receipt.VersionID
	type outcome struct {
		value ProjectSkillInstallResult
		err   error
	}
	results := make(chan outcome, 8)
	start := make(chan struct{})
	for i := 0; i < cap(results); i++ {
		go func() {
			<-start
			value, err := store.InstallProjectSkillDraft(ctx, project.ProjectID, args, "concurrent-upgrade-key", true)
			results <- outcome{value, err}
		}()
	}
	close(start)
	var receipt ProjectSkillInstallReceipt
	for i := 0; i < cap(results); i++ {
		got := <-results
		if got.err != nil {
			t.Errorf("concurrent upgrade: %v", got.err)
			continue
		}
		if i == 0 {
			receipt = got.value.Receipt
		}
		if got.value.Receipt != receipt || len(got.value.Installation.Versions) != 2 || *got.value.Installation.ActiveVersionID != got.value.Receipt.VersionID {
			t.Errorf("concurrent receipt mismatch: %+v", got.value)
		}
	}
}

func TestProjectSkillDraftRechecksMembershipAtCommit(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()
	ctx, project, preview := createSkillDraftForTest(t, store)
	snapshot, err := store.projectSkillDraftSnapshot(ctx, project.ProjectID, preview.RootPath)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := projectSkillDraftArchive(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	args := ProjectSkillInstallArguments{RootPath: preview.RootPath, SnapshotHash: preview.SnapshotHash, Scope: capability.SkillScopeProject}
	raw, _ := json.Marshal(args)
	_, hash, _, _ := summarizeAgentToolPayload(raw, true)
	guard := projectSkillInstallGuard{projectID: project.ProjectID, receiptID: "membership-race", argumentHash: hash, arguments: args}
	_, err = store.installSkillCheckedWithDraft(ctx, "zip", "draft.zip", "", "", "", func(quarantine string) (*capability.SkillPackage, error) {
		prepared, err := prepareSkillZIP(store.registry.ProjectRoot(), quarantine, "draft.zip", bytes.NewReader(archive))
		if err != nil {
			return nil, err
		}
		_, err = store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, identity.DefaultWorkspaceID, identity.DefaultUserID)
		return prepared, err
	}, &guard, SkillInstallTarget{Scope: capability.SkillScopeProject, ProjectID: project.ProjectID})
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM skill_installations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("permission revocation published an installation: %d, %v", count, err)
	}
}
