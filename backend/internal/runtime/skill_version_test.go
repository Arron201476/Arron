package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStandardSkillLegacySnapshotSurvivesUpgradeAndRestart(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "content_agent.db")
	store, err := Open(databasePath, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	source := writeSkillTestPackage(t, filepath.Join(root, "source"), "standard-upgrade", "standard_upgrade", "", "inline", "Original instructions.")
	if err := os.Remove(filepath.Join(source, "content-agent", "manifest.json")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	installed, err := store.InstallSkillDirectory(ctx, source, "user_test")
	if err != nil {
		t.Fatal(err)
	}
	first := activeSkillVersionForTest(t, installed)
	// Recreate the pre-content-version database while keeping its archive intact.
	if _, err := store.db.ExecContext(ctx, `UPDATE skill_versions SET version = '0.0.0' WHERE skill_version_id = ?`, first.SkillVersionID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceRegistrySkillVersion(t, store, installed.CapabilityID, "0.0.0", true)
	if _, skill, err := store.inspectManagedSkillPackage(first.packageRef, installed.SkillName, installed.CapabilityID, "0.0.0", "inline", first.ContentHash); err != nil || skill.Version != "0.0.0" {
		t.Fatalf("legacy archive: %v / %v", skill, err)
	}
	if err := os.MkdirAll(filepath.Join(source, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "references", "note.md"), []byte("new reference"), 0o644); err != nil {
		t.Fatal(err)
	}
	updated, err := store.InstallSkillDirectory(ctx, source, "user_test")
	if err != nil {
		t.Fatal(err)
	}
	second := activeSkillVersionForTest(t, updated)
	if len(updated.Versions) != 2 || second.Version == first.Version || second.Version == "0.0.0" {
		t.Fatalf("updated versions: %+v", updated.Versions)
	}
	if _, err := store.ActivateSkillVersion(ctx, installed.SkillInstallationID, "0.0.0", "user_test"); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceRegistrySkillVersion(t, store, installed.CapabilityID, "0.0.0", true)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceRegistrySkillVersion(t, store, installed.CapabilityID, "0.0.0", true)
}
