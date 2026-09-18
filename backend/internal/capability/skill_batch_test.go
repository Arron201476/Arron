package capability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedBatchSkillAdmission(t *testing.T) {
	for _, variant := range []struct{ execution, scope string }{{"sequential", "batch"}, {"parallel", "batch"}, {"parallel", ""}} {
		t.Run(variant.execution+"/"+variant.scope, func(t *testing.T) {
			root, directory := copyBatchSkill(t)
			editBatchWorkflow(t, directory, func(manifest *Manifest) {
				manifest.Steps[0].Batch.Execution = variant.execution
				manifest.Steps[0].Approval.Scope = variant.scope
			})
			skill, err := InspectSkillPackage(testProjectRoot(t), root, directory)
			if err != nil || skill.WorkflowDefinition == nil {
				t.Fatalf("InspectSkillPackage() = %+v, %v", skill, err)
			}
			definition := compileSkillPackage(skill)
			if len(definition.Steps) != 1 || definition.Steps[0].Kind != "batch" ||
				!SupportsDirectSkillOutput(definition.Steps[0]) {
				t.Fatalf("batch direct output = %+v", definition)
			}
			packages, diagnostics := scanSkillRoots(testProjectRoot(t), []SkillRoot{root})
			if len(packages) != 1 || len(diagnostics) != 0 {
				t.Fatalf("scan = %+v, %+v", packages, diagnostics)
			}
		})
	}
}

func TestManagedBatchSkillRejectsUnsupportedProtocolsAtAdmission(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Manifest)
		want string
	}{
		{"item_key", func(m *Manifest) { m.Steps[0].Batch.ItemKey = "source_analysis" }, "episode_no batch"},
		{"execution", func(m *Manifest) { m.Steps[0].Batch.Execution = "unknown" }, "episode_no batch"},
		{"ordering", func(m *Manifest) { m.Steps[0].Batch.Ordering = "unknown" }, "episode_no batch"},
		{"size", func(m *Manifest) { m.Steps[0].Batch.MaxItemsPerTask = 2 }, "episode_no batch"},
		{"failure", func(m *Manifest) { m.Steps[0].Batch.FailurePolicy = "unknown" }, "episode_no batch"},
		{"preparation", func(m *Manifest) { m.Steps[0].Batch.Preparation = &BatchInternalTaskStage{ID: "internal"} }, "episode_no batch"},
		{"task_stage", func(m *Manifest) { m.Steps[0].Batch.TaskStage = &BatchInternalTaskStage{ID: "internal"} }, "episode_no batch"},
		{"cardinality", func(m *Manifest) { m.Steps[0].OutputRefs[0].Cardinality = "one" }, "episode_no batch"},
		{"status", func(m *Manifest) { m.Steps[0].OutputRefs[0].InitialStatus = "confirmed" }, "episode_no batch"},
		{"approval_required", func(m *Manifest) { m.Steps[0].Approval.Required = false }, "episode_no batch"},
		{"approval_type", func(m *Manifest) { m.Steps[0].Approval.Type = "artifact" }, "episode_no batch"},
		{"approval_scope", func(m *Manifest) { m.Steps[0].Approval.Scope = "transition" }, "episode_no batch"},
		{"executor", func(m *Manifest) { m.Steps[0].ExecutorRef = "runtime.script_context" }, "worker.structured_content"},
		{"adapter", func(m *Manifest) { ref := "platform.adapter"; m.Steps[0].ResponseAdapterRef = &ref }, "platform response adapter"},
		{"missing_policy", func(m *Manifest) { m.Steps[0].Batch = nil }, "batch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, directory := copyBatchSkill(t)
			editBatchWorkflow(t, directory, test.edit)
			if _, err := InspectSkillPackage(testProjectRoot(t), root, directory); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("admission error = %v, want %q", err, test.want)
			}
			packages, diagnostics := scanSkillRoots(testProjectRoot(t), []SkillRoot{root})
			if len(packages) != 0 || len(diagnostics) == 0 {
				t.Fatalf("invalid package became callable: %+v, %+v", packages, diagnostics)
			}
		})
	}
}

func copyBatchSkill(t *testing.T) (SkillRoot, string) {
	t.Helper()
	root := SkillRoot{Scope: SkillScopeWorkspace, Path: t.TempDir()}
	directory := filepath.Join(root.Path, "episode-review-workflow")
	source := filepath.Join(testProjectRoot(t), "fixtures", "skills", "batched", "episode-review-workflow")
	if err := os.CopyFS(directory, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	return root, directory
}

func editBatchWorkflow(t *testing.T, directory string, edit func(*Manifest)) {
	t.Helper()
	path := filepath.Join(directory, "content-agent", "workflow.json")
	manifest, err := decodeManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	edit(&manifest)
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
