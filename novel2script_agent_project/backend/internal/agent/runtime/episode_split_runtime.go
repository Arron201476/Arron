package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"novel2script-agent/backend/internal/agent"
)

const episodeSplitBatchSize = 5

type episodeSplitProgress struct {
	LockedInputs      map[string]any
	GlobalPlan        map[string]any
	CompletedEpisodes []map[string]any
	GlobalRisks       []any
	NextEpisodeID     int
	TargetCount       int
}

func (r *Runtime) generateEpisodeSplitAsync(ctx context.Context, runID string, note string, worker EpisodeSplitContentWorker) {
	r.mu.Lock()
	run, ok := r.runs[runID]
	if !ok {
		r.mu.Unlock()
		return
	}
	source, storyBible, inputsOK := r.episodeSplitInputsLocked(runID)
	config := authoritativeGenerationConfigLocked(run)
	r.mu.Unlock()
	if !inputsOK {
		r.failRun(runID, stepIDForArtifact("episode_split"), "Planning failed", fmt.Errorf("episode split requires confirmed source input and story bible"))
		return
	}
	targetCount := intValueOrZero(config["target_episode_count"])
	if targetCount < 1 {
		r.failRun(runID, stepIDForArtifact("episode_split"), "Planning failed", fmt.Errorf("episode split requires target_episode_count"))
		return
	}
	text := strings.TrimSpace(fmt.Sprint(source.Payload["text"]))
	units := indexEpisodeSplitSource(text)
	if len(units) == 0 {
		r.failRun(runID, stepIDForArtifact("episode_split"), "Planning failed", fmt.Errorf("episode split source text is empty"))
		return
	}

	locked := episodeSplitLockedInputs(source, storyBible, run)
	progress := r.loadEpisodeSplitProgress(runID)
	if !sameEpisodeSplitLocks(progress.LockedInputs, locked) || progress.TargetCount != targetCount {
		progress = episodeSplitProgress{LockedInputs: locked, NextEpisodeID: 1, TargetCount: targetCount}
		r.saveEpisodeSplitProgress(runID, progress, "正在准备原文拆集")
	}

	batchCount := (targetCount + episodeSplitBatchSize - 1) / episodeSplitBatchSize
	taskTotal := batchCount + 2
	stepID := stepIDForArtifact("episode_split")
	r.startArtifactStep(runID, "episode_split")

	if len(progress.GlobalPlan) == 0 {
		taskID := "task_split_global_plan"
		r.startTask(runID, stepID, "episode_split", taskID, 0, taskTotal, nil)
		var result EpisodeSplitStageResult
		var err error
		if boolValue(config["preserve_existing_episode_marks"]) {
			result.GlobalPlan, err = preservedEpisodeSplitGlobalPlan(text, units, targetCount)
		} else {
			request := EpisodeSplitStageRequest{
				Stage: EpisodeSplitStageGlobal, TargetEpisodeCount: targetCount,
				SourceUnits: unitsForGlobalPlan(units, targetCount), StoryBible: compactStoryBibleForSplit(storyBible.Payload), GenerationConfig: clonePayload(config),
			}
			result, err = worker.PlanEpisodeSplitStage(ctx, run, request)
		}
		if err != nil {
			r.failRun(runID, stepID, "原文拆集整体规划失败", err)
			return
		}
		if err := validateEpisodeSplitGlobalPlan(result.GlobalPlan, units, targetCount); err != nil {
			r.failRun(runID, stepID, "原文拆集整体规划未通过校验", err)
			return
		}
		progress.GlobalPlan = result.GlobalPlan
		progress.GlobalRisks = append(progress.GlobalRisks, result.Risks...)
		progress.NextEpisodeID = 1
		r.saveEpisodeSplitProgress(runID, progress, "整体拆分结构已规划")
		r.completeTask(runID, taskID)
	}

	for batchStart := progress.NextEpisodeID; batchStart <= targetCount; batchStart += episodeSplitBatchSize {
		batchEnd := batchStart + episodeSplitBatchSize - 1
		if batchEnd > targetCount {
			batchEnd = targetCount
		}
		cursor := 1 + (batchStart-1)/episodeSplitBatchSize
		taskID := fmt.Sprintf("task_split_batch_%02d_%02d", batchStart, batchEnd)
		r.startTask(runID, stepID, "episode_split", taskID, cursor, taskTotal, nil)
		var candidates []EpisodeSplitCandidate
		var relevantUnits []EpisodeSplitSourceUnit
		var err error
		if boolValue(config["preserve_existing_episode_marks"]) {
			candidates, relevantUnits, err = preservedEpisodeSplitCandidates(units, progress.GlobalPlan, batchStart, batchEnd, targetCount)
		} else {
			candidates, relevantUnits, err = episodeSplitCandidates(units, progress.GlobalPlan, progress.CompletedEpisodes, batchStart, batchEnd, targetCount)
		}
		if err != nil {
			r.failRun(runID, stepID, "原文拆集候选边界生成失败", err)
			return
		}
		request := EpisodeSplitStageRequest{
			Stage: EpisodeSplitStageBatch, TargetEpisodeCount: targetCount, BatchStart: batchStart, BatchEnd: batchEnd,
			SourceUnits: relevantUnits, StoryBible: compactStoryBibleForSplit(storyBible.Payload), GenerationConfig: clonePayload(config),
			GlobalPlan: progress.GlobalPlan, CompletedEpisodes: recentCompletedSplitEpisodes(progress.CompletedEpisodes), Candidates: candidates,
		}
		result, err := worker.PlanEpisodeSplitStage(ctx, run, request)
		if err != nil {
			r.failRun(runID, stepID, fmt.Sprintf("第 %d-%d 集边界生成失败", batchStart, batchEnd), err)
			return
		}
		resolved, err := resolveEpisodeSplitBatch(result.Episodes, candidates, units, progress.CompletedEpisodes, batchStart, batchEnd)
		if err != nil {
			r.failRun(runID, stepID, fmt.Sprintf("第 %d-%d 集边界未通过校验", batchStart, batchEnd), err)
			return
		}
		progress.CompletedEpisodes = append(progress.CompletedEpisodes, resolved...)
		progress.GlobalRisks = append(progress.GlobalRisks, result.Risks...)
		progress.NextEpisodeID = batchEnd + 1
		r.saveEpisodeSplitProgress(runID, progress, fmt.Sprintf("已完成 %d/%d 集原文边界", batchEnd, targetCount))
		r.completeTask(runID, taskID)
	}

	validateTaskID := "task_split_validate_coverage"
	r.startTask(runID, stepID, "episode_split", validateTaskID, taskTotal-1, taskTotal, nil)
	payload, err := buildEpisodeSplitArtifactPayload(progress, units, config)
	if err != nil {
		r.failRun(runID, stepID, "原文覆盖检查失败", err)
		return
	}
	planned := PlannedArtifact{ArtifactType: "episode_split", Status: agent.ArtifactPendingApproval, Payload: payload}
	existing := r.artifactSnapshot(runID)
	applyDeterministicSourceMetrics("episode_split", planned.Payload, existing)
	r.appendPlannedArtifact(runID, run, planned, derivedFromLatest(existing))
	r.completeTask(runID, validateTaskID)
	r.markEpisodeSplitProgressCompleted(runID, targetCount)
	r.requestArtifactApproval(runID, "episode_split", nil)
}

func (r *Runtime) episodeSplitInputsLocked(runID string) (agent.Artifact, agent.Artifact, bool) {
	var source agent.Artifact
	var bible agent.Artifact
	for _, artifact := range r.artifacts[runID] {
		if artifact.Status == agent.ArtifactSuperseded || artifact.Status == agent.ArtifactInvalidated {
			continue
		}
		if artifact.ArtifactType == "source_input" && artifact.Version >= source.Version {
			source = artifact
		}
		if artifact.ArtifactType == "story_bible" && artifact.Status == agent.ArtifactConfirmed && artifact.Version >= bible.Version {
			bible = artifact
		}
	}
	return source, bible, source.ArtifactID != "" && bible.ArtifactID != ""
}

func episodeSplitLockedInputs(source agent.Artifact, bible agent.Artifact, run agent.Run) map[string]any {
	return map[string]any{
		"source_input_artifact_id": source.ArtifactID, "source_input_version": source.Version,
		"story_bible_artifact_id": bible.ArtifactID, "story_bible_version": bible.Version,
		"generation_config_version": generationConfigVersion(run),
	}
}

func sameEpisodeSplitLocks(left map[string]any, right map[string]any) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	for _, key := range []string{"source_input_artifact_id", "source_input_version", "story_bible_artifact_id", "story_bible_version", "generation_config_version"} {
		if fmt.Sprint(left[key]) != fmt.Sprint(right[key]) {
			return false
		}
	}
	return true
}

func (r *Runtime) loadEpisodeSplitProgress(runID string) episodeSplitProgress {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.runs[runID]
	raw, _ := run.Metadata["episode_split_progress"].(map[string]any)
	if raw == nil {
		if generic, ok := run.Metadata["episode_split_progress"].(map[string]interface{}); ok {
			raw = generic
		}
	}
	return episodeSplitProgress{
		LockedInputs: mapValue(raw["locked_inputs"]), GlobalPlan: mapValue(raw["global_plan"]),
		CompletedEpisodes: mapSliceValue(raw["completed_episodes"]), GlobalRisks: sliceValue(raw["global_risks"]),
		NextEpisodeID: episodeNumber(raw["next_episode_id"]), TargetCount: episodeNumber(raw["target_count"]),
	}
}

func (r *Runtime) saveEpisodeSplitProgress(runID string, progress episodeSplitProgress, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok || run.Status != agent.RunRunning {
		return
	}
	run.Metadata = cloneMetadata(run.Metadata)
	run.Metadata["episode_split_progress"] = map[string]any{
		"locked_inputs": progress.LockedInputs, "global_plan": progress.GlobalPlan,
		"completed_episodes": progress.CompletedEpisodes, "global_risks": progress.GlobalRisks,
		"next_episode_id": progress.NextEpisodeID, "completed_count": len(progress.CompletedEpisodes), "target_count": progress.TargetCount,
		"status_message": message,
	}
	r.runs[runID] = run
	r.events[runID] = append(r.events[runID], r.event(runID, stepIDForArtifact("episode_split"), agent.EventProgressUpdated, message, nil, nil, map[string]any{
		"artifact_type": "episode_split", "completed_count": len(progress.CompletedEpisodes), "target_count": progress.TargetCount, "progress": true,
	}))
	r.saveLocked()
}

func (r *Runtime) markEpisodeSplitProgressCompleted(runID string, target int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return
	}
	run.Metadata = cloneMetadata(run.Metadata)
	progress := mapValue(run.Metadata["episode_split_progress"])
	progress["status"] = "completed"
	progress["completed_count"] = target
	progress["target_count"] = target
	progress["status_message"] = "原文拆集完成，等待确认"
	run.Metadata["episode_split_progress"] = progress
	r.runs[runID] = run
	r.saveLocked()
}

func indexEpisodeSplitSource(text string) []EpisodeSplitSourceUnit {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	units := []EpisodeSplitSourceUnit{}
	start := 0
	flush := func(end int) {
		if end <= start {
			return
		}
		segment := string(runes[start:end])
		if strings.TrimSpace(segment) != "" {
			units = append(units, EpisodeSplitSourceUnit{UnitID: fmt.Sprintf("U%04d", len(units)+1), StartOffset: start, EndOffset: end, Text: segment})
		}
		start = end
	}
	for index, current := range runes {
		end := index + 1
		boundary := current == '\n' || current == '。' || current == '！' || current == '？' || current == '；' || current == '!' || current == '?'
		if boundary && end-start >= 8 {
			for end < len(runes) && (unicode.IsSpace(runes[end]) || strings.ContainsRune("”’」』", runes[end])) {
				end++
			}
			flush(end)
		}
	}
	flush(len(runes))
	if len(units) == 0 {
		return []EpisodeSplitSourceUnit{{UnitID: "U0001", StartOffset: 0, EndOffset: len(runes), Text: text}}
	}
	return units
}

func unitsForGlobalPlan(units []EpisodeSplitSourceUnit, targetCount int) []EpisodeSplitSourceUnit {
	limit := 300
	if targetCount > limit {
		limit = targetCount
	}
	if len(units) < limit {
		limit = len(units)
	}
	out := make([]EpisodeSplitSourceUnit, 0, limit)
	for bucket := 0; bucket < limit; bucket++ {
		start := bucket * len(units) / limit
		end := (bucket+1)*len(units)/limit - 1
		first := units[start]
		last := units[end]
		text := compactSplitUnitText(first.Text, 90)
		if end > start {
			text += " … " + compactSplitUnitText(last.Text, 90)
		}
		out = append(out, EpisodeSplitSourceUnit{
			UnitID: last.UnitID, StartOffset: first.StartOffset, EndOffset: last.EndOffset, Text: text,
		})
	}
	return out
}

func compactStoryBibleForSplit(payload map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"story_overview", "characters", "relationships", "major_plotline", "climax_map", "must_keep_facts", "short_drama_assets", "world_rules"} {
		if value, ok := payload[key]; ok {
			out[key] = compactSplitPromptValue(value, 2400)
		}
	}
	return out
}

func compactSplitUnitText(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

func compactSplitPromptValue(value any, limit int) any {
	data, err := json.Marshal(value)
	if err == nil && len([]rune(string(data))) <= limit {
		return value
	}
	return compactSplitUnitText(fmt.Sprint(value), limit)
}

func validateEpisodeSplitGlobalPlan(plan map[string]any, units []EpisodeSplitSourceUnit, target int) error {
	skeletons := mapSliceValue(plan["episode_skeletons"])
	if len(skeletons) != target {
		return fmt.Errorf("global plan episodes %d does not match target %d", len(skeletons), target)
	}
	unitIndex := map[string]int{}
	for index, unit := range units {
		unitIndex[unit.UnitID] = index
	}
	previous := -1
	for index, skeleton := range skeletons {
		episodeID := episodeNumber(skeleton["episode_id"])
		if episodeID != index+1 {
			return fmt.Errorf("global plan episode_id %d at index %d", episodeID, index)
		}
		unitID := strings.TrimSpace(fmt.Sprint(skeleton["approx_end_unit_id"]))
		position, ok := unitIndex[unitID]
		if !ok || position <= previous {
			return fmt.Errorf("global plan has invalid or non-increasing end unit %q", unitID)
		}
		previous = position
	}
	lastID := strings.TrimSpace(fmt.Sprint(skeletons[len(skeletons)-1]["approx_end_unit_id"]))
	if lastID != units[len(units)-1].UnitID {
		return fmt.Errorf("global plan final unit %q must be %q", lastID, units[len(units)-1].UnitID)
	}
	return nil
}

func preservedEpisodeSplitGlobalPlan(text string, units []EpisodeSplitSourceUnit, target int) (map[string]any, error) {
	markers := agent.DetectEpisodeMarkers(text)
	if len(markers) != target {
		return nil, fmt.Errorf("detected source episode markers %d do not match target %d", len(markers), target)
	}
	skeletons := make([]any, 0, target)
	for episodeID := 1; episodeID <= target; episodeID++ {
		endPosition := len(units) - 1
		if episodeID < target {
			nextStart := markers[episodeID].RuneOffset
			endPosition = -1
			for position, unit := range units {
				if unit.EndOffset <= nextStart {
					endPosition = position
					continue
				}
				break
			}
			if endPosition < 0 {
				return nil, fmt.Errorf("episode marker %d has no preceding source unit", episodeID+1)
			}
		}
		skeletons = append(skeletons, map[string]any{
			"episode_id": episodeID, "approx_end_unit_id": units[endPosition].UnitID,
			"source_marker": markers[episodeID-1].Label, "boundary_policy": "preserve_source_marker",
		})
	}
	return map[string]any{
		"episode_skeletons": skeletons,
		"global_risks":      []any{},
		"boundary_policy":   "preserve_existing_episode_marks",
	}, nil
}

func preservedEpisodeSplitCandidates(units []EpisodeSplitSourceUnit, plan map[string]any, batchStart int, batchEnd int, target int) ([]EpisodeSplitCandidate, []EpisodeSplitSourceUnit, error) {
	skeletons := mapSliceValue(plan["episode_skeletons"])
	if len(skeletons) != target {
		return nil, nil, fmt.Errorf("preserved split plan is unavailable")
	}
	unitIndex := map[string]int{}
	for index, unit := range units {
		unitIndex[unit.UnitID] = index
	}
	candidates := make([]EpisodeSplitCandidate, 0, batchEnd-batchStart+1)
	selectedUnits := map[int]bool{}
	for episodeID := batchStart; episodeID <= batchEnd; episodeID++ {
		unitID := strings.TrimSpace(fmt.Sprint(skeletons[episodeID-1]["approx_end_unit_id"]))
		position, ok := unitIndex[unitID]
		if !ok {
			return nil, nil, fmt.Errorf("unknown preserved boundary unit %q", unitID)
		}
		before := "[原文结束]"
		if position+1 < len(units) {
			before = anchorText(units[position+1].Text)
		}
		candidates = append(candidates, EpisodeSplitCandidate{
			CandidateID: fmt.Sprintf("E%03d-PRESERVED", episodeID), EpisodeID: episodeID,
			Offset: units[position].EndOffset, EndUnitID: unitID, CutAfterAnchor: anchorText(units[position].Text),
			CutBeforeAnchor: before, WindowChars: 0,
		})
		for nearby := position - 2; nearby <= position+2; nearby++ {
			if nearby >= 0 && nearby < len(units) {
				selectedUnits[nearby] = true
			}
		}
	}
	positions := make([]int, 0, len(selectedUnits))
	for position := range selectedUnits {
		positions = append(positions, position)
	}
	sort.Ints(positions)
	relevant := make([]EpisodeSplitSourceUnit, 0, len(positions))
	for _, position := range positions {
		relevant = append(relevant, units[position])
	}
	return candidates, relevant, nil
}

func episodeSplitCandidates(units []EpisodeSplitSourceUnit, plan map[string]any, completed []map[string]any, batchStart int, batchEnd int, target int) ([]EpisodeSplitCandidate, []EpisodeSplitSourceUnit, error) {
	skeletons := mapSliceValue(plan["episode_skeletons"])
	if len(skeletons) != target {
		return nil, nil, fmt.Errorf("global plan is unavailable")
	}
	unitIndex := map[string]int{}
	for index, unit := range units {
		unitIndex[unit.UnitID] = index
	}
	previousEnd := 0
	if len(completed) > 0 {
		previousEnd = episodeNumber(completed[len(completed)-1]["end_offset"])
	}
	candidates := []EpisodeSplitCandidate{}
	selectedUnits := map[int]bool{}
	averageChars := units[len(units)-1].EndOffset / target
	windowChars := averageChars * 12 / 10
	if windowChars < 300 {
		windowChars = 300
	}
	if windowChars > 1200 {
		windowChars = 1200
	}
	for episodeID := batchStart; episodeID <= batchEnd; episodeID++ {
		approxID := strings.TrimSpace(fmt.Sprint(skeletons[episodeID-1]["approx_end_unit_id"]))
		center, ok := unitIndex[approxID]
		if !ok {
			return nil, nil, fmt.Errorf("unknown skeleton unit %q", approxID)
		}
		minimumOffset := previousEnd
		if episodeID > 1 {
			previousID := strings.TrimSpace(fmt.Sprint(skeletons[episodeID-2]["approx_end_unit_id"]))
			previousCenter := unitIndex[previousID]
			minimumOffset = maxInt(minimumOffset, midpoint(units[previousCenter].EndOffset, units[center].EndOffset))
		}
		maximumOffset := units[len(units)-1].EndOffset
		if episodeID < target {
			nextID := strings.TrimSpace(fmt.Sprint(skeletons[episodeID]["approx_end_unit_id"]))
			nextCenter := unitIndex[nextID]
			maximumOffset = midpoint(units[center].EndOffset, units[nextCenter].EndOffset)
		}
		indexes := candidateUnitIndexes(units, center, minimumOffset, maximumOffset, episodeID == target, windowChars)
		if len(indexes) == 0 {
			return nil, nil, fmt.Errorf("episode %d has no valid boundary candidate", episodeID)
		}
		for candidateIndex, unitPosition := range indexes {
			unit := units[unitPosition]
			nextAnchor := "[原文结束]"
			if unitPosition+1 < len(units) {
				nextAnchor = anchorText(units[unitPosition+1].Text)
			}
			candidates = append(candidates, EpisodeSplitCandidate{
				CandidateID: fmt.Sprintf("E%03d-B%02d", episodeID, candidateIndex+1), EpisodeID: episodeID,
				Offset: unit.EndOffset, EndUnitID: unit.UnitID, CutAfterAnchor: anchorText(unit.Text), CutBeforeAnchor: nextAnchor, WindowChars: windowChars,
			})
			for nearby := unitPosition - 2; nearby <= unitPosition+2; nearby++ {
				if nearby >= 0 && nearby < len(units) {
					selectedUnits[nearby] = true
				}
			}
		}
	}
	positions := make([]int, 0, len(selectedUnits))
	for position := range selectedUnits {
		positions = append(positions, position)
	}
	sort.Ints(positions)
	relevant := make([]EpisodeSplitSourceUnit, 0, len(positions))
	for _, position := range positions {
		relevant = append(relevant, units[position])
	}
	return candidates, relevant, nil
}

func candidateUnitIndexes(units []EpisodeSplitSourceUnit, center int, minimumOffset int, maximumOffset int, forceLast bool, windowChars int) []int {
	if forceLast {
		return []int{len(units) - 1}
	}
	positions := []int{}
	centerOffset := units[center].EndOffset
	halfWindow := windowChars / 2
	for position, unit := range units {
		if position >= len(units)-1 || unit.EndOffset <= minimumOffset || unit.EndOffset > maximumOffset || absInt(unit.EndOffset-centerOffset) > halfWindow {
			continue
		}
		positions = append(positions, position)
	}
	if len(positions) > 7 {
		sort.SliceStable(positions, func(i, j int) bool {
			return absInt(units[positions[i]].EndOffset-centerOffset) < absInt(units[positions[j]].EndOffset-centerOffset)
		})
		positions = positions[:7]
		sort.Ints(positions)
	}
	if len(positions) == 0 && center >= 0 && center < len(units) && units[center].EndOffset > minimumOffset && units[center].EndOffset <= maximumOffset {
		positions = append(positions, center)
	}
	return positions
}

func midpoint(left int, right int) int {
	return left + (right-left)/2
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func anchorText(text string) string {
	trimmed := []rune(strings.TrimSpace(text))
	if len(trimmed) > 80 {
		trimmed = trimmed[len(trimmed)-80:]
	}
	return string(trimmed)
}

func recentCompletedSplitEpisodes(episodes []map[string]any) []map[string]any {
	if len(episodes) <= 2 {
		return episodes
	}
	return episodes[len(episodes)-2:]
}

func resolveEpisodeSplitBatch(raw []map[string]any, candidates []EpisodeSplitCandidate, units []EpisodeSplitSourceUnit, completed []map[string]any, batchStart int, batchEnd int) ([]map[string]any, error) {
	expected := batchEnd - batchStart + 1
	if len(raw) != expected {
		return nil, fmt.Errorf("batch returned %d episodes, expected %d", len(raw), expected)
	}
	candidateByID := map[string]EpisodeSplitCandidate{}
	for _, candidate := range candidates {
		candidateByID[candidate.CandidateID] = candidate
	}
	previousEnd := 0
	if len(completed) > 0 {
		previousEnd = episodeNumber(completed[len(completed)-1]["end_offset"])
	}
	resolved := make([]map[string]any, 0, expected)
	for index, item := range raw {
		episodeID := episodeNumber(item["episode_id"])
		if episodeID != batchStart+index {
			return nil, fmt.Errorf("batch episode_id %d does not match expected %d", episodeID, batchStart+index)
		}
		candidateID := strings.TrimSpace(fmt.Sprint(item["end_candidate_id"]))
		candidate, ok := candidateByID[candidateID]
		if !ok || candidate.EpisodeID != episodeID {
			return nil, fmt.Errorf("episode %d selected invalid candidate %q", episodeID, candidateID)
		}
		if candidate.Offset <= previousEnd {
			return nil, fmt.Errorf("episode %d boundary is not after previous boundary", episodeID)
		}
		startUnit, endUnit := sourceUnitRangeForOffsets(units, previousEnd, candidate.Offset)
		resolvedItem := clonePayload(item)
		delete(resolvedItem, "end_candidate_id")
		resolvedItem["episode_id"] = episodeID
		resolvedItem["start_offset"] = previousEnd
		resolvedItem["end_offset"] = candidate.Offset
		resolvedItem["source_refs"] = []any{map[string]any{
			"source_unit_id": startUnit.UnitID + "-" + endUnit.UnitID,
			"source_range":   fmt.Sprintf("字符 %d-%d", previousEnd, candidate.Offset),
			"start_anchor":   anchorText(startUnit.Text), "end_anchor": anchorText(endUnit.Text),
			"start_offset": previousEnd, "end_offset": candidate.Offset,
		}}
		resolvedItem["boundary_check"] = map[string]any{
			"previous_episode_end": candidate.CutAfterAnchor, "next_episode_start": candidate.CutBeforeAnchor,
			"candidate_window_chars": candidate.WindowChars, "cut_after_anchor": candidate.CutAfterAnchor, "cut_before_anchor": candidate.CutBeforeAnchor,
			"continuity_risk": "none", "qa_or_action_split_risk": false, "manual_review_required": boolValue(item["requires_user_attention"]),
		}
		resolved = append(resolved, resolvedItem)
		previousEnd = candidate.Offset
	}
	return resolved, nil
}

func sourceUnitRangeForOffsets(units []EpisodeSplitSourceUnit, start int, end int) (EpisodeSplitSourceUnit, EpisodeSplitSourceUnit) {
	first := units[0]
	last := units[len(units)-1]
	for _, unit := range units {
		if start >= unit.StartOffset && start < unit.EndOffset {
			first = unit
		}
		if end > unit.StartOffset && end <= unit.EndOffset {
			last = unit
			break
		}
	}
	return first, last
}

func buildEpisodeSplitArtifactPayload(progress episodeSplitProgress, units []EpisodeSplitSourceUnit, config map[string]any) (map[string]any, error) {
	if len(progress.CompletedEpisodes) != progress.TargetCount {
		return nil, fmt.Errorf("completed episodes %d does not match target %d", len(progress.CompletedEpisodes), progress.TargetCount)
	}
	expectedStart := 0
	covered := make([]any, 0, progress.TargetCount)
	for index, episode := range progress.CompletedEpisodes {
		if episodeNumber(episode["episode_id"]) != index+1 {
			return nil, fmt.Errorf("episode ids are not continuous at index %d", index)
		}
		start := episodeNumber(episode["start_offset"])
		end := episodeNumber(episode["end_offset"])
		if start != expectedStart || end <= start {
			return nil, fmt.Errorf("episode %d range %d-%d is not continuous from %d", index+1, start, end, expectedStart)
		}
		covered = append(covered, fmt.Sprintf("%d-%d", start, end))
		expectedStart = end
	}
	if expectedStart != units[len(units)-1].EndOffset {
		return nil, fmt.Errorf("final boundary %d does not cover source end %d", expectedStart, units[len(units)-1].EndOffset)
	}
	return map[string]any{
		"generation_config": clonePayload(config), "target_episode_count": progress.TargetCount, "actual_episode_count": progress.TargetCount,
		"split_strategy": "全局骨架与顺序分批边界拆分", "episodes": mapSliceToAny(progress.CompletedEpisodes),
		"source_volume_assessment": map[string]any{"source_chars": units[len(units)-1].EndOffset, "effective_source_chars": units[len(units)-1].EndOffset, "target_source_chars_per_episode": units[len(units)-1].EndOffset / progress.TargetCount},
		"coverage_check":           map[string]any{"covered_source_ranges": covered, "missing_source_ranges": []any{}, "duplicated_source_ranges": []any{}, "order_issues": []any{}},
		"global_risks":             progress.GlobalRisks, "source_trace": map[string]any{"from_source_text": []any{"完整原文索引"}, "from_story_bible": []any{"确认版故事圣经"}, "model_inference": []any{}},
	}, nil
}

func mapValue(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

func mapSliceValue(value any) []map[string]any {
	switch items := value.(type) {
	case []map[string]any:
		return items
	case []any:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if object, ok := item.(map[string]any); ok {
				out = append(out, object)
			}
		}
		return out
	default:
		return nil
	}
}

func mapSliceToAny(items []map[string]any) []any {
	out := make([]any, len(items))
	for index, item := range items {
		out[index] = item
	}
	return out
}

func sliceValue(value any) []any {
	if items, ok := value.([]any); ok {
		return items
	}
	return []any{}
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}
