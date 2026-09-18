package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func TestQueuedAgentTurnEditDurableCASAndOriginalAcceptanceReceipt(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "queue.db")
	store, err := Open(database, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	project, err := store.CreateProject(ctx, "Queue")
	if err != nil {
		t.Fatal(err)
	}
	acceptMeta := CommandMeta{Scope: project.ProjectID, CommandType: "create_message", IdempotencyKey: "11111111-1111-4111-8111-111111111111", RequestHash: "original"}
	request := agentcontract.MessageRequest{Content: "Original request"}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, request, acceptMeta)
	if err != nil {
		t.Fatal(err)
	}
	var catalogBefore string
	if err := store.db.QueryRow(`SELECT COALESCE(json_group_array(descriptor_json), '[]') FROM agent_turn_skills WHERE agent_turn_id=? ORDER BY capability_id`, turn.AgentTurnID).Scan(&catalogBefore); err != nil {
		t.Fatal(err)
	}
	command := UpdateQueuedAgentTurnCommand{CommandMeta: CommandMeta{Scope: turn.AgentTurnID, CommandType: "update_queued_agent_turn", IdempotencyKey: "22222222-2222-4222-8222-222222222222", RequestHash: "edit"}, AgentTurnID: turn.AgentTurnID, ExpectedContent: request.Content, Content: "  Revised request  "}
	updated, err := store.UpdateQueuedAgentTurn(ctx, command)
	if err != nil || updated.Request.Content != "Revised request" || !updated.CreatedAt.Equal(turn.CreatedAt) || updated.StartedAt != nil {
		t.Fatalf("edit: %+v %v", updated, err)
	}
	var catalogAfter string
	if err := store.db.QueryRow(`SELECT COALESCE(json_group_array(descriptor_json), '[]') FROM agent_turn_skills WHERE agent_turn_id=? ORDER BY capability_id`, turn.AgentTurnID).Scan(&catalogAfter); err != nil {
		t.Fatal(err)
	}
	if catalogBefore != catalogAfter {
		t.Fatal("queue edit changed pinned Skill catalog")
	}
	stale := command
	stale.IdempotencyKey, stale.RequestHash = "33333333-3333-4333-8333-333333333333", "stale-edit"
	_, err = store.UpdateQueuedAgentTurn(ctx, stale)
	assertQueueError(t, err, "AGENT_TURN_CONTENT_CONFLICT")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(database, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.UpdateQueuedAgentTurn(ctx, command)
	if err != nil || repeated.Request.Content != updated.Request.Content {
		t.Fatalf("replayed edit: %+v %v", repeated, err)
	}
	accepted, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, request, acceptMeta)
	if err != nil || accepted.AgentTurnID != turn.AgentTurnID || accepted.Request.Content != updated.Request.Content {
		t.Fatalf("lost original acceptance response: %+v %v", accepted, err)
	}
	messages, err := store.ListMessages(ctx, project.PrimaryConversationID)
	if err != nil || len(messages) != 0 {
		t.Fatalf("queue edit committed messages: %+v %v", messages, err)
	}
	events, err := store.ListProjectEvents(ctx, project.ProjectID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events.Items {
		if event.EventType == "agent.turn.queued_updated" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("edit events=%d", count)
	}
	claimed, err := store.ClaimRunnableAgentTurns(ctx, 1)
	if err != nil || len(claimed) != 1 || claimed[0].Request.Content != updated.Request.Content {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	stale.ExpectedContent = updated.Request.Content
	_, err = store.UpdateQueuedAgentTurn(ctx, stale)
	assertQueueError(t, err, "AGENT_TURN_STATE_CONFLICT")
	// An uncertain edit response can be retried after claiming without changing execution input.
	repeated, err = store.UpdateQueuedAgentTurn(ctx, command)
	if err != nil || repeated.Status != "running" {
		t.Fatalf("late receipt: %+v %v", repeated, err)
	}
}

func TestQueuedAgentTurnEditFencesResumesPermissionsAndRetainsRequest(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "queue.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "queue", Role: identity.RoleOwner}
	if err := store.BootstrapPrincipal(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithPrincipal(context.Background(), principal)
	project, err := store.CreateProject(ctx, "Queue")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Original"}, CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	command := UpdateQueuedAgentTurnCommand{AgentTurnID: turn.AgentTurnID, Content: "Changed", ExpectedContent: "Original"}
	for _, test := range []struct {
		user, workspace string
		role            identity.Role
		code            string
	}{
		{"owner", "queue", identity.RoleViewer, "ROLE_FORBIDDEN"},
		{"collaborator", "queue", identity.RoleOwner, "ROLE_FORBIDDEN"},
		{"owner", "other", identity.RoleOwner, "PROJECT_NOT_FOUND"},
	} {
		other := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: test.user, WorkspaceID: test.workspace, Role: test.role})
		_, err := store.UpdateQueuedAgentTurn(other, command)
		assertQueueError(t, err, test.code)
	}
	_, err = store.UpdateQueuedAgentTurn(WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: project.ProjectID, AgentTurnID: turn.AgentTurnID}), command)
	assertQueueError(t, err, "ROLE_FORBIDDEN")
	for _, status := range []string{"running", "waiting_approval", "cancel_requested", "committing", "committed", "failed", "cancelled"} {
		if _, err := store.db.Exec(`UPDATE agent_turns SET status=? WHERE agent_turn_id=?`, status, turn.AgentTurnID); err != nil {
			t.Fatal(err)
		}
		_, err := store.UpdateQueuedAgentTurn(ctx, command)
		assertQueueError(t, err, "AGENT_TURN_STATE_CONFLICT")
	}
	if _, err := store.db.Exec(`UPDATE agent_turns SET status='accepted', started_at=? WHERE agent_turn_id=?`, formatTime(store.now()), turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateQueuedAgentTurn(ctx, command)
	assertQueueError(t, err, "AGENT_TURN_STATE_CONFLICT")
	if _, err := store.db.Exec(`UPDATE agent_turns SET started_at=NULL WHERE agent_turn_id=?`, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	command.Content = " \n "
	_, err = store.UpdateQueuedAgentTurn(ctx, command)
	assertQueueError(t, err, "REQUEST_VALIDATION_FAILED")
	// Preserve the complete already-accepted envelope, including opaque selection and attachment snapshots.
	turn.Request.AttachmentRefs = []agentcontract.AttachmentRef{{AssetID: "pinned-asset", AssetSnapshotID: "pinned-snapshot"}}
	raw, _ := json.Marshal(turn.Request)
	if _, err := store.db.Exec(`UPDATE agent_turns SET request_json=? WHERE agent_turn_id=?`, string(raw), turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateQueuedAgentTurn(ctx, command)
	if err != nil || updated.Request.Content != "" || len(updated.Request.AttachmentRefs) != 1 || updated.Request.AttachmentRefs[0] != turn.Request.AttachmentRefs[0] {
		t.Fatalf("envelope: %+v %v", updated, err)
	}
	command.ExpectedContent, command.Content = "", "Cannot replace checkpoint"
	if _, err := store.db.Exec(`INSERT INTO agent_turn_run_states(agent_turn_id, schema_version, state_json, state_hash, pending_sdk_tool_call_ids_json, checkpoint_version, created_at, updated_at) VALUES(?, 'fixture', '{}', 'fixture', '[]', 1, ?, ?)`, turn.AgentTurnID, formatTime(store.now()), formatTime(store.now())); err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateQueuedAgentTurn(ctx, command)
	assertQueueError(t, err, "AGENT_TURN_STATE_CONFLICT")
	if _, err := store.db.Exec(`UPDATE projects SET deleted_at=? WHERE project_id=?`, formatTime(store.now()), project.ProjectID); err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateQueuedAgentTurn(ctx, command)
	assertQueueError(t, err, "PROJECT_NOT_FOUND")
}

func assertQueueError(t *testing.T, err error, code string) {
	t.Helper()
	var domain *DomainError
	if !errors.As(err, &domain) || domain.Code != code {
		t.Fatalf("error=%v want=%s", err, code)
	}
}
