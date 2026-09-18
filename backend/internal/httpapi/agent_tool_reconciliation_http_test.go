package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestExternalToolReconciliationHTTPAuthorizationAndStrictReceipt(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "outcome-service")
	registry := capability.NewEmptyRegistry()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "outcomes.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	toolRegistry, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion, MCPServers: []agenttool.MCPServerConfig{{
		ID: "fixture", Description: "Fixture MCP", Transport: "streamable_http", URL: "http://127.0.0.1:9321/mcp", Enabled: true,
		AllowedTools: []agenttool.MCPToolConfig{{Name: "write", Description: "Write fact", Access: agenttool.AccessWrite, Approval: agenttool.ApprovalAlways}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(toolRegistry)
	auth, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
		{Token: "owner", UserID: "owner", WorkspaceID: "workspace", Role: identity.RoleOwner},
		{Token: "editor", UserID: "editor", WorkspaceID: "workspace", Role: identity.RoleEditor},
		{Token: "viewer", UserID: "viewer", WorkspaceID: "workspace", Role: identity.RoleViewer},
		{Token: "foreign", UserID: "foreign", WorkspaceID: "foreign", Role: identity.RoleOwner},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, toolRegistry, nil)
	if err := server.ConfigureAuthentication(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "workspace", Role: identity.RoleOwner})
	project, err := store.CreateProject(ctx, "Outcome review")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Authorized write"}, businessruntime.CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	catalog, err := store.AgentToolCatalog(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	configurationHash := ""
	for _, tool := range catalog.Tools {
		if tool.ID == "mcp:fixture/write" {
			configurationHash = tool.ConfigurationHash
		}
	}
	toolCall, err := store.BeginAgentToolCall(ctx, businessruntime.BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		AgentTurnID: turn.AgentTurnID, SDKToolCallID: "native-write", ToolID: "mcp:fixture/write", ConfigurationHash: configurationHash, Arguments: json.RawMessage(`{"value":"fact"}`)})
	if err != nil {
		t.Fatal(err)
	}
	approval := toolCall.Approval
	if _, err := store.ResolveAgentToolApproval(ctx, businessruntime.ResolveAgentToolApprovalCommand{AgentToolApprovalID: approval.AgentToolApprovalID,
		ExpectedVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash, Action: "approve"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartAgentToolCall(ctx, businessruntime.StartAgentToolCallCommand{AgentToolCallID: toolCall.AgentToolCallID, ExpectedSDKToolCallID: toolCall.SDKToolCallID, ConfigurationHash: configurationHash}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FailAgentToolCall(ctx, businessruntime.FailAgentToolCallCommand{AgentToolCallID: toolCall.AgentToolCallID, ErrorCode: "MCP_TOOL_OUTCOME_UNKNOWN", ErrorMessage: "Transport lost"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FailAgentTurn(ctx, turn.AgentTurnID, "SDK_TOOL_OUTCOME_UNRESOLVED", "Original checkpoint unavailable"); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/agent-tool-calls/" + toolCall.AgentToolCallID + "/outcome-review"
	call := func(method, token string, body any, status int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(method, path, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s %s: %d want %d: %s", method, token, response.Code, status, response.Body.String())
		}
		if status == http.StatusOK && response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private evidence cacheable")
		}
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	call(http.MethodGet, "", nil, http.StatusUnauthorized)
	call(http.MethodGet, "foreign", nil, http.StatusNotFound)
	viewer := objectAt(t, call(http.MethodGet, "viewer", nil, http.StatusOK), "data")
	if viewer["can_resolve"] != false {
		t.Fatal("viewer can reconcile")
	}
	view := objectAt(t, call(http.MethodGet, "owner", nil, http.StatusOK), "data")
	command := map[string]any{"subject_snapshot_hash": view["subject_snapshot_hash"], "request_id": "review-1", "outcome": "applied", "evidence": "Checked actual provider history"}
	call(http.MethodPost, "viewer", command, http.StatusForbidden)
	call(http.MethodPost, "editor", command, http.StatusForbidden)
	call(http.MethodPost, "outcome-service", command, http.StatusForbidden)
	call(http.MethodPost, "foreign", command, http.StatusNotFound)
	command["actor_user_id"] = "owner"
	call(http.MethodPost, "owner", command, http.StatusBadRequest)
	delete(command, "actor_user_id")
	command["outcome"] = "unknown"
	call(http.MethodPost, "owner", command, http.StatusBadRequest)
	command["outcome"] = "applied"
	call(http.MethodPost, "owner", command, http.StatusOK)
	call(http.MethodPost, "owner", command, http.StatusOK)
	command["outcome"] = "not_applied"
	call(http.MethodPost, "owner", command, http.StatusConflict)
	stored, err := store.GetAgentToolCall(ctx, toolCall.AgentToolCallID)
	if err != nil || stored.Status != "failed" || stored.ResultHash != nil {
		t.Fatal("manual reconciliation forged success", err)
	}
}
