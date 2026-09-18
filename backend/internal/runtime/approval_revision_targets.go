package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
)

type ApprovalRevisionTarget struct {
	ArtifactID        string `json:"artifact_id"`
	ArtifactVersionID string `json:"artifact_version_id"`
	ArtifactType      string `json:"artifact_type"`
	ScopeKey          string `json:"scope_key"`
	Version           int    `json:"version"`
	Label             string `json:"label"`
}

type ApprovalRevisionTargets struct {
	Approval Approval                 `json:"approval"`
	Targets  []ApprovalRevisionTarget `json:"targets"`
}

func (s *Store) GetApprovalRevisionTargets(ctx context.Context, approvalID string) (ApprovalRevisionTargets, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApprovalRevisionTargets{}, err
	}
	defer tx.Rollback()
	approval, err := getApprovalTx(ctx, tx, approvalID)
	if err != nil {
		return ApprovalRevisionTargets{}, err
	}
	if err := authorizeApprovalMutationTx(ctx, tx, approval, ""); err != nil {
		return ApprovalRevisionTargets{}, err
	}
	if approval.Status != "pending" || !slices.Contains(approval.Options, "request_ai_revision") || approval.Scope == "quality_review" || approval.Scope == "final_selection" {
		return ApprovalRevisionTargets{}, domainError("APPROVAL_ACTION_NOT_ALLOWED", "当前确认请求不允许此返修操作。")
	}
	targets, err := s.approvalRevisionTargetsTx(ctx, tx, approval)
	if err != nil {
		return ApprovalRevisionTargets{}, err
	}
	result := ApprovalRevisionTargets{Approval: approval, Targets: make([]ApprovalRevisionTarget, 0, len(targets))}
	for _, target := range targets {
		result.Targets = append(result.Targets, ApprovalRevisionTarget{
			ArtifactID: target.Artifact.ArtifactID, ArtifactVersionID: target.CurrentVersion.ArtifactVersionID,
			ArtifactType: target.Artifact.ArtifactType, ScopeKey: target.Artifact.ScopeKey, Version: target.CurrentVersion.Version,
			Label: fmt.Sprintf("%s · %s · v%d", artifactDisplayLabel(target.Artifact.ArtifactType, target.CurrentVersion.Payload), scopeDisplay(target.Artifact.ScopeKey), target.CurrentVersion.Version),
		})
	}
	return result, nil
}

func (s *Store) approvalRevisionTargetsTx(ctx context.Context, tx *sql.Tx, approval Approval) ([]regenerationTarget, error) {
	targets, err := approvalRegenerationTargetsTx(ctx, tx, approval)
	if err != nil {
		return nil, err
	}
	changed := func() error {
		return domainError("APPROVAL_SUBJECT_CHANGED", "确认绑定的产物版本已经变化，请刷新后重试。")
	}
	if len(targets) == 0 {
		return nil, changed()
	}
	if approval.SubjectKind == "artifact_version" {
		target := targets[0]
		if len(targets) != 1 || target.CurrentVersion.Status != "pending_approval" || target.CurrentVersion.Version != approval.SubjectVersion || approval.SubjectSnapshotHash != approvalSubjectHash(target.CurrentVersion.ArtifactVersionID, target.CurrentVersion.Version) {
			return nil, changed()
		}
		return targets, nil
	}
	if approval.SubjectKind != "artifact_version_set" || approval.SubjectRefID != approval.StepRunID {
		return nil, changed()
	}
	byVersion := make(map[string]regenerationTarget, len(targets))
	for _, target := range targets {
		byVersion[target.CurrentVersion.ArtifactVersionID] = target
	}
	rows, err := tx.QueryContext(ctx, `SELECT artifact_version_id,item_order,scope_key FROM approval_subject_versions WHERE approval_request_id=? ORDER BY item_order`, approval.ApprovalRequestID)
	if err != nil {
		return nil, err
	}
	items := []approvalVersionSetItem{}
	for rows.Next() {
		var item approvalVersionSetItem
		if err := rows.Scan(&item.ArtifactVersionID, &item.ItemOrder, &item.ScopeKey); err != nil {
			rows.Close()
			return nil, err
		}
		target, ok := byVersion[item.ArtifactVersionID]
		if !ok || target.Artifact.ScopeKey != item.ScopeKey {
			rows.Close()
			return nil, changed()
		}
		item.ArtifactType = target.Artifact.ArtifactType
		items = append(items, item)
	}
	readErr := rows.Err()
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	hash, err := approvalVersionSetHash(items)
	if err != nil {
		return nil, err
	}
	if len(items) != len(targets) || hash != approval.SubjectSnapshotHash {
		return nil, changed()
	}
	step, err := s.compiledStepForRunTx(ctx, tx, approval.RunID, approval.StepRunID)
	if err != nil {
		return nil, err
	}
	if isScriptBundleStep(step) {
		// Legacy handoff files are derived from script edits, not independently revised.
		editable := make([]regenerationTarget, 0, len(targets))
		for _, target := range targets {
			if target.Artifact.ArtifactType == "script_unit" {
				editable = append(editable, target)
			}
		}
		targets = editable
	}
	return targets, nil
}
