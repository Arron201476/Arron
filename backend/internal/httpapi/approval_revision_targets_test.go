package httpapi

import (
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"testing"
)

// Runs inside the checkpoint HTTP fixture, against its isolated Store only.
func assertApprovalRevisionHTTPAdmission(t *testing.T, server *Server, approval map[string]any, versionID string) {
	t.Helper()
	executor := &revisionAdmissionExecutor{}
	previous := server.revisions
	server.revisions = executor
	defer func() { server.revisions = previous }()
	handler := server.Handler()
	approvalID := stringAt(t, approval, "approval_request_id")
	path := "/api/v1/approvals/" + approvalID + "/regeneration-requests"
	data := objectAt(t, performJSON(t, handler, http.MethodGet, "/api/v1/approvals/"+approvalID+"/revision-targets", nil, http.StatusOK), "data")
	targets := arrayAt(t, data, "targets")
	if len(targets) != 1 || stringAt(t, targets[0].(map[string]any), "artifact_version_id") != versionID {
		t.Fatalf("approval revision targets: %+v", data)
	}
	for index, explicit := range []bool{false, true} {
		body := map[string]any{"action": "request_ai_revision", "instruction": "Improve the selected output.",
			"expected_approval_version": approval["version"], "subject_snapshot_hash": approval["subject_snapshot_hash"]}
		if explicit {
			body["target_artifact_version_id"] = versionID
		}
		headers := map[string]string{"Idempotency-Key": fmt.Sprintf("e0000000-0000-4000-8000-%012d", index+1)}
		first := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, path, body, headers, http.StatusAccepted), "data")
		if first["status"] != "queued" || first["base_artifact_version_id"] != versionID || first["source_approval_request_id"] != approvalID {
			t.Fatalf("revision admission: %+v", first)
		}
		if executor.calls != 0 {
			t.Fatal("approval HTTP command synchronously invoked the SDK")
		}
		revisionID := stringAt(t, first, "revision_request_id")
		persisted := objectAt(t, performJSON(t, handler, http.MethodGet, "/api/v1/revision-requests/"+revisionID, nil, http.StatusOK), "data")
		if persisted["source_approval_request_id"] != approvalID {
			t.Fatalf("source approval lost: %+v", persisted)
		}
		if !explicit {
			body["target_artifact_version_id"] = ""
		}
		retry := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, path, body, headers, http.StatusAccepted), "data")
		if !reflect.DeepEqual(first, retry) {
			t.Fatal("omitted or explicit target changed original receipt")
		}
		wrong := maps.Clone(body)
		wrong["target_artifact_version_id"] = "another-version"
		performJSONWithHeaders(t, handler, http.MethodPost, path, wrong, headers, http.StatusConflict)
		wrong["action"] = "regenerate_artifact"
		performJSONWithHeaders(t, handler, http.MethodPost, path, wrong, headers, http.StatusBadRequest)
		cancelHeaders := map[string]string{"Idempotency-Key": fmt.Sprintf("e0000000-0000-4000-8000-%012d", index+11)}
		performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/revision-requests/"+revisionID+"/cancel", map[string]any{"expected_revision_version": first["version"]}, cancelHeaders, http.StatusOK)
	}
	if executor.calls != 0 {
		t.Fatal("admission retry invoked execution")
	}
}
