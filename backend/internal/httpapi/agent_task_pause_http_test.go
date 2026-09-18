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
	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func openBackgroundPauseHTTP(t *testing.T, database string) (*businessruntime.Store, http.Handler) {
	t.Helper()
	root := testRoot(t)
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: root, SkillRoots: []capability.SkillRoot{{
		Scope: capability.SkillScopeWorkspace, WorkspaceID: "pause", Path: filepath.Join(root, "fixtures", "skills", "background"), Priority: 200,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(database, registry)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(tools)
	auth, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
		{Token: "pause-owner", UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner},
		{Token: "pause-viewer", UserID: "viewer", WorkspaceID: "pause", Role: identity.RoleViewer},
		{Token: "pause-foreign", UserID: "foreign", WorkspaceID: "foreign", Role: identity.RoleOwner},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, tools, nil)
	if err := server.ConfigureAuthentication(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	return store, server.Handler()
}

func createBackgroundPauseHTTPTask(t *testing.T, store *businessruntime.Store) (businessruntime.Project, businessruntime.AgentTask) {
	t.Helper()
	ctx := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner})
	project, err := store.CreateProject(ctx, "Background pause HTTP")
	if err != nil {
		t.Fatal(err)
	}
	ref := &agentcontract.CapabilityRef{CapabilityID: "story_research_digest", Version: "1.0.0", SelectionMode: "explicit"}
	exchange, err := store.CreateMessageExchange(ctx, businessruntime.CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID, Request: agentcontract.MessageRequest{Content: "Research", CapabilityRef: ref},
		Decision: agentcontract.AgentDecision{Reply: "Queued", Intent: "propose_capability", Confidence: 1, CapabilityRef: ref,
			ProposedAction: &agentcontract.ProposedActionDraft{ActionType: "start_background_task", CapabilityRef: ref,
				Input: json.RawMessage(`{}`), Config: json.RawMessage(`{}`), RequiresConfirmation: false}},
	})
	if err != nil || exchange.TaskRef == nil {
		t.Fatalf("task: %+v %v", exchange, err)
	}
	return project, *exchange.TaskRef
}

func TestBackgroundPauseHTTPControlsCheckpointRestartAndTokenFences(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "pause-service")
	database := filepath.Join(t.TempDir(), "pause-http.db")
	store, handler := openBackgroundPauseHTTP(t, database)
	defer func() { store.Close() }()
	project, task := createBackgroundPauseHTTPTask(t, store)
	base := "/api/v1/agent-tasks/" + task.AgentTaskID
	key := "a3333333-3333-4333-8333-333333333333"
	owner := bearer("pause-owner", key)
	claimBody := map[string]any{"worker_id": "pause-worker", "provider_id": "sdk", "model_id": "fixture", "lease_seconds": 60}
	claimPath := "/internal/v1/agent-tasks/claims"
	assertNotClaimable := func() {
		t.Helper()
		body, _ := json.Marshal(claimBody)
		request := httptest.NewRequest(http.MethodPost, claimPath, strings.NewReader(string(body)))
		request.Header.Set("Authorization", "Bearer pause-service")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("claimed a paused task: %d %s", recorder.Code, recorder.Body.String())
		}
	}
	for _, action := range []string{"pause", "resume"} {
		performJSONWithHeaders(t, handler, http.MethodPost, base+"/"+action, nil, bearer("pause-viewer", key), http.StatusForbidden)
		performJSONWithHeaders(t, handler, http.MethodPost, base+"/"+action, nil, bearer("pause-foreign", key), http.StatusNotFound)
		performJSONWithHeaders(t, handler, http.MethodPost, base+"/"+action, nil, nil, http.StatusUnauthorized)
	}
	paused := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, base+"/pause", nil, owner, http.StatusAccepted), "data")
	if paused["status"] != "paused" || paused["attempt_count"] != float64(0) {
		t.Fatalf("queued pause: %+v", paused)
	}
	assertNotClaimable()
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/resume", nil, owner, http.StatusAccepted)
	claim := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, claimPath, claimBody, bearer("pause-service", ""), http.StatusOK), "data")
	attemptID := stringAt(t, objectAt(t, claim, "attempt"), "agent_task_attempt_id")
	token := stringAt(t, claim, "attempt_token")
	worker := map[string]string{"Authorization": "Bearer pause-service", "X-Attempt-Token": token}
	attemptPath := "/internal/v1/agent-task-attempts/" + attemptID
	state := map[string]any{"schema_version": "1.16", "run_state": map[string]any{"$schemaVersion": "1.16", "private_marker": "sdk-state"}, "pending_sdk_tool_call_ids": []string{}}
	performJSONWithHeaders(t, handler, http.MethodPost, attemptPath+"/pause", state, worker, http.StatusConflict)
	owner = bearer("pause-owner", "a4444444-4444-4444-8444-444444444444")
	pausing := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, base+"/pause", nil, owner, http.StatusAccepted), "data")
	if pausing["status"] != "pausing" {
		t.Fatalf("running pause: %+v", pausing)
	}
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/resume", nil, owner, http.StatusConflict)
	performJSONWithHeaders(t, handler, http.MethodPost, attemptPath+"/pause", state, owner, http.StatusUnauthorized)
	badToken := map[string]string{"Authorization": "Bearer pause-service", "X-Attempt-Token": "wrong"}
	performJSONWithHeaders(t, handler, http.MethodPost, attemptPath+"/pause", state, badToken, http.StatusBadRequest)
	progress := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, attemptPath+"/progress", map[string]any{"current": 1, "total": 3}, worker, http.StatusOK), "data")
	if progress["status"] != "pausing" {
		t.Fatalf("worker cannot observe pause: %+v", progress)
	}
	performJSONWithHeaders(t, handler, http.MethodPost, attemptPath+"/pause", state, worker, http.StatusOK)
	performJSONWithHeaders(t, handler, http.MethodPost, attemptPath+"/pause", state, worker, http.StatusOK)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, handler = openBackgroundPauseHTTP(t, database)
	projection := performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/projects/"+project.ProjectID+"/workspace-projection", nil, owner, http.StatusOK)
	encoded, _ := json.Marshal(projection)
	if strings.Contains(string(encoded), "private_marker") || strings.Contains(string(encoded), token) {
		t.Fatal("public projection leaked checkpoint or token")
	}
	projected := arrayAt(t, objectAt(t, objectAt(t, projection, "data"), "snapshot"), "agent_tasks")
	if len(projected) != 1 || projected[0].(map[string]any)["status"] != "paused" {
		t.Fatalf("pause lost on restart: %+v", projected)
	}
	assertNotClaimable()
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/resume", nil, owner, http.StatusAccepted)
	resumed := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, claimPath, claimBody, bearer("pause-service", ""), http.StatusOK), "data")
	if stringAt(t, resumed, "attempt_token") == token || objectAt(t, resumed, "attempt")["agent_task_attempt_id"] != attemptID || len(arrayAt(t, objectAt(t, resumed, "resume"), "approval_decisions")) != 0 {
		t.Fatalf("resume identity or decisions: %+v", resumed)
	}
	performJSONWithHeaders(t, handler, http.MethodPost, attemptPath+"/pause", state, worker, http.StatusBadRequest)
	worker["X-Attempt-Token"] = stringAt(t, resumed, "attempt_token")
	performJSONWithHeaders(t, handler, http.MethodPost, attemptPath+"/complete", map[string]any{"result": map[string]any{"summary": "done"}}, worker, http.StatusOK)
}
