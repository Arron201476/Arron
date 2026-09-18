//go:build sdk_integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestSDKStatefulRepairHTTPRebuild(t *testing.T) {
	root := testRoot(t)
	python := os.Getenv("CONTENT_AGENT_SDK_TEST_PYTHON")
	if python == "" {
		python = filepath.Join(root, ".tools", "openai-agents-sidecar-venv", "Scripts", "python.exe")
	}
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "sdk-http-test-internal")
	for _, boundary := range []string{"zero", "native", "guardrail", "output-guardrail"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			database := filepath.Join(t.TempDir(), "repair-http.db")
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
				command := exec.CommandContext(ctx, python, filepath.Join(root, "experiments", "openai-agents-sidecar", "tests", "fixtures", "stateful_repair_http_worker.py"),
					"--backend-url", server.URL, "--phase", phase, "--run-id", started.Run.RunID)
				command.Dir = root
				command.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1", "OPENAI_AGENTS_DISABLE_TRACING=1",
					"PYTHONPATH="+filepath.Join(root, "experiments", "openai-agents-sidecar", "src"))
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("SDK repair Worker %s: %v\n%s", phase, err, output)
				}
				t.Logf("SDK repair Worker: %s", output)
			}
			first := "start-zero"
			if boundary == "native" {
				first = "start-native"
			}
			runWorker(first)
			run := sdkPublicHTTP[businessruntime.Run](t, server, http.MethodGet, "/api/v1/runs/"+started.Run.RunID, nil)
			tasks, err := store.ListTaskItems(ctx, started.Steps[0].StepRunID)
			if err != nil || run.Status != "paused" || len(tasks) != 1 || tasks[0].Status != "paused" || tasks[0].CurrentAttemptID == nil {
				t.Fatalf("repair did not pause native execution: %+v %+v %v", run, tasks, err)
			}
			attemptID := *tasks[0].CurrentAttemptID
			original, err := store.GetExecutionAttempt(ctx, attemptID)
			if err != nil {
				t.Fatal(err)
			}
			server.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, server = open()
			sdkPublicHTTP[businessruntime.RunSnapshot](t, server, http.MethodPost, "/api/v1/runs/"+started.Run.RunID+"/resume", map[string]any{})
			last := "resume"
			if boundary == "guardrail" || boundary == "output-guardrail" {
				last = boundary
			}
			runWorker(last)
			attempt, err := store.GetExecutionAttempt(ctx, attemptID)
			if err != nil || attempt.AttemptNo != 1 || !attempt.StartedAt.Equal(original.StartedAt) || attempt.InputSnapshotHash != original.InputSnapshotHash {
				t.Fatalf("repair changed attempt identity: %+v %v", attempt, err)
			}
			file, err := store.ReadProjectFile(ctx, project.ProjectID, "repair-evidence.txt", 0, 0, 1000)
			if err != nil || file.File.Version != 1 || file.Content != "original execution" {
				t.Fatalf("original write replayed: %+v %v", file, err)
			}
			calls, err := store.ListAgentToolCalls(ctx, project.ProjectID)
			if err != nil || len(calls) != 1 || calls[0].Status != "completed" {
				t.Fatalf("repair replayed tools: %+v %v", calls, err)
			}
			snapshot, err := store.GetRunSnapshot(ctx, started.Run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if boundary == "guardrail" || boundary == "output-guardrail" {
				code := "AGENT_INPUT_GUARDRAIL_REJECTED"
				if boundary == "output-guardrail" {
					code = "AGENT_OUTPUT_GUARDRAIL_REJECTED"
				}
				if attempt.Status != "failed" || attempt.ErrorCode == nil || *attempt.ErrorCode != code || len(snapshot.Artifacts) != 0 {
					t.Fatalf("zero-model repair bypassed input guardrail: %+v %+v", attempt, snapshot)
				}
				return
			}
			if attempt.Status != "succeeded" || len(snapshot.Artifacts) != 1 {
				t.Fatalf("repair did not commit: %+v %+v", attempt, snapshot)
			}
			var usage struct {
				Requests    int `json:"requests"`
				TotalTokens int `json:"total_tokens"`
			}
			want := 3
			if boundary == "native" {
				want = 4
			}
			if err := json.Unmarshal(attempt.Usage, &usage); err != nil || usage.Requests != want || usage.TotalTokens != want*12 {
				t.Fatalf("repair lost cumulative usage: %s %v", attempt.Usage, err)
			}
		})
	}
}
