package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/shell"
)

func TestHealthWorksWithEmptyRegistry(t *testing.T) {
	server := New(shell.New(capability.NewEmptyRegistry()), nil)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data shell.Readiness `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.Data.AgentCoreAvailable || payload.Data.DomainCapabilityCount != 0 {
		t.Fatalf("unexpected readiness: %+v", payload.Data)
	}
}

func TestCapabilityAPIUsesSafeProjection(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	server := New(shell.New(registry), nil)

	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d; body = %s", listResponse.Code, listResponse.Body.String())
	}
	var listPayload struct {
		Data struct {
			Items []capability.PublicCapability `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listPayload.Data.Items) != 5 {
		t.Fatalf("capability count = %d, want 5", len(listPayload.Data.Items))
	}
	for _, item := range listPayload.Data.Items {
		if item.CapabilityID == "outline_critic" {
			if item.Skill == nil || item.Skill.Instructions != "" || !item.EntryPolicy.AutoRoute {
				t.Fatalf("Skill list projection = %+v", item)
			}
		}
	}

	detailRequest := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities/video_reference_creation", nil)
	detailResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status = %d; body = %s", detailResponse.Code, detailResponse.Body.String())
	}
	body := detailResponse.Body.String()
	for _, forbidden := range []string{"prompt_ref", "rule_refs", "executor_ref", "schema_ref", "novel2script_agent_project"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("detail response leaks %q: %s", forbidden, body)
		}
	}

	skillRequest := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities/outline_critic", nil)
	skillResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(skillResponse, skillRequest)
	if skillResponse.Code != http.StatusOK {
		t.Fatalf("Skill detail status = %d; body = %s", skillResponse.Code, skillResponse.Body.String())
	}
	var skillPayload struct {
		Data capability.PublicDefinition `json:"data"`
	}
	if err := json.Unmarshal(skillResponse.Body.Bytes(), &skillPayload); err != nil {
		t.Fatalf("decode Skill detail: %v", err)
	}
	if skillPayload.Data.Skill == nil || !strings.Contains(skillPayload.Data.Skill.Instructions, "读取用户指定的大纲全文") {
		t.Fatalf("Skill detail did not disclose instructions: %+v", skillPayload.Data.Skill)
	}
}

func testRoot(t *testing.T) string {
	t.Helper()
	if projectRoot := strings.TrimSpace(os.Getenv("CONTENT_AGENT_TEST_PROJECT_ROOT")); projectRoot != "" {
		return projectRoot
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
