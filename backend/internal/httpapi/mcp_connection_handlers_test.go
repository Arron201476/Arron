package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
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

func TestMCPConnectionsHTTPWriteOnlyPermissionsAndExecutionConsumption(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "credential-service-token")
	registry := capability.NewEmptyRegistry()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "credentials.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ConfigureMCPCredentials(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x72}, 32))); err != nil {
		t.Fatal(err)
	}
	tools, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion, MCPServers: []agenttool.MCPServerConfig{{
		ID: "service", Description: "Trusted service", Enabled: true, Transport: "streamable_http", URL: "https://example.com/mcp",
		CredentialHeaders: map[string]string{"Authorization": "api-token"},
		AllowedTools:      []agenttool.MCPToolConfig{{Name: "lookup", Description: "Lookup", Access: agenttool.AccessRead}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	principals := []StaticTokenPrincipal{
		{Token: "owner-token", UserID: "owner-A", WorkspaceID: "workspace-A", Role: identity.RoleOwner},
		{Token: "editor-token", UserID: "editor-A", WorkspaceID: "workspace-A", Role: identity.RoleEditor},
		{Token: "viewer-token", UserID: "viewer-A", WorkspaceID: "workspace-A", Role: identity.RoleViewer},
		{Token: "foreign-token", UserID: "owner-B", WorkspaceID: "workspace-B", Role: identity.RoleOwner},
	}
	auth, err := NewStaticTokenAuthenticator(principals)
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, tools, nil)
	if err := server.ConfigureAuthentication(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	call := func(method, path string, payload any, headers map[string]string, want int) map[string]any {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(method, path, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		writer := httptest.NewRecorder()
		handler.ServeHTTP(writer, request)
		if writer.Code != want {
			t.Fatalf("%s %s status=%d want=%d: %s", method, path, writer.Code, want, writer.Body.String())
		}
		if writer.Code == http.StatusOK && strings.Contains(path, "mcp-connections") && writer.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("credential response is cacheable")
		}
		var result map[string]any
		if err := json.Unmarshal(writer.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	path := "/api/v1/mcp-connections"
	command := func(scope, value, id string, version int) map[string]any {
		return map[string]any{"server_id": "service", "scope": scope,
			"expected_version": version, "request_id": id, "values": map[string]string{"api-token": value}}
	}
	shared := command("workspace", "Bearer workspace-only-value", "workspace-save", 0)
	call(http.MethodPut, path, shared, bearer("editor-token", ""), http.StatusForbidden)
	call(http.MethodPut, path, shared, bearer("owner-token", ""), http.StatusOK)
	call(http.MethodPut, path, shared, bearer("owner-token", ""), http.StatusOK)
	personal := command("user", "Bearer editor-only-value", "personal-save", 0)
	call(http.MethodPut, path, personal, bearer("viewer-token", ""), http.StatusForbidden)
	call(http.MethodPut, path, personal, bearer("editor-token", ""), http.StatusOK)
	for _, token := range []string{"owner-token", "editor-token", "viewer-token", "foreign-token"} {
		result := call(http.MethodGet, path, nil, bearer(token, ""), http.StatusOK)
		raw, _ := json.Marshal(result)
		for _, forbidden := range []string{"only-value", "Authorization", "example.com", "ciphertext", "credential_binding"} {
			if bytes.Contains(raw, []byte(forbidden)) {
				t.Fatalf("public inventory exposes %s", forbidden)
			}
		}
		item := arrayAt(t, objectAt(t, result, "data"), "items")[0].(map[string]any)
		want := "workspace"
		if token == "editor-token" {
			want = "user"
		}
		if token == "foreign-token" {
			want = "none"
		}
		if item["effective_scope"] != want {
			t.Fatal("scope crossed identity", token, item)
		}
	}
	forged := command("user", "value", "forged-owner", 1)
	forged["owner_user_id"] = "owner-A"
	call(http.MethodPut, path, forged, bearer("editor-token", ""), http.StatusBadRequest)
	resolvePath := "/internal/v1/mcp-connections/resolve"
	resolve := map[string]any{"server_id": "service", "credential_binding": "forged"}
	call(http.MethodPost, resolvePath, resolve, bearer("editor-token", ""), http.StatusUnauthorized)
	call(http.MethodPost, resolvePath, resolve, bearer("credential-service-token", ""), http.StatusForbidden)
	bindings := map[string]string{}
	for _, index := range []int{0, 1} {
		user := principals[index]
		ctx := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: user.UserID, WorkspaceID: user.WorkspaceID, Role: user.Role})
		project, err := store.CreateProject(ctx, "Credential HTTP execution")
		if err != nil {
			t.Fatal(err)
		}
		turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Inspect"}, businessruntime.CommandMeta{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
			t.Fatal(err)
		}
		headers := map[string]string{"Authorization": "Bearer credential-service-token", "X-Agent-Project-ID": project.ProjectID, "X-Agent-Turn-ID": turn.AgentTurnID}
		catalog := objectAt(t, call(http.MethodGet, "/internal/v1/agent-tools/catalog", nil, headers, http.StatusOK), "data")
		connection := arrayAt(t, catalog, "mcp_servers")[0].(map[string]any)
		binding := connection["credential_binding"].(string)
		bindings[user.UserID] = binding
		resolve := map[string]any{"server_id": "service", "credential_binding": binding}
		result := objectAt(t, call(http.MethodPost, resolvePath, resolve, headers, http.StatusOK), "data")
		want := "Bearer workspace-only-value"
		if index == 1 {
			want = "Bearer editor-only-value"
		}
		if objectAt(t, result, "values")["api-token"] != want {
			t.Fatal("execution used another user's credential")
		}
		if index == 1 {
			resolve["credential_binding"] = bindings["owner-A"]
			call(http.MethodPost, resolvePath, resolve, headers, http.StatusConflict)
			resolve["credential_binding"] = binding
		}
		call(http.MethodPut, path, personal, headers, http.StatusForbidden)
		if _, err := store.BeginAgentTurnCommit(ctx, turn.AgentTurnID); err != nil {
			t.Fatal(err)
		}
		call(http.MethodPost, resolvePath, resolve, headers, http.StatusForbidden)
	}
}
