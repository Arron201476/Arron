package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"content-agent/backend/internal/capability"
)

func (s *Store) advanceRunAfterConfirmedArtifactTx(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	currentStepRunID string,
	confirmedVersionID string,
	now time.Time,
	transitionFacts ...string,
) (*string, bool, error) {
	return s.advanceRunAfterConfirmedArtifactsTx(
		ctx,
		tx,
		runID,
		currentStepRunID,
		[]string{confirmedVersionID},
		now,
		transitionFacts...,
	)
}

func (s *Store) advanceRunAfterConfirmedArtifactsTx(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	currentStepRunID string,
	confirmedVersionIDs []string,
	now time.Time,
	transitionFacts ...string,
) (*string, bool, error) {
	if len(confirmedVersionIDs) == 0 {
		return nil, false, domainError(
			"DEPENDENCY_INCOMPLETE",
			"步骤推进缺少已确认产物版本。",
		)
	}
	focusVersionID := confirmedVersionIDs[len(confirmedVersionIDs)-1]
	run, err := getRunTx(ctx, tx, runID)
	if err != nil {
		return nil, false, err
	}
	entry, ok, registryErr := s.capabilityEntryForRunQuery(
		ctx, tx, run.RunID, run.CapabilityID,
	)
	if registryErr != nil {
		return nil, false, registryErr
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil ||
		entry.Definition.Version != run.CapabilityVersion {
		return nil, false, domainError(
			"CAPABILITY_VERSION_UNAVAILABLE",
			"原能力版本不可恢复。",
		)
	}
	var currentStepID string
	if err := tx.QueryRowContext(ctx, `
		SELECT step_id FROM step_runs
		WHERE step_run_id = ? AND run_id = ?`,
		currentStepRunID, runID,
	).Scan(&currentStepID); err != nil {
		return nil, false, err
	}

	var currentStep, nextStep *capability.CompiledStep
	for index := range entry.Definition.Steps {
		step := &entry.Definition.Steps[index]
		if step.ID == currentStepID {
			currentStep = step
		}
	}
	if currentStep == nil {
		return nil, false, domainError(
			"CAPABILITY_VERSION_UNAVAILABLE",
			"当前步骤不在原能力版本中。",
		)
	}
	if len(currentStep.Next) == 0 {
		if !containsString(entry.Definition.Completion.TerminalStepIDs, currentStepID) {
			return nil, false, domainError(
				"RUN_STATE_CONFLICT",
				"当前步骤没有下一步骤，也不是能力终点。",
			)
		}
		if run.Status == "completed" {
			return nil, true, nil
		}
		if err := updateExactlyOne(ctx, tx, `
			UPDATE runs
			SET status = 'completed', ended_at = ?, updated_at = ?
			WHERE run_id = ? AND status IN ('waiting_approval', 'paused')`,
			"生成任务状态已经变化。",
			formatTime(now), formatTime(now), runID,
		); err != nil {
			return nil, false, err
		}
		if err := updateExactlyOne(ctx, tx, `
			UPDATE projects
			SET version = version + 1, status = 'ready',
				active_write_run_id = NULL, current_capability_id = NULL,
				current_focus_artifact_version_id = ?, updated_at = ?
			WHERE project_id = ? AND active_write_run_id = ?`,
			"作品写锁已经变化。",
			focusVersionID, formatTime(now), run.ProjectID, runID,
		); err != nil {
			return nil, false, err
		}
		if err := syncSkillInvocationForRunTx(ctx, tx, runID, "completed", now); err != nil {
			return nil, false, err
		}
		if _, err := s.appendEvent(ctx, tx, run.ProjectID, &runID, &currentStepRunID,
			"run.completed", "run", runID, nil); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}

	nextStepID, err := completedWorkflowTransitionTx(ctx, tx, currentStepRunID, *currentStep, transitionFacts...)
	if err != nil {
		var domain *DomainError
		if errors.As(err, &domain) && domain.Code == "WORKFLOW_TRANSITION_UNMATCHED" && !containsString(transitionFacts, "user_continue") {
			for _, transition := range currentStep.Next {
				if transition.When == "user_continue" {
					return nil, false, s.requestWorkflowContinueTx(ctx, tx, run, currentStepRunID, *currentStep, confirmedVersionIDs, now)
				}
			}
		}
		return nil, false, err
	}
	for index := range entry.Definition.Steps {
		if entry.Definition.Steps[index].ID == nextStepID {
			nextStep = &entry.Definition.Steps[index]
			break
		}
	}
	if nextStep == nil {
		return nil, false, domainError(
			"CAPABILITY_VERSION_UNAVAILABLE",
			"下一步骤不在原能力版本中。",
		)
	}
	var inputVersions []map[string]any
	if isScriptQualityReviewStep(*nextStep) ||
		isScriptBundleStep(*currentStep) {
		inputVersions, err = currentRunInputsForStepTx(
			ctx, tx, run.ProjectID, runID, nextStep.InputRefs,
		)
	} else {
		inputVersions, err = lineageInputsForStepTx(
			ctx,
			tx,
			run.ProjectID,
			runID,
			confirmedVersionIDs,
			nextStep.InputRefs,
		)
	}
	if err != nil {
		return nil, false, err
	}
	inputJSON, err := json.Marshal(inputVersions)
	if err != nil {
		return nil, false, err
	}
	nextStepRunID := s.newID("step")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO step_runs(
			step_run_id, run_id, step_id, status, attempt_count, approval_policy,
			input_version_snapshot_json, task_cursor_json
		) VALUES(?, ?, ?, 'pending', 0, ?, ?, '{}')`,
		nextStepRunID,
		runID,
		nextStep.ID,
		nextStep.Approval.Type,
		string(inputJSON),
	); err != nil {
		return nil, false, err
	}
	if isScriptQualityReviewStep(*nextStep) {
		if err := updateExactlyOne(ctx, tx, `
			UPDATE step_runs SET status = 'running', attempt_count = 1, started_at = ?
			WHERE step_run_id = ? AND status = 'pending'`,
			"质量审核步骤已经变化。", formatTime(now), nextStepRunID); err != nil {
			return nil, false, err
		}
		if err := updateExactlyOne(ctx, tx, `
			UPDATE runs SET status = 'running', current_step_run_id = ?, updated_at = ?
			WHERE run_id = ? AND status IN ('waiting_approval', 'paused')`,
			"生成任务状态已经变化。", nextStepRunID, formatTime(now), runID); err != nil {
			return nil, false, err
		}
		if err := updateExactlyOne(ctx, tx, `
			UPDATE projects SET version = version + 1, status = 'running',
				current_focus_artifact_version_id = ?, updated_at = ?
			WHERE project_id = ? AND active_write_run_id = ?`,
			"作品写锁已经变化。", focusVersionID, formatTime(now), run.ProjectID, runID); err != nil {
			return nil, false, err
		}
		if err := s.planQualityReviewTasksTx(
			ctx, tx, run, nextStepRunID, *nextStep, json.RawMessage(inputJSON), now,
		); err != nil {
			return nil, false, err
		}
		return &nextStepRunID, false, nil
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE runs SET status = 'paused', current_step_run_id = ?, updated_at = ?
		WHERE run_id = ? AND status IN ('waiting_approval', 'paused')`,
		"生成任务状态已经变化。",
		nextStepRunID, formatTime(now), runID,
	); err != nil {
		return nil, false, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE projects
		SET version = version + 1, status = 'paused',
			current_focus_artifact_version_id = ?, updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		"作品写锁已经变化。",
		focusVersionID, formatTime(now), run.ProjectID, runID,
	); err != nil {
		return nil, false, err
	}
	return &nextStepRunID, false, nil
}

func completedWorkflowTransitionTx(ctx context.Context, tx *sql.Tx, stepRunID string, step capability.CompiledStep, facts ...string) (string, error) {
	conditions := append([]string{"completed"}, facts...)
	if step.Batch != nil {
		var total, succeeded int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*), COUNT(CASE WHEN status = 'succeeded' THEN 1 END)
			FROM task_items WHERE step_run_id = ?`, stepRunID).Scan(&total, &succeeded); err != nil {
			return "", err
		}
		if total > 0 && total == succeeded {
			conditions = append(conditions, "all_batch_items_succeeded")
		}
	}
	for _, transition := range step.Next {
		if transition.When != "incomplete_material_confirmed" || containsString(conditions, transition.When) {
			continue
		}
		var runID string
		if err := tx.QueryRowContext(ctx, `SELECT run_id FROM step_runs WHERE step_run_id = ?`, stepRunID).Scan(&runID); err != nil {
			return "", err
		}
		incomplete, err := sourceIncompleteMaterialConfirmedTx(ctx, tx, runID)
		if err != nil {
			return "", err
		}
		if step.Batch != nil {
			_, partial, err := partialBatchContinuationForStepTx(ctx, tx, runID, stepRunID, nil)
			if err != nil {
				return "", err
			}
			incomplete = incomplete || partial
		}
		if incomplete {
			conditions = append(conditions, transition.When)
		}
		break
	}
	return selectWorkflowTransition(step, conditions...)
}

// Facts come from the committing Runtime command, never from model-authored text.
func selectWorkflowTransition(step capability.CompiledStep, facts ...string) (string, error) {
	next := ""
	for _, transition := range step.Next {
		if !containsString(facts, transition.When) {
			continue
		}
		if transition.To == "" || next != "" && next != transition.To {
			return "", domainError("WORKFLOW_TRANSITION_AMBIGUOUS", "当前事实匹配多个不同后继或无效后继，无法推进工作流。")
		}
		next = transition.To
	}
	if next == "" {
		return "", domainError("WORKFLOW_TRANSITION_UNMATCHED", "当前步骤没有与已确认事实匹配的后继，无法推进工作流。")
	}
	return next, nil
}

func currentRunInputsForStepTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runID string,
	inputRefs []capability.ArtifactRef,
) ([]map[string]any, error) {
	result := make([]map[string]any, 0, len(inputRefs))
	for _, inputRef := range inputRefs {
		if inputRef.Cardinality != "one" && inputRef.Cardinality != "many" {
			return nil, domainError(
				"OUTPUT_COMMIT_UNSUPPORTED",
				"下一步骤输入基数当前不受支持。",
			)
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT a.artifact_id, av.artifact_version_id, av.version, av.status,
				a.scope_key
			FROM artifacts a
			JOIN artifact_versions av
				ON av.artifact_version_id = a.current_version_id
			WHERE a.project_id = ? AND a.run_id = ?
				AND a.artifact_type = ? AND av.status = 'confirmed'
			ORDER BY
				CASE
					WHEN a.scope_key LIKE 'episode:%'
					THEN CAST(SUBSTR(a.scope_key, 9) AS INTEGER)
					ELSE 2147483647
				END ASC,
				a.scope_key ASC,
				a.artifact_id ASC`,
			projectID, runID, inputRef.ArtifactType,
		)
		if err != nil {
			return nil, err
		}
		var matches []map[string]any
		for rows.Next() {
			var artifactID, artifactVersionID, status, scopeKey string
			var version int
			if err := rows.Scan(
				&artifactID, &artifactVersionID, &version, &status, &scopeKey,
			); err != nil {
				rows.Close()
				return nil, err
			}
			matches = append(matches, map[string]any{
				"artifact_id":         artifactID,
				"artifact_version_id": artifactVersionID,
				"version":             version,
				"status":              status,
				"scope_key":           scopeKey,
			})
			if inputRef.Cardinality == "one" {
				break
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, domainError(
				"DEPENDENCY_INCOMPLETE",
				fmt.Sprintf("下一步骤缺少当前有效的 %s 输入。", inputRef.ArtifactType),
			)
		}
		if inputRef.Cardinality == "one" {
			result = append(result, matches[0])
		} else {
			result = append(result, matches...)
		}
	}
	return result, nil
}

func lineageInputsForStepTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runID string,
	confirmedVersionIDs []string,
	inputRefs []capability.ArtifactRef,
) ([]map[string]any, error) {
	if len(confirmedVersionIDs) == 0 {
		return nil, domainError(
			"DEPENDENCY_INCOMPLETE",
			"下一步骤缺少依赖链起点。",
		)
	}
	seedQueries := make([]string, 0, len(confirmedVersionIDs))
	seedArguments := make([]any, 0, len(confirmedVersionIDs)*2)
	for index, versionID := range confirmedVersionIDs {
		seedQueries = append(seedQueries, "SELECT ?, 0, ?")
		seedArguments = append(seedArguments, versionID, index+1)
	}
	result := make([]map[string]any, 0, len(inputRefs))
	for _, inputRef := range inputRefs {
		if inputRef.Cardinality != "one" && inputRef.Cardinality != "many" {
			return nil, domainError(
				"OUTPUT_COMMIT_UNSUPPORTED",
				"下一步骤输入基数当前不受支持。",
			)
		}
		query := `
			WITH RECURSIVE ancestors(version_id, depth, root_order) AS (
				` + strings.Join(seedQueries, " UNION ALL ") + `
				UNION
				SELECT d.upstream_ref_id, ancestors.depth + 1, ancestors.root_order
				FROM artifact_dependencies d
				JOIN ancestors
					ON d.downstream_artifact_version_id = ancestors.version_id
				WHERE d.upstream_kind = 'artifact_version'
					AND ancestors.depth < 32
			)
			SELECT a.artifact_id, av.artifact_version_id, av.version, av.status,
				a.scope_key, MIN(ancestors.depth), MIN(ancestors.root_order)
			FROM ancestors
			JOIN artifact_versions av ON av.artifact_version_id = ancestors.version_id
			JOIN artifacts a ON a.artifact_id = av.artifact_id
			WHERE a.project_id = ? AND a.run_id = ?
				AND a.artifact_type = ? AND av.status = 'confirmed'
			GROUP BY a.artifact_id, av.artifact_version_id, av.version, av.status, a.scope_key
			ORDER BY MIN(ancestors.depth) ASC, MIN(ancestors.root_order) ASC,
				a.scope_key ASC, a.artifact_id ASC`
		arguments := append([]any{}, seedArguments...)
		arguments = append(arguments, projectID, runID, inputRef.ArtifactType)
		rows, err := tx.QueryContext(ctx, query, arguments...)
		if err != nil {
			return nil, err
		}
		var matches []map[string]any
		for rows.Next() {
			var artifactID, artifactVersionID, status, scopeKey string
			var version, depth, rootOrder int
			if err := rows.Scan(
				&artifactID,
				&artifactVersionID,
				&version,
				&status,
				&scopeKey,
				&depth,
				&rootOrder,
			); err != nil {
				rows.Close()
				return nil, err
			}
			matches = append(matches, map[string]any{
				"artifact_id":         artifactID,
				"artifact_version_id": artifactVersionID,
				"version":             version,
				"status":              status,
				"scope_key":           scopeKey,
			})
			if inputRef.Cardinality == "one" {
				break
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, domainError(
				"DEPENDENCY_INCOMPLETE",
				fmt.Sprintf(
					"下一步骤缺少与当前版本同一依赖链的 %s 输入。",
					inputRef.ArtifactType,
				),
			)
		}
		if inputRef.Cardinality == "one" {
			result = append(result, matches[0])
		} else {
			result = append(result, matches...)
		}
	}
	return result, nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
