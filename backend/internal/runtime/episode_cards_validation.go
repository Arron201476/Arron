package runtime

import (
	"encoding/json"
	"fmt"
)

func validateEpisodeCardsTargetCount(config, payload json.RawMessage) error {
	targetCount, err := targetEpisodeCount(config)
	if err != nil {
		return err
	}
	var cards struct {
		Episodes []struct {
			EpisodeNo int `json:"episode_no"`
		} `json:"episodes"`
	}
	if err := json.Unmarshal(payload, &cards); err != nil {
		return domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "分集卡产物无法读取。")
	}
	if len(cards.Episodes) != targetCount {
		return domainError(
			"BATCH_COVERAGE_INVALID",
			fmt.Sprintf("分集卡数量与目标集数不一致：目标 %d 集，实际 %d 集。", targetCount, len(cards.Episodes)),
		)
	}
	for index, episode := range cards.Episodes {
		expected := index + 1
		if episode.EpisodeNo != expected {
			return domainError(
				"BATCH_COVERAGE_INVALID",
				fmt.Sprintf("分集卡集号必须从 1 连续排列：第 %d 项的集号为 %d。", expected, episode.EpisodeNo),
			)
		}
	}
	return nil
}
