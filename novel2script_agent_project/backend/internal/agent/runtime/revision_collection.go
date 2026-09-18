package runtime

import (
	"fmt"
	"strings"
)

func (r *Runtime) syncGenerationConfigForCollectionRevision(runID string, artifactType string, payload map[string]any, pack revisionContextPack) {
	fieldPath := firstNonEmpty(pack.Target.FieldPath, pack.FocusedContext.FieldPath)
	if strings.TrimSpace(pack.RevisionIntent) != "patch_artifact_collection" || strings.TrimSpace(fieldPath) != "episodes" {
		return
	}
	if artifactType != "episode_split" && artifactType != "episode_cards" {
		return
	}
	episodes, ok := payload["episodes"].([]any)
	if !ok || len(episodes) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return
	}
	config := authoritativeGenerationConfigLocked(run)
	if len(config) == 0 || intValueOrZero(config["target_episode_count"]) == len(episodes) {
		return
	}
	config["target_episode_count"] = len(episodes)
	run.Metadata = cloneMetadata(run.Metadata)
	run.Metadata["generation_config"] = config
	run.Metadata["generation_config_version"] = generationConfigVersion(run) + 1
	r.runs[runID] = run
}

func applyArtifactCollectionPatch(base map[string]any, candidate map[string]any, targetPath string) bool {
	patch, ok := candidate["__artifact_collection_patch"].(map[string]any)
	if !ok {
		return false
	}
	fieldPath := strings.TrimSpace(fmt.Sprint(patch["field_path"]))
	if fieldPath == "" || (strings.TrimSpace(targetPath) != "" && fieldPath != strings.TrimSpace(targetPath)) {
		return false
	}
	fieldPath = resolveNamedFieldSelectors(fieldPath, base)
	tokens := parseFieldPath(fieldPath)
	value, ok := payloadValueAt(base, tokens)
	if !ok {
		return false
	}
	items, ok := value.([]any)
	if !ok {
		return false
	}
	items = append([]any(nil), items...)
	start := intValueOrZero(patch["start_index"])
	deleteCount := intValueOrZero(patch["delete_count"])
	operation := strings.TrimSpace(fmt.Sprint(patch["operation"]))
	replacements, _ := patch["items"].([]any)
	if start < 0 || deleteCount < 0 {
		return false
	}
	switch operation {
	case "replace_range":
		if deleteCount == 0 || len(replacements) == 0 || start > len(items) || start+deleteCount > len(items) {
			return false
		}
		updated := make([]any, 0, len(items)-deleteCount+len(replacements))
		updated = append(updated, items[:start]...)
		updated = append(updated, replacements...)
		updated = append(updated, items[start+deleteCount:]...)
		items = updated
	case "insert_entities":
		if len(replacements) == 0 || start > len(items) {
			return false
		}
		updated := make([]any, 0, len(items)+len(replacements))
		updated = append(updated, items[:start]...)
		updated = append(updated, replacements...)
		updated = append(updated, items[start:]...)
		items = updated
	case "delete_entities":
		if deleteCount == 0 || start >= len(items) || start+deleteCount > len(items) {
			return false
		}
		items = append(items[:start], items[start+deleteCount:]...)
	case "move_entity":
		if start >= len(items) {
			return false
		}
		moved := items[start]
		items = append(items[:start], items[start+1:]...)
		moveTo := intValueOrZero(patch["move_to"])
		if moveTo < 0 || moveTo > len(items) {
			return false
		}
		items = append(items, nil)
		copy(items[moveTo+1:], items[moveTo:])
		items[moveTo] = moved
	default:
		return false
	}
	if !setPayloadValueAt(base, tokens, items) {
		return false
	}
	syncEpisodeCollectionMetadata(base, fieldPath, items)
	return true
}

func syncEpisodeCollectionMetadata(payload map[string]any, fieldPath string, episodes []any) {
	if strings.TrimSpace(fieldPath) != "episodes" {
		return
	}
	for index, item := range episodes {
		if episode, ok := item.(map[string]any); ok {
			episode["episode_id"] = index + 1
		}
	}
	count := len(episodes)
	for _, key := range []string{"actual_episode_count", "target_episode_count", "episode_count"} {
		if _, exists := payload[key]; exists {
			payload[key] = count
		}
	}
}
