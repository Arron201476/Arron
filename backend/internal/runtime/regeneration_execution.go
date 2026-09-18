package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type artifactCommitTarget struct {
	ArtifactID          string
	ArtifactVersionID   string
	Version             int
	BaseVersionID       *string
	CreationReason      string
	CreatedArtifact     bool
	RegenerationPlanID  *string
	RegenerationGroupID *string
	ReplacedVersionID   *string
}

type regenerationStepBinding struct {
	PlanID          string
	GroupID         string
	GroupOrder      int
	PlanStatus      string
	CurrentOrder    int
	GroupStatus     string
	StaleVersionIDs []string
}

func (s *Store) prepareScopedRegenerationTargetsTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runID string,
	stepRunID string,
	scopeKey string,
	artifactTypes []string,
) (map[string]artifactCommitTarget, *regenerationStepBinding, error) {
	binding, err := regenerationBindingForStepTx(ctx, tx, stepRunID)
	if err != nil || binding == nil {
		return nil, binding, err
	}
	if binding.PlanStatus != "running" ||
		binding.GroupStatus != "running" ||
		binding.GroupOrder != binding.CurrentOrder {
		return nil, nil, domainError(
			"REGENERATION_PLAN_CONFLICT",
			"当前重生成组已经变化，执行结果不能提交。",
		)
	}
	expected := make(map[string]bool, len(artifactTypes))
	for _, artifactType := range artifactTypes {
		expected[artifactType] = true
	}
	targets := make(map[string]artifactCommitTarget, len(artifactTypes))
	for _, staleVersionID := range binding.StaleVersionIDs {
		var artifactID, currentVersionID, artifactType, candidateScope, status string
		var version int
		err := tx.QueryRowContext(ctx, `
			SELECT a.artifact_id, a.current_version_id, a.artifact_type, a.scope_key,
				av.version, av.status
			FROM artifact_versions av
			JOIN artifacts a ON a.artifact_id = av.artifact_id
			WHERE av.artifact_version_id = ?
				AND a.project_id = ? AND a.run_id = ?`,
			staleVersionID, projectID, runID,
		).Scan(
			&artifactID, &currentVersionID, &artifactType, &candidateScope,
			&version, &status,
		)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if !expected[artifactType] || candidateScope != scopeKey {
			continue
		}
		if _, exists := targets[artifactType]; exists {
			return nil, nil, domainError(
				"OUTPUT_COMMIT_UNSUPPORTED",
				"当前重生成任务包含重复的同类型作用域产物。",
			)
		}
		if currentVersionID != staleVersionID || status != "stale" {
			return nil, nil, domainError(
				"REGENERATION_PLAN_CONFLICT",
				"待替代产物已经变化，请重新生成影响预览。",
			)
		}
		planID, groupID, baseVersionID := binding.PlanID, binding.GroupID, staleVersionID
		targets[artifactType] = artifactCommitTarget{
			ArtifactID:          artifactID,
			ArtifactVersionID:   s.newID("av"),
			Version:             version + 1,
			BaseVersionID:       &baseVersionID,
			CreationReason:      "regeneration",
			RegenerationPlanID:  &planID,
			RegenerationGroupID: &groupID,
			ReplacedVersionID:   &baseVersionID,
		}
	}
	for _, artifactType := range artifactTypes {
		if _, ok := targets[artifactType]; !ok {
			return nil, nil, domainError(
				"REGENERATION_PLAN_CONFLICT",
				"当前重生成组缺少待替代的作用域产物。",
			)
		}
	}
	return targets, binding, nil
}

func (s *Store) prepareArtifactCommitTargetTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runID string,
	stepRunID string,
	capabilityID string,
	artifactType string,
	payload json.RawMessage,
	attemptID string,
	now time.Time,
) (artifactCommitTarget, error) {
	binding, err := regenerationBindingForStepTx(ctx, tx, stepRunID)
	if err != nil {
		return artifactCommitTarget{}, err
	}
	if binding == nil {
		artifactID := s.newID("art")
		versionID := s.newID("av")
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifacts(
				artifact_id, project_id, run_id, step_run_id, capability_id, artifact_type,
				scope_key, current_version_id, created_at, updated_at
			) VALUES(?, ?, ?, ?, ?, ?, 'singleton', ?, ?, ?)`,
			artifactID, projectID, runID, stepRunID, capabilityID, artifactType,
			versionID, formatTime(now), formatTime(now),
		); err != nil {
			if isUniqueConstraint(err) {
				return artifactCommitTarget{}, domainError(
					"ARTIFACT_COMMIT_CONFLICT",
					"该步骤的正式产物已存在。",
				)
			}
			return artifactCommitTarget{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifact_versions(
				artifact_version_id, artifact_id, version, status, payload_json, schema_id,
				schema_version, created_by_kind, actor_ref, creation_reason, created_at
			) VALUES(?, ?, 1, 'pending_approval', ?, ?, '1.0.0', 'model', ?, 'initial', ?)`,
			versionID, artifactID, string(payload), artifactType, attemptID, formatTime(now),
		); err != nil {
			return artifactCommitTarget{}, err
		}
		return artifactCommitTarget{
			ArtifactID:        artifactID,
			ArtifactVersionID: versionID,
			Version:           1,
			CreationReason:    "initial",
			CreatedArtifact:   true,
		}, nil
	}

	if binding.PlanStatus != "running" ||
		binding.GroupStatus != "running" ||
		binding.GroupOrder != binding.CurrentOrder {
		return artifactCommitTarget{}, domainError(
			"REGENERATION_PLAN_CONFLICT",
			"当前重生成组已经变化，执行结果不能提交。",
		)
	}

	var matchedArtifactID, matchedVersionID string
	var matchedVersion int
	for _, staleVersionID := range binding.StaleVersionIDs {
		var artifactID, currentVersionID, candidateType, status string
		var version int
		err := tx.QueryRowContext(ctx, `
			SELECT a.artifact_id, a.current_version_id, a.artifact_type,
				av.version, av.status
			FROM artifact_versions av
			JOIN artifacts a ON a.artifact_id = av.artifact_id
			WHERE av.artifact_version_id = ?
				AND a.project_id = ? AND a.run_id = ?`,
			staleVersionID, projectID, runID,
		).Scan(&artifactID, &currentVersionID, &candidateType, &version, &status)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return artifactCommitTarget{}, err
		}
		if candidateType != artifactType {
			continue
		}
		if matchedArtifactID != "" {
			return artifactCommitTarget{}, domainError(
				"OUTPUT_COMMIT_UNSUPPORTED",
				"当前重生成提交暂不支持一个任务同时替代多个同类型产物。",
			)
		}
		if currentVersionID != staleVersionID || status != "stale" {
			return artifactCommitTarget{}, domainError(
				"REGENERATION_PLAN_CONFLICT",
				"待替代产物已经变化，请重新生成影响预览。",
			)
		}
		matchedArtifactID = artifactID
		matchedVersionID = staleVersionID
		matchedVersion = version
	}
	if matchedArtifactID == "" {
		return artifactCommitTarget{}, domainError(
			"REGENERATION_PLAN_CONFLICT",
			"当前重生成组没有可由该任务替代的产物。",
		)
	}

	newVersionID := s.newID("av")
	newVersion := matchedVersion + 1
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason, base_version_id,
			created_at
		) VALUES(?, ?, ?, 'pending_approval', ?, ?, '1.0.0', 'model', ?,
			'regeneration', ?, ?)`,
		newVersionID, matchedArtifactID, newVersion, string(payload), artifactType,
		attemptID, matchedVersionID, formatTime(now),
	); err != nil {
		return artifactCommitTarget{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE artifacts SET current_version_id = ?, step_run_id = ?, updated_at = ?
		WHERE artifact_id = ? AND current_version_id = ?`,
		"待替代产物已经变化。",
		newVersionID, stepRunID, formatTime(now), matchedArtifactID, matchedVersionID,
	); err != nil {
		return artifactCommitTarget{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE artifact_versions SET status = 'superseded'
		WHERE artifact_version_id = ? AND status = 'stale'`,
		"待替代版本已经变化。",
		matchedVersionID,
	); err != nil {
		return artifactCommitTarget{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE regeneration_plan_groups SET status = 'waiting_approval'
		WHERE regeneration_plan_group_id = ? AND status = 'running'`,
		"当前重生成组已经变化。",
		binding.GroupID,
	); err != nil {
		return artifactCommitTarget{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE regeneration_plans SET status = 'waiting_approval'
		WHERE regeneration_plan_id = ? AND status = 'running'
			AND current_group_order = ?`,
		"当前重生成计划已经变化。",
		binding.PlanID, binding.GroupOrder,
	); err != nil {
		return artifactCommitTarget{}, err
	}

	planID := binding.PlanID
	groupID := binding.GroupID
	baseVersionID := matchedVersionID
	return artifactCommitTarget{
		ArtifactID:          matchedArtifactID,
		ArtifactVersionID:   newVersionID,
		Version:             newVersion,
		BaseVersionID:       &baseVersionID,
		CreationReason:      "regeneration",
		RegenerationPlanID:  &planID,
		RegenerationGroupID: &groupID,
		ReplacedVersionID:   &baseVersionID,
	}, nil
}

func regenerationBindingForStepTx(
	ctx context.Context,
	tx *sql.Tx,
	stepRunID string,
) (*regenerationStepBinding, error) {
	var result regenerationStepBinding
	var staleJSON string
	err := tx.QueryRowContext(ctx, `
		SELECT rp.regeneration_plan_id, rpg.regeneration_plan_group_id,
			rpg.group_order, rp.status, rp.current_group_order, rpg.status,
			rpg.stale_version_ids_json
		FROM regeneration_plan_groups rpg
		JOIN regeneration_plans rp
			ON rp.regeneration_plan_id = rpg.regeneration_plan_id
		WHERE rpg.step_run_id = ?`,
		stepRunID,
	).Scan(
		&result.PlanID,
		&result.GroupID,
		&result.GroupOrder,
		&result.PlanStatus,
		&result.CurrentOrder,
		&result.GroupStatus,
		&staleJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(staleJSON), &result.StaleVersionIDs); err != nil {
		return nil, err
	}
	return &result, nil
}
