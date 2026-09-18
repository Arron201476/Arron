package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

const managedWorkflowInputSnapshotKind = "managed_workflow"

type managedWorkflowInputSnapshot struct {
	SnapshotKind         string                   `json:"snapshot_kind"`
	SchemaRef            string                   `json:"schema_ref"`
	Input                json.RawMessage          `json:"input"`
	Assets               []assetInputReference    `json:"assets"`
	ArtifactVersions     []artifactInputReference `json:"artifact_versions,omitempty"`
	UserRequestMessageID string                   `json:"user_request_message_id"`
	SkillName            string                   `json:"skill_name,omitempty"`
	SkillInstructions    string                   `json:"skill_instructions,omitempty"`
	SkillContentHash     string                   `json:"skill_content_hash,omitempty"`
}

func legacyIngestWorkflow(definition *capability.CompiledDefinition) bool {
	if definition == nil || len(definition.Steps) == 0 {
		return false
	}
	first := definition.Steps[0]
	return first.Kind == "system" && first.ExecutorRef == "runtime.ingest_source" &&
		len(first.OutputRefs) == 1 && first.OutputRefs[0].ArtifactType == "source_input"
}

func workflowDefaultConfigRef(definition *capability.CompiledDefinition) string {
	if definition == nil {
		return ""
	}
	for _, step := range definition.Steps {
		if step.ConfigRef != nil && strings.TrimSpace(*step.ConfigRef) != "" {
			return strings.TrimSpace(*step.ConfigRef)
		}
	}
	if len(definition.ConfigSchemaRefs) == 1 {
		for ref := range definition.ConfigSchemaRefs {
			return ref
		}
	}
	return ""
}

func normalizeManagedWorkflowConfig(
	entry capability.Entry,
	raw json.RawMessage,
) (json.RawMessage, error) {
	if !jsonObject(raw) {
		return nil, domainError("CAPABILITY_CONFIG_INVALID", "生成配置必须是 JSON 对象。")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, domainError("CAPABILITY_CONFIG_INVALID", "生成配置不是有效 JSON。")
	}
	configRef := workflowDefaultConfigRef(entry.Definition)
	payload := raw
	if refJSON, hasRef := object["config_ref"]; hasRef {
		if err := json.Unmarshal(refJSON, &configRef); err != nil || strings.TrimSpace(configRef) == "" {
			return nil, domainError("CAPABILITY_CONFIG_INVALID", "生成配置缺少有效 config_ref。")
		}
		payloadJSON, hasPayload := object["payload"]
		if !hasPayload || !jsonObject(payloadJSON) {
			return nil, domainError("CAPABILITY_CONFIG_INVALID", "生成配置缺少有效 payload。")
		}
		payload = payloadJSON
	}
	configRef = strings.TrimSpace(configRef)
	if configRef == "" {
		return nil, domainError("CAPABILITY_CONFIG_INVALID", "工作流没有可解析的默认配置。")
	}
	schemaRef, ok := entry.Definition.ConfigSchemaRefs[configRef]
	if !ok {
		return nil, domainError("CAPABILITY_CONFIG_INVALID", "生成配置引用未在工作流中注册。")
	}
	schema, err := loadContextSchema(entry.ContentRoot, entry.SourceFile, schemaRef)
	if err != nil {
		return nil, err
	}
	if err := validateEmbeddedJSONSchema(schema, payload); err != nil {
		return nil, domainError(
			"CAPABILITY_CONFIG_INVALID",
			fmt.Sprintf("生成配置未通过 Skill Schema 校验：%v", err),
		)
	}
	normalizedPayload, err := normalizeJSON(payload)
	if err != nil {
		return nil, domainError("CAPABILITY_CONFIG_INVALID", "生成配置不是有效 JSON。")
	}
	envelope, err := json.Marshal(map[string]any{
		"config_ref": configRef,
		"payload":    json.RawMessage(normalizedPayload),
	})
	if err != nil {
		return nil, err
	}
	return normalizeJSON(envelope)
}

func (s *Store) prepareManagedWorkflowInputTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	entry capability.Entry,
	raw json.RawMessage,
	userMessageID string,
) (json.RawMessage, []assetInputReference, json.RawMessage, error) {
	normalized, err := normalizeManagedWorkflowInput(
		raw, nil, nil, projectID, userMessageID, entry.Definition,
	)
	if err != nil {
		return nil, nil, nil, err
	}
	_, references, artifactReferences, hostInput, err := s.prepareRunSourceInputTx(
		ctx, tx, projectID, entry.Definition, normalized,
	)
	if err != nil {
		return nil, nil, nil, err
	}
	var inputObject, hostObject map[string]any
	if err := json.Unmarshal(normalized, &inputObject); err != nil {
		return nil, nil, nil, err
	}
	if err := json.Unmarshal(hostInput, &hostObject); err != nil {
		return nil, nil, nil, err
	}
	for _, key := range []string{
		"project_id", "source_type", "assets", "artifact_versions",
		"asset_set_id", "asset_set_version_id", "collection_state",
		"user_request_message_id", "user_notes",
	} {
		value, exists := hostObject[key]
		if !exists || (value == nil && inputObject[key] == nil) {
			continue
		}
		inputObject[key] = value
	}
	normalized, err = json.Marshal(inputObject)
	if err != nil {
		return nil, nil, nil, err
	}
	normalized, err = normalizeJSON(normalized)
	if err != nil {
		return nil, nil, nil, err
	}
	inputSchema, err := loadContextSchema(
		entry.ContentRoot,
		entry.SourceFile,
		entry.Definition.InputSchemaRef,
	)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := validateEmbeddedJSONSchema(inputSchema, normalized); err != nil {
		return nil, nil, nil, domainError(
			"CAPABILITY_INPUT_INVALID",
			fmt.Sprintf("输入未通过 Skill Schema 校验：%v", err),
		)
	}
	if stepRequiresContext(entry.Definition.Steps[0], "asset_snapshot") &&
		len(references) == 0 && len(artifactReferences) == 0 {
		return nil, nil, nil, domainError("CAPABILITY_INPUT_INVALID", "当前 Skill 需要至少一个来源材料。")
	}
	snapshot := managedWorkflowInputSnapshot{
		SnapshotKind:         managedWorkflowInputSnapshotKind,
		SchemaRef:            entry.Definition.InputSchemaRef,
		Input:                normalized,
		Assets:               references,
		ArtifactVersions:     artifactReferences,
		UserRequestMessageID: userMessageID,
	}
	if entry.Skill != nil {
		snapshot.SkillName = entry.Skill.Name
		snapshot.SkillInstructions = entry.Skill.Instructions
		snapshot.SkillContentHash = entry.Skill.ContentHash
	}
	sealed, err := json.Marshal(snapshot)
	if err != nil {
		return nil, nil, nil, err
	}
	return normalized, references, sealed, nil
}

func stepRequiresContext(step capability.CompiledStep, expected string) bool {
	return slices.Contains(step.RequiredContext, expected)
}

func proposedActionUserMessageIDTx(
	ctx context.Context,
	tx *sql.Tx,
	proposedActionID string,
) (string, error) {
	var messageID string
	if err := tx.QueryRowContext(ctx, `
		SELECT ad.user_message_id
		FROM proposed_actions pa
		JOIN agent_decisions ad ON ad.agent_decision_id = pa.agent_decision_id
		WHERE pa.proposed_action_id = ?`, proposedActionID,
	).Scan(&messageID); err != nil {
		return "", err
	}
	return messageID, nil
}

func (s *Store) startManagedWorkflowRun(
	ctx context.Context,
	command StartRunCommand,
	entry capability.Entry,
	firstStep capability.CompiledStep,
) (RunSnapshot, error) {
	if !workerExecutableStepKind(firstStep.Kind) && firstStep.Kind != "batch" {
		return RunSnapshot{}, domainError("CAPABILITY_UNAVAILABLE", "工作流首步不是可执行的 Worker 步骤。")
	}
	if firstStep.PromptRef == nil || len(firstStep.OutputRefs) == 0 {
		return RunSnapshot{}, domainError("CAPABILITY_UNAVAILABLE", "工作流首步缺少 Prompt 或输出合同。")
	}
	normalizedConfig, err := normalizeManagedWorkflowConfig(entry, command.Config)
	if err != nil {
		return RunSnapshot{}, err
	}
	command.Config = normalizedConfig
	now := s.now()
	runID := s.newID("run")
	stepRunID := s.newID("step")
	inputSnapshotID := s.newID("risv")
	configSnapshotID := s.newID("cfg")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunSnapshot{}, err
	}
	defer tx.Rollback()
	action, err := s.authorizeProposedActionStartTx(ctx, tx, command.ProjectID, command.ConversationID, command.ProposedActionID, command.CapabilityID, command.CapabilityVersion, command.Scope)
	if err != nil {
		return RunSnapshot{}, err
	}
	if command.Scope == "" {
		command.Scope = command.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return RunSnapshot{}, err
	}
	if hit {
		return decodeProposedRunReceipt(cached, action)
	}
	if err := s.validateStartRunProposalTx(ctx, tx, command); err != nil {
		return RunSnapshot{}, err
	}
	skillVersionID, skillSnapshotID, err := s.pinExecutionSkillTx(ctx, tx, command.ProjectID, entry)
	if err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET write_intent = 0, updated_at = ?
		WHERE project_id = ? AND write_intent = 1 AND status = 'failed'`,
		formatTime(now), command.ProjectID,
	); err != nil {
		return RunSnapshot{}, err
	}
	userMessageID, err := proposedActionUserMessageIDTx(ctx, tx, command.ProposedActionID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if err := validateRunOwnership(ctx, tx, command.ProjectID, command.ConversationID, userMessageID); err != nil {
		return RunSnapshot{}, err
	}
	normalizedInput, assetReferences, sealedInput, err := s.prepareManagedWorkflowInputTx(
		ctx, tx, command.ProjectID, entry, command.Input, userMessageID,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	command.Input = normalizedInput
	configRef, err := configReference(command.Config)
	if err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO runs(
			run_id, user_id, skill_version_id, skill_snapshot_id, project_id, conversation_id, capability_id, capability_version,
			run_kind, write_intent, status, current_step_run_id,
			current_input_snapshot_version_id, input_snapshot_status, config_snapshot_json,
			run_event_seq, started_at, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 'running', ?, ?, 'sealed', ?, 0, ?, ?, ?)`,
		runID, identity.UserIDFromContext(ctx), skillVersionID, skillSnapshotID, command.ProjectID, command.ConversationID, command.CapabilityID, command.CapabilityVersion,
		command.RunKind, stepRunID, inputSnapshotID, string(command.Config),
		formatTime(now), formatTime(now), formatTime(now),
	); err != nil {
		if isProjectWriteRunConstraint(err) {
			return RunSnapshot{}, domainError("PROJECT_WRITE_RUN_CONFLICT", "当前作品已有活动生成任务。")
		}
		return RunSnapshot{}, fmt.Errorf("create run: %w", err)
	}
	if err := s.freezeRunSkillsTx(ctx, tx, command, runID, entry); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO run_input_snapshot_versions(
			run_input_snapshot_version_id, run_id, version, status, payload_json, created_at, sealed_at
		) VALUES(?, ?, 1, 'sealed', ?, ?, ?)`,
		inputSnapshotID, runID, string(sealedInput), formatTime(now), formatTime(now),
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO run_config_snapshots(
			config_snapshot_id, run_id, config_ref, version, status,
			payload_json, snapshot_hash, created_at, sealed_at
		) VALUES(?, ?, ?, 1, 'sealed', ?, ?, ?, ?)`,
		configSnapshotID, runID, configRef, string(command.Config), sha256Hex(command.Config),
		formatTime(now), formatTime(now),
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO step_runs(
			step_run_id, run_id, step_id, status, attempt_count, approval_policy,
			input_version_snapshot_json, task_cursor_json, started_at
		) VALUES(?, ?, ?, 'running', 1, ?, '[]', '{}', ?)`,
		stepRunID, runID, firstStep.ID, firstStep.Approval.Type, formatTime(now),
	); err != nil {
		return RunSnapshot{}, err
	}
	run := Run{
		RunID: runID, ProjectID: command.ProjectID, ConversationID: command.ConversationID,
		CapabilityID: command.CapabilityID, CapabilityVersion: command.CapabilityVersion,
		RunKind: command.RunKind, Status: "running", CurrentStepRunID: &stepRunID,
		CurrentInputSnapshotVersionID: inputSnapshotID, InputSnapshotStatus: "sealed",
		ConfigSnapshot: slices.Clone(command.Config), StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.ensureStepTasksForResume(
		ctx, tx, run, stepRunID, firstStep, json.RawMessage(`[]`), json.RawMessage(`{}`), now,
	); err != nil {
		return RunSnapshot{}, err
	}
	for _, reference := range assetReferences {
		if _, err := s.appendEvent(ctx, tx, command.ProjectID, &runID, &stepRunID,
			"run.input_asset_pinned", "asset_snapshot", reference.AssetSnapshotID,
			map[string]any{"asset_id": reference.AssetID, "order": reference.Order},
		); err != nil {
			return RunSnapshot{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET version = version + 1, status = 'running', active_write_run_id = ?,
			current_capability_id = ?, current_focus_artifact_version_id = NULL, updated_at = ?
		WHERE project_id = ? AND deleted_at IS NULL`,
		runID, command.CapabilityID, formatTime(now), command.ProjectID,
	); err != nil {
		return RunSnapshot{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE proposed_actions
		SET status = 'consumed', consumed_run_id = ?, updated_at = ?
		WHERE proposed_action_id = ? AND version = ? AND status = 'pending'`,
		runID, formatTime(now), command.ProposedActionID, command.ProposedActionVersion,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
		return RunSnapshot{}, rowsErr
	} else if affected != 1 {
		return RunSnapshot{}, domainError("CONFIRMATION_STALE", "确认卡已失效，请重新确认。")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE message_routing_contexts
		SET scope = 'run', run_id = ?, capability_id = ?
		WHERE message_id IN (
			SELECT ad.user_message_id FROM agent_decisions ad
			JOIN proposed_actions pa ON pa.agent_decision_id = ad.agent_decision_id
			WHERE pa.proposed_action_id = ?
			UNION
			SELECT ad.agent_message_id FROM agent_decisions ad
			JOIN proposed_actions pa ON pa.agent_decision_id = ad.agent_decision_id
			WHERE pa.proposed_action_id = ?
		)`, runID, command.CapabilityID, command.ProposedActionID, command.ProposedActionID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE skill_invocations
		SET status = 'delegated_to_run', run_id = ?, updated_at = ?
		WHERE proposed_action_id = ? AND status = 'awaiting_confirmation'`,
		runID, formatTime(now), command.ProposedActionID,
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE proposed_actions SET status = 'superseded', updated_at = ?
		WHERE project_id = ? AND status = 'pending' AND proposed_action_id <> ?`,
		formatTime(now), command.ProjectID, command.ProposedActionID,
	); err != nil {
		return RunSnapshot{}, err
	}
	events := []struct{ eventType, subjectType, subjectID string }{
		{"proposed_action.consumed", "proposed_action", command.ProposedActionID},
		{"run.created", "run", runID},
		{"run.started", "run", runID},
		{"step.started", "step_run", stepRunID},
	}
	for _, event := range events {
		if _, err := s.appendEvent(ctx, tx, command.ProjectID, &runID, &stepRunID,
			event.eventType, event.subjectType, event.subjectID, nil,
		); err != nil {
			return RunSnapshot{}, err
		}
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, snapshot, now); err != nil {
		return RunSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		if isProjectWriteRunConstraint(err) {
			return RunSnapshot{}, domainError("PROJECT_WRITE_RUN_CONFLICT", "当前作品已有活动生成任务。")
		}
		return RunSnapshot{}, err
	}
	return snapshot, nil
}

func normalizeManagedWorkflowInput(
	input json.RawMessage,
	attachments []agentcontract.AttachmentRef,
	currentArtifactVersionID *string,
	projectID string,
	userMessageID string,
	definition *capability.CompiledDefinition,
) (json.RawMessage, error) {
	var object map[string]any
	if err := json.Unmarshal(input, &object); err != nil || object == nil {
		return nil, domainError("AGENT_DECISION_REJECTED", "配置动作输入必须是 JSON 对象。")
	}
	if definition == nil || strings.TrimSpace(definition.InputBinding.SourceType) == "" {
		return nil, domainError("CAPABILITY_VERSION_UNAVAILABLE", "能力未声明来源类型。")
	}
	object["project_id"] = projectID
	object["source_type"] = strings.TrimSpace(definition.InputBinding.SourceType)
	if userMessageID != "" {
		object["user_request_message_id"] = userMessageID
	}
	if len(attachments) > 0 {
		assets := make([]map[string]any, 0, len(attachments))
		seen := make(map[string]struct{}, len(attachments))
		for index, ref := range attachments {
			if ref.AssetID == "" || ref.AssetSnapshotID == "" {
				continue
			}
			if _, duplicate := seen[ref.AssetID]; duplicate {
				continue
			}
			seen[ref.AssetID] = struct{}{}
			assets = append(assets, map[string]any{
				"asset_id": ref.AssetID, "asset_snapshot_id": ref.AssetSnapshotID,
				"role": sourceAssetRole(definition), "order": index + 1,
			})
		}
		if len(assets) > 0 {
			object["assets"] = assets
		}
	} else if !hasArtifactVersionInput(object) && currentArtifactVersionID != nil &&
		strings.TrimSpace(*currentArtifactVersionID) != "" {
		object["artifact_versions"] = []map[string]any{{
			"artifact_version_id": strings.TrimSpace(*currentArtifactVersionID),
			"role":                sourceAssetRole(definition),
			"order":               1,
		}}
	}
	return json.Marshal(object)
}
