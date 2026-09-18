package runtime

import (
	"context"

	"content-agent/backend/internal/scriptsandbox"
)

// TerminatePTYs is an owner lifecycle operation, not a model command or a way
// to signal an arbitrary process. Workspace files remain available for stop's
// snapshot only after every recorded live process confirms a terminal result.
func (manager *NativeWorkspaceManager) TerminatePTYs(ctx context.Context, access NativeWorkspaceAccess) (NativeWorkspaceEnvironment, error) {
	if !manager.requirePolicy {
		return NativeWorkspaceEnvironment{}, domainError("NATIVE_WORKSPACE_POLICY_REQUIRED", "Terminal cleanup requires current workspace policy.")
	}
	state, err := manager.begin(ctx, access, "pty_terminate")
	if err != nil {
		return NativeWorkspaceEnvironment{}, err
	}
	if state.operationID == "" {
		return NativeWorkspaceEnvironment{SessionID: access.SessionID, State: "stopped"}, nil
	}
	state.terminatedPTYs, err = manager.claimPTYTermination(ctx, access, state)
	if err != nil {
		return NativeWorkspaceEnvironment{}, manager.finish(ctx, access, state, err)
	}
	runCtx, stop := manager.watch(ctx, access, state)
	engine, supported := manager.engine.(nativeWorkspacePTYEngine)
	for _, process := range state.terminatedPTYs {
		if !supported {
			err = domainError("NATIVE_WORKSPACE_UNAVAILABLE", "The bound engine cannot confirm terminal cleanup.")
			break
		}
		if err = runCtx.Err(); err != nil {
			break
		}
		var result scriptsandbox.WorkspacePTYResult
		result, err = engine.WriteWorkspacePTY(runCtx, state.handle, scriptsandbox.WorkspacePTYInput{
			ProcessID: process.processID, YieldMillis: 1, Terminate: true,
		})
		if err != nil {
			break
		}
		if !validateNativePTYResult(result, process.processID, 0) || result.ExitCode == nil {
			err = domainError("NATIVE_WORKSPACE_PTY_UNCONFIRMED", "Terminal cleanup did not confirm the exact process stopped.")
			break
		}
	}
	if err == nil {
		err = runCtx.Err()
	}
	stop()
	if err := manager.finish(ctx, access, state, err); err != nil {
		return NativeWorkspaceEnvironment{}, err
	}
	return NativeWorkspaceEnvironment{SessionID: access.SessionID, State: "stopped"}, nil
}

func (manager *NativeWorkspaceManager) claimPTYTermination(ctx context.Context, access NativeWorkspaceAccess, state nativeEnvironment) ([]nativePTYProcess, error) {
	tx, err := manager.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	lease, err := manager.store.nativeWorkspaceAccessTx(ctx, tx, access)
	if err != nil {
		return nil, err
	}
	if err := manager.validatePolicyTx(ctx, tx, lease.workspaceID); err != nil {
		return nil, err
	}
	var valid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_workspace_environments WHERE session_id=? AND environment_id=? AND operation_id=? AND operation_kind='pty_terminate' AND status='ready')`,
		state.SessionID, state.environmentID, state.operationID).Scan(&valid); err != nil {
		return nil, err
	}
	if !valid {
		return nil, domainError("NATIVE_WORKSPACE_OPERATION_SUPERSEDED", "Terminal cleanup lost its environment operation.")
	}
	rows, err := tx.QueryContext(ctx, nativePTYProcessSelect+` WHERE session_id=? AND environment_id=? AND status!='deleted' ORDER BY pty_session_id LIMIT 129`, state.SessionID, state.environmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var selected []nativePTYProcess
	count := 0
	for rows.Next() {
		process, err := scanNativePTYProcess(rows)
		if err != nil {
			return nil, err
		}
		count++
		if count > 128 || process.status != "exited" && process.status != "running" {
			return nil, domainError("NATIVE_WORKSPACE_PTY_UNCONFIRMED", "Unknown terminal history cannot be treated as confirmed cleanup.")
		}
		if process.status == "running" {
			selected = append(selected, process)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// Lost until confirmed: a crash after signaling must not allow another input
	// or a second cleanup attempt to claim that an unobserved result was safe.
	for _, process := range selected {
		result, err := tx.ExecContext(ctx, `UPDATE native_workspace_pty_processes SET status='lost',updated_at=? WHERE session_id=? AND environment_id=? AND pty_session_id=? AND sequence=? AND status='running'`,
			formatTime(manager.store.now()), state.SessionID, state.environmentID, process.ptyID, process.sequence)
		if err != nil {
			return nil, err
		}
		if err := requireOneRow(result, "NATIVE_WORKSPACE_OPERATION_SUPERSEDED", "Terminal cleanup process changed before signaling."); err != nil {
			return nil, err
		}
	}
	return selected, tx.Commit()
}
