package httpapi

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

func TestNativePublicationHTTPBinaryDownloadAndValidation(t *testing.T) {
	f := newNativeHTTPFixture(t)
	f.policy(t, true, nil)
	registry, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	f.store.SetAgentToolRegistry(registry)
	activity := businessruntime.AgentActivityIdentity{ProjectID: f.headers["X-Agent-Project-ID"], AgentTurnID: f.headers["X-Agent-Turn-ID"]}
	ctx := businessruntime.WithAgentActivity(identity.WithPrincipal(f.ctx, identity.ServicePrincipal()), activity)
	project, err := f.store.GetProject(f.ctx, activity.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	headers := maps.Clone(f.headers)
	headers["Content-Type"] = "application/x-tar"
	headers["X-Workspace-Snapshot-Parent"] = "0"
	headers["X-Workspace-Snapshot-Sha256"] = f.engine.archive.SHA256
	f.request(t, http.MethodPut, "snapshots/"+strings.Repeat("d", 32), bytes.NewReader(f.engine.archive.Archive), headers, http.StatusOK)
	arguments := businessruntime.NativeWorkspacePublicationArguments{
		Snapshot: businessruntime.NativeWorkspaceSnapshotReference{Version: 1, SHA256: f.engine.archive.SHA256},
		Files:    []businessruntime.NativeWorkspacePublicationFile{{SourcePath: "artifact.bin", Path: "artifact.bin"}},
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	call, err := f.store.BeginAgentToolCall(ctx, businessruntime.BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, AgentTurnID: activity.AgentTurnID,
		SDKToolCallID: "sdk-publication", ToolID: "runtime:publish_workspace_files", Arguments: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ResolveAgentToolApproval(f.ctx, businessruntime.ResolveAgentToolApprovalCommand{
		AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version,
		SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.StartAgentToolCall(ctx, businessruntime.StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID}); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(businessruntime.NativeWorkspacePublicationRequest{AgentToolCallID: call.AgentToolCallID, SDKToolCallID: call.SDKToolCallID, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, "publications", bytes.NewReader(raw), f.headers, http.StatusOK)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+project.ProjectID+"/files/content?path=artifact.bin&version=1", nil)
	request.Header.Set("Authorization", "Bearer native-owner-token")
	result := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(result, request)
	if result.Code != http.StatusOK || !bytes.Equal(result.Body.Bytes(), []byte{0, 255, 128, 1}) || result.Header().Get("Content-Length") != "4" ||
		result.Header().Get("X-Project-File-Version") != "1" || result.Header().Get("Content-Type") != "application/octet-stream" || result.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("binary download changed bytes or headers: %d %+v %v", result.Code, result.Header(), result.Body.Bytes())
	}
	for _, body := range []string{`null`, `{"unexpected":true}`} {
		f.request(t, http.MethodPost, "publications", strings.NewReader(body), f.headers, http.StatusBadRequest)
	}
	if f.engine.creates != 0 || f.engine.reconnects != 0 {
		t.Fatal("immutable publication unexpectedly operated a live environment")
	}
}
