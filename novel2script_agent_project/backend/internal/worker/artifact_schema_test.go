package worker

import (
	"strings"
	"testing"
)

func validArtifactPayloads() map[string]map[string]any {
	return map[string]map[string]any{
		"story_bible":      {"story_overview": map[string]any{}, "source_structure": []any{}, "characters": []any{}, "relationships": []any{}, "short_drama_assets": map[string]any{}, "source_trace": map[string]any{}},
		"episode_split":    {"actual_episode_count": 1, "split_strategy": "按剧情", "episodes": []any{map[string]any{"episode_id": 1, "hook_strength": "high"}}, "coverage_check": map[string]any{}},
		"material_bank":    {"input_type_tags": []any{}, "user_supplied_facts": map[string]any{}, "conflict_materials": []any{}, "emotional_drives": []any{}, "source_trace": map[string]any{}},
		"story_seed":       {"logline": "一句话", "core_premise": "前提", "protagonist": map[string]any{}, "central_conflict": "冲突", "source_trace": map[string]any{}},
		"series_blueprint": {"resolved_episode_count": 2, "series_promise": "承诺", "phase_plan": []any{}, "continuity_rules": []any{}, "source_trace": map[string]any{}},
		"episode_cards":    {"episodes": []any{map[string]any{"episode_id": 1}}, "continuity_delta": map[string]any{}, "next_action": "script_generate"},
		"script_context":   {"source_mode": "novel", "must_follow_facts": []any{}, "source_material": map[string]any{}},
		"script_unit":      {"episode_id": 1, "source_mode": "novel", "title": "第一集", "script_text": "场 1-1", "scenes": []any{map[string]any{"scene_id": "scene_1", "blocks": []any{}}}, "source_refs": []any{}},
		"scripts":          {"source_mode": "novel", "episode_count": 1, "script_units": []any{}, "global_continuity_state": map[string]any{}, "quality_flags": []any{}},
	}
}

func cloneFixture(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func wrongValue(kind fieldKind) any {
	switch kind {
	case kindString:
		return []any{}
	case kindNumber:
		return "not-number"
	case kindBool:
		return "not-bool"
	case kindObject:
		return []any{}
	case kindArray:
		return map[string]any{}
	default:
		return nil
	}
}

func TestExecutableSchemaMatrixAcceptsEveryRealArtifactShape(t *testing.T) {
	fixtures := validArtifactPayloads()
	for artifactType := range executableArtifactSchemas {
		t.Run(artifactType, func(t *testing.T) {
			if err := validateArtifactPayload(artifactType, fixtures[artifactType]); err != nil {
				t.Fatalf("valid payload rejected: %v", err)
			}
		})
	}
}

func TestExecutableSchemaMatrixRejectsEveryMissingOrWrongTypedRequiredField(t *testing.T) {
	fixtures := validArtifactPayloads()
	for artifactType, schema := range executableArtifactSchemas {
		for field, kind := range schema.required {
			t.Run(artifactType+"/missing/"+field, func(t *testing.T) {
				payload := cloneFixture(fixtures[artifactType])
				delete(payload, field)
				if err := validateArtifactPayload(artifactType, payload); err == nil || !strings.Contains(err.Error(), field+" is required") {
					t.Fatalf("missing field accepted: %v", err)
				}
			})
			t.Run(artifactType+"/wrong/"+field, func(t *testing.T) {
				payload := cloneFixture(fixtures[artifactType])
				payload[field] = wrongValue(kind)
				if err := validateArtifactPayload(artifactType, payload); err == nil || !strings.Contains(err.Error(), field+" must be") {
					t.Fatalf("wrong type accepted: %v", err)
				}
			})
		}
	}
}

func TestValidateArtifactPayloadRejectsEnumsAndDuplicateStableIDs(t *testing.T) {
	payload := cloneFixture(validArtifactPayloads()["episode_split"])
	payload["episodes"] = []any{
		map[string]any{"episode_id": 1, "hook_strength": "extreme"},
		map[string]any{"episode_id": 1, "hook_strength": "high"},
	}
	err := validateArtifactPayload("episode_split", payload)
	if err == nil || !strings.Contains(err.Error(), "duplicate episode_id") || !strings.Contains(err.Error(), "high, medium, or low") {
		t.Fatalf("expected enum and duplicate errors, got %v", err)
	}
}

func TestValidateArtifactPayloadRejectsUnknownArtifactType(t *testing.T) {
	if err := validateArtifactPayload("unknown", map[string]any{}); err == nil {
		t.Fatal("unknown artifact schema was accepted")
	}
}

func TestGenerationConfigRejectsMismatchedEpisodeOutputs(t *testing.T) {
	config := map[string]any{"target_episode_count": 2}
	cases := []struct {
		artifactType string
		payload      map[string]any
	}{
		{"episode_split", map[string]any{"actual_episode_count": 1, "split_strategy": "按剧情", "episodes": []any{map[string]any{"episode_id": 1}}, "coverage_check": map[string]any{}}},
		{"series_blueprint", map[string]any{"resolved_episode_count": 1, "series_promise": "承诺", "phase_plan": []any{}, "continuity_rules": []any{}, "source_trace": map[string]any{}}},
		{"episode_cards", map[string]any{"episodes": []any{map[string]any{"episode_id": 1}}, "continuity_delta": map[string]any{}, "next_action": "script_generate"}},
	}
	for _, testCase := range cases {
		if err := ValidateArtifactAgainstGenerationConfig(testCase.artifactType, testCase.payload, config); err == nil {
			t.Fatalf("%s must reject a one-episode output for a two-episode run", testCase.artifactType)
		}
	}
}
