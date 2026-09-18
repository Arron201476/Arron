package runtime

import (
	"context"
	"database/sql"
	"errors"

	"content-agent/backend/internal/scriptsandbox"
)

const nativeWorkspaceRestoreSchema = `
CREATE TABLE IF NOT EXISTS native_workspace_restores (
	session_id TEXT NOT NULL REFERENCES native_workspace_leases(session_id),
	request_id TEXT NOT NULL,
	environment_id TEXT NOT NULL,
	snapshot_version INTEGER NOT NULL CHECK(snapshot_version>0),
	snapshot_hash TEXT NOT NULL,
	operation_id TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('running','completed','failed')),
	error_code TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(session_id,request_id)
);
`

type RestoreNativeWorkspaceCommand struct {
	RequestID string `json:"request_id"`
	Version   int    `json:"version"`
	SHA256    string `json:"sha256"`
}

func (manager *NativeWorkspaceManager) beginRestore(ctx context.Context, access NativeWorkspaceAccess, command RestoreNativeWorkspaceCommand) (nativeEnvironment, scriptsandbox.WorkspaceSnapshot, bool, error) {
	var state nativeEnvironment
	var snapshot scriptsandbox.WorkspaceSnapshot
	if !tenantIdentifierPattern.MatchString(command.RequestID) || command.Version < 1 || !nativeLeaseKeyPattern.MatchString(command.SHA256) {
		return state, snapshot, false, domainError("NATIVE_WORKSPACE_REQUEST_INVALID", "Recovery requires an explicit request, snapshot version and hash.")
	}
	s := manager.store
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return state, snapshot, false, err
	}
	defer tx.Rollback()
	lease, err := s.nativeWorkspaceAccessTx(ctx, tx, access)
	if err != nil {
		return state, snapshot, false, err
	}
	if err := manager.validatePolicyTx(ctx, tx, lease.workspaceID); err != nil {
		return state, snapshot, false, err
	}
	if _, err := s.nativeWorkspaceOwnerTx(ctx, tx, access.DispatchGeneration, false); err != nil {
		return state, snapshot, false, err
	}
	selected, err := readNativeWorkspaceSnapshotTx(ctx, tx, access.SessionID, command.Version, command.SHA256)
	if err != nil {
		return state, snapshot, false, err
	}
	snapshot = selected.Snapshot
	var previousEnvironment, previousHash, previousStatus string
	var previousVersion int
	err = tx.QueryRowContext(ctx, `SELECT environment_id,snapshot_version,snapshot_hash,status FROM native_workspace_restores WHERE session_id=? AND request_id=?`, access.SessionID, command.RequestID).Scan(&previousEnvironment, &previousVersion, &previousHash, &previousStatus)
	if err == nil {
		if previousHash != command.SHA256 || previousVersion != command.Version {
			return state, snapshot, false, domainError("NATIVE_WORKSPACE_REQUEST_CONFLICT", "Restore request was reused for another snapshot.")
		}
		if previousStatus == "running" {
			return state, snapshot, false, domainError("NATIVE_WORKSPACE_OPERATION_IN_PROGRESS", "The original restore remains unresolved and cannot be replayed.")
		}
		if previousStatus != "completed" {
			return state, snapshot, false, domainError("NATIVE_WORKSPACE_RECOVERY_REQUIRED", "A failed restore requires a new explicit recovery request.")
		}
		state, err = scanNativeEnvironment(tx.QueryRowContext(ctx, nativeEnvironmentSelect+` WHERE session_id=?`, access.SessionID))
		if err != nil {
			return state, snapshot, false, err
		}
		if state.environmentID != previousEnvironment || state.State != "ready" || state.operationID != "" {
			return state, snapshot, false, domainError("NATIVE_WORKSPACE_RECOVERY_REQUIRED", "Original restore does not identify the current ready environment.")
		}
		state, err = manager.claimBindingTx(ctx, tx, state, s.newID("nwr"), "ensure")
		if err != nil {
			return state, snapshot, false, err
		}
		return state, snapshot, true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return state, snapshot, false, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_workspace_restores WHERE session_id=?`, access.SessionID).Scan(&count); err != nil {
		return state, snapshot, false, err
	}
	if count >= 128 {
		return state, snapshot, false, domainError("NATIVE_WORKSPACE_QUOTA_EXCEEDED", "Execution recovery history quota reached.")
	}
	state, err = scanNativeEnvironment(tx.QueryRowContext(ctx, nativeEnvironmentSelect+` WHERE session_id=?`, access.SessionID))
	operationID := s.newID("nwr")
	if errors.Is(err, sql.ErrNoRows) || err == nil && state.State == "closed" && state.operationID == "" {
		state, err = manager.createBindingTx(ctx, tx, access.SessionID, operationID)
		if err != nil {
			return state, snapshot, false, err
		}
	} else {
		if err != nil {
			return state, snapshot, false, err
		}
		state, err = manager.claimBindingTx(ctx, tx, state, operationID, "restore")
		if err != nil {
			return state, snapshot, false, err
		}
	}
	state.restoreRequestID = command.RequestID
	now := formatTime(s.now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO native_workspace_restores(session_id,request_id,environment_id,snapshot_version,snapshot_hash,operation_id,status,created_at,updated_at) VALUES(?,?,?,?,?,?,'running',?,?)`, access.SessionID, command.RequestID, state.environmentID, command.Version, command.SHA256, operationID, now, now); err != nil {
		return state, snapshot, false, err
	}
	return state, snapshot, false, tx.Commit()
}

// Restore never clears a live directory. The engine accepts only empty or
// identical contents; replacing a closed environment uses a new private ID.
func (manager *NativeWorkspaceManager) Restore(ctx context.Context, access NativeWorkspaceAccess, command RestoreNativeWorkspaceCommand) (NativeWorkspaceEnvironment, error) {
	state, snapshot, completed, err := manager.beginRestore(ctx, access, command)
	if err != nil {
		return NativeWorkspaceEnvironment{}, err
	}
	runCtx, stop := manager.watch(ctx, access, state)
	if completed {
		// Receipt lookup and reconnect claim share one transaction. A later
		// replacement cannot redirect this request to another environment ID.
		err = manager.engine.ReconnectWorkspace(runCtx, state.handle)
	} else if state.operationKind == "create" {
		state.handle, err = manager.engine.CreateWorkspace(runCtx, state.environmentID, state.ownerHash, state.limits)
	}
	if err == nil && !state.matchesHandle(state.handle) {
		err = domainError("NATIVE_WORKSPACE_ENVIRONMENT_INVALID", "Restore did not obtain the original verified environment binding.")
	}
	if err == nil {
		err = manager.checkOperation(runCtx, access, state)
	}
	if err == nil && !completed {
		var restored scriptsandbox.WorkspaceSnapshot
		restored, err = manager.engine.HydrateWorkspace(runCtx, state.handle, snapshot.Archive, command.SHA256)
		if err == nil {
			verified, verifyErr := scriptsandbox.ParseWorkspaceSnapshot(restored.Archive, state.limits)
			if verifyErr != nil || restored.SHA256 != command.SHA256 || verified.SHA256 != command.SHA256 {
				err = domainError("NATIVE_WORKSPACE_SNAPSHOT_INVALID", "Restored files do not match the selected snapshot.")
			}
		}
		var sandboxErr *scriptsandbox.Error
		state.preservedOnFailure = errors.As(err, &sandboxErr) && (sandboxErr.Code == "WORKSPACE_HYDRATE_CONFLICT" || sandboxErr.Code == "WORKSPACE_BUSY" || sandboxErr.Code == "WORKSPACE_ADAPTER_UNAVAILABLE")
	}
	if err == nil {
		err = runCtx.Err()
	}
	stop()
	if err := manager.finish(ctx, access, state, err); err != nil {
		return NativeWorkspaceEnvironment{}, err
	}
	return NativeWorkspaceEnvironment{SessionID: access.SessionID, State: "ready"}, nil
}
