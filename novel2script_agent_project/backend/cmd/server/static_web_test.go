package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSPAHandlerServesAssetsAndRouteFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<main>app-shell</main>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("window.APP_READY=true"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := newSPAHandler(root)
	for _, testCase := range []struct {
		path     string
		contains string
	}{
		{path: "/", contains: "app-shell"},
		{path: "/workspace/project_1", contains: "app-shell"},
		{path: "/assets/app.js", contains: "APP_READY"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, testCase.path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), testCase.contains) {
			t.Fatalf("unexpected response for %s: status=%d body=%q", testCase.path, response.Code, response.Body.String())
		}
	}
}

func TestSPAHandlerDoesNotMaskUnknownAPI(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("app-shell"), 0o600); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	newSPAHandler(root).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/unknown", nil))
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "app-shell") {
		t.Fatalf("unknown API was masked by the SPA: status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestConfiguredStateAndWebDirectories(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	webDir := filepath.Join(t.TempDir(), "web")
	t.Setenv("N2S_TRACE_DIR", stateDir)
	t.Setenv("N2S_WEB_DIR", webDir)
	if got := stateDirectory(); got != stateDir {
		t.Fatalf("unexpected state directory %q", got)
	}
	if got := webDirectory(); got != webDir {
		t.Fatalf("unexpected web directory %q", got)
	}
}
