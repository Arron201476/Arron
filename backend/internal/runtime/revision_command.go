package runtime

import (
	"context"
	"database/sql"
	"encoding/json"

	"content-agent/backend/internal/identity"
)

func authorizeRevisionStartTx(ctx context.Context, tx *sql.Tx, projectID, scope string) error {
	if _, active := AgentActivityFromContext(ctx); active {
		return domainError("ROLE_FORBIDDEN", "请通过已授权的返修工具提交修改，不能直接确认用户的返修命令。")
	}
	if principal, present := identity.FromContext(ctx); present && !principal.ValidUser() {
		return domainError("ROLE_FORBIDDEN", "返修命令需要有效的用户身份。")
	}
	return authorizeArtifactCommandTx(ctx, tx, projectID, scope)
}

func (s *Store) authorizeRevisionAttemptTx(ctx context.Context, tx *sql.Tx, request RevisionRequest) error {
	_, err := s.revisionAttemptPrincipalTx(ctx, tx, request)
	return err
}

func revisionStartReceiptMatches(receipt, request RevisionRequest) bool {
	return receipt.RevisionRequestID == request.RevisionRequestID && receipt.ProjectID == request.ProjectID && receipt.ConversationID == request.ConversationID && receipt.RequestMessageID == request.RequestMessageID && receipt.TargetResolutionID == request.TargetResolutionID && sameOptionalID(receipt.ArtifactID, request.ArtifactID) && sameOptionalID(receipt.BaseVersionID, request.BaseVersionID) && receipt.Version <= request.Version
}

func validateRevisionTargetTx(ctx context.Context, tx *sql.Tx, request RevisionRequest) error {
	if request.ArtifactID == nil || request.BaseVersionID == nil {
		return domainError("TARGET_NOT_FOUND", "修改请求尚未定位。")
	}
	var bound bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifacts a
		JOIN artifact_versions av ON av.artifact_id=a.artifact_id
		JOIN target_resolutions tr ON tr.artifact_id=a.artifact_id AND tr.artifact_version_id=av.artifact_version_id
		WHERE a.project_id=? AND a.artifact_id=? AND av.artifact_version_id=?
		AND tr.target_resolution_id=? AND tr.project_id=a.project_id AND tr.conversation_id=? AND tr.request_message_id=? AND tr.status='resolved')`,
		request.ProjectID, *request.ArtifactID, *request.BaseVersionID, request.TargetResolutionID, request.ConversationID, request.RequestMessageID).Scan(&bound)
	if err == nil && !bound {
		return domainError("REVISION_CONTEXT_INVALID", "返修目标、基线版本或作品归属已变化。")
	}
	return err
}

func (s *Store) revisionCompletionTargetTx(ctx context.Context, tx *sql.Tx, requestID, attemptID string) (RevisionRequest, error) {
	request, err := getRevisionRequestQuery(ctx, tx, requestID)
	if err != nil {
		return RevisionRequest{}, err
	}
	if err := s.authorizeRevisionAttemptTx(ctx, tx, request); err != nil {
		return RevisionRequest{}, err
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM revision_attempts ra
		WHERE ra.revision_request_id=? AND ra.revision_attempt_id=? AND ra.status='running'
		AND ra.attempt_no=(SELECT MAX(attempt_no) FROM revision_attempts WHERE revision_request_id=ra.revision_request_id))`,
		requestID, attemptID).Scan(&active); err != nil {
		return RevisionRequest{}, err
	}
	if !active || request.Status != "running" {
		return RevisionRequest{}, domainError("RUN_STATE_CONFLICT", "该返修尝试已结束或已被替代。")
	}
	if err := validateRevisionTargetTx(ctx, tx, request); err != nil {
		return RevisionRequest{}, err
	}
	artifact, err := getArtifactQuery(ctx, tx, *request.ArtifactID)
	if err != nil {
		return RevisionRequest{}, err
	}
	if artifact.CurrentVersionID != *request.BaseVersionID {
		return RevisionRequest{}, domainError("REVISION_BASE_VERSION_CONFLICT", "目标产物已有新版本，请重新定位。")
	}
	return request, nil
}

// Admission is durable before the existing worker claims an attempt. Replaying
// the admission after a failed attempt must not enqueue another model call.
func (s *Store) RequestRevisionExecution(ctx context.Context, command RequestRevisionExecutionCommand) (RevisionRequest, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RevisionRequest{}, err
	}
	defer tx.Rollback()
	request, err := getRevisionRequestQuery(ctx, tx, command.RevisionRequestID)
	if err != nil {
		return RevisionRequest{}, err
	}
	if err := authorizeRevisionStartTx(ctx, tx, request.ProjectID, command.Scope); err != nil {
		return RevisionRequest{}, err
	}
	if command.Scope == "" {
		command.Scope = request.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return RevisionRequest{}, err
	}
	if hit {
		receipt, err := decodeIdempotentResult[RevisionRequest](cached)
		if err != nil {
			return RevisionRequest{}, err
		}
		if !revisionStartReceiptMatches(receipt, request) || (receipt.Status != "queued" && receipt.Status != "waiting_safe_checkpoint") || command.ExpectedVersion > 0 && receipt.Version != command.ExpectedVersion+1 {
			return RevisionRequest{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识不属于当前返修执行。")
		}
		return request, nil
	}
	if command.ExpectedVersion > 0 && command.ExpectedVersion != request.Version {
		return RevisionRequest{}, domainError("REVISION_VERSION_CONFLICT", "修改请求版本已变化。")
	}
	if request.Status != "queued" && request.Status != "failed" && request.Status != "waiting_safe_checkpoint" {
		return RevisionRequest{}, domainError("REVISION_STATE_CONFLICT", "修改请求当前不能再次执行。")
	}
	if err := validateRevisionTargetTx(ctx, tx, request); err != nil {
		return RevisionRequest{}, err
	}
	status := "queued"
	if request.Status == "waiting_safe_checkpoint" {
		status = request.Status
	}
	now := s.now()
	if err := updateExactlyOne(ctx, tx, `UPDATE revision_requests SET status=?,version=version+1,failure_code=NULL,finished_at=NULL,updated_at=? WHERE revision_request_id=? AND version=?`, "修改请求已变化。", status, formatTime(now), request.RevisionRequestID, request.Version); err != nil {
		return RevisionRequest{}, err
	}
	request.Status, request.Version, request.UpdatedAt = status, request.Version+1, now
	request.FailureCode, request.FinishedAt = nil, nil
	if err := s.authorizeRevisionExecutionTx(ctx, tx, request, "execute"); err != nil {
		return RevisionRequest{}, err
	}
	if _, err := s.appendEvent(ctx, tx, request.ProjectID, nil, nil, "revision_request.queued", "revision_request", request.RevisionRequestID, map[string]any{"status": status}); err != nil {
		return RevisionRequest{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, request, now); err != nil {
		return RevisionRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return RevisionRequest{}, err
	}
	return request, nil
}

func revisionReceiptMatches(receipt, request RevisionRequest, expectedVersion int, status string) bool {
	return receipt.RevisionRequestID == request.RevisionRequestID &&
		receipt.ProjectID == request.ProjectID && receipt.ConversationID == request.ConversationID &&
		receipt.Status == status && request.Status == status &&
		receipt.Version == expectedVersion+1 && receipt.Version == request.Version
}

func decodeRevisionAcceptReceiptTx(ctx context.Context, tx *sql.Tx, cached json.RawMessage, request RevisionRequest, command AcceptRevisionCommand) (RevisionAcceptResult, error) {
	receipt, err := decodeIdempotentResult[RevisionAcceptResult](cached)
	if err != nil {
		return RevisionAcceptResult{}, err
	}
	mismatch := func() (RevisionAcceptResult, error) {
		return RevisionAcceptResult{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识的回执不属于当前修改稿接受操作。")
	}
	if !revisionReceiptMatches(receipt.Revision, request, command.ExpectedVersion, "accepted") ||
		request.ArtifactID == nil || request.BaseVersionID == nil {
		return mismatch()
	}
	version := receipt.Version.ArtifactVersion
	if version.ArtifactID != *request.ArtifactID || version.BaseVersionID == nil || *version.BaseVersionID != *request.BaseVersionID {
		return mismatch()
	}
	var bound bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events e
		JOIN artifact_versions av ON av.artifact_version_id = json_extract(e.payload_json, '$.artifact_version_id')
		JOIN artifacts a ON a.artifact_id = av.artifact_id AND a.project_id = e.project_id
		WHERE e.project_id = ? AND e.event_type = 'revision_request.accepted'
		AND e.subject_type = 'revision_request' AND e.subject_id = ?
		AND av.artifact_version_id = ? AND av.artifact_id = ? AND av.base_version_id = ? AND av.version = ?)`,
		request.ProjectID, request.RevisionRequestID, version.ArtifactVersionID,
		*request.ArtifactID, *request.BaseVersionID, version.Version).Scan(&bound)
	if err == nil && !bound {
		return mismatch()
	}
	return receipt, err
}
