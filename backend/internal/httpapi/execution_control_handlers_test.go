package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestExecutionControlHTTPApprovalAndIdentityBoundaries(t *testing.T) {
	for _, decision := range []string{"approve", "reject", "stale"} {
		t.Run(decision, func(t *testing.T) {
			t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "control-service")
			root := testRoot(t)
			registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: root, SkillRoots: []capability.SkillRoot{{
				Scope: capability.SkillScopeWorkspace, WorkspaceID: "control", Path: filepath.Join(root, "fixtures", "skills", "background"), Priority: 200,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			store, err := businessruntime.Open(filepath.Join(t.TempDir(), "control.db"), registry)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			tools, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion})
			if err != nil {
				t.Fatal(err)
			}
			store.SetAgentToolRegistry(tools)
			auth, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
				{Token: "control-owner", UserID: "owner", WorkspaceID: "control", Role: identity.RoleOwner},
				{Token: "control-viewer", UserID: "viewer", WorkspaceID: "control", Role: identity.RoleViewer},
				{Token: "control-foreign", UserID: "foreign", WorkspaceID: "foreign", Role: identity.RoleOwner},
			})
			if err != nil {
				t.Fatal(err)
			}
			server := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, tools, nil)
			if err := server.ConfigureAuthentication(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			handler := server.Handler()
			ctx := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "control", Role: identity.RoleOwner})
			project, err := store.CreateProject(ctx, "Control task")
			if err != nil {
				t.Fatal(err)
			}
			ref := &agentcontract.CapabilityRef{CapabilityID: "story_research_digest", Version: "1.0.0", SelectionMode: "explicit"}
			exchange, err := store.CreateMessageExchange(ctx, businessruntime.CreateMessageExchangeCommand{
				ConversationID: project.PrimaryConversationID, Request: agentcontract.MessageRequest{Content: "Research", CapabilityRef: ref},
				Decision: agentcontract.AgentDecision{Reply: "Queued", Intent: "propose_capability", Confidence: 1, CapabilityRef: ref,
					ProposedAction: &agentcontract.ProposedActionDraft{ActionType: "start_background_task", CapabilityRef: ref,
						Input: json.RawMessage(`{}`), Config: json.RawMessage(`{}`), RequiresConfirmation: false}},
			})
			if err != nil || exchange.TaskRef == nil {
				t.Fatalf("task: %+v %v", exchange, err)
			}
			task := exchange.TaskRef
			turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Cancel that research task"}, businessruntime.CommandMeta{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
				t.Fatal(err)
			}
			service := map[string]string{"Authorization": "Bearer control-service", "X-Agent-Project-ID": project.ProjectID, "X-Agent-Turn-ID": turn.AgentTurnID}
			base := "/api/v1/projects/" + project.ProjectID
			listPath := base + "/execution-targets?target_type=agent_task"
			listed := objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, listPath, nil, service, http.StatusOK), "data")
			if len(listed["items"].([]any)) != 1 {
				t.Fatalf("target list: %+v", listed)
			}
			inspectPath := base + "/execution-controls?target_type=agent_task&target_id=" + task.AgentTaskID
			snapshot := objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, inspectPath, nil, service, http.StatusOK), "data")
			performJSONWithHeaders(t, handler, http.MethodGet, inspectPath, nil, bearer("control-viewer", ""), http.StatusOK)
			performJSONWithHeaders(t, handler, http.MethodGet, inspectPath, nil, bearer("control-foreign", ""), http.StatusNotFound)
			performJSONWithHeaders(t, handler, http.MethodGet, listPath, nil, bearer("control-foreign", ""), http.StatusNotFound)
			args := map[string]any{"target_type": "agent_task", "target_id": task.AgentTaskID, "action_id": "cancel_agent_task", "snapshot_hash": snapshot["snapshot_hash"]}
			call := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls", map[string]any{
				"project_id": project.ProjectID, "conversation_id": project.PrimaryConversationID, "agent_turn_id": turn.AgentTurnID,
				"sdk_tool_call_id": "sdk-control", "tool_id": "runtime:control_execution", "arguments": args,
			}, service, http.StatusCreated), "data")
			path := "/internal/v1/agent-tool-calls/" + stringAt(t, call, "agent_tool_call_id")
			body := map[string]any{"sdk_tool_call_id": "sdk-control", "arguments": args}
			performJSONWithHeaders(t, handler, http.MethodPost, path+"/execution-control", body, service, http.StatusConflict)
			performJSONWithHeaders(t, handler, http.MethodPost, path+"/execution-control", body, bearer("control-owner", ""), http.StatusUnauthorized)
			performJSONWithHeaders(t, handler, http.MethodPost, path+"/execution-control", body, bearer("control-service", ""), http.StatusForbidden)
			approval := objectAt(t, call, "approval")
			if !strings.Contains(stringAt(t, approval, "title"), task.AgentTaskID) {
				t.Fatalf("approval hides exact target: %+v", approval)
			}
			resolution := map[string]any{"expected_version": approval["version"], "subject_snapshot_hash": approval["subject_snapshot_hash"], "action": "approve"}
			if decision == "reject" {
				resolution["action"] = "reject"
			}
			resolvePath := "/api/v1/agent-tool-approvals/" + stringAt(t, approval, "agent_tool_approval_id") + "/resolutions"
			key := "a2222222-2222-4222-8222-222222222222"
			performJSONWithHeaders(t, handler, http.MethodPost, resolvePath, resolution, bearer("control-viewer", key), http.StatusForbidden)
			performJSONWithHeaders(t, handler, http.MethodPost, resolvePath, resolution, bearer("control-owner", key), http.StatusOK)
			if decision == "reject" {
				performJSONWithHeaders(t, handler, http.MethodPost, path+"/execution-control", body, service, http.StatusConflict)
				fresh, err := store.GetAgentTask(ctx, task.AgentTaskID)
				if err != nil || fresh.Status != "queued" {
					t.Fatalf("rejected request changed task: %+v %v", fresh, err)
				}
				return
			}
			performJSONWithHeaders(t, handler, http.MethodPost, path+"/start", map[string]any{"expected_sdk_tool_call_id": "sdk-control"}, service, http.StatusOK)
			if decision == "stale" {
				if claim, err := store.ClaimAgentTask(ctx, businessruntime.ClaimAgentTaskCommand{WorkerID: "test", ProviderID: "sdk", ModelID: "fixture", LeaseSeconds: 60}); err != nil || claim == nil {
					t.Fatalf("claim: %+v %v", claim, err)
				}
				rejected := performJSONWithHeaders(t, handler, http.MethodPost, path+"/execution-control", body, service, http.StatusConflict)
				if objectAt(t, rejected, "error")["code"] != "EXECUTION_CONTROL_STALE" {
					t.Fatalf("wrong stale response: %+v", rejected)
				}
				return
			}
			result := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, path+"/execution-control", body, service, http.StatusOK), "data")
			if result["status"] != "cancelled" || result["target_id"] != task.AgentTaskID {
				t.Fatalf("receipt: %+v", result)
			}
			replay := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, path+"/execution-control", body, service, http.StatusOK), "data")
			if replay["status"] != result["status"] {
				t.Fatalf("receipt replay changed: %+v", replay)
			}
			if _, err := store.CancelAgentTurn(ctx, turn.AgentTurnID); err != nil {
				t.Fatal(err)
			}
			performJSONWithHeaders(t, handler, http.MethodPost, path+"/execution-control", body, service, http.StatusForbidden)
		})
	}
}
