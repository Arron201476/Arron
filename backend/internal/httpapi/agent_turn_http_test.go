package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestAsyncAgentTurnReturnsBeforeSlowExecutionAndCommitsThroughDurableEvents(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("runtime.Open() error = %v", err)
	}
	defer store.Close()
	agent := newSlowStreamingAgent(store)
	server := NewWithRuntime(
		shell.NewWithAgentService(registry, agent), store,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	managerContext, stop := context.WithCancel(context.Background())
	defer stop()
	if err := server.StartAgentTurnManager(managerContext); err != nil {
		t.Fatalf("StartAgentTurnManager() error = %v", err)
	}
	project, err := store.CreateProject(context.Background(), "Async SDK")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	startedAt := time.Now()
	response := performJSONWithHeaders(
		t, server.Handler(), http.MethodPost,
		"/api/v1/conversations/"+project.PrimaryConversationID+"/messages",
		map[string]any{"content": "慢速工具请求", "attachment_refs": []any{}, "client_context": map[string]any{}},
		map[string]string{"Idempotency-Key": "11111111-1111-4111-8111-111111111111"},
		http.StatusAccepted,
	)
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("message acceptance blocked for %s", elapsed)
	}
	turnID := stringAt(t, objectAt(t, response, "data"), "agent_turn_id")
	select {
	case startedTurnID := <-agent.started:
		if startedTurnID != turnID {
			t.Fatalf("started turn = %q, want %q", startedTurnID, turnID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("background Agent turn did not start")
	}
	messages, err := store.ListMessages(context.Background(), project.PrimaryConversationID)
	if err != nil || len(messages) != 0 {
		t.Fatalf("messages before release = %+v, error = %v", messages, err)
	}
	projection := performJSON(
		t, server.Handler(), http.MethodGet,
		"/api/v1/projects/"+project.ProjectID+"/workspace-projection", nil, http.StatusOK,
	)
	snapshot := objectAt(t, objectAt(t, projection, "data"), "snapshot")
	turns := arrayAt(t, snapshot, "agent_turns")
	if len(turns) != 1 || stringAt(t, objectAt(t, turns[0].(map[string]any), "request"), "content") != "慢速工具请求" {
		t.Fatalf("refresh projection lost active turn: %#v", turns)
	}
	close(agent.release)
	committed := waitForAgentTurnStatus(t, store, turnID, "committed")
	if committed.Observation.ProviderID != "openai-compatible-responses" ||
		committed.Observation.Usage["total_tokens"] != 21 ||
		len(committed.Observation.TraceRefs) != 1 {
		t.Fatalf("committed observation = %+v", committed.Observation)
	}
	messages, err = store.ListMessages(context.Background(), project.PrimaryConversationID)
	if err != nil || len(messages) != 2 || messages[1].Content != "SDK 流式提交完成" {
		t.Fatalf("messages after commit = %+v, error = %v", messages, err)
	}
	events, err := store.ListProjectEvents(context.Background(), project.ProjectID, 0, 100)
	if err != nil {
		t.Fatalf("ListProjectEvents() error = %v", err)
	}
	seen := map[string]bool{}
	terminal := 0
	for _, event := range events.Items {
		if event.SubjectID != turnID {
			continue
		}
		seen[event.EventType] = true
		if event.EventType == "agent.turn.committed" || event.EventType == "agent.turn.failed" || event.EventType == "agent.turn.cancelled" {
			terminal++
		}
	}
	for _, required := range []string{
		"agent.turn.started", "agent.tool.started", "agent.tool.completed",
		"agent.output.delta", "agent.turn.committed",
	} {
		if !seen[required] {
			t.Fatalf("missing %s in events: %+v", required, seen)
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal events = %d, want 1", terminal)
	}
}

func TestAsyncAgentTurnSidecarDisconnectBecomesDurableFailure(t *testing.T) {
	registry, _ := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	store, _ := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	defer store.Close()
	agent := failingStreamingAgent{}
	server := NewWithRuntime(
		shell.NewWithAgentService(registry, agent), store,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	managerContext, stop := context.WithCancel(context.Background())
	defer stop()
	if err := server.StartAgentTurnManager(managerContext); err != nil {
		t.Fatal(err)
	}
	project, _ := store.CreateProject(context.Background(), "Sidecar restart")
	body := map[string]any{"content": "disconnect", "attachment_refs": []any{}, "client_context": map[string]any{}}
	headers := map[string]string{"Idempotency-Key": "66666666-6666-4666-8666-666666666666"}
	response := performJSONWithHeaders(
		t, server.Handler(), http.MethodPost,
		"/api/v1/conversations/"+project.PrimaryConversationID+"/messages",
		body, headers, http.StatusAccepted,
	)
	turnID := stringAt(t, objectAt(t, response, "data"), "agent_turn_id")
	failed := waitForAgentTurnStatus(t, store, turnID, "failed")
	if failed.ErrorCode == nil || *failed.ErrorCode != "AGENT_EXECUTION_FAILED" ||
		failed.ErrorMessage == nil || *failed.ErrorMessage != "Agent 暂时无法完成本次请求，请重试。" {
		t.Fatalf("failed turn exposed unsafe error: %+v", failed)
	}
	if failed.Observation.FailureStage != "sidecar_transport" {
		t.Fatalf("failure stage = %q", failed.Observation.FailureStage)
	}
	repeated := performJSONWithHeaders(
		t, server.Handler(), http.MethodPost,
		"/api/v1/conversations/"+project.PrimaryConversationID+"/messages",
		body, headers, http.StatusAccepted,
	)
	if repeatedTurn := objectAt(t, repeated, "data"); stringAt(t, repeatedTurn, "agent_turn_id") != turnID || stringAt(t, repeatedTurn, "status") != "failed" {
		t.Fatalf("idempotent failed turn = %#v", repeatedTurn)
	}
	terminal := 0
	events, _ := store.ListProjectEvents(context.Background(), project.ProjectID, 0, 100)
	for _, event := range events.Items {
		if event.SubjectID == turnID && (event.EventType == "agent.turn.committed" || event.EventType == "agent.turn.failed" || event.EventType == "agent.turn.cancelled") {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal events after disconnect = %d", terminal)
	}
}

func TestAsyncAgentTurnSerializesSameConversationAndCancellationIsIdempotent(t *testing.T) {
	registry, _ := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	store, _ := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	defer store.Close()
	agent := newSlowStreamingAgent(store)
	server := NewWithRuntime(
		shell.NewWithAgentService(registry, agent), store,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	managerContext, stop := context.WithCancel(context.Background())
	defer stop()
	if err := server.StartAgentTurnManager(managerContext); err != nil {
		t.Fatal(err)
	}
	project, _ := store.CreateProject(context.Background(), "Serialized SDK")
	send := func(key, content string) string {
		response := performJSONWithHeaders(
			t, server.Handler(), http.MethodPost,
			"/api/v1/conversations/"+project.PrimaryConversationID+"/messages",
			map[string]any{"content": content, "attachment_refs": []any{}, "client_context": map[string]any{}},
			map[string]string{"Idempotency-Key": key}, http.StatusAccepted,
		)
		return stringAt(t, objectAt(t, response, "data"), "agent_turn_id")
	}
	firstID := send("22222222-2222-4222-8222-222222222222", "first")
	select {
	case <-agent.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not start")
	}
	secondID := send("33333333-3333-4333-8333-333333333333", "second")
	time.Sleep(350 * time.Millisecond)
	second, err := store.GetAgentTurn(context.Background(), secondID)
	if err != nil || second.Status != "accepted" {
		t.Fatalf("second turn = %+v, error = %v", second, err)
	}
	cancelPath := "/api/v1/agent-turns/" + firstID + "/cancel"
	firstCancel := performJSONWithHeaders(
		t, server.Handler(), http.MethodPost, cancelPath, map[string]any{},
		map[string]string{"Idempotency-Key": "44444444-4444-4444-8444-444444444444"}, http.StatusOK,
	)
	if accepted, _ := objectAt(t, firstCancel, "data")["accepted"].(bool); !accepted {
		t.Fatalf("first cancel = %#v", firstCancel)
	}
	waitForAgentTurnStatus(t, store, firstID, "cancelled")
	select {
	case startedTurnID := <-agent.started:
		if startedTurnID != secondID {
			t.Fatalf("next started turn = %q, want %q", startedTurnID, secondID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second turn did not start after cancellation")
	}
	repeated := performJSONWithHeaders(
		t, server.Handler(), http.MethodPost, cancelPath, map[string]any{},
		map[string]string{"Idempotency-Key": "55555555-5555-4555-8555-555555555555"}, http.StatusOK,
	)
	if accepted, _ := objectAt(t, repeated, "data")["accepted"].(bool); accepted {
		t.Fatalf("terminal cancel unexpectedly accepted: %#v", repeated)
	}
}

func TestAgentTurnManagerSkipsExecutionCancelledBetweenClaimAndStart(t *testing.T) {
	registry, _ := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	store, _ := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	defer store.Close()
	project, _ := store.CreateProject(context.Background(), "Pre-start cancellation")
	turn, err := store.AcceptAgentTurn(
		context.Background(), project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "cancel before start"},
		businessruntime.CommandMeta{
			Scope: project.ProjectID, CommandType: "create_message",
			IdempotencyKey: "88888888-8888-4888-8888-888888888888", RequestHash: "cancel-race",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimRunnableAgentTurns(context.Background(), 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed = %+v, error = %v", claimed, err)
	}
	if _, err := store.CancelAgentTurn(context.Background(), turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	agent := &unexpectedStreamingAgent{called: make(chan struct{}, 1)}
	manager := newAgentTurnManager(
		store, shell.NewWithAgentService(registry, agent),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	ctx, cancel := context.WithCancel(context.Background())
	manager.sem <- struct{}{}
	manager.active[turn.AgentTurnID] = cancel
	done := make(chan struct{})
	go func() {
		manager.execute(ctx, claimed[0], cancel)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled turn did not finish")
	}
	select {
	case <-agent.called:
		t.Fatal("Agent was called after durable pre-start cancellation")
	default:
	}
	waitForAgentTurnStatus(t, store, turn.AgentTurnID, "cancelled")
}

type slowStreamingAgent struct {
	store   *businessruntime.Store
	started chan string
	release chan struct{}
	mu      sync.Mutex
	active  int
	maxSeen int
}

type failingStreamingAgent struct{}

type unexpectedStreamingAgent struct {
	called chan struct{}
}

func (a *unexpectedStreamingAgent) GenericChatAvailable() bool { return true }

func (a *unexpectedStreamingAgent) Decide(
	context.Context, agentcontract.AgentInput,
) (agentcontract.AgentDecision, error) {
	return agentcontract.AgentDecision{}, fmt.Errorf("stream path required")
}

func (a *unexpectedStreamingAgent) StreamTurn(
	context.Context, agentcontract.AgentInput, string, string, shell.AgentTurnStreamHandler,
) (bool, error) {
	a.called <- struct{}{}
	return true, fmt.Errorf("unexpected execution")
}

func (failingStreamingAgent) GenericChatAvailable() bool { return true }

func (failingStreamingAgent) Decide(
	context.Context, agentcontract.AgentInput,
) (agentcontract.AgentDecision, error) {
	return agentcontract.AgentDecision{}, fmt.Errorf("stream path required")
}

func (failingStreamingAgent) StreamTurn(
	_ context.Context,
	input agentcontract.AgentInput,
	_ string,
	agentTurnID string,
	handle shell.AgentTurnStreamHandler,
) (bool, error) {
	if err := handle(agentTurnTestEvent(
		input, agentTurnID, "agent.tool.started", false,
		map[string]any{"tool_name": "unstable_tool"},
	)); err != nil {
		return true, err
	}
	return true, io.ErrUnexpectedEOF
}

func newSlowStreamingAgent(store *businessruntime.Store) *slowStreamingAgent {
	return &slowStreamingAgent{
		store: store, started: make(chan string, 8), release: make(chan struct{}),
	}
}

func (a *slowStreamingAgent) GenericChatAvailable() bool { return true }

func (a *slowStreamingAgent) Decide(
	context.Context, agentcontract.AgentInput,
) (agentcontract.AgentDecision, error) {
	return agentcontract.AgentDecision{}, fmt.Errorf("stream path required")
}

func (a *slowStreamingAgent) StreamTurn(
	ctx context.Context,
	input agentcontract.AgentInput,
	idempotencyKey string,
	agentTurnID string,
	handle shell.AgentTurnStreamHandler,
) (bool, error) {
	a.mu.Lock()
	a.active++
	if a.active > a.maxSeen {
		a.maxSeen = a.active
	}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.active--
		a.mu.Unlock()
	}()
	a.started <- agentTurnID
	if err := handle(agentTurnTestEvent(input, agentTurnID, "agent.tool.started", false, map[string]any{"tool_name": "slow_tool"})); err != nil {
		return true, err
	}
	select {
	case <-ctx.Done():
		return true, ctx.Err()
	case <-a.release:
	}
	exchange, err := a.store.CreateMessageExchange(ctx, businessruntime.CreateMessageExchangeCommand{
		CommandMeta: businessruntime.CommandMeta{
			Scope: input.ProjectID, CommandType: "commit_sdk_agent_turn",
			IdempotencyKey: idempotencyKey, RequestHash: "stream-test-" + agentTurnID,
		},
		ConversationID: input.ConversationID,
		Request:        input.Request,
		Decision: agentcontract.AgentDecision{
			Intent: "chat", Reply: "SDK 流式提交完成", Confidence: 1,
		},
	})
	if err != nil {
		return true, err
	}
	if err := handle(agentTurnTestEvent(input, agentTurnID, "agent.tool.completed", false, map[string]any{})); err != nil {
		return true, err
	}
	return true, handle(agentTurnTestEvent(
		input, agentTurnID, "agent.turn.committed", true,
		map[string]any{
			"exchange": map[string]any{"data": exchange},
			"observation": map[string]any{
				"schema_version": "agent_turn_observation.v1",
				"provider_id":    "openai-compatible-responses", "model_id": "gpt-5.6",
				"release_id": "test", "trace_refs": []string{"trace_test"},
				"response_ids": []string{"resp_test"}, "request_ids": []string{"req_test"},
				"usage":      map[string]int64{"total_tokens": 21},
				"latency_ms": map[string]int64{"total": 30},
			},
		},
	))
}

func agentTurnTestEvent(
	input agentcontract.AgentInput, turnID, eventType string, terminal bool, payload map[string]any,
) shell.AgentTurnEvent {
	encoded, _ := json.Marshal(payload)
	return shell.AgentTurnEvent{
		SchemaVersion: "1.0.0", EventID: "sidecar_" + eventType,
		EventType: eventType, ProjectID: input.ProjectID,
		ConversationID: input.ConversationID, TurnID: turnID,
		Terminal: terminal, Payload: encoded,
	}
}

func waitForAgentTurnStatus(
	t *testing.T, store *businessruntime.Store, turnID, want string,
) businessruntime.AgentTurn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		turn, err := store.GetAgentTurn(context.Background(), turnID)
		if err == nil && turn.Status == want {
			return turn
		}
		time.Sleep(20 * time.Millisecond)
	}
	turn, err := store.GetAgentTurn(context.Background(), turnID)
	t.Fatalf("Agent turn status = %q, want %q; error = %v", turn.Status, want, err)
	return businessruntime.AgentTurn{}
}
