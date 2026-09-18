//go:build sdk_integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestSDKFormalRepairHTTPReceiptAndTokenBoundary(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", defaultTestInternalServiceToken)
	ctx := context.Background()
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "http-receipts.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, started := startSDKSourceAnalysisRepair(t, store)
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()
	claimCommand := businessruntime.ClaimExecutionTaskCommand{WorkerID: "formal-http-fixture", ProviderID: "openai_agents_sdk", ExecutorIDs: []string{"worker.structured_content"}, LeaseSeconds: 60}
	claim, err := store.ClaimExecutionTask(ctx, claimCommand)
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	received, err := store.SubmitExecutionResult(ctx, businessruntime.SubmitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash, ResponsePayload: json.RawMessage(`{"wrong":"HTTP_PRIVATE_CANDIDATE_47"}`)})
	if err != nil {
		t.Fatal(err)
	}
	path := "/internal/v1/executor/attempts/" + claim.Attempt.AttemptID + "/commit"
	body := map[string]any{"expected_response_hash": *received.ResponseHash, "repair_output": true}
	invoke := func(token, authorization string, body any) *httptest.ResponseRecorder {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Attempt-Token", token)
		request.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	auth := "Bearer " + defaultTestInternalServiceToken
	if response := invoke(claim.AttemptToken, "", body); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized commit: %d %s", response.Code, response.Body.String())
	}
	for _, token := range []string{"", "wrong-token"} {
		response := invoke(token, auth, body)
		if response.Code < 400 || !strings.Contains(response.Body.String(), "ATTEMPT_TOKEN_INVALID") {
			t.Fatalf("missing token gate: %d %s", response.Code, response.Body.String())
		}
	}
	legacy := invoke("", auth, map[string]any{"expected_response_hash": *received.ResponseHash})
	if legacy.Code < 400 || !strings.Contains(legacy.Body.String(), "OUTPUT_SCHEMA_VALIDATION_FAILED") {
		t.Fatalf("legacy opt-out changed: %d %s", legacy.Code, legacy.Body.String())
	}
	for range 2 {
		response := invoke(claim.AttemptToken, auth, body)
		if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), "output_repair_queued") || strings.Contains(response.Body.String(), "HTTP_PRIVATE_CANDIDATE_47") {
			t.Fatalf("repair receipt: %d %s", response.Code, response.Body.String())
		}
	}
	public := performJSON(t, handler, http.MethodGet, "/api/v1/steps/"+*started.Run.CurrentStepRunID+"/tasks", nil, http.StatusOK)
	item := arrayAt(t, objectAt(t, public, "data"), "items")[0].(map[string]any)
	if stringAt(t, item, "status") != "repair_pending" || stringAt(t, objectAt(t, item, "output_repair"), "status") != "queued" {
		t.Fatalf("missing public repair state: %+v", public)
	}
	payload, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"HTTP_PRIVATE_CANDIDATE_47", "error_detail", "response_payload_json", claim.AttemptToken} {
		if strings.Contains(string(payload), private) {
			t.Fatalf("public endpoint leaked %s", private)
		}
	}
	next, err := store.ClaimExecutionTask(ctx, claimCommand)
	if err != nil || next == nil || next.Repair == nil {
		t.Fatalf("repair claim: %+v %v", next, err)
	}
	response := invoke(claim.AttemptToken, auth, body)
	if response.Code < 400 || !strings.Contains(response.Body.String(), "ATTEMPT_TOKEN_INVALID") {
		t.Fatalf("old token survived claim: %d %s", response.Code, response.Body.String())
	}
}

func startSDKSourceAnalysisRepair(t *testing.T, store *businessruntime.Store) (businessruntime.Project, businessruntime.RunSnapshot) {
	t.Helper()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Isolated formal result repair")
	if err != nil {
		t.Fatal(err)
	}
	content := "Chapter 1. A reporter discovers a hidden record."
	upload, err := store.CreateUploadSession(ctx, project.ProjectID, []businessruntime.UploadItemSpec{{ClientItemKey: "source", Kind: "text", OriginalFilename: "source.txt", DeclaredMIMEType: "text/plain", DeclaredSizeBytes: int64(len(content))}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteUploadContent(ctx, upload.Items[0].UploadItemID, strings.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	asset, err := store.CompleteUploadItem(ctx, upload.Items[0].UploadItemID)
	if err != nil {
		t.Fatal(err)
	}
	request := agentcontract.MessageRequest{Content: "Analyze the source", CapabilityRef: &agentcontract.CapabilityRef{CapabilityID: "novel_to_script", Version: "1.4.0"}, AttachmentRefs: []agentcontract.AttachmentRef{{AssetID: asset.Asset.AssetID, AssetSnapshotID: asset.Snapshot.AssetSnapshotID}}}
	decision, err := checkpointAgentDecision(agentcontract.AgentInput{ProjectID: project.ProjectID, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := store.CreateMessageExchange(ctx, businessruntime.CreateMessageExchangeCommand{ConversationID: project.PrimaryConversationID, Request: request, Decision: decision})
	if err != nil || exchange.Action == nil {
		t.Fatalf("proposal: %+v %v", exchange, err)
	}
	action := exchange.Action
	initial, err := store.StartRun(ctx, businessruntime.StartRunCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, CapabilityID: "novel_to_script", CapabilityVersion: "1.4.0", RunKind: "generation", Input: action.Input, Config: action.Config, Confirmed: true, ProposedActionID: action.ProposedActionID, ProposedActionVersion: action.Version, ConfirmationMessageID: action.ConfirmationMessageID, ConfirmationSnapshotHash: action.SnapshotHash})
	if err != nil {
		t.Fatal(err)
	}
	approval := initial.CurrentApproval
	if approval == nil {
		t.Fatal("missing source configuration approval")
	}
	if _, err := store.ResolveApproval(ctx, businessruntime.ResolveApprovalCommand{ApprovalRequestID: approval.ApprovalRequestID, Action: "approve", ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash}); err != nil {
		t.Fatal(err)
	}
	running, err := store.ResumeRun(ctx, businessruntime.ResumeRunCommand{RunID: initial.Run.RunID})
	if err != nil {
		t.Fatal(err)
	}
	return project, running
}

func TestSDKStatefulFormalRejectionHTTPRebuild(t *testing.T) {
	root := testRoot(t)
	python := os.Getenv("CONTENT_AGENT_SDK_TEST_PYTHON")
	if python == "" {
		python = filepath.Join(root, ".tools", "openai-agents-sidecar-venv", "Scripts", "python.exe")
	}
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "sdk-http-test-internal")
	for _, boundary := range []string{"repair", "pause", "reject"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			database := filepath.Join(t.TempDir(), "formal-repair.db")
			open := func() (*businessruntime.Store, *httptest.Server) {
				registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: root})
				if err != nil {
					t.Fatal(err)
				}
				store, err := businessruntime.Open(database, registry)
				if err != nil {
					t.Fatal(err)
				}
				tools, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion})
				if err != nil {
					store.Close()
					t.Fatal(err)
				}
				return store, httptest.NewServer(NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, tools, nil).Handler())
			}
			store, server := open()
			defer func() { server.Close(); store.Close() }()
			rebuild := func() {
				server.Close()
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store, server = open()
			}
			project, started := startSDKSourceAnalysisRepair(t, store)
			runWorker := func(phase string) {
				ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, python, filepath.Join(root, "experiments", "openai-agents-sidecar", "tests", "fixtures", "stateful_result_repair_http_worker.py"), "--backend-url", server.URL, "--run-id", started.Run.RunID, "--phase", phase)
				command.Dir = root
				command.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1", "OPENAI_AGENTS_DISABLE_TRACING=1", "PYTHONPATH="+filepath.Join(root, "experiments", "openai-agents-sidecar", "src"))
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("formal repair %s: %v\n%s", phase, err, output)
				}
				t.Logf("formal repair Worker: %s", output)
			}
			runWorker("start")
			tasks, err := store.ListTaskItems(ctx, *started.Run.CurrentStepRunID)
			if err != nil || len(tasks) != 1 || tasks[0].Status != "repair_pending" || tasks[0].CurrentAttemptID == nil {
				t.Fatalf("formal rejection not queued: %+v %v", tasks, err)
			}
			original, err := store.GetExecutionAttempt(ctx, *tasks[0].CurrentAttemptID)
			if err != nil {
				t.Fatal(err)
			}
			rebuild()
			if boundary == "pause" {
				runWorker("pause")
				run := sdkPublicHTTP[businessruntime.Run](t, server, http.MethodGet, "/api/v1/runs/"+started.Run.RunID, nil)
				if run.Status != "paused" {
					t.Fatalf("repair not natively paused: %+v", run)
				}
				rebuild()
				sdkPublicHTTP[businessruntime.RunSnapshot](t, server, http.MethodPost, "/api/v1/runs/"+started.Run.RunID+"/resume", map[string]any{})
			}
			phase := "finish"
			if boundary == "reject" {
				phase = "reject"
			}
			runWorker(phase)
			attempt, err := store.GetExecutionAttempt(ctx, original.AttemptID)
			if err != nil || attempt.AttemptNo != 1 || attempt.InputSnapshotHash != original.InputSnapshotHash || !attempt.StartedAt.Equal(original.StartedAt) {
				t.Fatalf("attempt changed: %+v %v", attempt, err)
			}
			wantStatus := "succeeded"
			if boundary == "reject" {
				wantStatus = "failed"
			}
			if attempt.Status != wantStatus {
				t.Fatalf("formal result status: %+v", attempt)
			}
			if boundary == "reject" && (attempt.ErrorCode == nil || *attempt.ErrorCode != "OUTPUT_REPAIR_FAILED") {
				t.Fatalf("wrong failure: %+v", attempt)
			}
			file, err := store.ReadProjectFile(ctx, project.ProjectID, "formal-repair-evidence.txt", 0, 0, 1000)
			if err != nil || file.File.Version != 1 || file.Content != "original execution" {
				t.Fatalf("original write replayed: %+v %v", file, err)
			}
			calls, err := store.ListAgentToolCalls(ctx, project.ProjectID)
			if err != nil || len(calls) != 1 || calls[0].Status != "completed" {
				t.Fatalf("repair replayed tools: %+v %v", calls, err)
			}
			var usage struct {
				Requests    int `json:"requests"`
				TotalTokens int `json:"total_tokens"`
			}
			want := 3
			if boundary == "pause" {
				want = 4
			}
			if err := json.Unmarshal(attempt.Usage, &usage); err != nil || usage.Requests != want || usage.TotalTokens != want*12 {
				t.Fatalf("usage lost: %s %v", attempt.Usage, err)
			}
		})
	}
}
