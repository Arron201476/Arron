package httpapi

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"

	businessruntime "content-agent/backend/internal/runtime"
)

func TestNativeWorkspaceHTTPRecoveryRequiresIdentitySealedResourcesAndSnapshot(t *testing.T) {
	f := newNativeHTTPFixture(t)
	f.policy(t, true, nil)
	f.request(t, http.MethodGet, "recovery", nil, f.headers, http.StatusConflict)
	prepared := f.request(t, http.MethodPost, "manifest", strings.NewReader(`{"skills":[],"files":[]}`), f.headers, http.StatusOK)
	var manifest struct {
		Data businessruntime.NativeWorkspaceManifest `json:"data"`
	}
	if err := json.Unmarshal(prepared.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, "ensure", strings.NewReader(`{}`), f.headers, http.StatusOK)
	f.request(t, http.MethodPost, "manifest/seal", strings.NewReader(`{"manifest_hash":"`+manifest.Data.ManifestHash+`"}`), f.headers, http.StatusOK)
	f.request(t, http.MethodGet, "recovery", nil, f.headers, http.StatusConflict)
	headers := maps.Clone(f.headers)
	headers["Content-Type"] = "application/x-tar"
	headers["X-Workspace-Snapshot-Parent"] = "0"
	headers["X-Workspace-Snapshot-Sha256"] = f.engine.archive.SHA256
	f.request(t, http.MethodPut, "snapshots/"+strings.Repeat("d", 32), bytes.NewReader(f.engine.archive.Archive), headers, http.StatusOK)
	for _, key := range []string{"Authorization", "X-Workspace-Lease-Key", "X-Agent-Dispatch-Generation"} {
		wrong := maps.Clone(f.headers)
		wrong[key] = map[string]string{"Authorization": "Bearer native-owner-token", "X-Workspace-Lease-Key": strings.Repeat("b", 64), "X-Agent-Dispatch-Generation": "999"}[key]
		f.request(t, http.MethodGet, "recovery", nil, wrong, http.StatusForbidden)
	}
	result := f.request(t, http.MethodGet, "recovery", nil, f.headers, http.StatusOK)
	var recovered struct {
		Data businessruntime.NativeWorkspaceRecovery `json:"data"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &recovered); err != nil || recovered.Data.Manifest.ManifestHash != manifest.Data.ManifestHash || recovered.Data.Snapshot.Version != 1 || recovered.Data.Snapshot.SHA256 != f.engine.archive.SHA256 {
		t.Fatal("recovery did not return the exact authoritative references", err)
	}
	if f.engine.creates != 1 || f.engine.deletes != 0 || strings.Contains(result.Body.String(), f.headers["X-Workspace-Lease-Key"]) {
		t.Fatal("recovery operated the engine or exposed its holder key")
	}
}

func TestNativeManifestHTTPPrepareBeforeEnvironmentAndSeal(t *testing.T) {
	f := newNativeHTTPFixture(t)
	f.policy(t, true, nil)
	result := f.request(t, http.MethodPost, "manifest", strings.NewReader(`{"skills":[],"files":[]}`), f.headers, http.StatusOK)
	var envelope struct {
		Data businessruntime.NativeWorkspaceManifest `json:"data"`
	}
	if json.Unmarshal(result.Body.Bytes(), &envelope) != nil || envelope.Data.SessionID != f.session || len(envelope.Data.Files) != 0 || envelope.Data.ManifestHash == "" {
		t.Fatal("manifest response was not bound to the execution")
	}
	body := `{"manifest_hash":"` + envelope.Data.ManifestHash + `"}`
	f.request(t, http.MethodPost, "manifest/seal", strings.NewReader(body), f.headers, http.StatusConflict)
	f.request(t, http.MethodPost, "ensure", strings.NewReader(`{}`), f.headers, http.StatusOK)
	f.request(t, http.MethodPost, "export", strings.NewReader(`{}`), f.headers, http.StatusConflict)
	f.request(t, http.MethodPost, "manifest/seal", strings.NewReader(body), f.headers, http.StatusOK)
	f.request(t, http.MethodPost, "manifest/seal", strings.NewReader(body), f.headers, http.StatusOK)
	f.request(t, http.MethodPost, "manifest/file", strings.NewReader(`{"manifest_hash":"`+envelope.Data.ManifestHash+`","path":"private.txt"}`), f.headers, http.StatusConflict)
	for _, invalid := range []string{`null`, `{"files":[],"arbitrary_write":true}`} {
		f.request(t, http.MethodPost, "manifest", strings.NewReader(invalid), f.headers, http.StatusBadRequest)
	}
}

func TestNativeManifestHTTPIdentityBeforeBodyAndPolicy(t *testing.T) {
	f := newNativeHTTPFixture(t)
	for _, endpoint := range []string{"manifest", "manifest/file", "manifest/seal", "publications"} {
		for _, failure := range []string{"user", "lease", "dispatch"} {
			headers := maps.Clone(f.headers)
			switch failure {
			case "user":
				headers["Authorization"] = "Bearer native-owner-token"
			case "lease":
				headers["X-Workspace-Lease-Key"] = strings.Repeat("b", 64)
			case "dispatch":
				headers["X-Agent-Dispatch-Generation"] = "999"
			}
			f.request(t, http.MethodPost, endpoint, strings.NewReader(`invalid JSON`), headers, http.StatusForbidden)
		}
	}
	if f.engine.creates != 0 {
		t.Fatal("invalid source requests created an environment")
	}
}
