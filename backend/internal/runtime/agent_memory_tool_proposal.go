package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"content-agent/backend/internal/identity"
)

type AgentMemoryToolProposal struct {
	AgentToolCallID     string          `json:"agent_tool_call_id"`
	GenerationID        string          `json:"generation_id"`
	ProjectID           string          `json:"project_id"`
	UserID              string          `json:"user_id"`
	ArgumentsHash       string          `json:"arguments_hash"`
	Arguments           json.RawMessage `json:"arguments,omitempty"`
	ApprovalID          string          `json:"approval_id"`
	ApprovalVersion     int             `json:"approval_version"`
	SubjectSnapshotHash string          `json:"subject_snapshot_hash"`
	CanApprove          bool            `json:"can_approve"`
	CanReject           bool            `json:"can_reject"`
}

func memoryToolOwnerTx(ctx context.Context, tx *sql.Tx, callID string, write bool) (string, identity.Principal, error) {
	bound, err := memoryToolBoundQuery(ctx, tx, callID)
	if err != nil || !bound {
		return "", identity.Principal{}, err
	}
	var id, projectID, workspaceID, userID string
	err = tx.QueryRowContext(ctx, `SELECT g.generation_id,g.project_id,g.workspace_id,g.user_id
		FROM agent_memory_tool_calls b JOIN agent_memory_generations g ON g.generation_id=b.generation_id
		JOIN agent_tool_calls c ON c.agent_tool_call_id=b.agent_tool_call_id AND c.project_id=g.project_id AND c.workspace_id=g.workspace_id
		WHERE b.agent_tool_call_id=?`, callID).Scan(&id, &projectID, &workspaceID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", identity.Principal{}, domainError("AGENT_MEMORY_INVALID", "Private tool ownership is inconsistent.")
	}
	if err != nil {
		return "", identity.Principal{}, err
	}
	transport, ok := identity.FromContext(ctx)
	_, delegated := AgentActivityFromContext(ctx)
	if !ok || !transport.ValidUser() || delegated {
		return "", identity.Principal{}, domainError("AUTHENTICATION_REQUIRED", "Private memory approval requires its user directly.")
	}
	user, err := instructionUserQuery(ctx, tx, projectID, write)
	if err != nil {
		return "", identity.Principal{}, err
	}
	if user.UserID != userID || user.WorkspaceID != workspaceID {
		return "", identity.Principal{}, domainError("AGENT_TOOL_NOT_FOUND", "Private tool proposal was not found.")
	}
	return id, user, nil
}

func (s *Store) memoryToolProposalTx(ctx context.Context, tx *sql.Tx, callID string) (AgentMemoryToolProposal, error) {
	var result AgentMemoryToolProposal
	id, user, err := memoryToolOwnerTx(ctx, tx, callID, false)
	if err != nil {
		return result, err
	}
	if id == "" {
		return result, domainError("AGENT_TOOL_NOT_FOUND", "Private tool proposal was not found.")
	}
	call, err := getAgentToolCallTx(ctx, tx, callID)
	if err != nil {
		return result, err
	}
	approval, err := getAgentToolApprovalForCallTx(ctx, tx, callID)
	if err != nil {
		return result, err
	}
	result = AgentMemoryToolProposal{AgentToolCallID: callID, GenerationID: id, ProjectID: call.ProjectID, UserID: user.UserID,
		ArgumentsHash: call.ArgumentsHash, ApprovalID: approval.AgentToolApprovalID, ApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash,
		CanReject: approval.Status == "pending" && user.Allows(identity.RoleEditor)}
	if approval.Status != "pending" || call.Status != "pending_approval" {
		return result, nil
	}
	job, err := memoryGenerationQuery(ctx, tx, id)
	if err != nil {
		var domain *DomainError
		if errors.As(err, &domain) {
			return result, nil
		}
		return result, err
	}
	if job.Status != "running" && job.Status != "paused" {
		return result, nil
	}
	if _, err := s.memoryGenerationSourceTx(ctx, tx, job); err != nil {
		var domain *DomainError
		if errors.As(err, &domain) || errors.Is(err, sql.ErrNoRows) {
			return result, nil
		}
		return result, err
	}
	var raw, phase string
	if err := tx.QueryRowContext(ctx, `SELECT arguments_json,phase FROM agent_memory_tool_calls WHERE agent_tool_call_id=?`, callID).Scan(&raw, &phase); err != nil {
		return result, err
	}
	if raw == "" || phase != job.Phase {
		return result, nil
	}
	_, hash, _, err := summarizeAgentToolPayload(json.RawMessage(raw), true)
	if err != nil || hash != call.ArgumentsHash {
		return result, nil
	}
	result.Arguments = json.RawMessage(raw)
	result.CanApprove = result.CanReject
	return result, nil
}

func (s *Store) GetAgentMemoryToolProposal(ctx context.Context, callID string) (AgentMemoryToolProposal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentMemoryToolProposal{}, err
	}
	defer tx.Rollback()
	return s.memoryToolProposalTx(ctx, tx, callID)
}
