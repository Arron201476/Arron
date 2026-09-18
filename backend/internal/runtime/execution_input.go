package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

type ExecutionInput struct {
	InputID            string                                   `json:"input_id"`
	AttemptID          string                                   `json:"attempt_id"`
	UserID             string                                   `json:"user_id"`
	Sequence           int                                      `json:"sequence"`
	Content            string                                   `json:"content"`
	ContentHash        string                                   `json:"content_hash"`
	Status             string                                   `json:"status"`
	CreatedAt          time.Time                                `json:"created_at"`
	IncludedAt         *time.Time                               `json:"included_at,omitempty"`
	CanModify          bool                                     `json:"can_modify"`
	ReplacementInputID *string                                  `json:"replacement_input_id,omitempty"`
	Attachments        []agentcontract.ExecutionInputAttachment `json:"attachments,omitempty"`
}

type ExecutionInputsView struct {
	AttemptID  string           `json:"attempt_id"`
	RunID      string           `json:"run_id"`
	TaskItemID string           `json:"task_item_id"`
	Status     string           `json:"status"`
	CanAppend  bool             `json:"can_append"`
	Inputs     []ExecutionInput `json:"inputs"`
}

type AppendExecutionInputCommand struct {
	CommandMeta
	ProjectID, AttemptID, Content string
	AttachmentRefs                []agentcontract.AttachmentRef
}

type RecordExecutionInputsCommand struct {
	AttemptID         string   `json:"-"`
	AttemptToken      string   `json:"-"`
	InputSnapshotHash string   `json:"input_snapshot_hash"`
	IncludedInputIDs  []string `json:"included_input_ids"`
}

func migrateExecutionInputs(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS execution_inputs (
		input_id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL REFERENCES execution_attempts(attempt_id) ON DELETE CASCADE,
		user_id TEXT NOT NULL, sequence INTEGER NOT NULL CHECK(sequence > 0), content TEXT NOT NULL,
		content_hash TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('received','included')),
		claim_token_hash TEXT, created_at TEXT NOT NULL, included_at TEXT, UNIQUE(attempt_id, sequence)
	)`)
	return err
}

func executionInputAppendable(state executionToolState, attempt ExecutionAttempt) bool {
	return !state.ProjectDeleted && state.CurrentAttemptID == state.AttemptID &&
		attempt.ProviderID == "openai_agents_sdk" && attempt.ExecutorID != "workflow.video_script_extract" &&
		slices.Contains([]string{"running", "paused", "waiting_approval", "repair_pending"}, state.AttemptStatus) &&
		state.TaskStatus == state.AttemptStatus && slices.Contains([]string{"running", "pausing", "paused"}, state.RunStatus) &&
		slices.Contains([]string{"running", "paused"}, state.StepStatus)
}

func (s *Store) executionInputsViewTx(ctx context.Context, tx *sql.Tx, projectID, attemptID string) (ExecutionInputsView, executionToolState, error) {
	view := ExecutionInputsView{AttemptID: attemptID, Inputs: []ExecutionInput{}}
	state, err := loadExecutionToolStateTx(ctx, tx, attemptID)
	if err != nil {
		return view, state, err
	}
	if state.ProjectID != projectID {
		return view, state, domainError("EXECUTION_ATTEMPT_NOT_FOUND", "执行尝试不属于当前作品。")
	}
	if _, err := projectFilesWorkspace(ctx, tx, projectID, false); err != nil {
		return view, state, err
	}
	attempt, err := scanExecutionAttempt(tx.QueryRowContext(ctx, executionAttemptSelect+` WHERE attempt_id=?`, attemptID))
	if err != nil {
		return view, state, err
	}
	view.RunID, view.TaskItemID, view.Status = state.RunID, attempt.TaskItemID, state.AttemptStatus
	user, ok := identity.UserFromContext(ctx)
	view.CanAppend = ok && user.UserID == state.UserID && user.Allows(identity.RoleEditor) && executionInputAppendable(state, attempt)
	view.Inputs, err = loadExecutionInputs(ctx, tx, attemptID)
	return view, state, err
}

func (s *Store) GetExecutionInputs(ctx context.Context, projectID, attemptID string) (ExecutionInputsView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExecutionInputsView{}, err
	}
	defer tx.Rollback()
	view, _, err := s.executionInputsViewTx(ctx, tx, projectID, attemptID)
	return view, err
}

func (s *Store) AppendExecutionInput(ctx context.Context, command AppendExecutionInputCommand) (ExecutionInput, error) {
	command.Content = strings.TrimSpace(command.Content)
	if !validAdditionalInputEnvelope(command.Content, len(command.AttachmentRefs)) {
		return ExecutionInput{}, domainError("REQUEST_VALIDATION_FAILED", "追加内容不能为空且不能超过 32 KiB。")
	}
	principal, authenticated := identity.FromContext(ctx)
	if !authenticated || principal.Kind != identity.KindUser {
		return ExecutionInput{}, domainError("ROLE_FORBIDDEN", "追加用户要求必须由用户本人提交。")
	}
	if _, agent := AgentActivityFromContext(ctx); agent {
		return ExecutionInput{}, domainError("ROLE_FORBIDDEN", "Agent 不能冒充用户追加要求。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExecutionInput{}, err
	}
	defer tx.Rollback()
	view, state, err := s.executionInputsViewTx(ctx, tx, command.ProjectID, command.AttemptID)
	if err != nil {
		return ExecutionInput{}, err
	}
	if _, err := projectFilesWorkspace(ctx, tx, state.ProjectID, true); err != nil {
		return ExecutionInput{}, err
	}
	if principal.UserID != state.UserID {
		return ExecutionInput{}, domainError("ROLE_FORBIDDEN", "只能向本人发起的执行追加要求。")
	}
	if err := validateExecutionOwnerTx(ctx, tx, state); err != nil {
		return ExecutionInput{}, err
	}
	command.Scope = state.AttemptID
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return ExecutionInput{}, err
	}
	if hit {
		input, err := decodeIdempotentResult[ExecutionInput](cached)
		if err != nil {
			return ExecutionInput{}, err
		}
		for _, current := range view.Inputs {
			if current.InputID == input.InputID {
				return current, nil
			}
		}
		return ExecutionInput{}, domainError("EXECUTION_INPUT_CONFLICT", "追加输入记录已不存在。")
	}
	if !view.CanAppend {
		return ExecutionInput{}, domainError("EXECUTION_INPUT_CONFLICT", "本次执行已结束、正在提交结果或不支持中途追加。")
	}
	size := len(command.Content)
	for _, input := range view.Inputs {
		size += len(input.Content)
	}
	if len(view.Inputs) >= 128 || size > 512<<10 {
		return ExecutionInput{}, domainError("REQUEST_VALIDATION_FAILED", "本次执行的追加要求已达到数量或大小上限。")
	}
	attachments, attachmentsJSON, err := s.freezeInputAttachmentsTx(ctx, tx, state.ProjectID, "execution_inputs", "attempt_id", state.AttemptID, command.AttachmentRefs)
	if err != nil {
		return ExecutionInput{}, err
	}
	now := s.now()
	input := ExecutionInput{InputID: s.newID("exin"), AttemptID: state.AttemptID, UserID: state.UserID, Sequence: len(view.Inputs) + 1,
		Content: command.Content, ContentHash: executionInputContentHash(command.Content, attachments), Attachments: attachments, Status: "received", CreatedAt: now}
	input.CanModify = canChangeInput(ctx, input.UserID, true, input.Status)
	if _, err := tx.ExecContext(ctx, `INSERT INTO execution_inputs(input_id,attempt_id,user_id,sequence,content,content_hash,status,created_at,attachments_json) VALUES(?,?,?,?,?,?,'received',?,?)`,
		input.InputID, input.AttemptID, input.UserID, input.Sequence, input.Content, input.ContentHash, formatTime(now), attachmentsJSON); err != nil {
		return ExecutionInput{}, err
	}
	// Appending while old tools await approval must not silently resume them.
	var approvalBoundary bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_run_states WHERE attempt_id=? AND json_array_length(pending_sdk_tool_call_ids_json)>0)
		OR EXISTS(SELECT 1 FROM execution_tool_calls b JOIN agent_tool_calls c ON c.agent_tool_call_id=b.agent_tool_call_id WHERE b.execution_attempt_id=? AND c.status='pending_approval')`, state.AttemptID, state.AttemptID).Scan(&approvalBoundary); err != nil {
		return ExecutionInput{}, err
	}
	if approvalBoundary && state.RunStatus == "running" {
		run, err := getRunTx(ctx, tx, state.RunID)
		if err != nil {
			return ExecutionInput{}, err
		}
		active, err := runHasActiveExecutionTx(ctx, tx, run)
		if err != nil {
			return ExecutionInput{}, err
		}
		if active {
			_, err = tx.ExecContext(ctx, `UPDATE runs SET status='pausing',updated_at=? WHERE run_id=?`, formatTime(now), run.RunID)
		} else {
			err = s.pauseRunningStepTx(ctx, tx, run, nil, now, "additional_input_approval_review")
		}
		if err != nil {
			return ExecutionInput{}, err
		}
	}
	if _, err := s.appendEvent(ctx, tx, state.ProjectID, &state.RunID, nil, "execution.input_received", "execution_attempt", state.AttemptID, map[string]any{"input_id": input.InputID}); err != nil {
		return ExecutionInput{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, input, now); err != nil {
		return ExecutionInput{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExecutionInput{}, err
	}
	return input, nil
}

func loadExecutionInputs(ctx context.Context, query projectWorkspaceQuery, attemptID string) ([]ExecutionInput, error) {
	rows, err := query.QueryContext(ctx, `SELECT input_id,attempt_id,user_id,sequence,content,content_hash,`+projectedInputStatus+`,created_at,included_at,claim_token_hash IS NULL AND `+editableReconciliationInput+`,replacement_input_id,attachments_json FROM execution_inputs WHERE attempt_id=? ORDER BY sequence`, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inputs := []ExecutionInput{}
	size := 0
	for rows.Next() {
		var input ExecutionInput
		var created, attachmentsJSON string
		var included sql.NullString
		if err := rows.Scan(&input.InputID, &input.AttemptID, &input.UserID, &input.Sequence, &input.Content, &input.ContentHash, &input.Status, &created, &included, &input.CanModify, &input.ReplacementInputID, &attachmentsJSON); err != nil {
			return nil, err
		}
		input.Attachments, err = decodeInputAttachments(attachmentsJSON)
		if err != nil {
			return nil, err
		}
		size += len(input.Content)
		if input.Sequence != len(inputs)+1 || len(inputs) >= 128 || size > 512<<10 || !validAdditionalInputEnvelope(input.Content, len(input.Attachments)) || executionInputContentHash(input.Content, input.Attachments) != input.ContentHash {
			return nil, domainError("AGENT_RUN_STATE_CORRUPT", "追加输入内容或顺序校验失败。")
		}
		input.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		input.IncludedAt, err = optionalTime(included)
		if err != nil {
			return nil, err
		}
		input.CanModify = canChangeInput(ctx, input.UserID, input.CanModify, input.Status)
		inputs = append(inputs, input)
	}
	return inputs, rows.Err()
}

func bindExecutionInputsTx(ctx context.Context, tx *sql.Tx, claim *TaskClaim) error {
	inputs := activeExecutionInputs(claim.AdditionalInputs)
	if len(inputs) > 0 && claim.Attempt.ProviderID != "openai_agents_sdk" {
		return domainError("EXECUTION_INPUT_CONFLICT", "追加要求必须由 SDK Worker 接收。")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_inputs SET claim_token_hash=? WHERE attempt_id=? AND withdrawn_at IS NULL`, sha256Hex([]byte(claim.AttemptToken)), claim.Attempt.AttemptID); err != nil {
		return err
	}
	claim.AdditionalInputs = inputs
	return nil
}

func (s *Store) RecordExecutionInputsIncluded(ctx context.Context, command RecordExecutionInputsCommand) error {
	if command.AttemptID == "" || len(command.AttemptID) > 256 || len(command.AttemptToken) > 512 || len(command.IncludedInputIDs) > 128 {
		return domainError("REQUEST_VALIDATION_FAILED", "追加收件请求无效。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state, err := loadExecutionToolStateTx(ctx, tx, command.AttemptID)
	if err != nil {
		return err
	}
	if err := validateExecutionToolToken(state, command.AttemptToken); err != nil {
		return err
	}
	if activity, ok := AgentActivityFromContext(ctx); ok && (activity.ProjectID != state.ProjectID || activity.ExecutionAttemptID != state.AttemptID || activity.AttemptToken != command.AttemptToken || activity.AgentTurnID != "" || activity.AgentTaskAttemptID != "") {
		return domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "收件请求不属于当前执行。")
	}
	if err := validateExecutionOwnerTx(ctx, tx, state); err != nil {
		return err
	}
	if err := validateExecutionToolState(state, s.now()); err != nil {
		return err
	}
	var hash, provider string
	if err := tx.QueryRowContext(ctx, `SELECT input_snapshot_hash,provider_id FROM execution_attempts WHERE attempt_id=?`, state.AttemptID).Scan(&hash, &provider); err != nil {
		return err
	}
	if hash != command.InputSnapshotHash || provider != "openai_agents_sdk" {
		return domainError("EXECUTION_INPUT_CONFLICT", "收件请求的输入快照或执行器不匹配。")
	}
	inputs, err := loadExecutionInputs(ctx, tx, state.AttemptID)
	inputs = activeExecutionInputs(inputs)
	if err != nil {
		return err
	}
	if len(command.IncludedInputIDs) > len(inputs) {
		return domainError("EXECUTION_INPUT_CONFLICT", "追加输入未被当前执行领取。")
	}
	now, newIDs := s.now(), []string{}
	for index, id := range command.IncludedInputIDs {
		if id != inputs[index].InputID {
			return domainError("EXECUTION_INPUT_CONFLICT", "模型收件必须保持领取顺序且不能重复。")
		}
		result, err := tx.ExecContext(ctx, `UPDATE execution_inputs SET status='included',included_at=COALESCE(included_at,?) WHERE input_id=? AND claim_token_hash=?`, formatTime(now), id, state.TokenHash)
		if err != nil {
			return err
		}
		if err := requireOneRow(result, "EXECUTION_INPUT_CONFLICT", "追加输入未绑定到本次领取。"); err != nil {
			return err
		}
		if inputs[index].Status != "included" {
			newIDs = append(newIDs, id)
		}
	}
	if len(newIDs) > 0 {
		if _, err := s.appendEvent(ctx, tx, state.ProjectID, &state.RunID, nil, "execution.inputs_included", "execution_attempt", state.AttemptID, map[string]any{"input_ids": newIDs}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func validateExecutionInputCheckpointTx(ctx context.Context, tx *sql.Tx, attemptID, token string, worker json.RawMessage) error {
	var envelope struct {
		Schema string                                    `json:"schema_version"`
		Ledger *struct{ IDs, Hashes, Included []string } `json:"input_ledger"`
	}
	if err := json.Unmarshal(worker, &envelope); err != nil {
		return err
	}
	var claimed int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_inputs WHERE attempt_id=? AND claim_token_hash=?`, attemptID, sha256Hex([]byte(token))).Scan(&claimed); err != nil {
		return err
	}
	if envelope.Ledger == nil {
		if claimed > 0 || envelope.Schema == "stateful_worker.v3" {
			return domainError("EXECUTION_INPUT_CONFLICT", "追加 checkpoint 缺少当前领取的输入记录。")
		}
		return nil
	}
	if envelope.Schema != "stateful_worker.v3" {
		return domainError("AGENT_RUN_STATE_CORRUPT", "追加 checkpoint 版本无效。")
	}
	inputs, err := loadExecutionInputs(ctx, tx, attemptID)
	inputs = activeExecutionInputs(inputs)
	if err != nil {
		return err
	}
	l := envelope.Ledger
	if len(l.IDs) != claimed || len(l.IDs) > len(inputs) || len(l.Hashes) != len(l.IDs) || len(l.Included) > len(l.IDs) {
		return domainError("EXECUTION_INPUT_CONFLICT", "追加 checkpoint 与领取不匹配。")
	}
	for i, id := range l.IDs {
		if id != inputs[i].InputID || l.Hashes[i] != inputs[i].ContentHash {
			return domainError("EXECUTION_INPUT_CONFLICT", "追加 checkpoint 内容或顺序已变化。")
		}
		var bound bool
		if err := tx.QueryRowContext(ctx, `SELECT claim_token_hash=? FROM execution_inputs WHERE input_id=?`, sha256Hex([]byte(token)), id).Scan(&bound); err != nil {
			return err
		}
		if !bound || (i < len(l.Included) && (l.Included[i] != id || inputs[i].Status != "included")) || (i >= len(l.Included) && inputs[i].Status == "included") {
			return domainError("EXECUTION_INPUT_CONFLICT", "追加 checkpoint 缺少匹配的模型收件事实。")
		}
	}
	return nil
}

func activeExecutionInputs(inputs []ExecutionInput) []ExecutionInput {
	return slices.DeleteFunc(slices.Clone(inputs), func(input ExecutionInput) bool { return input.Status == "withdrawn" || input.Status == "superseded" })
}
