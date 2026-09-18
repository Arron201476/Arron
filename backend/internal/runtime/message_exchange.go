package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

var allowedAgentIntents = map[string]struct{}{
	"chat":               {},
	"create_artifact":    {},
	"clarify":            {},
	"inspect":            {},
	"propose_capability": {},
	"revise":             {},
	"regenerate":         {},
	"control_run":        {},
	"unsupported":        {},
}

var allowedProposedActionTypes = map[string]struct{}{
	"collect_run_configuration": {},
	"start_run":                 {},
	"start_background_task":     {},
	"inspect_artifact":          {},
	"control_run":               {},
}

func (s *Store) CreateMessageExchange(
	ctx context.Context,
	command CreateMessageExchangeCommand,
) (MessageExchange, error) {
	if err := validateMessageEnvelope(&command.Request); err != nil {
		return MessageExchange{}, err
	}
	if err := validateAgentDecision(command.Decision); err != nil {
		return MessageExchange{}, err
	}

	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MessageExchange{}, err
	}
	defer tx.Rollback()

	var projectID string
	err = tx.QueryRowContext(ctx, `
		SELECT project_id FROM conversations WHERE conversation_id = ?`,
		command.ConversationID,
	).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageExchange{}, domainError("CONVERSATION_NOT_FOUND", "对话不存在。")
	}
	if err != nil {
		return MessageExchange{}, err
	}
	if command.Scope == "" {
		command.Scope = projectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return MessageExchange{}, err
	}
	if hit {
		return decodeIdempotentResult[MessageExchange](cached)
	}
	var activeRunID, activeRunStatus sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT p.active_write_run_id, r.status
		FROM projects p LEFT JOIN runs r ON r.run_id = p.active_write_run_id
		WHERE p.project_id = ? AND p.deleted_at IS NULL`, projectID).Scan(&activeRunID, &activeRunStatus); err != nil {
		return MessageExchange{}, err
	}
	if err := s.validateMessageRequestTx(ctx, tx, projectID, command.Request); err != nil {
		return MessageExchange{}, err
	}
	revisionInstruction := strings.TrimSpace(command.Request.Content)
	if normalized, instruction, err := s.normalizeArtifactMutationTurnTx(
		ctx, tx, command.ConversationID, command.Request, command.Decision,
	); err != nil {
		return MessageExchange{}, err
	} else {
		command.Decision = normalized
		revisionInstruction = instruction
	}
	if err := s.validateDecisionReferences(ctx, tx, projectID, command.Request, command.Decision); err != nil {
		return MessageExchange{}, err
	}
	selectedCreatesRun := false
	if command.Decision.CapabilityRef != nil {
		entry, ok, err := s.capabilityEntryForProjectQuery(
			ctx, tx, projectID, command.Decision.CapabilityRef.CapabilityID,
		)
		if err != nil {
			return MessageExchange{}, err
		}
		if ok && entry.Definition != nil {
			selectedCreatesRun = entry.Definition.ExecutionMode == "stateful_workflow"
		}
	}
	failedRunCanBeReplaced := activeRunID.Valid && activeRunStatus.String == "failed" &&
		command.Request.CapabilityRef != nil && selectedCreatesRun
	if activeRunID.Valid && selectedCreatesRun && command.Decision.Intent == "propose_capability" && !failedRunCanBeReplaced {
		command.Decision.Intent = "clarify"
		command.Decision.Reply = "当前生成任务仍在运行，暂时不能启动另一个能力。你可以继续询问当前进度、查看产物，或提交对当前产物的修改要求。"
		command.Decision.ProposedAction = nil
	}

	userMessage := Message{
		MessageID:      s.newID("msg"),
		ConversationID: command.ConversationID,
		ProjectID:      projectID,
		Role:           "user",
		Content:        command.Request.Content,
		CreatedAt:      now,
	}
	var resolution *TargetResolution
	var revision *RevisionRequest
	if command.Decision.Intent == "revise" || command.Decision.Intent == "regenerate" {
		resolved, err := s.buildTargetResolutionTx(
			ctx, tx, projectID, command.ConversationID, userMessage.MessageID,
			command.Request, command.Decision.TargetRef, now,
		)
		if err != nil {
			return MessageExchange{}, err
		}
		resolution = &resolved
		switch resolved.Status {
		case "resolved":
			if command.Decision.Intent == "regenerate" {
				command.Decision.Reply = "已定位目标范围，正在从原始材料重新处理。"
			} else {
				command.Decision.Reply = "已定位修改位置，正在准备修改稿。"
			}
			if activeRunID.Valid && command.Decision.Intent == "revise" {
				command.Decision.Reply = "修改需求已记录，将在当前步骤完成后的安全检查点处理。"
			}
		case "ambiguous":
			command.Decision.Reply = "找到多个可能的处理位置，请先确认目标产物。"
		default:
			command.Decision.Reply = "暂时无法确定处理位置，请补充产物名称、集数或先选中内容。"
		}
	}
	agentMessage := Message{
		MessageID:      s.newID("msg"),
		ConversationID: command.ConversationID,
		ProjectID:      projectID,
		Role:           "assistant",
		Content:        strings.TrimSpace(command.Decision.Reply),
		CreatedAt:      now,
	}
	for _, message := range []Message{userMessage, agentMessage} {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO messages(message_id, conversation_id, project_id, role, content, created_at)
			VALUES(?, ?, ?, ?, ?, ?)`,
			message.MessageID,
			message.ConversationID,
			message.ProjectID,
			message.Role,
			message.Content,
			formatTime(message.CreatedAt),
		); err != nil {
			return MessageExchange{}, err
		}
	}
	messageContext := MessageContext{
		CapabilityRef:     command.Request.CapabilityRef,
		AttachmentRefs:    command.Request.AttachmentRefs,
		SelectionSnapshot: command.Request.SelectionSnapshot,
		ClientContext:     command.Request.ClientContext,
	}
	routingContext, err := s.resolveMessageRoutingContextTx(
		ctx, tx, projectID, command.Request, command.Decision, resolution, activeRunID,
	)
	if err != nil {
		return MessageExchange{}, err
	}
	messageContext.RoutingContext = routingContext
	if messageContext.AttachmentRefs == nil {
		messageContext.AttachmentRefs = []agentcontract.AttachmentRef{}
	}
	userMessage.Context = &messageContext
	agentMessage.Context = &MessageContext{
		AttachmentRefs: []agentcontract.AttachmentRef{},
		RoutingContext: routingContext,
	}
	capabilityJSON, err := nullableJSON(messageContext.CapabilityRef)
	if err != nil {
		return MessageExchange{}, err
	}
	attachmentsJSON, err := json.Marshal(messageContext.AttachmentRefs)
	if err != nil {
		return MessageExchange{}, err
	}
	selectionJSON, err := nullableJSON(messageContext.SelectionSnapshot)
	if err != nil {
		return MessageExchange{}, err
	}
	clientContextJSON, err := json.Marshal(messageContext.ClientContext)
	if err != nil {
		return MessageExchange{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO message_contexts(
			message_id, capability_ref_json, attachment_refs_json,
			selection_snapshot_json, client_context_json
		) VALUES(?, ?, ?, ?, ?)`,
		userMessage.MessageID,
		capabilityJSON,
		string(attachmentsJSON),
		selectionJSON,
		string(clientContextJSON),
	); err != nil {
		return MessageExchange{}, err
	}
	for _, messageID := range []string{userMessage.MessageID, agentMessage.MessageID} {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO message_routing_contexts(message_id, scope, run_id, capability_id, artifact_id)
			VALUES(?, ?, ?, ?, ?)`,
			messageID, routingContext.Scope, routingContext.RunID, routingContext.CapabilityID, routingContext.ArtifactID,
		); err != nil {
			return MessageExchange{}, err
		}
	}
	if resolution != nil {
		if err := s.persistTargetResolutionTx(ctx, tx, *resolution); err != nil {
			return MessageExchange{}, err
		}
		if command.Decision.Intent == "revise" && (resolution.Status == "resolved" || resolution.Status == "ambiguous") {
			created, err := s.createRevisionRequestTx(
				ctx, tx, *resolution, revisionInstruction, activeRunID.Valid, now,
			)
			if err != nil {
				return MessageExchange{}, err
			}
			revision = &created
		}
	}

	decisionID := s.newID("agd")
	persistedDecision := command.Decision
	persistedDecision.ProposedAction = nil
	decisionJSON, err := json.Marshal(persistedDecision)
	if err != nil {
		return MessageExchange{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_decisions(
			agent_decision_id, project_id, conversation_id, user_message_id,
			agent_message_id, intent, confidence, decision_json, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		decisionID,
		projectID,
		command.ConversationID,
		userMessage.MessageID,
		agentMessage.MessageID,
		command.Decision.Intent,
		command.Decision.Confidence,
		string(decisionJSON),
		formatTime(now),
	); err != nil {
		return MessageExchange{}, err
	}
	decisionRecord := AgentDecisionRecord{
		AgentDecisionID: decisionID,
		ProjectID:       projectID,
		ConversationID:  command.ConversationID,
		UserMessageID:   userMessage.MessageID,
		AgentMessageID:  agentMessage.MessageID,
		Decision:        persistedDecision,
		CreatedAt:       now,
	}

	var action *ProposedAction
	if command.Decision.ProposedAction != nil {
		action, err = s.createProposedActionTx(
			ctx,
			tx,
			projectID,
			command.ConversationID,
			decisionID,
			userMessage.MessageID,
			agentMessage.MessageID,
			command.Request,
			*command.Decision.ProposedAction,
			now,
		)
		if err != nil {
			return MessageExchange{}, err
		}
	}
	var invocation *SkillInvocation
	if command.Decision.CapabilityRef != nil {
		createdInvocation, err := s.createSkillInvocationTx(
			ctx, tx, projectID, command.ConversationID, userMessage.MessageID,
			agentMessage.MessageID, *command.Decision.CapabilityRef, command.Decision.Intent, action, now,
		)
		if err != nil {
			return MessageExchange{}, err
		}
		invocation = &createdInvocation
		routingContext.Scope = "invocation"
		routingContext.InvocationID = &createdInvocation.SkillInvocationID
		routingContext.CapabilityID = &createdInvocation.CapabilityID
		messageContext.RoutingContext = routingContext
		userMessage.Context.RoutingContext = routingContext
		agentMessage.Context.RoutingContext = routingContext
		if _, err := tx.ExecContext(ctx, `
			UPDATE message_routing_contexts
			SET scope = 'invocation', invocation_id = ?, capability_id = ?
			WHERE message_id IN (?, ?)`,
			createdInvocation.SkillInvocationID, createdInvocation.CapabilityID,
			userMessage.MessageID, agentMessage.MessageID,
		); err != nil {
			return MessageExchange{}, err
		}
	}
	goal, goalChanged, err := s.applyGoalUpdateTx(
		ctx, tx, projectID, command.ConversationID, userMessage.MessageID,
		command.Decision.GoalUpdate, now,
	)
	if err != nil {
		return MessageExchange{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversations SET updated_at = ? WHERE conversation_id = ?`,
		formatTime(now), command.ConversationID,
	); err != nil {
		return MessageExchange{}, err
	}
	for _, event := range []struct {
		eventType   string
		subjectType string
		subjectID   string
	}{
		{"message.created", "message", userMessage.MessageID},
		{"message.created", "message", agentMessage.MessageID},
		{"agent.decision.created", "agent_decision", decisionID},
	} {
		if _, err := s.appendEvent(
			ctx,
			tx,
			projectID,
			nil,
			nil,
			event.eventType,
			event.subjectType,
			event.subjectID,
			nil,
		); err != nil {
			return MessageExchange{}, err
		}
	}
	if action != nil {
		if _, err := s.appendEvent(
			ctx,
			tx,
			projectID,
			nil,
			nil,
			"proposed_action.created",
			"proposed_action",
			action.ProposedActionID,
			map[string]any{"snapshot_hash": action.SnapshotHash},
		); err != nil {
			return MessageExchange{}, err
		}
	}
	if invocation != nil {
		if _, err := s.appendEvent(
			ctx, tx, projectID, nil, nil,
			"skill_invocation.created", "skill_invocation", invocation.SkillInvocationID,
			map[string]any{"execution_mode": invocation.ExecutionMode},
		); err != nil {
			return MessageExchange{}, err
		}
	}
	var taskRef *AgentTask
	if invocation != nil && invocation.ExecutionMode == "background_task" && action != nil && !action.RequiresConfirmation {
		selected, exists, entryErr := s.capabilityEntryForInvocationQuery(ctx, tx, invocation.SkillInvocationID)
		if entryErr != nil {
			return MessageExchange{}, entryErr
		}
		entry, entryErr := validateBackgroundTaskEntry(selected, exists, invocation.CapabilityVersion)
		if entryErr != nil {
			return MessageExchange{}, entryErr
		}
		createdTask, startErr := s.startAgentTaskTx(
			ctx, tx, entry, *invocation, action, action.Input, action.Config, now,
		)
		if startErr != nil {
			return MessageExchange{}, startErr
		}
		taskRef = &createdTask
		invocation.Status = "delegated_to_task"
		invocation.AgentTaskID = &createdTask.AgentTaskID
		invocation.UpdatedAt = now
		action.Status = "consumed"
		action.ConsumedTaskID = &createdTask.AgentTaskID
		action.UpdatedAt = now
	}
	if resolution != nil {
		if _, err := s.appendEvent(ctx, tx, projectID, nil, nil,
			"target_resolution.created", "target_resolution", resolution.TargetResolutionID,
			map[string]any{"status": resolution.Status}); err != nil {
			return MessageExchange{}, err
		}
	}
	if revision != nil {
		if _, err := s.appendEvent(ctx, tx, projectID, nil, nil,
			"revision_request.created", "revision_request", revision.RevisionRequestID,
			map[string]any{"status": revision.Status}); err != nil {
			return MessageExchange{}, err
		}
	}
	if goalChanged && goal != nil {
		if _, err := s.appendEvent(ctx, tx, projectID, nil, nil,
			"project_goal."+goal.Status, "project_goal", goal.GoalID,
			map[string]any{"status": goal.Status, "version": goal.Version}); err != nil {
			return MessageExchange{}, err
		}
	}
	exchange := MessageExchange{
		UserMessage:  userMessage,
		AgentMessage: agentMessage,
		Context:      messageContext,
		Decision:     decisionRecord,
		Action:       action,
		Invocation:   invocation,
		TaskRef:      taskRef,
		RunRef:       nil,
		Resolution:   resolution,
		Revision:     revision,
		Goal:         goal,
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, exchange, now); err != nil {
		return MessageExchange{}, err
	}
	if err := tx.Commit(); err != nil {
		return MessageExchange{}, err
	}
	return exchange, nil
}

var artifactRevisionConfirmations = map[string]struct{}{
	"改": {}, "修改": {}, "改吧": {}, "修改吧": {}, "就改吧": {}, "按这个改": {},
	"确认修改": {}, "开始修改": {}, "执行修改": {}, "可以改": {}, "可以修改": {},
}

func (s *Store) normalizeArtifactMutationTurnTx(
	ctx context.Context,
	tx *sql.Tx,
	conversationID string,
	request agentcontract.MessageRequest,
	decision agentcontract.AgentDecision,
) (agentcontract.AgentDecision, string, error) {
	content := strings.Trim(strings.ToLower(strings.TrimSpace(request.Content)), "，。！？!? ")
	instruction := strings.TrimSpace(request.Content)
	artifactID := artifactHint(request, decision.TargetRef)
	if artifactID == "" {
		return decision, instruction, nil
	}

	_, isConfirmation := artifactRevisionConfirmations[content]
	explicitRegeneration := requestsArtifactRegeneration(content)
	if decision.Intent == "regenerate" && !explicitRegeneration {
		decision.Intent = "revise"
	}
	if isConfirmation {
		previous, err := priorArtifactRevisionInstructionTx(ctx, tx, conversationID, artifactID)
		if err != nil {
			return decision, instruction, err
		}
		if previous == "" {
			decision.Intent = "clarify"
			decision.Reply = "请说明希望修改的具体内容。"
			decision.TargetRef = nil
			return decision, instruction, nil
		}
		decision.Intent = "revise"
		instruction = previous
	}
	return decision, instruction, nil
}

func artifactHint(request agentcontract.MessageRequest, target *agentcontract.TargetRef) string {
	if request.SelectionSnapshot != nil {
		var selected struct {
			ArtifactID string `json:"artifact_id"`
		}
		if json.Unmarshal(request.SelectionSnapshot.Selection, &selected) == nil && selected.ArtifactID != "" {
			return selected.ArtifactID
		}
	}
	if request.ClientContext.CurrentArtifactID != nil {
		return strings.TrimSpace(*request.ClientContext.CurrentArtifactID)
	}
	if target != nil && target.TargetType == "artifact" {
		return strings.TrimSpace(target.TargetID)
	}
	return ""
}

func requestsArtifactRegeneration(content string) bool {
	for _, cue := range []string{
		"重新生成", "重新跑", "重跑", "重新处理", "从原文重做", "从原始材料重做",
		"重新解析", "重新识别", "regenerate", "rerun",
	} {
		if strings.Contains(content, cue) {
			return true
		}
	}
	return false
}

func priorArtifactRevisionInstructionTx(
	ctx context.Context,
	tx *sql.Tx,
	conversationID string,
	artifactID string,
) (string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT m.content, mc.selection_snapshot_json, mc.client_context_json
		FROM messages m
		LEFT JOIN message_contexts mc ON mc.message_id = m.message_id
		LEFT JOIN message_routing_contexts mrc ON mrc.message_id = m.message_id
		WHERE m.conversation_id = ? AND m.role = 'user'
			AND (mrc.artifact_id = ? OR mc.selection_snapshot_json IS NOT NULL OR mc.client_context_json IS NOT NULL)
		ORDER BY m.rowid DESC
		LIMIT 12`, conversationID, artifactID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var candidate string
		var selectionJSON, clientJSON sql.NullString
		if err := rows.Scan(&candidate, &selectionJSON, &clientJSON); err != nil {
			return "", err
		}
		candidateArtifactID := ""
		if selectionJSON.Valid && selectionJSON.String != "null" {
			var selection agentcontract.SelectionSnapshot
			if json.Unmarshal([]byte(selectionJSON.String), &selection) == nil {
				var selected struct {
					ArtifactID string `json:"artifact_id"`
				}
				if json.Unmarshal(selection.Selection, &selected) == nil {
					candidateArtifactID = selected.ArtifactID
				}
			}
		}
		if candidateArtifactID == "" && clientJSON.Valid {
			var client agentcontract.ClientContext
			if json.Unmarshal([]byte(clientJSON.String), &client) == nil && client.CurrentArtifactID != nil {
				candidateArtifactID = strings.TrimSpace(*client.CurrentArtifactID)
			}
		}
		if candidateArtifactID != "" && candidateArtifactID != artifactID {
			continue
		}
		normalized := strings.Trim(strings.ToLower(strings.TrimSpace(candidate)), "，。！？!? ")
		if _, confirmation := artifactRevisionConfirmations[normalized]; confirmation {
			continue
		}
		if looksLikeArtifactRevision(candidate) {
			return strings.TrimSpace(candidate), nil
		}
	}
	return "", rows.Err()
}

func looksLikeArtifactRevision(content string) bool {
	normalized := strings.ToLower(strings.TrimSpace(content))
	for _, cue := range []string{
		"修改", "改成", "换成", "替换", "删除", "删掉", "增加", "补充", "调整", "优化",
		"强化", "弱化", "扩写", "缩短", "保留", "不要", "需要", "应该", "不够", "太少", "太多",
		"有误", "错误", "不对", "rewrite", "revise", "change", "replace", "remove", "add",
	} {
		if strings.Contains(normalized, cue) {
			return true
		}
	}
	return false
}

func (s *Store) createSkillInvocationTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID, conversationID, userMessageID, agentMessageID string,
	reference agentcontract.CapabilityRef,
	intent string,
	action *ProposedAction,
	now time.Time,
) (SkillInvocation, error) {
	entry, ok, err := s.capabilityEntryForProjectVersionQuery(ctx, tx, projectID, reference.CapabilityID, reference.Version)
	if err != nil {
		return SkillInvocation{}, err
	}
	if !ok || entry.Definition == nil || entry.Definition.Version != reference.Version {
		return SkillInvocation{}, domainError("CAPABILITY_NOT_FOUND", "Skill 定义不存在或版本不匹配。")
	}
	skillVersionID, skillSnapshotID, err := s.pinExecutionSkillTx(ctx, tx, projectID, entry)
	if err != nil {
		return SkillInvocation{}, err
	}
	invocation := SkillInvocation{
		SkillInvocationID: s.newID("inv"), ProjectID: projectID, ConversationID: conversationID,
		UserMessageID: userMessageID, AgentMessageID: agentMessageID,
		CapabilityID: reference.CapabilityID, CapabilityVersion: reference.Version,
		ExecutionMode: entry.Definition.ExecutionMode, Status: "completed",
		CreatedAt: now, UpdatedAt: now,
	}
	if action != nil {
		invocation.Status = "awaiting_confirmation"
		invocation.ProposedActionID = &action.ProposedActionID
	} else if intent == "unsupported" {
		invocation.Status = "rejected"
	}
	activity, _ := AgentActivityFromContext(ctx)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO skill_invocations(
			skill_invocation_id, user_id, agent_turn_id, skill_version_id, skill_snapshot_id, project_id, conversation_id, user_message_id, agent_message_id,
			capability_id, capability_version, execution_mode, status, proposed_action_id,
			created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		invocation.SkillInvocationID, identity.UserIDFromContext(ctx), optionalSkillID(activity.AgentTurnID), skillVersionID, skillSnapshotID, projectID, conversationID, userMessageID, agentMessageID,
		invocation.CapabilityID, invocation.CapabilityVersion, invocation.ExecutionMode,
		invocation.Status, invocation.ProposedActionID, formatTime(now), formatTime(now),
	); err != nil {
		return SkillInvocation{}, err
	}
	return invocation, nil
}

func (s *Store) resolveMessageRoutingContextTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	request agentcontract.MessageRequest,
	decision agentcontract.AgentDecision,
	resolution *TargetResolution,
	activeRunID sql.NullString,
) (MessageRoutingContext, error) {
	// A visible historical artifact is a view hint. It becomes authoritative only
	// for an explicit selection/revision, or when no active workflow exists.
	if request.ClientContext.CurrentArtifactID != nil &&
		(request.SelectionSnapshot != nil || decision.Intent == "revise" || decision.Intent == "regenerate" ||
			(decision.Intent == "inspect" && (!activeRunID.Valid || !isRunOperationalInspect(request.Content)))) {
		var runID, capabilityID string
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(run_id, ''), capability_id FROM artifacts
			WHERE artifact_id = ? AND project_id = ?`,
			*request.ClientContext.CurrentArtifactID, projectID,
		).Scan(&runID, &capabilityID); err != nil {
			return MessageRoutingContext{}, err
		}
		return MessageRoutingContext{
			Scope: "artifact", RunID: &runID, CapabilityID: &capabilityID,
			ArtifactID: request.ClientContext.CurrentArtifactID,
		}, nil
	}
	if resolution != nil && resolution.ArtifactID != nil {
		var runID, capabilityID string
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(run_id, ''), capability_id FROM artifacts
			WHERE artifact_id = ? AND project_id = ?`, *resolution.ArtifactID, projectID,
		).Scan(&runID, &capabilityID); err != nil {
			return MessageRoutingContext{}, err
		}
		return MessageRoutingContext{
			Scope: "artifact", RunID: &runID, CapabilityID: &capabilityID, ArtifactID: resolution.ArtifactID,
		}, nil
	}
	if decision.Intent == "inspect" || decision.Intent == "control_run" {
		if viewedRunID := viewedRunID(request.ClientContext); viewedRunID != nil {
			var capabilityID string
			if err := tx.QueryRowContext(ctx, `
				SELECT capability_id FROM runs WHERE run_id = ? AND project_id = ?`,
				*viewedRunID, projectID,
			).Scan(&capabilityID); err != nil {
				return MessageRoutingContext{}, err
			}
			runID := *viewedRunID
			return MessageRoutingContext{Scope: "run", RunID: &runID, CapabilityID: &capabilityID}, nil
		}
		if activeRunID.Valid {
			var capabilityID string
			if err := tx.QueryRowContext(ctx, `SELECT capability_id FROM runs WHERE run_id = ?`, activeRunID.String).Scan(&capabilityID); err != nil {
				return MessageRoutingContext{}, err
			}
			runID := activeRunID.String
			return MessageRoutingContext{Scope: "run", RunID: &runID, CapabilityID: &capabilityID}, nil
		}
	}
	if request.CapabilityRef != nil {
		capabilityID := request.CapabilityRef.CapabilityID
		return MessageRoutingContext{Scope: "capability", CapabilityID: &capabilityID}, nil
	}
	return MessageRoutingContext{Scope: "project"}, nil
}

func isRunOperationalInspect(content string) bool {
	normalized := strings.Trim(strings.ToLower(strings.TrimSpace(content)), "，。！？!? ")
	for _, cue := range []string{
		"现在到哪", "进行到哪", "当前进度", "什么进度", "进度怎么样", "现在什么状态", "当前状态",
		"还要多久", "还在生成吗", "生成好了吗", "已经确认", "确认过", "刚确认", "刚才确认", "弹窗确认",
		"下一步", "接下来", "然后呢", "该干嘛", "要干嘛", "需要做什么", "应该做什么",
	} {
		if strings.Contains(normalized, cue) {
			return true
		}
	}
	return normalized == "继续" || normalized == "继续执行" || normalized == "往下执行"
}

// PreflightMessage rejects requests that cannot reach the Agent before a
// provider call. CreateMessageExchange repeats the checks transactionally, so
// this optimization never replaces Runtime's authoritative commit guard.
func (s *Store) PreflightMessage(
	ctx context.Context,
	conversationID string,
	request agentcontract.MessageRequest,
) (string, error) {
	if err := validateMessageEnvelope(&request); err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var projectID string
	if err := tx.QueryRowContext(ctx, `
		SELECT project_id FROM conversations WHERE conversation_id = ?`,
		conversationID,
	).Scan(&projectID); errors.Is(err, sql.ErrNoRows) {
		return "", domainError("CONVERSATION_NOT_FOUND", "对话不存在。")
	} else if err != nil {
		return "", err
	}
	if err := s.validateMessageRequestTx(ctx, tx, projectID, request); err != nil {
		return "", err
	}
	return projectID, nil
}

func validateMessageEnvelope(request *agentcontract.MessageRequest) error {
	request.Content = strings.TrimSpace(request.Content)
	if request.Content == "" && len(request.AttachmentRefs) == 0 {
		return domainError(
			"REQUEST_VALIDATION_FAILED",
			"消息内容和附件不能同时为空。",
		)
	}
	return nil
}

func (s *Store) validateMessageRequestTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	request agentcontract.MessageRequest,
) error {
	if err := s.validateCapabilityRef(ctx, tx, projectID, request.CapabilityRef); err != nil {
		return err
	}
	for _, attachment := range request.AttachmentRefs {
		if attachment.AssetID == "" || attachment.AssetSnapshotID == "" {
			return domainError("REQUEST_VALIDATION_FAILED", "附件引用不完整。")
		}
		reference := assetInputReference{
			AssetID:         attachment.AssetID,
			AssetSnapshotID: attachment.AssetSnapshotID,
			Role:            "message_attachment",
			Order:           1,
		}
		if err := validateAssetOwnership(ctx, tx, projectID, []assetInputReference{reference}); err != nil {
			return err
		}
	}
	viewArtifact, err := s.validateClientViewContextTx(ctx, tx, projectID, request.ClientContext)
	if err != nil {
		return err
	}
	if request.SelectionSnapshot != nil {
		selection := request.SelectionSnapshot
		if selection.ArtifactVersionID == "" || selection.SnapshotHash == "" ||
			len(selection.Selection) == 0 {
			return domainError("REQUEST_VALIDATION_FAILED", "选区快照不完整。")
		}
		var versionProjectID, artifactID, artifactType, scopeKey, currentVersionID string
		err := tx.QueryRowContext(ctx, `
			SELECT a.project_id, a.artifact_id, a.artifact_type, a.scope_key, a.current_version_id
			FROM artifact_versions av
			JOIN artifacts a ON a.artifact_id = av.artifact_id
			WHERE av.artifact_version_id = ?`, selection.ArtifactVersionID).Scan(
			&versionProjectID, &artifactID, &artifactType, &scopeKey, &currentVersionID,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return domainError("ARTIFACT_VERSION_NOT_FOUND", "选区对应的产物版本不存在。")
		}
		if err != nil {
			return err
		}
		if versionProjectID != projectID {
			return domainError("RESOURCE_PROJECT_MISMATCH", "选区不属于当前作品。")
		}
		if currentVersionID != selection.ArtifactVersionID {
			return domainError("SELECTION_SNAPSHOT_STALE", "选区对应的产物已有新版本，请重新选择。")
		}
		var target struct {
			ArtifactID   string `json:"artifact_id"`
			ArtifactType string `json:"artifact_type"`
			TargetScope  string `json:"target_scope"`
			ScopeKey     string `json:"scope_key"`
			TextRange    *struct {
				Start        int    `json:"start"`
				End          int    `json:"end"`
				SelectedText string `json:"selected_text"`
			} `json:"text_range"`
		}
		if err := json.Unmarshal(selection.Selection, &target); err != nil {
			return domainError("REQUEST_VALIDATION_FAILED", "选区快照不是有效 JSON。")
		}
		if target.ArtifactID != artifactID || target.ArtifactType != artifactType ||
			target.TargetScope != "selection" || (target.ScopeKey != "" && target.ScopeKey != scopeKey) ||
			target.TextRange == nil || target.TextRange.Start < 0 || target.TextRange.End <= target.TextRange.Start ||
			strings.TrimSpace(target.TextRange.SelectedText) == "" {
			return domainError("SELECTION_TARGET_MISMATCH", "选区定位信息与当前产物不一致，请重新选择。")
		}
		if viewArtifact != nil && (viewArtifact.ArtifactID != artifactID ||
			request.ClientContext.CurrentArtifactVersionID == nil ||
			*request.ClientContext.CurrentArtifactVersionID != selection.ArtifactVersionID) {
			return domainError("SELECTION_TARGET_MISMATCH", "当前查看内容与选区不一致，请重新选择。")
		}
		normalized, err := normalizeJSON(selection.Selection)
		if err != nil {
			return domainError("REQUEST_VALIDATION_FAILED", "选区快照不是有效 JSON。")
		}
		if sha256Hex(normalized) != selection.SnapshotHash {
			return domainError("SNAPSHOT_HASH_MISMATCH", "选区快照已变化，请重新选择。")
		}
	}
	return nil
}

func (s *Store) validateClientViewContextTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	client agentcontract.ClientContext,
) (*Artifact, error) {
	if client.CurrentArtifactVersionID != nil && client.CurrentArtifactID == nil {
		return nil, domainError("REQUEST_VALIDATION_FAILED", "当前产物版本缺少产物引用。")
	}
	var artifact *Artifact
	if client.CurrentArtifactID != nil {
		var item Artifact
		var createdAt, updatedAt string
		err := tx.QueryRowContext(ctx, `
			SELECT artifact_id, project_id, COALESCE(run_id, ''), COALESCE(step_run_id, ''),
			       origin_type, origin_id, capability_id,
			       artifact_type, scope_key, current_version_id, created_at, updated_at
			FROM artifacts WHERE artifact_id = ?`, *client.CurrentArtifactID).Scan(
			&item.ArtifactID, &item.ProjectID, &item.RunID, &item.StepRunID,
			&item.OriginType, &item.OriginID, &item.CapabilityID,
			&item.ArtifactType, &item.ScopeKey, &item.CurrentVersionID, &createdAt, &updatedAt,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domainError("ARTIFACT_NOT_FOUND", "当前查看的产物不存在。")
		}
		if err != nil {
			return nil, err
		}
		if item.ProjectID != projectID {
			return nil, domainError("RESOURCE_PROJECT_MISMATCH", "当前查看的产物不属于该作品。")
		}
		if client.CurrentArtifactVersionID != nil && *client.CurrentArtifactVersionID != item.CurrentVersionID {
			return nil, domainError("VIEW_CONTEXT_STALE", "当前查看的产物版本已更新，请刷新后重试。")
		}
		if viewedRunID(client) != nil && *viewedRunID(client) != item.RunID {
			return nil, domainError("VIEW_CONTEXT_MISMATCH", "当前生成记录与产物不一致。")
		}
		if viewedCapabilityID(client) != nil && *viewedCapabilityID(client) != item.CapabilityID {
			return nil, domainError("VIEW_CONTEXT_MISMATCH", "当前 Skill 与产物不一致。")
		}
		if client.CurrentScopeKey != nil && *client.CurrentScopeKey != item.ScopeKey {
			return nil, domainError("VIEW_CONTEXT_MISMATCH", "当前内容范围与产物不一致。")
		}
		artifact = &item
	}
	if viewedRunID(client) != nil && artifact == nil {
		var runProjectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM runs WHERE run_id = ?`, *viewedRunID(client)).Scan(&runProjectID); errors.Is(err, sql.ErrNoRows) {
			return nil, domainError("RUN_NOT_FOUND", "当前生成记录不存在。")
		} else if err != nil {
			return nil, err
		} else if runProjectID != projectID {
			return nil, domainError("RESOURCE_PROJECT_MISMATCH", "当前生成记录不属于该作品。")
		}
	}
	return artifact, nil
}

func viewedRunID(client agentcontract.ClientContext) *string {
	if client.ViewedRunID != nil {
		return client.ViewedRunID
	}
	return client.CurrentRunID
}

func viewedCapabilityID(client agentcontract.ClientContext) *string {
	if client.ViewedCapabilityID != nil {
		return client.ViewedCapabilityID
	}
	return client.CurrentCapabilityID
}

func (s *Store) validateDecisionReferences(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	request agentcontract.MessageRequest,
	decision agentcontract.AgentDecision,
) error {
	if err := s.validateCapabilityRef(ctx, tx, projectID, decision.CapabilityRef); err != nil {
		return err
	}
	if request.CapabilityRef != nil && decision.CapabilityRef != nil &&
		*request.CapabilityRef != *decision.CapabilityRef {
		return domainError("AGENT_DECISION_REJECTED", "Agent 决策改变了用户显式选择的 Skill。")
	}
	var selected *capability.CompiledDefinition
	if decision.CapabilityRef != nil {
		entry, ok, err := s.capabilityEntryForProjectQuery(
			ctx, tx, projectID, decision.CapabilityRef.CapabilityID,
		)
		if err != nil {
			return err
		}
		if ok {
			selected = entry.Definition
		}
	}
	if decision.ProposedAction == nil {
		if selected != nil && selected.ExecutionMode == "background_task" &&
			decision.Intent == "propose_capability" {
			return domainError("AGENT_DECISION_REJECTED", "后台 Skill 必须提交后台任务动作。")
		}
		return nil
	}
	action := decision.ProposedAction
	if _, ok := allowedProposedActionTypes[action.ActionType]; !ok {
		return domainError("AGENT_DECISION_REJECTED", "Agent 提出了未注册的动作。")
	}
	if err := s.validateCapabilityRef(ctx, tx, projectID, action.CapabilityRef); err != nil {
		return err
	}
	if decision.CapabilityRef != nil && action.CapabilityRef != nil &&
		*decision.CapabilityRef != *action.CapabilityRef {
		return domainError("AGENT_DECISION_REJECTED", "待执行动作与 Agent 选择的 Skill 不一致。")
	}
	if action.ActionType == "inspect_artifact" || action.ActionType == "control_run" {
		return nil
	}
	if selected == nil || action.CapabilityRef == nil {
		return domainError("AGENT_DECISION_REJECTED", "待执行动作缺少可用 Skill。")
	}
	switch selected.ExecutionMode {
	case "inline":
		return domainError("AGENT_DECISION_REJECTED", "inline Skill 不能创建后台 Task 或 Business Run。")
	case "background_task":
		if action.ActionType != "start_background_task" ||
			action.RequiresConfirmation != selected.EntryPolicy.RequiresUserConfirmation ||
			!jsonObject(action.Input) || !jsonObject(action.Config) {
			return domainError("AGENT_DECISION_REJECTED", "后台任务动作与 Skill 执行策略不一致。")
		}
	case "stateful_workflow":
		if action.ActionType != "collect_run_configuration" && action.ActionType != "start_run" {
			return domainError("AGENT_DECISION_REJECTED", "有状态 Skill 只能创建 Run 配置或启动动作。")
		}
	default:
		return domainError("AGENT_DECISION_REJECTED", "Skill 执行模式无效。")
	}
	if action.ActionType == "start_run" {
		if action.CapabilityRef == nil || !action.RequiresConfirmation ||
			!jsonObject(action.Input) || !jsonObject(action.Config) {
			return domainError("AGENT_DECISION_REJECTED", "启动动作缺少精确输入、配置或确认要求。")
		}
	}
	if action.ActionType == "start_background_task" &&
		(!jsonObject(action.Input) || !jsonObject(action.Config)) {
		return domainError("AGENT_DECISION_REJECTED", "后台任务动作缺少精确输入或配置。")
	}
	return nil
}

func (s *Store) validateCapabilityRef(
	ctx context.Context,
	query projectWorkspaceQuery,
	projectID string,
	reference *agentcontract.CapabilityRef,
) error {
	if reference == nil {
		return nil
	}
	entry, ok, err := s.capabilityEntryForProjectQuery(ctx, query, projectID, reference.CapabilityID)
	if err != nil {
		return err
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil {
		return domainError("CAPABILITY_UNAVAILABLE", "所选 Skill 当前不可用。")
	}
	if entry.Definition.Version != reference.Version {
		return domainError("CAPABILITY_VERSION_UNAVAILABLE", "所选 Skill 版本当前不可用。")
	}
	return nil
}

func validateAgentDecision(decision agentcontract.AgentDecision) error {
	decision.Reply = strings.TrimSpace(decision.Reply)
	if decision.Reply == "" {
		return domainError("AGENT_DECISION_REJECTED", "Agent 回复不能为空。")
	}
	if _, ok := allowedAgentIntents[decision.Intent]; !ok {
		return domainError("AGENT_DECISION_REJECTED", "Agent 返回了未注册的意图。")
	}
	if decision.Confidence < 0 || decision.Confidence > 1 {
		return domainError("AGENT_DECISION_REJECTED", "Agent 置信度超出有效范围。")
	}
	if decision.Intent == "clarify" &&
		(decision.Clarification == nil || strings.TrimSpace(decision.Clarification.Question) == "") {
		return domainError("AGENT_DECISION_REJECTED", "追问决策缺少明确问题。")
	}
	if decision.Intent == "create_artifact" {
		drafts := slices.Clone(decision.ArtifactDrafts)
		if decision.ArtifactDraft != nil {
			drafts = append([]agentcontract.ArtifactDraft{*decision.ArtifactDraft}, drafts...)
		}
		if len(drafts) == 0 || len(drafts) > 12 {
			return domainError("AGENT_DECISION_REJECTED", "通用产物决策缺少有效内容。")
		}
		for _, draft := range drafts {
			if (draft.ArtifactType != "generic_document" && draft.ArtifactType != "generic_table") ||
				strings.TrimSpace(draft.Title) == "" || !jsonObject(draft.Payload) {
				return domainError("AGENT_DECISION_REJECTED", "通用产物决策缺少有效内容。")
			}
		}
	}
	if decision.GoalUpdate != nil {
		update := decision.GoalUpdate
		if update.Action != "set" && update.Action != "complete" && update.Action != "cancel" {
			return domainError("AGENT_DECISION_REJECTED", "Goal 更新动作无效。")
		}
		if update.Action == "set" {
			title := strings.TrimSpace(update.Title)
			if title == "" || len(title) > 500 || len(update.SuccessCriteria) > 12 {
				return domainError("AGENT_DECISION_REJECTED", "Goal 缺少有效标题或成功标准过多。")
			}
			for _, criterion := range update.SuccessCriteria {
				if len(strings.TrimSpace(criterion)) > 500 {
					return domainError("AGENT_DECISION_REJECTED", "Goal 成功标准过长。")
				}
			}
		}
	}
	return nil
}

func (s *Store) createProposedActionTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	conversationID string,
	decisionID string,
	userMessageID string,
	confirmationMessageID string,
	request agentcontract.MessageRequest,
	draft agentcontract.ProposedActionDraft,
	now time.Time,
) (*ProposedAction, error) {
	input := draft.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	var actionEntry capability.Entry
	managedStateful := false
	if draft.CapabilityRef != nil {
		entry, ok, err := s.capabilityEntryForProjectQuery(
			ctx, tx, projectID, draft.CapabilityRef.CapabilityID,
		)
		if err != nil {
			return nil, err
		}
		if ok && entry.Definition != nil {
			actionEntry = entry
			managedStateful = entry.Definition.ExecutionMode == "stateful_workflow" &&
				!legacyIngestWorkflow(entry.Definition)
		}
	}
	if draft.ActionType == "start_background_task" && draft.CapabilityRef != nil {
		var value map[string]any
		if err := json.Unmarshal(input, &value); err != nil || value == nil {
			return nil, domainError("AGENT_DECISION_REJECTED", "后台任务输入必须是 JSON 对象。")
		}
		value["project_id"] = projectID
		value["user_request_message_id"] = userMessageID
		assetRole := sourceAssetRole(actionEntry.Definition)
		if len(request.AttachmentRefs) > 0 {
			assets := make([]map[string]any, 0, len(request.AttachmentRefs))
			for index, reference := range request.AttachmentRefs {
				assets = append(assets, map[string]any{
					"asset_id": reference.AssetID, "asset_snapshot_id": reference.AssetSnapshotID,
					"role": assetRole, "order": index + 1,
				})
			}
			value["assets"] = assets
		} else if !hasArtifactVersionInput(value) && request.ClientContext.CurrentArtifactVersionID != nil {
			value["artifact_versions"] = []map[string]any{{
				"artifact_version_id": *request.ClientContext.CurrentArtifactVersionID,
				"role":                assetRole, "order": 1,
			}}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		input = encoded
	}
	if draft.ActionType == "collect_run_configuration" && draft.CapabilityRef != nil {
		if managedStateful {
			encoded, err := normalizeManagedWorkflowInput(
				input, request.AttachmentRefs, request.ClientContext.CurrentArtifactVersionID,
				projectID, userMessageID, actionEntry.Definition,
			)
			if err != nil {
				return nil, err
			}
			input = encoded
		} else {
			var value map[string]any
			if err := json.Unmarshal(input, &value); err != nil || value == nil {
				return nil, domainError("AGENT_DECISION_REJECTED", "配置动作输入必须是 JSON 对象。")
			}
			value["project_id"] = projectID
			value["user_request_message_id"] = userMessageID
			if actionEntry.Definition == nil || strings.TrimSpace(actionEntry.Definition.InputBinding.SourceType) == "" {
				return nil, domainError("CAPABILITY_VERSION_UNAVAILABLE", "能力未声明来源类型。")
			}
			binding := actionEntry.Definition.InputBinding
			assetRole := sourceAssetRole(actionEntry.Definition)
			value["source_type"] = binding.SourceType
			if binding.AssetSetPurpose != "" && request.ClientContext.CurrentAssetSetVersionID != nil {
				value["asset_set_version_id"] = *request.ClientContext.CurrentAssetSetVersionID
				if snapshot, snapshotErr := getAssetSetSnapshotTx(ctx, tx, "", *request.ClientContext.CurrentAssetSetVersionID); snapshotErr == nil &&
					snapshot.AssetSet.ProjectID == projectID && snapshot.AssetSet.Purpose == binding.AssetSetPurpose &&
					snapshot.AssetSet.Status == "sealed" && snapshot.Version.Status == "sealed" &&
					snapshot.Version.Completeness.OrderConfirmed {
					value["asset_set_id"] = snapshot.AssetSet.AssetSetID
					value["collection_state"] = snapshot.AssetSet.Status
					draft.ActionType = "start_run"
					draft.RequiresConfirmation = true
					draft.Config = json.RawMessage(`{"config_ref":"extraction","payload":{"fidelity_level":"high","timecode_precision":"second","uncertain_content_policy":"mark"}}`)
				}
			}
			if len(request.AttachmentRefs) > 0 {
				assets := make([]map[string]any, 0, len(request.AttachmentRefs))
				for index, reference := range request.AttachmentRefs {
					assets = append(assets, map[string]any{
						"asset_id": reference.AssetID, "asset_snapshot_id": reference.AssetSnapshotID,
						"role": assetRole, "order": index + 1,
					})
				}
				value["assets"] = assets
			} else if !hasArtifactVersionInput(value) && request.ClientContext.CurrentArtifactVersionID != nil {
				value["artifact_versions"] = []map[string]any{{
					"artifact_version_id": *request.ClientContext.CurrentArtifactVersionID,
					"role":                assetRole, "order": 1,
				}}
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			input = encoded
		}
	}
	config := draft.Config
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	if draft.ActionType == "start_run" {
		var encoded json.RawMessage
		var err error
		if managedStateful {
			encoded, err = normalizeManagedWorkflowInput(
				input, request.AttachmentRefs, request.ClientContext.CurrentArtifactVersionID,
				projectID, userMessageID, actionEntry.Definition,
			)
		} else {
			var value map[string]any
			if json.Unmarshal(input, &value) != nil || value == nil {
				return nil, domainError("AGENT_DECISION_REJECTED", "启动动作输入必须是 JSON 对象。")
			}
			value["project_id"] = projectID
			value["user_request_message_id"] = userMessageID
			encoded, err = json.Marshal(value)
		}
		if err != nil {
			return nil, err
		}
		input = encoded
	}
	normalizedInput, err := normalizeJSON(input)
	if err != nil || !jsonObject(normalizedInput) {
		return nil, domainError("AGENT_DECISION_REJECTED", "待执行动作输入必须是 JSON 对象。")
	}
	normalizedConfig, err := normalizeJSON(config)
	if err != nil || !jsonObject(normalizedConfig) {
		return nil, domainError("AGENT_DECISION_REJECTED", "待执行动作配置必须是 JSON 对象。")
	}
	if managedStateful && draft.ActionType == "start_run" {
		normalizedConfig, err = normalizeManagedWorkflowConfig(actionEntry, normalizedConfig)
		if err != nil {
			return nil, err
		}
	}
	actionID := s.newID("pac")
	version := 1
	snapshotHash, err := proposedActionSnapshotHash(
		projectID,
		conversationID,
		confirmationMessageID,
		draft.ActionType,
		version,
		draft.CapabilityRef,
		normalizedInput,
		normalizedConfig,
	)
	if err != nil {
		return nil, err
	}
	var capabilityID, capabilityVersion any
	if draft.CapabilityRef != nil {
		capabilityID = draft.CapabilityRef.CapabilityID
		capabilityVersion = draft.CapabilityRef.Version
		if _, err := tx.ExecContext(ctx, `
			UPDATE skill_invocations SET status = 'superseded', updated_at = ?
			WHERE proposed_action_id IN (
				SELECT proposed_action_id FROM proposed_actions
				WHERE project_id = ? AND capability_id = ? AND status = 'pending'
			) AND status = 'awaiting_confirmation'`,
			formatTime(now), projectID, draft.CapabilityRef.CapabilityID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE proposed_actions SET status = 'superseded', updated_at = ?
			WHERE project_id = ? AND capability_id = ? AND status = 'pending'`,
			formatTime(now), projectID, draft.CapabilityRef.CapabilityID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO proposed_actions(
			proposed_action_id, agent_decision_id, project_id, conversation_id,
			confirmation_message_id, action_type, version, status, capability_id,
			capability_version, input_json, config_json, snapshot_hash,
			requires_confirmation, consumed_run_id, consumed_task_id, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		actionID,
		decisionID,
		projectID,
		conversationID,
		confirmationMessageID,
		draft.ActionType,
		version,
		capabilityID,
		capabilityVersion,
		string(normalizedInput),
		string(normalizedConfig),
		snapshotHash,
		boolInt(draft.RequiresConfirmation),
		formatTime(now),
		formatTime(now),
	); err != nil {
		return nil, err
	}
	return &ProposedAction{
		ProposedActionID:      actionID,
		AgentDecisionID:       decisionID,
		ProjectID:             projectID,
		ConversationID:        conversationID,
		ConfirmationMessageID: confirmationMessageID,
		ActionType:            draft.ActionType,
		Version:               version,
		Status:                "pending",
		CapabilityRef:         draft.CapabilityRef,
		Input:                 normalizedInput,
		Config:                normalizedConfig,
		SnapshotHash:          snapshotHash,
		RequiresConfirmation:  draft.RequiresConfirmation,
		CreatedAt:             now,
		UpdatedAt:             now,
	}, nil
}

func hasArtifactVersionInput(value map[string]any) bool {
	references, ok := value["artifact_versions"].([]any)
	return ok && len(references) > 0
}

func authorizeProposedActionMutationTx(ctx context.Context, tx *sql.Tx, action ProposedAction, scope string) error {
	var workspaceID string
	err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM projects
		WHERE project_id = ? AND deleted_at IS NULL`, action.ProjectID).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return domainError("PROPOSED_ACTION_NOT_FOUND", "待确认动作不存在。")
	}
	if err != nil {
		return err
	}
	if principal, ok := identity.UserFromContext(ctx); ok {
		if principal.WorkspaceID != workspaceID {
			return domainError("PROPOSED_ACTION_NOT_FOUND", "待确认动作不存在。")
		}
		if !principal.Allows(identity.RoleEditor) {
			return domainError("ROLE_FORBIDDEN", "当前角色不能修改待确认动作。")
		}
		if err := validateExecutionOwnerTx(ctx, tx, executionToolState{WorkspaceID: workspaceID, UserID: principal.UserID}); err != nil {
			return err
		}
	}
	if scope != "" && scope != action.ProjectID {
		return domainError("REQUEST_VALIDATION_FAILED", "配置卡请求范围与作品不一致。")
	}
	return nil
}

func decodeProposedActionReceipt(cached json.RawMessage, action ProposedAction) (ProposedAction, error) {
	receipt, err := decodeIdempotentResult[ProposedAction](cached)
	if err != nil {
		return ProposedAction{}, err
	}
	if receipt.ProposedActionID != action.ProposedActionID || receipt.ProjectID != action.ProjectID {
		return ProposedAction{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识已用于其他待确认动作。")
	}
	return receipt, nil
}

func (s *Store) ConfigureProposedAction(
	ctx context.Context,
	command ConfigureProposedActionCommand,
) (ProposedAction, error) {
	if command.ProposedActionID == "" || command.ExpectedVersion < 1 {
		return ProposedAction{}, domainError(
			"REQUEST_VALIDATION_FAILED",
			"配置卡缺少待确认动作或有效版本。",
		)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProposedAction{}, err
	}
	defer tx.Rollback()

	action := ProposedAction{ProposedActionID: command.ProposedActionID}
	var capabilityID, capabilityVersion, createdAt string
	var userRequestMessageID string
	var requiresConfirmation int
	err = tx.QueryRowContext(ctx, `
		SELECT pa.agent_decision_id, pa.project_id, pa.conversation_id,
			pa.confirmation_message_id, pa.action_type, pa.version, pa.status,
			pa.capability_id, pa.capability_version, pa.snapshot_hash,
			pa.requires_confirmation, pa.created_at,
			ad.user_message_id
		FROM proposed_actions pa
		JOIN agent_decisions ad ON ad.agent_decision_id = pa.agent_decision_id
		WHERE pa.proposed_action_id = ?`, command.ProposedActionID).Scan(
		&action.AgentDecisionID,
		&action.ProjectID,
		&action.ConversationID,
		&action.ConfirmationMessageID,
		&action.ActionType,
		&action.Version,
		&action.Status,
		&capabilityID,
		&capabilityVersion,
		&action.SnapshotHash,
		&requiresConfirmation,
		&createdAt,
		&userRequestMessageID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ProposedAction{}, domainError("PROPOSED_ACTION_NOT_FOUND", "待确认动作不存在。")
	}
	if err != nil {
		return ProposedAction{}, err
	}
	if err := authorizeProposedActionMutationTx(ctx, tx, action, command.Scope); err != nil {
		return ProposedAction{}, err
	}
	if command.Scope == "" {
		command.Scope = action.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return ProposedAction{}, err
	}
	if hit {
		return decodeProposedActionReceipt(cached, action)
	}
	if action.ActionType != "collect_run_configuration" || action.Status != "pending" ||
		action.Version != command.ExpectedVersion || requiresConfirmation != 0 {
		return ProposedAction{}, domainError("PROPOSED_ACTION_STALE", "配置卡已变化，请刷新后重试。")
	}
	if capabilityID == "" || capabilityVersion == "" {
		return ProposedAction{}, domainError("AGENT_DECISION_REJECTED", "配置卡缺少 Skill 引用。")
	}

	entry, ok, err := s.capabilityEntryForProposedActionQuery(
		ctx, tx, action.ProjectID, action.ProposedActionID, capabilityID, capabilityVersion,
	)
	if err != nil {
		return ProposedAction{}, err
	}
	if !ok || entry.Definition == nil {
		return ProposedAction{}, domainError("CAPABILITY_UNAVAILABLE", "请求的能力当前不可用。")
	}
	managedStateful := entry.Definition.ExecutionMode == "stateful_workflow" &&
		!legacyIngestWorkflow(entry.Definition)
	inputToNormalize := command.Input
	if managedStateful {
		inputToNormalize, err = normalizeManagedWorkflowInput(
			command.Input, nil, nil, action.ProjectID, userRequestMessageID, entry.Definition,
		)
		if err != nil {
			return ProposedAction{}, err
		}
	} else {
		var inputObject map[string]any
		if err := json.Unmarshal(command.Input, &inputObject); err != nil || inputObject == nil {
			return ProposedAction{}, domainError("CAPABILITY_INPUT_INVALID", "输入快照必须是 JSON 对象。")
		}
		inputObject["project_id"] = action.ProjectID
		inputObject["user_request_message_id"] = userRequestMessageID
		injectedInput, err := json.Marshal(inputObject)
		if err != nil {
			return ProposedAction{}, err
		}
		inputToNormalize = injectedInput
	}
	normalizedInput, err := normalizeJSON(inputToNormalize)
	if err != nil || !jsonObject(normalizedInput) {
		return ProposedAction{}, domainError("CAPABILITY_INPUT_INVALID", "输入快照必须是 JSON 对象。")
	}
	normalizedConfig, err := normalizeJSON(command.Config)
	if err != nil || !jsonObject(normalizedConfig) {
		return ProposedAction{}, domainError("CAPABILITY_CONFIG_INVALID", "生成配置必须是 JSON 对象。")
	}
	if managedStateful {
		normalizedConfig, err = normalizeManagedWorkflowConfig(entry, normalizedConfig)
		if err != nil {
			return ProposedAction{}, err
		}
	}
	if err := s.validateConfiguredStartRunTx(
		ctx,
		tx,
		action.ProjectID,
		action.ProposedActionID,
		capabilityID,
		capabilityVersion,
		normalizedInput,
		normalizedConfig,
	); err != nil {
		return ProposedAction{}, err
	}

	newVersion := action.Version + 1
	reference := &agentcontract.CapabilityRef{
		CapabilityID:  capabilityID,
		Version:       capabilityVersion,
		SelectionMode: "inferred",
	}
	if decisionRef, err := s.agentDecisionCapabilityRefTx(ctx, tx, action.AgentDecisionID); err != nil {
		return ProposedAction{}, err
	} else if decisionRef != nil {
		reference.SelectionMode = decisionRef.SelectionMode
	}
	snapshotHash, err := proposedActionSnapshotHash(
		action.ProjectID,
		action.ConversationID,
		action.ConfirmationMessageID,
		"start_run",
		newVersion,
		reference,
		normalizedInput,
		normalizedConfig,
	)
	if err != nil {
		return ProposedAction{}, err
	}
	now := s.now()
	result, err := tx.ExecContext(ctx, `
		UPDATE proposed_actions
		SET action_type = 'start_run', version = ?, input_json = ?, config_json = ?,
			snapshot_hash = ?, requires_confirmation = 1, updated_at = ?
		WHERE proposed_action_id = ? AND action_type = 'collect_run_configuration'
			AND status = 'pending' AND version = ?`,
		newVersion,
		string(normalizedInput),
		string(normalizedConfig),
		snapshotHash,
		formatTime(now),
		command.ProposedActionID,
		command.ExpectedVersion,
	)
	if err != nil {
		return ProposedAction{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return ProposedAction{}, err
	}
	if affected != 1 {
		return ProposedAction{}, domainError("PROPOSED_ACTION_STALE", "配置卡已变化，请刷新后重试。")
	}
	if _, err := s.appendEvent(
		ctx,
		tx,
		action.ProjectID,
		nil,
		nil,
		"proposed_action.configured",
		"proposed_action",
		command.ProposedActionID,
		map[string]any{"version": newVersion},
	); err != nil {
		return ProposedAction{}, err
	}
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return ProposedAction{}, err
	}
	action.ActionType = "start_run"
	action.Version = newVersion
	action.CapabilityRef = reference
	action.Input = normalizedInput
	action.Config = normalizedConfig
	action.SnapshotHash = snapshotHash
	action.RequiresConfirmation = true
	action.CreatedAt = parsedCreatedAt
	action.UpdatedAt = now
	if err := completeIdempotency(ctx, tx, command.CommandMeta, action, now); err != nil {
		return ProposedAction{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProposedAction{}, err
	}
	return action, nil
}

func (s *Store) validateConfiguredStartRunTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	proposedActionID string,
	capabilityID string,
	capabilityVersion string,
	input json.RawMessage,
	config json.RawMessage,
) error {
	var activeRunID sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT active_write_run_id FROM projects
		WHERE project_id = ? AND deleted_at IS NULL`, projectID).Scan(&activeRunID); errors.Is(err, sql.ErrNoRows) {
		return domainError("PROJECT_NOT_FOUND", "作品不存在。")
	} else if err != nil {
		return err
	}
	if activeRunID.Valid {
		return domainError("PROJECT_WRITE_RUN_CONFLICT", "当前作品已有活动生成任务。")
	}
	entry, ok, err := s.capabilityEntryForProposedActionQuery(
		ctx, tx, projectID, proposedActionID, capabilityID, capabilityVersion,
	)
	if err != nil {
		return err
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil {
		return domainError("CAPABILITY_UNAVAILABLE", "请求的能力当前不可用。")
	}
	definition := entry.Definition
	if definition.Version != capabilityVersion {
		return domainError("CAPABILITY_VERSION_UNAVAILABLE", "请求的能力版本不可用。")
	}
	if definition.ExecutionMode != "stateful_workflow" || len(definition.Steps) == 0 {
		return domainError("CAPABILITY_UNAVAILABLE", "当前能力尚未接通可执行入口。")
	}
	if legacyIngestWorkflow(definition) {
		if _, _, _, _, err := s.prepareRunSourceInputTx(ctx, tx, projectID, definition, input); err != nil {
			return err
		}
		if !jsonObject(config) {
			return domainError("CAPABILITY_CONFIG_INVALID", "生成配置必须是 JSON 对象。")
		}
		return nil
	}
	if _, _, _, err := s.prepareManagedWorkflowInputTx(ctx, tx, projectID, entry, input, ""); err != nil {
		return err
	}
	_, err = normalizeManagedWorkflowConfig(entry, config)
	return err
}

func (s *Store) BindProposedActionInput(ctx context.Context, command BindProposedActionInputCommand) (ProposedAction, error) {
	if command.ProposedActionID == "" || command.ExpectedVersion <= 0 {
		return ProposedAction{}, domainError("REQUEST_VALIDATION_FAILED", "能力输入绑定请求不完整。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProposedAction{}, err
	}
	defer tx.Rollback()

	action, err := scanProposedAction(tx.QueryRowContext(ctx, `
		SELECT proposed_action_id, agent_decision_id, project_id, conversation_id,
			confirmation_message_id, action_type, version, status, capability_id,
			capability_version, input_json, config_json, snapshot_hash,
			requires_confirmation, consumed_run_id, consumed_task_id, created_at, updated_at
		FROM proposed_actions WHERE proposed_action_id = ?`, command.ProposedActionID))
	if errors.Is(err, sql.ErrNoRows) {
		return ProposedAction{}, domainError("PROPOSED_ACTION_NOT_FOUND", "待确认动作不存在。")
	}
	if err != nil {
		return ProposedAction{}, err
	}
	if err := authorizeProposedActionMutationTx(ctx, tx, action, command.Scope); err != nil {
		return ProposedAction{}, err
	}
	if command.Scope == "" {
		command.Scope = action.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return ProposedAction{}, err
	}
	if hit {
		return decodeProposedActionReceipt(cached, action)
	}
	if action.ActionType != "collect_run_configuration" || action.Status != "pending" ||
		action.Version != command.ExpectedVersion || action.RequiresConfirmation || action.CapabilityRef == nil {
		return ProposedAction{}, domainError("PROPOSED_ACTION_STALE", "配置卡已变化，请刷新后重试。")
	}

	entry, ok, err := s.capabilityEntryForProposedActionQuery(
		ctx, tx, action.ProjectID, action.ProposedActionID, action.CapabilityRef.CapabilityID,
		action.CapabilityRef.Version,
	)
	if err != nil {
		return ProposedAction{}, err
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil ||
		entry.Definition.Version != action.CapabilityRef.Version {
		return ProposedAction{}, domainError("CAPABILITY_UNAVAILABLE", "请求的能力当前不可用。")
	}
	var normalizedInput json.RawMessage
	if legacyIngestWorkflow(entry.Definition) {
		var inputObject map[string]any
		if err := json.Unmarshal(command.Input, &inputObject); err != nil || inputObject == nil {
			return ProposedAction{}, domainError("CAPABILITY_INPUT_INVALID", "输入快照必须是 JSON 对象。")
		}
		inputObject["project_id"] = action.ProjectID
		injected, err := json.Marshal(inputObject)
		if err != nil {
			return ProposedAction{}, err
		}
		_, _, _, normalizedInput, err = s.prepareRunSourceInputTx(
			ctx, tx, action.ProjectID, entry.Definition, injected,
		)
		if err != nil {
			return ProposedAction{}, err
		}
	} else {
		userMessageID, lookupErr := proposedActionUserMessageIDTx(
			ctx, tx, action.ProposedActionID,
		)
		if lookupErr != nil {
			return ProposedAction{}, lookupErr
		}
		managedInput, normalizeErr := normalizeManagedWorkflowInput(
			command.Input, nil, nil, action.ProjectID, userMessageID, entry.Definition,
		)
		if normalizeErr != nil {
			return ProposedAction{}, normalizeErr
		}
		var err error
		normalizedInput, _, _, err = s.prepareManagedWorkflowInputTx(
			ctx, tx, action.ProjectID, entry, managedInput, userMessageID,
		)
		if err != nil {
			return ProposedAction{}, err
		}
	}

	newVersion := action.Version + 1
	snapshotHash, err := proposedActionSnapshotHash(
		action.ProjectID, action.ConversationID, action.ConfirmationMessageID,
		action.ActionType, newVersion, action.CapabilityRef, normalizedInput, action.Config,
	)
	if err != nil {
		return ProposedAction{}, err
	}
	now := s.now()
	result, err := tx.ExecContext(ctx, `
		UPDATE proposed_actions SET version = ?, input_json = ?, snapshot_hash = ?, updated_at = ?
		WHERE proposed_action_id = ? AND action_type = 'collect_run_configuration'
			AND status = 'pending' AND version = ?`,
		newVersion, string(normalizedInput), snapshotHash, formatTime(now),
		action.ProposedActionID, command.ExpectedVersion)
	if err != nil {
		return ProposedAction{}, err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
		return ProposedAction{}, rowsErr
	} else if affected != 1 {
		return ProposedAction{}, domainError("PROPOSED_ACTION_STALE", "配置卡已变化，请刷新后重试。")
	}
	if _, err := s.appendEvent(ctx, tx, action.ProjectID, nil, nil,
		"proposed_action.input_bound", "proposed_action", action.ProposedActionID,
		map[string]any{"version": newVersion}); err != nil {
		return ProposedAction{}, err
	}
	action.Version = newVersion
	action.Input = normalizedInput
	action.SnapshotHash = snapshotHash
	action.UpdatedAt = now
	if err := completeIdempotency(ctx, tx, command.CommandMeta, action, now); err != nil {
		return ProposedAction{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProposedAction{}, err
	}
	return action, nil
}

func (s *Store) agentDecisionCapabilityRefTx(
	ctx context.Context,
	tx *sql.Tx,
	decisionID string,
) (*agentcontract.CapabilityRef, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `
		SELECT decision_json FROM agent_decisions WHERE agent_decision_id = ?`, decisionID).Scan(&raw); err != nil {
		return nil, err
	}
	var decision agentcontract.AgentDecision
	if err := json.Unmarshal([]byte(raw), &decision); err != nil {
		return nil, err
	}
	return decision.CapabilityRef, nil
}

func proposedActionSnapshotHash(
	projectID string,
	conversationID string,
	confirmationMessageID string,
	actionType string,
	version int,
	reference *agentcontract.CapabilityRef,
	input json.RawMessage,
	config json.RawMessage,
) (string, error) {
	payload, err := json.Marshal(struct {
		ProjectID             string                       `json:"project_id"`
		ConversationID        string                       `json:"conversation_id"`
		ConfirmationMessageID string                       `json:"confirmation_message_id"`
		ActionType            string                       `json:"action_type"`
		Version               int                          `json:"version"`
		CapabilityRef         *agentcontract.CapabilityRef `json:"capability_ref"`
		Input                 json.RawMessage              `json:"input"`
		Config                json.RawMessage              `json:"config"`
	}{
		ProjectID:             projectID,
		ConversationID:        conversationID,
		ConfirmationMessageID: confirmationMessageID,
		ActionType:            actionType,
		Version:               version,
		CapabilityRef:         reference,
		Input:                 input,
		Config:                config,
	})
	if err != nil {
		return "", err
	}
	return sha256Hex(payload), nil
}

func nullableJSON(value any) (any, error) {
	if value == nil || (reflect.ValueOf(value).Kind() == reflect.Ptr && reflect.ValueOf(value).IsNil()) {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

func normalizeJSON(raw json.RawMessage) ([]byte, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) GetProposedAction(
	ctx context.Context,
	proposedActionID string,
) (ProposedAction, error) {
	return scanProposedAction(s.db.QueryRowContext(ctx, `
		SELECT proposed_action_id, agent_decision_id, project_id, conversation_id,
			confirmation_message_id, action_type, version, status, capability_id,
			capability_version, input_json, config_json, snapshot_hash,
			requires_confirmation, consumed_run_id, consumed_task_id, created_at, updated_at
		FROM proposed_actions WHERE proposed_action_id = ?`, proposedActionID))
}

func (s *Store) ListPendingProposedActions(
	ctx context.Context,
	projectID string,
) ([]ProposedAction, error) {
	return s.listProposedActions(ctx, projectID, "AND status = 'pending'")
}

func (s *Store) ListProjectProposedActions(
	ctx context.Context,
	projectID string,
) ([]ProposedAction, error) {
	return s.listProposedActions(ctx, projectID, "AND status IN ('pending', 'consumed')")
}

func (s *Store) listProposedActions(
	ctx context.Context,
	projectID string,
	statusClause string,
) ([]ProposedAction, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	query := `
		SELECT proposed_action_id, agent_decision_id, project_id, conversation_id,
			confirmation_message_id, action_type, version, status, capability_id,
			capability_version, input_json, config_json, snapshot_hash,
			requires_confirmation, consumed_run_id, consumed_task_id, created_at, updated_at
		FROM proposed_actions
		WHERE project_id = ? ` + statusClause + `
		ORDER BY created_at ASC, proposed_action_id ASC`
	rows, err := s.db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	actions := make([]ProposedAction, 0)
	for rows.Next() {
		action, scanErr := scanProposedAction(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}

func scanProposedAction(row rowScanner) (ProposedAction, error) {
	var action ProposedAction
	var capabilityID, capabilityVersion, consumedRunID, consumedTaskID sql.NullString
	var inputJSON, configJSON, createdAt, updatedAt string
	var requiresConfirmation int
	err := row.Scan(
		&action.ProposedActionID,
		&action.AgentDecisionID,
		&action.ProjectID,
		&action.ConversationID,
		&action.ConfirmationMessageID,
		&action.ActionType,
		&action.Version,
		&action.Status,
		&capabilityID,
		&capabilityVersion,
		&inputJSON,
		&configJSON,
		&action.SnapshotHash,
		&requiresConfirmation,
		&consumedRunID,
		&consumedTaskID,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ProposedAction{}, domainError("PROPOSED_ACTION_NOT_FOUND", "待确认动作不存在。")
	}
	if err != nil {
		return ProposedAction{}, err
	}
	if capabilityID.Valid && capabilityVersion.Valid {
		action.CapabilityRef = &agentcontract.CapabilityRef{
			CapabilityID: capabilityID.String,
			Version:      capabilityVersion.String,
		}
	}
	action.Input = json.RawMessage(inputJSON)
	action.Config = json.RawMessage(configJSON)
	action.RequiresConfirmation = requiresConfirmation != 0
	action.ConsumedRunID = stringPointer(consumedRunID)
	action.ConsumedTaskID = stringPointer(consumedTaskID)
	action.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return ProposedAction{}, err
	}
	action.UpdatedAt, err = parseTime(updatedAt)
	return action, err
}
