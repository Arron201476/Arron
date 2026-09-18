package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"content-agent/backend/internal/capability"
)

func (s *Store) commitDirectSkillBundleTx(ctx context.Context, tx *sql.Tx, attemptID, taskID, runID, projectID, stepRunID, capabilityID string,
	step capability.CompiledStep, pack StepExecutionContextPack, response json.RawMessage, now time.Time) (ArtifactCommitResult, error) {
	if step.Batch != nil || len(step.OutputRefs) < 2 || len(pack.OutputContracts) != len(step.OutputRefs) {
		return ArtifactCommitResult{}, domainError("CONTEXT_LINEAGE_CONFLICT", "Skill 多产物输出缺少完整的单任务合同。")
	}
	var taskKey string
	if err := tx.QueryRowContext(ctx, `SELECT item_key FROM task_items WHERE task_item_id=? AND step_run_id=? AND run_id=?`,
		taskID, stepRunID, runID).Scan(&taskKey); err != nil {
		return ArtifactCommitResult{}, err
	}
	if taskKey != "step:"+step.ID || pack.Target.ScopeKey != taskKey {
		return ArtifactCommitResult{}, domainError("CONTEXT_LINEAGE_CONFLICT", "Skill 多产物任务与步骤作用域不一致。")
	}
	types := make([]string, 0, len(step.OutputRefs))
	for i, output := range step.OutputRefs {
		contract := pack.OutputContracts[i]
		if output.Cardinality != "one" || output.InitialStatus != "pending_approval" ||
			contract.ArtifactType != output.ArtifactType || contract.SchemaRef != output.SchemaRef {
			return ArtifactCommitResult{}, domainError("CONTEXT_LINEAGE_CONFLICT", "Skill 多产物合同与冻结步骤声明不一致。")
		}
		types = append(types, output.ArtifactType)
	}
	if err := validateDirectSkillResponse(pack, response); err != nil {
		return ArtifactCommitResult{}, domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", fmt.Sprintf("Skill 多产物输出未通过合同校验：%v", err))
	}
	var payloads map[string]json.RawMessage
	if err := json.Unmarshal(response, &payloads); err != nil {
		return ArtifactCommitResult{}, err
	}
	for _, output := range step.OutputRefs {
		payload, err := normalizeJSON(payloads[output.ArtifactType])
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		payload, err = normalizeAndValidateArtifactOutput(step, output, pack, payload)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		payloads[output.ArtifactType] = payload
	}
	normalized, err := json.Marshal(payloads)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if err := validateDirectSkillResponse(pack, normalized); err != nil {
		return ArtifactCommitResult{}, domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", fmt.Sprintf("规范化后的 Skill 多产物输出未通过合同校验：%v", err))
	}
	// Replacements share one running group until every output is stored.
	targets, binding, err := s.prepareScopedRegenerationTargetsTx(ctx, tx, projectID, runID, stepRunID, "singleton", types)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if targets == nil {
		targets = make(map[string]artifactCommitTarget, len(types))
		for _, artifactType := range types {
			targets[artifactType] = artifactCommitTarget{
				ArtifactID: s.newID("art"), ArtifactVersionID: s.newID("av"), Version: 1,
				CreationReason: "initial", CreatedArtifact: true,
			}
		}
	}
	for _, output := range step.OutputRefs {
		target := targets[output.ArtifactType]
		if err := s.storeDirectSkillBundleOutputTx(ctx, tx, projectID, runID, stepRunID, capabilityID, attemptID,
			output.ArtifactType, target, payloads[output.ArtifactType], pack, now); err != nil {
			return ArtifactCommitResult{}, err
		}
	}
	primary := targets[types[0]].ArtifactVersionID
	if err := updateExactlyOne(ctx, tx, `UPDATE execution_attempts SET status='succeeded', ended_at=?
		WHERE attempt_id=? AND status='result_received'`, "执行尝试状态已变化。", formatTime(now), attemptID); err != nil {
		return ArtifactCommitResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE task_items SET status='succeeded', output_artifact_version_id=?,
		failure=NULL, ended_at=?, updated_at=? WHERE task_item_id=? AND status='running' AND current_attempt_id=?`,
		"执行任务状态已变化。", primary, formatTime(now), formatTime(now), taskID, attemptID); err != nil {
		return ArtifactCommitResult{}, err
	}
	if binding != nil {
		if err := updateExactlyOne(ctx, tx, `UPDATE regeneration_plan_groups SET status='waiting_approval'
			WHERE regeneration_plan_group_id=? AND status='running'`, "当前重生成组已经变化。", binding.GroupID); err != nil {
			return ArtifactCommitResult{}, err
		}
		if err := updateExactlyOne(ctx, tx, `UPDATE regeneration_plans SET status='waiting_approval'
			WHERE regeneration_plan_id=? AND status='running' AND current_group_order=?`,
			"当前重生成计划已经变化。", binding.PlanID, binding.GroupOrder); err != nil {
			return ArtifactCommitResult{}, err
		}
	}
	approval, err := s.createArtifactVersionSetApprovalTx(ctx, tx, projectID, runID, stepRunID, types[0], step, now)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE projects SET version=version+1, current_focus_artifact_version_id=?, updated_at=?
		WHERE project_id=? AND active_write_run_id=?`, "作品写锁已变化。", primary, formatTime(now), projectID, runID); err != nil {
		return ArtifactCommitResult{}, err
	}
	if err := s.markStepWaitingApprovalTx(ctx, tx, projectID, runID, stepRunID, approval, now); err != nil {
		return ArtifactCommitResult{}, err
	}
	if _, err := s.appendEvent(ctx, tx, projectID, &runID, &stepRunID, "task.completed", "task_item", taskID,
		map[string]any{"attempt_id": attemptID, "output_count": len(types)}); err != nil {
		return ArtifactCommitResult{}, err
	}
	return s.committedDirectSkillBundleResultTx(ctx, tx, attemptID, runID, pack.OutputContracts)
}

func (s *Store) committedDirectSkillBundleResultTx(ctx context.Context, tx *sql.Tx, attemptID, runID string, contracts []ContextOutputContract) (ArtifactCommitResult, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.artifact_type, av.artifact_version_id
		FROM artifact_versions av JOIN artifacts a ON a.artifact_id=av.artifact_id
		WHERE av.actor_ref=? AND av.created_by_kind='model' AND a.run_id=?`, attemptID, runID)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	versions := map[string]string{}
	for rows.Next() {
		var artifactType, versionID string
		if err := rows.Scan(&artifactType, &versionID); err != nil {
			rows.Close()
			return ArtifactCommitResult{}, err
		}
		if versions[artifactType] != "" {
			rows.Close()
			return ArtifactCommitResult{}, domainError("ATTEMPT_RESULT_CHANGED", "执行回执包含重复类型的产物版本。")
		}
		versions[artifactType] = versionID
	}
	readErr := rows.Err()
	if err := rows.Close(); err != nil {
		return ArtifactCommitResult{}, err
	}
	if readErr != nil {
		return ArtifactCommitResult{}, readErr
	}
	if len(contracts) < 2 || len(versions) != len(contracts) || versions[contracts[0].ArtifactType] == "" {
		return ArtifactCommitResult{}, domainError("ATTEMPT_RESULT_CHANGED", "执行回执的完整产物版本集合已变化。")
	}
	result, err := s.committedArtifactResultTx(ctx, tx, versions[contracts[0].ArtifactType], runID)
	if err != nil {
		return ArtifactCommitResult{}, err
	}
	for _, contract := range contracts {
		versionID := versions[contract.ArtifactType]
		if versionID == "" {
			return ArtifactCommitResult{}, domainError("ATTEMPT_RESULT_CHANGED", "执行回执缺少声明的产物版本。")
		}
		version, err := getArtifactVersionQuery(ctx, tx, versionID)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		artifact, err := getArtifactQuery(ctx, tx, version.ArtifactID)
		if err != nil {
			return ArtifactCommitResult{}, err
		}
		result.Outputs = append(result.Outputs, ArtifactCommitOutput{Artifact: artifact, ArtifactVersion: version})
	}
	return result, nil
}

func (s *Store) storeDirectSkillBundleOutputTx(ctx context.Context, tx *sql.Tx, projectID, runID, stepRunID, capabilityID, attemptID, artifactType string,
	target artifactCommitTarget, payload json.RawMessage, pack StepExecutionContextPack, now time.Time) error {
	if target.CreatedArtifact {
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(artifact_id, project_id, run_id, step_run_id, capability_id,
			artifact_type, scope_key, current_version_id, created_at, updated_at) VALUES(?,?,?,?,?,?,'singleton',?,?,?)`,
			target.ArtifactID, projectID, runID, stepRunID, capabilityID, artifactType, target.ArtifactVersionID, formatTime(now), formatTime(now)); err != nil {
			if isUniqueConstraint(err) {
				return domainError("ARTIFACT_COMMIT_CONFLICT", "该步骤的正式产物已存在。")
			}
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_versions(artifact_version_id, artifact_id, version, status, payload_json,
		schema_id, schema_version, created_by_kind, actor_ref, creation_reason, base_version_id, created_at)
		VALUES(?,?,?,'pending_approval',?,?,'1.0.0','model',?,?,?,?)`, target.ArtifactVersionID, target.ArtifactID, target.Version,
		string(payload), artifactType, attemptID, target.CreationReason, target.BaseVersionID, formatTime(now)); err != nil {
		return err
	}
	if !target.CreatedArtifact {
		if err := updateExactlyOne(ctx, tx, `UPDATE artifacts SET current_version_id=?, step_run_id=?, updated_at=?
			WHERE artifact_id=? AND current_version_id=?`, "待替代产物已经变化。",
			target.ArtifactVersionID, stepRunID, formatTime(now), target.ArtifactID, target.ReplacedVersionID); err != nil {
			return err
		}
		if err := updateExactlyOne(ctx, tx, `UPDATE artifact_versions SET status='superseded'
			WHERE artifact_version_id=? AND status='stale'`, "待替代版本已经变化。", target.ReplacedVersionID); err != nil {
			return err
		}
	}
	for _, upstream := range pack.UpstreamContext {
		if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
			ProjectID: projectID, RunID: runID, DownstreamArtifactVersionID: target.ArtifactVersionID,
			UpstreamKind: "artifact_version", UpstreamRefID: upstream.ArtifactVersionID, Relation: "derived_from",
			UpstreamScope:   dependencyScope(scopeKind(upstream.ScopeKey), upstream.ScopeKey),
			DownstreamScope: dependencyScope("artifact", "singleton"), ImpactPolicyID: "whole_downstream",
		}, now); err != nil {
			return err
		}
	}
	if err := s.insertTaskContextDependenciesTx(ctx, tx, projectID, runID, target.ArtifactVersionID, "singleton", pack, true, now); err != nil {
		return err
	}
	if target.CreatedArtifact {
		if _, err := s.appendEvent(ctx, tx, projectID, &runID, &stepRunID, "artifact.created", "artifact", target.ArtifactID,
			map[string]any{"artifact_type": artifactType, "scope_key": "singleton"}); err != nil {
			return err
		}
	} else {
		if _, err := s.appendEvent(ctx, tx, projectID, &runID, &stepRunID, "artifact.replacement_created", "artifact_version", target.ArtifactVersionID,
			map[string]any{"regeneration_plan_id": target.RegenerationPlanID, "replaced_version_id": target.ReplacedVersionID}); err != nil {
			return err
		}
	}
	_, err := s.appendEvent(ctx, tx, projectID, &runID, &stepRunID, "artifact.version_created", "artifact_version", target.ArtifactVersionID,
		map[string]any{"version": target.Version, "creation_reason": target.CreationReason})
	return err
}
