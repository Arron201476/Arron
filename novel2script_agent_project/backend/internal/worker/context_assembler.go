package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"novel2script-agent/backend/internal/agent"
)

const defaultContextMaxChars = 48000

var episodeIDPattern = regexp.MustCompile(`(?i)episode_id\s*[=:]\s*["']?([0-9]+)`)

type assembledContext struct {
	Artifacts []map[string]any
	Source    map[string]any
}

func sourcePayloadFromArtifacts(artifacts []agent.Artifact) map[string]any {
	for _, artifact := range currentEffectiveArtifacts(artifacts) {
		if artifact.ArtifactType == "source_input" {
			return artifact.Payload
		}
	}
	return map[string]any{}
}

func artifactContextWithSource(assembled assembledContext) []map[string]any {
	out := make([]map[string]any, 0, len(assembled.Artifacts)+1)
	if len(assembled.Source) > 0 {
		out = append(out, map[string]any{
			"artifact_type": "source_input",
			"status":        agent.ArtifactConfirmed,
			"payload":       assembled.Source,
		})
	}
	return append(out, assembled.Artifacts...)
}

func assembleWorkerContext(source map[string]any, artifacts []agent.Artifact, artifactType string, note string) assembledContext {
	maxChars := contextMaxChars()
	pack := parseRevisionPatchPack(note)
	if revisionLimit := revisionContextMaxChars(pack.RevisionIntent); revisionLimit > 0 && revisionLimit < maxChars {
		maxChars = revisionLimit
	}
	targetEpisode := episodeFromNote(note)
	if structuredEpisode := intValue(pack.Target.EpisodeID); structuredEpisode > 0 {
		targetEpisode = structuredEpisode
	}
	totalEpisodes := episodeCount(source, artifacts)
	selected := selectContextArtifacts(artifacts, artifactType, targetEpisode, pack.Target.ArtifactID)
	sourceStart, sourceEnd, hasSourceRange := sourceOffsetsForEpisode(artifacts, targetEpisode)
	sourceBudget := maxChars / 2
	if pack.RevisionIntent != "" {
		sourceBudget = maxChars * 3 / 10
	}
	artifactBudget := maxChars - sourceBudget
	return assembledContext{
		Source:    compactSourcePayload(source, artifactType, targetEpisode, totalEpisodes, sourceStart, sourceEnd, hasSourceRange, sourceBudget),
		Artifacts: compactArtifactDigest(selected, artifactType, targetEpisode, artifactBudget),
	}
}

func revisionContextMaxChars(intent string) int {
	switch strings.TrimSpace(intent) {
	case "patch_script_span":
		return 12000
	case "regenerate_script_scene":
		return 22000
	case "patch_artifact_field", "patch_artifact_entity", "patch_artifact_section", "patch_artifact_collection":
		return 20000
	default:
		return 0
	}
}

func contextMaxChars() int {
	raw := strings.TrimSpace(os.Getenv("N2S_CONTEXT_MAX_CHARS"))
	if raw == "" {
		return defaultContextMaxChars
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 8000 || value > 200000 {
		return defaultContextMaxChars
	}
	return value
}

func episodeFromNote(note string) int {
	match := episodeIDPattern.FindStringSubmatch(note)
	if len(match) != 2 {
		return 0
	}
	value, _ := strconv.Atoi(match[1])
	return value
}

func episodeCount(source map[string]any, artifacts []agent.Artifact) int {
	if config, ok := source["generation_config"].(map[string]any); ok {
		if count := intValue(config["target_episode_count"]); count > 0 {
			return count
		}
	}
	for _, artifact := range currentEffectiveArtifacts(artifacts) {
		if artifact.ArtifactType != "episode_cards" && artifact.ArtifactType != "episode_split" {
			continue
		}
		if episodes, ok := artifact.Payload["episodes"].([]any); ok && len(episodes) > 0 {
			return len(episodes)
		}
	}
	return 1
}

func selectContextArtifacts(artifacts []agent.Artifact, requestedType string, targetEpisode int, targetArtifactID string) []agent.Artifact {
	effective := currentEffectiveArtifacts(artifacts)
	selected := make([]agent.Artifact, 0, len(effective))
	for _, artifact := range effective {
		if artifact.ArtifactType == "scripts" || artifact.ArtifactType == "source_input" {
			continue
		}
		if targetArtifactID != "" && artifact.ArtifactID == targetArtifactID {
			continue
		}
		if artifact.ArtifactType == "script_unit" && targetEpisode > 0 {
			episode := intValue(artifact.Payload["episode_id"])
			if episode < targetEpisode-1 || episode > targetEpisode {
				continue
			}
		}
		selected = append(selected, artifact)
	}
	sort.SliceStable(selected, func(i, j int) bool {
		return contextPriority(selected[i].ArtifactType, requestedType) < contextPriority(selected[j].ArtifactType, requestedType)
	})
	return selected
}

func contextPriority(artifactType string, requestedType string) int {
	if artifactType == requestedType {
		return 0
	}
	order := map[string]int{
		"source_input": 1, "story_bible": 2, "material_bank": 2, "story_seed": 3,
		"series_blueprint": 4, "episode_split": 5, "episode_cards": 6,
		"script_context": 7, "script_unit": 8,
	}
	if value, ok := order[artifactType]; ok {
		return value
	}
	return 20
}

func compactSourcePayload(source map[string]any, artifactType string, targetEpisode int, totalEpisodes int, sourceStart int, sourceEnd int, hasSourceRange bool, budget int) map[string]any {
	out := cloneContextMap(source)
	textBudget := budget - 1000
	if textBudget < 1000 {
		textBudget = 1000
	}
	for _, key := range []string{"text", "source_text", "content"} {
		text, ok := out[key].(string)
		if !ok {
			continue
		}
		if hasSourceRange {
			windowBudget := sourceEnd - sourceStart + 1600
			if windowBudget < 3200 {
				windowBudget = 3200
			}
			if windowBudget > textBudget {
				windowBudget = textBudget
			}
			out[key] = offsetSourceWindow(text, targetEpisode, sourceStart, sourceEnd, windowBudget)
		} else if targetEpisode > 0 {
			windowBudget := textBudget
			if windowBudget > 8000 {
				windowBudget = 8000
			}
			out[key] = episodeSourceWindow(text, targetEpisode, totalEpisodes, windowBudget)
		} else if len([]rune(text)) > textBudget {
			out[key] = sampledSourceIndex(text, textBudget)
		}
	}
	return fitContextMap(out, budget)
}

func sourceOffsetsForEpisode(artifacts []agent.Artifact, targetEpisode int) (int, int, bool) {
	if targetEpisode <= 0 {
		return 0, 0, false
	}
	for _, artifact := range currentEffectiveArtifacts(artifacts) {
		if artifact.ArtifactType != "episode_split" {
			continue
		}
		episodes, _ := artifact.Payload["episodes"].([]any)
		for index, value := range episodes {
			episode, _ := value.(map[string]any)
			episodeID := intValue(episode["episode_id"])
			if episodeID == 0 {
				episodeID = index + 1
			}
			if episodeID != targetEpisode {
				continue
			}
			refs, _ := episode["source_refs"].([]any)
			start, end := 0, 0
			for _, refValue := range refs {
				ref, _ := refValue.(map[string]any)
				candidateStart := intValue(ref["start_offset"])
				candidateEnd := intValue(ref["end_offset"])
				if candidateEnd <= candidateStart {
					continue
				}
				if start == 0 || candidateStart < start {
					start = candidateStart
				}
				if candidateEnd > end {
					end = candidateEnd
				}
			}
			if end > start {
				return start, end, true
			}
		}
	}
	return 0, 0, false
}

func offsetSourceWindow(text string, episode int, sourceStart int, sourceEnd int, budget int) string {
	runes := []rune(text)
	if sourceStart < 0 {
		sourceStart = 0
	}
	if sourceEnd > len(runes) {
		sourceEnd = len(runes)
	}
	if sourceEnd <= sourceStart {
		return episodeSourceWindow(text, episode, 1, budget)
	}
	padding := (budget - (sourceEnd - sourceStart)) / 2
	if padding < 0 {
		padding = 0
	}
	start := sourceStart - padding
	if start < 0 {
		start = 0
	}
	end := sourceEnd + padding
	if end > len(runes) {
		end = len(runes)
	}
	if end-start > budget {
		end = start + budget
	}
	return fmt.Sprintf("[source_refs episode=%d offsets=%d-%d window=%d-%d]\n%s", episode, sourceStart, sourceEnd, start, end, string(runes[start:end]))
}

func compactArtifactDigest(artifacts []agent.Artifact, requestedType string, targetEpisode int, budget int) []map[string]any {
	if len(artifacts) == 0 {
		return []map[string]any{}
	}
	perArtifact := budget / len(artifacts)
	if perArtifact < 1200 {
		perArtifact = 1200
	}
	out := make([]map[string]any, 0, len(artifacts))
	for _, artifact := range artifacts {
		payload := cloneContextMap(artifact.Payload)
		if targetEpisode > 0 && (artifact.ArtifactType == "episode_cards" || artifact.ArtifactType == "episode_split") {
			payload = filterEpisodePayload(payload, targetEpisode)
		}
		out = append(out, map[string]any{
			"artifact_id": artifact.ArtifactID, "artifact_type": artifact.ArtifactType,
			"status": artifact.Status, "version": artifact.Version,
			"payload": fitContextMap(payload, perArtifact),
		})
	}
	for contextJSONChars(out) > budget && len(out) > 1 {
		out = out[:len(out)-1]
	}
	return out
}

func filterEpisodePayload(payload map[string]any, targetEpisode int) map[string]any {
	episodes, ok := payload["episodes"].([]any)
	if !ok {
		return payload
	}
	filtered := make([]any, 0, 3)
	for index, value := range episodes {
		item, _ := value.(map[string]any)
		episode := intValue(item["episode_id"])
		if episode == 0 {
			episode = index + 1
		}
		if episode >= targetEpisode-1 && episode <= targetEpisode+1 {
			filtered = append(filtered, value)
		}
	}
	payload["episodes"] = filtered
	return payload
}

func episodeSourceWindow(text string, episode int, total int, budget int) string {
	runes := []rune(text)
	if len(runes) <= budget {
		return text
	}
	if total < 1 {
		total = 1
	}
	if episode < 1 {
		episode = 1
	}
	if episode > total {
		episode = total
	}
	center := int((float64(episode) - 0.5) / float64(total) * float64(len(runes)))
	start := center - budget/2
	if start < 0 {
		start = 0
	}
	if start+budget > len(runes) {
		start = len(runes) - budget
	}
	return fmt.Sprintf("[source_window episode=%d/%d chars=%d-%d]\n%s", episode, total, start, start+budget, string(runes[start:start+budget]))
}

func sampledSourceIndex(text string, budget int) string {
	runes := []rune(text)
	if len(runes) <= budget {
		return text
	}
	const samples = 8
	chunk := budget / samples
	if chunk < 256 {
		chunk = 256
	}
	var builder strings.Builder
	for index := 0; index < samples; index++ {
		start := int(float64(index) / float64(samples-1) * float64(len(runes)-chunk))
		end := start + chunk
		if end > len(runes) {
			end = len(runes)
		}
		builder.WriteString(fmt.Sprintf("\n[source_sample %d/%d chars=%d-%d]\n", index+1, samples, start, end))
		builder.WriteString(string(runes[start:end]))
	}
	return builder.String()
}

func fitContextMap(value map[string]any, budget int) map[string]any {
	if contextJSONChars(value) <= budget {
		return value
	}
	for _, limits := range [][2]int{{2000, 24}, {1000, 12}, {500, 6}, {240, 3}} {
		compacted, _ := compactContextValue(value, limits[0], limits[1], 0).(map[string]any)
		if contextJSONChars(compacted) <= budget {
			return compacted
		}
		value = compacted
	}
	return value
}

func compactContextValue(value any, stringLimit int, arrayLimit int, depth int) any {
	if depth > 8 {
		return "[depth_limited]"
	}
	switch typed := value.(type) {
	case string:
		runes := []rune(typed)
		if len(runes) <= stringLimit {
			return typed
		}
		half := stringLimit / 2
		return string(runes[:half]) + "\n...[truncated]...\n" + string(runes[len(runes)-half:])
	case []any:
		limit := len(typed)
		if limit > arrayLimit {
			limit = arrayLimit
		}
		out := make([]any, 0, limit+1)
		for _, item := range typed[:limit] {
			out = append(out, compactContextValue(item, stringLimit, arrayLimit, depth+1))
		}
		if limit < len(typed) {
			out = append(out, map[string]any{"truncated_items": len(typed) - limit})
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = compactContextValue(item, stringLimit, arrayLimit, depth+1)
		}
		return out
	default:
		return value
	}
}

func cloneContextMap(value map[string]any) map[string]any {
	data, _ := json.Marshal(value)
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return out
}

func contextJSONChars(value any) int {
	data, _ := json.Marshal(value)
	return len([]rune(string(data)))
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		value, _ := typed.Int64()
		return int(value)
	case string:
		value, _ := strconv.Atoi(strings.TrimSpace(typed))
		return value
	default:
		return 0
	}
}
