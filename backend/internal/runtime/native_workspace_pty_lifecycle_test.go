package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"content-agent/backend/internal/scriptsandbox"
)

type nativePTYLifecycleEngine struct {
	*nativePTYEngineFixture
	requests     []scriptsandbox.WorkspacePTYInput
	stillRunning bool
}

func (engine *nativePTYLifecycleEngine) WriteWorkspacePTY(ctx context.Context, handle scriptsandbox.WorkspaceHandle, input scriptsandbox.WorkspacePTYInput) (scriptsandbox.WorkspacePTYResult, error) {
	engine.requests = append(engine.requests, input)
	result, err := engine.nativePTYEngineFixture.WriteWorkspacePTY(ctx, handle, input)
	if engine.stillRunning {
		result.ExitCode, result.Reason = nil, "running"
	}
	return result, err
}

func TestNativePTYLifecycleThreeModesStopsBeforeExportAndPreservesHistoricalReceipt(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, _, ctx, access := nativeWorkspaceFixture(t, mode)
			engine := &nativePTYLifecycleEngine{nativePTYEngineFixture: newNativePTYEngine(t)}
			nativeCommandPolicy(t, store)
			manager, err := NewPolicyNativeWorkspaceManager(store, engine, scriptsandbox.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			request := nativePTYCall(t, store, ctx, "lifecycle-start", nil, true)
			started, err := manager.ExecutePTY(ctx, access, request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: request.AgentToolCallID, Result: json.RawMessage(`"running"`)}); err != nil {
				t.Fatal(err)
			}
			engine.before = func() {
				var status string
				if err := store.db.QueryRow(`SELECT status FROM native_workspace_pty_processes WHERE session_id=? AND pty_session_id=?`, access.SessionID, started.PTYSessionID).Scan(&status); err != nil || status != "lost" {
					t.Fatalf("termination ran before durable uncertainty guard: %s, %v", status, err)
				}
			}
			stopped, err := manager.TerminatePTYs(ctx, access)
			if err != nil || stopped.State != "stopped" || stopped.SessionID != access.SessionID {
				t.Fatalf("stop = %+v, %v", stopped, err)
			}
			engine.before = nil
			if len(engine.requests) != 1 || !engine.requests[0].Terminate || engine.requests[0].ProcessID != started.Result.ProcessID || len(engine.requests[0].Input) != 0 {
				t.Fatalf("cleanup did not bind its exact process: %+v", engine.requests)
			}
			if _, err := manager.TerminatePTYs(ctx, access); err != nil {
				t.Fatal(err)
			}
			if len(engine.requests) != 1 {
				t.Fatal("confirmed terminal was signaled twice")
			}
			if engine.deletes != 0 || nativeEnvironmentForTest(t, store, access).State != "ready" {
				t.Fatal("termination destroyed the working tree")
			}
			if _, err := manager.Export(ctx, access); err != nil {
				t.Fatal(err)
			}
			again, err := manager.ExecutePTY(ctx, access, request)
			if err != nil || again.ResultHash != started.ResultHash || engine.starts.Load() != 1 {
				t.Fatalf("historical receipt replayed startup: %+v, %v", again, err)
			}
			input := nativePTYCall(t, store, ctx, "after-cleanup", &NativeWorkspacePTYInput{SessionID: started.PTYSessionID, ExpectedSequence: 1, YieldMillis: 1000}, true)
			_, err = manager.ExecutePTY(ctx, access, input)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_PTY_SESSION_LOST")
			if len(engine.requests) != 1 {
				t.Fatal("cleanup allowed a new input to the exited process")
			}
		})
	}
}

func TestNativePTYLifecycleUnknownTerminationCannotRetryOrExport(t *testing.T) {
	for _, fault := range []string{"transport", "running", "expired-lease", "pending-operation"} {
		t.Run(fault, func(t *testing.T) {
			store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
			engine := &nativePTYLifecycleEngine{nativePTYEngineFixture: newNativePTYEngine(t)}
			nativeCommandPolicy(t, store)
			manager, err := NewPolicyNativeWorkspaceManager(store, engine, scriptsandbox.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			request := nativePTYCall(t, store, ctx, "cleanup-unknown", nil, true)
			if _, err := manager.ExecutePTY(ctx, access, request); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "transport":
				engine.failure = errors.New("unknown signal result")
			case "running":
				engine.stillRunning = true
			case "expired-lease":
				engine.before = func() {
					if _, err := store.db.Exec(`UPDATE native_workspace_leases SET lease_until='2000-01-01T00:00:00Z' WHERE session_id=?`, access.SessionID); err != nil {
						t.Fatal(err)
					}
				}
			case "pending-operation":
				if _, err := store.db.Exec(`UPDATE native_workspace_environments SET operation_id='pending-input',operation_kind='pty_input' WHERE session_id=?`, access.SessionID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := manager.TerminatePTYs(ctx, access); err == nil {
				t.Fatal("unconfirmed terminal was accepted as stopped")
			}
			engine.failure, engine.stillRunning, engine.before = nil, false, nil
			if _, err := manager.TerminatePTYs(ctx, access); err == nil {
				t.Fatal("unknown lifecycle operation silently retried")
			}
			if _, err := manager.Export(ctx, access); err == nil {
				t.Fatal("uncertain terminal state was exported as saved")
			}
			expected := 1
			if fault == "pending-operation" {
				expected = 0
			}
			if len(engine.requests) != expected || engine.deletes != 0 {
				t.Fatal("failed cleanup repeated effects or destroyed files")
			}
		})
	}
}

func TestNativePTYLifecycleAbsentOrClosedDoesNotCreateAnEnvironment(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativePTYEngine(t)
	manager := nativePTYManagerForTest(t, store, engine)
	if result, err := manager.TerminatePTYs(ctx, access); err != nil || result.State != "stopped" {
		t.Fatalf("absent cleanup = %+v, %v", result, err)
	}
	if engine.creates != 0 || engine.inputs.Load() != 0 {
		t.Fatal("absent cleanup created a process or environment")
	}
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Shutdown(ctx, access); err != nil {
		t.Fatal(err)
	}
	if result, err := manager.TerminatePTYs(ctx, access); err != nil || result.State != "stopped" {
		t.Fatalf("closed cleanup = %+v, %v", result, err)
	}
	if engine.creates != 1 || engine.inputs.Load() != 0 || engine.deletes != 1 {
		t.Fatal("closed cleanup recreated resources")
	}
}
