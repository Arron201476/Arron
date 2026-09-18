package worker

import (
	"regexp"
	"strconv"
	"strings"

	"novel2script-agent/backend/internal/agent"
)

func workerTraceContext(run agent.Run, operation string, artifactType string, episodeID int) map[string]string {
	trace := map[string]string{
		"component":     "content",
		"operation":     strings.TrimSpace(operation),
		"project_id":    strings.TrimSpace(run.ProjectID),
		"run_id":        strings.TrimSpace(run.RunID),
		"artifact_type": strings.TrimSpace(artifactType),
	}
	if episodeID > 0 {
		trace["episode_id"] = strconv.Itoa(episodeID)
	}
	if taskID := workerTaskID(operation, artifactType, episodeID); taskID != "" {
		trace["task_id"] = taskID
	}
	for key, value := range trace {
		if value == "" {
			delete(trace, key)
		}
	}
	return trace
}

func workerTaskID(operation string, artifactType string, episodeID int) string {
	switch operation {
	case "build_script_context":
		if artifactType == "script_context" {
			return "task_build_script_context_model_request"
		}
	case "write_script", "write_script_length_correction":
		if artifactType == "script_unit" && episodeID > 0 {
			return "task_generate_script_episode_" + strconv.Itoa(episodeID)
		}
	case "plan_artifact":
		if capability := artifactCapabilityName(artifactType); capability != "" {
			return "task_" + capability + "_model_request"
		}
	case "patch_artifact":
		if capability := artifactCapabilityName(artifactType); capability != "" {
			return "task_" + capability + "_patch_request"
		}
	}
	return ""
}

func artifactCapabilityName(artifactType string) string {
	switch artifactType {
	case "story_bible":
		return "build_story_bible"
	case "episode_split":
		return "split_episodes"
	case "material_bank":
		return "build_material_bank"
	case "story_seed":
		return "build_story_seed"
	case "series_blueprint":
		return "plan_series_blueprint"
	case "episode_cards":
		return "plan_episode_cards"
	case "script_context":
		return "build_script_context"
	case "script_unit":
		return "generate_script_unit"
	default:
		return ""
	}
}

func episodeNumberText(value string) int {
	parsed, _ := strconv.Atoi(strings.TrimSpace(value))
	return parsed
}

var correctionAttemptPattern = regexp.MustCompile(`(?i)LENGTH CORRECTION ATTEMPT\s+(\d+)`)

func correctionAttemptFromNote(note string) int {
	match := correctionAttemptPattern.FindStringSubmatch(note)
	if len(match) < 2 {
		return 0
	}
	attempt, _ := strconv.Atoi(match[1])
	return attempt
}
