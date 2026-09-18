package runtime

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"content-agent/backend/internal/capability"
)

const agentTaskSelect = `
	SELECT agent_task_id, workspace_id, project_id, conversation_id,
		skill_invocation_id, proposed_action_id, capability_id, capability_version,
		status, progress_current, progress_total, progress_message, input_json,
		config_json, result_artifact_id, result_artifact_version_id, result_json,
		failure_code, failure_message, attempt_count, max_attempts, cancel_requested,
		created_at, queued_at, started_at, completed_at, updated_at, input_pause_requested,
		(SELECT i.user_id FROM skill_invocations i WHERE i.skill_invocation_id=agent_tasks.skill_invocation_id)
	FROM agent_tasks`

const agentTaskAttemptSelect = `
	SELECT agent_task_attempt_id, agent_task_id, attempt_no, worker_id, provider_id,
		model_id, input_snapshot_hash, status, lease_until, result_json, usage_json,
		trace_ref, error_code, error_message, started_at, ended_at
	FROM agent_task_attempts`

func (s *Store) GetAgentTask(ctx context.Context, agentTaskID string) (AgentTask, error) {
	task, err := scanAgentTask(s.db.QueryRowContext(ctx, agentTaskSelect+`
		WHERE agent_task_id = ?`, agentTaskID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTask{}, domainError("AGENT_TASK_NOT_FOUND", "后台任务不存在。")
	}
	if err == nil {
		err = projectAgentTaskRetry(ctx, s.db, &task)
	}
	if err == nil {
		task.AdditionalInputs, err = loadAgentTaskInputs(ctx, s.db, task.AgentTaskID)
	}
	return task, err
}

func (s *Store) ListAgentTasks(ctx context.Context, projectID string) ([]AgentTask, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, agentTaskSelect+`
		WHERE project_id = ? ORDER BY created_at DESC, agent_task_id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AgentTask, 0)
	for rows.Next() {
		task, scanErr := scanAgentTask(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range result {
		if err := projectAgentTaskRetry(ctx, s.db, &result[index]); err != nil {
			return nil, err
		}
		result[index].AdditionalInputs, err = loadAgentTaskInputs(ctx, s.db, result[index].AgentTaskID)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Store) ListAgentTaskAttempts(ctx context.Context, agentTaskID string) ([]AgentTaskAttempt, error) {
	if _, err := s.GetAgentTask(ctx, agentTaskID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, agentTaskAttemptSelect+`
		WHERE agent_task_id = ? ORDER BY attempt_no ASC`, agentTaskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AgentTaskAttempt, 0)
	for rows.Next() {
		attempt, scanErr := scanAgentTaskAttempt(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, attempt)
	}
	return result, rows.Err()
}

func (s *Store) StartAgentTask(ctx context.Context, command StartAgentTaskCommand) (AgentTask, error) {
	selected, exists, err := s.capabilityEntryForProposedAction(ctx, command.ProjectID, command.ProposedActionID, command.CapabilityID, command.CapabilityVersion)
	if err != nil {
		return AgentTask{}, err
	}
	entry, err := validateBackgroundTaskEntry(selected, exists, command.CapabilityVersion)
	if err != nil {
		return AgentTask{}, err
	}
	normalizedInput, err := normalizeJSONObject(command.Input, "CAPABILITY_INPUT_INVALID", "后台任务输入必须是 JSON 对象。")
	if err != nil {
		return AgentTask{}, err
	}
	normalizedConfig, err := normalizeJSONObject(command.Config, "CAPABILITY_CONFIG_INVALID", "后台任务配置必须是 JSON 对象。")
	if err != nil {
		return AgentTask{}, err
	}
	if command.ProposedActionID == "" || command.ProposedActionVersion < 1 {
		return AgentTask{}, domainError("REQUIRED_CONFIRMATION_MISSING", "后台任务缺少待执行动作。")
	}

	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTask{}, err
	}
	defer tx.Rollback()
	proposal, err := s.authorizeProposedActionStartTx(ctx, tx, command.ProjectID, command.ConversationID, command.ProposedActionID, command.CapabilityID, command.CapabilityVersion, command.Scope)
	if err != nil {
		return AgentTask{}, err
	}
	if command.Scope == "" {
		command.Scope = command.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return AgentTask{}, err
	}
	if hit {
		return decodeProposedTaskReceipt(cached, proposal)
	}
	action, invocation, err := s.validateStartAgentTaskProposalTx(
		ctx, tx, command, normalizedInput, normalizedConfig,
	)
	if err != nil {
		return AgentTask{}, err
	}
	task, err := s.startAgentTaskTx(ctx, tx, entry, invocation, action, normalizedInput, normalizedConfig, now)
	if err != nil {
		return AgentTask{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, task, now); err != nil {
		return AgentTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTask{}, err
	}
	return task, nil
}

func (s *Store) backgroundTaskEntry(
	ctx context.Context, projectID, capabilityID, version string,
) (capability.Entry, error) {
	entry, ok, err := s.capabilityEntryForProjectVersion(ctx, projectID, capabilityID, version)
	if err != nil {
		return capability.Entry{}, err
	}
	return validateBackgroundTaskEntry(entry, ok, version)
}

func (s *Store) backgroundTaskEntryForProjectQuery(
	ctx context.Context,
	query projectWorkspaceQuery,
	projectID, capabilityID, version string,
) (capability.Entry, error) {
	entry, ok, err := s.capabilityEntryForProjectVersionQuery(
		ctx, query, projectID, capabilityID, version,
	)
	if err != nil {
		return capability.Entry{}, err
	}
	return validateBackgroundTaskEntry(entry, ok, version)
}

func validateBackgroundTaskEntry(entry capability.Entry, ok bool, version string) (capability.Entry, error) {
	if !ok || entry.Status != capability.Available || entry.Definition == nil {
		return capability.Entry{}, domainError("CAPABILITY_UNAVAILABLE", "请求的 Skill 当前不可用。")
	}
	if entry.Definition.Version != version {
		return capability.Entry{}, domainError("CAPABILITY_VERSION_UNAVAILABLE", "请求的 Skill 版本当前不可用。")
	}
	if entry.Definition.ExecutionMode != "background_task" {
		return capability.Entry{}, domainError("CAPABILITY_EXECUTION_MODE_INVALID", "只有后台 Skill 可以创建 Task。")
	}
	return entry, nil
}

func normalizeJSONObject(raw json.RawMessage, code, message string) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	normalized, err := normalizeJSON(raw)
	if err != nil || !jsonObject(normalized) {
		return nil, domainError(code, message)
	}
	return json.RawMessage(normalized), nil
}

func (s *Store) validateStartAgentTaskProposalTx(
	ctx context.Context,
	tx *sql.Tx,
	command StartAgentTaskCommand,
	input json.RawMessage,
	config json.RawMessage,
) (*ProposedAction, SkillInvocation, error) {
	action, err := scanProposedAction(tx.QueryRowContext(ctx, `
		SELECT proposed_action_id, agent_decision_id, project_id, conversation_id,
			confirmation_message_id, action_type, version, status, capability_id,
			capability_version, input_json, config_json, snapshot_hash,
			requires_confirmation, consumed_run_id, consumed_task_id, created_at, updated_at
		FROM proposed_actions WHERE proposed_action_id = ?`, command.ProposedActionID))
	if err != nil {
		return nil, SkillInvocation{}, err
	}
	if action.ProjectID != command.ProjectID || action.ConversationID != command.ConversationID {
		return nil, SkillInvocation{}, domainError("RESOURCE_PROJECT_MISMATCH", "后台任务动作不属于当前作品和对话。")
	}
	if action.ActionType != "start_background_task" || action.CapabilityRef == nil ||
		action.CapabilityRef.CapabilityID != command.CapabilityID ||
		action.CapabilityRef.Version != command.CapabilityVersion {
		return nil, SkillInvocation{}, domainError("CONFIRMATION_ACTION_MISMATCH", "该动作不能启动请求的后台 Skill。")
	}
	if action.Status != "pending" || action.Version != command.ProposedActionVersion {
		return nil, SkillInvocation{}, domainError("CONFIRMATION_STALE", "后台任务动作已失效，请重新发起。")
	}
	if string(action.Input) != string(input) || string(action.Config) != string(config) {
		return nil, SkillInvocation{}, domainError("CONFIRMATION_SNAPSHOT_MISMATCH", "后台任务输入或配置已变化。")
	}
	if action.RequiresConfirmation {
		if !command.Confirmed || command.ConfirmationMessageID != action.ConfirmationMessageID ||
			command.ConfirmationSnapshotHash != action.SnapshotHash {
			return nil, SkillInvocation{}, domainError("REQUIRED_CONFIRMATION_MISSING", "后台任务需要用户确认。")
		}
	}
	invocation, err := scanSkillInvocation(tx.QueryRowContext(ctx, skillInvocationSelect+`
		WHERE proposed_action_id = ?`, action.ProposedActionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, SkillInvocation{}, domainError("SKILL_INVOCATION_NOT_FOUND", "后台任务缺少 Skill 调用记录。")
	}
	if err != nil {
		return nil, SkillInvocation{}, err
	}
	if invocation.ExecutionMode != "background_task" || invocation.Status != "awaiting_confirmation" ||
		invocation.CapabilityID != command.CapabilityID || invocation.CapabilityVersion != command.CapabilityVersion {
		return nil, SkillInvocation{}, domainError("SKILL_INVOCATION_STATE_INVALID", "Skill 调用状态不能启动后台任务。")
	}
	return &action, invocation, nil
}

func (s *Store) startAgentTaskTx(
	ctx context.Context,
	tx *sql.Tx,
	entry capability.Entry,
	invocation SkillInvocation,
	action *ProposedAction,
	input json.RawMessage,
	config json.RawMessage,
	now time.Time,
) (AgentTask, error) {
	var workspaceID, conversationProjectID string
	if err := tx.QueryRowContext(ctx, `
		SELECT p.workspace_id, c.project_id
		FROM projects p JOIN conversations c ON c.project_id = p.project_id
		WHERE p.project_id = ? AND c.conversation_id = ? AND p.deleted_at IS NULL`,
		invocation.ProjectID, invocation.ConversationID,
	).Scan(&workspaceID, &conversationProjectID); errors.Is(err, sql.ErrNoRows) {
		return AgentTask{}, domainError("CONVERSATION_NOT_FOUND", "对话不存在。")
	} else if err != nil {
		return AgentTask{}, err
	}
	if conversationProjectID != invocation.ProjectID {
		return AgentTask{}, domainError("RESOURCE_PROJECT_MISMATCH", "对话不属于当前作品。")
	}
	var existingID string
	if err := tx.QueryRowContext(ctx, `
		SELECT agent_task_id FROM agent_tasks WHERE skill_invocation_id = ?`,
		invocation.SkillInvocationID,
	).Scan(&existingID); err == nil {
		return scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id = ?`, existingID))
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AgentTask{}, err
	}

	skillName := entry.Definition.Label
	skillDescription := entry.Definition.Description
	skillInstructions := entry.Definition.Description
	skillContentHash := "definition:" + sha256Hex([]byte(entry.Definition.ID+"@"+entry.Definition.Version))
	if entry.Skill != nil {
		skillName = entry.Skill.Name
		skillDescription = entry.Skill.Description
		skillInstructions = entry.Skill.Instructions
		skillContentHash = entry.Skill.ContentHash
	}
	taskID := s.newID("agt")
	var proposedActionID any
	if action != nil {
		proposedActionID = action.ProposedActionID
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_tasks(
			agent_task_id, workspace_id, project_id, conversation_id,
			skill_invocation_id, proposed_action_id, capability_id, capability_version,
			skill_name, skill_description, skill_instructions, skill_content_hash,
			status, progress_current, progress_total, progress_message, input_json,
			config_json, attempt_count, max_attempts, cancel_requested,
			created_at, queued_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', 0, 1, '', ?, ?, 0, 3, 0, ?, ?, ?)`,
		taskID, workspaceID, invocation.ProjectID, invocation.ConversationID,
		invocation.SkillInvocationID, proposedActionID, invocation.CapabilityID,
		invocation.CapabilityVersion, skillName, skillDescription, skillInstructions,
		skillContentHash, string(input), string(config), formatTime(now), formatTime(now), formatTime(now),
	); err != nil {
		return AgentTask{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE skill_invocations
		SET status = 'delegated_to_task', agent_task_id = ?, updated_at = ?
		WHERE skill_invocation_id = ? AND execution_mode = 'background_task'
			AND status IN ('awaiting_confirmation', 'queued')`,
		taskID, formatTime(now), invocation.SkillInvocationID,
	); err != nil {
		return AgentTask{}, err
	}
	if action != nil {
		result, err := tx.ExecContext(ctx, `
			UPDATE proposed_actions
			SET status = 'consumed', consumed_task_id = ?, updated_at = ?
			WHERE proposed_action_id = ? AND status = 'pending' AND consumed_run_id IS NULL
				AND consumed_task_id IS NULL`,
			taskID, formatTime(now), action.ProposedActionID,
		)
		if err != nil {
			return AgentTask{}, err
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
			return AgentTask{}, rowsErr
		} else if affected != 1 {
			return AgentTask{}, domainError("CONFIRMATION_STALE", "后台任务动作已被使用。")
		}
		if _, err := s.appendEvent(ctx, tx, invocation.ProjectID, nil, nil,
			"proposed_action.consumed", "proposed_action", action.ProposedActionID,
			map[string]any{"agent_task_id": taskID}); err != nil {
			return AgentTask{}, err
		}
	}
	if _, err := s.appendEvent(ctx, tx, invocation.ProjectID, nil, nil,
		"agent_task.queued", "agent_task", taskID,
		map[string]any{"skill_invocation_id": invocation.SkillInvocationID}); err != nil {
		return AgentTask{}, err
	}
	return scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id = ?`, taskID))
}

func (s *Store) ClaimAgentTask(ctx context.Context, command ClaimAgentTaskCommand) (*AgentTaskClaim, error) {
	if strings.TrimSpace(command.WorkerID) == "" || strings.TrimSpace(command.ProviderID) == "" ||
		strings.TrimSpace(command.ModelID) == "" {
		return nil, domainError("REQUEST_VALIDATION_FAILED", "后台任务领取缺少 Worker、Provider 或模型。")
	}
	if command.LeaseSeconds == 0 {
		command.LeaseSeconds = defaultAttemptLeaseSeconds
	}
	if command.LeaseSeconds < minAttemptLeaseSeconds || command.LeaseSeconds > maxAttemptLeaseSeconds {
		return nil, domainError("REQUEST_VALIDATION_FAILED", "后台任务 Lease 时长不在允许范围内。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.requeueExpiredAgentTaskAttemptsTx(ctx, tx, now); err != nil {
		return nil, err
	}

	var taskID, skillName, description, instructions, requestContent string
	err = tx.QueryRowContext(ctx, `
		SELECT at.agent_task_id, at.skill_name, at.skill_description,
			at.skill_instructions, m.content
		FROM agent_tasks at
		JOIN skill_invocations si ON si.skill_invocation_id = at.skill_invocation_id
		JOIN messages m ON m.message_id = si.user_message_id
		JOIN projects p ON p.project_id = at.project_id
		WHERE at.status = 'queued' AND at.cancel_requested = 0 AND p.deleted_at IS NULL
		ORDER BY at.queued_at ASC, at.agent_task_id ASC
		LIMIT 1`).Scan(&taskID, &skillName, &description, &instructions, &requestContent)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	task, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id = ?`, taskID))
	if err != nil {
		return nil, err
	}
	if resumed, err := s.claimAgentTaskResumeTx(ctx, tx, task, command, now); err != nil {
		if isDomainErrorCode(err, "AGENT_RUN_STATE_CORRUPT") || isDomainErrorCode(err, "AGENT_RUN_STATE_APPROVAL_MISMATCH") {
			if err := s.failAgentTaskResumeTx(ctx, tx, task, now); err != nil {
				return nil, err
			}
			return nil, tx.Commit()
		}
		return nil, err
	} else if resumed != nil {
		resumed.SkillName, resumed.Description, resumed.Instructions, resumed.RequestContent = skillName, description, instructions, requestContent
		if err := bindAgentTaskInputsTx(ctx, tx, resumed); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return resumed, nil
	}
	attemptID := s.newID("agatm")
	attemptToken := s.newID("agtok")
	tokenHash := sha256Hex([]byte(attemptToken))
	inputSnapshotHash := sha256Hex([]byte(fmt.Sprintf(
		"%s\n%s\n%s\n%s@%s\n%d",
		task.Input, task.Config, instructions, task.CapabilityID, task.CapabilityVersion, task.AttemptCount+1,
	)))
	leaseUntil := now.Add(time.Duration(command.LeaseSeconds) * time.Second)
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_tasks
		SET status = 'running', attempt_count = attempt_count + 1,
			started_at = COALESCE(started_at, ?), updated_at = ?
		WHERE agent_task_id = ? AND status = 'queued' AND cancel_requested = 0`,
		formatTime(now), formatTime(now), taskID,
	)
	if err != nil {
		return nil, err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if affected != 1 {
		return nil, domainError("AGENT_TASK_CLAIM_CONFLICT", "后台任务已被其他 Worker 领取。")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_task_attempts(
			agent_task_attempt_id, agent_task_id, attempt_no, worker_id, provider_id,
			model_id, input_snapshot_hash, status, token_hash, lease_until,
			usage_json, started_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, 'running', ?, ?, '{}', ?)`,
		attemptID, taskID, task.AttemptCount+1, command.WorkerID, command.ProviderID,
		command.ModelID, inputSnapshotHash, tokenHash, formatTime(leaseUntil), formatTime(now),
	); err != nil {
		return nil, err
	}
	if _, err := s.appendEvent(ctx, tx, task.ProjectID, nil, nil,
		"agent_task.started", "agent_task", taskID,
		map[string]any{"attempt_id": attemptID, "attempt_no": task.AttemptCount + 1}); err != nil {
		return nil, err
	}
	claimedTask, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id = ?`, taskID))
	if err != nil {
		return nil, err
	}
	attempt, err := scanAgentTaskAttempt(tx.QueryRowContext(ctx, agentTaskAttemptSelect+`
		WHERE agent_task_attempt_id = ?`, attemptID))
	if err != nil {
		return nil, err
	}
	claim := &AgentTaskClaim{
		Task: claimedTask, Attempt: attempt, AttemptToken: attemptToken,
		SkillName: skillName, Description: description, Instructions: instructions,
		RequestContent: requestContent,
	}
	if err := bindAgentTaskInputsTx(ctx, tx, claim); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claim, nil
}

func (s *Store) UpdateAgentTaskProgress(
	ctx context.Context,
	command UpdateAgentTaskProgressCommand,
) (AgentTask, error) {
	if command.LeaseSeconds != 0 && (command.LeaseSeconds < minAttemptLeaseSeconds || command.LeaseSeconds > maxAttemptLeaseSeconds) {
		return AgentTask{}, domainError("REQUEST_VALIDATION_FAILED", "后台任务 Lease 时长不在允许范围内。")
	}
	if command.AgentTaskAttemptID == "" || command.AttemptToken == "" || command.Total < 1 ||
		command.Current < 0 || command.Current > command.Total || len(command.Message) > 500 {
		return AgentTask{}, domainError("REQUEST_VALIDATION_FAILED", "后台任务进度请求无效。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTask{}, err
	}
	defer tx.Rollback()
	state, err := loadAgentTaskAttemptStateTx(ctx, tx, command.AgentTaskAttemptID)
	if err != nil {
		return AgentTask{}, err
	}
	if err := validateActiveAgentTaskAttempt(state, command.AttemptToken, now); err != nil {
		return AgentTask{}, err
	}
	if command.LeaseSeconds > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_task_attempts SET lease_until = ? WHERE agent_task_attempt_id = ? AND status = 'running'`,
			formatTime(now.Add(time.Duration(command.LeaseSeconds)*time.Second)), command.AgentTaskAttemptID); err != nil {
			return AgentTask{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_tasks
		SET progress_current = ?, progress_total = ?, progress_message = CASE WHEN status='pausing' THEN '等待当前轮完成后暂停' ELSE ? END, updated_at = ?
		WHERE agent_task_id = ? AND status IN ('running','pausing')`,
		command.Current, command.Total, strings.TrimSpace(command.Message), formatTime(now), state.TaskID,
	); err != nil {
		return AgentTask{}, err
	}
	if _, err := s.appendEvent(ctx, tx, state.ProjectID, nil, nil,
		"agent_task.progressed", "agent_task", state.TaskID,
		map[string]any{"current": command.Current, "total": command.Total, "message": strings.TrimSpace(command.Message)}); err != nil {
		return AgentTask{}, err
	}
	task, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id = ?`, state.TaskID))
	if err != nil {
		return AgentTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTask{}, err
	}
	return task, nil
}

func (s *Store) CompleteAgentTask(ctx context.Context, command CompleteAgentTaskCommand) (AgentTask, error) {
	if command.AgentTaskAttemptID == "" || command.AttemptToken == "" {
		return AgentTask{}, domainError("REQUEST_VALIDATION_FAILED", "后台任务完成请求不完整。")
	}
	result, err := normalizeJSONValue(command.Result, "后台任务结果必须是有效 JSON。")
	if err != nil {
		return AgentTask{}, err
	}
	usage, err := normalizeJSONObject(command.Usage, "REQUEST_VALIDATION_FAILED", "Usage 必须是 JSON 对象。")
	if err != nil {
		return AgentTask{}, err
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTask{}, err
	}
	defer tx.Rollback()
	state, err := loadAgentTaskAttemptStateTx(ctx, tx, command.AgentTaskAttemptID)
	if err != nil {
		return AgentTask{}, err
	}
	if err := validateActiveAgentTaskAttempt(state, command.AttemptToken, now); err != nil {
		return AgentTask{}, err
	}
	if err := validateAgentMemoryCommitTx(ctx, tx, "background_task", state.AttemptID); err != nil {
		return AgentTask{}, err
	}
	if err := validateExternalToolOutcomesTx(ctx, tx, "background_task", state.AttemptID); err != nil {
		return AgentTask{}, err
	}
	task, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id = ?`, state.TaskID))
	if err != nil {
		return AgentTask{}, err
	}
	var artifact *Artifact
	if err := includeAgentTaskInputsTx(ctx, tx, task.AgentTaskID, state.AttemptID, command.IncludedInputIDs, now); err != nil {
		return AgentTask{}, err
	}
	if command.ArtifactDraft != nil {
		artifactCommand, normalizeErr := s.normalizeGenericArtifactCommand(CreateGenericArtifactCommand{
			ProjectID: task.ProjectID, ConversationID: task.ConversationID,
			Draft: *command.ArtifactDraft, OriginType: "agent_task", OriginID: task.AgentTaskID,
			CapabilityID: task.CapabilityID, ActorRef: "agent_task:" + state.WorkerID,
		})
		if normalizeErr != nil {
			return AgentTask{}, normalizeErr
		}
		created, createErr := s.createGenericArtifactTx(ctx, tx, artifactCommand, now)
		if createErr != nil {
			return AgentTask{}, createErr
		}
		artifact = &created
	}
	traceRef := nullableStringPointer(strings.TrimSpace(command.TraceRef))
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_task_attempts
		SET status = 'completed', result_json = ?, usage_json = ?, trace_ref = ?, ended_at = ?
		WHERE agent_task_attempt_id = ? AND status = 'running'`,
		string(result), string(usage), traceRef, formatTime(now), command.AgentTaskAttemptID,
	); err != nil {
		return AgentTask{}, err
	}
	var artifactID, artifactVersionID any
	if artifact != nil {
		artifactID = artifact.ArtifactID
		artifactVersionID = artifact.CurrentVersionID
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_tasks
		SET status = 'completed', progress_current = progress_total,
			progress_message = '', result_artifact_id = ?, result_artifact_version_id = ?,
			result_json = ?, failure_code = NULL, failure_message = NULL,
			completed_at = ?, updated_at = ?
		WHERE agent_task_id = ? AND status IN ('running','pausing')`,
		artifactID, artifactVersionID, string(result), formatTime(now), formatTime(now), state.TaskID,
	); err != nil {
		return AgentTask{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE skill_invocations SET status = 'completed', updated_at = ?
		WHERE skill_invocation_id = ? AND agent_task_id = ?`,
		formatTime(now), task.SkillInvocationID, task.AgentTaskID,
	); err != nil {
		return AgentTask{}, err
	}
	payload := map[string]any{"attempt_id": command.AgentTaskAttemptID}
	if artifact != nil {
		payload["artifact_id"] = artifact.ArtifactID
		payload["artifact_version_id"] = artifact.CurrentVersionID
	}
	if _, err := s.appendEvent(ctx, tx, task.ProjectID, nil, nil,
		"agent_task.completed", "agent_task", task.AgentTaskID, payload); err != nil {
		return AgentTask{}, err
	}
	completed, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id = ?`, task.AgentTaskID))
	if err != nil {
		return AgentTask{}, err
	}
	if err := deleteAgentTaskRunStateTx(ctx, tx, task.AgentTaskID); err != nil {
		return AgentTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTask{}, err
	}
	return completed, nil
}

func normalizeJSONValue(raw json.RawMessage, message string) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > maxProviderResponseBytes {
		return nil, domainError("REQUEST_VALIDATION_FAILED", message)
	}
	normalized, err := normalizeJSON(raw)
	if err != nil {
		return nil, domainError("REQUEST_VALIDATION_FAILED", message)
	}
	return json.RawMessage(normalized), nil
}

func (s *Store) FailAgentTask(ctx context.Context, command FailAgentTaskCommand) (AgentTask, error) {
	if command.AgentTaskAttemptID == "" || command.AttemptToken == "" ||
		strings.TrimSpace(command.ErrorCode) == "" || strings.TrimSpace(command.ErrorMessage) == "" {
		return AgentTask{}, domainError("REQUEST_VALIDATION_FAILED", "后台任务失败请求不完整。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTask{}, err
	}
	defer tx.Rollback()
	state, err := loadAgentTaskAttemptStateTx(ctx, tx, command.AgentTaskAttemptID)
	if err != nil {
		return AgentTask{}, err
	}
	if err := validateActiveAgentTaskAttempt(state, command.AttemptToken, now); err != nil {
		return AgentTask{}, err
	}
	if err := includeAgentTaskInputsTx(ctx, tx, state.TaskID, state.AttemptID, command.IncludedInputIDs, now); err != nil {
		return AgentTask{}, err
	}
	replayRisk, err := agentTaskReplayRiskTx(ctx, tx, state.TaskID, state.AttemptID)
	if err != nil {
		return AgentTask{}, err
	}
	if replayRisk {
		command.Retryable = false
		command.ErrorCode = "AGENT_TASK_REPLAY_UNSAFE"
		command.ErrorMessage = "审批恢复后的执行中断，或工具可能已产生外部操作。请核对外部状态后再重新执行。"
	}
	if err := cancelAgentTaskToolsTx(ctx, tx, state.TaskID, now); err != nil {
		return AgentTask{}, err
	}
	if err := deleteAgentTaskRunStateTx(ctx, tx, state.TaskID); err != nil {
		return AgentTask{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_task_attempts
		SET status = 'failed', error_code = ?, error_message = ?, ended_at = ?
		WHERE agent_task_attempt_id = ? AND status = 'running'`,
		strings.TrimSpace(command.ErrorCode), strings.TrimSpace(command.ErrorMessage),
		formatTime(now), command.AgentTaskAttemptID,
	); err != nil {
		return AgentTask{}, err
	}
	retry := command.Retryable && state.AttemptCount < state.MaxAttempts && state.TaskStatus != "pausing"
	status := "failed"
	completedAt := any(formatTime(now))
	eventType := "agent_task.failed"
	if retry {
		status = "queued"
		completedAt = nil
		eventType = "agent_task.retry_scheduled"
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_tasks
		SET status = ?, failure_code = ?, failure_message = ?, queued_at = ?,
			completed_at = ?, updated_at = ?
		WHERE agent_task_id = ? AND status IN ('running','pausing')`,
		status, strings.TrimSpace(command.ErrorCode), strings.TrimSpace(command.ErrorMessage),
		formatTime(now), completedAt, formatTime(now), state.TaskID,
	); err != nil {
		return AgentTask{}, err
	}
	if !retry {
		if _, err := tx.ExecContext(ctx, `
			UPDATE skill_invocations SET status = 'failed', updated_at = ?
			WHERE skill_invocation_id = ? AND agent_task_id = ?`,
			formatTime(now), state.SkillInvocationID, state.TaskID,
		); err != nil {
			return AgentTask{}, err
		}
	}
	if _, err := s.appendEvent(ctx, tx, state.ProjectID, nil, nil,
		eventType, "agent_task", state.TaskID,
		map[string]any{"attempt_id": command.AgentTaskAttemptID, "error_code": strings.TrimSpace(command.ErrorCode)}); err != nil {
		return AgentTask{}, err
	}
	task, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id = ?`, state.TaskID))
	if err != nil {
		return AgentTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTask{}, err
	}
	return task, nil
}

func (s *Store) CancelAgentTask(ctx context.Context, command CancelAgentTaskCommand) (AgentTask, error) {
	if command.AgentTaskID == "" {
		return AgentTask{}, domainError("REQUEST_VALIDATION_FAILED", "缺少后台任务 ID。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTask{}, err
	}
	defer tx.Rollback()
	task, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id = ?`, command.AgentTaskID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTask{}, domainError("AGENT_TASK_NOT_FOUND", "后台任务不存在。")
	}
	if err != nil {
		return AgentTask{}, err
	}
	if command.Scope == "" {
		command.Scope = task.ProjectID
	}
	cached, hit, err := s.beginExecutionControlCommand(ctx, tx, command.CommandMeta, task.ProjectID, "agent_task", task.AgentTaskID)
	if err != nil {
		return AgentTask{}, err
	}
	if hit {
		return decodeIdempotentResult[AgentTask](cached)
	}
	if task.Status == "completed" || task.Status == "cancelled" {
		return AgentTask{}, domainError("AGENT_TASK_TERMINAL", "后台任务已经结束。")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_task_attempts
		SET status = 'cancelled', error_code = 'AGENT_TASK_CANCELLED',
			error_message = 'cancelled by user', ended_at = ?
		WHERE agent_task_id = ? AND status IN ('running', 'waiting_approval', 'paused')`,
		formatTime(now), task.AgentTaskID,
	); err != nil {
		return AgentTask{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_tasks
		SET status = 'cancelled', cancel_requested = 1, failure_code = 'AGENT_TASK_CANCELLED',
			failure_message = 'cancelled by user', completed_at = ?, updated_at = ?
		WHERE agent_task_id = ? AND status IN ('queued', 'running', 'waiting_approval', 'pausing', 'paused', 'failed')`,
		formatTime(now), formatTime(now), task.AgentTaskID,
	); err != nil {
		return AgentTask{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE skill_invocations SET status = 'cancelled', updated_at = ?
		WHERE skill_invocation_id = ? AND agent_task_id = ?`,
		formatTime(now), task.SkillInvocationID, task.AgentTaskID,
	); err != nil {
		return AgentTask{}, err
	}
	if _, err := s.appendEvent(ctx, tx, task.ProjectID, nil, nil,
		"agent_task.cancelled", "agent_task", task.AgentTaskID,
		map[string]any{"actor_ref": strings.TrimSpace(command.ActorRef)}); err != nil {
		return AgentTask{}, err
	}
	cancelled, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id = ?`, task.AgentTaskID))
	if err != nil {
		return AgentTask{}, err
	}
	if err := cancelAgentTaskToolsTx(ctx, tx, task.AgentTaskID, now); err != nil {
		return AgentTask{}, err
	}
	if err := deleteAgentTaskRunStateTx(ctx, tx, task.AgentTaskID); err != nil {
		return AgentTask{}, err
	}
	if err := projectAgentTaskRetry(ctx, tx, &cancelled); err != nil {
		return AgentTask{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, cancelled, now); err != nil {
		return AgentTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTask{}, err
	}
	return cancelled, nil
}

func (s *Store) RetryAgentTask(ctx context.Context, command RetryAgentTaskCommand) (AgentTask, error) {
	if command.AgentTaskID == "" {
		return AgentTask{}, domainError("REQUEST_VALIDATION_FAILED", "缺少后台任务 ID。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTask{}, err
	}
	defer tx.Rollback()
	task, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id = ?`, command.AgentTaskID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTask{}, domainError("AGENT_TASK_NOT_FOUND", "后台任务不存在。")
	}
	if err != nil {
		return AgentTask{}, err
	}
	if command.Scope == "" {
		command.Scope = task.ProjectID
	}
	entry, exists, err := s.capabilityEntryForInvocationQuery(ctx, tx, task.SkillInvocationID)
	if err != nil {
		return AgentTask{}, err
	}
	if _, err := validateBackgroundTaskEntry(entry, exists, task.CapabilityVersion); err != nil {
		return AgentTask{}, err
	}
	if err := authorizePersonalSkillExecution(ctx, entry); err != nil {
		return AgentTask{}, err
	}
	cached, hit, err := s.beginExecutionControlCommand(ctx, tx, command.CommandMeta, task.ProjectID, "agent_task", task.AgentTaskID)
	if err != nil {
		return AgentTask{}, err
	}
	if hit {
		return decodeIdempotentResult[AgentTask](cached)
	}
	if task.Status != "failed" && task.Status != "cancelled" {
		return AgentTask{}, domainError("AGENT_TASK_RETRY_INVALID", "只有失败或已取消的后台任务可以重试。")
	}
	if risk, err := agentTaskReplayRisk(ctx, tx, task.AgentTaskID); err != nil {
		return AgentTask{}, err
	} else if risk {
		return AgentTask{}, domainError("SDK_TOOL_REPLAY_RISK", "后台任务已有写入调用或 SDK checkpoint，不能从头重试；请先核对已有结果，避免重复执行。")
	}
	if err := cancelAgentTaskToolsTx(ctx, tx, task.AgentTaskID, now); err != nil {
		return AgentTask{}, err
	}
	if err := deleteAgentTaskRunStateTx(ctx, tx, task.AgentTaskID); err != nil {
		return AgentTask{}, err
	}
	newMaxAttempts := task.MaxAttempts
	if newMaxAttempts <= task.AttemptCount {
		newMaxAttempts = task.AttemptCount + 1
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_tasks
		SET status = 'queued', max_attempts = ?, cancel_requested = 0,
			failure_code = NULL, failure_message = NULL, completed_at = NULL,
			queued_at = ?, updated_at = ?
		WHERE agent_task_id = ? AND status IN ('failed', 'cancelled')`,
		newMaxAttempts, formatTime(now), formatTime(now), task.AgentTaskID,
	); err != nil {
		return AgentTask{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE skill_invocations SET status = 'delegated_to_task', updated_at = ?
		WHERE skill_invocation_id = ? AND agent_task_id = ?`,
		formatTime(now), task.SkillInvocationID, task.AgentTaskID,
	); err != nil {
		return AgentTask{}, err
	}
	if _, err := s.appendEvent(ctx, tx, task.ProjectID, nil, nil,
		"agent_task.retry_scheduled", "agent_task", task.AgentTaskID,
		map[string]any{"actor_ref": strings.TrimSpace(command.ActorRef), "manual": true}); err != nil {
		return AgentTask{}, err
	}
	retried, err := scanAgentTask(tx.QueryRowContext(ctx, agentTaskSelect+` WHERE agent_task_id = ?`, task.AgentTaskID))
	if err != nil {
		return AgentTask{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, retried, now); err != nil {
		return AgentTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentTask{}, err
	}
	return retried, nil
}

type agentTaskAttemptState struct {
	AttemptID         string
	TaskID            string
	SkillInvocationID string
	ProjectID         string
	WorkerID          string
	TokenHash         string
	AttemptStatus     string
	TaskStatus        string
	LeaseUntil        time.Time
	AttemptCount      int
	MaxAttempts       int
	CancelRequested   bool
	ProjectDeleted    bool
}

func loadAgentTaskAttemptStateTx(
	ctx context.Context,
	tx *sql.Tx,
	attemptID string,
) (agentTaskAttemptState, error) {
	var state agentTaskAttemptState
	var leaseUntil string
	var cancelRequested int
	err := tx.QueryRowContext(ctx, `
		SELECT ata.agent_task_attempt_id, ata.agent_task_id, at.skill_invocation_id,
			at.project_id, ata.worker_id, ata.token_hash, ata.status, at.status,
			ata.lease_until, at.attempt_count, at.max_attempts, at.cancel_requested, p.deleted_at IS NOT NULL
		FROM agent_task_attempts ata
		JOIN agent_tasks at ON at.agent_task_id = ata.agent_task_id
		JOIN projects p ON p.project_id = at.project_id
		WHERE ata.agent_task_attempt_id = ?`, attemptID,
	).Scan(
		&state.AttemptID, &state.TaskID, &state.SkillInvocationID, &state.ProjectID,
		&state.WorkerID, &state.TokenHash, &state.AttemptStatus, &state.TaskStatus,
		&leaseUntil, &state.AttemptCount, &state.MaxAttempts, &cancelRequested, &state.ProjectDeleted,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return state, domainError("AGENT_TASK_ATTEMPT_NOT_FOUND", "后台任务执行尝试不存在。")
	}
	if err != nil {
		return state, err
	}
	state.CancelRequested = cancelRequested != 0
	state.LeaseUntil, err = parseTime(leaseUntil)
	return state, err
}

func validateActiveAgentTaskAttempt(state agentTaskAttemptState, token string, now time.Time) error {
	if subtle.ConstantTimeCompare([]byte(state.TokenHash), []byte(sha256Hex([]byte(token)))) != 1 {
		return domainError("ATTEMPT_TOKEN_INVALID", "后台任务执行 Token 无效。")
	}
	if state.CancelRequested || state.TaskStatus == "cancelled" || state.ProjectDeleted {
		return domainError("AGENT_TASK_CANCELLED", "后台任务已取消。")
	}
	if state.AttemptStatus != "running" || (state.TaskStatus != "running" && state.TaskStatus != "pausing") {
		return domainError("AGENT_TASK_ATTEMPT_STALE", "后台任务执行尝试已失效。")
	}
	if !state.LeaseUntil.After(now) {
		return domainError("ATTEMPT_LEASE_EXPIRED", "后台任务执行 Lease 已过期。")
	}
	return nil
}

func (s *Store) requeueExpiredAgentTaskAttemptsTx(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT ata.agent_task_attempt_id, at.agent_task_id, at.skill_invocation_id,
			at.project_id, at.attempt_count, at.max_attempts, at.status
		FROM agent_task_attempts ata
		JOIN agent_tasks at ON at.agent_task_id = ata.agent_task_id
		WHERE ata.status = 'running' AND ata.lease_until <= ? AND at.status IN ('running','pausing')`,
		formatTime(now),
	)
	if err != nil {
		return err
	}
	type expired struct {
		attemptID, taskID, invocationID, projectID string
		attemptCount, maxAttempts                  int
		status                                     string
	}
	items := make([]expired, 0)
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.attemptID, &item.taskID, &item.invocationID, &item.projectID,
			&item.attemptCount, &item.maxAttempts, &item.status); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range items {
		replayRisk, err := agentTaskReplayRiskTx(ctx, tx, item.taskID, item.attemptID)
		if err != nil {
			return err
		}
		errorCode, errorMessage := "ATTEMPT_LEASE_EXPIRED", "worker lease expired"
		if item.status == "pausing" {
			errorCode, errorMessage = "AGENT_TASK_PAUSE_INTERRUPTED", "暂停前 Worker 已失联，未取得可恢复状态。请核对已有结果后处理。"
		}
		if replayRisk {
			errorCode, errorMessage = "AGENT_TASK_REPLAY_UNSAFE", "审批恢复后的执行中断，或工具可能已产生外部操作。请核对外部状态后再重新执行。"
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_task_attempts
			SET status = 'expired', error_code = ?,
				error_message = ?, ended_at = ?
			WHERE agent_task_attempt_id = ? AND status = 'running'`,
			errorCode, errorMessage, formatTime(now), item.attemptID,
		); err != nil {
			return err
		}
		status := "queued"
		eventType := "agent_task.retry_scheduled"
		completedAt := any(nil)
		if item.attemptCount >= item.maxAttempts || replayRisk || item.status == "pausing" {
			status = "failed"
			eventType = "agent_task.failed"
			completedAt = formatTime(now)
			if _, err := tx.ExecContext(ctx, `
				UPDATE skill_invocations SET status = 'failed', updated_at = ?
				WHERE skill_invocation_id = ? AND agent_task_id = ?`,
				formatTime(now), item.invocationID, item.taskID,
			); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_tasks
			SET status = ?, failure_code = ?,
				failure_message = ?, queued_at = ?, completed_at = ?, updated_at = ?
			WHERE agent_task_id = ? AND status IN ('running','pausing')`,
			status, errorCode, errorMessage, formatTime(now), completedAt, formatTime(now), item.taskID,
		); err != nil {
			return err
		}
		if _, err := s.appendEvent(ctx, tx, item.projectID, nil, nil,
			eventType, "agent_task", item.taskID,
			map[string]any{"attempt_id": item.attemptID, "error_code": errorCode}); err != nil {
			return err
		}
		if err := cancelAgentTaskToolsTx(ctx, tx, item.taskID, now); err != nil {
			return err
		}
		if err := deleteAgentTaskRunStateTx(ctx, tx, item.taskID); err != nil {
			return err
		}
	}
	return nil
}

const skillInvocationSelect = `
	SELECT skill_invocation_id, project_id, conversation_id, user_message_id,
		agent_message_id, capability_id, capability_version, execution_mode, status,
		proposed_action_id, agent_task_id, run_id, created_at, updated_at
	FROM skill_invocations`

func scanSkillInvocation(row rowScanner) (SkillInvocation, error) {
	var invocation SkillInvocation
	var proposedActionID, agentTaskID, runID sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(
		&invocation.SkillInvocationID, &invocation.ProjectID, &invocation.ConversationID,
		&invocation.UserMessageID, &invocation.AgentMessageID, &invocation.CapabilityID,
		&invocation.CapabilityVersion, &invocation.ExecutionMode, &invocation.Status,
		&proposedActionID, &agentTaskID, &runID, &createdAt, &updatedAt,
	); err != nil {
		return SkillInvocation{}, err
	}
	invocation.ProposedActionID = stringPointer(proposedActionID)
	invocation.AgentTaskID = stringPointer(agentTaskID)
	invocation.RunID = stringPointer(runID)
	var err error
	invocation.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return SkillInvocation{}, err
	}
	invocation.UpdatedAt, err = parseTime(updatedAt)
	return invocation, err
}

func scanAgentTask(row rowScanner) (AgentTask, error) {
	var task AgentTask
	var proposedActionID, resultArtifactID, resultArtifactVersionID sql.NullString
	var resultJSON, failureCode, failureMessage sql.NullString
	var startedAt, completedAt sql.NullString
	var inputJSON, configJSON, createdAt, queuedAt, updatedAt string
	var cancelRequested int
	if err := row.Scan(
		&task.AgentTaskID, &task.WorkspaceID, &task.ProjectID, &task.ConversationID,
		&task.SkillInvocationID, &proposedActionID, &task.CapabilityID, &task.CapabilityVersion,
		&task.Status, &task.ProgressCurrent, &task.ProgressTotal, &task.ProgressMessage,
		&inputJSON, &configJSON, &resultArtifactID, &resultArtifactVersionID,
		&resultJSON, &failureCode, &failureMessage, &task.AttemptCount, &task.MaxAttempts,
		&cancelRequested, &createdAt, &queuedAt, &startedAt, &completedAt, &updatedAt,
		&task.InputPauseRequested,
		&task.UserID,
	); err != nil {
		return AgentTask{}, err
	}
	task.ProposedActionID = stringPointer(proposedActionID)
	task.Input = json.RawMessage(inputJSON)
	task.Config = json.RawMessage(configJSON)
	task.ResultArtifactID = stringPointer(resultArtifactID)
	task.ResultArtifactVersionID = stringPointer(resultArtifactVersionID)
	if resultJSON.Valid {
		task.Result = json.RawMessage(resultJSON.String)
	}
	task.FailureCode = stringPointer(failureCode)
	task.FailureMessage = stringPointer(failureMessage)
	task.CancelRequested = cancelRequested != 0
	var err error
	task.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return AgentTask{}, err
	}
	task.QueuedAt, err = parseTime(queuedAt)
	if err != nil {
		return AgentTask{}, err
	}
	task.StartedAt, err = optionalTime(startedAt)
	if err != nil {
		return AgentTask{}, err
	}
	task.CompletedAt, err = optionalTime(completedAt)
	if err != nil {
		return AgentTask{}, err
	}
	task.UpdatedAt, err = parseTime(updatedAt)
	return task, err
}

func scanAgentTaskAttempt(row rowScanner) (AgentTaskAttempt, error) {
	var attempt AgentTaskAttempt
	var resultJSON, traceRef, errorCode, errorMessage, endedAt sql.NullString
	var usageJSON, leaseUntil, startedAt string
	if err := row.Scan(
		&attempt.AgentTaskAttemptID, &attempt.AgentTaskID, &attempt.AttemptNo,
		&attempt.WorkerID, &attempt.ProviderID, &attempt.ModelID, &attempt.InputSnapshotHash,
		&attempt.Status, &leaseUntil, &resultJSON, &usageJSON, &traceRef, &errorCode,
		&errorMessage, &startedAt, &endedAt,
	); err != nil {
		return AgentTaskAttempt{}, err
	}
	if resultJSON.Valid {
		attempt.Result = json.RawMessage(resultJSON.String)
	}
	attempt.Usage = json.RawMessage(usageJSON)
	attempt.TraceRef = stringPointer(traceRef)
	attempt.ErrorCode = stringPointer(errorCode)
	attempt.ErrorMessage = stringPointer(errorMessage)
	var err error
	attempt.LeaseUntil, err = parseTime(leaseUntil)
	if err != nil {
		return AgentTaskAttempt{}, err
	}
	attempt.StartedAt, err = parseTime(startedAt)
	if err != nil {
		return AgentTaskAttempt{}, err
	}
	attempt.EndedAt, err = optionalTime(endedAt)
	return attempt, err
}
