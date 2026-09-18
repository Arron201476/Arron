package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// RequestTargetedRegeneration reruns the workflow step that produced one
// current artifact. It is the execution path for an unambiguous Agent command
// such as "重新跑第1集"; it does not create a text revision proposal.
func (s *Store) RequestTargetedRegeneration(
	ctx context.Context,
	command RequestTargetedRegenerationCommand,
) (ApprovalRegenerationResult, error) {
	if command.ProjectID == "" || command.ArtifactID == "" {
		return ApprovalRegenerationResult{}, domainError("REQUEST_VALIDATION_FAILED", "定向重生成请求不完整。")
	}
	if command.ActorRef == "" {
		command.ActorRef = actorRefFromContext(ctx)
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	defer tx.Rollback()

	if command.Scope == "" {
		command.Scope = command.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if hit {
		return decodeIdempotentResult[ApprovalRegenerationResult](cached)
	}

	var runID, stepRunID, currentVersionID string
	if err := tx.QueryRowContext(ctx, `
		SELECT run_id, step_run_id, current_version_id
		FROM artifacts
		WHERE artifact_id = ? AND project_id = ?`,
		command.ArtifactID, command.ProjectID,
	).Scan(&runID, &stepRunID, &currentVersionID); errors.Is(err, sql.ErrNoRows) {
		return ApprovalRegenerationResult{}, domainError("ARTIFACT_NOT_FOUND", "要重新处理的产物不存在。")
	} else if err != nil {
		return ApprovalRegenerationResult{}, err
	}

	var activeExecutingPlans int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM regeneration_plans
		WHERE run_id = ? AND status IN ('pending','running')`, runID,
	).Scan(&activeExecutingPlans); err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if activeExecutingPlans > 0 {
		return ApprovalRegenerationResult{}, domainError("REGENERATION_PLAN_CONFLICT", "当前重生成仍在执行，请等待完成或先暂停任务。")
	}
	var waitingPlans int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM regeneration_plans
		WHERE run_id = ? AND status = 'waiting_approval'`, runID,
	).Scan(&waitingPlans); err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if waitingPlans > 0 {
		// A newer explicit request replaces an unconfirmed candidate while the
		// shared lifecycle helper preserves history and restores confirmed bases.
		if err := cancelActiveRegenerationPlansTx(ctx, tx, runID, now); err != nil {
			return ApprovalRegenerationResult{}, err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT current_version_id FROM artifacts WHERE artifact_id = ?`,
			command.ArtifactID,
		).Scan(&currentVersionID); err != nil {
			return ApprovalRegenerationResult{}, err
		}
	}

	var activeRevisions int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM revision_requests
		WHERE project_id = ? AND base_artifact_version_id = ?
			AND status IN ('waiting_target_confirmation','waiting_safe_checkpoint','queued','running','proposed')`,
		command.ProjectID, currentVersionID,
	).Scan(&activeRevisions); err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if activeRevisions > 0 {
		return ApprovalRegenerationResult{}, domainError("REVISION_IN_PROGRESS", "当前版本存在未处理的修改请求，请先完成或取消修改。")
	}

	run, err := getRunTx(ctx, tx, runID)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if run.Status == "running" || run.Status == "cancelling" {
		return ApprovalRegenerationResult{}, domainError("RUN_STATE_CONFLICT", "当前步骤仍在执行，请等待安全检查点后再重新处理。")
	}

	approval := Approval{
		ProjectID:    command.ProjectID,
		RunID:        runID,
		StepRunID:    stepRunID,
		SubjectKind:  "artifact_version",
		SubjectRefID: currentVersionID,
	}
	targets, err := approvalRegenerationTargetsTx(ctx, tx, approval)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if len(targets) != 1 || targets[0].Artifact.ArtifactID != command.ArtifactID {
		return ApprovalRegenerationResult{}, domainError("ARTIFACT_VERSION_NOT_FOUND", "无法定位要重新处理的当前产物版本。")
	}

	review, source, err := s.createApprovalRegenerationReviewTx(ctx, tx, approval, targets, RequestApprovalRegenerationCommand{
		Action: "regenerate_artifact", Instruction: strings.TrimSpace(command.Instruction), ActorRef: command.ActorRef,
	}, now)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if command.RegenerationScope == "step" {
		if err := s.expandRegenerationReviewToStepTx(ctx, tx, &review, runID, targets[0].StepID); err != nil {
			return ApprovalRegenerationResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE approvals SET status = 'expired', resolved_at = ?
		WHERE run_id = ? AND step_run_id = ? AND status = 'pending'`,
		formatTime(now), runID, stepRunID,
	); err != nil {
		return ApprovalRegenerationResult{}, err
	}
	plan, err := s.planDownstreamRegenerationTx(ctx, tx, review, source, run, now)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if err := attachRevisionInstructionTx(ctx, tx, plan, strings.TrimSpace(command.Instruction)); err != nil {
		return ApprovalRegenerationResult{}, err
	}
	runRef := runID
	if _, err := s.appendEvent(ctx, tx, command.ProjectID, &runRef, nil,
		"regeneration.requested", "artifact", command.ArtifactID,
		map[string]any{"regeneration_plan_id": plan.RegenerationPlanID, "scope_key": targets[0].Artifact.ScopeKey}); err != nil {
		return ApprovalRegenerationResult{}, err
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, runID)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	result := ApprovalRegenerationResult{ImpactReview: review, RegenerationPlan: *plan, RunSnapshot: snapshot}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, result, now); err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApprovalRegenerationResult{}, err
	}
	return result, nil
}

func (s *Store) expandRegenerationReviewToStepTx(
	ctx context.Context,
	tx *sql.Tx,
	review *ImpactReview,
	runID string,
	stepID string,
) error {
	if review == nil || len(review.AffectedItems) == 0 {
		return domainError("REGENERATION_PLAN_CONFLICT", "整步重生成缺少目标产物。")
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT ti.item_key, MIN(ti.item_order)
		FROM task_items ti
		JOIN step_runs sr ON sr.step_run_id = ti.step_run_id
		WHERE ti.run_id = ? AND sr.step_id = ?
		GROUP BY ti.item_key
		ORDER BY MIN(ti.item_order), ti.item_key`, runID, stepID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var taskKeys []string
	for rows.Next() {
		var taskKey string
		var itemOrder int
		if err := rows.Scan(&taskKey, &itemOrder); err != nil {
			return err
		}
		if taskKey != "" {
			taskKeys = append(taskKeys, taskKey)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(taskKeys) == 0 {
		return domainError("REGENERATION_PLAN_CONFLICT", "当前步骤没有可重新处理的任务。")
	}
	encoded, err := json.Marshal(taskKeys)
	if err != nil {
		return err
	}
	updated := false
	for index := range review.AffectedItems {
		if review.AffectedItems[index].RegenerateFromStepID != stepID {
			continue
		}
		review.AffectedItems[index].RegenerateTaskKeys = append([]string(nil), taskKeys...)
		if _, err := tx.ExecContext(ctx, `
			UPDATE impact_review_items SET regenerate_task_keys_json = ?
			WHERE impact_review_item_id = ?`,
			string(encoded), review.AffectedItems[index].ImpactReviewItemID); err != nil {
			return err
		}
		updated = true
	}
	if !updated {
		return domainError("REGENERATION_PLAN_CONFLICT", "整步重生成目标与当前步骤不一致。")
	}
	hash, err := impactReviewSnapshotHash(*review)
	if err != nil {
		return err
	}
	review.SnapshotHash = hash
	if _, err := tx.ExecContext(ctx, `
		UPDATE impact_reviews SET snapshot_hash = ? WHERE impact_review_id = ?`,
		hash, review.ImpactReviewID); err != nil {
		return err
	}
	return nil
}

func (s *Store) UpdateAgentReply(
	ctx context.Context,
	agentMessageID string,
	agentDecisionID string,
	reply string,
) error {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return domainError("REQUEST_VALIDATION_FAILED", "Agent 回复不能为空。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var role, decisionJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT m.role, ad.decision_json
		FROM messages m
		JOIN agent_decisions ad ON ad.agent_decision_id = ? AND ad.agent_message_id = m.message_id
		WHERE m.message_id = ?`, agentDecisionID, agentMessageID,
	).Scan(&role, &decisionJSON); err != nil {
		return err
	}
	if role != "assistant" {
		return domainError("MESSAGE_ROLE_CONFLICT", "只能更新 Agent 回复。")
	}
	var decision map[string]any
	if err := json.Unmarshal([]byte(decisionJSON), &decision); err != nil {
		return err
	}
	decision["reply"] = reply
	encoded, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET content = ? WHERE message_id = ?`, reply, agentMessageID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_decisions SET decision_json = ? WHERE agent_decision_id = ?`, string(encoded), agentDecisionID); err != nil {
		return err
	}
	return tx.Commit()
}
