package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"content-agent/backend/internal/identity"
)

func authorizeApprovalMutationTx(ctx context.Context, tx *sql.Tx, approval Approval, scope string) error {
	var workspaceID string
	err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM projects WHERE project_id = ? AND deleted_at IS NULL`, approval.ProjectID).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return domainError("APPROVAL_NOT_FOUND", "确认请求不存在。")
	}
	if err != nil {
		return err
	}
	if principal, ok := identity.UserFromContext(ctx); ok {
		if principal.WorkspaceID != workspaceID {
			return domainError("APPROVAL_NOT_FOUND", "确认请求不存在。")
		}
		if !principal.Allows(identity.RoleEditor) {
			return domainError("ROLE_FORBIDDEN", "当前角色不能处理确认请求。")
		}
		if err := validateExecutionOwnerTx(ctx, tx, executionToolState{WorkspaceID: workspaceID, UserID: principal.UserID}); err != nil {
			return err
		}
	}
	if scope != "" && scope != approval.ProjectID {
		return domainError("REQUEST_VALIDATION_FAILED", "确认请求范围与作品不一致。")
	}
	return nil
}

func approvalReceiptMismatch() error {
	return domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识的回执不属于当前确认操作。")
}

func matchesResolvedApproval(approval Approval, version int, hash, action string) bool {
	var resolution struct {
		Action string `json:"action"`
	}
	return approval.Status == "approved" && approval.Version == version && approval.SubjectSnapshotHash == hash &&
		json.Unmarshal(approval.Resolution, &resolution) == nil && resolution.Action == action
}

func decodeApprovalReceiptTx(ctx context.Context, tx *sql.Tx, cached json.RawMessage, approval Approval, command ResolveApprovalCommand) (RunSnapshot, error) {
	receipt, err := decodeIdempotentResult[RunSnapshot](cached)
	if err != nil {
		return RunSnapshot{}, err
	}
	if receipt.Run.ProjectID != approval.ProjectID || receipt.Run.RunID != approval.RunID ||
		!matchesResolvedApproval(approval, command.ExpectedApprovalVersion, command.SubjectSnapshotHash, command.Action) {
		return RunSnapshot{}, approvalReceiptMismatch()
	}
	// The original finalizer records the resolved approval before taking its event cursor.
	var resolvedID string
	err = tx.QueryRowContext(ctx, `SELECT subject_id FROM events
		WHERE project_id = ? AND run_id = ? AND event_type = 'approval.resolved'
		AND subject_type = 'approval' AND project_event_seq <= ?
		ORDER BY project_event_seq DESC LIMIT 1`, approval.ProjectID, approval.RunID, receipt.EventCursor.ProjectEventSeq).Scan(&resolvedID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && resolvedID != approval.ApprovalRequestID {
		return RunSnapshot{}, approvalReceiptMismatch()
	}
	return receipt, err
}

func decodeApprovalRegenerationReceiptTx(ctx context.Context, tx *sql.Tx, cached json.RawMessage, approval Approval, command RequestApprovalRegenerationCommand) (ApprovalRegenerationResult, error) {
	receipt, err := decodeIdempotentResult[ApprovalRegenerationResult](cached)
	if err != nil {
		return ApprovalRegenerationResult{}, err
	}
	if receipt.RunSnapshot.Run.ProjectID != approval.ProjectID || receipt.RunSnapshot.Run.RunID != approval.RunID ||
		receipt.ImpactReview.ProjectID != approval.ProjectID || receipt.ImpactReview.RunID != approval.RunID ||
		receipt.RegenerationPlan.ProjectID != approval.ProjectID || receipt.RegenerationPlan.RunID != approval.RunID ||
		receipt.RegenerationPlan.RegenerationPlanID == "" || receipt.RegenerationPlan.ImpactReviewID != receipt.ImpactReview.ImpactReviewID ||
		!matchesResolvedApproval(approval, command.ExpectedApprovalVersion, command.SubjectSnapshotHash, command.Action) {
		return ApprovalRegenerationResult{}, approvalReceiptMismatch()
	}
	var linked bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE project_id = ? AND run_id = ?
		AND event_type = 'approval.resolved' AND subject_type = 'approval' AND subject_id = ?
		AND json_extract(payload_json, '$.regeneration_plan_id') = ?)`,
		approval.ProjectID, approval.RunID, approval.ApprovalRequestID, receipt.RegenerationPlan.RegenerationPlanID).Scan(&linked)
	if err == nil && !linked {
		return ApprovalRegenerationResult{}, approvalReceiptMismatch()
	}
	return receipt, err
}

func decodeApprovalRevisionReceiptTx(ctx context.Context, tx *sql.Tx, cached json.RawMessage, approval Approval) (RevisionRequest, error) {
	receipt, err := decodeIdempotentResult[RevisionRequest](cached)
	if err != nil {
		return RevisionRequest{}, err
	}
	if receipt.ProjectID != approval.ProjectID || receipt.RevisionRequestID == "" {
		return RevisionRequest{}, approvalReceiptMismatch()
	}
	var linked bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE project_id = ?
		AND event_type = 'revision_request.created' AND subject_type = 'revision_request' AND subject_id = ?
		AND json_extract(payload_json, '$.approval_request_id') = ?)`,
		approval.ProjectID, receipt.RevisionRequestID, approval.ApprovalRequestID).Scan(&linked)
	if err == nil && !linked {
		return RevisionRequest{}, approvalReceiptMismatch()
	}
	return receipt, err
}
