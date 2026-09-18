package runtime

import "content-agent/backend/internal/capability"

const (
	scriptQualityReviewExecutorID   = "workflow.shared_script_quality_review"
	videoScriptExtractionExecutorID = "workflow.video_script_extract"
	episodeSplitExecutorID          = "workflow.novel_episode_split"
	qualityReviewOperation          = "quality_review"
)

func isScriptQualityReviewStep(step capability.CompiledStep) bool {
	return step.Kind == "review" && step.ExecutorRef == scriptQualityReviewExecutorID
}

func isVideoScriptExtractionStep(step capability.CompiledStep) bool {
	return step.ExecutorRef == videoScriptExtractionExecutorID && step.Batch != nil &&
		step.Batch.ItemKey == "asset_id"
}

func isEpisodeSplitStep(step capability.CompiledStep) bool {
	return step.ExecutorRef == episodeSplitExecutorID && step.Batch != nil &&
		step.Batch.Preparation != nil
}

func scriptQualityReviewStep(
	definition *capability.CompiledDefinition,
	stepID string,
) *capability.CompiledStep {
	if definition == nil {
		return nil
	}
	step := compiledStep(definition.Steps, stepID)
	if step == nil || !isScriptQualityReviewStep(*step) {
		return nil
	}
	return step
}
