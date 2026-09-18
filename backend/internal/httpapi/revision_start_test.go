package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

type revisionAdmissionExecutor struct{ calls int }

func TestRevisionAuthorizationErrorsHaveActionableHTTPStatuses(t *testing.T) {
	for code, status := range map[string]int{
		"AUTHENTICATION_REQUIRED":           http.StatusUnauthorized,
		"REVISION_EXECUTION_OWNER_REQUIRED": http.StatusConflict,
		"REVISION_EXECUTION_OWNER_INVALID":  http.StatusConflict,
		"ROLE_FORBIDDEN":                    http.StatusForbidden,
	} {
		recorder := httptest.NewRecorder()
		writeRuntimeError(recorder, &businessruntime.DomainError{Code: code, Message: "Revision authorization unavailable"})
		if recorder.Code != status {
			t.Errorf("%s: status %d, want %d", code, recorder.Code, status)
		}
	}
}

func (executor *revisionAdmissionExecutor) Execute(context.Context, string) (businessruntime.RevisionRequest, error) {
	executor.calls++
	return businessruntime.RevisionRequest{}, nil
}

func TestRevisionPublicCommandsAdmitDurablyWithoutSynchronousExecution(t *testing.T) {
	for _, action := range []string{"execute", "execute_without_body", "target"} {
		t.Run(action, func(t *testing.T) {
			registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
			if err != nil {
				t.Fatal(err)
			}
			store, err := businessruntime.Open(filepath.Join(t.TempDir(), "revision-admission.db"), registry)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			project, err := store.CreateProject(ctx, "Revision admission")
			if err != nil {
				t.Fatal(err)
			}
			var artifact businessruntime.Artifact
			count := 1
			if action == "target" {
				count = 2
			}
			for index := 0; index < count; index++ {
				artifact, err = store.CreateGenericArtifact(ctx, businessruntime.CreateGenericArtifactCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, Draft: agentcontract.ArtifactDraft{ArtifactType: "generic_document", Title: "Draft", Payload: json.RawMessage(`{"content":"Saved body"}`)}})
				if err != nil {
					t.Fatal(err)
				}
			}
			message := agentcontract.MessageRequest{Content: "Revise Draft"}
			if action != "target" {
				message.ClientContext.CurrentArtifactID = &artifact.ArtifactID
				message.ClientContext.CurrentArtifactVersionID = &artifact.CurrentVersionID
			}
			exchange, err := store.CreateMessageExchange(ctx, businessruntime.CreateMessageExchangeCommand{ConversationID: project.PrimaryConversationID, Request: message, Decision: agentcontract.AgentDecision{Reply: "Revision requested", Intent: "revise", Confidence: 1}})
			if err != nil || exchange.Revision == nil {
				t.Fatalf("fixture: %+v %v", exchange, err)
			}
			executor := &revisionAdmissionExecutor{}
			handler := NewWithRuntimeAndRevision(shell.New(registry), store, executor, nil).Handler()
			request := *exchange.Revision
			path := "/api/v1/revision-requests/" + request.RevisionRequestID + "/execute"
			var body any = map[string]any{"expected_revision_version": request.Version}
			status := http.StatusAccepted
			if action == "execute_without_body" {
				body = nil
			}
			if action == "target" {
				resolution, err := store.GetTargetResolution(ctx, request.TargetResolutionID)
				if err != nil || len(resolution.Candidates) != 2 {
					t.Fatalf("target fixture: %+v %v", resolution, err)
				}
				path = "/api/v1/target-resolutions/" + resolution.TargetResolutionID + "/resolve"
				body = map[string]any{"candidate_id": resolution.Candidates[0].CandidateID, "expected_revision_version": request.Version}
				status = http.StatusOK
			}
			for retry := 0; retry < 2; retry++ {
				response := performJSON(t, handler, http.MethodPost, path, body, status)
				data := objectAt(t, response, "data")
				if stringAt(t, data, "revision_request_id") != request.RevisionRequestID || stringAt(t, data, "status") != "queued" || data["version"] != float64(request.Version+1) {
					t.Fatalf("admission receipt: %+v", data)
				}
			}
			if executor.calls != 0 {
				t.Fatal("public admission synchronously invoked the SDK")
			}
			if action != "execute_without_body" {
				response := performJSONWithHeaders(t, handler, http.MethodPost, path, body, map[string]string{"Idempotency-Key": "11111111-1111-4111-8111-111111111111"}, http.StatusConflict)
				want := "REVISION_VERSION_CONFLICT"
				if action == "target" {
					want = "TARGET_STATE_CONFLICT"
				}
				if stringAt(t, objectAt(t, response, "error"), "code") != want {
					t.Fatalf("stale admission response: %+v", response)
				}
			}
		})
	}
}
