package worker

import "testing"

func TestParseEpisodeCardsBatchAcceptsDocumentedAndLegacyEpisodeKeys(t *testing.T) {
	for _, response := range []string{
		`{"episodes":[{"episode_id":1}],"continuity_delta":{},"batch_risks":[]}`,
		`{"artifact_type":"episode_cards","payload":{"episode_cards":[{"episode_id":1}],"continuity_delta":{}}}`,
	} {
		result, err := parseEpisodeCardsBatchResult(response)
		if err != nil || len(result.Episodes) != 1 {
			t.Fatalf("unexpected parse result for %s: result=%#v err=%v", response, result, err)
		}
	}
}

func TestParseEpisodeCardsBatchLeavesEmptyCoverageForRuntimeValidation(t *testing.T) {
	result, err := parseEpisodeCardsBatchResult(`{"episodes":[],"continuity_delta":{}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Episodes) != 0 {
		t.Fatalf("expected empty coverage, got %#v", result.Episodes)
	}
}
