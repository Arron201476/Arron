package artifactcontract

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestIdentityAdapterUsesCanonicalArtifactPayload(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	schemaDocument := map[string]any{
		"$id":                  "https://content-agent.local/schemas/test-identity.json",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"value"},
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
	}
	if err := compiler.AddResource(
		"https://content-agent.local/schemas/test-identity.json",
		schemaDocument,
	); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("https://content-agent.local/schemas/test-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	registry := &Registry{adapters: map[string]adapter{
		"adapter.test_identity": {
			ID:              "adapter.test_identity",
			TargetSchemaRef: "test.schema.json#/$defs/payload",
			Behavior:        "artifact_payload_identity",
			schema:          compiled,
		},
	}}

	payload, err := registry.AdaptAndValidate(
		"adapter.test_identity",
		"test.schema.json#/$defs/payload",
		"test_payload",
		json.RawMessage(`{"value":"canonical"}`),
	)
	if err != nil {
		t.Fatalf("AdaptAndValidate() error = %v", err)
	}
	if string(payload) != `{"value":"canonical"}` {
		t.Fatalf("payload = %s", payload)
	}
}

func TestIdentityAdapterAcceptsSingleArtifactEnvelope(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	schemaDocument := map[string]any{
		"$id":                  "https://content-agent.local/schemas/test-identity-envelope.json",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"value"},
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
	}
	if err := compiler.AddResource(
		"https://content-agent.local/schemas/test-identity-envelope.json",
		schemaDocument,
	); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("https://content-agent.local/schemas/test-identity-envelope.json")
	if err != nil {
		t.Fatal(err)
	}
	registry := &Registry{adapters: map[string]adapter{
		"adapter.test_identity": {
			ID:              "adapter.test_identity",
			TargetSchemaRef: "test.schema.json#/$defs/payload",
			Behavior:        "artifact_payload_identity",
			schema:          compiled,
		},
	}}

	for _, response := range []json.RawMessage{
		json.RawMessage(`{"test_payload":{"value":"wrapped"}}`),
		json.RawMessage(`{"artifact":{"value":"wrapped"}}`),
	} {
		payload, adaptErr := registry.AdaptAndValidate(
			"adapter.test_identity",
			"test.schema.json#/$defs/payload",
			"test_payload",
			response,
		)
		if adaptErr != nil {
			t.Fatalf("AdaptAndValidate(%s) error = %v", response, adaptErr)
		}
		if string(payload) != `{"value":"wrapped"}` {
			t.Fatalf("payload = %s", payload)
		}
	}
}

func TestVideoSharedWorkflowAdaptersTargetVideoSchemas(t *testing.T) {
	registry, err := Load(filepath.Clean("../../.."))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	tests := map[string]string{
		"adapter.legacy_video_story_seed_to_v1":       "story_seed",
		"adapter.legacy_video_series_blueprint_to_v1": "series_blueprint",
	}
	for adapterID, wrapperKey := range tests {
		definition, ok := registry.adapters[adapterID]
		if !ok {
			t.Fatalf("adapter %q is not registered", adapterID)
		}
		if definition.wrapperKey != wrapperKey {
			t.Fatalf("adapter %q wrapper = %q, want %q", adapterID, definition.wrapperKey, wrapperKey)
		}
		if definition.Behavior != "legacy_response_wrapper_to_artifact_payload" {
			t.Fatalf("adapter %q behavior = %q", adapterID, definition.Behavior)
		}
	}
}

func TestVideoAdapterPreservesContinuityDelta(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	schemaDocument := map[string]any{
		"$id":                  "https://content-agent.local/schemas/test-video-state.json",
		"type":                 "object",
		"additionalProperties": true,
		"required":             []any{"continuity_delta"},
		"properties": map[string]any{
			"continuity_delta": map[string]any{
				"type":     "object",
				"required": []any{"new_facts"},
				"properties": map[string]any{
					"new_facts": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
			},
		},
	}
	if err := compiler.AddResource(
		"https://content-agent.local/schemas/test-video-state.json",
		schemaDocument,
	); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("https://content-agent.local/schemas/test-video-state.json")
	if err != nil {
		t.Fatal(err)
	}
	registry := &Registry{adapters: map[string]adapter{
		"adapter.legacy_video_script_unit_to_v1": {
			ID: "adapter.legacy_video_script_unit_to_v1", TargetSchemaRef: "video.schema.json",
			Behavior: "artifact_payload_identity", schema: compiled,
		},
	}}
	payload, err := registry.AdaptAndValidate(
		"adapter.legacy_video_script_unit_to_v1",
		"video.schema.json",
		"video_script_unit",
		json.RawMessage(`{"continuity_delta":{"new_facts":["角色确认姓王"]}}`),
	)
	if err != nil {
		t.Fatalf("AdaptAndValidate() error = %v", err)
	}
	var output struct {
		ContinuityDelta struct {
			NewFacts []string `json:"new_facts"`
		} `json:"continuity_delta"`
	}
	if err := json.Unmarshal(payload, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.ContinuityDelta.NewFacts) != 1 || output.ContinuityDelta.NewFacts[0] != "角色确认姓王" {
		t.Fatalf("video continuity delta was changed: %s", payload)
	}
}
