package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func startProposalFixture(t *testing.T, mode string) (*Store, identity.Principal, Project, ProposedAction) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "start.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	user := identity.Principal{Kind: identity.KindUser, UserID: "start_author", WorkspaceID: "start_workspace", Role: identity.RoleAdmin}
	if err := store.BootstrapPrincipal(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithPrincipal(context.Background(), user)
	ref := &agentcontract.CapabilityRef{CapabilityID: "novel_to_script", Version: "1.4.0", SelectionMode: "explicit"}
	if mode != "legacy" {
		kind, name := "stateful", "story-review-workflow"
		if mode == "background" {
			kind, name = "background", "story-research-digest"
		}
		installed, err := store.InstallSkillDirectory(ctx, filepath.Join(testProjectRoot(t), "fixtures", "skills", kind, name), "fixture")
		if err != nil {
			t.Fatal(err)
		}
		ref.CapabilityID, ref.Version = installed.CapabilityID, "1.0.0"
	}
	project, err := store.CreateProject(ctx, "Start confirmation")
	if err != nil {
		t.Fatal(err)
	}
	if mode == "background" {
		exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
			ConversationID: project.PrimaryConversationID,
			Request:        agentcontract.MessageRequest{Content: "Research the story", CapabilityRef: ref},
			Decision: agentcontract.AgentDecision{Intent: "propose_capability", Reply: "Confirm research", Confidence: 1, CapabilityRef: ref,
				ProposedAction: &agentcontract.ProposedActionDraft{ActionType: "start_background_task", CapabilityRef: ref, Input: json.RawMessage(`{"topic":"Story research"}`), Config: json.RawMessage(`{}`), RequiresConfirmation: true}},
		})
		if err != nil || exchange.Action == nil {
			t.Fatalf("background proposal: %+v %v", exchange, err)
		}
		return store, user, project, *exchange.Action
	}
	action := configurationProposalForTest(t, store, ctx, project, ref)
	asset := createTextAsset(t, store, project.ProjectID, "source.txt", "A reporter discovers evidence.")
	sourceType := "novel"
	if mode == "managed" {
		sourceType = "story"
	}
	input := mustJSON(t, map[string]any{"source_type": sourceType, "user_notes": []string{}, "assets": []map[string]any{{"asset_id": asset.Asset.AssetID, "asset_snapshot_id": asset.Snapshot.AssetSnapshotID, "role": "primary_source", "order": 1}}})
	config := json.RawMessage(`{"config_ref":"creation","payload":{"target_episode_count":3,"episode_duration_minutes":2,"preserve_existing_episode_marks":true,"expansion_policy":"confirm_if_needed","user_requirements":[]}}`)
	if mode == "managed" {
		config = json.RawMessage(`{"review_depth":"deep"}`)
	}
	action, err = store.ConfigureProposedAction(ctx, ConfigureProposedActionCommand{ProposedActionID: action.ProposedActionID, ExpectedVersion: action.Version, Input: input, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	return store, user, project, action
}

func startFixtureProposal(store *Store, ctx context.Context, project Project, action ProposedAction, key string) (string, error) {
	meta := CommandMeta{Scope: project.ProjectID, CommandType: "start_proposal", IdempotencyKey: key, RequestHash: "original-confirmation"}
	if action.ActionType == "start_background_task" {
		task, err := store.StartAgentTask(ctx, StartAgentTaskCommand{CommandMeta: meta, ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, CapabilityID: action.CapabilityRef.CapabilityID, CapabilityVersion: action.CapabilityRef.Version, ProposedActionID: action.ProposedActionID, ProposedActionVersion: action.Version, ConfirmationMessageID: action.ConfirmationMessageID, ConfirmationSnapshotHash: action.SnapshotHash, Confirmed: true, Input: action.Input, Config: action.Config})
		return task.AgentTaskID, err
	}
	run, err := store.StartRun(ctx, StartRunCommand{CommandMeta: meta, ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, CapabilityID: action.CapabilityRef.CapabilityID, CapabilityVersion: action.CapabilityRef.Version, ProposedActionID: action.ProposedActionID, ProposedActionVersion: action.Version, ConfirmationMessageID: action.ConfirmationMessageID, ConfirmationSnapshotHash: action.SnapshotHash, Confirmed: true, Input: action.Input, Config: action.Config})
	return run.Run.RunID, err
}

func TestAllProposalStartModesRecheckFreshAndCachedPermissions(t *testing.T) {
	for _, mode := range []string{"legacy", "managed", "background"} {
		for _, cached := range []bool{false, true} {
			for _, scenario := range []struct{ name, query, code string }{
				{"role", `UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, "ROLE_FORBIDDEN"},
				{"membership", `UPDATE workspace_memberships SET status='disabled' WHERE workspace_id=? AND user_id=?`, "WORKSPACE_ACCESS_DENIED"},
				{"user", `UPDATE users SET status='disabled' WHERE user_id=?`, "WORKSPACE_ACCESS_DENIED"},
				{"workspace", `UPDATE workspaces SET status='deleting' WHERE workspace_id=?`, "WORKSPACE_ACCESS_DENIED"},
			} {
				stage := "fresh"
				if cached {
					stage = "receipt"
				}
				t.Run(mode+"/"+stage+"/"+scenario.name, func(t *testing.T) {
					store, user, project, action := startProposalFixture(t, mode)
					ctx := identity.WithPrincipal(context.Background(), user)
					if cached {
						id, err := startFixtureProposal(store, ctx, project, action, "original-key")
						if err != nil || id == "" {
							t.Fatalf("initial start: %q %v", id, err)
						}
						repeated, err := startFixtureProposal(store, ctx, project, action, "original-key")
						if err != nil || repeated != id {
							t.Fatalf("original receipt: %q %v", repeated, err)
						}
					}
					args := []any{user.WorkspaceID, user.UserID}
					if scenario.name == "user" {
						args = []any{user.UserID}
					}
					if scenario.name == "workspace" {
						args = []any{user.WorkspaceID}
					}
					if _, err := store.db.Exec(scenario.query, args...); err != nil {
						t.Fatal(err)
					}
					_, err := startFixtureProposal(store, ctx, project, action, "original-key")
					assertDomainCode(t, err, scenario.code)
					var executions, receipts int
					if err := store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM runs WHERE project_id=?) + (SELECT COUNT(*) FROM agent_tasks WHERE project_id=?), (SELECT COUNT(*) FROM idempotency_records WHERE scope=? AND command_type='start_proposal')`, project.ProjectID, project.ProjectID, project.ProjectID).Scan(&executions, &receipts); err != nil {
						t.Fatal(err)
					}
					want := 0
					if cached {
						want = 1
					}
					if executions != want || receipts != want {
						t.Fatalf("rejected start changed state: executions=%d receipts=%d", executions, receipts)
					}
				})
			}
		}
	}
}

func TestProposalStartReceiptsRequireExactConsumedTarget(t *testing.T) {
	runID, taskID := "run-original", "task-original"
	base := ProposedAction{ProposedActionID: "proposal", ProjectID: "project", ConversationID: "conversation", Status: "consumed"}
	for _, scenario := range []string{"valid", "pending", "different_target", "different_project", "different_conversation", "missing_target"} {
		t.Run(scenario, func(t *testing.T) {
			action := base
			action.ActionType, action.ConsumedRunID, action.ConsumedTaskID = "start_run", &runID, &taskID
			run := RunSnapshot{Run: Run{RunID: runID, ProjectID: base.ProjectID, ConversationID: base.ConversationID}}
			task := AgentTask{AgentTaskID: taskID, ProjectID: base.ProjectID, ConversationID: base.ConversationID}
			switch scenario {
			case "pending":
				action.Status = "pending"
			case "different_target":
				run.Run.RunID = "another-run"
				task.AgentTaskID = "another-task"
			case "different_project":
				run.Run.ProjectID = "another-project"
				task.ProjectID = "another-project"
			case "different_conversation":
				run.Run.ConversationID = "another-conversation"
				task.ConversationID = "another-conversation"
			case "missing_target":
				action.ConsumedRunID, action.ConsumedTaskID = nil, nil
			}
			_, runErr := decodeProposedRunReceipt(mustJSON(t, run), action)
			action.ActionType = "start_background_task"
			_, taskErr := decodeProposedTaskReceipt(mustJSON(t, task), action)
			if scenario == "valid" {
				if runErr != nil || taskErr != nil {
					t.Fatalf("valid receipts rejected: %v %v", runErr, taskErr)
				}
			} else {
				assertDomainCode(t, runErr, "IDEMPOTENCY_KEY_REUSED")
				assertDomainCode(t, taskErr, "IDEMPOTENCY_KEY_REUSED")
			}
		})
	}
}

func TestProposalSchemaReadAllowsViewerButNotRevokedMembership(t *testing.T) {
	store, user, project, action := startProposalFixture(t, "legacy")
	ctx := identity.WithPrincipal(context.Background(), user)
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, user.WorkspaceID, user.UserID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.CapabilityDefinitionForProject(ctx, project.ProjectID, action.CapabilityRef.CapabilityID, action.CapabilityRef.Version, action.ProposedActionID); err != nil || !found {
		t.Fatalf("viewer schema read: %v %v", found, err)
	}
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET status='disabled' WHERE workspace_id=? AND user_id=?`, user.WorkspaceID, user.UserID); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.CapabilityDefinitionForProject(ctx, project.ProjectID, action.CapabilityRef.CapabilityID, action.CapabilityRef.Version, action.ProposedActionID)
	assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
}
