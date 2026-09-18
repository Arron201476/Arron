package runtime

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"unicode/utf8"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

type ChangeExecutionInputCommand struct {
	CommandMeta
	ProjectID   string `json:"-"`
	Mode        string `json:"mode"`
	ExecutionID string `json:"execution_id"`
	InputID     string `json:"input_id"`
	Action      string `json:"action"`
	Content     string `json:"content"`
}

type ExecutionInputChange struct {
	Mode               string `json:"mode"`
	ExecutionID        string `json:"execution_id"`
	InputID            string `json:"input_id"`
	Status             string `json:"status"`
	ReplacementInputID string `json:"replacement_input_id,omitempty"`
}

func migrateExecutionInputChanges(db *sql.DB) error {
	for _, table := range []string{"agent_turn_inputs", "agent_task_inputs", "execution_inputs"} {
		for _, column := range []string{"withdrawn_at", "replacement_input_id"} {
			present, err := tableHasColumn(db, table, column)
			if err != nil {
				return err
			}
			if !present {
				if _, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` TEXT`); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

type executionInputChangeTarget struct {
	table, ownerColumn, boundColumn string
	project, workspace, user        string
	active                          bool
	turn                            *AgentTurn
	runID                           string
}

func inputChangeTargetTx(ctx context.Context, tx *sql.Tx, mode, id string) (executionInputChangeTarget, error) {
	var target executionInputChangeTarget
	switch mode {
	case "conversation":
		turn, err := scanAgentTurn(tx.QueryRowContext(ctx, agentTurnSelect+` WHERE agent_turn_id=?`, id))
		if err != nil {
			return target, err
		}
		target = executionInputChangeTarget{table: "agent_turn_inputs", ownerColumn: "agent_turn_id", boundColumn: "dispatch_generation", project: turn.ProjectID, workspace: turn.WorkspaceID, user: turn.UserID, turn: &turn,
			active: containsString([]string{"accepted", "running", "waiting_approval", "pausing", "paused"}, turn.Status)}
	case "background_task":
		task, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id=?`, id))
		if err != nil {
			return target, err
		}
		target = executionInputChangeTarget{table: "agent_task_inputs", ownerColumn: "agent_task_id", boundColumn: "agent_task_attempt_id", project: task.ProjectID, workspace: task.WorkspaceID, user: task.UserID,
			active: !task.CancelRequested && containsString([]string{"queued", "running", "waiting_approval", "pausing", "paused"}, task.Status)}
	case "stateful_workflow":
		state, err := loadExecutionToolStateTx(ctx, tx, id)
		if err != nil {
			return target, err
		}
		attempt, err := scanExecutionAttempt(tx.QueryRowContext(ctx, executionAttemptSelect+` WHERE attempt_id=?`, id))
		if err != nil {
			return target, err
		}
		target = executionInputChangeTarget{table: "execution_inputs", ownerColumn: "attempt_id", boundColumn: "claim_token_hash", project: state.ProjectID, workspace: state.WorkspaceID, user: state.UserID, runID: state.RunID, active: executionInputAppendable(state, attempt)}
	default:
		return target, domainError("REQUEST_VALIDATION_FAILED", "执行类型无效。")
	}
	return target, nil
}

func (s *Store) ChangeExecutionInput(ctx context.Context, command ChangeExecutionInputCommand) (ExecutionInputChange, error) {
	var empty ExecutionInputChange
	user, ok := identity.FromContext(ctx)
	if !ok || !user.ValidUser() {
		return empty, domainError("ROLE_FORBIDDEN", "修改追加要求必须由用户本人提交。")
	}
	if _, agent := AgentActivityFromContext(ctx); agent {
		return empty, domainError("ROLE_FORBIDDEN", "Agent 不能修改用户追加要求。")
	}
	command.Content = strings.TrimSpace(command.Content)
	if (command.Action != "withdraw" && command.Action != "revise") || command.ExecutionID == "" || len(command.ExecutionID) > 256 || command.InputID == "" || len(command.InputID) > 256 ||
		(command.Action == "withdraw" && command.Content != "") || (command.Action == "revise" && (command.Content == "" || len(command.Content) > 32<<10 || !utf8.ValidString(command.Content) || strings.ContainsRune(command.Content, 0))) {
		return empty, domainError("REQUEST_VALIDATION_FAILED", "追加要求变更内容无效。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if _, err := projectFilesWorkspace(ctx, tx, command.ProjectID, true); err != nil {
		return empty, err
	}
	target, err := inputChangeTargetTx(ctx, tx, command.Mode, command.ExecutionID)
	if err == sql.ErrNoRows {
		return empty, domainError("EXECUTION_INPUT_NOT_FOUND", "追加要求不存在。")
	}
	if err != nil {
		return empty, err
	}
	if target.project != command.ProjectID {
		return empty, domainError("EXECUTION_INPUT_NOT_FOUND", "追加要求不属于当前作品。")
	}
	if user.UserID != target.user {
		return empty, domainError("ROLE_FORBIDDEN", "只能修改本人执行的追加要求。")
	}
	if err := validateExecutionOwnerTx(ctx, tx, executionToolState{WorkspaceID: target.workspace, UserID: target.user}); err != nil {
		return empty, err
	}
	command.Scope = command.Mode + ":" + command.InputID
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return empty, err
	}
	if hit {
		return decodeIdempotentResult[ExecutionInputChange](cached)
	}
	var inputUser, status string
	var unclaimed, unchanged bool
	err = tx.QueryRowContext(ctx, `SELECT user_id,status,`+target.boundColumn+` IS NULL,withdrawn_at IS NULL FROM `+target.table+` WHERE input_id=? AND `+target.ownerColumn+`=?`, command.InputID, command.ExecutionID).Scan(&inputUser, &status, &unclaimed, &unchanged)
	if err == sql.ErrNoRows {
		return empty, domainError("EXECUTION_INPUT_NOT_FOUND", "追加要求不存在。")
	}
	if err != nil {
		return empty, err
	}
	if inputUser != user.UserID {
		return empty, domainError("ROLE_FORBIDDEN", "只能修改本人提交的追加要求。")
	}
	if !unclaimed || !unchanged || status != "received" {
		return empty, domainError("EXECUTION_INPUT_CONFLICT", "追加要求已被领取、已送入模型或已变更，不能撤回或覆盖；请另行追加更正。")
	}
	var reconciliation bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_tool_reconciliation_inputs WHERE input_id=?)`, command.InputID).Scan(&reconciliation); err != nil {
		return empty, err
	}
	if reconciliation {
		return empty, domainError("EXECUTION_INPUT_CONFLICT", "外部操作核对依据必须保留，不能撤回或覆盖。")
	}
	if command.Action == "revise" && !target.active {
		return empty, domainError("EXECUTION_INPUT_CONFLICT", "执行已结束，不能提交修订。")
	}
	now := s.now()
	receipt := ExecutionInputChange{Mode: command.Mode, ExecutionID: command.ExecutionID, InputID: command.InputID, Status: "withdrawn"}
	if command.Action == "revise" {
		var originalJSON string
		if err := tx.QueryRowContext(ctx, `SELECT attachments_json FROM `+target.table+` WHERE input_id=?`, command.InputID).Scan(&originalJSON); err != nil {
			return empty, err
		}
		original, err := decodeInputAttachments(originalJSON)
		if err != nil {
			return empty, err
		}
		refs := make([]agentcontract.AttachmentRef, 0, len(original))
		for _, item := range original {
			refs = append(refs, agentcontract.AttachmentRef{AssetID: item.AssetID, AssetSnapshotID: item.AssetSnapshotID})
		}
		attachments, attachmentsJSON, err := s.freezeInputAttachmentsTx(ctx, tx, target.project, target.table, target.ownerColumn, command.ExecutionID, refs)
		if err != nil {
			return empty, err
		}
		if !slices.Equal(original, attachments) {
			return empty, domainError("EXECUTION_INPUT_CONFLICT", "原追加附件快照已变化，请重新提交材料。")
		}
		var count, size int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(content AS BLOB))),0) FROM `+target.table+` WHERE `+target.ownerColumn+`=?`, command.ExecutionID).Scan(&count, &size); err != nil {
			return empty, err
		}
		if count >= 128 || size+len(command.Content) > 512<<10 {
			return empty, domainError("REQUEST_VALIDATION_FAILED", "追加输入已达到数量或大小上限。")
		}
		receipt.ReplacementInputID = s.newID("inin")
		receipt.Status = "superseded"
		columns, values := `input_id,`+target.ownerColumn+`,user_id,sequence,content,status,created_at,attachments_json`, `?,?,?,?,?,'received',?,?`
		args := []any{receipt.ReplacementInputID, command.ExecutionID, user.UserID, count + 1, command.Content, formatTime(now), attachmentsJSON}
		if command.Mode == "stateful_workflow" {
			columns += `,content_hash`
			values += `,?`
			args = append(args, executionInputContentHash(command.Content, attachments))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+target.table+`(`+columns+`) VALUES(`+values+`)`, args...); err != nil {
			return empty, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE `+target.table+` SET withdrawn_at=?,replacement_input_id=? WHERE input_id=? AND `+target.ownerColumn+`=? AND withdrawn_at IS NULL AND status='received' AND `+target.boundColumn+` IS NULL`, formatTime(now), nullableStringPointer(receipt.ReplacementInputID), command.InputID, command.ExecutionID)
	if err != nil {
		return empty, err
	}
	if err := requireOneRow(result, "EXECUTION_INPUT_CONFLICT", "追加要求已被领取或修改。"); err != nil {
		return empty, err
	}
	payload := map[string]any{"input_id": receipt.InputID, "status": receipt.Status, "replacement_input_id": receipt.ReplacementInputID}
	if target.turn != nil {
		_, err = s.appendAgentTurnEventTx(ctx, tx, *target.turn, "agent.turn.input_changed", false, payload)
	} else {
		event, kind := "agent_task.input_changed", "agent_task"
		var runID *string
		if target.runID != "" {
			event, kind, runID = "execution.input_changed", "execution_attempt", &target.runID
		}
		_, err = s.appendEvent(ctx, tx, target.project, runID, nil, event, kind, command.ExecutionID, payload)
	}
	if err != nil {
		return empty, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, receipt, now); err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return receipt, nil
}

func canChangeInput(ctx context.Context, userID string, unclaimed bool, status string) bool {
	user, ok := identity.FromContext(ctx)
	return ok && user.ValidUser() && user.UserID == userID && user.Allows(identity.RoleEditor) && unclaimed && status == "received"
}

const projectedInputStatus = `CASE WHEN withdrawn_at IS NOT NULL THEN CASE WHEN replacement_input_id IS NOT NULL THEN 'superseded' ELSE 'withdrawn' END ELSE status END`
