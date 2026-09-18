package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

const agentTurnSelect = `
	SELECT agent_turn_id, workspace_id, user_id, project_id, conversation_id,
	       idempotency_key, request_json, status, error_code, error_message,
	       user_message_id, agent_message_id, created_at, started_at,
	       completed_at, updated_at, provider_id, model_id, release_id,
	       trace_refs_json, response_ids_json, request_ids_json, usage_json,
	       latency_json, failure_stage, cancel_reason, skill_invocation_id,
	       agent_task_id, run_id, agent_tool_call_ids_json, dispatch_generation, input_pause_requested
	FROM agent_turns`

const agentTurnObservationSchema = "agent_turn_observation.v1"

var agentTurnObservationIdentifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

func agentTurnSubmissionID(userID, conversationID, key string) string {
	if key == "" {
		return ""
	}
	return sha256Hex([]byte(strings.Join([]string{"agent-submission-v1", userID, conversationID, key}, "\x00")))
}

var agentTurnProgressEvents = map[string]struct{}{
	"agent.updated":            {},
	"agent.tool.started":       {},
	"agent.tool.completed":     {},
	"agent.output.delta":       {},
	"agent.approval.requested": {},
	"agent.artifact.created":   {},
}

func (s *Store) AcceptAgentTurn(
	ctx context.Context,
	conversationID string,
	request agentcontract.MessageRequest,
	meta CommandMeta,
) (AgentTurn, error) {
	return s.acceptAgentTurn(ctx, conversationID, request, meta, nil)
}

// AcceptAndClaimAgentTurn reserves a newly accepted turn for a direct dispatcher.
// A replay returns the original receipt with claimed=false; it never grants a
// second execution. The caller owns cancellation and durable event persistence.
func (s *Store) AcceptAndClaimAgentTurn(
	ctx context.Context, conversationID string, request agentcontract.MessageRequest, meta CommandMeta,
) (turn AgentTurn, claimed bool, err error) {
	if meta.CommandType != "create_message" {
		return AgentTurn{}, false, domainError("REQUEST_VALIDATION_FAILED", "直接领取必须沿用正式消息提交命令。")
	}
	if strings.TrimSpace(meta.IdempotencyKey) == "" {
		return AgentTurn{}, false, domainError("REQUEST_VALIDATION_FAILED", "直接领取回合必须提供幂等键。")
	}
	turn, err = s.acceptAgentTurn(ctx, conversationID, request, meta, func(tx *sql.Tx, accepted *AgentTurn) error {
		var occupied int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_turns
			WHERE conversation_id = ? AND agent_turn_id != ?
			AND status IN ('accepted','running','waiting_approval','paused','pausing','cancel_requested','committing')`,
			conversationID, accepted.AgentTurnID).Scan(&occupied); err != nil {
			return err
		}
		if occupied != 0 {
			return domainError("AGENT_TURN_NOT_RUNNABLE", "当前对话有未结束回合，不能直接领取新回合。")
		}
		value, ok, err := s.claimAcceptedAgentTurnTx(ctx, tx, accepted.AgentTurnID, s.now())
		if err != nil {
			return err
		}
		if !ok {
			return domainError("AGENT_TURN_NOT_RUNNABLE", "新回合的执行权未确认。")
		}
		*accepted = value
		claimed = true
		return nil
	})
	if err != nil {
		claimed = false
	}
	return
}

func (s *Store) acceptAgentTurn(
	ctx context.Context, conversationID string, request agentcontract.MessageRequest, meta CommandMeta,
	beforeCommit func(*sql.Tx, *AgentTurn) error,
) (AgentTurn, error) {
	if err := validateMessageEnvelope(&request); err != nil {
		return AgentTurn{}, err
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTurn{}, err
	}
	defer tx.Rollback()

	projectID, workspaceID, err := authorizeAgentTurnSubmissionTx(ctx, tx, conversationID)
	if err != nil {
		return AgentTurn{}, err
	}
	if meta.Scope != "" && meta.Scope != projectID {
		return AgentTurn{}, domainError("REQUEST_VALIDATION_FAILED", "消息提交范围与作品不一致。")
	}
	if meta.Scope == "" {
		meta.Scope = projectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, meta)
	if err != nil {
		return AgentTurn{}, err
	}
	if hit {
		_, turn, err := readAcceptedAgentTurnTx(ctx, tx, cached, projectID, workspaceID, conversationID, meta.IdempotencyKey)
		return turn, err
	}
	if err := s.enforceWorkspaceQuotaTx(ctx, tx, workspaceID, "active_agent_turns", 1); err != nil {
		return AgentTurn{}, err
	}
	if err := s.validateMessageRequestTx(ctx, tx, projectID, request); err != nil {
		return AgentTurn{}, err
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return AgentTurn{}, err
	}
	turn := AgentTurn{
		AgentTurnID: s.newID("turn"), WorkspaceID: workspaceID,
		UserID:    identity.UserIDFromContext(ctx),
		ProjectID: projectID, ConversationID: conversationID,
		Status: "accepted", Request: request, IdempotencyKey: meta.IdempotencyKey,
		SubmissionID: agentTurnSubmissionID(identity.UserIDFromContext(ctx), conversationID, meta.IdempotencyKey),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_turns(
			agent_turn_id, workspace_id, user_id, project_id, conversation_id,
			idempotency_key, request_hash, request_json, status,
			created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'accepted', ?, ?)`,
		turn.AgentTurnID, workspaceID, turn.UserID, projectID, conversationID,
		meta.IdempotencyKey, meta.RequestHash, string(requestJSON),
		formatTime(now), formatTime(now),
	); err != nil {
		return AgentTurn{}, err
	}
	if err := s.freezeAgentTurnSkillsTx(ctx, tx, turn); err != nil {
		return AgentTurn{}, err
	}
	acceptedReceipt := turn
	if beforeCommit != nil {
		if err := beforeCommit(tx, &turn); err != nil {
			return AgentTurn{}, err
		}
	}
	if err := completeIdempotency(ctx, tx, meta, acceptedReceipt, now); err != nil {
		return AgentTurn{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTurn{}, err
	}
	return turn, nil
}

func (s *Store) GetAgentTurn(ctx context.Context, agentTurnID string) (AgentTurn, error) {
	turn, err := scanAgentTurn(s.db.QueryRowContext(
		ctx, agentTurnSelect+" WHERE agent_turn_id = ?", agentTurnID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTurn{}, domainError("AGENT_TURN_NOT_FOUND", "Agent Turn 不存在。")
	}
	if err == nil {
		turn.AdditionalInputs, err = loadAgentTurnInputs(ctx, s.db, turn.AgentTurnID)
	}
	return turn, err
}

func (s *Store) ListProjectAgentTurns(
	ctx context.Context, projectID string, limit int,
) ([]AgentTurn, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	// Keep every controllable turn reachable even when newer history fills the page.
	rows, err := s.db.QueryContext(ctx, agentTurnSelect+`
		WHERE project_id = ? AND (
			status NOT IN ('committed','failed','cancelled') OR agent_turn_id IN (
				SELECT agent_turn_id FROM agent_turns WHERE project_id = ?
				ORDER BY created_at DESC, agent_turn_id DESC LIMIT ?
			)
		)
		ORDER BY created_at DESC, agent_turn_id DESC`, projectID, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AgentTurn, 0)
	for rows.Next() {
		turn, err := scanAgentTurn(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, turn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range items {
		items[index].AdditionalInputs, err = loadAgentTurnInputs(ctx, s.db, items[index].AgentTurnID)
		if err != nil {
			return nil, err
		}
	}
	slices.Reverse(items)
	return items, nil
}

// ClaimRunnableAgentTurns atomically starts only the oldest accepted write in
// each conversation. Different conversations may execute in parallel.
func (s *Store) ClaimRunnableAgentTurns(ctx context.Context, limit int, excludedIDs ...string) ([]AgentTurn, error) {
	if limit <= 0 {
		return []AgentTurn{}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	excluded, args := "", make([]any, 0, len(excludedIDs)+1)
	if len(excludedIDs) > 0 {
		excluded = " AND candidate.agent_turn_id NOT IN (" + strings.TrimSuffix(strings.Repeat("?,", len(excludedIDs)), ",") + ")"
		for _, id := range excludedIDs {
			args = append(args, id)
		}
	}
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, `
		SELECT candidate.agent_turn_id
		FROM agent_turns candidate
		WHERE candidate.status = 'accepted'`+excluded+`
		  AND NOT EXISTS (
			SELECT 1 FROM agent_turns active
			WHERE active.conversation_id = candidate.conversation_id
			  AND active.status IN ('running','waiting_approval','pausing','cancel_requested','committing')
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM agent_turns older
			WHERE older.conversation_id = candidate.conversation_id
			  AND older.status IN ('accepted','paused')
			  AND (older.created_at < candidate.created_at OR
			      (older.created_at = candidate.created_at AND older.agent_turn_id < candidate.agent_turn_id))
		  )
		ORDER BY candidate.created_at, candidate.agent_turn_id
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	now := s.now()
	claimed := make([]AgentTurn, 0, len(ids))
	for _, id := range ids {
		turn, ok, err := s.claimAcceptedAgentTurnTx(ctx, tx, id, now)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		claimed = append(claimed, turn)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

func (s *Store) claimAcceptedAgentTurnTx(ctx context.Context, tx *sql.Tx, id string, now time.Time) (AgentTurn, bool, error) {
	result, err := tx.ExecContext(ctx, `UPDATE agent_turns
		SET status = 'running', started_at = COALESCE(started_at, ?), updated_at = ?,
		    dispatch_generation = dispatch_generation + 1, input_pause_requested = 0
		WHERE agent_turn_id = ? AND status = 'accepted'`, formatTime(now), formatTime(now), id)
	if err != nil {
		return AgentTurn{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return AgentTurn{}, false, err
	}
	turn, err := scanAgentTurn(tx.QueryRowContext(ctx, agentTurnSelect+" WHERE agent_turn_id = ?", id))
	if err != nil {
		return AgentTurn{}, false, err
	}
	if err := bindAgentTurnInputsTx(ctx, tx, &turn); err != nil {
		return AgentTurn{}, false, err
	}
	if _, err := s.appendAgentTurnEventTx(ctx, tx, turn, "agent.turn.started", false,
		map[string]any{"status": "running"}); err != nil {
		return AgentTurn{}, false, err
	}
	return turn, true, nil
}

func (s *Store) AppendAgentTurnProgress(
	ctx context.Context, agentTurnID, eventType string, payload map[string]any,
) error {
	if _, ok := agentTurnProgressEvents[eventType]; !ok {
		return domainError("AGENT_EVENT_TYPE_INVALID", "Agent 事件类型不受支持。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	turn, err := scanAgentTurn(tx.QueryRowContext(
		ctx, agentTurnSelect+" WHERE agent_turn_id = ?", agentTurnID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return domainError("AGENT_TURN_NOT_FOUND", "Agent Turn 不存在。")
	}
	if err != nil {
		return err
	}
	if agentTurnTerminal(turn.Status) {
		return nil
	}
	if _, err := s.appendAgentTurnEventTx(ctx, tx, turn, eventType, false, payload); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordAgentTurnObservation persists privacy-filtered SDK telemetry. It is
// intentionally valid after a turn reaches a terminal state because the
// Sidecar emits its final SDK usage after commit_agent_action returns.
func (s *Store) RecordAgentTurnObservation(
	ctx context.Context, agentTurnID string, observation AgentTurnObservation,
) (AgentTurn, error) {
	observation, err := normalizeAgentTurnObservation(observation)
	if err != nil {
		return AgentTurn{}, err
	}
	traceRefsJSON, _ := json.Marshal(observation.TraceRefs)
	responseIDsJSON, _ := json.Marshal(observation.ResponseIDs)
	requestIDsJSON, _ := json.Marshal(observation.RequestIDs)
	usageJSON, _ := json.Marshal(observation.Usage)
	latencyJSON, _ := json.Marshal(observation.LatencyMS)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTurn{}, err
	}
	defer tx.Rollback()
	turn, err := scanAgentTurn(tx.QueryRowContext(
		ctx, agentTurnSelect+" WHERE agent_turn_id = ?", agentTurnID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTurn{}, domainError("AGENT_TURN_NOT_FOUND", "Agent Turn 不存在。")
	}
	if err != nil {
		return AgentTurn{}, err
	}
	toolCallIDs, err := agentTurnToolCallIDsTx(ctx, tx, agentTurnID)
	if err != nil {
		return AgentTurn{}, err
	}
	toolCallIDsJSON, _ := json.Marshal(toolCallIDs)
	now := s.now()
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_turns
		SET provider_id = ?, model_id = ?, release_id = ?, trace_refs_json = ?,
		    response_ids_json = ?, request_ids_json = ?, usage_json = ?,
		    latency_json = ?, failure_stage = ?, cancel_reason = ?,
		    agent_tool_call_ids_json = ?, updated_at = ?
		WHERE agent_turn_id = ?`,
		nullableStringPointer(observation.ProviderID), nullableStringPointer(observation.ModelID),
		nullableStringPointer(observation.ReleaseID), string(traceRefsJSON),
		string(responseIDsJSON), string(requestIDsJSON), string(usageJSON),
		string(latencyJSON), nullableStringPointer(observation.FailureStage),
		nullableStringPointer(observation.CancelReason), string(toolCallIDsJSON),
		formatTime(now), agentTurnID,
	); err != nil {
		return AgentTurn{}, err
	}
	turn.Observation = observation
	turn.Correlation.AgentToolCallIDs = toolCallIDs
	turn.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return AgentTurn{}, err
	}
	return turn, nil
}

func (s *Store) BeginAgentTurnCommit(ctx context.Context, agentTurnID string) (AgentTurn, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTurn{}, err
	}
	defer tx.Rollback()
	turn, err := scanAgentTurn(tx.QueryRowContext(
		ctx, agentTurnSelect+" WHERE agent_turn_id = ?", agentTurnID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTurn{}, domainError("AGENT_TURN_NOT_FOUND", "Agent Turn 不存在。")
	}
	if err != nil {
		return AgentTurn{}, err
	}
	switch turn.Status {
	case "committed":
		return turn, nil
	case "cancel_requested", "cancelled":
		return AgentTurn{}, domainError("AGENT_TURN_CANCELLED", "本轮已取消，不能提交结果。")
	case "failed":
		return AgentTurn{}, domainError("AGENT_TURN_TERMINAL", "本轮已经失败，不能提交结果。")
	case "committing":
	case "accepted", "running", "pausing":
	default:
		return AgentTurn{}, domainError("AGENT_TURN_STATE_INVALID", "Agent Turn 状态无效。")
	}
	var pendingApproval bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_tool_calls WHERE agent_turn_id = ? AND status = 'pending_approval')`, agentTurnID).Scan(&pendingApproval); err != nil {
		return AgentTurn{}, err
	}
	if pendingApproval {
		return AgentTurn{}, domainError("AGENT_TOOL_APPROVAL_REQUIRED", "本轮仍有工具等待授权，不能提交普通终态。")
	}
	if err := validateAgentMemoryCommitTx(ctx, tx, "conversation", agentTurnID); err != nil {
		return AgentTurn{}, err
	}
	if err := validateExternalToolOutcomesTx(ctx, tx, "conversation", agentTurnID); err != nil {
		return AgentTurn{}, err
	}
	if turn.Status == "committing" {
		return turn, nil
	}
	now := s.now()
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_turns SET status = 'committing', updated_at = ?
		WHERE agent_turn_id = ? AND status IN ('accepted','running','pausing')`,
		formatTime(now), agentTurnID); err != nil {
		return AgentTurn{}, err
	}
	turn.Status = "committing"
	turn.UpdatedAt = now
	if _, err := s.appendAgentTurnEventTx(
		ctx, tx, turn, "agent.updated", false,
		map[string]any{"status": "committing", "label": "正在保存结果"},
	); err != nil {
		return AgentTurn{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTurn{}, err
	}
	return turn, nil
}

func (s *Store) CompleteAgentTurnCommit(
	ctx context.Context, agentTurnID string, exchange MessageExchange,
) (AgentTurn, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTurn{}, err
	}
	defer tx.Rollback()
	turn, err := scanAgentTurn(tx.QueryRowContext(
		ctx, agentTurnSelect+" WHERE agent_turn_id = ?", agentTurnID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTurn{}, domainError("AGENT_TURN_NOT_FOUND", "Agent Turn 不存在。")
	}
	if err != nil {
		return AgentTurn{}, err
	}
	if turn.Status == "committed" {
		return turn, nil
	}
	if turn.Status != "committing" && turn.Status != "running" && turn.Status != "pausing" {
		return AgentTurn{}, domainError("AGENT_TURN_TERMINAL", "本轮不能再提交结果。")
	}
	if err := validateAgentMemoryCommitTx(ctx, tx, "conversation", agentTurnID); err != nil {
		return AgentTurn{}, err
	}
	if err := validateExternalToolOutcomesTx(ctx, tx, "conversation", agentTurnID); err != nil {
		return AgentTurn{}, err
	}
	exchangeJSON, err := json.Marshal(exchange)
	if err != nil {
		return AgentTurn{}, err
	}
	correlation, err := agentTurnCorrelationTx(ctx, tx, agentTurnID, exchange)
	if err != nil {
		return AgentTurn{}, err
	}
	toolCallIDsJSON, _ := json.Marshal(correlation.AgentToolCallIDs)
	now := s.now()
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_turns
		SET status = 'committed', exchange_json = ?, user_message_id = ?,
		    agent_message_id = ?, completed_at = ?, updated_at = ?,
		    error_code = NULL, error_message = NULL,
		    skill_invocation_id = ?, agent_task_id = ?, run_id = ?,
		    agent_tool_call_ids_json = ?
		WHERE agent_turn_id = ? AND status IN ('committing','running','pausing')`,
		string(exchangeJSON), exchange.UserMessage.MessageID,
		exchange.AgentMessage.MessageID, formatTime(now), formatTime(now),
		nullableString(correlation.SkillInvocationID), nullableString(correlation.AgentTaskID),
		nullableString(correlation.RunID), string(toolCallIDsJSON), agentTurnID,
	); err != nil {
		return AgentTurn{}, err
	}
	turn.Status = "committed"
	turn.UserMessageID = &exchange.UserMessage.MessageID
	turn.AgentMessageID = &exchange.AgentMessage.MessageID
	turn.CompletedAt = &now
	turn.UpdatedAt = now
	turn.Correlation = correlation
	for index, chunk := range chunkAgentOutput(exchange.AgentMessage.Content, 240) {
		if _, err := s.appendAgentTurnEventTx(
			ctx, tx, turn, "agent.output.delta", false,
			map[string]any{"text": chunk, "index": index},
		); err != nil {
			return AgentTurn{}, err
		}
	}
	artifacts := slices.Clone(exchange.GenericArtifacts)
	if len(artifacts) == 0 && exchange.GenericArtifact != nil {
		artifacts = append(artifacts, *exchange.GenericArtifact)
	}
	for _, artifact := range artifacts {
		if _, err := s.appendAgentTurnEventTx(
			ctx, tx, turn, "agent.artifact.created", false,
			map[string]any{
				"artifact_id":   artifact.ArtifactID,
				"artifact_type": artifact.ArtifactType,
				"title":         artifact.Title,
			},
		); err != nil {
			return AgentTurn{}, err
		}
	}
	if exchange.Action != nil && exchange.Action.RequiresConfirmation {
		if _, err := s.appendAgentTurnEventTx(
			ctx, tx, turn, "agent.approval.requested", false,
			map[string]any{
				"resource_type": "proposed_action",
				"resource_id":   exchange.Action.ProposedActionID,
			},
		); err != nil {
			return AgentTurn{}, err
		}
	}
	terminal, err := s.appendAgentTurnEventTx(
		ctx, tx, turn, "agent.turn.committed", true,
		map[string]any{
			"status":           "committed",
			"user_message_id":  exchange.UserMessage.MessageID,
			"agent_message_id": exchange.AgentMessage.MessageID,
		},
	)
	if err != nil {
		return AgentTurn{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_turns SET terminal_event_id = ?
		WHERE agent_turn_id = ? AND terminal_event_id IS NULL`,
		terminal.EventID, agentTurnID); err != nil {
		return AgentTurn{}, err
	}
	if err := deleteAgentTurnRunStateTx(ctx, tx, agentTurnID); err != nil {
		return AgentTurn{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTurn{}, err
	}
	return turn, nil
}

func (s *Store) CancelAgentTurn(ctx context.Context, agentTurnID string) (AgentTurnCancelResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTurnCancelResult{}, err
	}
	defer tx.Rollback()
	turn, err := scanAgentTurn(tx.QueryRowContext(
		ctx, agentTurnSelect+" WHERE agent_turn_id = ?", agentTurnID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTurnCancelResult{}, domainError("AGENT_TURN_NOT_FOUND", "Agent Turn 不存在。")
	}
	if err != nil {
		return AgentTurnCancelResult{}, err
	}
	if err := authorizeAgentTurnCommandTx(ctx, tx, turn, "", false); err != nil {
		return AgentTurnCancelResult{}, err
	}
	accepted := false
	now := s.now()
	switch turn.Status {
	case "accepted", "waiting_approval", "paused":
		if _, err = tx.ExecContext(ctx, `UPDATE agent_turns
			SET cancel_reason = 'user_request', updated_at = ?
			WHERE agent_turn_id = ?`, formatTime(now), agentTurnID); err != nil {
			break
		}
		turn.Observation.CancelReason = "user_request"
		turn, err = s.finishAgentTurnTx(
			ctx, tx, turn, "cancelled", "USER_CANCELLED", "用户已取消本轮。", now,
		)
		accepted = err == nil
	case "running", "pausing":
		if _, err = tx.ExecContext(ctx, `
			UPDATE agent_turns SET status = 'cancel_requested', cancel_reason = 'user_request', updated_at = ?
			WHERE agent_turn_id = ? AND status IN ('running','pausing')`,
			formatTime(now), agentTurnID); err == nil {
			turn.Status = "cancel_requested"
			turn.Observation.CancelReason = "user_request"
			turn.UpdatedAt = now
			_, err = s.appendAgentTurnEventTx(
				ctx, tx, turn, "agent.updated", false,
				map[string]any{"status": "cancel_requested", "label": "正在停止"},
			)
		}
		accepted = err == nil
	case "cancel_requested":
		accepted = true
	case "committing", "committed", "failed", "cancelled":
		accepted = false
	}
	if err != nil {
		return AgentTurnCancelResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTurnCancelResult{}, err
	}
	return AgentTurnCancelResult{Turn: turn, Accepted: accepted}, nil
}

func (s *Store) FinishAgentTurnCancelled(
	ctx context.Context, agentTurnID, reason string,
) (AgentTurn, error) {
	if strings.TrimSpace(reason) == "" {
		reason = "本轮已取消。"
	}
	return s.finishAgentTurn(ctx, agentTurnID, "cancelled", "AGENT_TURN_CANCELLED", reason)
}

func (s *Store) FailAgentTurn(
	ctx context.Context, agentTurnID, code, message string,
) (AgentTurn, error) {
	if strings.TrimSpace(code) == "" {
		code = "AGENT_EXECUTION_FAILED"
	}
	if strings.TrimSpace(message) == "" {
		message = "Agent 暂时无法完成本次请求，请重试。"
	}
	return s.finishAgentTurn(ctx, agentTurnID, "failed", code, message)
}

func (s *Store) finishAgentTurn(
	ctx context.Context, agentTurnID, status, code, message string,
) (AgentTurn, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTurn{}, err
	}
	defer tx.Rollback()
	turn, err := scanAgentTurn(tx.QueryRowContext(
		ctx, agentTurnSelect+" WHERE agent_turn_id = ?", agentTurnID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTurn{}, domainError("AGENT_TURN_NOT_FOUND", "Agent Turn 不存在。")
	}
	if err != nil {
		return AgentTurn{}, err
	}
	if agentTurnTerminal(turn.Status) {
		return turn, nil
	}
	turn, err = s.finishAgentTurnTx(ctx, tx, turn, status, code, message, s.now())
	if err != nil {
		return AgentTurn{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTurn{}, err
	}
	return turn, nil
}

func (s *Store) finishAgentTurnTx(
	ctx context.Context,
	tx *sql.Tx,
	turn AgentTurn,
	status, code, message string,
	now time.Time,
) (AgentTurn, error) {
	if status != "failed" && status != "cancelled" {
		return AgentTurn{}, domainError("AGENT_TURN_STATE_INVALID", "Agent Turn 终态无效。")
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_turns
		SET status = ?, error_code = ?, error_message = ?,
		    completed_at = ?, updated_at = ?
		WHERE agent_turn_id = ? AND status NOT IN ('committed','failed','cancelled')`,
		status, code, message, formatTime(now), formatTime(now), turn.AgentTurnID)
	if err != nil {
		return AgentTurn{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return AgentTurn{}, err
	}
	if affected != 1 {
		return scanAgentTurn(tx.QueryRowContext(
			ctx, agentTurnSelect+" WHERE agent_turn_id = ?", turn.AgentTurnID,
		))
	}
	turn.Status = status
	turn.ErrorCode = &code
	turn.ErrorMessage = &message
	turn.CompletedAt = &now
	turn.UpdatedAt = now
	if err := s.cancelAgentTurnToolCallsTx(ctx, tx, turn, code, message, now); err != nil {
		return AgentTurn{}, err
	}
	eventType := "agent.turn.failed"
	if status == "cancelled" {
		eventType = "agent.turn.cancelled"
	}
	terminal, err := s.appendAgentTurnEventTx(
		ctx, tx, turn, eventType, true,
		map[string]any{"status": status, "code": code, "message": message},
	)
	if err != nil {
		return AgentTurn{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_turns SET terminal_event_id = ?
		WHERE agent_turn_id = ? AND terminal_event_id IS NULL`,
		terminal.EventID, turn.AgentTurnID); err != nil {
		return AgentTurn{}, err
	}
	if err := deleteAgentTurnRunStateTx(ctx, tx, turn.AgentTurnID); err != nil {
		return AgentTurn{}, err
	}
	return turn, nil
}

func (s *Store) cancelAgentTurnToolCallsTx(
	ctx context.Context,
	tx *sql.Tx,
	turn AgentTurn,
	reasonCode, reason string,
	now time.Time,
) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT agent_tool_call_id, tool_id, approval_status
		FROM agent_tool_calls
		WHERE agent_turn_id = ? AND status IN ('pending_approval','approved','running')
		ORDER BY requested_at, agent_tool_call_id`, turn.AgentTurnID)
	if err != nil {
		return err
	}
	type activeToolCall struct {
		id, toolID, approvalStatus string
	}
	calls := make([]activeToolCall, 0)
	for rows.Next() {
		var call activeToolCall
		if err := rows.Scan(&call.id, &call.toolID, &call.approvalStatus); err != nil {
			rows.Close()
			return err
		}
		calls = append(calls, call)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, call := range calls {
		if call.approvalStatus == "pending" {
			resolution, _ := json.Marshal(map[string]any{"action": "cancel", "reason_code": reasonCode})
			if _, err := tx.ExecContext(ctx, `
				UPDATE agent_tool_approvals
				SET status = 'cancelled', version = version + 1, resolved_at = ?, resolution_json = ?
				WHERE agent_tool_call_id = ? AND status = 'pending'`,
				formatTime(now), string(resolution), call.id,
			); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE agent_tool_calls
			SET status = 'cancelled',
				approval_status = CASE WHEN approval_status = 'pending' THEN 'cancelled' ELSE approval_status END,
				error_code = ?, error_message = ?, completed_at = ?, updated_at = ?
			WHERE agent_tool_call_id = ? AND status IN ('pending_approval','approved','running')`,
			reasonCode, reason, formatTime(now), formatTime(now), call.id,
		)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			continue
		}
		if _, err := s.appendEvent(ctx, tx, turn.ProjectID, nil, nil,
			"agent.tool_call.cancelled", "agent_tool_call", call.id,
			map[string]any{
				"tool_id": call.toolID, "agent_turn_id": turn.AgentTurnID,
				"reason_code": reasonCode,
			}); err != nil {
			return err
		}
	}
	return nil
}

// RecoverInterruptedAgentTurns closes writes that were already running when
// the Go runtime stopped. Accepted turns remain queued and can be claimed by
// the new process.
func (s *Store) RecoverInterruptedAgentTurns(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, agentTurnSelect+`
		WHERE status IN ('running','pausing','cancel_requested','committing')
		ORDER BY created_at, agent_turn_id`)
	if err != nil {
		return err
	}
	turns := make([]AgentTurn, 0)
	for rows.Next() {
		turn, err := scanAgentTurn(rows)
		if err != nil {
			rows.Close()
			return err
		}
		turns = append(turns, turn)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := s.now()
	for _, turn := range turns {
		status, code, message := "failed", "RUNTIME_RESTART_INTERRUPTED", "服务重启中断了本轮，请重试。"
		if turn.Status == "cancel_requested" {
			status, code, message = "cancelled", "AGENT_TURN_CANCELLED", "本轮已在服务重启时完成取消。"
		}
		if _, err := s.finishAgentTurnTx(ctx, tx, turn, status, code, message, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) appendAgentTurnEventTx(
	ctx context.Context,
	tx *sql.Tx,
	turn AgentTurn,
	eventType string,
	terminal bool,
	payload map[string]any,
) (Event, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	return s.appendEvent(
		ctx, tx, turn.ProjectID, nil, nil, eventType, "agent_turn", turn.AgentTurnID,
		map[string]any{"agent_event": map[string]any{
			"schema_version":  "1.0.0",
			"conversation_id": turn.ConversationID,
			"turn_id":         turn.AgentTurnID,
			"terminal":        terminal,
			"payload":         payload,
		}},
	)
}

func scanAgentTurn(row rowScanner) (AgentTurn, error) {
	var turn AgentTurn
	var requestJSON, createdAt, updatedAt string
	var traceRefsJSON, responseIDsJSON, requestIDsJSON, usageJSON, latencyJSON string
	var toolCallIDsJSON string
	var errorCode, errorMessage, userMessageID, agentMessageID sql.NullString
	var providerID, modelID, releaseID, failureStage, cancelReason sql.NullString
	var skillInvocationID, agentTaskID, runID sql.NullString
	var startedAt, completedAt sql.NullString
	if err := row.Scan(
		&turn.AgentTurnID, &turn.WorkspaceID, &turn.UserID, &turn.ProjectID, &turn.ConversationID,
		&turn.IdempotencyKey, &requestJSON, &turn.Status, &errorCode, &errorMessage,
		&userMessageID, &agentMessageID, &createdAt, &startedAt, &completedAt, &updatedAt,
		&providerID, &modelID, &releaseID, &traceRefsJSON, &responseIDsJSON,
		&requestIDsJSON, &usageJSON, &latencyJSON, &failureStage, &cancelReason,
		&skillInvocationID, &agentTaskID, &runID, &toolCallIDsJSON, &turn.DispatchGeneration, &turn.InputPauseRequested,
	); err != nil {
		return AgentTurn{}, err
	}
	if err := json.Unmarshal([]byte(requestJSON), &turn.Request); err != nil {
		return AgentTurn{}, err
	}
	var err error
	turn.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return AgentTurn{}, err
	}
	turn.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return AgentTurn{}, err
	}
	turn.ErrorCode = stringPointer(errorCode)
	turn.SubmissionID = agentTurnSubmissionID(turn.UserID, turn.ConversationID, turn.IdempotencyKey)
	turn.ErrorMessage = stringPointer(errorMessage)
	turn.UserMessageID = stringPointer(userMessageID)
	turn.AgentMessageID = stringPointer(agentMessageID)
	turn.Observation = AgentTurnObservation{
		SchemaVersion: agentTurnObservationSchema,
		ProviderID:    providerID.String, ModelID: modelID.String, ReleaseID: releaseID.String,
		FailureStage: failureStage.String, CancelReason: cancelReason.String,
		TraceRefs: []string{}, ResponseIDs: []string{}, RequestIDs: []string{},
		Usage: map[string]int64{}, LatencyMS: map[string]int64{},
	}
	for _, item := range []struct {
		raw    string
		target any
	}{
		{traceRefsJSON, &turn.Observation.TraceRefs},
		{responseIDsJSON, &turn.Observation.ResponseIDs},
		{requestIDsJSON, &turn.Observation.RequestIDs},
		{usageJSON, &turn.Observation.Usage},
		{latencyJSON, &turn.Observation.LatencyMS},
		{toolCallIDsJSON, &turn.Correlation.AgentToolCallIDs},
	} {
		if err := json.Unmarshal([]byte(item.raw), item.target); err != nil {
			return AgentTurn{}, err
		}
	}
	turn.Correlation.SkillInvocationID = stringPointer(skillInvocationID)
	turn.Correlation.AgentTaskID = stringPointer(agentTaskID)
	turn.Correlation.RunID = stringPointer(runID)
	if startedAt.Valid {
		value, err := parseTime(startedAt.String)
		if err != nil {
			return AgentTurn{}, err
		}
		turn.StartedAt = &value
	}
	if completedAt.Valid {
		value, err := parseTime(completedAt.String)
		if err != nil {
			return AgentTurn{}, err
		}
		turn.CompletedAt = &value
	}
	return turn, nil
}

func normalizeAgentTurnObservation(observation AgentTurnObservation) (AgentTurnObservation, error) {
	if observation.SchemaVersion != agentTurnObservationSchema {
		return AgentTurnObservation{}, domainError(
			"AGENT_OBSERVATION_INVALID", "Agent 观测数据版本不受支持。",
		)
	}
	for _, field := range []struct {
		value string
		limit int
	}{
		{observation.ProviderID, 64},
		{observation.ModelID, 128},
		{observation.ReleaseID, 64},
		{observation.FailureStage, 64},
		{observation.CancelReason, 64},
	} {
		if field.value != "" && (len(field.value) > field.limit || !agentTurnObservationIdentifier.MatchString(field.value)) {
			return AgentTurnObservation{}, domainError(
				"AGENT_OBSERVATION_INVALID", "Agent 观测标识不合法。",
			)
		}
	}
	var err error
	if observation.TraceRefs, err = normalizeAgentObservationIDs(observation.TraceRefs, 32); err != nil {
		return AgentTurnObservation{}, err
	}
	if observation.ResponseIDs, err = normalizeAgentObservationIDs(observation.ResponseIDs, 64); err != nil {
		return AgentTurnObservation{}, err
	}
	if observation.RequestIDs, err = normalizeAgentObservationIDs(observation.RequestIDs, 64); err != nil {
		return AgentTurnObservation{}, err
	}
	if observation.Usage, err = normalizeAgentObservationCounters(observation.Usage); err != nil {
		return AgentTurnObservation{}, err
	}
	if observation.LatencyMS, err = normalizeAgentObservationCounters(observation.LatencyMS); err != nil {
		return AgentTurnObservation{}, err
	}
	return observation, nil
}

func normalizeAgentObservationIDs(values []string, limit int) ([]string, error) {
	if len(values) > limit {
		return nil, domainError("AGENT_OBSERVATION_INVALID", "Agent 观测标识数量超限。")
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 160 || !agentTurnObservationIdentifier.MatchString(value) {
			return nil, domainError("AGENT_OBSERVATION_INVALID", "Agent 观测标识不合法。")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func normalizeAgentObservationCounters(values map[string]int64) (map[string]int64, error) {
	if len(values) > 32 {
		return nil, domainError("AGENT_OBSERVATION_INVALID", "Agent 观测计数项超限。")
	}
	result := make(map[string]int64, len(values))
	for key, value := range values {
		if key == "" || len(key) > 64 || !agentTurnObservationIdentifier.MatchString(key) || value < 0 {
			return nil, domainError("AGENT_OBSERVATION_INVALID", "Agent 观测计数不合法。")
		}
		result[key] = value
	}
	return result, nil
}

func agentTurnCorrelationTx(
	ctx context.Context, tx *sql.Tx, agentTurnID string, exchange MessageExchange,
) (AgentTurnCorrelation, error) {
	correlation := AgentTurnCorrelation{AgentToolCallIDs: []string{}}
	if exchange.Invocation != nil {
		correlation.SkillInvocationID = &exchange.Invocation.SkillInvocationID
		correlation.AgentTaskID = exchange.Invocation.AgentTaskID
	}
	if exchange.TaskRef != nil {
		correlation.AgentTaskID = &exchange.TaskRef.AgentTaskID
		if correlation.SkillInvocationID == nil && exchange.TaskRef.SkillInvocationID != "" {
			correlation.SkillInvocationID = &exchange.TaskRef.SkillInvocationID
		}
	}
	if exchange.RunRef != nil {
		correlation.RunID = &exchange.RunRef.RunID
	} else if exchange.Action != nil {
		correlation.RunID = exchange.Action.ConsumedRunID
	}
	toolCallIDs, err := agentTurnToolCallIDsTx(ctx, tx, agentTurnID)
	if err != nil {
		return AgentTurnCorrelation{}, err
	}
	correlation.AgentToolCallIDs = toolCallIDs
	return correlation, nil
}

func agentTurnToolCallIDsTx(ctx context.Context, tx *sql.Tx, agentTurnID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT agent_tool_call_id FROM agent_tool_calls
		WHERE agent_turn_id = ? ORDER BY requested_at, agent_tool_call_id`, agentTurnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func agentTurnTerminal(status string) bool {
	return status == "committed" || status == "failed" || status == "cancelled"
}

func chunkAgentOutput(value string, limit int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return []string{}
	}
	if limit <= 0 {
		limit = 240
	}
	runes := []rune(value)
	chunks := make([]string, 0, (len(runes)+limit-1)/limit)
	for len(runes) > 0 {
		size := min(limit, len(runes))
		chunks = append(chunks, string(runes[:size]))
		runes = runes[size:]
	}
	return chunks
}
