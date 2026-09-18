package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
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

func TestWorkspaceToolConfigurationHTTPPermissionsAndCatalogConsumption(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "tools-service")
	registry := capability.NewEmptyRegistry()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "tools.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tools, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion, MCPServers: []agenttool.MCPServerConfig{{ID: "private", Description: "Private service", WorkspaceIDs: []string{"tools_workspace"},
		Transport: "streamable_http", URL: "https://example.com/private", HeaderEnvironment: map[string]string{"Authorization": "PRIVATE_TOKEN_ENV"},
		AllowedTools: []agenttool.MCPToolConfig{{Name: "lookup", Description: "Lookup", Access: agenttool.AccessRead}}}}})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
		{Token: "tools-owner", UserID: "tools_owner", WorkspaceID: "tools_workspace", Role: identity.RoleOwner},
		{Token: "tools-editor", UserID: "tools_editor", WorkspaceID: "tools_workspace", Role: identity.RoleEditor},
		{Token: "tools-foreign", UserID: "tools_foreign", WorkspaceID: "foreign_workspace", Role: identity.RoleOwner},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, tools, nil)
	if err := server.ConfigureAuthentication(context.Background(), authenticator); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	path := "/api/v1/agent-tools/configuration"
	get := func(token, path string) map[string]any {
		t.Helper()
		return objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, path, nil, bearer(token, ""), http.StatusOK), "data")
	}
	for _, token := range []string{"tools-owner", "tools-editor", "tools-foreign"} {
		data := get(token, path)
		encoded, _ := json.Marshal(data)
		if strings.Contains(string(encoded), "example.com") || strings.Contains(string(encoded), "PRIVATE_TOKEN_ENV") {
			t.Fatalf("management leaked private execution config: %s", encoded)
		}
		if token == "tools-foreign" && strings.Contains(string(encoded), "mcp:private") {
			t.Fatal("foreign workspace discovered restricted MCP")
		}
	}
	command := map[string]any{"expected_version": 0, "enabled": map[string]bool{"mcp:private": true, "hosted:native-web-search": true}}
	performJSONWithHeaders(t, handler, http.MethodPatch, path, command, bearer("tools-editor", ""), http.StatusForbidden)
	performJSONWithHeaders(t, handler, http.MethodPatch, path, command, bearer("tools-foreign", ""), http.StatusBadRequest)
	performJSONWithHeaders(t, handler, http.MethodPatch, path, command, bearer("tools-owner", ""), http.StatusOK)
	performJSONWithHeaders(t, handler, http.MethodPatch, path, command, bearer("tools-owner", ""), http.StatusConflict)
	performJSONWithHeaders(t, handler, http.MethodPatch, path, map[string]any{"expected_version": 1, "enabled": map[string]bool{"mcp:private": false}, "url": "https://untrusted.example/mcp"}, bearer("tools-owner", ""), http.StatusBadRequest)
	for _, token := range []string{"tools-owner", "tools-foreign"} {
		items := arrayAt(t, get(token, "/api/v1/agent-tools"), "tools")
		for _, raw := range items {
			item := raw.(map[string]any)
			if item["id"] == "hosted:native-web-search" && item["enabled"] != (token == "tools-owner") {
				t.Fatalf("wrong effective catalog for %s: %+v", token, item)
			}
			if item["id"] == "mcp:private/lookup" && (token != "tools-owner" || item["enabled"] != true) {
				t.Fatalf("MCP selection crossed scopes: %+v", item)
			}
		}
	}
	for _, principal := range []identity.Principal{
		{Kind: identity.KindUser, UserID: "tools_owner", WorkspaceID: "tools_workspace", Role: identity.RoleOwner},
		{Kind: identity.KindUser, UserID: "tools_foreign", WorkspaceID: "foreign_workspace", Role: identity.RoleOwner},
	} {
		ctx := identity.WithPrincipal(context.Background(), principal)
		project, err := store.CreateProject(ctx, "Scoped SDK tool catalog")
		if err != nil {
			t.Fatal(err)
		}
		turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Inspect tools"}, businessruntime.CommandMeta{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
			t.Fatal(err)
		}
		headers := map[string]string{"Authorization": "Bearer tools-service", "X-Agent-Project-ID": project.ProjectID, "X-Agent-Turn-ID": turn.AgentTurnID}
		catalog := objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, "/internal/v1/agent-tools/catalog", nil, headers, http.StatusOK), "data")
		encoded, _ := json.Marshal(catalog)
		if strings.Contains(string(encoded), "PRIVATE_TOKEN_ENV") != (principal.WorkspaceID == "tools_workspace") {
			t.Fatal("SDK activity did not select its own workspace configuration")
		}
		performJSONWithHeaders(t, handler, http.MethodPatch, path, command, headers, http.StatusForbidden)
	}
}
