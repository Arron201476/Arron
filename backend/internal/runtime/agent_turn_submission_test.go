package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func TestAgentTurnSubmissionLookupRetainsAcceptanceAcrossQueueEditsAndReopen(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "submission.db")
	store, err := Open(database, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	project, err := store.CreateProject(ctx, "Submission recovery")
	if err != nil {
		t.Fatal(err)
	}
	meta := CommandMeta{Scope: project.ProjectID, CommandType: "create_message", IdempotencyKey: "11111111-1111-4111-8111-111111111111", RequestHash: "message-request-v2:original"}
	projectID, missing, err := store.LookupAgentTurnSubmission(ctx, project.PrimaryConversationID, meta.IdempotencyKey)
	if err != nil || projectID != project.ProjectID || missing != nil {
		t.Fatalf("new lookup: %q %+v %v", projectID, missing, err)
	}
	var records int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM idempotency_records WHERE scope=?`, project.ProjectID).Scan(&records); err != nil || records != 0 {
		t.Fatalf("read-only lookup wrote a command: %d %v", records, err)
	}
	request := agentcontract.MessageRequest{Content: "Original message"}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, request, meta)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateQueuedAgentTurn(ctx, UpdateQueuedAgentTurnCommand{AgentTurnID: turn.AgentTurnID, ExpectedContent: request.Content, Content: "Later queue edit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE idempotency_records SET expires_at='2000-01-01T00:00:00Z' WHERE scope=?`, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(database, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	projectID, receipt, err := store.LookupAgentTurnSubmission(ctx, project.PrimaryConversationID, meta.IdempotencyKey)
	if err != nil || projectID != project.ProjectID || receipt == nil || receipt.RequestHash != meta.RequestHash || receipt.Request.Content != request.Content || receipt.Turn.Request.Content != "Later queue edit" || receipt.Turn.AgentTurnID != turn.AgentTurnID {
		t.Fatalf("reopened receipt: %q %+v %v", projectID, receipt, err)
	}
	repeated, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, request, meta)
	if err != nil || repeated.AgentTurnID != turn.AgentTurnID || repeated.Request.Content != "Later queue edit" {
		t.Fatalf("acceptance replay overwrote queue edit: %+v %v", repeated, err)
	}
	turns, err := store.ListProjectAgentTurns(ctx, project.ProjectID, 10)
	if err != nil || len(turns) != 1 {
		t.Fatalf("recovery created another turn: %+v %v", turns, err)
	}
}

func TestAgentTurnSubmissionRejectsCorruptOrMissingReceipts(t *testing.T) {
	for _, scenario := range []string{"missing", "running", "failed", "empty", "json", "hash", "different-key", "workspace", "user", "conversation", "created", "status", "submission"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "receipt.db"), loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			project, err := store.CreateProject(ctx, "Receipt integrity")
			if err != nil {
				t.Fatal(err)
			}
			meta := CommandMeta{Scope: project.ProjectID, CommandType: "create_message", IdempotencyKey: "11111111-1111-4111-8111-111111111111", RequestHash: "original"}
			turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Original"}, meta)
			if err != nil {
				t.Fatal(err)
			}
			code, expectedTurns := "IDEMPOTENCY_RESULT_INVALID", 1
			switch scenario {
			case "missing":
				_, err = store.db.Exec(`DELETE FROM idempotency_records WHERE scope=?`, project.ProjectID)
			case "running", "failed":
				_, err = store.db.Exec(`UPDATE idempotency_records SET status=? WHERE scope=?`, scenario, project.ProjectID)
				code = "COMMAND_IN_PROGRESS"
			case "empty":
				_, err = store.db.Exec(`UPDATE idempotency_records SET response_json=NULL WHERE scope=?`, project.ProjectID)
			case "json":
				_, err = store.db.Exec(`UPDATE idempotency_records SET response_json='broken' WHERE scope=?`, project.ProjectID)
			case "hash":
				_, err = store.db.Exec(`UPDATE idempotency_records SET request_hash='' WHERE scope=?`, project.ProjectID)
			default:
				changed := turn
				switch scenario {
				case "different-key":
					otherMeta := meta
					otherMeta.IdempotencyKey = "22222222-2222-4222-8222-222222222222"
					changed, err = store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Other message"}, otherMeta)
					expectedTurns, code = 2, "IDEMPOTENCY_KEY_REUSED"
				case "workspace":
					changed.WorkspaceID = "foreign"
				case "user":
					changed.UserID = "foreign"
				case "conversation":
					changed.ConversationID = "foreign"
				case "created":
					changed.CreatedAt = changed.CreatedAt.Add(time.Second)
				case "status":
					changed.Status = "committed"
				case "submission":
					changed.SubmissionID = "foreign"
				}
				if err != nil {
					t.Fatal(err)
				}
				var raw []byte
				raw, err = json.Marshal(changed)
				if err == nil {
					_, err = store.db.Exec(`UPDATE idempotency_records SET response_json=? WHERE scope=? AND command_type='create_message' AND idempotency_key=?`, string(raw), project.ProjectID, meta.IdempotencyKey)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			_, receipt, err := store.LookupAgentTurnSubmission(ctx, project.PrimaryConversationID, meta.IdempotencyKey)
			assertDomainCode(t, err, code)
			if receipt != nil {
				t.Fatal("corrupt acceptance exposed an execution")
			}
			turns, err := store.ListProjectAgentTurns(ctx, project.ProjectID, 10)
			if err != nil || len(turns) != expectedTurns {
				t.Fatalf("failed lookup changed turns: %+v %v", turns, err)
			}
		})
	}
}

func TestAgentTurnSubmissionRejectsForeignDeletedAndServiceContexts(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "authorization.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Submission scope")
	if err != nil {
		t.Fatal(err)
	}
	key := "11111111-1111-4111-8111-111111111111"
	foreign := identity.WithPrincipal(ctx, identity.Principal{Kind: identity.KindUser, UserID: "foreign", WorkspaceID: "foreign", Role: identity.RoleOwner})
	_, _, err = store.LookupAgentTurnSubmission(foreign, project.PrimaryConversationID, key)
	assertDomainCode(t, err, "CONVERSATION_NOT_FOUND")
	for _, blocked := range []context.Context{identity.WithPrincipal(ctx, identity.ServicePrincipal()), WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: project.ProjectID})} {
		_, _, err := store.LookupAgentTurnSubmission(blocked, project.PrimaryConversationID, key)
		assertDomainCode(t, err, "ROLE_FORBIDDEN")
		_, err = store.AcceptAgentTurn(blocked, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "No impersonation"}, CommandMeta{})
		assertDomainCode(t, err, "ROLE_FORBIDDEN")
	}
	_, err = store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Wrong scope"}, CommandMeta{Scope: "foreign"})
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	if _, err := store.db.Exec(`UPDATE projects SET deleted_at=? WHERE project_id=?`, formatTime(store.now()), project.ProjectID); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.LookupAgentTurnSubmission(ctx, project.PrimaryConversationID, key)
	assertDomainCode(t, err, "CONVERSATION_NOT_FOUND")
}
