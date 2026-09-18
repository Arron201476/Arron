package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func TestScopedSkillLifecycleIsolatedAndPersistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scopes.db")
	store, err := Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Scoped lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	bob := identity.Principal{Kind: identity.KindUser, UserID: "user_scope_bob", WorkspaceID: identity.DefaultWorkspaceID, Role: identity.RoleEditor}
	if err := store.BootstrapPrincipal(ctx, bob); err != nil {
		t.Fatal(err)
	}
	bobCtx := identity.WithPrincipal(ctx, bob)
	install := func(ctx context.Context, scope capability.SkillScope, version, body string) SkillInstallation {
		t.Helper()
		directory := writeSkillTestPackage(t, t.TempDir(), "lifecycle-scope", "lifecycle_scope", version, "inline", body)
		target := SkillInstallTarget{Scope: scope}
		if scope == capability.SkillScopeProject {
			target.ProjectID = project.ProjectID
		}
		item, err := store.InstallSkillDirectory(ctx, directory, "", target)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	workspace := install(ctx, capability.SkillScopeWorkspace, "1.0.0", "WORKSPACE")
	projectSkill := install(bobCtx, capability.SkillScopeProject, "1.0.0", "PROJECT")
	alice := install(ctx, capability.SkillScopeUser, "1.0.0", "ALICE")
	bobSkill := install(bobCtx, capability.SkillScopeUser, "1.0.0", "BOB")
	for _, item := range []SkillInstallation{workspace, projectSkill, alice, bobSkill} {
		root, err := store.scopedSkillActiveRoot(installationScope(item))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, item.SkillName, "SKILL.md")); err != nil {
			t.Fatal(err)
		}
	}
	for _, caller := range []context.Context{ctx, bobCtx} {
		items, err := store.ListSkillInstallations(caller, true)
		if err != nil || len(items) != 3 {
			t.Fatalf("visible installations=%d err=%v", len(items), err)
		}
		attempts, err := store.ListSkillInstallAttempts(caller, 100)
		if err != nil || len(attempts) != 3 {
			t.Fatalf("visible attempts=%d err=%v", len(attempts), err)
		}
	}
	_, err = store.GetSkillInstallation(bobCtx, alice.SkillInstallationID)
	assertDomainCode(t, err, "SKILL_INSTALLATION_NOT_FOUND")
	_, err = store.SetSkillInstallationEnabled(bobCtx, workspace.SkillInstallationID, false, "")
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
	_, err = store.UninstallSkill(bobCtx, alice.SkillInstallationID, "")
	assertDomainCode(t, err, "SKILL_INSTALLATION_NOT_FOUND")
	_, err = store.UpgradeSkillZIP(bobCtx, alice.SkillInstallationID, "private.zip", bytes.NewBufferString("invalid"), "")
	assertDomainCode(t, err, "SKILL_INSTALLATION_NOT_FOUND")
	install(bobCtx, capability.SkillScopeUser, "2.0.0", "BOB2")
	rolled, err := store.ActivateSkillVersion(bobCtx, bobSkill.SkillInstallationID, "1.0.0", "")
	if err != nil || activeSkillVersionForTest(t, rolled).Version != "1.0.0" {
		t.Fatalf("rollback=%+v %v", rolled, err)
	}
	if _, err := store.SetSkillInstallationEnabled(bobCtx, bobSkill.SkillInstallationID, false, ""); err != nil {
		t.Fatal(err)
	}
	assertSelection := func(ctx context.Context, body string, status capability.Availability) {
		t.Helper()
		registry, err := store.CapabilityRegistryForProject(ctx, project.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		entry, ok := registry.Get("lifecycle_scope")
		if !ok || entry.Status != status || entry.Skill.Instructions != body {
			t.Fatalf("selection=%+v", entry)
		}
	}
	assertSelection(ctx, "ALICE", capability.Available)
	assertSelection(bobCtx, "BOB", capability.Unavailable)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	assertSelection(ctx, "ALICE", capability.Available)
	assertSelection(bobCtx, "BOB", capability.Unavailable)
	if _, err := store.SetSkillInstallationEnabled(bobCtx, bobSkill.SkillInstallationID, true, ""); err != nil {
		t.Fatal(err)
	}
	assertSelection(bobCtx, "BOB", capability.Available)
	if _, err := store.UninstallSkill(bobCtx, bobSkill.SkillInstallationID, ""); err != nil {
		t.Fatal(err)
	}
	assertSelection(ctx, "ALICE", capability.Available)
	assertSelection(bobCtx, "BOB2", capability.Unavailable)
	_, err = store.InstallSkillZIP(bobCtx, "bob-secret.zip", bytes.NewBufferString("invalid"), "", SkillInstallTarget{Scope: capability.SkillScopeUser})
	if err == nil {
		t.Fatal("accepted invalid ZIP")
	}
	for _, caller := range []context.Context{ctx, bobCtx} {
		attempts, err := store.ListSkillInstallAttempts(caller, 100)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, attempt := range attempts {
			if attempt.SourceName == "bob-secret.zip" {
				found = true
			}
		}
		if found != (caller == bobCtx) {
			t.Fatal("private failed attempt visibility incorrect")
		}
	}
	preview, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmProjectDelete(ctx, ConfirmProjectDeleteCommand{ProjectID: project.ProjectID, PreviewHash: preview.SnapshotHash, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	projectRoot, err := store.scopedSkillActiveRoot(installationScope(projectSkill))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(projectRoot); !os.IsNotExist(err) {
		t.Fatalf("deleted project still has an active Skill directory: %v", err)
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM skill_installations WHERE skill_installation_id = ?`, projectSkill.SkillInstallationID).Scan(&status); err != nil || status != "uninstalled" {
		t.Fatalf("project installation status=%s err=%v", status, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(projectRoot); !os.IsNotExist(err) {
		t.Fatal("restart reactivated deleted project Skill")
	}
	if _, err := store.GetSkillInstallation(ctx, workspace.SkillInstallationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSkillInstallation(ctx, alice.SkillInstallationID); err != nil {
		t.Fatal(err)
	}
}

func TestScopedInstallCannotBypassWorkspaceQuotaWithSameName(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "quota.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	directory := writeSkillTestPackage(t, t.TempDir(), "quota-scope", "quota_scope", "1.0.0", "inline", "quota")
	if _, err := store.InstallSkillDirectory(ctx, directory, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE workspace_quotas SET max_installed_skills = 1 WHERE workspace_id = ?`, identity.DefaultWorkspaceID); err != nil {
		t.Fatal(err)
	}
	_, err = store.InstallSkillDirectory(ctx, directory, "", SkillInstallTarget{Scope: capability.SkillScopeUser})
	assertDomainCode(t, err, "WORKSPACE_QUOTA_EXCEEDED")
	if _, err := store.InstallSkillDirectory(ctx, directory, ""); err != nil {
		t.Fatal("same-scope reinstall should not consume another quota slot:", err)
	}
}

func TestSkillAttemptScopeMigrationFromV35PreservesHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	store, err := Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	directory := writeSkillTestPackage(t, t.TempDir(), "migration-scope", "migration_scope", "1.0.0", "inline", "migration")
	if _, err := store.InstallSkillDirectory(ctx, directory, "historical-actor"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`ALTER TABLE skill_install_attempts DROP COLUMN scope; ALTER TABLE skill_install_attempts DROP COLUMN scope_ref; PRAGMA user_version=35;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := store.ListSkillInstallAttempts(ctx, 10)
	if err != nil || len(attempts) != 1 || attempts[0].Scope != "workspace" || attempts[0].ScopeRef != identity.DefaultWorkspaceID || attempts[0].Status != "completed" {
		t.Fatalf("migrated attempts=%+v err=%v", attempts, err)
	}
	var backup string
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version = 35 AND to_version = ? AND status = 'completed'`, schemaVersion).Scan(&backup); err != nil || backup == "" {
		t.Fatalf("migration backup=%q err=%v", backup, err)
	}
}

func TestBrokenScopedSkillDoesNotBreakCatalogOrFallBack(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "broken.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Broken Skill")
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []capability.SkillScope{capability.SkillScopeWorkspace, capability.SkillScopeUser} {
		directory := writeSkillTestPackage(t, t.TempDir(), "broken-scope", "broken_scope", "1.0.0", "inline", string(scope))
		item, err := store.InstallSkillDirectory(ctx, directory, "", SkillInstallTarget{Scope: scope})
		if err != nil {
			t.Fatal(err)
		}
		if scope == capability.SkillScopeUser {
			version := activeSkillVersionForTest(t, item)
			packagePath, err := resolveDataPath(store.dataRoot, version.packageRef)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(packagePath, "SKILL.md"), []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	registry, err := store.CapabilityRegistryForProject(ctx, project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	entry, found := registry.Get("broken_scope")
	if !found || entry.Status != capability.Unavailable || entry.ReasonCode != "SKILL_PACKAGE_UNAVAILABLE" || entry.Skill.Scope != capability.SkillScopeUser {
		t.Fatalf("broken selection=%+v", entry)
	}
	if _, _, err := registry.PublicDefinitionWithSchemas("broken_scope"); err != nil {
		t.Fatal(err)
	}
}
