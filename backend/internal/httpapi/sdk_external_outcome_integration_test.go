//go:build sdk_integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestSDKExternalOutcomeHTTPRebuildAndExplicitUserFacts(t *testing.T) {
	root := testRoot(t)
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "outcome-service")
	for _, mode := range []string{"conversation", "background_task", "stateful_workflow"} {
		t.Run(mode, func(t *testing.T) {
			registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: root, SkillRoots: []capability.SkillRoot{{Scope: capability.SkillScopeWorkspace, WorkspaceID: "pause", Path: filepath.Join(root, "fixtures", "skills", "background"), Priority: 200}}})
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			database := filepath.Join(directory, "outcome.db")
			local := identity.DefaultLocalPrincipal()
			open := func() (*businessruntime.Store, *Server, *httptest.Server) {
				store, err := businessruntime.Open(database, registry)
				if err != nil {
					t.Fatal(err)
				}
				tools, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion, MCPServers: []agenttool.MCPServerConfig{{ID: "fixture", Description: "Isolated MCP", Transport: "streamable_http", URL: "http://127.0.0.1:1/no-transport", Enabled: true, TimeoutSeconds: 10, MaxResultBytes: 8192,
					AllowedTools: []agenttool.MCPToolConfig{{Name: "write", Description: "External write fixture", Access: agenttool.AccessWrite, Approval: agenttool.ApprovalAlways}}}}})
				if err != nil {
					t.Fatal(err)
				}
				api := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, tools, nil)
				auth, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{{Token: "local", UserID: local.UserID, WorkspaceID: local.WorkspaceID, Role: identity.RoleOwner},
					{Token: "pause-owner", UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner}, {Token: "foreign", UserID: "foreign", WorkspaceID: "foreign", Role: identity.RoleOwner}})
				if err != nil {
					t.Fatal(err)
				}
				if err := api.ConfigureAuthentication(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
				return store, api, httptest.NewServer(api.Handler())
			}
			store, api, server := open()
			defer func() { server.Close(); store.Close() }()
			ctx := identity.WithPrincipal(context.Background(), local)
			var project businessruntime.Project
			var turn businessruntime.AgentTurn
			var task businessruntime.AgentTask
			var run businessruntime.RunSnapshot
			owner := "local"
			switch mode {
			case "conversation":
				project, err = store.CreateProject(ctx, "External outcome acceptance")
				if err != nil {
					t.Fatal(err)
				}
				turn, err = store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Issue the two authorized writes and report"}, businessruntime.CommandMeta{IdempotencyKey: "f1111111-1111-4111-8111-111111111111", CommandType: "accept_agent_turn", RequestHash: "outcome-fixture"})
				if err != nil {
					t.Fatal(err)
				}
			case "background_task":
				project, task = createBackgroundPauseHTTPTask(t, store)
				owner = "pause-owner"
				ctx = identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner})
			case "stateful_workflow":
				project, run = startSDKAuthorWorkflow(t, store, root)
			}
			config := map[string]any{"mode": mode, "session_db": filepath.Join(directory, "session.db"), "effects_file": filepath.Join(directory, "effects.json"), "project_id": project.ProjectID, "conversation_id": project.PrimaryConversationID}
			var uncertain businessruntime.AgentToolCall
			for _, phase := range []string{"approve", "issued", "resume"} {
				if phase != "approve" {
					server.Close()
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
					store, api, server = open()
				}
				if phase == "resume" {
					path := "/api/v1/agent-turns/" + turn.AgentTurnID + "/resume"
					if mode == "background_task" {
						path = "/api/v1/agent-tasks/" + task.AgentTaskID + "/resume"
					}
					if mode == "stateful_workflow" {
						path = "/api/v1/runs/" + run.Run.RunID + "/resume"
					}
					performJSONWithHeaders(t, api.Handler(), http.MethodPost, path, map[string]any{}, bearer(owner, "f2222222-2222-4222-8222-222222222222"), http.StatusConflict)
					reviewPath := "/api/v1/agent-tool-calls/" + uncertain.AgentToolCallID + "/outcome-review"
					performJSONWithHeaders(t, api.Handler(), http.MethodGet, reviewPath, nil, bearer("foreign", ""), http.StatusNotFound)
					view := objectAt(t, performJSONWithHeaders(t, api.Handler(), http.MethodGet, reviewPath, nil, bearer(owner, ""), http.StatusOK), "data")
					body := map[string]any{"request_id": "checked-original", "subject_snapshot_hash": view["subject_snapshot_hash"], "outcome": "applied", "evidence": "CHECKED_REMOTE_ORIGINAL: both issued operations exist in provider history."}
					performJSONWithHeaders(t, api.Handler(), http.MethodPost, reviewPath, body, bearer(owner, ""), http.StatusOK)
					performJSONWithHeaders(t, api.Handler(), http.MethodPost, reviewPath, body, bearer(owner, ""), http.StatusOK)
					status := http.StatusOK
					if mode == "background_task" {
						status = http.StatusAccepted
					}
					performJSONWithHeaders(t, api.Handler(), http.MethodPost, path, map[string]any{}, bearer(owner, "f3333333-3333-4333-8333-333333333333"), status)
				}
				config["backend_url"], config["phase"] = server.URL, phase
				if mode == "conversation" {
					claimed, err := store.ClaimRunnableAgentTurns(ctx, 1)
					if err != nil || len(claimed) != 1 {
						t.Fatalf("claim %s: %+v %v", phase, claimed, err)
					}
					turn = claimed[0]
					resume, err := store.GetAgentTurnResumeContext(ctx, turn.AgentTurnID)
					if err != nil {
						t.Fatal(err)
					}
					config["agent_turn_id"], config["idempotency_key"], config["request"] = turn.AgentTurnID, turn.IdempotencyKey, turn.Request
					config["run_state"], config["approval_decisions"], config["additional_inputs"] = resume.RunState, resume.ApprovalDecisions, turn.AdditionalInputs
				}
				input, err := json.Marshal(config)
				if err != nil {
					t.Fatal(err)
				}
				runCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
				command := exec.CommandContext(runCtx, filepath.Join(root, ".tools", "openai-agents-sidecar-venv", "Scripts", "python.exe"), filepath.Join(root, "experiments", "openai-agents-sidecar", "tests", "fixtures", "outcome_http_runner.py"))
				command.Dir, command.Stdin = root, bytes.NewReader(input)
				command.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1", "OPENAI_AGENTS_DISABLE_TRACING=1", "PYTHONPATH="+filepath.Join(root, "experiments", "openai-agents-sidecar", "src"))
				var output, diagnostics bytes.Buffer
				command.Stdout, command.Stderr = &output, &diagnostics
				err = command.Run()
				cancel()
				if err != nil {
					t.Fatalf("SDK %s: %v\n%s\n%s", phase, err, output.String(), diagnostics.String())
				}
				var result struct {
					Events []struct {
						Event string          `json:"event"`
						Data  json.RawMessage `json:"data"`
					} `json:"events"`
					Effects []string `json:"effects"`
				}
				if err := json.Unmarshal(output.Bytes(), &result); err != nil {
					t.Fatalf("fixture result %v: %s", err, output.String())
				}
				if mode == "conversation" {
					manager := newAgentTurnManager(store, shell.New(registry), slog.Default())
					for _, event := range result.Events {
						if err := manager.persistEvent(ctx, turn, shell.AgentTurnEvent{EventType: event.Event, Payload: event.Data}); err != nil {
							t.Fatalf("persist %s: %v", event.Event, err)
						}
					}
				}
				calls, err := store.ListAgentToolCalls(ctx, project.ProjectID)
				if err != nil {
					t.Fatal(err)
				}
				if phase == "approve" {
					pending := 0
					for _, call := range calls {
						if call.ToolKind != "mcp" {
							continue
						}
						pending++
						if call.Approval == nil || call.Status != "pending_approval" {
							t.Fatalf("missing real approval: %+v", call)
						}
						performJSONWithHeaders(t, api.Handler(), http.MethodPost, "/api/v1/agent-tool-approvals/"+call.Approval.AgentToolApprovalID+"/resolutions", map[string]any{"action": "approve", "expected_version": call.Approval.Version, "subject_snapshot_hash": call.Approval.SubjectSnapshotHash}, bearer(owner, fmt.Sprintf("f4444444-4444-4444-8444-%012d", pending)), http.StatusOK)
					}
					if pending != 2 || len(result.Effects) != 0 {
						t.Fatal("unexpected issued writes before approval")
					}
				} else {
					for _, call := range calls {
						if call.SDKToolCallID == "uncertain" {
							uncertain = call
						}
					}
					if uncertain.Status != "failed" || uncertain.ErrorCode == nil || *uncertain.ErrorCode != "MCP_TOOL_OUTCOME_UNKNOWN" || len(result.Effects) != 2 {
						t.Fatalf("original facts lost: %+v", uncertain)
					}
				}
				t.Logf("%s %s: native phase finished; issued operations=%d", mode, phase, len(result.Effects))
			}
			switch mode {
			case "conversation":
				saved, err := store.GetAgentTurn(ctx, turn.AgentTurnID)
				if err != nil || saved.Status != "committed" || len(saved.AdditionalInputs) != 1 || saved.AdditionalInputs[0].Status != "included" {
					t.Fatalf("main final %+v %v", saved, err)
				}
			case "background_task":
				saved, err := store.GetAgentTask(ctx, task.AgentTaskID)
				if err != nil || saved.Status != "completed" || saved.AttemptCount != 1 || len(saved.AdditionalInputs) != 1 || saved.AdditionalInputs[0].Status != "included" {
					t.Fatalf("background final %+v %v", saved, err)
				}
			case "stateful_workflow":
				saved, err := store.GetExecutionAttempt(ctx, uncertain.Execution.AttemptID)
				if err != nil || saved.Status != "succeeded" || saved.AttemptNo != 1 {
					t.Fatalf("stateful final %+v %v", saved, err)
				}
				var usage struct {
					Requests int `json:"requests"`
				}
				if json.Unmarshal(saved.Usage, &usage) != nil || usage.Requests != 2 {
					t.Fatalf("native cumulative usage %s", saved.Usage)
				}
			}
		})
	}
}
