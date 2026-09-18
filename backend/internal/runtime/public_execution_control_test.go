package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/identity"
)

// These synthetic receipts isolate authorization and target checks from each
// transition's lifecycle fixtures; they do not prove a successful transition.
func seedPublicControlReceipt(t *testing.T, store *Store, meta CommandMeta, receipt any) {
	t.Helper()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, _, err := store.beginIdempotency(ctx, tx, meta); err != nil {
		t.Fatal(err)
	}
	if err := completeIdempotency(ctx, tx, meta, receipt, store.now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func publicControlFixture(t *testing.T, action string) (*Store, Project, any, func(context.Context, CommandMeta) error) {
	t.Helper()
	if strings.HasSuffix(action, "agent_task") {
		store, err := Open(filepath.Join(t.TempDir(), "public-control.db"), loadBackgroundTaskRegistry(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		task := createBackgroundTaskForTest(t, store)
		project, err := store.GetProject(context.Background(), task.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		return store, project, task, func(ctx context.Context, meta CommandMeta) error {
			var err error
			switch action {
			case "pause_agent_task":
				_, err = store.RequestAgentTaskPause(ctx, PauseAgentTaskCommand{CommandMeta: meta, AgentTaskID: task.AgentTaskID})
			case "resume_agent_task":
				_, err = store.ResumeAgentTask(ctx, ResumeAgentTaskCommand{CommandMeta: meta, AgentTaskID: task.AgentTaskID})
			case "cancel_agent_task":
				_, err = store.CancelAgentTask(ctx, CancelAgentTaskCommand{CommandMeta: meta, AgentTaskID: task.AgentTaskID})
			case "retry_agent_task":
				_, err = store.RetryAgentTask(ctx, RetryAgentTaskCommand{CommandMeta: meta, AgentTaskID: task.AgentTaskID})
			}
			return err
		}
	}
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "public-control.db"))
	t.Cleanup(func() { store.Close() })
	project, claim := statefulToolClaimForTest(t, store)
	snapshot, err := store.GetRunSnapshot(context.Background(), claim.Attempt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return store, project, snapshot, func(ctx context.Context, meta CommandMeta) error {
		var err error
		switch action {
		case "pause_run":
			_, err = store.RequestRunPause(ctx, PauseRunCommand{CommandMeta: meta, RunID: snapshot.Run.RunID})
		case "set_episode_execution_mode":
			_, err = store.SetEpisodeExecutionMode(ctx, SetEpisodeExecutionModeCommand{CommandMeta: meta, RunID: snapshot.Run.RunID, Mode: episodeExecutionModeContinuous})
		case "resume_run":
			_, err = store.ResumeRun(ctx, ResumeRunCommand{CommandMeta: meta, RunID: snapshot.Run.RunID})
		case "cancel_run":
			_, err = store.CancelRun(ctx, CancelRunCommand{CommandMeta: meta, RunID: snapshot.Run.RunID, Confirmed: true})
		case "retry_failed_step":
			_, err = store.RetryFailedStep(ctx, RetryFailedStepCommand{CommandMeta: meta, StepRunID: *snapshot.Run.CurrentStepRunID})
		case "continue_with_partial_results":
			_, err = store.ContinueWithPartialResults(ctx, ContinueWithPartialResultsCommand{CommandMeta: meta, StepRunID: *snapshot.Run.CurrentStepRunID, Confirmed: true})
		}
		return err
	}
}

func publicControlState(t *testing.T, store *Store, projectID string) string {
	t.Helper()
	var state string
	err := store.db.QueryRow(`SELECT json_array(version, status,
		(SELECT COUNT(*) FROM events), (SELECT COUNT(*) FROM idempotency_records),
		(SELECT group_concat(status) FROM runs WHERE project_id=?),
		(SELECT group_concat(status) FROM agent_tasks WHERE project_id=?))
		FROM projects WHERE project_id=?`, projectID, projectID, projectID).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestPublicExecutionControlsRecheckRoleBeforeFreshWritesAndCachedReceipts(t *testing.T) {
	for _, action := range []string{"pause_run", "resume_run", "cancel_run", "set_episode_execution_mode", "retry_failed_step", "continue_with_partial_results", "pause_agent_task", "resume_agent_task", "cancel_agent_task", "retry_agent_task"} {
		for _, cached := range []bool{false, true} {
			name := action + "/fresh"
			if cached {
				name = action + "/cached"
			}
			t.Run(name, func(t *testing.T) {
				store, project, receipt, invoke := publicControlFixture(t, action)
				meta := CommandMeta{Scope: project.ProjectID, CommandType: action, IdempotencyKey: "public-control", RequestHash: "original-http-request"}
				principal := identity.DefaultLocalPrincipal()
				ctx := identity.WithPrincipal(context.Background(), principal)
				if cached {
					seedPublicControlReceipt(t, store, meta, receipt)
					if err := invoke(ctx, meta); err != nil {
						t.Fatalf("authorized receipt: %v", err)
					}
				}
				if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, project.WorkspaceID, principal.UserID); err != nil {
					t.Fatal(err)
				}
				before := publicControlState(t, store, project.ProjectID)
				assertDomainCode(t, invoke(ctx, meta), "ROLE_FORBIDDEN")
				if after := publicControlState(t, store, project.ProjectID); after != before {
					t.Fatalf("unauthorized command changed state: %s -> %s", before, after)
				}
			})
		}
	}
}

func TestPublicExecutionControlReceiptRequiresCurrentProjectAndPrincipal(t *testing.T) {
	for _, scenario := range []string{"membership", "user", "workspace", "project", "foreign-workspace", "scope", "foreign-receipt", "changed-body", "unaudited-agent"} {
		t.Run(scenario, func(t *testing.T) {
			store, project, receipt, invoke := publicControlFixture(t, "pause_run")
			meta := CommandMeta{Scope: project.ProjectID, CommandType: "pause_run", IdempotencyKey: "public-control", RequestHash: "original-http-request"}
			principal := identity.DefaultLocalPrincipal()
			ctx := identity.WithPrincipal(context.Background(), principal)
			code := "WORKSPACE_ACCESS_DENIED"
			if scenario == "foreign-receipt" {
				snapshot := receipt.(RunSnapshot)
				snapshot.Run.RunID = "another-run"
				receipt = snapshot
			}
			seedPublicControlReceipt(t, store, meta, receipt)
			var err error
			switch scenario {
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
				meta.Scope = "foreign-project"
				code = "REQUEST_VALIDATION_FAILED"
			case "foreign-receipt":
				code = "IDEMPOTENCY_RESULT_INVALID"
			case "changed-body":
				meta.RequestHash = "changed-http-request"
				code = "IDEMPOTENCY_KEY_REUSED"
			case "unaudited-agent":
				ctx = WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: project.ProjectID, AgentTurnID: "unapproved"})
				code = "AGENT_TOOL_APPROVAL_REQUIRED"
			}
			if err != nil {
				t.Fatal(err)
			}
			before := publicControlState(t, store, project.ProjectID)
			assertDomainCode(t, invoke(ctx, meta), code)
			if after := publicControlState(t, store, project.ProjectID); after != before {
				t.Fatalf("rejected command changed state: %s -> %s", before, after)
			}
		})
	}
}

func TestPartialContinuationCannotBypassAgentApproval(t *testing.T) {
	store, project, _, invoke := publicControlFixture(t, "continue_with_partial_results")
	ctx := WithAgentActivity(identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal()), AgentActivityIdentity{ProjectID: project.ProjectID, AgentTurnID: "unapproved"})
	before := publicControlState(t, store, project.ProjectID)
	assertDomainCode(t, invoke(ctx, CommandMeta{}), "AGENT_TOOL_APPROVAL_REQUIRED")
	if after := publicControlState(t, store, project.ProjectID); after != before {
		t.Fatalf("unaudited continuation changed state: %s -> %s", before, after)
	}
}
