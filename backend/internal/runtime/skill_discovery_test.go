package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func TestDirectorySkillDiscoveryManagementAndUninstallTombstone(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "discovered")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	loadRegistry := func() *capability.Registry {
		t.Helper()
		registry := loadTestRegistry(t)
		if err := registry.AddSkillRoot(capability.SkillRoot{Scope: capability.SkillScopeWorkspace, Path: skillRoot, Priority: 200}); err != nil {
			t.Fatal(err)
		}
		if err := registry.AddSkillRoot(capability.SkillRoot{Scope: capability.SkillScopeWorkspace, WorkspaceID: "discovery_other_workspace", ScopeRef: "discovery_other_workspace", Path: filepath.Join(root, "other-discovered"), Priority: 200}); err != nil {
			t.Fatal(err)
		}
		return registry
	}
	databasePath := filepath.Join(root, "content_agent.db")
	store, err := Open(databasePath, loadRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	source := writeSkillTestPackage(t, skillRoot, "discovered-skill", "discovered_skill", "1.0.0", "inline", "Directory instructions.")
	if _, ok := workspaceRegistryForTest(t, store).Get("discovered_skill"); ok {
		t.Fatal("fixture was already in cached registry")
	}
	if _, err := store.RefreshWorkspaceSkills(ctx); err != nil {
		t.Fatal(err)
	}
	entry, ok := workspaceRegistryForTest(t, store).Get("discovered_skill")
	if !ok || entry.Skill == nil {
		t.Fatal("refresh did not discover new directory")
	}
	if _, err := store.InstallDiscoveredSkill(ctx, entry.CapabilityID, entry.Skill.Version, "sha256:stale", ""); err == nil {
		t.Fatal("accepted stale snapshot")
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: discovered-skill\ndescription: Updated description.\n---\nUpdated instructions.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = store.InstallDiscoveredSkill(ctx, entry.CapabilityID, entry.Skill.Version, entry.Skill.ContentHash, "")
	assertDomainCode(t, err, "SKILL_DISCOVERY_CHANGED")
	if _, err := store.RefreshWorkspaceSkills(ctx); err != nil {
		t.Fatal(err)
	}
	entry, _ = workspaceRegistryForTest(t, store).Get("discovered_skill")
	installed, err := store.InstallDiscoveredSkill(ctx, entry.CapabilityID, entry.Skill.Version, entry.Skill.ContentHash, "")
	if err != nil {
		t.Fatal(err)
	}
	managed, _ := workspaceRegistryForTest(t, store).Get("discovered_skill")
	if managed.Skill.Directory == source || managed.Skill.ContentHash != entry.Skill.ContentHash {
		t.Fatal("adoption did not create an independent immutable copy")
	}
	other := identity.Principal{Kind: identity.KindUser, UserID: "discovery_other", WorkspaceID: "discovery_other_workspace", Role: identity.RoleOwner}
	writeSkillTestPackage(t, filepath.Join(root, "other-discovered"), "discovered-skill", "discovered_skill", "1.0.0", "inline", "Other workspace instructions.")
	if err := store.BootstrapPrincipal(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UninstallSkill(ctx, installed.SkillInstallationID, ""); err != nil {
		t.Fatal(err)
	}
	assertDisabled := func() {
		t.Helper()
		current, found := workspaceRegistryForTest(t, store).Get("discovered_skill")
		if !found || current.ReasonCode != "SKILL_DISABLED" {
			t.Fatalf("uninstall reactivated directory: %+v", current)
		}
	}
	assertDisabled()
	if _, err := store.RefreshWorkspaceSkills(ctx); err != nil {
		t.Fatal(err)
	}
	assertDisabled()
	otherRegistry, err := store.CapabilityRegistryForWorkspace(ctx, other.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	otherEntry, _ := otherRegistry.Get("discovered_skill")
	if otherEntry.Status != capability.Available || otherEntry.Skill.Instructions != "Other workspace instructions." {
		t.Fatal("workspace uninstall disabled a different workspace")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath, loadRegistry())
	if err != nil {
		t.Fatal(err)
	}
	assertDisabled()
	reinstalled, err := store.InstallDiscoveredSkill(ctx, entry.CapabilityID, entry.Skill.Version, entry.Skill.ContentHash, "")
	if err != nil || reinstalled.SkillInstallationID != installed.SkillInstallationID || !reinstalled.Enabled {
		t.Fatalf("explicit reinstall: %+v, %v", reinstalled, err)
	}
}
