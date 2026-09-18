package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestAgentInstructionsHTTPPrivateScopesCASAndActiveExecution(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "instruction-service")
	registry := capability.NewEmptyRegistry()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "rules.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principals := []StaticTokenPrincipal{
		{Token: "owner", UserID: "owner", WorkspaceID: "workspace", Role: identity.RoleOwner},
		{Token: "editor", UserID: "editor", WorkspaceID: "workspace", Role: identity.RoleEditor},
		{Token: "viewer", UserID: "viewer", WorkspaceID: "workspace", Role: identity.RoleViewer},
		{Token: "foreign", UserID: "foreign", WorkspaceID: "other-workspace", Role: identity.RoleOwner},
	}
	auth, err := NewStaticTokenAuthenticator(principals)
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, nil, nil)
	if err := server.ConfigureAuthentication(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	call := func(method, path string, body any, headers map[string]string, status int) map[string]any {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(method, path, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, response.Code, status, response.Body.String())
		}
		if status == http.StatusOK && response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private rule response cacheable")
		}
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	path := "/api/v1/agent-instructions"
	command := map[string]any{"scope": "user", "expected_version": 0, "content": "OWNER_PRIVATE_RULE", "enabled": true, "request_id": "save"}
	call(http.MethodPut, path, command, bearer("owner", ""), http.StatusOK)
	call(http.MethodPut, path, command, bearer("owner", ""), http.StatusOK)
	command["request_id"] = "stale"
	call(http.MethodPut, path, command, bearer("owner", ""), http.StatusConflict)
	call(http.MethodPut, path, command, bearer("viewer", ""), http.StatusForbidden)
	for _, token := range []string{"editor", "viewer", "foreign"} {
		result := call(http.MethodGet, path, nil, bearer(token, ""), http.StatusOK)
		raw, _ := json.Marshal(result)
		if strings.Contains(string(raw), "OWNER_PRIVATE_RULE") {
			t.Fatal("personal rule exposed to another member")
		}
	}
	command["scope"] = "workspace"
	call(http.MethodPut, path, command, bearer("editor", ""), http.StatusForbidden)
	call(http.MethodPut, path, command, bearer("owner", ""), http.StatusOK)
	command["scope_ref"] = "another-user"
	call(http.MethodPut, path, command, bearer("owner", ""), http.StatusBadRequest)
	delete(command, "scope_ref")
	command["content"] = strings.Repeat("x", 70*1024)
	call(http.MethodPut, path, command, bearer("owner", ""), http.StatusBadRequest)
	owner := identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "workspace", Role: identity.RoleOwner}
	ctx := identity.WithPrincipal(context.Background(), owner)
	project, err := store.CreateProject(ctx, "HTTP rules")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Read rules"}, businessruntime.CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer instruction-service", "X-Agent-Project-ID": project.ProjectID, "X-Agent-Turn-ID": turn.AgentTurnID}
	snapshotPath := "/internal/v1/agent-instructions/snapshot"
	body := map[string]any{"project_id": project.ProjectID, "activity_key": "turn:" + turn.AgentTurnID}
	call(http.MethodPost, snapshotPath, body, bearer("owner", ""), http.StatusUnauthorized)
	call(http.MethodPost, snapshotPath, body, bearer("instruction-service", ""), http.StatusForbidden)
	first := objectAt(t, call(http.MethodPost, snapshotPath, body, headers, http.StatusOK), "data")
	if len(arrayAt(t, first, "documents")) != 2 || first["user_id"] != "owner" {
		t.Fatal("snapshot identity missing")
	}
	body["project_id"] = "foreign"
	call(http.MethodPost, snapshotPath, body, headers, http.StatusForbidden)
	body["project_id"] = project.ProjectID
	call(http.MethodGet, path+"?project_id="+project.ProjectID, nil, bearer("foreign", ""), http.StatusNotFound)
	clear := map[string]any{"scope": "user", "expected_version": 1, "content": "", "enabled": false, "request_id": "forget"}
	call(http.MethodPut, path, clear, headers, http.StatusForbidden)
	call(http.MethodPut, path, clear, bearer("owner", ""), http.StatusOK)
	again := objectAt(t, call(http.MethodPost, snapshotPath, body, headers, http.StatusOK), "data")
	if again["content_hash"] != first["content_hash"] {
		t.Fatal("running execution instructions changed")
	}
	if _, err := store.BeginAgentTurnCommit(ctx, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	call(http.MethodPost, snapshotPath, body, headers, http.StatusForbidden)
}
