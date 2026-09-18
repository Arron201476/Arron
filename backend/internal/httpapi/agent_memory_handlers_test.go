package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestMemoryArchiveRecoveryRejectsDelegationAndMalformedBody(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "recovery-test-token")
	for _, tc := range []struct {
		name, token, activity, body string
		status                      int
	}{
		{"unauthenticated", "", "", "PRIVATE_SOURCE_BODY", http.StatusUnauthorized},
		{"delegated", "recovery-test-token", "turn-id", "PRIVATE_SOURCE_BODY", http.StatusForbidden},
		{"unknown-field", "recovery-test-token", "", `{"unknown":"PRIVATE_SOURCE_BODY"}`, http.StatusBadRequest},
		{"multiple-values", "recovery-test-token", "", `{} {}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/internal/v1/agent-memory/archive-recover", strings.NewReader(tc.body))
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.activity != "" {
				request.Header.Set("X-Agent-Turn-ID", tc.activity)
			}
			response := httptest.NewRecorder()
			(&Server{}).recoverAgentMemoryArchive(response, request)
			if response.Code != tc.status || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "PRIVATE_SOURCE_BODY") {
				t.Fatalf("recovery admission: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestMemoryArchiveRecoveryStatusPreservesRetryDecision(t *testing.T) {
	for _, code := range []string{"AGENT_MEMORY_ARCHIVE_NOT_READY", "AGENT_MEMORY_ARCHIVE_SOURCE_REVOKED"} {
		response := httptest.NewRecorder()
		writeRuntimeError(response, &businessruntime.DomainError{Code: code, Message: "Memory archive source state changed."})
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), code) {
			t.Fatalf("lost recovery state: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestLegacyMemoryArchiveWriteCannotBypassConsent(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "archive-test-token")
	server := &Server{}
	for _, authorized := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodPost, "/internal/v1/agent-memory/rollouts", strings.NewReader("PRIVATE_SOURCE_BODY"))
		if authorized {
			request.Header.Set("Authorization", "Bearer archive-test-token")
		}
		response := httptest.NewRecorder()
		server.saveAgentMemoryRollout(response, request)
		expected := http.StatusUnauthorized
		if authorized {
			expected = http.StatusGone
		}
		if response.Code != expected || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "PRIVATE_SOURCE_BODY") {
			t.Fatalf("legacy archive response: %d %s", response.Code, response.Body.String())
		}
		if authorized && !strings.Contains(response.Body.String(), "AGENT_MEMORY_ARCHIVE_CONSENT_REQUIRED") {
			t.Fatal("legacy caller did not receive explicit migration failure")
		}
	}
}

func TestAgentMemoryHTTPIdentityReceiptsAndForget(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "memory-worker-test-token")
	registry := capability.NewEmptyRegistry()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "memory.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	auth, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
		{Token: "owner", UserID: "owner", WorkspaceID: "workspace", Role: identity.RoleOwner},
		{Token: "editor", UserID: "editor", WorkspaceID: "workspace", Role: identity.RoleEditor},
		{Token: "viewer", UserID: "viewer", WorkspaceID: "workspace", Role: identity.RoleViewer},
		{Token: "foreign", UserID: "foreign", WorkspaceID: "foreign", Role: identity.RoleOwner},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, nil, nil)
	if err := server.ConfigureAuthentication(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "workspace", Role: identity.RoleOwner})
	project, err := store.CreateProject(ctx, "Memory fixture")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/projects/" + project.ProjectID + "/memory"
	call := func(method, token string, body any, status int) string {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("%s %s: got %d want %d: %s", method, token, response.Code, status, response.Body.String())
		}
		if status == http.StatusOK && response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("memory response is cacheable")
		}
		return response.Body.String()
	}
	command := map[string]any{"expected_version": 0, "files": map[string]string{"memory_summary.md": "PRIVATE_MEMORY_BODY"}, "enabled": true, "forget": false, "request_id": "save-1"}
	path += "/preferences"
	call(http.MethodGet, "owner", nil, http.StatusOK)
	consent := map[string]any{"archive_enabled": true, "generate_enabled": true, "expected_revision": 0, "request_id": "consent-1"}
	call(http.MethodPut, "viewer", consent, http.StatusForbidden)
	call(http.MethodPut, "owner", map[string]any{"archive_enabled": true, "expected_revision": 0, "request_id": "incomplete"}, http.StatusBadRequest)
	firstConsent := call(http.MethodPut, "owner", consent, http.StatusOK)
	if repeated := call(http.MethodPut, "owner", consent, http.StatusOK); repeated != firstConsent {
		t.Fatal("consent receipt changed")
	}
	path = strings.TrimSuffix(path, "/preferences")
	first := call(http.MethodPut, "owner", command, http.StatusOK)
	if !strings.Contains(first, `"request_id":"save-1"`) {
		t.Fatal("missing original receipt identity")
	}
	if again := call(http.MethodPut, "owner", command, http.StatusOK); again != first {
		t.Fatal("idempotent receipt changed")
	}
	if other := call(http.MethodGet, "editor", nil, http.StatusOK); strings.Contains(other, "PRIVATE_MEMORY_BODY") {
		t.Fatal("private memory leaked")
	}
	call(http.MethodGet, "foreign", nil, http.StatusNotFound)
	call(http.MethodPut, "viewer", command, http.StatusForbidden)
	command["project_id"] = "another-project"
	call(http.MethodPut, "owner", command, http.StatusBadRequest)
	delete(command, "project_id")
	call(http.MethodPut, "owner", map[string]any{"expected_version": 1, "files": map[string]string{}, "enabled": false, "forget": true, "request_id": "forget-1"}, http.StatusOK)
	if receipt := call(http.MethodPut, "owner", command, http.StatusOK); strings.Contains(receipt, "PRIVATE_MEMORY_BODY") || !strings.Contains(receipt, `"forgotten":true`) {
		t.Fatal("forgotten body restored through original receipt")
	}
	command["request_id"] = "stale-2"
	call(http.MethodPut, "owner", command, http.StatusConflict)
	path += "/generations"
	call(http.MethodGet, "owner", nil, http.StatusOK)
	call(http.MethodPost, "owner", map[string]any{}, http.StatusBadRequest)
	queue := map[string]any{"activity_key": "turn:missing", "segment_id": "segment", "source_hash": strings.Repeat("a", 64)}
	call(http.MethodPost, "viewer", queue, http.StatusForbidden)
	call(http.MethodPost, "owner", queue, http.StatusNotFound)
	queue["rollout_jsonl"] = "untrusted source body"
	call(http.MethodPost, "owner", queue, http.StatusBadRequest)
	path += "/missing"
	call(http.MethodGet, "owner", nil, http.StatusNotFound)
	call(http.MethodGet, "editor", nil, http.StatusNotFound)
	path += "/control"
	call(http.MethodPost, "owner", map[string]any{"action": "pause", "expected_revision": 1}, http.StatusNotFound)
	call(http.MethodPost, "viewer", map[string]any{"action": "pause", "expected_revision": 1}, http.StatusForbidden)
	call(http.MethodPost, "owner", map[string]any{"action": "finish", "expected_revision": 1}, http.StatusBadRequest)
	path = "/internal/v1/agent-memory/generations/claim"
	claimRequest := map[string]any{"worker_id": "fixture-worker", "model_id": "fixture-model", "lease_seconds": 60}
	call(http.MethodPost, "owner", claimRequest, http.StatusUnauthorized)
	if result := call(http.MethodPost, "memory-worker-test-token", claimRequest, http.StatusOK); !strings.Contains(result, `"claim":null`) {
		t.Fatal("empty generation queue did not return an explicit empty claim")
	}
	path = "/internal/v1/agent-memory/generations/start"
	call(http.MethodPost, "memory-worker-test-token", map[string]any{"generation_id": "missing", "worker_id": "fixture-worker", "attempt_token": "fixture-token", "attempt": 1}, http.StatusNotFound)
	path = "/internal/v1/agent-memory/generations/finish"
	call(http.MethodPost, "memory-worker-test-token", map[string]any{}, http.StatusNotFound)
}
