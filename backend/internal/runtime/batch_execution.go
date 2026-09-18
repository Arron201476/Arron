package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"content-agent/backend/internal/capability"
)

func (s *Store) planBatchStepTasksTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	stepRunID string,
	step capability.CompiledStep,
	stepInput json.RawMessage,
	baseCursor json.RawMessage,
	now time.Time,
) error {
	if step.Batch == nil {
		return domainError(
			"RUN_TASKS_NOT_READY",
			"当前批量步骤缺少可执行的 Task 规划策略。",
		)
	}
	if (len(step.OutputRefs) != 1 && !isScriptBundleStep(step)) ||
		(step.OutputRefs[0].Cardinality != "one" &&
			step.OutputRefs[0].Cardinality != "many") {
		return domainError(
			"RUN_TASKS_NOT_READY",
			"批量步骤输出基数当前不受支持。",
		)
	}
	if step.OutputRefs[0].Cardinality == "many" &&
		step.Batch.MaxItemsPerTask != 1 {
		return domainError(
			"RUN_TASKS_NOT_READY",
			"多产物批量步骤必须每个 Task 只生成一个作用域产物。",
		)
	}
	if step.Batch.ItemKey == "source_analysis" {
		return s.planNovelSourceAnalysisTaskTx(
			ctx,
			tx,
			run,
			stepRunID,
			step,
			stepInput,
			baseCursor,
			now,
		)
	}
	if isVideoScriptExtractionStep(step) {
		return s.planVideoAssetTasksTx(ctx, tx, run, stepRunID, step, stepInput, baseCursor, nil, now)
	}
	if step.Batch.ItemKey != "episode_no" {
		return domainError(
			"RUN_TASKS_NOT_READY",
			"当前批量步骤没有已实现的 Task 规划策略。",
		)
	}
	configPayload, err := stepConfigPayloadTx(ctx, tx, run, step)
	if err != nil {
		return err
	}
	targetEpisodeCount, err := targetEpisodeCount(configPayload)
	if err != nil {
		return err
	}
	batchSize := step.Batch.MaxItemsPerTask
	if batchSize <= 0 {
		batchSize = 1
	}
	inputSnapshot, err := json.Marshal(map[string]any{
		"run_input_snapshot_version_id": run.CurrentInputSnapshotVersionID,
		"step_input_versions":           json.RawMessage(stepInput),
	})
	if err != nil {
		return err
	}
	var base map[string]any
	if len(baseCursor) != 0 {
		if err := json.Unmarshal(baseCursor, &base); err != nil {
			return domainError("RUN_CURSOR_INVALID", "批量步骤恢复游标无效。")
		}
	}
	if base == nil {
		base = map[string]any{}
	}
	if step.Batch.Preparation != nil {
		sourceUnits, err := s.episodeSplitSourceUnitsTx(
			ctx,
			tx,
			run.ProjectID,
			stepInput,
			targetEpisodeCount,
		)
		if err != nil {
			return err
		}
		cursor := cloneJSONMap(base)
		cursor["batch"] = map[string]any{
			"phase":                step.Batch.Preparation.ID,
			"target_episode_count": targetEpisodeCount,
			"max_items_per_task":   batchSize,
			"source_units":         sourceUnits,
		}
		return s.insertBatchTaskTx(
			ctx,
			tx,
			run,
			stepRunID,
			step.ExecutorRef,
			"preparation:"+step.Batch.Preparation.ID,
			1,
			inputSnapshot,
			cursor,
			map[string]any{
				"phase": step.Batch.Preparation.ID,
			},
			now,
		)
	}
	return s.enqueueBatchRangeTasksTx(
		ctx,
		tx,
		run,
		stepRunID,
		step,
		inputSnapshot,
		base,
		targetEpisodeCount,
		1,
		targetEpisodeCount,
		0,
		nil,
		now,
	)
}

func (s *Store) planVideoAssetTasksTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	stepRunID string,
	step capability.CompiledStep,
	stepInput json.RawMessage,
	baseCursor json.RawMessage,
	selectedTaskKeys []string,
	now time.Time,
) error {
	if step.Batch == nil || step.OutputRefs[0].ArtifactType != "video_script_unit" ||
		step.OutputRefs[0].Cardinality != "many" || step.Batch.MaxItemsPerTask != 1 {
		return domainError("RUN_TASKS_NOT_READY", "视频逐集提取批量合同无效。")
	}
	var input sourceInputRequest
	var snapshotPayload string
	if err := tx.QueryRowContext(ctx, `
		SELECT payload_json FROM run_input_snapshot_versions
		WHERE run_input_snapshot_version_id = ? AND run_id = ? AND status = 'sealed'`,
		run.CurrentInputSnapshotVersionID, run.RunID,
	).Scan(&snapshotPayload); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(snapshotPayload), &input); err != nil ||
		input.SourceType != "video_reference" || input.AssetSetVersionID == "" || len(input.Assets) == 0 {
		return domainError("RUN_TASKS_NOT_READY", "视频任务缺少已封存来源集合。")
	}
	inputSnapshot, err := json.Marshal(map[string]any{
		"run_input_snapshot_version_id": run.CurrentInputSnapshotVersionID,
		"step_input_versions":           json.RawMessage(stepInput),
	})
	if err != nil {
		return err
	}
	var base map[string]any
	if len(baseCursor) != 0 {
		if err := json.Unmarshal(baseCursor, &base); err != nil {
			return domainError("RUN_CURSOR_INVALID", "视频批量步骤恢复游标无效。")
		}
	}
	if base == nil {
		base = map[string]any{}
	}
	for _, raw := range input.Assets {
		reference, err := validateAssetReference(raw)
		if err != nil {
			return domainError("RUN_TASKS_NOT_READY", "视频任务材料引用无效。")
		}
		var filename string
		var episodeNo sql.NullInt64
		if err := tx.QueryRowContext(ctx, `
			SELECT a.original_filename, asm.episode_no
			FROM assets a
			JOIN asset_set_members asm ON asm.asset_id = a.asset_id
			WHERE a.asset_id = ? AND a.current_snapshot_id = ?
				AND asm.asset_set_version_id = ? AND asm.included = 1`,
			reference.AssetID, reference.AssetSnapshotID, input.AssetSetVersionID,
		).Scan(&filename, &episodeNo); err != nil {
			return domainError("RUN_TASKS_NOT_READY", "视频任务无法定位已封存集号和文件。")
		}
		itemKey := fmt.Sprintf("episode:%d", reference.Order)
		if len(selectedTaskKeys) > 0 && !slices.Contains(selectedTaskKeys, itemKey) {
			continue
		}
		cursor := cloneJSONMap(base)
		videoCursor := map[string]any{
			"asset_id":          reference.AssetID,
			"asset_snapshot_id": reference.AssetSnapshotID,
			"file_name":         filename,
			"episode_order":     reference.Order,
		}
		if episodeNo.Valid {
			videoCursor["episode_no"] = int(episodeNo.Int64)
		} else {
			videoCursor["episode_no"] = nil
		}
		cursor["video"] = videoCursor
		if err := s.insertBatchTaskTx(
			ctx, tx, run, stepRunID, step.ExecutorRef,
			itemKey, reference.Order,
			inputSnapshot, cursor,
			map[string]any{"asset_id": reference.AssetID, "episode_order": reference.Order},
			now,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) planNovelSourceAnalysisTaskTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	stepRunID string,
	step capability.CompiledStep,
	stepInput json.RawMessage,
	baseCursor json.RawMessage,
	now time.Time,
) error {
	if step.Batch == nil ||
		step.Batch.Preparation == nil ||
		step.Batch.Preparation.ID != "source_analysis" ||
		step.Batch.TaskStage == nil ||
		step.Batch.TaskStage.ID != "story_bible_aggregate" ||
		step.Batch.TaskStage.ResultMode != "artifact" ||
		step.OutputRefs[0].ArtifactType != "story_bible" ||
		step.OutputRefs[0].Cardinality != "one" {
		return domainError(
			"RUN_TASKS_NOT_READY",
			"小说来源分析批量合同无效。",
		)
	}
	sourceUnits, err := s.episodeSplitSourceUnitsTx(
		ctx,
		tx,
		run.ProjectID,
		stepInput,
		0,
	)
	if err != nil {
		return err
	}
	inputSnapshot, err := json.Marshal(map[string]any{
		"run_input_snapshot_version_id": run.CurrentInputSnapshotVersionID,
		"step_input_versions":           json.RawMessage(stepInput),
	})
	if err != nil {
		return err
	}
	var cursor map[string]any
	if len(baseCursor) != 0 {
		if err := json.Unmarshal(baseCursor, &cursor); err != nil {
			return domainError("RUN_CURSOR_INVALID", "小说来源分析恢复游标无效。")
		}
	}
	if cursor == nil {
		cursor = map[string]any{}
	}
	cursor["batch"] = map[string]any{
		"phase":        step.Batch.Preparation.ID,
		"source_units": sourceUnits,
	}
	return s.insertBatchTaskTx(
		ctx,
		tx,
		run,
		stepRunID,
		step.ExecutorRef,
		"preparation:"+step.Batch.Preparation.ID,
		1,
		inputSnapshot,
		cursor,
		map[string]any{
			"phase":             step.Batch.Preparation.ID,
			"source_unit_count": len(sourceUnits),
		},
		now,
	)
}

func (s *Store) enqueueBatchRangeTasksTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	stepRunID string,
	step capability.CompiledStep,
	inputSnapshot json.RawMessage,
	baseCursor map[string]any,
	targetEpisodeCount int,
	firstEpisode int,
	lastEpisode int,
	orderOffset int,
	globalPlan map[string]any,
	now time.Time,
) error {
	batchSize := step.Batch.MaxItemsPerTask
	if batchSize <= 0 {
		batchSize = 1
	}
	batchCount := (targetEpisodeCount + batchSize - 1) / batchSize
	if firstEpisode <= 0 {
		firstEpisode = 1
	}
	if lastEpisode > targetEpisodeCount {
		lastEpisode = targetEpisodeCount
	}
	for batchIndex := 1; batchIndex <= batchCount; batchIndex++ {
		start := (batchIndex-1)*batchSize + 1
		end := start + batchSize - 1
		if end > targetEpisodeCount {
			end = targetEpisodeCount
		}
		if start < firstEpisode || start > lastEpisode {
			continue
		}
		cursor := cloneJSONMap(baseCursor)
		batchCursor := map[string]any{
			"phase":                "batch",
			"batch_index":          batchIndex,
			"batch_count":          batchCount,
			"episode_start":        start,
			"episode_end":          end,
			"target_episode_count": targetEpisodeCount,
		}
		if step.Batch.TaskStage != nil {
			batchCursor["phase"] = step.Batch.TaskStage.ID
		}
		if globalPlan != nil {
			batchCursor["global_plan"] = globalPlan
		}
		cursor["batch"] = batchCursor
		itemKey := fmt.Sprintf("episode:%d-%d", start, end)
		if start == end {
			itemKey = fmt.Sprintf("episode:%d", start)
		}
		if err := s.insertBatchTaskTx(
			ctx,
			tx,
			run,
			stepRunID,
			step.ExecutorRef,
			itemKey,
			orderOffset+batchIndex,
			inputSnapshot,
			cursor,
			map[string]any{
				"batch_index":   batchIndex,
				"batch_count":   batchCount,
				"episode_start": start,
				"episode_end":   end,
			},
			now,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) insertBatchTaskTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	stepRunID string,
	executorID string,
	itemKey string,
	itemOrder int,
	inputSnapshot json.RawMessage,
	cursor map[string]any,
	eventPayload map[string]any,
	now time.Time,
) error {
	cursorJSON, err := json.Marshal(cursor)
	if err != nil {
		return err
	}
	taskID := s.newID("tsk")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO task_items(
			task_item_id, step_run_id, run_id, item_key, item_order, status,
			attempt_count, input_snapshot_json, cursor_json, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?, ?)`,
		taskID,
		stepRunID,
		run.RunID,
		itemKey,
		itemOrder,
		string(inputSnapshot),
		string(cursorJSON),
		formatTime(now),
		formatTime(now),
	); err != nil {
		return err
	}
	payload := cloneJSONMap(eventPayload)
	payload["executor_id"] = executorID
	payload["item_key"] = itemKey
	runRef, stepRef := run.RunID, stepRunID
	_, err = s.appendEvent(
		ctx,
		tx,
		run.ProjectID,
		&runRef,
		&stepRef,
		"task.queued",
		"task_item",
		taskID,
		payload,
	)
	return err
}

type episodeSplitSourceUnit struct {
	UnitID          string `json:"unit_id"`
	SourceUnitID    string `json:"source_unit_id"`
	AssetID         string `json:"asset_id"`
	AssetSnapshotID string `json:"asset_snapshot_id"`
	StartOffset     int    `json:"start_offset"`
	EndOffset       int    `json:"end_offset"`
	Text            string `json:"text"`
}

func (s *Store) episodeSplitSourceUnitsTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	stepInput json.RawMessage,
	minUnitCount int,
) ([]episodeSplitSourceUnit, error) {
	var inputs []struct {
		ArtifactVersionID string `json:"artifact_version_id"`
	}
	if err := json.Unmarshal(stepInput, &inputs); err != nil {
		return nil, domainError("RUN_CURSOR_INVALID", "拆集步骤输入版本无法读取。")
	}
	var sourcePayload string
	for _, input := range inputs {
		err := tx.QueryRowContext(ctx, `
			SELECT av.payload_json
			FROM artifact_versions av
			JOIN artifacts a ON a.artifact_id = av.artifact_id
			WHERE av.artifact_version_id = ? AND a.project_id = ?
				AND a.artifact_type = 'source_input'`,
			input.ArtifactVersionID,
			projectID,
		).Scan(&sourcePayload)
		if err == nil {
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if sourcePayload == "" {
		return nil, domainError("DEPENDENCY_INCOMPLETE", "小说拆集缺少来源材料版本。")
	}
	var source sourceInputPayload
	if err := json.Unmarshal([]byte(sourcePayload), &source); err != nil {
		return nil, domainError("DEPENDENCY_INCOMPLETE", "小说来源材料无法读取。")
	}
	manifest, err := loadSourceManifestFromInputsTx(ctx, tx, projectID, inputs)
	if err != nil {
		return nil, err
	}
	builtUnits, err := s.buildTextSourceUnitsTx(
		ctx,
		tx,
		projectID,
		source.SourceKind,
		source.Assets,
		source.ArtifactVersions,
	)
	if err != nil {
		return nil, err
	}
	if err := validateBuiltUnitsAgainstManifest(builtUnits, manifest); err != nil {
		return nil, err
	}
	units := make([]episodeSplitSourceUnit, 0, len(builtUnits))
	for index, unit := range builtUnits {
		units = append(units, episodeSplitSourceUnit{
			UnitID:          unitIDForOrder(index + 1),
			SourceUnitID:    unit.ManifestUnit.SourceUnitID,
			AssetID:         unit.ManifestUnit.AssetID,
			AssetSnapshotID: unit.ManifestUnit.AssetSnapshotID,
			StartOffset:     unit.StartOffset,
			EndOffset:       unit.EndOffset,
			Text:            unit.Text,
		})
	}
	return ensureMinimumEpisodeSplitSourceUnits(units, minUnitCount), nil
}

func ensureMinimumEpisodeSplitSourceUnits(
	units []episodeSplitSourceUnit,
	minUnitCount int,
) []episodeSplitSourceUnit {
	for len(units) < minUnitCount {
		largestIndex := -1
		largestLength := 1
		for index, unit := range units {
			if length := len([]rune(unit.Text)); length > largestLength {
				largestIndex = index
				largestLength = length
			}
		}
		if largestIndex < 0 {
			break
		}
		unit := units[largestIndex]
		runes := []rune(unit.Text)
		splitAt := nearestEpisodeSplitBoundary(runes)
		left := unit
		left.EndOffset = unit.StartOffset + splitAt
		left.Text = strings.TrimSpace(string(runes[:splitAt]))
		right := unit
		right.StartOffset = unit.StartOffset + splitAt
		right.Text = strings.TrimSpace(string(runes[splitAt:]))
		units = append(
			units[:largestIndex],
			append([]episodeSplitSourceUnit{left, right}, units[largestIndex+1:]...)...,
		)
	}
	for index := range units {
		units[index].UnitID = unitIDForOrder(index + 1)
	}
	return units
}

func nearestEpisodeSplitBoundary(runes []rune) int {
	middle := len(runes) / 2
	for distance := 0; distance < len(runes)/2; distance++ {
		for _, candidate := range []int{middle + distance, middle - distance} {
			if candidate <= 0 || candidate >= len(runes) {
				continue
			}
			character := runes[candidate-1]
			if character == '\n' || character == '。' || character == '！' ||
				character == '？' || character == '；' {
				return candidate
			}
		}
	}
	return middle
}

type sourceTextSegment struct {
	start int
	end   int
	text  string
}

func splitSourceTextIntoUnits(content string) []sourceTextSegment {
	runes := []rune(content)
	result := make([]sourceTextSegment, 0)
	start := 0
	appendSegment := func(end int) {
		if end <= start {
			return
		}
		text := strings.TrimSpace(string(runes[start:end]))
		if text != "" {
			const maxUnitPreviewRunes = 48
			preview := []rune(text)
			if len(preview) > maxUnitPreviewRunes {
				text = string(preview[:maxUnitPreviewRunes])
			}
			result = append(result, sourceTextSegment{start: start, end: end, text: text})
		}
		start = end
	}
	for index, character := range runes {
		length := index + 1 - start
		naturalBoundary := character == '\n' || character == '。' ||
			character == '！' || character == '？' || character == '；'
		if length >= 200 && naturalBoundary || length >= 800 {
			appendSegment(index + 1)
		}
	}
	appendSegment(len(runes))
	return result
}

func targetEpisodeCount(config json.RawMessage) (int, error) {
	var envelope struct {
		Payload struct {
			TargetEpisodeCount int `json:"target_episode_count"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(config, &envelope); err != nil {
		return 0, domainError("RUN_STATE_CONFLICT", "生成配置无法读取。")
	}
	if envelope.Payload.TargetEpisodeCount <= 0 || envelope.Payload.TargetEpisodeCount > MaxAssetSetEpisodeNo {
		return 0, domainError("RUN_STATE_CONFLICT", "目标集数必须为 1 到 10000 的整数。")
	}
	return envelope.Payload.TargetEpisodeCount, nil
}

func cloneJSONMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func batchInternalStageForCursor(
	policy *capability.BatchPolicy,
	cursor json.RawMessage,
) (*capability.BatchInternalTaskStage, bool) {
	stage, ok := batchStageForCursor(policy, cursor)
	if !ok || stage.ResultMode == "artifact" {
		return nil, false
	}
	return stage, true
}

func batchStageForCursor(
	policy *capability.BatchPolicy,
	cursor json.RawMessage,
) (*capability.BatchInternalTaskStage, bool) {
	if policy == nil {
		return nil, false
	}
	var payload struct {
		Batch struct {
			Phase string `json:"phase"`
		} `json:"batch"`
	}
	if json.Unmarshal(cursor, &payload) != nil || payload.Batch.Phase == "" {
		return nil, false
	}
	for _, stage := range []*capability.BatchInternalTaskStage{
		policy.Preparation,
		policy.TaskStage,
	} {
		if stage != nil && stage.ID == payload.Batch.Phase {
			return stage, true
		}
	}
	return nil, false
}

func (s *Store) commitBatchTaskCheckpointTx(
	ctx context.Context,
	tx *sql.Tx,
	attemptID string,
	taskItemID string,
	stepRunID string,
	runID string,
	projectID string,
	artifactType string,
	payload json.RawMessage,
	step capability.CompiledStep,
	now time.Time,
) (*TaskResultCheckpoint, bool, json.RawMessage, error) {
	checkpoint, err := s.saveTaskResultCheckpointTx(
		ctx,
		tx,
		attemptID,
		taskItemID,
		stepRunID,
		runID,
		projectID,
		artifactType,
		payload,
		now,
	)
	if err != nil {
		return nil, false, nil, err
	}
	if artifactType == "episode_split" &&
		step.Batch != nil &&
		step.Batch.Preparation != nil {
		if err := s.enqueueNextEpisodeSplitBatchTx(
			ctx,
			tx,
			runID,
			projectID,
			stepRunID,
			step,
			taskItemID,
			payload,
			now,
		); err != nil {
			return nil, false, nil, err
		}
	}

	var totalTasks, completedTasks, failedTasks int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*),
			SUM(CASE WHEN status = 'succeeded' THEN 1 ELSE 0 END),
			SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END)
		FROM task_items WHERE step_run_id = ?`,
		stepRunID,
	).Scan(&totalTasks, &completedTasks, &failedTasks); err != nil {
		return nil, false, nil, err
	}
	if failedTasks > 0 && completedTasks+failedTasks == totalTasks &&
		step.Batch != nil && step.Batch.FailurePolicy == "preserve_success_retry_failed" {
		if _, err := s.finalizePreservedBatchFailureIfSettledTx(
			ctx, tx, projectID, runID, stepRunID, "WORKER_INTERNAL_ERROR", now,
		); err != nil {
			return nil, false, nil, err
		}
		return checkpoint, false, nil, nil
	}
	if completedTasks != totalTasks {
		if err := s.pauseAfterCompletedTaskIfRequestedTx(
			ctx, tx, runID, now, "task_completed",
		); err != nil {
			return nil, false, nil, err
		}
		return checkpoint, false, nil, nil
	}
	merged, err := mergeBatchCheckpointsTx(
		ctx,
		tx,
		stepRunID,
		artifactType,
	)
	if err != nil {
		return nil, false, nil, err
	}
	return checkpoint, true, merged, nil
}

func (s *Store) saveTaskResultCheckpointTx(
	ctx context.Context,
	tx *sql.Tx,
	attemptID string,
	taskItemID string,
	stepRunID string,
	runID string,
	projectID string,
	artifactType string,
	payload json.RawMessage,
	now time.Time,
) (*TaskResultCheckpoint, error) {
	var itemKey string
	var itemOrder int
	if err := tx.QueryRowContext(ctx, `
		SELECT item_key, item_order FROM task_items
		WHERE task_item_id = ? AND step_run_id = ? AND status = 'running'
			AND current_attempt_id = ?`,
		taskItemID,
		stepRunID,
		attemptID,
	).Scan(&itemKey, &itemOrder); err != nil {
		return nil, err
	}
	checkpoint := &TaskResultCheckpoint{
		TaskResultCheckpointID: s.newID("trc"),
		TaskItemID:             taskItemID,
		AttemptID:              attemptID,
		StepRunID:              stepRunID,
		RunID:                  runID,
		ItemKey:                itemKey,
		ItemOrder:              itemOrder,
		ArtifactType:           artifactType,
		Payload:                payload,
		PayloadHash:            sha256Hex(payload),
		CreatedAt:              now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO task_result_checkpoints(
			task_result_checkpoint_id, task_item_id, attempt_id, step_run_id,
			run_id, item_key, item_order, artifact_type, payload_json,
			payload_hash, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		checkpoint.TaskResultCheckpointID,
		checkpoint.TaskItemID,
		checkpoint.AttemptID,
		checkpoint.StepRunID,
		checkpoint.RunID,
		checkpoint.ItemKey,
		checkpoint.ItemOrder,
		checkpoint.ArtifactType,
		string(checkpoint.Payload),
		checkpoint.PayloadHash,
		formatTime(checkpoint.CreatedAt),
	); err != nil {
		return nil, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE execution_attempts SET status = 'succeeded'
		WHERE attempt_id = ? AND status = 'result_received'`,
		"执行尝试状态已变化。",
		attemptID,
	); err != nil {
		return nil, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE task_items
		SET status = 'succeeded', failure = NULL, ended_at = ?, updated_at = ?
		WHERE task_item_id = ? AND status = 'running'
			AND current_attempt_id = ?`,
		"执行任务状态已变化。",
		formatTime(now), formatTime(now), taskItemID, attemptID,
	); err != nil {
		return nil, err
	}
	runRef, stepRef := runID, stepRunID
	for _, event := range []struct {
		eventType   string
		subjectType string
		subjectID   string
		payload     any
	}{
		{"task.checkpoint_saved", "task_result_checkpoint", checkpoint.TaskResultCheckpointID, map[string]any{
			"item_key":     checkpoint.ItemKey,
			"payload_hash": checkpoint.PayloadHash,
		}},
		{"task.completed", "task_item", taskItemID, map[string]any{
			"attempt_id": attemptID,
		}},
	} {
		if _, err := s.appendEvent(
			ctx,
			tx,
			projectID,
			&runRef,
			&stepRef,
			event.eventType,
			event.subjectType,
			event.subjectID,
			event.payload,
		); err != nil {
			return nil, err
		}
	}
	return checkpoint, nil
}

func (s *Store) commitBatchPreparationCheckpointTx(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	projectID string,
	stepRunID string,
	taskItemID string,
	attemptID string,
	step capability.CompiledStep,
	contextPack StepExecutionContextPack,
	payload json.RawMessage,
	now time.Time,
) (*TaskResultCheckpoint, error) {
	if step.Batch == nil || step.Batch.Preparation == nil ||
		step.Batch.TaskStage == nil {
		return nil, domainError(
			"RUN_STATE_CONFLICT",
			"批量准备任务缺少后续 Task Stage 声明。",
		)
	}
	if step.Batch.ItemKey == "source_analysis" {
		return s.commitNovelSourceAnalysisCheckpointTx(
			ctx,
			tx,
			runID,
			projectID,
			stepRunID,
			taskItemID,
			attemptID,
			step,
			contextPack,
			payload,
			now,
		)
	}
	globalPlan, sourceUnits, targetCount, err := validateEpisodeSplitGlobalPlan(
		contextPack.TaskCursor,
		payload,
	)
	if err != nil {
		return nil, err
	}
	checkpoint, err := s.saveTaskResultCheckpointTx(
		ctx,
		tx,
		attemptID,
		taskItemID,
		stepRunID,
		runID,
		projectID,
		step.OutputRefs[0].ArtifactType,
		payload,
		now,
	)
	if err != nil {
		return nil, err
	}
	run, err := getRunTx(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	var inputSnapshotJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT input_snapshot_json FROM task_items WHERE task_item_id = ?`,
		taskItemID,
	).Scan(&inputSnapshotJSON); err != nil {
		return nil, err
	}
	if err := s.enqueueEpisodeSplitBatchTaskTx(
		ctx,
		tx,
		run,
		stepRunID,
		step,
		json.RawMessage(inputSnapshotJSON),
		globalPlan,
		sourceUnits,
		targetCount,
		1,
		0,
		now,
	); err != nil {
		return nil, err
	}
	return checkpoint, nil
}

func (s *Store) commitNovelSourceAnalysisCheckpointTx(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	projectID string,
	stepRunID string,
	taskItemID string,
	attemptID string,
	step capability.CompiledStep,
	contextPack StepExecutionContextPack,
	payload json.RawMessage,
	now time.Time,
) (*TaskResultCheckpoint, error) {
	if step.Batch == nil ||
		step.Batch.Preparation == nil ||
		step.Batch.Preparation.ID != "source_analysis" ||
		step.Batch.TaskStage == nil ||
		step.Batch.TaskStage.ID != "story_bible_aggregate" ||
		step.Batch.TaskStage.ResultMode != "artifact" {
		return nil, domainError(
			"RUN_STATE_CONFLICT",
			"小说来源分析聚合合同无效。",
		)
	}
	if err := validateNovelSourceAnalysisCheckpoint(
		contextPack.TaskCursor,
		payload,
	); err != nil {
		return nil, err
	}
	checkpoint, err := s.saveTaskResultCheckpointTx(
		ctx,
		tx,
		attemptID,
		taskItemID,
		stepRunID,
		runID,
		projectID,
		step.OutputRefs[0].ArtifactType,
		payload,
		now,
	)
	if err != nil {
		return nil, err
	}
	run, err := getRunTx(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	var inputSnapshotJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT input_snapshot_json FROM task_items WHERE task_item_id = ?`,
		taskItemID,
	).Scan(&inputSnapshotJSON); err != nil {
		return nil, err
	}
	cursor := map[string]any{
		"batch": map[string]any{
			"phase":           step.Batch.TaskStage.ID,
			"source_analysis": json.RawMessage(payload),
		},
	}
	if err := s.insertBatchTaskTx(
		ctx,
		tx,
		run,
		stepRunID,
		step.ExecutorRef,
		"aggregate:"+step.Batch.TaskStage.ID,
		2,
		json.RawMessage(inputSnapshotJSON),
		cursor,
		map[string]any{
			"phase": step.Batch.TaskStage.ID,
		},
		now,
	); err != nil {
		return nil, err
	}
	return checkpoint, nil
}

func validateNovelSourceAnalysisCheckpoint(
	cursorPayload json.RawMessage,
	resultPayload json.RawMessage,
) error {
	var cursor struct {
		Batch struct {
			SourceUnits []episodeSplitSourceUnit `json:"source_units"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(cursorPayload, &cursor); err != nil ||
		len(cursor.Batch.SourceUnits) == 0 {
		return domainError(
			"RUN_CURSOR_INVALID",
			"小说来源分析任务缺少来源单元。",
		)
	}
	var result struct {
		SourceKind string `json:"source_kind"`
		Units      []struct {
			SourceUnitID string `json:"source_unit_id"`
			SourceRefs   []struct {
				SourceType      string `json:"source_type"`
				AssetID         string `json:"asset_id"`
				AssetSnapshotID string `json:"asset_snapshot_id"`
				SourceUnitID    string `json:"source_unit_id"`
			} `json:"source_refs"`
		} `json:"units"`
		CoverageCheck struct {
			CoveredSourceUnitIDs []string `json:"covered_source_unit_ids"`
			MissingSourceUnitIDs []string `json:"missing_source_unit_ids"`
			OrderIssues          []string `json:"order_issues"`
		} `json:"coverage_check"`
	}
	if err := json.Unmarshal(resultPayload, &result); err != nil ||
		result.SourceKind != "novel" ||
		len(result.Units) != len(cursor.Batch.SourceUnits) ||
		len(result.CoverageCheck.CoveredSourceUnitIDs) != len(cursor.Batch.SourceUnits) ||
		len(result.CoverageCheck.MissingSourceUnitIDs) != 0 ||
		len(result.CoverageCheck.OrderIssues) != 0 {
		return domainError(
			"BATCH_COVERAGE_INVALID",
			"小说来源分析没有完整覆盖来源清单。",
		)
	}
	for index, expected := range cursor.Batch.SourceUnits {
		actual := result.Units[index]
		if actual.SourceUnitID != expected.SourceUnitID ||
			result.CoverageCheck.CoveredSourceUnitIDs[index] != expected.SourceUnitID {
			return domainError(
				"BATCH_COVERAGE_INVALID",
				"小说来源分析的来源单元顺序或 ID 不一致。",
			)
		}
		matchedRef := false
		for _, reference := range actual.SourceRefs {
			if reference.SourceType == "asset_text_range" &&
				reference.AssetID == expected.AssetID &&
				reference.AssetSnapshotID == expected.AssetSnapshotID &&
				reference.SourceUnitID == expected.SourceUnitID {
				matchedRef = true
				break
			}
		}
		if !matchedRef {
			return domainError(
				"BATCH_COVERAGE_INVALID",
				"小说来源分析缺少与来源清单一致的精确引用。",
			)
		}
	}
	return nil
}

func validateStoryBibleAgainstSourceAnalysis(
	cursorPayload json.RawMessage,
	storyBiblePayload json.RawMessage,
) error {
	var cursor struct {
		Batch struct {
			SourceAnalysis struct {
				Units []struct {
					SourceUnitID string `json:"source_unit_id"`
				} `json:"units"`
			} `json:"source_analysis"`
		} `json:"batch"`
	}
	var story struct {
		SourceStructure []struct {
			SourceUnitID string `json:"source_unit_id"`
		} `json:"source_structure"`
	}
	if err := json.Unmarshal(cursorPayload, &cursor); err != nil ||
		len(cursor.Batch.SourceAnalysis.Units) == 0 ||
		json.Unmarshal(storyBiblePayload, &story) != nil ||
		len(story.SourceStructure) != len(cursor.Batch.SourceAnalysis.Units) {
		return domainError(
			"BATCH_COVERAGE_INVALID",
			"故事圣经没有完整聚合来源分析。",
		)
	}
	for index, sourceUnit := range cursor.Batch.SourceAnalysis.Units {
		if story.SourceStructure[index].SourceUnitID != sourceUnit.SourceUnitID {
			return domainError(
				"BATCH_COVERAGE_INVALID",
				"故事圣经的来源单元顺序或 ID 与来源分析不一致。",
			)
		}
	}
	return nil
}

func validateEpisodeSplitGlobalPlan(
	cursorPayload json.RawMessage,
	resultPayload json.RawMessage,
) (map[string]any, []episodeSplitSourceUnit, int, error) {
	var cursor struct {
		Batch struct {
			TargetEpisodeCount int                      `json:"target_episode_count"`
			SourceUnits        []episodeSplitSourceUnit `json:"source_units"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(cursorPayload, &cursor); err != nil ||
		cursor.Batch.TargetEpisodeCount <= 0 ||
		len(cursor.Batch.SourceUnits) == 0 {
		return nil, nil, 0, domainError(
			"RUN_CURSOR_INVALID",
			"全局拆集任务缺少来源单元或目标集数。",
		)
	}
	var result map[string]any
	if err := json.Unmarshal(resultPayload, &result); err != nil {
		return nil, nil, 0, domainError(
			"OUTPUT_SCHEMA_VALIDATION_FAILED",
			"全局拆集结果无法读取。",
		)
	}
	skeletons, ok := result["episode_skeletons"].([]any)
	if !ok || len(skeletons) != cursor.Batch.TargetEpisodeCount {
		return nil, nil, 0, domainError(
			"BATCH_COVERAGE_INVALID",
			"全局拆集骨架必须完整覆盖目标集数。",
		)
	}
	unitOrder := make(map[string]int, len(cursor.Batch.SourceUnits))
	for index, unit := range cursor.Batch.SourceUnits {
		unitOrder[unit.UnitID] = index + 1
	}
	previousUnitOrder := 0
	for index, raw := range skeletons {
		skeleton, ok := raw.(map[string]any)
		if !ok || integerValue(skeleton["episode_id"]) != index+1 {
			return nil, nil, 0, domainError(
				"BATCH_COVERAGE_INVALID",
				"全局拆集骨架的集号必须连续覆盖 1 到目标集数。",
			)
		}
		unitID, _ := skeleton["approx_end_unit_id"].(string)
		currentUnitOrder := unitOrder[unitID]
		if currentUnitOrder <= previousUnitOrder {
			return nil, nil, 0, domainError(
				"BATCH_COVERAGE_INVALID",
				"全局拆集骨架的来源边界必须存在且严格递增。",
			)
		}
		previousUnitOrder = currentUnitOrder
	}
	if previousUnitOrder != len(cursor.Batch.SourceUnits) {
		return nil, nil, 0, domainError(
			"BATCH_COVERAGE_INVALID",
			"全局拆集骨架最后一集必须覆盖到原文末单元。",
		)
	}
	return result, cursor.Batch.SourceUnits, cursor.Batch.TargetEpisodeCount, nil
}

type episodeSplitBoundaryCandidate struct {
	CandidateID     string `json:"candidate_id"`
	EpisodeID       int    `json:"episode_id"`
	UnitID          string `json:"end_unit_id"`
	UnitOrder       int    `json:"unit_order"`
	Offset          int    `json:"offset"`
	WindowChars     int    `json:"window_chars"`
	CutAfterAnchor  string `json:"cut_after_anchor"`
	CutBeforeAnchor string `json:"cut_before_anchor"`
}

func (s *Store) enqueueEpisodeSplitBatchTaskTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	stepRunID string,
	step capability.CompiledStep,
	inputSnapshot json.RawMessage,
	globalPlan map[string]any,
	sourceUnits []episodeSplitSourceUnit,
	targetCount int,
	startEpisode int,
	previousEndOrder int,
	now time.Time,
) error {
	batchSize := step.Batch.MaxItemsPerTask
	if batchSize <= 0 {
		batchSize = 1
	}
	endEpisode := startEpisode + batchSize - 1
	if endEpisode > targetCount {
		endEpisode = targetCount
	}
	batchIndex := (startEpisode-1)/batchSize + 1
	batchCount := (targetCount + batchSize - 1) / batchSize
	skeletons, _ := globalPlan["episode_skeletons"].([]any)
	candidates := make([]episodeSplitBoundaryCandidate, 0)
	maxCandidateOrder := previousEndOrder
	for episodeNo := startEpisode; episodeNo <= endEpisode; episodeNo++ {
		skeleton, _ := skeletons[episodeNo-1].(map[string]any)
		approxID, _ := skeleton["approx_end_unit_id"].(string)
		approxOrder := unitOrdinal(approxID)
		minOrder := previousEndOrder + (episodeNo - startEpisode) + 1
		maxOrder := len(sourceUnits) - (targetCount - episodeNo)
		candidateStart := approxOrder - 2
		candidateEnd := approxOrder + 2
		if candidateStart < minOrder {
			candidateStart = minOrder
		}
		if candidateEnd > maxOrder {
			candidateEnd = maxOrder
		}
		if episodeNo == targetCount {
			candidateStart = len(sourceUnits)
			candidateEnd = len(sourceUnits)
		}
		if candidateStart > candidateEnd || candidateStart <= 0 {
			return domainError(
				"BATCH_COVERAGE_INVALID",
				"全局骨架无法生成连续的合法边界候选。",
			)
		}
		for order := candidateStart; order <= candidateEnd; order++ {
			unit := sourceUnits[order-1]
			before := ""
			if order < len(sourceUnits) {
				before = sourceUnits[order].Text
			}
			candidates = append(candidates, episodeSplitBoundaryCandidate{
				CandidateID:     fmt.Sprintf("E%03d-%s", episodeNo, unit.UnitID),
				EpisodeID:       episodeNo,
				UnitID:          unit.UnitID,
				UnitOrder:       order,
				Offset:          unit.EndOffset,
				WindowChars:     unit.EndOffset - unit.StartOffset,
				CutAfterAnchor:  unit.Text,
				CutBeforeAnchor: before,
			})
			if order > maxCandidateOrder {
				maxCandidateOrder = order
			}
		}
	}
	relevantUnits := append(
		[]episodeSplitSourceUnit(nil),
		sourceUnits[previousEndOrder:maxCandidateOrder]...,
	)
	cursor := map[string]any{
		"batch": map[string]any{
			"phase":                   step.Batch.TaskStage.ID,
			"batch_index":             batchIndex,
			"batch_count":             batchCount,
			"episode_start":           startEpisode,
			"episode_end":             endEpisode,
			"target_episode_count":    targetCount,
			"previous_end_unit_id":    unitIDForOrder(previousEndOrder),
			"previous_end_unit_order": previousEndOrder,
			"global_plan":             globalPlan,
			"source_units":            relevantUnits,
			"candidates":              candidates,
		},
	}
	return s.insertBatchTaskTx(
		ctx,
		tx,
		run,
		stepRunID,
		step.ExecutorRef,
		fmt.Sprintf("episode:%d-%d", startEpisode, endEpisode),
		batchIndex+1,
		inputSnapshot,
		cursor,
		map[string]any{
			"phase":         step.Batch.TaskStage.ID,
			"batch_index":   batchIndex,
			"batch_count":   batchCount,
			"episode_start": startEpisode,
			"episode_end":   endEpisode,
		},
		now,
	)
}

func unitIDForOrder(order int) string {
	if order <= 0 {
		return ""
	}
	return fmt.Sprintf("U%06d", order)
}

func unitOrdinal(unitID string) int {
	var order int
	if _, err := fmt.Sscanf(unitID, "U%06d", &order); err != nil {
		return 0
	}
	return order
}

func normalizeInternalBatchResult(
	artifactType string,
	cursorPayload json.RawMessage,
	resultPayload json.RawMessage,
) (json.RawMessage, error) {
	if artifactType != "episode_split" {
		return nil, domainError(
			"OUTPUT_COMMIT_UNSUPPORTED",
			"当前内部 Batch Stage 尚未实现结果转换。",
		)
	}
	var cursor struct {
		Batch struct {
			EpisodeStart       int                             `json:"episode_start"`
			EpisodeEnd         int                             `json:"episode_end"`
			TargetEpisodeCount int                             `json:"target_episode_count"`
			PreviousEndOrder   int                             `json:"previous_end_unit_order"`
			GlobalPlan         map[string]any                  `json:"global_plan"`
			SourceUnits        []episodeSplitSourceUnit        `json:"source_units"`
			Candidates         []episodeSplitBoundaryCandidate `json:"candidates"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(cursorPayload, &cursor); err != nil ||
		cursor.Batch.EpisodeStart <= 0 ||
		cursor.Batch.EpisodeEnd < cursor.Batch.EpisodeStart ||
		cursor.Batch.TargetEpisodeCount < cursor.Batch.EpisodeEnd {
		return nil, domainError("RUN_CURSOR_INVALID", "拆集批次游标无效。")
	}
	var result struct {
		Episodes   []map[string]any `json:"episodes"`
		BatchRisks []string         `json:"batch_risks"`
	}
	if err := json.Unmarshal(resultPayload, &result); err != nil {
		return nil, err
	}
	expectedCount := cursor.Batch.EpisodeEnd - cursor.Batch.EpisodeStart + 1
	if len(result.Episodes) != expectedCount {
		return nil, domainError(
			"BATCH_COVERAGE_INVALID",
			"拆集批次结果没有完整覆盖当前批次集号。",
		)
	}
	candidateByID := make(map[string]episodeSplitBoundaryCandidate)
	for _, candidate := range cursor.Batch.Candidates {
		candidateByID[candidate.CandidateID] = candidate
	}
	unitByOrder := make(map[int]episodeSplitSourceUnit)
	for _, unit := range cursor.Batch.SourceUnits {
		unitByOrder[unitOrdinal(unit.UnitID)] = unit
	}
	previousEndOrder := cursor.Batch.PreviousEndOrder
	normalizedEpisodes := make([]any, 0, len(result.Episodes))
	coveredRanges := make([]string, 0, len(result.Episodes))
	groundedTrace := make([]any, 0, len(result.Episodes))
	for index, rawEpisode := range result.Episodes {
		episodeNo := integerValue(rawEpisode["episode_id"])
		if episodeNo != cursor.Batch.EpisodeStart+index {
			return nil, domainError(
				"BATCH_COVERAGE_INVALID",
				"拆集批次结果的集号必须按顺序完整覆盖当前批次。",
			)
		}
		candidateID, _ := rawEpisode["end_candidate_id"].(string)
		candidate, ok := candidateByID[candidateID]
		if !ok || candidate.EpisodeID != episodeNo ||
			candidate.UnitOrder <= previousEndOrder {
			return nil, domainError(
				"BATCH_COVERAGE_INVALID",
				"拆集批次选择了不存在、错集或逆序的来源边界。",
			)
		}
		sourceRefs, err := sourceRefsForUnitRange(
			unitByOrder,
			previousEndOrder+1,
			candidate.UnitOrder,
		)
		if err != nil {
			return nil, err
		}
		rangeLabel := fmt.Sprintf(
			"%s-%s",
			unitIDForOrder(previousEndOrder+1),
			unitIDForOrder(candidate.UnitOrder),
		)
		coveredRanges = append(coveredRanges, rangeLabel)
		normalizedEpisodes = append(normalizedEpisodes, map[string]any{
			"episode_no":      episodeNo,
			"source_refs":     sourceRefs,
			"source_summary":  stringValue(rawEpisode["source_summary"]),
			"core_event":      stringValue(rawEpisode["core_event"]),
			"character_turn":  stringValue(rawEpisode["character_turn"]),
			"boundary_reason": stringValue(rawEpisode["boundary_reason"]),
			"boundary_check": map[string]any{
				"previous_episode_end":   unitIDForOrder(previousEndOrder),
				"next_episode_start":     unitIDForOrder(candidate.UnitOrder + 1),
				"cut_after_anchor":       candidate.CutAfterAnchor,
				"cut_before_anchor":      candidate.CutBeforeAnchor,
				"continuity_risk":        stringValue(rawEpisode["pacing_risk"]),
				"manual_review_required": boolValue(rawEpisode["requires_user_attention"]),
			},
			"hook_strength":           stringValue(rawEpisode["hook_strength"]),
			"hook_type":               stringValue(rawEpisode["hook_type"]),
			"information_density":     stringValue(rawEpisode["information_density"]),
			"pacing_risk":             stringValue(rawEpisode["pacing_risk"]),
			"split_confidence":        stringValue(rawEpisode["split_confidence"]),
			"requires_user_attention": boolValue(rawEpisode["requires_user_attention"]),
			"adaptation_added":        false,
		})
		groundedTrace = append(groundedTrace, map[string]any{
			"claim":       fmt.Sprintf("第%d集来源边界由 Runtime 候选确定。", episodeNo),
			"source_refs": sourceRefs,
		})
		previousEndOrder = candidate.UnitOrder
	}
	if cursor.Batch.EpisodeEnd == cursor.Batch.TargetEpisodeCount &&
		previousEndOrder != maxSourceUnitOrder(unitByOrder) {
		return nil, domainError(
			"BATCH_COVERAGE_INVALID",
			"最后一集没有覆盖到来源材料末单元。",
		)
	}
	globalRisks := stringSlice(cursor.Batch.GlobalPlan["global_risks"])
	globalRisks = appendUniqueStrings(globalRisks, result.BatchRisks...)
	return json.Marshal(map[string]any{
		"target_episode_count": cursor.Batch.TargetEpisodeCount,
		"actual_episode_count": len(normalizedEpisodes),
		"split_strategy":       "全局骨架、合法边界候选与串行批次定界。",
		"episodes":             normalizedEpisodes,
		"coverage_check": map[string]any{
			"covered_ranges":    coveredRanges,
			"missing_ranges":    []string{},
			"duplicated_ranges": []string{},
			"order_issues":      []string{},
		},
		"global_risks": globalRisks,
		"source_trace": map[string]any{
			"grounded": groundedTrace,
			"inferred": []any{},
		},
	})
}

func sourceRefsForUnitRange(
	unitByOrder map[int]episodeSplitSourceUnit,
	start int,
	end int,
) ([]any, error) {
	result := []any{}
	groupStart := start
	var current episodeSplitSourceUnit
	for order := start; order <= end; order++ {
		unit, ok := unitByOrder[order]
		if !ok {
			return nil, domainError(
				"BATCH_COVERAGE_INVALID",
				"拆集候选缺少连续的来源单元。",
			)
		}
		if order == start {
			current = unit
			continue
		}
		if unit.AssetID != current.AssetID ||
			unit.AssetSnapshotID != current.AssetSnapshotID {
			result = append(result, sourceRefForUnitGroup(
				current,
				unitByOrder[order-1],
				groupStart,
				order-1,
			))
			groupStart = order
			current = unit
		}
	}
	if current.UnitID != "" {
		result = append(result, sourceRefForUnitGroup(
			current,
			unitByOrder[end],
			groupStart,
			end,
		))
	}
	return result, nil
}

func sourceRefForUnitGroup(
	startUnit episodeSplitSourceUnit,
	endUnit episodeSplitSourceUnit,
	start int,
	end int,
) map[string]any {
	return map[string]any{
		"source_type":       "asset_text_range",
		"asset_id":          startUnit.AssetID,
		"asset_snapshot_id": startUnit.AssetSnapshotID,
		"source_unit_id":    endUnit.SourceUnitID,
		"range_label": fmt.Sprintf(
			"%s-%s",
			unitIDForOrder(start),
			unitIDForOrder(end),
		),
	}
}

func maxSourceUnitOrder(units map[int]episodeSplitSourceUnit) int {
	maximum := 0
	for order := range units {
		if order > maximum {
			maximum = order
		}
	}
	return maximum
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	result := make([]string, 0, len(values)+len(additions))
	for _, value := range append(values, additions...) {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func (s *Store) enqueueNextEpisodeSplitBatchTx(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	projectID string,
	stepRunID string,
	step capability.CompiledStep,
	taskItemID string,
	payload json.RawMessage,
	now time.Time,
) error {
	var cursor struct {
		Batch struct {
			EpisodeEnd         int `json:"episode_end"`
			TargetEpisodeCount int `json:"target_episode_count"`
		} `json:"batch"`
	}
	var inputSnapshotJSON string
	var cursorJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT cursor_json, input_snapshot_json
		FROM task_items WHERE task_item_id = ?`,
		taskItemID,
	).Scan(&cursorJSON, &inputSnapshotJSON); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(cursorJSON), &cursor); err != nil {
		return err
	}
	if cursor.Batch.EpisodeEnd >= cursor.Batch.TargetEpisodeCount {
		return nil
	}
	var normalized struct {
		Episodes []struct {
			SourceRefs []struct {
				RangeLabel string `json:"range_label"`
			} `json:"source_refs"`
		} `json:"episodes"`
	}
	if err := json.Unmarshal(payload, &normalized); err != nil ||
		len(normalized.Episodes) == 0 {
		return domainError("BATCH_COVERAGE_INVALID", "拆集批次结果缺少来源范围。")
	}
	previousEndOrder := 0
	for _, ref := range normalized.Episodes[len(normalized.Episodes)-1].SourceRefs {
		parts := strings.Split(ref.RangeLabel, "-")
		if len(parts) == 2 && unitOrdinal(parts[1]) > previousEndOrder {
			previousEndOrder = unitOrdinal(parts[1])
		}
	}
	if previousEndOrder == 0 {
		return domainError("BATCH_COVERAGE_INVALID", "拆集批次结果无法定位结束单元。")
	}
	var preparationCursorJSON, globalPayloadJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT ti.cursor_json, trc.payload_json
		FROM task_items ti
		JOIN task_result_checkpoints trc ON trc.task_item_id = ti.task_item_id
		WHERE ti.step_run_id = ? AND ti.item_key = ?
		LIMIT 1`,
		stepRunID,
		"preparation:"+step.Batch.Preparation.ID,
	).Scan(&preparationCursorJSON, &globalPayloadJSON); err != nil {
		return err
	}
	globalPlan, sourceUnits, targetCount, err := validateEpisodeSplitGlobalPlan(
		json.RawMessage(preparationCursorJSON),
		json.RawMessage(globalPayloadJSON),
	)
	if err != nil {
		return err
	}
	run, err := getRunTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	if run.ProjectID != projectID {
		return domainError("CONTEXT_PROJECT_MISMATCH", "拆集批次不属于当前作品。")
	}
	return s.enqueueEpisodeSplitBatchTaskTx(
		ctx,
		tx,
		run,
		stepRunID,
		step,
		json.RawMessage(inputSnapshotJSON),
		globalPlan,
		sourceUnits,
		targetCount,
		cursor.Batch.EpisodeEnd+1,
		previousEndOrder,
		now,
	)
}

func taskCheckpointForAttemptTx(
	ctx context.Context,
	tx *sql.Tx,
	attemptID string,
) (*TaskResultCheckpoint, error) {
	var checkpoint TaskResultCheckpoint
	var payload, createdAt string
	err := tx.QueryRowContext(ctx, `
		SELECT task_result_checkpoint_id, task_item_id, attempt_id, step_run_id,
			run_id, item_key, item_order, artifact_type, payload_json,
			payload_hash, created_at
		FROM task_result_checkpoints WHERE attempt_id = ?`,
		attemptID,
	).Scan(
		&checkpoint.TaskResultCheckpointID,
		&checkpoint.TaskItemID,
		&checkpoint.AttemptID,
		&checkpoint.StepRunID,
		&checkpoint.RunID,
		&checkpoint.ItemKey,
		&checkpoint.ItemOrder,
		&checkpoint.ArtifactType,
		&payload,
		&checkpoint.PayloadHash,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domainError(
			"RUN_STATE_CONFLICT",
			"执行尝试已完成但缺少 Task Checkpoint。",
		)
	}
	if err != nil {
		return nil, err
	}
	checkpoint.Payload = json.RawMessage(payload)
	checkpoint.CreatedAt, err = parseTime(createdAt)
	return &checkpoint, err
}

type batchCheckpointPayload struct {
	ItemKey   string
	Cursor    json.RawMessage
	Payload   map[string]any
	ItemOrder int
}

func mergeBatchCheckpointsTx(
	ctx context.Context,
	tx *sql.Tx,
	stepRunID string,
	artifactType string,
) (json.RawMessage, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT trc.item_key, trc.item_order, ti.cursor_json, trc.payload_json
		FROM task_result_checkpoints trc
		JOIN task_items ti ON ti.task_item_id = trc.task_item_id
		WHERE trc.step_run_id = ?
		ORDER BY trc.item_order ASC, trc.task_result_checkpoint_id ASC`,
		stepRunID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var checkpoints []batchCheckpointPayload
	for rows.Next() {
		var item batchCheckpointPayload
		var cursorJSON, payloadJSON string
		if err := rows.Scan(
			&item.ItemKey,
			&item.ItemOrder,
			&cursorJSON,
			&payloadJSON,
		); err != nil {
			return nil, err
		}
		item.Cursor = json.RawMessage(cursorJSON)
		var cursor struct {
			Batch struct {
				EpisodeStart int `json:"episode_start"`
			} `json:"batch"`
		}
		if json.Unmarshal(item.Cursor, &cursor) == nil &&
			cursor.Batch.EpisodeStart == 0 {
			continue
		}
		if err := json.Unmarshal([]byte(payloadJSON), &item.Payload); err != nil {
			return nil, err
		}
		checkpoints = append(checkpoints, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(checkpoints) == 0 {
		return nil, domainError(
			"BATCH_MERGE_INCOMPLETE",
			"批量步骤没有可合并的 Task Checkpoint。",
		)
	}
	switch artifactType {
	case "episode_split":
		return mergeEpisodeSplitCheckpoints(checkpoints)
	case "episode_cards":
		return mergeEpisodeCardCheckpoints(checkpoints)
	default:
		return nil, domainError(
			"OUTPUT_COMMIT_UNSUPPORTED",
			"当前产物类型尚未实现确定性批量合并。",
		)
	}
}

func mergeEpisodeSplitCheckpoints(
	checkpoints []batchCheckpointPayload,
) (json.RawMessage, error) {
	base := cloneJSONMap(checkpoints[0].Payload)
	episodes, targetCount, err := collectBatchEpisodes(checkpoints, true)
	if err != nil {
		return nil, err
	}
	base["target_episode_count"] = targetCount
	base["actual_episode_count"] = len(episodes)
	base["episodes"] = episodes
	base["global_risks"] = mergeStringLists(checkpoints, "global_risks")
	base["coverage_check"] = mergeNamedStringListObject(
		checkpoints,
		"coverage_check",
		[]string{
			"covered_ranges",
			"missing_ranges",
			"duplicated_ranges",
			"order_issues",
		},
	)
	base["source_trace"] = mergeNamedArrayObject(
		checkpoints,
		"source_trace",
		[]string{"grounded", "inferred"},
	)
	return json.Marshal(base)
}

func mergeEpisodeCardCheckpoints(
	checkpoints []batchCheckpointPayload,
) (json.RawMessage, error) {
	base := cloneJSONMap(checkpoints[0].Payload)
	episodes, _, err := collectBatchEpisodes(checkpoints, false)
	if err != nil {
		return nil, err
	}
	base["episodes"] = episodes
	base["continuity_delta"] = mergeNamedStringListObject(
		checkpoints,
		"continuity_delta",
		[]string{
			"new_facts",
			"character_state_changes",
			"relationship_changes",
			"hooks_opened",
			"hooks_resolved",
		},
	)
	return json.Marshal(base)
}

func collectBatchEpisodes(
	checkpoints []batchCheckpointPayload,
	enforceSourceCoverage bool,
) ([]any, int, error) {
	byEpisode := map[int]any{}
	targetCount := 0
	expectedSourceStart := 1
	for _, checkpoint := range checkpoints {
		var cursor struct {
			Batch struct {
				EpisodeStart       int `json:"episode_start"`
				EpisodeEnd         int `json:"episode_end"`
				TargetEpisodeCount int `json:"target_episode_count"`
			} `json:"batch"`
		}
		if err := json.Unmarshal(checkpoint.Cursor, &cursor); err != nil ||
			cursor.Batch.EpisodeStart <= 0 ||
			cursor.Batch.EpisodeEnd < cursor.Batch.EpisodeStart ||
			cursor.Batch.TargetEpisodeCount <= 0 {
			return nil, 0, domainError(
				"BATCH_MERGE_INCOMPLETE",
				"批量 Task 缺少有效的集数范围。",
			)
		}
		if targetCount == 0 {
			targetCount = cursor.Batch.TargetEpisodeCount
		} else if targetCount != cursor.Batch.TargetEpisodeCount {
			return nil, 0, domainError(
				"BATCH_MERGE_INCOMPLETE",
				"批量 Task 的目标集数不一致。",
			)
		}
		rawEpisodes, ok := checkpoint.Payload["episodes"].([]any)
		if !ok {
			return nil, 0, domainError(
				"BATCH_MERGE_INCOMPLETE",
				"批量 Task 结果缺少 episodes。",
			)
		}
		for _, rawEpisode := range rawEpisodes {
			episode, ok := rawEpisode.(map[string]any)
			if !ok {
				return nil, 0, domainError(
					"BATCH_MERGE_INCOMPLETE",
					"批量 Task 的单集结果格式无效。",
				)
			}
			episodeNo := integerValue(episode["episode_no"])
			if episodeNo < cursor.Batch.EpisodeStart ||
				episodeNo > cursor.Batch.EpisodeEnd {
				return nil, 0, domainError(
					"BATCH_COVERAGE_INVALID",
					"批量 Task 返回了范围之外的集号。",
				)
			}
			if _, duplicate := byEpisode[episodeNo]; duplicate {
				return nil, 0, domainError(
					"BATCH_COVERAGE_INVALID",
					"批量 Task 返回了重复集号。",
				)
			}
			if enforceSourceCoverage {
				startOrder, endOrder := episodeSourceUnitRange(episode)
				if startOrder != expectedSourceStart || endOrder < startOrder {
					return nil, 0, domainError(
						"BATCH_COVERAGE_INVALID",
						"拆集来源范围存在缺失、重复或顺序问题。",
					)
				}
				expectedSourceStart = endOrder + 1
			}
			byEpisode[episodeNo] = episode
		}
	}
	if len(byEpisode) != targetCount {
		return nil, 0, domainError(
			"BATCH_COVERAGE_INVALID",
			"批量 Task 未完整覆盖目标集数。",
		)
	}
	episodeNumbers := make([]int, 0, len(byEpisode))
	for episodeNo := range byEpisode {
		episodeNumbers = append(episodeNumbers, episodeNo)
	}
	sort.Ints(episodeNumbers)
	episodes := make([]any, 0, len(episodeNumbers))
	for expected, episodeNo := range episodeNumbers {
		if episodeNo != expected+1 {
			return nil, 0, domainError(
				"BATCH_COVERAGE_INVALID",
				"批量 Task 的集号必须连续覆盖 1 到目标集数。",
			)
		}
		episodes = append(episodes, byEpisode[episodeNo])
	}
	return episodes, targetCount, nil
}

func episodeSourceUnitRange(episode map[string]any) (int, int) {
	refs, _ := episode["source_refs"].([]any)
	startOrder, endOrder := 0, 0
	for _, rawRef := range refs {
		ref, _ := rawRef.(map[string]any)
		label, _ := ref["range_label"].(string)
		parts := strings.Split(label, "-")
		if len(parts) != 2 {
			continue
		}
		start := unitOrdinal(parts[0])
		end := unitOrdinal(parts[1])
		if startOrder == 0 || start < startOrder {
			startOrder = start
		}
		if end > endOrder {
			endOrder = end
		}
	}
	return startOrder, endOrder
}

func integerValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case json.Number:
		result, _ := typed.Int64()
		return int(result)
	case int:
		return typed
	default:
		return 0
	}
}

func mergeStringLists(
	checkpoints []batchCheckpointPayload,
	key string,
) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, checkpoint := range checkpoints {
		for _, value := range stringSlice(checkpoint.Payload[key]) {
			if !seen[value] {
				seen[value] = true
				result = append(result, value)
			}
		}
	}
	return result
}

func mergeNamedStringListObject(
	checkpoints []batchCheckpointPayload,
	objectKey string,
	keys []string,
) map[string]any {
	result := map[string]any{}
	for _, key := range keys {
		seen := map[string]bool{}
		values := []string{}
		for _, checkpoint := range checkpoints {
			object, _ := checkpoint.Payload[objectKey].(map[string]any)
			for _, value := range stringSlice(object[key]) {
				if !seen[value] {
					seen[value] = true
					values = append(values, value)
				}
			}
		}
		result[key] = values
	}
	return result
}

func mergeNamedArrayObject(
	checkpoints []batchCheckpointPayload,
	objectKey string,
	keys []string,
) map[string]any {
	result := map[string]any{}
	for _, key := range keys {
		values := []any{}
		for _, checkpoint := range checkpoints {
			object, _ := checkpoint.Payload[objectKey].(map[string]any)
			items, _ := object[key].([]any)
			values = append(values, items...)
		}
		result[key] = values
	}
	return result
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}
