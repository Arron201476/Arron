package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"content-agent/backend/internal/scriptsandbox"
)

type nativeCommandEngineFixture struct {
	*nativeEngineFixture
	commands atomic.Int32
	execute  func(context.Context, scriptsandbox.WorkspaceCommand) (scriptsandbox.WorkspaceCommandResult, error)
}

func (e *nativeCommandEngineFixture) ExecuteWorkspace(ctx context.Context, h scriptsandbox.WorkspaceHandle, c scriptsandbox.WorkspaceCommand) (scriptsandbox.WorkspaceCommandResult, error) {
	e.commands.Add(1)
	if err := e.ReconnectWorkspace(ctx, h); err != nil {
		return scriptsandbox.WorkspaceCommandResult{}, err
	}
	if e.execute != nil {
		return e.execute(ctx, c)
	}
	return scriptsandbox.WorkspaceCommandResult{Stdout: []byte{0, 255, 128}, ExitCode: 7}, nil
}

func nativeCommandPolicy(t *testing.T, store *Store) {
	t.Helper()
	store.SetScriptSandbox(&runtimeScriptSandbox{status: scriptsandbox.Status{Available: true, Adapter: "linux", Engine: "docker"}})
	current, err := store.GetScriptSandboxPolicy(mcpOwnerContext())
	if err != nil {
		t.Fatal(err)
	}
	if !current.Enabled {
		if _, err := store.UpdateScriptSandboxPolicy(mcpOwnerContext(), UpdateScriptSandboxPolicyCommand{ExpectedVersion: current.Version, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
}

func nativeCommandManager(t *testing.T, store *Store, e *nativeCommandEngineFixture) *NativeWorkspaceManager {
	t.Helper()
	nativeCommandPolicy(t, store)
	manager, err := NewPolicyNativeWorkspaceManager(store, e, scriptsandbox.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func nativeCommandCall(t *testing.T, store *Store, ctx context.Context, sdkID string, approve bool) (NativeWorkspaceCommandRequest, AgentToolCall) {
	t.Helper()
	activity, _ := AgentActivityFromContext(ctx)
	project, err := store.GetProject(context.Background(), activity.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"cmd":"printf native","login":false,"tty":false}`)
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: activity.ProjectID, ConversationID: project.PrimaryConversationID,
		AgentTurnID: activity.AgentTurnID, AgentTaskAttemptID: activity.AgentTaskAttemptID, ExecutionAttemptID: activity.ExecutionAttemptID,
		AttemptToken: activity.AttemptToken, SDKToolCallID: sdkID, ToolID: nativeWorkspaceExecTool, Arguments: raw})
	if err != nil {
		t.Fatal(err)
	}
	request := NativeWorkspaceCommandRequest{AgentToolCallID: call.AgentToolCallID, SDKToolCallID: sdkID, Arguments: raw,
		Command: scriptsandbox.WorkspaceCommand{Argv: []string{"sh", "-c", "printf native"}, Cwd: "/workspace", TimeoutMillis: 1000}}
	if approve {
		if _, err := store.ResolveAgentToolApproval(mcpOwnerContext(), ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID,
			ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve"}); err != nil {
			t.Fatal(err)
		}
		call, err = store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: sdkID})
		if err != nil {
			t.Fatal(err)
		}
	}
	return request, call
}

func TestNativeCommandThreeModesDurableReceiptReopenAndNoReplay(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, database, ctx, access := nativeWorkspaceFixture(t, mode)
			e := &nativeCommandEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
			manager := nativeCommandManager(t, store, e)
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			request, _ := nativeCommandCall(t, store, ctx, "native-command-1", true)
			_, err := store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: request.AgentToolCallID, Result: json.RawMessage(`"invented success"`)})
			assertDomainCode(t, err, "NATIVE_WORKSPACE_COMMAND_UNCONFIRMED")
			saved, err := manager.ExecuteCommand(ctx, access, request)
			if err != nil || saved.ExitCode != 7 || !bytes.Equal(saved.Stdout, []byte{0, 255, 128}) {
				t.Fatalf("command result: %+v %v", saved, err)
			}
			if e.commands.Load() != 1 {
				t.Fatal("command did not run exactly once")
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
			manager = nativeCommandManager(t, reopened, e)
			again, err := manager.ExecuteCommand(ctx, access, request)
			if err != nil || again.ResultHash != saved.ResultHash || !bytes.Equal(again.Stdout, saved.Stdout) || e.commands.Load() != 1 {
				t.Fatalf("lost receipt replayed: %+v %v", again, err)
			}
			if _, err := reopened.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: request.AgentToolCallID, Result: json.RawMessage(`"native SDK output"`)}); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.ExecuteCommand(ctx, access, request); err != nil {
				t.Fatal(err)
			}
			changed := request
			changed.Command.Argv = []string{"sh", "-c", "different command"}
			_, err = manager.ExecuteCommand(ctx, access, changed)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_REQUEST_CONFLICT")
			if _, err := reopened.db.Exec(`UPDATE native_workspace_commands SET stdout=X'01' WHERE agent_tool_call_id=?`, request.AgentToolCallID); err != nil {
				t.Fatal(err)
			}
			_, err = manager.ExecuteCommand(ctx, access, request)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_COMMAND_UNCONFIRMED")
			if e.commands.Load() != 1 {
				t.Fatal("tampered receipt replayed command")
			}
		})
	}
}

func TestNativeCommandRequiresExactApprovalAndCurrentExecution(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	e := &nativeCommandEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
	manager := nativeCommandManager(t, store, e)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	pending, _ := nativeCommandCall(t, store, ctx, "not-approved", false)
	_, err := manager.ExecuteCommand(ctx, access, pending)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_COMMAND_APPROVAL_REQUIRED")
	request, _ := nativeCommandCall(t, store, ctx, "approved", true)
	altered := request
	altered.Arguments = json.RawMessage(`{"cmd":"other"}`)
	_, err = manager.ExecuteCommand(ctx, access, altered)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_COMMAND_APPROVAL_REQUIRED")
	wrong := access
	wrong.HolderKey = strings.Repeat("b", 64)
	_, err = manager.ExecuteCommand(ctx, wrong, request)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_LEASE_INVALID")
	_, err = manager.ExecuteCommand(mcpOwnerContext(), access, request)
	assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
	altered = request
	altered.Command.Cwd = "/tmp"
	if _, err := manager.ExecuteCommand(ctx, access, altered); err == nil {
		t.Fatal("command escaped workspace policy")
	}
	if e.commands.Load() != 0 {
		t.Fatal("invalid authority executed a command")
	}
}

func TestNativeCommandOutstandingOperationRejectsSecondOwnerAndLostOutcome(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	e := &nativeCommandEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
	manager := nativeCommandManager(t, store, e)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	first, _ := nativeCommandCall(t, store, ctx, "first", true)
	second, _ := nativeCommandCall(t, store, ctx, "second", true)
	entered, release := make(chan struct{}), make(chan struct{})
	e.execute = func(ctx context.Context, _ scriptsandbox.WorkspaceCommand) (scriptsandbox.WorkspaceCommandResult, error) {
		close(entered)
		select {
		case <-release:
			return scriptsandbox.WorkspaceCommandResult{}, errors.New("private-provider-error")
		case <-ctx.Done():
			return scriptsandbox.WorkspaceCommandResult{}, ctx.Err()
		}
	}
	done := make(chan error, 1)
	var releaseOnce sync.Once
	releaseCommand := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		releaseCommand()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("command goroutine did not finish")
		}
	})
	go func() {
		defer close(done)
		_, err := manager.ExecuteCommand(ctx, access, first)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("command did not start")
	}
	_, err := manager.ExecuteCommand(ctx, access, first)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_COMMAND_UNCONFIRMED")
	_, err = manager.ExecuteCommand(ctx, access, second)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_OPERATION_IN_PROGRESS")
	releaseCommand()
	if err := <-done; err == nil {
		t.Fatal("lost result was accepted")
	}
	_, err = manager.ExecuteCommand(ctx, access, first)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_COMMAND_UNCONFIRMED")
	if e.commands.Load() != 1 {
		t.Fatal("unconfirmed command replayed")
	}
}

func TestNativeCommandRevocationQuotaAndDeletion(t *testing.T) {
	for _, failure := range []string{"quota", "cancelled-call", "policy", "delete"} {
		t.Run(failure, func(t *testing.T) {
			store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
			e := &nativeCommandEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
			manager := nativeCommandManager(t, store, e)
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			request, _ := nativeCommandCall(t, store, ctx, "checked-command", true)
			if failure == "quota" {
				if _, err := store.db.Exec(`UPDATE workspace_quotas SET max_storage_bytes=1`); err != nil {
					t.Fatal(err)
				}
				if _, err := manager.ExecuteCommand(ctx, access, request); err == nil {
					t.Fatal("command started without reserving result capacity")
				}
				if e.commands.Load() != 0 {
					t.Fatal("quota rejected after command side effect")
				}
				return
			}
			e.execute = func(_ context.Context, _ scriptsandbox.WorkspaceCommand) (scriptsandbox.WorkspaceCommandResult, error) {
				var reserved int
				if err := store.db.QueryRow(`SELECT reserved_bytes FROM native_workspace_commands WHERE agent_tool_call_id=?`, request.AgentToolCallID).Scan(&reserved); err != nil || reserved != nativeCommandOutputBudget {
					t.Fatalf("missing pre-effect reservation: %d %v", reserved, err)
				}
				switch failure {
				case "cancelled-call":
					_, err := store.CancelAgentToolCall(ctx, CancelAgentToolCallCommand{AgentToolCallID: request.AgentToolCallID, Reason: "test cancellation"})
					if err != nil {
						t.Fatal(err)
					}
				case "policy":
					p, err := store.GetScriptSandboxPolicy(mcpOwnerContext())
					if err != nil {
						t.Fatal(err)
					}
					if _, err := store.UpdateScriptSandboxPolicy(mcpOwnerContext(), UpdateScriptSandboxPolicyCommand{ExpectedVersion: p.Version, Enabled: false}); err != nil {
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
				return scriptsandbox.WorkspaceCommandResult{Stdout: []byte("late private output")}, nil
			}
			if _, err := manager.ExecuteCommand(ctx, access, request); err == nil {
				t.Fatal("revoked execution produced a success receipt")
			}
			var status string
			var size, reserved int
			if err := store.db.QueryRow(`SELECT status,length(stdout)+length(stderr),reserved_bytes FROM native_workspace_commands WHERE agent_tool_call_id=?`, request.AgentToolCallID).Scan(&status, &size, &reserved); err != nil {
				t.Fatal(err)
			}
			if size != 0 || reserved != 0 || status == "completed" {
				t.Fatalf("late result leaked: %s %d %d", status, size, reserved)
			}
		})
	}
}

func TestNativeCommandV58MigrationPreservesExecutionAndBackup(t *testing.T) {
	store, database, ctx, access := nativeWorkspaceFixture(t, "conversation")
	e := &nativeCommandEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
	manager := nativeCommandManager(t, store, e)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	before := nativeEnvironmentForTest(t, store, access)
	if _, err := store.db.Exec(`DROP TABLE native_workspace_commands; PRAGMA user_version=58`); err != nil {
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
	if err := reopened.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=58 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	after := nativeEnvironmentForTest(t, reopened, access)
	if after.environmentID != before.environmentID || after.handle != before.handle {
		t.Fatal("migration replaced execution environment")
	}
	var count int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM native_workspace_commands`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration invented command execution")
	}
	if _, err := reopened.ValidateNativeWorkspaceLease(ctx, access); err != nil {
		t.Fatal(err)
	}
}
