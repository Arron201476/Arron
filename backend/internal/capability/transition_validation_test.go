package capability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func transitionManifest() Manifest {
	manifest := minimalManifest()
	next := manifest.Steps[0]
	next.ID = "second_step"
	manifest.Steps = append(manifest.Steps, next)
	manifest.Steps[0].Next = []TransitionRef{{When: "completed", To: next.ID}}
	manifest.Completion.TerminalStepIDs = []string{next.ID}
	return manifest
}

func TestTransitionContractNamesMatchTheSchema(t *testing.T) {
	expected := []string{"completed", "approved", "user_continue", "user_stop", "incomplete_material_confirmed", "all_batch_items_succeeded",
		"failure", "volume_fit_sufficient", "expansion_strategy_confirmed", "adaptation_selection_and_creation_config_confirmed"}
	var schema struct {
		Definitions struct {
			Transition struct {
				Properties struct {
					When struct {
						Enum []string `json:"enum"`
					} `json:"when"`
				} `json:"properties"`
			} `json:"transition"`
		} `json:"$defs"`
	}
	raw, err := os.ReadFile(filepath.Join(testProjectRoot(t), "capabilities", "v1", "manifest.schema.json"))
	if err != nil || json.Unmarshal(raw, &schema) != nil {
		t.Fatalf("read manifest schema: %v", err)
	}
	if !slices.Equal(expected, knownTransitionConditions) || !slices.Equal(expected, schema.Definitions.Transition.Properties.When.Enum) {
		t.Fatalf("contract condition drift: compiler=%v schema=%v", knownTransitionConditions, schema.Definitions.Transition.Properties.When.Enum)
	}
	for _, condition := range expected {
		t.Run(condition, func(t *testing.T) {
			manifest := transitionManifest()
			manifest.Steps[0].Next[0].When = condition
			_, err := compileManifest(manifest)
			if condition == "user_stop" {
				if err == nil || !strings.Contains(err.Error(), "user_stop is reserved") {
					t.Fatalf("unimplemented stop branch admitted: %v", err)
				}
			} else if err != nil {
				t.Fatalf("known condition rejected: %v", err)
			}
		})
	}
}

func TestCompileRejectsUnknownAndUnusableTransitions(t *testing.T) {
	for _, test := range []struct {
		name string
		next []TransitionRef
		want string
	}{
		{"typo", []TransitionRef{{When: "compeleted", To: "second_step"}}, "unknown transition condition"},
		{"case", []TransitionRef{{When: "Completed", To: "second_step"}}, "unknown transition condition"},
		{"space", []TransitionRef{{When: " completed ", To: "second_step"}}, "unknown transition condition"},
		{"missing-condition", []TransitionRef{{To: "second_step"}}, "incomplete transition"},
		{"missing-target", []TransitionRef{{When: "completed"}}, "incomplete transition"},
		{"unknown-target", []TransitionRef{{When: "completed", To: "missing"}}, "targets unknown step"},
		{"ambiguous", []TransitionRef{{When: "completed", To: "second_step"}, {When: "completed", To: "first_step"}}, "has multiple targets"},
		{"failure-self-retry", []TransitionRef{{When: "completed", To: "second_step"}, {When: "failure", To: "first_step"}}, "use the retry policy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := transitionManifest()
			manifest.Steps[0].Next = test.next
			if _, err := compileManifest(manifest); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("transition admission = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCompilePreservesShorthandAndSameTargetTransitions(t *testing.T) {
	var next []TransitionRef
	if err := json.Unmarshal([]byte(`["second_step",{"when":"completed","to":"second_step"},{"when":"failure","to":"second_step"}]`), &next); err != nil {
		t.Fatal(err)
	}
	manifest := transitionManifest()
	manifest.Steps[0].Next = next
	definition, err := compileManifest(manifest)
	if err != nil || len(definition.Steps[0].Next) != 3 || definition.Steps[0].Next[0].When != "completed" {
		t.Fatalf("compatible transitions rejected or rewritten: %+v %v", definition, err)
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	var restored CompiledDefinition
	if err := json.Unmarshal(encoded, &restored); err != nil || !slices.Equal(restored.Steps[0].Next, next) {
		t.Fatalf("compiled transition snapshot changed: %+v %v", restored.Steps, err)
	}
}

func TestTransitionObjectsRejectUnknownFields(t *testing.T) {
	for _, raw := range []string{
		`{"when":"completed","to":"second_step","when_text":"approved"}`,
		`{"when":"completed","to":"second_step","target":"another_step"}`,
		`{"when":3,"to":"second_step"}`,
		`["completed","second_step"]`,
	} {
		var next TransitionRef
		if err := json.Unmarshal([]byte(raw), &next); err == nil {
			t.Fatalf("invalid transition object was silently accepted: %s", raw)
		}
	}
}

func TestSkillInspectionRejectsInvalidWorkflowTransitions(t *testing.T) {
	for _, scenario := range []struct{ name, condition, extra, want string }{
		{"unknown-condition", "compeleted", "", "compeleted"},
		{"reserved-stop", "user_stop", "", "user_stop"},
		{"unknown-field", "completed", "when_text", "unknown field"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			projectRoot := testProjectRoot(t)
			root := SkillRoot{Scope: SkillScopeWorkspace, Path: t.TempDir()}
			directory := filepath.Join(root.Path, "story-review-workflow")
			if err := os.CopyFS(directory, os.DirFS(filepath.Join(projectRoot, "fixtures", "skills", "stateful", "story-review-workflow"))); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(directory, "content-agent", "workflow.json")
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var manifest map[string]any
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			step := manifest["steps"].([]any)[0].(map[string]any)
			next := map[string]any{"when": scenario.condition, "to": step["id"]}
			if scenario.extra != "" {
				next[scenario.extra] = "must not be ignored"
			}
			step["next"] = []any{next}
			updated, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, updated, 0o644); err != nil {
				t.Fatal(err)
			}
			if skill, err := InspectSkillPackage(projectRoot, root, directory); skill != nil || err == nil || !strings.Contains(err.Error(), scenario.want) {
				t.Fatalf("invalid package admitted: %+v %v", skill, err)
			}
			packages, diagnostics := scanSkillRoots(projectRoot, []SkillRoot{root})
			if len(packages) != 0 || len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, scenario.want) {
				t.Fatalf("refresh offered an invalid workflow: packages=%+v diagnostics=%+v", packages, diagnostics)
			}
		})
	}
}
