package httpapi

import (
	"net/http"
	"path/filepath"
	"testing"
)

func TestExecutionInputChangesHTTPAuthorizationRebuildAndIdempotency(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "pause-service")
	database := filepath.Join(t.TempDir(), "changes.db")
	store, handler := openBackgroundPauseHTTP(t, database)
	defer func() { store.Close() }()
	project, task := createBackgroundPauseHTTPTask(t, store)
	first := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/agent-tasks/"+task.AgentTaskID+"/inputs", map[string]any{"content": "Original"}, bearer("pause-owner", "c1111111-1111-4111-8111-111111111111"), http.StatusAccepted), "data")
	if first["can_modify"] != true {
		t.Fatal("fresh unclaimed input has no change action")
	}
	path := "/api/v1/projects/" + project.ProjectID + "/execution-input-changes"
	body := map[string]any{"mode": "background_task", "execution_id": task.AgentTaskID, "input_id": first["input_id"], "action": "revise", "content": "Revised"}
	key := "c2222222-2222-4222-8222-222222222222"
	performJSONWithHeaders(t, handler, http.MethodPost, path, body, nil, http.StatusUnauthorized)
	for _, token := range []string{"pause-viewer", "pause-service"} {
		performJSONWithHeaders(t, handler, http.MethodPost, path, body, bearer(token, key), http.StatusForbidden)
	}
	performJSONWithHeaders(t, handler, http.MethodPost, path, body, bearer("pause-foreign", key), http.StatusNotFound)
	performJSONWithHeaders(t, handler, http.MethodPost, path, body, map[string]string{"Authorization": "Bearer pause-owner", "Idempotency-Key": ""}, http.StatusBadRequest)
	body["status"] = "included"
	performJSONWithHeaders(t, handler, http.MethodPost, path, body, bearer("pause-owner", key), http.StatusBadRequest)
	delete(body, "status")
	result := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, path, body, bearer("pause-owner", key), http.StatusOK), "data")
	if result["status"] != "superseded" || result["replacement_input_id"] == "" {
		t.Fatal("missing revision receipt", result)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, handler = openBackgroundPauseHTTP(t, database)
	again := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, path, body, bearer("pause-owner", key), http.StatusOK), "data")
	if again["replacement_input_id"] != result["replacement_input_id"] {
		t.Fatal("duplicate revision after server rebuild")
	}
	body["content"] = "Different"
	performJSONWithHeaders(t, handler, http.MethodPost, path, body, bearer("pause-owner", key), http.StatusBadRequest)
	view := objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/agent-tasks/"+task.AgentTaskID, nil, bearer("pause-viewer", ""), http.StatusOK), "data")
	inputs := view["additional_inputs"].([]any)
	if len(inputs) != 2 || inputs[0].(map[string]any)["status"] != "superseded" || inputs[0].(map[string]any)["content"] != "Original" || inputs[1].(map[string]any)["content"] != "Revised" || inputs[1].(map[string]any)["can_modify"] != false {
		t.Fatal("history or viewer permission changed", inputs)
	}
}
