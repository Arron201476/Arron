package workspaceview

import (
	"slices"
	"testing"

	"content-agent/backend/internal/capability"
)

func TestCompileBuildsDeterministicRegistries(t *testing.T) {
	capabilities := []capability.PublicCapability{
		{
			CapabilityID: "dynamic_story_skill", Version: "1.0.0", Label: "Dynamic Story Skill",
			Description: "Fixture", ExecutionMode: "stateful_workflow", CreatesRun: true,
			Status: capability.Available, AcceptedAssetKinds: []string{"text", "document"},
			InputBinding:     capability.InputBinding{SourceType: "story", AssetRole: "primary_source"},
			DefaultConfigRef: "review", ConfigOptions: []string{"review"},
			UIEntry: capability.PublicUIEntry{
				IconKey: "sparkles", MenuOrder: 20, EntryViewKey: "source_materials",
				ConfigViewKey: "json_schema", DefaultPrompt: "Review this story.",
			},
		},
		{
			CapabilityID: "future_skill", Version: "1.0.0", Label: "Future Skill",
			Status:  capability.Available,
			UIEntry: capability.PublicUIEntry{MenuOrder: 10, EntryViewKey: "future_composer"},
		},
	}
	presentations := []capability.ArtifactPresentation{
		{
			ArtifactType: "generic_document", Label: "Document", Renderer: "document",
			PreferredFields: []string{"title"}, AvailableActions: []string{"inspect"},
			Navigation: capability.ArtifactNavigation{Order: 30, GroupMode: "single", Visibility: "user"},
		},
	}

	first := Compile(capabilities, presentations)
	second := Compile(capabilities, presentations)
	if first.ContractVersion != ContractVersion || first.Revision == "" || first.Revision != second.Revision {
		t.Fatalf("catalog identity = %+v / %+v", first, second)
	}
	if got := []string{first.Registries.Composer[0].CapabilityID, first.Registries.Composer[1].CapabilityID}; !slices.Equal(got, []string{"future_skill", "dynamic_story_skill"}) {
		t.Fatalf("composer order = %v", got)
	}
	entry := first.Registries.Composer[1]
	if entry.ViewKey != "source_materials" || entry.Config.ViewKey != "json_schema" ||
		entry.InputBinding.SourceType != "story" || entry.DefaultPrompt != "Review this story." ||
		!slices.Equal(entry.Config.Options, []string{"review"}) {
		t.Fatalf("composer entry = %+v", entry)
	}
	if first.Registries.Composer[0].ViewKey != "future_composer" {
		t.Fatalf("unknown server view key was rewritten: %+v", first.Registries.Composer[0])
	}
	if first.Registries.Artifacts[0].ViewKey != "document" || first.Registries.Navigation[0].GroupMode != "single" {
		t.Fatalf("artifact registries = %+v / %+v", first.Registries.Artifacts, first.Registries.Navigation)
	}
	if first.Registries.Approvals[len(first.Registries.Approvals)-1].ViewKey != "action_list" {
		t.Fatalf("approval fallback missing: %+v", first.Registries.Approvals)
	}
	if !slices.Contains(first.Registries.Tasks, TaskEntry{Status: "completed", ViewKey: "task_result"}) ||
		slices.Contains(first.Registries.Tasks, TaskEntry{Status: "succeeded", ViewKey: "task_result"}) {
		t.Fatalf("task status projection = %+v", first.Registries.Tasks)
	}
}

func TestCompileUsesInspectorForMissingComposerView(t *testing.T) {
	catalog := Compile([]capability.PublicCapability{{
		CapabilityID: "minimal_skill", Status: capability.Available,
	}}, nil)
	if got := catalog.Registries.Composer[0]; got.ViewKey != "inspector" || got.Config.ViewKey != "none" {
		t.Fatalf("minimal composer = %+v", got)
	}
}

func TestWorkflowContinuePrecedesLegacyExpansionView(t *testing.T) {
	entries := Compile(nil, nil).Registries.Approvals
	for _, scope := range []string{"workflow_transition", "transition"} {
		matched := ""
		for _, entry := range entries {
			if entry.Match.Scope != "" && entry.Match.Scope != scope || entry.Match.Option != "" || entry.Match.SubjectKind != "" && entry.Match.SubjectKind != "transition" {
				continue
			}
			matched = entry.ViewKey
			break
		}
		want := "volume_fit"
		if scope == "workflow_transition" {
			want = "action_list"
		}
		if matched != want {
			t.Fatalf("scope %q routed to %q, want %q", scope, matched, want)
		}
	}
}
