package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

type sourceInputRequest struct {
	ProjectID            string                   `json:"project_id"`
	SourceType           string                   `json:"source_type"`
	Assets               []json.RawMessage        `json:"assets"`
	ArtifactVersions     []artifactInputReference `json:"artifact_versions,omitempty"`
	AssetSetID           string                   `json:"asset_set_id,omitempty"`
	AssetSetVersionID    string                   `json:"asset_set_version_id,omitempty"`
	CollectionState      string                   `json:"collection_state,omitempty"`
	UserRequestMessageID string                   `json:"user_request_message_id"`
	UserNotes            []string                 `json:"user_notes"`
}

type artifactInputReference struct {
	ArtifactVersionID string `json:"artifact_version_id"`
	Role              string `json:"role"`
	Order             int    `json:"order"`
}

type assetInputReference struct {
	AssetID         string `json:"asset_id"`
	AssetSnapshotID string `json:"asset_snapshot_id"`
	Role            string `json:"role"`
	Order           int    `json:"order"`
}

func (s *Store) StartRun(ctx context.Context, command StartRunCommand) (RunSnapshot, error) {
	entry, ok, err := s.capabilityEntryForProposedAction(
		ctx, command.ProjectID, command.ProposedActionID, command.CapabilityID, command.CapabilityVersion,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	if !ok {
		return RunSnapshot{}, domainError("CAPABILITY_NOT_FOUND", "请求的能力不存在。")
	}
	if entry.Status != capability.Available || entry.Definition == nil {
		return RunSnapshot{}, domainError("CAPABILITY_UNAVAILABLE", "请求的能力当前不可用。")
	}
	definition := entry.Definition
	if definition.ExecutionMode != "stateful_workflow" {
		return RunSnapshot{}, domainError("CAPABILITY_EXECUTION_MODE_INVALID", "只有有状态生产工作流可以创建 Run。")
	}
	if command.CapabilityVersion != definition.Version {
		return RunSnapshot{}, domainError("CAPABILITY_VERSION_UNAVAILABLE", "请求的能力版本不可用。")
	}
	if !command.Confirmed {
		return RunSnapshot{}, domainError("REQUIRED_CONFIRMATION_MISSING", "启动前需要用户明确确认。")
	}
	if command.ProposedActionID == "" || command.ProposedActionVersion < 1 ||
		command.ConfirmationMessageID == "" || command.ConfirmationSnapshotHash == "" {
		return RunSnapshot{}, domainError(
			"REQUIRED_CONFIRMATION_MISSING",
			"启动确认缺少待确认动作、版本、消息或快照。",
		)
	}
	if command.RunKind == "" {
		command.RunKind = "generation"
	}
	if command.RunKind != "generation" {
		return RunSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "当前增量只支持 generation Run。")
	}
	if len(definition.Steps) == 0 {
		return RunSnapshot{}, domainError("CAPABILITY_UNAVAILABLE", "能力没有可执行步骤。")
	}
	firstStep := definition.Steps[0]
	if !legacyIngestWorkflow(definition) {
		return s.startManagedWorkflowRun(ctx, command, entry, firstStep)
	}

	if !jsonObject(command.Config) {
		return RunSnapshot{}, domainError("CAPABILITY_CONFIG_INVALID", "生成配置必须是 JSON 对象。")
	}

	now := s.now()
	runID := s.newID("run")
	stepRunID := s.newID("step")
	inputSnapshotID := s.newID("risv")
	configSnapshotID := s.newID("cfg")
	artifactID := s.newID("art")
	artifactVersionID := s.newID("av")
	approvalID := s.newID("apr")

	subjectHash := approvalSubjectHash(artifactVersionID, 1)
	options := slices.Clone(firstStep.Approval.AllowedActions)

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
	// A failed Run remains in history, but an explicitly confirmed replacement
	// must be able to acquire the project's single write slot.
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET write_intent = 0, updated_at = ?
		WHERE project_id = ? AND write_intent = 1 AND status = 'failed'`,
		formatTime(now), command.ProjectID,
	); err != nil {
		return RunSnapshot{}, err
	}
	source, assetReferences, artifactReferences, normalizedInput, err := s.prepareRunSourceInputTx(
		ctx, tx, command.ProjectID, definition, command.Input,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	if err := validateRunOwnership(ctx, tx, command.ProjectID, command.ConversationID, source.UserRequestMessageID); err != nil {
		return RunSnapshot{}, err
	}
	sourcePayload, err := json.Marshal(map[string]any{
		"input_snapshot_id":       inputSnapshotID,
		"source_kind":             source.SourceType,
		"assets":                  source.Assets,
		"artifact_versions":       source.ArtifactVersions,
		"asset_set_id":            source.AssetSetID,
		"asset_set_version_id":    source.AssetSetVersionID,
		"collection_state":        source.CollectionState,
		"user_request_message_id": source.UserRequestMessageID,
		"confirmed_strategy_refs": []string{},
	})
	if err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO runs(
			run_id, user_id, skill_version_id, skill_snapshot_id, project_id, conversation_id, capability_id, capability_version,
			run_kind, write_intent, status, current_step_run_id,
			current_input_snapshot_version_id, input_snapshot_status, config_snapshot_json,
			run_event_seq, started_at, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 'waiting_approval', ?, ?, 'sealed', ?, 0, ?, ?, ?)`,
		runID, identity.UserIDFromContext(ctx), skillVersionID, skillSnapshotID, command.ProjectID, command.ConversationID, command.CapabilityID, command.CapabilityVersion,
		command.RunKind, stepRunID, inputSnapshotID, string(command.Config), formatTime(now), formatTime(now), formatTime(now),
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
		inputSnapshotID, runID, string(normalizedInput), formatTime(now), formatTime(now),
	); err != nil {
		return RunSnapshot{}, err
	}
	configRef, err := configReference(command.Config)
	if err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO run_config_snapshots(
			config_snapshot_id, run_id, config_ref, version, status,
			payload_json, snapshot_hash, created_at, sealed_at
		) VALUES(?, ?, ?, 1, 'sealed', ?, ?, ?, ?)`,
		configSnapshotID,
		runID,
		configRef,
		string(command.Config),
		sha256Hex(command.Config),
		formatTime(now),
		formatTime(now),
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO step_runs(
			step_run_id, run_id, step_id, status, attempt_count, approval_policy,
			input_version_snapshot_json, task_cursor_json, started_at
		) VALUES(?, ?, ?, 'waiting_approval', 1, ?, '[]', '{}', ?)`,
		stepRunID, runID, firstStep.ID, firstStep.Approval.Type, formatTime(now),
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifacts(
			artifact_id, project_id, run_id, step_run_id, capability_id, artifact_type,
			scope_key, current_version_id, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, 'source_input', 'singleton', ?, ?, ?)`,
		artifactID, command.ProjectID, runID, stepRunID, command.CapabilityID,
		artifactVersionID, formatTime(now), formatTime(now),
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason, created_at
		) VALUES(?, ?, 1, 'pending_approval', ?, 'source_input', '1.0.0', 'system',
			'content_agent_runtime', 'initial', ?)`,
		artifactVersionID, artifactID, string(sourcePayload), formatTime(now),
	); err != nil {
		return RunSnapshot{}, err
	}
	for _, reference := range assetReferences {
		if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
			ProjectID:                   command.ProjectID,
			RunID:                       runID,
			DownstreamArtifactVersionID: artifactVersionID,
			UpstreamKind:                "asset_snapshot",
			UpstreamRefID:               reference.AssetSnapshotID,
			Relation:                    "references_source",
			UpstreamScope:               dependencyScope("asset", "asset:"+reference.AssetID),
			DownstreamScope:             dependencyScope("collection_item", "asset:"+reference.AssetID),
			ImpactPolicyID:              "asset_exact",
		}, now); err != nil {
			return RunSnapshot{}, err
		}
	}
	for _, reference := range artifactReferences {
		if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
			ProjectID:                   command.ProjectID,
			RunID:                       runID,
			DownstreamArtifactVersionID: artifactVersionID,
			UpstreamKind:                "artifact_version",
			UpstreamRefID:               reference.ArtifactVersionID,
			Relation:                    "references_source",
			UpstreamScope:               dependencyScope("artifact", "artifact_version:"+reference.ArtifactVersionID),
			DownstreamScope:             dependencyScope("artifact", "singleton"),
			ImpactPolicyID:              "artifact_exact",
		}, now); err != nil {
			return RunSnapshot{}, err
		}
	}
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO approvals(
			approval_request_id, project_id, run_id, step_run_id, scope, status, version,
			title, reason, options_json, subject_kind, subject_ref_id, subject_version,
			subject_snapshot_hash, requested_at
		) VALUES(?, ?, ?, ?, ?, 'pending', 1, ?, ?, ?, 'artifact_version', ?, 1, ?, ?)`,
		approvalID, command.ProjectID, runID, stepRunID, firstStep.Approval.Scope,
		"确认来源材料", "确认规范化后的来源材料后才能进入下一步骤。",
		string(optionsJSON), artifactVersionID, subjectHash, formatTime(now),
	); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET version = version + 1, status = 'waiting_approval', active_write_run_id = ?,
			current_capability_id = ?, current_focus_artifact_version_id = ?, updated_at = ?
		WHERE project_id = ? AND deleted_at IS NULL`,
		runID, command.CapabilityID, artifactVersionID, formatTime(now), command.ProjectID,
	); err != nil {
		return RunSnapshot{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE proposed_actions
		SET status = 'consumed', consumed_run_id = ?, updated_at = ?
		WHERE proposed_action_id = ? AND version = ? AND status = 'pending'`,
		runID,
		formatTime(now),
		command.ProposedActionID,
		command.ProposedActionVersion,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return RunSnapshot{}, err
	}
	if affected != 1 {
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
		)`, runID, command.CapabilityID, command.ProposedActionID, command.ProposedActionID); err != nil {
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
	if _, err := tx.ExecContext(ctx, `UPDATE proposed_actions
		SET status = 'superseded', updated_at = ?
		WHERE project_id = ? AND status = 'pending' AND proposed_action_id <> ?`,
		formatTime(now), command.ProjectID, command.ProposedActionID); err != nil {
		return RunSnapshot{}, err
	}

	runRef, stepRef := runID, stepRunID
	events := []struct {
		eventType   string
		subjectType string
		subjectID   string
	}{
		{"proposed_action.consumed", "proposed_action", command.ProposedActionID},
		{"run.created", "run", runID},
		{"run.started", "run", runID},
		{"artifact.created", "artifact", artifactID},
		{"artifact.version_created", "artifact_version", artifactVersionID},
		{"approval.requested", "approval", approvalID},
		{"run.waiting_approval", "run", runID},
	}
	for _, event := range events {
		if _, err := s.appendEvent(ctx, tx, command.ProjectID, &runRef, &stepRef, event.eventType, event.subjectType, event.subjectID, nil); err != nil {
			return RunSnapshot{}, err
		}
	}
	var cursor EventCursor
	if err := tx.QueryRowContext(ctx, `
		SELECT p.project_event_seq, r.run_event_seq
		FROM projects p JOIN runs r ON r.project_id = p.project_id
		WHERE r.run_id = ?`, runID).Scan(&cursor.ProjectEventSeq, &cursor.RunEventSeq); err != nil {
		return RunSnapshot{}, err
	}
	runRefValue, stepRefValue := runID, stepRunID
	snapshot := RunSnapshot{
		Run: Run{
			RunID:                         runID,
			ProjectID:                     command.ProjectID,
			ConversationID:                command.ConversationID,
			CapabilityID:                  command.CapabilityID,
			CapabilityVersion:             command.CapabilityVersion,
			RunKind:                       command.RunKind,
			Status:                        "waiting_approval",
			CurrentStepRunID:              &stepRefValue,
			CurrentInputSnapshotVersionID: inputSnapshotID,
			InputSnapshotStatus:           "sealed",
			ConfigSnapshot:                slices.Clone(command.Config),
			StartedAt:                     &now,
			CreatedAt:                     now,
			UpdatedAt:                     now,
		},
		Steps: []StepRun{{
			StepRunID:            stepRunID,
			RunID:                runID,
			StepID:               firstStep.ID,
			Status:               "waiting_approval",
			AttemptCount:         1,
			ApprovalPolicy:       firstStep.Approval.Type,
			InputVersionSnapshot: json.RawMessage(`[]`),
			TaskCursor:           json.RawMessage(`{}`),
			StartedAt:            &now,
		}},
		CurrentApproval: &Approval{
			ApprovalRequestID:   approvalID,
			ProjectID:           command.ProjectID,
			RunID:               runID,
			StepRunID:           stepRunID,
			Scope:               firstStep.Approval.Scope,
			Status:              "pending",
			Version:             1,
			Title:               "确认来源材料",
			Reason:              "确认规范化后的来源材料后才能进入下一步骤。",
			Options:             options,
			SubjectKind:         "artifact_version",
			SubjectRefID:        artifactVersionID,
			SubjectVersion:      1,
			SubjectSnapshotHash: subjectHash,
			RequestedAt:         now,
		},
		Artifacts: []Artifact{{
			ArtifactID:       artifactID,
			ProjectID:        command.ProjectID,
			RunID:            runRefValue,
			StepRunID:        stepRunID,
			CapabilityID:     command.CapabilityID,
			ArtifactType:     "source_input",
			ScopeKey:         "singleton",
			CurrentVersionID: artifactVersionID,
			CreatedAt:        now,
			UpdatedAt:        now,
		}},
		EventCursor: cursor,
	}
	snapshot.AvailableActions, err = s.runAvailableActions(
		ctx,
		tx,
		snapshot.Run,
		snapshot.Steps,
		snapshot.CurrentApproval,
	)
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

func configReference(raw json.RawMessage) (string, error) {
	var envelope struct {
		ConfigRef string `json:"config_ref"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.ConfigRef == "" {
		return "", domainError("CAPABILITY_CONFIG_INVALID", "生成配置缺少 config_ref。")
	}
	return envelope.ConfigRef, nil
}

func (s *Store) GetRun(ctx context.Context, runID string) (Run, error) {
	run, err := scanRun(s.db.QueryRowContext(ctx, `
		SELECT run_id, project_id, conversation_id, capability_id, capability_version,
			run_kind, status, current_step_run_id, current_input_snapshot_version_id,
			input_snapshot_status, config_snapshot_json, started_at, ended_at, created_at, updated_at
		FROM runs WHERE run_id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, domainError("RUN_NOT_FOUND", "生成任务不存在。")
	}
	if err == nil {
		err = s.enrichRunEpisodeExecutionMode(ctx, s.db, &run)
	}
	return run, err
}

func (s *Store) ListStepRuns(ctx context.Context, runID string) ([]StepRun, error) {
	if _, err := s.GetRun(ctx, runID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT step_run_id, run_id, step_id, status, attempt_count, approval_policy,
			input_version_snapshot_json, task_cursor_json, started_at, ended_at
		FROM step_runs WHERE run_id = ?
		ORDER BY rowid ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []StepRun
	for rows.Next() {
		step, err := scanStepRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, step)
	}
	if result == nil {
		result = []StepRun{}
	}
	return result, rows.Err()
}

func (s *Store) GetStepRun(ctx context.Context, stepRunID string) (StepRun, error) {
	step, err := scanStepRun(s.db.QueryRowContext(ctx, `
		SELECT step_run_id, run_id, step_id, status, attempt_count, approval_policy,
			input_version_snapshot_json, task_cursor_json, started_at, ended_at
		FROM step_runs WHERE step_run_id = ?`, stepRunID))
	if errors.Is(err, sql.ErrNoRows) {
		return StepRun{}, domainError("STEP_RUN_NOT_FOUND", "生成步骤不存在。")
	}
	return step, err
}

func (s *Store) GetRunSnapshot(ctx context.Context, runID string) (RunSnapshot, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	steps, err := s.ListStepRuns(ctx, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	artifacts, err := s.ListArtifactsByRun(ctx, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	approval, err := s.pendingApprovalForRun(ctx, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	var cursor EventCursor
	if err := s.db.QueryRowContext(ctx, `
		SELECT p.project_event_seq, r.run_event_seq
		FROM projects p JOIN runs r ON r.project_id = p.project_id
		WHERE r.run_id = ?`, runID).Scan(&cursor.ProjectEventSeq, &cursor.RunEventSeq); err != nil {
		return RunSnapshot{}, err
	}
	snapshot := RunSnapshot{
		Run:             run,
		Steps:           steps,
		CurrentApproval: approval,
		Artifacts:       artifacts,
		EventCursor:     cursor,
	}
	snapshot.AvailableActions, err = s.runAvailableActions(ctx, s.db, run, steps, approval)
	if err != nil {
		return RunSnapshot{}, err
	}
	snapshot.PendingScriptEdit, err = s.pendingScriptEditQuery(ctx, s.db, run, steps)
	if err != nil {
		return RunSnapshot{}, err
	}
	return snapshot, nil
}

func (s *Store) GetLatestRunSnapshotByProject(ctx context.Context, projectID string) (*RunSnapshot, error) {
	var runID string
	err := s.db.QueryRowContext(ctx, `
		SELECT run_id FROM runs WHERE project_id = ?
		ORDER BY created_at DESC, run_id DESC LIMIT 1`, projectID).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	snapshot, err := s.GetRunSnapshot(ctx, runID)
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func validateRunOwnership(ctx context.Context, tx *sql.Tx, projectID, conversationID, messageID string) error {
	var projectExists int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM projects
		WHERE project_id = ? AND deleted_at IS NULL`, projectID).Scan(&projectExists); err != nil {
		return err
	}
	if projectExists == 0 {
		return domainError("PROJECT_NOT_FOUND", "作品不存在。")
	}
	var conversationProject string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM conversations WHERE conversation_id = ?`, conversationID).Scan(&conversationProject); errors.Is(err, sql.ErrNoRows) {
		return domainError("CONVERSATION_NOT_FOUND", "对话不存在。")
	} else if err != nil {
		return err
	}
	if conversationProject != projectID {
		return domainError("RESOURCE_PROJECT_MISMATCH", "对话不属于当前作品。")
	}
	var messageProject, messageConversation string
	if err := tx.QueryRowContext(ctx, `
		SELECT project_id, conversation_id FROM messages WHERE message_id = ?`, messageID).
		Scan(&messageProject, &messageConversation); errors.Is(err, sql.ErrNoRows) {
		return domainError("MESSAGE_NOT_FOUND", "启动确认消息不存在。")
	} else if err != nil {
		return err
	}
	if messageProject != projectID || messageConversation != conversationID {
		return domainError("RESOURCE_PROJECT_MISMATCH", "启动确认消息不属于当前作品和对话。")
	}
	return nil
}

func (s *Store) validateStartRunProposalTx(
	ctx context.Context,
	tx *sql.Tx,
	command StartRunCommand,
) error {
	var projectID, conversationID, confirmationMessageID, actionType, status string
	var capabilityID, capabilityVersion sql.NullString
	var inputJSON, configJSON, snapshotHash string
	var version, requiresConfirmation int
	var messageProjectID, messageConversationID, messageRole string
	err := tx.QueryRowContext(ctx, `
		SELECT pa.project_id, pa.conversation_id, pa.confirmation_message_id,
			pa.action_type, pa.version, pa.status, pa.capability_id,
			pa.capability_version, pa.input_json, pa.config_json,
			pa.snapshot_hash, pa.requires_confirmation,
			m.project_id, m.conversation_id, m.role
		FROM proposed_actions pa
		JOIN messages m ON m.message_id = pa.confirmation_message_id
		WHERE pa.proposed_action_id = ?`, command.ProposedActionID).Scan(
		&projectID,
		&conversationID,
		&confirmationMessageID,
		&actionType,
		&version,
		&status,
		&capabilityID,
		&capabilityVersion,
		&inputJSON,
		&configJSON,
		&snapshotHash,
		&requiresConfirmation,
		&messageProjectID,
		&messageConversationID,
		&messageRole,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domainError("PROPOSED_ACTION_NOT_FOUND", "待确认动作不存在。")
	}
	if err != nil {
		return err
	}
	if projectID != command.ProjectID || conversationID != command.ConversationID ||
		messageProjectID != command.ProjectID || messageConversationID != command.ConversationID {
		return domainError("RESOURCE_PROJECT_MISMATCH", "确认卡不属于当前作品和对话。")
	}
	if actionType != "start_run" || requiresConfirmation != 1 {
		return domainError("CONFIRMATION_ACTION_MISMATCH", "该确认卡不能启动生成任务。")
	}
	if status != "pending" || version != command.ProposedActionVersion ||
		confirmationMessageID != command.ConfirmationMessageID ||
		snapshotHash != command.ConfirmationSnapshotHash || messageRole != "assistant" {
		return domainError("CONFIRMATION_STALE", "确认卡已失效，请重新确认。")
	}
	if !capabilityID.Valid || !capabilityVersion.Valid ||
		capabilityID.String != command.CapabilityID ||
		capabilityVersion.String != command.CapabilityVersion {
		return domainError("CONFIRMATION_ACTION_MISMATCH", "确认的 Skill 与启动请求不一致。")
	}
	normalizedInput, err := normalizeJSON(command.Input)
	if err != nil {
		return domainError("CAPABILITY_INPUT_INVALID", "输入快照不是有效 JSON。")
	}
	normalizedConfig, err := normalizeJSON(command.Config)
	if err != nil {
		return domainError("CAPABILITY_CONFIG_INVALID", "生成配置不是有效 JSON。")
	}
	if string(normalizedInput) != inputJSON || string(normalizedConfig) != configJSON {
		return domainError("CONFIRMATION_SNAPSHOT_MISMATCH", "输入或配置已变化，请重新确认。")
	}
	return nil
}

func validateAssetReference(raw json.RawMessage) (assetInputReference, error) {
	var value assetInputReference
	if err := json.Unmarshal(raw, &value); err != nil {
		return assetInputReference{}, fmt.Errorf("材料引用不是有效 JSON")
	}
	if value.AssetID == "" || value.AssetSnapshotID == "" || value.Role == "" || value.Order < 1 {
		return assetInputReference{}, fmt.Errorf("材料引用缺少 asset_id、asset_snapshot_id、role 或有效 order")
	}
	return value, nil
}

func validateAssetOwnership(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID string, references []assetInputReference) error {
	for _, reference := range references {
		var assetProjectID, snapshotProjectID, snapshotAssetID, assetStatus, snapshotStatus string
		if err := query.QueryRowContext(ctx, `
			SELECT a.project_id, a.status, ass.project_id, ass.asset_id, ass.status
			FROM assets a
			JOIN asset_snapshots ass ON ass.asset_snapshot_id = ?
			WHERE a.asset_id = ?`,
			reference.AssetSnapshotID, reference.AssetID,
		).Scan(&assetProjectID, &assetStatus, &snapshotProjectID, &snapshotAssetID, &snapshotStatus); errors.Is(err, sql.ErrNoRows) {
			return domainError("ASSET_SNAPSHOT_NOT_FOUND", "材料或材料快照不存在。")
		} else if err != nil {
			return err
		}
		if assetProjectID != projectID || snapshotProjectID != projectID || snapshotAssetID != reference.AssetID {
			return domainError("RESOURCE_PROJECT_MISMATCH", "材料或材料快照不属于当前作品。")
		}
		if assetStatus != "available" || snapshotStatus != "available" {
			return domainError("ASSET_SOURCE_UNAVAILABLE", "材料源文件当前不可用。")
		}
	}
	return nil
}

func (s *Store) prepareRunSourceInputTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	definition *capability.CompiledDefinition,
	raw json.RawMessage,
) (sourceInputRequest, []assetInputReference, []artifactInputReference, json.RawMessage, error) {
	var source sourceInputRequest
	if definition == nil {
		return source, nil, nil, nil, domainError("CAPABILITY_VERSION_UNAVAILABLE", "能力输入绑定不可用。")
	}
	sourceType := strings.TrimSpace(definition.InputBinding.SourceType)
	assetRole := sourceAssetRole(definition)
	if sourceType == "" {
		return source, nil, nil, nil, domainError("CAPABILITY_VERSION_UNAVAILABLE", "能力未声明来源类型。")
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		return source, nil, nil, nil, domainError("CAPABILITY_INPUT_INVALID", "输入快照不是有效 JSON。")
	}
	if source.SourceType == "" {
		source.SourceType = sourceType
	}
	if source.ProjectID != projectID || source.SourceType == "" || source.UserRequestMessageID == "" {
		return source, nil, nil, nil, domainError("CAPABILITY_INPUT_INVALID", "输入缺少作品、来源或请求消息。")
	}
	if source.SourceType != sourceType {
		return source, nil, nil, nil, domainError("CAPABILITY_INPUT_INVALID", "来源类型与所选能力不匹配。")
	}
	assetSetPurpose := strings.TrimSpace(definition.InputBinding.AssetSetPurpose)
	if assetSetPurpose != "" {
		if source.AssetSetID == "" || source.AssetSetVersionID == "" || source.CollectionState != "sealed" {
			return source, nil, nil, nil, domainError("ASSET_SET_NOT_SEALED", "来源集合尚未封存，不能启动创作。")
		}
		set, err := getAssetSetSnapshotTx(ctx, tx, source.AssetSetID, source.AssetSetVersionID)
		if errors.Is(err, sql.ErrNoRows) {
			return source, nil, nil, nil, domainError("ASSET_SET_NOT_SEALED", "来源集合与封存版本不匹配或不存在。")
		}
		if err != nil {
			return source, nil, nil, nil, err
		}
		if set.AssetSet.ProjectID != projectID || set.AssetSet.Purpose != assetSetPurpose ||
			set.AssetSet.Status != "sealed" || set.AssetSet.CurrentVersionID != source.AssetSetVersionID ||
			set.Version.Status != "sealed" {
			return source, nil, nil, nil, domainError("ASSET_SET_NOT_SEALED", "来源集合版本不是当前已封存版本。")
		}
		source.Assets = make([]json.RawMessage, 0, len(set.Members))
		for _, member := range set.Members {
			if !member.Included {
				continue
			}
			var snapshotID, kind, status string
			if err := tx.QueryRowContext(ctx, `
				SELECT current_snapshot_id, kind, status FROM assets
				WHERE asset_id = ? AND project_id = ?`, member.AssetID, projectID,
			).Scan(&snapshotID, &kind, &status); err != nil {
				return source, nil, nil, nil, err
			}
			if !slices.Contains(definition.AcceptedAssetKinds, kind) || status != "available" {
				return source, nil, nil, nil, domainError("ASSET_SOURCE_UNAVAILABLE", "来源集合包含当前 Skill 不接受或不可用的材料。")
			}
			reference, err := json.Marshal(assetInputReference{
				AssetID: member.AssetID, AssetSnapshotID: snapshotID,
				Role: assetRole, Order: member.EpisodeOrder,
			})
			if err != nil {
				return source, nil, nil, nil, err
			}
			source.Assets = append(source.Assets, reference)
		}
	} else if source.AssetSetID != "" || source.AssetSetVersionID != "" || source.CollectionState != "" {
		return source, nil, nil, nil, domainError("CAPABILITY_INPUT_INVALID", "当前 Skill 未声明来源集合输入。")
	}
	if len(source.Assets) == 0 && len(source.ArtifactVersions) == 0 {
		return source, nil, nil, nil, domainError("CAPABILITY_INPUT_INVALID", "输入缺少可用材料。")
	}
	references := make([]assetInputReference, 0, len(source.Assets))
	for _, asset := range source.Assets {
		reference, err := validateAssetReference(asset)
		if err != nil {
			return source, nil, nil, nil, domainError("CAPABILITY_INPUT_INVALID", err.Error())
		}
		if reference.Role != assetRole {
			return source, nil, nil, nil, domainError("CAPABILITY_INPUT_INVALID", "材料角色与当前 Skill 输入绑定不匹配。")
		}
		references = append(references, reference)
	}
	if err := validateAssetOwnership(ctx, tx, projectID, references); err != nil {
		return source, nil, nil, nil, err
	}
	if err := validateAcceptedAssetKinds(ctx, tx, projectID, references, definition.AcceptedAssetKinds); err != nil {
		return source, nil, nil, nil, err
	}
	artifactReferences := make([]artifactInputReference, 0, len(source.ArtifactVersions))
	for _, reference := range source.ArtifactVersions {
		if reference.ArtifactVersionID == "" || reference.Role != assetRole || reference.Order < 1 {
			return source, nil, nil, nil, domainError("CAPABILITY_INPUT_INVALID", "产物来源引用不完整。")
		}
		var artifactProjectID, currentVersionID, artifactType, status string
		err := tx.QueryRowContext(ctx, `
			SELECT a.project_id, a.current_version_id, a.artifact_type, av.status
			FROM artifact_versions av JOIN artifacts a ON a.artifact_id = av.artifact_id
			WHERE av.artifact_version_id = ?`, reference.ArtifactVersionID).Scan(
			&artifactProjectID, &currentVersionID, &artifactType, &status,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return source, nil, nil, nil, domainError("ARTIFACT_VERSION_NOT_FOUND", "来源产物版本不存在。")
		}
		if err != nil {
			return source, nil, nil, nil, err
		}
		if artifactProjectID != projectID || currentVersionID != reference.ArtifactVersionID || status != "confirmed" ||
			(artifactType != "generic_document" && artifactType != "generic_table") {
			return source, nil, nil, nil, domainError("CAPABILITY_INPUT_INVALID", "来源产物不是当前可用的通用文档。")
		}
		artifactReferences = append(artifactReferences, reference)
	}
	normalized, err := json.Marshal(source)
	if err != nil {
		return source, nil, nil, nil, err
	}
	return source, references, artifactReferences, normalized, nil
}

func sourceAssetRole(definition *capability.CompiledDefinition) string {
	if definition != nil && strings.TrimSpace(definition.InputBinding.AssetRole) != "" {
		return strings.TrimSpace(definition.InputBinding.AssetRole)
	}
	return "primary_source"
}

func validateAcceptedAssetKinds(
	ctx context.Context,
	query interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	projectID string,
	references []assetInputReference,
	acceptedKinds []string,
) error {
	for _, reference := range references {
		var kind string
		if err := query.QueryRowContext(ctx, `
			SELECT kind FROM assets WHERE asset_id = ? AND project_id = ? AND deleted_at IS NULL`,
			reference.AssetID, projectID,
		).Scan(&kind); err != nil {
			return err
		}
		if !slices.Contains(acceptedKinds, kind) {
			return domainError("CAPABILITY_INPUT_INVALID", "来源材料类型不被当前 Skill 接受。")
		}
	}
	return nil
}

func jsonObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value map[string]any
	return json.Unmarshal(raw, &value) == nil && value != nil
}

func approvalSubjectHash(versionID string, version int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", versionID, version)))
	return hex.EncodeToString(sum[:])
}

func isUniqueConstraint(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint failed") || strings.Contains(text, "constraint failed")
}

func isProjectWriteRunConstraint(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint failed: runs.project_id") ||
		strings.Contains(text, "one_active_write_run_per_project")
}

func scanRun(row rowScanner) (Run, error) {
	var run Run
	var currentStep, startedAt, endedAt sql.NullString
	var configJSON, createdAt, updatedAt string
	if err := row.Scan(
		&run.RunID, &run.ProjectID, &run.ConversationID, &run.CapabilityID, &run.CapabilityVersion,
		&run.RunKind, &run.Status, &currentStep, &run.CurrentInputSnapshotVersionID,
		&run.InputSnapshotStatus, &configJSON, &startedAt, &endedAt, &createdAt, &updatedAt,
	); err != nil {
		return Run{}, err
	}
	run.CurrentStepRunID = stringPointer(currentStep)
	run.ConfigSnapshot = json.RawMessage(configJSON)
	var err error
	run.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Run{}, err
	}
	run.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return Run{}, err
	}
	run.StartedAt, err = optionalTime(startedAt)
	if err != nil {
		return Run{}, err
	}
	run.EndedAt, err = optionalTime(endedAt)
	return run, err
}

func scanStepRun(row rowScanner) (StepRun, error) {
	var step StepRun
	var inputJSON, cursorJSON string
	var startedAt, endedAt sql.NullString
	if err := row.Scan(
		&step.StepRunID, &step.RunID, &step.StepID, &step.Status, &step.AttemptCount,
		&step.ApprovalPolicy, &inputJSON, &cursorJSON, &startedAt, &endedAt,
	); err != nil {
		return StepRun{}, err
	}
	step.InputVersionSnapshot = json.RawMessage(inputJSON)
	step.TaskCursor = json.RawMessage(cursorJSON)
	var err error
	step.StartedAt, err = optionalTime(startedAt)
	if err != nil {
		return StepRun{}, err
	}
	step.EndedAt, err = optionalTime(endedAt)
	return step, err
}

func optionalTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
