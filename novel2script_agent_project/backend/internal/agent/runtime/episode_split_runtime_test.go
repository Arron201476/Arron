package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"novel2script-agent/backend/internal/agent"
)

type batchedSplitTestWorker struct {
	mu                    sync.Mutex
	globalCalls           int
	batchCalls            map[int]int
	failBatchStartOnce    int
	failedConfiguredBatch bool
	episodeCardsInputs    []agent.Artifact
}

func (w *batchedSplitTestWorker) PlanEpisodeSplitStage(_ context.Context, _ agent.Run, request EpisodeSplitStageRequest) (EpisodeSplitStageResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if request.Stage == EpisodeSplitStageGlobal {
		w.globalCalls++
		skeletons := make([]any, 0, request.TargetEpisodeCount)
		for episodeID := 1; episodeID <= request.TargetEpisodeCount; episodeID++ {
			position := episodeID*len(request.SourceUnits)/request.TargetEpisodeCount - 1
			if position < 0 {
				position = 0
			}
			skeletons = append(skeletons, map[string]any{
				"episode_id": episodeID, "approx_end_unit_id": request.SourceUnits[position].UnitID,
				"core_event": fmt.Sprintf("第%d集事件", episodeID), "character_turn": "推进", "desired_hook": "冲突升级",
			})
		}
		return EpisodeSplitStageResult{GlobalPlan: map[string]any{"episode_skeletons": skeletons, "global_risks": []any{}}}, nil
	}
	if w.batchCalls == nil {
		w.batchCalls = map[int]int{}
	}
	w.batchCalls[request.BatchStart]++
	if request.BatchStart == w.failBatchStartOnce && !w.failedConfiguredBatch {
		w.failedConfiguredBatch = true
		return EpisodeSplitStageResult{}, fmt.Errorf("configured batch failure")
	}
	skeletons := mapSliceValue(request.GlobalPlan["episode_skeletons"])
	episodes := make([]map[string]any, 0, request.BatchEnd-request.BatchStart+1)
	previousOffset := 0
	if len(request.CompletedEpisodes) > 0 {
		previousOffset = episodeNumber(request.CompletedEpisodes[len(request.CompletedEpisodes)-1]["end_offset"])
	}
	for episodeID := request.BatchStart; episodeID <= request.BatchEnd; episodeID++ {
		approxID := fmt.Sprint(skeletons[episodeID-1]["approx_end_unit_id"])
		var selected EpisodeSplitCandidate
		for _, candidate := range request.Candidates {
			if candidate.EpisodeID != episodeID || candidate.Offset <= previousOffset {
				continue
			}
			if selected.CandidateID == "" || candidate.EndUnitID == approxID {
				selected = candidate
			}
			if candidate.EndUnitID == approxID {
				break
			}
		}
		if selected.CandidateID == "" {
			return EpisodeSplitStageResult{}, fmt.Errorf("test worker found no candidate for episode %d", episodeID)
		}
		episodes = append(episodes, map[string]any{
			"episode_id": episodeID, "end_candidate_id": selected.CandidateID,
			"source_summary": fmt.Sprintf("第%d集原文", episodeID), "core_event": fmt.Sprintf("第%d集事件", episodeID),
			"character_turn": "推进", "boundary_reason": "事件节点", "hook_strength": "medium", "hook_type": "conflict",
			"information_density": "medium", "pacing_risk": "none", "split_confidence": "high", "requires_user_attention": false,
		})
		previousOffset = selected.Offset
	}
	return EpisodeSplitStageResult{Episodes: episodes}, nil
}

func (w *batchedSplitTestWorker) PlanStep(_ context.Context, _ agent.Run, _ agent.Artifact, existing []agent.Artifact, artifactType string) (PlannedArtifact, *agent.ApprovalRequest, error) {
	if artifactType == "episode_cards" {
		w.mu.Lock()
		w.episodeCardsInputs = append([]agent.Artifact(nil), existing...)
		w.mu.Unlock()
		target := 1
		for _, artifact := range existing {
			if artifact.ArtifactType == "episode_split" {
				target = episodeNumber(artifact.Payload["actual_episode_count"])
			}
		}
		episodes := make([]any, 0, target)
		for episodeID := 1; episodeID <= target; episodeID++ {
			episodes = append(episodes, map[string]any{"episode_id": episodeID, "title": fmt.Sprintf("第%d集", episodeID)})
		}
		return PlannedArtifact{ArtifactType: artifactType, Status: agent.ArtifactPendingApproval, Payload: map[string]any{
			"episodes": episodes, "continuity_delta": map[string]any{}, "next_action": "script_generate",
		}}, nil, nil
	}
	return PlannedArtifact{}, nil, fmt.Errorf("unexpected plan artifact %s", artifactType)
}

func (w *batchedSplitTestWorker) WriteScriptStep(_ context.Context, run agent.Run, artifacts []agent.Artifact, artifactType string, note string) (PlannedArtifact, error) {
	if artifactType == "script_context" {
		return PlannedArtifact{ArtifactType: artifactType, Status: agent.ArtifactConfirmed, Payload: map[string]any{
			"source_mode": run.SourceMode, "must_follow_facts": []any{"遵守确认版拆集"}, "source_material": map[string]any{},
		}}, nil
	}
	if artifactType == "script_unit" {
		match := regexp.MustCompile(`episode_id=([0-9]+)`).FindStringSubmatch(note)
		episodeID := 1
		if len(match) == 2 {
			fmt.Sscanf(match[1], "%d", &episodeID)
		}
		return PlannedArtifact{ArtifactType: artifactType, Status: agent.ArtifactConfirmed, Payload: map[string]any{
			"episode_id": episodeID, "source_mode": run.SourceMode, "title": fmt.Sprintf("第%d集", episodeID),
			"script_text": strings.Repeat("△动作推进。\n主角：冲突继续。\n", 12), "scenes": []any{}, "source_refs": []any{},
		}}, nil
	}
	return PlannedArtifact{}, fmt.Errorf("unexpected script call %s with %d artifacts", artifactType, len(artifacts))
}

func newEpisodeSplitRuntimeFixture(t *testing.T, target int, worker *batchedSplitTestWorker) (*Runtime, string) {
	t.Helper()
	runtime := NewRuntimeWithStateAndName(worker, "", "eino")
	runID := "run_split_test"
	run := agent.Run{
		RunID: runID, ProjectID: "project_split_test", Intent: "generate_novel", SourceMode: agent.SourceModeNovel,
		Status: agent.RunRunning, CurrentStepID: "step_split_episodes", StartedAt: time.Now().UTC(),
		Metadata: map[string]any{"generation_config": map[string]any{
			"target_episode_count": target, "episode_duration_minutes": 1.0, "target_script_chars": 333, "boundary_detection_window_chars": 800,
		}, "generation_config_version": 1, "runtime": "eino"},
	}
	sourceText := strings.Builder{}
	for index := 1; index <= target*4; index++ {
		fmt.Fprintf(&sourceText, "第%d段发生了一个完整事件，人物作出反应并推动冲突。\n", index)
	}
	source := runtime.artifact(run, "source_input", agent.ArtifactConfirmed, nil, map[string]any{"text": sourceText.String()})
	bible := runtime.artifact(run, "story_bible", agent.ArtifactConfirmed, []string{source.ArtifactID}, map[string]any{
		"story_overview": map[string]any{"core_conflict": "持续升级"}, "characters": []any{}, "relationships": []any{}, "short_drama_assets": map[string]any{}, "source_structure": []any{}, "source_trace": map[string]any{},
	})
	runtime.runs[runID] = run
	runtime.artifacts[runID] = []agent.Artifact{source, bible}
	runtime.events[runID] = []agent.RunEvent{}
	return runtime, runID
}

func TestBatchedEpisodeSplitProducesThirtyContinuousEpisodes(t *testing.T) {
	worker := &batchedSplitTestWorker{batchCalls: map[int]int{}}
	runtime, runID := newEpisodeSplitRuntimeFixture(t, 30, worker)
	runtime.generateEpisodeSplitAsync(context.Background(), runID, "", worker)

	run, _ := runtime.GetRun(runID)
	if run.Status != agent.RunWaitingApproval || run.ApprovalRequestID == "" {
		t.Fatalf("split should wait for approval: %#v", run)
	}
	if worker.globalCalls != 1 || len(worker.batchCalls) != 6 {
		t.Fatalf("expected one global plan and six batches, global=%d batches=%#v", worker.globalCalls, worker.batchCalls)
	}
	progressEvents := 0
	for _, event := range runtime.Events(runID) {
		if event.Type == agent.EventProgressUpdated {
			progressEvents++
		}
	}
	if progressEvents != 8 {
		t.Fatalf("expected preparation, global and six batch progress events, got %d", progressEvents)
	}
	var split agent.Artifact
	for _, artifact := range runtime.Artifacts(runID) {
		if artifact.ArtifactType == "episode_split" {
			split = artifact
		}
	}
	if split.ArtifactID == "" || episodeNumber(split.Payload["actual_episode_count"]) != 30 {
		t.Fatalf("missing final 30 episode split: %#v", split)
	}
	episodes := mapSliceValue(split.Payload["episodes"])
	if len(episodes) != 30 {
		t.Fatalf("expected 30 episodes, got %d", len(episodes))
	}
	previousEnd := 0
	for index, episode := range episodes {
		if episodeNumber(episode["start_offset"]) != previousEnd || episodeNumber(episode["episode_id"]) != index+1 {
			t.Fatalf("episode %d is not continuous: %#v", index+1, episode)
		}
		previousEnd = episodeNumber(episode["end_offset"])
	}
	source := runtime.artifacts[runID][0]
	if previousEnd != len([]rune(strings.TrimSpace(fmt.Sprint(source.Payload["text"])))) {
		t.Fatalf("split ended at %d instead of source end", previousEnd)
	}
	coverage := mapValue(split.Payload["coverage_check"])
	if len(sliceValue(coverage["missing_source_ranges"])) != 0 || len(sliceValue(coverage["duplicated_source_ranges"])) != 0 {
		t.Fatalf("coverage contains gaps or duplicates: %#v", coverage)
	}
}

func TestBatchedEpisodeSplitRetryResumesFailedBatch(t *testing.T) {
	worker := &batchedSplitTestWorker{batchCalls: map[int]int{}, failBatchStartOnce: 6}
	runtime, runID := newEpisodeSplitRuntimeFixture(t, 15, worker)
	runtime.generateEpisodeSplitAsync(context.Background(), runID, "", worker)
	failed, _ := runtime.GetRun(runID)
	if failed.Status != agent.RunFailed {
		t.Fatalf("expected configured batch failure, got %s", failed.Status)
	}
	progress := mapValue(failed.Metadata["episode_split_progress"])
	if episodeNumber(progress["completed_count"]) != 5 || episodeNumber(progress["next_episode_id"]) != 6 {
		t.Fatalf("checkpoint did not retain first batch: %#v", progress)
	}
	if _, err := runtime.RerunStep(runID, "step_split_episodes", agent.RerunStepRequest{Reason: "继续"}); err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, runtime, runID, agent.RunWaitingApproval)
	if worker.globalCalls != 1 || worker.batchCalls[1] != 1 || worker.batchCalls[6] != 2 || worker.batchCalls[11] != 1 {
		t.Fatalf("retry repeated completed work: global=%d batches=%#v", worker.globalCalls, worker.batchCalls)
	}
}

func TestBatchedEpisodeSplitCheckpointSurvivesRuntimeReload(t *testing.T) {
	worker := &batchedSplitTestWorker{batchCalls: map[int]int{}, failBatchStartOnce: 6}
	runtime, runID := newEpisodeSplitRuntimeFixture(t, 15, worker)
	statePath := filepath.Join(t.TempDir(), "runtime-state.json")
	runtime.statePath = statePath
	runtime.generateEpisodeSplitAsync(context.Background(), runID, "", worker)
	if current, _ := runtime.GetRun(runID); current.Status != agent.RunFailed {
		t.Fatalf("expected failed checkpoint before reload, got %s", current.Status)
	}

	reloaded := NewRuntimeWithStateAndName(worker, statePath, "eino")
	progress := reloaded.loadEpisodeSplitProgress(runID)
	if len(progress.CompletedEpisodes) != 5 || progress.NextEpisodeID != 6 {
		t.Fatalf("checkpoint did not survive reload: %#v", progress)
	}
	if _, err := reloaded.RerunStep(runID, "step_split_episodes", agent.RerunStepRequest{Reason: "重试失败步骤"}); err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, reloaded, runID, agent.RunWaitingApproval)
	if worker.globalCalls != 1 || worker.batchCalls[1] != 1 || worker.batchCalls[6] != 2 {
		t.Fatalf("runtime reload repeated completed split work: global=%d batches=%#v", worker.globalCalls, worker.batchCalls)
	}
}

func TestEpisodeSplitCheckpointResetsWhenStoryBibleVersionChanges(t *testing.T) {
	worker := &batchedSplitTestWorker{batchCalls: map[int]int{}, failBatchStartOnce: 6}
	runtime, runID := newEpisodeSplitRuntimeFixture(t, 15, worker)
	runtime.generateEpisodeSplitAsync(context.Background(), runID, "", worker)
	runtime.mu.Lock()
	run := runtime.runs[runID]
	run.Status = agent.RunRunning
	run.EndedAt = nil
	runtime.runs[runID] = run
	for index := range runtime.artifacts[runID] {
		if runtime.artifacts[runID][index].ArtifactType == "story_bible" {
			runtime.artifacts[runID][index].Status = agent.ArtifactSuperseded
		}
	}
	newBible := runtime.artifact(run, "story_bible", agent.ArtifactConfirmed, nil, map[string]any{
		"story_overview": map[string]any{"core_conflict": "修改后的冲突"}, "characters": []any{}, "relationships": []any{}, "short_drama_assets": map[string]any{}, "source_structure": []any{}, "source_trace": map[string]any{},
	})
	newBible.Version = 2
	runtime.artifacts[runID] = append(runtime.artifacts[runID], newBible)
	runtime.mu.Unlock()

	runtime.generateEpisodeSplitAsync(context.Background(), runID, "", worker)
	if worker.globalCalls != 2 || worker.batchCalls[1] != 2 {
		t.Fatalf("changed story bible reused stale checkpoint: global=%d batches=%#v", worker.globalCalls, worker.batchCalls)
	}
}

func TestConfirmedBatchedSplitFeedsEpisodeCards(t *testing.T) {
	worker := &batchedSplitTestWorker{batchCalls: map[int]int{}}
	runtime, runID := newEpisodeSplitRuntimeFixture(t, 3, worker)
	runtime.generateEpisodeSplitAsync(context.Background(), runID, "", worker)
	if _, err := runtime.ContinueRun(runID, agent.ContinueRunRequest{Decision: "approve"}); err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, runtime, runID, agent.RunWaitingApproval)
	worker.mu.Lock()
	inputs := append([]agent.Artifact(nil), worker.episodeCardsInputs...)
	worker.mu.Unlock()
	foundConfirmedSplit := false
	for _, artifact := range inputs {
		if artifact.ArtifactType == "episode_split" && artifact.Status == agent.ArtifactConfirmed && len(mapSliceValue(artifact.Payload["episodes"])) == 3 {
			foundConfirmedSplit = true
		}
	}
	if !foundConfirmedSplit {
		t.Fatalf("episode cards did not receive the confirmed final split: %#v", inputs)
	}
}

func TestBatchedSplitContinuesThroughCardsAndScripts(t *testing.T) {
	worker := &batchedSplitTestWorker{batchCalls: map[int]int{}}
	runtime, runID := newEpisodeSplitRuntimeFixture(t, 3, worker)
	runtime.generateEpisodeSplitAsync(context.Background(), runID, "", worker)
	if _, err := runtime.ContinueRun(runID, agent.ContinueRunRequest{Decision: "approve"}); err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, runtime, runID, agent.RunWaitingApproval)
	current, _ := runtime.GetRun(runID)
	if current.CurrentStepID != "step_approval_episode_cards" {
		t.Fatalf("expected episode cards approval, got %#v", current)
	}
	if _, err := runtime.ContinueRun(runID, agent.ContinueRunRequest{Decision: "approve"}); err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, runtime, runID, agent.RunWaitingApproval)
	current, _ = runtime.GetRun(runID)
	if current.CurrentStepID != "step_approval_script_unit" {
		t.Fatalf("expected script approval, got %#v", current)
	}
	if _, err := runtime.ContinueRun(runID, agent.ContinueRunRequest{Decision: "approve"}); err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, runtime, runID, agent.RunCompleted)
	types := map[string]int{}
	for _, artifact := range runtime.Artifacts(runID) {
		if artifact.Status != agent.ArtifactSuperseded && artifact.Status != agent.ArtifactInvalidated {
			types[artifact.ArtifactType]++
		}
	}
	if types["episode_split"] != 1 || types["episode_cards"] != 1 || types["script_context"] != 1 || types["script_unit"] != 3 || types["scripts"] != 1 {
		t.Fatalf("downstream artifact chain is incomplete: %#v", types)
	}
}

func TestEpisodeSplitAdaptiveWindowIsBounded(t *testing.T) {
	units := indexEpisodeSplitSource(strings.Repeat("一个完整事件发生并结束。\n", 80))
	plan := map[string]any{"episode_skeletons": []any{
		map[string]any{"episode_id": 1, "approx_end_unit_id": units[39].UnitID},
		map[string]any{"episode_id": 2, "approx_end_unit_id": units[len(units)-1].UnitID},
	}}
	candidates, _, err := episodeSplitCandidates(units, plan, nil, 1, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.WindowChars < 300 || candidate.WindowChars > 1200 {
			t.Fatalf("adaptive window is out of bounds: %#v", candidate)
		}
	}
}

func TestPreserveExistingEpisodeMarksLocksOriginalBoundaries(t *testing.T) {
	worker := &batchedSplitTestWorker{batchCalls: map[int]int{}}
	runtime, runID := newEpisodeSplitRuntimeFixture(t, 3, worker)
	sourceText := "第1章 开始\n第一章发生完整事件并结束。\n第2章 继续\n第二章发生完整事件并结束。\n第3章 收尾\n第三章发生完整事件并结束。"
	runtime.mu.Lock()
	run := runtime.runs[runID]
	config := run.Metadata["generation_config"].(map[string]any)
	config["preserve_existing_episode_marks"] = true
	config["existing_episode_markers_detected"] = true
	config["detected_episode_count"] = 3
	run.Metadata["generation_config"] = config
	runtime.runs[runID] = run
	for index := range runtime.artifacts[runID] {
		if runtime.artifacts[runID][index].ArtifactType == "source_input" {
			runtime.artifacts[runID][index].Payload["text"] = sourceText
		}
	}
	runtime.mu.Unlock()

	runtime.generateEpisodeSplitAsync(context.Background(), runID, "", worker)
	if worker.globalCalls != 0 {
		t.Fatalf("preserve mode must not ask the model to redraw global boundaries, calls=%d", worker.globalCalls)
	}
	markers := agent.DetectEpisodeMarkers(sourceText)
	var split agent.Artifact
	for _, artifact := range runtime.Artifacts(runID) {
		if artifact.ArtifactType == "episode_split" {
			split = artifact
		}
	}
	episodes := mapSliceValue(split.Payload["episodes"])
	if len(episodes) != 3 {
		current, _ := runtime.GetRun(runID)
		t.Fatalf("expected three preserved episodes, got %#v; run=%#v events=%#v", episodes, current, runtime.Events(runID))
	}
	if episodeNumber(episodes[0]["end_offset"]) != markers[1].RuneOffset || episodeNumber(episodes[1]["end_offset"]) != markers[2].RuneOffset {
		t.Fatalf("boundaries do not match original markers: markers=%#v episodes=%#v", markers, episodes)
	}
}

func TestEpisodeSplitCandidatesDoNotOverlapBetweenEpisodes(t *testing.T) {
	units := indexEpisodeSplitSource(strings.Repeat("一个完整事件发生并结束。\n", 120))
	plan := map[string]any{"episode_skeletons": []any{
		map[string]any{"episode_id": 1, "approx_end_unit_id": units[29].UnitID},
		map[string]any{"episode_id": 2, "approx_end_unit_id": units[59].UnitID},
		map[string]any{"episode_id": 3, "approx_end_unit_id": units[89].UnitID},
		map[string]any{"episode_id": 4, "approx_end_unit_id": units[len(units)-1].UnitID},
	}}
	candidates, _, err := episodeSplitCandidates(units, plan, nil, 1, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	maximumByEpisode := map[int]int{}
	minimumByEpisode := map[int]int{}
	for _, candidate := range candidates {
		if minimumByEpisode[candidate.EpisodeID] == 0 || candidate.Offset < minimumByEpisode[candidate.EpisodeID] {
			minimumByEpisode[candidate.EpisodeID] = candidate.Offset
		}
		if candidate.Offset > maximumByEpisode[candidate.EpisodeID] {
			maximumByEpisode[candidate.EpisodeID] = candidate.Offset
		}
	}
	for episodeID := 1; episodeID < 4; episodeID++ {
		if maximumByEpisode[episodeID] >= minimumByEpisode[episodeID+1] {
			t.Fatalf("candidate ranges overlap between episodes %d and %d: %#v %#v", episodeID, episodeID+1, maximumByEpisode, minimumByEpisode)
		}
	}
}

func TestUnitsForGlobalPlanBoundsLongNovelInputAndPreservesFinalUnit(t *testing.T) {
	units := make([]EpisodeSplitSourceUnit, 1000)
	for index := range units {
		units[index] = EpisodeSplitSourceUnit{
			UnitID: fmt.Sprintf("U%04d", index+1), StartOffset: index * 200, EndOffset: (index + 1) * 200,
			Text: strings.Repeat("长篇小说结构单元", 40),
		}
	}
	globalUnits := unitsForGlobalPlan(units, 30)
	if len(globalUnits) != 300 {
		t.Fatalf("expected bounded global index of 300 units, got %d", len(globalUnits))
	}
	if globalUnits[len(globalUnits)-1].UnitID != units[len(units)-1].UnitID || globalUnits[len(globalUnits)-1].EndOffset != units[len(units)-1].EndOffset {
		t.Fatalf("final source boundary was not preserved: %#v", globalUnits[len(globalUnits)-1])
	}
	for _, unit := range globalUnits {
		if len([]rune(unit.Text)) > 185 {
			t.Fatalf("global unit text was not compacted: %d runes", len([]rune(unit.Text)))
		}
	}
}

func TestCompactStoryBibleForSplitDropsSourceStructureAndBoundsLargeFields(t *testing.T) {
	compact := compactStoryBibleForSplit(map[string]any{
		"story_overview":   strings.Repeat("主线", 2000),
		"source_structure": strings.Repeat("逐章结构", 5000),
		"must_keep_facts":  []any{"事实一", "事实二"},
	})
	if _, ok := compact["source_structure"]; ok {
		t.Fatal("source_structure should not duplicate the deterministic source index")
	}
	if len([]rune(fmt.Sprint(compact["story_overview"]))) > 2401 {
		t.Fatalf("large story bible field was not bounded: %d", len([]rune(fmt.Sprint(compact["story_overview"]))))
	}
	if _, ok := compact["must_keep_facts"].([]any); !ok {
		t.Fatalf("small structured fields should remain structured: %#v", compact["must_keep_facts"])
	}
}

func TestResolveEpisodeSplitBatchRejectsUnknownCandidate(t *testing.T) {
	units := indexEpisodeSplitSource("第一段完整事件。\n第二段完整事件。")
	_, err := resolveEpisodeSplitBatch([]map[string]any{{"episode_id": 1, "end_candidate_id": "invented"}}, []EpisodeSplitCandidate{{CandidateID: "E001-B01", EpisodeID: 1, Offset: units[0].EndOffset}}, units, nil, 1, 1)
	if err == nil || !strings.Contains(err.Error(), "invalid candidate") {
		t.Fatalf("expected unknown candidate rejection, got %v", err)
	}
}
