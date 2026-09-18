package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"novel2script-agent/backend/internal/agent"
	agentruntime "novel2script-agent/backend/internal/agent/runtime"
)

func (w *LLMWorker) PlanEpisodeCardsBatch(ctx context.Context, run agent.Run, request agentruntime.EpisodeCardsBatchRequest) (agentruntime.EpisodeCardsBatchResult, error) {
	if !w.Configured() {
		return agentruntime.EpisodeCardsBatchResult{}, fmt.Errorf("generation model is not configured")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return agentruntime.EpisodeCardsBatchResult{}, err
	}
	prompt := episodeCardsBatchPrompt(run.SourceMode, string(payload))
	trace := workerTraceContext(run, "episode_cards_batch", "episode_cards", request.BatchStart)
	trace["batch_start"] = fmt.Sprint(request.BatchStart)
	trace["batch_end"] = fmt.Sprint(request.BatchEnd)
	trace["task_id"] = fmt.Sprintf("task_episode_cards_batch_%02d_%02d", request.BatchStart, request.BatchEnd)
	response, err := w.completeWithTrace(ctx, prompt, trace)
	if err != nil {
		return agentruntime.EpisodeCardsBatchResult{}, err
	}
	return parseEpisodeCardsBatchResult(response)
}

func episodeCardsBatchPrompt(mode agent.SourceMode, requestJSON string) string {
	designReference := designReferenceForArtifact(mode, "episode_cards")
	return `You are generating exactly one sequential batch of Novel2Script episode cards.
Output strict JSON only. Do not output Markdown or explanation.
Return this exact shape:
{
  "episodes": [
    {"episode_id": 1}
  ],
  "continuity_delta": {},
  "batch_risks": []
}
The episodes array must contain every episode from batch_start through batch_end exactly once, in ascending order.
Do not output episodes outside that range. Do not rewrite completed_episodes.
For novel mode, preserve the confirmed episode_split boundaries and use only the corresponding split entries.
For non_novel mode, follow the confirmed series_blueprint and generate only the requested range.
Use completed_episodes only for continuity with the prior batch.
Use Chinese for user-facing content.

## Project prompt and rules
` + designReference + `

## Runtime batch input
` + requestJSON
}

func parseEpisodeCardsBatchResult(response string) (agentruntime.EpisodeCardsBatchResult, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(extractJSONObject(response)), &raw); err != nil {
		return agentruntime.EpisodeCardsBatchResult{}, fmt.Errorf("decode episode cards batch output: %w", err)
	}
	if payload, ok := raw["payload"].(map[string]any); ok {
		raw = payload
	}
	episodes := objectSlice(raw["episodes"])
	if len(episodes) == 0 {
		episodes = objectSlice(raw["episode_cards"])
	}
	continuity, _ := raw["continuity_delta"].(map[string]any)
	if continuity == nil {
		continuity = map[string]any{}
	}
	risks := anySlice(raw["batch_risks"])
	if len(risks) == 0 && strings.TrimSpace(fmt.Sprint(raw["global_risks"])) != "" {
		risks = anySlice(raw["global_risks"])
	}
	return agentruntime.EpisodeCardsBatchResult{Episodes: episodes, ContinuityDelta: continuity, Risks: risks}, nil
}
