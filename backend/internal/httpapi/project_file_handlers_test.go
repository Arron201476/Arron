package httpapi

import (
	"context"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestProjectFileHTTPDownloadScopeAuditAndValidation(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "file-service-fixture")
	registry := capability.NewEmptyRegistry()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "files.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tools, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(tools)
	authenticator, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
		{Token: "file-owner", UserID: "file_owner", WorkspaceID: "file_workspace", Role: identity.RoleOwner},
		{Token: "file-viewer", UserID: "file_viewer", WorkspaceID: "file_workspace", Role: identity.RoleViewer},
		{Token: "file-foreign", UserID: "file_foreign", WorkspaceID: "foreign_workspace", Role: identity.RoleOwner},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, tools, nil)
	if err := server.ConfigureAuthentication(context.Background(), authenticator); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	project := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/projects", map[string]any{"title": "File API"}, bearer("file-owner", "11111111-1111-4111-8111-111111111111"), http.StatusCreated), "data")
	projectID := stringAt(t, project, "project_id")
	projectPath := "/api/v1/projects/" + projectID + "/files"
	serviceHeaders := bearer("file-service-fixture", "")
	patch := map[string]any{"path": "draft/\u4e2d\u6587 & notes.txt", "expected_version": 0, "operation": "create_file", "diff": "+\u4f60\u597d\n+file"}
	descriptor, _ := tools.Get("runtime:apply_workspace_patch")
	call := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls", map[string]any{
		"project_id": projectID, "conversation_id": project["primary_conversation_id"],
		"sdk_tool_call_id": "sdk-file-http", "tool_id": descriptor.ID,
		"configuration_hash": descriptor.ConfigurationHash, "arguments": patch,
	}, serviceHeaders, http.StatusCreated), "data")
	patchPath := "/internal/v1/agent-tool-calls/" + stringAt(t, call, "agent_tool_call_id") + "/project-file-patches"
	body := map[string]any{"sdk_tool_call_id": "sdk-file-http", "patch": patch, "content": "\u4f60\u597d\nfile"}
	performJSONWithHeaders(t, handler, http.MethodPost, patchPath, body, nil, http.StatusUnauthorized)
	performJSONWithHeaders(t, handler, http.MethodPost, patchPath, body, bearer("file-viewer", ""), http.StatusUnauthorized)
	performJSONWithHeaders(t, handler, http.MethodPost, patchPath, body, serviceHeaders, http.StatusOK)
	pageURL := projectPath + "/read?" + url.Values{"path": {patch["path"].(string)}, "version": {"1"}, "offset": {"1"}, "limit": {"1"}}.Encode()
	page := objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, pageURL, nil, bearer("file-viewer", ""), http.StatusOK), "data")
	if page["content"] != "\u597d" || page["next_offset"] != float64(2) || page["truncated"] != true {
		t.Fatalf("read page: %+v", page)
	}
	downloadURL := projectPath + "/content?" + url.Values{"path": {patch["path"].(string)}, "version": {"1"}}.Encode()
	request := httptest.NewRequest(http.MethodGet, downloadURL, nil)
	request.Header.Set("Authorization", "Bearer file-viewer")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != body["content"] {
		t.Fatalf("download: %d %s", response.Code, response.Body.String())
	}
	kind, parameters, err := mime.ParseMediaType(response.Header().Get("Content-Disposition"))
	if err != nil || kind != "attachment" || parameters["filename"] != "\u4e2d\u6587 & notes.txt" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("unsafe download headers: %+v", response.Header())
	}
	for _, route := range []string{projectPath, pageURL, downloadURL} {
		performJSONWithHeaders(t, handler, http.MethodGet, route, nil, bearer("file-foreign", ""), http.StatusNotFound)
	}
	for _, query := range []string{"path=../invalid", "path=notes&version=no", "path=notes&limit=0", "path=notes&offset=-1"} {
		performJSONWithHeaders(t, handler, http.MethodGet, projectPath+"/read?"+query, nil, bearer("file-owner", ""), http.StatusBadRequest)
	}
	performJSONWithHeaders(t, handler, http.MethodGet, projectPath+"/read?path=missing.txt", nil, bearer("file-owner", ""), http.StatusNotFound)
	body["sdk_tool_call_id"] = "spoofed"
	performJSONWithHeaders(t, handler, http.MethodPost, patchPath, body, serviceHeaders, http.StatusConflict)
}
