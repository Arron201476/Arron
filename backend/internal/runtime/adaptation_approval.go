package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type adaptationResolutionPayload struct {
	AdaptationOptionsArtifactVersionID string `json:"adaptation_options_artifact_version_id"`
	Selection                          struct {
		SelectedOptionIDs        []string `json:"selected_option_ids"`
		CombinedMethods          []string `json:"combined_methods"`
		CustomChanges            []string `json:"custom_changes"`
		UserInstructionMessageID string   `json:"user_instruction_message_id,omitempty"`
	} `json:"selection"`
	CreationConfig map[string]any `json:"creation_config"`
}

func (s *Store) resolveAdaptationStrategyApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	command ResolveApprovalCommand,
	approval Approval,
	now time.Time,
) (RunSnapshot, error) {
	if approval.SubjectKind != "artifact_version" || len(command.ResolutionPayload) == 0 {
		return RunSnapshot{}, domainError(
			"APPROVAL_COMMAND_MISMATCH",
			"改编方法确认缺少结构化选择。",
		)
	}
	var resolution adaptationResolutionPayload
	if err := json.Unmarshal(command.ResolutionPayload, &resolution); err != nil {
		return RunSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "改编方法确认内容无法读取。")
	}
	if resolution.AdaptationOptionsArtifactVersionID != approval.SubjectRefID ||
		len(resolution.Selection.SelectedOptionIDs) == 0 {
		return RunSnapshot{}, domainError(
			"APPROVAL_SUBJECT_CHANGED",
			"改编方法选择与当前候选版本不一致。",
		)
	}

	var currentVersionID, versionStatus, artifactType, payloadJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT a.current_version_id, av.status, a.artifact_type, av.payload_json
		FROM artifact_versions av
		JOIN artifacts a ON a.artifact_id = av.artifact_id
		WHERE av.artifact_version_id = ? AND a.run_id = ? AND a.step_run_id = ?`,
		approval.SubjectRefID,
		approval.RunID,
		approval.StepRunID,
	).Scan(&currentVersionID, &versionStatus, &artifactType, &payloadJSON); err != nil {
		return RunSnapshot{}, err
	}
	if artifactType != "adaptation_options" || currentVersionID != approval.SubjectRefID ||
		versionStatus != "pending_approval" {
		return RunSnapshot{}, domainError(
			"APPROVAL_SUBJECT_CHANGED",
			"改编方法候选已经变化，请刷新后重试。",
		)
	}
	var options struct {
		Options []struct {
			OptionID string `json:"option_id"`
		} `json:"options"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &options); err != nil || len(options.Options) == 0 {
		return RunSnapshot{}, domainError("APPROVAL_SUBJECT_CHANGED", "改编方法候选无法读取。")
	}
	available := make(map[string]bool, len(options.Options))
	for _, option := range options.Options {
		available[option.OptionID] = option.OptionID != ""
	}
	selected := make(map[string]bool, len(resolution.Selection.SelectedOptionIDs))
	for _, optionID := range resolution.Selection.SelectedOptionIDs {
		if !available[optionID] || selected[optionID] {
			return RunSnapshot{}, domainError(
				"REQUEST_VALIDATION_FAILED",
				"选择的改编方法不存在或重复。",
			)
		}
		selected[optionID] = true
	}

	targetCount, ok := jsonNumberAsInt(resolution.CreationConfig["target_episode_count"])
	if !ok || targetCount < 1 || targetCount > 200 {
		return RunSnapshot{}, domainError("CAPABILITY_CONFIG_INVALID", "目标集数必须在 1 到 200 之间。")
	}
	duration, ok := resolution.CreationConfig["episode_duration_minutes"].(float64)
	if !ok || duration < 0.5 || duration > 10 {
		return RunSnapshot{}, domainError("CAPABILITY_CONFIG_INVALID", "单集时长必须在 0.5 到 10 分钟之间。")
	}
	requirements := stringSliceValue(resolution.CreationConfig["user_requirements"])
	creationEnvelope, err := json.Marshal(map[string]any{
		"config_ref": "creation",
		"payload": map[string]any{
			"target_episode_count":            targetCount,
			"episode_duration_minutes":        duration,
			"preserve_existing_episode_marks": false,
			"expansion_policy":                "confirm_if_needed",
			"user_requirements":               requirements,
		},
	})
	if err != nil {
		return RunSnapshot{}, err
	}
	decisionPayload, err := json.Marshal(map[string]any{
		"decision_type":                          "adaptation_strategy",
		"adaptation_options_artifact_version_id": approval.SubjectRefID,
		"selected_option_ids":                    resolution.Selection.SelectedOptionIDs,
		"combined_methods":                       resolution.Selection.CombinedMethods,
		"custom_changes":                         resolution.Selection.CustomChanges,
		"user_instruction_message_id":            resolution.Selection.UserInstructionMessageID,
	})
	if err != nil {
		return RunSnapshot{}, err
	}
	configID := s.newID("cfg")
	configSnapshot, err := createRunConfigSnapshotTx(
		ctx, tx, configID, approval.RunID, "creation", creationEnvelope, now,
	)
	if err != nil {
		return RunSnapshot{}, err
	}
	decision, err := s.createRunDecisionSnapshotTx(
		ctx, tx, approval.ProjectID, approval.RunID, approval.StepRunID,
		"adaptation_strategy", "artifact_version", approval.SubjectRefID,
		decisionPayload, now,
	)
	if err != nil {
		return RunSnapshot{}, err
	}

	resolvedAt := formatTime(now)
	if err := updateExactlyOne(ctx, tx, `
		UPDATE artifact_versions SET status = 'confirmed', confirmed_at = ?
		WHERE artifact_version_id = ? AND status = 'pending_approval'`,
		"改编方法候选已经变化。", resolvedAt, approval.SubjectRefID); err != nil {
		return RunSnapshot{}, err
	}
	storedResolution, err := json.Marshal(map[string]any{
		"action":                      command.Action,
		"decision_snapshot_id":        decision.DecisionSnapshotID,
		"creation_config_snapshot_id": configSnapshot.ConfigSnapshotID,
		"resolution_payload":          resolution,
		"resolved_subject_refs":       []string{approval.SubjectRefID},
	})
	if err != nil {
		return RunSnapshot{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE approvals
		SET status = 'approved', resolved_at = ?, resolution_json = ?, actor_ref = ?
		WHERE approval_request_id = ? AND status = 'pending'`,
		"改编方法确认已经变化。", resolvedAt, string(storedResolution),
		command.ActorRef, approval.ApprovalRequestID); err != nil {
		return RunSnapshot{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE step_runs SET status = 'completed', ended_at = ?
		WHERE step_run_id = ? AND status = 'waiting_approval'`,
		"改编方法步骤已经变化。", resolvedAt, approval.StepRunID); err != nil {
		return RunSnapshot{}, err
	}
	if _, _, err := s.advanceRunAfterConfirmedArtifactTx(
		ctx, tx, approval.RunID, approval.StepRunID, approval.SubjectRefID, now,
		"approved", "adaptation_selection_and_creation_config_confirmed",
	); err != nil {
		return RunSnapshot{}, err
	}
	runRef, stepRef := approval.RunID, approval.StepRunID
	if _, err := s.appendEvent(ctx, tx, approval.ProjectID, &runRef, &stepRef,
		"adaptation.decision_confirmed", "decision_snapshot", decision.DecisionSnapshotID,
		map[string]any{
			"source_artifact_version_id":  approval.SubjectRefID,
			"creation_config_snapshot_id": configSnapshot.ConfigSnapshotID,
			"target_episode_count":        targetCount,
		}); err != nil {
		return RunSnapshot{}, err
	}
	if _, err := s.appendEvent(ctx, tx, approval.ProjectID, &runRef, &stepRef,
		"config.snapshot_created", "config_snapshot", configSnapshot.ConfigSnapshotID,
		map[string]any{"config_ref": "creation", "version": configSnapshot.Version}); err != nil {
		return RunSnapshot{}, err
	}
	return s.finalizeResolvedApprovalTx(ctx, tx, command, approval, now)
}
