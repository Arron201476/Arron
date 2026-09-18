package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"

	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/scriptsandbox"
)

type nativeFileHTTPEngine struct {
	*nativeHTTPEngine
	files int
}

func (e *nativeFileHTTPEngine) FileWorkspace(ctx context.Context, handle scriptsandbox.WorkspaceHandle, operation scriptsandbox.WorkspaceFileOperation) (scriptsandbox.WorkspaceFileResult, error) {
	e.files++
	if err := e.ReconnectWorkspace(ctx, handle); err != nil {
		return scriptsandbox.WorkspaceFileResult{}, err
	}
	data := []byte{0, 255, 128}
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	return scriptsandbox.WorkspaceFileResult{Operation: "read", Path: operation.Path, Data: data, SHA256: hash,
		Entries: []scriptsandbox.WorkspaceEntry{{Path: operation.Path, SizeBytes: int64(len(data)), Mode: 0o600, SHA256: hash}}}, nil
}

func TestNativeFileHTTPBoundedBinaryReadAndWriteApproval(t *testing.T) {
	f := newNativeHTTPFixture(t)
	f.policy(t, true, nil)
	engine := &nativeFileHTTPEngine{nativeHTTPEngine: f.engine}
	f.server.ConfigureNativeWorkspaceEngine(engine)
	f.request(t, http.MethodPost, "ensure", strings.NewReader(`{}`), f.headers, http.StatusOK)
	operation := businessruntime.NativeWorkspaceFileRequest{RequestID: strings.Repeat("a", 32), File: scriptsandbox.WorkspaceFileOperation{Operation: "read", Path: "file"}}
	encoded, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	read := f.request(t, http.MethodPost, "files", bytes.NewReader(encoded), f.headers, http.StatusOK)
	var envelope struct {
		Data businessruntime.NativeWorkspaceFileReceipt `json:"data"`
	}
	if json.Unmarshal(read.Body.Bytes(), &envelope) != nil || !bytes.Equal(envelope.Data.File.Data, []byte{0, 255, 128}) || envelope.Data.SessionID != f.session {
		t.Fatal("file HTTP changed binary data or workspace binding")
	}
	operation.File.Operation = "write"
	encoded, err = json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, "files", bytes.NewReader(encoded), f.headers, http.StatusForbidden)
	for _, body := range []string{`null`, `{"request_id":"a","file":{}}`, `{"unexpected":true}`} {
		f.request(t, http.MethodPost, "files", strings.NewReader(body), f.headers, http.StatusBadRequest)
	}
	if engine.files != 1 {
		t.Fatal("unapproved or malformed write reached file engine")
	}
}

func TestNativeFileHTTPRequiresInternalCurrentLease(t *testing.T) {
	f := newNativeHTTPFixture(t)
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
		f.request(t, http.MethodPost, "files", strings.NewReader(`not even JSON`), headers, http.StatusForbidden)
	}
	if f.engine.creates != 0 {
		t.Fatal("file route implicitly created an environment")
	}
}
