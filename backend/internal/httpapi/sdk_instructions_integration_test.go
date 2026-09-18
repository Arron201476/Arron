//go:build sdk_integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestSDKInstructionAuthorHTTPThreeExecutionIdentities(t *testing.T) {
	root := testRoot(t)
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "instruction-service")
	for _, mode := range []string{"conversation", "background_task", "stateful_workflow"} {
		t.Run(mode, func(t *testing.T) {
			registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: root, SkillRoots: []capability.SkillRoot{{Scope: capability.SkillScopeWorkspace, WorkspaceID: "pause", Path: filepath.Join(root, "fixtures", "skills", "background"), Priority: 200}}})
			if err != nil {
				t.Fatal(err)
			}
			toolRegistry, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion})
			if err != nil {
				t.Fatal(err)
			}
			database := filepath.Join(t.TempDir(), "instructions.db")
			local := identity.DefaultLocalPrincipal()
			open := func() (*businessruntime.Store, *httptest.Server) {
				store, err := businessruntime.Open(database, registry)
				if err != nil {
					t.Fatal(err)
				}
				server := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, toolRegistry, nil)
				auth, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
					{Token: "local", UserID: local.UserID, WorkspaceID: local.WorkspaceID, Role: identity.RoleOwner},
					{Token: "pause", UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner},
					{Token: "other-local", UserID: "other-local", WorkspaceID: local.WorkspaceID, Role: identity.RoleOwner},
					{Token: "other-pause", UserID: "other-pause", WorkspaceID: "pause", Role: identity.RoleOwner},
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
			config := map[string]string{"mode": mode, "user_token": "local", "other_token": "other-local"}
			ctx := identity.WithPrincipal(context.Background(), local)
			var project businessruntime.Project
			switch mode {
			case "conversation":
				project, err = store.CreateProject(ctx, "SDK rule author")
				if err != nil {
					t.Fatal(err)
				}
				turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Remember this preference"}, businessruntime.CommandMeta{})
				if err != nil {
					t.Fatal(err)
				}
				if claims, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil || len(claims) != 1 {
					t.Fatalf("claim: %v", err)
				}
				config["agent_turn_id"] = turn.AgentTurnID
			case "background_task":
				var task businessruntime.AgentTask
				project, task = createBackgroundPauseHTTPTask(t, store)
				claim, err := store.ClaimAgentTask(context.Background(), businessruntime.ClaimAgentTaskCommand{WorkerID: "rule-worker", ProviderID: "openai_agents_sdk", ModelID: "fixture", LeaseSeconds: 300})
				if err != nil || claim == nil {
					t.Fatalf("claim: %v", err)
				}
				config["agent_task_id"], config["agent_task_attempt_id"], config["skill_invocation_id"], config["attempt_token"] = task.AgentTaskID, claim.Attempt.AgentTaskAttemptID, task.SkillInvocationID, claim.AttemptToken
				ctx = identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner})
				config["user_token"], config["other_token"] = "pause", "other-pause"
			case "stateful_workflow":
				project, _ = startSDKAuthorWorkflow(t, store, root)
				claim, err := store.ClaimExecutionTask(ctx, businessruntime.ClaimExecutionTaskCommand{WorkerID: "rule-worker", ProviderID: "openai_agents_sdk", ExecutorIDs: []string{"worker.structured_content"}, LeaseSeconds: 300})
				if err != nil || claim == nil {
					t.Fatalf("claim: %v", err)
				}
				config["execution_attempt_id"], config["attempt_token"] = claim.Attempt.AttemptID, claim.AttemptToken
			}
			if _, err := store.UpdateAgentInstructions(ctx, businessruntime.UpdateAgentInstructionsCommand{Scope: "user", Content: "PINNED_RULE_V1", Enabled: true, RequestID: "seed"}); err != nil {
				t.Fatal(err)
			}
			config["backend_url"], config["project_id"], config["conversation_id"] = server.URL, project.ProjectID, project.PrimaryConversationID
			input, _ := json.Marshal(config)
			runCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
			defer cancel()
			command := exec.CommandContext(runCtx, filepath.Join(root, ".tools", "openai-agents-sidecar-venv", "Scripts", "python.exe"), filepath.Join(root, "experiments", "openai-agents-sidecar", "tests", "fixtures", "instructions_http_runner.py"))
			command.Dir, command.Stdin = root, bytes.NewReader(input)
			command.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1", "OPENAI_AGENTS_DISABLE_TRACING=1", "PYTHONPATH="+filepath.Join(root, "experiments", "openai-agents-sidecar", "src"))
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("SDK rule fixture: %v\n%s", err, output)
			}
			t.Logf("SDK: %s", output)
			server.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, server = open()
			view, err := store.GetAgentInstructions(ctx, project.ProjectID)
			if err != nil || view.Documents[1].Content != "AUTHOR_RULE_V2" || view.Documents[1].Version != 2 {
				t.Fatalf("saved rule after reopen: %+v %v", view, err)
			}
			calls, err := store.ListAgentToolCalls(ctx, project.ProjectID)
			if err != nil || len(calls) != 3 {
				t.Fatalf("ledger: %d %v", len(calls), err)
			}
			for _, call := range calls {
				encoded, _ := json.Marshal(call)
				if strings.Contains(string(encoded), "RULE_V") || call.Status != "completed" || call.Execution == nil || call.Execution.Mode != mode {
					t.Fatalf("unsafe ledger: %+v", call)
				}
			}
		})
	}
}
