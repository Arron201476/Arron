package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/identity"
)

func toolApprovalCommandFixture(t *testing.T) (*Store, Project, AgentToolCall, ResolveAgentToolApprovalCommand) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "approval-command.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, err := store.CreateProject(context.Background(), "tool approval command")
	if err != nil {
		t.Fatal(err)
	}
	call, err := store.BeginAgentToolCall(context.Background(), BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: "sdk-write", ToolID: "mcp:fixture/save_fact",
		ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact"),
		Arguments:         json.RawMessage(`{"key":"city","value":"Shanghai"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if call.Approval == nil {
		t.Fatal("missing approval")
	}
	command := ResolveAgentToolApprovalCommand{
		CommandMeta:         CommandMeta{Scope: project.ProjectID, CommandType: "resolve_agent_tool_approval", IdempotencyKey: "approval-command", RequestHash: "original-http-body"},
		AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version,
		SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve",
	}
	return store, project, call, command
}

func toolApprovalCommandState(t *testing.T, store *Store, projectID string) string {
	t.Helper()
	var value string
	err := store.db.QueryRow(`SELECT json_array(
		(SELECT group_concat(status || ':' || version) FROM agent_tool_approvals WHERE project_id=?),
		(SELECT group_concat(status || ':' || approval_status) FROM agent_tool_calls WHERE project_id=?))`, projectID, projectID).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return publicControlState(t, store, projectID) + value
}

func TestToolApprovalRechecksAuthorizationBeforeWritesAndReceipts(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, scenario := range []string{"role", "membership", "user", "workspace", "project", "foreign-workspace", "scope", "agent", "service", "invalid-user"} {
			name := "fresh/" + scenario
			if cached {
				name = "cached/" + scenario
			}
			t.Run(name, func(t *testing.T) {
				store, project, _, command := toolApprovalCommandFixture(t)
				principal := identity.DefaultLocalPrincipal()
				ctx := identity.WithPrincipal(context.Background(), principal)
				if cached {
					if _, err := store.ResolveAgentToolApproval(ctx, command); err != nil {
						t.Fatal(err)
					}
				}
				code := "WORKSPACE_ACCESS_DENIED"
				var err error
				switch scenario {
				case "role":
					_, err = store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, project.WorkspaceID, principal.UserID)
					code = "ROLE_FORBIDDEN"
				case "membership":
					_, err = store.db.Exec(`UPDATE workspace_memberships SET status='disabled' WHERE workspace_id=? AND user_id=?`, project.WorkspaceID, principal.UserID)
				case "user":
					_, err = store.db.Exec(`UPDATE users SET status='disabled' WHERE user_id=?`, principal.UserID)
				case "workspace":
					_, err = store.db.Exec(`UPDATE workspaces SET status='deleting' WHERE workspace_id=?`, project.WorkspaceID)
				case "project":
					_, err = store.db.Exec(`UPDATE projects SET deleted_at='2026-09-09T00:00:00Z' WHERE project_id=?`, project.ProjectID)
					code = "PROJECT_NOT_FOUND"
				case "foreign-workspace":
					principal.WorkspaceID = "foreign-workspace"
					ctx = identity.WithPrincipal(context.Background(), principal)
					code = "PROJECT_NOT_FOUND"
				case "scope":
					command.Scope = "foreign-project"
					code = "REQUEST_VALIDATION_FAILED"
				case "agent":
					ctx = WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: project.ProjectID, AgentTurnID: "agent"})
				case "service":
					ctx = identity.WithDelegatedUser(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), principal)
				case "invalid-user":
					principal.UserID = ""
					ctx = identity.WithPrincipal(context.Background(), principal)
				}
				if err != nil {
					t.Fatal(err)
				}
				before := toolApprovalCommandState(t, store, project.ProjectID)
				_, err = store.ResolveAgentToolApproval(ctx, command)
				assertDomainCode(t, err, code)
				if after := toolApprovalCommandState(t, store, project.ProjectID); after != before {
					t.Fatalf("rejected command changed state: %s -> %s", before, after)
				}
			})
		}
	}
}

func TestToolApprovalReceiptSurvivesToolProgressWithoutRepeatingWrites(t *testing.T) {
	store, project, call, command := toolApprovalCommandFixture(t)
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	approved, err := store.ResolveAgentToolApproval(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.StartAgentToolCall(context.Background(), StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: toolConfigurationHashForTest(t, store, call.ToolID)}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompleteAgentToolCall(context.Background(), CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	before := toolApprovalCommandState(t, store, project.ProjectID)
	replayed, err := store.ResolveAgentToolApproval(ctx, command)
	if err != nil || replayed.AgentToolApprovalID != approved.AgentToolApprovalID || replayed.Version != approved.Version {
		t.Fatalf("replay = %+v, %v", replayed, err)
	}
	if after := toolApprovalCommandState(t, store, project.ProjectID); after != before {
		t.Fatalf("receipt repeated writes: %s -> %s", before, after)
	}
}

func TestToolApprovalRequiresBoundCallAndAvailableAction(t *testing.T) {
	for _, scenario := range []string{"call-project", "call-conversation", "call-workspace", "option"} {
		t.Run(scenario, func(t *testing.T) {
			store, project, call, command := toolApprovalCommandFixture(t)
			foreign, err := store.CreateProject(context.Background(), "other approval project")
			if err != nil {
				t.Fatal(err)
			}
			code := "AGENT_TOOL_APPROVAL_SUBJECT_CHANGED"
			switch scenario {
			case "call-project":
				_, err = store.db.Exec(`UPDATE agent_tool_approvals SET project_id=? WHERE agent_tool_approval_id=?`, foreign.ProjectID, call.Approval.AgentToolApprovalID)
				command.Scope = foreign.ProjectID
			case "call-conversation":
				_, err = store.db.Exec(`UPDATE agent_tool_approvals SET conversation_id=? WHERE agent_tool_approval_id=?`, foreign.PrimaryConversationID, call.Approval.AgentToolApprovalID)
			case "call-workspace":
				_, err = store.db.Exec(`UPDATE agent_tool_approvals SET workspace_id='foreign' WHERE agent_tool_approval_id=?`, call.Approval.AgentToolApprovalID)
			case "option":
				_, err = store.db.Exec(`UPDATE agent_tool_approvals SET options_json='["reject"]' WHERE agent_tool_approval_id=?`, call.Approval.AgentToolApprovalID)
				code = "AGENT_TOOL_APPROVAL_ACTION_UNAVAILABLE"
			}
			if err != nil {
				t.Fatal(err)
			}
			before := toolApprovalCommandState(t, store, project.ProjectID)
			_, err = store.ResolveAgentToolApproval(context.Background(), command)
			assertDomainCode(t, err, code)
			if after := toolApprovalCommandState(t, store, project.ProjectID); after != before {
				t.Fatal("rejected binding changed state")
			}
		})
	}
}

func TestToolApprovalCachedReceiptBindsEverySubjectField(t *testing.T) {
	store, _, call, command := toolApprovalCommandFixture(t)
	approved, err := store.ResolveAgentToolApproval(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"agent_tool_approval_id", "agent_tool_call_id", "project_id", "workspace_id", "conversation_id", "version", "subject_snapshot_hash", "status", "resolution"} {
		t.Run(field, func(t *testing.T) {
			encoded, err := json.Marshal(approved)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(encoded, &body); err != nil {
				t.Fatal(err)
			}
			body[field] = "foreign"
			if field == "version" {
				body[field] = approved.Version + 1
			}
			if field == "resolution" {
				body[field] = map[string]any{"action": "reject"}
			}
			encoded, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			_, err = decodeAgentToolApprovalReceipt(encoded, *call.Approval, command)
			assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
		})
	}
}
