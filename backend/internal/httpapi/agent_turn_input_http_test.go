package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestMainInputReceiptPrecedesFailureAndSurvivesRebuild(t *testing.T) {
	database := filepath.Join(t.TempDir(), "failed-input-http.db")
	store, handler := openBackgroundPauseHTTP(t, database)
	defer func() { store.Close() }()
	ctx := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner})
	project, err := store.CreateProject(ctx, "Failed input receipt")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Original"}, businessruntime.CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.AppendAgentTurnInput(ctx, businessruntime.AppendAgentTurnInputCommand{AgentTurnID: turn.AgentTurnID, Content: "Model received this"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimRunnableAgentTurns(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatal(err)
	}
	manager := newAgentTurnManager(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	payload, err := json.Marshal(map[string]any{"included_input_ids": []string{input.InputID}})
	if err != nil {
		t.Fatal(err)
	}
	event := shell.AgentTurnEvent{EventType: "agent.turn.inputs_included", Payload: payload}
	if err := manager.persistEvent(ctx, claimed[0], event); err != nil {
		t.Fatal(err)
	}
	if err := manager.persistEvent(ctx, claimed[0], event); err != nil {
		t.Fatal("idempotent receipt rejected", err)
	}
	if err := manager.persistEvent(ctx, claimed[0], shell.AgentTurnEvent{EventType: "agent.turn.failed", Payload: json.RawMessage(`{"failure_stage":"output_validation"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, handler = openBackgroundPauseHTTP(t, database)
	response := objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/agent-turns/"+turn.AgentTurnID, nil, bearer("pause-owner", ""), http.StatusOK), "data")
	inputView := arrayAt(t, response, "additional_inputs")[0].(map[string]any)
	if response["status"] != "failed" || inputView["status"] != "included" || inputView["included_at"] == nil {
		t.Fatal("failure lost native model receipt", response)
	}
}

func TestMainTurnInputHTTPAuthorizationIdempotencyAndPrivateCheckpointReceipt(t *testing.T) {
	store, handler := openBackgroundPauseHTTP(t, filepath.Join(t.TempDir(), "main-input-http.db"))
	defer store.Close()
	ctx := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner})
	project, err := store.CreateProject(ctx, "Main input")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Original"}, businessruntime.CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/agent-turns/" + turn.AgentTurnID
	key := "a8888888-8888-4888-8888-888888888888"
	owner, body := bearer("pause-owner", key), map[string]any{"content": "Additional instruction"}
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/inputs", body, nil, http.StatusUnauthorized)
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/inputs", body, bearer("pause-viewer", key), http.StatusForbidden)
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/inputs", body, bearer("pause-foreign", key), http.StatusNotFound)
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/inputs", body, map[string]string{"Authorization": "Bearer pause-owner", "Idempotency-Key": ""}, http.StatusBadRequest)
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/inputs", map[string]any{"content": "injected", "status": "included"}, owner, http.StatusBadRequest)
	first := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, base+"/inputs", body, owner, http.StatusAccepted), "data")
	if first["status"] != "received" {
		t.Fatalf("premature model receipt: %+v", first)
	}
	repeated := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, base+"/inputs", body, owner, http.StatusAccepted), "data")
	if repeated["input_id"] != first["input_id"] {
		t.Fatal("retry appended another instruction")
	}
	reused := performJSONWithHeaders(t, handler, http.MethodPost, base+"/inputs", map[string]any{"content": "changed"}, owner, http.StatusBadRequest)
	if objectAt(t, reused, "error")["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("changed retry must preserve the idempotency error: %+v", reused)
	}
	claimed, err := store.ClaimRunnableAgentTurns(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/pause", map[string]any{}, owner, http.StatusOK)
	manager := newAgentTurnManager(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	payload := map[string]any{"run_state_schema": "1.16", "run_state": json.RawMessage(`{"$schemaVersion":"1.16","private":9007199254740993}`), "pending_sdk_tool_call_ids": []string{}, "included_input_ids": []string{first["input_id"].(string)}}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.persistEvent(ctx, claimed[0], shell.AgentTurnEvent{EventType: "agent.turn.paused", Payload: encoded}); err != nil {
		t.Fatal(err)
	}
	receipt := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, base+"/inputs", body, owner, http.StatusAccepted), "data")
	if receipt["input_id"] != first["input_id"] || receipt["status"] != "included" || receipt["included_at"] == nil {
		t.Fatalf("receipt missing: %+v", receipt)
	}
	current := objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, base, nil, owner, http.StatusOK), "data")
	if current["status"] != "paused" || len(arrayAt(t, current, "additional_inputs")) != 1 || objectAt(t, current, "request")["content"] != "Original" {
		t.Fatalf("input changed original execution: %+v", current)
	}
	if _, err := store.CancelAgentTurn(ctx, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/inputs", body, bearer("pause-owner", "b8888888-8888-4888-8888-888888888888"), http.StatusConflict)
}
