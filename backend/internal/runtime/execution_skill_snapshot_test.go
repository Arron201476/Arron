package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
)

// A higher-priority package deliberately reuses the same public version. Old
// executions must resolve their database package binding, not this new winner.
func shadowExecutionSkillForTest(t *testing.T, store *Store, projectID, source, capabilityID string) {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, filepath.Base(source))
	if err := copySkillTree(source, directory); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "SKILL.md")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(data, []byte("\nSHADOW_SAME_VERSION_DO_NOT_EXECUTE\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.registry.AddSkillRoot(capability.SkillRoot{Scope: capability.SkillScopeUser, Path: root, Priority: 1000}); err != nil {
		t.Fatal(err)
	}
	registry, err := store.CapabilityRegistryForProject(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := registry.Get(capabilityID)
	if !ok || entry.Skill == nil || !strings.Contains(entry.Skill.Instructions, "SHADOW_SAME_VERSION_DO_NOT_EXECUTE") {
		t.Fatal("fixture failed to override active registry")
	}
}

func assertExecutionSkillBinding(t *testing.T, store *Store, table, idColumn, id, expectedID string) {
	t.Helper()
	var boundID string
	if err := store.db.QueryRow(`SELECT COALESCE(skill_version_id, '') FROM `+table+` WHERE `+idColumn+` = ?`, id).Scan(&boundID); err != nil || boundID != expectedID {
		t.Fatalf("%s binding=%q expected=%q err=%v", table, boundID, expectedID, err)
	}
}
