package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func acceptSnapshotTurn(t *testing.T, store *Store, project Project, key string, reference *agentcontract.CapabilityRef) AgentTurn {
	t.Helper()
	turn, err := store.AcceptAgentTurn(context.Background(), project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "Inspect the supplied outline.", CapabilityRef: reference},
		CommandMeta{Scope: project.ProjectID, CommandType: "create_message", IdempotencyKey: key, RequestHash: key})
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

func snapshotTurnContext(turn AgentTurn) context.Context {
	return WithAgentActivity(context.Background(), AgentActivityIdentity{ProjectID: turn.ProjectID, AgentTurnID: turn.AgentTurnID})
}

func assertSnapshotInstructions(t *testing.T, store *Store, ctx context.Context, projectID, capabilityID, version, expected string) {
	t.Helper()
	page, err := store.ReadSkillResource(ctx, projectID, capabilityID, version, "SKILL.md", 0, 16000)
	if err != nil || !strings.Contains(page.Content, expected) {
		t.Fatalf("resource expected=%s page=%+v err=%v", expected, page, err)
	}
}

func TestAgentTurnSkillsPinManagedSelectionBeforeUpgradeAndRespectRevocation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "turns.db")
	store, err := Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	project, err := store.CreateProject(ctx, "Turn snapshots")
	if err != nil {
		t.Fatal(err)
	}
	source := writeSkillTestPackage(t, t.TempDir(), "turn-pin", "turn_pin", "1.0.0", "inline", "ORIGINAL_TURN_PACKAGE")
	installed, err := store.InstallSkillDirectory(ctx, source, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	ref := &agentcontract.CapabilityRef{CapabilityID: installed.CapabilityID, Version: "1.0.0", SelectionMode: "explicit"}
	turn := acceptSnapshotTurn(t, store, project, "managed-turn", ref)
	turnCtx := snapshotTurnContext(turn)
	updated := writeSkillTestPackage(t, t.TempDir(), "turn-pin", "turn_pin", "2.0.0", "inline", "NEW_WORKSPACE_PACKAGE")
	if _, err := store.InstallSkillDirectory(ctx, updated, "fixture"); err != nil {
		t.Fatal(err)
	}
	personal := writeSkillTestPackage(t, t.TempDir(), "turn-pin", "turn_pin", "1.0.0", "inline", "NEW_PERSONAL_SHADOW")
	if _, err := store.InstallSkillDirectory(ctx, personal, "fixture", SkillInstallTarget{Scope: capability.SkillScopeUser}); err != nil {
		t.Fatal(err)
	}
	assertSnapshotInstructions(t, store, turnCtx, project.ProjectID, ref.CapabilityID, "1.0.0", "ORIGINAL_TURN_PACKAGE")
	assertSnapshotInstructions(t, store, ctx, project.ProjectID, ref.CapabilityID, "1.0.0", "NEW_PERSONAL_SHADOW")
	if entry, ok, err := store.capabilityEntryForProjectVersion(turnCtx, project.ProjectID, ref.CapabilityID, "2.0.0"); err != nil || ok {
		t.Fatalf("turn escaped frozen version: %+v found=%v err=%v", entry, ok, err)
	}
	exchange, err := store.CreateMessageExchange(turnCtx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request:        turn.Request,
		Decision:       agentcontract.AgentDecision{Intent: "chat", Reply: "Analysis complete.", Confidence: 1, CapabilityRef: ref},
	})
	if err != nil || exchange.Invocation == nil {
		t.Fatalf("commit original selection: %+v err=%v", exchange, err)
	}
	assertExecutionSkillBinding(t, store, "skill_invocations", "skill_invocation_id", exchange.Invocation.SkillInvocationID, *installed.ActiveVersionID)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotInstructions(t, store, turnCtx, project.ProjectID, ref.CapabilityID, "1.0.0", "ORIGINAL_TURN_PACKAGE")
	if _, err := store.SetSkillInstallationEnabled(ctx, installed.SkillInstallationID, false, "fixture"); err != nil {
		t.Fatal(err)
	}
	registry, err := store.CapabilityRegistryForProject(turnCtx, project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := registry.Get(ref.CapabilityID)
	if !ok || entry.Status != capability.Unavailable || entry.Skill.Scope != capability.SkillScopeWorkspace {
		t.Fatalf("revoked turn fell through to personal: %+v", entry)
	}
	if _, err := store.ReadSkillResource(turnCtx, project.ProjectID, ref.CapabilityID, "1.0.0", "SKILL.md", 0, 1000); err == nil {
		t.Fatal("revoked snapshot resource remained readable")
	}
}

func TestDirectorySkillSnapshotSurvivesMutationDeletionRestartAndBackgroundClaim(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := writeSkillTestPackage(t, root, "directory-pin", "directory_pin", "1.0.0", "background_task", "DIRECTORY_ORIGINAL")
	registry := loadTestRegistry(t)
	if err := registry.AddSkillRoot(capability.SkillRoot{Scope: capability.SkillScopeWorkspace, Path: root}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "directory.db")
	store, err := Open(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	project, err := store.CreateProject(ctx, "Directory snapshots")
	if err != nil {
		t.Fatal(err)
	}
	ref := &agentcontract.CapabilityRef{CapabilityID: "directory_pin", Version: "1.0.0", SelectionMode: "explicit"}
	turn := acceptSnapshotTurn(t, store, project, "directory-first", ref)
	turnCtx := snapshotTurnContext(turn)
	original, err := os.ReadFile(filepath.Join(source, "SKILL.md"))
	if err != nil {
		t.Fatal("snapshot moved or removed source:", err)
	}
	writeSkillTestPackage(t, root, "directory-pin", "directory_pin", "1.0.0", "background_task", "DIRECTORY_CHANGED_SAME_VERSION")
	assertSnapshotInstructions(t, store, ctx, project.ProjectID, ref.CapabilityID, ref.Version, "DIRECTORY_CHANGED_SAME_VERSION")
	assertSnapshotInstructions(t, store, turnCtx, project.ProjectID, ref.CapabilityID, ref.Version, "DIRECTORY_ORIGINAL")
	second := acceptSnapshotTurn(t, store, project, "directory-second", ref)
	assertSnapshotInstructions(t, store, snapshotTurnContext(second), project.ProjectID, ref.CapabilityID, ref.Version, "DIRECTORY_CHANGED_SAME_VERSION")
	exchange, err := store.CreateMessageExchange(turnCtx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID, Request: turn.Request,
		Decision: agentcontract.AgentDecision{Intent: "propose_capability", Reply: "Confirm background task.", Confidence: 1, CapabilityRef: ref,
			ProposedAction: &agentcontract.ProposedActionDraft{ActionType: "start_background_task", CapabilityRef: ref,
				Input: json.RawMessage(`{"topic":"snapshot"}`), Config: json.RawMessage(`{}`), RequiresConfirmation: true}},
	})
	if err != nil || exchange.Invocation == nil || exchange.Action == nil {
		t.Fatalf("directory proposal: %+v err=%v", exchange, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM skill_installations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("directory was silently installed: %d err=%v", count, err)
	}
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
	assertSnapshotInstructions(t, store, turnCtx, project.ProjectID, ref.CapabilityID, ref.Version, "DIRECTORY_ORIGINAL")
	_, err = store.StartAgentTask(ctx, StartAgentTaskCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "start_agent_task", IdempotencyKey: "directory-start", RequestHash: "directory-start"},
		ProjectID:   project.ProjectID, ConversationID: project.PrimaryConversationID, CapabilityID: ref.CapabilityID, CapabilityVersion: ref.Version,
		Input: exchange.Action.Input, Config: exchange.Action.Config, Confirmed: true,
		ProposedActionID: exchange.Action.ProposedActionID, ProposedActionVersion: exchange.Action.Version,
		ConfirmationMessageID: exchange.Action.ConfirmationMessageID, ConfirmationSnapshotHash: exchange.Action.SnapshotHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimAgentTask(ctx, ClaimAgentTaskCommand{WorkerID: "directory-worker", ProviderID: "openai", ModelID: "test", LeaseSeconds: 60})
	if err != nil || claim == nil || claim.Instructions != "DIRECTORY_ORIGINAL" {
		t.Fatalf("directory claim: %+v err=%v", claim, err)
	}
	activityCtx := WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: project.ProjectID, AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken})
	backgroundRegistry, err := store.CapabilityRegistryForProject(activityCtx, project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	backgroundEntry, ok := backgroundRegistry.Get(ref.CapabilityID)
	if !ok || backgroundEntry.Skill == nil || backgroundEntry.Skill.Instructions != "DIRECTORY_ORIGINAL" {
		t.Fatalf("background lost the initiating turn's catalog: %+v", backgroundEntry)
	}
	page, err := store.ReadSkillResource(activityCtx, project.ProjectID, ref.CapabilityID, ref.Version, "SKILL.md", 0, 16000)
	if err != nil || page.Content != string(original) {
		t.Fatalf("background lost directory bytes: %+v err=%v", page, err)
	}
}

func TestAgentTurnFrozenEmptyCatalogDoesNotDiscoverNewSkillsOrLeakToOtherUsers(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "empty.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, _ := store.CreateProject(ctx, "Empty snapshot")
	turn := acceptSnapshotTurn(t, store, project, "empty-turn", nil)
	source := writeSkillTestPackage(t, t.TempDir(), "after-turn", "after_turn", "1.0.0", "inline", "NEW_ONLY")
	if _, err := store.InstallSkillDirectory(ctx, source, "fixture"); err != nil {
		t.Fatal(err)
	}
	registry, err := store.CapabilityRegistryForProject(snapshotTurnContext(turn), project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("after_turn"); ok {
		t.Fatal("new Skill appeared inside frozen empty catalog")
	}
	if _, ok := registry.Get("novel_to_script"); !ok {
		t.Fatal("freezing Skills removed built-in workflows")
	}
	foreign := identity.WithPrincipal(snapshotTurnContext(turn), identity.Principal{Kind: identity.KindUser, UserID: "different-user", WorkspaceID: identity.DefaultWorkspaceID, Role: identity.RoleEditor})
	_, err = store.CapabilityRegistryForProject(foreign, project.ProjectID)
	assertDomainCode(t, err, "AGENT_ACTIVITY_INVALID")
}

func TestUnselectedUnarchivableDirectoryDoesNotBlockChatAndRollbackArchivesAreCleaned(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := writeSkillTestPackage(t, root, "archive-policy", "archive_policy", "1.0.0", "inline", "SAFE_INSTRUCTIONS")
	if err := os.WriteFile(filepath.Join(source, "unexpected.bin"), []byte("not accepted by import policy"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := loadTestRegistry(t)
	if err := registry.AddSkillRoot(capability.SkillRoot{Scope: capability.SkillScopeWorkspace, Path: root}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "isolation.db")
	store, err := Open(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	project, _ := store.CreateProject(ctx, "Archive failure isolation")
	turn := acceptSnapshotTurn(t, store, project, "ordinary-chat", nil)
	frozen, err := store.CapabilityRegistryForProject(snapshotTurnContext(turn), project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := frozen.Get("archive_policy")
	if !ok || entry.Status != capability.Unavailable {
		t.Fatalf("failed archive not marked unavailable: %+v", entry)
	}
	_, err = store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{
		Content: "Explicitly use archive policy.", CapabilityRef: &agentcontract.CapabilityRef{CapabilityID: "archive_policy", Version: "1.0.0", SelectionMode: "explicit"},
	}, CommandMeta{Scope: project.ProjectID, CommandType: "create_message", IdempotencyKey: "explicit-invalid", RequestHash: "explicit-invalid"})
	assertDomainCode(t, err, "SKILL_PACKAGE_FILE_UNSUPPORTED")
	if err := os.Remove(filepath.Join(source, "unexpected.bin")); err != nil {
		t.Fatal(err)
	}
	live, err := store.CapabilityRegistryForProject(ctx, project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ = live.Get("archive_policy")
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, snapshotID, err := store.pinExecutionSkillTx(ctx, tx, project.ProjectID, entry)
	tx.Rollback()
	if err != nil || snapshotID == nil {
		t.Fatalf("pin before rollback: %v", err)
	}
	orphanRoot := filepath.Join(store.skillDataRoot, "execution", identity.DefaultWorkspaceID, *snapshotID)
	if _, err := os.Stat(orphanRoot); err != nil {
		t.Fatal(err)
	}
	committed := acceptSnapshotTurn(t, store, project, "committed-archive", nil)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphanRoot); !os.IsNotExist(err) {
		t.Fatalf("uncommitted archive was not removed: %v", err)
	}
	assertSnapshotInstructions(t, store, snapshotTurnContext(committed), project.ProjectID, "archive_policy", "1.0.0", "SAFE_INSTRUCTIONS")
}
