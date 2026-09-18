package runtime

import (
	"encoding/json"
	"fmt"

	"content-agent/backend/internal/capability"
)

func normalizeAndValidateArtifactOutput(step capability.CompiledStep, output capability.ArtifactOutput, pack StepExecutionContextPack, payload json.RawMessage) (json.RawMessage, error) {
	var err error
	if output.ArtifactType == "video_script_unit" {
		payload, err = normalizeVideoScriptCharacterContinuity(pack.TaskCursor, payload)
		if err != nil {
			return nil, domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", fmt.Sprintf("视频剧本未通过正文和角色连续性校验：%v", err))
		}
	}
	if output.ArtifactType == "episode_cards" && step.Batch == nil {
		if err := validateEpisodeCardsTargetCount(pack.ConfigSnapshot, payload); err != nil {
			return nil, err
		}
	}
	if output.ArtifactType == "continuation_options" {
		if err := validateContinuationOptions(payload); err != nil {
			return nil, err
		}
	}
	if output.ArtifactType == "continuation_script" {
		if err := validateContinuationScriptSelection(pack.DecisionSnapshots, payload); err != nil {
			return nil, err
		}
		if err := validateContinuationScriptLength(pack.ConfigSnapshot, payload); err != nil {
			return nil, err
		}
	}
	if stage, ok := batchStageForCursor(step.Batch, pack.TaskCursor); ok &&
		stage.ResultMode == "artifact" && output.ArtifactType == "story_bible" {
		if err := validateStoryBibleAgainstSourceAnalysis(pack.TaskCursor, payload); err != nil {
			return nil, err
		}
	}
	if output.ArtifactType == "material_bank" {
		if err := validateMaterialBankClaims(pack.UpstreamContext, payload); err != nil {
			return nil, err
		}
	}
	return payload, nil
}
