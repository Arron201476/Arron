package runtime

import (
	"context"
	"database/sql"
	"errors"

	"content-agent/backend/internal/identity"
)

func migrateAgentMemorySnapshots(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_memory_snapshots (
		activity_key TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
		project_id TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
		user_id TEXT NOT NULL REFERENCES users(user_id),
		version INTEGER NOT NULL CHECK(version >= 0),
		content_hash TEXT NOT NULL,
		read_enabled INTEGER NOT NULL CHECK(read_enabled IN (0,1))
	);`)
	return err
}

type AgentMemorySnapshot struct {
	ActivityKey    string            `json:"activity_key"`
	WorkspaceID    string            `json:"workspace_id"`
	ProjectID      string            `json:"project_id"`
	UserID         string            `json:"user_id"`
	Version        int               `json:"version"`
	CurrentVersion int               `json:"current_version"`
	ContentHash    string            `json:"content_hash"`
	ReadEnabled    bool              `json:"read_enabled"`
	Files          map[string]string `json:"files"`
}

func agentMemorySnapshotQuery(ctx context.Context, query rowQueryer, activity AgentActivityIdentity, user identity.Principal) (AgentMemorySnapshot, error) {
	result := AgentMemorySnapshot{ActivityKey: instructionActivityKey(activity), Files: map[string]string{}}
	err := query.QueryRowContext(ctx, `SELECT workspace_id,project_id,user_id,version,content_hash,read_enabled FROM agent_memory_snapshots WHERE activity_key=?`, result.ActivityKey).
		Scan(&result.WorkspaceID, &result.ProjectID, &result.UserID, &result.Version, &result.ContentHash, &result.ReadEnabled)
	if err != nil {
		return AgentMemorySnapshot{}, err
	}
	if result.WorkspaceID != user.WorkspaceID || result.ProjectID != activity.ProjectID || result.UserID != user.UserID || result.Version < 0 || len(result.ContentHash) != 64 || (result.ReadEnabled && result.Version == 0) {
		return AgentMemorySnapshot{}, domainError("AGENT_MEMORY_INVALID", "执行记忆引用无效。")
	}
	return result, nil
}

// References contain no private bodies. Only executions which consumed memory
// or published memory are revoked by a later user edit. Unused legacy/disabled
// executions never gain memory on resume.
func validateAgentMemorySnapshotQuery(ctx context.Context, query rowQueryer, activity AgentActivityIdentity, user identity.Principal) error {
	snapshot, err := agentMemorySnapshotQuery(ctx, query, activity, user)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return validateAgentMemoryReferenceQuery(ctx, query, snapshot)
}

func validateAgentMemoryReferenceQuery(ctx context.Context, query rowQueryer, snapshot AgentMemorySnapshot) error {
	if !snapshot.ReadEnabled {
		var wrote bool
		if err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_memory_publications WHERE activity_key=? AND version>0)`, snapshot.ActivityKey).Scan(&wrote); err != nil {
			return err
		}
		if !wrote {
			return nil
		}
	}
	user := identity.Principal{UserID: snapshot.UserID, WorkspaceID: snapshot.WorkspaceID}
	current, err := agentMemoryQuery(ctx, query, user, snapshot.ProjectID, 0)
	if err != nil {
		return err
	}
	if current.Version != snapshot.Version || current.ContentHash != snapshot.ContentHash || !current.Enabled || current.Forgotten {
		var ownPublication bool
		if current.Enabled && !current.Forgotten {
			if err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_memory_publications WHERE activity_key=? AND workspace_id=? AND project_id=? AND user_id=? AND base_version=? AND base_hash=? AND version=? AND content_hash=?)`,
				snapshot.ActivityKey, snapshot.WorkspaceID, snapshot.ProjectID, snapshot.UserID, snapshot.Version, snapshot.ContentHash, current.Version, current.ContentHash).Scan(&ownPublication); err != nil {
				return err
			}
		}
		if ownPublication {
			return nil
		}
		return domainError("AGENT_MEMORY_CONFLICT", "执行使用的记忆已修改或遗忘，请开始新的执行。")
	}
	return nil
}

// Result publication must recheck inside its write transaction, including calls
// without activity headers and idempotent commit endpoints allowed past running.
func validateAgentMemoryCommitTx(ctx context.Context, tx *sql.Tx, mode, id string) error {
	var prefix string
	switch mode {
	case "conversation":
		prefix = "turn:"
	case "background_task":
		prefix = "background:"
	case "stateful_workflow":
		prefix = "execution:"
	default:
		return domainError("REQUEST_VALIDATION_FAILED", "执行类型无效。")
	}
	var snapshot AgentMemorySnapshot
	snapshot.ActivityKey = prefix + id
	err := tx.QueryRowContext(ctx, `SELECT workspace_id,project_id,user_id,version,content_hash,read_enabled FROM agent_memory_snapshots WHERE activity_key=?`, prefix+id).
		Scan(&snapshot.WorkspaceID, &snapshot.ProjectID, &snapshot.UserID, &snapshot.Version, &snapshot.ContentHash, &snapshot.ReadEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return validateAgentMemoryReferenceQuery(ctx, tx, snapshot)
}

func (s *Store) ResolveAgentMemorySnapshot(ctx context.Context) (AgentMemorySnapshot, error) {
	transport, ok := identity.FromContext(ctx)
	activity, active := AgentActivityFromContext(ctx)
	if !ok || transport.Kind != identity.KindService || !active || activity.AllowTerminal {
		return AgentMemorySnapshot{}, domainError("AGENT_ACTIVITY_FORBIDDEN", "记忆读取需要有效的内部执行身份。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentMemorySnapshot{}, err
	}
	defer tx.Rollback()
	user, err := s.resolveAgentActivityPrincipalTx(ctx, tx, activity)
	if err != nil {
		return AgentMemorySnapshot{}, err
	}
	if !user.Allows(identity.RoleEditor) {
		return AgentMemorySnapshot{}, domainError("WORKSPACE_ACCESS_DENIED", "当前用户没有执行权限。")
	}
	snapshot, err := agentMemorySnapshotQuery(ctx, tx, activity, user)
	if errors.Is(err, sql.ErrNoRows) {
		current, err := agentMemoryQuery(ctx, tx, user, activity.ProjectID, 0)
		if err != nil {
			return AgentMemorySnapshot{}, err
		}
		legacy, err := instructionLegacyCheckpoint(ctx, tx, activity)
		if err != nil {
			return AgentMemorySnapshot{}, err
		}
		snapshot = AgentMemorySnapshot{ActivityKey: instructionActivityKey(activity), WorkspaceID: user.WorkspaceID, ProjectID: activity.ProjectID, UserID: user.UserID,
			Version: current.Version, ContentHash: current.ContentHash, ReadEnabled: current.Enabled && !current.Forgotten && !legacy, Files: map[string]string{}}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_memory_snapshots(activity_key,workspace_id,project_id,user_id,version,content_hash,read_enabled) VALUES(?,?,?,?,?,?,?)`,
			snapshot.ActivityKey, snapshot.WorkspaceID, snapshot.ProjectID, snapshot.UserID, snapshot.Version, snapshot.ContentHash, snapshot.ReadEnabled); err != nil {
			return AgentMemorySnapshot{}, err
		}
	} else if err != nil {
		return AgentMemorySnapshot{}, err
	}
	if snapshot.ReadEnabled {
		current, err := agentMemoryQuery(ctx, tx, user, activity.ProjectID, snapshot.Version)
		if err != nil {
			return AgentMemorySnapshot{}, err
		}
		snapshot.Files = current.Files
	}
	current, err := agentMemoryQuery(ctx, tx, user, activity.ProjectID, 0)
	if err != nil {
		return AgentMemorySnapshot{}, err
	}
	snapshot.CurrentVersion = current.Version
	if err := tx.Commit(); err != nil {
		return AgentMemorySnapshot{}, err
	}
	return snapshot, nil
}
