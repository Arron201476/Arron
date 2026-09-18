//go:build sdk_integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestSDKMCPOutputTransportHTTPThreeExecutionIdentities(t *testing.T) {
	testSDKMCPOutputTransportHTTP(t, false)
}

func TestSDKMCPCredentialTransportHTTPThreeExecutionIdentities(t *testing.T) {
	testSDKMCPOutputTransportHTTP(t, true)
}

func testSDKMCPOutputTransportHTTP(t *testing.T, withCredentials bool) {
	root := testRoot(t)
	python := filepath.Join(root, ".tools", "openai-agents-sidecar-venv", "Scripts", "python.exe")
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "mcp-output-service")
	for _, mode := range []string{"conversation", "background_task", "stateful_workflow"} {
		t.Run(mode, func(t *testing.T) {
			database := filepath.Join(t.TempDir(), "output.db")
			registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: root, SkillRoots: []capability.SkillRoot{{
				Scope: capability.SkillScopeWorkspace, WorkspaceID: "pause", Path: filepath.Join(root, "fixtures", "skills", "background"), Priority: 200,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"-B", filepath.Join(root, "experiments", "openai-agents-sidecar", "tests", "fixtures", "mcp_output_server.py")}
			var credentialEnvironment map[string]string
			if withCredentials {
				args = append(args, "--credential-check")
				credentialEnvironment = map[string]string{"MCP_TEST_API_TOKEN": "api-token"}
			}
			tools, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion,
				StdioPolicy: agenttool.StdioPolicy{Enabled: true, AllowedCommands: []string{python}, AllowedCwds: []string{root}},
				MCPServers: []agenttool.MCPServerConfig{{ID: "story-fixture", Description: "Local file fixture", Transport: "stdio", Command: python,
					Args: args, Cwd: root, CredentialEnvironment: credentialEnvironment,
					Enabled: true, TimeoutSeconds: 30, MaxResultBytes: 64 * 1024,
					AllowedTools: []agenttool.MCPToolConfig{{Name: "lookup_story_fact", Description: "Return files", Access: agenttool.AccessRead}},
				}}})
			if err != nil {
				t.Fatal(err)
			}
			local := identity.DefaultLocalPrincipal()
			open := func() (*businessruntime.Store, *httptest.Server) {
				store, err := businessruntime.Open(database, registry)
				if err != nil {
					t.Fatal(err)
				}
				if withCredentials {
					if err := store.ConfigureMCPCredentials(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x45}, 32))); err != nil {
						t.Fatal(err)
					}
				}
				server := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, tools, nil)
				auth, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
					{Token: "output-local", UserID: local.UserID, WorkspaceID: local.WorkspaceID, Role: identity.RoleOwner},
					{Token: "output-pause", UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner},
					{Token: "output-foreign", UserID: "foreign", WorkspaceID: "foreign", Role: identity.RoleOwner},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := server.ConfigureAuthentication(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
				return store, httptest.NewServer(server.Handler())
			}
			store, server := open()
			defer func() { server.Close(); store.Close() }()
			config := map[string]string{"mode": mode}
			var project businessruntime.Project
			ctx := context.Background()
			userToken := "output-local"
			switch mode {
			case "conversation":
				project, err = store.CreateProject(ctx, "MCP output SDK fixture")
				if err != nil {
					t.Fatal(err)
				}
				turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Return a file"},
					businessruntime.CommandMeta{CommandType: "create_message", IdempotencyKey: "output-turn", RequestHash: "output-turn"})
				if err != nil {
					t.Fatal(err)
				}
				claims, err := store.ClaimRunnableAgentTurns(ctx, 1)
				if err != nil || len(claims) != 1 || claims[0].AgentTurnID != turn.AgentTurnID {
					t.Fatalf("main claim: %v", err)
				}
				config["agent_turn_id"] = turn.AgentTurnID
			case "background_task":
				var task businessruntime.AgentTask
				project, task = createBackgroundPauseHTTPTask(t, store)
				claim, err := store.ClaimAgentTask(ctx, businessruntime.ClaimAgentTaskCommand{WorkerID: "output-worker", ProviderID: "openai_agents_sdk", ModelID: "fixture", LeaseSeconds: 300})
				if err != nil || claim == nil || claim.Task.AgentTaskID != task.AgentTaskID {
					t.Fatalf("background claim: %v", err)
				}
				config["agent_task_id"], config["agent_task_attempt_id"], config["attempt_token"] = task.AgentTaskID, claim.Attempt.AgentTaskAttemptID, claim.AttemptToken
				config["skill_invocation_id"] = task.SkillInvocationID
				ctx = identity.WithPrincipal(ctx, identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner})
				userToken = "output-pause"
			case "stateful_workflow":
				project, _ = startSDKAuthorWorkflow(t, store, root)
				claim, err := store.ClaimExecutionTask(ctx, businessruntime.ClaimExecutionTaskCommand{WorkerID: "output-worker", ProviderID: "openai_agents_sdk", ExecutorIDs: []string{"worker.structured_content"}, LeaseSeconds: 300})
				if err != nil || claim == nil {
					t.Fatalf("stateful claim: %v", err)
				}
				config["execution_attempt_id"], config["attempt_token"] = claim.Attempt.AttemptID, claim.AttemptToken
			}
			if withCredentials {
				ownerCtx := ctx
				if _, ok := identity.UserFromContext(ownerCtx); !ok {
					ownerCtx = identity.WithPrincipal(ownerCtx, local)
				}
				if _, err := store.UpdateMCPConnection(ownerCtx, businessruntime.UpdateMCPConnectionCommand{
					ServerID: "story-fixture", Scope: "user", RequestID: "native-sdk-credential-fixture",
					Values: map[string]string{"api-token": "Bearer isolated-mcp-connection"},
				}); err != nil {
					t.Fatal(err)
				}
			}
			config["backend_url"], config["project_id"], config["conversation_id"] = server.URL, project.ProjectID, project.PrimaryConversationID
			input, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()
			command := exec.CommandContext(runCtx, python, filepath.Join(root, "experiments", "openai-agents-sidecar", "tests", "fixtures", "mcp_outputs_http_runner.py"))
			command.Dir, command.Stdin = root, bytes.NewReader(input)
			command.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1", "OPENAI_AGENTS_DISABLE_TRACING=1", "PYTHONPATH="+filepath.Join(root, "experiments", "openai-agents-sidecar", "src"))
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("MCP SDK output: %v\n%s", err, output)
			}
			t.Logf("SDK fixture: %s", output)
			server.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, server = open()
			calls, err := store.ListAgentToolCalls(ctx, project.ProjectID)
			if err != nil || len(calls) != 4 {
				t.Fatalf("tool ledger: %d %v", len(calls), err)
			}
			byID := map[string]businessruntime.AgentToolCall{}
			for _, call := range calls {
				if withCredentials {
					encoded, _ := json.Marshal(call)
					if bytes.Contains(encoded, []byte("isolated-mcp-connection")) {
						t.Fatal("MCP credential leaked to the public tool ledger")
					}
				}
				byID[call.AgentToolCallID] = call
				if call.Execution == nil || call.Execution.Mode != mode {
					t.Fatalf("wrong execution identity: %+v", call.Execution)
				}
				if (call.SDKToolCallID == "sdk-output-error" && call.Status != "failed") || (call.SDKToolCallID != "sdk-output-error" && call.Status != "completed") {
					t.Fatalf("false completion: %+v", call)
				}
			}
			assets, err := store.ListAssets(ctx, project.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, asset := range assets {
				if asset.SourceType != "mcp_tool" {
					continue
				}
				count++
				var metadata map[string]string
				if err := json.Unmarshal(asset.Metadata, &metadata); err != nil {
					t.Fatal(err)
				}
				call, ok := byID[metadata["agent_tool_call_id"]]
				if !ok || call.SDKToolCallID != metadata["sdk_tool_call_id"] || call.Status != "completed" {
					t.Fatal("output does not match its audited call")
				}
				want := []byte("value\n42\n")
				if strings.HasSuffix(asset.OriginalFilename, ".png") {
					want, err = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9Zl1sAAAAASUVORK5CYII=")
					if err != nil {
						t.Fatal(err)
					}
				}
				for _, token := range []string{userToken, "output-foreign", ""} {
					request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/assets/"+asset.AssetID+"/download", nil)
					if err != nil {
						t.Fatal(err)
					}
					if token != "" {
						request.Header.Set("Authorization", "Bearer "+token)
					}
					response, err := server.Client().Do(request)
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(response.Body)
					response.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
					if token == userToken {
						if response.StatusCode != http.StatusOK || !bytes.Equal(data, want) {
							t.Fatalf("download bytes: %d %q", response.StatusCode, data)
						}
					} else if response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusUnauthorized {
						t.Fatalf("foreign download allowed: %d", response.StatusCode)
					}
				}
			}
			if count != 3 {
				t.Fatalf("duplicate or missing MCP output assets: %d", count)
			}
		})
	}
}
