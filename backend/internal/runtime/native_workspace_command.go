package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"content-agent/backend/internal/scriptsandbox"
)

const nativeWorkspaceExecTool = "runtime:exec_command"
const nativeCommandOutputBudget = 2 << 20

const nativeWorkspaceCommandSchema = `
CREATE TABLE IF NOT EXISTS native_workspace_commands (
	agent_tool_call_id TEXT PRIMARY KEY REFERENCES agent_tool_calls(agent_tool_call_id),
	session_id TEXT NOT NULL REFERENCES native_workspace_leases(session_id),
	environment_id TEXT NOT NULL,
	operation_id TEXT NOT NULL,
	sdk_tool_call_id TEXT NOT NULL,
	arguments_hash TEXT NOT NULL,
	configuration_hash TEXT NOT NULL,
	command_hash TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('running','completed','failed','deleted')),
	stdout BLOB NOT NULL DEFAULT X'',
	stderr BLOB NOT NULL DEFAULT X'',
	exit_code INTEGER NOT NULL DEFAULT 0,
	result_hash TEXT NOT NULL DEFAULT '',
	reserved_bytes INTEGER NOT NULL,
	error_code TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS native_workspace_command_session ON native_workspace_commands(session_id);
`

type NativeWorkspaceCommandRequest struct {
	AgentToolCallID   string                         `json:"agent_tool_call_id"`
	SDKToolCallID     string                         `json:"sdk_tool_call_id"`
	Arguments         json.RawMessage                `json:"arguments"`
	ConfigurationHash string                         `json:"configuration_hash"`
	Command           scriptsandbox.WorkspaceCommand `json:"command"`
}

type NativeWorkspaceCommandReceipt struct {
	SessionID       string `json:"session_id"`
	AgentToolCallID string `json:"agent_tool_call_id"`
	CommandHash     string `json:"command_hash"`
	ResultHash      string `json:"result_hash"`
	Stdout          []byte `json:"stdout"`
	Stderr          []byte `json:"stderr"`
	ExitCode        int    `json:"exit_code"`
}

type nativeCommandRecord struct {
	NativeWorkspaceCommandReceipt
	environmentID, operationID, sdkID, argumentsHash, configurationHash, status string
	reservedBytes                                                               int64
}

const nativeCommandSelect = `SELECT session_id,agent_tool_call_id,command_hash,result_hash,stdout,stderr,exit_code,environment_id,operation_id,sdk_tool_call_id,arguments_hash,configuration_hash,status,reserved_bytes FROM native_workspace_commands`

func scanNativeCommand(row interface{ Scan(...any) error }) (nativeCommandRecord, error) {
	var r nativeCommandRecord
	err := row.Scan(&r.SessionID, &r.AgentToolCallID, &r.CommandHash, &r.ResultHash, &r.Stdout, &r.Stderr, &r.ExitCode,
		&r.environmentID, &r.operationID, &r.sdkID, &r.argumentsHash, &r.configurationHash, &r.status, &r.reservedBytes)
	return r, err
}

func nativeCommandResultHash(stdout, stderr []byte, exitCode int) string {
	if stdout == nil {
		stdout = []byte{}
	}
	if stderr == nil {
		stderr = []byte{}
	}
	// Encode both streams separately so different stdout/stderr splits cannot collide.
	data, _ := json.Marshal(struct {
		Stdout, Stderr []byte
		ExitCode       int
	}{stdout, stderr, exitCode})
	return sha256Hex(data)
}

func (r nativeCommandRecord) confirmed() bool {
	return r.status == "completed" && r.ExitCode >= 0 && r.ExitCode <= 255 && len(r.Stdout) <= 1<<20 && len(r.Stderr) <= 1<<20 &&
		r.reservedBytes == 0 && nativeLeaseKeyPattern.MatchString(r.CommandHash) &&
		r.ResultHash == nativeCommandResultHash(r.Stdout, r.Stderr, r.ExitCode)
}

func (s *Store) validateNativeCommandApprovalTx(ctx context.Context, tx *sql.Tx, r nativeCommandRecord, allowCompleted bool) (AgentToolCall, error) {
	if r.configurationHash != "" {
		return AgentToolCall{}, domainError("NATIVE_WORKSPACE_COMMAND_APPROVAL_REQUIRED", "Native shell tools cannot substitute a configured tool.")
	}
	return s.validateNativeShellApprovalTx(ctx, tx, r.AgentToolCallID, r.sdkID, r.argumentsHash, nativeWorkspaceExecTool, allowCompleted)
}

func (s *Store) validateNativeShellApprovalTx(ctx context.Context, tx *sql.Tx, callID, sdkID, argumentHash, toolID string, allowCompleted bool) (AgentToolCall, error) {
	activity, ok := AgentActivityFromContext(ctx)
	if !ok {
		return AgentToolCall{}, domainError("AGENT_ACTIVITY_FORBIDDEN", "A native command requires its execution identity.")
	}
	if err := validateAgentActivityToolCallQuery(ctx, tx, activity, callID); err != nil {
		return AgentToolCall{}, err
	}
	call, err := getAgentToolCallTx(ctx, tx, callID)
	if err != nil {
		return call, err
	}
	if (toolID != nativeWorkspaceExecTool && toolID != nativeWorkspaceStdinTool) || call.ToolID != toolID || call.ToolKind != "runtime_function" || call.SDKToolCallID != sdkID || call.ArgumentsHash != argumentHash ||
		call.ParentToolCallID != "" || call.ApprovalPolicy != "always" || call.ApprovalStatus != "approved" || call.ApprovalConsumedAt == nil ||
		(call.AccessMode != "sensitive" && call.AccessMode != "write") || (call.Status != "running" && !(allowCompleted && call.Status == "completed")) {
		return call, domainError("NATIVE_WORKSPACE_COMMAND_APPROVAL_REQUIRED", "The exact command must belong to a running, explicitly approved tool call.")
	}
	approval, err := getAgentToolApprovalForCallTx(ctx, tx, call.AgentToolCallID)
	if err != nil {
		return call, err
	}
	if approval.Status != "approved" {
		return call, domainError("NATIVE_WORKSPACE_COMMAND_APPROVAL_REQUIRED", "Command approval is no longer valid.")
	}
	if err := s.validateAgentToolConfigurationTx(ctx, tx, call, ""); err != nil {
		return call, err
	}
	return call, nil
}

func (manager *NativeWorkspaceManager) beginCommand(ctx context.Context, access NativeWorkspaceAccess, request NativeWorkspaceCommandRequest) (nativeEnvironment, *NativeWorkspaceCommandReceipt, error) {
	var state nativeEnvironment
	if !manager.requirePolicy {
		return state, nil, domainError("NATIVE_WORKSPACE_POLICY_REQUIRED", "Commands require the current administrator resource policy.")
	}
	if err := scriptsandbox.ValidateWorkspaceCommand(request.Command, manager.limits); err != nil {
		return state, nil, err
	}
	if request.AgentToolCallID == "" || len(request.AgentToolCallID) > 128 || request.SDKToolCallID == "" || len(request.SDKToolCallID) > 512 || len(request.Arguments) == 0 || len(request.Arguments) > 64<<10 {
		return state, nil, domainError("NATIVE_WORKSPACE_REQUEST_INVALID", "Command must carry bounded original SDK tool arguments and call identity.")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(request.Arguments, &object) != nil || object == nil {
		return state, nil, domainError("NATIVE_WORKSPACE_REQUEST_INVALID", "SDK command arguments must be an object.")
	}
	_, argumentHash, _, err := summarizeAgentToolPayload(request.Arguments, true)
	if err != nil {
		return state, nil, err
	}
	prepared, err := json.Marshal(request.Command)
	if err != nil {
		return state, nil, err
	}
	r := nativeCommandRecord{NativeWorkspaceCommandReceipt: NativeWorkspaceCommandReceipt{SessionID: access.SessionID, AgentToolCallID: request.AgentToolCallID, CommandHash: sha256Hex(prepared)},
		sdkID: request.SDKToolCallID, argumentsHash: argumentHash, configurationHash: request.ConfigurationHash}
	tx, err := manager.store.db.BeginTx(ctx, nil)
	if err != nil {
		return state, nil, err
	}
	defer tx.Rollback()
	lease, err := manager.store.nativeWorkspaceAccessTx(ctx, tx, access)
	if err != nil {
		return state, nil, err
	}
	if err := nativeManifestPendingTx(ctx, tx, access.SessionID); err != nil {
		return state, nil, err
	}
	if err := manager.validatePolicyTx(ctx, tx, lease.workspaceID); err != nil {
		return state, nil, err
	}
	call, err := manager.store.validateNativeCommandApprovalTx(ctx, tx, r, true)
	if err != nil {
		return state, nil, err
	}
	if call.WorkspaceID != lease.workspaceID || call.ProjectID != lease.projectID {
		return state, nil, domainError("NATIVE_WORKSPACE_SCOPE_MISMATCH", "Command and execution workspace have different owners.")
	}
	var pty bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_workspace_pty_operations WHERE agent_tool_call_id=?)`, r.AgentToolCallID).Scan(&pty); err != nil {
		return state, nil, err
	}
	if pty {
		return state, nil, domainError("NATIVE_WORKSPACE_REQUEST_CONFLICT", "An interactive command cannot fall back to a new one-shot execution.")
	}
	existing, err := scanNativeCommand(tx.QueryRowContext(ctx, nativeCommandSelect+` WHERE agent_tool_call_id=?`, r.AgentToolCallID))
	if err == nil {
		if existing.SessionID != r.SessionID || existing.CommandHash != r.CommandHash || existing.argumentsHash != r.argumentsHash || existing.sdkID != r.sdkID || existing.configurationHash != r.configurationHash {
			return state, nil, domainError("NATIVE_WORKSPACE_REQUEST_CONFLICT", "The original command request cannot be replaced.")
		}
		if !existing.confirmed() {
			return state, nil, domainError("NATIVE_WORKSPACE_COMMAND_UNCONFIRMED", "Command is running, failed or has an invalid receipt; it was not replayed.")
		}
		return state, &existing.NativeWorkspaceCommandReceipt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return state, nil, err
	}
	if call.Status != "running" {
		return state, nil, domainError("NATIVE_WORKSPACE_COMMAND_APPROVAL_REQUIRED", "A completed tool cannot start a new command.")
	}
	if _, err := manager.store.nativeWorkspaceOwnerTx(ctx, tx, access.DispatchGeneration, false); err != nil {
		return state, nil, err
	}
	var count int
	var used int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(stdout)+length(stderr)+reserved_bytes),0) FROM native_workspace_commands WHERE session_id=?`, access.SessionID).Scan(&count, &used); err != nil {
		return state, nil, err
	}
	if count >= 128 || used+nativeCommandOutputBudget > 128<<20 {
		return state, nil, domainError("NATIVE_WORKSPACE_QUOTA_EXCEEDED", "Command result history quota reached.")
	}
	if err := manager.store.enforceWorkspaceQuotaTx(ctx, tx, lease.workspaceID, "storage_bytes", nativeCommandOutputBudget); err != nil {
		return state, nil, err
	}
	state, err = scanNativeEnvironment(tx.QueryRowContext(ctx, nativeEnvironmentSelect+` WHERE session_id=?`, access.SessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil, domainError("NATIVE_WORKSPACE_ENVIRONMENT_NOT_FOUND", "Command execution does not implicitly create an environment.")
	}
	if err != nil {
		return state, nil, err
	}
	state, err = manager.claimBindingTx(ctx, tx, state, manager.store.newID("nwo"), "command")
	if err != nil {
		return state, nil, err
	}
	r.environmentID, r.operationID, r.status = state.environmentID, state.operationID, "running"
	r.reservedBytes = nativeCommandOutputBudget
	now := formatTime(manager.store.now())
	_, err = tx.ExecContext(ctx, `INSERT INTO native_workspace_commands(agent_tool_call_id,session_id,environment_id,operation_id,sdk_tool_call_id,arguments_hash,configuration_hash,command_hash,status,reserved_bytes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,'running',?,?,?)`, r.AgentToolCallID, r.SessionID, r.environmentID, r.operationID, r.sdkID, r.argumentsHash, r.configurationHash, r.CommandHash, r.reservedBytes, now, now)
	if err != nil {
		return state, nil, err
	}
	state.command = &r
	return state, nil, tx.Commit()
}

type nativeWorkspaceCommandEngine interface {
	ExecuteWorkspace(context.Context, scriptsandbox.WorkspaceHandle, scriptsandbox.WorkspaceCommand) (scriptsandbox.WorkspaceCommandResult, error)
}

var _ nativeWorkspaceCommandEngine = (*scriptsandbox.OCI)(nil)

func (manager *NativeWorkspaceManager) ExecuteCommand(ctx context.Context, access NativeWorkspaceAccess, request NativeWorkspaceCommandRequest) (NativeWorkspaceCommandReceipt, error) {
	request.Arguments = bytes.Clone(request.Arguments)
	request.Command.Argv = slices.Clone(request.Command.Argv)
	request.Command.Stdin = bytes.Clone(request.Command.Stdin)
	engine, ok := manager.engine.(nativeWorkspaceCommandEngine)
	if !ok {
		return NativeWorkspaceCommandReceipt{}, domainError("NATIVE_WORKSPACE_UNAVAILABLE", "The isolated engine has no command transport.")
	}
	state, saved, err := manager.beginCommand(ctx, access, request)
	if err != nil {
		return NativeWorkspaceCommandReceipt{}, err
	}
	if saved != nil {
		return *saved, nil
	}
	runCtx, stop := manager.watch(ctx, access, state)
	result, operationErr := engine.ExecuteWorkspace(runCtx, state.handle, request.Command)
	if operationErr == nil {
		operationErr = runCtx.Err()
	}
	if operationErr == nil && (len(result.Stdout) > 1<<20 || len(result.Stderr) > 1<<20 || result.ExitCode < 0 || result.ExitCode > 255) {
		operationErr = domainError("NATIVE_WORKSPACE_COMMAND_RESULT_INVALID", "Command returned an invalid or oversized result.")
	}
	stop()
	if operationErr == nil {
		state.command.Stdout, state.command.Stderr, state.command.ExitCode = result.Stdout, result.Stderr, result.ExitCode
		// nil and empty bytes have the same binary meaning and must hash consistently after SQL reload.
		if state.command.Stdout == nil {
			state.command.Stdout = []byte{}
		}
		if state.command.Stderr == nil {
			state.command.Stderr = []byte{}
		}
		state.command.ResultHash = nativeCommandResultHash(state.command.Stdout, state.command.Stderr, state.command.ExitCode)
	}
	if err := manager.finish(ctx, access, state, operationErr); err != nil {
		return NativeWorkspaceCommandReceipt{}, err
	}
	return state.command.NativeWorkspaceCommandReceipt, nil
}

func finishNativeCommandTx(ctx context.Context, tx *sql.Tx, state nativeEnvironment, operationErr error, now string) error {
	if state.command == nil {
		return nil
	}
	r := state.command
	status, code := "completed", ""
	stdout, stderr := r.Stdout, r.Stderr
	if operationErr != nil {
		status, code = "failed", "NATIVE_WORKSPACE_COMMAND_UNCONFIRMED"
		stdout, stderr = []byte{}, []byte{}
		r.ResultHash = ""
	}
	result, err := tx.ExecContext(ctx, `UPDATE native_workspace_commands SET status=?,stdout=?,stderr=?,exit_code=?,result_hash=?,reserved_bytes=0,error_code=?,updated_at=? WHERE agent_tool_call_id=? AND session_id=? AND environment_id=? AND operation_id=? AND status='running'`, status, stdout, stderr, r.ExitCode, r.ResultHash, code, now, r.AgentToolCallID, r.SessionID, r.environmentID, r.operationID)
	if err != nil {
		return err
	}
	return requireOneRow(result, "NATIVE_WORKSPACE_OPERATION_SUPERSEDED", "Command receipt no longer matches its original environment operation.")
}

func verifyNativeCommandReceiptTx(ctx context.Context, tx *sql.Tx, call AgentToolCall) error {
	r, err := scanNativeCommand(tx.QueryRowContext(ctx, nativeCommandSelect+` WHERE agent_tool_call_id=?`, call.AgentToolCallID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!r.confirmed() || r.argumentsHash != call.ArgumentsHash || r.sdkID != call.SDKToolCallID) {
		return domainError("NATIVE_WORKSPACE_COMMAND_UNCONFIRMED", "A command without its confirmed durable result cannot complete the SDK tool.")
	}
	return err
}
