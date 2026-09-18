package runtime

import (
	"testing"

	"content-agent/backend/internal/capability"
)

func TestSpecialWorkflowContractsUseExecutorMetadata(t *testing.T) {
	review := capability.CompiledStep{
		ID: "custom_review_gate", Kind: "review",
		ExecutorRef: scriptQualityReviewExecutorID,
	}
	video := capability.CompiledStep{
		ID: "custom_video_extraction", ExecutorRef: videoScriptExtractionExecutorID,
		Batch: &capability.BatchPolicy{ItemKey: "asset_id"},
	}
	split := capability.CompiledStep{
		ID: "custom_episode_boundaries", ExecutorRef: episodeSplitExecutorID,
		Batch: &capability.BatchPolicy{
			Preparation: &capability.BatchInternalTaskStage{ID: "prepare"},
		},
	}

	if !isScriptQualityReviewStep(review) {
		t.Fatal("custom quality review step was rejected")
	}
	if !isVideoScriptExtractionStep(video) {
		t.Fatal("custom video extraction step was rejected")
	}
	if !isEpisodeSplitStep(split) {
		t.Fatal("custom episode split step was rejected")
	}
}
