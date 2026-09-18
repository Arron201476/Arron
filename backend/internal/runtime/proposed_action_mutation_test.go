package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func configurationProposalForTest(t *testing.T, store *Store, ctx context.Context, project Project, ref *agentcontract.CapabilityRef) ProposedAction {
	t.Helper()
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request:        agentcontract.MessageRequest{Content: "Inspect the supplied outline.", CapabilityRef: ref},
		Decision: agentcontract.AgentDecision{
			Reply: "Confirm the configuration", Intent: "propose_capability", Confidence: 1, CapabilityRef: ref,
			ProposedAction: &agentcontract.ProposedActionDraft{ActionType: "collect_run_configuration", CapabilityRef: ref,
				Input: json.RawMessage(`{}`), Config: json.RawMessage(`{}`)},
		},
	})
	if err != nil || exchange.Action == nil {
		t.Fatalf("create proposal: %+v %v", exchange, err)
	}
	return *exchange.Action
}

func TestProposedActionMutationRechecksAuthorizationBeforeFreshAndCachedCommands(t *testing.T) {
	for _, kind := range []string{"configure_proposed_action", "bind_proposed_action_input"} {
		for _, scenario := range []struct{ name, query, code string }{
			{"role", `UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, "ROLE_FORBIDDEN"},
			{"membership", `UPDATE workspace_memberships SET status='disabled' WHERE workspace_id=? AND user_id=?`, "WORKSPACE_ACCESS_DENIED"},
			{"user", `UPDATE users SET status='disabled' WHERE user_id=?`, "WORKSPACE_ACCESS_DENIED"},
			{"workspace", `UPDATE workspaces SET status='deleting' WHERE workspace_id=?`, "WORKSPACE_ACCESS_DENIED"},
			{"project", `UPDATE projects SET deleted_at='2026-09-09T00:00:00Z' WHERE project_id=?`, "PROPOSED_ACTION_NOT_FOUND"},
			{"foreign_workspace", "", "PROPOSED_ACTION_NOT_FOUND"},
		} {
			t.Run(kind+"/"+scenario.name, func(t *testing.T) {
				store, err := Open(filepath.Join(t.TempDir(), "proposals.db"), loadTestRegistry(t))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				user := identity.Principal{Kind: identity.KindUser, UserID: "proposal_author", WorkspaceID: "proposal_workspace", Role: identity.RoleEditor}
				if err := store.BootstrapPrincipal(context.Background(), user); err != nil {
					t.Fatal(err)
				}
				ctx := identity.WithPrincipal(context.Background(), user)
				project, err := store.CreateProject(ctx, "Proposal authorization")
				if err != nil {
					t.Fatal(err)
				}
				ref := &agentcontract.CapabilityRef{CapabilityID: "novel_to_script", Version: "1.4.0", SelectionMode: "explicit"}
				action := configurationProposalForTest(t, store, ctx, project, ref)
				var messageID string
				if err := store.db.QueryRow(`SELECT user_message_id FROM agent_decisions WHERE agent_decision_id=?`, action.AgentDecisionID).Scan(&messageID); err != nil {
					t.Fatal(err)
				}
				asset := createTextAsset(t, store, project.ProjectID, "source.txt", "Source text")
				input, err := json.Marshal(map[string]any{
					"project_id": project.ProjectID, "source_type": "novel", "user_notes": []string{},
					"user_request_message_id": messageID,
					"assets":                  []map[string]any{{"asset_id": asset.Asset.AssetID, "asset_snapshot_id": asset.Snapshot.AssetSnapshotID, "role": "primary_source", "order": 1}},
				})
				if err != nil {
					t.Fatal(err)
				}
				config := json.RawMessage(`{"config_ref":"creation","payload":{"target_episode_count":3,"episode_duration_minutes":2,"preserve_existing_episode_marks":true,"expansion_policy":"confirm_if_needed","user_requirements":[]}}`)
				meta := CommandMeta{Scope: project.ProjectID, CommandType: kind, IdempotencyKey: "proposal-save", RequestHash: "original-body"}
				mutate := func(callCtx context.Context, target ProposedAction, commandMeta CommandMeta) (ProposedAction, error) {
					if kind == "configure_proposed_action" {
						return store.ConfigureProposedAction(callCtx, ConfigureProposedActionCommand{CommandMeta: commandMeta, ProposedActionID: target.ProposedActionID, ExpectedVersion: target.Version, Input: input, Config: config})
					}
					return store.BindProposedActionInput(callCtx, BindProposedActionInputCommand{CommandMeta: commandMeta, ProposedActionID: target.ProposedActionID, ExpectedVersion: target.Version, Input: input})
				}
				saved, err := mutate(ctx, action, meta)
				if err != nil {
					t.Fatal(err)
				}
				if repeated, err := mutate(ctx, action, meta); err != nil || repeated.Version != saved.Version {
					t.Fatalf("original receipt: %+v %v", repeated, err)
				}
				other := configurationProposalForTest(t, store, ctx, project, ref)
				_, err = mutate(ctx, other, meta)
				assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
				wrongScope := meta
				wrongScope.Scope = "another-project"
				_, err = mutate(ctx, other, wrongScope)
				assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
				args := []any{user.WorkspaceID, user.UserID}
				switch scenario.name {
				case "user":
					args = []any{user.UserID}
				case "workspace":
					args = []any{user.WorkspaceID}
				case "project":
					args = []any{project.ProjectID}
				case "foreign_workspace":
					user.WorkspaceID = "another-workspace"
					ctx = identity.WithPrincipal(context.Background(), user)
				}
				if scenario.query != "" {
					if _, err := store.db.Exec(scenario.query, args...); err != nil {
						t.Fatal(err)
					}
				}
				_, err = mutate(ctx, action, meta)
				assertDomainCode(t, err, scenario.code)
				fresh := meta
				fresh.IdempotencyKey = "new-save"
				_, err = mutate(ctx, other, fresh)
				assertDomainCode(t, err, scenario.code)
				var version, receipts int
				if err := store.db.QueryRow(`SELECT version FROM proposed_actions WHERE proposed_action_id=?`, other.ProposedActionID).Scan(&version); err != nil {
					t.Fatal(err)
				}
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM idempotency_records WHERE scope=? AND command_type=?`, project.ProjectID, kind).Scan(&receipts); err != nil {
					t.Fatal(err)
				}
				if version != other.Version || receipts != 1 {
					t.Fatalf("rejected mutation changed state: version=%d receipts=%d", version, receipts)
				}
			})
		}
	}
}

func TestConfigureProposedActionUsesFrozenSchemaDespiteSameVersionShadow(t *testing.T) {
	ctx := context.Background()
	registry := loadTestRegistry(t)
	root := t.TempDir()
	source := filepath.Join(root, "story-review-workflow")
	fixture := filepath.Join(testProjectRoot(t), "fixtures", "skills", "stateful", "story-review-workflow")
	if err := copySkillTree(fixture, source); err != nil {
		t.Fatal(err)
	}
	if err := registry.AddSkillRoot(capability.SkillRoot{Scope: capability.SkillScopeWorkspace, Path: root}); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(t.TempDir(), "frozen.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "Frozen configuration")
	if err != nil {
		t.Fatal(err)
	}
	ref := &agentcontract.CapabilityRef{CapabilityID: "story_review_workflow", Version: "1.0.0", SelectionMode: "explicit"}
	turn := acceptSnapshotTurn(t, store, project, "configuration-origin", ref)
	action := configurationProposalForTest(t, store, snapshotTurnContext(turn), project, ref)
	shadowRoot := t.TempDir()
	shadow := filepath.Join(shadowRoot, "story-review-workflow")
	if err := copySkillTree(fixture, shadow); err != nil {
		t.Fatal(err)
	}
	setJSONFileFieldsForTest(t, filepath.Join(shadow, "schemas", "config.json"), map[string]any{
		"$defs": map[string]any{"config": map[string]any{"type": "object", "required": []string{"review_depth"}, "properties": map[string]any{"review_depth": map[string]any{"type": "string", "enum": []string{"shadow_only"}, "default": "shadow_only"}}}},
	})
	if err := registry.AddSkillRoot(capability.SkillRoot{Scope: capability.SkillScopeUser, Path: shadowRoot, Priority: 1000}); err != nil {
		t.Fatal(err)
	}
	live, found, err := store.capabilityEntryForProjectVersion(ctx, project.ProjectID, ref.CapabilityID, ref.Version)
	if err != nil || !found {
		t.Fatalf("live shadow: %+v %v", live, err)
	}
	config := json.RawMessage(`{"review_depth":"deep"}`)
	if _, err := normalizeManagedWorkflowConfig(live, config); err == nil {
		t.Fatal("shadow fixture did not reject the original configuration")
	}
	definition, found, err := store.CapabilityDefinitionForProject(ctx, project.ProjectID, ref.CapabilityID, ref.Version, action.ProposedActionID)
	if err != nil || !found {
		t.Fatalf("frozen proposal definition: %+v %v", definition, err)
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"deep"`) || strings.Contains(string(encoded), "shadow_only") {
		t.Fatalf("proposal showed the shadow schema: %s", encoded)
	}
	for _, wrong := range []struct{ projectID, capabilityID, version, actionID string }{
		{"other-project", ref.CapabilityID, ref.Version, action.ProposedActionID},
		{project.ProjectID, "other-capability", ref.Version, action.ProposedActionID},
		{project.ProjectID, ref.CapabilityID, "2.0.0", action.ProposedActionID},
		{project.ProjectID, ref.CapabilityID, ref.Version, "other-action"},
	} {
		_, _, err := store.CapabilityDefinitionForProject(ctx, wrong.projectID, wrong.capabilityID, wrong.version, wrong.actionID)
		assertDomainCode(t, err, "PROPOSED_ACTION_NOT_FOUND")
	}
	asset := createTextAsset(t, store, project.ProjectID, "premise.txt", "A reporter discovers evidence.")
	input, err := json.Marshal(map[string]any{"assets": []map[string]any{{"asset_id": asset.Asset.AssetID, "asset_snapshot_id": asset.Snapshot.AssetSnapshotID, "role": "primary_source", "order": 1}}})
	if err != nil {
		t.Fatal(err)
	}
	configured, err := store.ConfigureProposedAction(ctx, ConfigureProposedActionCommand{ProposedActionID: action.ProposedActionID, ExpectedVersion: action.Version, Input: input, Config: config})
	if err != nil || configured.ProposedActionID != action.ProposedActionID || configured.ActionType != "start_run" {
		t.Fatalf("frozen proposal configuration: %+v %v", configured, err)
	}
}
