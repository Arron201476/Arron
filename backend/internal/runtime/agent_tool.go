package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/identity"
)

const (
	maxAgentToolArgumentsBytes = 256 * 1024
	maxAgentToolSummaryBytes   = 16 * 1024
)

func (s *Store) BeginAgentToolCall(
	ctx context.Context,
	command BeginAgentToolCallCommand,
) (AgentToolCall, error) {
	if err := validateProgramCallID(command.ProgramCallID); err != nil {
		return AgentToolCall{}, err
	}
	if err := validateMemoryToolBeginIdentity(ctx, command); err != nil {
		return AgentToolCall{}, err
	}
	command.ProjectID = strings.TrimSpace(command.ProjectID)
	command.ConversationID = strings.TrimSpace(command.ConversationID)
	command.SkillInvocationID = strings.TrimSpace(command.SkillInvocationID)
	command.AgentTurnID = strings.TrimSpace(command.AgentTurnID)
	command.SDKToolCallID = strings.TrimSpace(command.SDKToolCallID)
	command.ToolID = strings.TrimSpace(command.ToolID)
	if command.ProjectID == "" || command.ConversationID == "" ||
		command.SDKToolCallID == "" || command.ToolID == "" {
		return AgentToolCall{}, domainError("REQUEST_VALIDATION_FAILED", "工具调用登记信息不完整。")
	}
	if len(command.SDKToolCallID) > 256 || len(command.AgentTurnID) > 256 {
		return AgentToolCall{}, domainError("REQUEST_VALIDATION_FAILED", "工具调用标识过长。")
	}
	if s.agentTools == nil {
		return AgentToolCall{}, domainError("AGENT_TOOL_REGISTRY_UNAVAILABLE", "平台工具目录尚未配置。")
	}
	if len(command.Arguments) == 0 {
		command.Arguments = json.RawMessage(`{}`)
	}
	argumentSummary, argumentHash, _, err := summarizeAgentToolPayload(command.Arguments, true)
	if err != nil {
		return AgentToolCall{}, err
	}

	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentToolCall{}, err
	}
	defer tx.Rollback()
	workspaceID, err := validateAgentToolScopeTx(
		ctx, tx, command.ProjectID, command.ConversationID, command.SkillInvocationID,
	)
	if err != nil {
		return AgentToolCall{}, err
	}
	var registry *agenttool.Registry
	if command.MemoryGenerationID != "" {
		registry, err = s.memoryGenerationToolRegistryTx(ctx, tx)
	} else {
		registry, err = s.agentToolRegistryQuery(ctx, tx, workspaceID)
	}
	if err != nil {
		return AgentToolCall{}, err
	}
	descriptor, ok := registry.Get(command.ToolID)
	if !ok {
		return AgentToolCall{}, domainError("AGENT_TOOL_NOT_FOUND", "请求的 Agent 工具未在当前工作区可信目录中注册。")
	}
	if !descriptor.Enabled {
		return AgentToolCall{}, domainError("AGENT_TOOL_UNAVAILABLE", "请求的 Agent 工具当前已禁用。")
	}
	if descriptor.ConfigurationHash != command.ConfigurationHash {
		return AgentToolCall{}, domainError("AGENT_TOOL_CONFIGURATION_CHANGED", "工具配置已变化，请重新加载工具目录后发起调用。")
	}
	if err := validateProgramToolGrant(registry, command); err != nil {
		return AgentToolCall{}, err
	}
	if command.MemoryGenerationID != "" {
		activity, _ := AgentActivityFromContext(ctx)
		if _, err := s.resolveAgentActivityPrincipalTx(ctx, tx, activity); err != nil {
			return AgentToolCall{}, err
		}
		job, err := memoryGenerationQuery(ctx, tx, command.MemoryGenerationID)
		if err != nil {
			return AgentToolCall{}, err
		}
		if !memoryGenerationToolPolicy(job.Phase, descriptor) {
			return AgentToolCall{}, domainError("AGENT_TOOL_EXECUTION_MODE_UNSUPPORTED", "Memory tools require an approved native execution stage.")
		}
	}
	if err := s.validateAgentTaskToolBeginTx(ctx, tx, command); err != nil {
		return AgentToolCall{}, err
	}
	if err := s.validateExecutionToolBeginTx(ctx, tx, command); err != nil {
		return AgentToolCall{}, err
	}
	if err := validateAgentTurnToolStartTx(ctx, tx, command.AgentTurnID); err != nil {
		return AgentToolCall{}, err
	}
	if command.ToolID == updateSavedInstructionsTool {
		argumentSummary, err = s.prepareInstructionToolTx(ctx, tx, command)
		if err != nil {
			return AgentToolCall{}, err
		}
	}
	if command.ToolID == publishAgentMemoryTool {
		_, args, err := s.memoryPublicationBindingTx(ctx, tx, command)
		if err != nil {
			return AgentToolCall{}, err
		}
		argumentSummary, err = json.Marshal(map[string]any{"private_memory_publication": true, "expected_version": args.ExpectedVersion, "file_count": len(args.Files), "snapshot": args.Snapshot})
		if err != nil {
			return AgentToolCall{}, err
		}
	}
	if command.ToolID == "runtime:prepare_agent_memory_publication" {
		argumentSummary = json.RawMessage(`{"private_memory_preparation":true}`)
	}
	existing, err := scanAgentToolCall(tx.QueryRowContext(
		ctx,
		agentToolCallSelect+` WHERE conversation_id = ? AND sdk_tool_call_id = ?`,
		command.ConversationID,
		command.SDKToolCallID,
	))
	if err == nil {
		if _, err := s.validateMemoryGenerationToolCallTx(ctx, tx, existing.AgentToolCallID); err != nil {
			return AgentToolCall{}, err
		}
		if existing.ProgramCallID != "" || command.ProgramCallID != "" {
			turnID := ""
			if existing.AgentTurnID != nil {
				turnID = *existing.AgentTurnID
			}
			var backgroundID string
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT agent_task_attempt_id FROM agent_task_tool_calls WHERE agent_tool_call_id=?), '')`, existing.AgentToolCallID).Scan(&backgroundID); err != nil {
				return AgentToolCall{}, err
			}
			if turnID != command.AgentTurnID || backgroundID != command.AgentTaskAttemptID {
				return AgentToolCall{}, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Program tool call belongs to another execution.")
			}
		}
		if command.ToolID == delegateSubtaskTool || strings.HasPrefix(command.SDKToolCallID, "subtask:") {
			var turnID, backgroundID string
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(agent_turn_id,''),COALESCE((SELECT agent_task_attempt_id FROM agent_task_tool_calls WHERE agent_tool_call_id=agent_tool_calls.agent_tool_call_id),'') FROM agent_tool_calls WHERE agent_tool_call_id=?`, existing.AgentToolCallID).Scan(&turnID, &backgroundID); err != nil {
				return AgentToolCall{}, err
			}
			if turnID != command.AgentTurnID || backgroundID != command.AgentTaskAttemptID {
				return AgentToolCall{}, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "子任务调用属于其他执行。")
			}
		}
		var executionID string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT execution_attempt_id FROM execution_tool_calls WHERE agent_tool_call_id = ?), '')`, existing.AgentToolCallID).Scan(&executionID); err != nil {
			return AgentToolCall{}, err
		}
		if executionID != command.ExecutionAttemptID {
			return AgentToolCall{}, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "工具调用属于其他状态化执行尝试。")
		}
		if command.AgentTaskAttemptID != "" {
			var boundAttempt string
			if err := tx.QueryRowContext(ctx, `SELECT agent_task_attempt_id FROM agent_task_tool_calls WHERE agent_tool_call_id = ?`, existing.AgentToolCallID).Scan(&boundAttempt); err != nil || boundAttempt != command.AgentTaskAttemptID {
				return AgentToolCall{}, domainError("AGENT_TASK_ATTEMPT_SCOPE_MISMATCH", "工具调用属于其他执行尝试。")
			}
		}
		if existing.ProgramCallID != command.ProgramCallID || existing.ToolID != descriptor.ID || existing.ArgumentsHash != argumentHash ||
			existing.ProjectID != command.ProjectID {
			return AgentToolCall{}, domainError(
				"AGENT_TOOL_CALL_ID_CONFLICT",
				"同一 SDK tool_call_id 已用于不同的工具调用。",
			)
		}
		if command.ToolID == "runtime:execute_skill_script" {
			// Script package identity is checked independently of external service configuration.
			snapshot, _, _, err := scriptSnapshotForCallTx(ctx, tx, existing.AgentToolCallID)
			if err != nil {
				return AgentToolCall{}, err
			}
			if command.SkillSnapshot != nil && snapshot != *command.SkillSnapshot {
				return AgentToolCall{}, domainError("SKILL_SCRIPT_SNAPSHOT_MISMATCH", "脚本重试不能更改已确认的 Skill 版本。")
			}
			arguments, err := decodeScriptToolArguments(command.Arguments)
			if err != nil {
				return AgentToolCall{}, err
			}
			if arguments.CapabilityID != "" && arguments.CapabilityID != snapshot.CapabilityID {
				return AgentToolCall{}, domainError("SKILL_SCRIPT_SNAPSHOT_MISMATCH", "脚本重试不能更改已确认的 Skill 身份。")
			}
		}
		if err := s.validateAgentToolConfigurationTx(ctx, tx, existing, command.ConfigurationHash); err != nil {
			return AgentToolCall{}, err
		}
		if err := attachAgentToolApprovalTx(ctx, tx, &existing); err != nil {
			return AgentToolCall{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AgentToolCall{}, err
	}
	if err := validateSubtaskBudgetTx(ctx, tx, command); err != nil {
		return AgentToolCall{}, err
	}
	parentCallID, err := subtaskReadParentTx(ctx, tx, command, descriptor)
	if err != nil {
		return AgentToolCall{}, err
	}
	if command.ToolID == delegateSubtaskTool {
		var request struct {
			Title string `json:"title"`
		}
		_ = json.Unmarshal(command.Arguments, &request)
		argumentSummary, _ = json.Marshal(request)
	}
	callID := s.newID("tcall")
	var scriptSnapshot *activeSkillScript
	if command.ToolID == "runtime:execute_skill_script" {
		selected, err := s.requestedScriptSnapshotTx(ctx, tx, workspaceID, command)
		if err != nil {
			return AgentToolCall{}, err
		}
		scriptSnapshot = &selected
	}
	approvalStatus := "not_required"
	status := "running"
	var startedAt any = formatTime(now)
	if descriptor.Approval == agenttool.ApprovalAlways {
		approvalStatus = "pending"
		status = "pending_approval"
		startedAt = nil
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_tool_calls(
			agent_tool_call_id, workspace_id, project_id, conversation_id,
			skill_invocation_id, agent_turn_id, sdk_tool_call_id, tool_id, tool_kind,
			server_id, tool_name, access_mode, approval_policy, max_result_bytes,
			approval_status, status, arguments_summary_json, arguments_hash,
			requested_at, started_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		callID, workspaceID, command.ProjectID, command.ConversationID,
		nullableStringPointer(command.SkillInvocationID), nullableStringPointer(command.AgentTurnID),
		command.SDKToolCallID, descriptor.ID, string(descriptor.Kind),
		nullableStringPointer(descriptor.ServerID), descriptor.Name, string(descriptor.Access),
		string(descriptor.Approval), descriptor.MaxResultBytes, approvalStatus, status,
		string(argumentSummary), argumentHash, formatTime(now), startedAt, formatTime(now),
	); err != nil {
		return AgentToolCall{}, err
	}

	if command.MemoryGenerationID != "" {
		if err := s.bindMemoryGenerationToolTx(ctx, tx, callID, command.Arguments); err != nil {
			return AgentToolCall{}, err
		}
	}
	if command.ProgramCallID != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_program_tool_calls(agent_tool_call_id,program_call_id) VALUES(?,?)`, callID, command.ProgramCallID); err != nil {
			return AgentToolCall{}, err
		}
	}
	if parentCallID != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_subtask_reads(agent_tool_call_id,parent_tool_call_id) VALUES(?,?)`, callID, parentCallID); err != nil {
			return AgentToolCall{}, err
		}
	}
	if command.ToolID == updateSavedInstructionsTool {
		if err := s.saveInstructionProposalTx(ctx, tx, command, callID, argumentHash); err != nil {
			return AgentToolCall{}, err
		}
	}
	if command.ToolID == publishAgentMemoryTool {
		if err := s.saveMemoryPublicationIntentTx(ctx, tx, command, callID, argumentHash); err != nil {
			return AgentToolCall{}, err
		}
	}
	if descriptor.ConfigurationHash != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_tool_config_snapshots(agent_tool_call_id, configuration_hash, credential_user_id) VALUES(?, ?, ?)`, callID, descriptor.ConfigurationHash, identity.UserIDFromContext(ctx)); err != nil {
			return AgentToolCall{}, err
		}
	}
	if scriptSnapshot != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_tool_skill_snapshots(agent_tool_call_id, skill_version_id, skill_snapshot_id) VALUES(?, ?, ?)`, callID, optionalSkillID(scriptSnapshot.versionID), optionalSkillID(scriptSnapshot.snapshotID)); err != nil {
			return AgentToolCall{}, err
		}
	}
	if command.AgentTaskAttemptID != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_task_tool_calls(agent_tool_call_id, agent_task_attempt_id) VALUES(?, ?)`, callID, command.AgentTaskAttemptID); err != nil {
			return AgentToolCall{}, err
		}
	}
	if command.ExecutionAttemptID != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO execution_tool_calls(agent_tool_call_id, execution_attempt_id) VALUES(?, ?)`, callID, command.ExecutionAttemptID); err != nil {
			return AgentToolCall{}, err
		}
	}
	eventType := "agent.tool_call.started"
	if descriptor.Approval == agenttool.ApprovalAlways {
		approvalID := s.newID("tapr")
		snapshotHash := sha256Hex([]byte(descriptor.ID + "\n" + argumentHash))
		if descriptor.ConfigurationHash != "" {
			snapshotHash = sha256Hex([]byte(descriptor.ID + "\n" + argumentHash + "\n" + descriptor.ConfigurationHash))
		}
		title := truncateUTF8("允许调用 "+descriptor.Name, 160)
		reason := "该工具可能修改外部状态或处理敏感数据，执行前需要确认。"
		if descriptor.Access == agenttool.AccessWrite {
			reason = "该工具会修改外部状态，执行前需要确认。"
		}
		if descriptor.ID == "runtime:control_execution" {
			var args ExecutionControlArguments
			if json.Unmarshal(command.Arguments, &args) == nil {
				labels := map[string]string{"pause_run": "暂停运行", "resume_run": "继续运行", "cancel_run": "结束运行", "retry_failed_step": "重试失败步骤", "cancel_agent_task": "取消后台任务", "retry_agent_task": "重新执行后台任务", "pause_agent_task": "暂停后台任务", "resume_agent_task": "继续后台任务"}
				if label := labels[args.ActionID]; label != "" {
					title = truncateUTF8("允许"+label+"："+args.TargetID, 160)
					reason = "只对参数中指定的任务执行该操作。批准后仍会复查任务状态和权限；暂停请求可能需要等待安全检查点。"
				}
			}
		}
		if descriptor.Kind == agenttool.KindHosted {
			reason += " 批准后，请求内容及 input_assets 所列作品材料的文件内容将发送到当前配置的托管服务；材料按指定快照读取。"
		}
		if descriptor.ID == updateSavedInstructionsTool {
			title = "确认保存 Agent 规则"
			reason = "仅发起用户可确认此变更。批准后影响后续新执行；当前及已暂停执行继续使用冻结版本。清空不会删除历史执行版本。"
		}
		if scriptSnapshot != nil {
			snapshotHash = sha256Hex([]byte(descriptor.ID + "\n" + argumentHash + "\n" + scriptSnapshot.versionID + "\n" + scriptSnapshot.contentHash))
			reason += " Skill: " + scriptSnapshot.skillName + "@" + scriptSnapshot.version + " (" + scriptSnapshot.contentHash + ")"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agent_tool_approvals(
				agent_tool_approval_id, agent_tool_call_id, workspace_id, project_id,
				conversation_id, status, version, title, reason, options_json,
				subject_snapshot_hash, requested_at
			) VALUES(?, ?, ?, ?, ?, 'pending', 1, ?, ?, '["approve","reject"]', ?, ?)`,
			approvalID, callID, workspaceID, command.ProjectID, command.ConversationID,
			title, reason, snapshotHash, formatTime(now),
		); err != nil {
			return AgentToolCall{}, err
		}
		eventType = "agent.tool_approval.requested"
	}
	if _, err := s.appendEvent(
		ctx, tx, command.ProjectID, nil, nil, eventType, "agent_tool_call", callID,
		map[string]any{
			"tool_id": descriptor.ID, "tool_kind": descriptor.Kind,
			"access_mode": descriptor.Access, "approval_status": approvalStatus,
		},
	); err != nil {
		return AgentToolCall{}, err
	}
	call, err := scanAgentToolCall(tx.QueryRowContext(ctx, agentToolCallSelect+` WHERE agent_tool_call_id = ?`, callID))
	if err != nil {
		return AgentToolCall{}, err
	}
	if err := attachAgentToolApprovalTx(ctx, tx, &call); err != nil {
		return AgentToolCall{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentToolCall{}, err
	}
	return call, nil
}

func (s *Store) StartAgentToolCall(
	ctx context.Context,
	command StartAgentToolCallCommand,
) (AgentToolCall, error) {
	command.AgentToolCallID = strings.TrimSpace(command.AgentToolCallID)
	command.ExpectedSDKToolCallID = strings.TrimSpace(command.ExpectedSDKToolCallID)
	if command.AgentToolCallID == "" || command.ExpectedSDKToolCallID == "" {
		return AgentToolCall{}, domainError("REQUEST_VALIDATION_FAILED", "工具调用启动信息不完整。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentToolCall{}, err
	}
	defer tx.Rollback()
	call, err := getAgentToolCallTx(ctx, tx, command.AgentToolCallID)
	if err != nil {
		return AgentToolCall{}, err
	}
	if call.SDKToolCallID != command.ExpectedSDKToolCallID {
		return AgentToolCall{}, domainError("AGENT_TOOL_CALL_ID_CONFLICT", "SDK 工具调用标识不匹配。")
	}
	if call.ParentToolCallID != "" {
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_tool_calls WHERE agent_tool_call_id=? AND status='running')`, call.ParentToolCallID).Scan(&active); err != nil {
			return AgentToolCall{}, err
		}
		if !active {
			return AgentToolCall{}, domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "父子任务已经结束，不能启动新的读取。")
		}
	}
	if call.AgentTurnID != nil {
		if err := validateAgentTurnToolStartTx(ctx, tx, *call.AgentTurnID); err != nil {
			return AgentToolCall{}, err
		}
	}
	if err := s.validateAgentTaskToolCallTx(ctx, tx, call.AgentToolCallID); err != nil {
		return AgentToolCall{}, err
	}
	if err := s.validateAgentToolConfigurationTx(ctx, tx, call, command.ConfigurationHash); err != nil {
		return AgentToolCall{}, err
	}
	if call.Status == "running" {
		return call, nil
	}
	if call.Status == "pending_approval" {
		return AgentToolCall{}, domainError("AGENT_TOOL_APPROVAL_REQUIRED", "该工具调用仍在等待用户确认。")
	}
	if call.Status != "approved" {
		return AgentToolCall{}, domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "当前工具调用状态不允许执行。")
	}
	now := s.now()
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_tool_calls
		SET status = 'running', started_at = COALESCE(started_at, ?),
			approval_consumed_at = CASE WHEN approval_policy = 'always'
				THEN COALESCE(approval_consumed_at, ?) ELSE approval_consumed_at END,
			updated_at = ?
		WHERE agent_tool_call_id = ? AND status = 'approved'`,
		formatTime(now), formatTime(now), formatTime(now), call.AgentToolCallID,
	)
	if err != nil {
		return AgentToolCall{}, err
	}
	if err := requireOneRow(result, "AGENT_TOOL_CALL_STATE_CONFLICT", "工具调用状态已变化。"); err != nil {
		return AgentToolCall{}, err
	}
	if _, err := s.appendEvent(ctx, tx, call.ProjectID, nil, nil,
		"agent.tool_call.started", "agent_tool_call", call.AgentToolCallID,
		map[string]any{"tool_id": call.ToolID}); err != nil {
		return AgentToolCall{}, err
	}
	call, err = getAgentToolCallTx(ctx, tx, call.AgentToolCallID)
	if err != nil {
		return AgentToolCall{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentToolCall{}, err
	}
	return call, nil
}

func (s *Store) CompleteAgentToolCall(
	ctx context.Context,
	command CompleteAgentToolCallCommand,
) (AgentToolCall, error) {
	command.AgentToolCallID = strings.TrimSpace(command.AgentToolCallID)
	command.TraceRef = truncateUTF8(strings.TrimSpace(command.TraceRef), 512)
	if command.AgentToolCallID == "" || len(command.Result) == 0 {
		return AgentToolCall{}, domainError("REQUEST_VALIDATION_FAILED", "工具调用完成信息不完整。")
	}
	resultSummary, resultHash, canonicalSize, err := summarizeAgentToolPayload(command.Result, false)
	if err != nil {
		return AgentToolCall{}, err
	}
	resultSize := command.ResultSizeBytes
	if resultSize <= 0 {
		resultSize = canonicalSize
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentToolCall{}, err
	}
	defer tx.Rollback()
	call, err := getAgentToolCallTx(ctx, tx, command.AgentToolCallID)
	if err != nil {
		return AgentToolCall{}, err
	}
	if resultSize > call.MaxResultBytes || canonicalSize > call.MaxResultBytes {
		return AgentToolCall{}, domainError("AGENT_TOOL_RESULT_TOO_LARGE", "工具返回结果超过平台限制。")
	}
	memoryBound, err := s.validateMemoryGenerationToolCallTx(ctx, tx, call.AgentToolCallID)
	if err != nil {
		return AgentToolCall{}, err
	}
	if memoryBound {
		resultSummary = json.RawMessage(`{"private_memory_tool_result":true}`)
		command.TraceRef = ""
	}
	if call.Status == "completed" && call.ResultHash != nil && *call.ResultHash == resultHash {
		return call, nil
	}
	if err := s.validateAgentTaskToolCallTx(ctx, tx, call.AgentToolCallID); err != nil {
		return AgentToolCall{}, err
	}
	if call.Status != "running" {
		return AgentToolCall{}, domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "当前工具调用状态不允许完成。")
	}
	if err := s.completeSubtaskTx(ctx, tx, call, command.Result); err != nil {
		return AgentToolCall{}, err
	}
	if call.ToolID == publishAgentMemoryTool {
		if err := verifyMemoryPublicationReceiptTx(ctx, tx, call); err != nil {
			return AgentToolCall{}, err
		}
		resultSummary = json.RawMessage(`{"private_memory_result":true}`)
	}
	if call.ToolID == updateSavedInstructionsTool {
		if err := verifyInstructionWriteReceiptTx(ctx, tx, call); err != nil {
			return AgentToolCall{}, err
		}
	}
	if call.ToolID == nativeWorkspaceExecTool || call.ToolID == nativeWorkspaceStdinTool {
		if err := verifyNativeShellReceiptTx(ctx, tx, call); err != nil {
			return AgentToolCall{}, err
		}
	}
	if call.ToolID == nativeWorkspacePatchTool {
		if err := verifyNativePatchReceiptsTx(ctx, tx, call); err != nil {
			return AgentToolCall{}, err
		}
	}
	if call.ToolKind == "hosted" {
		resultSummary = hostedCitationSummary(command.Result, resultSummary)
	}
	if call.ToolID == "runtime:get_saved_instructions" || call.ToolID == updateSavedInstructionsTool {
		resultSummary = json.RawMessage(`{"private_instruction_result":true}`)
	}
	if call.ToolID == "runtime:prepare_agent_memory_publication" {
		resultSummary = json.RawMessage(`{"private_memory_result":true}`)
	}
	if call.ToolID == delegateSubtaskTool {
		resultSummary = json.RawMessage(`{"subtask_result_available":true}`)
	}
	now := s.now()
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_tool_calls
		SET status = 'completed', result_summary_json = ?, result_hash = ?,
			result_size_bytes = ?, trace_ref = ?, completed_at = ?, updated_at = ?
		WHERE agent_tool_call_id = ? AND status = 'running'`,
		string(resultSummary), resultHash, resultSize, nullableStringPointer(command.TraceRef),
		formatTime(now), formatTime(now), call.AgentToolCallID,
	)
	if err != nil {
		return AgentToolCall{}, err
	}
	if err := requireOneRow(result, "AGENT_TOOL_CALL_STATE_CONFLICT", "工具调用状态已变化。"); err != nil {
		return AgentToolCall{}, err
	}
	if _, err := s.appendEvent(ctx, tx, call.ProjectID, nil, nil,
		"agent.tool_call.completed", "agent_tool_call", call.AgentToolCallID,
		map[string]any{"tool_id": call.ToolID, "result_size_bytes": resultSize}); err != nil {
		return AgentToolCall{}, err
	}
	call, err = getAgentToolCallTx(ctx, tx, call.AgentToolCallID)
	if err != nil {
		return AgentToolCall{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentToolCall{}, err
	}
	return call, nil
}

func (s *Store) FailAgentToolCall(
	ctx context.Context,
	command FailAgentToolCallCommand,
) (AgentToolCall, error) {
	command.AgentToolCallID = strings.TrimSpace(command.AgentToolCallID)
	command.ErrorCode = truncateUTF8(strings.TrimSpace(command.ErrorCode), 128)
	command.ErrorMessage = truncateUTF8(strings.TrimSpace(command.ErrorMessage), 2048)
	command.TraceRef = truncateUTF8(strings.TrimSpace(command.TraceRef), 512)
	if command.AgentToolCallID == "" || command.ErrorCode == "" || command.ErrorMessage == "" {
		return AgentToolCall{}, domainError("REQUEST_VALIDATION_FAILED", "工具调用失败信息不完整。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentToolCall{}, err
	}
	defer tx.Rollback()
	call, err := getAgentToolCallTx(ctx, tx, command.AgentToolCallID)
	if err != nil {
		return AgentToolCall{}, err
	}
	memoryBound, err := s.validateMemoryGenerationToolCallTx(ctx, tx, call.AgentToolCallID)
	if err != nil {
		return AgentToolCall{}, err
	}
	if memoryBound {
		command.ErrorCode = "MEMORY_TOOL_FAILED"
		command.ErrorMessage = "Private memory tool failed."
		command.TraceRef = ""
	}
	if call.Status == "failed" && call.ErrorCode != nil && *call.ErrorCode == command.ErrorCode {
		return call, nil
	}
	if call.ToolID == "runtime:get_saved_instructions" || call.ToolID == updateSavedInstructionsTool {
		command.ErrorMessage = "规则工具调用失败，正文不进入共享执行记录。请核对当前规则版本、编辑权限和执行状态。"
	}
	if call.ToolID == publishAgentMemoryTool || call.ToolID == "runtime:prepare_agent_memory_publication" {
		command.ErrorMessage = "私有记忆保存失败，请核对版本、本人审批和原执行状态。"
	}
	if call.Status != "running" {
		return AgentToolCall{}, domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "当前工具调用状态不允许标记失败。")
	}
	now := s.now()
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_tool_calls
		SET status = 'failed', error_code = ?, error_message = ?, trace_ref = ?,
			completed_at = ?, updated_at = ?
		WHERE agent_tool_call_id = ? AND status = 'running'`,
		command.ErrorCode, command.ErrorMessage, nullableStringPointer(command.TraceRef),
		formatTime(now), formatTime(now), call.AgentToolCallID,
	)
	if err != nil {
		return AgentToolCall{}, err
	}
	if err := requireOneRow(result, "AGENT_TOOL_CALL_STATE_CONFLICT", "工具调用状态已变化。"); err != nil {
		return AgentToolCall{}, err
	}
	if _, err := s.appendEvent(ctx, tx, call.ProjectID, nil, nil,
		"agent.tool_call.failed", "agent_tool_call", call.AgentToolCallID,
		map[string]any{"tool_id": call.ToolID, "error_code": command.ErrorCode}); err != nil {
		return AgentToolCall{}, err
	}
	call, err = getAgentToolCallTx(ctx, tx, call.AgentToolCallID)
	if err != nil {
		return AgentToolCall{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentToolCall{}, err
	}
	return call, nil
}

func (s *Store) CancelAgentToolCall(
	ctx context.Context,
	command CancelAgentToolCallCommand,
) (AgentToolCall, error) {
	command.AgentToolCallID = strings.TrimSpace(command.AgentToolCallID)
	command.Reason = truncateUTF8(strings.TrimSpace(command.Reason), 512)
	if command.AgentToolCallID == "" {
		return AgentToolCall{}, domainError("REQUEST_VALIDATION_FAILED", "缺少工具调用 ID。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentToolCall{}, err
	}
	defer tx.Rollback()
	call, err := getAgentToolCallTx(ctx, tx, command.AgentToolCallID)
	if err != nil {
		return AgentToolCall{}, err
	}
	memoryBound, err := s.validateMemoryGenerationToolCallTx(ctx, tx, call.AgentToolCallID)
	if err != nil {
		return AgentToolCall{}, err
	}
	if memoryBound {
		command.Reason = "Private memory tool cancelled."
	}
	if call.Status == "cancelled" {
		return call, nil
	}
	if call.Status != "pending_approval" && call.Status != "approved" && call.Status != "running" {
		return AgentToolCall{}, domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "当前工具调用状态不允许取消。")
	}
	now := s.now()
	if call.ApprovalStatus == "pending" || call.ApprovalStatus == "approved" {
		resolution, _ := json.Marshal(map[string]any{"action": "cancel", "reason": command.Reason})
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_tool_approvals
			SET status = 'cancelled', version = version + 1, resolved_at = ?, resolution_json = ?
			WHERE agent_tool_call_id = ? AND status IN ('pending','approved')`,
			formatTime(now), string(resolution), call.AgentToolCallID,
		); err != nil {
			return AgentToolCall{}, err
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_tool_calls
		SET status = 'cancelled', approval_status = CASE
				WHEN approval_status IN ('pending','approved') THEN 'cancelled'
				ELSE approval_status END,
			error_code = 'AGENT_TOOL_CALL_CANCELLED', error_message = ?,
			completed_at = ?, updated_at = ?
		WHERE agent_tool_call_id = ? AND status IN ('pending_approval','approved','running')`,
		command.Reason, formatTime(now), formatTime(now), call.AgentToolCallID,
	)
	if err != nil {
		return AgentToolCall{}, err
	}
	if err := requireOneRow(result, "AGENT_TOOL_CALL_STATE_CONFLICT", "工具调用状态已变化。"); err != nil {
		return AgentToolCall{}, err
	}
	if _, err := s.appendEvent(ctx, tx, call.ProjectID, nil, nil,
		"agent.tool_call.cancelled", "agent_tool_call", call.AgentToolCallID,
		map[string]any{"tool_id": call.ToolID, "reason": command.Reason}); err != nil {
		return AgentToolCall{}, err
	}
	call, err = getAgentToolCallTx(ctx, tx, call.AgentToolCallID)
	if err != nil {
		return AgentToolCall{}, err
	}
	if err := attachAgentToolApprovalTx(ctx, tx, &call); err != nil {
		return AgentToolCall{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentToolCall{}, err
	}
	return call, nil
}

func (s *Store) ResolveAgentToolApproval(
	ctx context.Context,
	command ResolveAgentToolApprovalCommand,
) (AgentToolApproval, error) {
	command.AgentToolApprovalID = strings.TrimSpace(command.AgentToolApprovalID)
	command.SubjectSnapshotHash = strings.TrimSpace(command.SubjectSnapshotHash)
	command.Action = strings.TrimSpace(command.Action)
	command.ActorRef = strings.TrimSpace(command.ActorRef)
	if command.ActorRef == "" {
		command.ActorRef = actorRefFromContext(ctx)
	}
	if command.AgentToolApprovalID == "" || command.ExpectedVersion < 1 ||
		command.SubjectSnapshotHash == "" ||
		(command.Action != "approve" && command.Action != "reject") {
		return AgentToolApproval{}, domainError("REQUEST_VALIDATION_FAILED", "工具审批请求不完整。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentToolApproval{}, err
	}
	defer tx.Rollback()
	approval, err := getAgentToolApprovalTx(ctx, tx, command.AgentToolApprovalID)
	if err != nil {
		return AgentToolApproval{}, err
	}
	call, err := getAgentToolCallTx(ctx, tx, approval.AgentToolCallID)
	if err != nil {
		return AgentToolApproval{}, err
	}
	var ruleProposal *AgentInstructionProposal
	memoryGenerationID, _, err := memoryToolOwnerTx(ctx, tx, call.AgentToolCallID, true)
	if err != nil {
		return AgentToolApproval{}, err
	}
	if call.ToolID == publishAgentMemoryTool {
		if err := authorizeMemoryPublicationApprovalTx(ctx, tx, call); err != nil {
			return AgentToolApproval{}, err
		}
	}
	if call.ToolID == updateSavedInstructionsTool {
		proposal, user, err := s.instructionProposalApproverTx(ctx, tx, call.AgentToolCallID)
		if err != nil {
			return AgentToolApproval{}, err
		}
		if !user.Allows(identity.RoleEditor) {
			return AgentToolApproval{}, domainError("WORKSPACE_ACCESS_DENIED", "当前用户不能确认规则变更。")
		}
		ruleProposal = &proposal
	}
	if err := authorizeAgentToolApprovalCommandTx(ctx, tx, approval, call, command.Scope); err != nil {
		return AgentToolApproval{}, err
	}
	if command.Scope == "" {
		command.Scope = approval.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return AgentToolApproval{}, err
	}
	if hit {
		return decodeAgentToolApprovalReceipt(cached, approval, command)
	}
	if memoryGenerationID != "" && command.Action == "approve" {
		proposal, err := s.memoryToolProposalTx(ctx, tx, call.AgentToolCallID)
		if err != nil {
			return AgentToolApproval{}, err
		}
		if !proposal.CanApprove {
			return AgentToolApproval{}, domainError("AGENT_MEMORY_CONFLICT", "Private tool proposal is stale or unavailable.")
		}
	}
	if call.ToolID == publishAgentMemoryTool && command.Action == "approve" {
		proposal, err := s.memoryProposalTx(ctx, tx, call.AgentToolCallID)
		if err != nil {
			return AgentToolApproval{}, err
		}
		if !proposal.CanApprove {
			return AgentToolApproval{}, domainError("AGENT_MEMORY_CONFLICT", "Memory proposal is stale or unavailable; reject it and prepare a new publication.")
		}
	}
	if ruleProposal != nil && command.Action == "approve" && !ruleProposal.CanApprove {
		return AgentToolApproval{}, domainError("AGENT_INSTRUCTIONS_CONFLICT", "规则版本或编辑权限已变化，请拒绝此提案后重新发起。")
	}
	if approval.Status != "pending" {
		return AgentToolApproval{}, domainError("AGENT_TOOL_APPROVAL_ALREADY_RESOLVED", "该工具审批已经处理。")
	}
	if approval.Version != command.ExpectedVersion {
		return AgentToolApproval{}, domainError("AGENT_TOOL_APPROVAL_VERSION_CONFLICT", "工具审批版本已更新。")
	}
	if approval.SubjectSnapshotHash != command.SubjectSnapshotHash {
		return AgentToolApproval{}, domainError("AGENT_TOOL_APPROVAL_SUBJECT_CHANGED", "工具参数已经变化，请刷新后重试。")
	}
	if !slices.Contains(approval.Options, command.Action) {
		return AgentToolApproval{}, domainError("AGENT_TOOL_APPROVAL_ACTION_UNAVAILABLE", "当前工具审批不允许该操作。")
	}
	now := s.now()
	resolution, _ := json.Marshal(map[string]any{"action": command.Action})
	approvalStatus := "approved"
	callStatus := "approved"
	eventType := "agent.tool_approval.approved"
	if command.Action == "reject" {
		approvalStatus = "rejected"
		callStatus = "rejected"
		eventType = "agent.tool_approval.rejected"
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_tool_approvals
		SET status = ?, version = version + 1, resolved_at = ?, resolution_json = ?, actor_ref = ?
		WHERE agent_tool_approval_id = ? AND status = 'pending' AND version = ?`,
		approvalStatus, formatTime(now), string(resolution), command.ActorRef,
		approval.AgentToolApprovalID, command.ExpectedVersion,
	)
	if err != nil {
		return AgentToolApproval{}, err
	}
	if err := requireOneRow(result, "AGENT_TOOL_APPROVAL_VERSION_CONFLICT", "工具审批状态已变化。"); err != nil {
		return AgentToolApproval{}, err
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE agent_tool_calls
		SET approval_status = ?, status = ?,
			completed_at = CASE WHEN ? = 'rejected' THEN ? ELSE completed_at END,
			updated_at = ?
		WHERE agent_tool_call_id = ? AND status = 'pending_approval'`,
		approvalStatus, callStatus, callStatus, formatTime(now), formatTime(now), approval.AgentToolCallID,
	)
	if err != nil {
		return AgentToolApproval{}, err
	}
	if err := requireOneRow(result, "AGENT_TOOL_CALL_STATE_CONFLICT", "关联工具调用状态已变化。"); err != nil {
		return AgentToolApproval{}, err
	}
	if _, err := s.appendEvent(ctx, tx, approval.ProjectID, nil, nil,
		eventType, "agent_tool_approval", approval.AgentToolApprovalID,
		map[string]any{
			"agent_tool_call_id": approval.AgentToolCallID,
			"actor_ref":          command.ActorRef,
		}); err != nil {
		return AgentToolApproval{}, err
	}
	if call.AgentTurnID != nil && strings.TrimSpace(*call.AgentTurnID) != "" {
		result, err := tx.ExecContext(ctx, `
			UPDATE agent_turns SET status = 'accepted', updated_at = ?
			WHERE agent_turn_id = ? AND status = 'waiting_approval'
				AND EXISTS (SELECT 1 FROM agent_turn_run_states WHERE agent_turn_id = agent_turns.agent_turn_id)
				AND NOT EXISTS (
					SELECT 1 FROM agent_tool_calls pending_call
					JOIN agent_tool_approvals pending_approval
						ON pending_approval.agent_tool_call_id = pending_call.agent_tool_call_id
					WHERE pending_call.agent_turn_id = agent_turns.agent_turn_id
						AND pending_call.status = 'pending_approval'
						AND pending_approval.status = 'pending'
				)`,
			formatTime(now), *call.AgentTurnID,
		)
		if err != nil {
			return AgentToolApproval{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return AgentToolApproval{}, err
		}
		if affected == 1 {
			turn, err := scanAgentTurn(tx.QueryRowContext(
				ctx, agentTurnSelect+` WHERE agent_turn_id = ?`, *call.AgentTurnID,
			))
			if err != nil {
				return AgentToolApproval{}, err
			}
			if _, err := s.appendAgentTurnEventTx(ctx, tx, turn, "agent.updated", false, map[string]any{
				"status": "accepted", "label": "正在恢复审批后的执行",
			}); err != nil {
				return AgentToolApproval{}, err
			}
		}
	}
	if err := s.resumeApprovedAgentTaskTx(ctx, tx, call.AgentToolCallID, now); err != nil {
		return AgentToolApproval{}, err
	}
	approval, err = getAgentToolApprovalTx(ctx, tx, approval.AgentToolApprovalID)
	if err != nil {
		return AgentToolApproval{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, approval, now); err != nil {
		return AgentToolApproval{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentToolApproval{}, err
	}
	return approval, nil
}

func (s *Store) GetAgentToolCall(ctx context.Context, callID string) (AgentToolCall, error) {
	call, err := scanAgentToolCall(s.db.QueryRowContext(
		ctx, agentToolCallSelect+` WHERE agent_tool_call_id = ?`, strings.TrimSpace(callID),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentToolCall{}, domainError("AGENT_TOOL_CALL_NOT_FOUND", "工具调用不存在。")
	}
	if err != nil {
		return AgentToolCall{}, err
	}
	if err := attachAgentToolApproval(ctx, s.db, &call); err != nil {
		return AgentToolCall{}, err
	}
	return call, nil
}

func (s *Store) ListAgentToolCalls(ctx context.Context, projectID string) ([]AgentToolCall, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, agentToolCallSelect+`
		WHERE project_id = ? ORDER BY requested_at DESC, agent_tool_call_id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	var result []AgentToolCall
	for rows.Next() {
		call, scanErr := scanAgentToolCall(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		result = append(result, call)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range result {
		if err := attachAgentToolApproval(ctx, s.db, &result[index]); err != nil {
			return nil, err
		}
	}
	if result == nil {
		result = []AgentToolCall{}
	}
	return result, nil
}

func (s *Store) GetAgentToolApproval(ctx context.Context, approvalID string) (AgentToolApproval, error) {
	approval, err := scanAgentToolApproval(s.db.QueryRowContext(
		ctx, agentToolApprovalSelect+` WHERE agent_tool_approval_id = ?`, strings.TrimSpace(approvalID),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentToolApproval{}, domainError("AGENT_TOOL_APPROVAL_NOT_FOUND", "工具审批不存在。")
	}
	return approval, err
}

func (s *Store) ListAgentToolApprovals(
	ctx context.Context,
	projectID string,
	status string,
) ([]AgentToolApproval, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	query := agentToolApprovalSelect + ` WHERE project_id = ?`
	arguments := []any{projectID}
	status = strings.TrimSpace(status)
	if status != "" {
		query += ` AND status = ?`
		arguments = append(arguments, status)
	}
	query += ` ORDER BY requested_at ASC, agent_tool_approval_id ASC`
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AgentToolApproval
	for rows.Next() {
		approval, scanErr := scanAgentToolApproval(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, approval)
	}
	if result == nil {
		result = []AgentToolApproval{}
	}
	return result, rows.Err()
}

func validateAgentToolScopeTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	conversationID string,
	skillInvocationID string,
) (string, error) {
	var workspaceID string
	err := tx.QueryRowContext(ctx, `
		SELECT p.workspace_id
		FROM projects p
		JOIN conversations c ON c.project_id = p.project_id
		WHERE p.project_id = ? AND c.conversation_id = ? AND p.deleted_at IS NULL`,
		projectID, conversationID,
	).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domainError("CONVERSATION_PROJECT_MISMATCH", "会话不属于指定项目。")
	}
	if err != nil {
		return "", err
	}
	if skillInvocationID != "" {
		var count int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM skill_invocations
			WHERE skill_invocation_id = ? AND project_id = ? AND conversation_id = ?`,
			skillInvocationID, projectID, conversationID,
		).Scan(&count); err != nil {
			return "", err
		}
		if count != 1 {
			return "", domainError("SKILL_INVOCATION_SCOPE_MISMATCH", "Skill 调用不属于指定项目和会话。")
		}
	}
	return workspaceID, nil
}

func summarizeAgentToolPayload(
	raw json.RawMessage,
	requireObject bool,
) (json.RawMessage, string, int, error) {
	if len(raw) > maxAgentToolArgumentsBytes && requireObject {
		return nil, "", 0, domainError("AGENT_TOOL_ARGUMENTS_TOO_LARGE", "工具参数超过平台限制。")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, "", 0, domainError("REQUEST_VALIDATION_FAILED", "工具调用数据不是有效 JSON。")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, "", 0, domainError("REQUEST_VALIDATION_FAILED", "工具调用数据只能包含一个 JSON 值。")
	}
	if requireObject {
		if _, ok := value.(map[string]any); !ok {
			return nil, "", 0, domainError("REQUEST_VALIDATION_FAILED", "工具参数必须是 JSON 对象。")
		}
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, "", 0, err
	}
	summary, err := json.Marshal(redactAgentToolValue(value, 0))
	if err != nil {
		return nil, "", 0, err
	}
	if len(summary) > maxAgentToolSummaryBytes {
		summary, _ = json.Marshal(map[string]any{
			"truncated":  true,
			"size_bytes": len(canonical),
		})
	}
	return json.RawMessage(summary), sha256Hex(canonical), len(canonical), nil
}

func redactAgentToolValue(value any, depth int) any {
	if depth >= 6 {
		return "[truncated]"
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		result := make(map[string]any, min(len(keys), 50)+1)
		for index, key := range keys {
			if index >= 50 {
				result["_truncated_fields"] = len(keys) - 50
				break
			}
			result[key] = redactAgentToolField(key, typed[key], depth+1)
		}
		return result
	case []any:
		limit := min(len(typed), 20)
		result := make([]any, 0, limit+1)
		for _, item := range typed[:limit] {
			result = append(result, redactAgentToolValue(item, depth+1))
		}
		if len(typed) > limit {
			result = append(result, map[string]any{"truncated_items": len(typed) - limit})
		}
		return result
	case string:
		return truncateUTF8(typed, 512)
	default:
		return typed
	}
}

func redactAgentToolField(key string, value any, depth int) any {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.ToLower(key))
	if normalized == "inputjson" {
		if encoded, ok := value.(string); ok {
			decoder := json.NewDecoder(strings.NewReader(encoded))
			decoder.UseNumber()
			var nested any
			if err := decoder.Decode(&nested); err == nil {
				var extra any
				if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
					return redactAgentToolValue(nested, depth)
				}
			}
		}
	}
	if isSensitiveAgentToolKey(key) {
		return "[redacted]"
	}
	return redactAgentToolValue(value, depth)
}

func isSensitiveAgentToolKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.ToLower(key))
	for _, marker := range []string{
		"authorization", "password", "passwd", "token", "secret", "apikey",
		"accesskey", "privatekey", "cookie", "credential", "sessionid",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func requireOneRow(result sql.Result, code, message string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return domainError(code, message)
	}
	return nil
}

func getAgentToolCallTx(ctx context.Context, tx *sql.Tx, callID string) (AgentToolCall, error) {
	call, err := scanAgentToolCall(tx.QueryRowContext(ctx, agentToolCallSelect+` WHERE agent_tool_call_id = ?`, callID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentToolCall{}, domainError("AGENT_TOOL_CALL_NOT_FOUND", "工具调用不存在。")
	}
	return call, err
}

func attachAgentToolApprovalTx(ctx context.Context, tx *sql.Tx, call *AgentToolCall) error {
	return attachAgentToolApproval(ctx, tx, call)
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func attachAgentToolApproval(ctx context.Context, queryer rowQueryer, call *AgentToolCall) error {
	approval, err := scanAgentToolApproval(queryer.QueryRowContext(
		ctx, agentToolApprovalSelect+` WHERE agent_tool_call_id = ?`, call.AgentToolCallID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	call.Approval = &approval
	return nil
}

func getAgentToolApprovalTx(ctx context.Context, tx *sql.Tx, approvalID string) (AgentToolApproval, error) {
	approval, err := scanAgentToolApproval(tx.QueryRowContext(
		ctx, agentToolApprovalSelect+` WHERE agent_tool_approval_id = ?`, approvalID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentToolApproval{}, domainError("AGENT_TOOL_APPROVAL_NOT_FOUND", "工具审批不存在。")
	}
	return approval, err
}

const agentToolCallSelect = `
	SELECT agent_tool_call_id, workspace_id, project_id, conversation_id,
		skill_invocation_id, agent_turn_id, sdk_tool_call_id, tool_id, tool_kind,
		server_id, tool_name, access_mode, approval_policy, max_result_bytes,
		approval_status, status, arguments_summary_json, arguments_hash,
		result_summary_json, result_hash, result_size_bytes, trace_ref,
		error_code, error_message, requested_at, started_at, completed_at,
		approval_consumed_at, updated_at, ` + agentToolExecutionSelect + `,
		COALESCE((SELECT parent_tool_call_id FROM agent_subtask_reads WHERE agent_tool_call_id=agent_tool_calls.agent_tool_call_id), ''),
		COALESCE((SELECT program_call_id FROM agent_program_tool_calls WHERE agent_tool_call_id=agent_tool_calls.agent_tool_call_id), '')
	FROM agent_tool_calls`

func scanAgentToolCall(row rowScanner) (AgentToolCall, error) {
	var call AgentToolCall
	var skillInvocationID, agentTurnID, serverID sql.NullString
	var argumentsSummary string
	var resultSummary, resultHash, traceRef, errorCode, errorMessage sql.NullString
	var resultSize sql.NullInt64
	var requestedAt, updatedAt string
	var startedAt, completedAt, approvalConsumedAt sql.NullString
	var executionJSON sql.NullString
	if err := row.Scan(
		&call.AgentToolCallID, &call.WorkspaceID, &call.ProjectID, &call.ConversationID,
		&skillInvocationID, &agentTurnID, &call.SDKToolCallID, &call.ToolID, &call.ToolKind,
		&serverID, &call.ToolName, &call.AccessMode, &call.ApprovalPolicy,
		&call.MaxResultBytes, &call.ApprovalStatus, &call.Status,
		&argumentsSummary, &call.ArgumentsHash, &resultSummary, &resultHash,
		&resultSize, &traceRef, &errorCode, &errorMessage,
		&requestedAt, &startedAt, &completedAt, &approvalConsumedAt, &updatedAt,
		&executionJSON, &call.ParentToolCallID, &call.ProgramCallID,
	); err != nil {
		return AgentToolCall{}, err
	}
	call.SkillInvocationID = stringPointer(skillInvocationID)
	call.AgentTurnID = stringPointer(agentTurnID)
	if executionJSON.Valid {
		if err := json.Unmarshal([]byte(executionJSON.String), &call.Execution); err != nil {
			return AgentToolCall{}, fmt.Errorf("decode tool execution origin: %w", err)
		}
	}
	call.ServerID = stringPointer(serverID)
	call.ArgumentsSummary = json.RawMessage(argumentsSummary)
	if resultSummary.Valid {
		call.ResultSummary = json.RawMessage(resultSummary.String)
		attachToolCitations(&call)
	}
	call.ResultHash = stringPointer(resultHash)
	if resultSize.Valid {
		value := int(resultSize.Int64)
		call.ResultSizeBytes = &value
	}
	call.TraceRef = stringPointer(traceRef)
	call.ErrorCode = stringPointer(errorCode)
	call.ErrorMessage = stringPointer(errorMessage)
	var err error
	call.RequestedAt, err = parseTime(requestedAt)
	if err != nil {
		return AgentToolCall{}, err
	}
	call.StartedAt, err = optionalTime(startedAt)
	if err != nil {
		return AgentToolCall{}, err
	}
	call.CompletedAt, err = optionalTime(completedAt)
	if err != nil {
		return AgentToolCall{}, err
	}
	call.ApprovalConsumedAt, err = optionalTime(approvalConsumedAt)
	if err != nil {
		return AgentToolCall{}, err
	}
	call.UpdatedAt, err = parseTime(updatedAt)
	return call, err
}

const agentToolApprovalSelect = `
	SELECT agent_tool_approval_id, agent_tool_call_id, workspace_id, project_id,
		conversation_id, status, version, title, reason, options_json,
		subject_snapshot_hash, requested_at, resolved_at, resolution_json, actor_ref
	FROM agent_tool_approvals`

func scanAgentToolApproval(row rowScanner) (AgentToolApproval, error) {
	var approval AgentToolApproval
	var optionsJSON, requestedAt string
	var resolvedAt, resolutionJSON, actorRef sql.NullString
	if err := row.Scan(
		&approval.AgentToolApprovalID, &approval.AgentToolCallID,
		&approval.WorkspaceID, &approval.ProjectID, &approval.ConversationID,
		&approval.Status, &approval.Version, &approval.Title, &approval.Reason,
		&optionsJSON, &approval.SubjectSnapshotHash, &requestedAt,
		&resolvedAt, &resolutionJSON, &actorRef,
	); err != nil {
		return AgentToolApproval{}, err
	}
	if err := json.Unmarshal([]byte(optionsJSON), &approval.Options); err != nil {
		return AgentToolApproval{}, fmt.Errorf("decode Agent tool approval options: %w", err)
	}
	var err error
	approval.RequestedAt, err = parseTime(requestedAt)
	if err != nil {
		return AgentToolApproval{}, err
	}
	approval.ResolvedAt, err = optionalTime(resolvedAt)
	if err != nil {
		return AgentToolApproval{}, err
	}
	if resolutionJSON.Valid {
		approval.Resolution = json.RawMessage(resolutionJSON.String)
	}
	approval.ActorRef = stringPointer(actorRef)
	return approval, nil
}
