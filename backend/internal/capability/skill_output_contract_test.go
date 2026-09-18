package capability

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedSkillRejectsIncoherentModelOutputDeclarations(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Manifest)
		want string
	}{
		{"missing_approval", func(m *Manifest) { m.Steps[0].Approval.Required = false }, "model outputs require user approval"},
		{"none_approval", func(m *Manifest) { m.Steps[0].Approval.Type = "none" }, "model outputs require user approval"},
		{"many_without_scope", func(m *Manifest) { m.Steps[0].OutputRefs[0].Cardinality = "many" }, "requires a batch task scope"},
		{"repeated_output_type", func(m *Manifest) { m.Steps[0].OutputRefs = append(m.Steps[0].OutputRefs, m.Steps[0].OutputRefs[0]) }, "repeats output artifact_type"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, directory := copyBatchSkill(t)
			editBatchWorkflow(t, directory, func(m *Manifest) {
				m.Steps[0].Kind = "model"
				m.Steps[0].Batch = nil
				m.Steps[0].OutputRefs[0].Cardinality = "one"
				m.Steps[0].Approval.Type = "checkpoint"
				m.Steps[0].Approval.Scope = "artifact"
				m.Completion.RequiredArtifacts[0].Coverage = "one"
				test.edit(m)
			})
			_, err := InspectSkillPackage(testProjectRoot(t), root, directory)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("admission = %v, want %q", err, test.want)
			}
			packages, diagnostics := scanSkillRoots(testProjectRoot(t), []SkillRoot{root})
			if len(packages) != 0 || len(diagnostics) == 0 {
				t.Fatalf("invalid package registered: %+v, %+v", packages, diagnostics)
			}
		})
	}
}

func TestManagedSkillRetainsDistinctMultipleOutputDeclarations(t *testing.T) {
	root, directory := copyBatchSkill(t)
	editBatchWorkflow(t, directory, func(m *Manifest) {
		m.Steps[0].Kind = "model"
		m.Steps[0].Batch = nil
		m.Steps[0].OutputRefs[0].Cardinality = "one"
		other := m.Steps[0].OutputRefs[0]
		other.ArtifactType = "review_notes"
		m.Steps[0].OutputRefs = append(m.Steps[0].OutputRefs, other)
		m.Completion.RequiredArtifacts[0].Coverage = "one"
	})
	skill, err := InspectSkillPackage(testProjectRoot(t), root, directory)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := decodeManifest(filepath.Join(directory, filepath.FromSlash(skill.WorkflowRef)))
	if err != nil || len(manifest.Steps[0].OutputRefs) != 2 {
		t.Fatalf("multi-output declaration = %+v, %v", manifest, err)
	}
}
