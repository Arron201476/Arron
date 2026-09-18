package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"content-agent/backend/internal/capability"
)

func isScriptBundleStep(step capability.CompiledStep) bool {
	return step.ExecutorRef == "workflow.shared_script_generation" &&
		len(step.OutputRefs) == 2 &&
		step.OutputRefs[0].ArtifactType == "script_unit" &&
		step.OutputRefs[0].Cardinality == "many" &&
		step.OutputRefs[0].InitialStatus == "pending_approval" &&
		step.OutputRefs[1].ArtifactType == "script_handoff" &&
		step.OutputRefs[1].Cardinality == "many" &&
		step.OutputRefs[1].InitialStatus == "pending_approval" &&
		step.Approval.Type == "batch_checkpoint" &&
		step.Approval.Required
}

func (s *Store) commitScopedScriptBundleTx(
	ctx context.Context,
	tx *sql.Tx,
	attemptID string,
	taskItemID string,
	runID string,
	projectID string,
	stepRunID string,
	capabilityID string,
	scopeKey string,
	step capability.CompiledStep,
	contextPack StepExecutionContextPack,
	response json.RawMessage,
	now time.Time,
) (ArtifactCommitResult, error) {
	episodeNo, err := episodeNumberFromScope(scopeKey)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if len(contextPack.OutputContracts) != 2 ||
		contextPack.OutputContracts[0].ArtifactType != "script_unit" ||
		contextPack.OutputContracts[1].ArtifactType != "script_handoff" {
		return ArtifactCommitResult{}, domainError(
			"CONTEXT_LINEAGE_CONFLICT",
			"剧本生成缺少完整的双输出合同。",
		)
	}
	scriptPayload, err := s.contracts.AdaptAndValidate(
		*step.ResponseAdapterRef,
		step.OutputRefs[0].SchemaRef,
		"script_unit",
		response,
	)
	if err != nil {
		return ArtifactCommitResult{}, domainError(
			"OUTPUT_SCHEMA_VALIDATION_FAILED",
			fmt.Sprintf("剧本正文未通过产物合同校验：%v", err),
		)
	}
	handoffPayload, err := s.contracts.AdaptAndValidate(
		"adapter.legacy_script_handoff_to_v1",
		step.OutputRefs[1].SchemaRef,
		"script_handoff",
		response,
	)
	if err != nil {
		return ArtifactCommitResult{}, domainError(
			"OUTPUT_SCHEMA_VALIDATION_FAILED",
			fmt.Sprintf("剧本交接未通过产物合同校验：%v", err),
		)
	}
	var scriptIdentity struct {
		EpisodeNo int `json:"episode_no"`
	}
	var handoff map[string]any
	if err := json.Unmarshal(scriptPayload, &scriptIdentity); err != nil ||
		scriptIdentity.EpisodeNo != episodeNo {
		return ArtifactCommitResult{}, domainError(
			"BATCH_COVERAGE_INVALID",
			"剧本正文 episode_no 与 Task 作用域不一致。",
		)
	}
	if err := json.Unmarshal(handoffPayload, &handoff); err != nil {
		return ArtifactCommitResult{}, err
	}
	handoffEpisode, ok := jsonNumberAsInt(handoff["episode_no"])
	if !ok || handoffEpisode != episodeNo {
		return ArtifactCommitResult{}, domainError(
			"BATCH_COVERAGE_INVALID",
			"剧本交接 episode_no 与 Task 作用域不一致。",
		)
	}

	regenerationTargets, regenerationBinding, err := s.prepareScopedRegenerationTargetsTx(
		ctx,
		tx,
		projectID,
		runID,
		stepRunID,
		scopeKey,
		[]string{"script_unit", "script_handoff"},
	)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if regenerationTargets == nil {
		regenerationTargets = map[string]artifactCommitTarget{
			"script_unit": {
				ArtifactID:        s.newID("art"),
				ArtifactVersionID: s.newID("av"),
				Version:           1,
				CreationReason:    "initial",
				CreatedArtifact:   true,
			},
			"script_handoff": {
				ArtifactID:        s.newID("art"),
				ArtifactVersionID: s.newID("av"),
				Version:           1,
				CreationReason:    "initial",
				CreatedArtifact:   true,
			},
		}
	}
	scriptTarget := regenerationTargets["script_unit"]
	handoffTarget := regenerationTargets["script_handoff"]
	scriptVersionID := scriptTarget.ArtifactVersionID
	handoffVersionID := handoffTarget.ArtifactVersionID
	handoff["script_artifact_version_id"] = scriptVersionID
	handoffPayload, err = json.Marshal(handoff)
	if err != nil {
		return ArtifactCommitResult{}, err
	}

	outputs := []struct {
		target       artifactCommitTarget
		artifactType string
		payload      json.RawMessage
	}{
		{scriptTarget, "script_unit", scriptPayload},
		{handoffTarget, "script_handoff", handoffPayload},
	}
	for _, output := range outputs {
		if output.target.CreatedArtifact {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO artifacts(
					artifact_id, project_id, run_id, step_run_id, capability_id,
					artifact_type, scope_key, current_version_id, created_at, updated_at
				) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				output.target.ArtifactID,
				projectID,
				runID,
				stepRunID,
				capabilityID,
				output.artifactType,
				scopeKey,
				output.target.ArtifactVersionID,
				formatTime(now),
				formatTime(now),
			); err != nil {
				if isUniqueConstraint(err) {
					return ArtifactCommitResult{}, domainError(
						"ARTIFACT_COMMIT_CONFLICT",
						"该集剧本或交接产物已经存在。",
					)
				}
				return ArtifactCommitResult{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifact_versions(
				artifact_version_id, artifact_id, version, status, payload_json,
				schema_id, schema_version, created_by_kind, actor_ref,
				creation_reason, base_version_id, created_at
			) VALUES(?, ?, ?, 'pending_approval', ?, ?, '1.0.0',
				'model', ?, ?, ?, ?)`,
			output.target.ArtifactVersionID,
			output.target.ArtifactID,
			output.target.Version,
			string(output.payload),
			output.artifactType,
			attemptID,
			output.target.CreationReason,
			output.target.BaseVersionID,
			formatTime(now),
		); err != nil {
			return ArtifactCommitResult{}, err
		}
		if !output.target.CreatedArtifact {
			if err := updateExactlyOne(ctx, tx, `
				UPDATE artifacts SET current_version_id = ?, step_run_id = ?, updated_at = ?
				WHERE artifact_id = ? AND current_version_id = ?`,
				"待替代产物已经变化。",
				output.target.ArtifactVersionID,
				stepRunID,
				formatTime(now),
				output.target.ArtifactID,
				*output.target.ReplacedVersionID,
			); err != nil {
				return ArtifactCommitResult{}, err
			}
			if err := updateExactlyOne(ctx, tx, `
				UPDATE artifact_versions SET status = 'superseded'
				WHERE artifact_version_id = ? AND status = 'stale'`,
				"待替代版本已经变化。",
				*output.target.ReplacedVersionID,
			); err != nil {
				return ArtifactCommitResult{}, err
			}
		}
		for _, upstream := range contextPack.UpstreamContext {
			if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
				ProjectID:                   projectID,
				RunID:                       runID,
				DownstreamArtifactVersionID: output.target.ArtifactVersionID,
				UpstreamKind:                "artifact_version",
				UpstreamRefID:               upstream.ArtifactVersionID,
				Relation:                    "derived_from",
				UpstreamScope: dependencyScope(
					scopeKind(upstream.ScopeKey),
					upstream.ScopeKey,
				),
				DownstreamScope: dependencyScope("episode", scopeKey),
				ImpactPolicyID:  "scope_intersection",
			}, now); err != nil {
				return ArtifactCommitResult{}, err
			}
		}
	}
	if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
		ProjectID:                   projectID,
		RunID:                       runID,
		DownstreamArtifactVersionID: handoffVersionID,
		UpstreamKind:                "artifact_version",
		UpstreamRefID:               scriptVersionID,
		Relation:                    "describes",
		UpstreamScope:               dependencyScope("episode", scopeKey),
		DownstreamScope:             dependencyScope("episode", scopeKey),
		ImpactPolicyID:              "scope_intersection",
	}, now); err != nil {
		return ArtifactCommitResult{}, err
	}

	if err := updateExactlyOne(ctx, tx, `
		UPDATE execution_attempts SET status = 'succeeded', ended_at = ?
		WHERE attempt_id = ? AND status = 'result_received'`,
		"执行尝试状态已变化。",
		formatTime(now),
		attemptID,
	); err != nil {
		return ArtifactCommitResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE task_items
		SET status = 'succeeded', output_artifact_version_id = ?,
			failure = NULL, ended_at = ?, updated_at = ?
		WHERE task_item_id = ? AND status = 'running' AND current_attempt_id = ?`,
		"执行任务状态已变化。",
		scriptVersionID,
		formatTime(now),
		formatTime(now),
		taskItemID,
		attemptID,
	); err != nil {
		return ArtifactCommitResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET version = version + 1, current_focus_artifact_version_id = ?, updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		scriptVersionID,
		formatTime(now),
		projectID,
		runID,
	); err != nil {
		return ArtifactCommitResult{}, err
	}

	runRef, stepRef := runID, stepRunID
	for _, output := range outputs {
		if output.target.CreatedArtifact {
			if _, err := s.appendEvent(
				ctx,
				tx,
				projectID,
				&runRef,
				&stepRef,
				"artifact.created",
				"artifact",
				output.target.ArtifactID,
				map[string]any{
					"artifact_type": output.artifactType,
					"scope_key":     scopeKey,
				},
			); err != nil {
				return ArtifactCommitResult{}, err
			}
		} else if _, err := s.appendEvent(
			ctx,
			tx,
			projectID,
			&runRef,
			&stepRef,
			"artifact.replacement_created",
			"artifact_version",
			output.target.ArtifactVersionID,
			map[string]any{
				"regeneration_plan_id": output.target.RegenerationPlanID,
				"replaced_version_id":  output.target.ReplacedVersionID,
				"scope_key":            scopeKey,
			},
		); err != nil {
			return ArtifactCommitResult{}, err
		}
		if _, err := s.appendEvent(
			ctx,
			tx,
			projectID,
			&runRef,
			&stepRef,
			"artifact.version_created",
			"artifact_version",
			output.target.ArtifactVersionID,
			map[string]any{
				"version":         output.target.Version,
				"creation_reason": output.target.CreationReason,
				"scope_key":       scopeKey,
			},
		); err != nil {
			return ArtifactCommitResult{}, err
		}
	}
	if _, err := s.appendEvent(
		ctx,
		tx,
		projectID,
		&runRef,
		&stepRef,
		"task.completed",
		"task_item",
		taskItemID,
		map[string]any{"attempt_id": attemptID, "scope_key": scopeKey},
	); err != nil {
		return ArtifactCommitResult{}, err
	}

	var totalTasks, completedTasks int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), SUM(CASE WHEN status = 'succeeded' THEN 1 ELSE 0 END)
		FROM task_items WHERE step_run_id = ?`,
		stepRunID,
	).Scan(&totalTasks, &completedTasks); err != nil {
		return ArtifactCommitResult{}, err
	}
	var approval Approval
	commitStatus := "task_artifact_saved"
	episodeMode, err := episodeExecutionModeForRun(ctx, tx, runID, nil)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if regenerationBinding == nil && episodeMode == episodeExecutionModeReviewEach {
		approval, err = s.createEpisodeCheckpointApprovalTx(
			ctx, tx, projectID, runID, stepRunID, scopeKey, "script_bundle", step, now,
		)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		if err := s.markStepWaitingApprovalTx(ctx, tx, projectID, runID, stepRunID, approval, now); err != nil {
			return ArtifactCommitResult{}, err
		}
		commitStatus = "waiting_approval"
	} else if totalTasks > 0 && completedTasks == totalTasks {
		if regenerationBinding != nil {
			if err := updateExactlyOne(ctx, tx, `
				UPDATE regeneration_plan_groups SET status = 'waiting_approval'
				WHERE regeneration_plan_group_id = ? AND status = 'running'`,
				"当前重生成组已经变化。",
				regenerationBinding.GroupID,
			); err != nil {
				return ArtifactCommitResult{}, err
			}
			if err := updateExactlyOne(ctx, tx, `
				UPDATE regeneration_plans SET status = 'waiting_approval'
				WHERE regeneration_plan_id = ? AND status = 'running'
					AND current_group_order = ?`,
				"当前重生成计划已经变化。",
				regenerationBinding.PlanID,
				regenerationBinding.GroupOrder,
			); err != nil {
				return ArtifactCommitResult{}, err
			}
		}
		approval, err = s.createArtifactVersionSetApprovalTx(
			ctx,
			tx,
			projectID,
			runID,
			stepRunID,
			"script_bundle",
			step,
			now,
		)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		if err := s.markStepWaitingApprovalTx(ctx, tx, projectID, runID, stepRunID, approval, now); err != nil {
			return ArtifactCommitResult{}, err
		}
		commitStatus = "waiting_approval"
	}

	commitOutputs := make([]ArtifactCommitOutput, 0, len(outputs))
	for _, output := range outputs {
		artifact, err := scanArtifact(tx.QueryRowContext(ctx, `
			SELECT artifact_id, project_id, COALESCE(run_id, ''), COALESCE(step_run_id, ''),
				origin_type, origin_id, capability_id,
				artifact_type, scope_key, current_version_id, created_at, updated_at
			FROM artifacts WHERE artifact_id = ?`,
			output.target.ArtifactID,
		))
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		version, err := scanArtifactVersion(tx.QueryRowContext(ctx, `
			SELECT artifact_version_id, artifact_id, version, status, payload_json,
				schema_id, schema_version, created_by_kind, actor_ref,
				creation_reason, base_version_id, created_at, confirmed_at
			FROM artifact_versions WHERE artifact_version_id = ?`,
			output.target.ArtifactVersionID,
		))
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		commitOutputs = append(commitOutputs, ArtifactCommitOutput{
			Artifact:        artifact,
			ArtifactVersion: version,
		})
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, runID)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	return ArtifactCommitResult{
		CommitStatus:    commitStatus,
		Artifact:        commitOutputs[0].Artifact,
		ArtifactVersion: commitOutputs[0].ArtifactVersion,
		Outputs:         commitOutputs,
		Approval:        approval,
		RunSnapshot:     snapshot,
	}, nil
}
