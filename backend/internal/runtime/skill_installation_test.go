package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
)

func TestSkillInstallationLifecycleAndRestartRecovery(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "content_agent.db")
	registry := loadTestRegistry(t)
	store, err := Open(databasePath, registry)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	versionOneSource := writeSkillTestPackage(
		t, filepath.Join(root, "source-v1"), "install-test-skill",
		"install_test_skill", "1.0.0", "inline", "Always answer with the installed v1 policy.",
	)
	installed, err := store.InstallSkillDirectory(
		context.Background(), versionOneSource, "user_install_test",
	)
	if err != nil {
		t.Fatalf("InstallSkillDirectory(v1) error = %v", err)
	}
	if installed.Status != "installed" || !installed.Enabled || installed.RegistryStatus != string(capability.Available) {
		t.Fatalf("installed = %+v", installed)
	}
	if installed.CreatedBy != "user_install_test" || len(installed.Versions) != 1 || len(installed.Events) != 1 {
		t.Fatalf("installed metadata = %+v", installed)
	}
	versionOne := activeSkillVersionForTest(t, installed)
	if versionOne.Version != "1.0.0" || versionOne.SourceType != "directory" || versionOne.ContentHash == "" {
		t.Fatalf("version one = %+v", versionOne)
	}
	assertWorkspaceRegistrySkillVersion(t, store, installed.CapabilityID, "1.0.0", true)
	assertManagedSkillFiles(t, store, installed.SkillName, true)
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(versionOne.packageRef), "SKILL.md")); err != nil {
		t.Fatalf("immutable package missing: %v", err)
	}

	reused, err := store.InstallSkillDirectory(
		context.Background(), versionOneSource, "user_install_test",
	)
	if err != nil {
		t.Fatalf("InstallSkillDirectory(reuse) error = %v", err)
	}
	if reused.SkillInstallationID != installed.SkillInstallationID || len(reused.Versions) != 1 {
		t.Fatalf("reused = %+v", reused)
	}
	if !skillEventTypes(reused.Events)["skill.version.reused"] {
		t.Fatalf("events = %+v, want reused event", reused.Events)
	}

	changedVersionOne := writeSkillTestPackage(
		t, filepath.Join(root, "source-v1-changed"), "install-test-skill",
		"install_test_skill", "1.0.0", "inline", "Changed content must not replace immutable v1.",
	)
	_, err = store.InstallSkillDirectory(context.Background(), changedVersionOne, "user_install_test")
	assertDomainCode(t, err, "SKILL_VERSION_IMMUTABLE")
	current, err := store.GetSkillInstallation(context.Background(), installed.SkillInstallationID)
	if err != nil {
		t.Fatalf("GetSkillInstallation() error = %v", err)
	}
	if activeSkillVersionForTest(t, current).ContentHash != versionOne.ContentHash {
		t.Fatalf("immutable version changed: %+v", current.Versions)
	}
	assertWorkspaceRegistrySkillVersion(t, store, installed.CapabilityID, "1.0.0", true)

	versionTwoSource := writeSkillTestPackage(
		t, filepath.Join(root, "source-v2"), "install-test-skill",
		"install_test_skill", "2.0.0", "inline", "Always answer with the installed v2 policy.",
	)
	upgraded, err := store.InstallSkillDirectory(context.Background(), versionTwoSource, "user_upgrade_test")
	if err != nil {
		t.Fatalf("InstallSkillDirectory(v2) error = %v", err)
	}
	if len(upgraded.Versions) != 2 || activeSkillVersionForTest(t, upgraded).Version != "2.0.0" {
		t.Fatalf("upgraded = %+v", upgraded)
	}
	assertWorkspaceRegistrySkillVersion(t, store, installed.CapabilityID, "2.0.0", true)

	rolledBack, err := store.ActivateSkillVersion(
		context.Background(), installed.SkillInstallationID, "1.0.0", "user_rollback_test",
	)
	if err != nil {
		t.Fatalf("ActivateSkillVersion() error = %v", err)
	}
	if activeSkillVersionForTest(t, rolledBack).ContentHash != versionOne.ContentHash {
		t.Fatalf("rolled back = %+v", rolledBack)
	}
	assertWorkspaceRegistrySkillVersion(t, store, installed.CapabilityID, "1.0.0", true)

	disabled, err := store.SetSkillInstallationEnabled(
		context.Background(), installed.SkillInstallationID, false, "user_disable_test",
	)
	if err != nil {
		t.Fatalf("SetSkillInstallationEnabled(false) error = %v", err)
	}
	if disabled.Enabled || disabled.RegistryReasonCode != "SKILL_DISABLED" {
		t.Fatalf("disabled = %+v", disabled)
	}
	assertWorkspaceRegistrySkillVersion(t, store, installed.CapabilityID, "1.0.0", false)

	for _, version := range []string{"2.0.0", "1.0.0"} {
		switched, err := store.ActivateSkillVersion(context.Background(), installed.SkillInstallationID, version, "user_disabled_version_test")
		if err != nil {
			t.Fatalf("ActivateSkillVersion(disabled, %s): %v", version, err)
		}
		if switched.Enabled || switched.Status != "installed" || switched.RegistryReasonCode != "SKILL_DISABLED" {
			t.Fatalf("switching versions changed disabled state: %+v", switched)
		}
		assertWorkspaceRegistrySkillVersion(t, store, installed.CapabilityID, version, false)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	registry = loadTestRegistry(t)
	store, err = Open(databasePath, registry)
	if err != nil {
		t.Fatalf("Open(restart) error = %v", err)
	}
	defer store.Close()
	restarted, err := store.GetSkillInstallation(context.Background(), installed.SkillInstallationID)
	if err != nil {
		t.Fatalf("GetSkillInstallation(restart) error = %v", err)
	}
	if restarted.Enabled || restarted.RegistryReasonCode != "SKILL_DISABLED" {
		t.Fatalf("restarted = %+v", restarted)
	}
	assertWorkspaceRegistrySkillVersion(t, store, installed.CapabilityID, "1.0.0", false)

	enabled, err := store.SetSkillInstallationEnabled(
		context.Background(), installed.SkillInstallationID, true, "user_enable_test",
	)
	if err != nil {
		t.Fatalf("SetSkillInstallationEnabled(true) error = %v", err)
	}
	if !enabled.Enabled || enabled.RegistryStatus != string(capability.Available) {
		t.Fatalf("enabled = %+v", enabled)
	}
	assertWorkspaceRegistrySkillVersion(t, store, installed.CapabilityID, "1.0.0", true)

	uninstalled, err := store.UninstallSkill(
		context.Background(), installed.SkillInstallationID, "user_uninstall_test",
	)
	if err != nil {
		t.Fatalf("UninstallSkill() error = %v", err)
	}
	if uninstalled.Status != "uninstalled" || uninstalled.Enabled || uninstalled.ActiveVersionID != nil {
		t.Fatalf("uninstalled = %+v", uninstalled)
	}
	workspaceRegistry := workspaceRegistryForTest(t, store)
	if _, ok := workspaceRegistry.Get(installed.CapabilityID); ok {
		t.Fatalf("uninstalled Skill remains in registry")
	}
	assertManagedSkillFiles(t, store, installed.SkillName, false)
	visible, err := store.ListSkillInstallations(context.Background(), false)
	if err != nil || len(visible) != 0 {
		t.Fatalf("visible installations = %+v, err = %v", visible, err)
	}
	all, err := store.ListSkillInstallations(context.Background(), true)
	if err != nil || len(all) != 1 || len(all[0].Versions) != 2 {
		t.Fatalf("all installations = %+v, err = %v", all, err)
	}
	eventTypes := skillEventTypes(all[0].Events)
	for _, eventType := range []string{
		"skill.version.installed", "skill.version.reused", "skill.version.activated",
		"skill.disabled", "skill.enabled", "skill.uninstalled",
	} {
		if !eventTypes[eventType] {
			t.Fatalf("missing event %s in %+v", eventType, all[0].Events)
		}
	}
}

func TestSkillInstallationStatefulModeAndAttemptDiagnostics(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "content_agent.db")
	registry := loadTestRegistry(t)
	store, err := Open(databasePath, registry)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	projectRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if override := os.Getenv("CONTENT_AGENT_TEST_PROJECT_ROOT"); override != "" {
		projectRoot = override
	}
	statefulSource := filepath.Join(
		projectRoot, "fixtures", "skills", "stateful", "story-review-workflow",
	)
	installed, err := store.InstallSkillDirectory(context.Background(), statefulSource, "user_stateful_test")
	if err != nil {
		t.Fatalf("InstallSkillDirectory(stateful) error = %v", err)
	}
	if installed.Status != "installed" || !installed.Enabled ||
		installed.RegistryStatus != string(capability.Available) {
		t.Fatalf("stateful installation = %+v", installed)
	}
	workspaceRegistry := workspaceRegistryForTest(t, store)
	if entry, ok := workspaceRegistry.Get(installed.CapabilityID); !ok ||
		entry.Definition == nil || entry.Definition.ExecutionMode != "stateful_workflow" {
		t.Fatalf("installed stateful Skill registry entry = %+v, present = %v", entry, ok)
	}
	assertManagedSkillFiles(t, store, installed.SkillName, true)

	badSource := filepath.Join(root, "bad-source", "bad-skill")
	if err := os.MkdirAll(badSource, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badSource, "SKILL.md"), []byte("not frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = store.InstallSkillDirectory(context.Background(), badSource, "user_bad_test")
	assertDomainCode(t, err, "SKILL_PACKAGE_INVALID")
	attempts, err := store.ListSkillInstallAttempts(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListSkillInstallAttempts() error = %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d, want 2: %+v", len(attempts), attempts)
	}
	var failed *SkillInstallAttempt
	for index := range attempts {
		if attempts[index].Status == "failed" {
			failed = &attempts[index]
			break
		}
	}
	if failed == nil || failed.FailureCode != "SKILL_PACKAGE_INVALID" ||
		len(failed.Diagnostics) != 1 || failed.Diagnostics[0].Code != "SKILL_PACKAGE_INVALID" {
		t.Fatalf("failed attempt = %+v", failed)
	}
	assertDirectoryEmpty(t, filepath.Join(store.skillDataRoot, "quarantine"))
}

func TestSkillInstallationRecoversInterruptedAttemptAndTampering(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "content_agent.db")
	registry := loadTestRegistry(t)
	store, err := Open(databasePath, registry)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	source := writeSkillTestPackage(
		t, filepath.Join(root, "source"), "tamper-test-skill",
		"tamper_test_skill", "1.0.0", "inline", "Original trusted instructions.",
	)
	installed, err := store.InstallSkillDirectory(context.Background(), source, "user_tamper_test")
	if err != nil {
		t.Fatalf("InstallSkillDirectory() error = %v", err)
	}
	version := activeSkillVersionForTest(t, installed)
	if _, err := store.db.Exec(`
		INSERT INTO skill_install_attempts(
			skill_install_attempt_id, workspace_id, source_type, source_name,
			status, diagnostics_json, created_by, created_at
		) VALUES('ski_interrupted', ?, 'zip', 'interrupted.zip', 'validating', '[]', 'user_test', ?)`,
		SharedWorkspaceID, formatTime(store.now()),
	); err != nil {
		t.Fatalf("seed interrupted attempt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(store.skillDataRoot, "quarantine", "stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store.skillDataRoot, "activation", "stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	packagePath := filepath.Join(root, filepath.FromSlash(version.packageRef), "SKILL.md")
	if err := os.WriteFile(packagePath, []byte("tampered"), 0o644); err != nil {
		t.Fatalf("tamper package: %v", err)
	}
	registry = loadTestRegistry(t)
	store, err = Open(databasePath, registry)
	if err != nil {
		t.Fatalf("Open(recovery) error = %v", err)
	}
	defer store.Close()
	recovered, err := store.GetSkillInstallation(context.Background(), installed.SkillInstallationID)
	if err != nil {
		t.Fatalf("GetSkillInstallation() error = %v", err)
	}
	if recovered.Status != "broken" || recovered.Enabled {
		t.Fatalf("recovered installation = %+v", recovered)
	}
	workspaceRegistry := workspaceRegistryForTest(t, store)
	if _, ok := workspaceRegistry.Get(installed.CapabilityID); ok {
		t.Fatalf("tampered Skill remains registered")
	}
	assertManagedSkillFiles(t, store, installed.SkillName, false)
	assertDirectoryEmpty(t, filepath.Join(store.skillDataRoot, "quarantine"))
	assertDirectoryEmpty(t, filepath.Join(store.skillDataRoot, "activation"))
	attempts, err := store.ListSkillInstallAttempts(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	interruptedIndex := slices.IndexFunc(attempts, func(item SkillInstallAttempt) bool {
		return item.SkillInstallAttemptID == "ski_interrupted"
	})
	if interruptedIndex < 0 || attempts[interruptedIndex].FailureCode != "SKILL_INSTALL_INTERRUPTED" {
		t.Fatalf("interrupted attempt not recovered: %+v", attempts)
	}
}

func TestSkillVersionActivationRejectsTamperedInactivePackage(t *testing.T) {
	root := t.TempDir()
	registry := loadTestRegistry(t)
	store, err := Open(filepath.Join(root, "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	v1Source := writeSkillTestPackage(
		t, filepath.Join(root, "v1"), "activation-integrity-skill",
		"activation_integrity_skill", "1.0.0", "inline", "Trusted v1.",
	)
	installed, err := store.InstallSkillDirectory(context.Background(), v1Source, "user_integrity_test")
	if err != nil {
		t.Fatal(err)
	}
	v2Source := writeSkillTestPackage(
		t, filepath.Join(root, "v2"), "activation-integrity-skill",
		"activation_integrity_skill", "2.0.0", "inline", "Trusted v2.",
	)
	upgraded, err := store.InstallSkillDirectory(context.Background(), v2Source, "user_integrity_test")
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := store.ActivateSkillVersion(
		context.Background(), installed.SkillInstallationID, "1.0.0", "user_integrity_test",
	)
	if err != nil {
		t.Fatal(err)
	}
	var versionTwo SkillVersion
	for _, version := range upgraded.Versions {
		if version.Version == "2.0.0" {
			versionTwo = version
		}
	}
	if versionTwo.SkillVersionID == "" {
		t.Fatal("version two missing")
	}
	packageSkillFile := filepath.Join(root, filepath.FromSlash(versionTwo.packageRef), "SKILL.md")
	if err := os.WriteFile(packageSkillFile, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = store.ActivateSkillVersion(
		context.Background(), installed.SkillInstallationID, "2.0.0", "user_integrity_test",
	)
	assertDomainCode(t, err, "SKILL_PACKAGE_INTEGRITY_FAILED")
	current, err := store.GetSkillInstallation(context.Background(), installed.SkillInstallationID)
	if err != nil {
		t.Fatal(err)
	}
	if activeSkillVersionForTest(t, current).Version != "1.0.0" ||
		activeSkillVersionForTest(t, rolledBack).ContentHash != activeSkillVersionForTest(t, current).ContentHash {
		t.Fatalf("active version changed after rejected activation: %+v", current)
	}
	assertWorkspaceRegistrySkillVersion(t, store, installed.CapabilityID, "1.0.0", true)
}

func writeSkillTestPackage(
	t *testing.T,
	parent string,
	name string,
	capabilityID string,
	version string,
	executionMode string,
	instructions string,
) string {
	t.Helper()
	directory := filepath.Join(parent, name)
	if err := os.MkdirAll(filepath.Join(directory, "content-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillMarkdown := "---\nname: " + name + "\ndescription: Test dynamically installed Skill package.\n---\n\n" + instructions + "\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(skillMarkdown), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema_version":       "1.0.0",
		"id":                   capabilityID,
		"version":              version,
		"execution_mode":       executionMode,
		"accepted_asset_kinds": []string{"text"},
		"required_providers":   []string{"content_model_provider"},
		"commands":             []string{"inspect", "invoke"},
		"ui": map[string]any{
			"icon_key": "sparkles", "sort_order": 1000, "entry_view_key": "source_materials",
		},
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "content-agent", "manifest.json"), encoded, 0o644,
	); err != nil {
		t.Fatal(err)
	}
	return directory
}

func activeSkillVersionForTest(t *testing.T, installation SkillInstallation) SkillVersion {
	t.Helper()
	version, err := activeSkillVersion(installation)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func assertRegistrySkillVersion(
	t *testing.T,
	registry *capability.Registry,
	capabilityID string,
	version string,
	enabled bool,
) {
	t.Helper()
	entry, ok := registry.Get(capabilityID)
	if !ok || entry.Skill == nil || entry.Definition == nil {
		t.Fatalf("registry entry %s = %+v, present = %v", capabilityID, entry, ok)
	}
	if entry.Definition.Version != version {
		t.Fatalf("registry version = %s, want %s", entry.Definition.Version, version)
	}
	if enabled && entry.Status != capability.Available {
		t.Fatalf("registry status = %s, want available", entry.Status)
	}
	if !enabled && (entry.Status != capability.Unavailable || entry.ReasonCode != "SKILL_DISABLED") {
		t.Fatalf("registry disabled entry = %+v", entry)
	}
}

func workspaceRegistryForTest(t *testing.T, store *Store) *capability.Registry {
	t.Helper()
	registry, err := store.CapabilityRegistryForWorkspace(context.Background(), SharedWorkspaceID)
	if err != nil {
		t.Fatalf("CapabilityRegistryForWorkspace() error = %v", err)
	}
	return registry
}

func assertWorkspaceRegistrySkillVersion(
	t *testing.T,
	store *Store,
	capabilityID string,
	version string,
	enabled bool,
) {
	t.Helper()
	assertRegistrySkillVersion(t, workspaceRegistryForTest(t, store), capabilityID, version, enabled)
}

func assertManagedSkillFiles(t *testing.T, store *Store, skillName string, exists bool) {
	t.Helper()
	path := filepath.Join(store.managedSkillActiveRoot(SharedWorkspaceID), skillName, "SKILL.md")
	_, err := os.Stat(path)
	if exists && err != nil {
		t.Fatalf("active Skill missing at %s: %v", path, err)
	}
	if !exists && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active Skill still exists at %s: %v", path, err)
	}
}

func assertDirectoryEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", directory, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("directory %s is not empty: %s", directory, strings.Join(names, ", "))
	}
}

func skillEventTypes(events []SkillInstallationEvent) map[string]bool {
	result := make(map[string]bool, len(events))
	for _, event := range events {
		result[event.EventType] = true
	}
	return result
}
