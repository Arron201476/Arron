package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestProjectEventStreamRejectsInvalidCursor(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("runtime.Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(context.Background(), "SSE Cursor")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()

	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/projects/"+project.ProjectID+"/events/stream?after_seq=invalid",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), "EVENT_CURSOR_INVALID") {
		t.Fatalf("invalid cursor response = %d %s", response.Code, response.Body.String())
	}
}

func TestProjectEventReplayProjectsDurableAgentEnvelope(t *testing.T) {
	registry, _ := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	store, _ := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	defer store.Close()
	project, _ := store.CreateProject(context.Background(), "Agent replay")
	turn, err := store.AcceptAgentTurn(
		context.Background(), project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "hello"}, businessruntime.CommandMeta{
			Scope: project.ProjectID, CommandType: "create_message",
			IdempotencyKey: "11111111-1111-4111-8111-111111111111", RequestHash: "replay",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	cursor, _ := store.CurrentProjectEventSeq(context.Background(), project.ProjectID)
	if _, err := store.ClaimRunnableAgentTurns(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.ListProjectEvents(context.Background(), project.ProjectID, cursor, 10)
	if err != nil || len(replayed.Items) != 1 {
		t.Fatalf("replayed = %+v, error = %v", replayed, err)
	}
	event := replayed.Items[0]
	frame := httptest.NewRecorder()
	if err := writeEventFrame(frame, event.ProjectEventSeq, event); err != nil {
		t.Fatal(err)
	}
	dataLine := strings.Split(strings.Split(frame.Body.String(), "data: ")[1], "\n")[0]
	var envelope map[string]any
	if err := json.Unmarshal([]byte(dataLine), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["schema_version"] != "1.0.0" || envelope["turn_id"] != turn.AgentTurnID || envelope["terminal"] != false {
		t.Fatalf("agent envelope = %#v", envelope)
	}
	if envelope["project_event_seq"] != float64(event.ProjectEventSeq) {
		t.Fatalf("replay cursor missing from envelope: %#v", envelope)
	}
}

func TestProjectEventStreamCursorAheadRequiresSnapshot(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("runtime.Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(context.Background(), "SSE Reset")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()

	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/projects/"+project.ProjectID+"/events/stream?after_seq=0",
		nil,
	)
	request.Header.Set("Last-Event-ID", "99")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK ||
		response.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" ||
		!strings.Contains(body, "id:\nevent: stream.reset_required") ||
		!strings.Contains(body, `"reason":"cursor_ahead"`) ||
		!strings.Contains(body, `"requires_snapshot":true`) {
		t.Fatalf("cursor ahead response = %d headers=%v body=%s", response.Code, response.Header(), body)
	}
}
