package capability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnversionedSkillUsesImmutableContentVersion(t *testing.T) {
	root := t.TempDir()
	directory := writeTestSkill(t, root, "standard-skill", "Standard skill", "")
	if err := os.Remove(filepath.Join(directory, "content-agent", "manifest.json")); err != nil {
		t.Fatal(err)
	}
	inspect := func() *SkillPackage {
		t.Helper()
		skill, err := InspectSkillPackage(root, SkillRoot{Scope: SkillScopeWorkspace, Path: root}, directory)
		if err != nil {
			t.Fatal(err)
		}
		return skill
	}
	first, same := inspect(), inspect()
	if first.Version != "0.0.0+"+strings.TrimPrefix(first.ContentHash, "sha256:") || first.Version != same.Version {
		t.Fatalf("unstable content version: %s / %s", first.Version, same.Version)
	}
	legacy, ok := first.MatchSnapshot("0.0.0", first.ContentHash)
	if !ok || legacy.Version != "0.0.0" || first.Version == legacy.Version {
		t.Fatal("legacy snapshot did not preserve its version independently")
	}
	if _, ok := first.MatchSnapshot("0.0.0", ""); ok {
		t.Fatal("legacy alias accepted without a hash")
	}
	if err := os.WriteFile(filepath.Join(directory, "reference.txt"), []byte("updated resource"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := inspect()
	if changed.Version == first.Version {
		t.Fatal("resource update did not create a new version")
	}
	if _, ok := changed.MatchSnapshot("0.0.0", first.ContentHash); ok {
		t.Fatal("legacy alias accepted changed content")
	}
}

func TestSkillSnapshotPinBindsContentHashAndLegacyVersion(t *testing.T) {
	root := t.TempDir()
	directory := writeTestSkill(t, root, "standard-skill", "Standard skill", "")
	registry := NewEmptyRegistry()
	registry.projectRoot = root
	registry.skillRoots = []SkillRoot{{Scope: SkillScopeWorkspace, Path: root}}
	if err := registry.RefreshSkills(); err != nil {
		t.Fatal(err)
	}
	entry, _ := registry.Get("standard_skill")
	hash := entry.Skill.ContentHash
	if !registry.PinSkillSnapshot("standard_skill", "0.0.0", hash) {
		t.Fatal("pin rejected")
	}
	entry, _ = registry.Get("standard_skill")
	if entry.Status != Available || entry.Definition.Version != "0.0.0" || len(registry.SkillDiagnostics()) != 0 {
		t.Fatalf("legacy pin: %+v", entry)
	}
	if err := os.WriteFile(filepath.Join(directory, "reference.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := registry.RefreshSkills(); err != nil {
		t.Fatal(err)
	}
	entry, _ = registry.Get("standard_skill")
	if entry.ReasonCode != "SKILL_PIN_UNAVAILABLE" {
		t.Fatalf("hash pin allowed changed package: %+v", entry)
	}
	registry.ClearSkillState("standard_skill")
	if err := registry.RefreshSkills(); err != nil {
		t.Fatal(err)
	}
	entry, _ = registry.Get("standard_skill")
	if entry.Status != Available || !strings.HasPrefix(entry.Definition.Version, "0.0.0+") {
		t.Fatalf("clear pin: %+v", entry)
	}
}
