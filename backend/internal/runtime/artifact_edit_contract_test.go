package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func managedEditFixture(t *testing.T, mode string) (*Store, ArtifactCommitOutput, *TaskClaim) {
	t.Helper()
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "edit-contract.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if mode == "multi" {
		startMultiSkill(t, store, nil)
		claim := claimMultiSkill(t, store)
		result, err := store.CommitExecutionResult(ctx, submitMultiSkill(t, store, claim, multiSkillFixture(t).Valid))
		if err != nil || len(result.Outputs) != 2 {
			t.Fatalf("multi output claim/commit = %+v, %v", result, err)
		}
		return store, result.Outputs[1], claim
	}
	directory := filepath.Join(t.TempDir(), "episode-review-workflow")
	if err := os.CopyFS(directory, os.DirFS(filepath.Join(testProjectRoot(t), "fixtures", "skills", "batched", "episode-review-workflow"))); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(directory, "content-agent", "workflow.json")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	var workflow capability.Manifest
	if err := json.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	fixture := loadDirectResponseFixture(t)
	if mode == "single" {
		step := &workflow.Steps[0]
		step.Kind, step.Batch = "model", nil
		step.OutputRefs[0].Cardinality = "one"
		step.Approval.Type, step.Approval.Scope = "checkpoint", "artifact"
		workflow.Completion.RequiredArtifacts[0].Coverage = "one"
		if err := os.WriteFile(filepath.Join(filepath.Dir(workflowPath), workflow.Steps[0].OutputRefs[0].SchemaRef), fixture.OutputSchema, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ref := "../schemas/provider.json"
	workflow.Steps[0].ProviderResultSchemaRef = &ref
	data, err = json.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflowPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "schemas", "provider.json"), fixture.ProviderSchema, 0o600); err != nil {
		t.Fatal(err)
	}
	startInstalledBatchSkillFromDirectory(t, store, directory)
	var output ArtifactCommitOutput
	var first *TaskClaim
	count := 1
	if mode == "batch" {
		count = 3
	}
	for i := 0; i < count; i++ {
		claim := claimMultiSkill(t, store)
		payload := fixture.Valid
		if mode == "batch" {
			episodeNo, err := episodeNumberFromScope(claim.Task.ItemKey)
			if err != nil {
				t.Fatal(err)
			}
			payload = json.RawMessage(fmt.Sprintf(`{"episode_no":%d,"title":"Review","content":"Original content"}`, episodeNo))
		}
		result, err := store.CommitExecutionResult(ctx, submitMultiSkill(t, store, claim, payload))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = claim
			output = ArtifactCommitOutput{Artifact: result.Artifact, ArtifactVersion: result.ArtifactVersion}
		}
	}
	return store, output, first
}

func editContractCommand(output ArtifactCommitOutput, payload json.RawMessage, key string) CreateVersionCommand {
	return CreateVersionCommand{CommandMeta: CommandMeta{Scope: output.Artifact.ProjectID, CommandType: "create_artifact_version", IdempotencyKey: key, RequestHash: sha256Hex(payload)},
		ArtifactID: output.Artifact.ArtifactID, BaseVersionID: output.ArtifactVersion.ArtifactVersionID, BaseVersion: output.ArtifactVersion.Version,
		NewPayload: payload, ChangeMode: "full_payload"}
}

func editContractRevision(t *testing.T, store *Store, artifact Artifact) (RevisionRequest, RevisionAttempt) {
	t.Helper()
	project, err := store.GetProject(context.Background(), artifact.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := store.CreateMessageExchange(identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal()), CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{Content: "Revise the content.", ClientContext: agentcontract.ClientContext{
			CurrentArtifactID: &artifact.ArtifactID, CurrentArtifactVersionID: &artifact.CurrentVersionID}},
		Decision: agentcontract.AgentDecision{Reply: "Revise", Intent: "revise", Confidence: 1},
	})
	if err != nil || exchange.Revision == nil {
		t.Fatalf("revision = %+v, %v", exchange, err)
	}
	attempt, err := store.BeginRevisionAttempt(context.Background(), BeginRevisionAttemptCommand{
		RevisionRequestID: exchange.Revision.RevisionRequestID, ExpectedVersion: exchange.Revision.Version,
		ContextPayload: json.RawMessage(`{"artifact_validation":{"response_schema":{}},"pack_type":"sdk_revision"}`),
		ContextHash:    "untrusted-caller-hash", AdapterID: "openai_agents_sdk_apply_patch", AdapterVersion: "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return *exchange.Revision, attempt
}

func TestManagedOutputEditsRejectInvalidSchemaWithoutAnyWrites(t *testing.T) {
	for _, mode := range []string{"single", "batch", "multi"} {
		t.Run(mode, func(t *testing.T) {
			store, output, _ := managedEditFixture(t, mode)
			for i, payload := range []json.RawMessage{
				json.RawMessage(`{"title":"Review"}`), json.RawMessage(`{"title":42,"content":"Invalid type"}`),
				json.RawMessage(`{"title":"Short","content":"Invalid provider constraint"}`),
				json.RawMessage(`{"title":"Review","content":"Extra property","extra":true}`),
				json.RawMessage(`{"episode_no":999,"title":"Review","content":"Wrong scope"}`),
			} {
				if mode == "batch" && i != 4 {
					var object map[string]any
					if err := json.Unmarshal(payload, &object); err != nil {
						t.Fatal(err)
					}
					object["episode_no"] = 1
					var err error
					payload, err = json.Marshal(object)
					if err != nil {
						t.Fatal(err)
					}
				}
				before := artifactCommandState(t, store, output.Artifact.ArtifactID)
				_, err := store.CreateArtifactVersion(context.Background(), editContractCommand(output, payload, fmt.Sprintf("invalid-%d", i)))
				assertDomainCode(t, err, "OUTPUT_SCHEMA_VALIDATION_FAILED")
				if after := artifactCommandState(t, store, output.Artifact.ArtifactID); before != after {
					t.Fatalf("invalid edit changed state: %s -> %s", before, after)
				}
			}
		})
	}
}

func TestManagedEditAncestryAndIdempotentReceiptsKeepOriginalContract(t *testing.T) {
	for _, mode := range []string{"single", "batch", "multi"} {
		t.Run(mode, func(t *testing.T) {
			store, output, claim := managedEditFixture(t, mode)
			command := editContractCommand(output, output.ArtifactVersion.Payload, "first-edit")
			first, err := store.CreateArtifactVersion(context.Background(), command)
			if err != nil {
				t.Fatal(err)
			}
			output.Artifact.CurrentVersionID, output.ArtifactVersion = first.ArtifactVersion.ArtifactVersionID, first.ArtifactVersion
			secondCommand := editContractCommand(output, output.ArtifactVersion.Payload, "second-edit")
			second, err := store.CreateArtifactVersion(context.Background(), secondCommand)
			if err != nil || second.ArtifactVersion.Version != 3 {
				t.Fatalf("second edit = %+v, %v", second, err)
			}
			if _, err := store.db.Exec(`UPDATE context_packs SET context_hash='tampered' WHERE attempt_id=?`, claim.Attempt.AttemptID); err != nil {
				t.Fatal(err)
			}
			before := artifactCommandState(t, store, output.Artifact.ArtifactID)
			receipt, err := store.CreateArtifactVersion(context.Background(), command)
			if err != nil || receipt.ArtifactVersion.ArtifactVersionID != first.ArtifactVersion.ArtifactVersionID {
				t.Fatalf("replayed receipt = %+v, %v", receipt, err)
			}
			output.Artifact.CurrentVersionID, output.ArtifactVersion = second.ArtifactVersion.ArtifactVersionID, second.ArtifactVersion
			_, err = store.CreateArtifactVersion(context.Background(), editContractCommand(output, output.ArtifactVersion.Payload, "third-edit"))
			assertDomainCode(t, err, "CONTEXT_PACK_HASH_MISMATCH")
			if after := artifactCommandState(t, store, output.Artifact.ArtifactID); after != before {
				t.Fatal("receipt or rejected edit changed state")
			}
		})
	}
}

func TestManagedRevisionCarriesFrozenContractAndRechecksProposalAndAcceptance(t *testing.T) {
	for _, mode := range []string{"single", "batch", "multi"} {
		t.Run(mode, func(t *testing.T) {
			ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			store, output, claim := managedEditFixture(t, mode)
			request, attempt := editContractRevision(t, store, output.Artifact)
			var frozen struct {
				ArtifactValidation *ArtifactEditContract `json:"artifact_validation"`
			}
			if err := json.Unmarshal(attempt.ContextPayload, &frozen); err != nil {
				t.Fatal(err)
			}
			if frozen.ArtifactValidation == nil || frozen.ArtifactValidation.ContextHash != claim.ContextPack.ContextHash || attempt.ContextHash != sha256Hex(attempt.ContextPayload) {
				t.Fatalf("frozen contract = %+v", frozen)
			}
			if err := frozen.ArtifactValidation.validate(output.ArtifactVersion.Payload); err != nil {
				t.Fatal(err)
			}
			invalid := json.RawMessage(`{"title":"Review"}`)
			if err := frozen.ArtifactValidation.validate(invalid); err == nil {
				t.Fatal("model-facing contract accepted invalid output")
			}
			before := revisionCommandState(t, store, output.Artifact, request.RevisionRequestID)
			command := CompleteRevisionAttemptCommand{RevisionRequestID: request.RevisionRequestID, RevisionAttemptID: attempt.RevisionAttemptID, ProposalPayload: invalid}
			_, err := store.CompleteRevisionAttempt(ctx, command)
			assertDomainCode(t, err, "OUTPUT_SCHEMA_VALIDATION_FAILED")
			if after := revisionCommandState(t, store, output.Artifact, request.RevisionRequestID); after != before {
				t.Fatal("invalid proposal changed state")
			}
			command.ProposalPayload = output.ArtifactVersion.Payload
			proposed, err := store.CompleteRevisionAttempt(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			accept := AcceptRevisionCommand{CommandMeta: CommandMeta{Scope: request.ProjectID, CommandType: "accept_revision", IdempotencyKey: "accept", RequestHash: "accept"}, RevisionRequestID: request.RevisionRequestID, ExpectedVersion: proposed.Version}
			if _, err := store.db.Exec(`UPDATE revision_requests SET proposal_payload_json=?,proposal_hash=? WHERE revision_request_id=?`, string(invalid), sha256Hex(invalid), request.RevisionRequestID); err != nil {
				t.Fatal(err)
			}
			before = revisionCommandState(t, store, output.Artifact, request.RevisionRequestID)
			_, err = store.AcceptRevision(ctx, accept)
			assertDomainCode(t, err, "OUTPUT_SCHEMA_VALIDATION_FAILED")
			if after := revisionCommandState(t, store, output.Artifact, request.RevisionRequestID); after != before {
				t.Fatal("invalid acceptance changed state")
			}
			if _, err := store.db.Exec(`UPDATE revision_requests SET proposal_payload_json=?,proposal_hash=? WHERE revision_request_id=?`, string(command.ProposalPayload), sha256Hex(command.ProposalPayload), request.RevisionRequestID); err != nil {
				t.Fatal(err)
			}
			accepted, err := store.AcceptRevision(ctx, accept)
			if err != nil || accepted.Revision.Status != "accepted" {
				t.Fatalf("accept = %+v, %v", accepted, err)
			}
			receipt, err := store.AcceptRevision(ctx, accept)
			if err != nil || receipt.Version.ArtifactVersion.ArtifactVersionID != accepted.Version.ArtifactVersion.ArtifactVersionID {
				t.Fatalf("receipt = %+v, %v", receipt, err)
			}
		})
	}
}

func TestRevisionCompletionRechecksCurrentUserAndBase(t *testing.T) {
	for _, noChange := range []bool{false, true} {
		for _, revoked := range []bool{false, true} {
			t.Run(fmt.Sprintf("no-change=%t/revoked=%t", noChange, revoked), func(t *testing.T) {
				store, request, _ := revisionStartFixture(t, false)
				attempt := revisionStartAttempt(t, store, request)
				ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
				if revoked {
					principal := identity.DefaultLocalPrincipal()
					if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, principal.WorkspaceID, principal.UserID); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := store.db.Exec(`UPDATE artifacts SET current_version_id='replaced-base' WHERE artifact_id=?`, *request.ArtifactID); err != nil {
						t.Fatal(err)
					}
				}
				before := revisionStartState(t, store, request)
				var err error
				if noChange {
					_, err = store.CompleteRevisionNoChange(ctx, CompleteRevisionNoChangeCommand{RevisionRequestID: request.RevisionRequestID, RevisionAttemptID: attempt.RevisionAttemptID, Summary: "Keep"})
				} else {
					_, err = store.CompleteRevisionAttempt(ctx, CompleteRevisionAttemptCommand{RevisionRequestID: request.RevisionRequestID, RevisionAttemptID: attempt.RevisionAttemptID, ProposalPayload: json.RawMessage(`{"content":"Changed"}`)})
				}
				code := "REVISION_BASE_VERSION_CONFLICT"
				if revoked {
					code = "ROLE_FORBIDDEN"
				}
				assertDomainCode(t, err, code)
				if after := revisionStartState(t, store, request); after != before {
					t.Fatal("rejected completion changed state")
				}
			})
		}
	}
}

func TestManagedEditCannotFallbackPastMissingCurrentGenerationContract(t *testing.T) {
	store, output, _ := managedEditFixture(t, "single")
	created, err := store.CreateArtifactVersion(context.Background(), editContractCommand(output, output.ArtifactVersion.Payload, "edit"))
	if err != nil {
		t.Fatal(err)
	}
	// A broken latest model lineage must not silently use an older model schema.
	if _, err := store.db.Exec(`UPDATE artifact_versions SET created_by_kind='model',actor_ref='missing-attempt'
		WHERE artifact_version_id=?`, created.ArtifactVersion.ArtifactVersionID); err != nil {
		t.Fatal(err)
	}
	output.Artifact.CurrentVersionID, output.ArtifactVersion = created.ArtifactVersion.ArtifactVersionID, created.ArtifactVersion
	before := artifactCommandState(t, store, output.Artifact.ArtifactID)
	_, err = store.CreateArtifactVersion(context.Background(), editContractCommand(output, output.ArtifactVersion.Payload, "next-edit"))
	assertDomainCode(t, err, "CONTEXT_LINEAGE_CONFLICT")
	if after := artifactCommandState(t, store, output.Artifact.ArtifactID); after != before {
		t.Fatal("missing model lineage allowed a write")
	}
}

func TestRegeneratedManagedOutputEditsUseReplacementStepAndContract(t *testing.T) {
	for _, mode := range []string{"single", "batch", "multi"} {
		t.Run(mode, func(t *testing.T) {
			ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			store, original, _ := managedEditFixture(t, mode)
			snapshot, err := store.GetRunSnapshot(ctx, original.Artifact.RunID)
			if err != nil || snapshot.CurrentApproval == nil {
				t.Fatalf("approval = %+v, %v", snapshot, err)
			}
			approval := *snapshot.CurrentApproval
			_, err = store.RequestApprovalRegeneration(ctx, RequestApprovalRegenerationCommand{
				CommandMeta:       CommandMeta{Scope: approval.ProjectID, CommandType: "request_approval_regeneration", IdempotencyKey: "regenerate", RequestHash: "regenerate"},
				ApprovalRequestID: approval.ApprovalRequestID, ExpectedApprovalVersion: approval.Version,
				SubjectSnapshotHash: approval.SubjectSnapshotHash, Action: "regenerate_artifact",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: original.Artifact.RunID}); err != nil {
				t.Fatal(err)
			}
			count := 1
			if mode == "batch" {
				count = 3
			}
			var replacement ArtifactCommitOutput
			var replacementStep string
			for i := 0; i < count; i++ {
				claim := claimMultiSkill(t, store)
				payload := original.ArtifactVersion.Payload
				if mode == "multi" {
					payload = multiSkillFixture(t).Valid
				} else if mode == "batch" {
					episodeNo, err := episodeNumberFromScope(claim.Task.ItemKey)
					if err != nil {
						t.Fatal(err)
					}
					payload = json.RawMessage(fmt.Sprintf(`{"episode_no":%d,"title":"Review","content":"Replacement content"}`, episodeNo))
				}
				result, err := store.CommitExecutionResult(ctx, submitMultiSkill(t, store, claim, payload))
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					replacementStep = claim.ContextPack.Step.StepRunID
					replacement = ArtifactCommitOutput{Artifact: result.Artifact, ArtifactVersion: result.ArtifactVersion}
					if mode == "multi" {
						replacement = result.Outputs[1]
					}
				}
			}
			if replacement.Artifact.StepRunID != replacementStep || replacementStep == original.Artifact.StepRunID || replacement.Artifact.ArtifactID != original.Artifact.ArtifactID {
				t.Fatalf("replacement artifact still bound to original step: %+v", replacement.Artifact)
			}
			if _, err := store.CreateArtifactVersion(ctx, editContractCommand(replacement, replacement.ArtifactVersion.Payload, "replacement-edit")); err != nil {
				t.Fatalf("replacement edit lost generated contract: %v", err)
			}
		})
	}
}
