package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"content-agent/backend/internal/capability"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func (s *Store) commitExecutionResult(
	ctx context.Context,
	command CommitExecutionResultCommand,
) (ArtifactCommitResult, error) {
	if command.AttemptID == "" || command.ExpectedResponseHash == "" {
		return ArtifactCommitResult{}, domainError(
			"REQUEST_VALIDATION_FAILED",
			"正式提交缺少执行尝试或响应摘要。",
		)
	}

	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	defer tx.Rollback()

	var attemptStatus string
	var responseHashValue, responsePayloadValue sql.NullString
	var attemptInputHash, contextPackHash, contextPackPayload, usagePayload string
	var taskStatus, runStatus, stepStatus string
	var taskItemID, runID, stepRunID, stepID, projectID string
	var capabilityID, capabilityVersion, executorID string
	var currentAttemptID, outputVersionID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT ea.status, ea.response_hash, ea.response_payload_json, ea.executor_id,
			ea.usage_json,
			ea.input_snapshot_hash, cp.context_hash, cp.payload_json,
			ti.task_item_id, ti.status, ti.current_attempt_id,
			ti.output_artifact_version_id,
			r.run_id, r.project_id, r.capability_id, r.capability_version, r.status,
			sr.step_run_id, sr.step_id, sr.status
		FROM execution_attempts ea
		JOIN context_packs cp ON cp.attempt_id = ea.attempt_id
		JOIN task_items ti ON ti.task_item_id = ea.task_item_id
		JOIN runs r ON r.run_id = ea.run_id
		JOIN step_runs sr ON sr.step_run_id = ea.step_run_id
		WHERE ea.attempt_id = ?`, command.AttemptID).Scan(
		&attemptStatus, &responseHashValue, &responsePayloadValue, &executorID,
		&usagePayload,
		&attemptInputHash, &contextPackHash, &contextPackPayload,
		&taskItemID, &taskStatus, &currentAttemptID, &outputVersionID,
		&runID, &projectID, &capabilityID, &capabilityVersion, &runStatus,
		&stepRunID, &stepID, &stepStatus,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactCommitResult{}, domainError(
			"EXECUTION_ATTEMPT_NOT_FOUND",
			"执行尝试不存在。",
		)
	}
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if command.RepairOutput {
		if _, err := validateOutputRepairOwnerTx(ctx, tx, command); err != nil {
			return ArtifactCommitResult{}, err
		}
	}
	if !responseHashValue.Valid || responseHashValue.String != command.ExpectedResponseHash {
		return ArtifactCommitResult{}, domainError(
			"ATTEMPT_RESULT_CHANGED",
			"执行结果摘要不匹配，不能正式提交。",
		)
	}
	if attemptStatus == "succeeded" {
		var completedPack StepExecutionContextPack
		if err := json.Unmarshal([]byte(contextPackPayload), &completedPack); err == nil &&
			isScriptHandoffRefreshPack(completedPack) {
			return s.committedScriptHandoffRefreshResultTx(
				ctx,
				tx,
				command.AttemptID,
				runID,
			)
		}
		if !outputVersionID.Valid {
			checkpoint, err := taskCheckpointForAttemptTx(ctx, tx, command.AttemptID)
			if err != nil {
				return ArtifactCommitResult{}, err
			}
			snapshot, err := s.getRunSnapshotTx(ctx, tx, runID)
			if err != nil {
				return ArtifactCommitResult{}, err
			}
			return ArtifactCommitResult{
				CommitStatus:   "task_checkpointed",
				TaskCheckpoint: checkpoint,
				RunSnapshot:    snapshot,
			}, nil
		}
		if completedPack.SkillInstructions != nil && len(completedPack.OutputContracts) > 1 {
			return s.committedDirectSkillBundleResultTx(ctx, tx, command.AttemptID, runID, completedPack.OutputContracts)
		}
		return s.committedArtifactResultTx(ctx, tx, outputVersionID.String, runID)
	}
	if attemptStatus != "result_received" ||
		!currentAttemptID.Valid || currentAttemptID.String != command.AttemptID ||
		taskStatus != "running" || stepStatus != "running" || !runAcceptsActiveAttemptResult(runStatus) {
		return ArtifactCommitResult{}, domainError(
			"ATTEMPT_STALE",
			"执行结果已过期，不能正式提交。",
		)
	}
	if err := validateAgentMemoryCommitTx(ctx, tx, "stateful_workflow", command.AttemptID); err != nil {
		return ArtifactCommitResult{}, err
	}
	if err := validateExternalToolOutcomesTx(ctx, tx, "stateful_workflow", command.AttemptID); err != nil {
		return ArtifactCommitResult{}, err
	}
	if !responsePayloadValue.Valid || len(responsePayloadValue.String) > maxProviderResponseBytes ||
		!json.Valid([]byte(responsePayloadValue.String)) || sha256Hex([]byte(responsePayloadValue.String)) != responseHashValue.String {
		return ArtifactCommitResult{}, domainError("ATTEMPT_RESULT_CHANGED", "已保存结果的内容与摘要不匹配，不能正式提交。")
	}

	entry, ok, registryErr := s.capabilityEntryForRunQuery(ctx, tx, runID, capabilityID)
	if registryErr != nil {
		return ArtifactCommitResult{}, registryErr
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil ||
		entry.Definition.Version != capabilityVersion {
		return ArtifactCommitResult{}, domainError(
			"CAPABILITY_VERSION_UNAVAILABLE",
			"执行结果对应的能力版本当前不可用。",
		)
	}
	var step *capability.CompiledStep
	for index := range entry.Definition.Steps {
		if entry.Definition.Steps[index].ID == stepID {
			step = &entry.Definition.Steps[index]
			break
		}
	}
	if step == nil || step.ExecutorRef != executorID {
		return ArtifactCommitResult{}, domainError(
			"CAPABILITY_VERSION_UNAVAILABLE",
			"执行结果对应的步骤定义当前不可用。",
		)
	}
	if isScriptQualityReviewStep(*step) {
		var contextPack StepExecutionContextPack
		if err := json.Unmarshal([]byte(contextPackPayload), &contextPack); err != nil {
			return ArtifactCommitResult{}, domainError("CONTEXT_PACK_INVALID", "质量审核执行上下文无法读取。")
		}
		calculatedContextHash, err := calculateStepExecutionContextHash(contextPack)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		if contextPack.ContextHash != contextPackHash || contextPackHash != attemptInputHash ||
			calculatedContextHash != contextPackHash {
			return ArtifactCommitResult{}, domainError("CONTEXT_PACK_HASH_MISMATCH", "质量审核执行上下文完整性校验失败。")
		}
		if contextPack.ProjectID != projectID || contextPack.Run.RunID != runID ||
			contextPack.Step.StepRunID != stepRunID || contextPack.Step.TaskItemID != taskItemID {
			return ArtifactCommitResult{}, domainError("CONTEXT_LINEAGE_CONFLICT", "质量审核上下文与提交目标不一致。")
		}
		result, err := s.commitQualityReviewTaskTx(
			ctx, tx, command.AttemptID, taskItemID, runID, projectID, stepRunID,
			*step, contextPack, json.RawMessage(responsePayloadValue.String), now,
		)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return ArtifactCommitResult{}, err
		}
		return result, nil
	}
	directSkillOutput := entry.Skill != nil && capability.SupportsDirectSkillOutput(*step)
	if (!directSkillOutput && step.ResponseAdapterRef == nil) ||
		(len(step.OutputRefs) != 1 && !directSkillOutput && !isScriptBundleStep(*step)) {
		return ArtifactCommitResult{}, domainError(
			"OUTPUT_COMMIT_UNSUPPORTED",
			"当前步骤的输出提交合同不受支持。",
		)
	}
	output := step.OutputRefs[0]
	if (output.Cardinality != "one" && output.Cardinality != "many") ||
		output.InitialStatus != "pending_approval" ||
		!step.Approval.Required {
		return ArtifactCommitResult{}, domainError(
			"OUTPUT_COMMIT_UNSUPPORTED",
			"当前只支持需要用户确认的批量或单产物步骤正式提交。",
		)
	}
	var contextPack StepExecutionContextPack
	if err := json.Unmarshal([]byte(contextPackPayload), &contextPack); err != nil {
		return ArtifactCommitResult{}, domainError(
			"CONTEXT_PACK_INVALID",
			"执行上下文无法读取，正式产物未保存。",
		)
	}
	calculatedContextHash, err := calculateStepExecutionContextHash(contextPack)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if contextPack.ContextHash != contextPackHash ||
		contextPackHash != attemptInputHash ||
		calculatedContextHash != contextPackHash {
		return ArtifactCommitResult{}, domainError(
			"CONTEXT_PACK_HASH_MISMATCH",
			"执行上下文完整性校验失败，正式产物未保存。",
		)
	}
	if contextPack.ProjectID != projectID ||
		contextPack.Run.RunID != runID ||
		contextPack.Step.StepRunID != stepRunID ||
		contextPack.Step.TaskItemID != taskItemID {
		return ArtifactCommitResult{}, domainError(
			"CONTEXT_LINEAGE_CONFLICT",
			"执行上下文与正式提交目标不一致。",
		)
	}
	handoffRefresh := isScriptHandoffRefreshPack(contextPack)
	if handoffRefresh {
		if err := validateScriptHandoffRefreshContext(
			*step,
			contextPack,
		); err != nil {
			return ArtifactCommitResult{}, err
		}
	} else if isScriptBundleStep(*step) {
		if err := validateScriptExecutionContext(*step, contextPack); err != nil {
			return ArtifactCommitResult{}, err
		}
	} else {
		if err := validateContextUpstreamCoverage(
			step.InputRefs,
			declaredContextUpstream(contextPack.UpstreamContext),
		); err != nil {
			return ArtifactCommitResult{}, err
		}
	}
	selectedUpstream, err := selectedContextDependencies(contextPack, usagePayload)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	contextPack.UpstreamContext = selectedUpstream
	internalStage, isInternalStage := batchInternalStageForCursor(
		step.Batch,
		contextPack.TaskCursor,
	)
	if isInternalStage {
		if contextPack.OutputContract.ArtifactType != "task_checkpoint:"+internalStage.ID ||
			contextPack.OutputContract.SchemaRef != internalStage.SchemaRef {
			return ArtifactCommitResult{}, domainError(
				"CONTEXT_LINEAGE_CONFLICT",
				"内部 Task 输出合同与 Capability 声明不一致。",
			)
		}
		if err := validateEmbeddedJSONSchema(
			contextPack.OutputContract.Schema,
			json.RawMessage(responsePayloadValue.String),
		); err != nil {
			return ArtifactCommitResult{}, domainError(
				"OUTPUT_SCHEMA_VALIDATION_FAILED",
				fmt.Sprintf("内部 Task 输出未通过合同校验：%v", err),
			)
		}
		if step.Batch.Preparation != nil &&
			internalStage.ID == step.Batch.Preparation.ID {
			checkpoint, err := s.commitBatchPreparationCheckpointTx(
				ctx,
				tx,
				runID,
				projectID,
				stepRunID,
				taskItemID,
				command.AttemptID,
				*step,
				contextPack,
				json.RawMessage(responsePayloadValue.String),
				now,
			)
			if err != nil {
				return ArtifactCommitResult{}, err
			}
			snapshot, err := s.getRunSnapshotTx(ctx, tx, runID)
			if err != nil {
				return ArtifactCommitResult{}, err
			}
			result := ArtifactCommitResult{
				CommitStatus:   "task_checkpointed",
				TaskCheckpoint: checkpoint,
				RunSnapshot:    snapshot,
			}
			if err := tx.Commit(); err != nil {
				return ArtifactCommitResult{}, err
			}
			return result, nil
		}
	}
	if !isInternalStage && !handoffRefresh &&
		contextPack.OutputContract.ArtifactType != output.ArtifactType {
		return ArtifactCommitResult{}, domainError(
			"CONTEXT_LINEAGE_CONFLICT",
			"执行上下文与正式提交目标不一致。",
		)
	}
	if !isInternalStage && handoffRefresh {
		result, err := s.commitScriptHandoffRefreshTx(
			ctx,
			tx,
			command.AttemptID,
			taskItemID,
			runID,
			projectID,
			stepRunID,
			contextPack.Target.ScopeKey,
			*step,
			contextPack,
			json.RawMessage(responsePayloadValue.String),
			now,
		)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return ArtifactCommitResult{}, err
		}
		return result, nil
	}
	if !isInternalStage && isScriptBundleStep(*step) {
		result, err := s.commitScopedScriptBundleTx(
			ctx,
			tx,
			command.AttemptID,
			taskItemID,
			runID,
			projectID,
			stepRunID,
			capabilityID,
			contextPack.Target.ScopeKey,
			*step,
			contextPack,
			json.RawMessage(responsePayloadValue.String),
			now,
		)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return ArtifactCommitResult{}, err
		}
		return result, nil
	}

	if directSkillOutput && len(step.OutputRefs) > 1 {
		result, err := s.commitDirectSkillBundleTx(ctx, tx, command.AttemptID, taskItemID, runID,
			projectID, stepRunID, capabilityID, *step, contextPack, json.RawMessage(responsePayloadValue.String), now)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return ArtifactCommitResult{}, err
		}
		return result, nil
	}

	var adaptedPayload json.RawMessage
	if isInternalStage {
		adaptedPayload, err = normalizeInternalBatchResult(
			output.ArtifactType,
			contextPack.TaskCursor,
			json.RawMessage(responsePayloadValue.String),
		)
	} else if directSkillOutput {
		if err := validateDirectSkillResponse(contextPack, json.RawMessage(responsePayloadValue.String)); err != nil {
			return ArtifactCommitResult{}, domainError(
				"OUTPUT_SCHEMA_VALIDATION_FAILED",
				fmt.Sprintf("模型输出未通过 Skill 产物合同校验：%v", err),
			)
		}
		adaptedPayload, err = normalizeJSON(json.RawMessage(responsePayloadValue.String))
	} else {
		adaptedPayload, err = s.contracts.AdaptAndValidate(
			*step.ResponseAdapterRef,
			output.SchemaRef,
			output.ArtifactType,
			json.RawMessage(responsePayloadValue.String),
		)
	}
	if err != nil {
		return ArtifactCommitResult{}, domainError(
			"OUTPUT_SCHEMA_VALIDATION_FAILED",
			fmt.Sprintf("模型输出未通过产物合同校验：%v", err),
		)
	}
	adaptedPayload, err = normalizeAndValidateArtifactOutput(*step, output, contextPack, adaptedPayload)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if output.Cardinality == "many" {
		result, err := s.commitScopedBatchArtifactTx(
			ctx,
			tx,
			command.AttemptID,
			taskItemID,
			runID,
			projectID,
			stepRunID,
			capabilityID,
			contextPack.Target.ScopeKey,
			output,
			*step,
			contextPack,
			directSkillOutput,
			adaptedPayload,
			now,
		)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return ArtifactCommitResult{}, err
		}
		return result, nil
	}
	batchCheckpointed := false
	finalBatchStage, finalBatchStageFound := batchStageForCursor(
		step.Batch,
		contextPack.TaskCursor,
	)
	finalBatchStageEmitsArtifact := finalBatchStageFound &&
		finalBatchStage.ResultMode == "artifact"
	if (step.Batch != nil || step.Kind == "batch") &&
		!finalBatchStageEmitsArtifact {
		var taskCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM task_items WHERE step_run_id = ?`,
			stepRunID,
		).Scan(&taskCount); err != nil {
			return ArtifactCommitResult{}, err
		}
		if taskCount > 1 {
			checkpoint, allCompleted, mergedPayload, err :=
				s.commitBatchTaskCheckpointTx(
					ctx,
					tx,
					command.AttemptID,
					taskItemID,
					stepRunID,
					runID,
					projectID,
					output.ArtifactType,
					adaptedPayload,
					*step,
					now,
				)
			if err != nil {
				return ArtifactCommitResult{}, err
			}
			batchCheckpointed = true
			if !allCompleted {
				snapshot, err := s.getRunSnapshotTx(ctx, tx, runID)
				if err != nil {
					return ArtifactCommitResult{}, err
				}
				result := ArtifactCommitResult{
					CommitStatus:   "task_checkpointed",
					TaskCheckpoint: checkpoint,
					RunSnapshot:    snapshot,
				}
				if err := tx.Commit(); err != nil {
					return ArtifactCommitResult{}, err
				}
				return result, nil
			}
			wrapped, err := json.Marshal(map[string]any{
				"artifact": json.RawMessage(mergedPayload),
			})
			if err != nil {
				return ArtifactCommitResult{}, err
			}
			adaptedPayload, err = s.contracts.AdaptAndValidate(
				*step.ResponseAdapterRef,
				output.SchemaRef,
				output.ArtifactType,
				wrapped,
			)
			if err != nil {
				return ArtifactCommitResult{}, domainError(
					"OUTPUT_SCHEMA_VALIDATION_FAILED",
					fmt.Sprintf("批量合并结果未通过产物合同校验：%v", err),
				)
			}
		}
	}
	target, err := s.prepareArtifactCommitTargetTx(
		ctx,
		tx,
		projectID,
		runID,
		stepRunID,
		capabilityID,
		output.ArtifactType,
		adaptedPayload,
		command.AttemptID,
		now,
	)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	artifactID := target.ArtifactID
	artifactVersionID := target.ArtifactVersionID
	approvalID := s.newID("apr")
	for _, upstream := range contextPack.UpstreamContext {
		if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
			ProjectID:                   projectID,
			RunID:                       runID,
			DownstreamArtifactVersionID: artifactVersionID,
			UpstreamKind:                "artifact_version",
			UpstreamRefID:               upstream.ArtifactVersionID,
			Relation:                    "derived_from",
			UpstreamScope:               dependencyScope(scopeKind(upstream.ScopeKey), upstream.ScopeKey),
			DownstreamScope:             dependencyScope("artifact", "singleton"),
			ImpactPolicyID:              "whole_downstream",
		}, now); err != nil {
			return ArtifactCommitResult{}, err
		}
	}
	if err := s.insertTaskContextDependenciesTx(ctx, tx, projectID, runID, artifactVersionID,
		"singleton", contextPack, directSkillOutput, now); err != nil {
		return ArtifactCommitResult{}, err
	}

	optionsJSON, err := json.Marshal(step.Approval.AllowedActions)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	title, reason := approvalPresentation(output.ArtifactType)
	subjectHash := approvalSubjectHash(artifactVersionID, target.Version)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO approvals(
			approval_request_id, project_id, run_id, step_run_id, scope, status, version,
			title, reason, options_json, subject_kind, subject_ref_id, subject_version,
			subject_snapshot_hash, requested_at
		) VALUES(?, ?, ?, ?, ?, 'pending', 1, ?, ?, ?, 'artifact_version', ?, ?, ?, ?)`,
		approvalID, projectID, runID, stepRunID, step.Approval.Scope, title, reason,
		string(optionsJSON), artifactVersionID, target.Version, subjectHash, formatTime(now),
	); err != nil {
		return ArtifactCommitResult{}, err
	}
	if batchCheckpointed {
		result, err := tx.ExecContext(ctx, `
			UPDATE task_items
			SET output_artifact_version_id = ?, updated_at = ?
			WHERE step_run_id = ? AND status = 'succeeded'
				AND output_artifact_version_id IS NULL`,
			artifactVersionID,
			formatTime(now),
			stepRunID,
		)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		var taskCount int64
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM task_items WHERE step_run_id = ?`,
			stepRunID,
		).Scan(&taskCount); err != nil {
			return ArtifactCommitResult{}, err
		}
		if affected != taskCount {
			return ArtifactCommitResult{}, domainError(
				"RUN_STATE_CONFLICT",
				"批量任务输出版本绑定不完整。",
			)
		}
	} else {
		if err := updateExactlyOne(ctx, tx, `
			UPDATE execution_attempts SET status = 'succeeded'
			WHERE attempt_id = ? AND status = 'result_received'`,
			"执行尝试状态已变化。", command.AttemptID,
		); err != nil {
			return ArtifactCommitResult{}, err
		}
		if err := updateExactlyOne(ctx, tx, `
			UPDATE task_items
			SET status = 'succeeded', output_artifact_version_id = ?, failure = NULL, ended_at = ?, updated_at = ?
			WHERE task_item_id = ? AND status = 'running' AND current_attempt_id = ?`,
			"执行任务状态已变化。", artifactVersionID, formatTime(now), formatTime(now),
			taskItemID, command.AttemptID,
		); err != nil {
			return ArtifactCommitResult{}, err
		}
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE step_runs SET status = 'waiting_approval'
		WHERE step_run_id = ? AND status = 'running'`,
		"执行步骤状态已变化。", stepRunID,
	); err != nil {
		return ArtifactCommitResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE runs SET status = 'waiting_approval', updated_at = ?
		WHERE run_id = ? AND status IN ('running', 'pausing')`,
		"生成任务状态已变化。", formatTime(now), runID,
	); err != nil {
		return ArtifactCommitResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE projects
		SET version = version + 1, status = 'waiting_approval',
			current_focus_artifact_version_id = ?, updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		"作品写锁已变化。", artifactVersionID, formatTime(now), projectID, runID,
	); err != nil {
		return ArtifactCommitResult{}, err
	}

	runRef, stepRef := runID, stepRunID
	events := []struct {
		eventType   string
		subjectType string
		subjectID   string
		payload     any
	}{}
	if target.CreatedArtifact {
		events = append(events, struct {
			eventType   string
			subjectType string
			subjectID   string
			payload     any
		}{"artifact.created", "artifact", artifactID, map[string]any{"artifact_type": output.ArtifactType}})
	} else {
		events = append(events, struct {
			eventType   string
			subjectType string
			subjectID   string
			payload     any
		}{"artifact.replacement_created", "artifact_version", artifactVersionID, map[string]any{
			"regeneration_plan_id": target.RegenerationPlanID,
			"replaced_version_id":  target.ReplacedVersionID,
		}})
	}
	events = append(events,
		struct {
			eventType   string
			subjectType string
			subjectID   string
			payload     any
		}{"artifact.version_created", "artifact_version", artifactVersionID, map[string]any{
			"version":         target.Version,
			"creation_reason": target.CreationReason,
		}},
		struct {
			eventType   string
			subjectType string
			subjectID   string
			payload     any
		}{"step.waiting_approval", "step_run", stepRunID, nil},
		struct {
			eventType   string
			subjectType string
			subjectID   string
			payload     any
		}{"approval.requested", "approval", approvalID, nil},
		struct {
			eventType   string
			subjectType string
			subjectID   string
			payload     any
		}{"run.waiting_approval", "run", runID, nil},
	)
	if !batchCheckpointed {
		events = append(events, struct {
			eventType   string
			subjectType string
			subjectID   string
			payload     any
		}{"task.completed", "task_item", taskItemID, map[string]any{"attempt_id": command.AttemptID}})
	}
	for _, event := range events {
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
			return ArtifactCommitResult{}, err
		}
	}

	result, err := s.committedArtifactResultTx(ctx, tx, artifactVersionID, runID)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ArtifactCommitResult{}, err
	}
	return result, nil
}

func selectedContextDependencies(
	pack StepExecutionContextPack,
	usagePayload string,
) ([]ContextUpstreamArtifact, error) {
	type contextUsage struct {
		ContextArtifactVersionIDs []string `json:"context_artifact_version_ids"`
	}
	var usage contextUsage
	if err := json.Unmarshal([]byte(usagePayload), &usage); err != nil {
		return nil, domainError("EXECUTION_USAGE_INVALID", "执行结果的上下文读取记录无效。")
	}
	available := make(map[string]ContextUpstreamArtifact, len(pack.UpstreamContext))
	selected := make(map[string]struct{}, len(usage.ContextArtifactVersionIDs))
	for _, upstream := range pack.UpstreamContext {
		available[upstream.ArtifactVersionID] = upstream
	}
	if pack.StructuredRunState != nil {
		available[pack.StructuredRunState.ArtifactVersionID] = structuredRunStateDependency(pack.StructuredRunState)
		selected[pack.StructuredRunState.ArtifactVersionID] = struct{}{}
	}
	for _, versionID := range usage.ContextArtifactVersionIDs {
		if _, ok := available[versionID]; !ok {
			return nil, domainError("CONTEXT_LINEAGE_CONFLICT", "SDK 读取了本次冻结目录之外的产物版本。")
		}
		selected[versionID] = struct{}{}
	}

	dependencies := make([]ContextUpstreamArtifact, 0, len(pack.UpstreamContext))
	requiredPrior := make(map[string]bool, 2)
	candidateCount := 0
	episodeNo, episodeErr := episodeNumberFromScope(pack.Target.ScopeKey)
	previousScope := ""
	if episodeErr == nil && episodeNo > 1 {
		previousScope = fmt.Sprintf("episode:%d", episodeNo-1)
	}
	for _, upstream := range pack.UpstreamContext {
		if upstream.SelectionPolicy != "sdk_context_candidate" {
			dependencies = append(dependencies, upstream)
			continue
		}
		candidateCount++
		_, wasRead := selected[upstream.ArtifactVersionID]
		if upstream.ScopeKey == previousScope &&
			(upstream.ArtifactType == "script_handoff" || upstream.ArtifactType == "script_unit") {
			requiredPrior[upstream.ArtifactType] = wasRead
		}
		if wasRead {
			dependencies = append(dependencies, upstream)
		}
	}
	if pack.StructuredRunState != nil {
		dependencies = append(dependencies, structuredRunStateDependency(pack.StructuredRunState))
	}
	if candidateCount > 0 && previousScope != "" &&
		(!requiredPrior["script_handoff"] || !requiredPrior["script_unit"]) {
		return nil, domainError(
			"CONTEXT_REQUIRED_UPSTREAM_MISSING",
			"SDK 未读取紧邻上一集的交接和剧本，不能提交本集结果。",
		)
	}
	return dependencies, nil
}

func structuredRunStateDependency(state *ContextStructuredRunState) ContextUpstreamArtifact {
	return ContextUpstreamArtifact{
		ArtifactID: state.ArtifactID, ArtifactVersionID: state.ArtifactVersionID,
		ArtifactType: "structured_run_state", ScopeKey: "run", Status: "confirmed",
		SelectionPolicy: "structured_state_snapshot", Content: state.Content, ContentHash: state.ContentHash,
	}
}

func validateEmbeddedJSONSchema(
	schemaPayload json.RawMessage,
	instancePayload json.RawMessage,
) error {
	schemaDocument, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaPayload))
	if err != nil {
		return fmt.Errorf("decode schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	const location = "urn:content-agent:internal-task-schema"
	if err := compiler.AddResource(location, schemaDocument); err != nil {
		return fmt.Errorf("register schema: %w", err)
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(instancePayload))
	if err != nil {
		return fmt.Errorf("decode instance: %w", err)
	}
	return compiled.Validate(instance)
}

func (s *Store) committedArtifactResultTx(
	ctx context.Context,
	tx *sql.Tx,
	artifactVersionID string,
	runID string,
) (ArtifactCommitResult, error) {
	version, err := scanArtifactVersion(tx.QueryRowContext(ctx, `
		SELECT artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason, base_version_id,
			created_at, confirmed_at
		FROM artifact_versions WHERE artifact_version_id = ?`, artifactVersionID))
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	artifact, err := scanArtifact(tx.QueryRowContext(ctx, `
		SELECT artifact_id, project_id, COALESCE(run_id, ''), COALESCE(step_run_id, ''),
			origin_type, origin_id, capability_id,
			artifact_type, scope_key, current_version_id, created_at, updated_at
		FROM artifacts WHERE artifact_id = ?`, version.ArtifactID))
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	approval, err := scanApproval(tx.QueryRowContext(ctx, `
		SELECT approval_request_id, project_id, run_id, step_run_id, scope, status, version,
			title, reason, options_json, subject_kind, subject_ref_id, subject_version,
			subject_snapshot_hash, requested_at, resolved_at, resolution_json, actor_ref
		FROM approvals WHERE subject_ref_id = ?
		ORDER BY requested_at DESC LIMIT 1`, artifactVersionID))
	if errors.Is(err, sql.ErrNoRows) {
		approval, err = scanApproval(tx.QueryRowContext(ctx, `
			SELECT ap.approval_request_id, ap.project_id, ap.run_id, ap.step_run_id,
				ap.scope, ap.status, ap.version, ap.title, ap.reason, ap.options_json,
				ap.subject_kind, ap.subject_ref_id, ap.subject_version,
				ap.subject_snapshot_hash, ap.requested_at, ap.resolved_at,
				ap.resolution_json, ap.actor_ref
			FROM approval_subject_versions asv
			JOIN approvals ap
				ON ap.approval_request_id = asv.approval_request_id
			WHERE asv.artifact_version_id = ?
			ORDER BY ap.requested_at DESC LIMIT 1`,
			artifactVersionID,
		))
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ArtifactCommitResult{}, err
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, runID)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	commitStatus := "task_artifact_saved"
	if approval.ApprovalRequestID != "" {
		commitStatus = "waiting_approval"
	}
	return ArtifactCommitResult{
		CommitStatus:    commitStatus,
		Artifact:        artifact,
		ArtifactVersion: version,
		Approval:        approval,
		RunSnapshot:     snapshot,
	}, nil
}

func updateExactlyOne(
	ctx context.Context,
	tx *sql.Tx,
	query string,
	message string,
	arguments ...any,
) error {
	result, err := tx.ExecContext(ctx, query, arguments...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return domainError("RUN_STATE_CONFLICT", message)
	}
	return nil
}

func approvalPresentation(artifactType string) (string, string) {
	labels := map[string]string{
		"story_bible":          "故事圣经",
		"episode_split":        "拆集方案",
		"material_bank":        "素材库",
		"story_seed":           "故事种子",
		"series_blueprint":     "全剧蓝图",
		"episode_cards":        "分集卡",
		"script_unit":          "单集剧本",
		"continuation_options": "续写方向",
		"continuation_script":  "续写剧本",
	}
	label := labels[artifactType]
	if label == "" {
		label = "生成产物"
	}
	return "确认" + label, "确认该步骤生成内容后才能进入下一步骤。"
}
