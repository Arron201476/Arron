package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func revisionEditor(id string) identity.Principal {
	principal := identity.DefaultLocalPrincipal()
	principal.UserID, principal.Role = id, identity.RoleEditor
	return principal
}

func revisionAuthorizationCount(t *testing.T, store *Store, request RevisionRequest) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE project_id=? AND subject_id=? AND event_type='revision_request.execution_authorized'`, request.ProjectID, request.RevisionRequestID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func revisionIdentityMessage(request RevisionRequest) CreateMessageExchangeCommand {
	return CreateMessageExchangeCommand{
		ConversationID: request.ConversationID,
		Request: agentcontract.MessageRequest{Content: "Revise this draft", ClientContext: agentcontract.ClientContext{
			CurrentArtifactID: request.ArtifactID, CurrentArtifactVersionID: request.BaseVersionID,
		}},
		Decision: agentcontract.AgentDecision{Reply: "Revision requested", Intent: "revise", Confidence: 1},
	}
}

func legacyRevisionWithoutAuthorization(t *testing.T, store *Store, original RevisionRequest) RevisionRequest {
	t.Helper()
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	if _, err := store.CancelRevision(ctx, CancelRevisionCommand{RevisionRequestID: original.RevisionRequestID, ExpectedVersion: original.Version}); err != nil {
		t.Fatal(err)
	}
	// Represent a pre-authorization row without rewriting append-only history.
	id := store.newID("revision")
	if _, err := store.db.Exec(`INSERT INTO revision_requests(revision_request_id,project_id,conversation_id,request_message_id,target_resolution_id,
		artifact_id,base_artifact_version_id,instruction,operation,status,execution_policy,version,created_at,updated_at)
		SELECT ?,project_id,conversation_id,request_message_id,target_resolution_id,artifact_id,base_artifact_version_id,
		instruction,operation,'queued',execution_policy,1,created_at,updated_at FROM revision_requests WHERE revision_request_id=?`, id, original.RevisionRequestID); err != nil {
		t.Fatal(err)
	}
	request, err := store.GetRevisionRequest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestRevisionWorkerUsesPersistedSubmitterAfterReopen(t *testing.T) {
	editor := revisionEditor("revision_editor")
	store, request, _ := revisionStartFixtureForPrincipal(t, false, editor)
	var sequence int
	var name, path string
	if err := store.db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	actor, err := reopened.RevisionExecutionPrincipal(context.Background(), request.RevisionRequestID)
	if err != nil || actor.UserID != editor.UserID || actor.WorkspaceID != editor.WorkspaceID || actor.Role != identity.RoleEditor {
		t.Fatalf("persisted editor: %+v %v", actor, err)
	}
	attempt := revisionStartAttempt(t, reopened, request)
	completed, err := reopened.CompleteRevisionAttempt(context.Background(), CompleteRevisionAttemptCommand{
		RevisionRequestID: request.RevisionRequestID, RevisionAttemptID: attempt.RevisionAttemptID,
		ProposalPayload: json.RawMessage(`{"content":"Revised by the original editor"}`),
	})
	if err != nil || completed.Status != "proposed" || revisionAuthorizationCount(t, reopened, request) != 1 {
		t.Fatalf("background completion: %+v %v", completed, err)
	}
}

func TestRevisionWorkerRechecksRevocationBeforeClaimAndCompletion(t *testing.T) {
	for _, phase := range []string{"claim", "proposal", "no_change"} {
		for _, revoked := range []struct{ name, statement, code string }{
			{"role", `UPDATE workspace_memberships SET role='viewer' WHERE user_id=?`, "ROLE_FORBIDDEN"},
			{"membership", `UPDATE workspace_memberships SET status='disabled' WHERE user_id=?`, "WORKSPACE_ACCESS_DENIED"},
			{"user", `UPDATE users SET status='disabled' WHERE user_id=?`, "WORKSPACE_ACCESS_DENIED"},
		} {
			t.Run(phase+"/"+revoked.name, func(t *testing.T) {
				editor := revisionEditor("revision_editor")
				store, request, _ := revisionStartFixtureForPrincipal(t, false, editor)
				var attempt RevisionAttempt
				if phase != "claim" {
					attempt = revisionStartAttempt(t, store, request)
				}
				if _, err := store.db.Exec(revoked.statement, editor.UserID); err != nil {
					t.Fatal(err)
				}
				before := revisionStartState(t, store, request)
				var err error
				switch phase {
				case "claim":
					_, err = store.BeginRevisionAttempt(context.Background(), BeginRevisionAttemptCommand{RevisionRequestID: request.RevisionRequestID, ExpectedVersion: request.Version, ContextPayload: json.RawMessage(`{}`), ContextHash: "revoked"})
				case "proposal":
					_, err = store.CompleteRevisionAttempt(context.Background(), CompleteRevisionAttemptCommand{RevisionRequestID: request.RevisionRequestID, RevisionAttemptID: attempt.RevisionAttemptID, ProposalPayload: json.RawMessage(`{"content":"Not authorized"}`)})
				default:
					_, err = store.CompleteRevisionNoChange(context.Background(), CompleteRevisionNoChangeCommand{RevisionRequestID: request.RevisionRequestID, RevisionAttemptID: attempt.RevisionAttemptID, Summary: "Not authorized"})
				}
				assertDomainCode(t, err, revoked.code)
				if after := revisionStartState(t, store, request); before != after {
					t.Fatal("revoked worker changed revision state")
				}
				if phase == "claim" {
					changed, err := store.FailUnauthorizedQueuedRevision(context.Background(), request.RevisionRequestID, request.Version)
					if err != nil || !changed {
						t.Fatalf("unauthorized queue cleanup: %v %v", changed, err)
					}
				} else if err := store.FailRevisionAttempt(context.Background(), FailRevisionAttemptCommand{RevisionRequestID: request.RevisionRequestID, RevisionAttemptID: attempt.RevisionAttemptID, FailureCode: revoked.code}); err != nil {
					t.Fatalf("revocation prevented failure cleanup: %v", err)
				}
			})
		}
	}
}

func TestRevisionLegacyRequestsNeedExplicitReauthorization(t *testing.T) {
	store, request, _ := revisionStartFixture(t, false)
	request = legacyRevisionWithoutAuthorization(t, store, request)
	_, err := store.RevisionExecutionPrincipal(context.Background(), request.RevisionRequestID)
	assertDomainCode(t, err, "REVISION_EXECUTION_OWNER_REQUIRED")
	changed, err := store.FailUnauthorizedQueuedRevision(context.Background(), request.RevisionRequestID, request.Version)
	if err != nil || !changed {
		t.Fatalf("legacy request: %v %v", changed, err)
	}
	failed, err := store.GetRevisionRequest(context.Background(), request.RevisionRequestID)
	if err != nil || failed.Status != "failed" || failed.FailureCode == nil || *failed.FailureCode != "REVISION_EXECUTION_OWNER_REQUIRED" {
		t.Fatalf("missing authorization failure: %+v %v", failed, err)
	}
	before := revisionStartState(t, store, request)
	if changed, err := store.FailUnauthorizedQueuedRevision(context.Background(), request.RevisionRequestID, request.Version); err != nil || changed {
		t.Fatalf("repeated maintenance: %v %v", changed, err)
	}
	if after := revisionStartState(t, store, request); before != after {
		t.Fatal("maintenance duplicated failure")
	}
	editor := revisionEditor("reauthorizing_editor")
	if err := store.BootstrapPrincipal(context.Background(), editor); err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithPrincipal(context.Background(), editor)
	command := revisionStartCommand(failed)
	queued, err := store.RequestRevisionExecution(ctx, command)
	if err != nil || queued.Status != "queued" || queued.FailureCode != nil {
		t.Fatalf("explicit authorization: %+v %v", queued, err)
	}
	actor, err := store.RevisionExecutionPrincipal(context.Background(), request.RevisionRequestID)
	if err != nil || actor.UserID != editor.UserID {
		t.Fatalf("explicit actor: %+v %v", actor, err)
	}
	newCommand := revisionStartCommand(queued)
	newCommand.IdempotencyKey = "explicit-local-user"
	current, err := store.RequestRevisionExecution(identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal()), newCommand)
	if err != nil {
		t.Fatal(err)
	}
	before = revisionStartState(t, store, request)
	replayed, err := store.RequestRevisionExecution(ctx, command)
	if err != nil || replayed.Version != current.Version {
		t.Fatalf("original receipt: %+v %v", replayed, err)
	}
	actor, err = store.RevisionExecutionPrincipal(context.Background(), request.RevisionRequestID)
	if err != nil || actor.UserID != identity.DefaultLocalPrincipal().UserID || revisionAuthorizationCount(t, store, request) != 2 {
		t.Fatalf("receipt replaced newer authorization: %+v %v", actor, err)
	}
	if after := revisionStartState(t, store, request); before != after {
		t.Fatal("receipt replay changed state")
	}
}

func TestRevisionAuthorizationRejectsMismatchedCallersAndAnonymousAdmission(t *testing.T) {
	store, request, _ := revisionStartFixtureForPrincipal(t, false, revisionEditor("revision_editor"))
	for _, caller := range []identity.Principal{identity.DefaultLocalPrincipal(), identity.ServicePrincipal(), identity.SystemPrincipal()} {
		ctx := identity.WithPrincipal(context.Background(), caller)
		before := revisionStartState(t, store, request)
		_, err := store.RevisionExecutionPrincipal(ctx, request.RevisionRequestID)
		assertDomainCode(t, err, "ROLE_FORBIDDEN")
		_, err = store.BeginRevisionAttempt(ctx, BeginRevisionAttemptCommand{RevisionRequestID: request.RevisionRequestID, ContextPayload: json.RawMessage(`{}`), ContextHash: "foreign-caller"})
		assertDomainCode(t, err, "ROLE_FORBIDDEN")
		if after := revisionStartState(t, store, request); before != after {
			t.Fatal("mismatched caller changed revision")
		}
	}
	for _, ctx := range []context.Context{context.Background(), identity.WithPrincipal(context.Background(), identity.ServicePrincipal())} {
		before := revisionStartState(t, store, request)
		_, err := store.CreateMessageExchange(ctx, revisionIdentityMessage(request))
		assertDomainCode(t, err, "AUTHENTICATION_REQUIRED")
		if after := revisionStartState(t, store, request); before != after {
			t.Fatal("anonymous creation was not rolled back")
		}
	}
	before := revisionStartState(t, store, request)
	_, err := store.RequestRevisionExecution(context.Background(), revisionStartCommand(request))
	assertDomainCode(t, err, "AUTHENTICATION_REQUIRED")
	if after := revisionStartState(t, store, request); before != after {
		t.Fatal("anonymous admission was not rolled back")
	}
}

func TestRevisionTargetConfirmationSealsConfirmingUser(t *testing.T) {
	store, request, resolution := revisionStartFixture(t, true)
	editor := revisionEditor("confirming_editor")
	if err := store.BootstrapPrincipal(context.Background(), editor); err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithPrincipal(context.Background(), editor)
	command := revisionTargetCommand(request, resolution)
	queued, err := store.ResolveTargetCandidate(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := store.RevisionExecutionPrincipal(context.Background(), queued.RevisionRequestID)
	if err != nil || actor.UserID != editor.UserID || revisionAuthorizationCount(t, store, queued) != 2 {
		t.Fatalf("target confirmation actor: %+v %v", actor, err)
	}
	before := revisionStartState(t, store, queued)
	if _, err := store.ResolveTargetCandidate(ctx, command); err != nil {
		t.Fatal(err)
	}
	if after := revisionStartState(t, store, queued); before != after {
		t.Fatal("target receipt duplicated authorization")
	}
	revisionStartAttempt(t, store, queued)
}

func TestApprovalRevisionPersistsActualEditorAuthorization(t *testing.T) {
	store, approval, targets := approvalRevisionFixture(t, "single")
	editor := revisionEditor("approval_revision_editor")
	if err := store.BootstrapPrincipal(context.Background(), editor); err != nil {
		t.Fatal(err)
	}
	request, err := store.RequestApprovalRevision(identity.WithPrincipal(context.Background(), editor), approvalRevisionCommand(approval, targets[0], "editor-revision"))
	if err != nil {
		t.Fatal(err)
	}
	actor, err := store.RevisionExecutionPrincipal(context.Background(), request.RevisionRequestID)
	if err != nil || actor.UserID != editor.UserID || request.SourceApprovalRequestID == nil || *request.SourceApprovalRequestID != approval.ApprovalRequestID {
		t.Fatalf("approval author: %+v %+v %v", actor, request, err)
	}
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE user_id=?`, editor.UserID); err != nil {
		t.Fatal(err)
	}
	_, err = store.BeginRevisionAttempt(context.Background(), BeginRevisionAttemptCommand{RevisionRequestID: request.RevisionRequestID, ContextPayload: json.RawMessage(`{}`), ContextHash: "approval-worker"})
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
}

func TestRevisionCreationFromSDKUsesDurableTurnActor(t *testing.T) {
	store, request, _ := revisionStartFixture(t, false)
	editor := revisionEditor("sdk_revision_editor")
	if err := store.BootstrapPrincipal(context.Background(), editor); err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithPrincipal(context.Background(), editor)
	turn, err := store.AcceptAgentTurn(ctx, request.ConversationID, agentcontract.MessageRequest{Content: "Revise this draft"}, CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAgentTurnCommit(ctx, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	service := WithAgentActivity(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), AgentActivityIdentity{
		ProjectID: request.ProjectID, AgentTurnID: turn.AgentTurnID, AllowTerminal: true,
	})
	exchange, err := store.CreateMessageExchange(service, revisionIdentityMessage(request))
	if err != nil || exchange.Revision == nil {
		t.Fatalf("SDK revision creation: %+v %v", exchange, err)
	}
	actor, err := store.RevisionExecutionPrincipal(context.Background(), exchange.Revision.RevisionRequestID)
	if err != nil || actor.UserID != editor.UserID || actor.WorkspaceID != editor.WorkspaceID {
		t.Fatalf("SDK revision author: %+v %v", actor, err)
	}
}

func TestRevisionAuthorizationRejectsCorruptLatestGrant(t *testing.T) {
	for _, field := range []string{"workspace_id", "user_id", "conversation_id", "request_message_id", "target_resolution_id", "artifact_id", "base_artifact_version_id", "instruction_hash", "request_version", "source"} {
		t.Run(field, func(t *testing.T) {
			store, request, _ := revisionStartFixture(t, false)
			queued, err := store.RequestRevisionExecution(identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal()), revisionStartCommand(request))
			if err != nil {
				t.Fatal(err)
			}
			var encoded string
			if err := store.db.QueryRow(`SELECT payload_json FROM events WHERE subject_id=? AND event_type='revision_request.execution_authorized' ORDER BY project_event_seq DESC LIMIT 1`, request.RevisionRequestID).Scan(&encoded); err != nil {
				t.Fatal(err)
			}
			var grant map[string]any
			if err := json.Unmarshal([]byte(encoded), &grant); err != nil {
				t.Fatal(err)
			}
			grant[field] = ""
			if field == "request_version" {
				grant[field] = queued.Version + 1
			}
			tx, err := store.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := store.appendEvent(context.Background(), tx, request.ProjectID, nil, nil, "revision_request.execution_authorized", "revision_request", request.RevisionRequestID, grant); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			_, err = store.RevisionExecutionPrincipal(context.Background(), request.RevisionRequestID)
			assertDomainCode(t, err, "REVISION_EXECUTION_OWNER_INVALID")
		})
	}
}

func TestRevisionAuthorizationAndQueueFailureAreTransactional(t *testing.T) {
	for _, action := range []string{"created", "execute", "target_confirmation", "maintenance"} {
		t.Run(action, func(t *testing.T) {
			store, request, resolution := revisionStartFixture(t, action == "target_confirmation")
			ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			event := "revision_request.execution_authorized"
			if action == "maintenance" {
				event = "revision_request.failed"
				request = legacyRevisionWithoutAuthorization(t, store, request)
			}
			if _, err := store.db.Exec(`CREATE TRIGGER fail_revision_identity BEFORE INSERT ON events WHEN NEW.event_type='` + event + `' BEGIN SELECT RAISE(ABORT, 'injected authorization failure'); END`); err != nil {
				t.Fatal(err)
			}
			invoke := func() error {
				var err error
				switch action {
				case "created":
					_, err = store.CreateMessageExchange(ctx, revisionIdentityMessage(request))
				case "execute":
					_, err = store.RequestRevisionExecution(ctx, revisionStartCommand(request))
				case "target_confirmation":
					_, err = store.ResolveTargetCandidate(ctx, revisionTargetCommand(request, resolution))
				case "maintenance":
					_, err = store.FailUnauthorizedQueuedRevision(context.Background(), request.RevisionRequestID, request.Version)
				}
				return err
			}
			before := revisionStartState(t, store, request)
			if err := invoke(); err == nil || !strings.Contains(err.Error(), "injected") {
				t.Fatalf("expected event failure: %v", err)
			}
			if after := revisionStartState(t, store, request); before != after {
				t.Fatal("event failure partially committed")
			}
			if _, err := store.db.Exec(`DROP TRIGGER fail_revision_identity`); err != nil {
				t.Fatal(err)
			}
			if err := invoke(); err != nil {
				t.Fatalf("retry original command after rollback: %v", err)
			}
		})
	}
}

func TestRevisionQueueFailureSkipsHealthyAndNewlyAuthorizedRequests(t *testing.T) {
	store, head, _ := revisionStartFixtureForPrincipal(t, false, revisionEditor("head_editor"))
	before := revisionStartState(t, store, head)
	if changed, err := store.FailUnauthorizedQueuedRevision(context.Background(), head.RevisionRequestID, head.Version); err != nil || changed {
		t.Fatalf("healthy request was failed: %v %v", changed, err)
	}
	if after := revisionStartState(t, store, head); before != after {
		t.Fatal("healthy queue maintenance wrote state")
	}
	clock := store.now().Add(time.Second)
	store.now = func() time.Time { return clock }
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	next, err := store.CreateMessageExchange(ctx, revisionIdentityMessage(head))
	if err != nil || next.Revision == nil {
		t.Fatalf("second revision: %+v %v", next, err)
	}
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE user_id='head_editor'`); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.FailUnauthorizedQueuedRevision(context.Background(), head.RevisionRequestID, head.Version); err != nil || !changed {
		t.Fatalf("revoked queue head: %v %v", changed, err)
	}
	revisionStartAttempt(t, store, *next.Revision)
	failed, err := store.GetRevisionRequest(ctx, head.RevisionRequestID)
	if err != nil {
		t.Fatal(err)
	}
	reauthorized, err := store.RequestRevisionExecution(ctx, revisionStartCommand(failed))
	if err != nil {
		t.Fatal(err)
	}
	before = revisionStartState(t, store, head)
	if changed, err := store.FailUnauthorizedQueuedRevision(context.Background(), head.RevisionRequestID, failed.Version); err != nil || changed {
		t.Fatalf("stale maintenance overwrote new authorization: %v %v", changed, err)
	}
	_, err = store.FailUnauthorizedQueuedRevision(ctx, head.RevisionRequestID, reauthorized.Version)
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
	if after := revisionStartState(t, store, head); before != after {
		t.Fatal("stale or user maintenance changed state")
	}
}
