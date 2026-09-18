package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func TestAgentTurnAcceptanceRechecksPermissionBeforeFreshOrCachedSubmission(t *testing.T) {
	for _, scenario := range []struct{ name, query, code string }{
		{"role", `UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, "ROLE_FORBIDDEN"},
		{"membership", `UPDATE workspace_memberships SET status='disabled' WHERE workspace_id=? AND user_id=?`, "WORKSPACE_ACCESS_DENIED"},
		{"user", `UPDATE users SET status='disabled' WHERE user_id=?`, "WORKSPACE_ACCESS_DENIED"},
		{"workspace", `UPDATE workspaces SET status='deleting' WHERE workspace_id=?`, "WORKSPACE_ACCESS_DENIED"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "accept.db"), loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			user := identity.Principal{Kind: identity.KindUser, UserID: "submission_author", WorkspaceID: "submission_workspace", Role: identity.RoleEditor}
			if err := store.BootstrapPrincipal(context.Background(), user); err != nil {
				t.Fatal(err)
			}
			ctx := identity.WithPrincipal(context.Background(), user)
			project, err := store.CreateProject(ctx, "Submission authorization")
			if err != nil {
				t.Fatal(err)
			}
			request := agentcontract.MessageRequest{Content: "Original submission"}
			meta := CommandMeta{Scope: project.ProjectID, CommandType: "create_message", IdempotencyKey: "11111111-1111-4111-8111-111111111111", RequestHash: "original"}
			if _, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, request, meta); err != nil {
				t.Fatal(err)
			}
			args := []any{user.WorkspaceID, user.UserID}
			if scenario.name == "user" {
				args = []any{user.UserID}
			} else if scenario.name == "workspace" {
				args = []any{user.WorkspaceID}
			}
			if _, err := store.db.Exec(scenario.query, args...); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{meta.IdempotencyKey, "22222222-2222-4222-8222-222222222222"} {
				attempt := meta
				attempt.IdempotencyKey = key
				_, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, request, attempt)
				assertDomainCode(t, err, scenario.code)
				_, _, err = store.LookupAgentTurnSubmission(ctx, project.PrimaryConversationID, key)
				assertDomainCode(t, err, scenario.code)
			}
			var turns, records int
			if err := store.db.QueryRow(`SELECT
				(SELECT COUNT(*) FROM agent_turns WHERE project_id=?),
				(SELECT COUNT(*) FROM idempotency_records WHERE scope=? AND command_type='create_message')`, project.ProjectID, project.ProjectID).Scan(&turns, &records); err != nil {
				t.Fatal(err)
			}
			if turns != 1 || records != 1 {
				t.Fatalf("revoked submission changed durable state: turns=%d receipts=%d", turns, records)
			}
		})
	}
}

func TestAgentTurnAcceptanceReceiptCannotBeReusedByAnotherAuthor(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "authors.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := identity.Principal{Kind: identity.KindUser, UserID: "first", WorkspaceID: "authors", Role: identity.RoleEditor}
	second := first
	second.UserID = "second"
	for _, user := range []identity.Principal{first, second} {
		if err := store.BootstrapPrincipal(context.Background(), user); err != nil {
			t.Fatal(err)
		}
	}
	ctx := identity.WithPrincipal(context.Background(), first)
	other := identity.WithPrincipal(context.Background(), second)
	project, err := store.CreateProject(ctx, "Shared project")
	if err != nil {
		t.Fatal(err)
	}
	request := agentcontract.MessageRequest{Content: "Identical request"}
	meta := CommandMeta{Scope: project.ProjectID, CommandType: "create_message", IdempotencyKey: "11111111-1111-4111-8111-111111111111", RequestHash: "same-request"}
	accepted, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, request, meta)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AcceptAgentTurn(other, project.PrimaryConversationID, request, meta)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	_, _, err = store.LookupAgentTurnSubmission(other, project.PrimaryConversationID, meta.IdempotencyKey)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	repeated, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, request, meta)
	if err != nil || repeated.AgentTurnID != accepted.AgentTurnID {
		t.Fatalf("original author lost the durable receipt: %+v %v", repeated, err)
	}
	meta.IdempotencyKey = "22222222-2222-4222-8222-222222222222"
	created, err := store.AcceptAgentTurn(other, project.PrimaryConversationID, request, meta)
	if err != nil || created.UserID != second.UserID || created.AgentTurnID == accepted.AgentTurnID || created.SubmissionID == accepted.SubmissionID {
		t.Fatalf("independent authorized submission failed: %+v %v", created, err)
	}
}
