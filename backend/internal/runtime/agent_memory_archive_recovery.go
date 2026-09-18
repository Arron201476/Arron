package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"content-agent/backend/internal/identity"
)

// Recovery authenticates the independent service and durable original source,
// never a resurrected execution token or a delegated model tool.
func (s *Store) RecoverAgentMemoryArchive(ctx context.Context, command AgentMemoryRollout, consentRevision int) (AgentMemoryRolloutReceipt, error) {
	if err := memoryGenerationService(ctx); err != nil {
		return AgentMemoryRolloutReceipt{}, err
	}
	if consentRevision < 1 {
		return AgentMemoryRolloutReceipt{}, domainError("AGENT_MEMORY_CONFLICT", "Memory archive recovery requires its original consent revision.")
	}
	return s.saveAgentMemoryRollout(ctx, command, &consentRevision, true)
}

func (s *Store) memoryArchiveRecoverySourceTx(ctx context.Context, tx *sql.Tx, command AgentMemoryRollout) (AgentMemorySnapshot, error) {
	var empty AgentMemorySnapshot
	if err := memoryGenerationService(ctx); err != nil {
		return empty, err
	}
	// The shared saver has already validated the bounded immutable envelope.
	var envelope struct {
		Terminal struct {
			State string `json:"terminal_state"`
		} `json:"terminal_metadata"`
		Interruptions []json.RawMessage `json:"interruptions"`
	}
	if json.Unmarshal([]byte(command.RolloutJSONL), &envelope) != nil || envelope.Terminal.State != "completed" || len(envelope.Interruptions) != 0 {
		return empty, domainError("AGENT_MEMORY_ARCHIVE_SOURCE_REVOKED", "Only a completed SDK result can be recovered for archival.")
	}
	kind, id, ok := strings.Cut(command.ActivityKey, ":")
	if !ok || id == "" {
		return empty, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Memory archive source is invalid.")
	}
	activity := AgentActivityIdentity{ProjectID: command.ProjectID}
	var userID, workspaceID, projectID, status string
	var err error
	switch kind {
	case "turn":
		activity.AgentTurnID = id
		err = tx.QueryRowContext(ctx, `SELECT t.user_id,p.workspace_id,t.project_id,t.status FROM agent_turns t
			JOIN projects p ON p.project_id=t.project_id WHERE t.agent_turn_id=? AND p.deleted_at IS NULL`, id).
			Scan(&userID, &workspaceID, &projectID, &status)
		if err == nil && status != "committed" {
			return empty, memoryArchiveSourceNotReady(status)
		}
	case "background":
		activity.AgentTaskAttemptID = id
		state, loadErr := loadAgentTaskAttemptStateTx(ctx, tx, id)
		if loadErr != nil {
			return empty, memoryArchiveSourceLookupError(loadErr)
		}
		if state.ProjectDeleted || state.CancelRequested {
			return empty, memoryArchiveSourceNotReady("cancelled")
		}
		if state.AttemptStatus != "completed" {
			return empty, memoryArchiveSourceNotReady(state.AttemptStatus)
		}
		if state.TaskStatus != "completed" {
			return empty, memoryArchiveSourceNotReady(state.TaskStatus)
		}
		projectID = state.ProjectID
		err = tx.QueryRowContext(ctx, `SELECT i.user_id,p.workspace_id FROM skill_invocations i JOIN projects p ON p.project_id=i.project_id
			WHERE i.skill_invocation_id=? AND i.project_id=?`, state.SkillInvocationID, projectID).Scan(&userID, &workspaceID)
	case "execution":
		activity.ExecutionAttemptID = id
		state, loadErr := loadExecutionToolStateTx(ctx, tx, id)
		if loadErr != nil {
			return empty, memoryArchiveSourceLookupError(loadErr)
		}
		if state.ProjectDeleted || state.RunStatus == "cancelled" || state.RunStatus == "failed" {
			return empty, memoryArchiveSourceNotReady("cancelled")
		}
		if state.CurrentAttemptID != state.AttemptID {
			return empty, memoryArchiveSourceNotReady("superseded")
		}
		if state.AttemptStatus != "succeeded" {
			return empty, memoryArchiveSourceNotReady(state.AttemptStatus)
		}
		if state.TaskStatus != "succeeded" {
			return empty, memoryArchiveSourceNotReady(state.TaskStatus)
		}
		userID, workspaceID, projectID = state.UserID, state.WorkspaceID, state.ProjectID
	default:
		return empty, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Memory archive recovery cannot consume this source kind.")
	}
	if err != nil {
		return empty, memoryArchiveSourceLookupError(err)
	}
	if userID != command.UserID || workspaceID != command.WorkspaceID || projectID != command.ProjectID {
		return empty, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Memory archive recovery differs from its durable owner.")
	}
	user, err := resolvePrincipalQuery(ctx, tx, identity.Principal{Kind: identity.KindUser, UserID: userID, WorkspaceID: workspaceID, Role: identity.RoleViewer})
	if err != nil {
		return empty, err
	}
	if !user.Allows(identity.RoleEditor) {
		return empty, domainError("WORKSPACE_ACCESS_DENIED", "Memory archive owner no longer has execution access.")
	}
	source, err := agentMemorySnapshotQuery(ctx, tx, activity, user)
	if err != nil {
		return empty, memoryArchiveSourceLookupError(err)
	}
	if err := validateAgentMemoryReferenceQuery(ctx, tx, source); err != nil {
		return empty, err
	}
	current, err := agentMemoryQuery(ctx, tx, user, projectID, 0)
	if err != nil {
		return empty, err
	}
	if current.Version != source.Version || current.ContentHash != source.ContentHash {
		check := source
		check.ReadEnabled = true
		if err := validateAgentMemoryReferenceQuery(ctx, tx, check); err != nil {
			return empty, err
		}
	}
	return source, nil
}

// Only explicit absence in a source lookup is permanent. Database failures
// must remain retryable; do not apply this translation to the archive write.
func memoryArchiveSourceLookupError(err error) error {
	var domain *DomainError
	missing := errors.Is(err, sql.ErrNoRows)
	if errors.As(err, &domain) {
		missing = domain.Code == "EXECUTION_ATTEMPT_NOT_FOUND" || domain.Code == "AGENT_TASK_ATTEMPT_NOT_FOUND"
	}
	if missing {
		return domainError("AGENT_MEMORY_ARCHIVE_SOURCE_REVOKED", "The original memory archive source no longer exists.")
	}
	return err
}

func memoryArchiveSourceNotReady(status string) error {
	switch status {
	case "failed", "cancelled", "abandoned", "superseded":
		return domainError("AGENT_MEMORY_ARCHIVE_SOURCE_REVOKED", "Memory archive source did not complete successfully.")
	default:
		return domainError("AGENT_MEMORY_ARCHIVE_NOT_READY", "Memory archive source completion is not yet confirmed.")
	}
}
