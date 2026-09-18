package runtime

import (
	"context"
	"fmt"
	"sort"

	"novel2script-agent/backend/internal/agent"
)

const episodeCardsBatchSize = 5

type episodeCardsProgress struct {
	LockedInputs      map[string]any
	CompletedEpisodes []map[string]any
	ContinuityDeltas  []map[string]any
	GlobalRisks       []any
	NextEpisodeID     int
	TargetCount       int
}

func (r *Runtime) generateEpisodeCardsAsync(ctx context.Context, runID string, note string, worker EpisodeCardsContentWorker) {
	r.mu.Lock()
	run, ok := r.runs[runID]
	config := authoritativeGenerationConfigLocked(run)
	upstream, locked, inputsOK := r.episodeCardsInputsLocked(runID, run.SourceMode)
	r.mu.Unlock()
	if !ok {
		return
	}
	if !inputsOK {
		r.failRun(runID, stepIDForArtifact("episode_cards"), "分集卡生成失败", fmt.Errorf("episode cards require confirmed upstream artifacts"))
		return
	}
	targetCount := intValueOrZero(config["target_episode_count"])
	if targetCount < 1 {
		r.failRun(runID, stepIDForArtifact("episode_cards"), "分集卡生成失败", fmt.Errorf("episode cards require target_episode_count"))
		return
	}
	locked["generation_config_version"] = generationConfigVersion(run)
	progress := r.loadEpisodeCardsProgress(runID)
	if !sameLockedInputs(progress.LockedInputs, locked) || progress.TargetCount != targetCount {
		progress = episodeCardsProgress{LockedInputs: locked, NextEpisodeID: 1, TargetCount: targetCount}
		r.saveEpisodeCardsProgress(runID, progress, "正在准备分集卡")
	}

	batchCount := (targetCount + episodeCardsBatchSize - 1) / episodeCardsBatchSize
	taskTotal := batchCount + 1
	stepID := stepIDForArtifact("episode_cards")
	r.startArtifactStep(runID, "episode_cards")
	for batchStart := progress.NextEpisodeID; batchStart <= targetCount; batchStart += episodeCardsBatchSize {
		batchEnd := batchStart + episodeCardsBatchSize - 1
		if batchEnd > targetCount {
			batchEnd = targetCount
		}
		cursor := (batchStart - 1) / episodeCardsBatchSize
		taskID := fmt.Sprintf("task_episode_cards_batch_%02d_%02d", batchStart, batchEnd)
		r.startTask(runID, stepID, "episode_cards", taskID, cursor, taskTotal, nil)
		request := EpisodeCardsBatchRequest{
			TargetEpisodeCount: targetCount,
			BatchStart:         batchStart,
			BatchEnd:           batchEnd,
			SourceMode:         run.SourceMode,
			GenerationConfig:   clonePayload(config),
			Upstream:           episodeCardsBatchUpstream(upstream, batchStart, batchEnd),
			CompletedEpisodes:  recentCompletedEpisodeCards(progress.CompletedEpisodes),
			UserInstruction:    note,
		}
		result, err := worker.PlanEpisodeCardsBatch(ctx, run, request)
		if err != nil {
			r.failRun(runID, stepID, fmt.Sprintf("第 %d-%d 集分集卡生成失败", batchStart, batchEnd), err)
			return
		}
		if err := validateEpisodeCardsBatch(result.Episodes, batchStart, batchEnd); err != nil {
			r.failRun(runID, stepID, fmt.Sprintf("第 %d-%d 集分集卡返回不完整", batchStart, batchEnd), err)
			return
		}
		progress.CompletedEpisodes = append(progress.CompletedEpisodes, cloneMapSlice(result.Episodes)...)
		progress.ContinuityDeltas = append(progress.ContinuityDeltas, clonePayload(result.ContinuityDelta))
		progress.GlobalRisks = append(progress.GlobalRisks, result.Risks...)
		progress.NextEpisodeID = batchEnd + 1
		r.saveEpisodeCardsProgress(runID, progress, fmt.Sprintf("已完成 %d/%d 集分集卡", batchEnd, targetCount))
		r.completeTask(runID, taskID)
	}

	validateTaskID := "task_episode_cards_validate_coverage"
	r.startTask(runID, stepID, "episode_cards", validateTaskID, taskTotal-1, taskTotal, nil)
	if err := validateEpisodeCardsBatch(progress.CompletedEpisodes, 1, targetCount); err != nil {
		r.failRun(runID, stepID, "分集卡完整性检查失败", err)
		return
	}
	payload := map[string]any{
		"episodes":         mapSliceToAny(progress.CompletedEpisodes),
		"continuity_delta": mergeEpisodeCardsContinuity(progress.ContinuityDeltas),
		"global_risks":     progress.GlobalRisks,
		"next_action":      "script_generate",
	}
	planned := PlannedArtifact{ArtifactType: "episode_cards", Status: agent.ArtifactPendingApproval, Payload: payload}
	existing := r.artifactSnapshot(runID)
	r.appendPlannedArtifact(runID, run, planned, derivedFromLatest(existing))
	r.completeTask(runID, validateTaskID)
	r.markEpisodeCardsProgressCompleted(runID, targetCount)
	r.requestArtifactApproval(runID, "episode_cards", nil)
}

func (r *Runtime) episodeCardsInputsLocked(runID string, mode agent.SourceMode) (map[string]agent.Artifact, map[string]any, bool) {
	required := []string{"story_bible", "episode_split"}
	if mode == agent.SourceModeNonNovel {
		required = []string{"material_bank", "story_seed", "series_blueprint"}
	}
	selected := map[string]agent.Artifact{}
	for _, artifact := range r.artifacts[runID] {
		if artifact.Status != agent.ArtifactConfirmed {
			continue
		}
		for _, artifactType := range required {
			if artifact.ArtifactType == artifactType && artifact.Version >= selected[artifactType].Version {
				selected[artifactType] = artifact
			}
		}
	}
	locked := map[string]any{}
	for _, artifactType := range required {
		artifact := selected[artifactType]
		if artifact.ArtifactID == "" {
			return nil, nil, false
		}
		locked[artifactType+"_artifact_id"] = artifact.ArtifactID
		locked[artifactType+"_version"] = artifact.Version
	}
	return selected, locked, true
}

func episodeCardsBatchUpstream(upstream map[string]agent.Artifact, batchStart int, batchEnd int) map[string]any {
	out := map[string]any{}
	for artifactType, artifact := range upstream {
		payload := clonePayload(artifact.Payload)
		if artifactType == "episode_split" {
			filtered := make([]any, 0, batchEnd-batchStart+1)
			for _, episode := range mapSliceValue(payload["episodes"]) {
				episodeID := episodeNumber(episode["episode_id"])
				if episodeID >= batchStart && episodeID <= batchEnd {
					filtered = append(filtered, episode)
				}
			}
			payload = map[string]any{"episodes": filtered, "split_strategy": payload["split_strategy"]}
		} else {
			payload = compactEpisodeCardsPayload(artifactType, payload)
		}
		out[artifactType] = payload
	}
	return out
}

func compactEpisodeCardsPayload(artifactType string, payload map[string]any) map[string]any {
	keys := map[string][]string{
		"story_bible":      {"story_overview", "characters", "relationships", "major_plotline", "climax_map", "must_keep_facts", "world_rules", "short_drama_assets"},
		"material_bank":    {"user_supplied_facts", "conflict_materials", "emotional_drives", "payoff_candidates", "hook_candidates", "visual_scene_candidates", "most_promising_direction", "risks"},
		"story_seed":       {"logline", "core_premise", "protagonist", "main_characters", "relationship_engine", "central_conflict", "main_plotline", "payoff_chain", "hook_engine", "world_rules"},
		"series_blueprint": {"resolved_episode_count", "series_promise", "phase_plan", "payoff_distribution", "hook_distribution", "pacing_density_plan", "character_progression", "relationship_progression", "continuity_rules"},
	}
	out := map[string]any{}
	for _, key := range keys[artifactType] {
		if value, exists := payload[key]; exists {
			out[key] = compactSplitPromptValue(value, 5000)
		}
	}
	return out
}

func validateEpisodeCardsBatch(episodes []map[string]any, batchStart int, batchEnd int) error {
	expected := batchEnd - batchStart + 1
	if len(episodes) != expected {
		return &episodeCardsIncompleteError{BatchStart: batchStart, BatchEnd: batchEnd, Actual: len(episodes), Expected: expected}
	}
	seen := map[int]bool{}
	for index, episode := range episodes {
		episodeID := episodeNumber(episode["episode_id"])
		if episodeID == 0 {
			episodeID = episodeNumber(episode["episode_no"])
		}
		expectedID := batchStart + index
		if episodeID != expectedID || seen[episodeID] {
			return &episodeCardsIncompleteError{BatchStart: batchStart, BatchEnd: batchEnd, Actual: len(seen), Expected: expected}
		}
		episode["episode_id"] = episodeID
		seen[episodeID] = true
	}
	return nil
}

func recentCompletedEpisodeCards(episodes []map[string]any) []map[string]any {
	if len(episodes) <= 2 {
		return cloneMapSlice(episodes)
	}
	return cloneMapSlice(episodes[len(episodes)-2:])
}

func cloneMapSlice(items []map[string]any) []map[string]any {
	out := make([]map[string]any, len(items))
	for index, item := range items {
		out[index] = clonePayload(item)
	}
	return out
}

func mergeEpisodeCardsContinuity(deltas []map[string]any) map[string]any {
	out := map[string]any{}
	for _, delta := range deltas {
		for key, value := range delta {
			items, isItems := value.([]any)
			if !isItems {
				out[key] = value
				continue
			}
			existing, _ := out[key].([]any)
			out[key] = append(existing, items...)
		}
	}
	return out
}

func sameLockedInputs(left map[string]any, right map[string]any) bool {
	if len(left) == 0 || len(left) != len(right) {
		return false
	}
	keys := make([]string, 0, len(right))
	for key := range right {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if fmt.Sprint(left[key]) != fmt.Sprint(right[key]) {
			return false
		}
	}
	return true
}

func (r *Runtime) loadEpisodeCardsProgress(runID string) episodeCardsProgress {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw := mapValue(r.runs[runID].Metadata["episode_cards_progress"])
	return episodeCardsProgress{
		LockedInputs:      mapValue(raw["locked_inputs"]),
		CompletedEpisodes: mapSliceValue(raw["completed_episodes"]),
		ContinuityDeltas:  mapSliceValue(raw["continuity_deltas"]),
		GlobalRisks:       sliceValue(raw["global_risks"]),
		NextEpisodeID:     episodeNumber(raw["next_episode_id"]),
		TargetCount:       episodeNumber(raw["target_count"]),
	}
}

func (r *Runtime) saveEpisodeCardsProgress(runID string, progress episodeCardsProgress, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok || run.Status != agent.RunRunning {
		return
	}
	run.Metadata = cloneMetadata(run.Metadata)
	run.Metadata["episode_cards_progress"] = map[string]any{
		"locked_inputs": progress.LockedInputs, "completed_episodes": progress.CompletedEpisodes,
		"continuity_deltas": progress.ContinuityDeltas, "global_risks": progress.GlobalRisks,
		"next_episode_id": progress.NextEpisodeID, "completed_count": len(progress.CompletedEpisodes),
		"target_count": progress.TargetCount, "status_message": message,
	}
	r.runs[runID] = run
	r.events[runID] = append(r.events[runID], r.event(runID, stepIDForArtifact("episode_cards"), agent.EventProgressUpdated, message, nil, nil, map[string]any{
		"artifact_type": "episode_cards", "completed_count": len(progress.CompletedEpisodes), "target_count": progress.TargetCount, "progress": true,
	}))
	r.saveLocked()
}

func (r *Runtime) markEpisodeCardsProgressCompleted(runID string, target int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return
	}
	run.Metadata = cloneMetadata(run.Metadata)
	progress := mapValue(run.Metadata["episode_cards_progress"])
	progress["status"] = "completed"
	progress["completed_count"] = target
	progress["target_count"] = target
	progress["status_message"] = "分集卡完成，等待确认"
	run.Metadata["episode_cards_progress"] = progress
	r.runs[runID] = run
	r.saveLocked()
}
