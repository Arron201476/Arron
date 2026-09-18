package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"

	"content-agent/backend/internal/scriptsandbox"
)

const nativeWorkspacePatchTool = "runtime:apply_patch"
const nativeFileReceiptBudget = 2048

var nativeFileRequestIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

const nativeWorkspaceFileSchema = `
CREATE TABLE IF NOT EXISTS native_workspace_file_operations (
	session_id TEXT NOT NULL REFERENCES native_workspace_leases(session_id),
	request_id TEXT NOT NULL,
	agent_tool_call_id TEXT NOT NULL REFERENCES agent_tool_calls(agent_tool_call_id),
	sdk_tool_call_id TEXT NOT NULL,
	arguments_hash TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	environment_id TEXT NOT NULL,
	operation_id TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('running','completed','failed','deleted')),
	result_json TEXT NOT NULL DEFAULT '',
	result_hash TEXT NOT NULL DEFAULT '',
	reserved_bytes INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(session_id,request_id)
);
CREATE INDEX IF NOT EXISTS native_workspace_file_call ON native_workspace_file_operations(agent_tool_call_id);
`

type NativeWorkspaceFileRequest struct {
	RequestID           string                               `json:"request_id"`
	AgentToolCallID     string                               `json:"agent_tool_call_id"`
	SDKToolCallID       string                               `json:"sdk_tool_call_id"`
	ArgumentsHash       string                               `json:"arguments_hash"`
	ConfigurationHash   string                               `json:"configuration_hash"`
	MaterializationHash string                               `json:"materialization_hash,omitempty"`
	File                scriptsandbox.WorkspaceFileOperation `json:"file"`
}

type NativeWorkspaceFileReceipt struct {
	SessionID   string                            `json:"session_id"`
	RequestID   string                            `json:"request_id"`
	RequestHash string                            `json:"request_hash"`
	File        scriptsandbox.WorkspaceFileResult `json:"file"`
}

type nativeFileRecord struct {
	sessionID, requestID, callID, sdkID, argumentsHash, requestHash string
	environmentID, operationID, status, resultJSON, resultHash      string
	reservedBytes                                                   int64
}

const nativeFileSelect = `SELECT session_id,request_id,agent_tool_call_id,sdk_tool_call_id,arguments_hash,request_hash,environment_id,operation_id,status,result_json,result_hash,reserved_bytes FROM native_workspace_file_operations`

func scanNativeFile(row interface{ Scan(...any) error }) (nativeFileRecord, error) {
	var r nativeFileRecord
	err := row.Scan(&r.sessionID, &r.requestID, &r.callID, &r.sdkID, &r.argumentsHash, &r.requestHash,
		&r.environmentID, &r.operationID, &r.status, &r.resultJSON, &r.resultHash, &r.reservedBytes)
	return r, err
}

func (r nativeFileRecord) confirmed() bool {
	return r.status == "completed" && r.reservedBytes == 0 && len(r.resultJSON) > 0 && len(r.resultJSON) <= nativeFileReceiptBudget &&
		r.resultHash == sha256Hex([]byte(r.resultJSON))
}

func (s *Store) validateNativeFileApprovalTx(ctx context.Context, tx *sql.Tx, r nativeFileRecord, allowCompleted bool) (AgentToolCall, error) {
	activity, ok := AgentActivityFromContext(ctx)
	if !ok {
		return AgentToolCall{}, domainError("AGENT_ACTIVITY_FORBIDDEN", "A native file write requires its execution identity.")
	}
	if err := validateAgentActivityToolCallQuery(ctx, tx, activity, r.callID); err != nil {
		return AgentToolCall{}, err
	}
	call, err := getAgentToolCallTx(ctx, tx, r.callID)
	if err != nil {
		return call, err
	}
	if call.ToolID != nativeWorkspacePatchTool || call.ToolKind != "runtime_function" || call.SDKToolCallID != r.sdkID || call.ArgumentsHash != r.argumentsHash ||
		call.ParentToolCallID != "" || call.ApprovalPolicy != "always" || call.ApprovalStatus != "approved" || call.ApprovalConsumedAt == nil ||
		(call.AccessMode != "sensitive" && call.AccessMode != "write") || (call.Status != "running" && !(allowCompleted && call.Status == "completed")) {
		return call, domainError("NATIVE_WORKSPACE_FILE_APPROVAL_REQUIRED", "File writes require the exact running, explicitly approved native patch call.")
	}
	approval, err := getAgentToolApprovalForCallTx(ctx, tx, r.callID)
	if err != nil {
		return call, err
	}
	if approval.Status != "approved" {
		return call, domainError("NATIVE_WORKSPACE_FILE_APPROVAL_REQUIRED", "Native patch approval is no longer valid.")
	}
	return call, s.validateAgentToolConfigurationTx(ctx, tx, call, "")
}

func nativeFileMutation(operation scriptsandbox.WorkspaceFileOperation) bool {
	// The fixed root exists before any file RPC. SDK mkdir(root, parents=True)
	// only confirms that root; this exact no-op grants no other bootstrap write.
	return operation.Mutates() && !(operation.Operation == "mkdir" && operation.Path == "." && operation.Parents)
}

func (manager *NativeWorkspaceManager) beginFile(ctx context.Context, access NativeWorkspaceAccess, request NativeWorkspaceFileRequest) (nativeEnvironment, *NativeWorkspaceFileReceipt, string, error) {
	var state nativeEnvironment
	if !manager.requirePolicy {
		return state, nil, "", domainError("NATIVE_WORKSPACE_POLICY_REQUIRED", "File operations require the current administrator sandbox policy.")
	}
	if err := scriptsandbox.ValidateWorkspaceFileOperation(request.File, manager.limits); err != nil {
		return state, nil, "", err
	}
	if !nativeFileRequestIDPattern.MatchString(request.RequestID) || request.ConfigurationHash != "" {
		return state, nil, "", domainError("NATIVE_WORKSPACE_REQUEST_INVALID", "File operation identity or configuration is invalid.")
	}
	mutates := nativeFileMutation(request.File)
	hasCall := request.AgentToolCallID != "" || request.SDKToolCallID != "" || request.ArgumentsHash != ""
	materializes := request.MaterializationHash != ""
	if materializes && (hasCall || !nativeLeaseKeyPattern.MatchString(request.MaterializationHash) || !mutates) {
		return state, nil, "", domainError("NATIVE_WORKSPACE_MANIFEST_INVALID", "Materialization cannot impersonate a model call or authorize reads.")
	}
	if (mutates && !materializes || hasCall) && (request.AgentToolCallID == "" || len(request.AgentToolCallID) > 128 || request.SDKToolCallID == "" || len(request.SDKToolCallID) > 512 || !nativeLeaseKeyPattern.MatchString(request.ArgumentsHash)) {
		return state, nil, "", domainError("NATIVE_WORKSPACE_FILE_APPROVAL_REQUIRED", "Native file writes require a bounded approved call identity.")
	}
	encoded, err := json.Marshal(request.File)
	if err != nil {
		return state, nil, "", err
	}
	r := nativeFileRecord{sessionID: access.SessionID, requestID: request.RequestID, callID: request.AgentToolCallID, sdkID: request.SDKToolCallID,
		argumentsHash: request.ArgumentsHash, requestHash: sha256Hex(encoded)}
	tx, err := manager.store.db.BeginTx(ctx, nil)
	if err != nil {
		return state, nil, "", err
	}
	defer tx.Rollback()
	lease, err := manager.store.nativeWorkspaceAccessTx(ctx, tx, access)
	if err != nil {
		return state, nil, "", err
	}
	if err := manager.validatePolicyTx(ctx, tx, lease.workspaceID); err != nil {
		return state, nil, "", err
	}
	var manifestOperation *nativeManifestOperation
	if materializes {
		manifestOperation, err = beginNativeManifestFileTx(ctx, tx, access.SessionID, request.MaterializationHash, request.File)
		if err != nil {
			return state, nil, "", err
		}
	} else if !(request.File.Operation == "mkdir" && request.File.Path == "." && request.File.Parents && !hasCall) {
		if err := nativeManifestPendingTx(ctx, tx, access.SessionID); err != nil {
			return state, nil, "", err
		}
	}
	if hasCall {
		call, err := manager.store.validateNativeFileApprovalTx(ctx, tx, r, mutates)
		if err != nil {
			return state, nil, "", err
		}
		if call.WorkspaceID != lease.workspaceID || call.ProjectID != lease.projectID {
			return state, nil, "", domainError("NATIVE_WORKSPACE_SCOPE_MISMATCH", "File call and execution workspace have different owners.")
		}
		if mutates {
			existing, err := scanNativeFile(tx.QueryRowContext(ctx, nativeFileSelect+` WHERE session_id=? AND request_id=?`, access.SessionID, request.RequestID))
			if err == nil {
				if existing.callID != r.callID || existing.sdkID != r.sdkID || existing.argumentsHash != r.argumentsHash || existing.requestHash != r.requestHash {
					return state, nil, "", domainError("NATIVE_WORKSPACE_REQUEST_CONFLICT", "File request identity cannot be reused for different content or authority.")
				}
				var result scriptsandbox.WorkspaceFileResult
				if !existing.confirmed() || json.Unmarshal([]byte(existing.resultJSON), &result) != nil || scriptsandbox.ValidateWorkspaceFileResult(request.File, result, manager.limits) != nil {
					return state, nil, "", domainError("NATIVE_WORKSPACE_FILE_UNCONFIRMED", "File request is running, failed or has an invalid receipt; it was not replayed.")
				}
				return state, &NativeWorkspaceFileReceipt{SessionID: access.SessionID, RequestID: request.RequestID, RequestHash: r.requestHash, File: result}, r.requestHash, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return state, nil, "", err
			}
			if call.Status != "running" {
				return state, nil, "", domainError("NATIVE_WORKSPACE_FILE_APPROVAL_REQUIRED", "A completed patch cannot start another file write.")
			}
		}
	}
	// An after-turn pause must let an already-started patch finish its remaining
	// file operations. It still cannot create a new approved tool invocation.
	if _, err := manager.store.nativeWorkspaceOwnerTx(ctx, tx, access.DispatchGeneration, hasCall); err != nil {
		return state, nil, "", err
	}
	if mutates && !materializes {
		var sessionCount, callCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN agent_tool_call_id=? THEN 1 ELSE 0 END),0) FROM native_workspace_file_operations WHERE session_id=?`, r.callID, access.SessionID).Scan(&sessionCount, &callCount); err != nil {
			return state, nil, "", err
		}
		if sessionCount >= 4096 || callCount >= 1024 {
			return state, nil, "", domainError("NATIVE_WORKSPACE_QUOTA_EXCEEDED", "Native file receipt history quota reached.")
		}
		if err := manager.store.enforceWorkspaceQuotaTx(ctx, tx, lease.workspaceID, "storage_bytes", nativeFileReceiptBudget); err != nil {
			return state, nil, "", err
		}
	}
	state, err = scanNativeEnvironment(tx.QueryRowContext(ctx, nativeEnvironmentSelect+` WHERE session_id=?`, access.SessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil, "", domainError("NATIVE_WORKSPACE_ENVIRONMENT_NOT_FOUND", "File access never creates an implicit environment.")
	}
	if err != nil {
		return state, nil, "", err
	}
	state, err = manager.claimBindingTx(ctx, tx, state, manager.store.newID("nwo"), "file")
	if err != nil {
		return state, nil, "", err
	}
	if hasCall {
		state.fileCall = &r
	}
	state.manifestOperation = manifestOperation
	if mutates && !materializes {
		r.environmentID, r.operationID, r.status, r.reservedBytes = state.environmentID, state.operationID, "running", nativeFileReceiptBudget
		now := formatTime(manager.store.now())
		if _, err := tx.ExecContext(ctx, `INSERT INTO native_workspace_file_operations(session_id,request_id,agent_tool_call_id,sdk_tool_call_id,arguments_hash,request_hash,environment_id,operation_id,status,reserved_bytes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,'running',?,?,?)`, r.sessionID, r.requestID, r.callID, r.sdkID, r.argumentsHash, r.requestHash, r.environmentID, r.operationID, r.reservedBytes, now, now); err != nil {
			return state, nil, "", err
		}
		state.fileOperation = &r
	}
	return state, nil, r.requestHash, tx.Commit()
}

type nativeWorkspaceFileEngine interface {
	FileWorkspace(context.Context, scriptsandbox.WorkspaceHandle, scriptsandbox.WorkspaceFileOperation) (scriptsandbox.WorkspaceFileResult, error)
}

var _ nativeWorkspaceFileEngine = (*scriptsandbox.OCI)(nil)

func (manager *NativeWorkspaceManager) File(ctx context.Context, access NativeWorkspaceAccess, request NativeWorkspaceFileRequest) (NativeWorkspaceFileReceipt, error) {
	request.File.Data = bytes.Clone(request.File.Data)
	if request.File.Mode != nil {
		mode := *request.File.Mode
		request.File.Mode = &mode
	}
	engine, ok := manager.engine.(nativeWorkspaceFileEngine)
	if !ok {
		return NativeWorkspaceFileReceipt{}, domainError("NATIVE_WORKSPACE_UNAVAILABLE", "The isolated engine has no bounded file transport.")
	}
	state, saved, hash, err := manager.beginFile(ctx, access, request)
	if err != nil {
		return NativeWorkspaceFileReceipt{}, err
	}
	if saved != nil {
		return *saved, nil
	}
	runCtx, stop := manager.watch(ctx, access, state)
	result, operationErr := engine.FileWorkspace(runCtx, state.handle, request.File)
	state.preservedOnFailure = scriptsandbox.WorkspaceFileFailurePreserved(operationErr)
	if operationErr == nil {
		operationErr = runCtx.Err()
	}
	if operationErr == nil {
		operationErr = scriptsandbox.ValidateWorkspaceFileResult(request.File, result, state.limits)
	}
	if result.Data == nil {
		result.Data = []byte{}
	}
	if operationErr == nil && state.fileOperation != nil {
		encoded, err := json.Marshal(result)
		if err != nil || len(encoded) > nativeFileReceiptBudget {
			operationErr = domainError("NATIVE_WORKSPACE_FILE_UNCONFIRMED", "File mutation receipt exceeds its metadata bound.")
		} else {
			state.fileOperation.resultJSON, state.fileOperation.resultHash = string(encoded), sha256Hex(encoded)
		}
	}
	stop()
	if err := manager.finish(ctx, access, state, operationErr); err != nil {
		return NativeWorkspaceFileReceipt{}, err
	}
	return NativeWorkspaceFileReceipt{SessionID: access.SessionID, RequestID: request.RequestID, RequestHash: hash, File: result}, nil
}

func finishNativeFileTx(ctx context.Context, tx *sql.Tx, state nativeEnvironment, operationErr error, now string) error {
	r := state.fileOperation
	if r == nil {
		return nil
	}
	status := "completed"
	if operationErr != nil {
		status, r.resultJSON, r.resultHash = "failed", "", ""
	}
	result, err := tx.ExecContext(ctx, `UPDATE native_workspace_file_operations SET status=?,result_json=?,result_hash=?,reserved_bytes=0,updated_at=? WHERE session_id=? AND request_id=? AND environment_id=? AND operation_id=? AND status='running'`, status, r.resultJSON, r.resultHash, now, r.sessionID, r.requestID, r.environmentID, r.operationID)
	if err != nil {
		return err
	}
	return requireOneRow(result, "NATIVE_WORKSPACE_OPERATION_SUPERSEDED", "File receipt no longer belongs to the original environment operation.")
}

func verifyNativePatchReceiptsTx(ctx context.Context, tx *sql.Tx, call AgentToolCall) error {
	rows, err := tx.QueryContext(ctx, nativeFileSelect+` WHERE agent_tool_call_id=?`, call.AgentToolCallID)
	if err != nil {
		return err
	}
	defer rows.Close()
	// An SDK no-op may have no mutations. Every actual mutation, however, must
	// have a confirmed receipt; tool output alone cannot replace those receipts.
	for rows.Next() {
		r, err := scanNativeFile(rows)
		if err != nil {
			return err
		}
		if !r.confirmed() || r.argumentsHash != call.ArgumentsHash || r.sdkID != call.SDKToolCallID {
			return domainError("NATIVE_WORKSPACE_FILE_UNCONFIRMED", "A native patch cannot complete while any file mutation is unconfirmed.")
		}
	}
	return rows.Err()
}
