package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestDirectorySkillManagementHTTPAPI(t *testing.T) {
	root := t.TempDir()
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t), SkillRoots: []capability.SkillRoot{{Scope: capability.SkillScopeWorkspace, Path: root, Priority: 200}}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()
	directory := filepath.Join(root, "api-directory")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("---\nname: api-directory\ndescription: Directory fixture.\n---\nFollow these instructions.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	performJSON(t, handler, http.MethodPost, "/api/v1/skills/refresh", nil, http.StatusOK)
	definition := objectAt(t, performJSON(t, handler, http.MethodGet, "/api/v1/capabilities/api_directory", nil, http.StatusOK), "data")
	version := stringAt(t, definition, "version")
	hash := stringAt(t, objectAt(t, definition, "skill"), "content_hash")
	path := "/api/v1/skills/discovered/api_directory/install"
	performJSON(t, handler, http.MethodPost, path, map[string]any{"version": version, "content_hash": "stale"}, http.StatusConflict)
	performJSON(t, handler, http.MethodPost, path, map[string]any{"version": version, "content_hash": hash, "path": directory}, http.StatusBadRequest)
	installed := objectAt(t, performSkillPackageJSON(t, handler, path, map[string]any{"version": version, "content_hash": hash}, "", http.StatusCreated), "data")
	if stringAt(t, installed, "capability_id") != "api_directory" || stringAt(t, installed, "created_by") != identity.DefaultUserID {
		t.Fatalf("installed: %#v", installed)
	}
	performSkillPackageJSON(t, handler, "/api/v1/skills/discovered/missing/install", map[string]any{"version": version, "content_hash": hash}, "", http.StatusNotFound)
	installationID := stringAt(t, installed, "skill_installation_id")
	updatePath := "/api/v1/skills/" + installationID + "/directory-update"
	preview := objectAt(t, performJSON(t, handler, http.MethodGet, updatePath, nil, http.StatusOK), "data")
	if stringAt(t, preview, "status") != "current" {
		t.Fatalf("current preview: %+v", preview)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("---\nname: api-directory\ndescription: Updated directory.\n---\nUse the updated instructions.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	preview = objectAt(t, performJSON(t, handler, http.MethodGet, updatePath, nil, http.StatusOK), "data")
	if stringAt(t, preview, "status") != "available" {
		t.Fatalf("available preview: %+v", preview)
	}
	payload := map[string]any{"expected_active_version_id": stringAt(t, preview, "current_version_id"), "version": stringAt(t, preview, "version"), "content_hash": stringAt(t, preview, "content_hash")}
	forged := map[string]any{"expected_active_version_id": payload["expected_active_version_id"], "version": payload["version"], "content_hash": payload["content_hash"], "source_path": directory}
	performJSON(t, handler, http.MethodPost, updatePath, forged, http.StatusBadRequest)
	disabled := objectAt(t, performSkillLifecycle(t, handler, http.MethodPost, "/api/v1/skills/"+installationID+"/disable"), "data")
	payload["expected"] = skillLifecycleBody(t, disabled)["expected"]
	key := nextSkillPackageTestKey()
	updated := objectAt(t, performSkillPackageJSON(t, handler, updatePath, payload, key, http.StatusOK), "data")
	if updated["enabled"] != false || len(arrayAt(t, updated, "versions")) != 2 {
		t.Fatalf("directory update lost lifecycle state: %+v", updated)
	}
	performSkillPackageJSON(t, handler, updatePath, payload, key, http.StatusOK)
	performSkillPackageJSON(t, handler, updatePath, payload, "", http.StatusConflict)
}

func TestSkillManagementHTTPAPI(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("runtime.Open() error = %v", err)
	}
	defer store.Close()
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()

	list := performJSON(t, handler, http.MethodGet, "/api/v1/skills", nil, http.StatusOK)
	if items := arrayAt(t, objectAt(t, list, "data"), "items"); len(items) != 0 {
		t.Fatalf("initial items = %+v", items)
	}

	wrongType := httptest.NewRequest(http.MethodPost, "/api/v1/skills", bytes.NewReader([]byte("x")))
	wrongType.Header.Set("Content-Type", "application/json")
	wrongTypeResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongTypeResponse, wrongType)
	if wrongTypeResponse.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content type status = %d; body = %s", wrongTypeResponse.Code, wrongTypeResponse.Body.String())
	}

	versionOne := buildSkillAPIArchive(t, "api-test-skill", "api_test_skill", "1.0.0", "v1 instructions")
	installed := performSkillZIPRequest(
		t, handler, http.MethodPost, "/api/v1/skills", versionOne,
		map[string]string{
			"Content-Disposition": `attachment; filename="api-test-skill.zip"`,
			"X-Actor-Ref":         "spoofed_user",
		},
		http.StatusCreated,
	)
	installation := objectAt(t, installed, "data")
	installationID := stringAt(t, installation, "skill_installation_id")
	if stringAt(t, installation, "created_by") != identity.DefaultUserID ||
		stringAt(t, installation, "capability_id") != "api_test_skill" {
		t.Fatalf("installation = %+v", installation)
	}

	detail := performJSON(
		t, handler, http.MethodGet, "/api/v1/skills/"+installationID, nil, http.StatusOK,
	)
	if versions := arrayAt(t, objectAt(t, detail, "data"), "versions"); len(versions) != 1 {
		t.Fatalf("versions = %+v", versions)
	}

	disabled := performSkillLifecycle(
		t, handler, http.MethodPost, "/api/v1/skills/"+installationID+"/disable",
	)
	if enabled, ok := objectAt(t, disabled, "data")["enabled"].(bool); !ok || enabled {
		t.Fatalf("disabled response = %+v", disabled)
	}
	performSkillLifecycle(
		t, handler, http.MethodPost, "/api/v1/skills/"+installationID+"/enable",
	)

	versionTwo := buildSkillAPIArchive(t, "api-test-skill", "api_test_skill", "2.0.0", "v2 instructions")
	upgraded := performSkillZIPRequest(
		t, handler, http.MethodPost, "/api/v1/skills/"+installationID+"/versions",
		versionTwo, nil, http.StatusCreated,
	)
	if versions := arrayAt(t, objectAt(t, upgraded, "data"), "versions"); len(versions) != 2 {
		t.Fatalf("upgraded versions = %+v", versions)
	}
	activated := performSkillLifecycle(
		t, handler, http.MethodPost,
		"/api/v1/skills/"+installationID+"/versions/1.0.0/activate",
	)
	assertActiveSkillAPIVersion(t, objectAt(t, activated, "data"), "1.0.0")

	performSkillLifecycle(t, handler, http.MethodPost, "/api/v1/skills/"+installationID+"/disable")
	for _, version := range []string{"2.0.0", "1.0.0"} {
		switched := objectAt(t, performSkillLifecycle(t, handler, http.MethodPost, "/api/v1/skills/"+installationID+"/versions/"+version+"/activate"), "data")
		assertActiveSkillAPIVersion(t, switched, version)
		if switched["enabled"] != false || switched["registry_reason_code"] != "SKILL_DISABLED" {
			t.Fatalf("version selection enabled a disabled Skill: %+v", switched)
		}
	}
	performSkillLifecycle(t, handler, http.MethodPost, "/api/v1/skills/"+installationID+"/enable")

	mismatch := buildSkillAPIArchive(t, "other-api-skill", "other_api_skill", "1.0.0", "other")
	mismatchResponse := performSkillZIPRequest(
		t, handler, http.MethodPost, "/api/v1/skills/"+installationID+"/versions",
		mismatch, nil, http.StatusConflict,
	)
	if code := stringAt(t, objectAt(t, mismatchResponse, "error"), "code"); code != "SKILL_UPGRADE_ID_MISMATCH" {
		t.Fatalf("mismatch code = %s", code)
	}
	changedVersionOne := buildSkillAPIArchive(
		t, "api-test-skill", "api_test_skill", "1.0.0", "changed immutable content",
	)
	immutableResponse := performSkillZIPRequest(
		t, handler, http.MethodPost, "/api/v1/skills/"+installationID+"/versions",
		changedVersionOne, nil, http.StatusConflict,
	)
	if code := stringAt(t, objectAt(t, immutableResponse, "error"), "code"); code != "SKILL_VERSION_IMMUTABLE" {
		t.Fatalf("immutable code = %s", code)
	}

	corruptResponse := performSkillZIPRequest(
		t, handler, http.MethodPost, "/api/v1/skills", []byte("not zip"), nil,
		http.StatusUnprocessableEntity,
	)
	if code := stringAt(t, objectAt(t, corruptResponse, "error"), "code"); code != "SKILL_ARCHIVE_INVALID" {
		t.Fatalf("corrupt code = %s", code)
	}
	attempts := performJSON(
		t, handler, http.MethodGet, "/api/v1/skills/install-attempts?limit=100", nil, http.StatusOK,
	)
	if items := arrayAt(t, objectAt(t, attempts, "data"), "items"); len(items) != 5 {
		t.Fatalf("attempts = %+v, want five", items)
	}

	performSkillLifecycle(
		t, handler, http.MethodDelete, "/api/v1/skills/"+installationID,
	)
	list = performJSON(t, handler, http.MethodGet, "/api/v1/skills", nil, http.StatusOK)
	if items := arrayAt(t, objectAt(t, list, "data"), "items"); len(items) != 0 {
		t.Fatalf("visible after uninstall = %+v", items)
	}
	list = performJSON(
		t, handler, http.MethodGet, "/api/v1/skills?include_uninstalled=true", nil, http.StatusOK,
	)
	if items := arrayAt(t, objectAt(t, list, "data"), "items"); len(items) != 1 {
		t.Fatalf("all after uninstall = %+v", items)
	}
}

func skillLifecycleBody(t *testing.T, installation map[string]any) map[string]any {
	t.Helper()
	return map[string]any{"expected": map[string]any{"active_version_id": installation["active_version_id"], "enabled": installation["enabled"], "status": installation["status"], "event_count": len(arrayAt(t, installation, "events"))}}
}

func performSkillLifecycle(t *testing.T, handler http.Handler, method, path string) map[string]any {
	t.Helper()
	installationID := strings.Split(strings.Trim(path, "/"), "/")[3]
	installation := objectAt(t, performJSON(t, handler, http.MethodGet, "/api/v1/skills/"+installationID, nil, http.StatusOK), "data")
	headers := map[string]string{"Idempotency-Key": fmt.Sprintf("47000000-0000-4000-8000-%012d", len(arrayAt(t, installation, "events")))}
	result := objectAt(t, performJSONWithHeaders(t, handler, method, path, skillLifecycleBody(t, installation), headers, http.StatusOK), "data")
	receipt := objectAt(t, result, "receipt")
	if stringAt(t, receipt, "skill_installation_id") != installationID || stringAt(t, receipt, "skill_installation_event_id") == "" {
		t.Fatalf("invalid lifecycle receipt: %+v", receipt)
	}
	return map[string]any{"data": objectAt(t, result, "installation")}
}

func TestSkillManagementHTTPValidation(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()

	for _, path := range []string{
		"/api/v1/skills?include_uninstalled=invalid",
		"/api/v1/skills/install-attempts?limit=0",
	} {
		response := performJSON(t, handler, http.MethodGet, path, nil, http.StatusBadRequest)
		if code := stringAt(t, objectAt(t, response, "error"), "code"); code != "REQUEST_VALIDATION_FAILED" {
			t.Fatalf("%s code = %s", path, code)
		}
	}
	missing := performJSON(
		t, handler, http.MethodGet, "/api/v1/skills/ski_missing", nil, http.StatusNotFound,
	)
	if code := stringAt(t, objectAt(t, missing, "error"), "code"); code != "RESOURCE_NOT_FOUND" {
		t.Fatalf("missing code = %s", code)
	}
}

var skillPackageTestSequence atomic.Uint64

func nextSkillPackageTestKey() string {
	return fmt.Sprintf("47100000-0000-4000-8000-%012d", skillPackageTestSequence.Add(1))
}

func unwrapSkillPackageTestResult(t *testing.T, payload map[string]any, key string, status int) map[string]any {
	t.Helper()
	if status < 200 || status >= 300 {
		return payload
	}
	data := objectAt(t, payload, "data")
	receipt := objectAt(t, data, "receipt")
	installation := objectAt(t, data, "installation")
	if stringAt(t, receipt, "request_id") != key || stringAt(t, receipt, "skill_installation_id") != stringAt(t, installation, "skill_installation_id") || stringAt(t, receipt, "skill_installation_event_id") == "" {
		t.Fatalf("invalid package receipt: %+v", data)
	}
	return map[string]any{"data": installation}
}

func performSkillPackageJSON(t *testing.T, handler http.Handler, path string, body any, key string, status int) map[string]any {
	t.Helper()
	if key == "" {
		key = nextSkillPackageTestKey()
	}
	return unwrapSkillPackageTestResult(t, performJSONWithHeaders(t, handler, http.MethodPost, path, body, map[string]string{"Idempotency-Key": key}, status), key, status)
}

func performSkillZIPRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body []byte,
	headers map[string]string,
	wantStatus int,
) map[string]any {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/zip")
	request.Header.Set("X-Skill-Filename", "skill.zip")
	request.Header.Set("Idempotency-Key", nextSkillPackageTestKey())
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	if request.Header.Get("Idempotency-Key") == "" {
		request.Header.Set("Idempotency-Key", nextSkillPackageTestKey())
	}
	if strings.HasSuffix(request.URL.Path, "/versions") && request.Header.Get("X-Skill-Expected") == "" && (wantStatus < 300 || wantStatus == http.StatusConflict) {
		installation := objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, strings.TrimSuffix(request.URL.Path, "/versions"), nil, headers, http.StatusOK), "data")
		raw, err := json.Marshal(skillLifecycleBody(t, installation)["expected"])
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-Skill-Expected", string(raw))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body = %s", method, path, response.Code, wantStatus, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, response.Body.String())
	}
	return unwrapSkillPackageTestResult(t, payload, request.Header.Get("Idempotency-Key"), wantStatus)
}

func buildSkillAPIArchive(
	t *testing.T,
	name string,
	capabilityID string,
	version string,
	instructions string,
) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	files := map[string]string{
		"SKILL.md": "---\nname: " + name + "\ndescription: HTTP API Skill fixture.\n---\n\n" + instructions + "\n",
		"content-agent/manifest.json": `{"schema_version":"1.0.0","id":"` + capabilityID +
			`","version":"` + version + `","execution_mode":"inline","ui":{}}`,
	}
	for _, filename := range []string{"SKILL.md", "content-agent/manifest.json"} {
		output, err := writer.Create(filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := output.Write([]byte(files[filename])); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func assertActiveSkillAPIVersion(t *testing.T, installation map[string]any, want string) {
	t.Helper()
	activeID := stringAt(t, installation, "active_version_id")
	for _, raw := range arrayAt(t, installation, "versions") {
		version := raw.(map[string]any)
		if stringAt(t, version, "skill_version_id") == activeID {
			if got := stringAt(t, version, "version"); got != want {
				t.Fatalf("active version = %s, want %s", got, want)
			}
			return
		}
	}
	t.Fatalf("active version ID %s not found", activeID)
}
