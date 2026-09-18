package runtime

import (
	"encoding/json"
	"testing"
)

func TestValidateEpisodeCardsTargetCount(t *testing.T) {
	config := json.RawMessage(`{"config_ref":"creation","payload":{"target_episode_count":3}}`)

	if err := validateEpisodeCardsTargetCount(config, json.RawMessage(`{"episodes":[{"episode_no":1},{"episode_no":2},{"episode_no":3}]}`)); err != nil {
		t.Fatalf("valid cards rejected: %v", err)
	}

	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "missing episode", payload: `{"episodes":[{"episode_no":1},{"episode_no":2}]}`},
		{name: "non-contiguous episode", payload: `{"episodes":[{"episode_no":1},{"episode_no":3},{"episode_no":4}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateEpisodeCardsTargetCount(config, json.RawMessage(test.payload))
			assertDomainCode(t, err, "BATCH_COVERAGE_INVALID")
		})
	}
}
