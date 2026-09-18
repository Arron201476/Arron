package main

import (
	"testing"

	"novel2script-agent/backend/internal/agent"
)

func TestGenerationConfigWithDetectedMarkersDoesNotForcePreserve(t *testing.T) {
	config := generationConfigWithDetectedMarkers(nil, "第1章 开始\n正文\n第2章 结束", false)
	if config == nil || !config.ExistingEpisodeMarkersDetected || config.DetectedEpisodeCount != 2 {
		t.Fatalf("marker detection was not carried into config: %#v", config)
	}
	if config.PreserveExistingEpisodeMarks || config.TargetEpisodeCount != 0 {
		t.Fatalf("detection must not select preserve mode for the user: %#v", config)
	}
}

func TestGenerationConfigOverridesModelMarkerGuesses(t *testing.T) {
	config := generationConfigWithDetectedMarkers(&agent.GenerationConfig{
		PreserveExistingEpisodeMarks: true, ExistingEpisodeMarkersDetected: true,
	}, "没有可靠原分集标记的正文", false)
	if config != nil && (config.PreserveExistingEpisodeMarks || config.ExistingEpisodeMarkersDetected || config.DetectedEpisodeCount != 0) {
		t.Fatalf("model marker guesses must not remain authoritative: %#v", config)
	}
}

func TestGenerationConfigPreserveRequiresExplicitUserSelection(t *testing.T) {
	config := generationConfigWithDetectedMarkers(&agent.GenerationConfig{
		PreserveExistingEpisodeMarks: true,
	}, "第1章 开始\n正文\n第2章 结束", false)
	if config.PreserveExistingEpisodeMarks {
		t.Fatalf("model must not select preserve mode: %#v", config)
	}
	config = generationConfigWithDetectedMarkers(config, "第1章 开始\n正文\n第2章 结束", true)
	if !config.PreserveExistingEpisodeMarks || config.TargetEpisodeCount != 2 {
		t.Fatalf("explicit selection should use detected count: %#v", config)
	}
}

func TestValidatedGenerationConfigUsesDetectedCountWhenPreserving(t *testing.T) {
	config, err := validatedGenerationConfigForSource(&agent.GenerationConfig{
		TargetEpisodeCount: 99, EpisodeDurationMinutes: 1.5, PreserveExistingEpisodeMarks: true,
	}, "1\n第一段\n2\n第二段\n3\n第三段")
	if err != nil {
		t.Fatal(err)
	}
	if config.TargetEpisodeCount != 3 || config.DetectedEpisodeCount != 3 || !config.ExistingEpisodeMarkersDetected {
		t.Fatalf("preserve config is inconsistent: %#v", config)
	}
}

func TestValidatedGenerationConfigRejectsPreserveWithoutMarkers(t *testing.T) {
	_, err := validatedGenerationConfigForSource(&agent.GenerationConfig{
		EpisodeDurationMinutes: 1.5, PreserveExistingEpisodeMarks: true,
	}, "这是一段没有原分集标记的连续正文。")
	if err == nil {
		t.Fatal("expected preserve mode without markers to be rejected")
	}
}
