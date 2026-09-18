package runtime

import (
	"context"
	"database/sql"
	"errors"
)

type NativeWorkspacePTYProcessState struct {
	PTYSessionID int    `json:"pty_session_id"`
	ProcessID    string `json:"process_id"`
	Sequence     int    `json:"sequence"`
	TTY          bool   `json:"tty"`
	Status       string `json:"status"`
}

type NativeWorkspacePTYState struct {
	SessionID     string                           `json:"session_id"`
	EnvironmentID string                           `json:"environment_id"`
	State         string                           `json:"state"`
	Processes     []NativeWorkspacePTYProcessState `json:"processes"`
}

// ReadPTYState returns the last confirmed bindings, not a claim that a process
// is still alive. The next approved input must check the original engine stream.
func (manager *NativeWorkspaceManager) ReadPTYState(ctx context.Context, access NativeWorkspaceAccess) (NativeWorkspacePTYState, error) {
	result := NativeWorkspacePTYState{SessionID: access.SessionID, Processes: []NativeWorkspacePTYProcessState{}}
	if !manager.requirePolicy {
		return result, domainError("NATIVE_WORKSPACE_POLICY_REQUIRED", "Terminal recovery requires current workspace policy.")
	}
	tx, err := manager.store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	lease, err := manager.store.nativeWorkspaceAccessTx(ctx, tx, access)
	if err != nil {
		return result, err
	}
	if err := manager.validatePolicyTx(ctx, tx, lease.workspaceID); err != nil {
		return result, err
	}
	state, err := scanNativeEnvironment(tx.QueryRowContext(ctx, nativeEnvironmentSelect+` WHERE session_id=?`, access.SessionID))
	if errors.Is(err, sql.ErrNoRows) {
		result.State = "absent"
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if state.operationID != "" {
		return result, domainError("NATIVE_WORKSPACE_OPERATION_IN_PROGRESS", "Terminal recovery cannot race an outstanding environment operation.")
	}
	if state.State != "ready" && state.State != "closed" {
		return result, domainError("NATIVE_WORKSPACE_RECOVERY_REQUIRED", "The environment has no confirmed recovery boundary.")
	}
	if !nativeSessionIDPattern.MatchString(state.environmentID) || !state.matchesHandle(state.handle) {
		return result, domainError("NATIVE_WORKSPACE_ENVIRONMENT_INVALID", "Terminal recovery has no exact environment binding.")
	}
	result.EnvironmentID, result.State = state.environmentID, state.State
	if state.State == "closed" {
		return result, nil
	}
	rows, err := tx.QueryContext(ctx, nativePTYProcessSelect+` WHERE session_id=? AND environment_id=? AND status!='deleted' ORDER BY pty_session_id LIMIT 129`, access.SessionID, state.environmentID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		process, err := scanNativePTYProcess(rows)
		if err != nil {
			return result, err
		}
		if len(result.Processes) >= 128 || process.sequence < 1 || process.status != "running" && process.status != "exited" {
			return result, domainError("NATIVE_WORKSPACE_PTY_UNCONFIRMED", "Unknown process history cannot be rebound as a live terminal.")
		}
		result.Processes = append(result.Processes, NativeWorkspacePTYProcessState{PTYSessionID: process.ptyID, ProcessID: process.processID, Sequence: process.sequence, TTY: process.tty, Status: process.status})
	}
	return result, rows.Err()
}
