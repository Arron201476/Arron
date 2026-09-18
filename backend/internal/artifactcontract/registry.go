package artifactcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Registry struct {
	mu       sync.RWMutex
	adapters map[string]adapter
}

type adapter struct {
	ID              string `json:"id"`
	TargetSchemaRef string `json:"target_schema_ref"`
	Behavior        string `json:"behavior"`
	schema          *jsonschema.Schema
	wrapperKey      string
}

type registryFile struct {
	ContractVersion  string    `json:"contract_version"`
	DefinitionStatus string    `json:"definition_status"`
	Adapters         []adapter `json:"adapters"`
}

func NewEmptyRegistry() *Registry {
	return &Registry{adapters: map[string]adapter{}}
}

func Load(projectRoot string) (*Registry, error) {
	if projectRoot == "" {
		return NewEmptyRegistry(), nil
	}
	registryPath := filepath.Join(projectRoot, "capabilities", "v1", "response-adapters.json")
	data, err := os.ReadFile(registryPath)
	if err != nil {
		return nil, fmt.Errorf("read response adapter registry: %w", err)
	}
	var source registryFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&source); err != nil {
		return nil, fmt.Errorf("decode response adapter registry: %w", err)
	}
	if source.ContractVersion != "1.0.0" || source.DefinitionStatus != "design_contract" {
		return nil, errors.New("response adapter registry version or status is unsupported")
	}

	compiler := jsonschema.NewCompiler()
	schemaDir := filepath.Join(projectRoot, "schemas", "v1")
	schemaFiles, err := filepath.Glob(filepath.Join(schemaDir, "*.schema.json"))
	if err != nil {
		return nil, fmt.Errorf("list artifact schemas: %w", err)
	}
	for _, schemaFile := range schemaFiles {
		file, err := os.Open(schemaFile)
		if err != nil {
			return nil, fmt.Errorf("open schema %s: %w", schemaFile, err)
		}
		document, decodeErr := jsonschema.UnmarshalJSON(file)
		file.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode schema %s: %w", schemaFile, decodeErr)
		}
		schemaObject, ok := document.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema %s is not an object", schemaFile)
		}
		schemaID, ok := schemaObject["$id"].(string)
		if !ok || schemaID == "" {
			return nil, fmt.Errorf("schema %s has no $id", schemaFile)
		}
		if err := compiler.AddResource(schemaID, document); err != nil {
			return nil, fmt.Errorf("register schema %s: %w", schemaFile, err)
		}
	}

	result := NewEmptyRegistry()
	for _, definition := range source.Adapters {
		if definition.ID == "" || definition.TargetSchemaRef == "" ||
			(definition.Behavior != "legacy_response_wrapper_to_artifact_payload" &&
				definition.Behavior != "artifact_payload_identity") {
			return nil, fmt.Errorf("response adapter %q has unsupported definition", definition.ID)
		}
		if _, duplicate := result.adapters[definition.ID]; duplicate {
			return nil, fmt.Errorf("response adapter %q is duplicated", definition.ID)
		}
		targetLocation, err := schemaLocation(projectRoot, registryPath, definition.TargetSchemaRef)
		if err != nil {
			return nil, fmt.Errorf("resolve adapter %s target: %w", definition.ID, err)
		}
		compiled, err := compiler.Compile(targetLocation)
		if err != nil {
			return nil, fmt.Errorf("compile adapter %s schema: %w", definition.ID, err)
		}
		definition.schema = compiled
		definition.wrapperKey = wrapperKeyForAdapter(definition.ID)
		if definition.Behavior == "legacy_response_wrapper_to_artifact_payload" &&
			definition.wrapperKey == "" {
			return nil, fmt.Errorf("response adapter %q has no executable wrapper mapping", definition.ID)
		}
		result.adapters[definition.ID] = definition
	}
	return result, nil
}

func (r *Registry) AdaptAndValidate(
	adapterID string,
	expectedSchemaRef string,
	artifactType string,
	response json.RawMessage,
) (json.RawMessage, error) {
	r.mu.RLock()
	definition, ok := r.adapters[adapterID]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("response adapter %q is not registered", adapterID)
	}
	if cleanSchemaRef(definition.TargetSchemaRef) != cleanSchemaRef(expectedSchemaRef) {
		return nil, fmt.Errorf("response adapter target does not match step output schema")
	}
	var wrapper map[string]any
	decoder := json.NewDecoder(bytes.NewReader(response))
	decoder.UseNumber()
	if err := decoder.Decode(&wrapper); err != nil {
		return nil, fmt.Errorf("decode provider response: %w", err)
	}
	var payloadValue any
	if definition.Behavior == "artifact_payload_identity" {
		// Identity adapters accept the canonical bare payload, but tolerate the
		// standard single-artifact envelopes emitted by structured workers.
		// The payload itself is still validated against the canonical schema.
		switch {
		case wrapper[artifactType] != nil:
			payloadValue = wrapper[artifactType]
		case wrapper["artifact"] != nil:
			payloadValue = wrapper["artifact"]
		default:
			payloadValue = wrapper
		}
	} else {
		switch {
		case wrapper["artifact"] != nil:
			payloadValue = wrapper["artifact"]
		case wrapper[definition.wrapperKey] != nil:
			payloadValue = wrapper[definition.wrapperKey]
		case wrapper[artifactType] != nil:
			payloadValue = wrapper[artifactType]
		default:
			return nil, fmt.Errorf("provider response does not contain wrapper %q", definition.wrapperKey)
		}
	}
	payload, ok := payloadValue.(map[string]any)
	if !ok {
		return nil, errors.New("provider artifact payload is not an object")
	}
	if definition.Behavior == "legacy_response_wrapper_to_artifact_payload" {
		payload = normalizeLegacyObject(payload)
	}
	if artifactType == "script_unit" && adapterID != "adapter.legacy_video_script_unit_to_v1" {
		for _, internalField := range []string{
			"continuity_delta",
			"used_generation_basis",
			"risk_notes",
			"self_check",
		} {
			delete(payload, internalField)
		}
	}
	if adapterID == "adapter.legacy_video_script_unit_to_v1" && payload["continuity_delta"] == nil {
		// Older video workers predate the generic structured-state contract.
		// Keep their results readable while new workers emit the delta explicitly.
		payload["continuity_delta"] = map[string]any{
			"new_facts":               []any{},
			"character_state_changes": []any{},
			"relationship_changes":    []any{},
			"hooks_opened":            []any{},
			"hooks_resolved":          []any{},
		}
	}
	if definition.Behavior == "legacy_response_wrapper_to_artifact_payload" {
		if _, exists := payload["source_trace"]; !exists {
			if trace, exists := wrapper["source_trace"]; exists {
				payload["source_trace"] = normalizeSourceTrace(trace)
			}
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode adapted artifact: %w", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("decode adapted artifact: %w", err)
	}
	if err := definition.schema.Validate(instance); err != nil {
		return nil, fmt.Errorf("artifact schema validation failed: %w", err)
	}
	return json.RawMessage(encoded), nil
}

func schemaLocation(projectRoot, sourceFile, ref string) (string, error) {
	parts := strings.SplitN(ref, "#", 2)
	target := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), filepath.FromSlash(parts[0])))
	relative, err := filepath.Rel(projectRoot, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("schema reference escapes project root")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	var header struct {
		ID string `json:"$id"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return "", err
	}
	if header.ID == "" {
		return "", errors.New("target schema has no $id")
	}
	if len(parts) == 2 {
		return header.ID + "#" + parts[1], nil
	}
	return header.ID, nil
}

func cleanSchemaRef(ref string) string {
	return filepath.ToSlash(filepath.Clean(strings.ReplaceAll(ref, "\\", "/")))
}

func wrapperKeyForAdapter(adapterID string) string {
	switch adapterID {
	case "adapter.legacy_story_bible_to_v1":
		return "story_bible"
	case "adapter.legacy_episode_split_to_v1":
		return "episode_split"
	case "adapter.legacy_material_bank_to_v1":
		return "material_bank"
	case "adapter.legacy_story_seed_to_v1", "adapter.legacy_video_story_seed_to_v1":
		return "story_seed"
	case "adapter.legacy_series_blueprint_to_v1", "adapter.legacy_video_series_blueprint_to_v1":
		return "series_blueprint"
	case "adapter.legacy_episode_cards_to_v1":
		return "episode_cards"
	case "adapter.legacy_novel_episode_cards_to_v1", "adapter.legacy_video_episode_cards_to_v1":
		return "episode_cards"
	case "adapter.legacy_script_unit_to_v1":
		return "script_unit"
	case "adapter.legacy_video_script_unit_to_v1":
		return "video_script_unit"
	case "adapter.legacy_script_handoff_to_v1":
		return "script_handoff"
	case "adapter.novel_script_context_to_v1",
		"adapter.non_novel_script_context_to_v1",
		"adapter.video_script_context_to_v1":
		return "script_context"
	default:
		return ""
	}
}

func normalizeLegacyObject(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		normalizedKey := key
		switch key {
		case "source_evidence":
			normalizedKey = "source_refs"
		case "episode_id":
			normalizedKey = "episode_no"
		case "file_id":
			normalizedKey = "asset_id"
		case "episode_cards":
			normalizedKey = "episodes"
		}
		switch typed := value.(type) {
		case map[string]any:
			result[normalizedKey] = normalizeLegacyObject(typed)
		case []any:
			items := make([]any, len(typed))
			for index, item := range typed {
				if object, ok := item.(map[string]any); ok {
					items[index] = normalizeLegacyObject(object)
				} else {
					items[index] = item
				}
			}
			result[normalizedKey] = items
		default:
			result[normalizedKey] = value
		}
	}
	return result
}

func normalizeSourceTrace(value any) any {
	trace, ok := value.(map[string]any)
	if !ok {
		return value
	}
	if trace["grounded"] != nil && trace["inferred"] != nil {
		return normalizeLegacyObject(trace)
	}
	return map[string]any{
		"grounded": normalizeTraceBucket(trace["from_source_text"]),
		"inferred": normalizeTraceBucket(trace["model_inference"]),
	}
}

func normalizeTraceBucket(value any) []any {
	items, ok := value.([]any)
	if !ok {
		return []any{}
	}
	result := make([]any, 0, len(items))
	for _, item := range items {
		switch typed := item.(type) {
		case string:
			result = append(result, map[string]any{"claim": typed, "source_refs": []any{}})
		case map[string]any:
			result = append(result, normalizeLegacyObject(typed))
		default:
			result = append(result, item)
		}
	}
	return result
}
