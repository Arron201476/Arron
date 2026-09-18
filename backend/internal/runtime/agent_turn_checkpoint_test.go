package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
)

func TestAgentTurnApprovalCheckpointIsPrivateDurableAndResumable(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, err := store.CreateProject(ctx, "durable approval")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "write safely"}, CommandMeta{
			Scope: project.ProjectID, CommandType: "create_message",
			IdempotencyKey: "71111111-1111-4111-8111-111111111111", RequestHash: "turn",
		})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimRunnableAgentTurns(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %+v, error = %v", claimed, err)
	}
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		AgentTurnID: turn.AgentTurnID, SDKToolCallID: "sdk-write-durable",
		ToolID: "mcp:fixture/save_fact", Arguments: json.RawMessage(`{"value":"safe"}`),
		ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAgentTurnCommit(ctx, turn.AgentTurnID); err == nil {
		t.Fatal("terminal commit bypassed pending SDK approval")
	} else {
		assertDomainCode(t, err, "AGENT_TOOL_APPROVAL_REQUIRED")
	}
	runState, err := json.Marshal(map[string]any{"$schemaVersion": "1.16", "private_marker": "must-not-broadcast", "binary_fixture": strings.Repeat("A", 14<<20)})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := store.PauseAgentTurnForApproval(ctx, turn.AgentTurnID, PauseAgentTurnForApprovalCommand{
		SchemaVersion: "1.16", RunState: runState,
		PendingSDKToolCallIDs: []string{call.SDKToolCallID},
	})
	if err != nil || waiting.Status != "waiting_approval" {
		t.Fatalf("pause = %+v, error = %v", waiting, err)
	}
	encodedTurn, _ := json.Marshal(waiting)
	if strings.Contains(string(encodedTurn), "private_marker") {
		t.Fatalf("public Agent turn leaked SDK state: %s", encodedTurn)
	}
	var leakedEvents int
	if err := store.db.QueryRow(`
		SELECT COUNT(*) FROM events
		WHERE subject_id = ? AND payload_json LIKE '%private_marker%'`, turn.AgentTurnID,
	).Scan(&leakedEvents); err != nil || leakedEvents != 0 {
		t.Fatalf("checkpoint leaked into project events: count=%d error=%v", leakedEvents, err)
	}
	queuedBehind, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "later"}, CommandMeta{
			Scope: project.ProjectID, CommandType: "create_message",
			IdempotencyKey: "72222222-2222-4222-8222-222222222222", RequestHash: "later",
		})
	if err != nil {
		t.Fatal(err)
	}
	if next, err := store.ClaimRunnableAgentTurns(ctx, 2); err != nil || len(next) != 0 {
		t.Fatalf("later turn bypassed approval boundary: %+v, error=%v", next, err)
	}

	resolved, err := store.ResolveAgentToolApproval(ctx, ResolveAgentToolApprovalCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "resolve_agent_tool_approval",
			IdempotencyKey: "73333333-3333-4333-8333-333333333333", RequestHash: "approve",
		},
		AgentToolApprovalID: call.Approval.AgentToolApprovalID,
		ExpectedVersion:     call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash,
		Action: "approve", ActorRef: "test_user",
	})
	if err != nil || resolved.Status != "approved" {
		t.Fatalf("resolve = %+v, error = %v", resolved, err)
	}
	resume, err := store.GetAgentTurnResumeContext(ctx, turn.AgentTurnID)
	if err != nil || string(resume.RunState) != string(runState) || len(resume.ApprovalDecisions) != 1 ||
		resume.ApprovalDecisions[0].SDKToolCallID != call.SDKToolCallID ||
		resume.ApprovalDecisions[0].Action != "approve" {
		t.Fatalf("resume checkpoint bytes = %d, error = %v", len(resume.RunState), err)
	}
	next, err := store.ClaimRunnableAgentTurns(ctx, 2)
	if err != nil || len(next) != 1 || next[0].AgentTurnID != turn.AgentTurnID {
		t.Fatalf("resumed claim = %+v, error = %v", next, err)
	}
	if _, err := store.FailAgentTurn(ctx, turn.AgentTurnID, "TEST_DONE", "closed"); err != nil {
		t.Fatal(err)
	}
	closedCall, err := store.GetAgentToolCall(ctx, call.AgentToolCallID)
	if err != nil || closedCall.Status != "cancelled" || closedCall.Approval == nil || closedCall.Approval.Status != "approved" {
		t.Fatalf("terminal tool call = %+v, error = %v", closedCall, err)
	}
	resume, err = store.GetAgentTurnResumeContext(ctx, turn.AgentTurnID)
	if err != nil || len(resume.RunState) != 0 {
		t.Fatalf("terminal turn retained SDK state: %+v, error = %v", resume, err)
	}
	next, err = store.ClaimRunnableAgentTurns(ctx, 2)
	if err != nil || len(next) != 1 || next[0].AgentTurnID != queuedBehind.AgentTurnID {
		t.Fatalf("later claim = %+v, error = %v", next, err)
	}
}

func TestCancelWaitingAgentTurnCancelsPendingToolApproval(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, err := store.CreateProject(ctx, "cancel approval")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "cancel write"}, CommandMeta{
			Scope: project.ProjectID, CommandType: "create_message",
			IdempotencyKey: "75555555-5555-4555-8555-555555555555", RequestHash: "cancel",
		})
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %+v, error = %v", claimed, err)
	}
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		AgentTurnID: turn.AgentTurnID, SDKToolCallID: "sdk-write-cancel",
		ToolID: "mcp:fixture/save_fact", Arguments: json.RawMessage(`{"value":"cancel"}`),
		ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PauseAgentTurnForApproval(ctx, turn.AgentTurnID, PauseAgentTurnForApprovalCommand{
		SchemaVersion: "1.16", RunState: json.RawMessage(`{"$schemaVersion":"1.16"}`),
		PendingSDKToolCallIDs: []string{call.SDKToolCallID},
	}); err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.CancelAgentTurn(ctx, turn.AgentTurnID)
	if err != nil || !cancelled.Accepted || cancelled.Turn.Status != "cancelled" {
		t.Fatalf("cancel = %+v, error = %v", cancelled, err)
	}
	cancelledCall, err := store.GetAgentToolCall(ctx, call.AgentToolCallID)
	if err != nil || cancelledCall.Status != "cancelled" || cancelledCall.ApprovalStatus != "cancelled" ||
		cancelledCall.Approval == nil || cancelledCall.Approval.Status != "cancelled" {
		t.Fatalf("cancelled tool call = %+v, error = %v", cancelledCall, err)
	}
	resume, err := store.GetAgentTurnResumeContext(ctx, turn.AgentTurnID)
	if err != nil || len(resume.RunState) != 0 {
		t.Fatalf("cancelled turn retained SDK state: %+v, error = %v", resume, err)
	}
}

func TestAgentTurnResumesOnlyAfterEntireApprovalBatchIsResolved(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, err := store.CreateProject(ctx, "approval batch")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "write twice"}, CommandMeta{
			Scope: project.ProjectID, CommandType: "create_message",
			IdempotencyKey: "76666666-6666-4666-8666-666666666666", RequestHash: "batch",
		})
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %+v, error = %v", claimed, err)
	}
	calls := make([]AgentToolCall, 0, 2)
	for index, sdkID := range []string{"sdk-write-batch-1", "sdk-write-batch-2"} {
		call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{
			ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
			AgentTurnID: turn.AgentTurnID, SDKToolCallID: sdkID,
			ToolID: "mcp:fixture/save_fact", Arguments: json.RawMessage(`{"value":` + string(rune('1'+index)) + `}`),
			ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact"),
		})
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	_, err = store.PauseAgentTurnForApproval(ctx, turn.AgentTurnID, PauseAgentTurnForApprovalCommand{
		SchemaVersion: "1.16", RunState: json.RawMessage(`{"$schemaVersion":"1.16"}`),
		PendingSDKToolCallIDs: []string{calls[0].SDKToolCallID},
	})
	assertDomainCode(t, err, "AGENT_RUN_STATE_APPROVAL_MISMATCH")
	if _, err := store.PauseAgentTurnForApproval(ctx, turn.AgentTurnID, PauseAgentTurnForApprovalCommand{
		SchemaVersion: "1.16", RunState: json.RawMessage(`{"$schemaVersion":"1.16"}`),
		PendingSDKToolCallIDs: []string{calls[0].SDKToolCallID, calls[1].SDKToolCallID},
	}); err != nil {
		t.Fatal(err)
	}
	resolve := func(index int, action, idempotencyKey string) {
		t.Helper()
		_, err := store.ResolveAgentToolApproval(ctx, ResolveAgentToolApprovalCommand{
			CommandMeta: CommandMeta{
				Scope: project.ProjectID, CommandType: "resolve_agent_tool_approval",
				IdempotencyKey: idempotencyKey, RequestHash: action,
			},
			AgentToolApprovalID: calls[index].Approval.AgentToolApprovalID,
			ExpectedVersion:     calls[index].Approval.Version,
			SubjectSnapshotHash: calls[index].Approval.SubjectSnapshotHash,
			Action:              action, ActorRef: "test_user",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	resolve(0, "approve", "77777777-7777-4777-8777-777777777777")
	current, err := store.GetAgentTurn(ctx, turn.AgentTurnID)
	if err != nil || current.Status != "waiting_approval" {
		t.Fatalf("partially resolved turn = %+v, error = %v", current, err)
	}
	resolve(1, "reject", "78888888-8888-4888-8888-888888888888")
	current, err = store.GetAgentTurn(ctx, turn.AgentTurnID)
	if err != nil || current.Status != "accepted" {
		t.Fatalf("fully resolved turn = %+v, error = %v", current, err)
	}
	resume, err := store.GetAgentTurnResumeContext(ctx, turn.AgentTurnID)
	if err != nil || len(resume.ApprovalDecisions) != 2 ||
		resume.ApprovalDecisions[0].Action != "approve" || resume.ApprovalDecisions[1].Action != "reject" {
		t.Fatalf("resume decisions = %+v, error = %v", resume.ApprovalDecisions, err)
	}
}

func TestAgentTurnApprovalCheckpointRejectsMismatchedSDKCall(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, _ := store.CreateProject(ctx, "mismatched approval")
	turn, _ := store.AcceptAgentTurn(ctx, project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "unsafe"}, CommandMeta{
			Scope: project.ProjectID, CommandType: "create_message",
			IdempotencyKey: "74444444-4444-4444-8444-444444444444", RequestHash: "mismatch",
		})
	_, _ = store.ClaimRunnableAgentTurns(ctx, 1)
	_, err = store.PauseAgentTurnForApproval(ctx, turn.AgentTurnID, PauseAgentTurnForApprovalCommand{
		SchemaVersion: "1.16", RunState: json.RawMessage(`{"$schemaVersion":"1.16"}`),
		PendingSDKToolCallIDs: []string{"unknown-sdk-call"},
	})
	assertDomainCode(t, err, "AGENT_RUN_STATE_APPROVAL_MISMATCH")
}
