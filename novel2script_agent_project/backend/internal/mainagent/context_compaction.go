package mainagent

import (
	"fmt"
	"sort"
	"strings"

	"novel2script-agent/backend/internal/agent"
)

func compactDecisionInput(input Context) Context {
	if input.Project != nil {
		project := *input.Project
		if len(project.Files) > 12 {
			project.Files = append([]ProjectFile(nil), project.Files[len(project.Files)-12:]...)
		} else {
			project.Files = append([]ProjectFile(nil), project.Files...)
		}
		for index := range project.Files {
			project.Files[index].TextPreview = truncateRunes(project.Files[index].TextPreview, 400)
		}
		input.Project = &project
	}
	if input.Conversation != nil {
		conversation := *input.Conversation
		turns := conversation.RecentTurns
		if len(turns) > 6 {
			turns = turns[len(turns)-6:]
		}
		conversation.RecentTurns = append([]ConversationTurn(nil), turns...)
		for index := range conversation.RecentTurns {
			conversation.RecentTurns[index].Content = truncateRunes(conversation.RecentTurns[index].Content, 800)
			conversation.RecentTurns[index].SelectionContext = compactModelMap(conversation.RecentTurns[index].SelectionContext, 1)
		}
		conversation.Summary = truncateRunes(conversation.Summary, 1600)
		input.Conversation = &conversation
	}
	if input.Run != nil {
		run := *input.Run
		run.NextAction = compactModelMap(input.Run.NextAction, 3)
		run.Metadata = compactModelMap(input.Run.Metadata, 3)
		run.CreatedArtifacts = tailStrings(input.Run.CreatedArtifacts, 24)
		run.UpdatedArtifacts = tailStrings(input.Run.UpdatedArtifacts, 24)
		run.Invalidated = tailStrings(input.Run.Invalidated, 24)
		input.Run = &run
	}
	if input.CurrentStepContext != nil {
		current := *input.CurrentStepContext
		current.PayloadExcerpt = compactModelMap(input.CurrentStepContext.PayloadExcerpt, 2)
		input.CurrentStepContext = &current
	}
	if len(input.EventDigest) > 8 {
		input.EventDigest = append([]EventDigest(nil), input.EventDigest[len(input.EventDigest)-8:]...)
	}
	for index := range input.EventDigest {
		input.EventDigest[index].Message = truncateRunes(input.EventDigest[index].Message, 400)
		input.EventDigest[index].Payload = compactModelMap(input.EventDigest[index].Payload, 1)
	}
	if len(input.EventDigest) > 0 {
		input.Events = nil
	} else if len(input.Events) > 8 {
		input.Events = append([]agent.RunEvent(nil), input.Events[len(input.Events)-8:]...)
	}
	if len(input.Artifacts) > 24 {
		input.Artifacts = append([]ArtifactDigest(nil), input.Artifacts[len(input.Artifacts)-24:]...)
	}
	return input
}

func controlTraceContext(input Context, operation string) map[string]string {
	trace := map[string]string{"component": "control", "operation": operation}
	if projectID := strings.TrimSpace(input.Request.ProjectID); projectID != "" {
		trace["project_id"] = projectID
	} else if input.Project != nil && strings.TrimSpace(input.Project.ProjectID) != "" {
		trace["project_id"] = strings.TrimSpace(input.Project.ProjectID)
	}
	if runID := strings.TrimSpace(input.Request.RunID); runID != "" {
		trace["run_id"] = runID
	} else if input.Run != nil && strings.TrimSpace(input.Run.RunID) != "" {
		trace["run_id"] = strings.TrimSpace(input.Run.RunID)
	}
	if input.FocusedContext != nil {
		trace["artifact_type"] = strings.TrimSpace(input.FocusedContext.ArtifactType)
		trace["artifact_id"] = strings.TrimSpace(input.FocusedContext.ArtifactID)
		trace["episode_id"] = strings.TrimSpace(input.FocusedContext.EpisodeID)
	}
	for key, value := range trace {
		if strings.TrimSpace(value) == "" {
			delete(trace, key)
		}
	}
	return trace
}

func compactModelMap(input map[string]any, depth int) map[string]any {
	if len(input) == 0 {
		return nil
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 20 {
		keys = keys[:20]
	}
	output := make(map[string]any, len(keys)+1)
	for _, key := range keys {
		output[key] = compactModelValue(input[key], depth)
	}
	if len(input) > len(keys) {
		output["_truncated"] = true
	}
	return output
}

func compactModelValue(value any, depth int) any {
	if depth <= 0 {
		return truncateRunes(fmt.Sprint(value), 240)
	}
	switch typed := value.(type) {
	case string:
		return truncateRunes(typed, 600)
	case []string:
		return tailStrings(typed, 12)
	case []any:
		limit := len(typed)
		if limit > 8 {
			limit = 8
		}
		output := make([]any, 0, limit)
		for index := 0; index < limit; index++ {
			output = append(output, compactModelValue(typed[index], depth-1))
		}
		return output
	case map[string]any:
		return compactModelMap(typed, depth-1)
	default:
		return typed
	}
}

func tailStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) == 0 {
		return nil
	}
	start := len(values) - limit
	if start < 0 {
		start = 0
	}
	output := append([]string(nil), values[start:]...)
	for index := range output {
		output[index] = truncateRunes(output[index], 160)
	}
	return output
}
