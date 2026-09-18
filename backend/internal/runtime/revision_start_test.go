package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func revisionStartFixture(t *testing.T, ambiguous bool) (*Store, RevisionRequest, TargetResolution) {
	t.Helper()
	return revisionStartFixtureForPrincipal(t, ambiguous, identity.DefaultLocalPrincipal())
}

func revisionStartFixtureForPrincipal(t *testing.T, ambiguous bool, principal identity.Principal) (*Store, RevisionRequest, TargetResolution) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "revision-start.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	project, err := store.CreateProject(ctx, "Revision admission")
	if err != nil {
		t.Fatal(err)
	}
	artifact := createDeliveryArtifact(t, store, project, "generic_document", `{"content":"Saved body"}`)
	request := agentcontract.MessageRequest{Content: "Revise Download fixture"}
	if ambiguous {
		createDeliveryArtifact(t, store, project, "generic_document", `{"content":"Other saved body"}`)
	} else {
		request.ClientContext.CurrentArtifactID = &artifact.ArtifactID
		request.ClientContext.CurrentArtifactVersionID = &artifact.CurrentVersionID
	}
	if err := store.BootstrapPrincipal(ctx, principal); err != nil {
		t.Fatal(err)
	}
	ctx = identity.WithPrincipal(context.Background(), principal)
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID, Request: request,
		Decision: agentcontract.AgentDecision{Reply: "Revision requested", Intent: "revise", Confidence: 1},
	})
	if err != nil || exchange.Revision == nil {
		t.Fatalf("revision fixture: %+v %v", exchange, err)
	}
	resolution, err := store.GetTargetResolution(ctx, exchange.Revision.TargetResolutionID)
	if err != nil {
		t.Fatal(err)
	}
	if ambiguous && (resolution.Status != "ambiguous" || len(resolution.Candidates) != 2 || exchange.Revision.Status != "waiting_target_confirmation") {
		t.Fatalf("ambiguous fixture: %+v %+v", resolution, exchange.Revision)
	}
	if !ambiguous && (resolution.Status != "resolved" || exchange.Revision.Status != "queued") {
		t.Fatalf("resolved fixture: %+v %+v", resolution, exchange.Revision)
	}
	return store, *exchange.Revision, resolution
}

func revisionStartState(t *testing.T, store *Store, request RevisionRequest) string {
	t.Helper()
	var state string
	err := store.db.QueryRow(`SELECT json_array(status,version,artifact_id,base_artifact_version_id,
		(SELECT COUNT(*) FROM revision_attempts), (SELECT COUNT(*) FROM events),
		(SELECT COUNT(*) FROM idempotency_records),
		(SELECT json_array(status,artifact_id,artifact_version_id) FROM target_resolutions WHERE target_resolution_id=?))
		FROM revision_requests WHERE revision_request_id=?`, request.TargetResolutionID, request.RevisionRequestID).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func revisionStartCommand(request RevisionRequest) RequestRevisionExecutionCommand {
	return RequestRevisionExecutionCommand{CommandMeta: CommandMeta{Scope: request.ProjectID, CommandType: "execute_revision", IdempotencyKey: "admission", RequestHash: "original-body"}, RevisionRequestID: request.RevisionRequestID, ExpectedVersion: request.Version}
}

func revisionTargetCommand(request RevisionRequest, resolution TargetResolution) ResolveTargetCandidateCommand {
	return ResolveTargetCandidateCommand{CommandMeta: CommandMeta{Scope: request.ProjectID, CommandType: "resolve_target_candidate", IdempotencyKey: "target", RequestHash: "original-body"}, TargetResolutionID: resolution.TargetResolutionID, CandidateID: resolution.Candidates[0].CandidateID, ExpectedVersion: request.Version}
}

func revisionStartAttempt(t *testing.T, store *Store, request RevisionRequest) RevisionAttempt {
	t.Helper()
	attempt, err := store.BeginRevisionAttempt(context.Background(), BeginRevisionAttemptCommand{
		RevisionRequestID: request.RevisionRequestID, ExpectedVersion: request.Version,
		ContextPayload: json.RawMessage(`{"target":"artifact"}`), ContextHash: "fixture", AdapterID: "fixture", AdapterVersion: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func TestRevisionAdmissionRecoveryNeverReplaysGeneration(t *testing.T) {
	store, request, _ := revisionStartFixture(t, false)
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	command := revisionStartCommand(request)
	queued, err := store.RequestRevisionExecution(ctx, command)
	if err != nil || queued.Version != request.Version+1 || queued.Status != "queued" {
		t.Fatalf("admit: %+v %v", queued, err)
	}
	clock := store.now()
	store.now = func() time.Time { return clock }
	attempt := revisionStartAttempt(t, store, queued)
	before := revisionStartState(t, store, request)
	if err := store.RecoverExpiredRevisionAttempts(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	if after := revisionStartState(t, store, request); before != after {
		t.Fatal("recovery expired a live attempt")
	}
	clock = clock.Add(2 * time.Minute)
	if err := store.RecoverExpiredRevisionAttempts(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	failed, err := store.GetRevisionRequest(ctx, request.RevisionRequestID)
	if err != nil || failed.Status != "failed" || failed.FailureCode == nil || *failed.FailureCode != "SDK_REVISION_ATTEMPT_EXPIRED" {
		t.Fatalf("recover: %+v %v", failed, err)
	}
	before = revisionStartState(t, store, request)
	replayed, err := store.RequestRevisionExecution(ctx, command)
	if err != nil || replayed.Status != "failed" || replayed.Version != failed.Version {
		t.Fatalf("original admission replay: %+v %v", replayed, err)
	}
	if after := revisionStartState(t, store, request); before != after {
		t.Fatal("original admission replayed generation")
	}
	staleClaim := BeginRevisionAttemptCommand{RevisionRequestID: request.RevisionRequestID, ExpectedVersion: queued.Version, ContextPayload: json.RawMessage(`{}`), ContextHash: "stale"}
	_, err = store.BeginRevisionAttempt(ctx, staleClaim)
	assertDomainCode(t, err, "REVISION_VERSION_CONFLICT")
	retry := revisionStartCommand(failed)
	retry.IdempotencyKey = "explicit-retry"
	queued, err = store.RequestRevisionExecution(ctx, retry)
	if err != nil {
		t.Fatal(err)
	}
	next := revisionStartAttempt(t, store, queued)
	if next.AttemptNo != attempt.AttemptNo+1 {
		t.Fatal("retry did not create the next attempt")
	}
	before = revisionStartState(t, store, request)
	err = store.FailRevisionAttempt(ctx, FailRevisionAttemptCommand{RevisionRequestID: request.RevisionRequestID, RevisionAttemptID: attempt.RevisionAttemptID, FailureCode: "LATE_FAILURE"})
	assertDomainCode(t, err, "RUN_STATE_CONFLICT")
	_, err = store.CompleteRevisionAttempt(ctx, CompleteRevisionAttemptCommand{RevisionRequestID: request.RevisionRequestID, RevisionAttemptID: attempt.RevisionAttemptID, ProposalPayload: json.RawMessage(`{"content":"Late result"}`)})
	assertDomainCode(t, err, "RUN_STATE_CONFLICT")
	if after := revisionStartState(t, store, request); before != after {
		t.Fatal("old callback changed new attempt")
	}
	proposed, err := store.CompleteRevisionAttempt(ctx, CompleteRevisionAttemptCommand{RevisionRequestID: request.RevisionRequestID, RevisionAttemptID: next.RevisionAttemptID, ProposalPayload: json.RawMessage(`{"content":"New result"}`)})
	if err != nil || proposed.Status != "proposed" {
		t.Fatalf("current completion: %+v %v", proposed, err)
	}
	before = revisionStartState(t, store, request)
	err = store.FailRevisionAttempt(ctx, FailRevisionAttemptCommand{RevisionRequestID: request.RevisionRequestID, RevisionAttemptID: next.RevisionAttemptID, FailureCode: "LATE_FAILURE"})
	assertDomainCode(t, err, "RUN_STATE_CONFLICT")
	if after := revisionStartState(t, store, request); before != after {
		t.Fatal("failure overwrote completed attempt")
	}
}

func TestRevisionTargetConfirmationBindsReceiptAndCurrentAttempt(t *testing.T) {
	store, request, resolution := revisionStartFixture(t, true)
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	command := revisionTargetCommand(request, resolution)
	queued, err := store.ResolveTargetCandidate(ctx, command)
	if err != nil || queued.Status != "queued" || queued.Version != request.Version+1 || queued.ArtifactID == nil || *queued.ArtifactID != resolution.Candidates[0].ArtifactID {
		t.Fatalf("choose: %+v %v", queued, err)
	}
	revisionStartAttempt(t, store, queued)
	before := revisionStartState(t, store, request)
	replayed, err := store.ResolveTargetCandidate(ctx, command)
	if err != nil || replayed.Status != "running" {
		t.Fatalf("target receipt replay: %+v %v", replayed, err)
	}
	other := command
	other.CandidateID = resolution.Candidates[1].CandidateID
	_, err = store.ResolveTargetCandidate(ctx, other)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	other = command
	other.ExpectedVersion++
	_, err = store.ResolveTargetCandidate(ctx, other)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	if after := revisionStartState(t, store, request); before != after {
		t.Fatal("cached target changed revision")
	}
}

func TestRevisionAttemptAllowsBoundSDKCommitButNotUserConfirmation(t *testing.T) {
	for _, scenario := range []string{"committing", "foreign_project", "invalid_turn", "viewer"} {
		t.Run(scenario, func(t *testing.T) {
			store, request, _ := revisionStartFixture(t, false)
			ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			turn, err := store.AcceptAgentTurn(ctx, request.ConversationID, agentcontract.MessageRequest{Content: "Continue revision"}, CommandMeta{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := store.BeginAgentTurnCommit(ctx, turn.AgentTurnID); err != nil {
				t.Fatal(err)
			}
			activity := AgentActivityIdentity{ProjectID: request.ProjectID, AgentTurnID: turn.AgentTurnID, AllowTerminal: true}
			want := ""
			switch scenario {
			case "foreign_project":
				activity.ProjectID = "foreign"
				want = "PROJECT_NOT_FOUND"
			case "invalid_turn":
				activity.AgentTurnID = "missing"
				want = "AGENT_ACTIVITY_SCOPE_MISMATCH"
			case "viewer":
				if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, turn.WorkspaceID, turn.UserID); err != nil {
					t.Fatal(err)
				}
				want = "ROLE_FORBIDDEN"
			}
			service := WithAgentActivity(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), activity)
			_, err = store.RequestRevisionExecution(service, revisionStartCommand(request))
			assertDomainCode(t, err, "ROLE_FORBIDDEN")
			before := revisionStartState(t, store, request)
			attempt, err := store.BeginRevisionAttempt(service, BeginRevisionAttemptCommand{RevisionRequestID: request.RevisionRequestID, ExpectedVersion: request.Version, ContextPayload: json.RawMessage(`{}`), ContextHash: "sdk-commit", AdapterID: "sdk", AdapterVersion: "1"})
			if want == "" {
				if err != nil || attempt.Status != "running" {
					t.Fatalf("bound SDK commit: %+v %v", attempt, err)
				}
				completed, err := store.CompleteRevisionAttempt(service, CompleteRevisionAttemptCommand{
					RevisionRequestID: request.RevisionRequestID, RevisionAttemptID: attempt.RevisionAttemptID,
					ProposalPayload: json.RawMessage(`{"content":"Revised through the admitted SDK turn"}`),
				})
				if err != nil || completed.Status != "proposed" {
					t.Fatalf("bound SDK completion: %+v %v", completed, err)
				}
			} else {
				assertDomainCode(t, err, want)
				if after := revisionStartState(t, store, request); before != after {
					t.Fatal("invalid SDK identity claimed revision")
				}
			}
		})
	}
}

func TestRevisionStartCommandsRecheckAuthorizationBeforeFreshOrCachedWrites(t *testing.T) {
	for _, target := range []bool{false, true} {
		for _, cached := range []bool{false, true} {
			for _, mutation := range []struct{ name, query, code string }{
				{"role", `UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, "ROLE_FORBIDDEN"},
				{"membership", `UPDATE workspace_memberships SET status='disabled' WHERE workspace_id=? AND user_id=?`, "WORKSPACE_ACCESS_DENIED"},
				{"user", `UPDATE users SET status='disabled' WHERE user_id=?`, "WORKSPACE_ACCESS_DENIED"},
			} {
				name := "execute/"
				if target {
					name = "target/"
				}
				if cached {
					name += "cached/"
				} else {
					name += "fresh/"
				}
				t.Run(name+mutation.name, func(t *testing.T) {
					store, request, resolution := revisionStartFixture(t, target)
					principal := identity.DefaultLocalPrincipal()
					ctx := identity.WithPrincipal(context.Background(), principal)
					invoke := func(scope string) (RevisionRequest, error) {
						if target {
							command := revisionTargetCommand(request, resolution)
							command.Scope = scope
							return store.ResolveTargetCandidate(ctx, command)
						}
						command := revisionStartCommand(request)
						command.Scope = scope
						return store.RequestRevisionExecution(ctx, command)
					}
					if cached {
						if _, err := invoke(request.ProjectID); err != nil {
							t.Fatal(err)
						}
					}
					before := revisionStartState(t, store, request)
					_, err := invoke("foreign-project")
					assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
					args := []any{principal.WorkspaceID, principal.UserID}
					if mutation.name == "user" {
						args = []any{principal.UserID}
					}
					if _, err := store.db.Exec(mutation.query, args...); err != nil {
						t.Fatal(err)
					}
					_, err = invoke(request.ProjectID)
					assertDomainCode(t, err, mutation.code)
					if after := revisionStartState(t, store, request); before != after {
						t.Fatal("unauthorized admission changed state")
					}
				})
			}
		}
	}
}

func TestRevisionTargetCannotResolveAfterCancellation(t *testing.T) {
	store, request, resolution := revisionStartFixture(t, true)
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	_, err := store.CancelRevision(ctx, CancelRevisionCommand{RevisionRequestID: request.RevisionRequestID, ExpectedVersion: request.Version})
	if err != nil {
		t.Fatal(err)
	}
	before := revisionStartState(t, store, request)
	_, err = store.ResolveTargetCandidate(ctx, revisionTargetCommand(request, resolution))
	assertDomainCode(t, err, "TARGET_STATE_CONFLICT")
	if after := revisionStartState(t, store, request); before != after {
		t.Fatal("target revived cancelled revision")
	}
}

func TestRevisionStartRollsBackAdmissionAndTargetWhenReceiptCannotCommit(t *testing.T) {
	for _, target := range []bool{false, true} {
		name := "execute"
		if target {
			name = "target"
		}
		t.Run(name, func(t *testing.T) {
			store, request, resolution := revisionStartFixture(t, target)
			if _, err := store.db.Exec(`CREATE TRIGGER fail_start_receipt BEFORE UPDATE ON idempotency_records WHEN NEW.status='completed' BEGIN SELECT RAISE(ABORT, 'injected admission receipt failure'); END`); err != nil {
				t.Fatal(err)
			}
			invoke := func() (RevisionRequest, error) {
				if target {
					return store.ResolveTargetCandidate(identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal()), revisionTargetCommand(request, resolution))
				}
				return store.RequestRevisionExecution(identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal()), revisionStartCommand(request))
			}
			before := revisionStartState(t, store, request)
			if _, err := invoke(); err == nil || !strings.Contains(err.Error(), "injected admission receipt failure") {
				t.Fatalf("expected injected failure: %v", err)
			}
			if after := revisionStartState(t, store, request); before != after {
				t.Fatal("receipt failure partially admitted revision")
			}
			if _, err := store.db.Exec(`DROP TRIGGER fail_start_receipt`); err != nil {
				t.Fatal(err)
			}
			if _, err := invoke(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
