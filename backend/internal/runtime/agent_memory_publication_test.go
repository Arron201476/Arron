package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestMemoryPublicationBindingRejectsInjectedGenerationIdentity(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, _, ctx, _ := nativeWorkspaceFixture(t, mode)
			activity, _ := AgentActivityFromContext(ctx)
			raw, err := json.Marshal(AgentMemoryPublicationArguments{
				Snapshot: NativeWorkspaceSnapshotReference{Version: 1, SHA256: strings.Repeat("a", 64)},
				Files:    []string{nativeMemoryDirectory + "/memory_summary.md"},
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, injected := range []struct {
				id      string
				attempt int
			}{{"another-generation", 0}, {"", 1}, {"another-generation", 1}} {
				tx, err := store.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				_, _, err = store.memoryPublicationBindingTx(ctx, tx, BeginAgentToolCallCommand{
					ProjectID: activity.ProjectID, AgentTurnID: activity.AgentTurnID,
					AgentTaskAttemptID: activity.AgentTaskAttemptID, ExecutionAttemptID: activity.ExecutionAttemptID,
					AttemptToken: activity.AttemptToken, Arguments: raw,
					MemoryGenerationID: injected.id, MemoryGenerationAttempt: injected.attempt,
				})
				tx.Rollback()
				assertDomainCode(t, err, "AGENT_ACTIVITY_SCOPE_MISMATCH")
			}
		})
	}
}

func TestMemoryPublicationReceiptPreservesWriterAndUserForgetRevokesIt(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		for _, existing := range []bool{false, true} {
			for _, invalidation := range []string{"none", "edit", "forget", "legacy", "corrupt"} {
				t.Run(mode+map[bool]string{false: "-new", true: "-existing"}[existing]+"-"+invalidation, func(t *testing.T) {
					store, _, ctx, access := nativeWorkspaceFixture(t, mode)
					activity, _ := AgentActivityFromContext(ctx)
					project, err := store.GetProject(context.Background(), activity.ProjectID)
					if err != nil {
						t.Fatal(err)
					}
					version := 0
					if existing {
						_, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{ProjectID: activity.ProjectID, Files: map[string]string{"memory_summary.md": "old private"}, Enabled: true, RequestID: "initial"})
						if err != nil {
							t.Fatal(err)
						}
						version = 1
					}
					binding, err := store.ResolveAgentMemorySnapshot(ctx)
					if err != nil {
						t.Fatal(err)
					}
					var buffer bytes.Buffer
					writer := tar.NewWriter(&buffer)
					body := "new private memory"
					name := nativeMemoryDirectory + "/memory_summary.md"
					if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(body))}); err != nil {
						t.Fatal(err)
					}
					if _, err := writer.Write([]byte(body)); err != nil {
						t.Fatal(err)
					}
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
					saved, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "generated-memory", 0, buffer.Bytes())
					if err != nil {
						t.Fatal(err)
					}
					args := AgentMemoryPublicationArguments{Snapshot: NativeWorkspaceSnapshotReference{Version: saved.Version, SHA256: saved.Snapshot.SHA256}, ExpectedVersion: version, Files: []string{name}}
					raw, err := json.Marshal(args)
					if err != nil {
						t.Fatal(err)
					}
					call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: activity.ProjectID, ConversationID: project.PrimaryConversationID, AgentTurnID: activity.AgentTurnID,
						AgentTaskAttemptID: activity.AgentTaskAttemptID, ExecutionAttemptID: activity.ExecutionAttemptID, AttemptToken: activity.AttemptToken, SDKToolCallID: "memory-write", ToolID: publishAgentMemoryTool, Arguments: raw})
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(call.ArgumentsSummary), name) {
						t.Fatal("private paths leaked to shared tool summary")
					}
					approval := ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve"}
					other := identity.Principal{Kind: identity.KindUser, UserID: "other-memory-editor", WorkspaceID: project.WorkspaceID, Role: identity.RoleEditor}
					if err := store.BootstrapPrincipal(context.Background(), other); err != nil {
						t.Fatal(err)
					}
					preview, err := store.GetAgentMemoryProposal(mcpOwnerContext(), call.AgentToolCallID)
					if err != nil || !preview.Available || !preview.CanApprove || !preview.CanReject || preview.Files["memory_summary.md"] != body || preview.ExpectedVersion != version || preview.ApprovalID != approval.AgentToolApprovalID || preview.ArgumentsHash != call.ArgumentsHash {
						t.Fatalf("private preview: %+v %v", preview, err)
					}
					denied, err := store.GetAgentMemoryProposal(identity.WithPrincipal(context.Background(), other), call.AgentToolCallID)
					assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
					if len(denied.Files) != 0 || len(denied.Current.Files) != 0 {
						t.Fatal("private preview leaked on access denial")
					}
					_, err = store.ResolveAgentToolApproval(identity.WithPrincipal(context.Background(), other), approval)
					assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
					if invalidation != "none" {
						switch invalidation {
						case "edit", "forget":
							change := UpdateAgentMemoryCommand{ProjectID: project.ProjectID, ExpectedVersion: version, Files: map[string]string{"memory_summary.md": "new user edit"}, Enabled: true, RequestID: "invalidate-proposal"}
							if invalidation == "forget" {
								change.Files, change.Enabled, change.Forget = nil, false, true
							}
							if _, err := store.UpdateAgentMemory(mcpOwnerContext(), change); err != nil {
								t.Fatal(err)
							}
						case "legacy":
							if _, err := store.db.Exec(`UPDATE agent_memory_publications SET arguments_json='',session_id='' WHERE agent_tool_call_id=?`, call.AgentToolCallID); err != nil {
								t.Fatal(err)
							}
						case "corrupt":
							if _, err := store.db.Exec(`UPDATE native_workspace_snapshots SET content_hash=? WHERE session_id=? AND version=?`, strings.Repeat("a", 64), access.SessionID, args.Snapshot.Version); err != nil {
								t.Fatal(err)
							}
						}
						stale, err := store.GetAgentMemoryProposal(mcpOwnerContext(), call.AgentToolCallID)
						if err != nil || stale.Available || stale.CanApprove || !stale.CanReject || len(stale.Files) != 0 {
							t.Fatalf("stale proposal must only allow rejection: %+v %v", stale, err)
						}
						_, err = store.ResolveAgentToolApproval(mcpOwnerContext(), approval)
						assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
						approval.Action = "reject"
						if _, err := store.ResolveAgentToolApproval(mcpOwnerContext(), approval); err != nil {
							t.Fatal(err)
						}
						return
					}
					if _, err := store.ResolveAgentToolApproval(mcpOwnerContext(), approval); err != nil {
						t.Fatal(err)
					}
					if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID}); err != nil {
						t.Fatal(err)
					}
					_, err = store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: json.RawMessage(`{}`)})
					assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
					manager := nativeFileManager(t, store, &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)})
					request := AgentMemoryPublicationRequest{AgentToolCallID: call.AgentToolCallID, SDKToolCallID: call.SDKToolCallID, Arguments: args}
					first, err := manager.PublishMemory(ctx, access, request)
					if err != nil || first.Version != version+1 || first.ProjectID != project.ProjectID {
						t.Fatalf("publish: %+v %v", first, err)
					}
					again, err := manager.PublishMemory(ctx, access, request)
					if err != nil || !reflect.DeepEqual(first, again) {
						t.Fatalf("retry: %+v %v", again, err)
					}
					if _, err := store.ResolveAgentActivityPrincipal(ctx, activity); err != nil {
						t.Fatalf("writer invalidated itself: %v", err)
					}
					pinned, err := store.ResolveAgentMemorySnapshot(ctx)
					if pinned.CurrentVersion != first.Version {
						t.Fatalf("publication version missing: %+v", pinned)
					}
					pinned.CurrentVersion = binding.CurrentVersion
					if err != nil || !reflect.DeepEqual(binding, pinned) {
						t.Fatalf("input binding changed: %+v %v", pinned, err)
					}
					result, _ := json.Marshal(first)
					completed, err := store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: result})
					if err != nil || string(completed.ResultSummary) != `{"private_memory_result":true}` {
						t.Fatalf("complete: %+v %v", completed, err)
					}
					if _, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{ProjectID: project.ProjectID, ExpectedVersion: first.Version, Forget: true, RequestID: "forget"}); err != nil {
						t.Fatal(err)
					}
					_, err = store.ResolveAgentActivityPrincipal(ctx, activity)
					assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
					forgotten, err := store.GetAgentMemoryProposal(mcpOwnerContext(), call.AgentToolCallID)
					if err != nil || forgotten.Available || forgotten.CanApprove || len(forgotten.Files) != 0 || len(forgotten.Current.Files) != 0 {
						t.Fatalf("forgotten preview exposed private content: %+v %v", forgotten, err)
					}
					if _, err := manager.PublishMemory(ctx, access, request); err == nil {
						t.Fatal("forgotten execution republished its snapshot")
					}
				})
			}
		}
	}
}

func TestMemoryPublicationRejectsNonPrivateOrAmbiguousSelection(t *testing.T) {
	for _, files := range [][]string{
		{"notes.md"}, {".agent-memory/MEMORY_SUMMARY.md"}, {".agent-memory/../memory_summary.md"},
		{".agent-memory/memory_summary.md", ".agent-memory/memory_summary.md"},
		{".agent-memory/memory_summary.md", ".agent-memory/A/x", ".agent-memory/a/y"},
	} {
		raw, _ := json.Marshal(AgentMemoryPublicationArguments{Snapshot: NativeWorkspaceSnapshotReference{Version: 1, SHA256: strings.Repeat("a", 64)}, Files: files})
		_, err := decodeMemoryPublicationArguments(raw)
		assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	}
}
