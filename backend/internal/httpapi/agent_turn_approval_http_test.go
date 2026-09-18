package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

type approvalCheckpointAgent struct {
	configurationHash string
	store             *businessruntime.Store
	paused            chan businessruntime.AgentToolCall
	resumed           chan agentcontract.AgentInput
	mu                sync.Mutex
	calls             int
}

func (*approvalCheckpointAgent) GenericChatAvailable() bool { return true }

func (a *approvalCheckpointAgent) StreamTurn(
	ctx context.Context,
	input agentcontract.AgentInput,
	idempotencyKey string,
	agentTurnID string,
	handle shell.AgentTurnStreamHandler,
) (bool, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	if len(input.SDKRunState) == 0 {
		call, err := a.store.BeginAgentToolCall(ctx, businessruntime.BeginAgentToolCallCommand{
			ProjectID: input.ProjectID, ConversationID: input.ConversationID,
			AgentTurnID: agentTurnID, SDKToolCallID: "sdk-write-http-1",
			ToolID: "mcp:fixture/save_fact", Arguments: json.RawMessage(`{"value":"durable"}`),
			ConfigurationHash: a.configurationHash,
		})
		if err != nil {
			return true, err
		}
		a.paused <- call
		return true, handle(agentTurnTestEvent(
			input, agentTurnID, "agent.turn.waiting_approval", false,
			map[string]any{
				"run_state": map[string]any{
					"$schemaVersion": "1.16", "private_marker": "never-project-public",
				},
				"run_state_schema":          "1.16",
				"pending_sdk_tool_call_ids": []string{call.SDKToolCallID},
				"observation": map[string]any{
					"schema_version": "agent_turn_observation.v1", "provider_id": "sdk-test",
				},
			},
		))
	}
	if len(input.ApprovalDecisions) != 1 ||
		input.ApprovalDecisions[0].SDKToolCallID != "sdk-write-http-1" ||
		input.ApprovalDecisions[0].Action != "approve" {
		return true, fmt.Errorf("unexpected approval resume input: %+v", input.ApprovalDecisions)
	}
	a.resumed <- input
	calls, err := a.store.ListAgentToolCalls(ctx, input.ProjectID)
	if err != nil || len(calls) != 1 {
		return true, fmt.Errorf("load resumed tool call: count=%d error=%v", len(calls), err)
	}
	call, err := a.store.StartAgentToolCall(ctx, businessruntime.StartAgentToolCallCommand{
		AgentToolCallID: calls[0].AgentToolCallID, ExpectedSDKToolCallID: calls[0].SDKToolCallID,
		ConfigurationHash: a.configurationHash,
	})
	if err != nil {
		return true, err
	}
	if _, err := a.store.CompleteAgentToolCall(ctx, businessruntime.CompleteAgentToolCallCommand{
		AgentToolCallID: call.AgentToolCallID, Result: json.RawMessage(`{"ok":true}`), ResultSizeBytes: 11,
	}); err != nil {
		return true, err
	}
	exchange, err := a.store.CreateMessageExchange(ctx, businessruntime.CreateMessageExchangeCommand{
		CommandMeta: businessruntime.CommandMeta{
			Scope: input.ProjectID, CommandType: "commit_sdk_agent_turn",
			IdempotencyKey: idempotencyKey, RequestHash: "approval-http-" + agentTurnID,
		},
		ConversationID: input.ConversationID, Request: input.Request,
		Decision: agentcontract.AgentDecision{
			Intent: "chat", Reply: "approved tool completed", Confidence: 1,
		},
	})
	if err != nil {
		return true, err
	}
	return true, handle(agentTurnTestEvent(
		input, agentTurnID, "agent.turn.committed", true,
		map[string]any{"exchange": map[string]any{"data": exchange}},
	))
}

func TestAgentToolApprovalHTTPResumesExactDurableSDKState(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	toolRegistry, err := agenttool.Compile(agenttool.Config{
		SchemaVersion: agenttool.SchemaVersion,
		MCPServers: []agenttool.MCPServerConfig{{
			ID: "fixture", Description: "Fixture MCP", Transport: "streamable_http",
			URL: "http://127.0.0.1:9321/mcp", Enabled: true, MaxResultBytes: 4096,
			AllowedTools: []agenttool.MCPToolConfig{{
				Name: "save_fact", Description: "Save a fact", Access: agenttool.AccessWrite,
				Approval: agenttool.ApprovalAlways,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(toolRegistry)
	descriptor, _ := toolRegistry.Get("mcp:fixture/save_fact")
	agent := &approvalCheckpointAgent{
		configurationHash: descriptor.ConfigurationHash,
		store:             store, paused: make(chan businessruntime.AgentToolCall, 1),
		resumed: make(chan agentcontract.AgentInput, 1),
	}
	server := NewWithRuntime(
		shell.NewWithAgentService(registry, agent), store,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	startTestAgentTurnManager(t, server)
	project, err := store.CreateProject(context.Background(), "Durable approval HTTP")
	if err != nil {
		t.Fatal(err)
	}
	response := performJSONWithHeaders(
		t, server.Handler(), http.MethodPost,
		"/api/v1/conversations/"+project.PrimaryConversationID+"/messages",
		map[string]any{"content": "write after approval", "attachment_refs": []any{}, "client_context": map[string]any{}},
		map[string]string{"Idempotency-Key": "81111111-1111-4111-8111-111111111111"},
		http.StatusAccepted,
	)
	turnID := stringAt(t, objectAt(t, response, "data"), "agent_turn_id")
	var call businessruntime.AgentToolCall
	select {
	case call = <-agent.paused:
	case <-time.After(3 * time.Second):
		t.Fatal("Agent did not create approval checkpoint")
	}
	waitForAgentTurnStatus(t, store, turnID, "waiting_approval")

	projection := performJSON(
		t, server.Handler(), http.MethodGet,
		"/api/v1/projects/"+project.ProjectID+"/workspace-projection", nil, http.StatusOK,
	)
	snapshot := objectAt(t, objectAt(t, projection, "data"), "snapshot")
	toolCalls := arrayAt(t, snapshot, "agent_tool_calls")
	if len(toolCalls) != 1 || stringAt(t, toolCalls[0].(map[string]any), "status") != "pending_approval" {
		t.Fatalf("projected tool calls = %#v", toolCalls)
	}
	encodedProjection, _ := json.Marshal(projection)
	if strings.Contains(string(encodedProjection), "never-project-public") {
		t.Fatalf("workspace projection leaked SDK RunState: %s", encodedProjection)
	}
	events, err := store.ListProjectEvents(context.Background(), project.ProjectID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	encodedEvents, _ := json.Marshal(events)
	if strings.Contains(string(encodedEvents), "never-project-public") {
		t.Fatalf("project events leaked SDK RunState: %s", encodedEvents)
	}

	performJSONWithHeaders(
		t, server.Handler(), http.MethodPost,
		"/api/v1/agent-tool-approvals/"+call.Approval.AgentToolApprovalID+"/resolutions",
		map[string]any{
			"expected_version": call.Approval.Version, "subject_snapshot_hash": call.Approval.SubjectSnapshotHash,
			"action": "approve",
		},
		map[string]string{"Idempotency-Key": "82222222-2222-4222-8222-222222222222"},
		http.StatusOK,
	)
	committed := waitForAgentTurnStatus(t, store, turnID, "committed")
	if committed.AgentMessageID == nil {
		t.Fatalf("committed turn = %+v", committed)
	}
	var resumed agentcontract.AgentInput
	select {
	case resumed = <-agent.resumed:
	case <-time.After(3 * time.Second):
		t.Fatal("Agent did not receive approval resume input")
	}
	if !strings.Contains(string(resumed.SDKRunState), "never-project-public") ||
		len(resumed.ApprovalDecisions) != 1 || resumed.ApprovalDecisions[0].SDKToolCallID != call.SDKToolCallID {
		t.Fatalf("resumed input = %+v", resumed)
	}
	completedCall, err := store.GetAgentToolCall(context.Background(), call.AgentToolCallID)
	if err != nil || completedCall.Status != "completed" {
		t.Fatalf("completed tool call = %+v, error = %v", completedCall, err)
	}
	resumeContext, err := store.GetAgentTurnResumeContext(context.Background(), turnID)
	if err != nil || len(resumeContext.RunState) != 0 {
		t.Fatalf("committed checkpoint = %+v, error = %v", resumeContext, err)
	}
	agent.mu.Lock()
	callCount := agent.calls
	agent.mu.Unlock()
	if callCount != 2 {
		t.Fatalf("Agent stream calls = %d, want pause plus resume", callCount)
	}
}
