package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/identity"
)

func outcomeReviewFixture(t *testing.T, mode string) (*Store, Project, AgentToolCall, string) {
	t.Helper()
	ctx := context.Background()
	var store *Store
	var project Project
	var call AgentToolCall
	var path string
	switch mode {
	case "conversation":
		var turn AgentTurn
		store, project, turn, path = pauseTurnFixture(t)
		claimInputTurn(t, store, turn.AgentTurnID)
		var err error
		call, err = store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
			AgentTurnID: turn.AgentTurnID, SDKToolCallID: "uncertain", ToolID: "mcp:fixture/save_fact", Arguments: json.RawMessage(`{"value":"fact"}`),
			ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`UPDATE agent_turns SET status='paused' WHERE agent_turn_id=?`, turn.AgentTurnID); err != nil {
			t.Fatal(err)
		}
	case "background_task":
		var command ClaimAgentTaskCommand
		var task AgentTask
		store, task, command = pausedBackgroundStore(t)
		claim, err := store.ClaimAgentTask(ctx, command)
		if err != nil || claim == nil {
			t.Fatal(err)
		}
		project, err = store.GetProject(ctx, task.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		call = backgroundApprovalForTest(t, store, *claim, "uncertain")
		if _, err := store.db.Exec(`UPDATE agent_task_attempts SET status='paused' WHERE agent_task_attempt_id=?`, claim.Attempt.AgentTaskAttemptID); err != nil {
			t.Fatal(err)
		}
	case "stateful_workflow":
		path = filepath.Join(t.TempDir(), "outcome.db")
		store = openProjectFilesTestStore(t, path)
		store.SetAgentToolRegistry(agentToolRegistryForTest(t))
		var claim *TaskClaim
		project, claim = statefulToolClaimForTest(t, store)
		call = statefulApprovalForTest(t, store, project, claim, "uncertain")
		if _, err := store.db.Exec(`UPDATE execution_attempts SET status='paused' WHERE attempt_id=?`, claim.Attempt.AttemptID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`UPDATE agent_tool_calls SET status='failed',started_at=?,error_code='MCP_TOOL_OUTCOME_UNKNOWN' WHERE agent_tool_call_id=?`, formatTime(store.now()), call.AgentToolCallID); err != nil {
		t.Fatal(err)
	}
	return store, project, call, path
}

func TestExternalToolReconciliationIdentityAndImmutableReceipt(t *testing.T) {
	for _, mode := range []string{"conversation", "background_task", "stateful_workflow"} {
		t.Run(mode, func(t *testing.T) {
			store, project, call, _ := outcomeReviewFixture(t, mode)
			defer store.Close()
			ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			view, err := store.GetAgentToolOutcomeReview(ctx, call.AgentToolCallID)
			if err != nil || !view.CanResolve || view.ExecutionMode != mode || view.ArgumentsHash != call.ArgumentsHash || view.ConfigurationHash == "" {
				t.Fatalf("view: %+v %v", view, err)
			}
			command := ResolveAgentToolOutcomeCommand{AgentToolCallID: call.AgentToolCallID, SubjectSnapshotHash: view.SubjectSnapshotHash, RequestID: "review-1", Outcome: "applied", Evidence: "Checked the provider record; original operation exists."}
			for _, principal := range []identity.Principal{identity.ServicePrincipal(), identity.SystemPrincipal(),
				{Kind: identity.KindUser, UserID: "other-editor", WorkspaceID: project.WorkspaceID, Role: identity.RoleEditor},
				{Kind: identity.KindUser, UserID: "viewer", WorkspaceID: project.WorkspaceID, Role: identity.RoleViewer}} {
				if principal.Kind == identity.KindUser {
					if err := store.BootstrapPrincipal(context.Background(), principal); err != nil {
						t.Fatal(err)
					}
				}
				other := identity.WithPrincipal(context.Background(), principal)
				_, err := store.ResolveAgentToolOutcome(other, command)
				assertDomainCode(t, err, "ROLE_FORBIDDEN")
			}
			_, err = store.ResolveAgentToolOutcome(WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: project.ProjectID}), command)
			assertDomainCode(t, err, "ROLE_FORBIDDEN")
			wrong := command
			wrong.SubjectSnapshotHash = strings.Repeat("0", 64)
			_, err = store.ResolveAgentToolOutcome(ctx, wrong)
			assertDomainCode(t, err, "AGENT_TOOL_OUTCOME_REVIEW_CONFLICT")
			resolved, err := store.ResolveAgentToolOutcome(ctx, command)
			if err != nil || resolved.CanResolve || resolved.Resolution == nil || resolved.Resolution.ActorUserID != identity.DefaultUserID {
				t.Fatal("resolution missing", err)
			}
			replayed, err := store.ResolveAgentToolOutcome(ctx, command)
			if err != nil || *replayed.Resolution != *resolved.Resolution {
				t.Fatal("lost acknowledgement changed resolution", err)
			}
			wrong = command
			wrong.Outcome = "not_applied"
			_, err = store.ResolveAgentToolOutcome(ctx, wrong)
			assertDomainCode(t, err, "AGENT_TOOL_OUTCOME_REVIEW_CONFLICT")
			stored, err := store.GetAgentToolCall(ctx, call.AgentToolCallID)
			if err != nil || stored.Status != "failed" || stored.ResultHash != nil {
				t.Fatal("manual fact overwrote provider outcome", err)
			}
			foreign := identity.Principal{Kind: identity.KindUser, UserID: "foreign", WorkspaceID: "foreign-workspace", Role: identity.RoleOwner}
			if err := store.BootstrapPrincipal(context.Background(), foreign); err != nil {
				t.Fatal(err)
			}
			_, err = store.GetAgentToolOutcomeReview(identity.WithPrincipal(context.Background(), foreign), call.AgentToolCallID)
			assertDomainCode(t, err, "PROJECT_NOT_FOUND")
			if _, err := store.db.Exec(`UPDATE agent_tool_calls SET arguments_hash='different' WHERE agent_tool_call_id=?`, call.AgentToolCallID); err != nil {
				t.Fatal(err)
			}
			_, err = store.GetAgentToolOutcomeReview(ctx, call.AgentToolCallID)
			assertDomainCode(t, err, "AGENT_TOOL_OUTCOME_REVIEW_CORRUPT")
		})
	}
}

func TestExternalToolReconciliationReopenQuotaMigrationAndDeletion(t *testing.T) {
	for _, deletion := range []string{"project", "workspace"} {
		t.Run(deletion, func(t *testing.T) {
			store, project, call, path := outcomeReviewFixture(t, "conversation")
			defer func() { store.Close() }()
			ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			if _, err := store.db.Exec(`DROP TABLE agent_tool_reconciliation_inputs; DROP TABLE agent_tool_reconciliations; PRAGMA user_version=53;`); err != nil {
				t.Fatal(err)
			}
			store.Close()
			var err error
			store, err = Open(path, loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			var backup string
			if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=53 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(backup); err != nil {
				t.Fatal(err)
			}
			view, err := store.GetAgentToolOutcomeReview(ctx, call.AgentToolCallID)
			if err != nil {
				t.Fatal(err)
			}
			command := ResolveAgentToolOutcomeCommand{AgentToolCallID: call.AgentToolCallID, SubjectSnapshotHash: view.SubjectSnapshotHash, RequestID: "review-1", Outcome: "not_applied", Evidence: "Checked remote history and confirmed no operation exists."}
			before, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE workspace_quotas SET max_storage_bytes=1 WHERE workspace_id=?`, project.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			_, err = store.ResolveAgentToolOutcome(ctx, command)
			assertDomainCode(t, err, "WORKSPACE_QUOTA_EXCEEDED")
			if _, err := store.db.Exec(`UPDATE workspace_quotas SET max_storage_bytes=100000 WHERE workspace_id=?`, project.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ResolveAgentToolOutcome(ctx, command); err != nil {
				t.Fatal(err)
			}
			store.Close()
			store, err = Open(path, loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			view, err = store.GetAgentToolOutcomeReview(ctx, call.AgentToolCallID)
			if err != nil || view.Resolution == nil || view.Resolution.Outcome != "not_applied" {
				t.Fatal("lost durable resolution", err)
			}
			if err := store.CheckWorkspaceStorageQuota(ctx, project.WorkspaceID, 100000); err == nil {
				t.Fatal("resolution omitted from quota")
			}
			latest, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
			if err != nil || before.SnapshotHash == latest.SnapshotHash || latest.Impact.ToolReconciliationCount != 1 {
				t.Fatal("resolution omitted from delete preview", err)
			}
			if deletion == "project" {
				_, err = store.ConfirmProjectDelete(ctx, ConfirmProjectDeleteCommand{ProjectID: project.ProjectID, PreviewHash: latest.SnapshotHash, Confirmed: true})
			} else {
				_, err = store.DeleteWorkspace(ctx, DeleteWorkspaceCommand{WorkspaceID: project.WorkspaceID, UserID: identity.DefaultUserID, Confirmation: project.WorkspaceID})
			}
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_tool_reconciliations`).Scan(&count); err != nil || count != 0 {
				t.Fatal("deleted evidence retained", err)
			}
			_, err = store.GetAgentToolOutcomeReview(ctx, call.AgentToolCallID)
			assertDomainCode(t, err, "PROJECT_NOT_FOUND")
		})
	}
}
