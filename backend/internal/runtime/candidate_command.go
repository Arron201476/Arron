package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"content-agent/backend/internal/identity"
)

func authorizeCandidateCommandTx(ctx context.Context, tx *sql.Tx, projectID, scope string) error {
	if _, active := AgentActivityFromContext(ctx); active {
		return domainError("ROLE_FORBIDDEN", "候选稿确认需要用户提交。")
	}
	if principal, present := identity.FromContext(ctx); present && !principal.ValidUser() {
		return domainError("ROLE_FORBIDDEN", "候选稿操作需要有效用户身份。")
	}
	return authorizeArtifactCommandTx(ctx, tx, projectID, scope)
}

func decodeFinalPreviewReceiptTx(ctx context.Context, tx *sql.Tx, cached json.RawMessage, command CreateFinalSelectionPreviewCommand) (FinalSelectionPreviewResult, error) {
	receipt, err := decodeIdempotentResult[FinalSelectionPreviewResult](cached)
	if err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	old := receipt.Preview
	mismatch := func() (FinalSelectionPreviewResult, error) {
		return FinalSelectionPreviewResult{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识不属于当前最终稿预览。")
	}
	if old.ProjectID != command.ProjectID || old.CandidateID != command.CandidateID || old.Status != "pending" || !sameOptionalID(old.ExpectedCurrentSelectionID, command.ExpectedCurrentSelectionID) || !sameOptionalID(old.CurrentSelectionID, command.ExpectedCurrentSelectionID) || old.ProposedSelectionNo < 1 {
		return mismatch()
	}
	if command.ExpectedArtifactVersionID != "" && old.PreviewHash != finalSelectionPreviewHash(command.ProjectID, command.CandidateID, command.ExpectedArtifactVersionID, old.CurrentSelectionID, old.ProposedSelectionNo) {
		return mismatch()
	}
	current, err := scanFinalSelectionPreview(tx.QueryRowContext(ctx, `SELECT final_selection_preview_id,project_id,candidate_id,approval_request_id,current_selection_id,expected_current_selection_id,proposed_selection_no,preview_hash,status,created_at,resolved_at FROM final_selection_previews WHERE final_selection_preview_id=?`, old.FinalSelectionPreviewID))
	if errors.Is(err, sql.ErrNoRows) {
		return mismatch()
	}
	if err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	if current.ProjectID != old.ProjectID || current.CandidateID != old.CandidateID || current.ApprovalRequestID != old.ApprovalRequestID || current.PreviewHash != old.PreviewHash || current.ProposedSelectionNo != old.ProposedSelectionNo || !sameOptionalID(current.CurrentSelectionID, old.CurrentSelectionID) || !sameOptionalID(current.ExpectedCurrentSelectionID, old.ExpectedCurrentSelectionID) {
		return mismatch()
	}
	approval, err := getApprovalTx(ctx, tx, current.ApprovalRequestID)
	if err != nil {
		return FinalSelectionPreviewResult{}, err
	}
	if approval.ProjectID != command.ProjectID || approval.Scope != "final_selection" || approval.SubjectKind != "script_candidate" || approval.SubjectRefID != command.CandidateID || approval.SubjectSnapshotHash != current.PreviewHash || receipt.Approval.ApprovalRequestID != approval.ApprovalRequestID || receipt.Approval.ProjectID != approval.ProjectID || receipt.Approval.RunID != approval.RunID || receipt.Approval.SubjectRefID != approval.SubjectRefID || receipt.Approval.SubjectSnapshotHash != approval.SubjectSnapshotHash {
		return mismatch()
	}
	if current.Status == "pending" {
		candidate, _, err := getSelectableCandidateTx(ctx, tx, command.ProjectID, command.CandidateID)
		if err != nil {
			return FinalSelectionPreviewResult{}, err
		}
		if approval.Status != "pending" || current.PreviewHash != finalSelectionPreviewHash(command.ProjectID, command.CandidateID, candidate.ScriptsArtifactVersionID, current.CurrentSelectionID, current.ProposedSelectionNo) {
			return FinalSelectionPreviewResult{}, domainError("FINAL_SELECTION_PREVIEW_EXPIRED", "最终稿预览已变化，请重新预览。")
		}
	} else if current.Status != "consumed" || approval.Status != "resolved" {
		return FinalSelectionPreviewResult{}, domainError("FINAL_SELECTION_PREVIEW_EXPIRED", "最终稿预览已失效，请重新预览。")
	}
	return FinalSelectionPreviewResult{Preview: current, Approval: approval}, nil
}

func decodeFinalSelectionReceiptTx(ctx context.Context, tx *sql.Tx, cached json.RawMessage, command ConfirmFinalSelectionCommand) (FinalSelectionResult, error) {
	receipt, err := decodeIdempotentResult[FinalSelectionResult](cached)
	if err != nil {
		return FinalSelectionResult{}, err
	}
	selection, candidate, approval := receipt.Selection, receipt.Candidate, receipt.Approval
	mismatch := func() (FinalSelectionResult, error) {
		return FinalSelectionResult{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识不属于当前最终稿确认。")
	}
	if selection.ProjectID != command.ProjectID || selection.CandidateID != command.CandidateID || selection.Status != "active" || !sameOptionalID(selection.ReplacedSelectionID, command.ExpectedCurrentSelectionID) || candidate.ProjectID != command.ProjectID || candidate.CandidateID != command.CandidateID || approval.ApprovalRequestID != selection.ApprovalRequestID || approval.ProjectID != command.ProjectID || approval.SubjectRefID != command.CandidateID || approval.SubjectSnapshotHash != command.PreviewHash || command.PreviewHash != finalSelectionPreviewHash(command.ProjectID, command.CandidateID, candidate.ScriptsArtifactVersionID, command.ExpectedCurrentSelectionID, selection.SelectionNo) {
		return mismatch()
	}
	var bound bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM final_selections fs
		JOIN final_selection_previews p ON p.approval_request_id=fs.approval_request_id AND p.project_id=fs.project_id AND p.candidate_id=fs.candidate_id
		JOIN approvals a ON a.approval_request_id=fs.approval_request_id AND a.project_id=fs.project_id
		WHERE fs.final_selection_id=? AND fs.project_id=? AND fs.candidate_id=? AND fs.selection_no=? AND fs.replaced_selection_id IS ?
		AND p.preview_hash=? AND p.proposed_selection_no=fs.selection_no AND p.expected_current_selection_id IS ? AND p.status='consumed'
		AND a.status='resolved' AND a.scope='final_selection' AND a.subject_kind='script_candidate' AND a.subject_ref_id=fs.candidate_id AND a.subject_snapshot_hash=p.preview_hash
		AND json_extract(a.resolution_json,'$.action')='select_final' AND json_extract(a.resolution_json,'$.final_selection_id')=fs.final_selection_id)`, selection.FinalSelectionID, command.ProjectID, command.CandidateID, selection.SelectionNo, nullableString(command.ExpectedCurrentSelectionID), command.PreviewHash, nullableString(command.ExpectedCurrentSelectionID)).Scan(&bound)
	if err != nil {
		return FinalSelectionResult{}, err
	}
	if !bound {
		return mismatch()
	}
	// A later selection may replace this one. Return the original committed receipt
	// without making its candidate final again; the caller reloads current state.
	return receipt, nil
}
