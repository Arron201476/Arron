package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
)

func validateContinuationOptions(payload json.RawMessage) error {
	var value struct {
		Options []struct {
			OptionID string `json:"option_id"`
		} `json:"options"`
	}
	if err := json.Unmarshal(payload, &value); err != nil || len(value.Options) != 5 {
		return domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "续写方向必须恰好包含 5 个候选。")
	}
	seen := make(map[string]bool, 5)
	for _, option := range value.Options {
		if option.OptionID == "" || seen[option.OptionID] {
			return domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "续写方向编号缺失或重复。")
		}
		seen[option.OptionID] = true
	}
	for index := 1; index <= 5; index++ {
		if !seen[fmt.Sprintf("option_%d", index)] {
			return domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "续写方向必须使用 option_1 到 option_5。")
		}
	}
	return nil
}

func validateContinuationScriptSelection(decisions []ContextDecisionSnapshot, payload json.RawMessage) error {
	selectedOptionID := ""
	for _, decision := range decisions {
		if decision.DecisionType != "single_option_selection" {
			continue
		}
		var value struct {
			SelectedOptionID string `json:"selected_option_id"`
		}
		if err := json.Unmarshal(decision.Payload, &value); err != nil || value.SelectedOptionID == "" || selectedOptionID != "" {
			return domainError("CONTEXT_LINEAGE_CONFLICT", "续写正文缺少唯一有效的方向决策。")
		}
		selectedOptionID = value.SelectedOptionID
	}
	if selectedOptionID == "" {
		return domainError("CONTEXT_REQUIRED_UPSTREAM_MISSING", "续写正文缺少用户确认的方向。")
	}
	var output struct {
		SelectedOptionID string `json:"selected_option_id"`
	}
	if err := json.Unmarshal(payload, &output); err != nil || output.SelectedOptionID != selectedOptionID {
		return domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "续写正文使用的方向与用户选择不一致。")
	}
	return nil
}

func validateContinuationScriptLength(config, payload json.RawMessage) error {
	var envelope struct {
		Payload struct {
			TargetLengthChars int `json:"target_length_chars"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(config, &envelope); err != nil || envelope.Payload.TargetLengthChars <= 0 {
		return domainError("RUN_STATE_CONFLICT", "续写配置缺少有效的目标字数。")
	}
	var output struct {
		ScriptText string `json:"script_text"`
	}
	if err := json.Unmarshal(payload, &output); err != nil || strings.TrimSpace(output.ScriptText) == "" {
		return domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "续写正文无法读取。")
	}
	actual := 0
	for _, character := range output.ScriptText {
		if !unicode.IsSpace(character) {
			actual++
		}
	}
	target := envelope.Payload.TargetLengthChars
	minimum := (target*9 + 9) / 10
	if actual < minimum {
		return domainError(
			"OUTPUT_LENGTH_INSUFFICIENT",
			fmt.Sprintf(
				"续写正文未达到目标字数：目标约 %d 字，至少需 %d 字，当前 %d 字。请保持所选方向和完整结局并扩写全文。",
				target,
				minimum,
				actual,
			),
		)
	}
	return nil
}

type singleOptionResolutionPayload struct {
	OptionsArtifactVersionID string `json:"options_artifact_version_id"`
	SelectedOptionID         string `json:"selected_option_id"`
}

func (s *Store) resolveSingleOptionApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	command ResolveApprovalCommand,
	approval Approval,
	now time.Time,
) (RunSnapshot, error) {
	if approval.SubjectKind != "artifact_version" || len(command.ResolutionPayload) == 0 {
		return RunSnapshot{}, domainError("APPROVAL_COMMAND_MISMATCH", "候选确认缺少结构化单选结果。")
	}
	var resolution singleOptionResolutionPayload
	if err := json.Unmarshal(command.ResolutionPayload, &resolution); err != nil {
		return RunSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "候选选择无法读取。")
	}
	resolution.SelectedOptionID = strings.TrimSpace(resolution.SelectedOptionID)
	if resolution.OptionsArtifactVersionID != approval.SubjectRefID || resolution.SelectedOptionID == "" {
		return RunSnapshot{}, domainError("APPROVAL_SUBJECT_CHANGED", "选择结果与当前候选版本不一致。")
	}

	var currentVersionID, versionStatus, payloadJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT a.current_version_id, av.status, av.payload_json
		FROM artifact_versions av
		JOIN artifacts a ON a.artifact_id = av.artifact_id
		WHERE av.artifact_version_id = ? AND a.run_id = ? AND a.step_run_id = ?`,
		approval.SubjectRefID, approval.RunID, approval.StepRunID,
	).Scan(&currentVersionID, &versionStatus, &payloadJSON); err != nil {
		return RunSnapshot{}, err
	}
	if currentVersionID != approval.SubjectRefID || versionStatus != "pending_approval" {
		return RunSnapshot{}, domainError("APPROVAL_SUBJECT_CHANGED", "候选版本已经变化，请刷新后重试。")
	}

	var set struct {
		Options []json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &set); err != nil || len(set.Options) == 0 {
		return RunSnapshot{}, domainError("APPROVAL_SUBJECT_CHANGED", "候选内容无法读取。")
	}
	selectedIndex := -1
	seen := map[string]bool{}
	for index, option := range set.Options {
		var identity struct {
			OptionID string `json:"option_id"`
		}
		if err := json.Unmarshal(option, &identity); err != nil || strings.TrimSpace(identity.OptionID) == "" || seen[identity.OptionID] {
			return RunSnapshot{}, domainError("APPROVAL_SUBJECT_CHANGED", "候选编号缺失或重复。")
		}
		seen[identity.OptionID] = true
		if identity.OptionID == resolution.SelectedOptionID {
			selectedIndex = index
		}
	}
	if selectedIndex < 0 {
		return RunSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "选择的候选不存在。")
	}

	decisionPayload, err := json.Marshal(map[string]any{
		"decision_type":               "single_option_selection",
		"options_artifact_version_id": approval.SubjectRefID,
		"selected_option_id":          resolution.SelectedOptionID,
		"selected_option":             set.Options[selectedIndex],
	})
	if err != nil {
		return RunSnapshot{}, err
	}
	decision, err := s.createRunDecisionSnapshotTx(
		ctx, tx, approval.ProjectID, approval.RunID, approval.StepRunID,
		"single_option_selection", "artifact_version", approval.SubjectRefID,
		decisionPayload, now,
	)
	if err != nil {
		return RunSnapshot{}, err
	}

	resolvedAt := formatTime(now)
	if err := updateExactlyOne(ctx, tx, `
		UPDATE artifact_versions SET status = 'confirmed', confirmed_at = ?
		WHERE artifact_version_id = ? AND status = 'pending_approval'`,
		"候选版本已经变化。", resolvedAt, approval.SubjectRefID); err != nil {
		return RunSnapshot{}, err
	}
	storedResolution, err := json.Marshal(map[string]any{
		"action":                command.Action,
		"decision_snapshot_id":  decision.DecisionSnapshotID,
		"selected_option_id":    resolution.SelectedOptionID,
		"resolved_subject_refs": []string{approval.SubjectRefID},
	})
	if err != nil {
		return RunSnapshot{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE approvals
		SET status = 'approved', resolved_at = ?, resolution_json = ?, actor_ref = ?
		WHERE approval_request_id = ? AND status = 'pending'`,
		"候选确认已经变化。", resolvedAt, string(storedResolution), command.ActorRef, approval.ApprovalRequestID); err != nil {
		return RunSnapshot{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE step_runs SET status = 'completed', ended_at = ?
		WHERE step_run_id = ? AND status = 'waiting_approval'`,
		"候选确认步骤已经变化。", resolvedAt, approval.StepRunID); err != nil {
		return RunSnapshot{}, err
	}
	if _, _, err := s.advanceRunAfterConfirmedArtifactTx(
		ctx, tx, approval.RunID, approval.StepRunID, approval.SubjectRefID, now,
		"approved",
	); err != nil {
		return RunSnapshot{}, err
	}
	runRef, stepRef := approval.RunID, approval.StepRunID
	if _, err := s.appendEvent(ctx, tx, approval.ProjectID, &runRef, &stepRef,
		"selection.option_selected", "decision_snapshot", decision.DecisionSnapshotID,
		map[string]any{"source_artifact_version_id": approval.SubjectRefID, "selected_option_id": resolution.SelectedOptionID}); err != nil {
		return RunSnapshot{}, err
	}
	return s.finalizeResolvedApprovalTx(ctx, tx, command, approval, now)
}
