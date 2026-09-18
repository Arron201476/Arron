package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestMainTurnPauseHTTPAuthorizationCheckpointAndReopen(t *testing.T) {
	database := filepath.Join(t.TempDir(), "main-pause-http.db")
	store, handler := openBackgroundPauseHTTP(t, database)
	defer func() { store.Close() }()
	ctx := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner})
	project, err := store.CreateProject(ctx, "Main pause HTTP")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Original"}, businessruntime.CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/agent-turns/" + turn.AgentTurnID
	body := map[string]any{}
	key := "a5555555-5555-4555-8555-555555555555"
	owner := bearer("pause-owner", key)
	for _, action := range []string{"pause", "resume"} {
		performJSONWithHeaders(t, handler, http.MethodPost, base+"/"+action, body, nil, http.StatusUnauthorized)
		performJSONWithHeaders(t, handler, http.MethodPost, base+"/"+action, body, bearer("pause-viewer", key), http.StatusForbidden)
		performJSONWithHeaders(t, handler, http.MethodPost, base+"/"+action, body, bearer("pause-foreign", key), http.StatusNotFound)
		performJSONWithHeaders(t, handler, http.MethodPost, base+"/"+action, body, map[string]string{"Authorization": "Bearer pause-owner", "Idempotency-Key": ""}, http.StatusBadRequest)
		performJSONWithHeaders(t, handler, http.MethodPost, base+"/"+action, map[string]any{"content": "cannot replace input"}, owner, http.StatusBadRequest)
	}
	assertControl := func(action, status string, headers map[string]string) {
		t.Helper()
		result := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, base+"/"+action, body, headers, http.StatusOK), "data")
		if result["status"] != status || result["agent_turn_id"] != turn.AgentTurnID {
			t.Fatalf("control: %+v", result)
		}
	}
	assertControl("pause", "paused", owner)
	assertControl("resume", "accepted", owner)
	// A delayed response retry must not pause a turn that has since resumed.
	assertControl("pause", "accepted", owner)
	if items, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil || len(items) != 1 {
		t.Fatalf("claim: %+v %v", items, err)
	}
	owner = bearer("pause-owner", "a6666666-6666-4666-8666-666666666666")
	assertControl("pause", "pausing", owner)
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/resume", body, owner, http.StatusConflict)
	manager := newAgentTurnManager(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	state := `{"$schemaVersion":"1.16","private_marker":9007199254740993}`
	event := shell.AgentTurnEvent{EventType: "agent.turn.paused", Payload: json.RawMessage(`{"run_state":` + state + `,"run_state_schema":"1.16","pending_sdk_tool_call_ids":[]}`)}
	if err := manager.persistEvent(ctx, turn, event); err != nil {
		t.Fatal(err)
	}
	if err := manager.persistEvent(ctx, turn, event); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, handler = openBackgroundPauseHTTP(t, database)
	if err := store.RecoverInterruptedAgentTurns(ctx); err != nil {
		t.Fatal(err)
	}
	projection := performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/projects/"+project.ProjectID+"/workspace-projection", nil, owner, http.StatusOK)
	encoded, _ := json.Marshal(projection)
	if strings.Contains(string(encoded), "private_marker") {
		t.Fatal("public checkpoint leak")
	}
	turns := arrayAt(t, objectAt(t, objectAt(t, projection, "data"), "snapshot"), "agent_turns")
	if len(turns) != 1 || turns[0].(map[string]any)["status"] != "paused" {
		t.Fatalf("pause lost after reopen: %+v", turns)
	}
	assertControl("resume", "accepted", owner)
	checkpoint, err := store.GetAgentTurnResumeContext(ctx, turn.AgentTurnID)
	if err != nil || string(checkpoint.RunState) != state {
		t.Fatalf("checkpoint precision or identity changed: %+v %v", checkpoint, err)
	}
}

func TestMainTurnPauseManagerRetriesSignalAndExcludesClosingStream(t *testing.T) {
	registry := capability.NewEmptyRegistry()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "manager-pause.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	project, err := store.CreateProject(ctx, "Manager pause")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Original"}, businessruntime.CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	started, paused, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var signals, executions atomic.Int32
	state := `{"$schemaVersion":"1.16","private_marker":9007199254740993}`
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-service" {
			t.Error("missing internal authentication")
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/internal/v1/agent/runs/"+turn.AgentTurnID+"/pause" {
			count := signals.Add(1)
			if count == 2 {
				close(paused)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"run_id": turn.AgentTurnID, "accepted": count >= 2})
			return
		}
		if r.URL.Path != "/internal/v1/agent/execute-stream" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		var input struct {
			RunState    json.RawMessage              `json:"run_state"`
			AgentTurnID string                       `json:"agent_turn_id"`
			Request     agentcontract.MessageRequest `json:"request"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if input.AgentTurnID != turn.AgentTurnID || input.Request.Content != "Original" {
			t.Error("changed execution identity or original input")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Sidecar-Run-ID", turn.AgentTurnID)
		emit := func(kind string, terminal bool, payload json.RawMessage) {
			encoded, _ := json.Marshal(shell.AgentTurnEvent{SchemaVersion: "1.0.0", EventID: kind, EventType: kind, ProjectID: project.ProjectID, ConversationID: turn.ConversationID, TurnID: turn.AgentTurnID, Terminal: terminal, Payload: payload})
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
			w.(http.Flusher).Flush()
		}
		if executions.Add(1) == 1 {
			close(started)
			select {
			case <-paused:
			case <-ctx.Done():
				return
			}
			emit("agent.turn.paused", false, json.RawMessage(`{"run_state":`+state+`,"run_state_schema":"1.16","pending_sdk_tool_call_ids":[]}`))
			select {
			case <-release:
			case <-ctx.Done():
			}
			return
		}
		if string(input.RunState) != state {
			t.Errorf("lost native checkpoint: %s", input.RunState)
		}
		emit("agent.turn.cancelled", true, json.RawMessage(`{"reason":"fixture_finished"}`))
	}))
	defer sidecar.Close()
	service, err := shell.NewRequiredSidecarAgentService(sidecar.URL, "fixture-service", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	manager := newAgentTurnManager(store, shell.NewWithAgentService(registry, service), slog.New(slog.NewTextHandler(io.Discard, nil)))
	go manager.run(ctx)
	defer func() {
		cancel()
		select {
		case <-manager.done:
		case <-time.After(5 * time.Second):
			t.Error("manager did not shut down")
		}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("stream never started")
	}
	command := businessruntime.ControlAgentTurnCommand{AgentTurnID: turn.AgentTurnID}
	if _, err := store.RequestAgentTurnPause(ctx, command); err != nil {
		t.Fatal(err)
	}
	waitForAgentTurnStatus(t, store, turn.AgentTurnID, "paused")
	if signals.Load() != 2 {
		t.Fatalf("pause signal was not retried: %d", signals.Load())
	}
	if _, err := store.ResumeAgentTurn(ctx, command); err != nil {
		t.Fatal(err)
	}
	// While the old response is still open, dispatch must not launch this ID again.
	select {
	case <-time.After(600 * time.Millisecond):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if executions.Load() != 1 {
		t.Fatalf("duplicate active run: %d", executions.Load())
	}
	current, _ := store.GetAgentTurn(ctx, turn.AgentTurnID)
	if current.Status != "accepted" {
		t.Fatalf("claimed closing stream: %s", current.Status)
	}
	close(release)
	waitForAgentTurnStatus(t, store, turn.AgentTurnID, "cancelled")
	if executions.Load() != 2 {
		t.Fatalf("resume count: %d", executions.Load())
	}
}
