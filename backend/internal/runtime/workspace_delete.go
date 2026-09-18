package runtime

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"content-agent/backend/internal/identity"
)

type DeleteWorkspaceCommand struct {
	CommandMeta
	WorkspaceID  string
	UserID       string
	Confirmation string
}

type WorkspaceDeleteResult struct {
	WorkspaceID            string    `json:"workspace_id"`
	DeletedAt              time.Time `json:"deleted_at"`
	ProjectCount           int       `json:"project_count"`
	AssetCount             int       `json:"asset_count"`
	SkillCount             int       `json:"skill_count"`
	MCPCredentialCount     int       `json:"mcp_credential_count"`
	SourceDeletionJobCount int       `json:"source_deletion_job_count"`
}

type workspaceAssetRef struct {
	AssetID   string
	ProjectID string
}

func (s *Store) DeleteWorkspace(
	ctx context.Context, command DeleteWorkspaceCommand,
) (WorkspaceDeleteResult, error) {
	s.skillMu.Lock()
	defer s.skillMu.Unlock()
	command.WorkspaceID = strings.TrimSpace(command.WorkspaceID)
	command.UserID = strings.TrimSpace(command.UserID)
	if !validWorkspaceID(command.WorkspaceID) || !tenantIdentifierPattern.MatchString(command.UserID) {
		return WorkspaceDeleteResult{}, domainError("REQUEST_VALIDATION_FAILED", "工作区删除请求无效。")
	}
	if strings.TrimSpace(command.Confirmation) != command.WorkspaceID {
		return WorkspaceDeleteResult{}, domainError(
			"REQUIRED_CONFIRMATION_MISSING", "删除工作区前必须精确确认工作区 ID。",
		)
	}
	if command.Scope == "" {
		command.Scope = command.WorkspaceID
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkspaceDeleteResult{}, err
	}
	defer tx.Rollback()

	var workspaceStatus, role string
	err = tx.QueryRowContext(ctx, `
		SELECT w.status, wm.role
		FROM workspaces w
		JOIN workspace_memberships wm ON wm.workspace_id = w.workspace_id
		WHERE w.workspace_id = ? AND wm.user_id = ?
		  AND w.deleted_at IS NULL AND wm.status = 'active'`,
		command.WorkspaceID, command.UserID,
	).Scan(&workspaceStatus, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceDeleteResult{}, domainError("WORKSPACE_ACCESS_DENIED", "只有工作区所有者可以删除工作区。")
	}
	if err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if workspaceStatus != "active" || role != string(identity.RoleOwner) {
		return WorkspaceDeleteResult{}, domainError("WORKSPACE_ACCESS_DENIED", "只有工作区所有者可以删除工作区。")
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if hit {
		return decodeIdempotentResult[WorkspaceDeleteResult](cached)
	}

	projectIDs, err := workspaceProjectIDsTx(ctx, tx, command.WorkspaceID)
	if err != nil {
		return WorkspaceDeleteResult{}, err
	}
	for _, projectID := range projectIDs {
		if err := s.cancelProjectAgentTurnsTx(ctx, tx, projectID, "WORKSPACE_DELETED", "工作区已删除，本轮已取消。", now); err != nil {
			return WorkspaceDeleteResult{}, err
		}
		if err := s.cancelProjectAgentTasksTx(ctx, tx, projectID, now); err != nil {
			return WorkspaceDeleteResult{}, err
		}
		preview, err := s.buildProjectDeletePreviewTx(ctx, tx, projectID, now)
		if err != nil {
			return WorkspaceDeleteResult{}, err
		}
		if err := s.cancelProjectRunsForDeleteTx(
			ctx, tx, projectID, preview.ActiveRunImpacts, command.UserID, now,
		); err != nil {
			return WorkspaceDeleteResult{}, err
		}
	}

	stagingRefs, err := workspaceStagingRefsTx(ctx, tx, command.WorkspaceID)
	if err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if err := cancelWorkspaceActivityTx(ctx, tx, command.WorkspaceID, now); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	assets, err := workspaceAssetRefsTx(ctx, tx, command.WorkspaceID)
	if err != nil {
		return WorkspaceDeleteResult{}, err
	}
	jobCount := 0
	for _, asset := range assets {
		if _, err := tx.ExecContext(ctx, `
			UPDATE assets SET status = 'deleted', deleted_at = ?,
				delete_reason = 'workspace_deleted', updated_at = ?
			WHERE asset_id = ? AND status <> 'deleted'`,
			formatTime(now), formatTime(now), asset.AssetID,
		); err != nil {
			return WorkspaceDeleteResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE asset_snapshots SET status = 'deleted'
			WHERE asset_id = ? AND status <> 'deleted'`, asset.AssetID,
		); err != nil {
			return WorkspaceDeleteResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE asset_blobs SET status = 'delete_pending'
			WHERE asset_id = ? AND status IN ('available','delete_failed')`, asset.AssetID,
		); err != nil {
			return WorkspaceDeleteResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE retention_jobs SET status = 'cancelled', worker_id = NULL,
				lease_until = NULL, updated_at = ?
			WHERE asset_id = ? AND status IN ('scheduled','running','retry_wait')`,
			formatTime(now), asset.AssetID,
		); err != nil {
			return WorkspaceDeleteResult{}, err
		}
		if _, err := s.createRetentionJobTx(
			ctx, tx, asset.ProjectID, asset.AssetID,
			userDeletePolicyID, userDeleteAction, now, now,
		); err != nil {
			return WorkspaceDeleteResult{}, err
		}
		jobCount++
	}

	for _, projectID := range projectIDs {
		if err := closeNativeProjectWorkspacesTx(ctx, tx, projectID, now); err != nil {
			return WorkspaceDeleteResult{}, err
		}
		if _, err := s.appendEvent(
			ctx, tx, projectID, nil, nil, "project.deleted", "project", projectID,
			map[string]any{"actor_ref": command.UserID, "reason": "workspace_deleted"},
		); err != nil {
			return WorkspaceDeleteResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_subtask_results WHERE project_id IN (SELECT project_id FROM projects WHERE workspace_id = ?)`, command.WorkspaceID); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if err := deleteExternalToolFactInputsTx(ctx, tx, "", command.WorkspaceID); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_tool_reconciliations WHERE project_id IN (SELECT project_id FROM projects WHERE workspace_id = ?)`, command.WorkspaceID); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_file_versions WHERE project_id IN (SELECT project_id FROM projects WHERE workspace_id = ?)`, command.WorkspaceID); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_instruction_snapshots WHERE workspace_id=?`, command.WorkspaceID); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_instruction_proposals WHERE workspace_id=?`, command.WorkspaceID); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_instruction_versions WHERE workspace_id=?`, command.WorkspaceID); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_memory_publications WHERE workspace_id=?`, command.WorkspaceID); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_memory_generations WHERE workspace_id=?`, command.WorkspaceID); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_memory_rollouts WHERE workspace_id=?`, command.WorkspaceID); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_memory_snapshots WHERE workspace_id=?`, command.WorkspaceID); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_memory_versions WHERE workspace_id=?`, command.WorkspaceID); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	projectResult, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET status = 'deleted', version = version + 1, active_write_run_id = NULL,
			current_capability_id = NULL, current_focus_artifact_version_id = NULL,
			deleted_at = ?, updated_at = ?
		WHERE workspace_id = ? AND deleted_at IS NULL`,
		formatTime(now), formatTime(now), command.WorkspaceID,
	)
	if err != nil {
		return WorkspaceDeleteResult{}, err
	}
	projectCount, _ := projectResult.RowsAffected()

	skillResult, err := tx.ExecContext(ctx, `
		UPDATE skill_installations
		SET status = 'uninstalled', enabled = 0, active_version_id = NULL,
			uninstalled_at = ?, updated_at = ?
		WHERE workspace_id = ? AND status <> 'uninstalled'`,
		formatTime(now), formatTime(now), command.WorkspaceID,
	)
	if err != nil {
		return WorkspaceDeleteResult{}, err
	}
	skillCount, _ := skillResult.RowsAffected()
	credentialResult, err := tx.ExecContext(ctx, `
		UPDATE workspace_mcp_credentials
		SET status = 'deleted', deleted_at = ?, updated_at = ?
		WHERE workspace_id = ? AND deleted_at IS NULL`,
		formatTime(now), formatTime(now), command.WorkspaceID,
	)
	if err != nil {
		return WorkspaceDeleteResult{}, err
	}
	credentialCount, _ := credentialResult.RowsAffected()
	connectionResult, err := tx.ExecContext(ctx, `UPDATE mcp_connections SET status = 'deleted', ciphertext = NULL,
		version = version + 1, updated_at = ? WHERE workspace_id = ? AND status = 'active'`, formatTime(now), command.WorkspaceID)
	if err != nil {
		return WorkspaceDeleteResult{}, err
	}
	connectionCount, _ := connectionResult.RowsAffected()
	credentialCount += connectionCount
	if _, err := tx.ExecContext(ctx, `
		UPDATE workspace_memberships
		SET status = 'deleted', updated_at = ?
		WHERE workspace_id = ? AND status <> 'deleted'`,
		formatTime(now), command.WorkspaceID,
	); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE workspaces SET status = 'deleted', deleted_at = ?, updated_at = ?
		WHERE workspace_id = ? AND status = 'active' AND deleted_at IS NULL`,
		"工作区状态已经变化。", formatTime(now), formatTime(now), command.WorkspaceID,
	); err != nil {
		return WorkspaceDeleteResult{}, err
	}

	result := WorkspaceDeleteResult{
		WorkspaceID: command.WorkspaceID, DeletedAt: now,
		ProjectCount: int(projectCount), AssetCount: len(assets), SkillCount: int(skillCount),
		MCPCredentialCount: int(credentialCount), SourceDeletionJobCount: jobCount,
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, result, now); err != nil {
		return WorkspaceDeleteResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkspaceDeleteResult{}, err
	}

	s.invalidateWorkspaceRegistry(command.WorkspaceID)
	for _, stagingRef := range stagingRefs {
		if path, err := resolveDataPath(s.dataRoot, stagingRef); err == nil {
			_ = os.Remove(path)
		}
	}
	_ = os.RemoveAll(s.managedSkillActiveRoot(command.WorkspaceID))
	_ = os.RemoveAll(filepath.Join(s.skillDataRoot, "packages", command.WorkspaceID))
	_ = removeManagedSkillPath(s.skillDataRoot, filepath.Join(s.skillDataRoot, "scoped-active", command.WorkspaceID))
	_ = removeManagedSkillPath(s.skillDataRoot, filepath.Join(s.skillDataRoot, "execution", command.WorkspaceID))
	_ = os.RemoveAll(filepath.Join(s.dataRoot, "script-executions", command.WorkspaceID))
	return result, nil
}

func workspaceProjectIDsTx(ctx context.Context, tx *sql.Tx, workspaceID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT project_id FROM projects
		WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY project_id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projectIDs []string
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			return nil, err
		}
		projectIDs = append(projectIDs, projectID)
	}
	return projectIDs, rows.Err()
}

func workspaceAssetRefsTx(ctx context.Context, tx *sql.Tx, workspaceID string) ([]workspaceAssetRef, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT a.asset_id, a.project_id
		FROM assets a JOIN projects p ON p.project_id = a.project_id
		WHERE p.workspace_id = ? AND p.deleted_at IS NULL AND a.status <> 'deleted'
		ORDER BY a.asset_id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assets []workspaceAssetRef
	for rows.Next() {
		var asset workspaceAssetRef
		if err := rows.Scan(&asset.AssetID, &asset.ProjectID); err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, rows.Err()
}

func workspaceStagingRefsTx(ctx context.Context, tx *sql.Tx, workspaceID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT ui.staging_ref
		FROM upload_items ui
		JOIN upload_sessions us ON us.upload_session_id = ui.upload_session_id
		JOIN projects p ON p.project_id = us.project_id
		WHERE p.workspace_id = ? AND p.deleted_at IS NULL
		  AND ui.staging_ref IS NOT NULL AND ui.staging_ref <> ''`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var references []string
	for rows.Next() {
		var reference string
		if err := rows.Scan(&reference); err != nil {
			return nil, err
		}
		references = append(references, reference)
	}
	return references, rows.Err()
}

func cancelWorkspaceActivityTx(
	ctx context.Context, tx *sql.Tx, workspaceID string, now time.Time,
) error {
	formattedNow := formatTime(now)
	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE agent_turns
		  SET status = 'cancelled', error_code = 'WORKSPACE_DELETED',
		      error_message = '工作区已删除。', completed_at = ?, updated_at = ?
		  WHERE workspace_id = ? AND status IN ('accepted','running','waiting_approval','pausing','paused','cancel_requested','committing')`,
			[]any{formattedNow, formattedNow, workspaceID}},
		{`UPDATE agent_task_attempts
		  SET status = 'cancelled', error_code = 'WORKSPACE_DELETED',
		      error_message = '工作区已删除。', ended_at = ?
		  WHERE status = 'running' AND agent_task_id IN (
		      SELECT agent_task_id FROM agent_tasks WHERE workspace_id = ?
		  )`, []any{formattedNow, workspaceID}},
		{`UPDATE agent_tasks
		  SET status = 'cancelled', cancel_requested = 1,
		      failure_code = 'WORKSPACE_DELETED', failure_message = '工作区已删除。',
		      completed_at = ?, updated_at = ?
		  WHERE workspace_id = ? AND status IN ('queued','running')`,
			[]any{formattedNow, formattedNow, workspaceID}},
		{`UPDATE skill_invocations SET status = 'cancelled', updated_at = ?
		  WHERE project_id IN (SELECT project_id FROM projects WHERE workspace_id = ?)
		    AND status IN ('created','awaiting_confirmation','delegated_to_task','delegated_to_run','running')`,
			[]any{formattedNow, workspaceID}},
		{`UPDATE agent_tool_approvals
		  SET status = 'cancelled', version = version + 1, resolved_at = ?,
		      resolution_json = '{"action":"cancel","reason":"workspace_deleted"}'
		  WHERE workspace_id = ? AND status IN ('pending','approved')`,
			[]any{formattedNow, workspaceID}},
		{`UPDATE agent_tool_calls
		  SET status = 'cancelled', approval_status = CASE
		      WHEN approval_status IN ('pending','approved') THEN 'cancelled' ELSE approval_status END,
		      error_code = 'WORKSPACE_DELETED', error_message = '工作区已删除。',
		      completed_at = ?, updated_at = ?
		  WHERE workspace_id = ? AND status IN ('pending_approval','approved','running')`,
			[]any{formattedNow, formattedNow, workspaceID}},
		{`UPDATE skill_script_executions
		  SET status = 'failed', error_code = 'WORKSPACE_DELETED',
		      error_message = '工作区已删除。', completed_at = ?, updated_at = ?
		  WHERE workspace_id = ? AND status = 'running'`,
			[]any{formattedNow, formattedNow, workspaceID}},
		{`UPDATE revision_attempts SET status = 'failed', failure_code = 'WORKSPACE_DELETED', finished_at = ?
		  WHERE status = 'running' AND revision_request_id IN (
		      SELECT revision_request_id FROM revision_requests
		      WHERE project_id IN (SELECT project_id FROM projects WHERE workspace_id = ?)
		  )`, []any{formattedNow, workspaceID}},
		{`UPDATE revision_requests
		  SET status = 'cancelled', failure_code = 'WORKSPACE_DELETED', version = version + 1,
		      updated_at = ?, finished_at = ?
		  WHERE project_id IN (SELECT project_id FROM projects WHERE workspace_id = ?)
		    AND status IN ('queued','waiting_safe_checkpoint','running','proposed')`,
			[]any{formattedNow, formattedNow, workspaceID}},
		{`UPDATE proposed_actions SET status = 'superseded', updated_at = ?
		  WHERE project_id IN (SELECT project_id FROM projects WHERE workspace_id = ?)
		    AND status = 'pending'`, []any{formattedNow, workspaceID}},
		{`UPDATE upload_items
		  SET status = 'failed', failure = 'WORKSPACE_DELETED', staging_ref = NULL, completed_at = ?
		  WHERE upload_session_id IN (
		      SELECT upload_session_id FROM upload_sessions
		      WHERE project_id IN (SELECT project_id FROM projects WHERE workspace_id = ?)
		  ) AND status IN ('pending','validating')`, []any{formattedNow, workspaceID}},
		{`UPDATE upload_sessions
		  SET failed_item_count = declared_item_count - completed_item_count,
		      status = CASE WHEN completed_item_count > 0 THEN 'partial_failed' ELSE 'failed' END,
		      closed_at = ?
		  WHERE project_id IN (SELECT project_id FROM projects WHERE workspace_id = ?)
		    AND status NOT IN ('completed','failed','partial_failed')`, []any{formattedNow, workspaceID}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
}
