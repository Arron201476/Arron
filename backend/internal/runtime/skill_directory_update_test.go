package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func directoryUpdateCommand(preview SkillDirectoryUpdate) UpdateSkillFromDirectoryCommand {
	return UpdateSkillFromDirectoryCommand{ExpectedActiveVersionID: preview.CurrentVersionID, Version: preview.Version, ContentHash: preview.ContentHash}
}

func TestNativeDirectoryUpdatePreservesDisabledScopeAndRejectsStalePreview(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := writeSkillTestPackage(t, root, "native-update", "native_update", "1.0.0", "inline", "ORIGINAL")
	if err := os.Remove(filepath.Join(source, "content-agent", "manifest.json")); err != nil {
		t.Fatal(err)
	}
	registry := loadTestRegistry(t)
	if err := registry.AddSkillRoot(capability.SkillRoot{Scope: capability.SkillScopeWorkspace, Path: root}); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(t.TempDir(), "directory-update.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	selected, _ := registry.Get("native_update")
	installed, err := store.InstallDiscoveredSkill(ctx, selected.Skill.CapabilityID, selected.Skill.Version, selected.Skill.ContentHash, "fixture", SkillInstallTarget{Scope: capability.SkillScopeUser})
	if err != nil {
		t.Fatal(err)
	}
	firstID := *installed.ActiveVersionID
	current, err := store.PreviewSkillDirectoryUpdate(ctx, installed.SkillInstallationID)
	if err != nil || current.Status != "current" {
		t.Fatalf("current source: %+v err=%v", current, err)
	}
	writeSource := func(instructions string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: native-update\ndescription: Native update fixture.\n---\n\n"+instructions+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSource("SECOND")
	preview, err := store.PreviewSkillDirectoryUpdate(ctx, installed.SkillInstallationID)
	if err != nil || preview.Status != "available" || preview.Version == selected.Skill.Version || preview.SourcePath == "" {
		t.Fatalf("updated source: %+v err=%v", preview, err)
	}
	writeSource("THIRD")
	_, err = store.UpdateSkillFromDirectory(ctx, installed.SkillInstallationID, directoryUpdateCommand(preview), "fixture")
	assertDomainCode(t, err, "SKILL_DISCOVERY_CHANGED")
	preview, err = store.PreviewSkillDirectoryUpdate(ctx, installed.SkillInstallationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSkillInstallationEnabled(ctx, installed.SkillInstallationID, false, "fixture"); err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateSkillFromDirectory(ctx, installed.SkillInstallationID, directoryUpdateCommand(preview), "fixture")
	if err != nil || updated.Enabled || updated.Scope != "user" || updated.ScopeRef != identity.DefaultUserID || updated.SkillInstallationID != installed.SkillInstallationID || *updated.ActiveVersionID == firstID {
		t.Fatalf("directory update changed disabled scope: %+v err=%v", updated, err)
	}
	if len(updated.Versions) != 2 {
		t.Fatalf("unexpected history: %+v", updated.Versions)
	}
	_, err = store.UpdateSkillFromDirectory(ctx, installed.SkillInstallationID, directoryUpdateCommand(preview), "fixture")
	assertDomainCode(t, err, "SKILL_DISCOVERY_CHANGED")
	other := identity.Principal{Kind: identity.KindUser, UserID: "directory-other", WorkspaceID: identity.DefaultWorkspaceID, Role: identity.RoleAdmin}
	if err := store.BootstrapPrincipal(ctx, other); err != nil {
		t.Fatal(err)
	}
	_, err = store.PreviewSkillDirectoryUpdate(identity.WithPrincipal(ctx, other), installed.SkillInstallationID)
	assertDomainCode(t, err, "SKILL_INSTALLATION_NOT_FOUND")
}

func TestDirectoryUpdateChecksExplicitVersionAndInstallCommitPrecondition(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := writeSkillTestPackage(t, root, "explicit-update", "explicit_update", "1.0.0", "inline", "ORIGINAL")
	registry := loadTestRegistry(t)
	if err := registry.AddSkillRoot(capability.SkillRoot{Scope: capability.SkillScopeWorkspace, Path: root}); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(t.TempDir(), "explicit-update.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	installed, err := store.InstallSkillDirectory(ctx, source, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	writeSkillTestPackage(t, root, "explicit-update", "explicit_update", "1.0.0", "inline", "SAME_VERSION_MUTATION")
	preview, err := store.PreviewSkillDirectoryUpdate(ctx, installed.SkillInstallationID)
	if err != nil || preview.Status != "version_conflict" {
		t.Fatalf("explicit conflict: %+v err=%v", preview, err)
	}
	_, err = store.UpdateSkillFromDirectory(ctx, installed.SkillInstallationID, directoryUpdateCommand(preview), "fixture")
	assertDomainCode(t, err, "SKILL_VERSION_IMMUTABLE")
	writeSkillTestPackage(t, root, "explicit-update", "explicit_update", "2.0.0", "inline", "SECOND_VERSION")
	preview, err = store.PreviewSkillDirectoryUpdate(ctx, installed.SkillInstallationID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateSkillFromDirectory(ctx, installed.SkillInstallationID, directoryUpdateCommand(preview), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.installSkillChecked(ctx, "directory", "fixture", "fixture", installed.SkillInstallationID, *updated.ActiveVersionID, func(quarantineRoot string) (*capability.SkillPackage, error) {
		if _, err := store.ActivateSkillVersion(ctx, installed.SkillInstallationID, "1.0.0", "fixture"); err != nil {
			return nil, err
		}
		return prepareSkillDirectory(registry.ProjectRoot(), quarantineRoot, source)
	})
	assertDomainCode(t, err, "SKILL_DISCOVERY_CHANGED")
	after, err := store.GetSkillInstallation(ctx, installed.SkillInstallationID)
	if err != nil || *after.ActiveVersionID != *installed.ActiveVersionID {
		t.Fatalf("concurrent activation was overwritten: %+v err=%v", after, err)
	}
	editor := identity.Principal{Kind: identity.KindUser, UserID: "directory-editor", WorkspaceID: identity.DefaultWorkspaceID, Role: identity.RoleEditor}
	if err := store.BootstrapPrincipal(ctx, editor); err != nil {
		t.Fatal(err)
	}
	_, err = store.PreviewSkillDirectoryUpdate(identity.WithPrincipal(ctx, editor), installed.SkillInstallationID)
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
}
