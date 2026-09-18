package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"content-agent/backend/internal/scriptsandbox"
)

type nativeFileEngineFixture struct {
	*nativeEngineFixture
	operations []scriptsandbox.WorkspaceFileOperation
	operation  func(context.Context, scriptsandbox.WorkspaceFileOperation) (scriptsandbox.WorkspaceFileResult, error)
}

func nativeFileResult(request scriptsandbox.WorkspaceFileOperation) scriptsandbox.WorkspaceFileResult {
	r := scriptsandbox.WorkspaceFileResult{Operation: request.Operation, Path: request.Path, Data: []byte{}, Entries: []scriptsandbox.WorkspaceEntry{}}
	entry := scriptsandbox.WorkspaceEntry{Path: request.Path, Mode: 0o600}
	switch request.Operation {
	case "read", "write":
		body := request.Data
		if request.Operation == "read" {
			body = []byte{0, 255, 128}
			r.Data = body
		}
		entry.SizeBytes, entry.SHA256 = int64(len(body)), sha256Hex(body)
		r.SHA256 = entry.SHA256
		r.Entries = []scriptsandbox.WorkspaceEntry{entry}
	case "mkdir", "stat":
		entry.Directory, entry.Mode = true, 0o700
		r.Entries = []scriptsandbox.WorkspaceEntry{entry}
	case "chmod":
		entry.Mode = *request.Mode
		r.Entries = []scriptsandbox.WorkspaceEntry{entry}
	}
	return r
}

func (e *nativeFileEngineFixture) FileWorkspace(ctx context.Context, h scriptsandbox.WorkspaceHandle, request scriptsandbox.WorkspaceFileOperation) (scriptsandbox.WorkspaceFileResult, error) {
	if err := e.ReconnectWorkspace(ctx, h); err != nil {
		return scriptsandbox.WorkspaceFileResult{}, err
	}
	e.operations = append(e.operations, request)
	if e.operation != nil {
		return e.operation(ctx, request)
	}
	return nativeFileResult(request), nil
}

func nativeFileManager(t *testing.T, store *Store, engine *nativeFileEngineFixture) *NativeWorkspaceManager {
	t.Helper()
	nativeCommandPolicy(t, store)
	manager, err := NewPolicyNativeWorkspaceManager(store, engine, scriptsandbox.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func nativeFileCall(t *testing.T, store *Store, ctx context.Context, sdkID string, approve bool) NativeWorkspaceFileRequest {
	t.Helper()
	activity, _ := AgentActivityFromContext(ctx)
	project, err := store.GetProject(context.Background(), activity.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: activity.ProjectID, ConversationID: project.PrimaryConversationID,
		AgentTurnID: activity.AgentTurnID, AgentTaskAttemptID: activity.AgentTaskAttemptID, ExecutionAttemptID: activity.ExecutionAttemptID,
		AttemptToken: activity.AttemptToken, SDKToolCallID: sdkID, ToolID: nativeWorkspacePatchTool, Arguments: json.RawMessage(`{"input":"*** Begin Patch\n*** Add File: file\n+native\n*** End Patch\n"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if approve {
		if _, err := store.ResolveAgentToolApproval(mcpOwnerContext(), ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID,
			ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: sdkID}); err != nil {
			t.Fatal(err)
		}
	}
	return NativeWorkspaceFileRequest{RequestID: strings.Repeat("a", 32), AgentToolCallID: call.AgentToolCallID, SDKToolCallID: sdkID, ArgumentsHash: call.ArgumentsHash,
		File: scriptsandbox.WorkspaceFileOperation{Operation: "write", Path: "file", Data: []byte{0, 255, 128}}}
}

func TestNativeFilesThreeModesApprovedBinaryReceiptRebuildAndNoReplay(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, database, ctx, access := nativeWorkspaceFixture(t, mode)
			engine := &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
			manager := nativeFileManager(t, store, engine)
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			root := NativeWorkspaceFileRequest{RequestID: strings.Repeat("b", 32), File: scriptsandbox.WorkspaceFileOperation{Operation: "mkdir", Path: ".", Parents: true}}
			if _, err := manager.File(ctx, access, root); err != nil {
				t.Fatal(err)
			}
			read := NativeWorkspaceFileRequest{RequestID: strings.Repeat("c", 32), File: scriptsandbox.WorkspaceFileOperation{Operation: "read", Path: "file"}}
			readResult, err := manager.File(ctx, access, read)
			if err != nil || !bytes.Equal(readResult.File.Data, []byte{0, 255, 128}) {
				t.Fatalf("binary read = %+v, %v", readResult, err)
			}
			request := nativeFileCall(t, store, ctx, "native-patch", true)
			saved, err := manager.File(ctx, access, request)
			if err != nil || saved.File.SHA256 != sha256Hex(request.File.Data) || saved.RequestID != request.RequestID {
				t.Fatalf("file = %+v, %v", saved, err)
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
			manager = nativeFileManager(t, reopened, engine)
			again, err := manager.File(ctx, access, request)
			if err != nil || again.RequestHash != saved.RequestHash || len(engine.operations) != 3 {
				t.Fatalf("receipt recovery replayed file write: %+v, %v", again, err)
			}
			if _, err := reopened.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: request.AgentToolCallID, Result: json.RawMessage(`"Created file"`)}); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.File(ctx, access, request); err != nil || len(engine.operations) != 3 {
				t.Fatal("completed tool could not recover exact prior receipt")
			}
			altered := request
			altered.File.Data = []byte("changed")
			_, err = manager.File(ctx, access, altered)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_REQUEST_CONFLICT")
			altered = request
			altered.RequestID = strings.Repeat("c", 32)
			_, err = manager.File(ctx, access, altered)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_FILE_APPROVAL_REQUIRED")
			if _, err := reopened.db.Exec(`UPDATE native_workspace_file_operations SET result_json='{}' WHERE session_id=?`, access.SessionID); err != nil {
				t.Fatal(err)
			}
			_, err = manager.File(ctx, access, request)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_FILE_UNCONFIRMED")
		})
	}
}

func TestNativeFilesRejectMissingChangedOrReadOnlyWriteAuthority(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
	manager := nativeFileManager(t, store, engine)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	pending := nativeFileCall(t, store, ctx, "pending", false)
	_, err := manager.File(ctx, access, pending)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_FILE_APPROVAL_REQUIRED")
	request := nativeFileCall(t, store, ctx, "approved", true)
	for _, change := range []string{"no_call", "hash", "sdk", "configuration", "path", "lease", "parent", "read_descriptor"} {
		altered, wrong := request, access
		switch change {
		case "no_call":
			altered.AgentToolCallID, altered.SDKToolCallID, altered.ArgumentsHash = "", "", ""
		case "hash":
			altered.ArgumentsHash = strings.Repeat("b", 64)
		case "sdk":
			altered.SDKToolCallID = "other"
		case "configuration":
			altered.ConfigurationHash = "caller-selected"
		case "path":
			altered.File.Path = "../escape"
		case "lease":
			wrong.HolderKey = strings.Repeat("c", 64)
		case "parent":
			if _, err := store.db.Exec(`INSERT INTO agent_subtask_reads(agent_tool_call_id,parent_tool_call_id) VALUES(?,?)`, request.AgentToolCallID, pending.AgentToolCallID); err != nil {
				t.Fatal(err)
			}
		case "read_descriptor":
			if _, err := store.db.Exec(`DELETE FROM agent_subtask_reads WHERE agent_tool_call_id=?`, request.AgentToolCallID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE agent_tool_calls SET access_mode='read' WHERE agent_tool_call_id=?`, request.AgentToolCallID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := manager.File(ctx, wrong, altered); err == nil {
			t.Fatalf("%s incorrectly authorized a write", change)
		}
	}
	if len(engine.operations) != 0 {
		t.Fatal("invalid authority reached file engine")
	}
}

func TestNativeFilesPendingAndUnknownWritesCannotCompletePatchOrReplay(t *testing.T) {
	for _, fault := range []string{"inflight", "unknown", "safe_failure", "bad_hash"} {
		t.Run(fault, func(t *testing.T) {
			store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
			engine := &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
			manager := nativeFileManager(t, store, engine)
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			request := nativeFileCall(t, store, ctx, "patch", true)
			if fault == "inflight" {
				if _, _, _, err := manager.beginFile(ctx, access, request); err != nil {
					t.Fatal(err)
				}
			} else {
				engine.operation = func(_ context.Context, request scriptsandbox.WorkspaceFileOperation) (scriptsandbox.WorkspaceFileResult, error) {
					if fault == "safe_failure" {
						return scriptsandbox.WorkspaceFileResult{}, &scriptsandbox.Error{Code: "WORKSPACE_FILE_NOT_FOUND"}
					}
					if fault == "bad_hash" {
						result := nativeFileResult(request)
						result.SHA256 = "wrong"
						return result, nil
					}
					return scriptsandbox.WorkspaceFileResult{}, errors.New("file outcome lost")
				}
				if _, err := manager.File(ctx, access, request); err == nil {
					t.Fatal("unknown file result was accepted")
				}
			}
			_, err := store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: request.AgentToolCallID, Result: json.RawMessage(`"invented success"`)})
			assertDomainCode(t, err, "NATIVE_WORKSPACE_FILE_UNCONFIRMED")
			_, err = manager.File(ctx, access, request)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_FILE_UNCONFIRMED")
			if len(engine.operations) > 1 {
				t.Fatal("unknown operation was replayed")
			}
			if fault == "safe_failure" && nativeEnvironmentForTest(t, store, access).State != "ready" {
				t.Fatal("confirmed preflight rejection discarded workspace")
			}
		})
	}
}

func TestNativeFilesInFlightPatchFinishesDuringAfterTurnPause(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
	manager := nativeFileManager(t, store, engine)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	request := nativeFileCall(t, store, ctx, "patch", true)
	activity, _ := AgentActivityFromContext(ctx)
	if _, err := store.db.Exec(`UPDATE agent_turns SET status='pausing' WHERE agent_turn_id=?`, activity.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.File(ctx, access, request); err != nil {
		t.Fatal(err)
	}
}

func TestNativeFilesDeletionClearsMetadataAndLateResult(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
	manager := nativeFileManager(t, store, engine)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	request := nativeFileCall(t, store, ctx, "patch", true)
	engine.operation = func(_ context.Context, operation scriptsandbox.WorkspaceFileOperation) (scriptsandbox.WorkspaceFileResult, error) {
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
		return nativeFileResult(operation), nil
	}
	if _, err := manager.File(ctx, access, request); err == nil {
		t.Fatal("deleted project accepted a late file result")
	}
	var status, result string
	var reserved int
	if err := store.db.QueryRow(`SELECT status,result_json,reserved_bytes FROM native_workspace_file_operations WHERE session_id=?`, access.SessionID).Scan(&status, &result, &reserved); err != nil {
		t.Fatal(err)
	}
	if status != "deleted" || result != "" || reserved != 0 {
		t.Fatal("deleted file receipt retained private metadata or reservation")
	}
}

func TestNativeFilesV59MigrationPreservesLeaseAndBackup(t *testing.T) {
	store, database, ctx, access := nativeWorkspaceFixture(t, "conversation")
	registry := store.registry
	if _, err := store.db.Exec(`DROP TABLE native_workspace_file_operations; PRAGMA user_version=59`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(database, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.ValidateNativeWorkspaceLease(ctx, access); err != nil {
		t.Fatal(err)
	}
	var backup string
	if err := reopened.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=59 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM native_workspace_file_operations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration invented file writes")
	}
}
