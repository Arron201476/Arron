package worker

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type fieldKind uint8

const (
	kindString fieldKind = iota + 1
	kindNumber
	kindBool
	kindObject
	kindArray
)

type artifactSchemaSpec struct {
	required map[string]fieldKind
	enums    map[string]map[string]bool
}

var executableArtifactSchemas = map[string]artifactSchemaSpec{
	"story_bible":      {required: map[string]fieldKind{"story_overview": kindObject, "source_structure": kindArray, "characters": kindArray, "relationships": kindArray, "short_drama_assets": kindObject, "source_trace": kindObject}},
	"episode_split":    {required: map[string]fieldKind{"actual_episode_count": kindNumber, "split_strategy": kindString, "episodes": kindArray, "coverage_check": kindObject}},
	"material_bank":    {required: map[string]fieldKind{"input_type_tags": kindArray, "user_supplied_facts": kindObject, "conflict_materials": kindArray, "emotional_drives": kindArray, "source_trace": kindObject}},
	"story_seed":       {required: map[string]fieldKind{"logline": kindString, "core_premise": kindString, "protagonist": kindObject, "central_conflict": kindString, "source_trace": kindObject}},
	"series_blueprint": {required: map[string]fieldKind{"resolved_episode_count": kindNumber, "series_promise": kindString, "phase_plan": kindArray, "continuity_rules": kindArray, "source_trace": kindObject}},
	"episode_cards":    {required: map[string]fieldKind{"episodes": kindArray, "continuity_delta": kindObject, "next_action": kindString}},
	"script_context":   {required: map[string]fieldKind{"source_mode": kindString, "must_follow_facts": kindArray, "source_material": kindObject}, enums: enumSpec("source_mode", "novel", "non_novel")},
	"script_unit":      {required: map[string]fieldKind{"episode_id": kindNumber, "source_mode": kindString, "title": kindString, "script_text": kindString, "scenes": kindArray, "source_refs": kindArray}, enums: enumSpec("source_mode", "novel", "non_novel")},
	"scripts":          {required: map[string]fieldKind{"source_mode": kindString, "episode_count": kindNumber, "script_units": kindArray, "global_continuity_state": kindObject, "quality_flags": kindArray}, enums: enumSpec("source_mode", "novel", "non_novel")},
}

func enumSpec(field string, values ...string) map[string]map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return map[string]map[string]bool{field: set}
}

func validateArtifactPayload(artifactType string, payload map[string]any) error {
	schema, known := executableArtifactSchemas[artifactType]
	if !known {
		return fmt.Errorf("unsupported artifact schema %q", artifactType)
	}
	var problems []string
	for field, kind := range schema.required {
		value, exists := payload[field]
		if !exists || value == nil {
			problems = append(problems, field+" is required")
			continue
		}
		if !matchesKind(value, kind) {
			problems = append(problems, fmt.Sprintf("%s must be %s", field, kindLabel(kind)))
		}
	}
	for field, allowed := range schema.enums {
		if value, ok := payload[field].(string); ok && !allowed[value] {
			problems = append(problems, fmt.Sprintf("%s has unsupported value %q", field, value))
		}
	}
	validateKnownNestedFields(payload, "payload", &problems)
	validateStableIDs(payload, "payload", &problems)
	validateEpisodeCollections(artifactType, payload, &problems)
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("%s schema validation failed: %s", artifactType, strings.Join(problems, "; "))
}

// ValidateArtifactPayload applies the executable artifact contract to API edits.
func ValidateArtifactPayload(artifactType string, payload map[string]any) error {
	return validateArtifactPayload(artifactType, payload)
}

// ValidateArtifactAgainstGenerationConfig prevents a model or manual edit from
// silently changing the episode count confirmed at run level.
func ValidateArtifactAgainstGenerationConfig(artifactType string, payload map[string]any, config map[string]any) error {
	if err := validateArtifactPayload(artifactType, payload); err != nil {
		return err
	}
	expected := positiveInt(config["target_episode_count"])
	if expected == 0 {
		return nil
	}
	switch artifactType {
	case "episode_split":
		if actual := positiveInt(payload["actual_episode_count"]); actual != expected {
			return fmt.Errorf("episode_split actual_episode_count %d does not match configured target %d", actual, expected)
		}
		return validateEpisodeIDsAgainstTarget(payload["episodes"], expected)
	case "series_blueprint":
		if actual := positiveInt(payload["resolved_episode_count"]); actual != expected {
			return fmt.Errorf("series_blueprint resolved_episode_count %d does not match configured target %d", actual, expected)
		}
	case "episode_cards":
		return validateEpisodeIDsAgainstTarget(payload["episodes"], expected)
	}
	return nil
}

func validateEpisodeIDsAgainstTarget(value any, expected int) error {
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("episodes must be array")
	}
	if len(items) != expected {
		return fmt.Errorf("episodes count %d does not match configured target %d", len(items), expected)
	}
	seen := make(map[int]bool, expected)
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("episodes[%d] must be object", index)
		}
		id := positiveInt(object["episode_id"])
		if id < 1 || id > expected || seen[id] {
			return fmt.Errorf("episodes must contain each episode_id from 1 to %d exactly once", expected)
		}
		seen[id] = true
	}
	return nil
}

func positiveInt(value any) int {
	number, ok := numberValue(value)
	if !ok || number <= 0 || math.Trunc(number) != number {
		return 0
	}
	return int(number)
}

func matchesKind(value any, kind fieldKind) bool {
	switch kind {
	case kindString:
		_, ok := value.(string)
		return ok
	case kindNumber:
		n, ok := numberValue(value)
		return ok && !math.IsNaN(n) && !math.IsInf(n, 0)
	case kindBool:
		_, ok := value.(bool)
		return ok
	case kindObject:
		_, ok := value.(map[string]any)
		return ok
	case kindArray:
		_, ok := value.([]any)
		return ok
	default:
		return false
	}
}

func kindLabel(kind fieldKind) string {
	switch kind {
	case kindString:
		return "string"
	case kindNumber:
		return "number"
	case kindBool:
		return "boolean"
	case kindObject:
		return "object"
	case kindArray:
		return "array"
	default:
		return "known type"
	}
}

func validateKnownNestedFields(value any, path string, problems *[]string) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			childPath := path + "." + key
			switch key {
			case "episode_id", "episode_no", "scene_no", "start_offset", "end_offset", "actual_episode_count", "target_episode_count":
				if child != nil && !matchesKind(child, kindNumber) {
					*problems = append(*problems, childPath+" must be number")
				}
			case "requires_user_attention":
				if !matchesKind(child, kindBool) {
					*problems = append(*problems, childPath+" must be boolean")
				}
			case "hook_strength", "information_density":
				if text, ok := child.(string); !ok || (text != "high" && text != "medium" && text != "low") {
					*problems = append(*problems, childPath+" must be high, medium, or low")
				}
			case "source_mode":
				if text, ok := child.(string); !ok || (text != "novel" && text != "non_novel") {
					*problems = append(*problems, childPath+" must be novel or non_novel")
				}
			case "scenes", "blocks", "lines", "episodes", "episode_cards", "source_refs":
				if child != nil && !matchesKind(child, kindArray) {
					*problems = append(*problems, childPath+" must be array")
				}
			}
			validateKnownNestedFields(child, childPath, problems)
		}
	case []any:
		for index, child := range current {
			validateKnownNestedFields(child, fmt.Sprintf("%s[%d]", path, index), problems)
		}
	}
}

func validateStableIDs(value any, path string, problems *[]string) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			validateStableIDs(child, path+"."+key, problems)
		}
	case []any:
		for _, key := range []string{"episode_id", "scene_id", "line_id", "source_unit_id", "character_id", "id"} {
			seen := map[string]bool{}
			for index, item := range current {
				object, ok := item.(map[string]any)
				if !ok {
					continue
				}
				raw, exists := object[key]
				if !exists {
					continue
				}
				id := strings.TrimSpace(fmt.Sprint(raw))
				if id == "" {
					*problems = append(*problems, fmt.Sprintf("%s[%d].%s must not be empty", path, index, key))
					continue
				}
				if seen[id] {
					*problems = append(*problems, fmt.Sprintf("%s has duplicate %s %q", path, key, id))
				}
				seen[id] = true
			}
		}
		for index, child := range current {
			validateStableIDs(child, fmt.Sprintf("%s[%d]", path, index), problems)
		}
	}
}

func validateEpisodeCollections(artifactType string, payload map[string]any, problems *[]string) {
	field := ""
	switch artifactType {
	case "episode_split":
		field = "episodes"
	case "episode_cards":
		field = "episodes"
	}
	if field == "" {
		return
	}
	items, ok := payload[field].([]any)
	if !ok {
		return
	}
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			*problems = append(*problems, fmt.Sprintf("payload.%s[%d] must be object", field, index))
			continue
		}
		n, ok := numberValue(object["episode_id"])
		if !ok || n <= 0 || math.Trunc(n) != n {
			*problems = append(*problems, fmt.Sprintf("payload.%s[%d].episode_id must be a positive integer", field, index))
		}
	}
}

func numberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case int32:
		return float64(number), true
	case uint:
		return float64(number), true
	case uint64:
		return float64(number), true
	default:
		return 0, false
	}
}
