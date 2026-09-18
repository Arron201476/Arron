package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"content-agent/backend/internal/identity"
)

// Explicit admission seals an actor and target; workers cannot substitute the
// project owner or their own identity when they claim or complete an attempt.
type revisionExecutionAuthorization struct {
	WorkspaceID        string  `json:"workspace_id"`
	UserID             string  `json:"user_id"`
	ConversationID     string  `json:"conversation_id"`
	RequestMessageID   string  `json:"request_message_id"`
	TargetResolutionID string  `json:"target_resolution_id"`
	ArtifactID         *string `json:"artifact_id"`
	BaseVersionID      *string `json:"base_artifact_version_id"`
	InstructionHash    string  `json:"instruction_hash"`
	RequestVersion     int     `json:"request_version"`
	Source             string  `json:"source"`
}

func (s *Store) authorizeRevisionExecutionTx(ctx context.Context, tx *sql.Tx, request RevisionRequest, source string) error {
	var principal identity.Principal
	var err error
	if activity, active := AgentActivityFromContext(ctx); active {
		if source != "created" || activity.ProjectID != request.ProjectID {
			return domainError("ROLE_FORBIDDEN", "Agent 执行不能代替用户重新授权返修。")
		}
		principal, err = s.resolveAgentActivityPrincipalTx(ctx, tx, activity)
		if err != nil {
			return err
		}
	} else {
		var present bool
		principal, present = identity.UserFromContext(ctx)
		if !present {
			return domainError("AUTHENTICATION_REQUIRED", "返修执行授权需要明确的用户身份。")
		}
	}
	if err := authorizeArtifactCommandTx(identity.WithPrincipal(ctx, principal), tx, request.ProjectID, ""); err != nil {
		return err
	}
	grant := revisionExecutionAuthorization{
		WorkspaceID: principal.WorkspaceID, UserID: principal.UserID,
		ConversationID: request.ConversationID, RequestMessageID: request.RequestMessageID, TargetResolutionID: request.TargetResolutionID,
		ArtifactID: request.ArtifactID, BaseVersionID: request.BaseVersionID,
		InstructionHash: sha256Hex([]byte(request.Instruction)), RequestVersion: request.Version, Source: source,
	}
	_, err = s.appendEvent(ctx, tx, request.ProjectID, nil, nil, "revision_request.execution_authorized", "revision_request", request.RevisionRequestID, grant)
	return err
}

func revisionExecutionPrincipalQuery(ctx context.Context, query rowQueryer, request RevisionRequest) (identity.Principal, error) {
	var encoded string
	err := query.QueryRowContext(ctx, `SELECT payload_json FROM events WHERE project_id=? AND subject_type='revision_request'
		AND subject_id=? AND event_type='revision_request.execution_authorized' ORDER BY project_event_seq DESC LIMIT 1`, request.ProjectID, request.RevisionRequestID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Principal{}, domainError("REVISION_EXECUTION_OWNER_REQUIRED", "该修改请求缺少执行授权，请确认目标后重新执行。")
	}
	if err != nil {
		return identity.Principal{}, err
	}
	var grant revisionExecutionAuthorization
	if json.Unmarshal([]byte(encoded), &grant) != nil || grant.WorkspaceID == "" || grant.UserID == "" ||
		grant.ConversationID != request.ConversationID || grant.RequestMessageID != request.RequestMessageID || grant.TargetResolutionID != request.TargetResolutionID ||
		!sameOptionalID(grant.ArtifactID, request.ArtifactID) || !sameOptionalID(grant.BaseVersionID, request.BaseVersionID) ||
		grant.InstructionHash != sha256Hex([]byte(request.Instruction)) || grant.RequestVersion < 1 || grant.RequestVersion > request.Version ||
		(grant.Source != "created" && grant.Source != "target_confirmation" && grant.Source != "execute") {
		return identity.Principal{}, domainError("REVISION_EXECUTION_OWNER_INVALID", "返修执行授权与当前请求不一致，请重新确认执行。")
	}
	principal, err := resolvePrincipalQuery(ctx, query, identity.Principal{Kind: identity.KindUser, WorkspaceID: grant.WorkspaceID, UserID: grant.UserID, Role: identity.RoleViewer, AuthMethod: "revision_authorization"})
	if err != nil {
		return identity.Principal{}, err
	}
	if _, err := projectFilesWorkspace(identity.WithPrincipal(ctx, principal), query, request.ProjectID, true); err != nil {
		return identity.Principal{}, err
	}
	return principal, nil
}

func (s *Store) revisionAttemptPrincipalTx(ctx context.Context, tx *sql.Tx, request RevisionRequest) (identity.Principal, error) {
	principal, err := revisionExecutionPrincipalQuery(ctx, tx, request)
	if err != nil {
		return identity.Principal{}, err
	}
	if activity, active := AgentActivityFromContext(ctx); active {
		if activity.ProjectID != request.ProjectID {
			return identity.Principal{}, domainError("PROJECT_NOT_FOUND", "作品不存在。")
		}
		caller, err := s.resolveAgentActivityPrincipalTx(ctx, tx, activity)
		if err != nil {
			return identity.Principal{}, err
		}
		if caller.UserID != principal.UserID || caller.WorkspaceID != principal.WorkspaceID {
			return identity.Principal{}, domainError("ROLE_FORBIDDEN", "Agent 执行身份与返修授权用户不一致。")
		}
	} else if caller, present := identity.FromContext(ctx); present {
		if !caller.ValidUser() || caller.UserID != principal.UserID || caller.WorkspaceID != principal.WorkspaceID {
			return identity.Principal{}, domainError("ROLE_FORBIDDEN", "当前执行身份与返修授权用户不一致。")
		}
	}
	return principal, nil
}

func (s *Store) RevisionExecutionPrincipal(ctx context.Context, requestID string) (identity.Principal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return identity.Principal{}, err
	}
	defer tx.Rollback()
	request, err := getRevisionRequestQuery(ctx, tx, requestID)
	if err != nil {
		return identity.Principal{}, err
	}
	return s.revisionAttemptPrincipalTx(ctx, tx, request)
}

// Failed authorization must not indefinitely block newer queue entries. This
// maintenance operation records failure only; it never grants execution rights.
func (s *Store) FailUnauthorizedQueuedRevision(ctx context.Context, requestID string, expectedVersion int) (bool, error) {
	if _, active := AgentActivityFromContext(ctx); active {
		return false, domainError("ROLE_FORBIDDEN", "仅后台维护可结算返修队列。")
	}
	if _, present := identity.FromContext(ctx); present {
		return false, domainError("ROLE_FORBIDDEN", "仅后台维护可结算返修队列。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	request, err := getRevisionRequestQuery(ctx, tx, requestID)
	if err != nil {
		return false, err
	}
	if request.Version != expectedVersion || (request.Status != "queued" && request.Status != "waiting_safe_checkpoint") {
		return false, nil
	}
	_, ownerErr := revisionExecutionPrincipalQuery(ctx, tx, request)
	if ownerErr == nil {
		return false, nil
	}
	var domain *DomainError
	if !errors.As(ownerErr, &domain) {
		return false, ownerErr
	}
	switch domain.Code {
	case "REVISION_EXECUTION_OWNER_REQUIRED", "REVISION_EXECUTION_OWNER_INVALID", "AUTHENTICATION_REQUIRED", "WORKSPACE_ACCESS_DENIED", "ROLE_FORBIDDEN", "PROJECT_NOT_FOUND", "IDENTITY_INVALID":
	default:
		return false, ownerErr
	}
	now := s.now()
	if err := updateExactlyOne(ctx, tx, `UPDATE revision_requests SET status='failed', failure_code=?, version=version+1,updated_at=?,finished_at=?
		WHERE revision_request_id=? AND version=? AND status IN ('queued','waiting_safe_checkpoint')`, "修改请求已变化。", domain.Code, formatTime(now), formatTime(now), requestID, expectedVersion); err != nil {
		return false, err
	}
	if _, err := s.appendEvent(ctx, tx, request.ProjectID, nil, nil, "revision_request.failed", "revision_request", requestID, map[string]any{"failure_code": domain.Code}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
