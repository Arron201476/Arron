package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"content-agent/backend/internal/capability"
)

type workflowContinueRequest struct {
	Condition       string   `json:"condition"`
	FromStepID      string   `json:"from_step_id"`
	ToStepID        string   `json:"to_step_id"`
	InputSnapshotID string   `json:"input_snapshot_id"`
	VersionIDs      []string `json:"artifact_version_ids"`
}

func confirmedTransitionVersionsTx(ctx context.Context, tx *sql.Tx, run Run, versions []string) ([]approvalVersionSetItem, error) {
	if len(versions) == 0 {
		return nil, domainError("DEPENDENCY_INCOMPLETE", "流程转换缺少已确认产物。")
	}
	items := make([]approvalVersionSetItem, 0, len(versions))
	seen := make(map[string]bool, len(versions))
	for index, id := range versions {
		if id == "" || seen[id] {
			return nil, domainError("APPROVAL_SUBJECT_CHANGED", "流程转换包含无效或重复版本。")
		}
		seen[id] = true
		item := approvalVersionSetItem{ArtifactVersionID: id, ItemOrder: index + 1}
		var current, status string
		err := tx.QueryRowContext(ctx, `SELECT a.current_version_id, v.status, a.scope_key, a.artifact_type
			FROM artifacts a JOIN artifact_versions v ON v.artifact_id=a.artifact_id
			WHERE v.artifact_version_id=? AND a.project_id=? AND a.run_id=?`, id, run.ProjectID, run.RunID).
			Scan(&current, &status, &item.ScopeKey, &item.ArtifactType)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (current != id || status != "confirmed") {
			return nil, domainError("APPROVAL_SUBJECT_CHANGED", "流程转换依赖的已确认版本已经变化。")
		}
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Store) requestWorkflowContinueTx(ctx context.Context, tx *sql.Tx, run Run, stepRunID string, step capability.CompiledStep, versions []string, now time.Time) error {
	target, err := selectWorkflowTransition(step, "user_continue")
	if err != nil {
		return err
	}
	entry, ok, err := s.capabilityEntryForRunQuery(ctx, tx, run.RunID, run.CapabilityID)
	if err != nil {
		return err
	}
	if !ok || entry.Definition == nil || compiledStep(entry.Definition.Steps, target) == nil {
		return domainError("CAPABILITY_VERSION_UNAVAILABLE", "继续分支的目标步骤不可用。")
	}
	items, err := confirmedTransitionVersionsTx(ctx, tx, run, versions)
	if err != nil {
		return err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE run_id=? AND status='pending' AND scope<>'final_selection'`, run.RunID).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return domainError("RUN_STATE_CONFLICT", "尚有其他待处理确认，不能创建流程继续确认。")
	}
	request := workflowContinueRequest{Condition: "user_continue", FromStepID: step.ID, ToStepID: target, InputSnapshotID: run.CurrentInputSnapshotVersionID, VersionIDs: slices.Clone(versions)}
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	decision, err := s.createRunDecisionSnapshotTx(ctx, tx, run.ProjectID, run.RunID, stepRunID, "workflow_transition_request", "step_run", stepRunID, payload, now)
	if err != nil {
		return err
	}
	approvalID := s.newID("apr")
	if _, err := tx.ExecContext(ctx, `INSERT INTO approvals(approval_request_id, project_id, run_id, step_run_id,
		scope, status, version, title, reason, options_json, subject_kind, subject_ref_id, subject_version, subject_snapshot_hash, requested_at)
		VALUES(?, ?, ?, ?, 'workflow_transition', 'pending', 1, ?, ?, '["approve"]', 'transition', ?, ?, ?, ?)`,
		approvalID, run.ProjectID, run.RunID, stepRunID, "确认继续工作流", "当前步骤已完成，是否按已确认的结果继续下一步骤？",
		decision.DecisionSnapshotID, decision.Version, decision.SnapshotHash, formatTime(now)); err != nil {
		return err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO approval_subject_versions(approval_request_id, artifact_version_id, item_order, scope_key) VALUES(?, ?, ?, ?)`,
			approvalID, item.ArtifactVersionID, item.ItemOrder, item.ScopeKey); err != nil {
			return err
		}
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE step_runs SET status='waiting_approval' WHERE step_run_id=? AND run_id=? AND status='completed'`,
		"流程继续确认对应的步骤已经变化。", stepRunID, run.RunID); err != nil {
		return err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE runs SET status='waiting_approval', updated_at=?
		WHERE run_id=? AND current_step_run_id=? AND status IN ('running','waiting_approval','paused')`,
		"流程继续确认对应的任务已经变化。", formatTime(now), run.RunID, stepRunID); err != nil {
		return err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE projects SET version=version+1, status='waiting_approval', current_focus_artifact_version_id=?, updated_at=?
		WHERE project_id=? AND active_write_run_id=?`, "作品写锁已经变化。", versions[len(versions)-1], formatTime(now), run.ProjectID, run.RunID); err != nil {
		return err
	}
	for _, event := range []struct{ kind, subject, id string }{
		{"workflow.awaiting_continue", "decision_snapshot", decision.DecisionSnapshotID},
		{"approval.requested", "approval", approvalID},
		{"run.waiting_approval", "run", run.RunID},
	} {
		if _, err := s.appendEvent(ctx, tx, run.ProjectID, &run.RunID, &stepRunID, event.kind, event.subject, event.id,
			map[string]any{"decision_snapshot_id": decision.DecisionSnapshotID, "condition": request.Condition, "to_step_id": target}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) resolveWorkflowContinueTx(ctx context.Context, tx *sql.Tx, command ResolveApprovalCommand, approval Approval, now time.Time) (RunSnapshot, error) {
	changed := func() error {
		return domainError("APPROVAL_SUBJECT_CHANGED", "流程继续确认的步骤、输入或产物版本已经变化。")
	}
	if len(command.ResolutionPayload) > 0 {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(command.ResolutionPayload, &payload); err != nil || len(payload) != 0 {
			return RunSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "流程继续确认不接受客户端指定的跳转目标或附加策略。")
		}
	}
	run, err := getRunTx(ctx, tx, approval.RunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if run.Status != "waiting_approval" || run.CurrentStepRunID == nil || *run.CurrentStepRunID != approval.StepRunID {
		return RunSnapshot{}, changed()
	}
	var payload, hash string
	err = tx.QueryRowContext(ctx, `SELECT payload_json, snapshot_hash FROM run_decision_snapshots
		WHERE decision_snapshot_id=? AND project_id=? AND run_id=? AND step_run_id=? AND source_kind='step_run' AND source_ref_id=?
		AND decision_type='workflow_transition_request' AND status='sealed' AND version=?`,
		approval.SubjectRefID, approval.ProjectID, approval.RunID, approval.StepRunID, approval.StepRunID, approval.SubjectVersion).Scan(&payload, &hash)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (hash != approval.SubjectSnapshotHash || sha256Hex([]byte(payload)) != hash) {
		return RunSnapshot{}, changed()
	}
	if err != nil {
		return RunSnapshot{}, err
	}
	var request workflowContinueRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil || request.Condition != "user_continue" || request.InputSnapshotID != run.CurrentInputSnapshotVersionID {
		return RunSnapshot{}, changed()
	}
	step, err := s.compiledStepForRunTx(ctx, tx, run.RunID, approval.StepRunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if request.FromStepID != step.ID {
		return RunSnapshot{}, changed()
	}
	next, err := completedWorkflowTransitionTx(ctx, tx, approval.StepRunID, step, "user_continue")
	if err != nil {
		return RunSnapshot{}, err
	}
	if next != request.ToStepID {
		return RunSnapshot{}, changed()
	}
	items, err := confirmedTransitionVersionsTx(ctx, tx, run, request.VersionIDs)
	if err != nil {
		return RunSnapshot{}, err
	}
	var bound int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM approval_subject_versions WHERE approval_request_id=?`, approval.ApprovalRequestID).Scan(&bound); err != nil {
		return RunSnapshot{}, err
	}
	if bound != len(items) {
		return RunSnapshot{}, changed()
	}
	for _, item := range items {
		var matches bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM approval_subject_versions WHERE approval_request_id=? AND artifact_version_id=? AND item_order=? AND scope_key=?)`,
			approval.ApprovalRequestID, item.ArtifactVersionID, item.ItemOrder, item.ScopeKey).Scan(&matches); err != nil {
			return RunSnapshot{}, err
		}
		if !matches {
			return RunSnapshot{}, changed()
		}
	}
	resolution, err := json.Marshal(map[string]any{"action": command.Action, "condition": "user_continue", "decision_snapshot_id": approval.SubjectRefID, "to_step_id": next, "resolved_subject_refs": request.VersionIDs})
	if err != nil {
		return RunSnapshot{}, err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE approvals SET status='approved', resolution_json=?, resolved_at=?, actor_ref=? WHERE approval_request_id=? AND status='pending'`,
		"流程继续确认已经处理。", string(resolution), formatTime(now), command.ActorRef, approval.ApprovalRequestID); err != nil {
		return RunSnapshot{}, err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE step_runs SET status='completed', ended_at=COALESCE(ended_at, ?) WHERE step_run_id=? AND run_id=? AND status='waiting_approval'`,
		"流程继续确认对应的步骤已经变化。", formatTime(now), approval.StepRunID, run.RunID); err != nil {
		return RunSnapshot{}, err
	}
	if _, _, err := s.advanceRunAfterConfirmedArtifactsTx(ctx, tx, run.RunID, approval.StepRunID, request.VersionIDs, now, "user_continue"); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := s.appendEvent(ctx, tx, run.ProjectID, &run.RunID, &approval.StepRunID, "workflow.continue_confirmed", "decision_snapshot", approval.SubjectRefID,
		map[string]any{"approval_request_id": approval.ApprovalRequestID, "to_step_id": next}); err != nil {
		return RunSnapshot{}, err
	}
	return s.finalizeResolvedApprovalTx(ctx, tx, command, approval, now)
}
