package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Called only by the owner's explicit resume transaction. Never infer a phase
// from the current job alone, discard SDK state, or mutate a read-only lookup.
func (s *Store) recoverMemoryWorkspacePhaseTx(ctx context.Context, tx *sql.Tx, job AgentMemoryGeneration) error {
	legacyKey := "memory:" + job.GenerationID
	record, err := scanNativeWorkspace(tx.QueryRowContext(ctx, nativeWorkspaceSelect+` WHERE activity_key=?`, legacyKey))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	invalid := domainError("AGENT_MEMORY_CONFLICT", "Legacy memory workspace requires an exact saved phase and snapshot before recovery.")
	if record.workspaceID != job.WorkspaceID || record.projectID != job.ProjectID || record.userID != job.UserID || record.status != "reserved" || job.Checkpoint == "" {
		return invalid
	}
	if record.LeaseUntil.After(s.now()) {
		return domainError("NATIVE_WORKSPACE_BUSY", "Legacy memory workspace lease must expire before explicit recovery.")
	}
	var checkpoint struct {
		SchemaVersion string `json:"schema_version"`
		StateJSON     string `json:"state_json"`
		StateHash     string `json:"state_hash"`
		StageHash     string `json:"stage_hash"`
		Binding       struct {
			GenerationID string `json:"generation_id"`
			Phase        string `json:"phase"`
			ProjectID    string `json:"project_id"`
			UserID       string `json:"user_id"`
			SourceHash   string `json:"source_hash"`
			BaseVersion  int    `json:"base_version"`
			BaseHash     string `json:"base_hash"`
			ModelID      string `json:"model_id"`
			PolicyHash   string `json:"policy_hash"`
		} `json:"binding"`
		Workspace struct {
			SchemaVersion string `json:"schema_version"`
			SessionID     string `json:"session_id"`
			EnvironmentID string `json:"environment_id"`
			State         string `json:"state"`
			Snapshot      struct {
				Version int    `json:"version"`
				Hash    string `json:"sha256"`
			} `json:"snapshot"`
		} `json:"native_workspace"`
	}
	if len(job.Checkpoint) > 4<<20 || sha256Hex([]byte(job.Checkpoint)) != job.CheckpointHash || json.Unmarshal([]byte(job.Checkpoint), &checkpoint) != nil {
		return invalid
	}
	b, w := checkpoint.Binding, checkpoint.Workspace
	if checkpoint.SchemaVersion != "agent_memory_checkpoint.v1" || !json.Valid([]byte(checkpoint.StateJSON)) || sha256Hex([]byte(checkpoint.StateJSON)) != checkpoint.StateHash || !nativeLeaseKeyPattern.MatchString(checkpoint.StageHash) || !nativeLeaseKeyPattern.MatchString(b.PolicyHash) ||
		b.GenerationID != job.GenerationID || b.Phase != job.Phase || (b.Phase != "extraction" && b.Phase != "consolidation") || b.ProjectID != job.ProjectID || b.UserID != job.UserID || b.SourceHash != job.SourceHash || b.BaseVersion != job.BaseVersion || b.BaseHash != job.BaseHash || b.ModelID != job.ModelID ||
		w.SchemaVersion != "content_agent_native_workspace.v1" || w.SessionID != record.SessionID || !nativeSessionIDPattern.MatchString(w.EnvironmentID) || (w.State != "ready" && w.State != "closed") || w.Snapshot.Version < 1 || w.Snapshot.Version != record.SnapshotVersion || !nativeLeaseKeyPattern.MatchString(w.Snapshot.Hash) {
		return invalid
	}
	if job.Phase == "consolidation" && job.InputPlan == "" {
		return invalid
	}
	if _, err := readNativeWorkspaceSnapshotTx(ctx, tx, record.SessionID, w.Snapshot.Version, w.Snapshot.Hash); err != nil {
		return err
	}
	key := legacyKey + ":" + b.Phase
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_workspace_leases WHERE activity_key=?)`, key).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return invalid
	}
	// Preserve the old epoch, holder and generation: normal reservation must
	// still perform a fenced takeover with the exact previous generation.
	return updateExactlyOne(ctx, tx, `UPDATE native_workspace_leases SET activity_key=?,updated_at=? WHERE session_id=? AND activity_key=?`,
		"Legacy memory workspace changed during recovery.", key, formatTime(s.now()), record.SessionID, legacyKey)
}
