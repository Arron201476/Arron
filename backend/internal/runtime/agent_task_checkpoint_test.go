package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackgroundTaskApprovalSurvivesRestartAndDoesNotSpendRetryBudget(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "content_agent.db")
	store, err := Open(path, loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	task := createBackgroundTaskForTest(t, store)
	claimCommand := ClaimAgentTaskCommand{WorkerID: "worker", ProviderID: "sdk", ModelID: "fixture", LeaseSeconds: 60}
	claim, err := store.ClaimAgentTask(ctx, claimCommand)
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v, %v", claim, err)
	}
	for index := 0; index < 4; index++ {
		sdkID := "sdk-approval-" + string(rune('a'+index))
		call := backgroundApprovalForTest(t, store, *claim, sdkID)
		command := PauseAgentTaskForApprovalCommand{AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken,
			PauseAgentTurnForApprovalCommand: PauseAgentTurnForApprovalCommand{SchemaVersion: "1.16", RunState: json.RawMessage(`{"$schemaVersion":"1.16","private_marker":"do-not-project"}`), PendingSDKToolCallIDs: []string{sdkID}}}
		waiting, err := store.PauseAgentTaskForApproval(ctx, command)
		if err != nil || waiting.Status != "waiting_approval" {
			t.Fatalf("pause: %+v, %v", waiting, err)
		}
		if _, err := store.PauseAgentTaskForApproval(ctx, command); err != nil {
			t.Fatalf("idempotent pause: %v", err)
		}
		if next, err := store.ClaimAgentTask(ctx, claimCommand); err != nil || next != nil {
			t.Fatalf("claimed unapproved task: %+v, %v", next, err)
		}
		public, _ := json.Marshal(waiting)
		if strings.Contains(string(public), "private_marker") {
			t.Fatal("private state leaked into task")
		}
		var leaked int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE project_id = ? AND payload_json LIKE '%private_marker%'`, task.ProjectID).Scan(&leaked); err != nil || leaked != 0 {
			t.Fatalf("private state leaked: %d, %v", leaked, err)
		}
		oldToken := claim.AttemptToken
		if index == 0 {
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(path, loadBackgroundTaskRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
		}
		resolveBackgroundApprovalForTest(t, store, call, "approve")
		claim, err = store.ClaimAgentTask(ctx, claimCommand)
		if err != nil || claim == nil || claim.Resume == nil || len(claim.Resume.ApprovalDecisions) != 1 || claim.Task.AttemptCount != 1 || claim.Attempt.AttemptNo != 1 || claim.AttemptToken == oldToken {
			t.Fatalf("resume: %+v, %v", claim, err)
		}
		if _, err := store.UpdateAgentTaskProgress(ctx, UpdateAgentTaskProgressCommand{AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: oldToken, Current: 1, Total: 2}); err == nil {
			t.Fatal("old Worker token reused")
		}
		if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: sdkID, ConfigurationHash: toolConfigurationHashForTest(t, store, call.ToolID)}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: json.RawMessage(`{"ok":true}`)}); err != nil {
			t.Fatal(err)
		}
	}
	completed, err := store.CompleteAgentTask(ctx, CompleteAgentTaskCommand{AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken, Result: json.RawMessage(`{"summary":"done"}`)})
	if err != nil || completed.Status != "completed" {
		t.Fatalf("complete: %+v, %v", completed, err)
	}
	var checkpoints int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_task_run_states WHERE agent_task_id = ?`, task.AgentTaskID).Scan(&checkpoints); err != nil || checkpoints != 0 {
		t.Fatalf("retained checkpoint: %d, %v", checkpoints, err)
	}
}

func TestBackgroundTaskApprovalCancelRejectAndLeaseBoundaries(t *testing.T) {
	for _, action := range []string{"cancel", "reject", "expired", "early_approve", "delete_project"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadBackgroundTaskRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			task := createBackgroundTaskForTest(t, store)
			claimCommand := ClaimAgentTaskCommand{WorkerID: "worker", ProviderID: "sdk", ModelID: "fixture", LeaseSeconds: 60}
			claim, err := store.ClaimAgentTask(ctx, claimCommand)
			if err != nil || claim == nil {
				t.Fatalf("claim: %+v, %v", claim, err)
			}
			call := backgroundApprovalForTest(t, store, *claim, "sdk-paused")
			command := PauseAgentTaskForApprovalCommand{AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken,
				PauseAgentTurnForApprovalCommand: PauseAgentTurnForApprovalCommand{SchemaVersion: "1.16", RunState: json.RawMessage(`{"$schemaVersion":"1.16"}`), PendingSDKToolCallIDs: []string{call.SDKToolCallID}}}
			bad := command
			bad.PendingSDKToolCallIDs = []string{"sdk-other-task"}
			if _, err := store.PauseAgentTaskForApproval(ctx, bad); err == nil {
				t.Fatal("foreign approval accepted")
			}
			if action == "early_approve" {
				resolveBackgroundApprovalForTest(t, store, call, "approve")
			}
			if _, err := store.PauseAgentTaskForApproval(ctx, command); err != nil {
				t.Fatal(err)
			}
			if action == "cancel" || action == "delete_project" {
				var cancelled AgentTask
				if action == "cancel" {
					cancelled, err = store.CancelAgentTask(ctx, CancelAgentTaskCommand{AgentTaskID: task.AgentTaskID, ActorRef: "test"})
				} else {
					preview, previewErr := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: task.ProjectID})
					if previewErr != nil {
						t.Fatal(previewErr)
					}
					_, err = store.ConfirmProjectDelete(ctx, ConfirmProjectDeleteCommand{ProjectID: task.ProjectID, PreviewHash: preview.SnapshotHash, Confirmed: true, ActorRef: "test"})
					if err != nil {
						t.Fatal(err)
					}
					cancelled, err = store.GetAgentTask(ctx, task.AgentTaskID)
				}
				if err != nil || cancelled.Status != "cancelled" {
					t.Fatalf("cancel: %+v, %v", cancelled, err)
				}
				fresh, err := store.GetAgentToolCall(ctx, call.AgentToolCallID)
				if err != nil || fresh.Status != "cancelled" || fresh.Approval.Status != "cancelled" {
					t.Fatalf("cancelled tool: %+v, %v", fresh, err)
				}
				if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID}); err == nil {
					t.Fatal("cancelled task executed tool")
				}
				var remaining int
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_task_run_states WHERE agent_task_id = ?`, task.AgentTaskID).Scan(&remaining); err != nil || remaining != 0 {
					t.Fatalf("private checkpoint survived deletion: %d, %v", remaining, err)
				}
				return
			}
			if action != "early_approve" {
				decision := "approve"
				if action == "reject" {
					decision = "reject"
				}
				resolveBackgroundApprovalForTest(t, store, call, decision)
			}
			resumed, err := store.ClaimAgentTask(ctx, claimCommand)
			if err != nil || resumed == nil || resumed.Resume == nil {
				t.Fatalf("resume: %+v, %v", resumed, err)
			}
			if action == "reject" && resumed.Resume.ApprovalDecisions[0].Action != "reject" {
				t.Fatal("rejection lost")
			}
			if action == "expired" {
				store.now = func() time.Time { return resumed.Attempt.LeaseUntil.Add(time.Second) }
				if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID}); err == nil {
					t.Fatal("expired Worker started a write")
				}
				if next, err := store.ClaimAgentTask(ctx, claimCommand); err != nil || next != nil {
					t.Fatalf("unsafe replay: %+v, %v", next, err)
				}
				fresh, _ := store.GetAgentTask(ctx, task.AgentTaskID)
				if fresh.Status != "failed" || fresh.FailureCode == nil || *fresh.FailureCode != "AGENT_TASK_REPLAY_UNSAFE" {
					t.Fatalf("unsafe replay not marked: %+v", fresh)
				}
			}
		})
	}
}

func backgroundApprovalForTest(t *testing.T, store *Store, claim AgentTaskClaim, sdkID string) AgentToolCall {
	t.Helper()
	command := BeginAgentToolCallCommand{ProjectID: claim.Task.ProjectID, ConversationID: claim.Task.ConversationID, SkillInvocationID: claim.Task.SkillInvocationID,
		AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken, SDKToolCallID: sdkID,
		ToolID: "mcp:fixture/save_fact", Arguments: json.RawMessage(`{"value":"safe"}`), ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact")}
	bad := command
	bad.AttemptToken = "wrong-token"
	if _, err := store.BeginAgentToolCall(context.Background(), bad); err == nil {
		t.Fatal("unowned attempt was accepted")
	}
	bad.AgentTaskAttemptID = ""
	if _, err := store.BeginAgentToolCall(context.Background(), bad); err == nil {
		t.Fatal("background call without attempt was accepted")
	}
	call, err := store.BeginAgentToolCall(context.Background(), command)
	if err != nil || call.Approval == nil {
		t.Fatalf("background tool: %+v, %v", call, err)
	}
	return call
}

func resolveBackgroundApprovalForTest(t *testing.T, store *Store, call AgentToolCall, action string) {
	t.Helper()
	_, err := store.ResolveAgentToolApproval(context.Background(), ResolveAgentToolApprovalCommand{
		AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version,
		SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: action, ActorRef: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
}
