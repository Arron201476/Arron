//go:build sdk_integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestSDKSubtasksProductionEntrypointsHTTPAndDurableResults(t *testing.T) {
	runSDKSubtasksHTTPFixture(t, "complete")
}

func TestSDKSubtasksModelFailureRebuildDoesNotReplayChildren(t *testing.T) {
	runSDKSubtasksHTTPFixture(t, "fail")
}

func TestSDKSubtasksUserPauseRebuildDoesNotReplayChildren(t *testing.T) {
	runSDKSubtasksHTTPFixture(t, "pause")
}

func runSDKSubtasksHTTPFixture(t *testing.T, lifecycle string) {
	root := testRoot(t)
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "subtask-service")
	for _, mode := range []string{"conversation", "background_task", "stateful_workflow"} {
		t.Run(mode, func(t *testing.T) {
			registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: root, SkillRoots: []capability.SkillRoot{{Scope: capability.SkillScopeWorkspace, WorkspaceID: "pause", Path: filepath.Join(root, "fixtures", "skills", "background"), Priority: 200}}})
			if err != nil {
				t.Fatal(err)
			}
			tools, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion})
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			database := filepath.Join(directory, "subtasks.db")
			local := identity.DefaultLocalPrincipal()
			open := func() (*businessruntime.Store, *Server, *httptest.Server) {
				store, err := businessruntime.Open(database, registry)
				if err != nil {
					t.Fatal(err)
				}
				server := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, tools, nil)
				auth, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
					{Token: "local", UserID: local.UserID, WorkspaceID: local.WorkspaceID, Role: identity.RoleOwner},
					{Token: "local-viewer", UserID: "viewer", WorkspaceID: local.WorkspaceID, Role: identity.RoleViewer},
					{Token: "pause-owner", UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner},
					{Token: "pause-viewer", UserID: "viewer", WorkspaceID: "pause", Role: identity.RoleViewer},
					{Token: "foreign", UserID: "foreign", WorkspaceID: "foreign", Role: identity.RoleOwner},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := server.ConfigureAuthentication(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
				return store, server, httptest.NewServer(server.Handler())
			}
			store, api, server := open()
			defer func() { server.Close(); store.Close() }()
			ctx := identity.WithPrincipal(context.Background(), local)
			config := map[string]any{"mode": mode, "session_db": filepath.Join(directory, "sdk-session.db")}
			var project businessruntime.Project
			var turn businessruntime.AgentTurn
			var task businessruntime.AgentTask
			var run businessruntime.RunSnapshot
			viewer := "local-viewer"
			owner := "local"
			switch mode {
			case "conversation":
				project, err = store.CreateProject(ctx, "Subtask acceptance")
				if err != nil {
					t.Fatal(err)
				}
				turn, err = store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Analyze two independent questions and join results"}, businessruntime.CommandMeta{IdempotencyKey: "e1111111-1111-4111-8111-111111111111", CommandType: "accept_agent_turn", RequestHash: "subtask-fixture"})
				if err != nil {
					t.Fatal(err)
				}
				config["agent_turn_id"], config["idempotency_key"], config["request"] = turn.AgentTurnID, turn.IdempotencyKey, turn.Request
			case "background_task":
				project, task = createBackgroundPauseHTTPTask(t, store)
				viewer = "pause-viewer"
				owner = "pause-owner"
				ctx = identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner})
			case "stateful_workflow":
				project, run = startSDKAuthorWorkflow(t, store, root)
			}
			artifact, err := store.CreateGenericArtifact(ctx, businessruntime.CreateGenericArtifactCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
				Draft: agentcontract.ArtifactDraft{ArtifactType: "generic_document", Title: "Subtask evidence", Payload: json.RawMessage(`{"content_markdown":"SUBTASK_SOURCE_EVIDENCE"}`)}})
			if err != nil {
				t.Fatal(err)
			}
			config["backend_url"], config["project_id"], config["conversation_id"], config["artifact_id"], config["artifact_version_id"] = server.URL, project.ProjectID, project.PrimaryConversationID, artifact.ArtifactID, artifact.CurrentVersionID
			config["owner"], config["agent_task_id"], config["run_id"] = owner, task.AgentTaskID, run.Run.RunID
			phases := []string{"complete"}
			if lifecycle != "complete" {
				phases = []string{lifecycle, "resume"}
			}
			for _, phase := range phases {
				if phase == "resume" {
					server.Close()
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
					store, api, server = open()
					path, status := "/api/v1/agent-turns/"+turn.AgentTurnID+"/resume", http.StatusOK
					if mode == "background_task" {
						path, status = "/api/v1/agent-tasks/"+task.AgentTaskID+"/resume", http.StatusAccepted
					} else if mode == "stateful_workflow" {
						path = "/api/v1/runs/" + run.Run.RunID + "/resume"
					}
					performJSONWithHeaders(t, api.Handler(), http.MethodPost, path, map[string]any{}, bearer(owner, "e2222222-2222-4222-8222-222222222222"), status)
				}
				config["backend_url"], config["phase"] = server.URL, phase
				if mode == "conversation" {
					claims, err := store.ClaimRunnableAgentTurns(ctx, 1)
					if err != nil || len(claims) != 1 {
						t.Fatal("claim failed", err)
					}
					turn = claims[0]
					resume, err := store.GetAgentTurnResumeContext(ctx, turn.AgentTurnID)
					if err != nil {
						t.Fatal(err)
					}
					config["run_state"] = resume.RunState
				}
				input, _ := json.Marshal(config)
				runCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
				defer cancel()
				command := exec.CommandContext(runCtx, filepath.Join(root, ".tools", "openai-agents-sidecar-venv", "Scripts", "python.exe"), filepath.Join(root, "experiments", "openai-agents-sidecar", "tests", "fixtures", "subtasks_http_runner.py"))
				command.Dir, command.Stdin = root, bytes.NewReader(input)
				command.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1", "OPENAI_AGENTS_DISABLE_TRACING=1", "PYTHONPATH="+filepath.Join(root, "experiments", "openai-agents-sidecar", "src"))
				var diagnostics bytes.Buffer
				command.Stderr = &diagnostics
				output, err := command.Output()
				cancel()
				if err != nil {
					t.Fatalf("SDK subtasks %s: %v\n%s\n%s", phase, err, output, diagnostics.String())
				}
				var result struct {
					Events []struct {
						Event string          `json:"event"`
						Data  json.RawMessage `json:"data"`
					} `json:"events"`
					ParentRequests int            `json:"parent_requests"`
					ChildRequests  map[string]int `json:"child_requests"`
				}
				if err := json.Unmarshal(output, &result); err != nil {
					t.Fatal(err)
				}
				if mode == "conversation" {
					manager := newAgentTurnManager(store, shell.New(registry), slog.Default())
					for _, event := range result.Events {
						if err := manager.persistEvent(ctx, turn, shell.AgentTurnEvent{EventType: event.Event, Payload: event.Data}); err != nil {
							t.Fatal(err)
						}
					}
					if phase == "fail" || phase == "pause" {
						saved, err := store.GetAgentTurn(ctx, turn.AgentTurnID)
						if err != nil || saved.Status != "paused" || (phase == "fail" && (saved.ErrorCode == nil || *saved.ErrorCode != "SDK_MODEL_RECOVERY_REQUIRED")) {
							t.Fatalf("missing model recovery state: %+v %v", saved, err)
						}
					}
				}
				t.Logf("SDK %s: parent requests=%d child requests=%v", phase, result.ParentRequests, result.ChildRequests)
			}
			server.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, api, server = open()
			if mode == "conversation" {
				saved, err := store.GetAgentTurn(ctx, turn.AgentTurnID)
				if err != nil || saved.Status != "committed" {
					t.Fatalf("turn not committed %+v %v", saved, err)
				}
			}
			if mode == "background_task" {
				saved, err := store.GetAgentTask(ctx, task.AgentTaskID)
				if err != nil || saved.Status != "completed" || saved.AttemptCount != 1 {
					t.Fatalf("task not completed %+v %v", saved, err)
				}
			}
			calls, err := store.ListAgentToolCalls(ctx, project.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			parents, reads := 0, 0
			for _, call := range calls {
				if call.Status != "completed" || call.Execution == nil || call.Execution.Mode != mode {
					t.Fatalf("invalid execution ledger %+v", call)
				}
				if call.ParentToolCallID != "" {
					reads++
				}
				if call.ToolID != "runtime:delegate_subtask" {
					continue
				}
				if mode == "stateful_workflow" {
					attempt, err := store.GetExecutionAttempt(ctx, call.Execution.AttemptID)
					if err != nil || attempt.Status != "succeeded" || attempt.AttemptNo != 1 {
						t.Fatalf("stateful result not committed %+v %v", attempt, err)
					}
					var usage struct {
						Requests int `json:"requests"`
					}
					if json.Unmarshal(attempt.Usage, &usage) != nil || usage.Requests != 6 {
						t.Fatalf("subtask usage lost: %s", attempt.Usage)
					}
				}
				parents++
				path := "/api/v1/agent-tool-calls/" + call.AgentToolCallID + "/subtask-result"
				performJSONWithHeaders(t, api.Handler(), http.MethodGet, path, nil, nil, http.StatusUnauthorized)
				performJSONWithHeaders(t, api.Handler(), http.MethodGet, path, nil, bearer("foreign", ""), http.StatusNotFound)
				view := objectAt(t, performJSONWithHeaders(t, api.Handler(), http.MethodGet, path, nil, bearer(viewer, ""), http.StatusOK), "data")
				if view["project_id"] != project.ProjectID || !strings.Contains(stringAt(t, view, "text"), "inspected the source") || len(arrayAt(t, view, "read_call_ids")) != 1 || len(arrayAt(t, view, "inspected_artifacts")) != 1 {
					t.Fatalf("result lost %+v", view)
				}
			}
			if parents != 2 || reads != 2 {
				t.Fatalf("expected two joined native subtasks and separate reads, got %d/%d", parents, reads)
			}
		})
	}
}
