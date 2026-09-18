package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func executionControlCallForTest(t *testing.T, store *Store, project Project, args ExecutionControlArguments) (context.Context, AgentToolCall) {
	t.Helper()
	user := identity.DefaultLocalPrincipal()
	ctx := identity.WithPrincipal(context.Background(), user)
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Control this exact task"}, CommandMeta{Scope: project.ProjectID, CommandType: "agent_turn", IdempotencyKey: store.newID("control-test"), RequestHash: "control-test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_turns SET status='running' WHERE agent_turn_id=?`, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	service := WithAgentActivity(identity.WithDelegatedUser(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), user), AgentActivityIdentity{ProjectID: project.ProjectID, AgentTurnID: turn.AgentTurnID})
	raw, _ := json.Marshal(args)
	call, err := store.BeginAgentToolCall(service, BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, AgentTurnID: turn.AgentTurnID, ToolID: "runtime:control_execution", SDKToolCallID: "sdk-control-" + turn.AgentTurnID, Arguments: raw})
	if err != nil || call.Approval == nil || call.Status != "pending_approval" {
		t.Fatalf("approval: %+v %v", call, err)
	}
	return service, call
}

func approveExecutionControlForTest(t *testing.T, store *Store, ctx context.Context, call AgentToolCall) {
	t.Helper()
	resolveBackgroundApprovalForTest(t, store, call, "approve")
	if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID}); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionControlRunUsesExistingTransitionsAndDurableReceipts(t *testing.T) {
	for _, action := range []string{"pause_run", "resume_run", "cancel_run", "retry_failed_step"} {
		t.Run(action, func(t *testing.T) {
			database := filepath.Join(t.TempDir(), "control.db")
			store := openProjectFilesTestStore(t, database)
			defer func() { store.Close() }()
			project, claim := statefulToolClaimForTest(t, store)
			user := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			expected := "pausing"
			if action == "resume_run" {
				if _, err := store.RequestRunPause(user, PauseRunCommand{RunID: claim.Attempt.RunID}); err != nil {
					t.Fatal(err)
				}
				expected = "running"
			} else if action == "cancel_run" {
				expected = "cancelled"
			} else if action == "retry_failed_step" {
				if _, err := store.FailExecutionAttempt(user, FailExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash, ErrorCode: "PROVIDER_RESPONSE_INVALID"}); err != nil {
					t.Fatal(err)
				}
				expected = "running"
			}
			snapshot, err := store.InspectExecutionControl(user, project.ProjectID, "run", claim.Attempt.RunID)
			if err != nil {
				t.Fatal(err)
			}
			args := ExecutionControlArguments{"run", claim.Attempt.RunID, action, snapshot.SnapshotHash}
			ctx, call := executionControlCallForTest(t, store, project, args)
			if _, err := store.ControlExecution(ctx, call.AgentToolCallID, call.SDKToolCallID, args); err == nil {
				t.Fatal("unapproved control executed")
			}
			approveExecutionControlForTest(t, store, ctx, call)
			result, err := store.ControlExecution(ctx, call.AgentToolCallID, call.SDKToolCallID, args)
			if err != nil || result.Status != expected {
				t.Fatalf("control: %+v %v", result, err)
			}
			if action == "retry_failed_step" {
				if _, err := store.db.Exec(`UPDATE runs SET status='completed', current_step_run_id=NULL WHERE run_id=?`, args.TargetID); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store = openProjectFilesTestStore(t, database)
			replayed, err := store.ControlExecution(ctx, call.AgentToolCallID, call.SDKToolCallID, args)
			if err != nil || replayed != result {
				t.Fatalf("durable replay: %+v %v", replayed, err)
			}
			if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=?`, project.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			_, err = store.ControlExecution(ctx, call.AgentToolCallID, call.SDKToolCallID, args)
			assertDomainCode(t, err, "ROLE_FORBIDDEN")
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM idempotency_records WHERE idempotency_key=?`, "agent-control:"+call.AgentToolCallID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("receipt count %d %v", count, err)
			}
		})
	}
}

func TestExecutionControlRejectsStaleUnauthorizedAndMismatchedTargets(t *testing.T) {
	for _, mode := range []string{"stale", "arguments", "sdk-id", "rejected", "turn-stopped", "permission-revoked", "wrong-project", "unaudited-public-command"} {
		t.Run(mode, func(t *testing.T) {
			store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "control.db"))
			defer store.Close()
			project, claim := statefulToolClaimForTest(t, store)
			user := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			snapshot, err := store.InspectExecutionControl(user, project.ProjectID, "run", claim.Attempt.RunID)
			if err != nil {
				t.Fatal(err)
			}
			args := ExecutionControlArguments{"run", claim.Attempt.RunID, "cancel_run", snapshot.SnapshotHash}
			ctx, call := executionControlCallForTest(t, store, project, args)
			if mode == "rejected" {
				resolveBackgroundApprovalForTest(t, store, call, "reject")
			} else {
				approveExecutionControlForTest(t, store, ctx, call)
			}
			expected := "AGENT_TOOL_CALL_STATE_CONFLICT"
			switch mode {
			case "stale":
				if _, err := store.RequestRunPause(user, PauseRunCommand{RunID: args.TargetID}); err != nil {
					t.Fatal(err)
				}
				expected = "EXECUTION_CONTROL_STALE"
			case "arguments":
				args.ActionID = "pause_run"
			case "sdk-id":
				call.SDKToolCallID = "wrong"
			case "turn-stopped":
				if _, err := store.db.Exec(`UPDATE agent_turns SET status='cancelled' WHERE agent_turn_id=?`, *call.AgentTurnID); err != nil {
					t.Fatal(err)
				}
				expected = "AGENT_ACTIVITY_STALE"
			case "permission-revoked":
				if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=?`, project.WorkspaceID); err != nil {
					t.Fatal(err)
				}
				expected = "ROLE_FORBIDDEN"
			case "wrong-project":
				other, err := store.CreateProject(user, "other")
				if err != nil {
					t.Fatal(err)
				}
				_, err = store.InspectExecutionControl(ctx, other.ProjectID, "run", args.TargetID)
				assertDomainCode(t, err, "PROJECT_NOT_FOUND")
				return
			case "unaudited-public-command":
				_, err := store.CancelRun(ctx, CancelRunCommand{RunID: args.TargetID, Confirmed: true})
				assertDomainCode(t, err, "AGENT_TOOL_APPROVAL_REQUIRED")
				return
			}
			_, err = store.ControlExecution(ctx, call.AgentToolCallID, call.SDKToolCallID, args)
			assertDomainCode(t, err, expected)
			run, err := store.GetRun(user, claim.Attempt.RunID)
			if err != nil || run.Status == "cancelled" {
				t.Fatalf("rejected command changed target: %+v %v", run, err)
			}
		})
	}
}

func TestExecutionControlBackgroundPauseResumeCancellationAndRetry(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "control.db"), loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	task := createBackgroundTaskForTest(t, store)
	user := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	project, err := store.GetProject(user, task.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"pause_agent_task", "resume_agent_task", "cancel_agent_task", "retry_agent_task"} {
		snapshot, err := store.InspectExecutionControl(user, project.ProjectID, "agent_task", task.AgentTaskID)
		if err != nil {
			t.Fatal(err)
		}
		args := ExecutionControlArguments{"agent_task", task.AgentTaskID, action, snapshot.SnapshotHash}
		ctx, call := executionControlCallForTest(t, store, project, args)
		approveExecutionControlForTest(t, store, ctx, call)
		result, err := store.ControlExecution(ctx, call.AgentToolCallID, call.SDKToolCallID, args)
		expected := map[string]string{"pause_agent_task": "paused", "resume_agent_task": "queued", "cancel_agent_task": "cancelled", "retry_agent_task": "queued"}[action]
		if err != nil || result.Status != expected {
			t.Fatalf("task control: %+v %v", result, err)
		}
	}
}

func TestExecutionControlTargetPaginationAndProjectIsolation(t *testing.T) {
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "targets.db"))
	defer store.Close()
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	project, _, first := startNovelRunForLifecycle(t, store, "many runs")
	other, _, _ := startNovelRunForLifecycle(t, store, "other project")
	for index := 0; index < 52; index++ {
		_, err := store.db.Exec(`INSERT INTO runs(run_id, project_id, conversation_id, capability_id,
			capability_version, run_kind, write_intent, status, current_input_snapshot_version_id,
			input_snapshot_status, config_snapshot_json, created_at, updated_at)
			SELECT ?, project_id, conversation_id, capability_id, capability_version, run_kind, 0,
			'completed', current_input_snapshot_version_id, input_snapshot_status,
			config_snapshot_json, created_at, updated_at FROM runs WHERE run_id=?`,
			fmt.Sprintf("run-page-%03d", index), first.Run.RunID)
		if err != nil {
			t.Fatal(err)
		}
	}
	items, next, err := store.ListExecutionControlTargets(ctx, project.ProjectID, "run", "")
	if err != nil || len(items) != 50 || next != items[49].TargetID {
		t.Fatalf("first page: %d %q %v", len(items), next, err)
	}
	rest, end, err := store.ListExecutionControlTargets(ctx, project.ProjectID, "run", next)
	if err != nil || len(rest) != 3 || end != "" {
		t.Fatalf("last page: %d %q %v", len(rest), end, err)
	}
	seen := map[string]bool{}
	for _, item := range append(items, rest...) {
		if seen[item.TargetID] || item.ProjectID != project.ProjectID || item.TargetType != "run" || item.CreatedAt.IsZero() || item.SnapshotHash != "" {
			t.Fatalf("invalid target: %+v", item)
		}
		seen[item.TargetID] = true
	}
	items, _, err = store.ListExecutionControlTargets(ctx, other.ProjectID, "run", "")
	if err != nil || len(items) != 1 || seen[items[0].TargetID] {
		t.Fatalf("project isolation: %+v %v", items, err)
	}
	agent := WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: project.ProjectID, AgentTurnID: "turn"})
	_, _, err = store.ListExecutionControlTargets(agent, other.ProjectID, "run", "")
	assertDomainCode(t, err, "PROJECT_NOT_FOUND")
	_, _, err = store.ListExecutionControlTargets(ctx, project.ProjectID, "other", "")
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
}

func TestBackgroundRetryChecksAllAttemptsAndProjectsTheSameRisk(t *testing.T) {
	for _, mode := range []string{"not-started", "read-only", "write-started", "old-write", "checkpoint"} {
		t.Run(mode, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "retry.db"), loadBackgroundTaskRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			ctx := context.Background()
			task := createBackgroundTaskForTest(t, store)
			claim, err := store.ClaimAgentTask(ctx, ClaimAgentTaskCommand{WorkerID: "worker", ProviderID: "sdk", ModelID: "fixture", LeaseSeconds: 60})
			if err != nil || claim == nil {
				t.Fatalf("claim: %+v %v", claim, err)
			}
			call := backgroundApprovalForTest(t, store, *claim, "sdk-risk")
			if mode == "checkpoint" {
				_, err = store.PauseAgentTaskForApproval(ctx, PauseAgentTaskForApprovalCommand{
					AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken,
					PauseAgentTurnForApprovalCommand: PauseAgentTurnForApprovalCommand{SchemaVersion: "1.16",
						RunState: json.RawMessage(`{"$schemaVersion":"1.16"}`), PendingSDKToolCallIDs: []string{call.SDKToolCallID}},
				})
				if err != nil {
					t.Fatal(err)
				}
			} else if mode != "not-started" {
				resolveBackgroundApprovalForTest(t, store, call, "approve")
				_, err = store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: toolConfigurationHashForTest(t, store, call.ToolID)})
				if err != nil {
					t.Fatal(err)
				}
				if mode == "read-only" {
					if _, err := store.db.Exec(`UPDATE agent_tool_calls SET access_mode='read' WHERE agent_tool_call_id=?`, call.AgentToolCallID); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Model historical failed attempts, including a later attempt with no writes.
			if _, err := store.db.Exec(`UPDATE agent_task_attempts SET status='failed' WHERE agent_task_id=?`, task.AgentTaskID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE agent_tasks SET status='failed' WHERE agent_task_id=?`, task.AgentTaskID); err != nil {
				t.Fatal(err)
			}
			if mode == "old-write" {
				_, err = store.db.Exec(`INSERT INTO agent_task_attempts(agent_task_attempt_id, agent_task_id, attempt_no, worker_id,
					provider_id, model_id, input_snapshot_hash, status, token_hash, lease_until, started_at)
					SELECT 'later-attempt', agent_task_id, 2, worker_id, provider_id, model_id, input_snapshot_hash,
					'failed', token_hash, lease_until, started_at FROM agent_task_attempts WHERE agent_task_attempt_id=?`, claim.Attempt.AgentTaskAttemptID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.Exec(`UPDATE agent_tasks SET attempt_count=2 WHERE agent_task_id=?`, task.AgentTaskID); err != nil {
					t.Fatal(err)
				}
			}
			blocked := mode == "write-started" || mode == "old-write" || mode == "checkpoint"
			fresh, err := store.GetAgentTask(ctx, task.AgentTaskID)
			if err != nil || (fresh.RetryBlockedReason != "") != blocked {
				t.Fatalf("detail risk: %+v %v", fresh, err)
			}
			listed, err := store.ListAgentTasks(ctx, task.ProjectID)
			if err != nil || len(listed) != 1 || listed[0].RetryBlockedReason != fresh.RetryBlockedReason {
				t.Fatalf("list risk: %+v %v", listed, err)
			}
			snapshot, err := store.InspectExecutionControl(ctx, task.ProjectID, "agent_task", task.AgentTaskID)
			if err != nil || slices.ContainsFunc(snapshot.AvailableActions, func(a AvailableAction) bool { return a.ActionID == "retry_agent_task" && a.Enabled }) == blocked {
				t.Fatalf("SDK control risk: %+v %v", snapshot, err)
			}
			result, err := store.RetryAgentTask(ctx, RetryAgentTaskCommand{AgentTaskID: task.AgentTaskID})
			if blocked {
				assertDomainCode(t, err, "SDK_TOOL_REPLAY_RISK")
				fresh, _ = store.GetAgentTask(ctx, task.AgentTaskID)
				if fresh.Status != "failed" {
					t.Fatal("blocked retry mutated task")
				}
				cancelled, err := store.CancelAgentTask(ctx, CancelAgentTaskCommand{AgentTaskID: task.AgentTaskID})
				writeRisk := mode != "checkpoint"
				if err != nil || cancelled.Status != "cancelled" || (cancelled.RetryBlockedReason != "") != writeRisk {
					t.Fatalf("cancel receipt lost retry policy: %+v %v", cancelled, err)
				}
				if writeRisk {
					_, err := store.RetryAgentTask(ctx, RetryAgentTaskCommand{AgentTaskID: task.AgentTaskID})
					assertDomainCode(t, err, "SDK_TOOL_REPLAY_RISK")
				}
			} else if err != nil || result.Status != "queued" {
				t.Fatalf("safe retry: %+v %v", result, err)
			}
		})
	}
}
