package revision

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

func sidecarRevisionFixture(t *testing.T) (*businessruntime.Store, businessruntime.RevisionRequest, identity.Principal) {
	t.Helper()
	root := strings.TrimSpace(os.Getenv("CONTENT_AGENT_TEST_PROJECT_ROOT"))
	if root == "" {
		_, file, _, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("cannot locate test fixture root")
		}
		root = filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	}
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "revision-worker.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	project, err := store.CreateProject(ctx, "Revision worker")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.CreateGenericArtifact(ctx, businessruntime.CreateGenericArtifactCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		Draft: agentcontract.ArtifactDraft{ArtifactType: "generic_document", Title: "Draft", Payload: json.RawMessage(`{"content":"Original body"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	editor := identity.DefaultLocalPrincipal()
	editor.UserID, editor.Role = "revision_worker_editor", identity.RoleEditor
	if err := store.BootstrapPrincipal(ctx, editor); err != nil {
		t.Fatal(err)
	}
	exchange, err := store.CreateMessageExchange(identity.WithPrincipal(context.Background(), editor), businessruntime.CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{Content: "Revise Draft", ClientContext: agentcontract.ClientContext{
			CurrentArtifactID: &artifact.ArtifactID, CurrentArtifactVersionID: &artifact.CurrentVersionID,
		}},
		Decision: agentcontract.AgentDecision{Reply: "Revision requested", Intent: "revise", Confidence: 1},
	})
	if err != nil || exchange.Revision == nil {
		t.Fatalf("revision fixture: %+v %v", exchange, err)
	}
	return store, *exchange.Revision, editor
}

func TestSidecarBackgroundExecutionRechecksStoredAuthor(t *testing.T) {
	for _, scenario := range []string{"valid", "revoked_before_claim", "revoked_before_proposal", "revoked_before_no_change"} {
		t.Run(scenario, func(t *testing.T) {
			store, request, editor := sidecarRevisionFixture(t)
			revoke := func() error {
				viewer := editor
				viewer.Role = identity.RoleViewer
				return store.BootstrapPrincipal(context.Background(), viewer)
			}
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				calls.Add(1)
				if incoming.URL.Path != "/internal/v1/revisions/execute" || incoming.Method != http.MethodPost || incoming.Header.Get("Authorization") != "Bearer fixture-only-token" {
					t.Error("unexpected revision transport")
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				var body struct {
					RevisionRequestID string `json:"revision_request_id"`
					RevisionAttemptID string `json:"revision_attempt_id"`
				}
				if err := json.NewDecoder(incoming.Body).Decode(&body); err != nil || body.RevisionRequestID != request.RevisionRequestID || body.RevisionAttemptID == "" {
					t.Errorf("incorrect request identity: %+v %v", body, err)
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				if strings.HasPrefix(scenario, "revoked_before_p") || scenario == "revoked_before_no_change" {
					if err := revoke(); err != nil {
						t.Errorf("fixture revocation: %v", err)
						writer.WriteHeader(http.StatusInternalServerError)
						return
					}
				}
				output := map[string]any{"outcome": "proposed", "proposal_payload": map[string]any{"content": "Revised body"}, "proposal_summary": "Revision fixture", "provider_id": "fixture"}
				if scenario == "revoked_before_no_change" {
					output["outcome"] = "no_change"
				}
				if err := json.NewEncoder(writer).Encode(output); err != nil {
					t.Errorf("encode response: %v", err)
				}
			}))
			defer server.Close()
			service, err := NewSidecar(store, server.URL, "fixture-only-token", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "revoked_before_claim" {
				if err := revoke(); err != nil {
					t.Fatal(err)
				}
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			service.poll(context.Background(), logger)
			stored, err := store.GetRevisionRequest(context.Background(), request.RevisionRequestID)
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := int64(1)
			if scenario == "revoked_before_claim" {
				wantCalls = 0
			}
			if scenario == "valid" {
				if stored.Status != "proposed" {
					t.Fatalf("background execution: %+v", stored)
				}
			} else if stored.Status != "failed" || stored.FailureCode == nil || *stored.FailureCode != "ROLE_FORBIDDEN" {
				t.Fatalf("revoked execution result: %+v", stored)
			}
			service.poll(context.Background(), logger)
			if calls.Load() != wantCalls {
				t.Fatalf("model calls = %d, want %d", calls.Load(), wantCalls)
			}
			artifact, err := store.GetArtifact(context.Background(), *request.ArtifactID)
			if err != nil || artifact.CurrentVersionID != *request.BaseVersionID {
				t.Fatalf("generation changed original artifact: %+v %v", artifact, err)
			}
		})
	}
}
