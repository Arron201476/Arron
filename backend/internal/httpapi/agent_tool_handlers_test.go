package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestAgentToolCatalogSeparatesPublicAndInternalConfiguration(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "tool-test-token")
	tools, err := agenttool.Compile(agenttool.Config{
		SchemaVersion: agenttool.SchemaVersion,
		MCPServers: []agenttool.MCPServerConfig{{
			ID: "fixture", Description: "Fixture server", Transport: "streamable_http",
			URL: "http://127.0.0.1:9234/mcp", Enabled: true,
			HeaderEnvironment: map[string]string{"Authorization": "FIXTURE_MCP_TOKEN"},
			AllowedTools: []agenttool.MCPToolConfig{{
				Name: "lookup", Description: "Read fixture data", Access: agenttool.AccessRead,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntimeRevisionAndTools(
		shell.New(capability.NewEmptyRegistry()), nil, nil, tools, nil,
	)

	publicRequest := httptest.NewRequest(http.MethodGet, "/api/v1/agent-tools", nil)
	publicResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(publicResponse, publicRequest)
	if publicResponse.Code != http.StatusOK {
		t.Fatalf("public status = %d; body = %s", publicResponse.Code, publicResponse.Body.String())
	}
	if strings.Contains(publicResponse.Body.String(), "127.0.0.1") ||
		strings.Contains(publicResponse.Body.String(), "FIXTURE_MCP_TOKEN") {
		t.Fatalf("public catalog leaked private config: %s", publicResponse.Body.String())
	}

	unauthorized := httptest.NewRequest(http.MethodGet, "/internal/v1/agent-tools/catalog", nil)
	unauthorizedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", unauthorizedResponse.Code)
	}

	internal := httptest.NewRequest(http.MethodGet, "/internal/v1/agent-tools/catalog", nil)
	internal.Header.Set("Authorization", "Bearer tool-test-token")
	internalResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(internalResponse, internal)
	if internalResponse.Code != http.StatusOK {
		t.Fatalf("internal status = %d; body = %s", internalResponse.Code, internalResponse.Body.String())
	}
	if !strings.Contains(internalResponse.Body.String(), "127.0.0.1") ||
		!strings.Contains(internalResponse.Body.String(), "FIXTURE_MCP_TOKEN") {
		t.Fatalf("internal catalog omitted trusted config: %s", internalResponse.Body.String())
	}
}

func TestBackgroundCheckpointAndHostedOutputEndpointsRequireServiceIdentity(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "service-fixture")
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), capability.NewEmptyRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewWithRuntimeRevisionAndTools(shell.New(capability.NewEmptyRegistry()), store, nil, nil, nil).Handler()
	for _, path := range []string{"/internal/v1/agent-task-attempts/attempt/pause-for-approval", "/internal/v1/agent-tool-calls/call/outputs"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("public client reached %s: %d", path, response.Code)
		}
	}
}

func TestAgentToolHTTPApprovalGate(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "tool-test-token")
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := agenttool.Compile(agenttool.Config{
		SchemaVersion: agenttool.SchemaVersion,
		MCPServers: []agenttool.MCPServerConfig{{
			ID: "fixture", Description: "Fixture server", Transport: "streamable_http",
			URL: "http://127.0.0.1:9234/mcp", Enabled: true,
			AllowedTools: []agenttool.MCPToolConfig{{
				Name: "save", Description: "Save fixture data", Access: agenttool.AccessWrite,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(tools)
	project, err := store.CreateProject(context.Background(), "tool API")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, tools, nil).Handler()
	beginPath := "/internal/v1/agent-tool-calls"
	descriptor, _ := tools.Get("mcp:fixture/save")
	beginBody := map[string]any{
		"configuration_hash": descriptor.ConfigurationHash,
		"project_id":         project.ProjectID,
		"conversation_id":    project.PrimaryConversationID,
		"agent_turn_id":      "turn_http_1",
		"sdk_tool_call_id":   "sdk_http_1",
		"tool_id":            "mcp:fixture/save",
		"arguments":          map[string]any{"key": "title", "value": "New title"},
	}
	performJSONWithHeaders(t, handler, http.MethodPost, beginPath, beginBody, nil, http.StatusUnauthorized)
	payload := performJSONWithHeaders(
		t, handler, http.MethodPost, beginPath, beginBody,
		map[string]string{"Authorization": "Bearer tool-test-token"}, http.StatusCreated,
	)
	call := mustMap(t, payload["data"])
	if call["status"] != "pending_approval" {
		t.Fatalf("pending call = %+v", call)
	}
	approval := mustMap(t, call["approval"])
	approvalID := approval["agent_tool_approval_id"].(string)
	callID := call["agent_tool_call_id"].(string)
	outputPath := "/internal/v1/agent-tool-calls/" + callID + "/outputs?sdk_tool_call_id=sdk_http_1&filename=result.csv"
	writeOutput := func(token string, want int) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, outputPath, strings.NewReader("value\n42\n"))
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("MCP output: %d %s", response.Code, response.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	writeOutput("", http.StatusUnauthorized)
	writeOutput("tool-test-token", http.StatusConflict)

	list := performJSON(
		t, handler, http.MethodGet,
		"/api/v1/projects/"+project.ProjectID+"/agent-tool-approvals?status=pending",
		nil, http.StatusOK,
	)
	items := mustMap(t, list["data"])["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("pending approval items = %+v", items)
	}
	resolved := performJSON(
		t, handler, http.MethodPost,
		"/api/v1/agent-tool-approvals/"+approvalID+"/resolutions",
		map[string]any{
			"expected_version":      int(approval["version"].(float64)),
			"subject_snapshot_hash": approval["subject_snapshot_hash"],
			"action":                "approve",
		},
		http.StatusOK,
	)
	if mustMap(t, resolved["data"])["status"] != "approved" {
		t.Fatalf("resolved approval = %+v", resolved)
	}
	started := performJSONWithHeaders(
		t, handler, http.MethodPost,
		"/internal/v1/agent-tool-calls/"+callID+"/start",
		map[string]any{"expected_sdk_tool_call_id": "sdk_http_1", "configuration_hash": descriptor.ConfigurationHash},
		map[string]string{"Authorization": "Bearer tool-test-token"}, http.StatusOK,
	)
	if mustMap(t, started["data"])["status"] != "running" {
		t.Fatalf("started call = %+v", started)
	}
	output := mustMap(t, writeOutput("tool-test-token", http.StatusCreated)["data"])
	asset := mustMap(t, output["asset"])
	if asset["source_type"] != "mcp_tool" || mustMap(t, asset["metadata"])["agent_tool_call_id"] != callID {
		t.Fatalf("MCP provenance: %+v", asset)
	}
	duplicate := mustMap(t, writeOutput("tool-test-token", http.StatusCreated)["data"])
	if mustMap(t, duplicate["asset"])["asset_id"] != asset["asset_id"] {
		t.Fatal("duplicate output created a second asset")
	}
	download := httptest.NewRecorder()
	handler.ServeHTTP(download, httptest.NewRequest(http.MethodGet, "/api/v1/assets/"+asset["asset_id"].(string)+"/download", nil))
	if download.Code != http.StatusOK || download.Body.String() != "value\n42\n" || download.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("MCP download: %d %s", download.Code, download.Body.String())
	}
	completed := performJSONWithHeaders(
		t, handler, http.MethodPost,
		"/internal/v1/agent-tool-calls/"+callID+"/complete",
		map[string]any{"result": map[string]any{"ok": true}, "result_size_bytes": 11},
		map[string]string{"Authorization": "Bearer tool-test-token"}, http.StatusOK,
	)
	if mustMap(t, completed["data"])["status"] != "completed" {
		t.Fatalf("completed call = %+v", completed)
	}
	writeOutput("tool-test-token", http.StatusConflict)
}

func mustMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value is %T, want map[string]any", value)
	}
	return result
}
