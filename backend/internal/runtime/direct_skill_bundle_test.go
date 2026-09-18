package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/capability"
)

type multiResponseFixture struct {
	Outputs        []ContextOutputContract `json:"output_contracts"`
	ProviderSchema json.RawMessage         `json:"provider_schema"`
	Valid          json.RawMessage         `json:"valid"`
	Invalid        []json.RawMessage       `json:"invalid"`
}

func multiSkillFixture(t *testing.T) multiResponseFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testProjectRoot(t), "fixtures", "skills", "response-contracts", "multi-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture multiResponseFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func startMultiSkill(t *testing.T, store *Store, mutate func(*capability.Manifest)) (RunSnapshot, string, string) {
	t.Helper()
	fixture := multiSkillFixture(t)
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
	step := &workflow.Steps[0]
	step.Kind, step.Batch = "model", nil
	step.Approval.Type, step.Approval.Scope = "checkpoint", "artifact"
	step.OutputRefs = nil
	workflow.Completion.RequiredArtifacts = nil
	for _, contract := range fixture.Outputs {
		step.OutputRefs = append(step.OutputRefs, capability.ArtifactOutput{ArtifactType: contract.ArtifactType, Cardinality: "one", InitialStatus: "pending_approval", SchemaRef: contract.SchemaRef})
		workflow.Completion.RequiredArtifacts = append(workflow.Completion.RequiredArtifacts, capability.CompletionArtifact{ArtifactType: contract.ArtifactType, RequiredStatus: "confirmed", Coverage: "one"})
		if err := os.WriteFile(filepath.Join(filepath.Dir(workflowPath), filepath.FromSlash(contract.SchemaRef)), contract.Schema, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	providerRef := "../schemas/provider.json"
	step.ProviderResultSchemaRef = &providerRef
	if err := os.WriteFile(filepath.Join(directory, "schemas", "provider.json"), fixture.ProviderSchema, 0o600); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(&workflow)
	}
	data, err = json.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflowPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return startInstalledBatchSkillFromDirectory(t, store, directory)
}

func claimMultiSkill(t *testing.T, store *Store) *TaskClaim {
	t.Helper()
	claim, err := store.ClaimExecutionTask(context.Background(), ClaimExecutionTaskCommand{WorkerID: "sdk-multi-output", ExecutorIDs: []string{"worker.structured_content"}, ProviderID: "openai", ModelID: "test-model", LeaseSeconds: 60})
	if err != nil || claim == nil {
		t.Fatalf("claim = %+v, %v", claim, err)
	}
	return claim
}

func submitMultiSkill(t *testing.T, store *Store, claim *TaskClaim, payload json.RawMessage) CommitExecutionResultCommand {
	t.Helper()
	result, err := store.SubmitExecutionResult(context.Background(), SubmitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash, ResponsePayload: payload})
	if err != nil || result.ResponseHash == nil {
		t.Fatalf("submit = %+v, %v", result, err)
	}
	return CommitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, ExpectedResponseHash: *result.ResponseHash}
}

func approveMultiSkill(t *testing.T, store *Store, approval Approval, key string) RunSnapshot {
	t.Helper()
	snapshot, err := store.ResolveApproval(context.Background(), ResolveApprovalCommand{
		CommandMeta:       CommandMeta{Scope: approval.ProjectID, CommandType: "resolve_approval", IdempotencyKey: key, RequestHash: key},
		ApprovalRequestID: approval.ApprovalRequestID, Action: "approve", ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestInstalledMultiOutputSkillCommitsAndApprovesAllVersionsAfterReopen(t *testing.T) {
	for _, count := range []int{2, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "content_agent.db")
			store, err := Open(path, loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			started, assetID, invocationID := startMultiSkill(t, store, func(m *capability.Manifest) {
				if count == 3 {
					third := m.Steps[0].OutputRefs[1]
					third.ArtifactType = "revision_plan"
					m.Steps[0].OutputRefs = append(m.Steps[0].OutputRefs, third)
					m.Completion.RequiredArtifacts = append(m.Completion.RequiredArtifacts, capability.CompletionArtifact{ArtifactType: third.ArtifactType, RequiredStatus: "confirmed", Coverage: "one"})
				}
			})
			fixture := multiSkillFixture(t)
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(fixture.Valid, &payload); err != nil {
				t.Fatal(err)
			}
			if count == 3 {
				payload["revision_plan"] = payload["review_notes"]
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			claim := claimMultiSkill(t, store)
			if len(claim.ContextPack.OutputContracts) != count || claim.Task.ItemKey != "step:review_episodes" || claim.ContextPack.Target.ScopeKey != claim.Task.ItemKey {
				t.Fatalf("context = %+v", claim)
			}
			if err := validateEmbeddedJSONSchema(claim.ContextPack.ProviderResultContract.Schema, encoded); err != nil {
				t.Fatal(err)
			}
			command := submitMultiSkill(t, store, claim, encoded)
			committed, err := store.CommitExecutionResult(ctx, command)
			if err != nil || len(committed.Outputs) != count || len(committed.RunSnapshot.Artifacts) != count || committed.Approval.SubjectKind != "artifact_version_set" {
				t.Fatalf("commit = %+v, %v", committed, err)
			}
			for _, output := range committed.Outputs {
				if output.Artifact.ScopeKey != "singleton" || output.ArtifactVersion.Status != "pending_approval" {
					t.Fatalf("output = %+v", output)
				}
				var assets, configs int
				if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_dependencies WHERE downstream_artifact_version_id=? AND upstream_kind='asset_snapshot' AND upstream_ref_id=?`, output.ArtifactVersion.ArtifactVersionID, assetID).Scan(&assets); err != nil {
					t.Fatal(err)
				}
				if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_dependencies WHERE downstream_artifact_version_id=? AND upstream_kind='config_snapshot'`, output.ArtifactVersion.ArtifactVersionID).Scan(&configs); err != nil || assets != 1 || configs != 1 {
					t.Fatalf("dependencies = %d/%d, %v", assets, configs, err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(path, loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			replayed, err := store.CommitExecutionResult(ctx, command)
			if err != nil || replayed.RunSnapshot.EventCursor != committed.RunSnapshot.EventCursor || len(replayed.Outputs) != count {
				t.Fatalf("replay = %+v, %v", replayed, err)
			}
			completed := approveMultiSkill(t, store, committed.Approval, "multi-approve")
			if completed.Run.Status != "completed" {
				t.Fatalf("run = %+v", completed)
			}
			for _, output := range committed.Outputs {
				version, err := store.GetArtifactVersion(ctx, output.ArtifactVersion.ArtifactVersionID)
				if err != nil || version.Status != "confirmed" {
					t.Fatalf("version = %+v, %v", version, err)
				}
			}
			invocation, err := scanSkillInvocation(store.db.QueryRowContext(ctx, skillInvocationSelect+` WHERE skill_invocation_id=?`, invocationID))
			if err != nil || invocation.Status != "completed" || invocation.RunID == nil || *invocation.RunID != started.Run.RunID {
				t.Fatalf("invocation = %+v, %v", invocation, err)
			}
		})
	}
}

func TestInstalledMultiOutputSkillRejectsInvalidBundleWithoutPartialArtifacts(t *testing.T) {
	for index, payload := range multiSkillFixture(t).Invalid {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			started, _, _ := startMultiSkill(t, store, nil)
			claim := claimMultiSkill(t, store)
			command := submitMultiSkill(t, store, claim, payload)
			_, err = store.CommitExecutionResult(context.Background(), command)
			assertDomainCode(t, err, "OUTPUT_SCHEMA_VALIDATION_FAILED")
			snapshot, err := store.GetRunSnapshot(context.Background(), started.Run.RunID)
			if err != nil || len(snapshot.Artifacts) != 0 || snapshot.CurrentApproval != nil || snapshot.Run.Status != "running" {
				t.Fatalf("invalid bundle changed run: %+v, %v", snapshot, err)
			}
		})
	}
}

func TestInstalledMultiOutputSkillRollsBackSecondOutputWriteFailure(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started, _, _ := startMultiSkill(t, store, nil)
	command := submitMultiSkill(t, store, claimMultiSkill(t, store), multiSkillFixture(t).Valid)
	before, err := store.GetRunSnapshot(ctx, started.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER fixture_multi_write BEFORE INSERT ON artifact_versions WHEN NEW.schema_id='review_notes' BEGIN SELECT RAISE(ABORT,'fixture second output failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitExecutionResult(ctx, command); err == nil {
		t.Fatal("second output failure was ignored")
	}
	after, err := store.GetRunSnapshot(ctx, started.Run.RunID)
	if err != nil || len(after.Artifacts) != 0 || after.CurrentApproval != nil || after.EventCursor != before.EventCursor {
		t.Fatalf("partial transaction = %+v, %v", after, err)
	}
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM execution_attempts WHERE attempt_id=?`, command.AttemptID).Scan(&status); err != nil || status != "result_received" {
		t.Fatalf("attempt = %s, %v", status, err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER fixture_multi_write`); err != nil {
		t.Fatal(err)
	}
	committed, err := store.CommitExecutionResult(ctx, command)
	if err != nil || len(committed.Outputs) != 2 {
		t.Fatalf("retry = %+v, %v", committed, err)
	}
}

func TestInstalledMultiOutputSkillEditsEitherOutputAndPreservesOriginalCommitReceipt(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	startMultiSkill(t, store, nil)
	command := submitMultiSkill(t, store, claimMultiSkill(t, store), multiSkillFixture(t).Valid)
	committed, err := store.CommitExecutionResult(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	approval := committed.Approval
	for index, output := range committed.Outputs {
		edited, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
			CommandMeta: CommandMeta{Scope: output.Artifact.ProjectID, CommandType: "create_artifact_version", IdempotencyKey: fmt.Sprint("multi-edit-", index), RequestHash: "multi-edit"},
			ArtifactID:  output.Artifact.ArtifactID, BaseVersionID: output.ArtifactVersion.ArtifactVersionID, BaseVersion: output.ArtifactVersion.Version,
			ChangeMode: "unknown", NewPayload: json.RawMessage(`{"title":"Updated review","content":"Revised motivation."}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.ResolveApproval(ctx, ResolveApprovalCommand{
			CommandMeta:       CommandMeta{Scope: approval.ProjectID, CommandType: "resolve_approval", IdempotencyKey: fmt.Sprint("old-bundle-approval-", index), RequestHash: "old-bundle-approval"},
			ApprovalRequestID: approval.ApprovalRequestID, Action: "approve", ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash})
		assertDomainCode(t, err, "APPROVAL_ALREADY_RESOLVED")
		approval = edited.Approval
		if approval.SubjectKind != "artifact_version_set" || approval.SubjectSnapshotHash == committed.Approval.SubjectSnapshotHash {
			t.Fatalf("replacement approval = %+v", approval)
		}
	}
	replayed, err := store.CommitExecutionResult(ctx, command)
	if err != nil || len(replayed.Outputs) != len(committed.Outputs) {
		t.Fatalf("replay = %+v, %v", replayed, err)
	}
	for i, output := range replayed.Outputs {
		if output.ArtifactVersion.ArtifactVersionID != committed.Outputs[i].ArtifactVersion.ArtifactVersionID {
			t.Fatal("old commit receipt replaced by a later user edit")
		}
	}
	if completed := approveMultiSkill(t, store, approval, "approve-edited-bundle"); completed.Run.Status != "completed" {
		t.Fatalf("completion = %+v", completed)
	}
}

func TestInstalledMultiOutputSkillRegeneratesTheExactOutputSet(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started, _, _ := startMultiSkill(t, store, nil)
	initial, err := store.CommitExecutionResult(ctx, submitMultiSkill(t, store, claimMultiSkill(t, store), multiSkillFixture(t).Valid))
	if err != nil {
		t.Fatal(err)
	}
	approval := initial.Approval
	plan, err := store.RequestApprovalRegeneration(ctx, RequestApprovalRegenerationCommand{
		CommandMeta:       CommandMeta{Scope: approval.ProjectID, CommandType: "request_approval_regeneration", IdempotencyKey: "multi-regenerate", RequestHash: "multi-regenerate"},
		ApprovalRequestID: approval.ApprovalRequestID, ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash, Action: "regenerate_artifact",
	})
	if err != nil || len(plan.RegenerationPlan.Groups) != 1 || len(plan.RegenerationPlan.Groups[0].StaleVersionIDs) != 2 {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	if _, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: started.Run.RunID}); err != nil {
		t.Fatal(err)
	}
	replacement, err := store.CommitExecutionResult(ctx, submitMultiSkill(t, store, claimMultiSkill(t, store), multiSkillFixture(t).Valid))
	if err != nil || len(replacement.Outputs) != 2 || replacement.Approval.SubjectKind != "artifact_version_set" {
		t.Fatalf("replacement = %+v, %v", replacement, err)
	}
	for i, output := range replacement.Outputs {
		if output.Artifact.ArtifactID != initial.Outputs[i].Artifact.ArtifactID || output.ArtifactVersion.Version != 3 || output.ArtifactVersion.CreationReason != "regeneration" {
			t.Fatalf("replaced identity = %+v", output)
		}
	}
	if snapshot := approveMultiSkill(t, store, replacement.Approval, "approve-regenerated-bundle"); snapshot.Run.Status != "completed" {
		t.Fatalf("regenerated completion = %+v", snapshot)
	}
}

func TestInstalledMultiOutputSkillRejectsInvalidEditsBeforeChangingVersionsOrApproval(t *testing.T) {
	for _, payload := range []json.RawMessage{
		json.RawMessage(`{"title":"Review"}`),
		json.RawMessage(`{"title":"Short","content":"Violates provider constraints."}`),
	} {
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		startMultiSkill(t, store, nil)
		committed, err := store.CommitExecutionResult(context.Background(), submitMultiSkill(t, store, claimMultiSkill(t, store), multiSkillFixture(t).Valid))
		if err != nil {
			t.Fatal(err)
		}
		output := committed.Outputs[1]
		_, err = store.CreateArtifactVersion(context.Background(), CreateVersionCommand{
			CommandMeta: CommandMeta{Scope: output.Artifact.ProjectID, CommandType: "create_artifact_version", IdempotencyKey: "invalid-multi-edit", RequestHash: "invalid-multi-edit"},
			ArtifactID:  output.Artifact.ArtifactID, BaseVersionID: output.ArtifactVersion.ArtifactVersionID, BaseVersion: output.ArtifactVersion.Version, ChangeMode: "unknown", NewPayload: payload,
		})
		assertDomainCode(t, err, "OUTPUT_SCHEMA_VALIDATION_FAILED")
		snapshot, err := store.GetRunSnapshot(context.Background(), output.Artifact.RunID)
		if err != nil || snapshot.EventCursor != committed.RunSnapshot.EventCursor || snapshot.CurrentApproval == nil || snapshot.CurrentApproval.ApprovalRequestID != committed.Approval.ApprovalRequestID {
			t.Fatalf("invalid edit changed approval: %+v, %v", snapshot, err)
		}
		versions, err := store.ListArtifactVersions(context.Background(), output.Artifact.ArtifactID)
		if err != nil || len(versions) != 1 || versions[0].Status != "pending_approval" {
			t.Fatalf("invalid edit changed versions: %+v, %v", versions, err)
		}
	}
}
