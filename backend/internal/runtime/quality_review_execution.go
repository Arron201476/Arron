package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"content-agent/backend/internal/capability"
)

const qualityReviewBatchSize = 5

type qualityReviewCursor struct {
	Review struct {
		Phase             string `json:"phase"`
		QualityReviewID   string `json:"quality_review_id"`
		InputSnapshotHash string `json:"input_snapshot_hash"`
		EpisodeStart      int    `json:"episode_start,omitempty"`
		EpisodeEnd        int    `json:"episode_end,omitempty"`
	} `json:"review"`
}

func parseQualityReviewCursor(payload json.RawMessage) (qualityReviewCursor, error) {
	var cursor qualityReviewCursor
	if err := json.Unmarshal(payload, &cursor); err != nil ||
		(cursor.Review.Phase != "episode_batch" && cursor.Review.Phase != "global") ||
		cursor.Review.QualityReviewID == "" || cursor.Review.InputSnapshotHash == "" {
		return qualityReviewCursor{}, domainError("RUN_CURSOR_INVALID", "质量审核任务游标无效。")
	}
	if cursor.Review.Phase == "episode_batch" &&
		(cursor.Review.EpisodeStart <= 0 || cursor.Review.EpisodeEnd < cursor.Review.EpisodeStart) {
		return qualityReviewCursor{}, domainError("RUN_CURSOR_INVALID", "质量审核分集范围无效。")
	}
	return cursor, nil
}

func (s *Store) planQualityReviewTasksTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	stepRunID string,
	step capability.CompiledStep,
	stepInput json.RawMessage,
	now time.Time,
) error {
	if !isScriptQualityReviewStep(step) ||
		step.Batch == nil || step.Batch.Execution != "parallel" ||
		step.Batch.MaxItemsPerTask <= 0 || step.Batch.MaxItemsPerTask > qualityReviewBatchSize {
		return domainError("CAPABILITY_VERSION_UNAVAILABLE", "剧本质量审核批次合同无效。")
	}
	configPayload, err := stepConfigPayloadTx(ctx, tx, run, step)
	if err != nil {
		return err
	}
	targetCount, err := targetEpisodeCount(configPayload)
	if err != nil {
		return err
	}
	var inputs []struct {
		ArtifactID        string `json:"artifact_id"`
		ArtifactVersionID string `json:"artifact_version_id"`
		Status            string `json:"status"`
		ScopeKey          string `json:"scope_key"`
	}
	if err := json.Unmarshal(stepInput, &inputs); err != nil || len(inputs) == 0 {
		return domainError("CONTEXT_REQUIRED_UPSTREAM_MISSING", "质量审核缺少已确认剧本输入。")
	}
	coverage := map[string]map[int]bool{
		"script_unit": {}, "script_handoff": {}, "script_context": {},
	}
	for _, input := range inputs {
		var artifactType, scopeKey, versionStatus string
		err := tx.QueryRowContext(ctx, `
			SELECT a.artifact_type, a.scope_key, av.status
			FROM artifacts a
			JOIN artifact_versions av ON av.artifact_id = a.artifact_id
			WHERE a.artifact_id = ? AND av.artifact_version_id = ?
				AND a.project_id = ? AND a.run_id = ?`,
			input.ArtifactID, input.ArtifactVersionID, run.ProjectID, run.RunID,
		).Scan(&artifactType, &scopeKey, &versionStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return domainError("CONTEXT_REQUIRED_UPSTREAM_MISSING", "质量审核输入版本不存在。")
		}
		if err != nil {
			return err
		}
		if input.Status != "confirmed" ||
			(versionStatus != "confirmed" && versionStatus != "superseded") {
			return domainError("CONTEXT_REQUIRED_UPSTREAM_MISSING", "质量审核输入版本尚未确认。")
		}
		byEpisode, ok := coverage[artifactType]
		if !ok {
			return domainError("CONTEXT_LINEAGE_CONFLICT", "质量审核包含未声明的输入类型。")
		}
		episodeNo, err := episodeNumberFromScope(scopeKey)
		if err != nil || episodeNo > targetCount || byEpisode[episodeNo] {
			return domainError("DEPENDENCY_INCOMPLETE", "质量审核输入不是完整、连续且唯一的分集版本。")
		}
		byEpisode[episodeNo] = true
	}
	for artifactType, byEpisode := range coverage {
		for episodeNo := 1; episodeNo <= targetCount; episodeNo++ {
			if !byEpisode[episodeNo] {
				return domainError("DEPENDENCY_INCOMPLETE", fmt.Sprintf("质量审核缺少第 %d 集的 %s。", episodeNo, artifactType))
			}
		}
	}
	inputSnapshot, err := json.Marshal(map[string]any{
		"run_input_snapshot_version_id": run.CurrentInputSnapshotVersionID,
		"step_input_versions":           json.RawMessage(stepInput),
	})
	if err != nil {
		return err
	}
	inputHash := sha256Hex([]byte(run.CurrentInputSnapshotVersionID + "\n" + string(stepInput) + "\n" + run.CapabilityVersion))
	review, err := s.startQualityReviewTx(ctx, tx, run.ProjectID, StartQualityReviewCommand{
		RunID: run.RunID, StepRunID: stepRunID, InputSnapshotHash: inputHash, Scope: "full_script",
	}, now)
	if err != nil {
		return err
	}
	batchSize := step.Batch.MaxItemsPerTask
	order := 1
	for start := 1; start <= targetCount; start += batchSize {
		end := start + batchSize - 1
		if end > targetCount {
			end = targetCount
		}
		cursor := map[string]any{"review": map[string]any{
			"phase": "episode_batch", "quality_review_id": review.QualityReviewID,
			"input_snapshot_hash": inputHash, "episode_start": start, "episode_end": end,
		}}
		if err := s.insertBatchTaskTx(ctx, tx, run, stepRunID, step.ExecutorRef,
			fmt.Sprintf("episode:%d-%d", start, end), order, inputSnapshot, cursor,
			map[string]any{"phase": "episode_batch", "episode_start": start, "episode_end": end}, now); err != nil {
			return err
		}
		order++
	}
	globalCursor, err := json.Marshal(map[string]any{"review": map[string]any{
		"phase": "global", "quality_review_id": review.QualityReviewID,
		"input_snapshot_hash": inputHash,
	}})
	if err != nil {
		return err
	}
	globalTaskID := s.newID("tsk")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO task_items(
			task_item_id, step_run_id, run_id, item_key, item_order, status,
			attempt_count, input_snapshot_json, cursor_json, created_at, updated_at
		) VALUES(?, ?, ?, 'review:global', ?, 'blocked', 0, ?, ?, ?, ?)`,
		globalTaskID, stepRunID, run.RunID, order, string(inputSnapshot), string(globalCursor),
		formatTime(now), formatTime(now)); err != nil {
		return err
	}
	runRef, stepRef := run.RunID, stepRunID
	_, err = s.appendEvent(ctx, tx, run.ProjectID, &runRef, &stepRef,
		"task.blocked", "task_item", globalTaskID, map[string]any{"phase": "global", "waiting_for": "episode_batches"})
	return err
}

func (s *Store) qualityReviewBatchCheckpointsTx(
	ctx context.Context,
	tx *sql.Tx,
	stepRunID string,
) ([]ContextUpstreamArtifact, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT task_result_checkpoint_id, item_key, payload_json, payload_hash
		FROM task_result_checkpoints
		WHERE step_run_id = ? AND artifact_type = 'quality_review_batch'
		ORDER BY item_order ASC, task_result_checkpoint_id ASC`, stepRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ContextUpstreamArtifact
	for rows.Next() {
		var checkpointID, itemKey, payload, hash string
		if err := rows.Scan(&checkpointID, &itemKey, &payload, &hash); err != nil {
			return nil, err
		}
		result = append(result, ContextUpstreamArtifact{
			ArtifactID: checkpointID, ArtifactVersionID: checkpointID,
			ArtifactType: "quality_review_batch", ScopeKey: itemKey, Status: "confirmed",
			SelectionPolicy: "review_checkpoint", Content: json.RawMessage(payload), ContentHash: hash,
		})
	}
	return result, rows.Err()
}

func validateQualityReviewBatchResult(cursor qualityReviewCursor, payload json.RawMessage) error {
	var result struct {
		InputSnapshotHash string `json:"input_snapshot_hash"`
		EpisodeStart      int    `json:"episode_start"`
		EpisodeEnd        int    `json:"episode_end"`
	}
	if err := json.Unmarshal(payload, &result); err != nil ||
		result.InputSnapshotHash != cursor.Review.InputSnapshotHash ||
		result.EpisodeStart != cursor.Review.EpisodeStart ||
		result.EpisodeEnd != cursor.Review.EpisodeEnd {
		return domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "质量审核批次结果与输入快照或分集范围不一致。")
	}
	return nil
}

func (s *Store) commitQualityReviewTaskTx(
	ctx context.Context,
	tx *sql.Tx,
	attemptID, taskItemID, runID, projectID, stepRunID string,
	step capability.CompiledStep,
	contextPack StepExecutionContextPack,
	payload json.RawMessage,
	now time.Time,
) (ArtifactCommitResult, error) {
	cursor, err := parseQualityReviewCursor(contextPack.TaskCursor)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if contextPack.Intent.Operation != qualityReviewOperation ||
		contextPack.ProviderResultContract == nil ||
		contextPack.OutputContract.SchemaRef != contextPack.ProviderResultContract.SchemaRef {
		return ArtifactCommitResult{}, domainError("CONTEXT_LINEAGE_CONFLICT", "质量审核输出合同与任务阶段不一致。")
	}
	if err := validateEmbeddedJSONSchema(contextPack.OutputContract.Schema, payload); err != nil {
		return ArtifactCommitResult{}, domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", fmt.Sprintf("质量审核输出未通过合同校验：%v", err))
	}
	if cursor.Review.Phase == "episode_batch" {
		if contextPack.OutputContract.ArtifactType != "task_checkpoint:quality_review_batch" {
			return ArtifactCommitResult{}, domainError("CONTEXT_LINEAGE_CONFLICT", "质量审核批次输出类型不一致。")
		}
		if err := validateQualityReviewBatchResult(cursor, payload); err != nil {
			return ArtifactCommitResult{}, err
		}
		checkpoint, err := s.saveTaskResultCheckpointTx(ctx, tx, attemptID, taskItemID,
			stepRunID, runID, projectID, "quality_review_batch", payload, now)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		var remaining int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM task_items
			WHERE step_run_id = ? AND item_key LIKE 'episode:%' AND status != 'succeeded'`,
			stepRunID).Scan(&remaining); err != nil {
			return ArtifactCommitResult{}, err
		}
		if remaining == 0 {
			if err := updateExactlyOne(ctx, tx, `
				UPDATE task_items SET status = 'pending', updated_at = ?
				WHERE step_run_id = ? AND item_key = 'review:global' AND status = 'blocked'`,
				"质量审核全局任务状态已经变化。", formatTime(now), stepRunID); err != nil {
				return ArtifactCommitResult{}, err
			}
		}
		snapshot, err := s.getRunSnapshotTx(ctx, tx, runID)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		return ArtifactCommitResult{CommitStatus: "task_checkpointed", TaskCheckpoint: checkpoint, RunSnapshot: snapshot}, nil
	}
	if contextPack.OutputContract.ArtifactType != "task_checkpoint:quality_review_global" {
		return ArtifactCommitResult{}, domainError("CONTEXT_LINEAGE_CONFLICT", "质量审核全局输出类型不一致。")
	}
	var resultHash struct {
		InputSnapshotHash string `json:"input_snapshot_hash"`
	}
	if json.Unmarshal(payload, &resultHash) != nil || resultHash.InputSnapshotHash != cursor.Review.InputSnapshotHash {
		return ArtifactCommitResult{}, domainError("QUALITY_REVIEW_INPUT_CHANGED", "质量审核全局结果对应的输入快照已经变化。")
	}
	review, err := s.completeQualityReviewTx(ctx, tx, CompleteQualityReviewCommand{
		QualityReviewID:   cursor.Review.QualityReviewID,
		InputSnapshotHash: cursor.Review.InputSnapshotHash,
		Result:            payload,
	}, now)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	checkpoint, err := s.saveTaskResultCheckpointTx(ctx, tx, attemptID, taskItemID,
		stepRunID, runID, projectID, "quality_review_global", payload, now)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if review.Status == "passed" {
		if err := s.advancePassedQualityReviewTx(ctx, tx, runID, stepRunID, step, now); err != nil {
			return ArtifactCommitResult{}, err
		}
	} else {
		if err := s.requestQualityReviewApprovalTx(ctx, tx, review, step, now); err != nil {
			return ArtifactCommitResult{}, err
		}
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, runID)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	return ArtifactCommitResult{CommitStatus: "task_checkpointed", TaskCheckpoint: checkpoint, RunSnapshot: snapshot}, nil
}

func (s *Store) advancePassedQualityReviewTx(
	ctx context.Context,
	tx *sql.Tx,
	runID, reviewStepRunID string,
	reviewStep capability.CompiledStep,
	now time.Time,
	transitionFacts ...string,
) error {
	run, err := getRunTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	entry, ok, registryErr := s.capabilityEntryForRunQuery(
		ctx, tx, run.RunID, run.CapabilityID,
	)
	if registryErr != nil {
		return registryErr
	}
	if !ok || entry.Definition == nil || len(reviewStep.Next) != 1 {
		return domainError("CAPABILITY_VERSION_UNAVAILABLE", "质量审核后续聚合步骤不可用。")
	}
	waitForContinue := reviewStep.Next[0].When == "user_continue" && !containsString(transitionFacts, "user_continue")
	var nextStepID string
	if waitForContinue {
		nextStepID, err = selectWorkflowTransition(reviewStep, "user_continue")
	} else {
		nextStepID, err = completedWorkflowTransitionTx(ctx, tx, reviewStepRunID, reviewStep, transitionFacts...)
	}
	if err != nil {
		return err
	}
	var aggregate *capability.CompiledStep
	for index := range entry.Definition.Steps {
		if entry.Definition.Steps[index].ID == nextStepID {
			aggregate = &entry.Definition.Steps[index]
			break
		}
	}
	if aggregate == nil || aggregate.ExecutorRef != "runtime.aggregate_scripts" {
		return domainError("CAPABILITY_VERSION_UNAVAILABLE", "质量审核后续聚合步骤合同无效。")
	}
	var reviewInput string
	if err := tx.QueryRowContext(ctx, `SELECT input_version_snapshot_json FROM step_runs WHERE step_run_id = ?`,
		reviewStepRunID).Scan(&reviewInput); err != nil {
		return err
	}
	var allInputs []map[string]any
	if err := json.Unmarshal([]byte(reviewInput), &allInputs); err != nil {
		return err
	}
	var scriptInputs []map[string]any
	for _, input := range allInputs {
		artifactID, _ := input["artifact_id"].(string)
		if artifactID == "" {
			continue
		}
		var artifactType string
		if err := tx.QueryRowContext(ctx, `SELECT artifact_type FROM artifacts WHERE artifact_id = ?`, artifactID).Scan(&artifactType); err != nil {
			return err
		}
		if artifactType == "script_unit" {
			scriptInputs = append(scriptInputs, input)
		}
	}
	sort.Slice(scriptInputs, func(i, j int) bool {
		left, _ := scriptInputs[i]["scope_key"].(string)
		right, _ := scriptInputs[j]["scope_key"].(string)
		li, _ := episodeNumberFromScope(left)
		ri, _ := episodeNumberFromScope(right)
		return li < ri
	})
	inputJSON, err := json.Marshal(scriptInputs)
	if err != nil {
		return err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE step_runs SET status = 'completed', ended_at = ? WHERE step_run_id = ? AND status = 'running'`,
		"质量审核步骤已经变化。", formatTime(now), reviewStepRunID); err != nil {
		return err
	}
	if waitForContinue {
		versions := make([]string, 0, len(allInputs))
		for _, input := range allInputs {
			id, _ := input["artifact_version_id"].(string)
			versions = append(versions, id)
		}
		return s.requestWorkflowContinueTx(ctx, tx, run, reviewStepRunID, reviewStep, versions, now)
	}
	aggregateStepRunID := s.newID("step")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO step_runs(step_run_id, run_id, step_id, status, attempt_count, approval_policy,
			input_version_snapshot_json, task_cursor_json)
		VALUES(?, ?, ?, 'pending', 0, ?, ?, '{}')`,
		aggregateStepRunID, runID, aggregate.ID, aggregate.Approval.Type, string(inputJSON)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'paused', current_step_run_id = ?, updated_at = ? WHERE run_id = ? AND status IN ('running','waiting_approval')`,
		aggregateStepRunID, formatTime(now), runID); err != nil {
		return err
	}
	_, err = s.executeRuntimeStepForResumeTx(
		ctx, tx, run, entry.Definition, aggregateStepRunID, *aggregate, string(inputJSON), now,
	)
	return err
}

func (s *Store) requestQualityReviewApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	review QualityReview,
	step capability.CompiledStep,
	now time.Time,
) error {
	optionsJSON, err := json.Marshal(step.Approval.AllowedActions)
	if err != nil {
		return err
	}
	approvalID := s.newID("apr")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO approvals(
			approval_request_id, project_id, run_id, step_run_id, scope, status, version,
			title, reason, options_json, subject_kind, subject_ref_id, subject_version,
			subject_snapshot_hash, requested_at
		) VALUES(?, ?, ?, ?, 'quality_review', 'pending', 1, ?, ?, ?,
			'quality_review', ?, ?, ?, ?)`,
		approvalID, review.ProjectID, review.RunID, review.StepRunID,
		"剧本质量审核需要处理", "审核发现会影响后续生产的问题，请确认处理方式。",
		string(optionsJSON), review.QualityReviewID, review.ReviewVersion,
		review.InputSnapshotHash, formatTime(now)); err != nil {
		return err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE step_runs SET status = 'waiting_approval' WHERE step_run_id = ? AND status = 'running'`,
		"质量审核步骤已经变化。", review.StepRunID); err != nil {
		return err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE runs SET status = 'waiting_approval', updated_at = ? WHERE run_id = ? AND status = 'running'`,
		"生成任务状态已经变化。", formatTime(now), review.RunID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET version = version + 1, status = 'waiting_approval', updated_at = ? WHERE project_id = ? AND active_write_run_id = ?`,
		formatTime(now), review.ProjectID, review.RunID); err != nil {
		return err
	}
	runRef, stepRef := review.RunID, review.StepRunID
	_, err = s.appendEvent(ctx, tx, review.ProjectID, &runRef, &stepRef,
		"approval.requested", "approval", approvalID, map[string]any{"quality_review_id": review.QualityReviewID})
	return err
}

func (s *Store) advanceAcceptedQualityReviewOverrideTx(
	ctx context.Context,
	tx *sql.Tx,
	review QualityReview,
	actorRef string,
	now time.Time,
) error {
	var approvalID string
	err := tx.QueryRowContext(ctx, `
		SELECT approval_request_id FROM approvals
		WHERE run_id = ? AND step_run_id = ? AND subject_kind = 'quality_review'
			AND subject_ref_id = ? AND status = 'pending'
		ORDER BY requested_at DESC LIMIT 1`,
		review.RunID, review.StepRunID, review.QualityReviewID,
	).Scan(&approvalID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	run, err := getRunTx(ctx, tx, review.RunID)
	if err != nil {
		return err
	}
	entry, ok, registryErr := s.capabilityEntryForRunQuery(
		ctx, tx, run.RunID, run.CapabilityID,
	)
	if registryErr != nil {
		return registryErr
	}
	if !ok || entry.Definition == nil {
		return domainError("CAPABILITY_VERSION_UNAVAILABLE", "风险保留后的质量审核步骤不可恢复。")
	}
	var stepID string
	if err := tx.QueryRowContext(ctx, `
		SELECT step_id FROM step_runs
		WHERE step_run_id = ? AND run_id = ?`,
		review.StepRunID, review.RunID,
	).Scan(&stepID); err != nil {
		return err
	}
	step := scriptQualityReviewStep(entry.Definition, stepID)
	if step == nil {
		return domainError("CAPABILITY_VERSION_UNAVAILABLE", "质量审核步骤定义不可用。")
	}
	resolution, _ := json.Marshal(map[string]any{"action": "accept_with_risk"})
	if err := updateExactlyOne(ctx, tx, `
		UPDATE approvals SET status = 'approved', resolved_at = ?, resolution_json = ?, actor_ref = ?
		WHERE approval_request_id = ? AND status = 'pending'`,
		"质量审核确认请求已经变化。", formatTime(now), string(resolution), actorRef, approvalID); err != nil {
		return err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE step_runs SET status = 'running' WHERE step_run_id = ? AND status = 'waiting_approval'`,
		"质量审核步骤已经变化。", review.StepRunID); err != nil {
		return err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE runs SET status = 'running', updated_at = ? WHERE run_id = ? AND status = 'waiting_approval'`,
		"生成任务状态已经变化。", formatTime(now), review.RunID); err != nil {
		return err
	}
	if err := s.advancePassedQualityReviewTx(ctx, tx, review.RunID, review.StepRunID, *step, now, "approved"); err != nil {
		return err
	}
	runRef, stepRef := review.RunID, review.StepRunID
	_, err = s.appendEvent(ctx, tx, review.ProjectID, &runRef, &stepRef,
		"approval.resolved", "approval", approvalID, map[string]any{"action": "accept_with_risk"})
	return err
}
