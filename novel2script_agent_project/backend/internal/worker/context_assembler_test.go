package worker

import (
	"fmt"
	"strings"
	"testing"

	"novel2script-agent/backend/internal/agent"
)

func TestContextAssemblerBoundsLongNovelAndSelectsEpisodeNeighborhood(t *testing.T) {
	t.Setenv("N2S_CONTEXT_MAX_CHARS", "12000")
	sourceText := strings.Repeat("甲乙丙丁", 100000)
	source := map[string]any{
		"text":              sourceText,
		"generation_config": map[string]any{"target_episode_count": 30},
	}
	artifacts := []agent.Artifact{{ArtifactID: "source", ArtifactType: "source_input", Status: agent.ArtifactConfirmed, Payload: source}}
	for episode := 1; episode <= 30; episode++ {
		artifacts = append(artifacts, agent.Artifact{
			ArtifactID: "unit_" + string(rune('A'+episode)), ArtifactType: "script_unit", Status: agent.ArtifactConfirmed,
			Payload: map[string]any{"episode_id": episode, "script_text": strings.Repeat("历史剧本", 1000)},
		})
	}
	artifacts = append(artifacts, agent.Artifact{ArtifactID: "scripts", ArtifactType: "scripts", Status: agent.ArtifactConfirmed, Payload: map[string]any{"script_units": strings.Repeat("重复聚合", 100000)}})

	assembled := assembleWorkerContext(source, artifacts, "script_unit", "TASK: Generate only episode_id=20")
	if chars := contextJSONChars(assembled); chars > 13000 {
		t.Fatalf("assembled context exceeded bounded allowance: %d", chars)
	}
	seenEpisodes := map[int]bool{}
	for _, item := range assembled.Artifacts {
		if item["artifact_type"] == "scripts" {
			t.Fatal("derived scripts aggregate must not be included")
		}
		if item["artifact_type"] == "script_unit" {
			payload := item["payload"].(map[string]any)
			seenEpisodes[intValue(payload["episode_id"])] = true
		}
	}
	if !seenEpisodes[19] || !seenEpisodes[20] || len(seenEpisodes) != 2 {
		t.Fatalf("expected only previous and current episodes, got %#v", seenEpisodes)
	}
	sourceWindow, _ := assembled.Source["text"].(string)
	if !strings.Contains(sourceWindow, "source_window episode=20/30") {
		t.Fatalf("expected episode-scoped source window, got prefix %.80s", sourceWindow)
	}
}

func TestContextAssemblerSamplesWholeSourceForPlanning(t *testing.T) {
	t.Setenv("N2S_CONTEXT_MAX_CHARS", "8000")
	source := map[string]any{"text": "START_MARK" + strings.Repeat("中", 50000) + "END_MARK"}
	assembled := assembleWorkerContext(source, nil, "story_bible", "")
	text, _ := assembled.Source["text"].(string)
	if !strings.Contains(text, "START_MARK") || !strings.Contains(text, "END_MARK") {
		t.Fatalf("planning source index must sample beginning and end")
	}
	if contextJSONChars(assembled.Source) > 5000 {
		t.Fatalf("source planning context exceeded its budget: %d", contextJSONChars(assembled.Source))
	}
}

func TestContextAssemblerFiltersEpisodeCardsToNeighbors(t *testing.T) {
	episodes := make([]any, 0, 10)
	for episode := 1; episode <= 10; episode++ {
		episodes = append(episodes, map[string]any{"episode_id": episode, "hook": strings.Repeat("钩子", 100)})
	}
	artifacts := []agent.Artifact{{ArtifactID: "cards", ArtifactType: "episode_cards", Status: agent.ArtifactConfirmed, Payload: map[string]any{"episodes": episodes}}}
	assembled := assembleWorkerContext(map[string]any{}, artifacts, "script_unit", "episode_id=6")
	payload := assembled.Artifacts[0]["payload"].(map[string]any)
	filtered := payload["episodes"].([]any)
	if len(filtered) != 3 || intValue(filtered[0].(map[string]any)["episode_id"]) != 5 || intValue(filtered[2].(map[string]any)["episode_id"]) != 7 {
		t.Fatalf("expected cards 5-7, got %#v", filtered)
	}
}

func TestContextAssemblerUsesEpisodeSplitSourceOffsets(t *testing.T) {
	t.Setenv("N2S_CONTEXT_MAX_CHARS", "8000")
	text := strings.Repeat("前", 10000) + "EXACT_TARGET_SOURCE" + strings.Repeat("后", 10000)
	artifacts := []agent.Artifact{{
		ArtifactID: "split", ArtifactType: "episode_split", Status: agent.ArtifactConfirmed,
		Payload: map[string]any{"episodes": []any{map[string]any{
			"episode_id": 2, "source_refs": []any{map[string]any{"start_offset": 9950, "end_offset": 10100}},
		}}},
	}}
	assembled := assembleWorkerContext(map[string]any{"text": text}, artifacts, "script_unit", "episode_id=2")
	window, _ := assembled.Source["text"].(string)
	if !strings.Contains(window, "source_refs episode=2 offsets=9950-10100") || !strings.Contains(window, "EXACT_TARGET_SOURCE") {
		t.Fatalf("expected source_refs window around exact offsets, got %.160s", window)
	}
}

func TestContextAssemblerScopesShortSourceWhenEpisodeOffsetsExist(t *testing.T) {
	text := strings.Repeat("甲", 12000)
	artifacts := []agent.Artifact{{
		ArtifactID: "split", ArtifactType: "episode_split", Status: agent.ArtifactConfirmed,
		Payload: map[string]any{"episodes": []any{map[string]any{
			"episode_id": 2, "source_refs": []any{map[string]any{"start_offset": 2000, "end_offset": 4000}},
		}}},
	}}
	note := `USER_REVISION_REQUEST:
拆分第二集
REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_collection","target":{"artifact_id":"split","artifact_type":"episode_split","episode_id":"2","field_path":"episodes","scope":"collection"}}`
	assembled := assembleWorkerContext(map[string]any{"text": text}, artifacts, "episode_split", note)
	window := fmt.Sprint(assembled.Source["text"])
	if !strings.Contains(window, "source_refs episode=2 offsets=2000-4000") {
		t.Fatalf("short source was not scoped to the target episode: %.120s", window)
	}
	if len([]rune(window)) >= len([]rune(text)) {
		t.Fatalf("targeted revision received the full source: %d >= %d", len([]rune(window)), len([]rune(text)))
	}
	for _, artifact := range assembled.Artifacts {
		if artifact["artifact_id"] == "split" {
			t.Fatal("target artifact must not be duplicated in available artifact digest")
		}
	}
}
