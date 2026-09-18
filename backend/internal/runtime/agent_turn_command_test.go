package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func userTurnCommandFixture(t *testing.T, action string) (*Store, AgentTurn, CommandMeta, func(context.Context, CommandMeta) (AgentTurn, error)) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "turn-command.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	project, err := store.CreateProject(context.Background(), "Turn command")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(context.Background(), project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Original"}, CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if action == "resume" {
		if _, err := store.RequestAgentTurnPause(context.Background(), ControlAgentTurnCommand{AgentTurnID: turn.AgentTurnID}); err != nil {
			t.Fatal(err)
		}
	}
	meta := CommandMeta{Scope: turn.AgentTurnID, CommandType: "user-turn-" + action, IdempotencyKey: "turn-command", RequestHash: "original-body"}
	invoke := func(ctx context.Context, meta CommandMeta) (AgentTurn, error) {
		switch action {
		case "edit":
			return store.UpdateQueuedAgentTurn(ctx, UpdateQueuedAgentTurnCommand{CommandMeta: meta, AgentTurnID: turn.AgentTurnID, ExpectedContent: "Original", Content: "Changed"})
		case "pause":
			return store.RequestAgentTurnPause(ctx, ControlAgentTurnCommand{CommandMeta: meta, AgentTurnID: turn.AgentTurnID})
		case "resume":
			return store.ResumeAgentTurn(ctx, ControlAgentTurnCommand{CommandMeta: meta, AgentTurnID: turn.AgentTurnID})
		default:
			result, err := store.CancelAgentTurn(ctx, turn.AgentTurnID)
			return result.Turn, err
		}
	}
	return store, turn, meta, invoke
}

func userTurnCommandState(t *testing.T, store *Store, turn AgentTurn) string {
	t.Helper()
	var state string
	err := store.db.QueryRow(`SELECT json_array(status,request_json,updated_at,cancel_reason,
		(SELECT COUNT(*) FROM agent_turn_run_states WHERE agent_turn_id=?)) FROM agent_turns WHERE agent_turn_id=?`, turn.AgentTurnID, turn.AgentTurnID).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return publicControlState(t, store, turn.ProjectID) + state
}

func TestUserTurnCommandsRecheckPermissionOnFirstAndRepeatedRequests(t *testing.T) {
	for _, action := range []string{"edit", "pause", "resume", "cancel"} {
		for _, repeated := range []bool{false, true} {
			for _, scenario := range []string{"role", "membership", "user", "workspace", "project", "foreign-workspace", "service", "agent"} {
				name := action + "/fresh/" + scenario
				if repeated {
					name = action + "/repeated/" + scenario
				}
				t.Run(name, func(t *testing.T) {
					store, turn, meta, invoke := userTurnCommandFixture(t, action)
					principal := identity.DefaultLocalPrincipal()
					ctx := identity.WithPrincipal(context.Background(), principal)
					if repeated {
						if _, err := invoke(ctx, meta); err != nil {
							t.Fatal(err)
						}
					}
					code := "WORKSPACE_ACCESS_DENIED"
					var err error
					switch scenario {
					case "role":
						_, err = store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, turn.WorkspaceID, principal.UserID)
						code = "ROLE_FORBIDDEN"
					case "membership":
						_, err = store.db.Exec(`UPDATE workspace_memberships SET status='disabled' WHERE workspace_id=? AND user_id=?`, turn.WorkspaceID, principal.UserID)
					case "user":
						_, err = store.db.Exec(`UPDATE users SET status='disabled' WHERE user_id=?`, principal.UserID)
					case "workspace":
						_, err = store.db.Exec(`UPDATE workspaces SET status='deleting' WHERE workspace_id=?`, turn.WorkspaceID)
					case "project":
						_, err = store.db.Exec(`UPDATE projects SET deleted_at='2026-09-09T00:00:00Z' WHERE project_id=?`, turn.ProjectID)
						code = "PROJECT_NOT_FOUND"
					case "foreign-workspace":
						principal.WorkspaceID = "foreign"
						ctx = identity.WithPrincipal(context.Background(), principal)
						code = "PROJECT_NOT_FOUND"
					case "service":
						ctx = identity.WithDelegatedUser(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), principal)
						code = "ROLE_FORBIDDEN"
					case "agent":
						ctx = WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: turn.ProjectID, AgentTurnID: turn.AgentTurnID})
						code = "ROLE_FORBIDDEN"
					}
					if err != nil {
						t.Fatal(err)
					}
					before := userTurnCommandState(t, store, turn)
					_, err = invoke(ctx, meta)
					assertDomainCode(t, err, code)
					if after := userTurnCommandState(t, store, turn); after != before {
						t.Fatalf("denied command changed state: %s -> %s", before, after)
					}
				})
			}
		}
	}
}

func TestUserTurnControlsPreserveWorkspaceCancellationButRequireAuthorForOtherWrites(t *testing.T) {
	for _, action := range []string{"edit", "pause", "resume", "cancel"} {
		t.Run(action, func(t *testing.T) {
			store, turn, meta, invoke := userTurnCommandFixture(t, action)
			other := identity.DefaultLocalPrincipal()
			other.UserID, other.Role = "collaborator", identity.RoleEditor
			if err := store.BootstrapPrincipal(context.Background(), other); err != nil {
				t.Fatal(err)
			}
			ctx := identity.WithPrincipal(context.Background(), other)
			before := userTurnCommandState(t, store, turn)
			result, err := invoke(ctx, meta)
			if action == "cancel" {
				if err != nil || result.Status != "cancelled" {
					t.Fatalf("collaborator cancellation: %+v %v", result, err)
				}
				after := userTurnCommandState(t, store, turn)
				if _, err := invoke(ctx, meta); err != nil {
					t.Fatal(err)
				}
				if again := userTurnCommandState(t, store, turn); again != after {
					t.Fatal("repeated cancel wrote state")
				}
			} else {
				assertDomainCode(t, err, "ROLE_FORBIDDEN")
				if after := userTurnCommandState(t, store, turn); after != before {
					t.Fatal("non-author command changed state")
				}
			}
		})
	}
}

func TestUserTurnReceiptsRequireExactTurnScopeAndOwner(t *testing.T) {
	for _, action := range []string{"edit", "pause", "resume"} {
		for _, scenario := range []string{"scope", "turn", "project", "workspace", "conversation", "user", "body-or-action"} {
			t.Run(action+"/"+scenario, func(t *testing.T) {
				store, turn, meta, invoke := userTurnCommandFixture(t, action)
				result, err := invoke(context.Background(), meta)
				if err != nil {
					t.Fatal(err)
				}
				code := "IDEMPOTENCY_KEY_REUSED"
				switch scenario {
				case "scope":
					meta.Scope, code = "foreign", "REQUEST_VALIDATION_FAILED"
				case "turn":
					result.AgentTurnID = "foreign"
				case "project":
					result.ProjectID = "foreign"
				case "workspace":
					result.WorkspaceID = "foreign"
				case "conversation":
					result.ConversationID = "foreign"
				case "user":
					result.UserID = "foreign"
				case "body-or-action":
					if action == "edit" {
						result.Request.Content = "foreign"
					} else {
						result.Status = "cancelled"
					}
				}
				if scenario != "scope" {
					body, err := json.Marshal(result)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := store.db.Exec(`UPDATE idempotency_records SET response_json=? WHERE scope=? AND command_type=? AND idempotency_key=?`, string(body), meta.Scope, meta.CommandType, meta.IdempotencyKey); err != nil {
						t.Fatal(err)
					}
				}
				before := userTurnCommandState(t, store, turn)
				_, err = invoke(context.Background(), meta)
				assertDomainCode(t, err, code)
				if after := userTurnCommandState(t, store, turn); after != before {
					t.Fatal("invalid receipt changed turn")
				}
			})
		}
	}
}
