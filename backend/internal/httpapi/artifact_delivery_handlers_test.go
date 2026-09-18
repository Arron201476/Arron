package httpapi

import (
	"context"
	"encoding/json"
	"mime"
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

func TestArtifactDeliveryHTTPAuthenticationScopeAndDownload(t *testing.T) {
	registry := capability.NewEmptyRegistry()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "delivery.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authenticator, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
		{Token: "delivery-owner", UserID: "delivery_owner", WorkspaceID: "delivery_workspace", Role: identity.RoleOwner},
		{Token: "delivery-viewer", UserID: "delivery_viewer", WorkspaceID: "delivery_workspace", Role: identity.RoleViewer},
		{Token: "delivery-foreign", UserID: "delivery_foreign", WorkspaceID: "other_workspace", Role: identity.RoleOwner},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntime(shell.New(registry), store, nil)
	if err := server.ConfigureAuthentication(context.Background(), authenticator); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	project := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/projects", map[string]any{"title": "Document downloads"}, bearer("delivery-owner", "11111111-1111-4111-8111-111111111111"), http.StatusCreated), "data")
	artifact, err := store.CreateGenericArtifact(context.Background(), businessruntime.CreateGenericArtifactCommand{
		CommandMeta: businessruntime.CommandMeta{Scope: stringAt(t, project, "project_id"), IdempotencyKey: "fixture", CommandType: "create_generic_artifact", RequestHash: "fixture"},
		ProjectID:   stringAt(t, project, "project_id"), ConversationID: stringAt(t, project, "primary_conversation_id"),
		Draft: agentcontract.ArtifactDraft{ArtifactType: "generic_document", Title: "\u4e2d\u6587 & <title>", Payload: json.RawMessage(`{"content_markdown":"# Original\n\nbody"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := "/api/v1/artifact-versions/" + artifact.CurrentVersionID
	delivery := objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, root+"/delivery", nil, bearer("delivery-viewer", ""), http.StatusOK), "data")
	if delivery["artifact_id"] != artifact.ArtifactID || delivery["artifact_version_id"] != artifact.CurrentVersionID || len(delivery["downloads"].([]any)) != 4 {
		t.Fatalf("delivery: %+v", delivery)
	}
	request := httptest.NewRequest(http.MethodGet, root+"/download?format=md", nil)
	request.Header.Set("Authorization", "Bearer delivery-viewer")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	kind, parameters, err := mime.ParseMediaType(response.Header().Get("Content-Disposition"))
	if response.Code != http.StatusOK || response.Body.String() != "# Original\n\nbody" || err != nil || kind != "attachment" || !strings.HasSuffix(parameters["filename"], ".md") || !strings.Contains(parameters["filename"], "\u4e2d\u6587") {
		t.Fatalf("download: %d %+v %q %v", response.Code, response.Header(), response.Body.String(), err)
	}
	if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("unsafe headers: %+v", response.Header())
	}
	for _, route := range []string{root + "/delivery", root + "/download?format=json"} {
		performJSONWithHeaders(t, handler, http.MethodGet, route, nil, nil, http.StatusUnauthorized)
		performJSONWithHeaders(t, handler, http.MethodGet, route, nil, bearer("delivery-foreign", ""), http.StatusNotFound)
	}
	performJSONWithHeaders(t, handler, http.MethodGet, root+"/download?format=pdf", nil, bearer("delivery-owner", ""), http.StatusBadRequest)
	performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/artifact-versions/missing/delivery", nil, bearer("delivery-owner", ""), http.StatusNotFound)
}
