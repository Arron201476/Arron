package runtime

import (
	"encoding/json"
	"testing"
)

func TestSelectedContextDependenciesUsesSDKReadSubset(t *testing.T) {
	pack := StepExecutionContextPack{
		Target: ContextTarget{ScopeKey: "episode:3"},
		UpstreamContext: []ContextUpstreamArtifact{
			{ArtifactVersionID: "av_source", ArtifactType: "generic_document", SelectionPolicy: "explicit_source"},
			{ArtifactVersionID: "av_h1", ArtifactType: "script_handoff", ScopeKey: "episode:1", SelectionPolicy: "sdk_context_candidate"},
			{ArtifactVersionID: "av_s1", ArtifactType: "script_unit", ScopeKey: "episode:1", SelectionPolicy: "sdk_context_candidate"},
			{ArtifactVersionID: "av_h2", ArtifactType: "script_handoff", ScopeKey: "episode:2", SelectionPolicy: "sdk_context_candidate"},
			{ArtifactVersionID: "av_s2", ArtifactType: "script_unit", ScopeKey: "episode:2", SelectionPolicy: "sdk_context_candidate"},
		},
	}
	usage := `{"context_artifact_version_ids":["av_h2","av_s2","av_h1"]}`
	selected, err := selectedContextDependencies(pack, usage)
	if err != nil {
		t.Fatalf("selectedContextDependencies() error = %v", err)
	}
	got := make([]string, 0, len(selected))
	for _, item := range selected {
		got = append(got, item.ArtifactVersionID)
	}
	want := []string{"av_source", "av_h1", "av_h2", "av_s2"}
	if string(mustJSONNoTest(got)) != string(mustJSONNoTest(want)) {
		t.Fatalf("selected dependencies = %v, want %v", got, want)
	}
}

func TestSelectedContextDependenciesAlwaysIncludesStructuredRunState(t *testing.T) {
	pack := StepExecutionContextPack{
		StructuredRunState: &ContextStructuredRunState{
			ArtifactID: "art_state", ArtifactVersionID: "av_state", Version: 3,
			SchemaRef: "state.schema.json", Content: json.RawMessage(`{"state_version":3,"state":{}}`),
			ContentHash: "hash_state",
		},
	}
	selected, err := selectedContextDependencies(pack, `{"context_artifact_version_ids":[]}`)
	if err != nil {
		t.Fatalf("selectedContextDependencies() error = %v", err)
	}
	if len(selected) != 1 || selected[0].ArtifactVersionID != "av_state" ||
		selected[0].SelectionPolicy != "structured_state_snapshot" {
		t.Fatalf("structured state dependency = %+v", selected)
	}
}

func TestSelectedContextDependenciesRequiresImmediatePriorEpisode(t *testing.T) {
	pack := StepExecutionContextPack{
		Target: ContextTarget{ScopeKey: "episode:2"},
		UpstreamContext: []ContextUpstreamArtifact{
			{ArtifactVersionID: "av_h1", ArtifactType: "script_handoff", ScopeKey: "episode:1", SelectionPolicy: "sdk_context_candidate"},
			{ArtifactVersionID: "av_s1", ArtifactType: "script_unit", ScopeKey: "episode:1", SelectionPolicy: "sdk_context_candidate"},
		},
	}
	_, err := selectedContextDependencies(pack, `{"context_artifact_version_ids":["av_h1"]}`)
	assertDomainCode(t, err, "CONTEXT_REQUIRED_UPSTREAM_MISSING")
	_, err = selectedContextDependencies(pack, `{"context_artifact_version_ids":["av_h1","av_unknown"]}`)
	assertDomainCode(t, err, "CONTEXT_LINEAGE_CONFLICT")

	if !json.Valid([]byte(`{"context_artifact_version_ids":[]}`)) {
		t.Fatal("invalid test usage fixture")
	}
}
