package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
)

func sidecarTestInput(projectID, content string) agentcontract.AgentInput {
	return agentcontract.AgentInput{
		ProjectID: projectID, ConversationID: "conversation_1",
		Request: agentcontract.MessageRequest{Content: content},
	}
}

func TestSidecarStreamValidatesAndForwardsProviderIndependentEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/internal/v1/agent/execute-stream" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("X-Sidecar-Run-ID", "turn_1")
		_, _ = fmt.Fprint(writer, `event: agent.tool.started
data: {"schema_version":"1.0.0","event_id":"one","event_type":"agent.tool.started","project_id":"project_allowed","conversation_id":"conversation_1","turn_id":"turn_1","terminal":false,"payload":{"tool_name":"inspect_project"},"occurred_at":"2026-09-04T00:00:00Z"}

event: agent.turn.committed
data: {"schema_version":"1.0.0","event_id":"two","event_type":"agent.turn.committed","project_id":"project_allowed","conversation_id":"conversation_1","turn_id":"turn_1","terminal":true,"payload":{"exchange":{"data":{"agent_message":{"message_id":"msg_1"}}}},"occurred_at":"2026-09-04T00:00:01Z"}

`)
	}))
	defer server.Close()
	service, err := NewRequiredSidecarAgentService(server.URL, "token", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	handled, err := service.StreamTurn(
		context.Background(), sidecarTestInput("project_allowed", "hello"),
		"11111111-1111-4111-8111-111111111111", "turn_1",
		func(event AgentTurnEvent) error {
			got = append(got, event.EventType)
			return nil
		},
	)
	if err != nil || !handled {
		t.Fatalf("StreamTurn() handled=%v error=%v", handled, err)
	}
	if strings.Join(got, ",") != "agent.tool.started,agent.turn.committed" {
		t.Fatalf("events = %v", got)
	}
}

func TestSidecarStreamCarriesRunStateDecisionsAndAcceptsApprovalPause(t *testing.T) {
	runState := json.RawMessage(`{"$schemaVersion":"1.16","private":"checkpoint"}`)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			RunState          json.RawMessage                           `json:"run_state"`
			ApprovalDecisions []agentcontract.AgentToolApprovalDecision `json:"approval_decisions"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if string(payload.RunState) != string(runState) || len(payload.ApprovalDecisions) != 1 ||
			payload.ApprovalDecisions[0].SDKToolCallID != "sdk-write-1" ||
			payload.ApprovalDecisions[0].Action != "approve" {
			t.Fatalf("sidecar payload = %+v", payload)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("X-Sidecar-Run-ID", "turn_approval")
		_, _ = fmt.Fprint(writer, `event: agent.turn.waiting_approval
data: {"schema_version":"1.0.0","event_id":"pause","event_type":"agent.turn.waiting_approval","project_id":"project_allowed","conversation_id":"conversation_1","turn_id":"turn_approval","terminal":false,"payload":{"run_state":{"$schemaVersion":"1.16"},"run_state_schema":"1.16","pending_sdk_tool_call_ids":["sdk-write-2"]},"occurred_at":"2026-09-04T00:00:00Z"}

`)
	}))
	defer server.Close()
	service, err := NewRequiredSidecarAgentService(server.URL, "token", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	input := sidecarTestInput("project_allowed", "resume")
	input.SDKRunState = runState
	input.ApprovalDecisions = []agentcontract.AgentToolApprovalDecision{{
		SDKToolCallID: "sdk-write-1", Action: "approve",
	}}
	var got AgentTurnEvent
	handled, err := service.StreamTurn(
		context.Background(), input,
		"11111111-1111-4111-8111-111111111111", "turn_approval",
		func(event AgentTurnEvent) error { got = event; return nil },
	)
	if err != nil || !handled || got.EventType != "agent.turn.waiting_approval" || got.Terminal {
		t.Fatalf("StreamTurn() handled=%v event=%+v error=%v", handled, got, err)
	}
}

func TestSidecarStreamRejectsMissingTerminalEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, `event: agent.updated
data: {"schema_version":"1.0.0","event_id":"one","event_type":"agent.updated","project_id":"project_allowed","conversation_id":"conversation_1","turn_id":"turn_1","terminal":false,"payload":{},"occurred_at":"2026-09-04T00:00:00Z"}

`)
	}))
	defer server.Close()
	service, _ := NewRequiredSidecarAgentService(server.URL, "token", 2*time.Second)
	_, err := service.StreamTurn(
		context.Background(), sidecarTestInput("project_allowed", "hello"),
		"11111111-1111-4111-8111-111111111111", "turn_1", nil,
	)
	if err == nil || !strings.Contains(err.Error(), "without a final") {
		t.Fatalf("error = %v", err)
	}
}

func TestSidecarStreamSupportsBinaryResourceCheckpointEnvelope(t *testing.T) {
	state, err := json.Marshal(map[string]any{"$schemaVersion": "1.16", "binary_fixture": strings.Repeat("A", 14<<20)})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"run_state": json.RawMessage(state), "run_state_schema": "1.16", "pending_sdk_tool_call_ids": []string{"write"}})
	event, _ := json.Marshal(AgentTurnEvent{SchemaVersion: "1.0.0", EventID: "pause", EventType: "agent.turn.waiting_approval", ProjectID: "project", ConversationID: "conversation_1", TurnID: "turn", Payload: payload, OccurredAt: time.Now().UTC().Format(time.RFC3339)})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(writer, "event: agent.turn.waiting_approval\ndata: %s\n\n", event)
	}))
	defer server.Close()
	service, err := NewRequiredSidecarAgentService(server.URL, "token", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var received AgentTurnEvent
	handled, err := service.StreamTurn(context.Background(), sidecarTestInput("project", "read media"), "11111111-1111-4111-8111-111111111111", "turn", func(item AgentTurnEvent) error { received = item; return nil })
	if err != nil || !handled || string(received.Payload) != string(payload) {
		t.Fatalf("large private envelope handled=%v bytes=%d err=%v", handled, len(received.Payload), err)
	}
}

func TestSidecarStreamRejectsDataAfterTerminalEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, `event: agent.turn.cancelled
data: {"schema_version":"1.0.0","event_id":"one","event_type":"agent.turn.cancelled","project_id":"project_allowed","conversation_id":"conversation_1","turn_id":"turn_1","terminal":true,"payload":{},"occurred_at":"2026-09-04T00:00:00Z"}

event: agent.updated
data: {"schema_version":"1.0.0","event_id":"two","event_type":"agent.updated","project_id":"project_allowed","conversation_id":"conversation_1","turn_id":"turn_1","terminal":false,"payload":{},"occurred_at":"2026-09-04T00:00:01Z"}

`)
	}))
	defer server.Close()
	service, _ := NewRequiredSidecarAgentService(server.URL, "token", 2*time.Second)
	_, err := service.StreamTurn(
		context.Background(), sidecarTestInput("project_allowed", "hello"),
		"11111111-1111-4111-8111-111111111111", "turn_1", nil,
	)
	if err == nil || !strings.Contains(err.Error(), "after its terminal") {
		t.Fatalf("error = %v", err)
	}
}
