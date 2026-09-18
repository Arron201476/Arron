package runtime

import (
	"context"

	"novel2script-agent/backend/internal/agent"
)

type EpisodeSplitStage string

const (
	EpisodeSplitStageGlobal EpisodeSplitStage = "global_plan"
	EpisodeSplitStageBatch  EpisodeSplitStage = "boundary_batch"
)

type EpisodeSplitSourceUnit struct {
	UnitID      string `json:"unit_id"`
	StartOffset int    `json:"start_offset"`
	EndOffset   int    `json:"end_offset"`
	Text        string `json:"text"`
}

type EpisodeSplitCandidate struct {
	CandidateID     string `json:"candidate_id"`
	EpisodeID       int    `json:"episode_id"`
	Offset          int    `json:"offset"`
	EndUnitID       string `json:"end_unit_id"`
	CutAfterAnchor  string `json:"cut_after_anchor"`
	CutBeforeAnchor string `json:"cut_before_anchor"`
	WindowChars     int    `json:"window_chars"`
}

type EpisodeSplitStageRequest struct {
	Stage              EpisodeSplitStage        `json:"stage"`
	TargetEpisodeCount int                      `json:"target_episode_count"`
	BatchStart         int                      `json:"batch_start,omitempty"`
	BatchEnd           int                      `json:"batch_end,omitempty"`
	SourceUnits        []EpisodeSplitSourceUnit `json:"source_units"`
	StoryBible         map[string]any           `json:"story_bible,omitempty"`
	GenerationConfig   map[string]any           `json:"generation_config"`
	GlobalPlan         map[string]any           `json:"global_plan,omitempty"`
	CompletedEpisodes  []map[string]any         `json:"completed_episodes,omitempty"`
	Candidates         []EpisodeSplitCandidate  `json:"candidates,omitempty"`
}

type EpisodeSplitStageResult struct {
	GlobalPlan map[string]any   `json:"global_plan,omitempty"`
	Episodes   []map[string]any `json:"episodes,omitempty"`
	Risks      []any            `json:"risks,omitempty"`
}

type EpisodeSplitContentWorker interface {
	PlanEpisodeSplitStage(ctx context.Context, run agent.Run, request EpisodeSplitStageRequest) (EpisodeSplitStageResult, error)
}
