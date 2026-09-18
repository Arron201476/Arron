package httpapi

import (
	"bytes"
	"context"
	"mime"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestAssetDeliveryHTTPUploadsReusableTextAndDownloadsExactBytes(t *testing.T) {
	registry := capability.NewEmptyRegistry()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "assets.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	auth, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
		{Token: "asset-owner", UserID: "asset_owner", WorkspaceID: "asset_workspace", Role: identity.RoleOwner},
		{Token: "asset-viewer", UserID: "asset_viewer", WorkspaceID: "asset_workspace", Role: identity.RoleViewer},
		{Token: "asset-other", UserID: "asset_other", WorkspaceID: "foreign_workspace", Role: identity.RoleOwner},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntime(shell.New(registry), store, nil)
	if err := server.ConfigureAuthentication(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	project := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/projects", map[string]any{"title": "Assets"}, bearer("asset-owner", "11111111-1111-4111-8111-111111111111"), http.StatusCreated), "data")
	for index, item := range []struct{ name, kind, mime, body string }{{"table.csv", "text", "text/csv", "name,value\n\u4e2d\u6587,42\n"}, {"data.json", "text", "application/json", `{"number":9007199254740993}`}, {"notes.md", "text", "text/markdown", "# Saved\n"}} {
		t.Run(item.name, func(t *testing.T) {
			key := []string{"22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"}[index]
			upload := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/projects/"+stringAt(t, project, "project_id")+"/upload-sessions", map[string]any{"items": []map[string]any{{"client_item_key": item.name, "kind": item.kind, "original_filename": item.name, "declared_mime_type": item.mime, "declared_size_bytes": len([]byte(item.body))}}}, bearer("asset-owner", key), http.StatusCreated), "data")
			entry := upload["items"].([]any)[0].(map[string]any)
			path := "/api/v1/upload-items/" + stringAt(t, entry, "upload_item_id")
			request := httptest.NewRequest(http.MethodPut, path+"/content", bytes.NewBufferString(item.body))
			request.Header.Set("Authorization", "Bearer asset-owner")
			request.Header.Set("Content-Type", item.mime)
			request.Header.Set("Idempotency-Key", key)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("upload: %d %s", response.Code, response.Body.String())
			}
			completed := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, path+"/complete", nil, bearer("asset-owner", key), http.StatusCreated), "data")
			asset, snapshot := objectAt(t, completed, "asset"), objectAt(t, completed, "asset_snapshot")
			assetID, snapshotID := stringAt(t, asset, "asset_id"), stringAt(t, snapshot, "asset_snapshot_id")
			text, err := store.GetParsedAssetText(context.Background(), assetID, snapshotID)
			if err != nil || text != item.body {
				t.Fatalf("reusable: %q %v", text, err)
			}
			download := "/api/v1/assets/" + assetID + "/download"
			request = httptest.NewRequest(http.MethodGet, download, nil)
			request.Header.Set("Authorization", "Bearer asset-viewer")
			response = httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			kind, params, err := mime.ParseMediaType(response.Header().Get("Content-Disposition"))
			if err != nil || kind != "attachment" || params["filename"] != item.name || response.Body.String() != item.body || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("download: %d %+v %q %v", response.Code, response.Header(), response.Body.String(), err)
			}
			performJSONWithHeaders(t, handler, http.MethodGet, download, nil, nil, http.StatusUnauthorized)
			performJSONWithHeaders(t, handler, http.MethodGet, download, nil, bearer("asset-other", ""), http.StatusNotFound)
			request = httptest.NewRequest(http.MethodGet, download, nil)
			request.Header.Set("Authorization", "Bearer asset-viewer")
			request.Header.Set("Range", "bytes=0-3")
			response = httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusPartialContent || response.Body.String() != item.body[:4] {
				t.Fatalf("range: %d %q", response.Code, response.Body.String())
			}
			contentPath := "/api/v1/assets/" + assetID + "/content?asset_snapshot_id="
			request = httptest.NewRequest(http.MethodGet, contentPath+snapshotID, nil)
			request.Header.Set("Authorization", "Bearer asset-viewer")
			response = httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Body.String() != item.body || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("snapshot content: %d %q", response.Code, response.Body.String())
			}
			performJSONWithHeaders(t, handler, http.MethodGet, contentPath+"stale-snapshot", nil, bearer("asset-viewer", ""), http.StatusConflict)
			performJSONWithHeaders(t, handler, http.MethodGet, contentPath+snapshotID, nil, bearer("asset-other", ""), http.StatusNotFound)
		})
	}
}
