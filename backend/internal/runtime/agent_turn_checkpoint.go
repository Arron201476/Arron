package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"content-agent/backend/internal/agentcontract"
)

const (
	agentTurnRunStateSchema = "openai_agents_run_state"
	maxAgentTurnRunState    = agentcontract.MaxSDKRunStateBytes
	maxAgentTurnApprovals   = 32
)

func (s *Store) PauseAgentTurnForApproval(
	ctx context.Context,
	agentTurnID string,
	command PauseAgentTurnForApprovalCommand,
) (AgentTurn, error) {
	return s.saveAgentTurnCheckpoint(ctx, agentTurnID, command, false)
}

func (s *Store) CompleteAgentTurnPause(ctx context.Context, agentTurnID string, command PauseAgentTurnForApprovalCommand) (AgentTurn, error) {
	return s.saveAgentTurnCheckpoint(ctx, agentTurnID, command, true)
}

func (s *Store) saveAgentTurnCheckpoint(ctx context.Context, agentTurnID string, command PauseAgentTurnForApprovalCommand, userPause bool) (AgentTurn, error) {
	recovery, recoveryErr := validateModelRecoveryCheckpoint(command)
	if recoveryErr != nil {
		return AgentTurn{}, recoveryErr
	}
	if recovery && !userPause {
		return AgentTurn{}, domainError("REQUEST_VALIDATION_FAILED", "模型恢复必须使用暂停通道。")
	}
	agentTurnID = strings.TrimSpace(agentTurnID)
	command.SchemaVersion = strings.TrimSpace(command.SchemaVersion)
	if agentTurnID == "" || command.SchemaVersion == "" || len(command.SchemaVersion) > 64 ||
		len(command.RunState) == 0 || len(command.RunState) > maxAgentTurnRunState ||
		(!userPause && len(command.PendingSDKToolCallIDs) == 0) || len(command.PendingSDKToolCallIDs) > maxAgentTurnApprovals {
		return AgentTurn{}, domainError("REQUEST_VALIDATION_FAILED", "Agent 审批 checkpoint 不完整。")
	}
	var stateEnvelope map[string]json.RawMessage
	if !json.Valid(command.RunState) || json.Unmarshal(command.RunState, &stateEnvelope) != nil || stateEnvelope == nil {
		return AgentTurn{}, domainError("REQUEST_VALIDATION_FAILED", "Agent SDK RunState 不是有效对象。")
	}
	var embeddedSchema string
	if json.Unmarshal(stateEnvelope["$schemaVersion"], &embeddedSchema) != nil || embeddedSchema != command.SchemaVersion {
		return AgentTurn{}, domainError("AGENT_RUN_STATE_SCHEMA_MISMATCH", "Agent SDK RunState 版本不匹配。")
	}
	pendingSDKIDs, err := normalizeSDKToolCallIDs(command.PendingSDKToolCallIDs)
	if err != nil {
		return AgentTurn{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTurn{}, err
	}
	defer tx.Rollback()
	turn, err := scanAgentTurn(tx.QueryRowContext(ctx, agentTurnSelect+" WHERE agent_turn_id = ?", agentTurnID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTurn{}, domainError("AGENT_TURN_NOT_FOUND", "Agent Turn 不存在。")
	}
	if err != nil {
		return AgentTurn{}, err
	}
	if command.AgentTurnDispatchGeneration != 0 && command.AgentTurnDispatchGeneration != turn.DispatchGeneration {
		return AgentTurn{}, domainError("AGENT_TURN_STATE_CONFLICT", "暂停状态不属于当前执行调度。")
	}
	if len(command.IncludedAgentTurnInputIDs) > 0 {
		if err := s.includeAgentTurnInputsTx(ctx, tx, turn, command.AgentTurnDispatchGeneration, command.IncludedAgentTurnInputIDs, s.now()); err != nil {
			return AgentTurn{}, err
		}
	}
	if userPause && turn.Status == "paused" {
		var hash, pending string
		if err := tx.QueryRowContext(ctx, `SELECT state_hash,pending_sdk_tool_call_ids_json FROM agent_turn_run_states WHERE agent_turn_id=?`, agentTurnID).Scan(&hash, &pending); err != nil {
			return AgentTurn{}, err
		}
		encoded, _ := json.Marshal(pendingSDKIDs)
		if hash != sha256Hex(command.RunState) || pending != string(encoded) {
			return AgentTurn{}, domainError("AGENT_TURN_STATE_CONFLICT", "已保存的暂停状态与重复回执不一致。")
		}
		return turn, nil
	}
	if (userPause && turn.Status != "pausing" && !(recovery && turn.Status == "running")) || (!userPause && turn.Status != "running" && turn.Status != "waiting_approval" && turn.Status != "pausing") {
		return AgentTurn{}, domainError("AGENT_TURN_STATE_INVALID", "当前 Agent Turn 不能保存审批 checkpoint。")
	}
	if recovery {
		if err := validateRecoveryToolsTx(ctx, tx, "conversation", turn.AgentTurnID, command); err != nil {
			return AgentTurn{}, err
		}
	}

	callIDs := make([]string, 0, len(pendingSDKIDs))
	unresolved := 0
	for _, sdkCallID := range pendingSDKIDs {
		var callID, callStatus, approvalStatus string
		err := tx.QueryRowContext(ctx, `
			SELECT c.agent_tool_call_id, c.status, a.status
			FROM agent_tool_calls c
			JOIN agent_tool_approvals a ON a.agent_tool_call_id = c.agent_tool_call_id
			WHERE c.agent_turn_id = ? AND c.sdk_tool_call_id = ?`,
			agentTurnID, sdkCallID,
		).Scan(&callID, &callStatus, &approvalStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return AgentTurn{}, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "SDK 审批项未绑定到当前 Agent Turn。")
		}
		if err != nil {
			return AgentTurn{}, err
		}
		validStatePair := (approvalStatus == "pending" && callStatus == "pending_approval") ||
			(approvalStatus == "approved" && callStatus == "approved") ||
			(approvalStatus == "rejected" && callStatus == "rejected")
		if !validStatePair {
			return AgentTurn{}, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "SDK 审批项状态与 Runtime 不一致。")
		}
		if approvalStatus == "pending" {
			unresolved++
		}
		callIDs = append(callIDs, callID)
	}
	var currentPending int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM agent_tool_calls c
		JOIN agent_tool_approvals a ON a.agent_tool_call_id = c.agent_tool_call_id
		WHERE c.agent_turn_id = ? AND c.status = 'pending_approval' AND a.status = 'pending'`,
		agentTurnID,
	).Scan(&currentPending); err != nil {
		return AgentTurn{}, err
	}
	if currentPending != unresolved {
		return AgentTurn{}, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "SDK 审批项与 Runtime 待审批集合不一致。")
	}

	stateHash := sha256Hex(command.RunState)
	pendingJSON, _ := json.Marshal(pendingSDKIDs)
	now := s.now()
	var checkpointVersion int
	err = tx.QueryRowContext(ctx, `SELECT checkpoint_version FROM agent_turn_run_states WHERE agent_turn_id = ?`, agentTurnID).Scan(&checkpointVersion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AgentTurn{}, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		checkpointVersion = 1
		_, err = tx.ExecContext(ctx, `
			INSERT INTO agent_turn_run_states(
				agent_turn_id, schema_version, state_json, state_hash,
				pending_sdk_tool_call_ids_json, checkpoint_version, created_at, updated_at
			) VALUES(?, ?, ?, ?, ?, 1, ?, ?)`,
			agentTurnID, agentTurnRunStateSchema+":"+command.SchemaVersion,
			string(command.RunState), stateHash, string(pendingJSON), formatTime(now), formatTime(now),
		)
	} else {
		checkpointVersion++
		_, err = tx.ExecContext(ctx, `
			UPDATE agent_turn_run_states
			SET schema_version = ?, state_json = ?, state_hash = ?,
				pending_sdk_tool_call_ids_json = ?, checkpoint_version = ?, updated_at = ?
			WHERE agent_turn_id = ?`,
			agentTurnRunStateSchema+":"+command.SchemaVersion, string(command.RunState), stateHash,
			string(pendingJSON), checkpointVersion, formatTime(now), agentTurnID,
		)
	}
	if err != nil {
		return AgentTurn{}, err
	}
	status := "waiting_approval"
	if unresolved == 0 {
		status = "accepted"
	}
	eventType := "agent.turn.waiting_approval"
	if userPause || turn.Status == "pausing" {
		status, eventType = "paused", "agent.turn.paused"
		if turn.InputPauseRequested && len(pendingSDKIDs) == 0 && !recovery {
			status, eventType = "accepted", "agent.turn.resume_requested"
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_turns SET status = ?, input_pause_requested = 0, updated_at = ?
		WHERE agent_turn_id = ? AND status IN ('running','waiting_approval','pausing')`,
		status, formatTime(now), agentTurnID,
	)
	if err != nil {
		return AgentTurn{}, err
	}
	if err := requireOneRow(result, "AGENT_TURN_STATE_CONFLICT", "Agent Turn 状态已经变化。"); err != nil {
		return AgentTurn{}, err
	}
	turn.Status = status
	turn.InputPauseRequested = false
	turn.UpdatedAt = now
	if recovery {
		code, message := recoveryDetails(command.RecoveryReason)
		if _, err := tx.ExecContext(ctx, `UPDATE agent_turns SET error_code=?,error_message=? WHERE agent_turn_id=?`, code, message, turn.AgentTurnID); err != nil {
			return AgentTurn{}, err
		}
		turn.ErrorCode, turn.ErrorMessage = &code, &message
	}
	if _, err := s.appendAgentTurnEventTx(ctx, tx, turn, eventType, false, map[string]any{
		"status":                 status,
		"pending_approval_count": unresolved,
		"agent_tool_call_ids":    callIDs,
		"checkpoint_version":     checkpointVersion,
		"sdk_run_state_schema":   command.SchemaVersion,
	}); err != nil {
		return AgentTurn{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTurn{}, err
	}
	return turn, nil
}

func (s *Store) GetAgentTurnResumeContext(
	ctx context.Context, agentTurnID string,
) (AgentTurnResumeContext, error) {
	result, _, err := loadAgentTurnCheckpoint(ctx, s.db, agentTurnID)
	return result, err
}

func loadAgentTurnCheckpoint(ctx context.Context, query rowQueryer, agentTurnID string) (AgentTurnResumeContext, int, error) {
	var stateJSON, stateHash, schema, pendingJSON string
	err := query.QueryRowContext(ctx, `
		SELECT state_json, state_hash, schema_version, pending_sdk_tool_call_ids_json
		FROM agent_turn_run_states WHERE agent_turn_id = ?`, strings.TrimSpace(agentTurnID),
	).Scan(&stateJSON, &stateHash, &schema, &pendingJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTurnResumeContext{ApprovalDecisions: []agentcontract.AgentToolApprovalDecision{}}, 0, nil
	}
	if err != nil {
		return AgentTurnResumeContext{}, 0, err
	}
	var envelope map[string]json.RawMessage
	var embeddedSchema string
	if len(stateJSON) == 0 || len(stateJSON) > maxAgentTurnRunState || sha256Hex([]byte(stateJSON)) != stateHash ||
		json.Unmarshal([]byte(stateJSON), &envelope) != nil || json.Unmarshal(envelope["$schemaVersion"], &embeddedSchema) != nil ||
		schema != agentTurnRunStateSchema+":"+embeddedSchema || embeddedSchema == "" {
		return AgentTurnResumeContext{}, 0, domainError("AGENT_RUN_STATE_CORRUPT", "Agent 暂停状态校验失败，不能从头重跑。")
	}
	var pendingIDs []string
	if err := json.Unmarshal([]byte(pendingJSON), &pendingIDs); err != nil || pendingIDs == nil || len(pendingIDs) > maxAgentTurnApprovals {
		return AgentTurnResumeContext{}, 0, domainError("AGENT_RUN_STATE_CORRUPT", "Agent 暂停审批集合无效。")
	}
	if _, err := normalizeSDKToolCallIDs(pendingIDs); err != nil {
		return AgentTurnResumeContext{}, 0, err
	}
	unresolved := 0
	decisions := make([]agentcontract.AgentToolApprovalDecision, 0, len(pendingIDs))
	for _, sdkCallID := range pendingIDs {
		var status, callStatus string
		err := query.QueryRowContext(ctx, `
			SELECT a.status, c.status
			FROM agent_tool_calls c
			JOIN agent_tool_approvals a ON a.agent_tool_call_id = c.agent_tool_call_id
			WHERE c.agent_turn_id = ? AND c.sdk_tool_call_id = ?`,
			agentTurnID, sdkCallID,
		).Scan(&status, &callStatus)
		if err != nil {
			return AgentTurnResumeContext{}, 0, err
		}
		if (status == "pending" && callStatus != "pending_approval") || (status != "pending" && status != "approved" && status != "rejected") || (status != "pending" && callStatus != status) {
			return AgentTurnResumeContext{}, 0, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "Agent 暂停审批已失效或与工具状态不一致。")
		}
		if status == "pending" {
			unresolved++
		}
		if status == "approved" || status == "rejected" {
			action := "approve"
			if status == "rejected" {
				action = "reject"
			}
			decisions = append(decisions, agentcontract.AgentToolApprovalDecision{
				SDKToolCallID: sdkCallID,
				Action:        action,
			})
		}
	}
	var currentPending int
	if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_tool_calls WHERE agent_turn_id=? AND status='pending_approval'`, agentTurnID).Scan(&currentPending); err != nil {
		return AgentTurnResumeContext{}, 0, err
	}
	if currentPending != unresolved {
		return AgentTurnResumeContext{}, 0, domainError("AGENT_RUN_STATE_APPROVAL_MISMATCH", "Agent 暂停审批集合与当前执行不一致。")
	}
	return AgentTurnResumeContext{
		RunState:          json.RawMessage(stateJSON),
		ApprovalDecisions: decisions,
	}, unresolved, nil
}

func normalizeSDKToolCallIDs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 256 {
			return nil, domainError("REQUEST_VALIDATION_FAILED", "SDK 工具调用标识无效。")
		}
		if _, exists := seen[value]; exists {
			return nil, domainError("REQUEST_VALIDATION_FAILED", "SDK 审批项不能重复。")
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func deleteAgentTurnRunStateTx(ctx context.Context, tx *sql.Tx, agentTurnID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM agent_turn_run_states WHERE agent_turn_id = ?`, agentTurnID)
	return err
}
