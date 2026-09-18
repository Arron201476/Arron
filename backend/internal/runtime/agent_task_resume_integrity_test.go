package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCorruptBackgroundResumeDoesNotPoisonTheQueueOrReplay(t *testing.T) {
	for _, test := range []struct{ name, statement string }{
		{"hash", `UPDATE agent_task_run_states SET state_hash='broken' WHERE agent_task_id=?`},
		{"schema", `UPDATE agent_task_run_states SET schema_version='wrong' WHERE agent_task_id=?`},
		{"version", `UPDATE agent_task_run_states SET checkpoint_version=0 WHERE agent_task_id=?`},
		{"null_approvals", `UPDATE agent_task_run_states SET pending_sdk_tool_call_ids_json='null' WHERE agent_task_id=?`},
		{"empty_approvals", `UPDATE agent_task_run_states SET pending_sdk_tool_call_ids_json='[]' WHERE agent_task_id=?`},
		{"duplicate_approvals", `UPDATE agent_task_run_states SET pending_sdk_tool_call_ids_json='["sdk-corrupt","sdk-corrupt"]' WHERE agent_task_id=?`},
		{"unbound_approval", `UPDATE agent_task_run_states SET pending_sdk_tool_call_ids_json='["unbound"]' WHERE agent_task_id=?`},
		{"missing", `DELETE FROM agent_task_run_states WHERE agent_task_id=?`},
		{"call_status", `UPDATE agent_tool_calls SET status='cancelled' WHERE agent_tool_call_id IN
			(SELECT b.agent_tool_call_id FROM agent_task_tool_calls b JOIN agent_task_attempts a ON a.agent_task_attempt_id=b.agent_task_attempt_id WHERE a.agent_task_id=?)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(filepath.Join(t.TempDir(), "queue.db"), loadBackgroundTaskRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			bad, call, command := queuedBackgroundResumeForTest(t, store)
			if _, err := store.db.Exec(test.statement, bad.Task.AgentTaskID); err != nil {
				t.Fatal(err)
			}
			healthy := createBackgroundTaskForTest(t, store)
			claim, err := store.ClaimAgentTask(ctx, command)
			if err != nil || claim != nil {
				t.Fatalf("corrupt head must be isolated without dispatch: %+v %v", claim, err)
			}
			failed, err := store.GetAgentTask(ctx, bad.Task.AgentTaskID)
			if err != nil || failed.Status != "failed" || failed.FailureCode == nil || *failed.FailureCode != "AGENT_RUN_STATE_CORRUPT" || failed.RetryBlockedReason == "" || failed.AttemptCount != 1 {
				t.Fatalf("corrupt resume did not retain a replay barrier: %+v %v", failed, err)
			}
			if _, err := store.RetryAgentTask(ctx, RetryAgentTaskCommand{AgentTaskID: bad.Task.AgentTaskID}); !isDomainErrorCode(err, "SDK_TOOL_REPLAY_RISK") {
				t.Fatalf("corrupt recovery was restarted from scratch: %v", err)
			}
			if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: toolConfigurationHashForTest(t, store, call.ToolID)}); err == nil {
				t.Fatal("a quarantined task executed its old approved tool")
			}
			next, err := store.ClaimAgentTask(ctx, command)
			if err != nil || next == nil || next.Task.AgentTaskID != healthy.AgentTaskID || next.Resume != nil {
				t.Fatalf("healthy task stayed behind the corrupt queue head: %+v %v", next, err)
			}
			var count int
			var payload string
			if err := store.db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(payload_json),'') FROM events
				WHERE project_id=? AND event_type='agent_task.failed'`, bad.Task.ProjectID).Scan(&count, &payload); err != nil || count != 1 || strings.Contains(payload, "private_marker") {
				t.Fatalf("failure audit is missing, repeated, or contains private checkpoint: %d %q %v", count, payload, err)
			}
			var checkpoints int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_task_run_states WHERE agent_task_id=?`, bad.Task.AgentTaskID).Scan(&checkpoints); err != nil {
				t.Fatal(err)
			}
			if (test.name == "missing" && checkpoints != 0) || (test.name != "missing" && checkpoints != 1) {
				t.Fatalf("checkpoint was fabricated or discarded: %d", checkpoints)
			}
		})
	}
}

func TestBackgroundResumeFailureAuditIsAtomic(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "queue.db"), loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	bad, call, command := queuedBackgroundResumeForTest(t, store)
	if _, err := store.db.Exec(`UPDATE agent_task_run_states SET state_hash='broken' WHERE agent_task_id=?`, bad.Task.AgentTaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_resume_audit BEFORE INSERT ON events
		WHEN NEW.event_type='agent_task.failed' BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if claim, err := store.ClaimAgentTask(ctx, command); err == nil || claim != nil {
		t.Fatalf("failed audit was reported committed: %+v %v", claim, err)
	}
	task, err := store.GetAgentTask(ctx, bad.Task.AgentTaskID)
	if err != nil || task.Status != "queued" {
		t.Fatalf("task changed despite rollback: %+v %v", task, err)
	}
	tool, err := store.GetAgentToolCall(ctx, call.AgentToolCallID)
	if err != nil || tool.Status != "approved" {
		t.Fatalf("tool cancellation escaped rollback: %+v %v", tool, err)
	}
}

func queuedBackgroundResumeForTest(t *testing.T, store *Store) (*AgentTaskClaim, AgentToolCall, ClaimAgentTaskCommand) {
	t.Helper()
	ctx := context.Background()
	createBackgroundTaskForTest(t, store)
	command := ClaimAgentTaskCommand{WorkerID: "worker", ProviderID: "sdk", ModelID: "fixture", LeaseSeconds: 60}
	claim, err := store.ClaimAgentTask(ctx, command)
	if err != nil || claim == nil {
		t.Fatalf("initial claim: %+v %v", claim, err)
	}
	call := backgroundApprovalForTest(t, store, *claim, "sdk-corrupt")
	if _, err := store.PauseAgentTaskForApproval(ctx, PauseAgentTaskForApprovalCommand{
		AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken,
		PauseAgentTurnForApprovalCommand: PauseAgentTurnForApprovalCommand{SchemaVersion: "1.16", RunState: json.RawMessage(`{"$schemaVersion":"1.16","private_marker":"retained-for-audit"}`), PendingSDKToolCallIDs: []string{call.SDKToolCallID}},
	}); err != nil {
		t.Fatal(err)
	}
	resolveBackgroundApprovalForTest(t, store, call, "approve")
	if _, err := store.db.Exec(`UPDATE agent_tasks SET queued_at=? WHERE agent_task_id=?`, formatTime(store.now().Add(-time.Hour)), claim.Task.AgentTaskID); err != nil {
		t.Fatal(err)
	}
	return claim, call, command
}
