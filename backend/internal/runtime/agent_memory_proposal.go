package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"content-agent/backend/internal/identity"
)

type AgentMemoryProposal struct {
	AgentToolCallID     string              `json:"agent_tool_call_id"`
	ProjectID           string              `json:"project_id"`
	UserID              string              `json:"user_id"`
	ArgumentsHash       string              `json:"arguments_hash"`
	ApprovalID          string              `json:"approval_id"`
	ApprovalVersion     int                 `json:"approval_version"`
	SubjectSnapshotHash string              `json:"subject_snapshot_hash"`
	ExpectedVersion     int                 `json:"expected_version"`
	Current             AgentMemoryDocument `json:"current"`
	Files               map[string]string   `json:"files"`
	Available           bool                `json:"available"`
	CanApprove          bool                `json:"can_approve"`
	CanReject           bool                `json:"can_reject"`
}

func (s *Store) memoryProposalTx(ctx context.Context, tx *sql.Tx, callID string) (AgentMemoryProposal, error) {
	result := AgentMemoryProposal{AgentToolCallID: callID, Files: map[string]string{}}
	var workspace, raw, session string
	var source AgentMemorySnapshot
	err := tx.QueryRowContext(ctx, `SELECT p.workspace_id,p.project_id,p.user_id,p.arguments_hash,p.arguments_json,p.session_id,p.expected_version,s.activity_key,s.version,s.content_hash,s.read_enabled
		FROM agent_memory_publications p JOIN agent_memory_snapshots s ON s.activity_key=p.activity_key AND s.workspace_id=p.workspace_id AND s.project_id=p.project_id AND s.user_id=p.user_id
		WHERE p.agent_tool_call_id=?`, callID).Scan(&workspace, &result.ProjectID, &result.UserID, &result.ArgumentsHash, &raw, &session, &result.ExpectedVersion, &source.ActivityKey, &source.Version, &source.ContentHash, &source.ReadEnabled)
	if err != nil {
		return AgentMemoryProposal{}, err
	}
	user, err := instructionUserQuery(ctx, tx, result.ProjectID, false)
	if err != nil {
		return AgentMemoryProposal{}, err
	}
	if user.UserID != result.UserID || user.WorkspaceID != workspace {
		return AgentMemoryProposal{}, domainError("WORKSPACE_ACCESS_DENIED", "Only the memory owner can view this proposal.")
	}
	call, err := getAgentToolCallTx(ctx, tx, callID)
	if err != nil {
		return AgentMemoryProposal{}, err
	}
	approval, err := getAgentToolApprovalForCallTx(ctx, tx, callID)
	if err != nil {
		return AgentMemoryProposal{}, err
	}
	if call.ToolID != publishAgentMemoryTool || call.ProjectID != result.ProjectID || call.WorkspaceID != workspace || call.ArgumentsHash != result.ArgumentsHash {
		return AgentMemoryProposal{}, domainError("AGENT_MEMORY_INVALID", "Memory proposal identity is invalid.")
	}
	result.ApprovalID, result.ApprovalVersion, result.SubjectSnapshotHash = approval.AgentToolApprovalID, approval.Version, approval.SubjectSnapshotHash
	result.Current, err = agentMemoryQuery(ctx, tx, user, result.ProjectID, 0)
	if err != nil {
		return AgentMemoryProposal{}, err
	}
	result.CanReject = approval.Status == "pending" && user.Allows(identity.RoleEditor)
	// Stale and legacy intents remain rejectable without reopening their private archive.
	if approval.Status != "pending" || result.Current.Version != result.ExpectedVersion || raw == "" || session == "" {
		return result, nil
	}
	source.ProjectID, source.UserID, source.WorkspaceID = result.ProjectID, result.UserID, workspace
	if err := validateAgentMemoryReferenceQuery(ctx, tx, source); err != nil {
		var domain *DomainError
		if errors.As(err, &domain) && domain.Code == "AGENT_MEMORY_CONFLICT" {
			return result, nil
		}
		return AgentMemoryProposal{}, err
	}
	args, err := decodeMemoryPublicationArguments(json.RawMessage(raw))
	if err != nil {
		return result, nil
	}
	_, hash, _, err := summarizeAgentToolPayload(json.RawMessage(raw), true)
	if err != nil || hash != result.ArgumentsHash || args.ExpectedVersion != result.ExpectedVersion {
		return result, nil
	}
	var bound bool
	workspaceKey := source.ActivityKey
	if strings.HasPrefix(source.ActivityKey, "memory:") {
		proposal, err := s.memoryToolProposalTx(ctx, tx, callID)
		if err != nil {
			return AgentMemoryProposal{}, err
		}
		if !proposal.CanApprove || source.ActivityKey != "memory:"+proposal.GenerationID {
			return result, nil
		}
		var phase string
		if err := tx.QueryRowContext(ctx, `SELECT phase FROM agent_memory_tool_calls WHERE agent_tool_call_id=?`, callID).Scan(&phase); err != nil {
			return AgentMemoryProposal{}, err
		}
		if phase != "consolidation" {
			return result, nil
		}
		workspaceKey += ":consolidation"
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_workspace_leases WHERE session_id=? AND activity_key=? AND workspace_id=? AND project_id=? AND user_id=?)`, session, workspaceKey, workspace, result.ProjectID, result.UserID).Scan(&bound); err != nil {
		return AgentMemoryProposal{}, err
	}
	if !bound {
		return result, nil
	}
	files, err := memoryPublicationFilesTx(ctx, tx, session, args)
	if err != nil {
		var domain *DomainError
		if errors.Is(err, sql.ErrNoRows) || errors.As(err, &domain) {
			return result, nil
		}
		return AgentMemoryProposal{}, err
	}
	result.Files, result.Available, result.CanApprove = files, true, result.CanReject
	return result, nil
}

func (s *Store) GetAgentMemoryProposal(ctx context.Context, callID string) (AgentMemoryProposal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentMemoryProposal{}, err
	}
	defer tx.Rollback()
	return s.memoryProposalTx(ctx, tx, callID)
}
