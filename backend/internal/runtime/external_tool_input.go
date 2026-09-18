package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"content-agent/backend/internal/identity"
)

const editableReconciliationInput = `input_id NOT IN (SELECT input_id FROM agent_tool_reconciliation_inputs)`

func (s *Store) prepareExternalRunResumeTx(ctx context.Context, tx *sql.Tx, runID string) error {
	ids, err := queryStringList(ctx, tx, `SELECT a.attempt_id FROM execution_attempts a JOIN task_items t ON t.current_attempt_id=a.attempt_id
		WHERE a.run_id=? AND EXISTS(SELECT 1 FROM execution_tool_calls e JOIN agent_tool_calls c ON c.agent_tool_call_id=e.agent_tool_call_id
		WHERE e.execution_attempt_id=a.attempt_id AND c.tool_kind='mcp' AND c.access_mode!='read' AND c.started_at IS NOT NULL AND c.status!='completed') ORDER BY a.attempt_id`, runID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var raw, hash string
		err := tx.QueryRowContext(ctx, `SELECT state_json,state_hash FROM execution_run_states WHERE attempt_id=?`, id).Scan(&raw, &hash)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && hash != sha256Hex([]byte(raw)) {
			return domainError("SDK_TOOL_REPLAY_RISK", "外部操作缺少有效的原执行检查点，不能从头恢复。")
		}
		if err := s.prepareExternalToolResumeTx(ctx, tx, "stateful_workflow", id, json.RawMessage(raw)); err != nil {
			return err
		}
	}
	return nil
}

func deleteExternalToolFactInputsTx(ctx context.Context, tx *sql.Tx, projectID, workspaceID string) error {
	selection, id := `r.project_id=?`, projectID
	if workspaceID != "" {
		selection, id = `r.project_id IN (SELECT project_id FROM projects WHERE workspace_id=?)`, workspaceID
	}
	for _, table := range []string{"agent_turn_inputs", "agent_task_inputs", "execution_inputs"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE input_id IN (SELECT i.input_id FROM agent_tool_reconciliation_inputs i
			JOIN agent_tool_reconciliations r ON r.agent_tool_call_id=i.agent_tool_call_id WHERE `+selection+`)`, id); err != nil {
			return err
		}
	}
	return nil
}

func externalToolFact(view AgentToolOutcomeReview) (string, error) {
	raw, err := json.Marshal(struct {
		Schema        string                 `json:"schema_version"`
		CallID        string                 `json:"agent_tool_call_id"`
		SDKCallID     string                 `json:"sdk_tool_call_id"`
		ToolID        string                 `json:"tool_id"`
		ArgumentsHash string                 `json:"arguments_hash"`
		SubjectHash   string                 `json:"subject_snapshot_hash"`
		Resolution    *ToolOutcomeResolution `json:"user_reconciliation"`
		Instruction   string                 `json:"instruction"`
	}{"agent_tool_reconciliation.v1", view.AgentToolCallID, view.SDKToolCallID, view.ToolID,
		view.ArgumentsHash, view.SubjectSnapshotHash, view.Resolution,
		"The original tool result remains unknown. This is the initiating user's checked fact, not a provider success receipt. Do not replay the original call. Any new operation requires a new tool call and its normal authorization."})
	return string(raw), err
}

func externalInputTargetTx(ctx context.Context, tx *sql.Tx, mode, executionID string) (executionInputChangeTarget, string, error) {
	ownerID := executionID
	if mode == "background_task" {
		if err := tx.QueryRowContext(ctx, `SELECT agent_task_id FROM agent_task_attempts WHERE agent_task_attempt_id=?`, executionID).Scan(&ownerID); err != nil {
			return executionInputChangeTarget{}, "", err
		}
	}
	target, err := inputChangeTargetTx(ctx, tx, mode, ownerID)
	return target, ownerID, err
}

func validateExternalFactInputTx(ctx context.Context, tx *sql.Tx, view AgentToolOutcomeReview, target executionInputChangeTarget, ownerID, content string) (bool, error) {
	var inputID, mode, executionID, inputOwner, hash string
	var size int
	err := tx.QueryRowContext(ctx, `SELECT input_id,execution_mode,execution_id,input_owner_id,content_hash,content_size_bytes
		FROM agent_tool_reconciliation_inputs WHERE agent_tool_call_id=?`, view.AgentToolCallID).Scan(&inputID, &mode, &executionID, &inputOwner, &hash, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var actual, user, attachments, status string
	var unchanged bool
	err = tx.QueryRowContext(ctx, `SELECT content,user_id,attachments_json,status,withdrawn_at IS NULL AND replacement_input_id IS NULL
		FROM `+target.table+` WHERE input_id=? AND `+target.ownerColumn+`=?`, inputID, ownerID).Scan(&actual, &user, &attachments, &status, &unchanged)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err != nil || mode != view.ExecutionMode || executionID != view.ExecutionID || inputOwner != ownerID || hash != sha256Hex([]byte(content)) ||
		size != len(content) || actual != content || user != view.UserID || attachments != "[]" || !unchanged || (status != "received" && status != "included") {
		return false, domainError("AGENT_TOOL_OUTCOME_REVIEW_CORRUPT", "核对事实的追加输入校验失败，不能继续原执行。")
	}
	if mode == "stateful_workflow" {
		var contentHash string
		if err := tx.QueryRowContext(ctx, `SELECT content_hash FROM execution_inputs WHERE input_id=?`, inputID).Scan(&contentHash); err != nil {
			return false, err
		}
		if contentHash != executionInputContentHash(content, nil) {
			return false, domainError("AGENT_TOOL_OUTCOME_REVIEW_CORRUPT", "核对输入哈希已变化。")
		}
	}
	return true, nil
}

func reviewedExternalInputTx(ctx context.Context, tx *sql.Tx, call AgentToolCall, mode, executionID string, consumed bool) (bool, error) {
	if call.Status != "failed" && call.Status != "cancelled" {
		return false, nil
	}
	view, err := agentToolOutcomeReviewTx(ctx, tx, call.AgentToolCallID)
	if err != nil {
		var domain *DomainError
		if errors.As(err, &domain) && domain.Code == "AGENT_TOOL_OUTCOME_REVIEW_NOT_FOUND" {
			return false, nil
		}
		return false, err
	}
	if view.Resolution == nil {
		return false, nil
	}
	if view.ExecutionMode != mode || view.ExecutionID != executionID {
		return false, domainError("AGENT_TOOL_OUTCOME_REVIEW_CORRUPT", "核对事实不属于原执行。")
	}
	target, ownerID, err := externalInputTargetTx(ctx, tx, mode, executionID)
	if err != nil {
		return false, err
	}
	content, err := externalToolFact(view)
	if err != nil {
		return false, err
	}
	exists, err := validateExternalFactInputTx(ctx, tx, view, target, ownerID, content)
	if err != nil || !exists {
		return false, err
	}
	var status string
	var bound bool
	boundClause, boundValue := target.boundColumn+`=?`, any(executionID)
	switch mode {
	case "conversation":
		boundValue = target.turn.DispatchGeneration
	case "stateful_workflow":
		state, err := loadExecutionToolStateTx(ctx, tx, executionID)
		if err != nil {
			return false, err
		}
		boundValue = state.TokenHash
	}
	if err := tx.QueryRowContext(ctx, `SELECT status,COALESCE(`+boundClause+`,0) FROM `+target.table+` WHERE input_id=(SELECT input_id FROM agent_tool_reconciliation_inputs WHERE agent_tool_call_id=?)`, boundValue, call.AgentToolCallID).Scan(&status, &bound); err != nil {
		return false, err
	}
	if consumed {
		return status == "included", nil
	}
	return status == "included" || bound, nil
}

// Called only inside an explicit user resume transaction. The original SDK
// checkpoint stays untouched; existing input admission delivers these facts.
func (s *Store) prepareExternalToolResumeTx(ctx context.Context, tx *sql.Tx, mode, executionID string, raw json.RawMessage) error {
	calls, err := uncertainExternalCallsTx(ctx, tx, mode, executionID)
	if err != nil || len(calls) == 0 {
		return err
	}
	unconsumed := make([]AgentToolCall, 0, len(calls))
	for _, call := range calls {
		consumed, err := reviewedExternalInputTx(ctx, tx, call, mode, executionID, true)
		if err != nil {
			return err
		}
		if !consumed {
			unconsumed = append(unconsumed, call)
		}
	}
	if len(unconsumed) == 0 {
		return nil
	}
	if err := validateExternalToolCheckpointTx(ctx, tx, mode, executionID, raw); err != nil {
		return err
	}
	principal, ok := identity.FromContext(ctx)
	_, active := AgentActivityFromContext(ctx)
	if !ok || principal.Kind != identity.KindUser || active {
		return domainError("ROLE_FORBIDDEN", "外部操作核对后只能由原用户明确继续。")
	}
	principal, err = resolvePrincipalQuery(ctx, tx, principal)
	if err != nil {
		return err
	}
	ctx = identity.WithPrincipal(ctx, principal)
	target, ownerID, err := externalInputTargetTx(ctx, tx, mode, executionID)
	if err != nil {
		return err
	}
	if _, err := projectFilesWorkspace(ctx, tx, target.project, true); err != nil {
		return err
	}
	if !principal.Allows(identity.RoleEditor) || principal.UserID != target.user {
		return domainError("ROLE_FORBIDDEN", "只能继续本人核对的外部操作。")
	}
	if !target.active {
		return domainError("SDK_TOOL_REPLAY_RISK", "原执行已结束或不支持原生输入恢复，不能重跑。")
	}
	for _, call := range unconsumed {
		view, err := agentToolOutcomeReviewTx(ctx, tx, call.AgentToolCallID)
		if err != nil {
			return err
		}
		if view.Resolution == nil {
			return domainError(externalToolRecoveryCode, "请先逐项核对已发出的外部操作，再继续原执行。")
		}
		content, err := externalToolFact(view)
		if err != nil {
			return err
		}
		exists, err := validateExternalFactInputTx(ctx, tx, view, target, ownerID, content)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		var count, size int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(content AS BLOB))),0) FROM `+target.table+` WHERE `+target.ownerColumn+`=?`, ownerID).Scan(&count, &size); err != nil {
			return err
		}
		if count >= 128 || size+len(content) > 512<<10 || len(content) > 32<<10 {
			return domainError("REQUEST_VALIDATION_FAILED", "追加输入已达到数量或大小上限，核对事实尚未送入执行。")
		}
		if err := s.enforceWorkspaceQuotaTx(ctx, tx, target.workspace, "storage_bytes", int64(len(content))); err != nil {
			return err
		}
		now, inputID := s.now(), s.newID("inin")
		columns, values := `input_id,`+target.ownerColumn+`,user_id,sequence,content,status,created_at,attachments_json`, `?,?,?,?,?,'received',?,'[]'`
		args := []any{inputID, ownerID, target.user, count + 1, content, formatTime(now)}
		if mode == "stateful_workflow" {
			columns += `,content_hash`
			values += `,?`
			args = append(args, executionInputContentHash(content, nil))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+target.table+`(`+columns+`) VALUES(`+values+`)`, args...); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_tool_reconciliation_inputs(agent_tool_call_id,input_id,execution_mode,execution_id,input_owner_id,content_hash,content_size_bytes,created_at)
			VALUES(?,?,?,?,?,?,?,?)`, call.AgentToolCallID, inputID, mode, executionID, ownerID, sha256Hex([]byte(content)), len(content), formatTime(now)); err != nil {
			return err
		}
		payload := map[string]any{"input_id": inputID, "sequence": count + 1, "source": "tool_outcome_reconciliation", "agent_tool_call_id": call.AgentToolCallID}
		if target.turn != nil {
			_, err = s.appendAgentTurnEventTx(ctx, tx, *target.turn, "agent.turn.input_received", false, payload)
		} else {
			event, kind := "agent_task.input_received", "agent_task"
			var runID *string
			if target.runID != "" {
				event, kind, runID = "execution.input_received", "execution_attempt", &target.runID
			}
			_, err = s.appendEvent(ctx, tx, target.project, runID, nil, event, kind, ownerID, payload)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
