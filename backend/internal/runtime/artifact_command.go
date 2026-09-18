package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
)

func authorizeArtifactCommandTx(ctx context.Context, tx *sql.Tx, projectID, scope string) error {
	if _, err := projectFilesWorkspace(ctx, tx, projectID, true); err != nil {
		return err
	}
	if scope != "" && scope != projectID {
		return domainError("REQUEST_VALIDATION_FAILED", "产物操作范围与作品不一致。")
	}
	return nil
}

func decodeArtifactVersionReceiptTx(ctx context.Context, tx *sql.Tx, cached json.RawMessage, artifact Artifact, command CreateVersionCommand) (VersionResult, error) {
	receipt, err := decodeIdempotentResult[VersionResult](cached)
	if err != nil {
		return VersionResult{}, err
	}
	version := receipt.ArtifactVersion
	mismatch := func() (VersionResult, error) {
		return VersionResult{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识的回执不属于当前产物版本操作。")
	}
	if version.ArtifactID != artifact.ArtifactID || version.BaseVersionID == nil || *version.BaseVersionID != command.BaseVersionID ||
		version.ArtifactVersionID == "" || version.Version <= command.BaseVersion ||
		receipt.Approval.ApprovalRequestID != "" && (receipt.Approval.ProjectID != artifact.ProjectID || receipt.Approval.RunID != artifact.RunID) {
		return mismatch()
	}
	var bound bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifact_versions result
		JOIN artifact_versions base ON base.artifact_version_id = result.base_version_id AND base.artifact_id = result.artifact_id
		WHERE result.artifact_version_id = ? AND result.artifact_id = ? AND result.version = ?
		AND base.artifact_version_id = ? AND base.version = ?)`, version.ArtifactVersionID, artifact.ArtifactID,
		version.Version, command.BaseVersionID, command.BaseVersion).Scan(&bound)
	if err == nil && !bound {
		return mismatch()
	}
	return receipt, err
}

func decodeScriptEditReceiptTx(ctx context.Context, tx *sql.Tx, cached json.RawMessage, projectID string, command CompleteScriptEditCommand) (ScriptEditCompletionResult, error) {
	receipt, err := decodeIdempotentResult[ScriptEditCompletionResult](cached)
	if err != nil {
		return ScriptEditCompletionResult{}, err
	}
	mismatch := func() (ScriptEditCompletionResult, error) {
		return ScriptEditCompletionResult{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识的回执不属于当前剧本编辑操作。")
	}
	if receipt.RunSnapshot.Run.ProjectID != projectID || receipt.RunSnapshot.Run.RunID != command.RunID ||
		len(receipt.RefreshTaskIDs) != len(command.ExpectedScriptVersionIDs) || len(receipt.RefreshTaskIDs) == 0 {
		return mismatch()
	}
	// A task can be refreshed more than once; the receipt cursor bounds this submission.
	versions := make(map[string]bool, len(command.ExpectedScriptVersionIDs))
	for _, versionID := range command.ExpectedScriptVersionIDs {
		if versionID == "" || versions[versionID] {
			return mismatch()
		}
		versions[versionID] = true
	}
	for _, taskID := range receipt.RefreshTaskIDs {
		var versionID string
		err := tx.QueryRowContext(ctx, `SELECT json_extract(payload_json, '$.script_artifact_version_id') FROM events
			WHERE project_id = ? AND run_id = ? AND event_type = 'script_handoff.refresh_requested'
			AND subject_type = 'task_item' AND subject_id = ? AND project_event_seq <= ?
			ORDER BY project_event_seq DESC LIMIT 1`, projectID, command.RunID, taskID, receipt.RunSnapshot.EventCursor.ProjectEventSeq).Scan(&versionID)
		if err == sql.ErrNoRows || err == nil && !versions[versionID] {
			return mismatch()
		}
		if err != nil {
			return ScriptEditCompletionResult{}, err
		}
		delete(versions, versionID)
	}
	return receipt, nil
}
