//go:build sdk_integration

package httpapi

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Change only unclaimed inputs between real Worker processes, through user APIs.
func revisePendingInputHTTP(t *testing.T, handler http.Handler, project, mode, execution, original, content, token string, attach ...bool) string {
	t.Helper()
	headers := func(key string) map[string]string {
		h := map[string]string{"Idempotency-Key": key}
		if token != "" {
			h["Authorization"] = "Bearer " + token
		}
		return h
	}
	path := "/api/v1/projects/" + project + "/execution-input-changes"
	change := func(id, action, content, key string) map[string]any {
		return objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, path, map[string]any{"mode": mode, "execution_id": execution, "input_id": id, "action": action, "content": content}, headers(key), http.StatusOK), "data")
	}
	change(original, "withdraw", "", "d1111111-1111-4111-8111-111111111111")
	appendPath := "/api/v1/agent-turns/" + execution + "/inputs"
	if mode == "background_task" {
		appendPath = "/api/v1/agent-tasks/" + execution + "/inputs"
	} else if mode == "stateful_workflow" {
		appendPath = "/api/v1/projects/" + project + "/execution-attempts/" + execution + "/inputs"
	}
	body := map[string]any{"content": "REMOVED_BEFORE_MODEL"}
	if len(attach) > 0 && attach[0] {
		body["attachment_refs"] = uploadAdditionalInputMaterials(t, handler, project, token)
	}
	input := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, appendPath, body, headers("d2222222-2222-4222-8222-222222222222"), http.StatusAccepted), "data")
	result := change(input["input_id"].(string), "revise", content, "d3333333-3333-4333-8333-333333333333")
	return result["replacement_input_id"].(string)
}

func uploadAdditionalInputMaterials(t *testing.T, handler http.Handler, project, token string) []map[string]any {
	t.Helper()
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	refs := []map[string]any{}
	for index, item := range []struct {
		name, kind, mime string
		content          []byte
	}{
		{"additional-source.txt", "text", "text/plain", []byte("FROZEN_ADDITIONAL_ATTACHMENT_SOURCE")},
		{"additional-image.png", "image", "image/png", picture.Bytes()},
	} {
		key := []string{"f1111111-1111-4111-8111-111111111111", "f2222222-2222-4222-8222-222222222222"}[index]
		headers := map[string]string{"Idempotency-Key": key}
		if token != "" {
			headers["Authorization"] = "Bearer " + token
		}
		upload := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/projects/"+project+"/upload-sessions", map[string]any{"items": []map[string]any{{"client_item_key": item.name, "kind": item.kind, "original_filename": item.name, "declared_mime_type": item.mime, "declared_size_bytes": len(item.content)}}}, headers, http.StatusCreated), "data")
		entry := upload["items"].([]any)[0].(map[string]any)
		path := "/api/v1/upload-items/" + stringAt(t, entry, "upload_item_id")
		request := httptest.NewRequest(http.MethodPut, path+"/content", bytes.NewReader(item.content))
		request.RemoteAddr = "127.0.0.1:42300"
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		request.Header.Set("Content-Type", item.mime)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("attachment upload failed: %d %s", response.Code, response.Body.String())
		}
		completed := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, path+"/complete", nil, headers, http.StatusCreated), "data")
		asset, snapshot := objectAt(t, completed, "asset"), objectAt(t, completed, "asset_snapshot")
		refs = append(refs, map[string]any{"asset_id": stringAt(t, asset, "asset_id"), "asset_snapshot_id": stringAt(t, snapshot, "asset_snapshot_id")})
	}
	return refs
}
