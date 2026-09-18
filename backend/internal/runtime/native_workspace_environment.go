package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"content-agent/backend/internal/identity"
	"content-agent/backend/internal/scriptsandbox"
)

const nativeWorkspaceOperationTimeout = 120 * time.Second

const nativeWorkspaceEnvironmentSchema = `
CREATE TABLE IF NOT EXISTS native_workspace_environments (
	session_id TEXT PRIMARY KEY REFERENCES native_workspace_leases(session_id),
	environment_id TEXT NOT NULL UNIQUE,
	owner_hash TEXT NOT NULL,
	limits_json TEXT NOT NULL,
	handle_json TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL CHECK(status IN ('creating','ready','blocked','closing','closed')),
	operation_id TEXT NOT NULL DEFAULT '',
	operation_kind TEXT NOT NULL DEFAULT '',
	operation_until TEXT NOT NULL,
	last_error_code TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);
`

// NativeWorkspaceEngine is configured by the host, never by a model or saved
// checkpoint. This lifecycle bridge intentionally does not expose command tools.
type NativeWorkspaceEngine interface {
	CreateWorkspace(context.Context, string, string, scriptsandbox.Limits) (scriptsandbox.WorkspaceHandle, error)
	FindWorkspace(context.Context, string, string, scriptsandbox.Limits) (scriptsandbox.WorkspaceHandle, bool, error)
	ReconnectWorkspace(context.Context, scriptsandbox.WorkspaceHandle) error
	DeleteWorkspace(context.Context, scriptsandbox.WorkspaceHandle) error
	ExportWorkspace(context.Context, scriptsandbox.WorkspaceHandle) (scriptsandbox.WorkspaceSnapshot, error)
	HydrateWorkspace(context.Context, scriptsandbox.WorkspaceHandle, []byte, string) (scriptsandbox.WorkspaceSnapshot, error)
}

var _ NativeWorkspaceEngine = (*scriptsandbox.OCI)(nil)

type NativeWorkspaceManager struct {
	store         *Store
	engine        NativeWorkspaceEngine
	limits        scriptsandbox.Limits
	requirePolicy bool
}

type NativeWorkspaceEnvironment struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

type nativeEnvironment struct {
	NativeWorkspaceEnvironment
	environmentID, ownerHash, operationID, operationKind string
	limits                                               scriptsandbox.Limits
	handle                                               scriptsandbox.WorkspaceHandle
	preservedOnFailure                                   bool
	restoreRequestID                                     string
	command                                              *nativeCommandRecord
	pty                                                  *nativePTYRecord
	terminatedPTYs                                       []nativePTYProcess
	fileCall, fileOperation                              *nativeFileRecord
	manifestOperation                                    *nativeManifestOperation
}

const nativeEnvironmentSelect = `SELECT session_id,environment_id,owner_hash,limits_json,handle_json,status,operation_id,operation_kind FROM native_workspace_environments`

func scanNativeEnvironment(row interface{ Scan(...any) error }) (nativeEnvironment, error) {
	var state nativeEnvironment
	var limits, handle string
	err := row.Scan(&state.SessionID, &state.environmentID, &state.ownerHash, &limits, &handle, &state.State, &state.operationID, &state.operationKind)
	if err != nil {
		return state, err
	}
	if json.Unmarshal([]byte(limits), &state.limits) != nil || state.limits.Validate() != nil ||
		!nativeSessionIDPattern.MatchString(state.environmentID) || !nativeLeaseKeyPattern.MatchString(state.ownerHash) {
		return state, domainError("NATIVE_WORKSPACE_ENVIRONMENT_INVALID", "Stored environment binding is invalid.")
	}
	if handle != "" && (json.Unmarshal([]byte(handle), &state.handle) != nil || !state.matchesHandle(state.handle)) {
		return state, domainError("NATIVE_WORKSPACE_ENVIRONMENT_INVALID", "Stored environment handle does not match its binding.")
	}
	return state, nil
}

func (state nativeEnvironment) matchesHandle(handle scriptsandbox.WorkspaceHandle) bool {
	return handle.SessionID == state.environmentID && handle.OwnerHash == state.ownerHash && handle.Limits == state.limits &&
		nativeLeaseKeyPattern.MatchString(handle.ContainerID) && nativeLeaseKeyPattern.MatchString(handle.PolicyHash)
}

func NewNativeWorkspaceManager(store *Store, engine NativeWorkspaceEngine) (*NativeWorkspaceManager, error) {
	if store == nil || engine == nil {
		return nil, domainError("NATIVE_WORKSPACE_UNAVAILABLE", "A trusted store and isolated engine are required.")
	}
	return &NativeWorkspaceManager{store: store, engine: engine, limits: scriptsandbox.DefaultLimits()}, nil
}

func NewPolicyNativeWorkspaceManager(store *Store, engine NativeWorkspaceEngine, limits scriptsandbox.Limits) (*NativeWorkspaceManager, error) {
	manager, err := NewNativeWorkspaceManager(store, engine)
	if err != nil {
		return nil, err
	}
	if err := limits.Validate(); err != nil {
		return nil, domainError("NATIVE_WORKSPACE_POLICY_INVALID", "Workspace resource policy is invalid.")
	}
	manager.requirePolicy = true
	manager.limits = nativeWorkspacePolicyLimits(limits)
	return manager, nil
}

func nativeWorkspacePolicyLimits(limits scriptsandbox.Limits) scriptsandbox.Limits {
	defaults := scriptsandbox.DefaultLimits()
	return scriptsandbox.Limits{TimeoutSeconds: min(limits.TimeoutSeconds, defaults.TimeoutSeconds), CPUCount: min(limits.CPUCount, defaults.CPUCount),
		MemoryBytes: min(limits.MemoryBytes, defaults.MemoryBytes), ProcessCount: min(limits.ProcessCount, defaults.ProcessCount),
		DiskBytes: min(limits.DiskBytes, defaults.DiskBytes), TempBytes: min(limits.TempBytes, defaults.TempBytes)}
}

func (manager *NativeWorkspaceManager) validatePolicyTx(ctx context.Context, tx *sql.Tx, workspaceID string) error {
	if !manager.requirePolicy {
		return nil
	}
	policy, err := manager.store.getScriptSandboxPolicyForWorkspaceQuery(ctx, tx, workspaceID)
	if err != nil {
		return err
	}
	if !policy.Enabled || !policy.Sandbox.Available {
		return domainError("NATIVE_WORKSPACE_POLICY_DISABLED", "The workspace administrator has not enabled an available isolated sandbox.")
	}
	if policy.Limits.Validate() != nil || nativeWorkspacePolicyLimits(policy.Limits) != manager.limits {
		return domainError("NATIVE_WORKSPACE_POLICY_CHANGED", "Resource policy changed; the previous execution environment is no longer authorized.")
	}
	return nil
}

func (manager *NativeWorkspaceManager) begin(ctx context.Context, access NativeWorkspaceAccess, kind string) (nativeEnvironment, error) {
	s := manager.store
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nativeEnvironment{}, err
	}
	defer tx.Rollback()
	lease, err := s.nativeWorkspaceAccessTx(ctx, tx, access)
	if err != nil {
		return nativeEnvironment{}, err
	}
	if err := manager.validatePolicyTx(ctx, tx, lease.workspaceID); err != nil {
		return nativeEnvironment{}, err
	}
	cleanup := kind == "probe" || kind == "shutdown" || kind == "pty_terminate"
	if _, err := s.nativeWorkspaceOwnerTx(ctx, tx, access.DispatchGeneration, kind == "snapshot" || cleanup); err != nil {
		return nativeEnvironment{}, err
	}
	if kind == "snapshot" {
		if err := nativeManifestPendingTx(ctx, tx, access.SessionID); err != nil {
			return nativeEnvironment{}, err
		}
	}
	state, err := scanNativeEnvironment(tx.QueryRowContext(ctx, nativeEnvironmentSelect+` WHERE session_id=?`, access.SessionID))
	if errors.Is(err, sql.ErrNoRows) && cleanup {
		return nativeEnvironment{NativeWorkspaceEnvironment: NativeWorkspaceEnvironment{SessionID: access.SessionID, State: "absent"}}, tx.Commit()
	}
	if err == nil && state.State == "closed" && state.operationID == "" && cleanup {
		return state, tx.Commit()
	}
	operationID := s.newID("nwo")
	if errors.Is(err, sql.ErrNoRows) && kind == "ensure" {
		if lease.SnapshotVersion != 0 {
			return state, domainError("NATIVE_WORKSPACE_RECOVERY_REQUIRED", "This execution has a saved snapshot; creating an empty environment requires explicit recovery selection.")
		}
		state, err = manager.createBindingTx(ctx, tx, access.SessionID, operationID)
		if err != nil {
			return state, err
		}
	} else {
		if errors.Is(err, sql.ErrNoRows) {
			return state, domainError("NATIVE_WORKSPACE_ENVIRONMENT_NOT_FOUND", "Execution has no environment; one was not created implicitly.")
		}
		if err != nil {
			return state, err
		}
		state, err = manager.claimBindingTx(ctx, tx, state, operationID, kind)
		if err != nil {
			return state, err
		}
	}
	return state, tx.Commit()
}

func (manager *NativeWorkspaceManager) claimBindingTx(ctx context.Context, tx *sql.Tx, state nativeEnvironment, operationID, kind string) (nativeEnvironment, error) {
	if state.operationID != "" {
		// Elapsed time never proves that a command or a lost create stopped.
		return state, domainError("NATIVE_WORKSPACE_OPERATION_IN_PROGRESS", "An outstanding operation must be resolved before another operation can start.")
	}
	if state.State != "ready" || state.handle.ContainerID == "" || state.limits != manager.limits {
		return state, domainError("NATIVE_WORKSPACE_RECOVERY_REQUIRED", "Environment is not ready; explicit verified recovery is required.")
	}
	state.operationID, state.operationKind = operationID, kind
	now, until := formatTime(manager.store.now()), formatTime(manager.store.now().Add(nativeWorkspaceOperationTimeout))
	result, err := tx.ExecContext(ctx, `UPDATE native_workspace_environments SET operation_id=?,operation_kind=?,operation_until=?,updated_at=? WHERE session_id=? AND environment_id=? AND operation_id='' AND status='ready'`, operationID, kind, until, now, state.SessionID, state.environmentID)
	if err != nil {
		return state, err
	}
	return state, requireOneRow(result, "NATIVE_WORKSPACE_OPERATION_IN_PROGRESS", "Environment operation changed.")
}

func (manager *NativeWorkspaceManager) createBindingTx(ctx context.Context, tx *sql.Tx, sessionID, operationID string) (nativeEnvironment, error) {
	var state nativeEnvironment
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_workspace_environments e JOIN native_workspace_leases l ON l.session_id=e.session_id WHERE l.workspace_id=(SELECT workspace_id FROM native_workspace_leases WHERE session_id=?) AND e.status!='closed'`, sessionID).Scan(&count); err != nil {
		return state, err
	}
	if count >= 8 {
		return state, domainError("NATIVE_WORKSPACE_QUOTA_EXCEEDED", "Active environment quota reached; unconfirmed cleanup still counts.")
	}
	environmentID, err := newNativeWorkspaceUUID()
	if err != nil {
		return state, err
	}
	state = nativeEnvironment{NativeWorkspaceEnvironment: NativeWorkspaceEnvironment{SessionID: sessionID, State: "creating"},
		environmentID: environmentID, ownerHash: sha256Hex([]byte(sessionID + ":" + environmentID)), limits: manager.limits,
		operationID: operationID, operationKind: "create"}
	limits, _ := json.Marshal(state.limits)
	now, until := formatTime(manager.store.now()), formatTime(manager.store.now().Add(nativeWorkspaceOperationTimeout))
	result, err := tx.ExecContext(ctx, `INSERT INTO native_workspace_environments(session_id,environment_id,owner_hash,limits_json,status,operation_id,operation_kind,operation_until,updated_at) VALUES(?,?,?,?,'creating',?,'create',?,?)
		ON CONFLICT(session_id) DO UPDATE SET environment_id=excluded.environment_id,owner_hash=excluded.owner_hash,limits_json=excluded.limits_json,handle_json='',status='creating',operation_id=excluded.operation_id,operation_kind='create',operation_until=excluded.operation_until,last_error_code='',updated_at=excluded.updated_at
		WHERE native_workspace_environments.status='closed' AND native_workspace_environments.operation_id=''`, sessionID, environmentID, state.ownerHash, string(limits), operationID, until, now)
	if err != nil {
		return state, err
	}
	return state, requireOneRow(result, "NATIVE_WORKSPACE_RECOVERY_REQUIRED", "An unconfirmed environment cannot be replaced.")
}

func (manager *NativeWorkspaceManager) checkOperation(ctx context.Context, access NativeWorkspaceAccess, state nativeEnvironment) error {
	tx, err := manager.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	lease, err := manager.store.nativeWorkspaceAccessTx(ctx, tx, access)
	if err != nil {
		return err
	}
	if err := manager.validatePolicyTx(ctx, tx, lease.workspaceID); err != nil {
		return err
	}
	if state.command != nil {
		if _, err := manager.store.validateNativeCommandApprovalTx(ctx, tx, *state.command, false); err != nil {
			return err
		}
	}
	if state.pty != nil {
		if _, err := manager.store.validateNativePTYApprovalTx(ctx, tx, *state.pty, false); err != nil {
			return err
		}
	}
	if state.fileCall != nil {
		if _, err := manager.store.validateNativeFileApprovalTx(ctx, tx, *state.fileCall, false); err != nil {
			return err
		}
	}
	if state.manifestOperation != nil {
		if err := validateNativeManifestOperationTx(ctx, tx, state.SessionID, *state.manifestOperation); err != nil {
			return err
		}
	}
	var valid bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_workspace_environments WHERE session_id=? AND environment_id=? AND operation_id=? AND status IN ('ready','creating'))`, state.SessionID, state.environmentID, state.operationID).Scan(&valid)
	if err == nil && !valid {
		return domainError("NATIVE_WORKSPACE_OPERATION_SUPERSEDED", "Environment operation is no longer current.")
	}
	return err
}

func (manager *NativeWorkspaceManager) watch(ctx context.Context, access NativeWorkspaceAccess, state nativeEnvironment) (context.Context, func()) {
	runCtx, cancel := context.WithTimeout(ctx, nativeWorkspaceOperationTimeout)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if manager.checkOperation(runCtx, access, state) != nil {
					cancel()
					return
				}
			}
		}
	}()
	return runCtx, func() { cancel(); <-done }
}

func (manager *NativeWorkspaceManager) finish(ctx context.Context, access NativeWorkspaceAccess, state nativeEnvironment, operationErr error) error {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	tx, err := manager.store.db.BeginTx(finishCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	lease, accessErr := manager.store.nativeWorkspaceAccessTx(finishCtx, tx, access)
	if accessErr == nil {
		accessErr = manager.validatePolicyTx(finishCtx, tx, lease.workspaceID)
	}
	if accessErr == nil && state.command != nil {
		_, accessErr = manager.store.validateNativeCommandApprovalTx(finishCtx, tx, *state.command, false)
	}
	if accessErr == nil && state.pty != nil {
		_, accessErr = manager.store.validateNativePTYApprovalTx(finishCtx, tx, *state.pty, false)
	}
	if accessErr == nil && state.fileCall != nil {
		_, accessErr = manager.store.validateNativeFileApprovalTx(finishCtx, tx, *state.fileCall, false)
	}
	if accessErr == nil && state.manifestOperation != nil {
		accessErr = validateNativeManifestOperationTx(finishCtx, tx, state.SessionID, *state.manifestOperation)
	}
	if accessErr != nil && (operationErr == nil || errors.Is(operationErr, context.Canceled) || errors.Is(operationErr, context.DeadlineExceeded)) {
		operationErr = accessErr
	}
	status, code, handle := "ready", "", ""
	if operationErr == nil && state.operationKind == "shutdown" {
		status = "closed"
	}
	if operationErr != nil {
		status, code = "blocked", "NATIVE_WORKSPACE_OPERATION_UNCONFIRMED"
		if state.preservedOnFailure && state.State == "ready" && accessErr == nil && ctx.Err() == nil {
			status = "ready"
		}
		if nativeEnvironmentTerminationConfirmed(operationErr) && state.matchesHandle(state.handle) {
			status = "closed"
		}
	}
	if operationErr == nil && state.handle.ContainerID == "" {
		operationErr = domainError("NATIVE_WORKSPACE_ENVIRONMENT_INVALID", "Engine did not return a confirmed environment handle.")
		status, code = "blocked", "NATIVE_WORKSPACE_ENVIRONMENT_INVALID"
	}
	if state.handle.ContainerID != "" {
		if !state.matchesHandle(state.handle) {
			operationErr = domainError("NATIVE_WORKSPACE_ENVIRONMENT_INVALID", "Engine returned another environment's handle.")
			status, code = "blocked", "NATIVE_WORKSPACE_ENVIRONMENT_INVALID"
		} else {
			encoded, _ := json.Marshal(state.handle)
			handle = string(encoded)
		}
	}
	result, err := tx.ExecContext(finishCtx, `UPDATE native_workspace_environments SET status=?,handle_json=CASE WHEN ?='' THEN handle_json ELSE ? END,operation_id='',operation_kind='',last_error_code=?,updated_at=? WHERE session_id=? AND environment_id=? AND operation_id=? AND status IN ('creating','ready')`, status, handle, handle, code, formatTime(manager.store.now()), state.SessionID, state.environmentID, state.operationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "NATIVE_WORKSPACE_OPERATION_SUPERSEDED", "A late operation cannot replace cleanup or a newer binding."); err != nil {
		return err
	}
	if state.restoreRequestID != "" {
		restoreStatus := "completed"
		if operationErr != nil {
			restoreStatus = "failed"
		}
		result, err := tx.ExecContext(finishCtx, `UPDATE native_workspace_restores SET status=?,error_code=?,updated_at=? WHERE session_id=? AND request_id=? AND environment_id=? AND operation_id=? AND status='running'`, restoreStatus, code, formatTime(manager.store.now()), state.SessionID, state.restoreRequestID, state.environmentID, state.operationID)
		if err != nil {
			return err
		}
		if err := requireOneRow(result, "NATIVE_WORKSPACE_OPERATION_SUPERSEDED", "Restore receipt no longer matches the environment operation."); err != nil {
			return err
		}
	}
	if err := finishNativeCommandTx(finishCtx, tx, state, operationErr, formatTime(manager.store.now())); err != nil {
		return err
	}
	if err := finishNativePTYTx(finishCtx, tx, state, operationErr, formatTime(manager.store.now())); err != nil {
		return err
	}
	if operationErr == nil {
		for _, process := range state.terminatedPTYs {
			result, err := tx.ExecContext(finishCtx, `UPDATE native_workspace_pty_processes SET status='exited',updated_at=? WHERE session_id=? AND environment_id=? AND pty_session_id=? AND sequence=? AND status='lost'`,
				formatTime(manager.store.now()), state.SessionID, state.environmentID, process.ptyID, process.sequence)
			if err != nil {
				return err
			}
			if err := requireOneRow(result, "NATIVE_WORKSPACE_OPERATION_SUPERSEDED", "Terminal cleanup no longer owns its original process."); err != nil {
				return err
			}
		}
	}
	if status == "closed" {
		if err := closeNativePTYRecordsTx(finishCtx, tx, state.SessionID, state.environmentID, formatTime(manager.store.now())); err != nil {
			return err
		}
	}
	if err := finishNativeFileTx(finishCtx, tx, state, operationErr, formatTime(manager.store.now())); err != nil {
		return err
	}
	if err := finishNativeManifestFileTx(finishCtx, tx, state, operationErr, formatTime(manager.store.now())); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return operationErr
}

func (manager *NativeWorkspaceManager) Ensure(ctx context.Context, access NativeWorkspaceAccess) (NativeWorkspaceEnvironment, error) {
	state, err := manager.begin(ctx, access, "ensure")
	if err != nil {
		return NativeWorkspaceEnvironment{}, err
	}
	runCtx, stop := manager.watch(ctx, access, state)
	if state.operationKind == "create" {
		state.handle, err = manager.engine.CreateWorkspace(runCtx, state.environmentID, state.ownerHash, state.limits)
	} else {
		err = manager.engine.ReconnectWorkspace(runCtx, state.handle)
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

// Probe reconnects only a recorded environment; an absent or closed workspace
// must never turn into a newly created empty directory through running().
func (manager *NativeWorkspaceManager) Probe(ctx context.Context, access NativeWorkspaceAccess) (NativeWorkspaceEnvironment, error) {
	state, err := manager.begin(ctx, access, "probe")
	if err != nil {
		return NativeWorkspaceEnvironment{}, err
	}
	if state.operationID == "" {
		return state.NativeWorkspaceEnvironment, nil
	}
	runCtx, stop := manager.watch(ctx, access, state)
	err = manager.engine.ReconnectWorkspace(runCtx, state.handle)
	if err == nil {
		err = runCtx.Err()
	}
	stop()
	if err := manager.finish(ctx, access, state, err); err != nil {
		return NativeWorkspaceEnvironment{}, err
	}
	return NativeWorkspaceEnvironment{SessionID: access.SessionID, State: "ready"}, nil
}

func (manager *NativeWorkspaceManager) Shutdown(ctx context.Context, access NativeWorkspaceAccess) (NativeWorkspaceEnvironment, error) {
	state, err := manager.begin(ctx, access, "shutdown")
	if err != nil {
		return NativeWorkspaceEnvironment{}, err
	}
	if state.operationID != "" {
		runCtx, stop := manager.watch(ctx, access, state)
		err = manager.engine.DeleteWorkspace(runCtx, state.handle)
		if err == nil {
			err = runCtx.Err()
		}
		stop()
		if err := manager.finish(ctx, access, state, err); err != nil {
			return NativeWorkspaceEnvironment{}, err
		}
	}
	return NativeWorkspaceEnvironment{SessionID: access.SessionID, State: "closed"}, nil
}

func (manager *NativeWorkspaceManager) Checkpoint(ctx context.Context, access NativeWorkspaceAccess, requestID string, expectedVersion int) (NativeWorkspaceSnapshot, error) {
	if !tenantIdentifierPattern.MatchString(requestID) || expectedVersion < 0 {
		return NativeWorkspaceSnapshot{}, domainError("NATIVE_WORKSPACE_REQUEST_INVALID", "Snapshot identity and parent version are required before exporting files.")
	}
	if saved, found, err := manager.checkpointReceipt(ctx, access, requestID, expectedVersion); found || err != nil {
		return saved, err
	}
	state, err := manager.begin(ctx, access, "snapshot")
	if err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	runCtx, stop := manager.watch(ctx, access, state)
	exported, operationErr := manager.engine.ExportWorkspace(runCtx, state.handle)
	var saved NativeWorkspaceSnapshot
	state.preservedOnFailure = nativeSnapshotExportPreserved(operationErr)
	if operationErr == nil {
		parsed, parseErr := scriptsandbox.ParseWorkspaceSnapshot(exported.Archive, state.limits)
		if parseErr != nil || parsed.SHA256 != exported.SHA256 {
			operationErr = domainError("NATIVE_WORKSPACE_SNAPSHOT_INVALID", "Engine snapshot failed canonical integrity validation.")
		} else {
			// A verified export cannot change live files. A failed database save
			// must not make the janitor destroy those unsaved files.
			state.preservedOnFailure = true
			saved, operationErr = manager.store.saveNativeWorkspaceSnapshot(runCtx, access, requestID, expectedVersion, exported.Archive, state.operationID)
		}
	}
	stop()
	if err := manager.finish(ctx, access, state, operationErr); err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	return saved, nil
}

// Export implements the first half of the SDK's persist_workspace -> Snapshot
// persist lifecycle. It does not advance the persistent snapshot version.
func (manager *NativeWorkspaceManager) Export(ctx context.Context, access NativeWorkspaceAccess) (scriptsandbox.WorkspaceSnapshot, error) {
	state, err := manager.begin(ctx, access, "snapshot")
	if err != nil {
		return scriptsandbox.WorkspaceSnapshot{}, err
	}
	runCtx, stop := manager.watch(ctx, access, state)
	exported, err := manager.engine.ExportWorkspace(runCtx, state.handle)
	state.preservedOnFailure = nativeSnapshotExportPreserved(err)
	var parsed scriptsandbox.WorkspaceSnapshot
	if err == nil {
		parsed, err = scriptsandbox.ParseWorkspaceSnapshot(exported.Archive, state.limits)
		if err == nil && parsed.SHA256 != exported.SHA256 {
			err = domainError("NATIVE_WORKSPACE_SNAPSHOT_INVALID", "Exported archive does not match its integrity receipt.")
		}
	}
	if err == nil {
		err = runCtx.Err()
	}
	stop()
	if err := manager.finish(ctx, access, state, err); err != nil {
		return scriptsandbox.WorkspaceSnapshot{}, err
	}
	return parsed, nil
}

func nativeEnvironmentTerminationConfirmed(err error) bool {
	var sandboxErr *scriptsandbox.Error
	if !errors.As(err, &sandboxErr) {
		return false
	}
	return sandboxErr.Code == "WORKSPACE_TRANSFER_UNCONFIRMED" || sandboxErr.Code == "WORKSPACE_COMMAND_INTERRUPTED" || sandboxErr.Code == "WORKSPACE_PTY_INTERRUPTED"
}

func nativeSnapshotExportPreserved(err error) bool {
	var sandboxErr *scriptsandbox.Error
	if !errors.As(err, &sandboxErr) {
		return false
	}
	switch sandboxErr.Code {
	case "WORKSPACE_BUSY", "WORKSPACE_ADAPTER_UNAVAILABLE", "WORKSPACE_ARCHIVE_ENTRY_UNSAFE", "WORKSPACE_ARCHIVE_LIMIT_EXCEEDED", "WORKSPACE_ARCHIVE_PATH_UNSAFE", "WORKSPACE_ARCHIVE_PATH_COLLISION":
		return true
	}
	return false
}

func (manager *NativeWorkspaceManager) checkpointReceipt(ctx context.Context, access NativeWorkspaceAccess, requestID string, expectedVersion int) (NativeWorkspaceSnapshot, bool, error) {
	tx, err := manager.store.db.BeginTx(ctx, nil)
	if err != nil {
		return NativeWorkspaceSnapshot{}, false, err
	}
	defer tx.Rollback()
	if _, err := manager.store.nativeWorkspaceAccessTx(ctx, tx, access); err != nil {
		return NativeWorkspaceSnapshot{}, false, err
	}
	var version int
	var hash string
	err = tx.QueryRowContext(ctx, `SELECT version,content_hash FROM native_workspace_snapshots WHERE session_id=? AND request_id=?`, access.SessionID, requestID).Scan(&version, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return NativeWorkspaceSnapshot{}, false, nil
	}
	if err != nil {
		return NativeWorkspaceSnapshot{}, false, err
	}
	if version != expectedVersion+1 {
		return NativeWorkspaceSnapshot{}, false, domainError("NATIVE_WORKSPACE_REQUEST_CONFLICT", "Checkpoint request was reused with another parent version.")
	}
	if err := validateNativeSnapshotOperationTx(ctx, tx, access.SessionID, ""); err != nil {
		return NativeWorkspaceSnapshot{}, false, err
	}
	result, err := readNativeWorkspaceSnapshotTx(ctx, tx, access.SessionID, version, hash)
	return result, true, err
}

type NativeWorkspaceCleanupReport struct {
	Attempted int `json:"attempted"`
	Confirmed int `json:"confirmed"`
	Pending   int `json:"pending"`
}

func nativeJanitorIdentity(ctx context.Context) bool {
	principal, ok := identity.FromContext(ctx)
	_, activity := AgentActivityFromContext(ctx)
	return ok && principal.Kind == identity.KindService && !activity
}

// Sweep is bounded and explicit. Constructing a manager does not start a
// janitor or change any environment outside an authorized host lifecycle.
func (manager *NativeWorkspaceManager) Sweep(ctx context.Context, limit int) (NativeWorkspaceCleanupReport, error) {
	var report NativeWorkspaceCleanupReport
	if !nativeJanitorIdentity(ctx) || limit < 1 || limit > 32 {
		return report, domainError("AGENT_ACTIVITY_FORBIDDEN", "Bounded cleanup requires the internal janitor identity.")
	}
	now := formatTime(manager.store.now())
	rows, err := manager.store.db.QueryContext(ctx, `SELECT e.session_id FROM native_workspace_environments e JOIN native_workspace_leases l ON l.session_id=e.session_id WHERE e.status!='closed' AND (e.status IN ('blocked','closing') OR l.status='closing' OR l.lease_until<=?) AND NOT(e.status='closing' AND e.operation_id!='' AND e.operation_until>?) ORDER BY e.updated_at,e.session_id LIMIT ?`, now, now, limit)
	if err != nil {
		return report, err
	}
	var sessions []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			rows.Close()
			return report, err
		}
		sessions = append(sessions, sessionID)
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return report, rowErr
	}
	for _, sessionID := range sessions {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		report.Attempted++
		if err := manager.Cleanup(ctx, sessionID); err != nil {
			report.Pending++
		} else {
			report.Confirmed++
		}
	}
	return report, nil
}

// Cleanup is an internal janitor entry point, not a delegated Agent operation.
// Unknown absence is not proof of deletion: only a verified exact handle may
// be deleted, and only a confirmed engine receipt transitions to closed.
func (manager *NativeWorkspaceManager) Cleanup(ctx context.Context, sessionID string) error {
	if !nativeJanitorIdentity(ctx) || !nativeSessionIDPattern.MatchString(sessionID) {
		return domainError("AGENT_ACTIVITY_FORBIDDEN", "Environment cleanup requires the internal janitor identity.")
	}
	tx, err := manager.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state, err := scanNativeEnvironment(tx.QueryRowContext(ctx, nativeEnvironmentSelect+` WHERE session_id=?`, sessionID))
	if err != nil {
		return err
	}
	if state.State == "closed" {
		return nil
	}
	var eligible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_workspace_leases WHERE session_id=? AND (status='closing' OR lease_until<=?))`, sessionID, formatTime(manager.store.now())).Scan(&eligible)
	if err != nil {
		return err
	}
	if !eligible && state.State != "blocked" && state.State != "closing" {
		return domainError("NATIVE_WORKSPACE_BUSY", "A live execution environment is not eligible for janitor cleanup.")
	}
	if state.State == "closing" && state.operationID != "" {
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT operation_until>? FROM native_workspace_environments WHERE session_id=?`, formatTime(manager.store.now()), sessionID).Scan(&active); err != nil {
			return err
		}
		if active {
			return domainError("NATIVE_WORKSPACE_OPERATION_IN_PROGRESS", "An existing cleanup must finish or reach its bounded deadline.")
		}
	}
	cleanupID := manager.store.newID("nwc")
	if _, err := tx.ExecContext(ctx, `UPDATE native_workspace_environments SET status='closing',operation_id=?,operation_kind='cleanup',operation_until=?,updated_at=? WHERE session_id=?`, cleanupID, formatTime(manager.store.now().Add(90*time.Second)), formatTime(manager.store.now()), sessionID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if state.handle.ContainerID == "" {
		state.handle, _, err = manager.engine.FindWorkspace(cleanupCtx, state.environmentID, state.ownerHash, state.limits)
	}
	if err == nil && !state.matchesHandle(state.handle) {
		err = domainError("NATIVE_WORKSPACE_ENVIRONMENT_INVALID", "Cleanup lookup returned a mismatched environment.")
	}
	if err == nil {
		err = manager.engine.DeleteWorkspace(cleanupCtx, state.handle)
	}
	if err != nil {
		return domainError("NATIVE_WORKSPACE_CLEANUP_UNCONFIRMED", "Environment cleanup remains unconfirmed; its capacity reservation was retained.")
	}
	finishCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer stop()
	finishTx, err := manager.store.db.BeginTx(finishCtx, nil)
	if err != nil {
		return err
	}
	defer finishTx.Rollback()
	now := formatTime(manager.store.now())
	result, err := finishTx.ExecContext(finishCtx, `UPDATE native_workspace_environments SET status='closed',operation_id='',operation_kind='',last_error_code='',updated_at=? WHERE session_id=? AND environment_id=? AND operation_id=? AND status='closing'`, now, sessionID, state.environmentID, cleanupID)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "NATIVE_WORKSPACE_OPERATION_SUPERSEDED", "Cleanup receipt no longer owns the environment record."); err != nil {
		return err
	}
	if _, err := finishTx.ExecContext(finishCtx, `UPDATE native_workspace_commands SET status='failed',reserved_bytes=0,error_code='NATIVE_WORKSPACE_COMMAND_UNCONFIRMED',updated_at=? WHERE session_id=? AND environment_id=? AND status='running'`, now, sessionID, state.environmentID); err != nil {
		return err
	}
	if err := closeNativePTYRecordsTx(finishCtx, finishTx, sessionID, state.environmentID, now); err != nil {
		return err
	}
	if _, err := finishTx.ExecContext(finishCtx, `UPDATE native_workspace_file_operations SET status='failed',reserved_bytes=0,updated_at=? WHERE session_id=? AND environment_id=? AND status='running'`, now, sessionID, state.environmentID); err != nil {
		return err
	}
	if _, err := finishTx.ExecContext(finishCtx, `UPDATE native_workspace_manifests SET status='failed',updated_at=? WHERE session_id=? AND status='prepared'`, now, sessionID); err != nil {
		return err
	}
	return finishTx.Commit()
}

func validateNativeSnapshotOperationTx(ctx context.Context, tx *sql.Tx, sessionID, operationID string) error {
	if err := nativeManifestPendingTx(ctx, tx, sessionID); err != nil {
		return err
	}
	var current, kind, status string
	err := tx.QueryRowContext(ctx, `SELECT operation_id,operation_kind,status FROM native_workspace_environments WHERE session_id=?`, sessionID).Scan(&current, &kind, &status)
	if errors.Is(err, sql.ErrNoRows) && operationID == "" {
		return nil
	}
	if err != nil {
		return err
	}
	if operationID == "" && current == "" && status == "ready" || operationID != "" && operationID == current && kind == "snapshot" && status == "ready" {
		return nil
	}
	return domainError("NATIVE_WORKSPACE_OPERATION_IN_PROGRESS", fmt.Sprintf("Snapshot cannot replace environment state while it is %s.", status))
}
