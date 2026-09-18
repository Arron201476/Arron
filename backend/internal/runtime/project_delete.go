package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"content-agent/backend/internal/capability"
)

const projectDeletePreviewTTL = 15 * time.Minute

type projectDeleteHashState struct {
	NativeWorkspaces        []string                 `json:"native_workspaces,omitempty"`
	WorkspaceSnapshots      []string                 `json:"workspace_snapshots,omitempty"`
	WorkspaceCommands       []string                 `json:"workspace_commands,omitempty"`
	WorkspaceFileOperations []string                 `json:"workspace_file_operations,omitempty"`
	WorkspaceManifests      []string                 `json:"workspace_manifests,omitempty"`
	ToolReconciliations     []string                 `json:"tool_reconciliations,omitempty"`
	SubtaskResults          []string                 `json:"subtask_results,omitempty"`
	InstructionVersions     []string                 `json:"instruction_versions,omitempty"`
	MemoryVersions          []string                 `json:"memory_versions,omitempty"`
	MemorySnapshots         []string                 `json:"memory_snapshots,omitempty"`
	MemoryPublications      []string                 `json:"memory_publications,omitempty"`
	MemoryRollouts          []string                 `json:"memory_rollouts,omitempty"`
	MemoryGenerations       []string                 `json:"memory_generations,omitempty"`
	InstructionSnapshots    []string                 `json:"instruction_snapshots,omitempty"`
	InstructionProposals    []string                 `json:"instruction_proposals,omitempty"`
	WorkingFileVersions     []string                 `json:"working_file_versions,omitempty"`
	ProjectVersion          int                      `json:"project_version"`
	ProjectStatus           string                   `json:"project_status"`
	UpdatedAt               string                   `json:"updated_at"`
	ActiveRuns              []ProjectDeleteRunImpact `json:"active_runs"`
	AssetStates             []string                 `json:"asset_states"`
	ArtifactIDs             []string                 `json:"artifact_ids"`
	CandidateIDs            []string                 `json:"candidate_ids"`
	FinalIDs                []string                 `json:"final_ids"`
}

func (s *Store) CreateProjectDeletePreview(
	ctx context.Context,
	command CreateProjectDeletePreviewCommand,
) (ProjectDeletePreview, error) {
	if command.ProjectID == "" {
		return ProjectDeletePreview{}, domainError("REQUEST_VALIDATION_FAILED", "删除预览缺少作品。")
	}
	if command.Scope == "" {
		command.Scope = command.ProjectID
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	defer tx.Rollback()
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	if hit {
		return decodeIdempotentResult[ProjectDeletePreview](cached)
	}
	preview, err := s.buildProjectDeletePreviewTx(ctx, tx, command.ProjectID, now)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	preview.ProjectDeletePreviewID = s.newID("pdp")
	preview.Status = "pending"
	preview.CreatedAt = now
	preview.ExpiresAt = now.Add(projectDeletePreviewTTL)
	if _, err := tx.ExecContext(ctx, `
		UPDATE project_delete_previews
		SET status = 'expired', resolved_at = ?
		WHERE project_id = ? AND status = 'pending'`,
		formatTime(now), command.ProjectID,
	); err != nil {
		return ProjectDeletePreview{}, err
	}
	impactJSON, err := json.Marshal(preview)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO project_delete_previews(
			project_delete_preview_id, project_id, project_version, impact_json,
			snapshot_hash, status, created_at, expires_at
		) VALUES(?, ?, ?, ?, ?, 'pending', ?, ?)`,
		preview.ProjectDeletePreviewID, preview.ProjectID, preview.ProjectVersion,
		string(impactJSON), preview.SnapshotHash, formatTime(now), formatTime(preview.ExpiresAt),
	); err != nil {
		return ProjectDeletePreview{}, err
	}
	if _, err := s.appendEvent(ctx, tx, command.ProjectID, nil, nil,
		"project.delete_requested", "project", command.ProjectID,
		map[string]any{
			"project_delete_preview_id": preview.ProjectDeletePreviewID,
			"snapshot_hash":             preview.SnapshotHash,
			"active_run_count":          len(preview.ActiveRunImpacts),
			"source_asset_count":        preview.Impact.AssetCount,
		},
	); err != nil {
		return ProjectDeletePreview{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, preview, now); err != nil {
		return ProjectDeletePreview{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectDeletePreview{}, err
	}
	return preview, nil
}

func (s *Store) ConfirmProjectDelete(
	ctx context.Context,
	command ConfirmProjectDeleteCommand,
) (ProjectDeleteResult, error) {
	s.skillMu.Lock()
	defer s.skillMu.Unlock()
	if !command.Confirmed {
		return ProjectDeleteResult{}, domainError("REQUIRED_CONFIRMATION_MISSING", "删除作品前需要用户明确确认。")
	}
	if command.ProjectID == "" || command.PreviewHash == "" {
		return ProjectDeleteResult{}, domainError("REQUEST_VALIDATION_FAILED", "删除确认缺少作品或预览快照。")
	}
	if command.Scope == "" {
		command.Scope = command.ProjectID
	}
	if command.ActorRef == "" {
		command.ActorRef = actorRefFromContext(ctx)
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectDeleteResult{}, err
	}
	defer tx.Rollback()
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return ProjectDeleteResult{}, err
	}
	if hit {
		return decodeIdempotentResult[ProjectDeleteResult](cached)
	}
	storedID, expiresAt, err := getPendingProjectDeletePreviewTx(ctx, tx, command.ProjectID, command.PreviewHash)
	if err != nil {
		return ProjectDeleteResult{}, err
	}
	if !expiresAt.After(now) {
		return ProjectDeleteResult{}, domainError("DELETE_PREVIEW_EXPIRED", "作品删除预览已经过期，请重新预览。")
	}
	current, err := s.buildProjectDeletePreviewTx(ctx, tx, command.ProjectID, now)
	if err != nil {
		return ProjectDeleteResult{}, err
	}
	if current.SnapshotHash != command.PreviewHash {
		return ProjectDeleteResult{}, domainError("DELETE_PREVIEW_EXPIRED", "作品删除影响已经变化，请重新预览。")
	}
	if err := s.cancelProjectRunsForDeleteTx(ctx, tx, command.ProjectID, current.ActiveRunImpacts, command.ActorRef, now); err != nil {
		return ProjectDeleteResult{}, err
	}
	if err := s.cancelProjectAgentTasksTx(ctx, tx, command.ProjectID, now); err != nil {
		return ProjectDeleteResult{}, err
	}
	if err := s.cancelProjectAgentTurnsTx(ctx, tx, command.ProjectID, "PROJECT_DELETED", "作品已删除，本轮已取消。", now); err != nil {
		return ProjectDeleteResult{}, err
	}
	var workspaceID string
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM projects WHERE project_id = ?`, command.ProjectID).Scan(&workspaceID); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO skill_installation_events(skill_installation_event_id, skill_installation_id, skill_version_id, event_type, actor_ref, payload_json, created_at)
		SELECT ? || skill_installation_id, skill_installation_id, active_version_id, 'skill.project_deleted', ?, '{}', ?
		FROM skill_installations WHERE workspace_id = ? AND scope = 'project' AND scope_ref = ? AND status != 'uninstalled'`,
		s.newID("ske")+"_", command.ActorRef, formatTime(now), workspaceID, command.ProjectID); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE skill_installations SET status = 'uninstalled', enabled = 0, active_version_id = NULL, updated_at = ?, uninstalled_at = ?
		WHERE workspace_id = ? AND scope = 'project' AND scope_ref = ? AND status != 'uninstalled'`,
		formatTime(now), formatTime(now), workspaceID, command.ProjectID); err != nil {
		return ProjectDeleteResult{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT asset_id FROM assets
		WHERE project_id = ? AND status <> 'deleted'
		ORDER BY asset_id`, command.ProjectID)
	if err != nil {
		return ProjectDeleteResult{}, err
	}
	var assetIDs []string
	for rows.Next() {
		var assetID string
		if err := rows.Scan(&assetID); err != nil {
			rows.Close()
			return ProjectDeleteResult{}, err
		}
		assetIDs = append(assetIDs, assetID)
	}
	if err := rows.Close(); err != nil {
		return ProjectDeleteResult{}, err
	}
	if err := rows.Err(); err != nil {
		return ProjectDeleteResult{}, err
	}
	jobIDs := make([]string, 0, len(assetIDs))
	for _, assetID := range assetIDs {
		if _, err := tx.ExecContext(ctx, `
			UPDATE assets SET status = 'deleted', deleted_at = ?, delete_reason = 'project_deleted', updated_at = ?
			WHERE asset_id = ? AND status <> 'deleted'`,
			formatTime(now), formatTime(now), assetID,
		); err != nil {
			return ProjectDeleteResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE asset_snapshots SET status = 'deleted' WHERE asset_id = ? AND status <> 'deleted'`, assetID); err != nil {
			return ProjectDeleteResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE asset_blobs SET status = 'delete_pending' WHERE asset_id = ? AND status IN ('available', 'delete_failed')`, assetID); err != nil {
			return ProjectDeleteResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE retention_jobs SET status = 'cancelled', worker_id = NULL, lease_until = NULL, updated_at = ?
			WHERE asset_id = ? AND status IN ('scheduled', 'running', 'retry_wait')`,
			formatTime(now), assetID,
		); err != nil {
			return ProjectDeleteResult{}, err
		}
		job, err := s.createRetentionJobTx(ctx, tx, command.ProjectID, assetID,
			userDeletePolicyID, userDeleteAction, now, now)
		if err != nil {
			return ProjectDeleteResult{}, err
		}
		jobIDs = append(jobIDs, job.RetentionJobID)
	}
	if err := closeNativeProjectWorkspacesTx(ctx, tx, command.ProjectID, now); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_subtask_results WHERE project_id = ?`, command.ProjectID); err != nil {
		return ProjectDeleteResult{}, err
	}
	if err := deleteExternalToolFactInputsTx(ctx, tx, command.ProjectID, ""); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_tool_reconciliations WHERE project_id = ?`, command.ProjectID); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_file_versions WHERE project_id = ?`, command.ProjectID); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_instruction_snapshots WHERE project_id=?`, command.ProjectID); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_instruction_proposals WHERE project_id=?`, command.ProjectID); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_instruction_versions WHERE scope='project' AND scope_ref=?`, command.ProjectID); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_memory_publications WHERE project_id=?`, command.ProjectID); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_memory_generations WHERE project_id=?`, command.ProjectID); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_memory_rollouts WHERE project_id=?`, command.ProjectID); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_memory_snapshots WHERE project_id=?`, command.ProjectID); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_memory_versions WHERE project_id=?`, command.ProjectID); err != nil {
		return ProjectDeleteResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE projects
		SET status = 'deleted', version = version + 1, active_write_run_id = NULL,
			current_capability_id = NULL, current_focus_artifact_version_id = NULL,
			deleted_at = ?, updated_at = ?
		WHERE project_id = ? AND deleted_at IS NULL`,
		"作品状态已经变化，请重新预览。",
		formatTime(now), formatTime(now), command.ProjectID,
	); err != nil {
		return ProjectDeleteResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE project_delete_previews SET status = 'consumed', resolved_at = ?
		WHERE project_delete_preview_id = ? AND status = 'pending'`,
		"作品删除预览已经失效。", formatTime(now), storedID,
	); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE project_delete_previews SET status = 'expired', resolved_at = ?
		WHERE project_id = ? AND status = 'pending'`, formatTime(now), command.ProjectID,
	); err != nil {
		return ProjectDeleteResult{}, err
	}
	if _, err := s.appendEvent(ctx, tx, command.ProjectID, nil, nil,
		"project.deleted", "project", command.ProjectID,
		map[string]any{
			"actor_ref":                      command.ActorRef,
			"source_deletion_job_count":      len(jobIDs),
			"derived_records_retained_audit": true,
		},
	); err != nil {
		return ProjectDeleteResult{}, err
	}
	result := ProjectDeleteResult{
		ProjectID:              command.ProjectID,
		DeletedAt:              now,
		SourceDeletionJobIDs:   jobIDs,
		DerivedRecordsRetained: true,
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, result, now); err != nil {
		return ProjectDeleteResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectDeleteResult{}, err
	}
	if root, err := s.scopedSkillActiveRoot(managedSkillScope{workspaceID, capability.SkillScopeProject, command.ProjectID}); err == nil {
		_ = removeManagedSkillPath(s.skillDataRoot, root)
	}
	return result, nil
}

// cancelProjectRunsForDeleteTx closes unfinished workflow state as part of the
// same transaction that removes the project. Project deletion is the user's
// confirmation to stop these runs; it must not require separate run actions.
func (s *Store) cancelProjectRunsForDeleteTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runs []ProjectDeleteRunImpact,
	actorRef string,
	now time.Time,
) error {
	if len(runs) == 0 {
		return nil
	}
	runIDs := make([]string, 0, len(runs))
	for _, run := range runs {
		if err := cancelExecutionToolsTx(ctx, tx, run.RunID, "", now); err != nil {
			return err
		}
		runIDs = append(runIDs, run.RunID)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(runIDs)), ",")
	args := make([]any, 0, len(runIDs)+3)
	for _, runID := range runIDs {
		args = append(args, runID)
	}

	approvalRows, err := tx.QueryContext(ctx, `
		SELECT approval_request_id, subject_kind, subject_ref_id
		FROM approvals
		WHERE status = 'pending' AND scope <> 'final_selection'
			AND run_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return err
	}
	type approvalImpact struct{ id, subjectKind, subjectID string }
	var approvals []approvalImpact
	for approvalRows.Next() {
		var item approvalImpact
		if err := approvalRows.Scan(&item.id, &item.subjectKind, &item.subjectID); err != nil {
			approvalRows.Close()
			return err
		}
		approvals = append(approvals, item)
	}
	if err := approvalRows.Close(); err != nil {
		return err
	}
	resolution, err := json.Marshal(map[string]any{"action": "project_delete"})
	if err != nil {
		return err
	}
	for _, approval := range approvals {
		switch approval.subjectKind {
		case "artifact_version":
			if _, err := tx.ExecContext(ctx, `UPDATE artifact_versions SET status = 'invalidated' WHERE artifact_version_id = ? AND status = 'pending_approval'`, approval.subjectID); err != nil {
				return err
			}
		case "artifact_version_set":
			if _, err := tx.ExecContext(ctx, `
				UPDATE artifact_versions SET status = 'invalidated'
				WHERE status = 'pending_approval' AND artifact_version_id IN (
					SELECT artifact_version_id FROM approval_subject_versions WHERE approval_request_id = ?
				)`, approval.id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE approvals SET status = 'cancelled', resolved_at = ?, resolution_json = ?, actor_ref = ?
			WHERE approval_request_id = ? AND status = 'pending'`,
			formatTime(now), string(resolution), actorRef, approval.id); err != nil {
			return err
		}
	}

	updates := []struct {
		query string
		args  []any
	}{
		{`UPDATE impact_reviews SET status = 'expired', resolved_at = ? WHERE status = 'pending' AND run_id IN (` + placeholders + `)`, append([]any{formatTime(now)}, args...)},
		{`UPDATE step_runs SET status = 'cancelled', ended_at = ? WHERE status IN ('pending','running','waiting_approval','paused','failed') AND run_id IN (` + placeholders + `)`, append([]any{formatTime(now)}, args...)},
		{`UPDATE execution_attempts SET status = 'cancelled', ended_at = ?, error_code = 'RUN_CANCELLED' WHERE status IN ('running','result_received','repair_pending','waiting_approval','paused') AND run_id IN (` + placeholders + `)`, append([]any{formatTime(now)}, args...)},
		{`UPDATE task_items SET status = 'cancelled', current_attempt_id = NULL, ended_at = ?, updated_at = ? WHERE status IN ('pending','running','repair_pending','waiting_approval','paused','failed') AND run_id IN (` + placeholders + `)`, append([]any{formatTime(now), formatTime(now)}, args...)},
		{`UPDATE runs SET status = 'cancelled', ended_at = ?, updated_at = ? WHERE run_id IN (` + placeholders + `)`, append([]any{formatTime(now), formatTime(now)}, args...)},
	}
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, update.query, update.args...); err != nil {
			return err
		}
	}
	for _, run := range runs {
		runRef := run.RunID
		if _, err := s.appendEvent(ctx, tx, projectID, &runRef, nil, "run.cancelled", "run", run.RunID, map[string]any{"reason": "project_deleted"}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) buildProjectDeletePreviewTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	now time.Time,
) (ProjectDeletePreview, error) {
	var version int
	var status, updatedAt string
	if err := tx.QueryRowContext(ctx, `
		SELECT version, status, updated_at FROM projects
		WHERE project_id = ? AND deleted_at IS NULL`, projectID,
	).Scan(&version, &status, &updatedAt); errors.Is(err, sql.ErrNoRows) {
		return ProjectDeletePreview{}, domainError("PROJECT_NOT_FOUND", "作品不存在。")
	} else if err != nil {
		return ProjectDeletePreview{}, err
	}
	activeRuns := []ProjectDeleteRunImpact{}
	runRows, err := tx.QueryContext(ctx, `
		SELECT run_id, status FROM runs
		WHERE project_id = ? AND status IN ('pending', 'running', 'waiting_approval', 'pausing', 'paused', 'failed')
		ORDER BY run_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	for runRows.Next() {
		var impact ProjectDeleteRunImpact
		if err := runRows.Scan(&impact.RunID, &impact.Status); err != nil {
			runRows.Close()
			return ProjectDeletePreview{}, err
		}
		activeRuns = append(activeRuns, impact)
	}
	if err := runRows.Close(); err != nil {
		return ProjectDeletePreview{}, err
	}
	impact := ProjectDeleteImpact{}
	countQueries := []struct {
		query string
		dest  *int
	}{
		{`SELECT COUNT(*) FROM native_workspace_leases WHERE project_id=? AND status!='closing'`, &impact.NativeWorkspaceCount},
		{`SELECT COUNT(*) FROM native_workspace_snapshots WHERE session_id IN (SELECT session_id FROM native_workspace_leases WHERE project_id=?)`, &impact.WorkspaceSnapshotCount},
		{`SELECT COUNT(*) FROM native_workspace_commands WHERE status!='deleted' AND session_id IN (SELECT session_id FROM native_workspace_leases WHERE project_id=?)`, &impact.WorkspaceCommandCount},
		{`SELECT COUNT(*) FROM native_workspace_file_operations WHERE status!='deleted' AND session_id IN (SELECT session_id FROM native_workspace_leases WHERE project_id=?)`, &impact.WorkspaceFileOperationCount},
		{`SELECT COUNT(*) FROM agent_subtask_results WHERE project_id=?`, &impact.SubtaskResultCount},
		{`SELECT COUNT(*) FROM agent_tool_reconciliations WHERE project_id=?`, &impact.ToolReconciliationCount},
		{`SELECT COUNT(*) FROM project_file_versions WHERE project_id = ?`, &impact.WorkingFileVersionCount},
		{`SELECT COUNT(*) FROM agent_instruction_versions WHERE scope='project' AND scope_ref=?`, &impact.InstructionVersionCount},
		{`SELECT COUNT(*) FROM agent_instruction_snapshots WHERE project_id=?`, &impact.InstructionSnapshotCount},
		{`SELECT COUNT(*) FROM agent_instruction_proposals WHERE project_id=?`, &impact.InstructionProposalCount},
		{`SELECT COUNT(*) FROM conversations WHERE project_id = ?`, &impact.ConversationCount},
		{`SELECT COUNT(*) FROM messages WHERE project_id = ?`, &impact.MessageCount},
		{`SELECT COUNT(*) FROM assets WHERE project_id = ? AND status <> 'deleted'`, &impact.AssetCount},
		{`SELECT COUNT(*) FROM artifacts WHERE project_id = ?`, &impact.ArtifactCount},
		{`SELECT COUNT(*) FROM script_candidates WHERE project_id = ?`, &impact.CandidateCount},
	}
	for _, item := range countQueries {
		if err := tx.QueryRowContext(ctx, item.query, projectID).Scan(item.dest); err != nil {
			return ProjectDeletePreview{}, err
		}
	}
	var finalCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM final_selections WHERE project_id = ? AND status = 'active'`, projectID).Scan(&finalCount); err != nil {
		return ProjectDeletePreview{}, err
	}
	impact.HasFinalSelection = finalCount > 0
	state := projectDeleteHashState{ProjectVersion: version, ProjectStatus: status, UpdatedAt: updatedAt, ActiveRuns: activeRuns}
	state.MemoryPublications, err = queryStringList(ctx, tx, `SELECT agent_tool_call_id || ':' || arguments_hash || ':' || session_id || ':' || version || ':' || content_hash FROM agent_memory_publications WHERE project_id=? ORDER BY agent_tool_call_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.MemoryGenerations, err = queryStringList(ctx, tx, `SELECT generation_id || ':' || revision || ':' || status || ':' || phase || ':' || checkpoint_hash FROM agent_memory_generations WHERE project_id=? ORDER BY generation_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.MemoryRollouts, err = queryStringList(ctx, tx, `SELECT activity_key || ':' || segment_id || ':' || request_hash || ':' || forgotten FROM agent_memory_rollouts WHERE project_id=? ORDER BY activity_key,segment_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.MemorySnapshots, err = queryStringList(ctx, tx, `SELECT activity_key || ':' || user_id || ':' || version || ':' || content_hash || ':' || read_enabled FROM agent_memory_snapshots WHERE project_id=? ORDER BY activity_key`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.MemoryVersions, err = queryStringList(ctx, tx, `SELECT user_id || ':' || version || ':' || content_hash || ':' || enabled || ':' || forgotten FROM agent_memory_versions WHERE project_id=? ORDER BY user_id,version`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.NativeWorkspaces, err = queryStringList(ctx, tx, `SELECT session_id || ':' || generation || ':' || status FROM native_workspace_leases WHERE project_id=? ORDER BY session_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.WorkspaceSnapshots, err = queryStringList(ctx, tx, `SELECT session_id || ':' || version || ':' || content_hash FROM native_workspace_snapshots WHERE session_id IN (SELECT session_id FROM native_workspace_leases WHERE project_id=?) ORDER BY session_id,version`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.WorkspaceCommands, err = queryStringList(ctx, tx, `SELECT agent_tool_call_id || ':' || command_hash || ':' || status || ':' || result_hash FROM native_workspace_commands WHERE status!='deleted' AND session_id IN (SELECT session_id FROM native_workspace_leases WHERE project_id=?) ORDER BY agent_tool_call_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.WorkspaceFileOperations, err = queryStringList(ctx, tx, `SELECT session_id || ':' || request_id || ':' || request_hash || ':' || status || ':' || result_hash FROM native_workspace_file_operations WHERE status!='deleted' AND session_id IN (SELECT session_id FROM native_workspace_leases WHERE project_id=?) ORDER BY session_id,request_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.WorkspaceManifests, err = queryStringList(ctx, tx, `SELECT session_id || ':' || manifest_hash || ':' || status || ':' || done_json FROM native_workspace_manifests WHERE status!='deleted' AND session_id IN (SELECT session_id FROM native_workspace_leases WHERE project_id=?) ORDER BY session_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.ToolReconciliations, err = queryStringList(ctx, tx, `SELECT r.agent_tool_call_id || ':' || r.content_hash || ':' || COALESCE(i.input_id,'') || ':' || COALESCE(i.content_hash,'')
		FROM agent_tool_reconciliations r LEFT JOIN agent_tool_reconciliation_inputs i ON i.agent_tool_call_id=r.agent_tool_call_id WHERE r.project_id=? ORDER BY r.agent_tool_call_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.SubtaskResults, err = queryStringList(ctx, tx, `SELECT agent_tool_call_id || ':' || content_hash FROM agent_subtask_results WHERE project_id=? ORDER BY agent_tool_call_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.InstructionVersions, err = queryStringList(ctx, tx, `SELECT CAST(version AS TEXT) || ':' || content_hash FROM agent_instruction_versions WHERE scope='project' AND scope_ref=? ORDER BY version`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.InstructionSnapshots, err = queryStringList(ctx, tx, `SELECT activity_key || ':' || content_hash FROM agent_instruction_snapshots WHERE project_id=? ORDER BY activity_key`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.InstructionProposals, err = queryStringList(ctx, tx, `SELECT agent_tool_call_id || ':' || arguments_hash FROM agent_instruction_proposals WHERE project_id=? ORDER BY agent_tool_call_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.WorkingFileVersions, err = queryStringList(ctx, tx, `SELECT agent_tool_call_id FROM project_file_versions WHERE project_id = ? ORDER BY path_key, version`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.AssetStates, err = queryStringList(ctx, tx, `SELECT asset_id || ':' || status || ':' || checksum FROM assets WHERE project_id = ? ORDER BY asset_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.ArtifactIDs, err = queryStringList(ctx, tx, `SELECT artifact_id || ':' || current_version_id FROM artifacts WHERE project_id = ? ORDER BY artifact_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.CandidateIDs, err = queryStringList(ctx, tx, `SELECT candidate_id || ':' || status FROM script_candidates WHERE project_id = ? ORDER BY candidate_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	state.FinalIDs, err = queryStringList(ctx, tx, `SELECT final_selection_id || ':' || status FROM final_selections WHERE project_id = ? ORDER BY final_selection_id`, projectID)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return ProjectDeletePreview{}, err
	}
	return ProjectDeletePreview{
		ProjectID:              projectID,
		ProjectVersion:         version,
		ActiveRunImpacts:       activeRuns,
		Impact:                 impact,
		SourceFilesWillDelete:  impact.AssetCount > 0,
		DerivedRecordsRetained: true,
		SnapshotHash:           sha256Hex(stateJSON),
		ExpiresAt:              now.Add(projectDeletePreviewTTL),
	}, nil
}

func queryStringList(ctx context.Context, tx *sql.Tx, query, projectID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func getPendingProjectDeletePreviewTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	snapshotHash string,
) (string, time.Time, error) {
	var previewID, expiresAtText string
	if err := tx.QueryRowContext(ctx, `
		SELECT project_delete_preview_id, expires_at
		FROM project_delete_previews
		WHERE project_id = ? AND snapshot_hash = ? AND status = 'pending'
		ORDER BY created_at DESC LIMIT 1`, projectID, snapshotHash,
	).Scan(&previewID, &expiresAtText); errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, domainError("DELETE_PREVIEW_EXPIRED", "作品删除预览不存在或已经失效。")
	} else if err != nil {
		return "", time.Time{}, err
	}
	expiresAt, err := parseTime(expiresAtText)
	return previewID, expiresAt, err
}
