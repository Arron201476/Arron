package httpapi

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackgroundInputHTTPPermissionsAndDurableReceipts(t *testing.T) {
	database := filepath.Join(t.TempDir(), "input-http.db")
	store, handler := openBackgroundPauseHTTP(t, database)
	defer func() { store.Close() }()
	_, task := createBackgroundPauseHTTPTask(t, store)
	path := "/api/v1/agent-tasks/" + task.AgentTaskID + "/inputs"
	key := "11111111-1111-4111-8111-111111111111"
	body := map[string]any{"content": "New instruction"}
	performJSONWithHeaders(t, handler, http.MethodPost, path, body, nil, http.StatusUnauthorized)
	performJSONWithHeaders(t, handler, http.MethodPost, path, body, bearer("pause-viewer", key), http.StatusForbidden)
	performJSONWithHeaders(t, handler, http.MethodPost, path, body, bearer("pause-foreign", key), http.StatusNotFound)
	performJSONWithHeaders(t, handler, http.MethodPost, path, map[string]any{"content": "  "}, bearer("pause-owner", key), http.StatusBadRequest)
	performJSONWithHeaders(t, handler, http.MethodPost, path, map[string]any{"content": strings.Repeat("x", 33<<10)}, bearer("pause-owner", key), http.StatusBadRequest)
	performJSONWithHeaders(t, handler, http.MethodPost, path, map[string]any{"content": "text", "attempt_token": "forged"}, bearer("pause-owner", key), http.StatusBadRequest)
	first := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, path, body, bearer("pause-owner", key), http.StatusAccepted), "data")
	if first["status"] != "received" {
		t.Fatalf("premature receipt: %+v", first)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, handler = openBackgroundPauseHTTP(t, database)
	repeated := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, path, body, bearer("pause-owner", key), http.StatusAccepted), "data")
	if first["input_id"] != repeated["input_id"] {
		t.Fatalf("input duplicated: %+v", repeated)
	}
	response := objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/agent-tasks/"+task.AgentTaskID, nil, bearer("pause-owner", ""), http.StatusOK), "data")
	inputs, ok := response["additional_inputs"].([]any)
	if !ok || len(inputs) != 1 {
		t.Fatalf("missing public input: %+v", response)
	}
}

func TestBackgroundFailedInputHTTPReceiptUsesAuthenticatedAttempt(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "pause-service")
	database := filepath.Join(t.TempDir(), "failure-receipt.db")
	store, handler := openBackgroundPauseHTTP(t, database)
	defer func() { store.Close() }()
	_, task := createBackgroundPauseHTTPTask(t, store)
	base := "/api/v1/agent-tasks/" + task.AgentTaskID
	input := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, base+"/inputs", map[string]any{"content": "Model received this"}, bearer("pause-owner", "12222222-2222-4222-8222-222222222222"), http.StatusAccepted), "data")
	claim := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tasks/claims", map[string]any{"worker_id": "failure-worker", "provider_id": "sdk", "model_id": "fixture", "lease_seconds": 300}, bearer("pause-service", ""), http.StatusOK), "data")
	attemptID := stringAt(t, objectAt(t, claim, "attempt"), "agent_task_attempt_id")
	path := "/internal/v1/agent-task-attempts/" + attemptID + "/failures"
	body := map[string]any{"error_code": "AGENT_OUTPUT_GUARDRAIL_REJECTED", "error_message": "Output rejected", "retryable": false, "included_input_ids": []string{stringAt(t, input, "input_id")}}
	performJSONWithHeaders(t, handler, http.MethodPost, path, body, bearer("pause-owner", ""), http.StatusUnauthorized)
	performJSONWithHeaders(t, handler, http.MethodPost, path, body, map[string]string{"Authorization": "Bearer pause-service", "X-Attempt-Token": "invalid"}, http.StatusBadRequest)
	performJSONWithHeaders(t, handler, http.MethodPost, path, body, map[string]string{"Authorization": "Bearer pause-service", "X-Attempt-Token": stringAt(t, claim, "attempt_token")}, http.StatusOK)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, handler = openBackgroundPauseHTTP(t, database)
	response := objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, base, nil, bearer("pause-owner", ""), http.StatusOK), "data")
	view := arrayAt(t, response, "additional_inputs")[0].(map[string]any)
	if response["status"] != "failed" || view["status"] != "included" || view["included_at"] == nil {
		t.Fatal("authenticated failure lost its receipt", response)
	}
}
