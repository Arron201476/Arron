package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"content-agent/backend/internal/capability"
)

// ContinueWithPartialResults converts a settled, partially successful batch into
// the normal whole-step approval flow. The policy is Runtime-owned; capabilities
// only declare the generic preserve_success_retry_failed batch behavior.
func (s *Store) ContinueWithPartialResults(
	ctx context.Context,
	command ContinueWithPartialResultsCommand,
) (RunSnapshot, error) {
	if command.StepRunID == "" {
		return RunSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "缺少需要继续处理的批次步骤。")
	}
	if !command.Confirmed {
		return RunSnapshot{}, domainError("CONFIRMATION_REQUIRED", "按不完整结果继续前必须明确确认。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunSnapshot{}, err
	}
	defer tx.Rollback()

	var runID, projectID, capabilityID, capabilityVersion, stepID, stepStatus, runStatus string
	var currentStepRunID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT sr.run_id, r.project_id, r.capability_id, r.capability_version,
			sr.step_id, sr.status, r.status, r.current_step_run_id
		FROM step_runs sr
		JOIN runs r ON r.run_id = sr.run_id
		WHERE sr.step_run_id = ?`, command.StepRunID).Scan(
		&runID, &projectID, &capabilityID, &capabilityVersion,
		&stepID, &stepStatus, &runStatus, &currentStepRunID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RunSnapshot{}, domainError("STEP_RUN_NOT_FOUND", "生成步骤不存在。")
	}
	if err != nil {
		return RunSnapshot{}, err
	}
	if command.Scope == "" {
		command.Scope = projectID
	}
	cached, hit, err := s.beginExecutionControlCommand(ctx, tx, command.CommandMeta, projectID, "run", runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if hit {
		return decodeIdempotentResult[RunSnapshot](cached)
	}
	if runStatus != "failed" || stepStatus != "failed" ||
		!currentStepRunID.Valid || currentStepRunID.String != command.StepRunID {
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "当前批次不处于可按部分结果继续的状态。")
	}

	entry, ok, registryErr := s.capabilityEntryForRunQuery(
		ctx, tx, runID, capabilityID,
	)
	if registryErr != nil {
		return RunSnapshot{}, registryErr
	}
	if err := authorizePersonalSkillExecution(ctx, entry); err != nil {
		return RunSnapshot{}, err
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil ||
		entry.Definition.Version != capabilityVersion {
		return RunSnapshot{}, domainError("CAPABILITY_VERSION_UNAVAILABLE", "原能力版本当前不可继续。")
	}
	step := compiledStep(entry.Definition.Steps, stepID)
	if step == nil || step.Batch == nil || step.Batch.FailurePolicy != "preserve_success_retry_failed" ||
		len(step.OutputRefs) == 0 {
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "当前步骤不支持保留成功结果后继续。")
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT item_key, status, output_artifact_version_id
		FROM task_items WHERE step_run_id = ?
		ORDER BY item_order ASC, task_item_id ASC`, command.StepRunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	var succeeded int
	var failedKeys []string
	var unsettled int
	for rows.Next() {
		var itemKey, status string
		var outputVersionID sql.NullString
		if err := rows.Scan(&itemKey, &status, &outputVersionID); err != nil {
			rows.Close()
			return RunSnapshot{}, err
		}
		switch status {
		case "succeeded":
			if outputVersionID.Valid && outputVersionID.String != "" {
				succeeded++
			}
		case "failed":
			failedKeys = append(failedKeys, itemKey)
		case "pending", "running", "repair_pending", "waiting_approval", "paused":
			unsettled++
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RunSnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return RunSnapshot{}, err
	}
	if succeeded == 0 || len(failedKeys) == 0 || unsettled > 0 {
		return RunSnapshot{}, domainError("RUN_STATE_CONFLICT", "批次必须已经结束，并且同时包含成功结果和失败项。")
	}

	approval, err := s.createArtifactVersionSetApprovalTx(
		ctx, tx, projectID, runID, command.StepRunID,
		step.OutputRefs[0].ArtifactType, *step, now,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	reason := fmt.Sprintf(
		"已保留 %d 项成功结果；以下 %d 项未生成：%s。确认后将仅使用现有结果继续，后续产物会保留材料不完整记录。",
		succeeded, len(failedKeys), strings.Join(failedKeys, "、"),
	)
	decisionPayload, err := json.Marshal(map[string]any{
		"mode":             "continue_with_partial_results",
		"succeeded_count":  succeeded,
		"failed_item_keys": failedKeys,
	})
	if err != nil {
		return RunSnapshot{}, err
	}
	decision, err := s.createRunDecisionSnapshotTx(
		ctx, tx, projectID, runID, command.StepRunID,
		"partial_batch_continuation", "step_run", command.StepRunID,
		decisionPayload, now,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE approvals SET title = ?, reason = ?
		WHERE approval_request_id = ? AND status = 'pending'`,
		"确认按现有结果继续", reason, approval.ApprovalRequestID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE step_runs SET status = 'waiting_approval', ended_at = NULL
		WHERE step_run_id = ? AND status = 'failed'`,
		"批次步骤状态已经变化。", command.StepRunID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE runs SET status = 'waiting_approval', ended_at = NULL, updated_at = ?
		WHERE run_id = ? AND status = 'failed'`,
		"生成任务状态已经变化。", formatTime(now), runID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects SET version = version + 1, status = 'waiting_approval', updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		formatTime(now), projectID, runID,
	); err != nil {
		return RunSnapshot{}, err
	}

	runRef, stepRef := runID, command.StepRunID
	events := []struct {
		typeName string
		subject  string
		id       string
	}{
		{"batch.partial_results_confirmed", "step_run", command.StepRunID},
		{"step.waiting_approval", "step_run", command.StepRunID},
		{"approval.requested", "approval", approval.ApprovalRequestID},
		{"run.waiting_approval", "run", runID},
	}
	for _, event := range events {
		if _, err := s.appendEvent(ctx, tx, projectID, &runRef, &stepRef,
			event.typeName, event.subject, event.id,
			map[string]any{"succeeded_count": succeeded, "failed_item_keys": failedKeys, "actor_ref": command.ActorRef, "decision_snapshot_id": decision.DecisionSnapshotID},
		); err != nil {
			return RunSnapshot{}, err
		}
	}

	snapshot, err := s.getRunSnapshotTx(ctx, tx, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, snapshot, now); err != nil {
		return RunSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunSnapshot{}, err
	}
	return snapshot, nil
}
