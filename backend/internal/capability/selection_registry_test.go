package capability

import (
	"path/filepath"
	"testing"
)

func TestSelectionRegistryFiltersOwnersBeforeApplyingSnapshotPriority(t *testing.T) {
	root := t.TempDir()
	systemRoot := SkillRoot{Scope: SkillScopeSystem, Path: filepath.Join(root, "system"), Priority: 100}
	userA := SkillRoot{Scope: SkillScopeUser, Path: filepath.Join(root, "user-a"), Priority: 400, WorkspaceID: "workspace-a", ScopeRef: "user-a"}
	userB := SkillRoot{Scope: SkillScopeUser, Path: filepath.Join(root, "user-b"), Priority: 400, WorkspaceID: "workspace-a", ScopeRef: "user-b"}
	workspaceRoot := SkillRoot{Scope: SkillScopeWorkspace, Path: filepath.Join(root, "managed"), Priority: 250, WorkspaceID: "workspace-a", ScopeRef: "workspace-a"}
	for index, skillRoot := range []SkillRoot{systemRoot, userA, userB, workspaceRoot} {
		writeTestSkill(t, skillRoot.Path, "shared-skill", "Scoped fixture", []string{"1.0.0", "2.0.0", "3.0.0", "4.0.0"}[index])
	}
	managed, err := InspectSkillPackage(root, workspaceRoot, filepath.Join(workspaceRoot.Path, "shared-skill"))
	if err != nil {
		t.Fatal(err)
	}
	registry := NewEmptyRegistry()
	registry.projectRoot = root
	registry.skillRoots = []SkillRoot{systemRoot, userA, userB}
	if err := registry.RefreshSkills(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ workspace, user, version string }{
		{"workspace-a", "user-a", "2.0.0"}, {"workspace-a", "user-b", "3.0.0"},
		{"workspace-a", "user-c", "4.0.0"}, {"workspace-b", "user-a", "1.0.0"},
	} {
		selected, err := registry.ForkForSelection(func(root SkillRoot) bool {
			return root.Scope == SkillScopeSystem || (root.WorkspaceID == tc.workspace && (root.Scope == SkillScopeWorkspace || root.Scope == SkillScopeUser && root.ScopeRef == tc.user))
		}, []*SkillPackage{managed})
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			entry, ok := selected.Get("shared_skill")
			if !ok || entry.Definition.Version != tc.version {
				t.Fatalf("%+v got %+v", tc, entry)
			}
			if err := selected.RefreshSkills(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestDisabledManagedSnapshotDoesNotFallBackOrAffectAnotherSelection(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, root, "shared-skill", "Original", "1.0.0")
	skill, err := InspectSkillPackage(root, SkillRoot{Scope: SkillScopeUser, Path: root, Priority: 450}, filepath.Join(root, "shared-skill"))
	if err != nil {
		t.Fatal(err)
	}
	skill.Disabled = true
	registry := NewEmptyRegistry()
	registry.projectRoot = root
	registry.skillRoots = []SkillRoot{{Scope: SkillScopeSystem, Path: root, Priority: 100}}
	if err := registry.RefreshSkills(); err != nil {
		t.Fatal(err)
	}
	selected, err := registry.ForkForSelection(nil, []*SkillPackage{skill})
	if err != nil {
		t.Fatal(err)
	}
	skill.Disabled = false
	if err := selected.RefreshSkills(); err != nil {
		t.Fatal(err)
	}
	entry, _ := selected.Get("shared_skill")
	if entry.Status != Unavailable || entry.ReasonCode != "SKILL_DISABLED" {
		t.Fatalf("disabled overlay fell back: %+v", entry)
	}
	original, _ := registry.Get("shared_skill")
	if original.Status != Available {
		t.Fatal("selection mutated parent registry")
	}
}
