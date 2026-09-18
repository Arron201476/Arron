package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"novel2script-agent/backend/internal/agent"
	agentruntime "novel2script-agent/backend/internal/agent/runtime"
)

func (w *LLMWorker) PlanEpisodeSplitStage(ctx context.Context, run agent.Run, request agentruntime.EpisodeSplitStageRequest) (agentruntime.EpisodeSplitStageResult, error) {
	if !w.Configured() {
		return agentruntime.EpisodeSplitStageResult{}, fmt.Errorf("generation model is not configured")
	}
	prompt, err := episodeSplitStagePrompt(run, request)
	if err != nil {
		return agentruntime.EpisodeSplitStageResult{}, err
	}
	trace := workerTraceContext(run, "episode_split_"+string(request.Stage), "episode_split", request.BatchStart)
	trace["batch_start"] = fmt.Sprint(request.BatchStart)
	trace["batch_end"] = fmt.Sprint(request.BatchEnd)
	if request.Stage == agentruntime.EpisodeSplitStageGlobal {
		trace["task_id"] = "task_split_global_plan"
	} else {
		trace["task_id"] = fmt.Sprintf("task_split_batch_%02d_%02d", request.BatchStart, request.BatchEnd)
	}
	response, err := w.completeWithTrace(ctx, prompt, trace)
	if err != nil {
		return agentruntime.EpisodeSplitStageResult{}, err
	}
	return parseEpisodeSplitStageResult(request, response)
}

func episodeSplitStagePrompt(run agent.Run, request agentruntime.EpisodeSplitStageRequest) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	if request.Stage == agentruntime.EpisodeSplitStageGlobal {
		return readDesignText("design/小说-prompts/step2a_episode_split_global.md") + "\n\n## 运行输入\n" + string(payload), nil
	}
	if request.Stage == agentruntime.EpisodeSplitStageBatch {
		return readDesignText("design/小说-prompts/step2b_episode_split_batch.md") + "\n\n## 运行输入\n" + string(payload), nil
	}
	return "", fmt.Errorf("unsupported episode split stage %q", request.Stage)
}

func parseEpisodeSplitStageResult(request agentruntime.EpisodeSplitStageRequest, response string) (agentruntime.EpisodeSplitStageResult, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(extractJSONObject(response)), &raw); err != nil {
		return agentruntime.EpisodeSplitStageResult{}, fmt.Errorf("decode episode split %s output: %w", request.Stage, err)
	}
	if request.Stage == agentruntime.EpisodeSplitStageGlobal {
		if _, ok := raw["episode_skeletons"].([]any); !ok {
			return agentruntime.EpisodeSplitStageResult{}, fmt.Errorf("global split output requires episode_skeletons")
		}
		return agentruntime.EpisodeSplitStageResult{GlobalPlan: raw, Risks: anySlice(raw["global_risks"])}, nil
	}
	if request.Stage == agentruntime.EpisodeSplitStageBatch {
		episodes := objectSlice(raw["episodes"])
		if len(episodes) == 0 {
			return agentruntime.EpisodeSplitStageResult{}, fmt.Errorf("batch split output requires episodes")
		}
		return agentruntime.EpisodeSplitStageResult{Episodes: episodes, Risks: anySlice(raw["batch_risks"])}, nil
	}
	return agentruntime.EpisodeSplitStageResult{}, fmt.Errorf("unsupported episode split stage %q", request.Stage)
}

func objectSlice(value any) []map[string]any {
	items, _ := value.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

func anySlice(value any) []any {
	if items, ok := value.([]any); ok {
		return items
	}
	if strings.TrimSpace(fmt.Sprint(value)) == "" || value == nil {
		return []any{}
	}
	return []any{value}
}
