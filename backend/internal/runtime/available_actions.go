package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"content-agent/backend/internal/capability"
)

type runActionQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) runAvailableActions(
	ctx context.Context,
	query runActionQuery,
	run Run,
	steps []StepRun,
	approval *Approval,
) ([]AvailableAction, error) {
	resumeAllowed := false
	retryAllowed := false
	partialContinueAllowed := false
	var err error
	if run.Status == "pausing" {
		resumeAllowed = true
	} else if run.Status == "paused" {
		resumeAllowed, err = s.runCanResume(ctx, query, run, steps, approval)
		if err != nil {
			return nil, err
		}
	}
	if run.Status == "failed" {
		retryAllowed, err = s.stepCanRetry(
			ctx,
			query,
			run,
			steps,
			currentFailedStepRunID(run, steps),
		)
		if err != nil {
			return nil, err
		}
		partialContinueAllowed, err = s.runCanContinueWithPartialResults(
			ctx,
			query,
			run,
			steps,
		)
		if err != nil {
			return nil, err
		}
	}
	return projectRunAvailableActions(run, steps, resumeAllowed, retryAllowed, partialContinueAllowed), nil
}

func projectRunAvailableActions(
	run Run,
	steps []StepRun,
	resumeAllowed bool,
	retryAllowed bool,
	partialContinueAllowed bool,
) []AvailableAction {
	actions := make([]AvailableAction, 0, 3)
	add := func(actionID, targetType, targetID string) {
		actions = append(actions, AvailableAction{
			ActionID:   actionID,
			TargetType: targetType,
			TargetID:   targetID,
			Enabled:    true,
		})
	}

	switch run.Status {
	case "running":
		add("pause_run", "run", run.RunID)
	case "pausing", "paused":
		if resumeAllowed {
			add("resume_run", "run", run.RunID)
		}
	case "failed":
		stepRunID := currentFailedStepRunID(run, steps)
		if retryAllowed && stepRunID != "" {
			add("retry_failed_step", "step_run", stepRunID)
		}
		if partialContinueAllowed && stepRunID != "" {
			add("continue_with_partial_results", "step_run", stepRunID)
		}
	}

	switch run.Status {
	case "pending", "running", "waiting_approval", "pausing", "paused", "failed":
		add("cancel_run", "run", run.RunID)
	}
	return actions
}

func (s *Store) runCanContinueWithPartialResults(
	ctx context.Context,
	query runActionQuery,
	run Run,
	steps []StepRun,
) (bool, error) {
	stepRunID := currentFailedStepRunID(run, steps)
	if run.Status != "failed" || stepRunID == "" {
		return false, nil
	}
	currentStep := findStepRun(steps, stepRunID)
	entry, ok, err := s.capabilityEntryForRunQuery(
		ctx, query, run.RunID, run.CapabilityID,
	)
	if err != nil {
		return false, err
	}
	if currentStep == nil || !ok || entry.Status != capability.Available || entry.Definition == nil ||
		entry.Definition.Version != run.CapabilityVersion {
		return false, nil
	}
	step := compiledStep(entry.Definition.Steps, currentStep.StepID)
	if step == nil || step.Batch == nil || step.Batch.FailurePolicy != "preserve_success_retry_failed" {
		return false, nil
	}

	var succeeded, failed, unsettled int
	if err := query.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN status = 'succeeded' AND output_artifact_version_id IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status IN ('pending', 'running', 'repair_pending', 'waiting_approval', 'paused') THEN 1 ELSE 0 END), 0)
		FROM task_items WHERE step_run_id = ?`, stepRunID).Scan(&succeeded, &failed, &unsettled); err != nil {
		return false, err
	}
	return succeeded > 0 && failed > 0 && unsettled == 0, nil
}

func (s *Store) runCanResume(
	ctx context.Context,
	query runActionQuery,
	run Run,
	steps []StepRun,
	approval *Approval,
) (bool, error) {
	if approval != nil || run.InputSnapshotStatus != "sealed" || run.CurrentStepRunID == nil {
		return false, nil
	}
	currentStep := findStepRun(steps, *run.CurrentStepRunID)
	if currentStep == nil || (currentStep.Status != "pending" && currentStep.Status != "paused") {
		return false, nil
	}
	entry, ok, err := s.capabilityEntryForRunQuery(
		ctx, query, run.RunID, run.CapabilityID,
	)
	if err != nil {
		return false, err
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil ||
		entry.Definition.Version != run.CapabilityVersion ||
		compiledStep(entry.Definition.Steps, currentStep.StepID) == nil {
		return false, nil
	}

	var payload string
	err = query.QueryRowContext(ctx, `
		SELECT payload_json FROM run_input_snapshot_versions
		WHERE run_input_snapshot_version_id = ? AND run_id = ? AND status = 'sealed'`,
		run.CurrentInputSnapshotVersionID,
		run.RunID,
	).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var source sourceInputRequest
	if err := json.Unmarshal([]byte(payload), &source); err != nil {
		return false, nil
	}
	references := make([]assetInputReference, 0, len(source.Assets))
	for _, raw := range source.Assets {
		reference, err := validateAssetReference(raw)
		if err != nil {
			return false, nil
		}
		references = append(references, reference)
	}
	if err := validateAssetOwnership(ctx, query, run.ProjectID, references); err != nil {
		var domain *DomainError
		if errors.As(err, &domain) {
			return false, nil
		}
		return false, err
	}
	if !jsonObject(currentStep.TaskCursor) {
		return false, nil
	}
	var activeRunID sql.NullString
	if err := query.QueryRowContext(ctx, `
		SELECT active_write_run_id FROM projects
		WHERE project_id = ? AND deleted_at IS NULL`, run.ProjectID).Scan(&activeRunID); err != nil {
		return false, err
	}
	return activeRunID.Valid && activeRunID.String == run.RunID, nil
}

func (s *Store) stepCanRetry(
	ctx context.Context,
	query runActionQuery,
	run Run,
	steps []StepRun,
	stepRunID string,
) (bool, error) {
	if stepRunID == "" {
		return false, nil
	}
	stepRun := findStepRun(steps, stepRunID)
	entry, ok, err := s.capabilityEntryForRunQuery(
		ctx, query, run.RunID, run.CapabilityID,
	)
	if err != nil {
		return false, err
	}
	if stepRun == nil || !ok || entry.Status != capability.Available ||
		entry.Definition == nil || entry.Definition.Version != run.CapabilityVersion {
		return false, nil
	}
	step := compiledStep(entry.Definition.Steps, stepRun.StepID)
	if step == nil || !step.Retry.UserRetryAllowed {
		return false, nil
	}
	if risk, err := failedStepExecutionReplayRisk(ctx, query, stepRunID); err != nil || risk {
		return false, err
	}

	rows, err := query.QueryContext(ctx, `
		SELECT failure FROM task_items
		WHERE step_run_id = ? AND status = 'failed'
		ORDER BY item_order ASC, task_item_id ASC`, stepRunID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var failure sql.NullString
		if err := rows.Scan(&failure); err != nil {
			return false, err
		}
		if !failure.Valid || !slices.Contains(step.Retry.RetryableErrors, failure.String) {
			return false, nil
		}
		found = true
	}
	return found, rows.Err()
}

func findStepRun(steps []StepRun, stepRunID string) *StepRun {
	for index := range steps {
		if steps[index].StepRunID == stepRunID {
			return &steps[index]
		}
	}
	return nil
}

func compiledStep(steps []capability.CompiledStep, stepID string) *capability.CompiledStep {
	for index := range steps {
		if steps[index].ID == stepID {
			return &steps[index]
		}
	}
	return nil
}

func currentFailedStepRunID(run Run, steps []StepRun) string {
	if run.CurrentStepRunID == nil {
		return ""
	}
	step := findStepRun(steps, *run.CurrentStepRunID)
	if step != nil && step.Status == "failed" {
		return step.StepRunID
	}
	return ""
}
