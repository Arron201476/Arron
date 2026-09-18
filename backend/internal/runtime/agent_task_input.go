package runtime

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

type AgentTaskInput struct {
	InputID            string                                   `json:"input_id"`
	AgentTaskID        string                                   `json:"agent_task_id"`
	UserID             string                                   `json:"user_id"`
	Sequence           int                                      `json:"sequence"`
	Content            string                                   `json:"content"`
	Status             string                                   `json:"status"`
	CreatedAt          time.Time                                `json:"created_at"`
	IncludedAt         *time.Time                               `json:"included_at,omitempty"`
	CanModify          bool                                     `json:"can_modify"`
	ReplacementInputID *string                                  `json:"replacement_input_id,omitempty"`
	Attachments        []agentcontract.ExecutionInputAttachment `json:"attachments,omitempty"`
}

type AppendAgentTaskInputCommand struct {
	CommandMeta
	AgentTaskID    string
	Content        string
	AttachmentRefs []agentcontract.AttachmentRef
}

func migrateAgentTaskInputs(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_task_inputs (
		input_id TEXT PRIMARY KEY, agent_task_id TEXT NOT NULL REFERENCES agent_tasks(agent_task_id) ON DELETE CASCADE,
		user_id TEXT NOT NULL, sequence INTEGER NOT NULL, content TEXT NOT NULL,
		status TEXT NOT NULL CHECK(status IN ('received','included')),
		agent_task_attempt_id TEXT REFERENCES agent_task_attempts(agent_task_attempt_id),
		created_at TEXT NOT NULL, included_at TEXT, UNIQUE(agent_task_id, sequence)
	)`); err != nil {
		return err
	}
	present, err := tableHasColumn(db, "agent_tasks", "input_pause_requested")
	if err == nil && !present {
		_, err = db.Exec(`ALTER TABLE agent_tasks ADD COLUMN input_pause_requested INTEGER NOT NULL DEFAULT 0`)
	}
	return err
}

func (s *Store) AppendAgentTaskInput(ctx context.Context, command AppendAgentTaskInputCommand) (AgentTaskInput, error) {
	command.Content = strings.TrimSpace(command.Content)
	if !validAdditionalInputEnvelope(command.Content, len(command.AttachmentRefs)) {
		return AgentTaskInput{}, domainError("REQUEST_VALIDATION_FAILED", "追加内容不能为空且不能超过 32 KiB。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTaskInput{}, err
	}
	defer tx.Rollback()
	task, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id=?`, command.AgentTaskID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTaskInput{}, domainError("AGENT_TASK_NOT_FOUND", "后台任务不存在。")
	}
	if err != nil {
		return AgentTaskInput{}, err
	}
	if _, err := projectFilesWorkspace(ctx, tx, task.ProjectID, true); err != nil {
		return AgentTaskInput{}, err
	}
	if user, ok := identity.UserFromContext(ctx); ok && user.UserID != task.UserID {
		return AgentTaskInput{}, domainError("ROLE_FORBIDDEN", "只能向本人发起的后台任务追加要求。")
	}
	if _, ok := AgentActivityFromContext(ctx); ok {
		return AgentTaskInput{}, domainError("ROLE_FORBIDDEN", "追加用户要求必须由用户提交。")
	}
	if err := validateExecutionOwnerTx(ctx, tx, executionToolState{WorkspaceID: task.WorkspaceID, UserID: task.UserID}); err != nil {
		return AgentTaskInput{}, err
	}
	if command.Scope == "" {
		command.Scope = task.AgentTaskID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return AgentTaskInput{}, err
	}
	if hit {
		input, err := decodeIdempotentResult[AgentTaskInput](cached)
		if err != nil {
			return AgentTaskInput{}, err
		}
		inputs, err := loadAgentTaskInputs(ctx, tx, task.AgentTaskID)
		if err != nil {
			return AgentTaskInput{}, err
		}
		for _, current := range inputs {
			if current.InputID == input.InputID {
				return current, nil
			}
		}
		return AgentTaskInput{}, domainError("AGENT_TASK_INPUT_CONFLICT", "追加输入记录已不存在。")
	}
	if task.CancelRequested || !containsString([]string{"queued", "running", "waiting_approval", "pausing", "paused"}, task.Status) {
		return AgentTaskInput{}, domainError("AGENT_TASK_STATE_CONFLICT", "任务已结束或正在取消，不能追加要求。")
	}
	var count, size int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(length(CAST(content AS BLOB))),0) FROM agent_task_inputs WHERE agent_task_id=?`, task.AgentTaskID).Scan(&count, &size); err != nil {
		return AgentTaskInput{}, err
	}
	if count >= 128 || size+len(command.Content) > 512<<10 {
		return AgentTaskInput{}, domainError("REQUEST_VALIDATION_FAILED", "本任务的追加输入已达到数量或大小上限。")
	}
	attachments, attachmentsJSON, err := s.freezeInputAttachmentsTx(ctx, tx, task.ProjectID, "agent_task_inputs", "agent_task_id", task.AgentTaskID, command.AttachmentRefs)
	if err != nil {
		return AgentTaskInput{}, err
	}
	now := s.now()
	input := AgentTaskInput{InputID: s.newID("agin"), AgentTaskID: task.AgentTaskID, UserID: identity.UserIDFromContext(ctx), Sequence: count + 1, Content: command.Content, Status: "received", CreatedAt: now}
	input.CanModify = canChangeInput(ctx, input.UserID, true, input.Status)
	input.Attachments = attachments
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_task_inputs(input_id, agent_task_id, user_id, sequence, content, status, created_at, attachments_json) VALUES(?,?,?,?,?,'received',?,?)`, input.InputID, task.AgentTaskID, input.UserID, input.Sequence, input.Content, formatTime(now), attachmentsJSON); err != nil {
		return AgentTaskInput{}, err
	}
	if task.Status == "running" {
		_, err = tx.ExecContext(ctx, `UPDATE agent_tasks SET status='pausing', input_pause_requested=1, progress_message='等待当前轮结束后追加要求', updated_at=? WHERE agent_task_id=?`, formatTime(now), task.AgentTaskID)
	} else if task.Status == "waiting_approval" || task.Status == "queued" {
		// A new instruction never silently approves an old pending tool.
		_, err = tx.ExecContext(ctx, `UPDATE agent_tasks SET status='paused', input_pause_requested=0, progress_message='追加要求已接收，继续前请核对工具授权', updated_at=? WHERE agent_task_id=? AND (status='waiting_approval' OR EXISTS(SELECT 1 FROM agent_task_run_states WHERE agent_task_id=? AND json_array_length(pending_sdk_tool_call_ids_json)>0))`, formatTime(now), task.AgentTaskID, task.AgentTaskID)
	}
	if err != nil {
		return AgentTaskInput{}, err
	}
	if _, err := s.appendEvent(ctx, tx, task.ProjectID, nil, nil, "agent_task.input_received", "agent_task", task.AgentTaskID, map[string]any{"input_id": input.InputID}); err != nil {
		return AgentTaskInput{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, input, now); err != nil {
		return AgentTaskInput{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTaskInput{}, err
	}
	return input, nil
}

func loadAgentTaskInputs(ctx context.Context, query projectWorkspaceQuery, taskID string) ([]AgentTaskInput, error) {
	rows, err := query.QueryContext(ctx, `SELECT input_id, agent_task_id, user_id, sequence, content, `+projectedInputStatus+`, created_at, included_at, agent_task_attempt_id IS NULL AND `+editableReconciliationInput+`,replacement_input_id,attachments_json FROM agent_task_inputs WHERE agent_task_id=? ORDER BY sequence`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AgentTaskInput{}
	for rows.Next() {
		var input AgentTaskInput
		var created, attachmentsJSON string
		var included sql.NullString
		if err := rows.Scan(&input.InputID, &input.AgentTaskID, &input.UserID, &input.Sequence, &input.Content, &input.Status, &created, &included, &input.CanModify, &input.ReplacementInputID, &attachmentsJSON); err != nil {
			return nil, err
		}
		input.Attachments, err = decodeInputAttachments(attachmentsJSON)
		if err != nil {
			return nil, err
		}
		input.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		if included.Valid {
			at, err := parseTime(included.String)
			if err != nil {
				return nil, err
			}
			input.IncludedAt = &at
		}
		input.CanModify = canChangeInput(ctx, input.UserID, input.CanModify, input.Status)
		items = append(items, input)
	}
	return items, rows.Err()
}

func bindAgentTaskInputsTx(ctx context.Context, tx *sql.Tx, claim *AgentTaskClaim) error {
	if _, err := tx.ExecContext(ctx, `UPDATE agent_task_inputs SET agent_task_attempt_id=? WHERE agent_task_id=? AND withdrawn_at IS NULL`, claim.Attempt.AgentTaskAttemptID, claim.Task.AgentTaskID); err != nil {
		return err
	}
	var err error
	claim.AdditionalInputs, err = loadAgentTaskInputs(ctx, tx, claim.Task.AgentTaskID)
	claim.AdditionalInputs = slices.DeleteFunc(claim.AdditionalInputs, func(input AgentTaskInput) bool { return input.Status == "withdrawn" || input.Status == "superseded" })
	return err
}

func includeAgentTaskInputsTx(ctx context.Context, tx *sql.Tx, taskID, attemptID string, ids []string, now time.Time) error {
	if len(ids) > 128 {
		return domainError("REQUEST_VALIDATION_FAILED", "追加输入回执超过上限。")
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT input_id FROM agent_task_inputs WHERE agent_task_id=? AND agent_task_attempt_id=? AND withdrawn_at IS NULL ORDER BY sequence`, taskID, attemptID)
	if err != nil {
		return err
	}
	claimed := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		claimed = append(claimed, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) > len(claimed) || !slices.Equal(ids, claimed[:min(len(ids), len(claimed))]) {
		return domainError("AGENT_TASK_INPUT_CONFLICT", "追加输入回执必须是当前领取记录的有序前缀。")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			return domainError("AGENT_TASK_INPUT_CONFLICT", "追加输入回执重复或缺少标识。")
		}
		seen[id] = true
		result, err := tx.ExecContext(ctx, `UPDATE agent_task_inputs SET status='included', included_at=COALESCE(included_at,?) WHERE input_id=? AND agent_task_id=? AND agent_task_attempt_id=?`, formatTime(now), id, taskID, attemptID)
		if err != nil {
			return err
		}
		if err := requireOneRow(result, "AGENT_TASK_INPUT_CONFLICT", "追加输入未绑定到当前执行尝试。"); err != nil {
			return err
		}
	}
	return nil
}
