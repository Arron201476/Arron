//go:build sdk_integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

func TestSDKStatefulSkillAuthorHTTP(t *testing.T) {
	runSDKStatefulAuthorFixture(t, false)
}

func TestSDKStatefulNativePauseHTTPRebuildAndApproval(t *testing.T) {
	runSDKStatefulAuthorFixture(t, true)
}

func runSDKStatefulAuthorFixture(t *testing.T, nativePause bool) {
	root := testRoot(t)
	python := os.Getenv("CONTENT_AGENT_SDK_TEST_PYTHON")
	if python == "" {
		python = filepath.Join(root, ".tools", "openai-agents-sidecar-venv", "Scripts", "python.exe")
	}
	if _, err := os.Stat(python); err != nil {
		t.Fatalf("sdk_integration requires CONTENT_AGENT_SDK_TEST_PYTHON: %v", err)
	}
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "sdk-http-test-internal")
	for _, action := range []string{"approve", "reject"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			database := filepath.Join(t.TempDir(), "sdk-http.db")
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
					t.Fatal(err)
				}
				return store, httptest.NewServer(NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, tools, nil).Handler())
			}
			store, server := open()
			defer func() { server.Close(); store.Close() }()
			project, started := startSDKAuthorWorkflow(t, store, root)
			runWorker := func(phase string) {
				ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, python, filepath.Join(root, "experiments", "openai-agents-sidecar", "tests", "fixtures", "stateful_http_worker.py"),
					"--backend-url", server.URL, "--phase", phase, "--run-id", started.Run.RunID)
				command.Dir = root
				command.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1", "OPENAI_AGENTS_DISABLE_TRACING=1",
					"PYTHONPATH="+filepath.Join(root, "experiments", "openai-agents-sidecar", "src"))
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("real SDK Worker %s: %v\n%s", phase, err, output)
				}
				t.Logf("real SDK Worker: %s", output)
			}
			if nativePause {
				runWorker("pause-author")
				run := sdkPublicHTTP[businessruntime.Run](t, server, http.MethodGet, "/api/v1/runs/"+started.Run.RunID, nil)
				tasks, err := store.ListTaskItems(ctx, started.Steps[0].StepRunID)
				if err != nil || run.Status != "paused" || len(tasks) != 1 || tasks[0].Status != "paused" || tasks[0].CurrentAttemptID == nil {
					t.Fatalf("native pause did not preserve execution: %+v %+v %v", run, tasks, err)
				}
				originalAttempt := *tasks[0].CurrentAttemptID
				server.Close()
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store, server = open()
				sdkPublicHTTP[businessruntime.RunSnapshot](t, server, http.MethodPost, "/api/v1/runs/"+started.Run.RunID+"/resume", map[string]any{})
				runWorker("after-pause")
				tasks, err = store.ListTaskItems(ctx, started.Steps[0].StepRunID)
				if err != nil || len(tasks) != 1 || tasks[0].CurrentAttemptID == nil || *tasks[0].CurrentAttemptID != originalAttempt {
					t.Fatalf("resume replaced attempt: %+v %v", tasks, err)
				}
			} else {
				runWorker("author")
			}
			calls, err := store.ListAgentToolCalls(ctx, project.ProjectID)
			if err != nil || len(calls) != 3 {
				t.Fatalf("author tool calls: %+v %v", calls, err)
			}
			var pending businessruntime.AgentToolCall
			for _, call := range calls {
				if call.SDKToolCallID == "install-skill" {
					pending = call
				}
			}
			if pending.Approval == nil || pending.Status != "pending_approval" {
				t.Fatalf("install did not await approval: %+v", pending)
			}
			before, err := store.GetRunSnapshot(ctx, started.Run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			tasks, err := store.ListTaskItems(ctx, started.Steps[0].StepRunID)
			if err != nil || len(tasks) != 1 || tasks[0].Status != "waiting_approval" || tasks[0].CurrentAttemptID == nil || len(before.Artifacts) != 0 {
				t.Fatalf("Worker did not persist waiting state: %+v %v", before, err)
			}
			attemptID := *tasks[0].CurrentAttemptID
			server.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, server = open()
			projectionURL := "/api/v1/projects/" + project.ProjectID + "/workspace-projection"
			waitingProjection := sdkPublicHTTP[projectWorkspaceProjection](t, server, http.MethodGet, projectionURL, nil)
			if len(waitingProjection.Snapshot.AgentToolCalls) != 3 {
				t.Fatalf("public waiting history lost tools: %+v", waitingProjection.Snapshot.AgentToolCalls)
			}
			resolved := sdkPublicHTTP[businessruntime.AgentToolApproval](t, server, http.MethodPost,
				"/api/v1/agent-tool-approvals/"+pending.Approval.AgentToolApprovalID+"/resolutions",
				map[string]any{"expected_version": pending.Approval.Version, "subject_snapshot_hash": pending.Approval.SubjectSnapshotHash, "action": action})
			if resolved.AgentToolApprovalID != pending.Approval.AgentToolApprovalID || resolved.Status == "pending" {
				t.Fatalf("public approval resolution failed: %+v", resolved)
			}
			runWorker(action)
			after, err := store.GetRunSnapshot(ctx, started.Run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			tasks, err = store.ListTaskItems(ctx, started.Steps[0].StepRunID)
			if err != nil || after.Run.Status != "waiting_approval" || len(after.Artifacts) != 1 || len(tasks) != 1 || tasks[0].Status != "succeeded" {
				t.Fatalf("resumed Worker did not commit a document: %+v %v", after, err)
			}
			attempt, err := store.GetExecutionAttempt(ctx, attemptID)
			if err != nil || attempt.Status != "succeeded" || attempt.AttemptNo != 1 || tasks[0].CurrentAttemptID == nil || *tasks[0].CurrentAttemptID != attemptID {
				t.Fatalf("native resume replaced the attempt: %+v %v", attempt, err)
			}
			var usage struct {
				Requests     int `json:"requests"`
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			}
			wantRequests, wantCalls := 4, 3
			if action == "approve" {
				wantRequests, wantCalls = 5, 4
			}
			if err := json.Unmarshal(attempt.Usage, &usage); err != nil || usage.Requests != wantRequests || usage.InputTokens != wantRequests*10 || usage.OutputTokens != wantRequests*2 {
				t.Fatalf("usage lost or duplicated across recovery: %s %v", attempt.Usage, err)
			}
			file, err := store.ReadProjectFile(ctx, project.ProjectID, "skills/http-authored/SKILL.md", 0, 0, 1000)
			if err != nil || file.File.Version != 1 || !strings.Contains(file.Content, "HTTP_SDK_AUTHORED") {
				t.Fatalf("Skill file changed or replayed: %+v %v", file, err)
			}
			registry, err := store.CapabilityRegistryForProject(ctx, project.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			_, installed := registry.Get("http_authored")
			if installed != (action == "approve") {
				t.Fatalf("installation disagrees with decision: %v", installed)
			}
			projection := sdkPublicHTTP[projectWorkspaceProjection](t, server, http.MethodGet, projectionURL, nil)
			calls = projection.Snapshot.AgentToolCalls
			if len(calls) != wantCalls || len(projection.Snapshot.Artifacts) != 1 {
				t.Fatalf("public history lost or replayed work: %+v", projection.Snapshot)
			}
			for _, call := range calls {
				if call.Execution == nil || call.Execution.Mode != "stateful_workflow" || call.Execution.RunID != started.Run.RunID ||
					call.Execution.AttemptID != attemptID || call.Execution.TaskItemID != tasks[0].TaskItemID {
					t.Errorf("lost durable execution origin after recovery: %+v", call.Execution)
				}
				want := "completed"
				if call.SDKToolCallID == "install-skill" && action == "reject" {
					want = "rejected"
				}
				if call.Status != want {
					t.Errorf("tool %s ended %s, want %s", call.SDKToolCallID, call.Status, want)
				}
			}
		})
	}
}

func sdkPublicHTTP[T any](t *testing.T, server *httptest.Server, method, path string, payload any) T {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "81111111-1111-4111-8111-111111111111")
	client := server.Client()
	client.Timeout = 15 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("public API %s %s returned %d", method, path, response.StatusCode)
	}
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func startSDKAuthorWorkflow(t *testing.T, store *businessruntime.Store, root string) (businessruntime.Project, businessruntime.RunSnapshot) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.InstallSkillDirectory(ctx, filepath.Join(root, "fixtures", "skills", "stateful", "story-review-workflow"), "fixture"); err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, "Isolated SDK author acceptance")
	if err != nil {
		t.Fatal(err)
	}
	content := "A reporter must expose a mentor's hidden evidence."
	upload, err := store.CreateUploadSession(ctx, project.ProjectID, []businessruntime.UploadItemSpec{{ClientItemKey: "premise", Kind: "text", OriginalFilename: "premise.txt", DeclaredMIMEType: "text/plain", DeclaredSizeBytes: int64(len(content))}})
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
	ref := &agentcontract.CapabilityRef{CapabilityID: "story_review_workflow", Version: "1.0.0", SelectionMode: "explicit"}
	exchange, err := store.CreateMessageExchange(ctx, businessruntime.CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{Content: "Create and install a review Skill", CapabilityRef: ref,
			AttachmentRefs: []agentcontract.AttachmentRef{{AssetID: asset.Asset.AssetID, AssetSnapshotID: asset.Snapshot.AssetSnapshotID}}},
		Decision: agentcontract.AgentDecision{Reply: "Configure review", Intent: "propose_capability", Confidence: 1, CapabilityRef: ref,
			ProposedAction: &agentcontract.ProposedActionDraft{ActionType: "collect_run_configuration", CapabilityRef: ref, Input: json.RawMessage(`{}`), Config: json.RawMessage(`{}`)}}})
	if err != nil || exchange.Action == nil {
		t.Fatalf("workflow proposal: %+v %v", exchange, err)
	}
	configured, err := store.ConfigureProposedAction(ctx, businessruntime.ConfigureProposedActionCommand{ProposedActionID: exchange.Action.ProposedActionID,
		ExpectedVersion: exchange.Action.Version, Input: exchange.Action.Input, Config: json.RawMessage(`{"review_depth":"deep"}`)})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.StartRun(ctx, businessruntime.StartRunCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		CapabilityID: ref.CapabilityID, CapabilityVersion: ref.Version, RunKind: "generation", Input: configured.Input, Config: configured.Config, Confirmed: true,
		ProposedActionID: configured.ProposedActionID, ProposedActionVersion: configured.Version,
		ConfirmationMessageID: configured.ConfirmationMessageID, ConfirmationSnapshotHash: configured.SnapshotHash})
	if err != nil {
		t.Fatal(err)
	}
	return project, started
}
