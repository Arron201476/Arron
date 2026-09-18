package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
)

func sourceIncompleteMaterialConfirmedTx(ctx context.Context, tx *sql.Tx, runID string) (bool, error) {
	var projectID, payload string
	if err := tx.QueryRowContext(ctx, `
		SELECT r.project_id, i.payload_json FROM runs r
		JOIN run_input_snapshot_versions i ON i.run_input_snapshot_version_id = r.current_input_snapshot_version_id
			AND i.run_id = r.run_id AND i.status = 'sealed'
		WHERE r.run_id = ?`, runID).Scan(&projectID, &payload); err != nil {
		return false, err
	}
	var source sourceInputRequest
	if err := json.Unmarshal([]byte(payload), &source); err != nil {
		return false, err
	}
	if source.ProjectID != projectID {
		return false, domainError("DEPENDENCY_INCOMPLETE", "封存输入与作品归属不一致。")
	}
	if source.AssetSetID == "" && source.AssetSetVersionID == "" {
		return false, nil
	}
	set, err := getAssetSetSnapshotTx(ctx, tx, source.AssetSetID, source.AssetSetVersionID)
	if err != nil {
		return false, err
	}
	if set.AssetSet.ProjectID != projectID || set.Version.Status != "sealed" || source.CollectionState != "sealed" {
		return false, domainError("DEPENDENCY_INCOMPLETE", "不完整材料确认未绑定本次封存输入。")
	}
	if len(set.Version.Completeness.MissingEpisodeNumbers) == 0 {
		return false, nil
	}
	if !validContinueIncompletePolicy(set.Version.ContinuationPolicy) {
		return false, domainError("DEPENDENCY_INCOMPLETE", "缺集材料没有有效的用户继续确认。")
	}
	return true, nil
}

// A run-wide historical decision cannot authorize a different or revised batch.
func partialBatchContinuationForStepTx(ctx context.Context, tx *sql.Tx, runID, stepRunID string, inputVersionIDs []string) ([]string, bool, error) {
	return partialBatchContinuationForStepApprovalTx(ctx, tx, runID, stepRunID, inputVersionIDs, nil)
}

func partialBatchContinuationForStepApprovalTx(ctx context.Context, tx *sql.Tx, runID, stepRunID string, inputVersionIDs []string, pending *Approval) ([]string, bool, error) {
	conflict := func() error {
		return domainError("PARTIAL_CONTINUATION_CONFLICT", "部分结果确认与当前批次或产物版本不一致，请重新确认。")
	}
	rows, err := tx.QueryContext(ctx, `SELECT item_key, status, output_artifact_version_id
		FROM task_items WHERE run_id = ? AND step_run_id = ? ORDER BY item_key`, runID, stepRunID)
	if err != nil {
		return nil, false, err
	}
	var failed, outputs []string
	valid := true
	for rows.Next() {
		var key, status string
		var output sql.NullString
		if err := rows.Scan(&key, &status, &output); err != nil {
			rows.Close()
			return nil, false, err
		}
		switch status {
		case "failed":
			failed = append(failed, key)
		case "succeeded":
			if !output.Valid || output.String == "" {
				valid = false
			} else {
				outputs = append(outputs, output.String)
			}
		default:
			valid = false
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	if !valid {
		return nil, false, conflict()
	}
	if len(failed) == 0 {
		if inputVersionIDs != nil {
			inputs := slices.Clone(inputVersionIDs)
			slices.Sort(inputs)
			slices.Sort(outputs)
			if !slices.Equal(inputs, outputs) {
				return nil, false, conflict()
			}
		}
		return []string{}, false, nil
	}
	if len(outputs) == 0 {
		return nil, false, conflict()
	}
	var decisionID, payload, hash, projectID, sourceKind, sourceRefID string
	err = tx.QueryRowContext(ctx, `SELECT decision_snapshot_id, payload_json, snapshot_hash, project_id, source_kind, source_ref_id
		FROM run_decision_snapshots WHERE run_id = ? AND step_run_id = ?
		AND decision_type = 'partial_batch_continuation' AND status = 'sealed'
		ORDER BY version DESC LIMIT 1`, runID, stepRunID).Scan(&decisionID, &payload, &hash, &projectID, &sourceKind, &sourceRefID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, conflict()
	}
	if err != nil {
		return nil, false, err
	}
	var decision struct {
		Mode           string   `json:"mode"`
		SucceededCount int      `json:"succeeded_count"`
		FailedItemKeys []string `json:"failed_item_keys"`
	}
	if sourceKind != "step_run" || sourceRefID != stepRunID || sha256Hex([]byte(payload)) != hash ||
		json.Unmarshal([]byte(payload), &decision) != nil || decision.Mode != "continue_with_partial_results" || decision.SucceededCount != len(outputs) {
		return nil, false, conflict()
	}
	slices.Sort(decision.FailedItemKeys)
	if !slices.Equal(failed, decision.FailedItemKeys) {
		return nil, false, conflict()
	}
	var approvalID string
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(subject_id), '') FROM events
		WHERE project_id = ? AND run_id = ? AND step_run_id = ? AND event_type = 'approval.requested'
		AND subject_type = 'approval' AND json_extract(payload_json, '$.decision_snapshot_id') = ?`,
		projectID, runID, stepRunID, decisionID).Scan(&count, &approvalID)
	if err != nil {
		return nil, false, err
	}
	if count != 1 {
		return nil, false, conflict()
	}
	var status, kind, subject, snapshotHash string
	err = tx.QueryRowContext(ctx, `SELECT status, subject_kind, subject_ref_id, subject_snapshot_hash FROM approvals
		WHERE approval_request_id = ? AND project_id = ? AND run_id = ? AND step_run_id = ?`,
		approvalID, projectID, runID, stepRunID).Scan(&status, &kind, &subject, &snapshotHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, conflict()
	}
	if err != nil {
		return nil, false, err
	}
	expectedApprovalStatus, expectedVersionStatus := "approved", "confirmed"
	if pending != nil {
		if approvalID != pending.ApprovalRequestID || snapshotHash != pending.SubjectSnapshotHash || pending.Status != "pending" {
			return nil, false, conflict()
		}
		expectedApprovalStatus, expectedVersionStatus = "pending", "pending_approval"
	}
	if status != expectedApprovalStatus || kind != "artifact_version_set" || subject != stepRunID {
		return nil, false, conflict()
	}
	rows, err = tx.QueryContext(ctx, `SELECT s.artifact_version_id, s.item_order, s.scope_key, a.artifact_type,
		a.current_version_id, v.status FROM approval_subject_versions s
		JOIN artifact_versions v ON v.artifact_version_id = s.artifact_version_id
		JOIN artifacts a ON a.artifact_id = v.artifact_id
		WHERE s.approval_request_id = ? AND a.project_id = ? AND a.run_id = ? AND a.step_run_id = ?
		ORDER BY s.item_order`, approvalID, projectID, runID, stepRunID)
	if err != nil {
		return nil, false, err
	}
	var items []approvalVersionSetItem
	var approved []string
	for rows.Next() {
		var item approvalVersionSetItem
		var currentVersion, versionStatus string
		if err := rows.Scan(&item.ArtifactVersionID, &item.ItemOrder, &item.ScopeKey, &item.ArtifactType, &currentVersion, &versionStatus); err != nil {
			rows.Close()
			return nil, false, err
		}
		if currentVersion != item.ArtifactVersionID || versionStatus != expectedVersionStatus {
			rows.Close()
			return nil, false, conflict()
		}
		items = append(items, item)
		approved = append(approved, item.ArtifactVersionID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	actualHash, err := approvalVersionSetHash(items)
	if err != nil || len(items) == 0 || actualHash != snapshotHash {
		return nil, false, conflict()
	}
	for _, output := range outputs {
		if !slices.Contains(approved, output) {
			return nil, false, conflict()
		}
	}
	if inputVersionIDs != nil {
		slices.Sort(approved)
		inputVersionIDs = slices.Clone(inputVersionIDs)
		slices.Sort(inputVersionIDs)
		if !slices.Equal(inputVersionIDs, approved) {
			return nil, false, conflict()
		}
	}
	return failed, true, nil
}
