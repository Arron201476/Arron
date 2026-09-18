package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

func TestQueuedMessageHTTPAuthorizationCASAndReopen(t *testing.T) {
	database := filepath.Join(t.TempDir(), "queue-http.db")
	store, handler := openBackgroundPauseHTTP(t, database)
	defer func() { store.Close() }()
	ctx := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner})
	project, err := store.CreateProject(ctx, "Queue HTTP")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Original"}, businessruntime.CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/agent-turns/" + turn.AgentTurnID + "/queued-message"
	body := map[string]any{"content": "Revised", "expected_content": "Original"}
	key := "44444444-4444-4444-8444-444444444444"
	performJSONWithHeaders(t, handler, http.MethodPatch, path, body, nil, http.StatusUnauthorized)
	performJSONWithHeaders(t, handler, http.MethodPatch, path, body, bearer("pause-viewer", key), http.StatusForbidden)
	performJSONWithHeaders(t, handler, http.MethodPatch, path, body, bearer("pause-foreign", key), http.StatusNotFound)
	performJSONWithHeaders(t, handler, http.MethodPatch, path, body, map[string]string{"Authorization": "Bearer pause-owner", "Idempotency-Key": ""}, http.StatusBadRequest)
	performJSONWithHeaders(t, handler, http.MethodPatch, path, map[string]any{"content": "missing expected"}, bearer("pause-owner", key), http.StatusBadRequest)
	updated := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPatch, path, body, bearer("pause-owner", key), http.StatusOK), "data")
	if objectAt(t, updated, "request")["content"] != "Revised" || updated["status"] != "accepted" {
		t.Fatalf("updated: %+v", updated)
	}
	performJSONWithHeaders(t, handler, http.MethodPatch, path, map[string]any{"content": "wrong", "expected_content": "Original"}, bearer("pause-owner", key), http.StatusBadRequest)
	performJSONWithHeaders(t, handler, http.MethodPatch, path, body, bearer("pause-owner", "55555555-5555-4555-8555-555555555555"), http.StatusConflict)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, handler = openBackgroundPauseHTTP(t, database)
	performJSONWithHeaders(t, handler, http.MethodPatch, path, body, bearer("pause-owner", key), http.StatusOK)
	claimed, err := store.ClaimRunnableAgentTurns(context.Background(), 1)
	if err != nil || len(claimed) != 1 || claimed[0].Request.Content != "Revised" {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	performJSONWithHeaders(t, handler, http.MethodPatch, path, map[string]any{"content": "Too late", "expected_content": "Revised"}, bearer("pause-owner", "66666666-6666-4666-8666-666666666666"), http.StatusConflict)
}
