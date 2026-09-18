package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func proposedRevisionForArtifact(t *testing.T, store *Store, artifact Artifact) RevisionRequest {
	t.Helper()
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	project, err := store.GetProject(ctx, artifact.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	base, err := store.GetArtifactVersion(ctx, artifact.CurrentVersionID)
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{Content: "Revise the current artifact.", ClientContext: agentcontract.ClientContext{
			CurrentArtifactID: &artifact.ArtifactID, CurrentArtifactVersionID: &artifact.CurrentVersionID,
		}},
		Decision: agentcontract.AgentDecision{Reply: "Revision proposed.", Intent: "revise", Confidence: 1},
	})
	if err != nil || exchange.Revision == nil {
		t.Fatalf("create revision: %+v %v", exchange, err)
	}
	attempt, err := store.BeginRevisionAttempt(ctx, BeginRevisionAttemptCommand{
		RevisionRequestID: exchange.Revision.RevisionRequestID, ContextPayload: json.RawMessage(`{"target":"artifact"}`),
		ContextHash: "revision-command-fixture", AdapterID: artifact.ArtifactType + "_revision", AdapterVersion: "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := store.CompleteRevisionAttempt(ctx, CompleteRevisionAttemptCommand{
		RevisionRequestID: exchange.Revision.RevisionRequestID, RevisionAttemptID: attempt.RevisionAttemptID,
		ProposalPayload: base.Payload, ProposalSummary: "Review transaction fixture", ProviderID: "fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

func revisionCommandFixture(t *testing.T, workflow bool) (*Store, Artifact, AcceptRevisionCommand) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "revision.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	var artifact Artifact
	if workflow {
		project, initial, _, storyVersion := prepareStoryBibleForImpact(t, store, "Revision transaction")
		addConfirmedImpactDownstream(t, store, project.ProjectID, initial.Run.RunID, storyVersion.ArtifactVersionID)
		artifact = initial.Artifacts[0]
	} else {
		project, err := store.CreateProject(context.Background(), "Revision transaction")
		if err != nil {
			t.Fatal(err)
		}
		artifact = createDeliveryArtifact(t, store, project, "generic_document", `{"content":"Saved body"}`)
	}
	proposal := proposedRevisionForArtifact(t, store, artifact)
	return store, artifact, AcceptRevisionCommand{
		CommandMeta:       CommandMeta{Scope: artifact.ProjectID, CommandType: "accept_revision", IdempotencyKey: "accept", RequestHash: "accept-body"},
		RevisionRequestID: proposal.RevisionRequestID, ExpectedVersion: proposal.Version,
	}
}

func revisionCommandState(t *testing.T, store *Store, artifact Artifact, revisionID string) string {
	t.Helper()
	var state string
	err := store.db.QueryRow(`SELECT json_array(status, version,
		(SELECT COUNT(*) FROM dependency_decisions), (SELECT COUNT(*) FROM regeneration_plans),
		(SELECT COUNT(*) FROM impact_reviews), (SELECT COUNT(*) FROM artifact_version_change_sets))
		FROM revision_requests WHERE revision_request_id=?`, revisionID).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state + artifactCommandState(t, store, artifact.ArtifactID)
}

func TestAcceptRevisionRollsBackVersionPropagationAndReceiptTogether(t *testing.T) {
	for _, workflow := range []bool{false, true} {
		for _, fault := range []struct{ name, trigger string }{
			{"accept_state", `CREATE TRIGGER fail_accept BEFORE UPDATE OF status ON revision_requests WHEN NEW.status='accepted' BEGIN SELECT RAISE(ABORT, 'injected acceptance failure'); END`},
			{"accept_event", `CREATE TRIGGER fail_accept BEFORE INSERT ON events WHEN NEW.event_type='revision_request.accepted' BEGIN SELECT RAISE(ABORT, 'injected event failure'); END`},
			{"accept_receipt", `CREATE TRIGGER fail_accept BEFORE UPDATE ON idempotency_records WHEN NEW.command_type='accept_revision' AND NEW.idempotency_key='accept' AND NEW.status='completed' BEGIN SELECT RAISE(ABORT, 'injected receipt failure'); END`},
		} {
			name := "generic/" + fault.name
			if workflow {
				name = "workflow/" + fault.name
			}
			t.Run(name, func(t *testing.T) {
				store, artifact, command := revisionCommandFixture(t, workflow)
				if _, err := store.db.Exec(fault.trigger); err != nil {
					t.Fatal(err)
				}
				before := revisionCommandState(t, store, artifact, command.RevisionRequestID)
				if _, err := store.AcceptRevision(context.Background(), command); err == nil || !strings.Contains(err.Error(), "injected") {
					t.Fatalf("expected injected transaction failure, got %v", err)
				}
				if after := revisionCommandState(t, store, artifact, command.RevisionRequestID); after != before {
					t.Fatalf("partial acceptance: %s -> %s", before, after)
				}
				if _, err := store.db.Exec(`DROP TRIGGER fail_accept`); err != nil {
					t.Fatal(err)
				}
				accepted, err := store.AcceptRevision(context.Background(), command)
				if err != nil || accepted.Revision.Status != "accepted" || accepted.Version.ArtifactVersion.Version != 2 {
					t.Fatalf("retry after rollback: %+v %v", accepted, err)
				}
				if workflow && (accepted.Version.Propagation == nil || accepted.Version.Propagation.RegenerationPlan == nil) {
					t.Fatal("acceptance skipped downstream propagation")
				}
				before = revisionCommandState(t, store, artifact, command.RevisionRequestID)
				retry, err := store.AcceptRevision(context.Background(), command)
				if err != nil || retry.Version.ArtifactVersion.ArtifactVersionID != accepted.Version.ArtifactVersion.ArtifactVersionID {
					t.Fatalf("acceptance receipt lost: %+v %v", retry, err)
				}
				if after := revisionCommandState(t, store, artifact, command.RevisionRequestID); after != before {
					t.Fatalf("retry changed state: %s -> %s", before, after)
				}
			})
		}
	}
}

func TestRevisionTerminalCommandsRecheckAuthorizationAndBindReceipts(t *testing.T) {
	for _, action := range []string{"accept", "reject", "cancel"} {
		for _, cached := range []bool{false, true} {
			for _, scenario := range []struct{ name, query, code string }{
				{"role", `UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, "ROLE_FORBIDDEN"},
				{"membership", `UPDATE workspace_memberships SET status='disabled' WHERE workspace_id=? AND user_id=?`, "WORKSPACE_ACCESS_DENIED"},
				{"user", `UPDATE users SET status='disabled' WHERE user_id=?`, "WORKSPACE_ACCESS_DENIED"},
				{"workspace", `UPDATE workspaces SET status='deleting' WHERE workspace_id=?`, "WORKSPACE_ACCESS_DENIED"},
				{"project", `UPDATE projects SET deleted_at='2026-09-09T00:00:00Z' WHERE project_id=?`, "PROJECT_NOT_FOUND"},
				{"foreign_workspace", "", "PROJECT_NOT_FOUND"},
			} {
				name := action + "/fresh/" + scenario.name
				if cached {
					name = action + "/cached/" + scenario.name
				}
				t.Run(name, func(t *testing.T) {
					store, artifact, command := revisionCommandFixture(t, false)
					command.CommandType = action + "_revision"
					principal := identity.DefaultLocalPrincipal()
					ctx := identity.WithPrincipal(context.Background(), principal)
					invoke := func(command AcceptRevisionCommand) (RevisionRequest, error) {
						switch action {
						case "reject":
							return store.RejectRevision(ctx, RejectRevisionCommand{CommandMeta: command.CommandMeta, RevisionRequestID: command.RevisionRequestID, ExpectedVersion: command.ExpectedVersion})
						case "cancel":
							return store.CancelRevision(ctx, CancelRevisionCommand{CommandMeta: command.CommandMeta, RevisionRequestID: command.RevisionRequestID, ExpectedVersion: command.ExpectedVersion})
						default:
							result, err := store.AcceptRevision(ctx, command)
							return result.Revision, err
						}
					}
					if cached {
						first, err := invoke(command)
						if err != nil {
							t.Fatal(err)
						}
						retry, err := invoke(command)
						if err != nil || retry.Version != first.Version || retry.RevisionRequestID != first.RevisionRequestID {
							t.Fatalf("original receipt lost: %+v %v", retry, err)
						}
						wrongVersion := command
						wrongVersion.ExpectedVersion++
						_, err = invoke(wrongVersion)
						assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
						other, err := store.GetArtifact(context.Background(), artifact.ArtifactID)
						if err != nil {
							t.Fatal(err)
						}
						otherProposal := proposedRevisionForArtifact(t, store, other)
						wrongRequest := command
						wrongRequest.RevisionRequestID = otherProposal.RevisionRequestID
						wrongRequest.ExpectedVersion = otherProposal.Version
						_, err = invoke(wrongRequest)
						assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
					}
					wrongScope := command
					wrongScope.Scope = "foreign-project"
					_, err := invoke(wrongScope)
					assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
					args := []any{principal.WorkspaceID, principal.UserID}
					switch scenario.name {
					case "user":
						args = []any{principal.UserID}
					case "workspace":
						args = []any{principal.WorkspaceID}
					case "project":
						args = []any{artifact.ProjectID}
					case "foreign_workspace":
						principal.WorkspaceID = "foreign-workspace"
						ctx = identity.WithPrincipal(context.Background(), principal)
					}
					if scenario.query != "" {
						if _, err := store.db.Exec(scenario.query, args...); err != nil {
							t.Fatal(err)
						}
					}
					before := revisionCommandState(t, store, artifact, command.RevisionRequestID)
					_, err = invoke(command)
					assertDomainCode(t, err, scenario.code)
					if after := revisionCommandState(t, store, artifact, command.RevisionRequestID); after != before {
						t.Fatalf("revoked mutation changed state: %s -> %s", before, after)
					}
				})
			}
		}
	}
}

func TestAcceptRevisionCompetesWithRejectAndCancelWithoutSavingLosingProposal(t *testing.T) {
	for _, action := range []string{"reject", "cancel", "accept"} {
		t.Run(action, func(t *testing.T) {
			store, artifact, command := revisionCommandFixture(t, false)
			ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			start := make(chan struct{})
			results := make(chan error, 2)
			go func() {
				<-start
				_, err := store.AcceptRevision(ctx, command)
				results <- err
			}()
			go func() {
				<-start
				meta := command.CommandMeta
				meta.IdempotencyKey = "competing-command"
				meta.CommandType = action + "_revision"
				var err error
				switch action {
				case "reject":
					_, err = store.RejectRevision(ctx, RejectRevisionCommand{CommandMeta: meta, RevisionRequestID: command.RevisionRequestID, ExpectedVersion: command.ExpectedVersion})
				case "cancel":
					_, err = store.CancelRevision(ctx, CancelRevisionCommand{CommandMeta: meta, RevisionRequestID: command.RevisionRequestID, ExpectedVersion: command.ExpectedVersion})
				default:
					other := command
					other.CommandMeta = meta
					_, err = store.AcceptRevision(ctx, other)
				}
				results <- err
			}()
			close(start)
			first, second := <-results, <-results
			if (first == nil) == (second == nil) {
				t.Fatalf("expected one committed command, got %v and %v", first, second)
			}
			if first != nil {
				assertDomainCode(t, first, "REVISION_STATE_CONFLICT")
			} else {
				assertDomainCode(t, second, "REVISION_STATE_CONFLICT")
			}
			request, err := store.GetRevisionRequest(ctx, command.RevisionRequestID)
			if err != nil {
				t.Fatal(err)
			}
			versions, err := store.ListArtifactVersions(ctx, artifact.ArtifactID)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if request.Status == "accepted" {
				want = 2
			}
			if len(versions) != want || request.Version != command.ExpectedVersion+1 {
				t.Fatalf("split acceptance: revision=%+v versions=%+v", request, versions)
			}
		})
	}
}

func TestAcceptedRevisionReceiptSurvivesLaterVersionButRequiresAcceptanceEvent(t *testing.T) {
	store, artifact, command := revisionCommandFixture(t, false)
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	accepted, err := store.AcceptRevision(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	version := accepted.Version.ArtifactVersion
	later, err := store.ApplyArtifactVersion(ctx, CreateVersionCommand{
		CommandMeta: CommandMeta{Scope: artifact.ProjectID, CommandType: "create_artifact_version", IdempotencyKey: "later", RequestHash: "later"},
		ArtifactID:  artifact.ArtifactID, BaseVersionID: version.ArtifactVersionID, BaseVersion: version.Version,
		ChangeMode: "whole_artifact", NewPayload: json.RawMessage(`{"content":"Later saved body"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	before := revisionCommandState(t, store, artifact, command.RevisionRequestID)
	retry, err := store.AcceptRevision(ctx, command)
	if err != nil || retry.Version.ArtifactVersion.ArtifactVersionID != version.ArtifactVersionID {
		t.Fatalf("historical receipt lost: %+v %v", retry, err)
	}
	if after := revisionCommandState(t, store, artifact, command.RevisionRequestID); after != before {
		t.Fatalf("historical retry mutated state: %s -> %s", before, after)
	}
	if _, err := store.db.Exec(`UPDATE idempotency_records SET response_json=json_set(response_json,
		'$.version_result.artifact_version.artifact_version_id', ?, '$.version_result.artifact_version.version', ?)
		WHERE scope=? AND command_type=? AND idempotency_key=?`, later.ArtifactVersion.ArtifactVersionID,
		later.ArtifactVersion.Version, command.Scope, command.CommandType, command.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	_, err = store.AcceptRevision(ctx, command)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
}

func TestAcceptRevisionRejectsForeignArtifactAndMismatchedBase(t *testing.T) {
	for _, field := range []string{"artifact", "base"} {
		t.Run(field, func(t *testing.T) {
			store, artifact, command := revisionCommandFixture(t, false)
			ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			project, err := store.CreateProject(ctx, "Foreign artifact")
			if err != nil {
				t.Fatal(err)
			}
			other := createDeliveryArtifact(t, store, project, "generic_document", `{"content":"Other project"}`)
			query := `UPDATE revision_requests SET artifact_id=? WHERE revision_request_id=?`
			id := other.ArtifactID
			if field == "base" {
				query = `UPDATE revision_requests SET base_artifact_version_id=? WHERE revision_request_id=?`
				id = other.CurrentVersionID
			}
			if _, err := store.db.Exec(query, id, command.RevisionRequestID); err != nil {
				t.Fatal(err)
			}
			before := revisionCommandState(t, store, artifact, command.RevisionRequestID)
			_, err = store.AcceptRevision(ctx, command)
			assertDomainCode(t, err, "REVISION_STATE_CONFLICT")
			if after := revisionCommandState(t, store, artifact, command.RevisionRequestID); after != before {
				t.Fatalf("foreign revision changed state: %s -> %s", before, after)
			}
		})
	}
}

func TestApplyArtifactVersionRollsBackSaveWhenPropagationFails(t *testing.T) {
	store, artifact, revisionCommand := revisionCommandFixture(t, true)
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	version, err := store.GetArtifactVersion(ctx, artifact.CurrentVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_propagation BEFORE INSERT ON dependency_decisions BEGIN SELECT RAISE(ABORT, 'injected propagation failure'); END`); err != nil {
		t.Fatal(err)
	}
	command := CreateVersionCommand{
		CommandMeta: CommandMeta{Scope: artifact.ProjectID, CommandType: "apply_artifact_version", IdempotencyKey: "apply", RequestHash: "apply"},
		ArtifactID:  artifact.ArtifactID, BaseVersionID: version.ArtifactVersionID, BaseVersion: version.Version,
		ChangeMode: "whole_artifact", NewPayload: version.Payload,
	}
	before := revisionCommandState(t, store, artifact, revisionCommand.RevisionRequestID)
	if _, err := store.ApplyArtifactVersion(ctx, command); err == nil || !strings.Contains(err.Error(), "injected propagation failure") {
		t.Fatalf("expected injected propagation failure, got %v", err)
	}
	if after := revisionCommandState(t, store, artifact, revisionCommand.RevisionRequestID); after != before {
		t.Fatalf("partially saved before propagation: %s -> %s", before, after)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_propagation`); err != nil {
		t.Fatal(err)
	}
	result, err := store.ApplyArtifactVersion(ctx, command)
	if err != nil || result.ArtifactVersion.Version != 2 || result.Propagation == nil {
		t.Fatalf("retry after failed propagation: %+v %v", result, err)
	}
}
