package worker

import (
	"context"
	"strings"
	"testing"

	"novel2script-agent/backend/internal/agent"
	agentruntime "novel2script-agent/backend/internal/agent/runtime"
	"novel2script-agent/backend/internal/llm"
)

type einoTestClient struct{}

func (einoTestClient) Configured() bool { return true }
func (einoTestClient) Complete(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{Content: `{"artifact_type":"material_bank","status":"pending_approval","payload":{"input_type_tags":[],"user_supplied_facts":{},"conflict_materials":[],"emotional_drives":[],"payoff_candidates":[],"hook_candidates":[],"visual_scene_candidates":[],"discard_or_later":[],"inferred_candidates":[],"gaps_and_questions":[],"volume_fit_notes":{},"most_promising_direction":"","source_trace":{}}}`}, nil
}

func TestEinoWorkerExecutesEpisodeSplitStageGraph(t *testing.T) {
	client := &countingEinoClient{response: `{"episode_skeletons":[{"episode_id":1,"approx_end_unit_id":"U0001","core_event":"事件","character_turn":"推进","desired_hook":"冲突"}],"global_risks":[]}`}
	adapter, err := NewEinoWorker(NewLLMWorker(client, "test-model"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.PlanEpisodeSplitStage(context.Background(), agent.Run{SourceMode: agent.SourceModeNovel}, agentruntime.EpisodeSplitStageRequest{
		Stage: agentruntime.EpisodeSplitStageGlobal, TargetEpisodeCount: 1,
		SourceUnits:      []agentruntime.EpisodeSplitSourceUnit{{UnitID: "U0001", StartOffset: 0, EndOffset: 4, Text: "原文"}},
		GenerationConfig: map[string]any{"target_episode_count": 1},
	})
	if err != nil || len(result.GlobalPlan) == 0 || client.calls != 1 {
		t.Fatalf("episode split graph failed: result=%#v calls=%d err=%v", result, client.calls, err)
	}
}

func TestEinoWorkerRejectsInvalidEpisodeSplitStageBeforeModelCall(t *testing.T) {
	client := &countingEinoClient{response: `{}`}
	adapter, err := NewEinoWorker(NewLLMWorker(client, "test-model"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.PlanEpisodeSplitStage(context.Background(), agent.Run{SourceMode: agent.SourceModeNonNovel}, agentruntime.EpisodeSplitStageRequest{Stage: agentruntime.EpisodeSplitStageGlobal})
	if err == nil || client.calls != 0 {
		t.Fatalf("invalid split input reached model: calls=%d err=%v", client.calls, err)
	}
}

type countingEinoClient struct {
	response string
	calls    int
}

func (c *countingEinoClient) Configured() bool { return true }
func (c *countingEinoClient) Complete(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	c.calls++
	return llm.ChatResponse{Content: c.response}, nil
}

func TestEinoWorkerExecutesPlanningStepThroughGraph(t *testing.T) {
	delegate := NewLLMWorker(einoTestClient{}, "test-model")
	adapter, err := NewEinoWorker(delegate)
	if err != nil {
		t.Fatal(err)
	}
	planned, _, err := adapter.PlanStep(context.Background(), agent.Run{SourceMode: agent.SourceModeNonNovel}, agent.Artifact{Payload: map[string]any{"text": "素材"}}, nil, "material_bank")
	if err != nil {
		t.Fatal(err)
	}
	if planned.ArtifactType != "material_bank" {
		t.Fatalf("expected material_bank, got %s", planned.ArtifactType)
	}
}

func TestEinoWorkerExecutesScriptAndPatchGraphs(t *testing.T) {
	scriptClient := &countingEinoClient{response: `{"artifact_type":"script_unit","status":"pending_approval","payload":{"episode_id":1,"source_mode":"non_novel","title":"第1集","script_text":"场 1-1 客厅 日 内\n△主角进门。","scenes":[],"source_refs":[]}}`}
	scriptWorker, err := NewEinoWorker(NewLLMWorker(scriptClient, "test-model"))
	if err != nil {
		t.Fatal(err)
	}
	planned, err := scriptWorker.WriteScriptStep(context.Background(), agent.Run{SourceMode: agent.SourceModeNonNovel}, nil, "script_unit", "Generate only episode_id=1")
	if err != nil || planned.ArtifactType != "script_unit" {
		t.Fatalf("script graph failed: artifact=%#v err=%v", planned, err)
	}

	patchClient := &countingEinoClient{response: `{"artifact_type":"material_bank","status":"pending_approval","field_path":"most_promising_direction","scope":"field","patch_value":"新方向"}`}
	patchWorker, err := NewEinoWorker(NewLLMWorker(patchClient, "test-model"))
	if err != nil {
		t.Fatal(err)
	}
	instruction := "USER_REVISION_REQUEST:\n修改方向\nREVISION_CONTEXT_PACK_JSON:\n" +
		`{"revision_intent":"patch_artifact_field","target":{"artifact_type":"material_bank","field_path":"most_promising_direction","scope":"field"}}`
	planned, err = patchWorker.PatchArtifactStep(context.Background(), agent.Run{SourceMode: agent.SourceModeNonNovel}, agent.Artifact{Payload: map[string]any{"most_promising_direction": "旧方向"}}, nil, "material_bank", instruction)
	if err != nil || planned.Payload["most_promising_direction"] != "新方向" {
		t.Fatalf("patch graph failed: artifact=%#v err=%v", planned, err)
	}
	if scriptClient.calls != 1 || patchClient.calls != 1 {
		t.Fatalf("expected one model call per graph, script=%d patch=%d", scriptClient.calls, patchClient.calls)
	}
}

func TestEinoWorkerRejectsInvalidInputBeforeModelCall(t *testing.T) {
	client := &countingEinoClient{response: `{}`}
	adapter, err := NewEinoWorker(NewLLMWorker(client, "test-model"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = adapter.PlanStep(context.Background(), agent.Run{SourceMode: agent.SourceModeUnknown}, agent.Artifact{Payload: map[string]any{}}, nil, "material_bank")
	if err == nil || !strings.Contains(err.Error(), "unsupported source mode") {
		t.Fatalf("expected source mode validation error, got %v", err)
	}
	if client.calls != 0 {
		t.Fatalf("invalid graph input called model %d times", client.calls)
	}
}
