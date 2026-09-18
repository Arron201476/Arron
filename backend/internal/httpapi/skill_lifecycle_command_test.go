package httpapi

import (
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestSkillLifecycleHTTPRequiresSnapshotAndReturnsOriginalReceipt(t *testing.T) {
	for _, action := range []string{"enable", "disable", "activate", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
			if err != nil {
				t.Fatal(err)
			}
			store, err := businessruntime.Open(filepath.Join(t.TempDir(), "skill.db"), registry)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			handler := NewWithRuntime(shell.New(registry), store, nil).Handler()
			archive := buildSkillAPIArchive(t, "http-lifecycle", "http_lifecycle", "1.0.0", "original")
			item := objectAt(t, performSkillZIPRequest(t, handler, http.MethodPost, "/api/v1/skills", archive, nil, http.StatusCreated), "data")
			id := stringAt(t, item, "skill_installation_id")
			if action == "enable" {
				item = objectAt(t, performSkillLifecycle(t, handler, http.MethodPost, "/api/v1/skills/"+id+"/disable"), "data")
			}
			path, method := "/api/v1/skills/"+id+"/"+action, http.MethodPost
			if action == "activate" {
				path = "/api/v1/skills/" + id + "/versions/1.0.0/activate"
			}
			if action == "uninstall" {
				path, method = "/api/v1/skills/"+id, http.MethodDelete
			}
			body := skillLifecycleBody(t, item)
			key := "47010000-0000-4000-8000-000000000001"
			headers := map[string]string{"Idempotency-Key": key}
			performJSONWithHeaders(t, handler, method, path, map[string]any{}, headers, http.StatusBadRequest)
			performJSONWithHeaders(t, handler, method, path, body, map[string]string{"Idempotency-Key": ""}, http.StatusBadRequest)
			first := objectAt(t, performJSONWithHeaders(t, handler, method, path, body, headers, http.StatusOK), "data")
			receipt := objectAt(t, first, "receipt")
			if stringAt(t, receipt, "request_id") != key || stringAt(t, receipt, "action") != action {
				t.Fatalf("wrong receipt: %+v", receipt)
			}
			if action == "uninstall" {
				performSkillZIPRequest(t, handler, http.MethodPost, "/api/v1/skills", archive, nil, http.StatusCreated)
			} else {
				performSkillLifecycle(t, handler, http.MethodDelete, "/api/v1/skills/"+id)
			}
			current := objectAt(t, performJSON(t, handler, http.MethodGet, "/api/v1/skills/"+id, nil, http.StatusOK), "data")
			retried := objectAt(t, performJSONWithHeaders(t, handler, method, path, body, headers, http.StatusOK), "data")
			if !reflect.DeepEqual(receipt, objectAt(t, retried, "receipt")) || !reflect.DeepEqual(current, objectAt(t, retried, "installation")) {
				t.Fatalf("retry replayed old operation: %+v", retried)
			}
			performJSONWithHeaders(t, handler, method, path, body, map[string]string{"Idempotency-Key": "47010000-0000-4000-8000-000000000002"}, http.StatusConflict)
		})
	}
}
