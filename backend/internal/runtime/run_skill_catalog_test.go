package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func statefulCatalogContext(project Project, claim *TaskClaim) context.Context {
	return WithAgentActivity(context.Background(), AgentActivityIdentity{ProjectID: project.ProjectID,
		ExecutionAttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken})
}

func TestRunSkillCatalogPinsDirectoryBeforeMutationAndSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	source := writeSkillTestPackage(t, root, "run-helper", "run_helper", "1.0.0", "inline", "RUN_ORIGINAL")
	registry := loadTestRegistry(t)
	if err := registry.AddSkillRoot(capability.SkillRoot{Scope: capability.SkillScopeWorkspace, Path: root}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "run.db")
	store, err := Open(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	project, claim := statefulToolClaimForTest(t, store)
	ctx := statefulCatalogContext(project, claim)
	writeSkillTestPackage(t, root, "run-helper", "run_helper", "1.0.0", "inline", "CHANGED_SAME_VERSION")
	assertSnapshotInstructions(t, store, context.Background(), project.ProjectID, "run_helper", "1.0.0", "CHANGED_SAME_VERSION")
	assertSnapshotInstructions(t, store, ctx, project.ProjectID, "run_helper", "1.0.0", "RUN_ORIGINAL")
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotInstructions(t, store, ctx, project.ProjectID, "run_helper", "1.0.0", "RUN_ORIGINAL")
	foreign := identity.WithPrincipal(ctx, identity.Principal{Kind: identity.KindUser, UserID: "another-author", WorkspaceID: project.WorkspaceID, Role: identity.RoleEditor})
	_, err = store.CapabilityRegistryForProject(foreign, project.ProjectID)
	assertDomainCode(t, err, "AGENT_ACTIVITY_INVALID")
	mixed := WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: project.ProjectID, ExecutionAttemptID: claim.Attempt.AttemptID, AgentTurnID: "mixed"})
	_, err = store.CapabilityRegistryForProject(mixed, project.ProjectID)
	assertDomainCode(t, err, "AGENT_ACTIVITY_INVALID")
}

func TestRunSkillCatalogEmptyDoesNotDiscoverLaterInstallations(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "empty.db"))
	defer store.Close()
	project, claim := statefulToolClaimForTest(t, store)
	source := writeSkillTestPackage(t, t.TempDir(), "new-helper", "new_helper", "1.0.0", "inline", "ONLY_NEW_RUNS")
	if _, err := store.InstallSkillDirectory(context.Background(), source, "fixture"); err != nil {
		t.Fatal(err)
	}
	registry, err := store.CapabilityRegistryForProject(statefulCatalogContext(project, claim), project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("new_helper"); ok {
		t.Fatal("new installation changed an empty frozen Run catalog")
	}
	if _, ok := registry.Get("novel_to_script"); !ok {
		t.Fatal("freezing Skills removed built-in workflows")
	}
	newProject, newClaim := statefulToolClaimForTest(t, store)
	assertSnapshotInstructions(t, store, statefulCatalogContext(newProject, newClaim), newProject.ProjectID, "new_helper", "1.0.0", "ONLY_NEW_RUNS")
}

func TestRunSkillCatalogCollaboratorDoesNotInheritOtherAuthorsPersonalSkills(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "collaboration.db"))
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Collaborating authors")
	if err != nil {
		t.Fatal(err)
	}
	source := writeSkillTestPackage(t, t.TempDir(), "author-private", "author_private", "1.0.0", "inline", "PRIVATE_TO_ORIGIN_AUTHOR")
	if _, err := store.InstallSkillDirectory(ctx, source, "fixture", SkillInstallTarget{Scope: capability.SkillScopeUser}); err != nil {
		t.Fatal(err)
	}
	ref := &agentcontract.CapabilityRef{CapabilityID: "novel_to_script", Version: "1.4.0", SelectionMode: "explicit"}
	turn := acceptSnapshotTurn(t, store, project, "collaboration-origin", ref)
	message, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "Start the workflow")
	if err != nil {
		t.Fatal(err)
	}
	asset := createTextAsset(t, store, project.ProjectID, "source.txt", "第一章 少年下山。")
	command := novelStartCommand(project, message.MessageID, asset)
	exchange, err := store.CreateMessageExchange(snapshotTurnContext(turn), CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID, Request: turn.Request,
		Decision: agentcontract.AgentDecision{Reply: "Confirm start", Intent: "propose_capability", Confidence: 1, CapabilityRef: ref,
			ProposedAction: &agentcontract.ProposedActionDraft{ActionType: "start_run", CapabilityRef: ref, Input: command.Input, Config: command.Config, RequiresConfirmation: true}},
	})
	if err != nil || exchange.Action == nil {
		t.Fatalf("origin proposal: %+v %v", exchange, err)
	}
	command.Input, command.Config = exchange.Action.Input, exchange.Action.Config
	command.ProposedActionID, command.ProposedActionVersion = exchange.Action.ProposedActionID, exchange.Action.Version
	command.ConfirmationMessageID, command.ConfirmationSnapshotHash = exchange.Action.ConfirmationMessageID, exchange.Action.SnapshotHash
	collaborator := identity.Principal{Kind: identity.KindUser, UserID: "run-collaborator", WorkspaceID: project.WorkspaceID, Role: identity.RoleEditor}
	if err := store.BootstrapPrincipal(ctx, collaborator); err != nil {
		t.Fatal(err)
	}
	started, err := store.StartRun(identity.WithPrincipal(ctx, collaborator), command)
	if err != nil {
		t.Fatal(err)
	}
	approveAndResumeLifecycleRun(t, store, project, started)
	claim := claimStructuredTaskOnce(t, store, 60)
	registry, err := store.CapabilityRegistryForProject(identity.WithPrincipal(statefulCatalogContext(project, claim), collaborator), project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("author_private"); ok {
		t.Fatal("collaborator inherited the originating author's personal Skill")
	}
	assertSnapshotInstructions(t, store, snapshotTurnContext(turn), project.ProjectID, "author_private", "1.0.0", "PRIVATE_TO_ORIGIN_AUTHOR")
}

func TestRunSkillCatalogApprovedInstallIsScopedToExecutionAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install.db")
	store := openProjectFilesTestStore(t, path)
	defer func() { store.Close() }()
	project, claim := statefulToolClaimForTest(t, store)
	siblingProject, siblingClaim := statefulToolClaimForTest(t, store)
	turn := acceptSnapshotTurn(t, store, project, "sibling-turn", nil)
	writeDraftFile(t, store, project, "skills/authored-story/SKILL.md", authoredSkillText, 0)
	writeDraftFile(t, store, project, "skills/authored-story/references/checklist.md", "Check narrative causality.", 0)
	preview, err := store.PreviewProjectSkillDraft(context.Background(), project.ProjectID, "skills/authored-story")
	if err != nil || preview.Status != "valid" {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	args := ProjectSkillInstallArguments{RootPath: preview.RootPath, SnapshotHash: preview.SnapshotHash, Scope: capability.SkillScopeWorkspace}
	raw, _ := json.Marshal(args)
	call, err := store.BeginAgentToolCall(context.Background(), statefulToolCommand(project, claim, "runtime:install_workspace_skill", "sdk-install-stateful", raw))
	if err != nil || call.Approval == nil || call.Status != "pending_approval" {
		t.Fatalf("approval: %+v %v", call, err)
	}
	service := identity.WithDelegatedUser(identity.WithPrincipal(statefulCatalogContext(project, claim), identity.ServicePrincipal()), identity.DefaultLocalPrincipal())
	_, err = store.InstallProjectSkillDraftForTool(service, call.AgentToolCallID, call.SDKToolCallID, args)
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
	if _, err := store.ResolveAgentToolApproval(context.Background(), ResolveAgentToolApprovalCommand{
		AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version,
		SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve", ActorRef: identity.DefaultUserID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartAgentToolCall(service, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID}); err != nil {
		t.Fatal(err)
	}
	installed, err := store.InstallProjectSkillDraftForTool(service, call.AgentToolCallID, call.SDKToolCallID, args)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, path)
	assertSnapshotInstructions(t, store, service, project.ProjectID, preview.CapabilityID, preview.Version, "Story review")
	replay, err := store.InstallProjectSkillDraftForTool(service, call.AgentToolCallID, call.SDKToolCallID, args)
	if err != nil || replay.Receipt != installed.Receipt {
		t.Fatalf("restart replay: %+v %v", replay, err)
	}
	for _, item := range []struct {
		ctx       context.Context
		projectID string
	}{
		{statefulCatalogContext(siblingProject, siblingClaim), siblingProject.ProjectID},
		{snapshotTurnContext(turn), project.ProjectID},
	} {
		registry, err := store.CapabilityRegistryForProject(item.ctx, item.projectID)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := registry.Get(preview.CapabilityID); ok {
			t.Fatal("another execution inherited this attempt's approved installation")
		}
	}
	if _, err := store.SetSkillInstallationEnabled(context.Background(), installed.Receipt.InstallationID, false, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadSkillResource(service, project.ProjectID, preview.CapabilityID, preview.Version, "SKILL.md", 0, 1000); err == nil {
		t.Fatal("disabled installation remained readable via its receipt")
	}
}

func TestRunSkillCatalogV42MigrationDoesNotInventLegacySnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	store := openProjectFilesTestStore(t, path)
	project, claim := statefulToolClaimForTest(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`DROP TABLE run_skills; ALTER TABLE runs DROP COLUMN skill_catalog_ready; PRAGMA user_version=42;`)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("v42 fixture: %v %v", err, closeErr)
	}
	store = openProjectFilesTestStore(t, path)
	defer store.Close()
	var ready bool
	if err := store.db.QueryRow(`SELECT skill_catalog_ready FROM runs WHERE run_id=?`, claim.Attempt.RunID).Scan(&ready); err != nil || ready {
		t.Fatalf("legacy snapshot was invented: %v %v", ready, err)
	}
	var backup string
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=42 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil || backup == "" {
		t.Fatalf("backup: %s %v", backup, err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	_, err = store.CapabilityRegistryForProject(statefulCatalogContext(project, claim), project.ProjectID)
	assertDomainCode(t, err, "SKILL_CATALOG_NOT_FROZEN")
	var inputHash string
	if err := store.db.QueryRow(`SELECT input_snapshot_hash FROM execution_attempts WHERE attempt_id=?`, claim.Attempt.AttemptID).Scan(&inputHash); err != nil || inputHash != claim.Attempt.InputSnapshotHash {
		t.Fatalf("existing attempt changed: %s %v", inputHash, err)
	}
	newProject, newClaim := statefulToolClaimForTest(t, store)
	if _, err := store.CapabilityRegistryForProject(statefulCatalogContext(newProject, newClaim), newProject.ProjectID); err != nil {
		t.Fatal(err)
	}
}
