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
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

func TestSDKBackgroundPauseHTTPRebuildAndApproval(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "pause-service")
	root := testRoot(t)
	python := filepath.Join(root, ".tools", "openai-agents-sidecar-venv", "Scripts", "python.exe")
	if configured := os.Getenv("CONTENT_AGENT_SDK_TEST_PYTHON"); configured != "" {
		python = configured
	}
	for _, decision := range []string{"finish", "approve", "reject", "append_finish", "revise_finish", "attachment_finish", "recovery"} {
		t.Run(decision, func(t *testing.T) {
			changedInput := decision == "revise_finish" || decision == "attachment_finish"
			database := filepath.Join(t.TempDir(), "background-sdk.db")
			store, handler := openBackgroundPauseHTTP(t, database)
			server := httptest.NewServer(handler)
			defer func() { server.Close(); store.Close() }()
			project, task := createBackgroundPauseHTTPTask(t, store)
			ctx := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner})
			rebuild := func() {
				server.Close()
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store, handler = openBackgroundPauseHTTP(t, database)
				server = httptest.NewServer(handler)
			}
			runWorker := func(phase string) {
				workerContext, cancel := context.WithTimeout(ctx, 75*time.Second)
				defer cancel()
				command := exec.CommandContext(workerContext, python, filepath.Join(root, "experiments", "openai-agents-sidecar", "tests", "fixtures", "background_pause_http_worker.py"),
					"--backend-url", server.URL, "--task-id", task.AgentTaskID, "--phase", phase)
				command.Dir = root
				command.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1", "OPENAI_AGENTS_DISABLE_TRACING=1", "PYTHONPATH="+filepath.Join(root, "experiments", "openai-agents-sidecar", "src"))
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("background SDK Worker %s: %v\n%s", phase, err, output)
				}
				t.Logf("SDK Worker: %s", output)
			}
			resume := func(key string) {
				performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/agent-tasks/"+task.AgentTaskID+"/resume", nil, bearer("pause-owner", key), http.StatusAccepted)
			}
			appending := decision == "append_finish" || changedInput
			if appending {
				runWorker("append")
			} else if decision == "recovery" {
				runWorker("failure-write")
			} else {
				runWorker("write")
			}
			attempts, err := store.ListAgentTaskAttempts(ctx, task.AgentTaskID)
			if err != nil || len(attempts) != 1 || attempts[0].Status != "paused" {
				t.Fatalf("SDK checkpoint not persisted: %+v %v", attempts, err)
			}
			attemptID := attempts[0].AgentTaskAttemptID
			if changedInput {
				fresh, err := store.GetAgentTask(ctx, task.AgentTaskID)
				if err != nil || len(fresh.AdditionalInputs) != 1 {
					t.Fatal("missing unclaimed addition", err)
				}
				revisePendingInputHTTP(t, handler, project.ProjectID, "background_task", task.AgentTaskID, fresh.AdditionalInputs[0].InputID, fresh.AdditionalInputs[0].Content, "pause-owner", decision == "attachment_finish")
			}
			rebuild()
			if !appending {
				resume("a5555555-5555-4555-8555-555555555555")
			}
			if decision != "finish" && decision != "recovery" && !appending {
				runWorker("install")
				performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/agent-tasks/"+task.AgentTaskID+"/inputs", map[string]any{"content": "APPENDED_REQUIREMENT_ONCE"}, bearer("pause-owner", "a8888888-8888-4888-8888-888888888888"), http.StatusAccepted)
				rebuild()
				calls, err := store.ListAgentToolCalls(ctx, project.ProjectID)
				if err != nil || len(calls) != 3 {
					t.Fatalf("Skill install ledger: %+v %v", calls, err)
				}
				var pending businessruntime.AgentToolCall
				for _, call := range calls {
					if call.SDKToolCallID == "install-skill" {
						pending = call
					}
				}
				if pending.Approval == nil || pending.Status != "pending_approval" {
					t.Fatalf("Skill installation bypassed approval: %+v", pending)
				}
				performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/agent-tool-approvals/"+pending.Approval.AgentToolApprovalID+"/resolutions",
					map[string]any{"expected_version": pending.Approval.Version, "subject_snapshot_hash": pending.Approval.SubjectSnapshotHash, "action": decision},
					bearer("pause-owner", "a6666666-6666-4666-8666-666666666666"), http.StatusOK)
				fresh, err := store.GetAgentTask(ctx, task.AgentTaskID)
				if err != nil || fresh.Status != "paused" {
					t.Fatalf("approval ignored user pause: %+v %v", fresh, err)
				}
				resume("a7777777-7777-4777-8777-777777777777")
			}
			if decision == "recovery" {
				runWorker("finish")
			} else {
				runWorker(decision)
			}
			finished, err := store.GetAgentTask(ctx, task.AgentTaskID)
			if err != nil || finished.Status != "completed" || finished.AttemptCount != 1 || finished.ResultArtifactID == nil {
				t.Fatalf("resume did not complete original task: %+v %v", finished, err)
			}
			if changedInput && (len(finished.AdditionalInputs) != 3 || finished.AdditionalInputs[0].Status != "withdrawn" || finished.AdditionalInputs[1].Status != "superseded" || finished.AdditionalInputs[2].Status != "included") {
				t.Fatal("background changed input history missing")
			}
			attempts, err = store.ListAgentTaskAttempts(ctx, task.AgentTaskID)
			if err != nil || len(attempts) != 1 || attempts[0].AgentTaskAttemptID != attemptID || attempts[0].Status != "completed" {
				t.Fatalf("SDK resume replaced attempt: %+v %v", attempts, err)
			}
			var usage struct {
				Requests    int `json:"requests"`
				TotalTokens int `json:"total_tokens"`
			}
			wantRequests, wantCalls := 2, 1
			if decision != "finish" && decision != "recovery" && !appending {
				wantRequests, wantCalls = 4, 3
			}
			if err := json.Unmarshal(attempts[0].Usage, &usage); err != nil || usage.Requests != wantRequests || usage.TotalTokens != wantRequests*12 {
				t.Fatalf("SDK usage lost or repeated: %s %v", attempts[0].Usage, err)
			}
			file, err := store.ReadProjectFile(ctx, project.ProjectID, "skills/background-pause/SKILL.md", 0, 0, 1000)
			if err != nil || file.File.Version != 1 || !strings.Contains(file.Content, "BACKGROUND_PAUSE_RESTORED") {
				t.Fatalf("working file lost or replayed: %+v %v", file, err)
			}
			registry, err := store.CapabilityRegistryForProject(ctx, project.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			_, installed := registry.Get("background_pause")
			if installed != (decision == "approve") {
				t.Fatalf("Skill installation does not match approval: %v", installed)
			}
			calls, err := store.ListAgentToolCalls(ctx, project.ProjectID)
			if err != nil || len(calls) != wantCalls {
				t.Fatalf("tool replay: %+v %v", calls, err)
			}
			for _, call := range calls {
				if call.Execution == nil || call.Execution.Mode != "background_task" || call.Execution.AttemptID != attemptID || call.Execution.AgentTaskID != task.AgentTaskID {
					t.Fatalf("execution identity changed: %+v", call.Execution)
				}
			}
		})
	}
}
