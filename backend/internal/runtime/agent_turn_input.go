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

type AgentTurnInput struct {
	InputID            string                                   `json:"input_id"`
	AgentTurnID        string                                   `json:"agent_turn_id"`
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

type AppendAgentTurnInputCommand struct {
	CommandMeta
	AgentTurnID    string
	Content        string
	AttachmentRefs []agentcontract.AttachmentRef
}

func migrateAgentTurnInputs(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, column := range []string{"input_pause_requested", "dispatch_generation"} {
		var present bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('agent_turns') WHERE name=?)`, column).Scan(&present); err != nil {
			return err
		}
		if !present {
			if _, err := tx.Exec(`ALTER TABLE agent_turns ADD COLUMN ` + column + ` INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS agent_turn_inputs (
		input_id TEXT PRIMARY KEY,
		agent_turn_id TEXT NOT NULL REFERENCES agent_turns(agent_turn_id) ON DELETE CASCADE,
		user_id TEXT NOT NULL, sequence INTEGER NOT NULL CHECK(sequence > 0), content TEXT NOT NULL,
		status TEXT NOT NULL CHECK(status IN ('received','included')),
		dispatch_generation INTEGER, created_at TEXT NOT NULL, included_at TEXT,
		UNIQUE(agent_turn_id, sequence)
	)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AppendAgentTurnInput(ctx context.Context, command AppendAgentTurnInputCommand) (AgentTurnInput, error) {
	command.Content = strings.TrimSpace(command.Content)
	if !validAdditionalInputEnvelope(command.Content, len(command.AttachmentRefs)) {
		return AgentTurnInput{}, domainError("REQUEST_VALIDATION_FAILED", "追加内容不能为空且不能超过 32 KiB。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTurnInput{}, err
	}
	defer tx.Rollback()
	turn, err := scanAgentTurn(tx.QueryRowContext(ctx, agentTurnSelect+` WHERE agent_turn_id=?`, command.AgentTurnID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTurnInput{}, domainError("AGENT_TURN_NOT_FOUND", "消息不存在。")
	}
	if err != nil {
		return AgentTurnInput{}, err
	}
	if _, err := projectFilesWorkspace(ctx, tx, turn.ProjectID, true); err != nil {
		return AgentTurnInput{}, err
	}
	if user, ok := identity.UserFromContext(ctx); ok && user.UserID != turn.UserID {
		return AgentTurnInput{}, domainError("ROLE_FORBIDDEN", "只能向本人发起的对话执行追加要求。")
	}
	if _, ok := AgentActivityFromContext(ctx); ok {
		return AgentTurnInput{}, domainError("ROLE_FORBIDDEN", "追加用户要求必须由用户提交。")
	}
	if err := validateExecutionOwnerTx(ctx, tx, executionToolState{WorkspaceID: turn.WorkspaceID, UserID: turn.UserID}); err != nil {
		return AgentTurnInput{}, err
	}
	if command.Scope == "" {
		command.Scope = turn.AgentTurnID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return AgentTurnInput{}, err
	}
	if hit {
		input, err := decodeIdempotentResult[AgentTurnInput](cached)
		if err != nil {
			return AgentTurnInput{}, err
		}
		// Retry the immutable identity but return the current model receipt.
		input, err = scanAgentTurnInput(tx.QueryRowContext(ctx, agentTurnInputSelect+` WHERE input_id=? AND agent_turn_id=?`, input.InputID, turn.AgentTurnID))
		input.CanModify = canChangeInput(ctx, input.UserID, input.CanModify, input.Status)
		return input, err
	}
	if !containsString([]string{"accepted", "running", "waiting_approval", "pausing", "paused"}, turn.Status) {
		return AgentTurnInput{}, domainError("AGENT_TURN_STATE_CONFLICT", "本轮已结束或正在提交、取消，不能追加要求。")
	}
	var count, size int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(content AS BLOB))),0) FROM agent_turn_inputs WHERE agent_turn_id=?`, turn.AgentTurnID).Scan(&count, &size); err != nil {
		return AgentTurnInput{}, err
	}
	if count >= 128 || size+len(command.Content) > 512<<10 {
		return AgentTurnInput{}, domainError("REQUEST_VALIDATION_FAILED", "本轮的追加输入已达到数量或大小上限。")
	}
	attachments, attachmentsJSON, err := s.freezeInputAttachmentsTx(ctx, tx, turn.ProjectID, "agent_turn_inputs", "agent_turn_id", turn.AgentTurnID, command.AttachmentRefs)
	if err != nil {
		return AgentTurnInput{}, err
	}
	now := s.now()
	input := AgentTurnInput{InputID: s.newID("atin"), AgentTurnID: turn.AgentTurnID, UserID: identity.UserIDFromContext(ctx), Sequence: count + 1, Content: command.Content, Status: "received", CreatedAt: now}
	input.CanModify = canChangeInput(ctx, input.UserID, true, input.Status)
	input.Attachments = attachments
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_turn_inputs(input_id,agent_turn_id,user_id,sequence,content,status,created_at,attachments_json) VALUES(?,?,?,?,?,'received',?,?)`, input.InputID, turn.AgentTurnID, input.UserID, input.Sequence, input.Content, formatTime(now), attachmentsJSON); err != nil {
		return AgentTurnInput{}, err
	}
	status := turn.Status
	if turn.Status == "running" {
		status = "pausing"
		_, err = tx.ExecContext(ctx, `UPDATE agent_turns SET status='pausing',input_pause_requested=1,updated_at=? WHERE agent_turn_id=?`, formatTime(now), turn.AgentTurnID)
	} else if turn.Status == "waiting_approval" || turn.Status == "accepted" {
		// New input cannot silently run old approved checkpoint tools.
		var hasApprovalCheckpoint bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_turn_run_states WHERE agent_turn_id=? AND json_array_length(pending_sdk_tool_call_ids_json)>0)`, turn.AgentTurnID).Scan(&hasApprovalCheckpoint)
		if err == nil && (turn.Status == "waiting_approval" || hasApprovalCheckpoint) {
			status = "paused"
			_, err = tx.ExecContext(ctx, `UPDATE agent_turns SET status='paused',input_pause_requested=0,updated_at=? WHERE agent_turn_id=?`, formatTime(now), turn.AgentTurnID)
		}
	}
	if err != nil {
		return AgentTurnInput{}, err
	}
	turn.Status = status
	if _, err := s.appendAgentTurnEventTx(ctx, tx, turn, "agent.turn.input_received", false, map[string]any{"input_id": input.InputID, "status": status}); err != nil {
		return AgentTurnInput{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, input, now); err != nil {
		return AgentTurnInput{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTurnInput{}, err
	}
	return input, nil
}

const agentTurnInputSelect = `SELECT input_id,agent_turn_id,user_id,sequence,content,` + projectedInputStatus + `,created_at,included_at,dispatch_generation IS NULL AND ` + editableReconciliationInput + `,replacement_input_id,attachments_json FROM agent_turn_inputs`

func scanAgentTurnInput(row rowScanner) (AgentTurnInput, error) {
	var input AgentTurnInput
	var created, attachmentsJSON string
	var included sql.NullString
	if err := row.Scan(&input.InputID, &input.AgentTurnID, &input.UserID, &input.Sequence, &input.Content, &input.Status, &created, &included, &input.CanModify, &input.ReplacementInputID, &attachmentsJSON); err != nil {
		return AgentTurnInput{}, err
	}
	var err error
	input.Attachments, err = decodeInputAttachments(attachmentsJSON)
	if err != nil {
		return AgentTurnInput{}, err
	}
	input.CreatedAt, err = parseTime(created)
	if err != nil {
		return AgentTurnInput{}, err
	}
	if included.Valid {
		at, err := parseTime(included.String)
		if err != nil {
			return AgentTurnInput{}, err
		}
		input.IncludedAt = &at
	}
	return input, nil
}

func loadAgentTurnInputs(ctx context.Context, query projectWorkspaceQuery, turnID string) ([]AgentTurnInput, error) {
	rows, err := query.QueryContext(ctx, agentTurnInputSelect+` WHERE agent_turn_id=? ORDER BY sequence`, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inputs := []AgentTurnInput{}
	for rows.Next() {
		input, err := scanAgentTurnInput(rows)
		if err != nil {
			return nil, err
		}
		input.CanModify = canChangeInput(ctx, input.UserID, input.CanModify, input.Status)
		inputs = append(inputs, input)
	}
	return inputs, rows.Err()
}

func bindAgentTurnInputsTx(ctx context.Context, tx *sql.Tx, turn *AgentTurn) error {
	if _, err := tx.ExecContext(ctx, `UPDATE agent_turn_inputs SET dispatch_generation=? WHERE agent_turn_id=? AND withdrawn_at IS NULL`, turn.DispatchGeneration, turn.AgentTurnID); err != nil {
		return err
	}
	var err error
	turn.AdditionalInputs, err = loadAgentTurnInputs(ctx, tx, turn.AgentTurnID)
	turn.AdditionalInputs = slices.DeleteFunc(turn.AdditionalInputs, func(input AgentTurnInput) bool { return input.Status == "withdrawn" || input.Status == "superseded" })
	return err
}

// Only the trusted stream consumer can acknowledge a completed native model turn.
func (s *Store) RecordAgentTurnInputsIncluded(ctx context.Context, turnID string, generation int64, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	turn, err := scanAgentTurn(tx.QueryRowContext(ctx, agentTurnSelect+` WHERE agent_turn_id=?`, turnID))
	if err != nil {
		return err
	}
	if err := s.includeAgentTurnInputsTx(ctx, tx, turn, generation, ids, s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) includeAgentTurnInputsTx(ctx context.Context, tx *sql.Tx, turn AgentTurn, generation int64, ids []string, now time.Time) error {
	if generation <= 0 || generation != turn.DispatchGeneration || len(ids) > 128 ||
		!containsString([]string{"running", "pausing", "waiting_approval", "paused", "accepted", "committing", "committed"}, turn.Status) {
		return domainError("AGENT_TURN_INPUT_CONFLICT", "追加输入回执不属于当前执行调度。")
	}
	rows, err := tx.QueryContext(ctx, `SELECT input_id,status FROM agent_turn_inputs WHERE agent_turn_id=? AND dispatch_generation=? ORDER BY sequence`, turn.AgentTurnID, generation)
	if err != nil {
		return err
	}
	var claimed, statuses []string
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			rows.Close()
			return err
		}
		claimed, statuses = append(claimed, id), append(statuses, status)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) > len(claimed) {
		return domainError("AGENT_TURN_INPUT_CONFLICT", "追加输入尚未被当前执行领取。")
	}
	for index, id := range ids {
		if id != claimed[index] {
			return domainError("AGENT_TURN_INPUT_CONFLICT", "追加输入回执必须保持领取顺序且不能重复。")
		}
	}
	newIDs := []string{}
	for index, id := range ids {
		if statuses[index] == "included" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_turn_inputs SET status='included',included_at=? WHERE input_id=?`, formatTime(now), id); err != nil {
			return err
		}
		newIDs = append(newIDs, id)
	}
	if len(newIDs) > 0 {
		_, err = s.appendAgentTurnEventTx(ctx, tx, turn, "agent.turn.inputs_included", false, map[string]any{"input_ids": newIDs})
	}
	return err
}
