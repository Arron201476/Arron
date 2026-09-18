package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"content-agent/backend/internal/scriptsandbox"
)

type nativePTYEngineFixture struct {
	*nativeCommandEngineFixture
	starts, inputs atomic.Int32
	failure        error
	before         func()
}

func (engine *nativePTYEngineFixture) StartWorkspacePTY(ctx context.Context, handle scriptsandbox.WorkspaceHandle, id string, request scriptsandbox.WorkspacePTYStart) (scriptsandbox.WorkspacePTYResult, error) {
	engine.starts.Add(1)
	if engine.before != nil {
		engine.before()
	}
	if err := engine.ReconnectWorkspace(ctx, handle); err != nil {
		return scriptsandbox.WorkspacePTYResult{}, err
	}
	return scriptsandbox.WorkspacePTYResult{ProcessID: id, Output: []byte("ready"), Reason: "running"}, engine.failure
}

func (engine *nativePTYEngineFixture) WriteWorkspacePTY(ctx context.Context, handle scriptsandbox.WorkspaceHandle, input scriptsandbox.WorkspacePTYInput) (scriptsandbox.WorkspacePTYResult, error) {
	engine.inputs.Add(1)
	if engine.before != nil {
		engine.before()
	}
	if err := engine.ReconnectWorkspace(ctx, handle); err != nil {
		return scriptsandbox.WorkspacePTYResult{}, err
	}
	exit := 23
	return scriptsandbox.WorkspacePTYResult{ProcessID: input.ProcessID, Output: []byte{0, 255, 128}, ExitCode: &exit, Reason: "exited", InputBytes: len(input.Input)}, engine.failure
}

func nativePTYManagerForTest(t *testing.T, store *Store, engine *nativePTYEngineFixture) *NativeWorkspaceManager {
	t.Helper()
	nativeCommandPolicy(t, store)
	manager, err := NewPolicyNativeWorkspaceManager(store, engine, scriptsandbox.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func newNativePTYEngine(t *testing.T) *nativePTYEngineFixture {
	return &nativePTYEngineFixture{nativeCommandEngineFixture: &nativeCommandEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}}
}

func nativePTYCall(t *testing.T, store *Store, ctx context.Context, sdkID string, input *NativeWorkspacePTYInput, approve bool) NativeWorkspacePTYRequest {
	t.Helper()
	activity, _ := AgentActivityFromContext(ctx)
	project, err := store.GetProject(ctx, activity.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	toolID, raw := nativeWorkspaceExecTool, json.RawMessage(`{"cmd":"read input","tty":true}`)
	request := NativeWorkspacePTYRequest{SDKToolCallID: sdkID, Input: input}
	if input == nil {
		request.Start = &scriptsandbox.WorkspacePTYStart{Command: scriptsandbox.WorkspaceCommand{Argv: []string{"python", "-c", "input()"}, Cwd: "/workspace"}, TTY: true, YieldMillis: 1000}
	} else {
		toolID = nativeWorkspaceStdinTool
		raw, err = json.Marshal(map[string]any{"session_id": input.SessionID, "chars": input.Chars})
		if err != nil {
			t.Fatal(err)
		}
	}
	request.Arguments = raw
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: activity.ProjectID, ConversationID: project.PrimaryConversationID,
		AgentTurnID: activity.AgentTurnID, AgentTaskAttemptID: activity.AgentTaskAttemptID, ExecutionAttemptID: activity.ExecutionAttemptID,
		AttemptToken: activity.AttemptToken, SDKToolCallID: sdkID, ToolID: toolID, Arguments: raw})
	if err != nil {
		t.Fatal(err)
	}
	request.AgentToolCallID = call.AgentToolCallID
	if approve {
		if _, err := store.ResolveAgentToolApproval(mcpOwnerContext(), ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID,
			ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: sdkID}); err != nil {
			t.Fatal(err)
		}
	}
	return request
}

func TestNativePTYThreeModesDurableApprovalInputAndHistoricalReceipt(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, database, ctx, access := nativeWorkspaceFixture(t, mode)
			engine := newNativePTYEngine(t)
			manager := nativePTYManagerForTest(t, store, engine)
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			request := nativePTYCall(t, store, ctx, "terminal-start", nil, true)
			engine.before = func() {
				var status string
				if err := store.db.QueryRow(`SELECT status FROM native_workspace_pty_operations WHERE agent_tool_call_id=?`, request.AgentToolCallID).Scan(&status); err != nil || status != "running" {
					t.Fatalf("engine ran before durable intent: %s, %v", status, err)
				}
			}
			started, err := manager.ExecutePTY(ctx, access, request)
			if err != nil || started.Sequence != 1 || started.Result.ExitCode != nil || started.PTYSessionID != 1000 {
				t.Fatalf("start = %+v, %v", started, err)
			}
			engine.before = nil
			if _, err := store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: request.AgentToolCallID, Result: json.RawMessage(`"running terminal"`)}); err != nil {
				t.Fatal(err)
			}
			input := nativePTYCall(t, store, ctx, "terminal-input", &NativeWorkspacePTYInput{SessionID: started.PTYSessionID, ExpectedSequence: 1, Chars: "hello\n", YieldMillis: 1000}, true)
			_, err = store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: input.AgentToolCallID, Result: json.RawMessage(`"invented output"`)})
			assertDomainCode(t, err, "NATIVE_WORKSPACE_PTY_UNCONFIRMED")
			finished, err := manager.ExecutePTY(ctx, access, input)
			if err != nil || finished.Result.ExitCode == nil || *finished.Result.ExitCode != 23 || finished.Sequence != 2 ||
				finished.Result.InputBytes != 6 || !bytes.Equal(finished.Result.Output, []byte{0, 255, 128}) {
				t.Fatalf("input = %+v, %v", finished, err)
			}
			registry := store.registry
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(database, registry)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			manager = nativePTYManagerForTest(t, reopened, engine)
			for _, original := range []NativeWorkspacePTYRequest{request, input} {
				if _, err := manager.ExecutePTY(ctx, access, original); err != nil {
					t.Fatal(err)
				}
			}
			if engine.starts.Load() != 1 || engine.inputs.Load() != 1 {
				t.Fatal("receipt recovery repeated terminal I/O")
			}
			if _, err := reopened.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: input.AgentToolCallID, Result: json.RawMessage(`"confirmed output"`)}); err != nil {
				t.Fatal(err)
			}
			if _, err := reopened.db.Exec(`UPDATE native_workspace_pty_operations SET result_hash='' WHERE agent_tool_call_id=?`, input.AgentToolCallID); err != nil {
				t.Fatal(err)
			}
			_, err = manager.ExecutePTY(ctx, access, input)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_PTY_UNCONFIRMED")
		})
	}
}

func TestNativePTYRejectsUnapprovedChangedAndStaleInputBeforeEngine(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativePTYEngine(t)
	manager := nativePTYManagerForTest(t, store, engine)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	pending := nativePTYCall(t, store, ctx, "unapproved-terminal", nil, false)
	_, err := manager.ExecutePTY(ctx, access, pending)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_COMMAND_APPROVAL_REQUIRED")
	request := nativePTYCall(t, store, ctx, "approved-terminal", nil, true)
	started, err := manager.ExecutePTY(ctx, access, request)
	if err != nil {
		t.Fatal(err)
	}
	input := nativePTYCall(t, store, ctx, "approved-input", &NativeWorkspacePTYInput{SessionID: started.PTYSessionID, ExpectedSequence: 1, Chars: "yes", YieldMillis: 1000}, true)
	input.Input.Chars = "different"
	_, err = manager.ExecutePTY(ctx, access, input)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_PTY_REQUEST_INVALID")
	input.Input.Chars, input.Input.ExpectedSequence = "yes", 2
	_, err = manager.ExecutePTY(ctx, access, input)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_PTY_SESSION_LOST")
	input.Input.ExpectedSequence = 1
	_, err = manager.ExecutePTY(mcpOwnerContext(), access, input)
	assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
	if engine.starts.Load() != 1 || engine.inputs.Load() != 0 {
		t.Fatal("invalid input performed terminal I/O")
	}
	_, err = manager.ExecuteCommand(ctx, access, NativeWorkspaceCommandRequest{AgentToolCallID: request.AgentToolCallID, SDKToolCallID: request.SDKToolCallID,
		Arguments: request.Arguments, Command: request.Start.Command})
	assertDomainCode(t, err, "NATIVE_WORKSPACE_REQUEST_CONFLICT")
	if engine.commands.Load() != 0 {
		t.Fatal("terminal fell back to a second one-shot execution")
	}
}

func TestNativePTYUnknownStartIsNotReplayedAndDeleteErasesOutputs(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativePTYEngine(t)
	manager := nativePTYManagerForTest(t, store, engine)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	request := nativePTYCall(t, store, ctx, "lost-terminal", nil, true)
	engine.failure = errors.New("engine receipt lost")
	if _, err := manager.ExecutePTY(ctx, access, request); err == nil {
		t.Fatal("unknown start was accepted")
	}
	_, err := manager.ExecutePTY(ctx, access, request)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_PTY_UNCONFIRMED")
	if engine.starts.Load() != 1 {
		t.Fatal("unknown start replayed")
	}
	activity, _ := AgentActivityFromContext(ctx)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := closeNativeProjectWorkspacesTx(ctx, tx, activity.ProjectID, store.now()); err != nil {
		t.Fatal(err)
	}
	var status string
	var used int
	if err := tx.QueryRow(`SELECT status,length(result_json)+length(result_hash)+reserved_bytes FROM native_workspace_pty_operations WHERE agent_tool_call_id=?`, request.AgentToolCallID).Scan(&status, &used); err != nil || status != "deleted" || used != 0 {
		t.Fatalf("deleted terminal data retained: %s %d, %v", status, used, err)
	}
}

func TestNativePTYUnknownInputCannotBeSentAgain(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativePTYEngine(t)
	manager := nativePTYManagerForTest(t, store, engine)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	start := nativePTYCall(t, store, ctx, "input-loss-start", nil, true)
	started, err := manager.ExecutePTY(ctx, access, start)
	if err != nil {
		t.Fatal(err)
	}
	input := nativePTYCall(t, store, ctx, "input-loss", &NativeWorkspacePTYInput{SessionID: started.PTYSessionID, ExpectedSequence: 1, Chars: "do once\n", YieldMillis: 1000}, true)
	engine.failure = errors.New("input may already have reached child")
	if _, err := manager.ExecutePTY(ctx, access, input); err == nil {
		t.Fatal("unknown input reported success")
	}
	_, err = manager.ExecutePTY(ctx, access, input)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_PTY_UNCONFIRMED")
	if engine.inputs.Load() != 1 {
		t.Fatal("unknown input was sent again")
	}
	process, err := scanNativePTYProcess(store.db.QueryRow(nativePTYProcessSelect+` WHERE session_id=? AND pty_session_id=?`, access.SessionID, started.PTYSessionID))
	if err != nil || process.status != "lost" {
		t.Fatalf("unknown process = %+v, %v", process, err)
	}
}

func TestNativePTYRejectsInvalidUTF8BeforeJSONCanReplaceIt(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativePTYEngine(t)
	manager := nativePTYManagerForTest(t, store, engine)
	request := nativePTYCall(t, store, ctx, "invalid-terminal", nil, true)
	request.Start.Command.Argv = []string{"python", string([]byte{255})}
	_, err := manager.ExecutePTY(ctx, access, request)
	var sandboxError *scriptsandbox.Error
	if !errors.As(err, &sandboxError) || sandboxError.Code != "WORKSPACE_PTY_REQUEST_INVALID" {
		t.Fatalf("invalid UTF-8 was normalized before validation: %v", err)
	}
	if engine.starts.Load() != 0 {
		t.Fatal("invalid UTF-8 reached the engine")
	}
}

func TestNativePTYMissingInputTargetReturnsSessionLost(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativePTYEngine(t)
	manager := nativePTYManagerForTest(t, store, engine)
	input := nativePTYCall(t, store, ctx, "missing-terminal", &NativeWorkspacePTYInput{SessionID: 1000, ExpectedSequence: 1, YieldMillis: 1000}, true)
	_, err := manager.ExecutePTY(ctx, access, input)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_PTY_SESSION_LOST")
	if engine.inputs.Load() != 0 {
		t.Fatal("missing terminal was implicitly started")
	}
}

func TestNativePTYCannotReplayAnExistingOneShotCall(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativePTYEngine(t)
	manager := nativePTYManagerForTest(t, store, engine)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	command, _ := nativeCommandCall(t, store, ctx, "one-shot-only", true)
	if _, err := manager.ExecuteCommand(ctx, access, command); err != nil {
		t.Fatal(err)
	}
	_, err := manager.ExecutePTY(ctx, access, NativeWorkspacePTYRequest{AgentToolCallID: command.AgentToolCallID, SDKToolCallID: command.SDKToolCallID,
		Arguments: command.Arguments, Start: &scriptsandbox.WorkspacePTYStart{Command: command.Command, YieldMillis: 1000}})
	assertDomainCode(t, err, "NATIVE_WORKSPACE_REQUEST_CONFLICT")
	if engine.starts.Load() != 0 || engine.commands.Load() != 1 {
		t.Fatal("one tool call executed through two transports")
	}
}

func TestNativePTYRechecksRevocationAndReservesBeforeSideEffects(t *testing.T) {
	for _, fault := range []string{"quota", "cancel-call", "policy", "delete"} {
		t.Run(fault, func(t *testing.T) {
			store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
			engine := newNativePTYEngine(t)
			manager := nativePTYManagerForTest(t, store, engine)
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			request := nativePTYCall(t, store, ctx, "revoked-start", nil, true)
			if fault == "quota" {
				if _, err := store.db.Exec(`UPDATE workspace_quotas SET max_storage_bytes=1`); err != nil {
					t.Fatal(err)
				}
			} else {
				engine.before = func() {
					var reserved int
					if err := store.db.QueryRow(`SELECT reserved_bytes FROM native_workspace_pty_operations WHERE agent_tool_call_id=?`, request.AgentToolCallID).Scan(&reserved); err != nil || reserved != nativePTYReceiptBudget {
						t.Fatalf("I/O preceded reservation: %d, %v", reserved, err)
					}
					switch fault {
					case "cancel-call":
						if _, err := store.CancelAgentToolCall(ctx, CancelAgentToolCallCommand{AgentToolCallID: request.AgentToolCallID, Reason: "test"}); err != nil {
							t.Fatal(err)
						}
					case "policy":
						policy, err := store.GetScriptSandboxPolicy(mcpOwnerContext())
						if err != nil {
							t.Fatal(err)
						}
						if _, err := store.UpdateScriptSandboxPolicy(mcpOwnerContext(), UpdateScriptSandboxPolicyCommand{ExpectedVersion: policy.Version, Enabled: false}); err != nil {
							t.Fatal(err)
						}
					case "delete":
						activity, _ := AgentActivityFromContext(ctx)
						tx, err := store.db.BeginTx(ctx, nil)
						if err != nil {
							t.Fatal(err)
						}
						defer tx.Rollback()
						if err := closeNativeProjectWorkspacesTx(ctx, tx, activity.ProjectID, store.now()); err != nil {
							t.Fatal(err)
						}
						if err := tx.Commit(); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if _, err := manager.ExecutePTY(ctx, access, request); err == nil {
				t.Fatal("revoked or unreserved execution returned success")
			}
			if fault == "quota" {
				if engine.starts.Load() != 0 {
					t.Fatal("quota was checked after launch")
				}
				return
			}
			var status string
			var stored int
			if err := store.db.QueryRow(`SELECT status,length(result_json)+length(result_hash)+reserved_bytes FROM native_workspace_pty_operations WHERE agent_tool_call_id=?`, request.AgentToolCallID).Scan(&status, &stored); err != nil || status == "completed" || stored != 0 {
				t.Fatalf("late output was published: %s %d, %v", status, stored, err)
			}
		})
	}
}

func TestNativePTYShutdownKeepsHistoricalReceiptButMarksLiveProcessLost(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativePTYEngine(t)
	manager := nativePTYManagerForTest(t, store, engine)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	request := nativePTYCall(t, store, ctx, "shutdown-start", nil, true)
	started, err := manager.ExecutePTY(ctx, access, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Shutdown(ctx, access); err != nil {
		t.Fatal(err)
	}
	process, err := scanNativePTYProcess(store.db.QueryRow(nativePTYProcessSelect+` WHERE session_id=? AND pty_session_id=?`, access.SessionID, started.PTYSessionID))
	if err != nil || process.status != "lost" {
		t.Fatalf("closed process = %+v, %v", process, err)
	}
	repeated, err := manager.ExecutePTY(ctx, access, request)
	if err != nil || repeated.ResultHash != started.ResultHash || engine.starts.Load() != 1 {
		t.Fatalf("historical receipt was replaced or replayed: %+v, %v", repeated, err)
	}
}

func TestNativePTYV62MigrationBacksUpAndPreservesWorkspace(t *testing.T) {
	store, database, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativePTYEngine(t)
	manager := nativePTYManagerForTest(t, store, engine)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	before := nativeEnvironmentForTest(t, store, access)
	if _, err := store.db.Exec(`DROP TABLE native_workspace_pty_operations; DROP TABLE native_workspace_pty_processes; PRAGMA user_version=62`); err != nil {
		t.Fatal(err)
	}
	registry := store.registry
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(database, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var backup string
	if err := reopened.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=62 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	after := nativeEnvironmentForTest(t, reopened, access)
	if before.environmentID != after.environmentID || before.handle != after.handle {
		t.Fatal("PTY schema migration replaced existing workspace")
	}
	var count int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM native_workspace_pty_processes`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration invented a running process")
	}
	if _, err := reopened.ValidateNativeWorkspaceLease(ctx, access); err != nil {
		t.Fatal(err)
	}
}
