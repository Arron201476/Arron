package runtime

import (
	"context"

	"novel2script-agent/backend/internal/agent"
)

type EpisodeCardsBatchRequest struct {
	TargetEpisodeCount int              `json:"target_episode_count"`
	BatchStart         int              `json:"batch_start"`
	BatchEnd           int              `json:"batch_end"`
	SourceMode         agent.SourceMode `json:"source_mode"`
	GenerationConfig   map[string]any   `json:"generation_config"`
	Upstream           map[string]any   `json:"upstream"`
	CompletedEpisodes  []map[string]any `json:"completed_episodes,omitempty"`
	UserInstruction    string           `json:"user_instruction,omitempty"`
}

type EpisodeCardsBatchResult struct {
	Episodes        []map[string]any `json:"episodes"`
	ContinuityDelta map[string]any   `json:"continuity_delta,omitempty"`
	Risks           []any            `json:"batch_risks,omitempty"`
}

type EpisodeCardsContentWorker interface {
	PlanEpisodeCardsBatch(context.Context, agent.Run, EpisodeCardsBatchRequest) (EpisodeCardsBatchResult, error)
}

type episodeCardsIncompleteError struct {
	BatchStart int
	BatchEnd   int
	Actual     int
	Expected   int
}

func (e *episodeCardsIncompleteError) Error() string {
	return "episode cards batch returned incomplete episode coverage"
}
