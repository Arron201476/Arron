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
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestSDKExecutionInputHTTPAuthorization(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "inputs-service")
	root := testRoot(t)
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "input-auth.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, _ := startSDKAuthorWorkflow(t, store, root)
	claim, err := store.ClaimExecutionTask(context.Background(), businessruntime.ClaimExecutionTaskCommand{WorkerID: "auth", ProviderID: "openai_agents_sdk", ExecutorIDs: []string{"worker.structured_content"}, LeaseSeconds: 60})
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	local := identity.DefaultLocalPrincipal()
	auth, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
		{Token: "input-owner", UserID: local.UserID, WorkspaceID: local.WorkspaceID, Role: identity.RoleOwner},
		{Token: "input-editor", UserID: "editor", WorkspaceID: local.WorkspaceID, Role: identity.RoleEditor},
		{Token: "input-viewer", UserID: "viewer", WorkspaceID: local.WorkspaceID, Role: identity.RoleViewer},
		{Token: "input-foreign", UserID: "foreign", WorkspaceID: "foreign", Role: identity.RoleOwner},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntime(shell.New(registry), store, nil)
	if err := server.ConfigureAuthentication(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	base := "/api/v1/projects/" + project.ProjectID + "/execution-attempts/" + claim.Attempt.AttemptID + "/inputs"
	key := "b1111111-1111-4111-8111-111111111111"
	body := map[string]any{"content": "User addition"}
	performJSONWithHeaders(t, handler, http.MethodPost, base, body, nil, http.StatusUnauthorized)
	for _, token := range []string{"input-viewer", "input-editor", "inputs-service"} {
		performJSONWithHeaders(t, handler, http.MethodPost, base, body, bearer(token, key), http.StatusForbidden)
	}
	performJSONWithHeaders(t, handler, http.MethodPost, base, body, bearer("input-foreign", key), http.StatusNotFound)
	performJSONWithHeaders(t, handler, http.MethodPost, base, body, map[string]string{"Authorization": "Bearer input-owner", "Idempotency-Key": ""}, http.StatusBadRequest)
	performJSONWithHeaders(t, handler, http.MethodPost, base, map[string]any{"content": "Forged", "status": "included"}, bearer("input-owner", key), http.StatusBadRequest)
	first := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, base, body, bearer("input-owner", key), http.StatusAccepted), "data")
	second := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, base, body, bearer("input-owner", key), http.StatusAccepted), "data")
	if first["input_id"] != second["input_id"] || first["status"] != "received" {
		t.Fatal("invalid idempotent receipt")
	}
	view := objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, base, nil, bearer("input-viewer", ""), http.StatusOK), "data")
	if view["can_append"] != false {
		t.Fatal("viewer may append")
	}
	performJSONWithHeaders(t, handler, http.MethodGet, base, nil, bearer("input-foreign", ""), http.StatusNotFound)
	history := "/api/v1/projects/" + project.ProjectID + "/runs/" + claim.Attempt.RunID + "/input-attempts"
	performJSONWithHeaders(t, handler, http.MethodGet, history, nil, nil, http.StatusUnauthorized)
	performJSONWithHeaders(t, handler, http.MethodGet, history, nil, bearer("input-foreign", ""), http.StatusNotFound)
	page := objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, history, nil, bearer("input-viewer", ""), http.StatusOK), "data")
	items, ok := page["items"].([]any)
	if !ok || len(items) != 1 || items[0].(map[string]any)["attempt_id"] != claim.Attempt.AttemptID || page["next_cursor"] != "" {
		t.Fatal("history projection is invalid", page)
	}
	record := map[string]any{"input_snapshot_hash": claim.Attempt.InputSnapshotHash, "included_input_ids": []string{first["input_id"].(string)}}
	internal := "/internal/v1/executor/attempts/" + claim.Attempt.AttemptID + "/inputs/included"
	performJSONWithHeaders(t, handler, http.MethodPost, internal, record, bearer("input-owner", ""), http.StatusUnauthorized)
	headers := bearer("inputs-service", "")
	headers["X-Attempt-Token"] = "wrong"
	invalid := performJSONWithHeaders(t, handler, http.MethodPost, internal, record, headers, http.StatusBadRequest)
	if objectAt(t, invalid, "error")["code"] != "ATTEMPT_TOKEN_INVALID" {
		t.Fatal("invalid token was not rejected")
	}
	headers["X-Attempt-Token"] = claim.AttemptToken
	performJSONWithHeaders(t, handler, http.MethodPost, internal, record, headers, http.StatusConflict)
}

func TestSDKStatefulInputsHTTPRebuild(t *testing.T) {
	root := testRoot(t)
	python := filepath.Join(root, ".tools", "openai-agents-sidecar-venv", "Scripts", "python.exe")
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "sdk-http-test-internal")
	for _, phase := range []string{"finish", "reject-output", "late", "revise", "attachment", "recovery"} {
		t.Run(phase, func(t *testing.T) {
			changedInput := phase == "revise" || phase == "attachment"
			ctx := context.Background()
			database := filepath.Join(t.TempDir(), "inputs.db")
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
			runWorker := func(mode string) {
				runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
				defer cancel()
				command := exec.CommandContext(runCtx, python, filepath.Join(root, "experiments", "openai-agents-sidecar", "tests", "fixtures", "stateful_inputs_http_worker.py"), "--backend-url", server.URL, "--phase", mode)
				command.Dir = root
				command.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1", "OPENAI_AGENTS_DISABLE_TRACING=1", "PYTHONPATH="+filepath.Join(root, "experiments", "openai-agents-sidecar", "src"))
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("SDK input Worker %s: %v\n%s", mode, err, output)
				}
				t.Logf("SDK input Worker: %s", output)
			}
			first := "start"
			if phase == "late" {
				first = "late"
			} else if phase == "recovery" {
				first = "failure-start"
			}
			runWorker(first)
			tasks, err := store.ListTaskItems(ctx, started.Steps[0].StepRunID)
			if err != nil || len(tasks) != 1 || tasks[0].CurrentAttemptID == nil {
				t.Fatalf("task identity: %v", err)
			}
			original, err := store.GetExecutionAttempt(ctx, *tasks[0].CurrentAttemptID)
			if err != nil {
				t.Fatal(err)
			}
			base := "/api/v1/projects/" + project.ProjectID + "/execution-attempts/" + original.AttemptID + "/inputs"
			before := sdkPublicHTTP[businessruntime.ExecutionInputsView](t, server, http.MethodGet, base, nil)
			if len(before.Inputs) != 1 || before.Inputs[0].Status != "received" {
				t.Fatalf("premature model receipt: %+v", before)
			}
			if phase != "late" {
				if original.Status != "paused" {
					t.Fatalf("not paused: %s", original.Status)
				}
				if changedInput {
					revisePendingInputHTTP(t, server.Config.Handler, project.ProjectID, "stateful_workflow", original.AttemptID, before.Inputs[0].InputID, before.Inputs[0].Content, "", phase == "attachment")
				}
				server.Close()
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store, server = open()
				workerPhase := phase
				if phase == "recovery" {
					if original.ErrorCode == nil || *original.ErrorCode != "SDK_MODEL_RECOVERY_REQUIRED" {
						t.Fatal("native model recovery cause was lost")
					}
					sdkPublicHTTP[businessruntime.RunSnapshot](t, server, http.MethodPost, "/api/v1/runs/"+started.Run.RunID+"/resume", map[string]any{})
				}
				if changedInput || phase == "recovery" {
					workerPhase = "finish"
				}
				runWorker(workerPhase)
			}
			after := sdkPublicHTTP[businessruntime.ExecutionInputsView](t, server, http.MethodGet, base, nil)
			want := "included"
			if phase == "late" {
				want = "received"
			}
			wantCount := 1
			if changedInput {
				wantCount = 3
				if len(after.Inputs) != 3 || after.Inputs[0].Status != "withdrawn" || after.Inputs[1].Status != "superseded" {
					t.Fatal("changed input history missing", after)
				}
			}
			if len(after.Inputs) != wantCount || after.Inputs[wantCount-1].Status != want || after.CanAppend {
				t.Fatalf("incorrect terminal receipt: %+v", after)
			}
			attempt, err := store.GetExecutionAttempt(ctx, original.AttemptID)
			if err != nil || attempt.AttemptNo != 1 || attempt.InputSnapshotHash != original.InputSnapshotHash || !attempt.StartedAt.Equal(original.StartedAt) {
				t.Fatalf("attempt changed: %v", err)
			}
			wantStatus := "succeeded"
			if phase == "reject-output" {
				wantStatus = "failed"
			}
			if attempt.Status != wantStatus {
				t.Fatalf("attempt=%s want=%s", attempt.Status, wantStatus)
			}
			if phase != "late" {
				file, err := store.ReadProjectFile(ctx, project.ProjectID, "input-evidence.txt", 0, 0, 100)
				if err != nil || file.File.Version != 1 || file.Content != "original-write" {
					t.Fatalf("write replayed: %v", err)
				}
			}
			if phase == "finish" || phase == "recovery" {
				var usage struct {
					Requests int `json:"requests"`
				}
				if json.Unmarshal(attempt.Usage, &usage) != nil || usage.Requests != 2 {
					t.Fatalf("usage=%s", attempt.Usage)
				}
			}
		})
	}
}
