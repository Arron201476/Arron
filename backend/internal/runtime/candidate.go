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
	"time"
)

func (s *Store) ListScriptCandidates(ctx context.Context, projectID string) ([]ScriptCandidate, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			candidate_id, project_id, source_run_id, source_capability_id,
			scripts_artifact_version_id, status, label, supersedes_candidate_id,
			created_at, updated_at
		FROM script_candidates
		WHERE project_id = ?
		ORDER BY created_at DESC, candidate_id ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ScriptCandidate, 0)
	for rows.Next() {
		candidate, err := scanScriptCandidate(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}

func (s *Store) GetScriptCandidate(ctx context.Context, candidateID string) (ScriptCandidate, error) {
	candidate, err := scanScriptCandidate(s.db.QueryRowContext(ctx, `
		SELECT
			candidate_id, project_id, source_run_id, source_capability_id,
			scripts_artifact_version_id, status, label, supersedes_candidate_id,
			created_at, updated_at
		FROM script_candidates
		WHERE candidate_id = ?`,
		candidateID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return ScriptCandidate{}, domainError("SCRIPT_CANDIDATE_NOT_FOUND", "候选剧本不存在。")
	}
	return candidate, err
}

func (s *Store) GetCurrentFinalSelection(
	ctx context.Context,
	projectID string,
) (*FinalSelection, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	selection, err := scanFinalSelection(s.db.QueryRowContext(ctx, `
		SELECT
			final_selection_id, project_id, candidate_id, approval_request_id,
			selection_no, status, selected_at, replaced_selection_id
		FROM final_selections
		WHERE project_id = ? AND status = 'active'`,
		projectID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &selection, nil
}

func (s *Store) CreateFinalSelectionPreview(
	ctx context.Context,
	command CreateFinalSelectionPreviewCommand,
) (FinalSelectionPreviewResult, error) {
	if command.ProjectID == "" || command.CandidateID == "" {
		return FinalSelectionPreviewResult{}, domainError(
			"REQUEST_VALIDATION_FAILED",
			"最终稿预览缺少作品或候选剧本。",
		)
	}
	if command.ActorRef == "" {
		command.ActorRef = actorRefFromContext(ctx)
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	defer tx.Rollback()
	if err := authorizeCandidateCommandTx(ctx, tx, command.ProjectID, command.Scope); err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	if command.Scope == "" {
		command.Scope = command.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	if hit {
		return decodeFinalPreviewReceiptTx(ctx, tx, cached, command)
	}
	candidate, stepRunID, err := getSelectableCandidateTx(
		ctx,
		tx,
		command.ProjectID,
		command.CandidateID,
	)
	if err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	if command.ExpectedArtifactVersionID != "" && candidate.ScriptsArtifactVersionID != command.ExpectedArtifactVersionID {
		return FinalSelectionPreviewResult{}, domainError("FINAL_SELECTION_CONFLICT", "候选剧本版本已变化，请刷新后重新预览。")
	}
	current, err := currentFinalSelectionTx(ctx, tx, command.ProjectID)
	if err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	currentID := finalSelectionID(current)
	if !sameOptionalID(currentID, command.ExpectedCurrentSelectionID) {
		return FinalSelectionPreviewResult{}, domainError(
			"FINAL_SELECTION_CONFLICT",
			"当前最终稿已经变化，请刷新后重新预览。",
		)
	}
	if current != nil && current.CandidateID == candidate.CandidateID {
		return FinalSelectionPreviewResult{}, domainError(
			"FINAL_SELECTION_CONFLICT",
			"该候选剧本已经是当前最终稿。",
		)
	}
	var selectionNo int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(selection_no), 0) + 1
		FROM final_selections WHERE project_id = ?`,
		command.ProjectID,
	).Scan(&selectionNo); err != nil {
		return FinalSelectionPreviewResult{}, err
	}

	expiredApprovalIDs, err := expireFinalSelectionPreviewsTx(
		ctx,
		tx,
		command.ProjectID,
		now,
	)
	if err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	previewID := s.newID("fsp")
	approvalID := s.newID("apr")
	previewHash := finalSelectionPreviewHash(
		command.ProjectID,
		candidate.CandidateID,
		candidate.ScriptsArtifactVersionID,
		currentID,
		selectionNo,
	)
	options := []string{"select_final"}
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO approvals(
			approval_request_id, project_id, run_id, step_run_id, scope, status, version,
			title, reason, options_json, subject_kind, subject_ref_id, subject_version,
			subject_snapshot_hash, requested_at
		) VALUES(?, ?, ?, ?, 'final_selection', 'pending', 1, ?, ?, ?,
			'script_candidate', ?, 1, ?, ?)`,
		approvalID,
		command.ProjectID,
		candidate.SourceRunID,
		stepRunID,
		"确认设为最终稿",
		"确认后将该候选剧本设为作品当前唯一最终稿，历史最终稿仍会保留。",
		string(optionsJSON),
		candidate.CandidateID,
		previewHash,
		formatTime(now),
	); err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO final_selection_previews(
			final_selection_preview_id, project_id, candidate_id, approval_request_id,
			current_selection_id, expected_current_selection_id, proposed_selection_no,
			preview_hash, status, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
		previewID,
		command.ProjectID,
		candidate.CandidateID,
		approvalID,
		nullableString(currentID),
		nullableString(command.ExpectedCurrentSelectionID),
		selectionNo,
		previewHash,
		formatTime(now),
	); err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	preview := FinalSelectionPreview{
		FinalSelectionPreviewID:    previewID,
		ProjectID:                  command.ProjectID,
		CandidateID:                candidate.CandidateID,
		ApprovalRequestID:          approvalID,
		CurrentSelectionID:         currentID,
		ExpectedCurrentSelectionID: command.ExpectedCurrentSelectionID,
		ProposedSelectionNo:        selectionNo,
		PreviewHash:                previewHash,
		Status:                     "pending",
		CreatedAt:                  now,
	}
	approval := Approval{
		ApprovalRequestID:   approvalID,
		ProjectID:           command.ProjectID,
		RunID:               candidate.SourceRunID,
		StepRunID:           stepRunID,
		Scope:               "final_selection",
		Status:              "pending",
		Version:             1,
		Title:               "确认设为最终稿",
		Reason:              "确认后将该候选剧本设为作品当前唯一最终稿，历史最终稿仍会保留。",
		Options:             options,
		SubjectKind:         "script_candidate",
		SubjectRefID:        candidate.CandidateID,
		SubjectVersion:      1,
		SubjectSnapshotHash: previewHash,
		RequestedAt:         now,
	}
	for _, expiredApprovalID := range expiredApprovalIDs {
		if _, err := s.appendEvent(
			ctx,
			tx,
			command.ProjectID,
			nil,
			nil,
			"approval.expired",
			"approval",
			expiredApprovalID,
			map[string]any{"reason": "final_selection_preview_replaced"},
		); err != nil {
			return FinalSelectionPreviewResult{}, err
		}
	}
	if _, err := s.appendEvent(
		ctx,
		tx,
		command.ProjectID,
		nil,
		nil,
		"final_selection.requested",
		"script_candidate",
		candidate.CandidateID,
		map[string]any{
			"final_selection_preview_id": previewID,
			"approval_request_id":        approvalID,
			"current_selection_id":       currentID,
			"proposed_selection_no":      selectionNo,
		},
	); err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	result := FinalSelectionPreviewResult{Preview: preview, Approval: approval}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, result, now); err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	return result, nil
}

func (s *Store) ConfirmFinalSelection(
	ctx context.Context,
	command ConfirmFinalSelectionCommand,
) (FinalSelectionResult, error) {
	if !command.Confirmed {
		return FinalSelectionResult{}, domainError(
			"REQUIRED_CONFIRMATION_MISSING",
			"设为最终稿前需要用户明确确认。",
		)
	}
	if command.ProjectID == "" || command.CandidateID == "" || command.PreviewHash == "" {
		return FinalSelectionResult{}, domainError(
			"REQUEST_VALIDATION_FAILED",
			"最终稿确认缺少作品、候选剧本或预览快照。",
		)
	}
	if command.ActorRef == "" {
		command.ActorRef = actorRefFromContext(ctx)
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FinalSelectionResult{}, err
	}
	defer tx.Rollback()
	if err := authorizeCandidateCommandTx(ctx, tx, command.ProjectID, command.Scope); err != nil {
		return FinalSelectionResult{}, err
	}
	if command.Scope == "" {
		command.Scope = command.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return FinalSelectionResult{}, err
	}
	if hit {
		return decodeFinalSelectionReceiptTx(ctx, tx, cached, command)
	}
	candidate, _, err := getSelectableCandidateTx(
		ctx,
		tx,
		command.ProjectID,
		command.CandidateID,
	)
	if err != nil {
		return FinalSelectionResult{}, err
	}
	preview, err := pendingFinalSelectionPreviewTx(
		ctx,
		tx,
		command.ProjectID,
		command.CandidateID,
		command.PreviewHash,
	)
	if err != nil {
		return FinalSelectionResult{}, err
	}
	if preview.Status != "pending" {
		return FinalSelectionResult{}, domainError(
			"FINAL_SELECTION_PREVIEW_EXPIRED",
			"最终稿预览已经失效，请重新预览。",
		)
	}
	if preview.PreviewHash != finalSelectionPreviewHash(command.ProjectID, candidate.CandidateID, candidate.ScriptsArtifactVersionID, preview.CurrentSelectionID, preview.ProposedSelectionNo) {
		return FinalSelectionResult{}, domainError("FINAL_SELECTION_CONFLICT", "候选剧本已不是预览时的版本，请重新预览。")
	}
	if !sameOptionalID(
		preview.ExpectedCurrentSelectionID,
		command.ExpectedCurrentSelectionID,
	) {
		return FinalSelectionResult{}, domainError(
			"FINAL_SELECTION_CONFLICT",
			"确认请求与预览时的最终稿版本不一致。",
		)
	}
	current, err := currentFinalSelectionTx(ctx, tx, command.ProjectID)
	if err != nil {
		return FinalSelectionResult{}, err
	}
	if !sameOptionalID(finalSelectionID(current), preview.CurrentSelectionID) {
		return FinalSelectionResult{}, domainError(
			"FINAL_SELECTION_CONFLICT",
			"当前最终稿已经变化，请重新预览。",
		)
	}
	if current != nil && current.CandidateID == candidate.CandidateID {
		return FinalSelectionResult{}, domainError(
			"FINAL_SELECTION_CONFLICT",
			"该候选剧本已经是当前最终稿。",
		)
	}
	approval, err := getApprovalTx(ctx, tx, preview.ApprovalRequestID)
	if err != nil {
		return FinalSelectionResult{}, err
	}
	if approval.Status != "pending" || approval.ProjectID != command.ProjectID || approval.RunID != candidate.SourceRunID ||
		approval.Scope != "final_selection" ||
		approval.SubjectKind != "script_candidate" ||
		approval.SubjectRefID != candidate.CandidateID ||
		approval.SubjectSnapshotHash != command.PreviewHash ||
		!slices.Contains(approval.Options, "select_final") {
		return FinalSelectionResult{}, domainError(
			"FINAL_SELECTION_PREVIEW_EXPIRED",
			"最终稿确认对象已经变化，请重新预览。",
		)
	}

	selectionID := s.newID("fsel")
	if current != nil {
		if err := updateExactlyOne(ctx, tx, `
			UPDATE final_selections SET status = 'replaced'
			WHERE final_selection_id = ? AND project_id = ? AND status = 'active'`,
			"当前最终稿已经变化。",
			current.FinalSelectionID,
			command.ProjectID,
		); err != nil {
			return FinalSelectionResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE script_candidates
			SET status = 'historical_final', updated_at = ?
			WHERE candidate_id = ? AND project_id = ? AND status = 'final'`,
			formatTime(now),
			current.CandidateID,
			command.ProjectID,
		); err != nil {
			return FinalSelectionResult{}, err
		}
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE script_candidates
		SET status = 'final', updated_at = ?
		WHERE candidate_id = ? AND project_id = ?`,
		"候选剧本已经变化。",
		formatTime(now),
		candidate.CandidateID,
		command.ProjectID,
	); err != nil {
		return FinalSelectionResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO final_selections(
			final_selection_id, project_id, candidate_id, approval_request_id,
			selection_no, status, selected_at, replaced_selection_id
		) VALUES(?, ?, ?, ?, ?, 'active', ?, ?)`,
		selectionID,
		command.ProjectID,
		candidate.CandidateID,
		approval.ApprovalRequestID,
		preview.ProposedSelectionNo,
		formatTime(now),
		nullableString(finalSelectionID(current)),
	); err != nil {
		if isUniqueConstraint(err) {
			return FinalSelectionResult{}, domainError(
				"FINAL_SELECTION_CONFLICT",
				"当前最终稿已经变化，请刷新后重试。",
			)
		}
		return FinalSelectionResult{}, err
	}
	resolution, err := json.Marshal(map[string]any{
		"action":                "select_final",
		"candidate_id":          candidate.CandidateID,
		"final_selection_id":    selectionID,
		"replaced_selection_id": finalSelectionID(current),
	})
	if err != nil {
		return FinalSelectionResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE approvals
		SET status = 'resolved', resolved_at = ?, resolution_json = ?, actor_ref = ?
		WHERE approval_request_id = ? AND status = 'pending'`,
		"最终稿确认已经处理。",
		formatTime(now),
		string(resolution),
		command.ActorRef,
		approval.ApprovalRequestID,
	); err != nil {
		return FinalSelectionResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE final_selection_previews
		SET status = 'consumed', resolved_at = ?
		WHERE final_selection_preview_id = ? AND status = 'pending'`,
		"最终稿预览已经失效。",
		formatTime(now),
		preview.FinalSelectionPreviewID,
	); err != nil {
		return FinalSelectionResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE projects
		SET version = version + 1, current_focus_artifact_version_id = ?, updated_at = ?
		WHERE project_id = ? AND deleted_at IS NULL`,
		"作品已经变化。",
		candidate.ScriptsArtifactVersionID,
		formatTime(now),
		command.ProjectID,
	); err != nil {
		return FinalSelectionResult{}, err
	}
	if _, err := s.appendEvent(
		ctx,
		tx,
		command.ProjectID,
		nil,
		nil,
		"approval.resolved",
		"approval",
		approval.ApprovalRequestID,
		map[string]any{"action": "select_final"},
	); err != nil {
		return FinalSelectionResult{}, err
	}
	if _, err := s.appendEvent(
		ctx,
		tx,
		command.ProjectID,
		nil,
		nil,
		"final_selection.changed",
		"final_selection",
		selectionID,
		map[string]any{
			"candidate_id":          candidate.CandidateID,
			"selection_no":          preview.ProposedSelectionNo,
			"replaced_selection_id": finalSelectionID(current),
		},
	); err != nil {
		return FinalSelectionResult{}, err
	}

	resolvedAt := now
	actorRef := command.ActorRef
	approval.Status = "resolved"
	approval.ResolvedAt = &resolvedAt
	approval.Resolution = resolution
	approval.ActorRef = &actorRef
	candidate.Status = "final"
	candidate.UpdatedAt = now
	selection := FinalSelection{
		FinalSelectionID:    selectionID,
		ProjectID:           command.ProjectID,
		CandidateID:         candidate.CandidateID,
		ApprovalRequestID:   approval.ApprovalRequestID,
		SelectionNo:         preview.ProposedSelectionNo,
		Status:              "active",
		SelectedAt:          now,
		ReplacedSelectionID: finalSelectionID(current),
	}
	result := FinalSelectionResult{
		Selection: selection,
		Candidate: candidate,
		Approval:  approval,
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, result, now); err != nil {
		return FinalSelectionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return FinalSelectionResult{}, err
	}
	return result, nil
}

func (s *Store) createScriptCandidateTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	scriptsArtifactVersionID string,
	now time.Time,
) (ScriptCandidate, bool, error) {
	var projectID, sourceRunID, capabilityID, artifactType, versionStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT
			a.project_id, a.run_id, a.capability_id, a.artifact_type, av.status
		FROM artifact_versions av
		JOIN artifacts a ON a.artifact_id = av.artifact_id
		WHERE av.artifact_version_id = ?`,
		scriptsArtifactVersionID,
	).Scan(
		&projectID,
		&sourceRunID,
		&capabilityID,
		&artifactType,
		&versionStatus,
	); errors.Is(err, sql.ErrNoRows) {
		return ScriptCandidate{}, false, domainError(
			"COMPLETION_INVARIANT_FAILED",
			"剧本聚合版本不存在。",
		)
	} else if err != nil {
		return ScriptCandidate{}, false, err
	}
	if projectID != run.ProjectID ||
		sourceRunID != run.RunID ||
		capabilityID != run.CapabilityID ||
		artifactType != "scripts" ||
		(versionStatus != "confirmed" && versionStatus != "superseded") {
		return ScriptCandidate{}, false, domainError(
			"COMPLETION_INVARIANT_FAILED",
			"剧本聚合版本不满足候选稿创建条件。",
		)
	}
	existing, err := scanScriptCandidate(tx.QueryRowContext(ctx, `
		SELECT candidate_id, project_id, source_run_id, source_capability_id,
			scripts_artifact_version_id, status, label, supersedes_candidate_id,
			created_at, updated_at
		FROM script_candidates WHERE scripts_artifact_version_id = ?`, scriptsArtifactVersionID))
	if err == nil {
		if existing.ProjectID != run.ProjectID || existing.SourceRunID != run.RunID || existing.SourceCapabilityID != run.CapabilityID {
			return ScriptCandidate{}, false, domainError("COMPLETION_INVARIANT_FAILED", "候选稿与剧本聚合版本归属不一致。")
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ScriptCandidate{}, false, err
	}
	if versionStatus != "confirmed" {
		return ScriptCandidate{}, false, domainError("COMPLETION_INVARIANT_FAILED", "新候选稿必须引用已确认的剧本聚合版本。")
	}
	var previousID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT c.candidate_id FROM script_candidates c
		JOIN artifact_versions av ON av.artifact_version_id = c.scripts_artifact_version_id
		WHERE c.source_run_id = ? AND c.project_id = ?
		ORDER BY av.version DESC, c.created_at DESC, c.candidate_id
		LIMIT 1`, run.RunID, run.ProjectID).Scan(&previousID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ScriptCandidate{}, false, err
	}
	var candidateNo int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) + 1 FROM script_candidates WHERE project_id = ?`,
		run.ProjectID,
	).Scan(&candidateNo); err != nil {
		return ScriptCandidate{}, false, err
	}
	candidate := ScriptCandidate{
		CandidateID:              s.newID("cand"),
		ProjectID:                run.ProjectID,
		SourceRunID:              run.RunID,
		SourceCapabilityID:       run.CapabilityID,
		ScriptsArtifactVersionID: scriptsArtifactVersionID,
		Status:                   "candidate",
		Label:                    fmt.Sprintf("候选剧本 %d", candidateNo),
		SupersedesCandidateID:    stringPointer(previousID),
		CreatedAt:                now,
		UpdatedAt:                now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO script_candidates(
			candidate_id, project_id, source_run_id, source_capability_id,
			scripts_artifact_version_id, status, label, supersedes_candidate_id,
			created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, 'candidate', ?, ?, ?, ?)`,
		candidate.CandidateID,
		candidate.ProjectID,
		candidate.SourceRunID,
		candidate.SourceCapabilityID,
		candidate.ScriptsArtifactVersionID,
		candidate.Label,
		candidate.SupersedesCandidateID,
		formatTime(now),
		formatTime(now),
	); err != nil {
		return ScriptCandidate{}, false, err
	}
	return candidate, true, nil
}

func getSelectableCandidateTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	candidateID string,
) (ScriptCandidate, string, error) {
	candidate, err := scanScriptCandidate(tx.QueryRowContext(ctx, `
		SELECT
			c.candidate_id, c.project_id, c.source_run_id, c.source_capability_id,
			c.scripts_artifact_version_id, c.status, c.label, c.supersedes_candidate_id,
			c.created_at, c.updated_at
		FROM script_candidates c
		JOIN projects p ON p.project_id = c.project_id
		WHERE c.candidate_id = ? AND c.project_id = ? AND p.deleted_at IS NULL`,
		candidateID,
		projectID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return ScriptCandidate{}, "", domainError(
			"SCRIPT_CANDIDATE_NOT_FOUND",
			"候选剧本不存在。",
		)
	}
	if err != nil {
		return ScriptCandidate{}, "", err
	}
	var stepRunID, artifactType, versionStatus, sourceRunID string
	if err := tx.QueryRowContext(ctx, `
		SELECT a.step_run_id, a.artifact_type, av.status, a.run_id
		FROM artifact_versions av
		JOIN artifacts a ON a.artifact_id = av.artifact_id
		WHERE av.artifact_version_id = ? AND a.project_id = ?`,
		candidate.ScriptsArtifactVersionID,
		projectID,
	).Scan(&stepRunID, &artifactType, &versionStatus, &sourceRunID); errors.Is(err, sql.ErrNoRows) {
		return ScriptCandidate{}, "", domainError(
			"SCRIPT_CANDIDATE_INVALID",
			"候选剧本引用的聚合版本不存在。",
		)
	} else if err != nil {
		return ScriptCandidate{}, "", err
	}
	if artifactType != "scripts" || sourceRunID != candidate.SourceRunID || (candidate.Status != "candidate" && candidate.Status != "final" && candidate.Status != "historical_final") ||
		(versionStatus != "confirmed" && versionStatus != "superseded") {
		return ScriptCandidate{}, "", domainError(
			"SCRIPT_CANDIDATE_INVALID",
			"候选剧本引用的聚合版本当前不可选择。",
		)
	}
	return candidate, stepRunID, nil
}

func currentFinalSelectionTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
) (*FinalSelection, error) {
	selection, err := scanFinalSelection(tx.QueryRowContext(ctx, `
		SELECT
			final_selection_id, project_id, candidate_id, approval_request_id,
			selection_no, status, selected_at, replaced_selection_id
		FROM final_selections
		WHERE project_id = ? AND status = 'active'`,
		projectID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &selection, nil
}

func pendingFinalSelectionPreviewTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	candidateID string,
	previewHash string,
) (FinalSelectionPreview, error) {
	preview, err := scanFinalSelectionPreview(tx.QueryRowContext(ctx, `
		SELECT
			final_selection_preview_id, project_id, candidate_id, approval_request_id,
			current_selection_id, expected_current_selection_id, proposed_selection_no,
			preview_hash, status, created_at, resolved_at
		FROM final_selection_previews
		WHERE project_id = ? AND candidate_id = ? AND preview_hash = ?
		ORDER BY created_at DESC, final_selection_preview_id DESC
		LIMIT 1`,
		projectID,
		candidateID,
		previewHash,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return FinalSelectionPreview{}, domainError(
			"FINAL_SELECTION_PREVIEW_NOT_FOUND",
			"最终稿预览不存在。",
		)
	}
	return preview, err
}

func expireFinalSelectionPreviewsTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	now time.Time,
) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT approval_request_id
		FROM final_selection_previews
		WHERE project_id = ? AND status = 'pending'
		ORDER BY created_at ASC, final_selection_preview_id ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	var approvalIDs []string
	for rows.Next() {
		var approvalID string
		if err := rows.Scan(&approvalID); err != nil {
			rows.Close()
			return nil, err
		}
		approvalIDs = append(approvalIDs, approvalID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(approvalIDs) == 0 {
		return []string{}, nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE final_selection_previews
		SET status = 'expired', resolved_at = ?
		WHERE project_id = ? AND status = 'pending'`,
		formatTime(now),
		projectID,
	); err != nil {
		return nil, err
	}
	for _, approvalID := range approvalIDs {
		if _, err := tx.ExecContext(ctx, `
			UPDATE approvals
			SET status = 'expired', resolved_at = ?
			WHERE approval_request_id = ? AND status = 'pending'`,
			formatTime(now),
			approvalID,
		); err != nil {
			return nil, err
		}
	}
	return approvalIDs, nil
}

func finalSelectionPreviewHash(
	projectID string,
	candidateID string,
	scriptsArtifactVersionID string,
	currentSelectionID *string,
	selectionNo int,
) string {
	currentID := ""
	if currentSelectionID != nil {
		currentID = *currentSelectionID
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\n%s\n%s\n%s\n%d",
		projectID,
		candidateID,
		scriptsArtifactVersionID,
		currentID,
		selectionNo,
	)))
	return hex.EncodeToString(sum[:])
}

func finalSelectionID(selection *FinalSelection) *string {
	if selection == nil {
		return nil
	}
	value := selection.FinalSelectionID
	return &value
}

func sameOptionalID(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func scanScriptCandidate(row rowScanner) (ScriptCandidate, error) {
	var candidate ScriptCandidate
	var supersedes sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(
		&candidate.CandidateID,
		&candidate.ProjectID,
		&candidate.SourceRunID,
		&candidate.SourceCapabilityID,
		&candidate.ScriptsArtifactVersionID,
		&candidate.Status,
		&candidate.Label,
		&supersedes,
		&createdAt,
		&updatedAt,
	); err != nil {
		return ScriptCandidate{}, err
	}
	candidate.SupersedesCandidateID = stringPointer(supersedes)
	var err error
	candidate.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return ScriptCandidate{}, err
	}
	candidate.UpdatedAt, err = parseTime(updatedAt)
	return candidate, err
}

func scanFinalSelection(row rowScanner) (FinalSelection, error) {
	var selection FinalSelection
	var replaced sql.NullString
	var selectedAt string
	if err := row.Scan(
		&selection.FinalSelectionID,
		&selection.ProjectID,
		&selection.CandidateID,
		&selection.ApprovalRequestID,
		&selection.SelectionNo,
		&selection.Status,
		&selectedAt,
		&replaced,
	); err != nil {
		return FinalSelection{}, err
	}
	selection.ReplacedSelectionID = stringPointer(replaced)
	parsed, err := parseTime(selectedAt)
	if err != nil {
		return FinalSelection{}, err
	}
	selection.SelectedAt = parsed
	return selection, nil
}

func scanFinalSelectionPreview(row rowScanner) (FinalSelectionPreview, error) {
	var preview FinalSelectionPreview
	var current, expected, resolvedAt sql.NullString
	var createdAt string
	if err := row.Scan(
		&preview.FinalSelectionPreviewID,
		&preview.ProjectID,
		&preview.CandidateID,
		&preview.ApprovalRequestID,
		&current,
		&expected,
		&preview.ProposedSelectionNo,
		&preview.PreviewHash,
		&preview.Status,
		&createdAt,
		&resolvedAt,
	); err != nil {
		return FinalSelectionPreview{}, err
	}
	preview.CurrentSelectionID = stringPointer(current)
	preview.ExpectedCurrentSelectionID = stringPointer(expected)
	var err error
	preview.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return FinalSelectionPreview{}, err
	}
	preview.ResolvedAt, err = optionalTime(resolvedAt)
	return preview, err
}
