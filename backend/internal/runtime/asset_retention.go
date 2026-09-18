package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

const (
	userDeletePolicyID = "user_delete_v1"
	userDeleteAction   = "delete_asset_source"
)

type AssetContent struct {
	Asset *Asset
	File  *os.File
}

func (s *Store) OpenAssetContent(ctx context.Context, assetID string) (AssetContent, error) {
	asset, err := s.GetAsset(ctx, assetID)
	if err != nil {
		return AssetContent{}, err
	}
	if activity, ok := AgentActivityFromContext(ctx); ok && activity.ProjectID != asset.ProjectID {
		return AssetContent{}, domainError("ASSET_NOT_FOUND", "材料不存在。")
	}
	if _, err := s.GetProject(ctx, asset.ProjectID); err != nil {
		return AssetContent{}, err
	}
	switch asset.Status {
	case "expired":
		return AssetContent{}, domainError("ASSET_SOURCE_EXPIRED", "材料源文件已经过期。")
	case "deleted":
		return AssetContent{}, domainError("ASSET_SOURCE_DELETED", "材料源文件已经删除。")
	}
	if asset.ExpiresAt != nil && !asset.ExpiresAt.After(s.now()) {
		return AssetContent{}, domainError("ASSET_SOURCE_EXPIRED", "材料源文件已经过期。")
	}
	if asset.Status != "available" && asset.Status != "processing" {
		return AssetContent{}, domainError("ASSET_SOURCE_UNAVAILABLE", "材料源文件当前不可用。")
	}
	var storageRef, blobStatus string
	if err := s.db.QueryRowContext(ctx, `
		SELECT storage_ref, status
		FROM asset_blobs
		WHERE blob_id = ? AND project_id = ? AND asset_id = ?`,
		asset.OriginalBlobID,
		asset.ProjectID,
		asset.AssetID,
	).Scan(&storageRef, &blobStatus); errors.Is(err, sql.ErrNoRows) {
		return AssetContent{}, domainError("ASSET_SOURCE_UNAVAILABLE", "材料源文件记录不存在。")
	} else if err != nil {
		return AssetContent{}, err
	}
	if blobStatus != "available" {
		return AssetContent{}, domainError("ASSET_SOURCE_UNAVAILABLE", "材料源文件当前不可用。")
	}
	path, err := resolveDataPath(s.dataRoot, storageRef)
	if err != nil {
		return AssetContent{}, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return AssetContent{}, domainError("ASSET_SOURCE_UNAVAILABLE", "材料源文件缺失。")
	}
	if err != nil {
		return AssetContent{}, err
	}
	return AssetContent{Asset: &asset, File: file}, nil
}

func (s *Store) ListRetentionJobs(ctx context.Context, assetID string) ([]RetentionJob, error) {
	if _, err := s.GetAsset(ctx, assetID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, retentionJobSelect+`
		WHERE asset_id = ?
		ORDER BY created_at ASC, retention_job_id ASC`,
		assetID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RetentionJob{}
	for rows.Next() {
		job, err := scanRetentionJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

func (s *Store) CreateAssetDeletePreview(
	ctx context.Context,
	command CreateAssetDeletePreviewCommand,
) (AssetDeletePreview, error) {
	if command.AssetID == "" {
		return AssetDeletePreview{}, domainError(
			"REQUEST_VALIDATION_FAILED",
			"删除预览缺少材料。",
		)
	}
	asset, err := s.GetAsset(ctx, command.AssetID)
	if err != nil {
		return AssetDeletePreview{}, err
	}
	if asset.Status == "deleted" {
		return AssetDeletePreview{}, domainError("ASSET_SOURCE_DELETED", "材料源文件已经删除。")
	}
	if command.Scope == "" {
		command.Scope = asset.ProjectID
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssetDeletePreview{}, err
	}
	defer tx.Rollback()
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return AssetDeletePreview{}, err
	}
	if hit {
		return decodeIdempotentResult[AssetDeletePreview](cached)
	}
	preview, checksum, err := s.buildAssetDeletePreviewTx(ctx, tx, command.AssetID)
	if err != nil {
		return AssetDeletePreview{}, err
	}
	preview.AssetDeletePreviewID = s.newID("adp")
	preview.Status = "pending"
	preview.CreatedAt = now
	if _, err := tx.ExecContext(ctx, `
		UPDATE asset_delete_previews
		SET status = 'expired', resolved_at = ?
		WHERE asset_id = ? AND status = 'pending'`,
		formatTime(now),
		command.AssetID,
	); err != nil {
		return AssetDeletePreview{}, err
	}
	runImpacts, err := json.Marshal(preview.ActiveRunImpacts)
	if err != nil {
		return AssetDeletePreview{}, err
	}
	artifactImpacts, err := json.Marshal(preview.ArtifactImpacts)
	if err != nil {
		return AssetDeletePreview{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO asset_delete_previews(
			asset_delete_preview_id, project_id, asset_id, asset_status, asset_checksum,
			active_run_impacts_json, artifact_impacts_json, existing_artifacts_preserved,
			snapshot_hash, status, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, 1, ?, 'pending', ?)`,
		preview.AssetDeletePreviewID,
		preview.ProjectID,
		preview.AssetID,
		preview.AssetStatus,
		checksum,
		string(runImpacts),
		string(artifactImpacts),
		preview.SnapshotHash,
		formatTime(now),
	); err != nil {
		return AssetDeletePreview{}, err
	}
	if _, err := s.appendEvent(
		ctx,
		tx,
		preview.ProjectID,
		nil,
		nil,
		"asset.delete_requested",
		"asset",
		preview.AssetID,
		map[string]any{
			"asset_delete_preview_id": preview.AssetDeletePreviewID,
			"snapshot_hash":           preview.SnapshotHash,
			"active_run_impacts":      len(preview.ActiveRunImpacts),
			"artifact_impacts":        len(preview.ArtifactImpacts),
		},
	); err != nil {
		return AssetDeletePreview{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, preview, now); err != nil {
		return AssetDeletePreview{}, err
	}
	if err := tx.Commit(); err != nil {
		return AssetDeletePreview{}, err
	}
	return preview, nil
}

func (s *Store) ConfirmAssetDelete(
	ctx context.Context,
	command ConfirmAssetDeleteCommand,
) (AssetDeleteResult, error) {
	if !command.Confirmed {
		return AssetDeleteResult{}, domainError(
			"REQUIRED_CONFIRMATION_MISSING",
			"删除材料前需要用户明确确认。",
		)
	}
	if command.AssetID == "" || command.PreviewHash == "" {
		return AssetDeleteResult{}, domainError(
			"REQUEST_VALIDATION_FAILED",
			"删除确认缺少材料或预览快照。",
		)
	}
	if command.ActorRef == "" {
		command.ActorRef = actorRefFromContext(ctx)
	}
	asset, err := s.GetAsset(ctx, command.AssetID)
	if err != nil {
		return AssetDeleteResult{}, err
	}
	if command.Scope == "" {
		command.Scope = asset.ProjectID
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssetDeleteResult{}, err
	}
	defer tx.Rollback()
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return AssetDeleteResult{}, err
	}
	if hit {
		return decodeIdempotentResult[AssetDeleteResult](cached)
	}
	stored, err := getPendingAssetDeletePreviewTx(
		ctx,
		tx,
		command.AssetID,
		command.PreviewHash,
	)
	if err != nil {
		return AssetDeleteResult{}, err
	}
	current, _, err := s.buildAssetDeletePreviewTx(ctx, tx, command.AssetID)
	if err != nil {
		return AssetDeleteResult{}, err
	}
	if current.SnapshotHash != stored.SnapshotHash {
		return AssetDeleteResult{}, domainError(
			"DELETE_PREVIEW_EXPIRED",
			"材料删除影响已经变化，请重新预览。",
		)
	}
	for _, impact := range current.ActiveRunImpacts {
		if impact.BlocksDelete {
			return AssetDeleteResult{}, domainError(
				"ASSET_DELETE_BLOCKED_BY_RUN",
				"活动任务仍在使用该材料，请先暂停或取消。",
			)
		}
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE assets
		SET status = 'deleted', deleted_at = ?, delete_reason = 'user_confirmed',
			updated_at = ?
		WHERE asset_id = ? AND status <> 'deleted'`,
		"材料状态已经变化，请重新预览。",
		formatTime(now),
		formatTime(now),
		command.AssetID,
	); err != nil {
		return AssetDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE asset_snapshots SET status = 'deleted'
		WHERE asset_id = ? AND status <> 'deleted'`,
		command.AssetID,
	); err != nil {
		return AssetDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE asset_blobs SET status = 'delete_pending'
		WHERE asset_id = ? AND status IN ('available', 'delete_failed')`,
		command.AssetID,
	); err != nil {
		return AssetDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE retention_jobs
		SET status = 'cancelled', worker_id = NULL, lease_until = NULL, updated_at = ?
		WHERE asset_id = ? AND action = 'expire_video_source'
			AND status IN ('scheduled', 'running', 'retry_wait')`,
		formatTime(now),
		command.AssetID,
	); err != nil {
		return AssetDeleteResult{}, err
	}
	job, err := s.createRetentionJobTx(
		ctx,
		tx,
		asset.ProjectID,
		command.AssetID,
		userDeletePolicyID,
		userDeleteAction,
		now,
		now,
	)
	if err != nil {
		return AssetDeleteResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE asset_delete_previews
		SET status = 'consumed', resolved_at = ?
		WHERE asset_delete_preview_id = ? AND status = 'pending'`,
		"删除预览已经失效。",
		formatTime(now),
		stored.AssetDeletePreviewID,
	); err != nil {
		return AssetDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE asset_delete_previews
		SET status = 'expired', resolved_at = ?
		WHERE asset_id = ? AND status = 'pending'`,
		formatTime(now),
		command.AssetID,
	); err != nil {
		return AssetDeleteResult{}, err
	}
	if _, err := s.appendEvent(
		ctx,
		tx,
		asset.ProjectID,
		nil,
		nil,
		"asset.delete_confirmed",
		"asset",
		command.AssetID,
		map[string]any{
			"retention_job_id":              job.RetentionJobID,
			"formal_artifacts_preserved":    true,
			"artifact_dependency_preserved": true,
			"actor_ref":                     command.ActorRef,
		},
	); err != nil {
		return AssetDeleteResult{}, err
	}
	deletedAsset, err := scanAsset(tx.QueryRowContext(
		ctx,
		assetSelect+" WHERE asset_id = ?",
		command.AssetID,
	))
	if err != nil {
		return AssetDeleteResult{}, err
	}
	result := AssetDeleteResult{Asset: deletedAsset, Job: job}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, result, now); err != nil {
		return AssetDeleteResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AssetDeleteResult{}, err
	}
	return result, nil
}

func (s *Store) RunRetentionSweep(
	ctx context.Context,
	workerID string,
	limit int,
	leaseSeconds int,
	maxAttempts int,
) (RetentionSweepResult, error) {
	if workerID == "" {
		return RetentionSweepResult{}, domainError(
			"REQUEST_VALIDATION_FAILED",
			"保留任务 Worker 缺少标识。",
		)
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if leaseSeconds < 5 {
		leaseSeconds = 60
	}
	if maxAttempts < 1 {
		maxAttempts = 5
	}
	result := RetentionSweepResult{}
	for range limit {
		job, err := s.claimRetentionJob(ctx, workerID, leaseSeconds)
		if err != nil {
			return result, err
		}
		if job == nil {
			break
		}
		result.Claimed++
		if err := s.prepareRetentionDeletion(ctx, *job); err != nil {
			failed, recordErr := s.recordRetentionFailure(ctx, *job, maxAttempts)
			if recordErr != nil {
				return result, recordErr
			}
			if failed {
				result.Failed++
			} else {
				result.Retried++
			}
			continue
		}
		if err := s.deleteRetentionBlobs(ctx, *job); err != nil {
			failed, recordErr := s.recordRetentionFailure(ctx, *job, maxAttempts)
			if recordErr != nil {
				return result, recordErr
			}
			if failed {
				result.Failed++
			} else {
				result.Retried++
			}
			continue
		}
		completed, err := s.completeRetentionJob(ctx, *job)
		if err != nil {
			return result, err
		}
		if completed {
			result.Completed++
		}
	}
	return result, nil
}

func (s *Store) createRetentionJobTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	assetID string,
	policyID string,
	action string,
	dueAt time.Time,
	now time.Time,
) (RetentionJob, error) {
	job := RetentionJob{
		RetentionJobID: s.newID("rtj"),
		ProjectID:      projectID,
		AssetID:        assetID,
		PolicyID:       policyID,
		Action:         action,
		DueAt:          dueAt,
		Status:         "scheduled",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO retention_jobs(
			retention_job_id, project_id, asset_id, policy_id, action, due_at,
			status, attempt_count, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, 'scheduled', 0, ?, ?)`,
		job.RetentionJobID,
		projectID,
		assetID,
		policyID,
		action,
		formatTime(dueAt),
		formatTime(now),
		formatTime(now),
	)
	if err == nil {
		return job, nil
	}
	if !isUniqueConstraint(err) {
		return RetentionJob{}, err
	}
	existing, queryErr := scanRetentionJob(tx.QueryRowContext(ctx, retentionJobSelect+`
		WHERE asset_id = ? AND policy_id = ? AND action = ? AND due_at = ?`,
		assetID,
		policyID,
		action,
		formatTime(dueAt),
	))
	return existing, queryErr
}

func (s *Store) buildAssetDeletePreviewTx(
	ctx context.Context,
	tx *sql.Tx,
	assetID string,
) (AssetDeletePreview, string, error) {
	asset, err := scanAsset(tx.QueryRowContext(ctx, assetSelect+" WHERE asset_id = ?", assetID))
	if errors.Is(err, sql.ErrNoRows) {
		return AssetDeletePreview{}, "", domainError("ASSET_NOT_FOUND", "材料不存在。")
	}
	if err != nil {
		return AssetDeletePreview{}, "", err
	}
	if asset.Status == "deleted" {
		return AssetDeletePreview{}, "", domainError("ASSET_SOURCE_DELETED", "材料源文件已经删除。")
	}
	runImpacts, err := assetRunImpactsTx(ctx, tx, asset)
	if err != nil {
		return AssetDeletePreview{}, "", err
	}
	artifactImpacts, err := assetArtifactImpactsTx(ctx, tx, asset)
	if err != nil {
		return AssetDeletePreview{}, "", err
	}
	previewForSort := AssetDeletePreview{
		ActiveRunImpacts: runImpacts,
		ArtifactImpacts:  artifactImpacts,
	}
	sortAssetDeleteImpacts(&previewForSort)
	runImpacts = previewForSort.ActiveRunImpacts
	artifactImpacts = previewForSort.ArtifactImpacts
	snapshot := struct {
		AssetID          string                      `json:"asset_id"`
		ProjectID        string                      `json:"project_id"`
		AssetStatus      string                      `json:"asset_status"`
		AssetChecksum    string                      `json:"asset_checksum"`
		ActiveRunImpacts []AssetDeleteRunImpact      `json:"active_run_impacts"`
		ArtifactImpacts  []AssetDeleteArtifactImpact `json:"artifact_impacts"`
	}{
		AssetID:          asset.AssetID,
		ProjectID:        asset.ProjectID,
		AssetStatus:      asset.Status,
		AssetChecksum:    asset.Checksum,
		ActiveRunImpacts: runImpacts,
		ArtifactImpacts:  artifactImpacts,
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return AssetDeletePreview{}, "", err
	}
	return AssetDeletePreview{
		ProjectID:                  asset.ProjectID,
		AssetID:                    asset.AssetID,
		AssetStatus:                asset.Status,
		ActiveRunImpacts:           runImpacts,
		ArtifactImpacts:            artifactImpacts,
		ExistingArtifactsPreserved: true,
		SnapshotHash:               sha256Hex(encoded),
	}, asset.Checksum, nil
}

func assetRunImpactsTx(
	ctx context.Context,
	tx *sql.Tx,
	asset Asset,
) ([]AssetDeleteRunImpact, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT r.run_id, r.status, risv.payload_json
		FROM runs r
		JOIN run_input_snapshot_versions risv
			ON risv.run_input_snapshot_version_id = r.current_input_snapshot_version_id
		WHERE r.project_id = ?
			AND r.status IN (
				'pending', 'running', 'waiting_approval',
				'pausing', 'paused', 'failed'
			)
		ORDER BY r.created_at ASC, r.run_id ASC`,
		asset.ProjectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AssetDeleteRunImpact{}
	for rows.Next() {
		var runID, status, payloadJSON string
		if err := rows.Scan(&runID, &status, &payloadJSON); err != nil {
			return nil, err
		}
		var payload any
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return nil, err
		}
		if !jsonContainsString(payload, asset.AssetID) {
			continue
		}
		result = append(result, AssetDeleteRunImpact{
			RunID:        runID,
			Status:       status,
			BlocksDelete: status != "paused" && status != "failed",
		})
	}
	return result, rows.Err()
}

func assetArtifactImpactsTx(
	ctx context.Context,
	tx *sql.Tx,
	asset Asset,
) ([]AssetDeleteArtifactImpact, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT
			d.downstream_artifact_version_id, a.artifact_type, a.scope_key
		FROM artifact_dependencies d
		JOIN asset_snapshots ass
			ON d.upstream_kind = 'asset_snapshot'
			AND d.upstream_ref_id = ass.asset_snapshot_id
		JOIN artifact_versions av
			ON av.artifact_version_id = d.downstream_artifact_version_id
		JOIN artifacts a ON a.artifact_id = av.artifact_id
		WHERE ass.asset_id = ? AND d.project_id = ?
		ORDER BY a.artifact_type ASC, a.scope_key ASC, d.downstream_artifact_version_id ASC`,
		asset.AssetID,
		asset.ProjectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AssetDeleteArtifactImpact{}
	for rows.Next() {
		var impact AssetDeleteArtifactImpact
		if err := rows.Scan(
			&impact.ArtifactVersionID,
			&impact.ArtifactType,
			&impact.ScopeKey,
		); err != nil {
			return nil, err
		}
		impact.Effect = "source_will_be_unavailable"
		result = append(result, impact)
	}
	return result, rows.Err()
}

func getPendingAssetDeletePreviewTx(
	ctx context.Context,
	tx *sql.Tx,
	assetID string,
	snapshotHash string,
) (AssetDeletePreview, error) {
	var preview AssetDeletePreview
	var checksum, runImpactsJSON, artifactImpactsJSON, createdAt string
	var preserved int
	err := tx.QueryRowContext(ctx, `
		SELECT
			asset_delete_preview_id, project_id, asset_id, asset_status, asset_checksum,
			active_run_impacts_json, artifact_impacts_json, existing_artifacts_preserved,
			snapshot_hash, status, created_at
		FROM asset_delete_previews
		WHERE asset_id = ? AND snapshot_hash = ? AND status = 'pending'
		ORDER BY created_at DESC, asset_delete_preview_id DESC
		LIMIT 1`,
		assetID,
		snapshotHash,
	).Scan(
		&preview.AssetDeletePreviewID,
		&preview.ProjectID,
		&preview.AssetID,
		&preview.AssetStatus,
		&checksum,
		&runImpactsJSON,
		&artifactImpactsJSON,
		&preserved,
		&preview.SnapshotHash,
		&preview.Status,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetDeletePreview{}, domainError(
			"DELETE_PREVIEW_EXPIRED",
			"材料删除预览已经失效，请重新预览。",
		)
	}
	if err != nil {
		return AssetDeletePreview{}, err
	}
	if err := json.Unmarshal([]byte(runImpactsJSON), &preview.ActiveRunImpacts); err != nil {
		return AssetDeletePreview{}, err
	}
	if err := json.Unmarshal([]byte(artifactImpactsJSON), &preview.ArtifactImpacts); err != nil {
		return AssetDeletePreview{}, err
	}
	preview.ExistingArtifactsPreserved = preserved == 1
	preview.CreatedAt, err = parseTime(createdAt)
	return preview, err
}

func jsonContainsString(value any, target string) bool {
	switch typed := value.(type) {
	case string:
		return typed == target
	case []any:
		for _, item := range typed {
			if jsonContainsString(item, target) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if jsonContainsString(item, target) {
				return true
			}
		}
	}
	return false
}

func (s *Store) claimRetentionJob(
	ctx context.Context,
	workerID string,
	leaseSeconds int,
) (*RetentionJob, error) {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	job, err := scanRetentionJob(tx.QueryRowContext(ctx, retentionJobSelect+`
		WHERE
			(status = 'scheduled' AND due_at <= ?)
			OR (status = 'retry_wait' AND lease_until <= ?)
			OR (status = 'running' AND lease_until <= ?)
		ORDER BY due_at ASC, retention_job_id ASC
		LIMIT 1`,
		formatTime(now),
		formatTime(now),
		formatTime(now),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	leaseUntil := now.Add(time.Duration(leaseSeconds) * time.Second)
	result, err := tx.ExecContext(ctx, `
		UPDATE retention_jobs
		SET status = 'running', attempt_count = attempt_count + 1,
			worker_id = ?, lease_until = ?, last_failure = NULL, updated_at = ?
		WHERE retention_job_id = ?
			AND (
				(status = 'scheduled' AND due_at <= ?)
				OR (status = 'retry_wait' AND lease_until <= ?)
				OR (status = 'running' AND lease_until <= ?)
			)`,
		workerID,
		formatTime(leaseUntil),
		formatTime(now),
		job.RetentionJobID,
		formatTime(now),
		formatTime(now),
		formatTime(now),
	)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, nil
	}
	job.Status = "running"
	job.AttemptCount++
	job.WorkerID = &workerID
	job.LeaseUntil = &leaseUntil
	job.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *Store) prepareRetentionDeletion(ctx context.Context, job RetentionJob) error {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var assetStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT status FROM assets WHERE asset_id = ? AND project_id = ?`,
		job.AssetID,
		job.ProjectID,
	).Scan(&assetStatus); errors.Is(err, sql.ErrNoRows) {
		return domainError("ASSET_NOT_FOUND", "保留任务引用的材料不存在。")
	} else if err != nil {
		return err
	}
	switch job.Action {
	case "expire_video_source":
		if assetStatus != "deleted" {
			if _, err := tx.ExecContext(ctx, `
				UPDATE assets
				SET status = 'expired', deleted_at = COALESCE(deleted_at, ?),
					delete_reason = COALESCE(delete_reason, 'retention_expired'),
					updated_at = ?
				WHERE asset_id = ? AND status <> 'deleted'`,
				formatTime(now),
				formatTime(now),
				job.AssetID,
			); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE asset_snapshots SET status = 'expired'
				WHERE asset_id = ? AND status <> 'deleted'`,
				job.AssetID,
			); err != nil {
				return err
			}
		}
	case userDeleteAction:
		if assetStatus != "deleted" {
			return domainError(
				"RETENTION_JOB_STATE_CONFLICT",
				"用户删除任务缺少已确认的逻辑删除状态。",
			)
		}
	default:
		return domainError("RETENTION_JOB_ACTION_UNSUPPORTED", "保留任务动作不受支持。")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE asset_blobs SET status = 'delete_pending'
		WHERE asset_id = ? AND status IN ('available', 'delete_failed')`,
		job.AssetID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) deleteRetentionBlobs(ctx context.Context, job RetentionJob) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT storage_ref
		FROM asset_blobs
		WHERE asset_id = ? AND project_id = ?
			AND status IN ('delete_pending', 'delete_failed')
		ORDER BY blob_id ASC`,
		job.AssetID,
		job.ProjectID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	refs := []string{}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, ref := range refs {
		path, err := resolveDataPath(s.dataRoot, ref)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete asset blob: %w", err)
		}
	}
	return nil
}

func (s *Store) completeRetentionJob(ctx context.Context, job RetentionJob) (bool, error) {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `
		SELECT status FROM retention_jobs WHERE retention_job_id = ?`,
		job.RetentionJobID,
	).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		return false, domainError("RETENTION_JOB_NOT_FOUND", "保留任务不存在。")
	} else if err != nil {
		return false, err
	}
	if status != "running" {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE asset_blobs SET status = 'deleted'
		WHERE asset_id = ? AND status IN ('delete_pending', 'delete_failed')`,
		job.AssetID,
	); err != nil {
		return false, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE retention_jobs
		SET status = 'completed', worker_id = NULL, lease_until = NULL,
			completed_at = ?, updated_at = ?
		WHERE retention_job_id = ? AND status = 'running' AND worker_id = ?`,
		"保留任务状态已经变化。",
		formatTime(now),
		formatTime(now),
		job.RetentionJobID,
		nullableString(job.WorkerID),
	); err != nil {
		return false, err
	}
	eventType := "asset.deleted"
	if job.Action == "expire_video_source" {
		eventType = "asset.expired"
	}
	if _, err := s.appendEvent(
		ctx,
		tx,
		job.ProjectID,
		nil,
		nil,
		eventType,
		"asset",
		job.AssetID,
		map[string]any{
			"retention_job_id":           job.RetentionJobID,
			"policy_id":                  job.PolicyID,
			"formal_artifacts_preserved": true,
		},
	); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) recordRetentionFailure(
	ctx context.Context,
	job RetentionJob,
	maxAttempts int,
) (bool, error) {
	now := s.now()
	failed := job.AttemptCount >= maxAttempts
	status := "retry_wait"
	backoffShift := job.AttemptCount - 1
	if backoffShift < 0 {
		backoffShift = 0
	}
	if backoffShift > 6 {
		backoffShift = 6
	}
	backoff := time.Duration(1<<backoffShift) * time.Minute
	if backoff > time.Hour {
		backoff = time.Hour
	}
	nextAttempt := now.Add(backoff)
	if failed {
		status = "failed"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE asset_blobs SET status = 'delete_failed'
		WHERE asset_id = ? AND status = 'delete_pending'`,
		job.AssetID,
	); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE retention_jobs
		SET status = ?, worker_id = NULL, lease_until = ?,
			last_failure = 'BLOB_DELETE_FAILED', updated_at = ?
		WHERE retention_job_id = ? AND status = 'running' AND worker_id = ?`,
		status,
		nullableRetryTime(failed, nextAttempt),
		formatTime(now),
		job.RetentionJobID,
		nullableString(job.WorkerID),
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, nil
	}
	if _, err := s.appendEvent(
		ctx,
		tx,
		job.ProjectID,
		nil,
		nil,
		"asset.retention_failed",
		"asset",
		job.AssetID,
		map[string]any{
			"retention_job_id": job.RetentionJobID,
			"attempt_count":    job.AttemptCount,
			"will_retry":       !failed,
		},
	); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return failed, nil
}

func nullableRetryTime(failed bool, value time.Time) any {
	if failed {
		return nil
	}
	return formatTime(value)
}

const retentionJobSelect = `
	SELECT
		retention_job_id, project_id, asset_id, policy_id, action, due_at,
		status, attempt_count, worker_id, lease_until, last_failure, completed_at,
		created_at, updated_at
	FROM retention_jobs`

func scanRetentionJob(row rowScanner) (RetentionJob, error) {
	var job RetentionJob
	var dueAt, createdAt, updatedAt string
	var workerID, leaseUntil, lastFailure, completedAt sql.NullString
	if err := row.Scan(
		&job.RetentionJobID,
		&job.ProjectID,
		&job.AssetID,
		&job.PolicyID,
		&job.Action,
		&dueAt,
		&job.Status,
		&job.AttemptCount,
		&workerID,
		&leaseUntil,
		&lastFailure,
		&completedAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		return RetentionJob{}, err
	}
	var err error
	job.DueAt, err = parseTime(dueAt)
	if err != nil {
		return RetentionJob{}, err
	}
	job.LeaseUntil, err = optionalTime(leaseUntil)
	if err != nil {
		return RetentionJob{}, err
	}
	job.CompletedAt, err = optionalTime(completedAt)
	if err != nil {
		return RetentionJob{}, err
	}
	job.LastFailure = stringPointer(lastFailure)
	job.WorkerID = stringPointer(workerID)
	job.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return RetentionJob{}, err
	}
	job.UpdatedAt, err = parseTime(updatedAt)
	return job, err
}

func sortAssetDeleteImpacts(preview *AssetDeletePreview) {
	sort.Slice(preview.ActiveRunImpacts, func(i, j int) bool {
		return preview.ActiveRunImpacts[i].RunID < preview.ActiveRunImpacts[j].RunID
	})
	sort.Slice(preview.ArtifactImpacts, func(i, j int) bool {
		left := preview.ArtifactImpacts[i]
		right := preview.ArtifactImpacts[j]
		if left.ArtifactType != right.ArtifactType {
			return left.ArtifactType < right.ArtifactType
		}
		if left.ScopeKey != right.ScopeKey {
			return left.ScopeKey < right.ScopeKey
		}
		return left.ArtifactVersionID < right.ArtifactVersionID
	})
}
