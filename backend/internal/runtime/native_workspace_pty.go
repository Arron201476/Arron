package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"content-agent/backend/internal/scriptsandbox"
)

const nativeWorkspaceStdinTool = "runtime:write_stdin"
const nativePTYReceiptBudget = 2 << 20

var nativePTYProcessIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

const nativeWorkspacePTYSchema = `
CREATE TABLE IF NOT EXISTS native_workspace_pty_processes (
	session_id TEXT NOT NULL REFERENCES native_workspace_leases(session_id),
	pty_session_id INTEGER NOT NULL,
	process_id TEXT NOT NULL,
	environment_id TEXT NOT NULL,
	start_call_id TEXT NOT NULL UNIQUE REFERENCES agent_tool_calls(agent_tool_call_id),
	tty INTEGER NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('starting','running','exited','lost','deleted')),
	sequence INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(session_id,pty_session_id),
	UNIQUE(session_id,process_id)
);
CREATE TABLE IF NOT EXISTS native_workspace_pty_operations (
	agent_tool_call_id TEXT PRIMARY KEY REFERENCES agent_tool_calls(agent_tool_call_id),
	session_id TEXT NOT NULL,
	pty_session_id INTEGER NOT NULL,
	environment_id TEXT NOT NULL,
	operation_id TEXT NOT NULL,
	operation_kind TEXT NOT NULL CHECK(operation_kind IN ('start','input')),
	sdk_tool_call_id TEXT NOT NULL,
	arguments_hash TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	sequence INTEGER NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('running','completed','failed','deleted')),
	result_json TEXT NOT NULL DEFAULT '',
	result_hash TEXT NOT NULL DEFAULT '',
	reserved_bytes INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	FOREIGN KEY(session_id,pty_session_id) REFERENCES native_workspace_pty_processes(session_id,pty_session_id)
);
CREATE INDEX IF NOT EXISTS native_workspace_pty_operations_session ON native_workspace_pty_operations(session_id);
`

type NativeWorkspacePTYInput struct {
	SessionID        int    `json:"pty_session_id"`
	ExpectedSequence int    `json:"expected_sequence"`
	Chars            string `json:"chars"`
	YieldMillis      int    `json:"yield_ms"`
}

type NativeWorkspacePTYRequest struct {
	AgentToolCallID string                           `json:"agent_tool_call_id"`
	SDKToolCallID   string                           `json:"sdk_tool_call_id"`
	Arguments       json.RawMessage                  `json:"arguments"`
	Start           *scriptsandbox.WorkspacePTYStart `json:"start,omitempty"`
	Input           *NativeWorkspacePTYInput         `json:"input,omitempty"`
}

type NativeWorkspacePTYReceipt struct {
	SessionID       string                           `json:"session_id"`
	AgentToolCallID string                           `json:"agent_tool_call_id"`
	PTYSessionID    int                              `json:"pty_session_id"`
	Sequence        int                              `json:"sequence"`
	RequestHash     string                           `json:"request_hash"`
	ResultHash      string                           `json:"result_hash"`
	Result          scriptsandbox.WorkspacePTYResult `json:"result"`
}

type nativePTYRecord struct {
	NativeWorkspacePTYReceipt
	environmentID, operationID, kind, sdkID, argumentsHash, status, resultJSON string
	reservedBytes                                                              int64
	processID                                                                  string
}

type nativePTYProcess struct {
	sessionID, processID, environmentID, startCallID, status string
	ptyID, sequence                                          int
	tty                                                      bool
}

const nativePTYSelect = `SELECT session_id,agent_tool_call_id,pty_session_id,sequence,request_hash,result_hash,environment_id,operation_id,operation_kind,sdk_tool_call_id,arguments_hash,status,result_json,reserved_bytes FROM native_workspace_pty_operations`
const nativePTYProcessSelect = `SELECT session_id,pty_session_id,process_id,environment_id,start_call_id,tty,status,sequence FROM native_workspace_pty_processes`

func scanNativePTY(row interface{ Scan(...any) error }) (nativePTYRecord, error) {
	var r nativePTYRecord
	err := row.Scan(&r.SessionID, &r.AgentToolCallID, &r.PTYSessionID, &r.Sequence, &r.RequestHash, &r.ResultHash,
		&r.environmentID, &r.operationID, &r.kind, &r.sdkID, &r.argumentsHash, &r.status, &r.resultJSON, &r.reservedBytes)
	return r, err
}

func scanNativePTYProcess(row interface{ Scan(...any) error }) (nativePTYProcess, error) {
	var p nativePTYProcess
	err := row.Scan(&p.sessionID, &p.ptyID, &p.processID, &p.environmentID, &p.startCallID, &p.tty, &p.status, &p.sequence)
	if err == nil && (p.ptyID < 1000 || p.ptyID > 1127 || p.sequence < 0 || p.sequence > 512 || !nativePTYProcessIDPattern.MatchString(p.processID)) {
		return p, domainError("NATIVE_WORKSPACE_PTY_UNCONFIRMED", "Stored terminal identity is invalid.")
	}
	return p, err
}

func validateNativePTYResult(result scriptsandbox.WorkspacePTYResult, processID string, inputBytes int) bool {
	return result.ProcessID == processID && result.Output != nil && len(result.Output) <= 1<<20 &&
		result.InputBytes >= 0 && result.InputBytes <= inputBytes &&
		(result.ExitCode == nil && result.Reason == "running" && result.InputBytes == inputBytes ||
			result.ExitCode != nil && *result.ExitCode >= 0 && *result.ExitCode <= 255 &&
				slices.Contains([]string{"exited", "terminated", "timeout", "output_limit"}, result.Reason))
}

func (r *nativePTYRecord) confirmed(process nativePTYProcess) bool {
	if r.status != "completed" || r.reservedBytes != 0 || len(r.resultJSON) == 0 || len(r.resultJSON) > nativePTYReceiptBudget ||
		!nativeLeaseKeyPattern.MatchString(r.RequestHash) || sha256Hex([]byte(r.resultJSON)) != r.ResultHash ||
		r.SessionID != process.sessionID || r.PTYSessionID != process.ptyID || r.environmentID != process.environmentID ||
		r.Sequence < 1 || r.Sequence > process.sequence || json.Unmarshal([]byte(r.resultJSON), &r.Result) != nil {
		return false
	}
	// A receipt is historical, not a promise that its process is still alive.
	return r.Result.InputBytes <= 64<<10 && (r.kind != "start" || r.Result.InputBytes == 0) && validateNativePTYResult(r.Result, process.processID, r.Result.InputBytes)
}

func (s *Store) validateNativePTYApprovalTx(ctx context.Context, tx *sql.Tx, r nativePTYRecord, allowCompleted bool) (AgentToolCall, error) {
	toolID := nativeWorkspaceExecTool
	if r.kind == "input" {
		toolID = nativeWorkspaceStdinTool
	} else if r.kind != "start" {
		return AgentToolCall{}, domainError("NATIVE_WORKSPACE_PTY_REQUEST_INVALID", "Unknown terminal operation.")
	}
	call, err := s.validateNativeShellApprovalTx(ctx, tx, r.AgentToolCallID, r.sdkID, r.argumentsHash, toolID, allowCompleted)
	if err != nil || r.kind == "start" {
		return call, err
	}
	process, err := scanNativePTYProcess(tx.QueryRowContext(ctx, nativePTYProcessSelect+` WHERE session_id=? AND pty_session_id=?`, r.SessionID, r.PTYSessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return call, domainError("NATIVE_WORKSPACE_PTY_SESSION_LOST", "The approved input targets a terminal that no longer exists.")
	}
	if err != nil {
		return call, err
	}
	start, err := getAgentToolCallTx(ctx, tx, process.startCallID)
	if err != nil {
		return call, err
	}
	_, err = s.validateNativeShellApprovalTx(ctx, tx, start.AgentToolCallID, start.SDKToolCallID, start.ArgumentsHash, nativeWorkspaceExecTool, true)
	return call, err
}

func validateNativePTYRequest(request NativeWorkspacePTYRequest, limits scriptsandbox.Limits) (string, string, error) {
	invalid := func() (string, string, error) {
		return "", "", domainError("NATIVE_WORKSPACE_PTY_REQUEST_INVALID", "Terminal request requires exact audited arguments and one bounded operation.")
	}
	if request.AgentToolCallID == "" || len(request.AgentToolCallID) > 128 || request.SDKToolCallID == "" || len(request.SDKToolCallID) > 512 ||
		len(request.Arguments) == 0 || len(request.Arguments) > 64<<10 || (request.Start == nil) == (request.Input == nil) {
		return invalid()
	}
	var arguments map[string]json.RawMessage
	if json.Unmarshal(request.Arguments, &arguments) != nil || arguments == nil {
		return invalid()
	}
	kind := "start"
	if request.Start != nil {
		if err := scriptsandbox.ValidateWorkspacePTYStart(*request.Start, limits); err != nil {
			return "", "", err
		}
		var tty bool
		if raw, exists := arguments["tty"]; exists && (string(raw) == "null" || json.Unmarshal(raw, &tty) != nil) || tty != request.Start.TTY {
			return invalid()
		}
	} else {
		kind = "input"
		input := request.Input
		var id int
		var chars string
		if json.Unmarshal(arguments["session_id"], &id) != nil || id != input.SessionID || id < 1000 || id > 1127 ||
			input.ExpectedSequence < 1 || input.ExpectedSequence >= 512 || !utf8.ValidString(input.Chars) || len(input.Chars) > 64<<10 ||
			input.YieldMillis < 1 || input.YieldMillis > 30000 {
			return invalid()
		}
		if raw, exists := arguments["chars"]; exists && (string(raw) == "null" || json.Unmarshal(raw, &chars) != nil) || chars != input.Chars {
			return invalid()
		}
	}
	_, hash, _, err := summarizeAgentToolPayload(request.Arguments, true)
	return kind, hash, err
}

func (manager *NativeWorkspaceManager) beginPTY(ctx context.Context, access NativeWorkspaceAccess, request NativeWorkspacePTYRequest) (nativeEnvironment, *NativeWorkspacePTYReceipt, error) {
	var state nativeEnvironment
	if !manager.requirePolicy {
		return state, nil, domainError("NATIVE_WORKSPACE_POLICY_REQUIRED", "Terminals require the administrator resource policy.")
	}
	kind, argumentHash, err := validateNativePTYRequest(request, manager.limits)
	if err != nil {
		return state, nil, err
	}
	encoded, err := json.Marshal(struct {
		Start *scriptsandbox.WorkspacePTYStart `json:"start,omitempty"`
		Input *NativeWorkspacePTYInput         `json:"input,omitempty"`
	}{request.Start, request.Input})
	if err != nil {
		return state, nil, err
	}
	r := nativePTYRecord{NativeWorkspacePTYReceipt: NativeWorkspacePTYReceipt{SessionID: access.SessionID, AgentToolCallID: request.AgentToolCallID, RequestHash: sha256Hex(encoded)},
		kind: kind, sdkID: request.SDKToolCallID, argumentsHash: argumentHash}
	if request.Input != nil {
		r.PTYSessionID = request.Input.SessionID
	}
	tx, err := manager.store.db.BeginTx(ctx, nil)
	if err != nil {
		return state, nil, err
	}
	defer tx.Rollback()
	lease, err := manager.store.nativeWorkspaceAccessTx(ctx, tx, access)
	if err != nil {
		return state, nil, err
	}
	if err := manager.validatePolicyTx(ctx, tx, lease.workspaceID); err != nil {
		return state, nil, err
	}
	if err := nativeManifestPendingTx(ctx, tx, access.SessionID); err != nil {
		return state, nil, err
	}
	call, err := manager.store.validateNativePTYApprovalTx(ctx, tx, r, true)
	if err != nil {
		return state, nil, err
	}
	if call.WorkspaceID != lease.workspaceID || call.ProjectID != lease.projectID {
		return state, nil, domainError("NATIVE_WORKSPACE_SCOPE_MISMATCH", "Terminal and execution workspace have different owners.")
	}
	var oneShot bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_workspace_commands WHERE agent_tool_call_id=?)`, r.AgentToolCallID).Scan(&oneShot); err != nil {
		return state, nil, err
	}
	if oneShot {
		return state, nil, domainError("NATIVE_WORKSPACE_REQUEST_CONFLICT", "A one-shot call cannot launch an additional interactive command.")
	}
	previous, err := scanNativePTY(tx.QueryRowContext(ctx, nativePTYSelect+` WHERE agent_tool_call_id=?`, r.AgentToolCallID))
	if err == nil {
		if previous.SessionID != r.SessionID || previous.RequestHash != r.RequestHash || previous.argumentsHash != r.argumentsHash || previous.sdkID != r.sdkID || previous.kind != r.kind {
			return state, nil, domainError("NATIVE_WORKSPACE_REQUEST_CONFLICT", "A terminal request cannot replace its original operation.")
		}
		process, err := scanNativePTYProcess(tx.QueryRowContext(ctx, nativePTYProcessSelect+` WHERE session_id=? AND pty_session_id=?`, r.SessionID, previous.PTYSessionID))
		if err != nil || !previous.confirmed(process) {
			return state, nil, domainError("NATIVE_WORKSPACE_PTY_UNCONFIRMED", "Terminal operation is running, failed or has an invalid receipt; it was not replayed.")
		}
		return state, &previous.NativeWorkspacePTYReceipt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return state, nil, err
	}
	if call.Status != "running" {
		return state, nil, domainError("NATIVE_WORKSPACE_PTY_UNCONFIRMED", "A completed tool cannot create another terminal operation.")
	}
	if _, err := manager.store.nativeWorkspaceOwnerTx(ctx, tx, access.DispatchGeneration, false); err != nil {
		return state, nil, err
	}
	var count int
	var used int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(result_json AS BLOB))+reserved_bytes),0) FROM native_workspace_pty_operations WHERE session_id=?`, access.SessionID).Scan(&count, &used); err != nil {
		return state, nil, err
	}
	if count >= 512 || used+nativePTYReceiptBudget > 128<<20 {
		return state, nil, domainError("NATIVE_WORKSPACE_QUOTA_EXCEEDED", "Terminal operation history quota reached.")
	}
	if err := manager.store.enforceWorkspaceQuotaTx(ctx, tx, lease.workspaceID, "storage_bytes", nativePTYReceiptBudget); err != nil {
		return state, nil, err
	}
	state, err = scanNativeEnvironment(tx.QueryRowContext(ctx, nativeEnvironmentSelect+` WHERE session_id=?`, access.SessionID))
	if err != nil {
		return state, nil, err
	}
	state, err = manager.claimBindingTx(ctx, tx, state, manager.store.newID("nwo"), "pty")
	if err != nil {
		return state, nil, err
	}
	now := formatTime(manager.store.now())
	if kind == "start" {
		var number int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_workspace_pty_processes WHERE session_id=?`, access.SessionID).Scan(&number); err != nil {
			return state, nil, err
		}
		if number >= 128 {
			return state, nil, domainError("NATIVE_WORKSPACE_QUOTA_EXCEEDED", "Terminal process history quota reached.")
		}
		id, err := newNativeWorkspaceUUID()
		if err != nil {
			return state, nil, err
		}
		r.PTYSessionID, r.processID, r.Sequence = 1000+number, strings.ReplaceAll(id, "-", ""), 1
		_, err = tx.ExecContext(ctx, `INSERT INTO native_workspace_pty_processes(session_id,pty_session_id,process_id,environment_id,start_call_id,tty,status,sequence,created_at,updated_at) VALUES(?,?,?,?,?,?,'starting',0,?,?)`,
			access.SessionID, r.PTYSessionID, r.processID, state.environmentID, r.AgentToolCallID, request.Start.TTY, now, now)
		if err != nil {
			return state, nil, err
		}
	} else {
		process, err := scanNativePTYProcess(tx.QueryRowContext(ctx, nativePTYProcessSelect+` WHERE session_id=? AND pty_session_id=?`, access.SessionID, r.PTYSessionID))
		if err != nil {
			return state, nil, err
		}
		if process.status != "running" || process.environmentID != state.environmentID || process.sequence != request.Input.ExpectedSequence {
			return state, nil, domainError("NATIVE_WORKSPACE_PTY_SESSION_LOST", "The original terminal is no longer current at the expected sequence; no input was sent.")
		}
		if request.Input.Chars != "" && !process.tty {
			return state, nil, domainError("NATIVE_WORKSPACE_PTY_STDIN_UNAVAILABLE", "This process was not started with interactive stdin.")
		}
		r.Sequence, r.processID = process.sequence+1, process.processID
	}
	r.environmentID, r.operationID, r.status, r.reservedBytes = state.environmentID, state.operationID, "running", nativePTYReceiptBudget
	_, err = tx.ExecContext(ctx, `INSERT INTO native_workspace_pty_operations(agent_tool_call_id,session_id,pty_session_id,environment_id,operation_id,operation_kind,sdk_tool_call_id,arguments_hash,request_hash,sequence,status,reserved_bytes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,'running',?,?,?)`,
		r.AgentToolCallID, r.SessionID, r.PTYSessionID, r.environmentID, r.operationID, r.kind, r.sdkID, r.argumentsHash, r.RequestHash, r.Sequence, r.reservedBytes, now, now)
	if err != nil {
		return state, nil, err
	}
	state.pty = &r
	return state, nil, tx.Commit()
}

type nativeWorkspacePTYEngine interface {
	StartWorkspacePTY(context.Context, scriptsandbox.WorkspaceHandle, string, scriptsandbox.WorkspacePTYStart) (scriptsandbox.WorkspacePTYResult, error)
	WriteWorkspacePTY(context.Context, scriptsandbox.WorkspaceHandle, scriptsandbox.WorkspacePTYInput) (scriptsandbox.WorkspacePTYResult, error)
}

var _ nativeWorkspacePTYEngine = (*scriptsandbox.OCI)(nil)

func (manager *NativeWorkspaceManager) ExecutePTY(ctx context.Context, access NativeWorkspaceAccess, request NativeWorkspacePTYRequest) (NativeWorkspacePTYReceipt, error) {
	if _, _, err := validateNativePTYRequest(request, manager.limits); err != nil {
		return NativeWorkspacePTYReceipt{}, err
	}
	// Freeze caller-owned slices/pointers before validation, hashing and execution.
	encoded, err := json.Marshal(request)
	if err != nil {
		return NativeWorkspacePTYReceipt{}, err
	}
	var frozen NativeWorkspacePTYRequest
	if err := json.Unmarshal(encoded, &frozen); err != nil {
		return NativeWorkspacePTYReceipt{}, err
	}
	request = frozen
	engine, ok := manager.engine.(nativeWorkspacePTYEngine)
	if !ok {
		return NativeWorkspacePTYReceipt{}, domainError("NATIVE_WORKSPACE_UNAVAILABLE", "The isolated engine has no terminal transport.")
	}
	state, saved, err := manager.beginPTY(ctx, access, request)
	if err != nil {
		return NativeWorkspacePTYReceipt{}, err
	}
	if saved != nil {
		return *saved, nil
	}
	runCtx, stop := manager.watch(ctx, access, state)
	var result scriptsandbox.WorkspacePTYResult
	var operationErr error
	inputBytes := 0
	if request.Start != nil {
		result, operationErr = engine.StartWorkspacePTY(runCtx, state.handle, state.pty.processID, *request.Start)
	} else {
		inputBytes = len(request.Input.Chars)
		result, operationErr = engine.WriteWorkspacePTY(runCtx, state.handle, scriptsandbox.WorkspacePTYInput{ProcessID: state.pty.processID, Input: []byte(request.Input.Chars), YieldMillis: request.Input.YieldMillis})
	}
	if operationErr == nil {
		operationErr = runCtx.Err()
	}
	if operationErr == nil && !validateNativePTYResult(result, state.pty.processID, inputBytes) {
		operationErr = domainError("NATIVE_WORKSPACE_PTY_UNCONFIRMED", "Terminal result does not confirm its exact process and bounded I/O.")
	}
	stop()
	if operationErr == nil {
		state.pty.Result = result
		body, err := json.Marshal(result)
		if err != nil || len(body) > nativePTYReceiptBudget {
			operationErr = domainError("NATIVE_WORKSPACE_PTY_UNCONFIRMED", "Terminal receipt exceeds its storage budget.")
		} else {
			state.pty.resultJSON, state.pty.ResultHash = string(body), sha256Hex(body)
		}
	}
	if err := manager.finish(ctx, access, state, operationErr); err != nil {
		return NativeWorkspacePTYReceipt{}, err
	}
	return state.pty.NativeWorkspacePTYReceipt, nil
}

func finishNativePTYTx(ctx context.Context, tx *sql.Tx, state nativeEnvironment, operationErr error, now string) error {
	if state.pty == nil {
		return nil
	}
	r := state.pty
	status, processStatus := "completed", "running"
	if r.Result.ExitCode != nil {
		processStatus = "exited"
	}
	if operationErr != nil {
		status, processStatus, r.resultJSON, r.ResultHash = "failed", "lost", "", ""
	}
	result, err := tx.ExecContext(ctx, `UPDATE native_workspace_pty_operations SET status=?,result_json=?,result_hash=?,reserved_bytes=0,updated_at=? WHERE agent_tool_call_id=? AND session_id=? AND environment_id=? AND operation_id=? AND status='running'`,
		status, r.resultJSON, r.ResultHash, now, r.AgentToolCallID, r.SessionID, r.environmentID, r.operationID)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "NATIVE_WORKSPACE_OPERATION_SUPERSEDED", "Terminal receipt lost its operation binding."); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE native_workspace_pty_processes SET status=?,sequence=?,updated_at=? WHERE session_id=? AND pty_session_id=? AND environment_id=? AND sequence=? AND status IN ('starting','running')`,
		processStatus, r.Sequence, now, r.SessionID, r.PTYSessionID, r.environmentID, r.Sequence-1)
	if err != nil {
		return err
	}
	return requireOneRow(result, "NATIVE_WORKSPACE_OPERATION_SUPERSEDED", "Terminal process advanced outside its original operation.")
}

func verifyNativeShellReceiptTx(ctx context.Context, tx *sql.Tx, call AgentToolCall) error {
	r, err := scanNativePTY(tx.QueryRowContext(ctx, nativePTYSelect+` WHERE agent_tool_call_id=?`, call.AgentToolCallID))
	if errors.Is(err, sql.ErrNoRows) && call.ToolID == nativeWorkspaceExecTool {
		return verifyNativeCommandReceiptTx(ctx, tx, call)
	}
	if err != nil {
		return domainError("NATIVE_WORKSPACE_PTY_UNCONFIRMED", "A terminal tool cannot complete without its durable receipt.")
	}
	process, err := scanNativePTYProcess(tx.QueryRowContext(ctx, nativePTYProcessSelect+` WHERE session_id=? AND pty_session_id=?`, r.SessionID, r.PTYSessionID))
	if err != nil || !r.confirmed(process) || r.sdkID != call.SDKToolCallID || r.argumentsHash != call.ArgumentsHash ||
		(r.kind == "start") != (call.ToolID == nativeWorkspaceExecTool) {
		return domainError("NATIVE_WORKSPACE_PTY_UNCONFIRMED", "Terminal tool receipt differs from its original call.")
	}
	var oneShot bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_workspace_commands WHERE agent_tool_call_id=?)`, call.AgentToolCallID).Scan(&oneShot); err != nil {
		return err
	}
	if oneShot {
		return domainError("NATIVE_WORKSPACE_REQUEST_CONFLICT", "A terminal call has conflicting one-shot execution state.")
	}
	return nil
}

func closeNativePTYRecordsTx(ctx context.Context, tx *sql.Tx, sessionID, environmentID, now string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE native_workspace_pty_processes SET status='lost',updated_at=? WHERE session_id=? AND environment_id=? AND status IN ('starting','running')`, now, sessionID, environmentID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE native_workspace_pty_operations SET status='failed',result_json='',result_hash='',reserved_bytes=0,updated_at=? WHERE session_id=? AND environment_id=? AND status='running'`, now, sessionID, environmentID)
	return err
}
