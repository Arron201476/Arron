package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"novel2script-agent/backend/internal/agent"
)

type batchedCardsTestWorker struct {
	mu              sync.Mutex
	batchCalls      map[int]int
	failStartOnce   int
	failedOnce      bool
	incompleteStart int
	requests        []EpisodeCardsBatchRequest
}

func (w *batchedCardsTestWorker) PlanEpisodeCardsBatch(_ context.Context, _ agent.Run, request EpisodeCardsBatchRequest) (EpisodeCardsBatchResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.batchCalls == nil {
		w.batchCalls = map[int]int{}
	}
	w.batchCalls[request.BatchStart]++
	w.requests = append(w.requests, request)
	if request.BatchStart == w.failStartOnce && !w.failedOnce {
		w.failedOnce = true
		return EpisodeCardsBatchResult{}, fmt.Errorf("configured cards batch failure")
	}
	end := request.BatchEnd
	if request.BatchStart == w.incompleteStart {
		end--
	}
	episodes := make([]map[string]any, 0, end-request.BatchStart+1)
	for episodeID := request.BatchStart; episodeID <= end; episodeID++ {
		episodes = append(episodes, map[string]any{
			"episode_id": episodeID, "title": fmt.Sprintf("第%d集", episodeID),
			"main_conflict": "冲突推进", "ending_hook": "下一集钩子",
		})
	}
	return EpisodeCardsBatchResult{
		Episodes:        episodes,
		ContinuityDelta: map[string]any{"new_facts": []any{fmt.Sprintf("完成%d-%d", request.BatchStart, request.BatchEnd)}},
		Risks:           []any{},
	}, nil
}

func (w *batchedCardsTestWorker) PlanStep(context.Context, agent.Run, agent.Artifact, []agent.Artifact, string) (PlannedArtifact, *agent.ApprovalRequest, error) {
	return PlannedArtifact{}, nil, fmt.Errorf("unexpected generic plan call")
}

func (w *batchedCardsTestWorker) WriteScriptStep(context.Context, agent.Run, []agent.Artifact, string, string) (PlannedArtifact, error) {
	return PlannedArtifact{}, fmt.Errorf("unexpected script call")
}

func newEpisodeCardsRuntimeFixture(t *testing.T, mode agent.SourceMode, target int, worker *batchedCardsTestWorker) (*Runtime, string) {
	t.Helper()
	runtime := NewRuntimeWithStateAndName(worker, "", "eino")
	runID := "run_cards_test"
	run := agent.Run{
		RunID: runID, ProjectID: "project_cards_test", Intent: "generate", SourceMode: mode,
		Status: agent.RunRunning, CurrentStepID: "step_plan_episode_cards", StartedAt: time.Now().UTC(),
		Metadata: map[string]any{
			"generation_config":         map[string]any{"target_episode_count": target, "episode_duration_minutes": 1.5, "target_script_chars": 500},
			"generation_config_version": 1, "runtime": "eino",
		},
	}
	artifacts := []agent.Artifact{}
	if mode == agent.SourceModeNovel {
		bible := runtime.artifact(run, "story_bible", agent.ArtifactConfirmed, nil, map[string]any{"story_overview": map[string]any{"core_conflict": "主冲突"}})
		episodes := make([]any, target)
		for index := range episodes {
			episodes[index] = map[string]any{"episode_id": index + 1, "source_summary": fmt.Sprintf("第%d集原文", index+1)}
		}
		split := runtime.artifact(run, "episode_split", agent.ArtifactConfirmed, []string{bible.ArtifactID}, map[string]any{"episodes": episodes})
		artifacts = []agent.Artifact{bible, split}
	} else {
		bank := runtime.artifact(run, "material_bank", agent.ArtifactConfirmed, nil, map[string]any{"most_promising_direction": "主方向"})
		seed := runtime.artifact(run, "story_seed", agent.ArtifactConfirmed, []string{bank.ArtifactID}, map[string]any{"logline": "一句话故事"})
		blueprint := runtime.artifact(run, "series_blueprint", agent.ArtifactConfirmed, []string{seed.ArtifactID}, map[string]any{"resolved_episode_count": target, "phase_plan": []any{}})
		artifacts = []agent.Artifact{bank, seed, blueprint}
	}
	runtime.runs[runID] = run
	runtime.artifacts[runID] = artifacts
	runtime.events[runID] = []agent.RunEvent{}
	return runtime, runID
}

func TestEpisodeCardsBatchesFourteenEpisodesAsFiveFiveFour(t *testing.T) {
	worker := &batchedCardsTestWorker{batchCalls: map[int]int{}}
	runtime, runID := newEpisodeCardsRuntimeFixture(t, agent.SourceModeNovel, 14, worker)
	runtime.generateEpisodeCardsAsync(context.Background(), runID, "", worker)
	run, _ := runtime.GetRun(runID)
	if run.Status != agent.RunWaitingApproval {
		t.Fatalf("expected episode cards approval, got %#v", run)
	}
	if worker.batchCalls[1] != 1 || worker.batchCalls[6] != 1 || worker.batchCalls[11] != 1 || len(worker.batchCalls) != 3 {
		t.Fatalf("unexpected batch calls: %#v", worker.batchCalls)
	}
	var cards agent.Artifact
	for _, artifact := range runtime.Artifacts(runID) {
		if artifact.ArtifactType == "episode_cards" {
			cards = artifact
		}
	}
	if episodes := mapSliceValue(cards.Payload["episodes"]); len(episodes) != 14 || episodeNumber(episodes[13]["episode_id"]) != 14 {
		t.Fatalf("final cards are incomplete: %#v", cards.Payload)
	}
	continuity := mapValue(cards.Payload["continuity_delta"])
	if len(sliceValue(continuity["new_facts"])) != 3 {
		t.Fatalf("continuity deltas were not merged: %#v", continuity)
	}
}

func TestEpisodeCardsRetryResumesFailedBatch(t *testing.T) {
	worker := &batchedCardsTestWorker{batchCalls: map[int]int{}, failStartOnce: 6}
	runtime, runID := newEpisodeCardsRuntimeFixture(t, agent.SourceModeNovel, 14, worker)
	runtime.generateEpisodeCardsAsync(context.Background(), runID, "", worker)
	failed, _ := runtime.GetRun(runID)
	progress := mapValue(failed.Metadata["episode_cards_progress"])
	if failed.Status != agent.RunFailed || episodeNumber(progress["completed_count"]) != 5 || episodeNumber(progress["next_episode_id"]) != 6 {
		t.Fatalf("first batch checkpoint was not retained: run=%#v progress=%#v", failed, progress)
	}
	if _, err := runtime.RerunStep(runID, "step_plan_episode_cards", agent.RerunStepRequest{Reason: "继续"}); err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, runtime, runID, agent.RunWaitingApproval)
	if worker.batchCalls[1] != 1 || worker.batchCalls[6] != 2 || worker.batchCalls[11] != 1 {
		t.Fatalf("retry repeated completed cards: %#v", worker.batchCalls)
	}
}

func TestEpisodeCardsIncompleteBatchIsNotSaved(t *testing.T) {
	worker := &batchedCardsTestWorker{batchCalls: map[int]int{}, incompleteStart: 1}
	runtime, runID := newEpisodeCardsRuntimeFixture(t, agent.SourceModeNovel, 6, worker)
	runtime.generateEpisodeCardsAsync(context.Background(), runID, "", worker)
	run, _ := runtime.GetRun(runID)
	if run.Status != agent.RunFailed {
		t.Fatalf("expected incomplete cards failure, got %#v", run)
	}
	for _, artifact := range runtime.Artifacts(runID) {
		if artifact.ArtifactType == "episode_cards" {
			t.Fatalf("partial episode cards artifact must not be saved: %#v", artifact)
		}
	}
	events := runtime.Events(runID)
	last := events[len(events)-1]
	if last.Payload["error_code"] != "EPISODE_CARDS_INCOMPLETE" || fmt.Sprint(last.Payload["user_message"]) == "" {
		t.Fatalf("missing user-facing incomplete output error: %#v", last.Payload)
	}
}

func TestEpisodeCardsBatchingSupportsNonNovelFlow(t *testing.T) {
	worker := &batchedCardsTestWorker{batchCalls: map[int]int{}}
	runtime, runID := newEpisodeCardsRuntimeFixture(t, agent.SourceModeNonNovel, 6, worker)
	runtime.generateEpisodeCardsAsync(context.Background(), runID, "", worker)
	run, _ := runtime.GetRun(runID)
	if run.Status != agent.RunWaitingApproval || worker.batchCalls[1] != 1 || worker.batchCalls[6] != 1 {
		t.Fatalf("non-novel cards did not use batching: run=%#v calls=%#v", run, worker.batchCalls)
	}
	worker.mu.Lock()
	request := worker.requests[0]
	worker.mu.Unlock()
	if _, ok := request.Upstream["series_blueprint"]; !ok {
		t.Fatalf("non-novel batch did not receive series blueprint: %#v", request.Upstream)
	}
}

func TestEpisodeCardsCheckpointSurvivesRuntimeReload(t *testing.T) {
	worker := &batchedCardsTestWorker{batchCalls: map[int]int{}, failStartOnce: 6}
	runtime, runID := newEpisodeCardsRuntimeFixture(t, agent.SourceModeNovel, 14, worker)
	runtime.statePath = filepath.Join(t.TempDir(), "runtime-state.json")
	runtime.generateEpisodeCardsAsync(context.Background(), runID, "", worker)
	if current, _ := runtime.GetRun(runID); current.Status != agent.RunFailed {
		t.Fatalf("expected failed checkpoint before reload, got %#v", current)
	}

	reloaded := NewRuntimeWithStateAndName(worker, runtime.statePath, "eino")
	progress := reloaded.loadEpisodeCardsProgress(runID)
	if len(progress.CompletedEpisodes) != 5 || progress.NextEpisodeID != 6 {
		t.Fatalf("cards checkpoint did not survive reload: %#v", progress)
	}
	if _, err := reloaded.RerunStep(runID, "step_plan_episode_cards", agent.RerunStepRequest{Reason: "重试失败批次"}); err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, reloaded, runID, agent.RunWaitingApproval)
	if worker.batchCalls[1] != 1 || worker.batchCalls[6] != 2 || worker.batchCalls[11] != 1 {
		t.Fatalf("reload repeated completed cards: %#v", worker.batchCalls)
	}
}
