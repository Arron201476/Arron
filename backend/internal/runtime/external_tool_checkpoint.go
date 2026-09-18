package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
)

const externalToolRecoveryReason = "external_tool_outcome_unknown"
const externalToolRecoveryCode = "SDK_TOOL_OUTCOME_UNRESOLVED"

type nativeOutcomeItem struct {
	Type       string `json:"type"`
	ToolName   string `json:"tool_name"`
	ToolOrigin struct {
		Type   string `json:"type"`
		Server string `json:"mcp_server_name"`
	} `json:"tool_origin"`
	Raw struct {
		Type      string          `json:"type"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Output    json.RawMessage `json:"output"`
	} `json:"raw_item"`
}

type nativeOutcomeMarker struct {
	Schema         string `json:"schema_version"`
	Status         string `json:"status"`
	CallID         string `json:"agent_tool_call_id"`
	SDKCallID      string `json:"sdk_tool_call_id"`
	ToolID         string `json:"tool_id"`
	ArgumentsHash  string `json:"arguments_hash"`
	RequiresReview bool   `json:"requires_user_reconciliation"`
}

func uncertainExternalCallsTx(ctx context.Context, tx *sql.Tx, mode, id string) ([]AgentToolCall, error) {
	selection, err := executionToolSelection(mode)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.agent_tool_call_id FROM agent_tool_calls c WHERE (`+selection+`) AND
		c.tool_kind='mcp' AND c.access_mode!='read' AND c.started_at IS NOT NULL AND c.status!='completed' ORDER BY c.agent_tool_call_id`, id)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var callID string
		if err := rows.Scan(&callID); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, callID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	calls := make([]AgentToolCall, 0, len(ids))
	for _, callID := range ids {
		call, err := getAgentToolCallTx(ctx, tx, callID)
		if err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	return calls, nil
}

// Read the pinned SDK's public serialized items. Never reconstruct a missing
// tool batch: an exception checkpoint without its issued calls is not resumable.
func validateExternalToolCheckpoint(raw json.RawMessage, calls []AgentToolCall) error {
	invalid := func() error {
		return domainError("SDK_TOOL_REPLAY_RISK", "原生检查点缺少完整的外部操作事实，不能重跑原操作。")
	}
	var state struct {
		CurrentTurn int `json:"current_turn"`
		MaxTurns    int `json:"max_turns"`
		CurrentStep struct {
			Type string `json:"type"`
		} `json:"current_step"`
		Items []nativeOutcomeItem `json:"generated_items"`
	}
	if len(calls) == 0 || len(raw) == 0 || len(raw) > maxAgentTurnRunState || json.Unmarshal(raw, &state) != nil ||
		state.CurrentTurn <= 0 || state.CurrentTurn >= state.MaxTurns || state.CurrentStep.Type != "next_step_run_again" {
		return invalid()
	}
	inputs, outputs := map[string]nativeOutcomeItem{}, map[string]nativeOutcomeItem{}
	for _, item := range state.Items {
		var target map[string]nativeOutcomeItem
		switch item.Type {
		case "tool_call_item":
			if item.Raw.Type != "function_call" {
				continue
			}
			target = inputs
		case "tool_call_output_item":
			if item.Raw.Type != "function_call_output" {
				continue
			}
			target = outputs
		default:
			continue
		}
		if item.Raw.CallID == "" {
			return invalid()
		}
		if _, duplicate := target[item.Raw.CallID]; duplicate {
			return invalid()
		}
		target[item.Raw.CallID] = item
	}
	for _, call := range calls {
		input, ok := inputs[call.SDKToolCallID]
		output, outputOK := outputs[call.SDKToolCallID]
		if !ok || !outputOK || call.Status != "failed" || call.ErrorCode == nil || *call.ErrorCode != "MCP_TOOL_OUTCOME_UNKNOWN" ||
			call.ServerID == nil || input.ToolOrigin.Type != "mcp" || input.ToolOrigin.Server != *call.ServerID ||
			output.ToolOrigin.Type != "mcp" || output.ToolOrigin.Server != *call.ServerID || input.Raw.Name == "" || input.Raw.Name != input.ToolName {
			return invalid()
		}
		_, hash, _, err := summarizeAgentToolPayload(json.RawMessage(input.Raw.Arguments), true)
		if err != nil || hash != call.ArgumentsHash {
			return invalid()
		}
		var content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(output.Raw.Output, &content) != nil || len(content) != 1 || content[0].Type != "input_text" {
			return invalid()
		}
		var marker nativeOutcomeMarker
		if json.Unmarshal([]byte(content[0].Text), &marker) != nil || marker.Schema != "agent_tool_outcome.v1" || marker.Status != "outcome_unknown" ||
			!marker.RequiresReview || marker.CallID != call.AgentToolCallID || marker.SDKCallID != call.SDKToolCallID || marker.ToolID != call.ToolID || marker.ArgumentsHash != call.ArgumentsHash {
			return invalid()
		}
	}
	return nil
}

func validateExternalToolCheckpointTx(ctx context.Context, tx *sql.Tx, mode, id string, raw json.RawMessage) error {
	selection, err := executionToolSelection(mode)
	if err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_tool_calls c WHERE (`+selection+`) AND
		(c.status IN ('pending_approval','approved','running') OR
		(c.tool_kind!='mcp' AND c.access_mode!='read' AND c.started_at IS NOT NULL AND c.status!='completed'
		AND NOT (c.tool_id='runtime:commit_agent_action' AND c.status='failed' AND COALESCE(c.error_code,'')='RUNTIME_COMMIT_DEFERRED'))))`, id).Scan(&active); err != nil {
		return err
	}
	if active {
		return domainError("SDK_TOOL_REPLAY_RISK", "仍有未结束工具或未确认的平台写入，不能保存外部操作恢复状态。")
	}
	calls, err := uncertainExternalCallsTx(ctx, tx, mode, id)
	if err != nil {
		return err
	}
	unconsumed := make([]AgentToolCall, 0, len(calls))
	for _, call := range calls {
		consumed, err := reviewedExternalInputTx(ctx, tx, call, mode, id, true)
		if err != nil {
			return err
		}
		if !consumed {
			unconsumed = append(unconsumed, call)
		}
	}
	return validateExternalToolCheckpoint(raw, unconsumed)
}
