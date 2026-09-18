package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"content-agent/backend/internal/capability"
	agentruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestSkillPackageHTTPOriginalKeyAndSnapshotContract(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := agentruntime.Open(filepath.Join(t.TempDir(), "packages.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()
	upload := func(path string, archive []byte, key, expected string, status int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(archive))
		r.Header.Set("Content-Type", "application/zip")
		r.Header.Set("Content-Disposition", "attachment; filename*=UTF-8''%E6%8A%80%E8%83%BD.zip")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if expected != "" {
			r.Header.Set("X-Skill-Expected", expected)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	archive := buildSkillAPIArchive(t, "http-package", "http_package", "1.0.0", "FIRST")
	newer := buildSkillAPIArchive(t, "http-package", "http_package", "2.0.0", "SECOND")
	upload("/api/v1/skills", archive, "", "", http.StatusBadRequest)
	key := nextSkillPackageTestKey()
	first := objectAt(t, upload("/api/v1/skills", archive, key, "", http.StatusCreated), "data")
	installed := objectAt(t, first, "installation")
	path := "/api/v1/skills/" + stringAt(t, installed, "skill_installation_id") + "/versions"
	if stringAt(t, objectAt(t, arrayAt(t, installed, "versions")[0].(map[string]any), "manifest"), "name") == "" {
		t.Fatal("missing package metadata")
	}
	expectedBytes, err := json.Marshal(skillLifecycleBody(t, installed)["expected"])
	if err != nil {
		t.Fatal(err)
	}
	expected := string(expectedBytes)
	for _, raw := range []string{"", "null", "{}", expected + " {}", `{"unknown":true}`} {
		upload(path, newer, nextSkillPackageTestKey(), raw, http.StatusBadRequest)
	}
	upgradeKey := nextSkillPackageTestKey()
	second := objectAt(t, upload(path, newer, upgradeKey, expected, http.StatusCreated), "data")
	if stringAt(t, objectAt(t, second, "receipt"), "action") != "upgrade_zip" {
		t.Fatal("missing upgrade receipt")
	}
	retry := objectAt(t, upload("/api/v1/skills", archive, key, "", http.StatusCreated), "data")
	if !reflect.DeepEqual(first["receipt"], retry["receipt"]) || !reflect.DeepEqual(second["installation"], retry["installation"]) {
		t.Fatalf("old install replayed newer state: %+v", retry)
	}
	upload(path, newer, nextSkillPackageTestKey(), expected, http.StatusConflict)
	upload("/api/v1/skills", newer, key, "", http.StatusConflict)
	upload("/api/v1/skills", newer, nextSkillPackageTestKey(), "", http.StatusConflict)
	versions := arrayAt(t, installed, "versions")
	if stringAt(t, versions[0].(map[string]any), "source_name") != "技能.zip" {
		t.Fatal("Unicode filename was not decoded")
	}
}
